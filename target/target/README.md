# Target UDP Server 开发内容

本文从 `masque-go_CONNECT-UDP软件设计文档.md` 中提取 Target 模块需要开发的内容。Target 在 MASQUE CONNECT-UDP 架构中只是普通 UDP 服务端，不需要理解 MASQUE、QUIC、HTTP/3、CONNECT-UDP、HTTP Datagram 或 Context ID。

## 模块定位

Target UDP Server 是真正的业务 UDP 服务。MASQUE Proxy 会把 Client 通过 CONNECT-UDP 隧道发送的 UDP Payload 解封装后，用普通 UDP 发给 Target。当前 Proxy -> Target 方向会在业务 Payload 前增加 `CLIENT-IP` 自定义头，Target 需要先解析该头，再处理原始业务 Payload。

Target 看到的通信关系是：

```text
+-------------------------------+       UDP       +-------------------+
| MASQUE Proxy 后端 UDP socket |  ----------->   | Target UDP Server |
| ProxyIP:ProxyRandomPort      |  <-----------   | TargetIP:Port     |
+-------------------------------+                 +-------------------+
```

Target 看到的源地址通常是 MASQUE Proxy 创建的后端 UDP socket 地址，不是原始 Client 地址。

## 需要开发的内容

1. 启动一个普通 UDP Server。
2. 绑定目标监听地址，例如 `0.0.0.0:7400`。
3. 循环读取 UDP 报文。
4. 解析 Proxy 添加的 `CLIENT-IP` 自定义头。
5. 记录 Client IP 和原始业务 Payload。
6. 使用 `recvfrom` 返回的来源地址进行回包。
7. 支持多个来源地址，不要把 Target socket 固定成只能与一个 peer 通信。

## 监听要求

Target 必须先在目标 IP:Port 上启动监听 socket。这个地址就是 Client 创建 CONNECT-UDP 请求时传给 `masque.NewRequest()` 的 target 参数，也是 Proxy 最终解析出的 `target_host:target_port`。

示例目标：

```text
192.168.123.100:7400
```

那么 Target 需要监听 UDP 7400 端口：

```text
bind 0.0.0.0:7400
```

Proxy 创建 `net.DialUDP()` 只是在 Proxy 侧创建后端 UDP socket，不会替 Target 创建监听 socket。如果 Target 没有监听，Client 可能能成功建立 CONNECT-UDP 隧道，但业务 Payload 无人处理，也收不到有效响应。

## 收包格式

Target 收到的是普通 UDP 报文：

```text
UDP/IP Header:
    src = ProxyIP:ProxyRandomPort
    dst = TargetIP:TargetPort

UDP Payload:
    CLIENT-IP: <client_ip>\r\n
    \r\n
    Client 原始业务 Payload
```

Target 不会收到 HTTP Datagram 的 Context ID，也不需要解析 MASQUE 封装。但 Target 需要解析 Proxy 添加的 `CLIENT-IP` 自定义头。

示例：

```text
CLIENT-IP: 192.168.1.20

hello
```

Target 解析后得到：

```text
client_ip = 192.168.1.20
original_payload = hello
```

## 回包要求

Target 回包时必须回给本次 `recvfrom()` 返回的源地址：

```text
UDP/IP Header:
    src = TargetIP:TargetPort
    dst = ProxyIP:ProxyRandomPort

UDP Payload:
    Target 原始响应 Payload
```

这会让响应先回到 MASQUE Proxy，再由 Proxy 封装为 HTTP Datagram 转回 Client。

Target 回包时不需要再附加 `CLIENT-IP` 头。`-mode echo` 会基于原始业务 Payload 生成响应，例如收到：

```text
CLIENT-IP: 192.168.1.20

hello
```

默认回包：

```text
reply: hello
```

## 多来源处理

Target UDP Server 应使用 `bind + recvfrom + sendto` 模型。这个模型可以支持多个 Proxy 后端 UDP socket：

```text
ProxyIP:51001 -> Target:7400
ProxyIP:51002 -> Target:7400
ProxyIP:51003 -> Target:7400
```

每次收包时都使用本次的来源地址回包：

```text
data, addr = recvfrom()
sendto(reply, addr)
```

除非业务明确要求一对一连接，否则 Target 不应主动调用 UDP `connect()` 固定 peer。

## 最小验收标准

1. Target 能监听配置的 UDP 地址和端口。
2. Proxy 转发 UDP Payload 到 Target 后，Target 能解析出 `CLIENT-IP`。
3. Target 能从 UDP Payload 中取出原始业务 Payload。
4. Target 能基于原始业务 Payload 生成响应，不把 `CLIENT-IP` 头当作业务数据。
5. Target 能把响应发送回本次来源地址。
6. Client 能通过 MASQUE Proxy 收到 Target 的原始响应 Payload。
7. Target 日志能显示来源地址为 Proxy 的后端 UDP socket，并显示解析出的 Client IP。

## QoS业务模式

Target 当前默认运行在 `qos` 模式：

