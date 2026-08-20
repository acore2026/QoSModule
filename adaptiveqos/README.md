# adaptive-qos

`adaptive-qos` 是传输无关的 QoS 策略模块。根包定义统一的 `Intent`、`Limits`、`Decision`、`Policy`、`LimitsProvider` 和 `Enforcer`，不依赖 UDP、MASQUE、free5GC 或具体 RAN 协议。

## 包职责

| 包 | 职责 | 当前状态 |
| --- | --- | --- |
| 根包 | BurstPolicy、Processor 和统一模型 | 已实现并有测试 |
| `masqueapi` | MASQUE JSON 请求转换为 `Intent` | 已实现并有测试 |
| `ranapi` | 将 `Decision` 转为 `POST /api/v1/qos/update` | 已实现并有测试 |
| `smfenforcer` | 构造 SMF OAM 请求（方案 A）并调用 fork SMF `/nsmf-oam/v1/qos-update` | 已实现并端到端验证到 gNB 建 DRB；暂无独立单元测试 |
| `routerenforcer` | 按 `ran/ngap/auto` 选择 Enforcer | 已实现，暂无独立测试 |

`routerenforcer` 的 `ngap` 模式实际选择 SMF OAM Enforcer（方案 A），Target 本身不会直接发送 NGAP。NGAP 最终由 AMF 发给 gNB。

本模块的 `smfenforcer` 调用 fork SMF `/nsmf-oam/v1/qos-update`，已端到端验证可触发 PFCP、N1N2、NGAP 并建立 DRB。原 AF/PCF 路径（`afenforcer`）因 free5GC PCF/SMF 链路 panic/重复 URR 问题已删除，由方案 A 取代。

新项目应在边缘完成协议转换，并通过新增 `LimitsProvider` 或 `Enforcer` 适配不同资源模型和下发接口，避免把外部 JSON/HTTP 字段放入核心策略。
