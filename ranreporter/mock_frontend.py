#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""前端接收端 mock: 实现规范 POST /api/v1/qos -> {"ok":true,"type":"metrics"} 或 400。用于联调采集器。"""
import json
from http.server import BaseHTTPRequestHandler, HTTPServer

VALID_KEYS = {"timestamp", "sendrate_kbps", "gbr_kbps", "q_lvl"}


class H(BaseHTTPRequestHandler):
    def do_POST(self):
        if self.path != "/api/v1/qos":
            self._err(404, "not found")
            return
        n = int(self.headers.get("Content-Length", 0))
        try:
            body = json.loads(self.rfile.read(n) or "{}")
        except Exception as e:
            self._err(400, "invalid json: %s" % e)
            return
        metrics = body.get("metrics")
        if not isinstance(metrics, list) or not metrics:
            self._err(400, "metrics must be a non-empty list")
            return
        for i, m in enumerate(metrics):
            if not isinstance(m, dict):
                self._err(400, "metrics[%d] not object" % i)
                return
            missing = VALID_KEYS - set(m.keys())
            if missing:
                self._err(400, "metrics[%d] missing: %s" % (i, ",".join(sorted(missing))))
                return
            if not isinstance(m["timestamp"], int) or m["timestamp"] <= 0:
                self._err(400, "metrics[%d] bad timestamp" % i)
                return
            for k in ("sendrate_kbps", "gbr_kbps", "q_lvl"):
                if not isinstance(m[k], (int, float)):
                    self._err(400, "metrics[%d] %s not numeric" % (i, k))
                    return
        self._ok(metrics)

    def _ok(self, metrics):
        resp = json.dumps({"ok": True, "type": "metrics"}).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(resp)))
        self.end_headers()
        self.wfile.write(resp)
        print("[recv] %d samples: %s" % (len(metrics), json.dumps(metrics, ensure_ascii=False)[:300]), flush=True)

    def _err(self, code, msg):
        resp = json.dumps({"error": msg}).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(resp)))
        self.end_headers()
        self.wfile.write(resp)
        print("[reject %d] %s" % (code, msg), flush=True)

    def log_message(self, *a):
        pass


if __name__ == "__main__":
    HTTPServer(("127.0.0.1", 28448), H).serve_forever()
