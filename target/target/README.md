# MASQUE Target UDP Server

Target 是 MASQUE Proxy 后端的普通 UDP 服务。它不解析 QUIC、HTTP/3、CONNECT-UDP 或 HTTP Datagram，只处理 Proxy 解封装后的 UDP Payload。

## 1. 通信关系

```text
UE/Client
  -> MASQUE CONNECT-UDP
  -> MASQUE Proxy
  -> 普通 UDP + CLIENT-IP
  -> Target:7400
  -> QoS Handler
  -> Enforcer
```

Target 必须监听 Proxy 配置的目标地址，例如：

```bash
go run ./cmd/target -b 0.0.0.0:7400
```

Target 应使用每次 UDP 收包得到的来源地址回包，不固定 Proxy peer。

## 2. 收包格式

### 2.1 普通 Payload

Proxy 在原始业务 Payload 前增加：

```text
CLIENT-IP: <client_ip>\r\n
\r\n
<original payload>
```

示例：

```text
CLIENT-IP: 192.168.1.20

{"request_id":"req-001", ...}
```

Target 将其解析为：

- `Message.ClientIP = 192.168.1.20`
- `Message.Payload = {"request_id":"req-001", ...}`

如果没有 `CLIENT-IP` 头，则使用 UDP 来源 IP 作为兜底。跨机器部署时，该兜底值通常是 Proxy IP，不一定是 UE IP。

### 2.2 可靠请求信封

Target 也支持可靠信封：

```json
{
  "type": "request",
  "request_id": "retry-001",
  "payload": "<base64 QoS JSON>"
}
```

首次处理后，Target 按 `request_id` 缓存响应。TTL 内收到相同请求时不会重复调用下游 Enforcer，而是返回缓存结果：

```json
{
  "type": "response",
  "request_id": "retry-001",
  "payload": "<base64 response JSON>"
}
```

配置示例见 `reliability.example.json`：

```json
{
  "ttl": "2m",
  "max_entries": 10000,
  "max_payload": 65536,
  "max_retries": 3,
  "timeout": "1s"
}
```

Target 使用 `ttl`、`max_entries` 和 `max_payload`；重试次数和超时由发送端使用。

## 3. QoS 请求

### 3.1 必选字段

```text
request_id
rnti
qfi
burst_info.ul_burst_size
burst_info.ul_burst_duration
service_info.e2e_delay
```

`burst_info.dl_burst_size` 与 `burst_info.dl_burst_duration` 成对可选：

- 两者都缺失：只计算 UL，DL 字段不下发。
- 只携带一个或任一值为 0：返回 `INVALID_PARAM`。

`source_address` 缺失时使用 `CLIENT-IP`。在 gNB HTTP 路径中，RNTI 和 QFI 是主匹配键；在 AF/PCF 或未来 SMF 路径中，UE IP 是核心网寻址的重要输入。

### 3.2 请求示例

```json
{
  "request_id": "req-video-001",
  "rnti": 11222,
  "qfi": 5,
  "source_address": "10.60.0.1",
  "packet_filter": {
    "src_ip": "10.60.0.1",
    "dst_ip": "10.0.0.5",
    "dst_port": 443,
    "protocol": 6
  },
  "burst_info": {
    "ul_burst_size": 120,
    "ul_burst_duration": 100,
    "dl_burst_size": 300,
    "dl_burst_duration": 100,
    "ul_transit_delay": 80,
    "dl_transit_delay": 80
  },
  "service_info": {
    "service_type": "videostreaming",
    "e2e_delay": 160
  }
}
```

## 4. 处理流程

```text
server.go
  -> 解析 CLIENT-IP 和可靠信封
  -> qos_handler.go
  -> masqueapi.Decode
  -> Request.Intent
  -> Processor.Process
      -> StaticLimits.Limits
      -> BurstPolicy.Generate
      -> RouterEnforcer.Apply
  -> UDP 原路回包
```

策略公式：

```text
MBR = burst_size_kB * 8000 / burst_duration_ms
GBR = burst_size_kB * 8000 / transit_delay_ms
PDB = e2e_delay_ms * 0.625
Priority = 3
```

速率向上取整，PDB 四舍五入，然后按 `Limits` 裁剪。

## 5. 下发模式

### 5.1 当前代码支持

