# QoSModule

QoSModule 接收 MASQUE Proxy 转发的 UDP QoS 请求，将业务突发需求转换为统一 `Intent`，计算 MBR、GBR、PDB 和优先级，再通过可替换的 Enforcer 下发。

## 当前状态

更新时间：2026-09-10。

| 能力 | 状态 | 说明 |
| --- | --- | --- |
| MASQUE UDP Target | 已实现 | 解析 `CLIENT-IP`，支持可靠信封、去重缓存和原路回包 |
| QoS 请求校验与策略计算 | 已实现 | UL 必选、DL 成对可选，按静态范围裁剪 |
| gNB HTTP 下发 | 已实现 | `ranapi.Client` 调用 `POST /api/v1/qos/update`，当前部署指向本地 mock-ran |
| gNB UDP 下发 | 已实现 | `udpranenforcer` 调用远程基站 `10.88.0.3:9999`，可选等 ack |
| mock-ran 自报前端 | 已实现 | `ranreporter/mock_ran.py` 内置 pusher 线程，模拟空口状态机并直接 POST 前端 |
| SMF 外挂下发（方案 A） | 已实现但已废弃 | `smfenforcer` 代码与测试保留，部署脚本不再提供 `ngap` 入口 |
| AF/PCF 下发（方案 B） | 已移除 | 原 `afenforcer` 因 free5GC PCF/SMF 链路 panic/重复 URR 不通，已删除，由方案 A 取代 |
| 中间采集器 collector.py | 已移除 | 上报责任下放到下发目标自己；原 SSH→odi tracebuff 采集链路（含 `10.88.120.212`）整体退役 |
| 直接发送 NGAP | 未实现，也不应由本模块直接实现 | NGAP 应由 AMF 向 gNB 发送 |
| GTP-U 自定义扩展头传 QoS JSON | 未实现且不属于当前方案 | 当前基站通过标准 NGAP 接收 QoS 修改 |

当前**部署脚本**（`scripts/start-qos.sh`、`/home/core/restart-all.sh`）只暴露两种模式，二选一：

- `ran-udp`：UDP 直连远程基站（默认 `10.88.0.3:9999`），下发后由**远程基站自己上报前端**。
- `mock-ran`：HTTP 直连本地 mock-ran（`127.0.0.1:18081`），下发后由 **mock-ran 自己上报前端**。

Go 侧 `routerenforcer` 仍保留 `ran`/`ran-udp`/`ngap`/`auto` 四种 Mode（`router_test.go` 有 7 个 auto 测试覆盖），其中只有 `ran-udp` 是当前生产模式，其余三种已退役且部署脚本不再提供入口：

| 已退役模式 | 退役原因 |
| --- | --- |
| `ran` | 脚本默认目标 `10.88.120.212:80` 已下线。注意 `mock-ran` 模式内部仍用 `-core-mode ran`，只是 `-ran-url` 指向本地 mock——不存在 `-core-mode mock-ran` |
| `ngap` / SMF 外挂 | 方案 A 已废弃 |
| `auto` | 三档回退会让远程基站与 mock-ran **两个上报源同时活着**，而前端 schema 无数据源标识字段，两源同推会画出无法区分合并的交织曲线。另外 auto 逐请求同步重试、无状态记忆、无熔断，第 1 档 UDP 不通时每请求都要先吃满 `-ran-timeout`（默认 3s）空等才回退，单请求 ≈3s 而 mock-ran 那步只占 ~2ms（0.07%） |

### 上报机制

**上报责任属于下发目标自己，本机不跑任何中间采集器。**

| 模式 | 下发 | 上报 |
| --- | --- | --- |
| `ran-udp` | QoSModule → UDP `10.88.0.3:9999` | 远程基站自己 POST 前端 |
| `mock-ran` | QoSModule → HTTP `127.0.0.1:18081/api/v1/qos/update` | mock-ran 进程自己 POST 前端 |

前端契约（`POST {FRONTEND_URL}`，默认 `http://192.168.1.10:28448/api/v1/qos`）：

```json
{"metrics": [{"timestamp": 1720000000000, "sendrate_kbps": 4900, "gbr_kbps": 5459, "q_lvl": 3}]}
```

成功 `200 {"ok":true,"type":"metrics"}`；非法 `400 {"error":"..."}`（字段缺失/类型错/空 metrics 数组）。

mock-ran 的推送策略（`ranreporter/mock_ran.py` 的 `_push_loop`）：

