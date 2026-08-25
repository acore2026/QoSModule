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
QM_APPLY_RE = re.compile(r"smf enforcer apply .*?request_id=(\S+).*?five_qi=(\d+).*?gbr_ul=(\d+).*?gbr_dl=(\d+)")
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


def take_sample(host, local=False):
    if local:
        r = subprocess.run(ODI_CMD, shell=True, capture_output=True, text=True, timeout=SSH_TIMEOUT)
        if r.returncode != 0 and r.stderr:
            log("odi stderr: %s" % r.stderr.strip()[:200])
        return parse_trace(r.stdout)
    return parse_trace(ssh_run(host, ODI_CMD))


def load_active_qos_qm():
    """从 QoSModule 日志取当前活跃下发(apply 后未 release)的 (five_qi, gbr_kbps)。
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
            rid, qi, gu, gd = am.group(1), int(am.group(2)), int(am.group(3)), int(am.group(4))
            apply_state[rid] = (ts, qi, max(gu, gd))
        rm = QM_REL_RE.search(ln)
        if rm:
            rel_ts[rm.group(1)] = ts
    best = None
    for rid, (ts, qi, gbr) in apply_state.items():
        rts = rel_ts.get(rid)
        if rts and rts >= ts:
            continue
        if best is None or ts >= best[0]:
            best = (ts, qi, gbr)
    return (best[1], best[2]) if best else None


def build_sample(prev, curr, dt, q_lvl_override, gbr_override, qm=None):
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
            q_lvl, gbr_kbps = qm
        else:
            q_lvl, gbr_kbps = q_auto, gbr_auto
    s = {
        "timestamp": int(time.time() * 1000),
        "sendrate_kbps": compute_sendrate(prev, curr, dt),
        "gbr_kbps": gbr_kbps,
        "q_lvl": q_lvl,
    }
    return s, alive, crnti


def run_once(host, url, q_lvl_override, gbr_override, interval, local=False):
    prev = take_sample(host, local)
    t_prev = time.time()
    time.sleep(interval)
    curr = take_sample(host, local)
    t_curr = time.time()
    dt = t_curr - t_prev or interval
    q, g, alive, crnti = extract_qos(curr)
    log("auto关联: alive=%s crnti=%s q_lvl=%s gbr=%skbps dt=%.3fs (override: q=%s gbr=%s)" % (alive, crnti, q, g, dt, q_lvl_override, gbr_override))
    s, _alive, _crnti = build_sample(prev, curr, dt, q_lvl_override, gbr_override)
    log("sample: %s" % json.dumps(s, ensure_ascii=False))
    st, resp = post_metrics(url, [s])
    log("POST -> %s %s" % (st, resp[:200]))
    return st == 200


def run_loop(host, url, q_lvl_override, gbr_override, interval, window_size=WINDOW_SIZE, push_interval=PUSH_INTERVAL, local=False):
    log("loop start: interval=%.2fs push=%.2fs window=%d target=%s 变更/活动触发推送,空闲静默 (override q=%s gbr=%s local=%s)" % (interval, push_interval, window_size, url, q_lvl_override, gbr_override, local))
    window = deque(maxlen=window_size)
    prev = take_sample(host, local)
    t_prev = time.time()
    next_t = t_prev
    last_push = 0.0
    last_pushed = None
    last_alive = None
    while True:
        next_t += interval
        sleep_for = next_t - time.time()
        if sleep_for > 0:
            time.sleep(sleep_for)
        curr = take_sample(host, local)
        t_curr = time.time()
        dt = t_curr - t_prev or interval
        qm = load_active_qos_qm()
        s, alive, crnti = build_sample(prev, curr, dt, q_lvl_override, gbr_override, qm)
        window.append(s)
        prev = curr
        t_prev = t_curr
        now = time.time()
        active = alive or qm is not None
        state_changed = (last_pushed is None or
                         s["q_lvl"] != last_pushed["q_lvl"] or
                         s["gbr_kbps"] != last_pushed["gbr_kbps"] or
                         alive != last_alive)
        should_push = active or state_changed
        rate_ok = last_pushed is None or now - last_push >= push_interval
        if should_push and rate_ok:
            st, resp = post_metrics(url, list(window))
            if st == 200:
                log("ok alive=%s crnti=%s sendrate=%dkbps gbr=%dkbps qlvl=%d (push %d samples, %s)" % (alive, crnti, s["sendrate_kbps"], s["gbr_kbps"], s["q_lvl"], len(window), "变更触发" if state_changed else "活动持续"))
            else:
                log("ERR %s %s" % (st, resp[:200]))
            last_push = now
            last_pushed = s
            last_alive = alive
        elif rate_ok and not should_push:
            log("idle alive=%s crnti=%s sendrate=%dkbps gbr=%dkbps qlvl=%d (空闲静默)" % (alive, crnti, s["sendrate_kbps"], s["gbr_kbps"], s["q_lvl"]))
            last_push = now


def main():
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
    args = ap.parse_args()

    if not args.local and not args.no_mux:
        open_ssh_master(args.host)
    cfg = load_qos_config(args.host, args.local)
    if cfg and cfg["fiveqi"]:
        log("confdb FiveQI entries: %s" % cfg["fiveqi"])

    try:
        if args.once:
            ok = run_once(args.host, args.url, args.q_lvl, args.gbr_kbps, args.interval, args.local)
            sys.exit(0 if ok else 1)
        run_loop(args.host, args.url, args.q_lvl, args.gbr_kbps, args.interval, args.window, args.push_interval, args.local)
    finally:
        if not args.local and not args.no_mux:
            close_ssh_master(args.host)


if __name__ == "__main__":
    main()
