# fork SMF 镜像测试记录 + 当前现状(暂停点)

> 归档状态：失败链路的实验记录。后续已经采用 SMF 外挂端点绕过该 PCF→SMF ApplyPccRules 路径。

> 时间:2026-08-07
> 背景:阶段3 真 PCF 联调卡在 stock SMF `ApplyPccRules` nil panic。尝试换 fork SMF 镜像(含上游修复)看是否解决。
> 结论:**fork SMF 修了 panic,但撞新的 `Duplicate URR creation`,QoS 仍到不了 gNB**。已回退 stock,暂停。

---

## 1. 做了什么

- 构建 fork SMF 镜像 `free5gc/smf:fork`:从 `base/free5gc/NFs/smf` 源码 `CGO_ENABLED=0` 静态构建(仿 AMF buildv16,`nf_smf/Dockerfile.build`),alpine 基
  - 注:fork 预编译二进制 `base/free5gc/bin/smf` 是 glibc 动态链接,alpine 跑不了,故从源码静态构建
- fork SMF 源码 commit:含上游修复 `fix/float-bitrate-causing-zero-MBR-GBR`、`Fix multi-UPF PCC path selection` 等(stock v4.2.1 没有)
- fork PCF 不换(fork pcf 只有 1 个 rebrand commit,无修复)
- compose smf 标签切 `free5gc/smf:fork`,重建容器,UE 903 重新入网(pdu_session_id=1,UE IP 10.60.0.1)

## 2. fork SMF 测试结果

跑原版 QoS create(带 fDescs,不加 altSerReqs)到真 PCF:

| 环节 | stock v4.2.1(之前) | **fork SMF(本次)** |
|---|---|---|
| PCF create | 201 ✅ | 201 ✅ |
| SMF notify | ✅ | ✅ |
| ApplyPccRules | ❌ nil QosData **panic** | ✅ **无 panic**:`Modify PccRuleId-1/2` + `Install PccRuleId-3` 成功 |
| PFCP modify→UPF(QER) | — | ❌ 没发,卡 `Duplicate URR creation`(10 条警告) |
| N1N2→AMF NGAP modify | ❌ | ❌ 没发 |
| gNB 调整 | ❌ | ❌ |

**部分胜利**:上游修复解决了 nil deref,新 PCC rule 成功 Install。但 apply 在 `Duplicate URR creation` 处停住,没继续到 PFCP modify + N1N2,QoS 没到 gNB。

## 3. 根因小结

AF→PCF→SMF 这条"AF 驱动动态 QoS"链在 free5gc 里**连续有 bug**:
- stock v4.2.1:PCF 生成的 PCC rule 的 QosData 链接有问题 → SMF `ApplyPccRules.func2` nil panic
- fork SMF:panic 修了,但 `Duplicate URR creation`(URR/计费规则重复)挡住 PFCP+N1N2

两个版本都没跑通"PCF 生成的 PCC rule → SMF 全套 apply → gNB"。AF 侧(请求格式)已验证正确(PCF 201 接受),改不了 SMF 内部这两个 bug。

## 4. 已回退

- compose smf 标签改回 `free5gc/smf:v4.2.1`(stock),容器已重建运行
- `free5gc/smf:fork` 镜像保留在本地(后续可再用),`nf_smf/Dockerfile.build` 保留(可用于重建 fork SMF)

## 5. 当前状态(暂停点)

- 核心网:全 stock v4.2.1(SMF 已回退),AMF buildv16(自建),UPF v4.2.1.ac2
- QoS 模块:方案 B(afenforcer + routerenforcer + mockpcf)已实现并推送(`6a28679`、`c2ff7b8`),真 PCF 格式已适配(3GPP `AppSessionContextReqData`)
- 真 PCF 联调:AF→PCF→SMF-notify→ApplyPccRules 通,最后 PFCP+N1N2 被 SMF bug 挡住(stock panic / fork URR dup)
- gNB:未收到任何 QoS flow modify(QoS 没下发到 RAN)

## 6. 下一步建议(暂停后再续)

**推荐转方案 A(SMF 外挂)**:给 SMF 加 `POST /qos/v1/update` 端点,**复用 SMF 内部已验证可用的 QoS flow modify 路径**(UE 正常 PDU session establish 时走的 `Install PCCRule`→`BuildEstReq/ModReq`→PFCP→NGAP setup,这条是好的),**绕开 PCF 生成 PCC rule 这条坏链**。

理由:
- UE 正常建会话时 SMF 的 PFCP+NGAP 全通(903 建会话日志可证)
- 坏的是"PCF 从 AF app-session 生成的 PCC rule"喂给 `ApplyPccRules` 那段(stock panic / fork URR dup)
- 方案 A 自定义端点直接调内部好的那段,跳过坏的 PCF 生成环节
- 代价:需重建 SMF(加端点),但这比继续修 free5gc 的 PCF→SMF-apply 连续 bug 更可控

备选:继续 debug fork SMF 的 `Duplicate URR creation`(再一个 SMF 改动),但free5gc 这条链 bug 多,收益不确定。

## 7. 相关 commit

- `6a28679` 方案B afenforcer+routerenforcer+mockpcf(阶段2)
- `c2ff7b8` afenforcer 适配真 PCF 3GPP 格式(阶段3,PCF 201 接受)
- `e4ccd13` 阶段3 真 PCF 联调记录(stock SMF panic 卡点)
- 本记录:fork SMF 测试(panic 修了但 URR dup,回退暂停)

## 8. 构建/复现备忘

- 重建 fork SMF:`docker build -f /home/core/free5gc-compose/nf_smf/Dockerfile.build -t free5gc/smf:fork /home/core/free5gc-compose/base/free5gc/NFs/smf`(需 goproxy.cn 拉私有 deps)
- 切 fork:compose smf `image: free5gc/smf:fork`,`docker-compose up -d free5gc-smf`
- 回退:compose smf `image: free5gc/smf:v4.2.1`
