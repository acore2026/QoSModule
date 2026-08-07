# 阶段3 真 PCF 联调验证记录

> 时间:2026-08-07
> 目标:把 afenforcer 从中间格式适配为真 free5gc PCF 的 3GPP `AppSessionContextReqData`,跑通 AF→PCF→SMF→AMF→gNB 全链,验证 QoS 是否让 RAN 动态调整某 UE。
> 结论:**AF→PCF→SMF-notify→ApplyPccRules 信令链全通,格式 100% 正确**;最后一步 SMF 处理生成的 PCC rule 时 nil 指针 panic(free5gc SMF/PCF 集成 bug),未到 AMF/gNB。

---

## 1. 适配内容(已完成,commit `c2ff7b8`)

`afenforcer/enforcer.go` 的 `buildAppSessionBody` 从中间 JSON 改为真 3GPP 格式,基于 `acore2026/openapi v1.2.4` 的 `AppSessionContextReqData` 模型:

- **包装层**:body 包在 `{"ascReqData": {...}}` 里(PCF 的 `HTTPPostAppSessions` 读 `appSessionContext.AscReqData`,不是平铺字段)
- **必填字段**(openapi 无 omitempty):`notifUri`、`suppFeat`、`MediaComponent.medCompN`、`MediaSubComponent.fNum`、`Snssai.sst`
- **AF 顶层字段**:`afAppId`、`supi`、`ueIpv4`、`dnn`、`sliceInfo`
- **MediaComponent**:`qosReference`(5QI,string)、`marBwDl/Ul`(MBR,**string bps**)、`fStatus`、`medSubComps`
- **MediaSubComponent.fDescs**:3GPP FlowDescription,`permit out ip from <src> to <dst>` / `permit in ip from <dst> to <src>`(SMF/UPF 的 `ParseFlowDesc` 语法)
- Config 加 `NotifUri`/`SuppFeat`/`AfAppId`(`afenforcer/config.go`)

关键源码依据:
- PCF create 校验:`pcf/internal/sbi/api_policyauthorization.go:78` `HTTPPostAppSessions` → 检查 `AscReqData==nil || SuppFeat=="" || NotifUri==""`
- FlowDescription 转换:`pcf/.../policyauthorization.go` `flowDescFromN5toN7`(`permit out/in/inout` → N7)
- SMF/UPF 解析:`upf/internal/forwarder/flowdesc.go` `ParseFlowDesc`(grammar: `action dir proto 'from' src 'to' dst`)

## 2. 验证结果(2026-08-07 02:19)

环境:重启 SMF 清空陈旧状态 → UE `imsi-001012345678903` 重新入网(pdu_session_id=5,UE IP `10.60.0.1`,dnn=internet)→ 一次干净 create。

请求体(有效):
```json
{"ascReqData":{"afAppId":"qos-module","notifUri":"http://127.0.0.1:0/pcf-notif","suppFeat":"0",
"supi":"imsi-001012345678903","ueIpv4":"10.60.0.1","dnn":"internet","sliceInfo":{"sst":1,"sd":"000000"},
"medComponents":{"1":{"medCompN":1,"fStatus":"ENABLED","qosReference":"2","marBwDl":"24000000","marBwUl":"9600000",
"medSubComps":{"1":{"fNum":1,"fStatus":"ENABLED",
"fDescs":["permit out ip from 10.60.0.1 to 0.0.0.0/0","permit in ip from 0.0.0.0/0 to 10.60.0.1"]}}}}}}}
```

| 环节 | 结果 | 证据 |
|---|---|---|
| ① afenforcer → PCF create | ✅ **201 Created** | `Location: .../app-sessions/imsi-...903-3` |
| ② PCF → SMF 通知 | ✅ 触发 | SMF `In HandleSMPolicyUpdateNotify` |
| ③ SMF 解析 flow | ✅ 无 "too few fields" | `Modify PCCRule[PccRuleId-1]` + `Install PCCRule[PccRuleId-3]` |
| ④ SMF `ApplyPccRules.func2` | ❌ **panic** | `nil pointer dereference` @ `ApplyPccRules.func2`(`notifier.go:110`) |
| ⑤ AMF NGAP modify / gNB | ❌ 未到 | SMF panic 卡在发 N1N2 之前 |

