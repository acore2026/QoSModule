# QoSModule

QoSModule 接收 MASQUE Proxy 转发的 UDP QoS 请求，将业务突发需求转换为统一 `Intent`，计算 MBR、GBR、PDB 和优先级，再通过可替换的 Enforcer 下发。

## 当前状态

更新时间：2026-08-11。

| 能力 | 状态 | 说明 |
| --- | --- | --- |
| MASQUE UDP Target | 已实现 | 解析 `CLIENT-IP`，支持可靠信封、去重缓存和原路回包 |
| QoS 请求校验与策略计算 | 已实现 | UL 必选、DL 成对可选，按静态范围裁剪 |
| gNB HTTP 下发 | 已实现 | `ranapi.Client` 调用 `POST /api/v1/qos/update`，适用于开放 gNB 和 Mock RAN |
| AF/PCF 下发 | 已实现但不建议当前部署使用 | `afenforcer` 已适配真实 PCF 请求格式；真实链路受 free5GC PCF/SMF 问题阻塞 |
| SMF 外挂下发 | 外部仓库已验证，本仓库未接入 | `acore2026/smf` 的 `/nsmf-oam/v1/qos-update` 已跑通至 gNB 并建立 DRB；本仓库尚无 `smfenforcer` |
| 直接发送 NGAP | 未实现，也不应由本模块直接实现 | NGAP 应由 AMF 向 gNB 发送 |
| GTP-U 自定义扩展头传 QoS JSON | 未实现且不属于当前方案 | 当前基站通过标准 NGAP 接收 QoS 修改 |

当前仓库实际支持的下发模式为：

- `ran`：调用 gNB HTTP API。
- `ngap`：调用 AF/PCF Enforcer，由核心网尝试触发 NGAP。该名称不表示 Target 直接发送 NGAP。
- `auto`：先尝试 gNB HTTP，失败后回退 AF/PCF。

已经验证成功的 SMF 外挂路径尚未装配到上述模式中，这是当前最主要的实现缺口。

## 代码结构

```text
adaptiveqos/                     传输无关的策略模块
├── model.go                     Intent、Decision、Limits、Enforcer 接口
├── policy.go                    BurstPolicy 动态 QoS 计算
├── processor.go                 查询范围、计算、下发的编排
├── masqueapi/                   MASQUE JSON 适配器
├── ranapi/                      gNB HTTP Enforcer
├── afenforcer/                  AF/PCF Enforcer
└── routerenforcer/              ran、ngap、auto 路由

target/target/                   MASQUE 后端 UDP 服务
├── server.go                    UDP 收发和 CLIENT-IP 解析
├── reliability.go               可靠信封和请求去重
├── qos_handler.go               请求到 Processor 的业务入口
└── cmd/
    ├── target/                  正式 Target
    ├── mockran/                 Mock RAN
    └── mockpcf/                 Mock PCF
```

`target_backup_20260803-200912/` 是旧 Target 快照，不是当前运行入口。
`ref/` 被顶层仓库忽略，可能包含本地 free6gc 实验代码，不属于本仓库发布内容。

## 当前文档

| 文档 | 用途 |
| --- | --- |
| [随路 QoS 设计文档](随路Qos设计文档.md) | MASQUE 请求、策略计算和 gNB HTTP 协议参考；HTTP 章节只适用于支持该接口的 gNB |
| [NGAP 下发改造方案](NGAP下发改造方案.md) | 当前下发路径、真实验证结果和待接入项 |
| [方案 A：SMF 外挂实现与验证](方案A-SMF外挂-实现与验证.md) | 已跑通的 SMF、PFCP、N1N2、NGAP 和 DRB 证据 |
| [基站侧随路 QoS 需求](基站侧随路QoS需求文档.md) | 当前基站通过标准 NGAP 接入时的职责和验收要求 |
| [Target README](target/target/README.md) | UDP 协议、运行参数和 Mock 联调方法 |
| [adaptive-qos README](adaptiveqos/README.md) | 共享策略模块及适配器边界 |

历史方案和失败实验统一位于 [docs/archive](docs/archive/README.md)，不能作为当前部署依据。

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

1. 在 `adaptiveqos` 中实现 `smfenforcer`，调用已验证的 SMF `/nsmf-oam/v1/qos-update`。
2. 为 RouterEnforcer 增加明确的 `smf` 模式，避免继续用 `ngap` 混指 AF/PCF 与 SMF 两条路径。
3. 统一 Enforcer 返回值为 MASQUE 侧的 `request_id/status/error_code/message`，不要原样透传不同下游协议。
4. 补充 QoS Flow 释放接口和 N1 NAS QoS Rule，再进行用户面拥塞下的 UL/DL GBR 验证。
