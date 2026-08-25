# gNB 实时指标上报方案

| 项目 | 内容 |
| --- | --- |
| 文档版本 | V1.0 |
| 更新日期 | 2026-08-17 |
| 适用基站 | SBR61232 真实小基站(`10.88.120.212`,aarch64,内核 `4.19.68-..._N78_...`) |
| 上报目标 | 前端接收端 `POST http://127.0.0.1:28448/api/v1/qos` |
| 采集器 | `collector.py`(跑在核心机,SSH 到基站取数) |
| 采集源 | `odi -n duapp0 display-tracebuff 1`(L2/RLC 实时 trace buffer) |

---

## 1. 背景与目标

前端需要展示基站的**实时真实空口情况**:`sendrate`(上下行吞吐)、`gbr`(保证比特率)、`q_lvl`(QoS 等级/5QI)。经核查,该厂商基站的 OAM HTTP 接口(80/8400 端口 CGI)**不提供实时数据**——只有静态配置、5 分钟粒度的离线 PM 文件、且 Web 登录带图形验证码。唯一实时数据源是 L2 进程 `duapp0` 内存中的 trace buffer,通过厂商调试 CLI `odi` 读取,粒度到 TTI(1ms)。

本方案在基站外构建一个采集器,把 trace buffer 解析为前端要求的 JSON 指标并持续上报。

---

## 2. 上报接口规范(前端侧)

### 2.1 请求

```
POST http://127.0.0.1:28448/api/v1/qos
Content-Type: application/json
```

### 2.2 请求体

```json
{
  "metrics": [
    {"timestamp": 1720000000000, "sendrate_kbps": 920,  "gbr_kbps": 1000, "q_lvl": 3},
    {"timestamp": 1720000001000, "sendrate_kbps": 1100, "gbr_kbps": 1000, "q_lvl": 4}
  ]
}
```

| 字段 | 类型 | 含义 | 单位/取值 |
| --- | --- | --- | --- |
| `timestamp` | int | 毫秒级 epoch | 采集时刻 |
| `sendrate_kbps` | int | 空口上下行合计吞吐 | kbps |
| `gbr_kbps` | int | 活跃流的 GFBR 保证速率 | kbps(非 GBR 流为 0) |
| `q_lvl` | int | 活跃流的 5QI | 1-9, 65-67, 75, 79, 80, 82 等 |

### 2.3 响应

- 成功:`200 {"ok": true, "type": "metrics"}`
- 非法:`400 {"error": "..."}`(字段缺失/类型错/空 metrics 数组等)

---

## 3. 数据源:`odi display-tracebuff`

### 3.1 odi 调试接口

`odi` 是基站厂商的在线调试 CLI,经 ODS Name Server(`127.0.0.1:26999`)定位目标进程后发送调试命令。注册的进程:

| 进程 | 端口 | 角色 |
| --- | --- | --- |
| `duapp0` | 49839 | L2/RLC/MAC(实时空口数据所在) |
| `cpcellapp0` | 44735 | 呼叫处理 |
| `cpgnbapp` | 44503 | gNB 控制面 |
| `upapp` | 33990 | 用户面(GTP-U) |
| `oamProcess` | 49961 | OAM |

实测只有 `duapp0` 注册了可用 verb:`display-tracebuff`,其余 verb 全部 `not registered`。`upapp`/`cpcellapp0`/`cpgnbapp` 的 tracebuff 为空。

### 3.2 tracebuff 结构

`odi -n duapp0 display-tracebuff 1` 返回 ~39KB、约 300 行。**每个 trace tag 一行汇总**(非逐事件),字段:

```
TAG       count   1st_tti   latest_tti   文件名(行号)   消息模板(msgstr)
UPT_454   5505355 26763745  1010401424   RlcTxDataPduUn[1557]  AMDL::dlDataPduReq() ... in_macTbSzBytes=18
```

- `count`:该 tag 自 trace 启动以来的累计触发次数
- `1st_tti` / `latest_tti`:首末次触发的 TTI 序号(1ms 粒度)
- `msgstr`:末次触发的消息模板(含各参数的末次值)

> 关键限制:tracebuff 只保留每个 tag 的**末次消息**,不是逐事件流。因此所有速率计算都基于"两次采样间的累计 count 增量 × 末次字节值",稳态准确,突发会滞后 1 拍。

---

## 4. 三项指标的关联映射

### 4.1 sendrate_kbps(实时吞吐)

从字节承载 tag 的累计 count 增量计算:

