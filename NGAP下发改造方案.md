# QoS 模块双模下发改造方案

> 目标:**保留**原有 gNB-HTTP 下发路径(给支持 `/api/v1/qos/update` 的其他基站),**新增** NGAP 下发路径(给当前这台封闭基站),两条路径在同一个 QoS 模块内**并存**,按基站能力切换。
> 原则:gNB-HTTP 路径的代码与线缆格式**原样保留不动**(不加 5QI、不改 model/policy);NGAP 路径的所有改动**隔离在新增 Enforcer 包内**。

---

## 1. 两种基站,两条路径

| 基站类型 | 是否支持 `POST /api/v1/qos/update` | 下发路径 | 模块状态 |
|---|---|---|---|
| 其他基站(软件/开放 gNB) | ✅ 支持 | **gNB-HTTP 直连**(`ranapi.Client`) | 已有,原样保留 |
| 当前基站(封闭厂商 BBU) | ❌ 不支持 | **NGAP 经核心网**(`smfenforcer` 新增) | 待实现 |

切换由配置 `mode`(`ran`/`ngap`/`auto`)决定,程序内用 `RouterEnforcer` 分发,见 §5。

---

## 2. 当前基站详细情况(给 NGAP 开发参考)

> 开发 NGAP 路径前必须搞清这台基站的接口,以确认"只能走 NGAP、不能走 HTTP/直连"。

### 2.1 基本信息

| 项 | 值 |
|---|---|
| 地址 | `10.88.120.212`(eth1 网段,AMF 在 `10.88.120.100`) |
| 登录 | `root`,已授权本机公钥(`ssh -i ~/.ssh/id_ed25519 root@10.88.120.212`) |
| hostname | localhost |
| 硬件 | NXP Layerscape **ARM64(aarch64)** BBU |
| 内核 | `4.19.68-241.00.2_N78_20240613_v2.5.5`(N78 频段,2024-06-13 构建) |
| 系统 | NXP LSDK 2004 main |
| 软件版本 | `5GNR_t.fa.tdd.fr1.2.2.3.F2_826_20241204_051423`(TDD / FR1 / F2 band,2024-12-04 构建) |
| 安装路径 | `/opt/loads/5GNR_t.fa.tdd.fr1.2.2.3.F2_826_20241204_051423/rootfs/bts/bin/`、`/opt/bbu/` |

### 2.2 gNB 协议栈进程(经典 split)

| 进程 | 角色 | 关键启动参数 |
|---|---|---|
| `cpgnbapp` | **CU-CP**:NGAP/X2-CP | `-localIntGnbNgCpIpAddr`/`-localExtGnbNgCpIpAddr`(=NG 控制面,即 NGAP)、`-backhaulIpAddress 192.0.2.2` |
| `cpcellapp` | 每小区 RRC | `-nameServerIP 192.0.2.1`、`-callp_mac_intf_type udp` |
| `cpupproxy` | CP-UP 代理(PDCP 到核心) | `-pdcpCoreIpAddress 192.0.2.2`、`-pdcpCtrlMsgDstPort 4381` |
| `upapp` | **UP**:PDCP/N3 用户面 | `-pdcpCoreIpAddress 192.0.2.2`、`-l2IpAddress1 192.0.2.10` |
| `duapp` | **DU**:MAC/RLC 调度器 | `-mac_phy_intf_type intel`、`-callp_mac_intf_type udp`、`-l2CpuInstance 1` |
| `oamProcess` | OAM(网管) | 监听 7547/8040/27149/27167 |
| `thttpd` | OAM Web/CGI | `-C /opt/bbu/oam/web/thttpd.conf`,端口 8400 |
| `apache2` | 默认 Web | 端口 80(仅默认页) |

### 2.3 监听端口(全机)

| 端口 | 协议 | 进程 | 用途 | 可用性 |
|---|---|---|---|---|
| 22 | tcp | sshd | SSH 管理 | ✅ 已登录 |
| 80 | tcp | apache2 | **默认 Apache 页,无 API** | ✗ `POST /api/v1/qos/update` 返回 404 |
| 8400 | tcp | thttpd | **OAM Web UI + CGI 分发器** | OAM,需 login.cgi 会话 |
| 7547 | tcp | oamProcess | **TR-069 CWMP**(XML/SOAP) | OAM,XML |
| 8040 | tcp | oamProcess | OAM(本地) | 本地 |
| 27149/27167 | tcp | oamProcess | OAM 内部 | 本地 |
| 514 | udp | rsyslog | syslog | — |
| 2947 | tcp | gpsd | GPS | — |
| 38412 | sctp | cpgnbapp | **NGAP**(gNB 作客户端外联 AMF,已 ESTAB) | ✅ 标准控制面 |
| NETCONF | — | (YANG 模型在 `/opt/bbu/oam/netconf/yang`) | 外部端口未确认 | 未开放 |

