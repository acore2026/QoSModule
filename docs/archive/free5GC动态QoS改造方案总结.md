# free5GC 动态 QoS 改造方案总结

> 归档状态：实施前方案比较。真实联调已经证明 AF/PCF 路径受 SMF 问题阻塞，当前推荐路径改为 SMF 外挂。

## 1. 当前目标

当前项目的目标是让 MASQUE Proxy 上报的视频业务突发信息进入 QoS 模块，生成动态 QoS 参数，并最终让核心网和 RAN 侧生效。

当前完整目标链路为：

```text
MASQUE Proxy
  -> QoS 模块
  -> free5GC SMF
  -> UPF + AMF
  -> RAN/gNB
  -> UE
```

其中 QoS 模块负责：

- 接收 MASQUE Proxy 通过 UDP 转发的 QoS 协同请求
- 校验 `request_id`、`rnti`、`qfi`、`burst_info`、`service_info.e2e_delay`
- 根据突发数据量、突发时长和端到端时延预算计算 MBR、GBR、PDB、Priority
- 根据不同下发模式调用不同 Enforcer
- 将最终处理结果通过 UDP 回给 MASQUE Proxy

## 2. 当前已有能力

### 2.1 MASQUE 接收接口

QoS target 当前通过普通 UDP 接收 MASQUE Proxy 转发的业务消息。

默认监听：

```text
UDP 0.0.0.0:7400
```

Proxy 到 target 的普通消息格式：

```text
CLIENT-IP: <client_ip>\r\n
\r\n
<QoS JSON>
```

也支持可靠重传信封：

```json
{
  "type": "request",
  "request_id": "retry-req-001",
  "payload": "<base64编码后的QoS JSON>"
}
```

### 2.2 QoS 计算

当前 QoS 计算不查询用户原有 QoS，不做 PFCP modify，直接根据 MASQUE 请求中的突发参数计算目标值。

主要计算逻辑：

```text
MBR = burst_size * 8000 / burst_duration
GBR = burst_size * 8000 / transit_delay
PDB = e2e_delay * 0.625
Priority = 3
```

如果请求没有携带真实传输时延，则使用：

```text
transit_delay = e2e_delay * transit_ratio
```

默认 `transit_ratio = 0.8`。

### 2.3 gNB-HTTP 下发

当前已有 `ranapi.Client`，通过 HTTP POST 下发 RAN QoS 更新：

```text
POST /api/v1/qos/update
```

适用于支持该 HTTP API 的开放式或软件 gNB。

### 2.4 Mock RAN

当前已经新增 Mock RAN，用于没有真实 RAN 时联调：

```bash
cd target/target

go run ./cmd/mockran \
  -b 127.0.0.1:8080 \
  -path /api/v1/qos/update \
  -status ACCEPTED \
  -message "mock ran accepted"
```

target 指向 Mock RAN：

```bash
go run ./cmd/target \
  -mode qos \
  -b 0.0.0.0:7400 \
  -ran-url http://127.0.0.1:8080/api/v1/qos/update
```

联调链路：

```text
MASQUE Proxy -> UDP -> target -> HTTP POST -> mockran -> target -> UDP response -> MASQUE Proxy
```

该路径可以验证 MASQUE Proxy 到 QoS 模块的收包、字段校验、动态 QoS 计算、RAN 请求构造、失败返回和重传缓存。

## 3. 当前问题

当前封闭厂商基站不支持：

```text
POST /api/v1/qos/update
```

因此原有 gNB-HTTP 路径不能直接用于这台基站。

已确认的限制包括：

- 端口 80 只是默认 Apache 页面，无 QoS API
- OAM CGI 是小区级静态配置，不适合每 UE、每 QFI 实时动态 QoS
- `rate.cgi` 只能读取速率，不能下发策略
- DU 内部接口是厂商私有二进制协议，直连风险高
- 当前标准可用路径是 NGAP，经 AMF 到 gNB

所以对当前基站，应走核心网控制面：

```text
QoS 模块 -> SMF -> AMF -> RAN
```

## 4. 为什么不能只走 UPF

UPF 可以被配置 QoS，但 UPF 不能直接给 RAN 下发 QoS。

