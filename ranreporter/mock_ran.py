#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""mock-ran: 模拟 gNB 的 QoS 下发接收端 + 实时 sendrate 状态机。

供 collector.py --mock-ran 模式本地联调用。替代真实 gNB 的 odi tracebuff。

行为:
  - 无 QoS 下发时 (IDLE):     sendrate ~ 1500 ± N(0,60) kbps
  - QoS 下发后 (RAMP_UP→STEADY→RAMP_DOWN→RELEASED):
      * RAMP_UP   (0~0.5s):     sendrate 从 1500 渐升到 GBR*0.9 (慢于 GBR 瞬时到位)
      * STEADY    (0.5s~burst-0.3s): sendrate = GBR*0.9 ± N(0,30) (略小于 GBR)
      * RAMP_DOWN (burst-0.3s~burst): sendrate 从 GBR*0.9 渐降到 1500 (早于 GBR 释放)
      * t>=burst:               自动释放, 回 IDLE
  - GBR=0 (非 GBR 5QI): 全程保持 IDLE 基线 (QoS 不影响吞吐)

接口:
  POST /api/v1/qos/update          接收 QoSModule (ran 模式) 的 ranapi.Request 体
  POST /nsmf-oam/v1/qos-update     接收 QoSModule (ngap 模式) 的 SMF OAM 体 (mock-ran 当 SMF)
  POST /api/v1/qos/release         手动提前释放 (调试用)
  POST /nsmf-oam/v1/qos-release   SMF 释放端点 (smfenforcer 自动释放调用)
  GET  /metrics                    返回当前快照 (collector 拉取)
  GET  /                           简易状态页