### 2.4 OAM CGI 接口(thttpd:8400)

所有 `*.cgi` 都是指向 `web_main.cgi`(单分发器 ELF,未 strip)的符号链接。请求格式:
```
POST /public/cgi-bin/<name>.cgi   { "set":"<op>", ...data }   或   { "get":"<op>" }
```
均需先经 `login.cgi` 建会话。

| CGI | 主要 set/get 操作 | 范围 |
|---|---|---|
| `qos.cgi` | `set_QoS_info`、`set_QoSIndex_list`、`set_SinrToMcsTableQos_list`、`set_Slice_info`、`set_PLMN_info`、`set_NGC_info` | **小区/网络级静态 QoS profile**(Default5QI、QoS index、SINR→MCS 表、切片 QoS) |
| `ran.cgi` | `set_MAC_info`(PUCCH/PUSCH/PDCCH/PRACH)、`set_McsOffsetTableBe/Voip_info`、`set_RadioBearParam_list`、`set_PDCP_list`、`set_RLC_list`、`set_PHY_info`、`set_BWP`、`set_SIB`、`set_UAC`、`set_DRX` | **小区级无线/MAC/PDCP/RLC 配置** |
| `rate.cgi` | `get_mac_rate_list`、`get_pdcp_rate_list`(**仅 get**) | **每 UE 速率只读监控**,无 set |
| `phycell.cgi`/`mobility.cgi`/`son.cgi`/... | 见名 | 小区级配置 |

OAM 里相关常量(可 set,但都是**小区级静态**):`GbrDl/UlThreshold`、`MaxBitRate`、`MaximumDataBurstVolume(MDBV)`、`Default5QI`、`Max5QIEntries`、`DeltaMCS`、`Mcs_0..21`、`CrntiTimerRelease`、`LCH.PrioritisedBitRate`。

### 2.5 duapp 内部调度接口(不可调用)

`duapp`(pid 示例 7659)的内部通信全是**厂商私有二进制协议**,无文档、无 CLI、无可注入命令式 API:

| 通道 | 详情 | 评估 |
|---|---|---|
| UDP | `0.0.0.0:54003`(外,MAC CP 接口)、`127.0.0.1:27144/27148/27460`(内) | 私有二进制,发探测无响应 |
| FIFO | `/tmp/bh0-rlc1.{pdfifo,pgfifo,sdfifo,sgfifo}` | RLC 数据管道 |
| 共享内存 | `/dev/shm/scshared_root`、`/dev/shm/pmshared_root/LteL2App_1`、SysV `NTP0..7`(96B) | 调度状态可能在此,**布局无头文件,盲写必崩 BBU** |
| 设备 | `/dev/fsm-tti`(TTI tick)、`/dev/fsm-dp` | 内核驱动,只读时序 |
| strings | 无 `setMcs/setQos/perUe/inject/debugcli` 等命令式关键词 | 无 CLI/调试口 |

### 2.6 当前基站结论(给开发的硬约束)

1. **没有 `POST /api/v1/qos/update`**:端口 80 是 Apache 默认页,探测返回 404。→ gNB-HTTP 路径对这台基站**不可用**。
2. **OAM CGI 是小区级静态配置**,改了通常要 cell 重激活(会断 UE),且非每 UE 每 QFI 动态;`rate.cgi` 只读。→ **不能用于 per-UE 实时不断连 QoS 下发**。
3. **duapp 内部接口私有**,直连调度器需逆向 + 写活共享内存,风险过高。→ **不走直连**。
4. **唯一可用**的是标准 **NGAP**(SCTP 38412,已和 AMF 建联),`PDUSessionResourceModifyRequest` 可对在线 UE per-UE、实时、不断连下发 QoS flow。→ **当前基站必须走 NGAP**。

> 其他基站若支持 `/api/v1/qos/update`,则继续用 `ranapi.Client` 走 gNB-HTTP,**与 NGAP 路径并存**。

---

## 3. 现状架构与接缝