| `-core-mode` | 实际 Enforcer | 状态 |
| --- | --- | --- |
| `ran` | `ranapi.Client` | 默认；调用 gNB HTTP API |
| `ngap` | `afenforcer.Enforcer` | 调用 PCF，不是 Target 直接发送 NGAP |
| `auto` | 先 RAN，失败后 AF/PCF | 只有 RAN 返回 `ACCEPTED` 才停止回退 |

已经验证成功的 SMF `/nsmf-oam/v1/qos-update` 尚未接入 Target，当前不存在 `-core-mode smf`。

### 5.2 gNB HTTP 模式

```bash
go run ./cmd/target \
  -mode qos \
  -b 0.0.0.0:7400 \
  -core-mode ran \
  -ran-url http://127.0.0.1:8080/api/v1/qos/update
```

常用 RAN 参数：

```text
-ran-timeout
-ran-mask
-q-type
-q-cap
-q-vul
-dl-max-mcs / -ul-max-mcs
-dl-max-rb / -ul-max-rb
-dl-bler-upper / -ul-bler-upper
-dl-smooth / -ul-smooth
```

`-ran-mask auto` 会根据实际序列化字段自动置位。UL-only 请求不会设置 DL 字段对应的 bit。

### 5.3 AF/PCF 模式

```bash
go run ./cmd/target \
  -mode qos \
  -b 0.0.0.0:7400 \
  -core-mode ngap \
  -pcf-endpoint http://pcf.free5gc.org:8000/npcf-policyauthorization/v1/app-sessions \
  -supi-map "10.60.0.1=imsi-001012345678903" \
  -default-dnn internet \
  -default-5qi 2
```

AF/PCF 路径使用 UE IP 查静态 SUPI 映射，并生成真实 3GPP `AppSessionContext` 请求。真实 PCF 已接受该格式，但当前 free5GC PCF→SMF apply 链存在 panic/重复 URR 问题，所以该模式不应被视为当前生产可用路径。

`cmd/mockpcf` 仍按早期平铺 JSON 结构进行严格校验，而 `afenforcer` 已改为 `ascReqData` 包装结构。更新 Mock PCF 前，不能把二者的严格模式测试结果作为当前协议验证依据。

## 6. Mock RAN 联调

启动 Mock RAN：

```bash
go run ./cmd/mockran \
  -b 127.0.0.1:8080 \
  -path /api/v1/qos/update \
  -status ACCEPTED \
  -message "mock ran accepted"
```

启动 Target：

```bash
go run ./cmd/target \
  -mode qos \
  -b 0.0.0.0:7400 \
  -core-mode ran \
  -ran-url http://127.0.0.1:8080/api/v1/qos/update
```

链路为：

```text
MASQUE Proxy -> UDP -> Target -> HTTP POST -> Mock RAN
             <- UDP response <- Target <- HTTP response
```

Mock RAN 默认 `-strict=true`，至少校验：

```text
request_id
mask
rnti
q_qfi
q_mbr_ul
q_gbr_ul
q_pdb
burst_info.ul_burst_size
burst_info.ul_burst_duration
```

失败和超时模拟：

```bash
go run ./cmd/mockran \
  -status REJECTED \
  -http-status 503 \
  -error-code RAN_BUSY \
  -message "mock ran rejected qos update"

go run ./cmd/mockran -delay 5s
```

## 7. Windows 一键联调

在仓库根目录执行：

```powershell
.\scripts\start-windows-mock-test.ps1
```

或双击：

```text
scripts\start-windows-mock-test.bat
```

脚本默认启动：

```text
Mock RAN: http://127.0.0.1:18081/api/v1/qos/update
Target:   UDP 0.0.0.0:7400
```

MASQUE Proxy 与 Target 不在同一台机器时，需要使用 Windows 局域网 IP，并放行入站 UDP 7400。

## 8. Echo 模式

Echo 模式只验证 MASQUE UDP 转发：

```bash
go run ./cmd/target -mode echo -prefix "reply: "
```

## 9. 测试

```bash
go test ./...
```

当前缺口：

- `afenforcer` 和 `routerenforcer` 没有独立单元测试。
- `mockpcf` 没有测试，且协议模型落后于当前 `afenforcer`。
- 尚无 `smfenforcer` 和 SMF Mock 联调测试。
