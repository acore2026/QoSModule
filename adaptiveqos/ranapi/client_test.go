package ranapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	adaptiveqos "github.com/acore2026/adaptive-qos"
)

func TestClientAppliesRANRequest(t *testing.T) {
	var received Request
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != DefaultPath {
			t.Errorf("path = %q, want %q", r.URL.Path, DefaultPath)
		}
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"request_id":"req-1","status":"ACCEPTED","message":"ok"}`))
	}))
	defer server.Close()

	client := NewClient(server.URL+DefaultPath, time.Second)
	result, err := client.Apply(context.Background(), adaptiveqos.Intent{
		RequestID:  "req-1",
		Flow:       adaptiveqos.FlowSelector{RNTI: 11222, QFI: 1},
		ULBurst:    adaptiveqos.BurstDemand{SizeKB: 1024, DurationMS: 100},
		DLBurst:    adaptiveqos.BurstDemand{SizeKB: 2048, DurationMS: 100},
		E2EDelayMS: 160,
	}, adaptiveqos.Decision{QoSValues: adaptiveqos.QoSValues{
		MBRULKbps: 81920,
		MBRDLKbps: 163840,
		GBRULKbps: 64000,
		GBRDLKbps: 100000,
		PDBMS:     100,
		Priority:  3,
	}})
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if result.Status != adaptiveqos.StatusAccepted {
		t.Fatalf("status = %q", result.Status)
	}
	if received.QGBRDL != 100000 || received.QPDB != 100 || received.QPri != 3 {
		t.Fatalf("unexpected RAN request: %+v", received)
	}
	if received.Mask != expectedFullMask() {
		t.Fatalf("mask = %d, want %d", received.Mask, expectedFullMask())
	}
}

func TestBuildRequestOmitsDLFieldsWhenDLBurstMissing(t *testing.T) {
	request := BuildRequest(adaptiveqos.Intent{
		RequestID:  "req-1",
		Flow:       adaptiveqos.FlowSelector{RNTI: 11222, QFI: 1},
		ULBurst:    adaptiveqos.BurstDemand{SizeKB: 1024, DurationMS: 100},
		E2EDelayMS: 160,
	}, adaptiveqos.Decision{QoSValues: adaptiveqos.QoSValues{
		MBRULKbps: 81920,
		GBRULKbps: 64000,
		PDBMS:     100,
		Priority:  3,
	}}, DefaultRequestDefaults())

	body, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	for _, name := range []string{"q_mbr_dl", "q_gbr_dl", "dl_max_mcs", "dl_max_rb", "dl_bler_upper", "dl_smooth"} {
		if _, ok := fields[name]; ok {
			t.Fatalf("%s should be omitted when DL burst is missing: %s", name, body)
		}
	}
	var burst map[string]json.RawMessage
	if err := json.Unmarshal(fields["burst_info"], &burst); err != nil {
		t.Fatalf("decode burst_info: %v", err)
	}
	for _, name := range []string{"dl_burst_size", "dl_burst_duration"} {
		if _, ok := burst[name]; ok {
			t.Fatalf("burst_info.%s should be omitted when DL burst is missing: %s", name, body)
		}
	}
	if request.Mask != expectedULOnlyMask() {
		t.Fatalf("mask = %d, want %d", request.Mask, expectedULOnlyMask())
	}
}

func TestBuildRequestAllowsStaticMaskOverride(t *testing.T) {
	request := BuildRequest(adaptiveqos.Intent{
		RequestID: "req-1",
		Flow:      adaptiveqos.FlowSelector{RNTI: 11222, QFI: 1},
		ULBurst:   adaptiveqos.BurstDemand{SizeKB: 1024, DurationMS: 100},
	}, adaptiveqos.Decision{QoSValues: adaptiveqos.QoSValues{
		MBRULKbps: 81920,
		GBRULKbps: 64000,
		PDBMS:     100,
		Priority:  3,
	}}, RequestDefaults{Mask: 123, StaticMask: true})
	if request.Mask != 122 {
		t.Fatalf("mask = %d, want 122 (static 123 with RNTI bit forced to 0)", request.Mask)
	}
}

func expectedFullMask() uint32 {
	return expectedULOnlyMask() |
		MaskBitDLMaxMCS |
		MaskBitDLMaxRB |
		MaskBitDLBLERUpper |
		MaskBitDLSmooth |
		MaskBitQMBRDL |
		MaskBitQGBRDL |
		MaskBitDLBurstSize |
		MaskBitDLBurstDuration
}

func expectedULOnlyMask() uint32 {
	return MaskBitULMaxMCS |
		MaskBitULMaxRB |
		MaskBitULBLERUpper |
		MaskBitULSmooth |
		MaskBitQFI |
		MaskBitQPri |
		MaskBitQType |
		MaskBitQMBRUL |
		MaskBitQGBRUL |
		MaskBitQPDB |
		MaskBitQLvl |
		MaskBitQCap |
		MaskBitQVul |
		MaskBitULBurstSize |
		MaskBitULBurstDuration |
		MaskBitE2EDelayBudget
}
