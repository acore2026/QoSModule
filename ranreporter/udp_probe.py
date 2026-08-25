#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""UDP 端点探针: 给 host:port 发一字节,1s 内有回包则退出0(可达),否则退出1。
用于 restart-all.sh 判定 QoSModule auto 模式是否回退到 ran-udp(udp 端点生效)。"""
import socket
import sys


def main():
    if len(sys.argv) < 2 or ":" not in sys.argv[1]:
        sys.exit(2)
    host, port = sys.argv[1].rsplit(":", 1)
    s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    s.settimeout(1)
    try:
        s.sendto(b"ping", (host, int(port)))
        data, _ = s.recvfrom(64)
        sys.exit(0 if data else 1)
    except Exception:
        sys.exit(1)
    finally:
        s.close()


if __name__ == "__main__":
    main()
