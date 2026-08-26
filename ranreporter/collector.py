#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""gNB 实时指标采集器: odi tracebuff -> sendrate/gbr/q_lvl -> 前端 /api/v1/qos."""
import argparse
import json
import re
import subprocess
import sys
import time
import urllib.error
import urllib.request
import xml.etree.ElementTree as ET
from collections import deque
from datetime import datetime


GNB_HOST = "10.88.120.212"
ODI_CMD = "odi -q -e -n duapp0 display-tracebuff 1"
CONFDB_PATH = "/opt/bbu/oam/log/confdb_v2.xml"
QOSMODULE_LOG = "/home/core/QoSModule/logs/qos-module.log"
FRONTEND_URL = "http://192.168.1.10:28448/api/v1/qos"
INTERVAL = 0.5
WINDOW_SIZE = 30
PUSH_INTERVAL = 1.0
SSH_TIMEOUT = 6
HTTP_TIMEOUT = 5
SSH_MUX_SOCKET = "/tmp/ranreporter_ssh_mux_%d" % __import__("os").getpid()

DL_BYTE_TAGS = {"UPT_454": "in_macTbSzBytes", "UPT_410": "byteScheduled"}
UL_BYTE_TAGS = {"UPT_1100": "pduSzBytes", "UPT_563": "sduSz"}

LINE_RE = re.compile(r"^([A-Z]+_\d+)\s+(\d+)\s+(\d+)\s+(\d+)\s+(.*)$")
INT_RE_TMPL = {k: re.compile(k + r"=*(\d+)") for k in set(list(DL_BYTE_TAGS.values()) + list(UL_BYTE_TAGS.values()))}

QCI_RE = re.compile(r"Qci=(\d+)")
AMBR_RE = re.compile(r"Dl=\[(\d+),(\d+)\],\s*Ul=\[(\d+),(\d+)\]")
CRNTI_RE = re.compile(r"CRNTI=(\d+)")
RELEASE_MARKERS = ("releaseFlow", "releaseAllFlowsForUe", "releaseAllDrbForUe")

GBR_5QI = {1, 2, 3, 4, 65, 66, 67, 71, 72, 73, 74, 75, 76, 79, 80, 82, 83, 84}

# QoSModule 下发日志解析: 基站 L2 trace 不暴露 GFBR/专载 5QI 时, 用下发侧真值兜底
QM_TS_RE = re.compile(r"(\d{4}/\d{2}/\d{2} \d{2}:\d{2}:\d{2}\.\d+)")
QM_APPLY_RE = re.compile(r"smf enforcer apply .*?request_id=(\S+).*?five_qi=(\d+).*?gbr_ul=(\d+).*?gbr_dl=(\d+).*?status=(\S+).*?burst_ms=(\d+)")
QM_REL_RE = re.compile(r"smf enforcer released .*?request_id=(\S+)")


def log(msg):
    print("[%s] %s" % (datetime.now().strftime("%H:%M:%S.%f")[:-3], msg), flush=True)


def open_ssh_master(host):
    """建立 SSH ControlMaster 长连接,后续 ssh_run 复用,单次调用从 ~0.2s 降到 ~0.02s。
    0.5s 高频上报必须开复用,否则 SSH 握手会吃掉周期预算。"""
    import os
    try:
        os.unlink(SSH_MUX_SOCKET)
    except OSError:
        pass
    args = ["ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=5",
            "-o", "ControlMaster=yes", "-o", "ControlPath=" + SSH_MUX_SOCKET,
            "-o", "ControlPersist=300", "-f", host, "true"]
    r = subprocess.run(args, capture_output=True, text=True, timeout=10)
    if r.returncode == 0:
        log("ssh master 已建立: %s" % SSH_MUX_SOCKET)
    else:
        log("ssh master 建立失败(将回退到普通连接): %s" % r.stderr.strip()[:120])


def close_ssh_master(host):
    try:
        subprocess.run(["ssh", "-o", "ControlPath=" + SSH_MUX_SOCKET, "-O", "exit", host],
                        capture_output=True, text=True, timeout=5)
    except Exception:
        pass


def ssh_run(host, cmd):
    args = ["ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=3",
            "-o", "ControlPath=" + SSH_MUX_SOCKET, host, cmd]
    r = subprocess.run(args, capture_output=True, text=True, timeout=SSH_TIMEOUT)
    if r.returncode != 0 and r.stderr:
        log("ssh stderr: %s" % r.stderr.strip()[:200])
    return r.stdout