"""
import json
import math
import random
import re
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

import argparse
DEFAULT_PORT = 18081

# smfenforcer bitrateBps 输出 "N bps" (N=kbps*1000), 用于解析 OAM 体的 gbr/mbr
_BPS_RE = re.compile(r"(\d+)\s*bps", re.IGNORECASE)

# ---- 状态机参数 (锁定) ----
BASELINE_KBPS = 1500
BASELINE_NOISE = 60.0      # IDLE 段高斯噪声 sigma
STEADY_RATIO = 0.9        # STEADY = GBR * 0.9
STEADY_NOISE = 30.0       # STEADY 段高斯噪声 sigma
RAMP_UP_MS = 500.0        # 上升窗口
RAMP_DOWN_LEAD_MS = 300.0  # 下降提前窗口
TICK_MS = 100             # 状态机采样间隔
DEFAULT_BURST_MS = 1000   # 请求体无 burst_duration 时的兜底

_lock = threading.Lock()
_state = {
    "active": None,  # None=IDLE, 或 {"gbr","q_lvl","crnti","request_id","apply_t","burst_ms"}
    "current": {
        "timestamp_ms": int(time.time() * 1000),
        "sendrate_kbps": BASELINE_KBPS,
        "gbr_kbps": 0,
        "q_lvl": 9,
        "alive": False,
        "crnti": None,
    },
}


def _lerp(a, b, t):
    if t <= 0:
        return a
    if t >= 1:
        return b
    return a + (b - a) * t


def _gauss(sigma):
    return random.gauss(0.0, sigma)


def _tick():
    """100ms 一次, 根据 active 状态推进 sendrate。"""
    now = time.time()
    with _lock:
        a = _state["active"]
        # 自动释放: burst 窗口结束
        if a is not None and (now - a["apply_t"]) * 1000.0 >= a["burst_ms"]:
            _state["active"] = None
            a = None

        if a is None:
            sr = BASELINE_KBPS + _gauss(BASELINE_NOISE)
            gbr = 0
            q_lvl = 9
            alive = False
            crnti = None
        else:
            t_ms = (now - a["apply_t"]) * 1000.0
            gbr = a["gbr"]
            q_lvl = a["q_lvl"]
            crnti = a["crnti"]
            burst_ms = a["burst_ms"]
            alive = True
            if gbr == 0:
                # 非 GBR 5QI: QoS 不影响吞吐, 维持基线
                sr = BASELINE_KBPS + _gauss(BASELINE_NOISE)
            elif t_ms < RAMP_UP_MS:
                p = t_ms / RAMP_UP_MS
                sr = _lerp(BASELINE_KBPS, gbr * STEADY_RATIO, p) + _gauss(STEADY_NOISE)
            elif t_ms < burst_ms - RAMP_DOWN_LEAD_MS:
                sr = gbr * STEADY_RATIO + _gauss(STEADY_NOISE)
            elif t_ms < burst_ms:
                p = (t_ms - (burst_ms - RAMP_DOWN_LEAD_MS)) / RAMP_DOWN_LEAD_MS
                sr = _lerp(gbr * STEADY_RATIO, BASELINE_KBPS, p) + _gauss(STEADY_NOISE)
            else:
                # 理论上下次 tick 会自动释放, 兜底
                sr = BASELINE_KBPS + _gauss(BASELINE_NOISE)
                gbr = 0
                q_lvl = 9
                alive = False
                crnti = None
        _state["current"] = {
            "timestamp_ms": int(now * 1000),
            "sendrate_kbps": max(0, int(round(sr))),
            "gbr_kbps": gbr,
            "q_lvl": q_lvl,
            "alive": alive,
            "crnti": crnti,
        }


def _tick_loop():
    while True:
        _tick()
        time.sleep(TICK_MS / 1000.0)


def _parse_qos_update(body):
    """从 ranapi.Request 体提取生效 GBR / q_lvl / burst_ms / crnti。
    字段定义对齐 adaptiveqos/ranapi/client.go BuildRequest。"""
    try:
        req = json.loads(body or b"{}")
    except Exception:
        return None
    gbr_ul = int(req.get("q_gbr_ul", 0) or 0)
    gbr_dl = int(req.get("q_gbr_dl", 0) or 0)
    gbr = max(gbr_ul, gbr_dl)  # 取上下行最大为生效 GBR
    q_lvl = int(req.get("q_lvl", 9) or 9)
    crnti = int(req.get("rnti", 0) or 0)
    request_id = req.get("request_id", "")
    burst = req.get("burst_info") or {}
    burst_ms = int(burst.get("ul_burst_duration", 0) or 0)
    if burst_ms == 0:
        burst_ms = int(burst.get("dl_burst_duration", 0) or 0)
    if burst_ms == 0:
        burst_ms = int(burst.get("e2e_delay_budget", 0) or 0)
    if burst_ms == 0:
        burst_ms = DEFAULT_BURST_MS
    return {
        "gbr": gbr,
        "q_lvl": q_lvl,
        "crnti": crnti,
        "request_id": request_id,
        "burst_ms": burst_ms,
    }


def _parse_bps(s):
    """解析 smfenforcer 的比特率字符串 "N bps" (bitrateBps 输出), 返回 bps 整数。空/0 返回 0。"""
    if not s:
        return 0
    s = str(s).strip()
    m = _BPS_RE.match(s)
    if not m:
        return 0
    return int(m.group(1))


def _parse_smf_oam_update(body):
    """从 smfenforcer 的 oamQoSUpdateRequest 体提取生效 GBR / q_lvl / burst_ms。
    字段定义对齐 adaptiveqos/smfenforcer/enforcer.go buildOAMBody:
      gbr_ul/gbr_dl 为 "N bps" 字符串(由 bitrateBps(kbps)=kbps*1000+" bps" 生成),
      five_qi 即 q_lvl, ue_ip 用于日志, burst_ms 由 smfenforcer 透传(真实 SMF 忽略此字段)。"""
    try:
        req = json.loads(body or b"{}")
    except Exception:
        return None
    gbr_ul_bps = _parse_bps(req.get("gbr_ul", ""))
    gbr_dl_bps = _parse_bps(req.get("gbr_dl", ""))
    gbr = max(gbr_ul_bps, gbr_dl_bps) // 1000  # bps -> kbps
    q_lvl = int(req.get("five_qi", 9) or 9)
    request_id = req.get("request_id", "")
    ue_ip = req.get("ue_ip", "")
    burst_ms = int(req.get("burst_ms", 0) or 0)
    if burst_ms == 0:
        burst_ms = DEFAULT_BURST_MS  # OAM 体无 burst_ms 时兜底(smfenforcer 未透传)
    # SMF 路径无 rnti, 用 ue_ip 末段做 crnti 占位便于日志辨识
    crnti = 0
    if ue_ip:
        try:
            crnti = int(ue_ip.split(".")[-1])
        except ValueError:
            crnti = 0
    return {
        "gbr": gbr,
        "q_lvl": q_lvl,
        "crnti": crnti,
        "request_id": request_id,
        "burst_ms": burst_ms,
        "ue_ip": ue_ip,
    }


def _apply_parsed(parsed):
    """把解析出的 QoS 参数写入活跃状态, 触发状态机。ranapi 与 SMF OAM 端点共用。"""
    with _lock:
        _state["active"] = {
            "gbr": parsed["gbr"],
            "q_lvl": parsed["q_lvl"],
            "crnti": parsed["crnti"],
            "request_id": parsed["request_id"],
            "apply_t": time.time(),
            "burst_ms": parsed["burst_ms"],
        }
    print("[apply] request_id=%s gbr=%dkbps q_lvl=%d burst=%dms crnti=%d"
          % (parsed["request_id"], parsed["gbr"], parsed["q_lvl"],
             parsed["burst_ms"], parsed["crnti"]), flush=True)


class H(BaseHTTPRequestHandler):
    def do_POST(self):
        n = int(self.headers.get("Content-Length", 0))
        body = self.rfile.read(n) if n > 0 else b""

        # ran 模式: ranapi.Request 格式 (q_gbr_ul/q_lvl/burst_info)
        if self.path == "/api/v1/qos/update":
            parsed = _parse_qos_update(body)
            if parsed is None:
                self._reply(400, {"status": "REJECTED", "error_code": "INVALID_BODY",
                                  "message": "invalid json body"})
                return
            _apply_parsed(parsed)
            # ranapi.Client 期望 {status, error_code, message}; 附 request_id 便于追溯
            self._reply(200, {"status": "ACCEPTED", "request_id": parsed["request_id"],
                              "error_code": "", "message": "applied"})
            return

        # ngap 模式: mock-ran 当 SMF, 接收 smfenforcer 的 oamQoSUpdateRequest
        # (ue_ip/five_qi/mbr_ul "N bps"/gbr_ul/burst_ms), 真实模拟由 mock-ran 完成
        if self.path == "/nsmf-oam/v1/qos-update":
            parsed = _parse_smf_oam_update(body)
            if parsed is None:
                # ProblemDetails 错误格式 (smfenforcer qosUpdateResponse 解析 cause/detail)
                self._reply(400, {"title": "invalid body", "detail": "invalid json",
                                  "cause": "INVALID_BODY"})
                return
            _apply_parsed(parsed)
            # smfenforcer qosUpdateResponse 期望: status/request_id/ue_ip/supi/qfi/five_qi/amf_cause
            self._reply(200, {
                "status": "ACCEPTED",
                "request_id": parsed["request_id"],
                "ue_ip": parsed.get("ue_ip", ""),
                "supi": "imsi-mock-0001",
                "qfi": 4,
                "five_qi": parsed["q_lvl"],
                "amf_cause": "",
            })
            return

        # 释放端点 (两个路径)
        if self.path in ("/api/v1/qos/release", "/nsmf-oam/v1/qos-release"):
            with _lock:
                _state["active"] = None
            print("[release] %s -> IDLE" % self.path, flush=True)
            self._reply(200, {"status": "ACCEPTED", "message": "released"})
            return

        self._reply(404, {"error": "not found"})

    def do_GET(self):
        if self.path == "/metrics":
            with _lock:
                snap = dict(_state["current"])
            self._reply(200, snap)
        elif self.path == "/" or self.path == "/state":
            with _lock:
                snap = dict(_state["current"])
                active = _state["active"]
            html = "<html><body><h3>mock-ran state</h3><pre>%s</pre><pre>active=%s</pre></body></html>" % (
                json.dumps(snap, indent=2), json.dumps(active, default=str, indent=2) if active else "None")
            self._reply(200, html, ctype="text/html; charset=utf-8")
        else:
            self._reply(404, {"error": "not found"})

    def _reply(self, code, obj, ctype="application/json"):
        if "json" in ctype:
            body = json.dumps(obj).encode()
        else:
            body = obj.encode() if isinstance(obj, str) else obj
        self.send_response(code)
        self.send_header("Content-Type", ctype)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *a):
        pass


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--port", type=int, default=DEFAULT_PORT, help="监听端口(默认 %d)" % DEFAULT_PORT)
    ap.add_argument("--host", default="0.0.0.0", help="监听地址")
    args = ap.parse_args()
    listen = (args.host, args.port)
    t = threading.Thread(target=_tick_loop, daemon=True)
    t.start()
    print("mock-ran listening on %s:%d (IDLE baseline=%dkbps, STEADY=GBR*%.2f, ramp_up=%.0fms ramp_down_lead=%.0fms)"
          % (listen[0], listen[1], BASELINE_KBPS, STEADY_RATIO, RAMP_UP_MS, RAMP_DOWN_LEAD_MS), flush=True)
    ThreadingHTTPServer(listen, H).serve_forever()


if __name__ == "__main__":
    main()
