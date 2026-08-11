# adaptive-qos

`adaptive-qos` 是传输无关的 QoS 策略模块。根包定义统一的 `Intent`、`Limits`、`Decision`、`Policy`、`LimitsProvider` 和 `Enforcer`，不依赖 UDP、MASQUE、free5GC 或具体 RAN 协议。

## 包职责

| 包 | 职责 | 当前状态 |
| --- | --- | --- |
| 根包 | BurstPolicy、Processor 和统一模型 | 已实现并有测试 |
| `masqueapi` | MASQUE JSON 请求转换为 `Intent` | 已实现并有测试 |
| `ranapi` | 将 `Decision` 转为 `POST /api/v1/qos/update` | 已实现并有测试 |
| `afenforcer` | 构造 3GPP AppSession 请求并调用 PCF | 已实现；真实端到端受 free5GC SMF 问题阻塞，暂无独立测试 |
| `routerenforcer` | 按 `ran/ngap/auto` 选择 Enforcer | 已实现，暂无独立测试 |

`routerenforcer` 的 `ngap` 模式实际选择 AF/PCF Enforcer，Target 本身不会直接发送 NGAP。NGAP 最终应由 AMF 发给 gNB。

外部 `acore2026/smf` 已验证 `/nsmf-oam/v1/qos-update` 可以触发 PFCP、N1N2 和 NGAP，但本模块当前没有 `smfenforcer`，不能直接使用该路径。

新项目应在边缘完成协议转换，并通过新增 `LimitsProvider` 或 `Enforcer` 适配不同资源模型和下发接口，避免把外部 JSON/HTTP 字段放入核心策略。