def parse_trace(text):
    tags = {}
    for line in text.splitlines():
        m = LINE_RE.match(line.strip())
        if not m:
            continue
        tags[m.group(1)] = {
            "count": int(m.group(2)),
            "first_tti": int(m.group(3)),
            "latest_tti": int(m.group(4)),
            "rest": m.group(5),
        }
    return tags


def byte_of(rest, key):
    rx = INT_RE_TMPL.get(key)
    if not rx:
        return 0
    m = rx.search(rest)
    return int(m.group(1)) if m else 0


def compute_sendrate(prev, curr, dt):
    total_bytes = 0
    for tag, key in dict(DL_BYTE_TAGS, **UL_BYTE_TAGS).items():
        p, c = prev.get(tag), curr.get(tag)
        if not p or not c:
            continue
        dcount = c["count"] - p["count"]
        if dcount <= 0:
            continue
        b = byte_of(c["rest"], key)
        total_bytes += dcount * b
        if dcount:
            log("  %s Δcount=%d × %s=%d bytes" % (tag, dcount, key, b))
    return round(total_bytes * 8 / dt / 1000) if total_bytes > 0 else 0


def extract_qos(tags, default_q=9, default_gbr=0):
    """从 tracebuff 自动关联活跃流的 5QI(q_lvl)与 GFBR(gbr_kbps),并判活。
    按 msgstr 内容匹配(不依赖固件版本相关的 tag 编号):
      createBearerFlow: Qci=N, CRNTI=M     -> q_lvl(忽略 255 信令)
      SCHED_setupFlow: AMBR Dl=[MBR,GBR], Ul=[MBR,GBR]  -> gbr_kbps
      releaseFlow / releaseAllDrbForUe 等   -> DRB 释放时间戳
    存活判定: tracebuff 仅保留每个 tag 末次消息,故用 latest_tti 比较——
      若最近一次 DRB 释放 tti 晚于最近一次 DRB 建立 tti,认为承载已释放,上报默认值。
    非 GBR 类型 5QI(5/6/7/8/9 等)无 GFBR,gbr_kbps=0。"""
    q_lvl, gbr_kbps = default_q, default_gbr
    create_tti, gbr_tti, release_tti = -1, -1, -1
    crnti = None
    for t in tags.values():
        rest = t["rest"]
        if "createBearerFlow" in rest and "Qci=" in rest and t["latest_tti"] > create_tti:
            m = QCI_RE.search(rest)
            if m and m.group(1) != "255":
                q_lvl = int(m.group(1))
                create_tti = t["latest_tti"]
                cm = CRNTI_RE.search(rest)
                if cm:
                    crnti = cm.group(1)
        if "SCHED_setupFlow" in rest and "AMBR info" in rest and t["latest_tti"] > gbr_tti:
            m = AMBR_RE.search(rest)
            if m:
                _dl_mbr, dl_gbr, _ul_mbr, ul_gbr = (int(x) for x in m.groups())
                gbr_kbps = max(dl_gbr, ul_gbr) // 1000
                gbr_tti = t["latest_tti"]
        if any(k in rest for k in RELEASE_MARKERS) and t["latest_tti"] > release_tti:
            release_tti = t["latest_tti"]
    alive = create_tti >= 0 and create_tti >= release_tti
    if not alive:
        q_lvl, gbr_kbps = default_q, 0
    elif q_lvl not in GBR_5QI or gbr_tti < 0:
        gbr_kbps = 0 if q_lvl not in GBR_5QI else default_gbr
    return q_lvl, gbr_kbps, alive, crnti


def load_qos_config(host, local=False):
    if local:
        try:
            xml = open(CONFDB_PATH).read()
        except Exception:
            return None
    else:
        xml = ssh_run(host, "cat %s" % CONFDB_PATH)
    if not xml:
        return None
    try:
        root = ET.fromstring(xml)
    except ET.ParseError:
        return None
    cfg = {"fiveqi": [], "gfbr_ul_max": 0, "gfbr_dl_max": 0}
    for tag, text in _walk(root):
        if tag == "FiveQI" and text not in (None, ""):
            cfg["fiveqi"].append(text.strip())
        elif tag == "ULMaxAccumGFBRPerDrb" and text not in (None, ""):
            cfg["gfbr_ul_max"] = int(text.strip())
        elif tag == "DLMaxAccumGFBRPerDrb" and text not in (None, ""):
            cfg["gfbr_dl_max"] = int(text.strip())
    return cfg


