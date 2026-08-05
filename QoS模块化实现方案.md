# QoS模块化实现方案

## 1. 目标

将 QoS 能力拆分为可跨项目复用的策略核心和可替换的边缘适配器：

- 策略核心不依赖 MASQUE、UDP、HTTP、free6gc 或具体 RAN。
- 当前项目使用 `rnti + qfi`、必选UL burst、成对可选DL burst和时延预算生成 RAN QoS。
- free6gc 保留原有 `AdaptiveReport -> Session/QER Override` 功能。
- 不同项目可以替换请求格式、资源范围来源、下发接口和反馈格式。

## 2. 模块结构

```text
QoS/
├── adaptiveqos/                         独立 Go module
│   ├── model.go                         统一 Intent、Decision、Limits
│   ├── policy.go                        当前项目 MBR/GBR/PDB 策略
│   ├── processor.go                     策略与下发编排
│   ├── masqueapi/                       当前 MASQUE JSON 适配器
│   └── ranapi/                          当前 RAN HTTP API 适配器
├── target/target/
│   ├── server.go                        通用 UDP Target
│   └── qos_handler.go                   当前项目业务入口
└── ref/free6gc/free6gc-upf/
    └── internal/forwarder/userspace/
        ├── adaptive_qos.go               原功能和协议分流
        └── adaptive_qos_project.go       当前项目 RAN 适配路径
```

`adaptiveqos` 位于独立 module，避免 Go `internal` 目录限制其他项目引用。

## 3. 核心接口

```go
type Policy interface {
    Generate(context.Context, Intent, Limits) (Decision, error)
}

type LimitsProvider interface {
    Limits(context.Context, FlowSelector) (Limits, error)
}

type Enforcer interface {
    Apply(context.Context, Intent, Decision) (ApplyResult, error)
}
```

项目只需选择或实现：

1. 请求适配器：外部协议转换为 `Intent`。
2. `Policy`：选择当前 burst 策略或项目自定义策略。
3. `LimitsProvider`：使用固定 RAN 范围、配置中心或未来 UPF 查询。
4. `Enforcer`：调用 RAN API、写入 UPF QER 或调用其他控制接口。
5. 反馈适配器：将 `ApplyResult` 转为项目需要的返回格式。

## 4. 当前项目处理链

```text
MASQUE Proxy
  -> UDP Target（解析 CLIENT-IP）
  -> masqueapi.Decode
  -> BurstPolicy.Generate
  -> 按 RAN Limits 裁剪
  -> ranapi.Client.Apply
  -> UDP 原路返回 RAN 结果
  -> MASQUE Proxy
  -> UE
```

策略规则：

- `MBR = burst_size_kB * 8 * 1000 / burst_duration_ms`
- `GBR = burst_size_kB * 8 * 1000 / transit_delay_ms`
- `service_info.e2e_delay`为必选字段；真实传输时延优先使用请求值，否则使用 `e2e_delay * 0.8`。
- `PDB = e2e_delay * 0.625`
- 优先级固定为 `3`。
- MBR、GBR、PDB、优先级按 RAN 接口范围裁剪。
- 5QI 不参与当前策略。

当前请求必须携带 `request_id`、`rnti`、`qfi`、`ul_burst_size`、`ul_burst_duration` 和 `service_info.e2e_delay`。
`dl_burst_size` 与 `dl_burst_duration` 成对可选；二者同时缺失时只更新UL，不下发DL相关RAN字段；只携带其中一个或值为0时拒绝。
`source_address` 或 Proxy 的 `CLIENT-IP` 只作为辅助信息。

## 5. free6gc兼容路径

`adaptive_qos.go` 在 `adaptiveQos.projectRanQos.enable=true` 时，先检查报文是否同时包含当前项目的
`request_id/rnti/qfi/burst_info` 主字段：

- 匹配当前格式：调用共享 `BurstPolicy` 和 RAN API。
- 不匹配：继续按原 `AdaptiveReport`、Session 查找、Profile 选择和 QER Override 流程处理。

原 predictive-burst 数学计算继续保留在 `adaptive_qos.go` 中，不进行移动或替换。
原文件只在 UDP `reportLoop` 中增加一次 `handleProjectQoSDatagram` 分流调用；
具体识别、策略调用、RAN 下发、计数和回包均位于 `adaptive_qos_project.go`。

free6gc 当前项目路径使用：

```yaml
adaptiveQos:
  enable: true
  projectRanQos:
    enable: true
  gnbControl:
    addr: 127.0.0.1
    port: 8080
```

`projectRanQos.enable`默认关闭；关闭时不进入当前项目随路QoS分流，原free6gc AdaptiveReport流程保持不变。

RAN URL 组装为：

```text
http://<gnbControl.addr>:<gnbControl.port>/api/v1/qos/update
```

## 6. Target运行方式

当前 QoS 模式：

```bash
cd target/target
go run ./cmd/target \
  -mode qos \
  -b 0.0.0.0:7400 \
  -ran-url http://127.0.0.1:8080/api/v1/qos/update
```

保留的 MASQUE echo 测试模式：

```bash
go run ./cmd/target -mode echo -prefix "reply: "
```

可配置项包括 RAN 超时、真实传输时延默认比例和默认时延。

## 7. 扩展约束

- 外部 JSON/HTTP 字段不能进入核心模型。
- 核心字段名必须带单位后缀，例如 `SizeKB`、`DurationMS`、`MBRDLKbps`。
- 新项目不得直接依赖 free6gc 的 `Driver`、`SessionState` 或 `factory.Config`。
- 新协议通过新增 adapter 兼容，不在核心策略中加入协议判断。
- 新下发端实现 `Enforcer`，不要修改策略计算。
- UPF 原 QoS 查询未来可实现为新的 `LimitsProvider`，当前固定范围无需变更策略。

## 8. 验证范围

- 当前设计示例的 MBR、GBR、PDB 和优先级计算。
- RAN GBR 上限裁剪。
- 显式真实传输时延优先级。
- 必填字段校验。
- Target 到 RAN 的请求和响应回送。
- free6gc 新格式识别与 RAN 下发。
- legacy 报文不被新格式分支接管。
- free6gc 原 predictive-burst 数值保持不变。
