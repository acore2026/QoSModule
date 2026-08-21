package smfenforcer

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"sync"
	"time"

	adaptiveqos "github.com/acore2026/adaptive-qos"
)

// Enforcer pushes QoS to the core network by calling the fork SMF OAM endpoint
// POST /nsmf-oam/v1/qos-update (方案 A). The SMF resolves the PDU session by
// UE IP, installs a QER on the UPF via PFCP and sends a PDU Session Resource
// Modify Request to the AMF, which forwards it as NGAP to the gNB.
//
// After the burst window (plus grace), the enforcer calls
// /nsmf-oam/v1/qos-release to remove the QoS flow (SMF: RemoveQosFlow + PFCP
// QER removal + NGAP modify release list), so the DRB is torn down and the
// resources reclaimed. The MASQUE reply is normalized to a uniform
// {request_id,status,error_code,message} shape.
type Enforcer struct {
	cfg    Config
	client *client
	logger *log.Logger

	mu     sync.Mutex
	active map[string]releaseTarget // request_id -> release info
}

// releaseTarget records what a scheduled release needs to call /qos-release.
type releaseTarget struct {
	ueIP string
	qfi  uint8
}

// New builds an SMF enforcer from cfg.
func New(cfg Config) *Enforcer {
	if cfg.DefaultFiveQI == 0 {
		cfg.DefaultFiveQI = 2
	}
	if cfg.EndGrace <= 0 {
		cfg.EndGrace = 2 * time.Second
	}
	return &Enforcer{
		cfg:    cfg,
		client: newClient(cfg),
		logger: cfg.Logger,
		active: make(map[string]releaseTarget),
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
	RequestID string  `json:"request_id"`
	UEIP     string  `json:"ue_ip"`
	QFI      uint8   `json:"qfi"`
	FiveQI   uint8   `json:"five_qi"`
	MbrUL    string  `json:"mbr_ul"`
	MbrDL    string  `json:"mbr_dl,omitempty"`
	GbrUL    string  `json:"gbr_ul,omitempty"`
	GbrDL    string  `json:"gbr_dl,omitempty"`
	Arp      oamARP  `json:"arp"`
}

// oamQoSReleaseRequest is the body of POST /nsmf-oam/v1/qos-release. The SMF
// resolves the SMContext by ue_ip and calls RemoveQosFlow(qfi).
type oamQoSReleaseRequest struct {
	RequestID string `json:"request_id"`
	UEIP     string `json:"ue_ip"`
	QFI      uint8  `json:"qfi"`
}

// masqueFeedback mirrors masqueapi.Feedback so the UDP reply to the MASQUE
// client is in a uniform {request_id,status,error_code,message} shape,
// regardless of the downstream SMF response body.
type masqueFeedback struct {
	RequestID string `json:"request_id,omitempty"`
	Status    string `json:"status"`
	ErrorCode string `json:"error_code,omitempty"`
	Message   string `json:"message,omitempty"`
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

// burstDurationMs returns the burst window to keep the QoS flow alive before
// releasing it. UL/DL burst duration takes precedence; falls back to e2e delay
// then 1s so a flow is never left installed forever by a malformed intent.
func burstDurationMs(intent adaptiveqos.Intent) uint64 {
	d := intent.ULBurst.DurationMS
	if intent.DLBurst.DurationMS > d {
		d = intent.DLBurst.DurationMS
	}
	if d == 0 {
		d = intent.E2EDelayMS
	}
	if d == 0 {
		d = 1000
	}
	return d
}

// buildOAMBody assembles the oamQoSUpdateRequest JSON for the fork SMF.
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

func (e *Enforcer) buildReleaseBody(requestID, ueIP string, qfi uint8) []byte {
	body, _ := json.Marshal(oamQoSReleaseRequest{RequestID: requestID, UEIP: ueIP, QFI: qfi})
	return body
}

// feedback normalizes an ApplyResult into the uniform MASQUE reply payload.
func (e *Enforcer) feedback(requestID string, result adaptiveqos.ApplyResult) []byte {
	fb := masqueFeedback{
		RequestID: requestID,
		Status:    result.Status,
		ErrorCode: result.ErrorCode,
		Message:   result.Message,
	}
	if fb.Status == "" {
		if result.HTTPStatus >= 200 && result.HTTPStatus < 300 {
			fb.Status = adaptiveqos.StatusAccepted
		} else {
			fb.Status = adaptiveqos.StatusRejected
		}
	}
	b, _ := json.Marshal(fb)
	return b
}

// Apply posts the OAM QoS update to the SMF, normalizes the reply into a
// MASQUE feedback, and on success schedules a release after the burst window
// so the QoS flow does not linger on the SMContext/UPF/gNB.
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
	result := adaptiveqos.ApplyResult{HTTPStatus: status}
	if err != nil {
		result.Status = adaptiveqos.StatusRejected
		result.ErrorCode = "SMF_UNREACHABLE"
		result.Message = err.Error()
		result.RawResponse = e.feedback(intent.RequestID, result)
		return result, err
	}

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

	// Normalize the reply: MASQUE clients always see {request_id,status,
	// error_code,message}, never the SMF-private OAM/QoS body.
	result.RawResponse = e.feedback(intent.RequestID, result)

	// Schedule release after the burst window so the flow is reclaimed.
	if status >= 200 && status < 300 && result.Status == adaptiveqos.StatusAccepted {
		e.mu.Lock()
		e.active[intent.RequestID] = releaseTarget{ueIP: intent.Flow.UEAddress, qfi: intent.Flow.QFI}
		e.mu.Unlock()
		e.scheduleTerminate(intent.RequestID, intent.Flow.UEAddress, intent.Flow.QFI, burstDurationMs(intent))
	}

	if e.logger != nil {
		e.logger.Printf("smf enforcer apply request_id=%s ue_ip=%s qfi=%d five_qi=%d mbr_ul=%d mbr_dl=%d gbr_ul=%d gbr_dl=%d http=%d status=%s amf_cause=%s burst_ms=%d",
			intent.RequestID, intent.Flow.UEAddress, intent.Flow.QFI, e.cfg.DefaultFiveQI,
			decision.MBRULKbps, decision.MBRDLKbps, decision.GBRULKbps, decision.GBRDLKbps,
			status, result.Status, resp.AMFCause, burstDurationMs(intent))
	}
	return result, nil
}

// scheduleTerminate releases the QoS flow after the burst window plus grace.
// It reuses the enforcer's own client; if a new intent for the same
// request_id replaced the active target, the release is skipped.
func (e *Enforcer) scheduleTerminate(requestID, ueIP string, qfi uint8, burstMS uint64) {
	if e == nil || e.client == nil || e.client.releaseEndpoint == "" || burstMS == 0 {
		return
	}
	delay := time.Duration(burstMS)*time.Millisecond + e.cfg.EndGrace
	time.AfterFunc(delay, func() {
		e.mu.Lock()
		if _, ok := e.active[requestID]; !ok {
			e.mu.Unlock()
			return
		}
		delete(e.active, requestID)
		e.mu.Unlock()

		ctx, cancel := context.WithTimeout(context.Background(), e.cfg.Timeout)
		defer cancel()
		body := e.buildReleaseBody(requestID, ueIP, qfi)
		status, raw, err := e.client.postQoSRelease(ctx, body)
		if e.logger != nil {
			if err != nil {
				e.logger.Printf("smf enforcer release failed request_id=%s ue_ip=%s qfi=%d err=%v", requestID, ueIP, qfi, err)
			} else {
				e.logger.Printf("smf enforcer released request_id=%s ue_ip=%s qfi=%d http=%d bytes=%d", requestID, ueIP, qfi, status, len(raw))
			}
		}
	})
}