def _walk(el):
    children = list(el)
    if not children:
        yield el.tag, (el.text or "").strip()
    else:
        for c in children:
            yield from _walk(c)


def post_metrics(url, samples):
    body = json.dumps({"metrics": samples}).encode()
    req = urllib.request.Request(url, data=body, method="POST")
    req.add_header("Content-Type", "application/json")
    try:
        with urllib.request.urlopen(req, timeout=HTTP_TIMEOUT) as r:
            return r.status, r.read().decode("utf-8", "replace")
    except urllib.error.HTTPError as e:
        return e.code, e.read().decode("utf-8", "replace")
    except Exception as e:
        return 0, str(e)


def bridge_apply_to_mock_ran(mock_url, request_id, five_qi, gbr_kbps, burst_ms):
    """smf-mock 桥接: 把 QoSModule 经 SMF 下发的 QoS 通知 mock-ran, 触发其状态机模拟吞吐。
    QoSModule smf 模式 POST 真 SMF(不经过 mock-ran), 故 mock-ran 不知道发生了下发;
    由 collector 从 qos-module.log 检测到 apply 后, 用 ranapi 格式 POST 给 mock-ran,
    mock-ran 收到后跑 RAMP_UP/STEADY/RAMP_DOWN, collector 再读其 /metrics 上报前端。"""
    body = json.dumps({
        "request_id": request_id,
        "rnti": 0,
        "q_qfi": 4,
        "q_lvl": five_qi,
        "q_gbr_ul": gbr_kbps,
        "q_gbr_dl": 0,
        "burst_info": {"ul_burst_duration": burst_ms, "ul_burst_size": 0, "e2e_delay_budget": 0},
    }).encode()
    url = mock_url.rstrip("/") + "/api/v1/qos/update"
    req = urllib.request.Request(url, data=body, method="POST")
    req.add_header("Content-Type", "application/json")
    try:
        with urllib.request.urlopen(req, timeout=HTTP_TIMEOUT) as r:
            return r.status, r.read().decode("utf-8", "replace")
    except Exception as e:
        return 0, str(e)


def take_sample(host, local=False, mock_url=None):
    # mock-ran 模式: HTTP GET /metrics 直接拿快照, 跳过 SSH/odi/parse_trace。
    # mock-ran 已是真值源(sendrate/gbr/q_lvl/alive/crnti 由其状态机算好)。
    if mock_url:
        url = mock_url.rstrip("/") + "/metrics"
        try:
            with urllib.request.urlopen(url, timeout=HTTP_TIMEOUT) as r:
                data = json.loads(r.read().decode("utf-8", "replace"))
        except Exception as e:
            log("mock-ran /metrics failed: %s" % e)
            return {}
        return data
    if local:
        r = subprocess.run(ODI_CMD, shell=True, capture_output=True, text=True, timeout=SSH_TIMEOUT)
        if r.returncode != 0 and r.stderr:
            log("odi stderr: %s" % r.stderr.strip()[:200])
        return parse_trace(r.stdout)
    return parse_trace(ssh_run(host, ODI_CMD))


def load_active_qos_qm():
    """从 QoSModule 日志取当前活跃下发(apply 后未 release, 且 status=ACCEPTED)的最新一条。
    返回 dict {request_id, five_qi, gbr_kbps, burst_ms} 或 None。
    基站 L2 trace 只到 DRB 级(5QI=5 默认承载、无 GFBR), 不暴露专载 5QI/GBR;
    当基站侧取不到 GBR 时, 用下发侧真值兜底。无活跃下发返回 None。"""
    try:
        with open(QOSMODULE_LOG) as f:
            lines = f.readlines()[-80:]
    except Exception:
        return None
    apply_state = {}
    rel_ts = {}
    for ln in lines:
        tsm = QM_TS_RE.search(ln)
        ts = tsm.group(1) if tsm else ""
        am = QM_APPLY_RE.search(ln)
        if am:
            rid = am.group(1)
            qi = int(am.group(2))
            gbr = max(int(am.group(3)), int(am.group(4)))
            status = am.group(5)
            burst_ms = int(am.group(6))
            # 仅 ACCEPTED 的 apply 才算真正下发成功(失败的 apply 无后续 release)
            apply_state[rid] = (ts, qi, gbr, burst_ms) if status == "ACCEPTED" else None
        rm = QM_REL_RE.search(ln)
        if rm:
            rel_ts[rm.group(1)] = ts
    best = None
    for rid, st in apply_state.items():
        if st is None:
            continue
        ts, qi, gbr, burst_ms = st
        rts = rel_ts.get(rid)
        if rts and rts >= ts:
            continue
        if best is None or ts >= best[0]:
            best = (ts, rid, qi, gbr, burst_ms)
    if not best:
        return None
    return {"request_id": best[1], "five_qi": best[2], "gbr_kbps": best[3], "burst_ms": best[4]}