UPF 能处理：

- PDR
- FAR
- QER
- URR
- 用户面门控和速率控制

UPF 不能直接处理：

- gNB 侧 QoS Flow 修改
- RAN DRB/QoS Flow 映射
- `PDUSessionResourceModifyRequest`
- UE 侧 QoS rule 同步
- NGAP 控制面下发

因此只改 UPF 的链路最多是：

```text
QoS 模块 -> UPF QER 更新
```

这只能让核心网用户面部分生效，RAN 侧不会同步感知 QoS 变化。对于视频传输场景，如果瓶颈在空口调度，该方案效果不完整。

完整方案应由 SMF 统一协调：

```text
QoS 模块 -> SMF
              ├-> UPF：PFCP Session Modification / QER
              └-> AMF：N1N2 SM 信息
                    └-> RAN：NGAP PDU Session Resource Modify
```

## 5. source IP 到 SUPI 的方案

如果使用 free5GC，可以使用 UE source IP 作为核心网侧寻址 key。

推荐转换关系：

```text
source_ip / UE IP
  -> PDU Session
  -> SUPI + pduSessionId
  -> QoS Flow Modify
```

原因是 UE IP 由 SMF 管理，并用于 PDU Session 和 UPF PDR 匹配。SMF 内部天然持有：

```text
UE IP <-> PDU Session <-> SUPI <-> pduSessionId
```

不推荐使用：

```text
rnti -> supi
```

原因是 RNTI 属于 gNB/空口内部标识，核心网侧通常没有 C-RNTI 到 SUPI 的直接映射。AMF/SMF 不认 C-RNTI，只有 gNB 内部知道 RNTI 和 RAN UE 上下文的关系。

因此 MASQUE Proxy 应尽量携带真实 UE IP：

```json
{
  "request_id": "req-001",
  "source_address": "10.60.0.2",
  "rnti": 4660,
  "qfi": 9,
  "burst_info": {
    "ul_burst_size": 120000,
    "ul_burst_duration": 100
  },
  "service_info": {
    "service_type": "videostreaming",
    "e2e_delay": 100
  }
}
```

字段用途：

| 字段 | 用途 |
|---|---|
| `source_address` / `CLIENT-IP` | free5GC SMF 侧查 PDU Session、SUPI、pduSessionId |
| `rnti` | gNB-HTTP 路径使用，当前封闭基站的 NGAP 路径不依赖 |
| `qfi` | QoS Flow 标识，SMF/RAN 路径仍需要 |
| `burst_info` | 动态计算 MBR/GBR |
| `service_info.e2e_delay` | 动态计算 PDB 和 transit delay fallback |

## 6. 是否需要修改 free5GC

如果选择完整的 free5GC 下发路径，通常需要修改或扩展 SMF。

### 6.1 QoS 模块需要新增

QoS 模块需要新增 SMF Enforcer：

```text
adaptiveqos/smfenforcer
```

职责：

- 接收 QoS 模块计算后的 `Decision`
- 根据 `Intent.Flow.UEAddress` 调 SMF 动态 QoS 接口
- 将 MBR、GBR、PDB、Priority、QFI、5QI/ARP 等转换为 SMF 需要的格式
- 接收 SMF 处理结果
- 转换为 MASQUE 侧响应

### 6.2 SMF 需要新增

SMF 需要提供一个外部动态 QoS 修改入口，例如：

```text
POST /qos/v1/update
```

建议请求内容：

```json
{
  "request_id": "req-001",
  "ue_ip": "10.60.0.2",
  "qfi": 9,
  "five_qi": 2,
  "mbr_ul": 9600000,
  "mbr_dl": 24000000,
  "gbr_ul": 100000,
  "gbr_dl": 100000,
  "pdb": 75,
  "arp": {
    "priority": 3,
    "preempt_cap": true,
    "preempt_vuln": false
  }
}
```

SMF 收到后需要：

1. 根据 `ue_ip` 找到 SM Context / PDU Session
2. 确认 SUPI、pduSessionId、DNN、S-NSSAI
3. 更新或新增 QoS Flow
4. 生成或更新 UPF 侧 PFCP QER
5. 生成 N1/N2 SM 信息
6. 通过 AMF 触发 PDU Session Resource Modify
7. 返回处理结果给 QoS 模块

