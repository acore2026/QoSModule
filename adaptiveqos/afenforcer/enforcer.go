package afenforcer

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

// --- 3GPP AppSessionContextReqData (TS 29.514 / acore2026 openapi v1.2.4) ---
// Only the fields the AF uses are modeled; the rest stay absent (omitempty).
// JSON tags match the openapi model exactly so a real free5gc PCF accepts it.

type snssaiJSON struct {
	Sst int32  `json:"sst"`
	Sd  string `json:"sd,omitempty"`
}

type mediaSubComponentJSON struct {
	FNum    int32    `json:"fNum"`
	FStatus string   `json:"fStatus,omitempty"`
	FDescs  []string `json:"fDescs,omitempty"`
}

type mediaComponentJSON struct {
	MedCompN     int32                            `json:"medCompN"`
	FStatus      string                           `json:"fStatus,omitempty"`
	QosReference string                           `json:"qosReference,omitempty"`
	MarBwDl      string                           `json:"marBwDl,omitempty"`
	MarBwUl      string                           `json:"marBwUl,omitempty"`
	MedSubComps  map[string]mediaSubComponentJSON `json:"medSubComps,omitempty"`
}

type appSessionRequestJSON struct {
	AfAppId       string                        `json:"afAppId,omitempty"`
	NotifUri      string                        `json:"notifUri"`
	SuppFeat      string                        `json:"suppFeat"`
	Supi          string                        `json:"supi,omitempty"`
	UeIpv4        string                        `json:"ueIpv4,omitempty"`
	Dnn           string                        `json:"dnn,omitempty"`
	SliceInfo     *snssaiJSON                   `json:"sliceInfo,omitempty"`
	MedComponents map[string]mediaComponentJSON `json:"medComponents,omitempty"`
}

// appSessionContextJSON wraps AscReqData, matching the 3GPP AppSessionContext
// shape the PCF deserializes (see api_policyauthorization.go HTTPPostAppSessions:
// it reads appSessionContext.AscReqData, not flat fields).
type appSessionContextJSON struct {
	AscReqData *appSessionRequestJSON `json:"ascReqData"`
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

// buildAppSessionBody assembles the 3GPP AppSessionContextReqData JSON for the
// real PCF Npcf_PolicyAuthorization service. MediaComponent carries
// qosReference (=5QI) and marBwDl/Ul (=MBR); for a GBR 5QI the PCF/SMF apply
// the standardized GBR/ARP. Custom GBR/ARP would need AltSerReqsData (later).
func (e *Enforcer) buildAppSessionBody(intent adaptiveqos.Intent, decision adaptiveqos.Decision) ([]byte, error) {
	ueIP := intent.Flow.UEAddress
	supi := e.cfg.resolveSUPI(ueIP)
	mc := mediaComponentJSON{
		MedCompN:     1,
		FStatus:      "ENABLED",
		QosReference: strconv.FormatUint(uint64(e.cfg.DefaultFiveQI), 10),
		MarBwUl:      strconv.FormatUint(kbpsToBps(decision.MBRULKbps), 10),
		MarBwDl:      strconv.FormatUint(kbpsToBps(decision.MBRDLKbps), 10),
		MedSubComps: map[string]mediaSubComponentJSON{
			"1": {FNum: 1, FStatus: "ENABLED"},
		},
	}
	if descs := buildFlowDescription(intent.Filter, ueIP); len(descs) > 0 {
		sub := mc.MedSubComps["1"]
		sub.FDescs = descs
		mc.MedSubComps["1"] = sub
	}
	req := appSessionRequestJSON{
		AfAppId:       e.cfg.AfAppId,
		NotifUri:      e.cfg.NotifUri,
		SuppFeat:      e.cfg.SuppFeat,
		Supi:          supi,
		UeIpv4:        ueIP,
		Dnn:           e.cfg.DefaultDNN,
		SliceInfo:     &snssaiJSON{Sst: e.cfg.DefaultSlice.SST, Sd: e.cfg.DefaultSlice.SD},
		MedComponents: map[string]mediaComponentJSON{"1": mc},
	}
	return json.Marshal(appSessionContextJSON{AscReqData: &req})
}

// buildFlowDescription builds 3GPP FlowDescription strings (TS 29.212) in the
// format the free5gc SMF packet-filter parser expects, e.g.
//   "permit out ip from <src> <sport> to <dst> <dport>"
// Always returns UL + DL flows keyed on the UE IP so the SMF can classify
// traffic (an empty fDescs makes the SMF fail with "too few fields").
func buildFlowDescription(f adaptiveqos.FlowFilter, ueIP string) []string {
	src := ueIP
	if f.SrcIP != "" {
		src = f.SrcIP
	}
	dst := "0.0.0.0/0"
	if f.DstIP != "" {
		dst = f.DstIP
	}
	if src == "" {
		return nil
	}
	ul := "permit out ip from " + src
	dl := "permit in ip from " + dst
	if f.SrcPort != 0 {
		ul += " " + strconv.Itoa(int(f.SrcPort))
	}
	if f.DstPort != 0 {
		dl += " " + strconv.Itoa(int(f.DstPort))
	}
	ul += " to " + dst
	dl += " to " + src
	if f.DstPort != 0 {
		ul += " " + strconv.Itoa(int(f.DstPort))
	}
	return []string{ul, dl}
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
