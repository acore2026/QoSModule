package ranapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	adaptiveqos "github.com/acore2026/adaptive-qos"
)

const (
	DefaultPath         = "/api/v1/qos/update"
	defaultResponseSize = 1 << 20
)

type BurstInfo struct {
	ULBurstSize     uint64 `json:"ul_burst_size"`
	DLBurstSize     uint64 `json:"dl_burst_size,omitempty"`
	ULBurstDuration uint64 `json:"ul_burst_duration"`
	DLBurstDuration uint64 `json:"dl_burst_duration,omitempty"`
	E2EDelayBudget  uint64 `json:"e2e_delay_budget,omitempty"`
}

type Request struct {
	RequestID string `json:"request_id"`
	Mask      uint32 `json:"mask"`
	RNTI      uint32 `json:"rnti"`
	QFI       uint8  `json:"q_qfi"`

	QType uint8  `json:"q_type"`
	QPri  uint8  `json:"q_pri"`
	QLvl  uint8  `json:"q_lvl"`
	QCap  uint8  `json:"q_cap"`
	QVul  uint8  `json:"q_vul"`
	QPDB  uint64 `json:"q_pdb"`

	QMBRDL uint64 `json:"q_mbr_dl,omitempty"`
	QMBRUL uint64 `json:"q_mbr_ul"`
	QGBRDL uint64 `json:"q_gbr_dl,omitempty"`
	QGBRUL uint64 `json:"q_gbr_ul"`

	DLMaxMCS uint8  `json:"dl_max_mcs,omitempty"`
	ULMaxMCS uint8  `json:"ul_max_mcs"`
	DLMaxRB  uint16 `json:"dl_max_rb,omitempty"`
	ULMaxRB  uint16 `json:"ul_max_rb"`

	ULBLERUpper float64 `json:"ul_bler_upper"`
	DLBLERUpper float64 `json:"dl_bler_upper,omitempty"`
	ULSmooth    float64 `json:"ul_smooth"`
	DLSmooth    float64 `json:"dl_smooth,omitempty"`

	BurstInfo BurstInfo `json:"burst_info"`
}

type RequestDefaults struct {
	Mask        uint32
	StaticMask  bool
	QType       uint8
	QCap        uint8
	QVul        uint8
	DLMaxMCS    uint8
	ULMaxMCS    uint8
	DLMaxRB     uint16
	ULMaxRB     uint16
	ULBLERUpper float64
	DLBLERUpper float64
	ULSmooth    float64
	DLSmooth    float64
}

func DefaultRequestDefaults() RequestDefaults {
	return RequestDefaults{
		QType:       0,
		QCap:        1,
		QVul:        0,
		DLMaxMCS:    28,
		ULMaxMCS:    28,
		DLMaxRB:     273,
		ULMaxRB:     273,
		ULBLERUpper: 0.01,
		DLBLERUpper: 0.01,
		ULSmooth:    0.5,
		DLSmooth:    0.5,
	}
}

type Client struct {
	Endpoint   string
	HTTPClient *http.Client
	Defaults   RequestDefaults
}

func NewClient(endpoint string, timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	return &Client{
		Endpoint:   endpoint,
		HTTPClient: &http.Client{Timeout: timeout},
		Defaults:   DefaultRequestDefaults(),
	}
}

func (c *Client) Apply(ctx context.Context, intent adaptiveqos.Intent, decision adaptiveqos.Decision) (adaptiveqos.ApplyResult, error) {
	if c == nil || c.Endpoint == "" {
		return adaptiveqos.ApplyResult{}, fmt.Errorf("RAN endpoint is not configured")
	}
	client := c.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 3 * time.Second}
	}

	request := BuildRequest(intent, decision, c.Defaults)
	body, err := json.Marshal(request)
	if err != nil {
		return adaptiveqos.ApplyResult{}, fmt.Errorf("encode RAN request: %w", err)
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Endpoint, bytes.NewReader(body))
	if err != nil {
		return adaptiveqos.ApplyResult{}, fmt.Errorf("create RAN request: %w", err)
	}
	httpRequest.Header.Set("Content-Type", "application/json")

	response, err := client.Do(httpRequest)
	if err != nil {
		return adaptiveqos.ApplyResult{}, fmt.Errorf("call RAN API: %w", err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, defaultResponseSize))
	if err != nil {
		return adaptiveqos.ApplyResult{}, fmt.Errorf("read RAN response: %w", err)
	}

	result := adaptiveqos.ApplyResult{
		HTTPStatus:  response.StatusCode,
		RawResponse: raw,
	}
	var feedback struct {
		Status    string `json:"status"`
		ErrorCode string `json:"error_code"`
		Message   string `json:"message"`
	}
	_ = json.Unmarshal(raw, &feedback)
	result.Status = feedback.Status
	result.ErrorCode = feedback.ErrorCode
	result.Message = feedback.Message
	if result.Status == "" {
		if response.StatusCode >= 200 && response.StatusCode < 300 {
			result.Status = adaptiveqos.StatusAccepted
		} else {
			result.Status = adaptiveqos.StatusRejected
		}
	}
	if len(result.RawResponse) == 0 {
		result.RawResponse, _ = json.Marshal(map[string]any{
			"request_id": intent.RequestID,
			"status":     result.Status,
		})
	}
	return result, nil
}