## 3. 卡点:SMF nil 指针 panic(根因分析)

- panic:`runtime error: invalid memory address or nil pointer dereference`
- 位置:`smf/internal/context/sm_context_policy.go` `(*SMContext).ApplyPccRules.func2`(被 `notifier.go:110` 调用)
- 触发:SMF 处理 PCF 从 AF app-session 生成的 **PCC rule `PccRuleId-3`** 时
- 推测根因:PCC rule 引用的 `QosData` 在 `decision.QosDecs` 里为 nil,SMF 的 `ApplyPccRules`(及 func2/line 339-352 的 `c.QosDatas[qosID]` 解引用)未做 nil 防护

这是 **free5gc SMF/PCF 的集成 bug**:PCF 的 `CreateQosData`/`SetPccRuleRelatedData`(`pcf/.../policyauthorization.go:81/101`)生成的 PCC rule→QosData 链接,在 SMF 侧取到 nil。SMF 没做 nil 防护就解引用。

## 4. 对方案 B 的影响

方案 B(PCF/AF)选定的核心理由是 **0 核心网 NF 改动**。但本次卡在 SMF panic——要继续必须**改 SMF(加 nil 防护)或改 PCF(生成完整 QosData 并链接)**,即最后一步仍要碰核心网。方案 B 的"0 改动"红利在此失效。

## 5. 已确认"对"的部分(无需再改)

- afenforcer 的 3GPP `AppSessionContextReqData` 格式:✅ PCF 接受(201)
- `ascReqData` 包装层、`notifUri`/`suppFeat` 必填、`medComponents` 结构、`qosReference`(5QI)、`marBwDl/Ul`(string bps):✅
- `fDescs` 的 FlowDescription 格式(`permit out ip from X to Y`):✅ SMF `ParseFlowDesc` 正确解析,无 "too few fields"
- PCF→SMF notify:✅ 触发,SMF 进入 apply

## 6. 下一步选项

1. **改 SMF(最小)**:给 `ApplyPccRules.func2` 及 `c.QosDatas[qosID]` 解引用处加 nil 防护,重建 SMF 镜像。改动小(几处 nil-guard),但属于"碰 SMF"
2. **改 PCF**:让 PCF 从 AF app-session 生成 PCC rule 时正确链接非 nil 的 QosData(`policyauthorization.go` 的 `CreateQosData`/`SetPccRuleRelatedData` 路径),重建 PCF
3. **重新评估方案**:既然最后一步必须碰核心网,可与方案 A(SMF 外挂,直接给 SMF 加 `POST /qos/v1/update` 端点 + 复用内部 QoS flow modify)对比——后者反而更可控(SMF 自己内部 apply,不依赖 PCF 生成 PCC rule 的完整性)

## 7. 相关 commit

- `6a28679` feat: 方案B PCF/AF 外挂——afenforcer + routerenforcer + mockpcf(阶段2,mockpcf 验证通过)
- `c2ff7b8` feat(afenforcer): 适配真 PCF 3GPP AppSessionContextReqData 格式(阶段3,真 PCF 201 接受)

## 8. 注意事项

- SMF 被 panic 后状态可能卡在 `ModificationPending`(gin 兜住 panic,容器存活但该 SMContext 异常);下次测试前需重启 SMF 清状态 + UE 重新入网
- 真 PCF create 会留下 app-session(`imsi-...903-N`),PCF 侧需清理或等其过期
- `notifUri` 用的是占位 `http://127.0.0.1:0/pcf-notif`(create 成功,异步通知不可达无影响);如需接收 PCF 事件通知,要给 QoS 模块加一个 HTTP callback server