### 6.3 AMF 是否需要改

AMF 尽量不改。

理想情况下，SMF 复用 free5GC 已有的 N1N2 通知流程，把 N2 SM information 交给 AMF，AMF 继续负责 NGAP 下发到 RAN。

### 6.4 UPF 是否需要改

UPF 尽量不改。

完整方案下，UPF 由 SMF 通过 PFCP Session Modification 更新 QER。UPF 只执行 SMF 下发的用户面策略。

只有在选择“QoS 模块直接控制 UPF”的非完整方案时，才需要改 UPF 或使用 UPF 原生控制接口。

### 6.5 PCF 是否需要改

短期方案可以不改 PCF。

长期更标准的方案是：

```text
QoS 模块作为 AF -> PCF Policy Authorization -> SMF -> AMF/UPF/RAN
```

该方案标准性更好，但链路更长，实现和联调成本更高。

## 7. 推荐双模架构

为了兼容不同基站，建议保留双模：

```text
MASQUE Proxy
  -> QoS target
  -> Processor
  -> RouterEnforcer
        ├-> ranapi.Client：gNB-HTTP 下发
        └-> smfenforcer：free5GC/SMF/NGAP 下发
```

模式配置：

| mode | 行为 | 适用场景 |
|---|---|---|
| `ran` | 只走 gNB-HTTP | 支持 `/api/v1/qos/update` 的开放基站 |
| `ngap` | 只走 SMF/NGAP | 当前封闭厂商基站 |
| `auto` | 优先 HTTP，失败后 fallback 到 SMF/NGAP | 基站能力不确定时 |

## 8. 推荐落地顺序

### 阶段 1：MASQUE 联调

目标：不依赖真实 RAN，先打通 MASQUE Proxy 和 QoS 模块。

链路：

```text
MASQUE Proxy -> target -> Mock RAN -> target -> MASQUE Proxy
```

验证内容：

- UDP 接收
- `CLIENT-IP` 解析
- QoS JSON 校验
- 动态 QoS 计算
- RAN HTTP 请求体构造
- MASQUE 响应回包
- 可靠重传和重复 request 缓存

### 阶段 2：SMF Mock 联调

目标：在不修改真实 free5GC 的情况下，先定义 QoS 模块到 SMF 的接口。

链路：

```text
MASQUE Proxy -> target -> Mock SMF -> target -> MASQUE Proxy
```

验证内容：

- `source_address` 是否是真实 UE IP
- SMF Enforcer 请求体是否完整
- `qfi`、MBR、GBR、PDB、5QI、ARP 是否转换正确
- SMF 失败/超时是否能正确回 MASQUE

### 阶段 3：free5GC SMF 改造

目标：在 SMF 中实现动态 QoS 修改入口。

重点：

- UE IP 到 PDU Session 的查找
- QoS Flow 新增或更新
- PFCP QER 更新
- N1N2 SM 信息生成
- AMF/RAN NGAP 下发
- 返回处理结果

### 阶段 4：真实 RAN 联调

目标：验证 QoS 是否真正下发到基站并影响视频业务。

重点：

- RAN 是否收到 PDU Session Resource Modify
- QoS Flow 是否生效
- UPF 侧 QER 是否同步
- 视频码率、时延、丢包、MOS 等指标是否变化
- 失败路径是否可定位

## 9. 当前推荐结论

短期：

```text
先用 Mock RAN 完成 MASQUE Proxy 与 QoS 模块联调。
```

中期：

```text
新增 SMF Enforcer，并给 free5GC SMF 增加动态 QoS 修改 API。
```

长期：

```text
考虑将 QoS 模块作为 AF，通过 PCF Policy Authorization 走更标准的策略链路。
```

最终推荐路径：

```text
MASQUE Proxy
  -> QoS 模块
  -> SMF
  -> UPF + AMF
  -> RAN
```

不推荐路径：

```text
QoS 模块 -> UPF -> RAN
```

原因是 UPF 不负责 NGAP，也不能直接修改 RAN 侧 QoS Flow。
