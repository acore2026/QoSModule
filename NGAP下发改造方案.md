# NGAP 下发改造方案

> 状态日期：2026-08-11。
>
> 当前结论：封闭厂商 gNB 不提供 `/api/v1/qos/update`，应由核心网通过标准 NGAP 下发。SMF 外挂路径已在外部 `acore2026/smf` 仓库完成端到端验证，且本 QoSModule 仓库已实现 `smfenforcer`（`adaptiveqos/smfenforcer/`）并接入 `ngap`/`auto` 模式，Target 现在可直接经该路径下发到 gNB。原 AF/PCF 路径（方案 B）已删除。

## 1. 目标与边界

QoSModule 负责接收 MASQUE 请求并计算动态 QoS。不同基站通过可替换的 Enforcer 使用不同下发路径：

| 路径 | 适用对象 | 当前状态 |
| --- | --- | --- |
| gNB HTTP | 支持私有 QoS HTTP API 的开放 gNB、Mock RAN | 本仓库已实现并测试 |
| ~~AF/PCF~~ | ~~支持完整 free5GC Policy Authorization 链路的部署~~ | 已删除：真实链路被 PCF/SMF 问题阻塞，由方案 A 取代 |
| SMF 外挂 | 当前封闭厂商 gNB | 本仓库 `smfenforcer` 已实现并接入 `ngap`/`auto`，端到端验证到 gNB 建 DRB |

NGAP 不由 QoSModule 或 UPF 直接发送。标准职责链是：

```text
QoSModule -> SMF -> AMF -> gNB
               |       |
               |       +-- NGAP PDU Session Resource Modify
               +-- PFCP Session Modification -> UPF/QER
```

## 2. 当前代码事实

### 2.1 已实现组件

| 组件 | 文件 | 行为 |
| --- | --- | --- |
| MASQUE 适配 | `adaptiveqos/masqueapi/request.go` | 校验请求并生成 `Intent` |
| 动态策略 | `adaptiveqos/policy.go` | 计算 MBR、GBR、PDB 和优先级 |
| gNB HTTP Enforcer | `adaptiveqos/ranapi/client.go` | 调用 `/api/v1/qos/update` |
| SMF OAM Enforcer | `adaptiveqos/smfenforcer/` | 构造 SMF OAM 请求（方案 A），调用 `/nsmf-oam/v1/qos-update` |
| 路由 | `adaptiveqos/routerenforcer/router.go` | 支持 `ran`、`ngap`、`auto` |
| Target 装配 | `target/target/qos_handler.go` | 将 Enforcer 装入共享 Processor |

代码中的 `ngap` 模式实际指向 SMF OAM Enforcer（方案 A），不表示 Target 直接编码或发送 NGAP。

### 2.2 尚未实现组件

本仓库不存在以下内容：

- SMF QoS Flow 释放请求（`/qos-update` 当前只 add 不 release）。
- N1 NAS QoS Rule 生成（当前只发 N2，UL 分类未覆盖）。
- `-core-mode smf` 独立模式名（当前 `ngap` 模式直接用 `smfenforcer`，未新增 `smf` 别名）。
- 通过 GTP-U 自定义扩展头向 gNB 传输 QoS JSON。

`adaptiveqos/smfenforcer/` 已实现并接入 `ngap`/`auto`；原 `adaptiveqos/afenforcer/`（AF/PCF）已删除。

因此，不能把外部 SMF 的成功记录表述成“当前 Target 已经完整支持真实基站”。

## 3. 当前基站能力结论

对当前封闭厂商 BBU 的实际检查结果：

| 能力 | 结果 | 结论 |
| --- | --- | --- |
| `POST /api/v1/qos/update` | 端口 80 返回 404 | 不可使用 gNB HTTP 路径 |
| OAM CGI | 提供小区/网络级静态配置 | 不适合 per-UE、per-QFI 实时修改 |
| DU 内部接口 | 厂商私有二进制协议 | 不作为项目集成接口 |
| NGAP SCTP 38412 | gNB 已与 AMF 建联 | 当前唯一合适的动态 QoS 控制路径 |
| PDU Session Resource Modify | 已处理成功 | 已验证可以建立 DRB |

当前基站应使用：

```text
QoSModule -> SMF -> AMF -> gNB
```

而不是：

```text
QoSModule -X-> gNB HTTP
UPF -X-> gNB 控制接口
GTP-U 扩展头 -X-> gNB 调度器 JSON
```

