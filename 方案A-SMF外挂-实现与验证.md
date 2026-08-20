# 方案 A(SMF 外挂)实现与验证总结

> 文档状态：方案 A 的真实联调记录。SMF 端改造位于外部 `acore2026/smf` 仓库；本 QoSModule 仓库已实现 `smfenforcer`（`adaptiveqos/smfenforcer/`）并接入 `ngap`/`auto` 模式（`-smf-endpoint`），已通过 Target UDP 触发端到端验证到 gNB 建 DRB。原“独立接口验证”已升级为“Target 全链验证”。原 AF/PCF 路径（方案 B，`afenforcer`）已删除。

> 时间:2026-08-07
> 目标:对当前封闭 gNB,让 QoS 下发真正让 RAN 动态调整某 UE 的 QoS。
> 结论:**方案 A 全链跑通,gNB 建立 DRB(QFI=5/5QI=2 GBR)**。绕开 free5gc PCF 生成 PCC rule 的坏链(方案 B 卡在此)。

---

## 1. 为什么从方案 B 转方案 A

方案 B(PCF/AF 外挂)在 free5gc 里撞连续 bug:
- stock SMF `ApplyPccRules` nil QosData panic
- fork SMF `Duplicate URR creation` 挡住 PFCP+N1N2
- 都是"PCF 从 AF app-session 生成 PCC rule 喂给 SMF apply"这条坏链上的问题,AF 侧改不动

方案 A(SMF 外挂):给 SMF 加自定义端点,**直接复用 SMF 内部已验证可用**的 QoS flow modify 路径(UE 建会话时走的那条 AddQosFlow→PFCP→BuildModifyTransfer→N1N2),**绕开 PCF 生成 PCC rule 的环节**,一路到 gNB。

## 2. 修改内容(全部在 fork SMF,acore2026/smf)

| 文件 | 改动 |
|---|---|
| `internal/context/sm_context.go` | 加 `GetSMContextByPDUAddress(ueIP string) *SMContext`:Range `smContextPool` 按 `PDUAddress`(UE IP)找会话(原有只有 ByRef/ById/BySEID,缺按 UE IP) |
| `internal/sbi/processor/oam_qos.go`(新) | `HandleOAMQoSUpdate`:① 按 ue_ip 查 SMContext ② 构造 `QosData`(Var5qi/MaxbrUl-Dl/GbrUl-Dl/Arp) ③ `AddQosFlow(qfi,qos)` ④ `dataPath.AddQoS`(建 QER) ⑤ `ActivateUPFSession`(发 PFCP Session Modification 给 UPF) ⑥ `BuildPDUSessionResourceModifyRequestTransfer`(构建 N2 modify) ⑦ `N1N2MessageTransfer` 发 AMF;含 bitrate 单位校验(防 `StringToBitRate`/`BitRateTokbps` panic) |
| `internal/sbi/api_oam.go` | 加路由 `POST /nsmf-oam/v1/qos-update` + `HTTPOAMQoSUpdate` |

构建:`free5gc/smf:fork`(`nf_smf/Dockerfile.build`,`CGO_ENABLED=0` 静态,alpine 基;仿 AMF buildv16)。compose smf 标签切 `free5gc/smf:fork`。

## 3. 验证结果(2026-08-07 06:56,UE imsi-001012345678903)

`restart-all.sh` 全量重启核心网 + UE 重新入网(pdu_session_id=1, UE IP 10.60.0.1)后,一次干净请求:

```
POST http://<smf>:8000/nsmf-oam/v1/qos-update
{"ue_ip":"10.60.0.1","qfi":5,"five_qi":2,
 "mbr_ul":"9600000 bps","mbr_dl":"24000000 bps",
 "gbr_ul":"100000 bps","gbr_dl":"100000 bps",
 "arp":{"priority":8,"preempt_cap":"MAY_PREEMPT","preempt_vuln":"NOT_PREEMPTABLE"}}
```

| 环节 | 证据 |
|---|---|
| ① SMF 端点 | 200 OK,`status:ACCEPTED, amf_cause:N1_N2_TRANSFER_INITIATED` |
| ② SMF PFCP→UPF(QER) | `Sending/Received PFCP Session Modification Request/Response` |
| ③ SMF→AMF N1N2 | AMF `Handle N1N2 Message Transfer Request` (200) |
| ④ AMF→gNB NGAP modify | gNB `Handle PDUSessionResourceModifyResponse` (RAN UE NGAP ID 239) |
| ⑤ gNB 建 DRB | gNB `l2appbh.log: RoHC disabled for DRB 5`(DRB 5 = QFI 5 的承载) |

**gNB 收到 NGAP `PDUSessionResourceModifyRequest` 并建立了 DRB 5**——RAN 动态调整了该 UE 的 QoS(5QI=2 GBR,可抢占)。

## 4. 关键技术点

- **寻址**:用 UE IP(`GetSMContextByPDUAddress`),不用 SUPI/RNTI(详见 NGAP下发改造方案.md §6.1)
- **bitrate 必须带单位**(`9600000 bps`):SMF 的 `util.StringToBitRate`/`BitRateTokbps` 按 `strings.Split(s," ")` 取 `s[1]` 当单位,无单位会 panic 并污染 SMContext。handler 加了单位校验
- **绕开 PCF**:不复用 `ApplyPccRules`(那条对 AF 驱动 PCC rule 有 panic/URR dup bug),直接 `AddQosFlow`+`AddQoS`+`ActivateUPFSession`+`BuildPDUSessionResourceModifyRequestTransfer`+`N1N2MessageTransfer`
- **fork SMF 源码**:acore2026/smf(含上游修复,如 `fix/float-bitrate-causing-zero-MBR-GBR`),静态构建(原预编译二进制是 glibc 动态链接,alpine 跑不了)

## 5. 对比

| | 方案 B(PCF/AF) | **方案 A(SMF 外挂)** |
|---|---|---|
| 改核心网 | 0(但撞 free5gc PCF/SMF bug) | 改 SMF(加端点) |
| 到 gNB | ❌(panic/URR dup) | ✅(NGAP modify + DRB) |
| 链路 | AF→PCF→SMF-apply(坏) | QoS模块→SMF 端点→内部好的路径 |

## 6. 待办/可改进

- **UPF DL 标 QFI**:当前 PFCP QER 已发,但 DL 流量是否按新 QFI 标记待用户面 iperf 验证(拥塞下 GBR 保障实测)
- **N1 NAS QoS 规则**:当前只发 N2(gNB 建 DRB),UE 侧 NAS QoS 规则未发(UL 流量分类);如需 UL 保障,补 N1
- **突发 end 拆 flow**:QoS 模块侧再 POST 一次带 `release` 标志或新端点拆 QFI(当前 SMF 端点只 add)
- **SCTP 稳定性**:docker-proxy 转发 SCTP 不稳(AMF 跑久会卡死),建议 AMF 改 host 网络或直绑 10.88.120.100

## 7. 相关 commit

- 方案 B(对比/前置):`6a28679`(afenforcer+routerenforcer+mockpcf)、`c2ff7b8`(真 PCF 格式)、`e4ccd13`/`a2ca387`(阶段3/fork SMF 记录)
- 方案 A(SMF 侧):本批——acore2026/smf(`GetSMContextByPDUAddress`+`HandleOAMQoSUpdate`+`/qos-update` 路由+校验)、free5gc-compose(`nf_smf/Dockerfile.build` 已 `5fe1acd`、compose smf:fork)