```
MASQUE Proxy ──► masqueapi.Decode ──► Intent ──► Processor
                                         │
                                         ├─ Policy(BurstPolicy) ─► Decision(MBR/GBR/PDB/Priority)
                                         └─ Enforcer.Apply(ctx, Intent, Decision)
                                                 ▲
                                                 │ 当前唯一实现:ranapi.Client(gNB-HTTP)
                                                   缺:NGAP Enforcer + RouterEnforcer
```

关键代码位置:

| 文件 | 位置 | 作用 | 改动 |
|---|---|---|---|
| `adaptiveqos/model.go:105` | `Enforcer` 接口 `Apply(ctx, Intent, Decision) (ApplyResult, error)` | **接缝** | 不动 |
| `adaptiveqos/model.go:10` | `FlowSelector{RNTI, QFI, UEAddress, SEID}` | 流匹配;**已含 UEAddress/SEID** | 不动(5QI 不加这里) |
| `adaptiveqos/model.go:77` | `QoSValues{MBRUL/DL/UL/DL Kbps, PDBMS, Priority}` | **缺 5QI**,但 gNB-HTTP 不需要 | 不动 |
| `adaptiveqos/policy.go:49` | `BurstPolicy.Generate`(`pdbFromBudget`@`policy.go:132`) | 反推 MBR/GBR/PDB | 不动(5QI 派生放 NGAP Enforcer 内) |
| `adaptiveqos/processor.go:20` | `Processor.Process` | 管线 | 不动 |
| `adaptiveqos/ranapi/client.go:108` | `ranapi.Client.Apply` | gNB-HTTP Enforcer | **原样保留** |
| `adaptiveqos/ranapi/client.go:28` | `ranapi.Request`(rnti/mcs/rb/bler/burst) | gNB 线缆格式 | **原样保留,不加 5QI** |
| `adaptiveqos/masqueapi/request.go` | `masqueapi.Request{...,PacketFilter,SourceAddress,ServiceInfo}` | 输入已含 SDF/UE IP/E2EDelay | 不动(PacketFilter 可由 NGAP Enforcer 直接从原始请求读) |
| `target/target/qos_handler.go:49` | `Enforcer: ranClient`(只绑一个) | 装配 | 改成装配 `RouterEnforcer` |

---

## 4. 双模设计(共存,不是三选一)

两条路径**都留在程序里**,由 `RouterEnforcer` 按配置分发;既有 `ranapi.Client` 和管线**全不动**。

```
                    ┌─ ranapi.Client      (gNB-HTTP /api/v1/qos/update)  ← 保留原样
Intent+Decision ──► RouterEnforcer ─┤
                    └─ smfenforcer        (NGAP via SMF→AMF→gNB)        ← 新增,隔离
```

### 4.1 gNB-HTTP 路径(保留,原样)

- Enforcer:`ranapi.Client`(`client.go:108`),不动
- 线缆:`ranapi.Request`(rnti/qfi/mcs/rb/bler/burst/mbr/gbr + mask),不动,**不加 5QI**
- `mask`/`AutomaticMask`(`client.go:276`)逻辑保留
- 适用:支持 `/api/v1/qos/update` 的基站

### 4.2 NGAP 路径(新增,隔离)

- 新增包 `adaptiveqos/smfenforcer/`,实现 `Enforcer` 接口
- **5QI 派生关在 NGAP Enforcer 内**:由 `Decision.PDBMS` 或 `ServiceInfo.service_type`→5QI 映射得出,**不改 `QoSValues`/`policy.go`**
- **SUPI 解析关在 NGAP Enforcer 内**:由 `Intent.Flow.UEAddress`(UE IP)调 AMF `GET /namf-oam/v1/registered-ue-context` 反查 SUPI + pduSessionId;或用 `SEID` 映 SMF SM context
- 调 SMF 自定义端点(或 PCF PolicyAuthorization),详见 §6
- 适用:当前封闭基站

### 4.3 RouterEnforcer(新增,分发)

- 持有 `ranEnforcer` + `ngapEnforcer` + `mode`
- `mode`:
  - `ran` → 只走 gNB-HTTP(其他基站)
  - `ngap` → 只走 NGAP(当前基站)
  - `auto` → 优先 gNB-HTTP,不可达/REJECTED 则 fallback NGAP(不确定基站能力)
- `QoSHandler` 改成装配 `RouterEnforcer`(替换单一 `ranClient`)

### 4.4 字段归属与冲突避免