## 4. 三条路径对比

### 4.1 gNB HTTP 路径

```text
Target -> ranapi.Client -> POST /api/v1/qos/update -> gNB
```

特点：

- 已实现、已通过 Mock RAN 测试。
- 使用 `rnti + q_qfi` 定位 RAN 内部 QoS Flow。
- 可以携带 MBR、GBR、PDB、MCS、RB、BLER、smooth 和 burst 信息。
- 仅适用于明确实现该私有接口的 gNB。

### 4.2 ~~AF/PCF 路径~~（已删除）

原方案 B 路径 `Target -> afenforcer -> PCF -> SMF -> AMF -> gNB` 已随 `afenforcer` 包删除一并移除。历史验证记录：AF 请求符合真实 PCF `AppSessionContextReqData`、PCF 返回 201，但 stock SMF `ApplyPccRules` nil `QosData` panic、fork SMF `Duplicate URR creation`，PFCP/N1N2 未能到 gNB。该路径不再保留，由方案 A（§4.3）取代。

### 4.3 SMF 外挂路径（方案 A，已接入 Target）

```text
QoSModule (smfenforcer)
  -> POST /nsmf-oam/v1/qos-update
  -> SMF 根据 UE IP 查找 SM Context
  -> AddQosFlow + 创建 QER
  -> PFCP Session Modification -> UPF
  -> N1N2 Message Transfer -> AMF
  -> PDU Session Resource Modify -> gNB
```

该路径绕开 PCF 生成 PCC Rule 后进入 `ApplyPccRules` 的问题链路，直接复用 SMF 内部已工作的 QoS Flow 修改流程。已由本仓库 `smfenforcer` 实现并接入 `ngap`/`auto` 模式（`-smf-endpoint`），通过 Target UDP 触发端到端验证到 gNB 返回 `PDUSessionResourceModifyResponse` 并建立 DRB。

## 5. 已验证的 SMF 接口

### 5.1 请求

```http
POST /nsmf-oam/v1/qos-update
Content-Type: application/json
```

```json
{
  "ue_ip": "10.60.0.1",
  "qfi": 5,
  "five_qi": 2,
  "mbr_ul": "9600000 bps",
  "mbr_dl": "24000000 bps",
  "gbr_ul": "100000 bps",
  "gbr_dl": "100000 bps",
  "arp": {
    "priority": 8,
    "preempt_cap": "MAY_PREEMPT",
    "preempt_vuln": "NOT_PREEMPTABLE"
  }
}
```

注意：bitrate 必须同时携带数值和单位，例如 `9600000 bps`。外部 SMF 处理器依赖该格式进行换算。

### 5.2 响应

成功验证时 SMF 返回 HTTP 200，业务状态为 `ACCEPTED`，并携带 `N1_N2_TRANSFER_INITIATED` 原因。

QoSModule 接入时不能把不同下游的原始响应直接透传给 MASQUE。应统一转换为：

```json
{
  "request_id": "req-001",
  "status": "ACCEPTED",
  "message": "N1/N2 QoS modification initiated"
}
```

失败时建议增加稳定的 `error_code`，例如 `UE_SESSION_NOT_FOUND`、`SMF_UNAVAILABLE` 或 `N1N2_REJECTED`。

## 6. 字段映射

| QoSModule 字段 | SMF 请求 | 说明 |
| --- | --- | --- |
| `Intent.Flow.UEAddress` | `ue_ip` | SMF 用 UE IP 查找 PDU Session |
| `Intent.Flow.QFI` | `qfi` | 指定 QoS Flow |
| 配置或业务映射 | `five_qi` | 当前验证值为 2 |
| `Decision.MBRULKbps` | `mbr_ul` | 乘 1000 后格式化为 `bps` 字符串 |
| `Decision.MBRDLKbps` | `mbr_dl` | DL 缺失时需要与 SMF 接口约定省略行为 |
| `Decision.GBRULKbps` | `gbr_ul` | 乘 1000 后格式化为 `bps` 字符串 |
| `Decision.GBRDLKbps` | `gbr_dl` | DL 缺失时需要与 SMF 接口约定省略行为 |
| 配置 | `arp` | 当前验证使用优先级 8、可抢占、不可被抢占 |

标识分工：