| 方向 | trace tag | msgstr 中的字节字段 |
| --- | --- | --- |
| DL | `UPT_454` `dlDataPduReq` | `in_macTbSzBytes` |
| DL | `UPT_410` `dlBuildPDUs` | `byteScheduled` |
| UL | `UPT_1100` `uplinkPduPut` | `pduSzBytes` |
| UL | `UPT_563` `AM UL SDU Tx` | `sduSz` |

算式:
```
Δcount = count(本采样) - count(上采样)
total_bytes += Δcount × 末次字节值
sendrate_kbps = total_bytes × 8 / Δt / 1000
```
匹配按 msgstr 内容(`dlDataPduReq`/`uplinkPduPut` 等)而非 tag 编号,兼容固件版本变化。

### 4.2 q_lvl(5QI)

从 DRB 建立 tag 取最近一次承载流的 QCI:

```
UPT_4615  createBearerFlow: cellId=0, Qci=5, CRNTI=1725, LCID=4, IsHoCreate=0
                                                              ↑ q_lvl
```
- 忽略 `QCI=255`(信令无线承载 SRB,`configureFlow` tag)。
- `q_lvl` = 最近一次 `createBearerFlow` 的 `Qci`。

### 4.3 gbr_kbps(GFBR)

从调度器建流 tag 的 AMBR 信息取:

```
UPT_3242  SCHED_setupFlow: Create Flow with AMBR info Dl=[0,0], Ul=[800000000,48828].
                                                              ↑     ↑
                                                             MBR   GBR
```
- 格式 `Dl=[MBR, GBR], Ul=[MBR, GBR]`(按观测推断,无厂商文档背书)。
- 取 `max(dl_gbr, ul_gbr)` bps → 转 kbps。
- **仅 GBR 类型 5QI 才上报 GFBR**;非 GBR 流(5/6/7/8/9 等)的 AMBR 是最大速率而非保证速率,`gbr_kbps=0`。

### 4.4 GBR 类型判定

基站 confdb 的 5QI 表**不含** GBR/NonGBR 类型字段(只有 `ULMaxAccumGFBRPerDrb`=2Gbps 的全局上限),因此按 **3GPP TS 23.501 标准 5QI 集合**判定:

- GBR 5QI:`{1, 2, 3, 4, 65, 66, 67, 71, 72, 73, 74, 75, 76, 79, 80, 82, 83, 84}`
- 非 GBR:`{5, 6, 7, 8, 9, 69, 70, 77, 78, 81, ...}` 以及厂商自定义(`60, 100, 0`)

---

## 5. 流存活判定

tracebuff 只保留末次消息,无法遍历所有建立/释放事件,故用**时间戳比较**判活:

```
T_create = 最近 createBearerFlow                的 latest_tti
T_release = max(releaseFlow / releaseAllFlowsForUe / releaseAllDrbForUe) 的 latest_tti

T_create >= T_release  → alive=True  → 上报该流 q_lvl 与 gbr
T_release > T_create   → alive=False → 上报默认值(q_lvl=9, gbr=0)
无 createBearerFlow     → alive=False → 默认值
```

判活为假时,关联的 `crnti` 仍记入采集器日志便于排障,但不进入上报指标。

---

## 6. 采集器架构

```
┌── gNB 10.88.120.212 ────────────────┐    ┌── 核心机 127.0.0.1 ──────────────────┐
│ duapp0  实时 trace buffer            │    │  collector.py(守护进程)              │
│   odi -n duapp0 display-tracebuff 1 │◄───│   SSH ControlMaster 复用(单次~20ms) │
│ confdb_v2.xml  5QI 配置表(启动读1次)│◄───│   解析: sendrate / q_lvl / gbr /判活 │
└──────────────────────────────────────┘    │   POST /api/v1/qos → 28448 前端      │
                                            └──────────────────────────────────────┘
```

- **采集器位置**:核心机(匹配前端 `127.0.0.1:28448`)。如需更高频率(如 0.1s/10Hz),可把采集器搬到基站本机直跑 `odi`(无 SSH),POST 到 `核心IP:28448`。
- **SSH 连接复用**:ControlMaster/ControlPersist,首次建 master,后续 ssh 复用,单次调用从 ~0.2s 降到 ~0.02s。**这是 0.5s 高频的前提**。
- **精确周期计时**:`next_t += interval; sleep(next_t - now)`,执行耗时不累计、无 drift。
- **解析匹配方式**:按 msgstr 关键字匹配(非 tag 编号),兼容固件升级。

