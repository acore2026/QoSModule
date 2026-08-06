package afenforcer

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	adaptiveqos "github.com/acore2026/adaptive-qos"
)

// Enforcer pushes QoS to the core network by acting as a 3GPP AF and calling
// the PCF Npcf_PolicyAuthorization service. It is one of the enforcers wired
// behind the RouterEnforcer (mode "ngap").
type Enforcer struct {
	cfg    Config
	client *client
	logger *log.Logger

	mu     sync.Mutex
	active map[string]string // request_id -> appSessionRef
}

// New builds an AF enforcer from cfg.
func New(cfg Config) *Enforcer {
	if cfg.DefaultFiveQI == 0 {
		cfg.DefaultFiveQI = 2
	}
	if cfg.DefaultDNN == "" {
		cfg.DefaultDNN = "internet"
	}
	if cfg.DefaultSlice == (SNSSAI{}) {
		cfg.DefaultSlice = SNSSAI{SST: 1, SD: "000000"}
	}
	if cfg.EndGrace <= 0 {
		cfg.EndGrace = 2 * time.Second
	}
	return &Enforcer{
		cfg:    cfg,
		client: newClient(cfg),
		logger: cfg.Logger,
		active: make(map[string]string),
	}
}

// sdfFilter is the service data flow 5-tuple carried to the PCF.
type sdfFilter struct {
	SrcIP    string `json:"src_ip,omitempty"`
	DstIP    string `json:"dst_ip,omitempty"`
	SrcPort  uint16 `json:"src_port,omitempty"`
	DstPort  uint16 `json:"dst_port,omitempty"`
	Protocol uint8  `json:"protocol,omitempty"`
}