| 标识 | 使用者 | 用途 |
| --- | --- | --- |
| RNTI | gNB 内部、gNB HTTP 私有路径 | 不用于 SMF 寻址 |
| UE IP | SMF、UPF | SMF 外挂路径的会话查找键 |
| SUPI | AMF、SMF、PCF | AF/PCF 路径使用静态 UE IP→SUPI 映射 |
| QFI | UE、SMF、UPF、gNB | 标识 QoS Flow |

RNTI 与 SUPI 不存在可由 QoSModule 直接查询的通用映射，不应尝试硬转换。

## 7. QoSModule 待实现改造

### 7.1 `smfenforcer`（已实现）

`adaptiveqos/smfenforcer/` 已实现 `Enforcer` 接口：

```go
type Enforcer interface {
    Apply(context.Context, Intent, Decision) (ApplyResult, error)
}
```

已实现职责：

1. 校验 UE IP（`intent.Flow.UEAddress` 作为 `ue_ip`）。
2. 把 kbps 转换成带 ` bps` 单位的字符串（防 SMF `StringToBitRate` panic）。
3. 构造 OAM 请求体（`request_id`/`ue_ip`/`qfi`/`five_qi`/`mbr_ul`/`arp`）并 POST 到 `-smf-endpoint`。
4. 解析 HTTP 状态和 `status`/`amf_cause`/`cause` 业务状态。
5. 转换成统一 `ApplyResult`（成功透传 SMF 的 `ACCEPTED`+`amf_cause` 响应体）。

已接入 `routerenforcer` 的 `ngap`/`auto` 模式，`auto` 回退链为 RAN → UDP RAN → SMF OAM。待补：独立单元测试、Mock SMF 联调、QoS Flow 释放语义。

### 7.2 路由模式（已更新）

当前 `routerenforcer` 模式：

| 模式 | Enforcer |
| --- | --- |
| `ran` | gNB HTTP |
| `ngap` | SMF OAM（方案 A） |
| `auto` | RAN → UDP RAN → SMF OAM 依次回退 |

方案 B 删除后，`ngap` 已统一指向 SMF OAM，不再有 AF/PCF 歧义。未来若需区分多条核心网路径，可再新增独立 `smf` 模式名，当前不必要。

### 7.3 补充释放流程

当前 SMF 外挂接口只增加或修改 QoS Flow，没有释放语义。需要增加以下一种能力：

- 同一接口增加 `operation: release`。
- 新增 `/nsmf-oam/v1/qos-release`。
- 使用具有明确生命周期的策略控制对象。

burst 到期后的恢复应由 QoSModule/SMF 发起标准 QoS Flow 修改或释放，不应要求 gNB 从自定义 burst 字段启动本地定时器。

### 7.4 补充 N1 和用户面验证

当前真实验证确认了 N2 和 DRB 建立，但仍需：

- 补充 UE 侧 N1 NAS QoS Rule，验证 UL 流量分类。
- 验证 UPF 下行流量是否按新 QFI 标记。
- 在拥塞环境中测量 UL/DL GBR、时延、丢包和视频体验。

## 8. 验证结果

外部 SMF 路径已经观察到：

| 环节 | 结果 |
| --- | --- |
| SMF QoS 接口 | HTTP 200，`ACCEPTED` |
| SMF→UPF PFCP | Session Modification 请求和响应成功 |
| SMF→AMF N1N2 | AMF 接收并返回成功 |
| AMF→gNB NGAP | gNB 返回 PDU Session Resource Modify Response |
| gNB DRB | 建立 DRB 5，对应 QFI 5、5QI 2 |

详细证据见《方案 A：SMF 外挂实现与验证》。

## 9. 实施顺序

1. ~~在本仓库实现 `smfenforcer` 和单元测试。~~（已实现 `smfenforcer`，接入 `ngap`/`auto`；独立单元测试待补）
2. ~~增加 `smf` 模式并保持 `ran` 路径兼容。~~（当前 `ngap` 直接用 `smfenforcer`，未新增独立 `smf` 模式名）
3. ~~使用 Mock SMF 验证请求映射、错误转换和超时。~~（当前直接对接外部 fork SMF 验证，Mock SMF 待补）
4. ~~接入外部 fork SMF，复测 PFCP、N1N2、NGAP 和 DRB。~~（已完成，见 §8 与《方案 A》）
5. 增加 QoS Flow 释放和 N1 NAS QoS Rule。
6. 完成真实视频和拥塞场景验收后，再将 SMF 路径标记为生产可用。
