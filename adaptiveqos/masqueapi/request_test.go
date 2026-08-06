package masqueapi

import (
	"errors"
	"testing"
)

func TestDecodeCurrentProjectRequest(t *testing.T) {
	payload := []byte(`{
		"request_id":"req-1",
		"rnti":11222,
		"qfi":1,
		"burst_info":{
			"ul_burst_size":1024,
			"ul_burst_duration":100,
			"dl_burst_size":2048,
			"dl_burst_duration":100
		},
		"service_info":{"e2e_delay":160}
	}`)
	request, recognized, err := Decode(payload, "192.0.2.10")
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if !recognized {
		t.Fatal("Decode() recognized = false")
	}
	intent := request.Intent()
	if intent.Flow.RNTI != 11222 || intent.Flow.QFI != 1 {
		t.Fatalf("unexpected flow: %+v", intent.Flow)
	}
	if intent.Flow.UEAddress != "192.0.2.10" {
		t.Fatalf("UEAddress = %q", intent.Flow.UEAddress)
	}
	if intent.E2EDelayMS != 160 {
		t.Fatalf("E2EDelayMS = %d", intent.E2EDelayMS)
	}
}

func TestDecodeDoesNotClaimLegacyReport(t *testing.T) {
	_, recognized, err := Decode([]byte(`{"flowId":"legacy","burstSize":1000}`), "")
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if recognized {
		t.Fatal("Decode() recognized legacy report")
	}
}

func TestDecodeDoesNotClaimPartialProjectMarkers(t *testing.T) {
	_, recognized, err := Decode([]byte(`{"request_id":"legacy-request","flowId":"legacy"}`), "")
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if recognized {
		t.Fatal("Decode() recognized partial project markers")
	}
}

func TestDecodeRecognizesUppercaseFieldNames(t *testing.T) {
	payload := []byte(`{
		"request_id":"req-upper-1",
		"RNTI":11222,
		"QFI":1,
		"burst_info":{
			"ul_burst_size":1024,
			"ul_burst_duration":100,
			"dl_burst_size":2048,
			"dl_burst_duration":100
		},
		"service_info":{"e2e_delay":160}
	}`)
	request, recognized, err := Decode(payload, "192.0.2.10")
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if !recognized {
		t.Fatal("Decode() recognized = false for uppercase field names")
	}
	intent := request.Intent()
	if intent.Flow.RNTI != 11222 || intent.Flow.QFI != 1 {
		t.Fatalf("unexpected flow: %+v", intent.Flow)
	}
}

func TestDecodeRejectsEachMissingOrZeroBurstField(t *testing.T) {
	payloads := map[string]string{
		"ul burst size":     `{"request_id":"req-1","rnti":1,"qfi":1,"burst_info":{"ul_burst_duration":10,"dl_burst_size":10,"dl_burst_duration":10},"service_info":{"e2e_delay":160}}`,
		"ul burst duration": `{"request_id":"req-1","rnti":1,"qfi":1,"burst_info":{"ul_burst_size":10,"dl_burst_size":10,"dl_burst_duration":10},"service_info":{"e2e_delay":160}}`,
		"zero burst value":  `{"request_id":"req-1","rnti":1,"qfi":1,"burst_info":{"ul_burst_size":0,"ul_burst_duration":10,"dl_burst_size":10,"dl_burst_duration":10},"service_info":{"e2e_delay":160}}`,
	}
	for name, payload := range payloads {
		t.Run(name, func(t *testing.T) {
			_, recognized, err := Decode([]byte(payload), "")
			if !recognized {
				t.Fatal("Decode() recognized = false")
			}
			if !errors.Is(err, ErrInvalidRequest) {
				t.Fatalf("Decode() error = %v, want ErrInvalidRequest", err)
			}
		})
	}
}

func TestDecodeAllowsMissingDLBurst(t *testing.T) {
	request, recognized, err := Decode([]byte(`{
		"request_id":"req-1",
		"rnti":1,
		"qfi":1,
			"burst_info":{
				"ul_burst_size":10,
				"ul_burst_duration":10
			},
			"service_info":{"e2e_delay":160}
	}`), "")
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if !recognized {
		t.Fatal("Decode() recognized = false")
	}
	intent := request.Intent()
	if intent.DLBurst.SizeKB != 0 || intent.DLBurst.DurationMS != 0 {
		t.Fatalf("DL burst should be omitted: %+v", intent.DLBurst)
	}
}

func TestDecodeRejectsPartialOrZeroDLBurst(t *testing.T) {
	payloads := map[string]string{
		"dl burst size missing":     `{"request_id":"req-1","rnti":1,"qfi":1,"burst_info":{"ul_burst_size":10,"ul_burst_duration":10,"dl_burst_duration":10},"service_info":{"e2e_delay":160}}`,
		"dl burst duration missing": `{"request_id":"req-1","rnti":1,"qfi":1,"burst_info":{"ul_burst_size":10,"ul_burst_duration":10,"dl_burst_size":10},"service_info":{"e2e_delay":160}}`,
		"dl burst size zero":        `{"request_id":"req-1","rnti":1,"qfi":1,"burst_info":{"ul_burst_size":10,"ul_burst_duration":10,"dl_burst_size":0,"dl_burst_duration":10},"service_info":{"e2e_delay":160}}`,
		"dl burst duration zero":    `{"request_id":"req-1","rnti":1,"qfi":1,"burst_info":{"ul_burst_size":10,"ul_burst_duration":10,"dl_burst_size":10,"dl_burst_duration":0},"service_info":{"e2e_delay":160}}`,
	}
	for name, payload := range payloads {
		t.Run(name, func(t *testing.T) {
			_, recognized, err := Decode([]byte(payload), "")
			if !recognized {
				t.Fatal("Decode() recognized = false")
			}
			if !errors.Is(err, ErrInvalidRequest) {
				t.Fatalf("Decode() error = %v, want ErrInvalidRequest", err)
			}
		})
	}
}

func TestDecodeRejectsMissingOrZeroE2EDelay(t *testing.T) {
	payloads := map[string]string{
		"missing service info": `{"request_id":"req-1","rnti":1,"qfi":1,"burst_info":{"ul_burst_size":10,"ul_burst_duration":10}}`,
		"missing e2e delay":    `{"request_id":"req-1","rnti":1,"qfi":1,"burst_info":{"ul_burst_size":10,"ul_burst_duration":10},"service_info":{}}`,
		"zero e2e delay":       `{"request_id":"req-1","rnti":1,"qfi":1,"burst_info":{"ul_burst_size":10,"ul_burst_duration":10},"service_info":{"e2e_delay":0}}`,
	}
	for name, payload := range payloads {
		t.Run(name, func(t *testing.T) {
			_, recognized, err := Decode([]byte(payload), "")
			if !recognized {
				t.Fatal("Decode() recognized = false")
			}
			if !errors.Is(err, ErrInvalidRequest) {
				t.Fatalf("Decode() error = %v, want ErrInvalidRequest", err)
			}
		})
	}
}