单模式(`ran`/`ngap`)各自完整下发,无重叠。`auto` 互斥(同一请求只走一条),无冲突。
若未来要"NGAP 建 QoS flow + gNB-HTTP 叠加 RAN-internal"互补模式(`split`),用现有 `mask` 切分:NGAP 下发 QFI/5QI/MBR/GBR/ARP,gNB-HTTP 的 mask 只勾 `dl/ul_max_mcs/rb/bler/smooth`+`burst_info`,不勾 `qfi/mbr/gbr/pri/cap/vul/pdb` → 不重叠。

---

## 5. 配置(双模切换)

复用现有 `-config`(JSON 文件)与 `QOS_*` 环境变量机制,加 `core.mode`:

```json
{
  "ran": { "endpoint": "http://<gNB>:<port>/api/v1/qos/update", "timeout": "3s", "mask": "auto" },
  "core": {
    "mode": "ngap",
    "smf_endpoint": "http://smf.free5gc.org:8000",
    "amf_endpoint": "http://amf.free5gc.org:8000",
    "timeout": "5s"
  }
}
```

- 当前基站:`core.mode = "ngap"`
- 其他基站:`core.mode = "ran"`
- 不确定:`core.mode = "auto"`

env:`QOS_CORE_MODE`、`QOS_CORE_SMF_ENDPOINT`、`QOS_CORE_AMF_ENDPOINT` 覆盖文件;flag `-core-mode`/`-core-url` 最高优先级。优先级:flag > env > file > 默认。

---

## 6. NGAP 路径方案选择(SMF 外挂,推荐)

经 §2 确认当前基站只能走 NGAP,NGAP 又分 AMF/SMF/PCF-AF 三种外挂:

| 维度 | A. AMF 外挂 | **B. SMF 外挂(推荐)** | C. PCF/AF 外挂 |
|---|---|---|---|
| 路径 | QoS模块→AMF→gNB(2 段) | QoS模块→SMF→AMF→gNB(3 段) | QoS模块(AF)→PCF→SMF→AMF→gNB(4 段) |
| 改 NF | AMF(已自建) | SMF(需重建) | **0 改动**(stock PCF 原生) |
| 端到端 | ⚠️ 仅 gNB DRB,UPF/UE 不配 → QoS 不生效 | ✅ UPF+gNB+UE 全配 | ✅ 全链 |
| 标准 | 非标准 | 标准 | 标准(AF 驱动) |

> **不推荐 AMF 外挂**:只配 gNB,UPF/UE 不配,新 QoS flow 在空口建了但流量进不去。**真 QoS 必须走 SMF 或 PCF/AF**。
> 推荐短期 **SMF 外挂**(端到端、补字段少),长期 **PCF/AF 外挂**(免 NF 重建)。

### NGAP Enforcer 最小必填字段(基于实际代码)

| 必填 | 来源 | 现有代码是否有 |
|---|---|---|
| SUPI | `Intent.Flow.UEAddress`(UE IP)→SMF 解析 / `SEID` | △ 需 enforcer 内解析,详见 §6.1 |
| pduSessionId | SUPI 解析 / `SEID` 映 SMF | △ 需 enforcer 内解析 |
| QFI | `Intent.Flow.QFI` | ✅ |
| 5QI | `Decision.PDBMS`/`ServiceInfo` 派生 | △ enforcer 内派生(不入 model) |
| ARP pri/cap/vuln | `Decision.Priority` + `ranapi.RequestDefaults`(QCap/QVul) | △ cap/vuln 需从 defaults 带入 enforcer |
| MBR-UL/DL、GBR-UL/DL | `Decision.MBRULKbps/DL`、`Decision.GBRULKbps/DL` | ✅(单位 kbps↔NGAP BitRate 换算) |

gNB-HTTP 路径独有的 `mcs/rb/bler/smooth/burst`/`rnti` **NGAP 不带**,这些只在 gNB-HTTP 线缆格式里,NGAP Enforcer 不用。

### 6.1 寻址 key 分流(RNTI vs UE IP vs SUPI,无需硬转换)

RNTI 和 SUPI 分属不同域,**没有直接映射表**:

| 标识 | 谁分配 | 谁认 | 域 |
|---|---|---|---|
| C-RNTI | gNB MAC(RRC 接入时) | **只有 gNB 认** | 空口/gNB 内部 |
| SUPI | USIM/UDM | AMF/SMF/PCF | 核心网 |
| UE IP | SMF(PDU session) | SMF、UPF(PDR) | 核心网用户面 |
| SEID | SMF/UPF(PFCP) | SMF、UPF | N4 |