def build_sample(prev, curr, dt, q_lvl_override, gbr_override, qm=None, mock=False):
    # mock-ran 模式: curr 是 /metrics 快照 dict, sendrate/gbr/q_lvl/alive/crnti 已算好。
    # 不调 extract_qos (trace 解析), 不调 load_active_qos_qm (qos-module.log 兜底) —— mock-ran 是真值源。
    if mock:
        q_auto = int(curr.get("q_lvl", 9) or 9)
        gbr_auto = int(curr.get("gbr_kbps", 0) or 0)
        alive = bool(curr.get("alive", False))
        crnti = curr.get("crnti")
        sendrate = int(curr.get("sendrate_kbps", 0) or 0)
        if q_lvl_override is not None or gbr_override is not None:
            q_lvl = q_lvl_override if q_lvl_override is not None else q_auto
            gbr_kbps = gbr_override if gbr_override is not None else gbr_auto
        else:
            q_lvl, gbr_kbps = q_auto, gbr_auto
        s = {
            "timestamp": int(time.time() * 1000),
            "sendrate_kbps": sendrate,
            "gbr_kbps": gbr_kbps,
            "q_lvl": q_lvl,
        }
        return s, alive, crnti
    q_auto, gbr_auto, alive, crnti = extract_qos(curr)
    if q_lvl_override is not None or gbr_override is not None:
        # 人工 override 模式(联调): 完全用人工值, 缺省字段回退基站, 不混入 QoSModule
        q_lvl = q_lvl_override if q_lvl_override is not None else q_auto
        gbr_kbps = gbr_override if gbr_override is not None else gbr_auto
    else:
        # 自动模式: QoSModule 活跃下发的 (five_qi, gbr) 优先——
        # 基站 L2 的 AMBR "第二槽"是推断值(非真 GFBR, 不可信), 故有下发就用下发真值;
        # 无下发则回退基站 DRB 的 5QI/gbr(默认承载/空闲)。
        if qm is None:
            qm = load_active_qos_qm()
        if qm:
            q_lvl, gbr_kbps = qm["five_qi"], qm["gbr_kbps"]
        else:
            q_lvl, gbr_kbps = q_auto, gbr_auto
    s = {
        "timestamp": int(time.time() * 1000),
        "sendrate_kbps": compute_sendrate(prev, curr, dt),
        "gbr_kbps": gbr_kbps,
        "q_lvl": q_lvl,
    }
    return s, alive, crnti


def run_once(host, url, q_lvl_override, gbr_override, interval, local=False, mock_url=None):
    prev = take_sample(host, local, mock_url)
    t_prev = time.time()
    time.sleep(interval)
    curr = take_sample(host, local, mock_url)
    t_curr = time.time()
    dt = t_curr - t_prev or interval
    mock = bool(mock_url)
    if mock:
        q, g, alive, crnti = (int(curr.get("q_lvl", 9) or 9),
                               int(curr.get("gbr_kbps", 0) or 0),
                               bool(curr.get("alive", False)),
                               curr.get("crnti"))
        log("mock-ran snapshot: alive=%s crnti=%s q_lvl=%s gbr=%skbps dt=%.3fs (override: q=%s gbr=%s)"
            % (alive, crnti, q, g, dt, q_lvl_override, gbr_override))
    else:
        q, g, alive, crnti = extract_qos(curr)
        log("auto关联: alive=%s crnti=%s q_lvl=%s gbr=%skbps dt=%.3fs (override: q=%s gbr=%s)"
            % (alive, crnti, q, g, dt, q_lvl_override, gbr_override))
    s, _alive, _crnti = build_sample(prev, curr, dt, q_lvl_override, gbr_override, mock=mock)
    log("sample: %s" % json.dumps(s, ensure_ascii=False))
    st, resp = post_metrics(url, [s])
    log("POST -> %s %s" % (st, resp[:200]))
    return st == 200