```bash
go run ./cmd/target \
  -mode qos \
  -b 0.0.0.0:7400 \
  -ran-url http://127.0.0.1:8080/api/v1/qos/update
```

收到普通 QoS JSON 时，Target 会调用共享 `adaptive-qos` 模块完成：

```text
masqueapi.Decode -> Request.Intent -> Processor.Process -> BurstPolicy.Generate -> ranapi.Client.Apply
```

当前 QoS 请求必选字段：

```text
request_id
rnti
qfi
burst_info.ul_burst_size
burst_info.ul_burst_duration
service_info.e2e_delay
```

`burst_info.dl_burst_size` 与 `burst_info.dl_burst_duration` 成对可选。二者同时缺失时只更新 UL；只携带其中一个或值为 0 时返回 `INVALID_PARAM`。

RAN API默认字段可通过启动参数覆盖，常用参数包括：

```bash
go run ./cmd/target \
  -mode qos \
  -ran-url http://127.0.0.1:8080/api/v1/qos/update \
  -ran-mask auto \
  -q-type 0 \
  -q-cap 1 \
  -q-vul 0 \
  -dl-max-mcs 28 \
  -ul-max-mcs 28 \
  -dl-max-rb 273 \
  -ul-max-rb 273 \
  -dl-bler-upper 0.01 \
  -ul-bler-upper 0.01 \
  -dl-smooth 0.5 \
  -ul-smooth 0.5
```

`ran-mask` 默认为 `auto`，会按照设计文档中 RAN 字段表里 `MASK` 之后的字段顺序生成 bitmask，并且只为本次 JSON 请求实际携带的字段置 bit。也可以传入十进制 `uint32` 数值手动覆盖，例如 `-ran-mask 4294967295`。

### Mock RAN 联调

没有真实 RAN 时，可以先启动 Mock RAN。Mock RAN 只模拟 gNB-HTTP 下发接口，接收 `POST /api/v1/qos/update`，打印 target 下发的 RAN 请求，并返回 MASQUE 侧需要的 `request_id/status/message`。

启动 Mock RAN：

```bash
go run ./cmd/mockran \
  -b 127.0.0.1:8080 \
  -path /api/v1/qos/update \
  -status ACCEPTED \
  -message "mock ran accepted"
```

再启动 target，并把 RAN endpoint 指向 Mock RAN：

```bash
go run ./cmd/target \
  -mode qos \
  -b 0.0.0.0:7400 \
  -ran-url http://127.0.0.1:8080/api/v1/qos/update
```

这样联调链路为：

```text
MASQUE Proxy -> UDP -> target -> HTTP POST -> mockran -> target -> UDP response -> MASQUE Proxy
```

Mock RAN 默认开启 `-strict=true`，会校验 target 下发的 RAN 请求至少包含：

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

`burst_info.dl_burst_size` 与 `burst_info.dl_burst_duration` 仍然成对可选。需要只测试链路、不校验字段时，可以加 `-strict=false`。

常用失败场景模拟：

```bash
go run ./cmd/mockran \
  -status REJECTED \
  -http-status 503 \
  -error-code RAN_BUSY \
  -message "mock ran rejected qos update"
```

常用超时场景模拟：

```bash
go run ./cmd/mockran -delay 5s
```

Echo 模式仅用于链路测试：

```bash
go run ./cmd/target -mode echo -prefix "reply: "
```

## 设计文档来源

主要对应设计文档中的以下章节：

- `4.2 Target UDP Server 启动监听 socket`
- `4.6 Target 监听 socket 与 MASQUE 的关系`
- `4.7 Target 监听 socket 的消息格式`
- `7.5 Proxy 到 Target 的 UDP 消息格式`
- `8.2 Target UDP Server 回包逻辑`
- `8.3 UDP socket 是否只能一对一`
- `8.4 Target 到 Proxy 的 UDP 消息格式`

## 可靠请求去重缓存

Target 支持解析 Client 封装的可靠请求信封：

```json
{
  "type": "request",
  "request_id": "<唯一请求ID>",
  "payload": "<base64编码后的原始payload>"
}
```

首次收到某个 `request_id` 时，Target 会基于原始业务 payload 生成响应，并缓存：

```text
request_id -> response payload
```

如果在 `ttl` 时间内再次收到同一个 `request_id`，Target 不重复执行业务处理，直接返回缓存响应。响应格式为：

```json
{
  "type": "response",
  "request_id": "<同一个请求ID>",
  "payload": "<base64编码后的响应payload>"
}
```

可靠机制配置文件示例：

```json
{
  "ttl": "2m",
  "max_entries": 10000,
  "max_payload": 65536,
  "max_retries": 3,
  "timeout": "1s"
}
```

Target 主要使用 `ttl`、`max_entries` 和 `max_payload`；`max_retries`、`timeout` 由 Client 使用。启动时通过 `-config reliability.example.json` 指定配置文件。

可靠信封包住 QoS JSON 时，Target 会先解出原始 QoS payload，再调用 QoS Handler。相同可靠 `request_id` 在 TTL 内重传时，Target 不会再次调用 RAN，而是返回第一次缓存的响应。
- `11. 关键结论`