C-RNTI↔RAN UE NGAP ID 映射**只在 gNB 内部**,不暴露;AMF 只认 SUPI/GUTI/RAN UE NGAP ID,**不认 C-RNTI**。所以**从核心侧查不到 RNTI→SUPI**。

**结论:不要硬转 RNTI→SUPI,每条路径用它自己的寻址 key:**

| 路径 | 寻址 key | key 来源 | 定位方式 |
|---|---|---|---|
| gNB-HTTP | **RNTI** | 请求 `rnti` | gNB 内部认 |
| UPF 自适应 | **UE IP**(或 SEID) | `Intent.Flow.UEAddress`/`SEID` | UPF 按 UE IP 匹配 PDR `UEIPv4`→session(`adaptive_qos.go:1366` `resolveAdaptiveSessionLocked`)。**无需 SUPI** |
| SMF 外挂 | **UE IP** 或 **SEID** | `Intent.Flow.UEAddress`/`SEID` | SMF 持有 UE IP↔PDU session↔SUPI,解析出 SUPI+pduSessionId |
| AMF 外挂 | **SUPI** | 经 SMF 解析,或请求带 | AMF 按 SUPI→RAN UE NGAP ID |

NGAP 经 SMF 的转换流程:
```
QoS 模块(带 UEAddress=UE IP)
   ├─► SMF:"按 UE IP 找 PDU session" → 返回 SUPI + pduSessionId
   │    (SMF 内部:UE IP → PDU session → SUPI,它本来就有这张表)
   └─► SMF 用 SUPI+pduSessionId 触发 QoS flow modify → N1N2 → AMF → gNB
```

你模块的 `Intent.Flow.UEAddress`(来自 `masqueapi.SourceAddress`——MASQUE 代理在 UE 数据路径上看得到 UE 源 IP,handler 用 `ClientIP` 兜底)和 `SEID` 已备好,**不需要新增 RNTI→SUPI 转换**。

> 若上游只给 RNTI、不给 UE IP:RNTI 在核心侧**无法解析**(无 gNB 配合查不到)。解法:让 MASQUE 代理在请求里带 `source_address`(`masqueapi` 已有该字段),由 UE IP 作为跨域桥梁。

---

## 7. 当前模块是否支持 + 需补什么

| 能力 | 现状 | 要补 |
|---|---|---|
| gNB-HTTP 下发(`ranapi.Client`) | ✅ 已有 | 无,原样保留 |
| NGAP 下发 | ❌ 没有 | 新增 `smfenforcer`(或 `afenforcer`) |
| 按基站切换(双模) | ❌ `QoSHandler` 只绑一个 enforcer | 新增 `RouterEnforcer`,装配进 `qos_handler.go` |
| `mode` 配置 | ❌ | 加 `core.mode`(flag/env/file) |

改动范围:**只动 QoSModule,不改核心网代码**(除非选 SMF 外挂需重建 SMF;选 PCF/AF 则 0 NF 改动)。

---

## 8. 落地顺序

1. 实现 `smfenforcer`(NGAP Enforcer):5QI/SUPI/pduSession 解析全在本包内,调 SMF(或 PCF PolicyAuthorization)
2. 实现 `RouterEnforcer`:`ran`/`ngap`/`auto` 分发
3. `qos_handler.go` 装配 `RouterEnforcer`(替换单一 `ranClient`)
4. 加 `core.mode` 配置(flag/env/file)
5. 当前基站配 `ngap`,其他基站配 `ran`,跑通双模

gNB-HTTP 路径(`ranapi.Client`/`ranapi.Request`/`mask`)全程不改,保证其他基站继续可用。

---

## 附:5QI 参考映射(供 NGAP Enforcer 派生)

| 5QI | PDB | 类型 | 典型业务 |
|---|---|---|---|
| 1 | 100ms | GBR | 语音会话 |
| 2 | 150ms | GBR | 视频会话 |
| 5 | 100ms | non-GBR | IMS 信令 |
| 7 | 100ms | non-GBR | 语音/视频/交互 |
| 9 | 300ms | non-GBR | 默认 Internet |

> 注:PDB→5QI 反推有歧义(100ms 对应 5QI=1/5/7…),NGAP Enforcer 应优先用 `ServiceInfo.service_type` 映射,否则按 GBR/non-GBR + PDB 选标准化 5QI。