def run_loop(host, url, q_lvl_override, gbr_override, interval, window_size=WINDOW_SIZE, push_interval=PUSH_INTERVAL, local=False, mock_url=None, tail_secs=8.0, smf_bridge=False):
    mock = bool(mock_url)
    log("loop start: interval=%.2fs push=%.2fs window=%d tail=%.1fs target=%s 变更/活动触发推送,burst结束后tail填满窗口右侧再冻结 (override q=%s gbr=%s local=%s mock=%s smf_bridge=%s%s)"
        % (interval, push_interval, window_size, tail_secs, url, q_lvl_override, gbr_override, local, mock, smf_bridge, (" url=%s" % mock_url) if mock else ""))
    window = deque(maxlen=window_size)
    prev = take_sample(host, local, mock_url)
    t_prev = time.time()
    next_t = t_prev
    last_push = 0.0
    last_pushed = None
    last_alive = None
    tail_until = 0.0
    bridged_rids = set()  # smf_bridge: 已桥接到 mock-ran 的 request_id, 避免重复 POST
    while True:
        next_t += interval
        sleep_for = next_t - time.time()
        if sleep_for > 0:
            time.sleep(sleep_for)
        curr = take_sample(host, local, mock_url)
        t_curr = time.time()
        dt = t_curr - t_prev or interval
        # smf-mock 桥接: QoSModule 经 SMF 下发(POST 真 SMF, 不经过 mock-ran), collector
        # 从 qos-module.log 检测到新 apply 后, 用 ranapi 格式通知 mock-ran 触发其状态机模拟。
        # ran/ran-udp 模式 QoSModule 直 POST mock-ran, 无需桥接。
        if smf_bridge and mock:
            apply_info = load_active_qos_qm()
            if apply_info and apply_info["request_id"] not in bridged_rids:
                st, resp = bridge_apply_to_mock_ran(mock_url, apply_info["request_id"],
                                                    apply_info["five_qi"], apply_info["gbr_kbps"],
                                                    apply_info["burst_ms"])
                bridged_rids.add(apply_info["request_id"])
                log("bridge smf apply -> mock-ran: request_id=%s five_qi=%d gbr=%dkbps burst_ms=%d -> HTTP %s"
                    % (apply_info["request_id"], apply_info["five_qi"], apply_info["gbr_kbps"],
                       apply_info["burst_ms"], st))
        # mock-ran 是真值源, 无需读 qos-module.log 兜底 build_sample; 真实模式才读
        qm = None if mock else load_active_qos_qm()
        s, alive, crnti = build_sample(prev, curr, dt, q_lvl_override, gbr_override, qm, mock=mock)
        window.append(s)
        prev = curr
        t_prev = t_curr
        now = time.time()
        active = alive or qm is not None
        state_changed = (last_pushed is None or
                         s["q_lvl"] != last_pushed["q_lvl"] or
                         s["gbr_kbps"] != last_pushed["gbr_kbps"] or
                         alive != last_alive)
        # burst 结束(alive 由 True 转 False)时启动 tail: 继续推 tail_secs 秒,
        # 让前端窗口右侧的恢复段(IDLE 基线)填满, 再冻结。避免 burst 一结束就冻、右侧留空。
        if not alive and alive != last_alive:
            tail_until = now + tail_secs
        in_tail = now < tail_until
        should_push = active or state_changed or in_tail
        rate_ok = last_pushed is None or now - last_push >= push_interval
        if should_push and rate_ok:
            st, resp = post_metrics(url, list(window))
            if st == 200:
                reason = "变更触发" if state_changed else ("tail填满" if in_tail and not active else "活动持续")
                log("ok alive=%s crnti=%s sendrate=%dkbps gbr=%dkbps qlvl=%d (push %d samples, %s)" % (alive, crnti, s["sendrate_kbps"], s["gbr_kbps"], s["q_lvl"], len(window), reason))
            else:
                log("ERR %s %s" % (st, resp[:200]))
            last_push = now
            last_pushed = s
            last_alive = alive
        elif rate_ok and not should_push:
            log("idle alive=%s crnti=%s sendrate=%dkbps gbr=%dkbps qlvl=%d (空闲静默)" % (alive, crnti, s["sendrate_kbps"], s["gbr_kbps"], s["q_lvl"]))
            last_push = now


