# 历史文档归档

本目录保存已经被当前方案替代的设计和联调记录，仅用于追溯决策过程，不代表当前代码或部署状态。

| 文档 | 归档原因 |
| --- | --- |
| `QoS模块化实现方案.md` | 只描述早期 `ranapi` 和 free6gc 内嵌路径，缺少后续 AF/PCF、RouterEnforcer 和 SMF 验证结果 |
| `QoS项目结构与实现详解.md` | 基于早期目录和 direct-RAN 主链生成，内容过长且与当前代码不一致 |
| `free5GC动态QoS改造方案总结.md` | 属于实施前的候选方案比较，结论已被真实 PCF/SMF 联调结果更新 |
| `阶段3-真PCF联调记录.md` | 保存 AF→PCF→SMF 失败链路及 stock SMF panic 证据 |
| `fork-SMF测试记录与现状.md` | 保存 fork SMF 的 Duplicate URR 问题及回退记录 |
| `NGAP下发改造方案.md` | 方案 A（SMF 外挂→AMF→NGAP）已废弃，部署脚本不再提供 `ngap` 入口；`smfenforcer` 代码保留仅供追溯 |
| `方案A-SMF外挂-实现与验证.md` | 方案 A 的 SMF/PFCP/N1N2/NGAP/DRB 端到端验证证据，随方案 A 一并归档 |
| `基站侧随路QoS需求文档.md` | 面向 gNB 开发团队的 NGAP 接收需求规格（C1–C7、NGAP 处理、日志、验收）。整体基于方案 A 的 SMF→AMF→NGAP 架构，随方案 A 归档；其中路径无关的 gNB NGAP 处理要求仍有参考价值 |

当前状态请从仓库根目录的 [README](../../README.md) 开始阅读。本目录所有文档仅用于追溯决策过程，不代表当前代码或部署状态。