// appSessionRequest is the intermediate AF JSON sent to the PCF app-session
// collection. Phase 3 (real free5gc PCF联调) adapts this to the exact 3GPP
// AppSessionContextReqData shape; the field set maps 1:1 so the conversion is
// localized here.
type appSessionRequest struct {
	RequestID  string    `json:"request_id"`
	UEIP      string    `json:"ue_ip"`
	SUPI      string    `json:"supi,omitempty"`
	DNN       string    `json:"dnn,omitempty"`
	SNSSAI    SNSSAI    `json:"snssai,omitempty"`
	QFI       uint8     `json:"qfi,omitempty"`
	FiveQI    uint8     `json:"five_qi"`
	MBRUL     uint64    `json:"mbr_ul"`
	MBRDL     uint64    `json:"mbr_dl,omitempty"`
	GBRUL     uint64    `json:"gbr_ul,omitempty"`
	GBRDL     uint64    `json:"gbr_dl,omitempty"`
	ARP       ARPConfig `json:"arp"`
	SDF       *sdfFilter `json:"sdf,omitempty"`
	DurationMs uint64    `json:"duration_ms"`
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

// burstDurationMs returns the burst window to keep the QoS flow alive.
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

// buildAppSessionBody assembles the AF request from the Intent+Decision+cfg.
// Isolated so Phase 3 can swap to the real PCF wire format.
func (e *Enforcer) buildAppSessionBody(intent adaptiveqos.Intent, decision adaptiveqos.Decision) ([]byte, error) {
	ueIP := intent.Flow.UEAddress
	supi := e.cfg.resolveSUPI(ueIP)
	req := appSessionRequest{
		RequestID:  intent.RequestID,
		UEIP:      ueIP,
		SUPI:      supi,
		DNN:       e.cfg.DefaultDNN,
		SNSSAI:    e.cfg.DefaultSlice,
		QFI:       intent.Flow.QFI,
		FiveQI:    e.cfg.DefaultFiveQI,
		MBRUL:     kbpsToBps(decision.MBRULKbps),
		MBRDL:     kbpsToBps(decision.MBRDLKbps),
		GBRUL:     kbpsToBps(decision.GBRULKbps),
		GBRDL:     kbpsToBps(decision.GBRDLKbps),
		ARP:       e.cfg.ARP,
		DurationMs: burstDurationMs(intent),
	}
	if intent.Filter.Present() {
		req.SDF = &sdfFilter{
			SrcIP:    intent.Filter.SrcIP,
			DstIP:    intent.Filter.DstIP,
			SrcPort:  intent.Filter.SrcPort,
			DstPort:  intent.Filter.DstPort,
			Protocol: intent.Filter.Protocol,
		}
	}
	return json.Marshal(req)
}

func (e *Enforcer) Apply(ctx context.Context, intent adaptiveqos.Intent, decision adaptiveqos.Decision) (adaptiveqos.ApplyResult, error) {
	if e == nil || e.client == nil || e.cfg.PCFEndpoint == "" {
		return adaptiveqos.ApplyResult{}, fmt.Errorf("AF PCF endpoint is not configured")
	}
	if intent.Flow.UEAddress == "" {
		return adaptiveqos.ApplyResult{}, fmt.Errorf("intent.UEAddress (UE IP) is empty; cannot resolve SUPI")
	}
	if e.cfg.resolveSUPI(intent.Flow.UEAddress) == "" {
		return adaptiveqos.ApplyResult{}, fmt.Errorf("no SUPI mapped for UE IP %s (supi_map)", intent.Flow.UEAddress)
	}

	body, err := e.buildAppSessionBody(intent, decision)
	if err != nil {
		return adaptiveqos.ApplyResult{}, fmt.Errorf("encode AF request: %w", err)
	}

	ref, status, raw, err := e.client.createAppSession(ctx, body)
	if err != nil {
		return adaptiveqos.ApplyResult{HTTPStatus: status, RawResponse: raw}, err
	}

	result := adaptiveqos.ApplyResult{HTTPStatus: status, RawResponse: raw}
	var feedback createResponse
	_ = json.Unmarshal(raw, &feedback)
	result.Status = feedback.Status
	result.ErrorCode = feedback.ErrorCode
	result.Message = feedback.Message
	if result.Status == "" {
		if status >= 200 && status < 300 {
			result.Status = adaptiveqos.StatusAccepted
		} else {
			result.Status = adaptiveqos.StatusRejected
		}
	}

	if ref != "" && status >= 200 && status < 300 {
		e.mu.Lock()
		e.active[intent.RequestID] = ref
		e.mu.Unlock()
		e.scheduleTerminate(intent.RequestID, ref, time.Duration(burstDurationMs(intent))*time.Millisecond)
	}
	if e.logger != nil {
		e.logger.Printf("af enforcer apply request_id=%s ue=%s supi=%s qfi=%d five_qi=%d mbr_ul=%d mbr_dl=%d gbr_ul=%d gbr_dl=%d ref=%s status=%s http=%d",
			intent.RequestID, intent.Flow.UEAddress, e.cfg.resolveSUPI(intent.Flow.UEAddress),
			intent.Flow.QFI, e.cfg.DefaultFiveQI, decision.MBRULKbps, decision.MBRDLKbps,
			decision.GBRULKbps, decision.GBRDLKbps, ref, result.Status, status)
	}
	return result, nil
}

// scheduleTerminate releases the AF app-session after the burst window plus
// grace. This is the "end" handling for burst guarantees (NGAP has no
// auto-expiry, so the AF actively terminates).
func (e *Enforcer) scheduleTerminate(requestID, ref string, after time.Duration) {
	if e == nil || ref == "" || after <= 0 {
		return
	}
	delay := after + e.cfg.EndGrace
	time.AfterFunc(delay, func() {
		ctx, cancel := context.WithTimeout(context.Background(), e.cfg.Timeout)
		defer cancel()
		status, raw, err := e.client.terminateAppSession(ctx, ref)
		e.mu.Lock()
		// only delete if the ref still matches (not replaced by a new intent)
		if cur, ok := e.active[requestID]; ok && cur == ref {
			delete(e.active, requestID)
		}
		e.mu.Unlock()
		if e.logger != nil {
			if err != nil {
				e.logger.Printf("af enforcer terminate failed request_id=%s ref=%s err=%v", requestID, ref, err)
			} else {
				e.logger.Printf("af enforcer terminated request_id=%s ref=%s status=%d bytes=%d", requestID, ref, status, len(raw))
			}
		}
	})
}
