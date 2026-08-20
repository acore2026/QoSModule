package smfenforcer

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strconv"

	adaptiveqos "github.com/acore2026/adaptive-qos"
)

// Enforcer pushes QoS to the core network by calling the fork SMF OAM endpoint
// POST /nsmf-oam/v1/qos-update (方案 A). The SMF resolves the PDU session by
// UE IP, installs a QER on the UPF via PFCP and sends a PDU Session Resource
// Modify Request to the AMF, which forwards it as NGAP to the gNB.
//
// Unlike the AF/PCF path, the SMF addresses the session by ue_ip, so no
// SUPI map is required and there is no app-session to terminate afterwards
// (the SMF endpoint currently only adds a QoS flow; release is a TODO).
type Enforcer struct {
	cfg    Config
	client *client
	logger *log.Logger
}

// New builds an SMF enforcer from cfg.
func New(cfg Config) *Enforcer {
	if cfg.DefaultFiveQI == 0 {
		cfg.DefaultFiveQI = 2
	}
	return &Enforcer{
		cfg:    cfg,
		client: newClient(cfg),
		logger: cfg.Logger,
	}
}

// oamARP mirrors the arp object of oamQoSUpdateRequest in acore2026/smf.
type oamARP struct {
	PriorityLevel int32  `json:"priority"`
	PreemptCap    string `json:"preempt_cap"`
	PreemptVuln   string `json:"preempt_vuln"`
}

// oamQoSUpdateRequest mirrors the body of POST /nsmf-oam/v1/qos-update in
// acore2026/smf (internal/sbi/processor/oam_qos.go). Bitrate strings must
// carry a " bps" unit suffix or the SMF StringToBitRate parser panics.
type oamQoSUpdateRequest struct {
	RequestID string `json:"request_id"`
	UEIP     string `json:"ue_ip"`
	QFI      uint8  `json:"qfi"`
	FiveQI   uint8  `json:"five_qi"`
	MbrUL    string `json:"mbr_ul"`
	MbrDL    string `json:"mbr_dl,omitempty"`
	GbrUL    string `json:"gbr_ul,omitempty"`
	GbrDL    string `json:"gbr_dl,omitempty"`
	Arp      oamARP `json:"arp"`
}

func kbpsToBps(kbps uint64) uint64 {
	if kbps == 0 {
		return 0
	}
	v := kbps * 1000
	if v < kbps { // overflow guard
		return ^uint64(0)
	}
	return v
}

// bitrateBps formats a kbps value as the "N bps" string the SMF parser expects.
// Returns "" for 0 so the field stays omitempty (no QER limit on that dir).
func bitrateBps(kbps uint64) string {
	if kbps == 0 {
		return ""
	}
	return strconv.FormatUint(kbpsToBps(kbps), 10) + " bps"
}

func preemptCapString(b bool) string {
	if b {
		return "MAY_PREEMPT"
	}
	return "NOT_PREEMPT"
}

func preemptVulnString(b bool) string {
	if b {
		return "PREEMPTABLE"
	}
	return "NOT_PREEMPTABLE"
}

// buildOAMBody assembles the oamQoSUpdateRequest JSON for the fork SMF.
// ue_ip comes from the intent (the MASQUE request's source UE address); the
// SMF resolves the SMContext by PDU address. qfi is forwarded as-is (0 lets
// the SMF auto-assign). Bitrates come from the policy decision (kbps→bps).
func (e *Enforcer) buildOAMBody(intent adaptiveqos.Intent, decision adaptiveqos.Decision) ([]byte, error) {
	req := oamQoSUpdateRequest{
		RequestID: intent.RequestID,
		UEIP:      intent.Flow.UEAddress,
		QFI:       intent.Flow.QFI,
		FiveQI:    e.cfg.DefaultFiveQI,
		MbrUL:     bitrateBps(decision.MBRULKbps),
		MbrDL:     bitrateBps(decision.MBRDLKbps),
		GbrUL:     bitrateBps(decision.GBRULKbps),
		GbrDL:     bitrateBps(decision.GBRDLKbps),
		Arp: oamARP{
			PriorityLevel: int32(e.cfg.ARP.PriorityLevel),
			PreemptCap:    preemptCapString(e.cfg.ARP.PreemptCap),
			PreemptVuln:   preemptVulnString(e.cfg.ARP.PreemptVuln),
		},
	}
	return json.Marshal(req)
}

// Apply posts the OAM QoS update to the SMF and normalizes the response into
// an ApplyResult. A 2xx with status "ACCEPTED" lets the router treat the
// enforcement as successful; anything else falls through as REJECTED.
func (e *Enforcer) Apply(ctx context.Context, intent adaptiveqos.Intent, decision adaptiveqos.Decision) (adaptiveqos.ApplyResult, error) {
	if e == nil || e.client == nil || e.cfg.SMFEndpoint == "" {
		return adaptiveqos.ApplyResult{}, fmt.Errorf("SMF endpoint is not configured")
	}
	if intent.Flow.UEAddress == "" {
		return adaptiveqos.ApplyResult{}, fmt.Errorf("intent.UEAddress (UE IP) is empty; SMF resolves the PDU session by ue_ip")
	}

	body, err := e.buildOAMBody(intent, decision)
	if err != nil {
		return adaptiveqos.ApplyResult{}, fmt.Errorf("encode SMF OAM request: %w", err)
	}

	status, raw, err := e.client.postQoSUpdate(ctx, body)
	if err != nil {
		return adaptiveqos.ApplyResult{HTTPStatus: status, RawResponse: raw}, err
	}

	result := adaptiveqos.ApplyResult{HTTPStatus: status, RawResponse: raw}
	var resp qosUpdateResponse
	_ = json.Unmarshal(raw, &resp)
	result.Status = resp.Status
	result.ErrorCode = resp.Cause
	result.Message = resp.Detail
	if result.Status == "" {
		if status >= 200 && status < 300 {
			result.Status = adaptiveqos.StatusAccepted
		} else {
			result.Status = adaptiveqos.StatusRejected
		}
	}

	if e.logger != nil {
		e.logger.Printf("smf enforcer apply request_id=%s ue_ip=%s qfi=%d five_qi=%d mbr_ul=%d mbr_dl=%d gbr_ul=%d gbr_dl=%d http=%d status=%s amf_cause=%s",
			intent.RequestID, intent.Flow.UEAddress, intent.Flow.QFI, e.cfg.DefaultFiveQI,
			decision.MBRULKbps, decision.MBRDLKbps, decision.GBRULKbps, decision.GBRDLKbps,
			status, result.Status, resp.AMFCause)
	}
	return result, nil
}
