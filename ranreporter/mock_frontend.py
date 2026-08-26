#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""前端接收端 mock: 实现规范 POST /api/v1/qos -> {"ok":true,"type":"metrics"} 或 400。
并增配 GET / 提供 sendrate/gbr 双曲线实时图(纯 canvas, 无外部依赖), GET /samples 拉环形缓冲。
用于联调采集器与 mock-ran 本地闭环。"""
import json
import time
import argparse
from collections import deque
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

VALID_KEYS = {"timestamp", "sendrate_kbps", "gbr_kbps", "q_lvl"}
SAMPLES = deque(maxlen=600)  # ~5 分钟 @0.5s
SEEN_TS = set()  # 已存入 SAMPLES 的 timestamp, 用于去重(collector 每次推整窗会重叠)

DEFAULT_PORT = 28448

HTML_PAGE = """<!DOCTYPE html>
<html><head><meta charset="utf-8"><title>QoS 保障曲线</title>
<style>
body{font-family:monospace;margin:12px;background:#1e1e1e;color:#ddd}
h3{margin:4px 0}
#hud{font-size:13px;margin-bottom:6px}
canvas{background:#111;border:1px solid #333;display:block}
.legend span{display:inline-block;margin-right:14px}
</style></head>
<body>
<h3>QoS 保障曲线 (sendrate vs gbr)</h3>
<div id="hud">等待数据...</div>
<div class="legend">
  <span style="color:#4af">■ sendrate_kbps</span>
  <span style="color:#fa3">■ gbr_kbps</span>
  <span style="color:#9f9">■ q_lvl</span>
</div>
<canvas id="c" width="900" height="360"></canvas>
<script>
const cv=document.getElementById('c'),ctx=cv.getContext('2d');
const hud=document.getElementById('hud');
async function poll(){
  try{
    const r=await fetch('/samples');
    const j=await r.json();
    draw(j.samples||[]);
  }catch(e){hud.textContent='fetch err: '+e}
}
function draw(s){
  ctx.clearRect(0,0,cv.width,cv.height);
  if(!s.length){hud.textContent='(暂无样本)';return;}
  s.sort((a,b)=>a.timestamp-b.timestamp);
  // X 轴锚定最后一个样本的时间戳(不是 Date.now()): burst 结束后 collector 进入
  // tail(继续推 8s 填满右侧)再冻结, last_ts 停在 burst 后 8s, 图表随之冻结。
  // 窗口 24s(=8s burst 的三倍): burst 跑完整窗口再冻, 前后各 8s 留白/恢复段, burst 居中。
  const last_ts=s[s.length-1].timestamp;
  const tmin=last_ts-24000, tmax=last_ts, span=tmax-tmin;
  const data=s.filter(d=>d.timestamp>=tmin && d.timestamp<=tmax);
  if(!data.length){hud.textContent='(窗口内暂无样本, 已有 '+s.length+')';return;}
  // Y 范围: sendrate/gbr 最大值, 20% 顶部留白, 上取整到 100
  let yv=0;
  for(const d of data){yv=Math.max(yv,d.sendrate_kbps,d.gbr_kbps);}
  let ytop=Math.max(Math.ceil(yv*1.2/100)*100, 1000);
  const padL=50,padR=20,padT=16,padB=28;
  const W=cv.width-padL-padR, H=cv.height-padT-padB;
  function xpx(t){return padL+W*(t-tmin)/span;}
  function ypx(v){return padT+H-(v*H/ytop);}
  // 网格 + Y 轴刻度
  ctx.strokeStyle='#333';ctx.fillStyle='#888';ctx.font='11px monospace';ctx.textAlign='right';
  for(let v=0;v<=ytop;v+=Math.max(200,Math.floor(ytop/5))){
    ctx.beginPath();ctx.moveTo(padL,ypx(v));ctx.lineTo(padL+W,ypx(v));ctx.stroke();
    ctx.fillText(v+'',padL-4,ypx(v)+3);
  }
  // X 轴时间标签(相对最后样本: 0=末样本, 负数=之前秒, 正数=之后留白)
  ctx.textAlign='center';
  const xsteps=8;
  for(let i=0;i<=xsteps;i++){
    const t=tmin+span*i/xsteps, x=xpx(t);
    ctx.beginPath();ctx.moveTo(x,padT);ctx.lineTo(x,padT+H);ctx.strokeStyle='#222';ctx.stroke();
    ctx.fillText(((t-last_ts)/1000).toFixed(0)+'s',x,padT+H+14);
  }
  // sendrate 线 (蓝)
  ctx.strokeStyle='#4af';ctx.lineWidth=1.6;ctx.beginPath();
  for(let i=0;i<data.length;i++){const d=data[i];const x=xpx(d.timestamp),y=ypx(d.sendrate_kbps);if(i===0)ctx.moveTo(x,y);else ctx.lineTo(x,y);}
  ctx.stroke();
  // gbr 阶梯线 (橙)
  ctx.strokeStyle='#fa3';ctx.lineWidth=1.8;ctx.beginPath();
  for(let i=0;i<data.length;i++){const d=data[i];const x=xpx(d.timestamp),y=ypx(d.gbr_kbps);if(i===0)ctx.moveTo(x,y);else ctx.lineTo(x,y);}
  ctx.stroke();
  // q_lvl 点 (绿, 右轴 0-15)
  ctx.fillStyle='#9f9';
  for(let i=0;i<data.length;i++){const d=data[i];const x=xpx(d.timestamp);const y=padT+H-H*(d.q_lvl/15);ctx.fillRect(x-1,y-1,2,2);}
  // HUD
  const last=data[data.length-1];
  hud.textContent='sendrate='+last.sendrate_kbps+'kbps  gbr='+last.gbr_kbps+'kbps  q_lvl='+last.q_lvl+'  win=24s(锚定末样本)  samples='+s.length;
}
setInterval(poll,500);
poll();
</script>
</body></html>
"""


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
        # 按 timestamp 去重存入环形缓冲: collector 每次推整个滑动窗口(30条),
        # 相邻推送窗口重叠, 若不去重会导致同时刻样本被重复写入, 曲线错乱。
        new_n = 0
        for m in metrics:
            ts = m["timestamp"]
            if ts in SEEN_TS:
                continue
            SAMPLES.append({
                "timestamp": ts,
                "sendrate_kbps": m["sendrate_kbps"],
                "gbr_kbps": m["gbr_kbps"],
                "q_lvl": m["q_lvl"],
            })
            SEEN_TS.add(ts)
            new_n += 1
        # deque maxlen 淘汰了旧样本后 SEEN_TS 仍残留其 ts, 周期性重建避免无限增长
        if len(SEEN_TS) > len(SAMPLES) * 2:
            SEEN_TS.clear()
            SEEN_TS.update(s["timestamp"] for s in SAMPLES)
        self._ok(metrics, new_n)

    def do_GET(self):
        if self.path == "/":
            self._send(200, HTML_PAGE, "text/html; charset=utf-8")
        elif self.path == "/samples":
            # 按 timestamp 排序输出, 保证绘图 X 轴单调递增
            ordered = sorted(SAMPLES, key=lambda s: s["timestamp"])
            self._send(200, json.dumps({"samples": ordered}),
                       "application/json")
        else:
            self._err(404, "not found")

    def _ok(self, metrics, new_n=0):
        resp = json.dumps({"ok": True, "type": "metrics"}).encode()
        self._send(200, resp, "application/json")
        print("[recv] %d samples (new=%d buf=%d)" % (len(metrics), new_n, len(SAMPLES)), flush=True)

    def _err(self, code, msg):
        resp = json.dumps({"error": msg}).encode()
        self._send(code, resp, "application/json")
        print("[reject %d] %s" % (code, msg), flush=True)

    def _send(self, code, body, ctype):
        if isinstance(body, str):
            body = body.encode()
        self.send_response(code)
        self.send_header("Content-Type", ctype)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *a):
        pass


if __name__ == "__main__":
    ap = argparse.ArgumentParser()
    ap.add_argument("--port", type=int, default=DEFAULT_PORT, help="监听端口(默认 %d)" % DEFAULT_PORT)
    ap.add_argument("--host", default="0.0.0.0", help="监听地址(默认 0.0.0.0)")
    args = ap.parse_args()
    listen = (args.host, args.port)
    print("frontend mock listening on %s:%d (GET / for chart, POST /api/v1/qos, GET /samples)"
          % (listen[0], listen[1]), flush=True)
    ThreadingHTTPServer(listen, H).serve_forever()