---

## 7. 上报周期

| 周期 | 单次 SSH(复用) | 占周期 | 评估 |
| --- | --- | --- | --- |
| 0.5s(默认) | ~20ms | 4% | ✅ 稳定 |
| 0.3s | ~20ms | 7% | ✅ 可行 |
| 0.1s(10Hz) | ~20ms | 20% | ⚠ 紧,建议搬到基站本机 |
| 0.05s | ~20ms | 40% | ❌ 不建议(走 SSH) |

实测 0.5s 下 11 条样本的相邻时间戳差:平均 500ms,最小 482ms,最大 517ms,无 drift。

---

## 8. 使用方法

```bash
# 0.5s 持续上报(默认)
python3 collector.py

# 自定义周期
python3 collector.py --interval 0.3

# 单次联调(采一次并 POST)
python3 collector.py --once

# 覆盖 q_lvl / gbr(绕过自动关联,联调用)
python3 collector.py --once --q-lvl 2 --gbr-kbps 100

# 禁用 SSH 复用(降级,高频不建议)
python3 collector.py --no-mux

# 起前端 mock 联调接收端
python3 mock_frontend.py
```

采集器日志样例:
```
[12:08:57.281] ssh master 已建立: /tmp/ranreporter_ssh_mux_17028
[12:08:57.310] loop start: interval=0.50s target=http://127.0.0.1:28448/api/v1/qos
[12:08:57.849] ok alive=False crnti=1725 sendrate=0kbps gbr=0kbps qlvl=9
```

---

## 9. 已知局限与诚实声明

1. **sendrate 是估算值**:tracebuff 是累计计数 + 末次字节模板(非逐 PDU)。稳态准确;突发流量会滞后 1 个采样周期。精确逐 PDU 字节需厂商开放 L2 详细 trace(odi 仅 `0/1` 档,无法开启)。
2. **取最近一次 DRB 建立**:tracebuff 每 tag 仅末次消息,多流场景下取最新那条;`createBearerFlow` 与 `SCHED_setupFlow` 各自取最新,多流时偶有错配风险(非 GBR 已强制 gbr=0 规避)。
3. **AMBR `[MBR, GBR]` 槽位是推断**:无厂商文档背书,真实 GBR 流上线后建议对照 NGAP 下发值校准一次(参考 `基站侧随路QoS需求文档.md` 验证值:5QI=2,GFBR UL/DL=100kbps)。
4. **判活基于时间戳近似**:因 summary 限制无法遍历全部释放事件;用"最近建立 vs 最近释放"的 tti 比较,属最佳近似。
5. **MCS / CQI / PRB 利用率不可得**:这些是 MAC/PHY 层指标,而唯一注册的 odi verb 只有 RLC 层的 `display-tracebuff`;MAC/PHY 实时计数器厂商未通过 odi 暴露。

---

## 10. 文件清单

| 文件 | 说明 |
| --- | --- |
| `collector.py` | 采集器主程序:SSH→odi→解析→算指标→判活→POST |
| `mock_frontend.py` | 符合上报规范的接收端 mock(联调用) |

### collector.py 关键函数

| 函数 | 作用 |
| --- | --- |
| `open_ssh_master` / `close_ssh_master` | SSH ControlMaster 长连接管理 |
| `ssh_run` | 复用 master 执行远程命令 |
| `parse_trace` | 解析 tracebuff 文本为 `{tag: {count, first_tti, latest_tti, rest}}` |
| `compute_sendrate` | 字节 tag Δcount × 末次字节值 / Δt |
| `extract_qos` | 关联 q_lvl(5QI)、gbr(GFBR)、判活(alive)、CRNTI |
| `build_sample` | 组装单条 metrics 样本(含 override 优先逻辑) |
| `run_loop` / `run_once` | 持续循环 / 单次联调 |

---

## 11. 后续可选增强

- **搬到基站本机直跑 odi**:去 SSH 开销,支持 0.1s+ 高频。
- **per-UE / per-flow 上报**:前端 schema 增加字段,采集器按 `traceKey/flowId/crnti` 分组上报多条。
- **MAC/PHY 指标**:翻 `duapp` 二进制找隐藏 verb,或接 PHY 层 `fsmPhyAdapter`,补 MCS/CQI/PRB/BLER。
- **GBR 自动校准**:真实 GBR 流上线时,比对 NGAP 下发 GFBR 与 tracebuff 推断值,修正 AMBR 槽位假设。