- 窗口每 `--interval`（0.5s）**无条件累积**，保证 burst 到来时窗口里已有 ~15s 基线段，前端能画出完整梯形
- 仅在 `active(alive)` 或 `state_changed(q_lvl/gbr/alive 变了)` 或 `in_tail`，且距上次推送 ≥ `--push-interval`（1s）时，POST **整窗**（`--window`，默认 30 条）
- burst 结束（`alive` True→False）后继续推 `--tail-secs`（8s）填满前端窗口右侧的恢复段，再进入空闲静默
- 出站用空 `ProxyHandler`：`urllib` 默认继承 shell 的 `http_proxy`，代理 IP 失效会导致 POST 全部超时
- `--frontend-url` 留空则关闭自报，mock-ran 退化为纯模拟器（仅 `/metrics` 被动拉取）

mock-ran 模拟的空口行为：IDLE 基线 `1500±N(0,60)` kbps；下发后 `RAMP_UP`（0~0.5s 渐升）→ `STEADY`（`GBR×0.9 ±N(0,30)`）→ `RAMP_DOWN`（burst 结束前 0.3s 渐降）→ 到 `burst_ms` 自动释放回 IDLE；`GBR=0`（非 GBR 5QI）全程维持基线。

## 代码结构

```text
adaptiveqos/                     传输无关的策略模块
├── model.go                     Intent、Decision、Limits、Enforcer 接口
├── policy.go                    BurstPolicy 动态 QoS 计算
├── processor.go                 查询范围、计算、下发的编排
├── masqueapi/                   MASQUE JSON 适配器
├── ranapi/                      gNB HTTP Enforcer
├── smfenforcer/                 SMF OAM Enforcer (方案 A)
└── routerenforcer/              ran、ngap、auto 路由

target/target/                   MASQUE 后端 UDP 服务
├── server.go                    UDP 收发和 CLIENT-IP 解析
├── reliability.go               可靠信封和请求去重
├── qos_handler.go               请求到 Processor 的业务入口
└── cmd/
    ├── target/                  正式 Target
    ├── mockran/                 Mock RAN
    └── mockpcf/                 Mock PCF

ranreporter/                     基站模拟与前端联调 (Python)
├── mock_ran.py                  模拟 gNB: 收 QoS 下发 + 空口状态机 + 自报前端
└── mock_frontend.py             前端接收端 mock (联调用, 带 sendrate/gbr 实时双曲线图)
```

`target_backup_20260803-200912/` 是旧 Target 快照，不是当前运行入口。

### mock-ran 与前端联调

`ranreporter/mock_frontend.py` 是前端契约的本地实现（`POST /api/v1/qos` 校验 + `GET /` 实时双曲线图 + `GET /samples` 拉环形缓冲），用于不起真前端时验证 mock-ran 自报：

```bash
python3 ranreporter/mock_frontend.py --port 28555
python3 ranreporter/mock_ran.py --port 18099 --frontend-url http://127.0.0.1:28555/api/v1/qos
# 另开一个终端触发一次下发, 浏览器打开 http://127.0.0.1:28555/ 看梯形曲线
curl -X POST -H 'Content-Type: application/json' \
  -d '{"request_id":"t1","rnti":1,"q_lvl":3,"q_gbr_ul":5459,"burst_info":{"ul_burst_duration":8000}}' \
  http://127.0.0.1:18099/api/v1/qos/update
```

## 当前文档

| 文档 | 用途 |
| --- | --- |
| [随路 QoS 设计文档](随路Qos设计文档.md) | MASQUE 请求、策略计算和 gNB HTTP 协议参考；HTTP 章节只适用于支持该接口的 gNB |
| [Target README](target/target/README.md) | UDP 协议、运行参数和 Mock 联调方法 |
| [adaptive-qos README](adaptiveqos/README.md) | 共享策略模块及适配器边界 |

历史方案、失败实验和已废弃路径（方案 A SMF 外挂、基站侧 NGAP 需求等）统一位于 [docs/archive](docs/archive/README.md)，不能作为当前部署依据。

## 快速验证

```bash
cd adaptiveqos
go test ./...

cd ../target/target
go test ./...
```

没有真实 RAN 时，可在 Windows 仓库根目录运行：

```powershell
.\scripts\start-windows-mock-test.ps1
```

## 下一步

1. 决定 `smfenforcer` 与 `routerenforcer` 的 `ngap`/`auto` 模式去留：方案 A 已废弃、部署脚本不再提供 `ngap` 入口，但代码和测试仍在仓库中保留，应明确删除或标注 `Deprecated`。
2. 清理死代码：`target_backup_20260803-200912/`（与在用 `target/target/` 同名模块 `masque-target` 的旧快照）、`target/target/cmd/mockpcf`（方案 B 遗留，无测试）、`logs/*.bak` 历史日志。
3. 原计划「统一 Enforcer 返回值为 `request_id/status/error_code/message`」已由 `adaptiveqos.ApplyResult` 完成（见 `model.go`），不再单列。
