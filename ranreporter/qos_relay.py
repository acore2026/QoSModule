#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""中转: 监听 0.0.0.0:28448, 把 POST 转发到前端 192.168.1.10:28448。
供部署在基站本机的 collector(--local) 用——基站路由表无默认路由、到不了 192.168.1.10,
只能到同子网的核心机 10.88.120.100, 故由核心机中转。"""
import urllib.request
import urllib.error
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

TARGET = "http://192.168.1.10:28448"
LISTEN = ("0.0.0.0", 28448)


class H(BaseHTTPRequestHandler):
    def do_POST(self):
        n = int(self.headers.get("Content-Length", 0))
        body = self.rfile.read(n)
        req = urllib.request.Request(TARGET + self.path, data=body, method="POST")
        req.add_header("Content-Type", self.headers.get("Content-Type", "application/json"))
        try:
            with urllib.request.urlopen(req, timeout=5) as r:
                resp = r.read()
                self.send_response(r.status)
                self.end_headers()
                self.wfile.write(resp)
        except urllib.error.HTTPError as e:
            self.send_response(e.code)
            self.end_headers()
            self.wfile.write(e.read())
        except Exception as e:
            self.send_response(502)
            self.end_headers()
            self.wfile.write(str(e).encode())

    def log_message(self, *a):
        pass


if __name__ == "__main__":
    print("relay %s:%d -> %s" % (LISTEN[0], LISTEN[1], TARGET), flush=True)
    ThreadingHTTPServer(LISTEN, H).serve_forever()
