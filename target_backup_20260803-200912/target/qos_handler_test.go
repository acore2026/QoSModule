package target

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/acore2026/adaptive-qos/ranapi"
)

func TestQoSHandlerAppliesPolicyAndReturnsRANResponse(t *testing.T) {
	var received ranapi.Request
	ran := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Errorf("decode RAN request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"request_id":"req-1","status":"ACCEPTED","message":"applied"}`))
	}))
	defer ran.Close()

	handler, err := NewQoSHandler(QoSConfig{
		RANEndpoint: ran.URL + ranapi.DefaultPath,
		RANTimeout:  time.Second,
	})
	if err != nil {
		t.Fatalf("NewQoSHandler() error = %v", err)
	}
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
	response, err := handler.Handle(context.Background(), Message{
		ClientIP: "192.0.2.10",
		Payload:  payload,
	})
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if got := string(response); got != `{"request_id":"req-1","status":"ACCEPTED","message":"applied"}` {
		t.Fatalf("response = %s", got)
	}
	if received.RNTI != 11222 || received.QFI != 1 {
		t.Fatalf("unexpected RAN flow selector: %+v", received)
	}
	if received.QMBRUL != 81920 || received.QMBRDL != 163840 {
		t.Fatalf("unexpected RAN MBR: ul=%d dl=%d", received.QMBRUL, received.QMBRDL)
	}
	if received.QGBRUL != 64000 || received.QGBRDL != 100000 {
		t.Fatalf("unexpected RAN GBR: ul=%d dl=%d", received.QGBRUL, received.QGBRDL)
	}
	if received.QPDB != 100 || received.QPri != 3 {
		t.Fatalf("unexpected RAN PDB/priority: pdb=%d priority=%d", received.QPDB, received.QPri)
	}
}

func TestQoSHandlerRejectsInvalidRequestWithoutCallingRAN(t *testing.T) {
	called := false
	ran := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		called = true
	}))
	defer ran.Close()
	handler, err := NewQoSHandler(QoSConfig{RANEndpoint: ran.URL})
	if err != nil {
		t.Fatalf("NewQoSHandler() error = %v", err)
	}

	response, err := handler.Handle(context.Background(), Message{
		Payload: []byte(`{"request_id":"req-1","rnti":1,"qfi":1}`),
	})
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if called {
		t.Fatal("invalid request was sent to RAN")
	}
	var feedback struct {
		Status    string `json:"status"`
		ErrorCode string `json:"error_code"`
	}
	if err := json.Unmarshal(response, &feedback); err != nil {
		t.Fatalf("decode feedback: %v", err)
	}
	if feedback.Status != "REJECTED" || feedback.ErrorCode != "INVALID_PARAM" {
		t.Fatalf("unexpected feedback: %s", response)
	}
}

func TestQoSHandlerOmitsDLFieldsWhenDLBurstMissing(t *testing.T) {
	var received map[string]json.RawMessage
	ran := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Errorf("decode RAN request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"request_id":"req-1","status":"ACCEPTED","message":"applied"}`))
	}))
	defer ran.Close()

	handler, err := NewQoSHandler(QoSConfig{
		RANEndpoint: ran.URL + ranapi.DefaultPath,
		RANTimeout:  time.Second,
	})
	if err != nil {
		t.Fatalf("NewQoSHandler() error = %v", err)
	}
	_, err = handler.Handle(context.Background(), Message{
		Payload: []byte(`{
			"request_id":"req-1",
			"rnti":11222,
			"qfi":1,
			"burst_info":{
				"ul_burst_size":1024,
				"ul_burst_duration":100
			},
			"service_info":{"e2e_delay":160}
		}`),
	})
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	for _, name := range []string{"q_mbr_dl", "q_gbr_dl", "dl_max_mcs", "dl_max_rb", "dl_bler_upper", "dl_smooth"} {
		if _, ok := received[name]; ok {
			t.Fatalf("%s should be omitted when DL burst is missing", name)
		}
	}
	var burst map[string]json.RawMessage
	if err := json.Unmarshal(received["burst_info"], &burst); err != nil {
		t.Fatalf("decode burst_info: %v", err)
	}
	for _, name := range []string{"dl_burst_size", "dl_burst_duration"} {
		if _, ok := burst[name]; ok {
			t.Fatalf("burst_info.%s should be omitted when DL burst is missing", name)
		}
	}
}

func TestUDPServerRunsCurrentQoSFlowEndToEnd(t *testing.T) {
	ran := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"request_id":"req-udp-1","status":"ACCEPTED"}`))
	}))
	defer ran.Close()
	handler, err := NewQoSHandler(QoSConfig{
		RANEndpoint: ran.URL + ranapi.DefaultPath,
		RANTimeout:  time.Second,
	})
	if err != nil {
		t.Fatalf("NewQoSHandler() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	server := NewServer(Config{
		ListenAddr: "127.0.0.1:0",
		Handler:    handler,
	})
	errCh := make(chan error, 1)
	go func() { errCh <- server.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		_ = server.Close()
		<-errCh
	})

	conn := dialUDP(t, waitForAddr(t, server))
	defer conn.Close()
	payload := "CLIENT-IP: 192.0.2.10\r\n\r\n" +
		`{"request_id":"req-udp-1","rnti":11222,"qfi":1,"burst_info":{"ul_burst_size":1024,"ul_burst_duration":100,"dl_burst_size":2048,"dl_burst_duration":100},"service_info":{"e2e_delay":160}}`
	if _, err := conn.Write([]byte(payload)); err != nil {
		t.Fatalf("write UDP request: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	response := make([]byte, 512)
	n, err := conn.Read(response)
	if err != nil {
		t.Fatalf("read UDP response: %v", err)
	}
	if got, want := string(response[:n]), `{"request_id":"req-udp-1","status":"ACCEPTED"}`; got != want {
		t.Fatalf("response = %s, want %s", got, want)
	}
}