def main():
    global QOSMODULE_LOG
    ap = argparse.ArgumentParser()
    ap.add_argument("--host", default=GNB_HOST)
    ap.add_argument("--url", default=FRONTEND_URL)
    ap.add_argument("--interval", type=float, default=INTERVAL, help="上报周期秒(默认0.5)")
    ap.add_argument("--q-lvl", type=int, default=None, help="覆盖 5QI(默认自动从 trace 关联)")
    ap.add_argument("--gbr-kbps", type=int, default=None, help="覆盖 GFBR kbps(默认自动从 trace 关联)")
    ap.add_argument("--window", type=int, default=WINDOW_SIZE, help="滑动窗口样本数(默认30,每次推送整窗替换前端曲线)")
    ap.add_argument("--push-interval", type=float, default=PUSH_INTERVAL, help="推送周期秒(默认1.0,每次发整份窗口)")
    ap.add_argument("--once", action="store_true", help="只采一次并 POST(联调用)")
    ap.add_argument("--no-mux", action="store_true", help="禁用 SSH 连接复用(0.5s高频不建议)")
    ap.add_argument("--local", action="store_true", help="本机直跑 odi(部署在基站上时用,去 SSH 开销)")
    ap.add_argument("--mock-ran", default=None, help="mock-ran URL(如 http://127.0.0.1:18080); 跳过 SSH/parse_trace/qos-module.log, 直接采 mock 数据。ran/ran-udp 模式: QoSModule 直 POST mock-ran。")
    ap.add_argument("--smf-mock-ran", default=None, help="smf 桥接 mock-ran URL: QoSModule 经 SMF 下发(POST 真 SMF 不经 mock-ran), collector 从 qos-module.log 检测 apply 后桥接通知 mock-ran 模拟吞吐, 再读 mock-ran 上报。数据源同 --mock-ran, 额外开启 smf_bridge。")
    ap.add_argument("--qos-log", default=None, help="QoSModule 日志路径(默认 %s), smf-mock-ran 桥接从此读 smf enforcer apply 行" % QOSMODULE_LOG)
    ap.add_argument("--tail-secs", type=float, default=8.0, help="burst 结束后继续推送的尾期秒数(默认8.0, 填满前端窗口右侧恢复段后再冻结)")
    args = ap.parse_args()

    # --qos-log 覆盖 QoSModule 日志路径(load_active_qos_qm 读此)
    if args.qos_log:
        QOSMODULE_LOG = args.qos_log

    # smf-mock-ran: 数据源用 mock-ran(--mock-ran 语义), 额外开启 smf_bridge
    mock_url = args.mock_ran or args.smf_mock_ran
    smf_bridge = bool(args.smf_mock_ran)

    # mock-ran / smf-mock-ran 模式: 跳过 SSH/odi/confdb, mock-ran 是真值源
    if mock_url:
        mode = "smf-mock-ran 桥接" if smf_bridge else "mock-ran"
        log("%s 模式: %s (跳过 SSH/odi/confdb%s)" % (mode, mock_url, ", smf apply 从 qos-module.log 桥接到 mock-ran" if smf_bridge else ""))
    else:
        if not args.local and not args.no_mux:
            open_ssh_master(args.host)
        cfg = load_qos_config(args.host, args.local)
        if cfg and cfg["fiveqi"]:
            log("confdb FiveQI entries: %s" % cfg["fiveqi"])

    try:
        if args.once:
            ok = run_once(args.host, args.url, args.q_lvl, args.gbr_kbps, args.interval, args.local, mock_url)
            sys.exit(0 if ok else 1)
        run_loop(args.host, args.url, args.q_lvl, args.gbr_kbps, args.interval, args.window, args.push_interval, args.local, mock_url, args.tail_secs, smf_bridge)
    finally:
        if not mock_url and not args.local and not args.no_mux:
            close_ssh_master(args.host)


if __name__ == "__main__":
    main()