func BuildRequest(intent adaptiveqos.Intent, decision adaptiveqos.Decision, defaults RequestDefaults) Request {
	if defaults == (RequestDefaults{}) {
		defaults = DefaultRequestDefaults()
	}
	request := Request{
		RequestID:   intent.RequestID,
		RNTI:        intent.Flow.RNTI,
		QFI:         intent.Flow.QFI,
		QType:       defaults.QType,
		QPri:        decision.Priority,
		QLvl:        decision.Priority,
		QCap:        defaults.QCap,
		QVul:        defaults.QVul,
		QPDB:        decision.PDBMS,
		QMBRUL:      decision.MBRULKbps,
		QGBRUL:      decision.GBRULKbps,
		ULMaxMCS:    defaults.ULMaxMCS,
		ULMaxRB:     defaults.ULMaxRB,
		ULBLERUpper: defaults.ULBLERUpper,
		ULSmooth:    defaults.ULSmooth,
		BurstInfo: BurstInfo{
			ULBurstSize:     intent.ULBurst.SizeKB,
			ULBurstDuration: intent.ULBurst.DurationMS,
			E2EDelayBudget:  intent.E2EDelayMS,
		},
	}
	if intent.DLBurst.Complete() {
		request.QMBRDL = decision.MBRDLKbps
		request.QGBRDL = decision.GBRDLKbps
		request.DLMaxMCS = defaults.DLMaxMCS
		request.DLMaxRB = defaults.DLMaxRB
		request.DLBLERUpper = defaults.DLBLERUpper
		request.DLSmooth = defaults.DLSmooth
		request.BurstInfo.DLBurstSize = intent.DLBurst.SizeKB
		request.BurstInfo.DLBurstDuration = intent.DLBurst.DurationMS
	}
	if defaults.StaticMask || defaults.Mask != 0 {
		request.Mask = defaults.Mask
	} else {
		request.Mask = AutomaticMask(request)
	}
	return request
}

const (
	MaskBitRNTI uint32 = 1 << iota
	MaskBitDLMaxMCS
	MaskBitDLFixMCS
	MaskBitDLMaxRB
	MaskBitDLFixRB
	MaskBitULMaxMCS
	MaskBitULFixMCS
	MaskBitULMaxRB
	MaskBitULFixRB
	MaskBitULBLERUpper
	MaskBitULBLERLower
	MaskBitDLBLERUpper
	MaskBitDLBLERLower
	MaskBitULSmooth
	MaskBitDLSmooth
	MaskBitQFI
	MaskBitQPri
	MaskBitQType
	MaskBitQMBRDL
	MaskBitQMBRUL
	MaskBitQGBRDL
	MaskBitQGBRUL
	MaskBitQPDB
	MaskBitQLvl
	MaskBitQCap
	MaskBitQVul
	MaskBitULBurstSize
	MaskBitDLBurstSize
	MaskBitULBurstDuration
	MaskBitDLBurstDuration
	MaskBitE2EDelayBudget
)

var topLevelMaskBits = map[string]uint32{
	"rnti":          MaskBitRNTI,
	"dl_max_mcs":    MaskBitDLMaxMCS,
	"dl_max_rb":     MaskBitDLMaxRB,
	"ul_max_mcs":    MaskBitULMaxMCS,
	"ul_max_rb":     MaskBitULMaxRB,
	"ul_bler_upper": MaskBitULBLERUpper,
	"dl_bler_upper": MaskBitDLBLERUpper,
	"ul_smooth":     MaskBitULSmooth,
	"dl_smooth":     MaskBitDLSmooth,
	"q_qfi":         MaskBitQFI,
	"q_pri":         MaskBitQPri,
	"q_type":        MaskBitQType,
	"q_mbr_dl":      MaskBitQMBRDL,
	"q_mbr_ul":      MaskBitQMBRUL,
	"q_gbr_dl":      MaskBitQGBRDL,
	"q_gbr_ul":      MaskBitQGBRUL,
	"q_pdb":         MaskBitQPDB,
	"q_lvl":         MaskBitQLvl,
	"q_cap":         MaskBitQCap,
	"q_vul":         MaskBitQVul,
}

var burstInfoMaskBits = map[string]uint32{
	"ul_burst_size":     MaskBitULBurstSize,
	"dl_burst_size":     MaskBitDLBurstSize,
	"ul_burst_duration": MaskBitULBurstDuration,
	"dl_burst_duration": MaskBitDLBurstDuration,
	"e2e_delay_budget":  MaskBitE2EDelayBudget,
}

func AutomaticMask(request Request) uint32 {
	body, err := json.Marshal(request)
	if err != nil {
		return 0
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		return 0
	}
	var mask uint32
	for name, bit := range topLevelMaskBits {
		if _, ok := fields[name]; ok {
			mask |= bit
		}
	}
	var burstFields map[string]json.RawMessage
	if err := json.Unmarshal(fields["burst_info"], &burstFields); err == nil {
		for name, bit := range burstInfoMaskBits {
			if _, ok := burstFields[name]; ok {
				mask |= bit
			}
		}
	}
	return mask
}
