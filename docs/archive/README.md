# 历史文档归档

本目录保存已经被当前方案替代的设计和联调记录，仅用于追溯决策过程，不代表当前代码或部署状态。

| 文档 | 归档原因 |
| --- | --- |
| `QoS模块化实现方案.md` | 只描述早期 `ranapi` 和 free6gc 内嵌路径，缺少后续 AF/PCF、RouterEnforcer 和 SMF 验证结果 |
| `QoS项目结构与实现详解.md` | 基于早期目录和 direct-RAN 主链生成，内容过长且与当前代码不一致 |
| `free5GC动态QoS改造方案总结.md` | 属于实施前的候选方案比较，结论已被真实 PCF/SMF 联调结果更新 |
| `阶段3-真PCF联调记录.md` | 保存 AF→PCF→SMF 失败链路及 stock SMF panic 证据 |
| `fork-SMF测试记录与现状.md` | 保存 fork SMF 的 Duplicate URR 问题及回退记录 |

当前状态请从仓库根目录的 [README](../../README.md) 和 [NGAP 下发改造方案](../../NGAP下发改造方案.md) 开始阅读。
