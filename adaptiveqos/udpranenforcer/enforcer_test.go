package udpranenforcer

import (
	"context"
	"encoding/json"
	"net"
	"strings"
	"testing"
	"time"

	adaptiveqos "github.com/acore2026/adaptive-qos"
	"github.com/acore2026/adaptive-qos/ranapi"
)

func TestApplySendsRANRequestWithoutAck(t *testing.T) {
	listener := listenUDP(t)
	defer listener.Close()

	enforcer := New(Config{
		Endpoint: listener.LocalAddr().String(),
		Timeout:  time.Second,
	})
	intent, decision := testIntentAndDecision()

	result, err := enforcer.Apply(context.Background(), intent, decision)
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if result.Status != adaptiveqos.StatusAccepted {
		t.Fatalf("status = %q, want %q", result.Status, adaptiveqos.StatusAccepted)
	}

	var feedback struct {
		RequestID string `json:"request_id"`
		Status    string `json:"status"`
	}
	if err := json.Unmarshal(result.RawResponse, &feedback); err != nil {
		t.Fatalf("decode generated feedback: %v", err)
	}
	if feedback.RequestID != intent.RequestID || feedback.Status != adaptiveqos.StatusAccepted {
		t.Fatalf("unexpected generated feedback: %+v", feedback)
	}

	received, _ := readRequest(t, listener)
	want := ranapi.BuildRequest(intent, decision, ranapi.DefaultRequestDefaults())
	if received != want {
		t.Fatalf("UDP request = %+v, want %+v", received, want)
	}
	if received.Mask == 0 {
		t.Fatal("automatic mask was not populated")
	}
}

func TestApplyParsesAckResponse(t *testing.T) {
	listener := listenUDP(t)
	defer listener.Close()

	serverErr := make(chan error, 1)
	go func() {
		buf := make([]byte, udpResponseSize)
		if err := listener.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
			serverErr <- err
			return
		}
		_, peer, err := listener.ReadFromUDP(buf)
		if err != nil {
			serverErr <- err
			return
		}
		_, err = listener.WriteToUDP([]byte(`{"request_id":"req-udp-1","status":"REJECTED","error_code":"RAN_BUSY","message":"insufficient resources"}`), peer)
		serverErr <- err
	}()

	enforcer := New(Config{
		Endpoint:   listener.LocalAddr().String(),
		Timeout:    time.Second,
		WaitForAck: true,
	})
	intent, decision := testIntentAndDecision()
	result, err := enforcer.Apply(context.Background(), intent, decision)
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if err := <-serverErr; err != nil {
		t.Fatalf("UDP server error = %v", err)
	}
	if result.Status != adaptiveqos.StatusRejected {
		t.Fatalf("status = %q, want %q", result.Status, adaptiveqos.StatusRejected)
	}
	if result.ErrorCode != "RAN_BUSY" || result.Message != "insufficient resources" {
		t.Fatalf("unexpected ACK result: %+v", result)
	}
	if !strings.Contains(string(result.RawResponse), `"request_id":"req-udp-1"`) {
		t.Fatalf("raw response = %s", result.RawResponse)
	}
}

func TestApplyDefaultsAckStatusToAccepted(t *testing.T) {
	listener := listenUDP(t)
	defer listener.Close()

	go func() {
		buf := make([]byte, udpResponseSize)
		_ = listener.SetReadDeadline(time.Now().Add(time.Second))
		_, peer, err := listener.ReadFromUDP(buf)
		if err == nil {
			_, _ = listener.WriteToUDP([]byte(`{"request_id":"req-udp-1","message":"applied"}`), peer)
		}
	}()

	enforcer := New(Config{
		Endpoint:   listener.LocalAddr().String(),
		Timeout:    time.Second,
		WaitForAck: true,
	})
	intent, decision := testIntentAndDecision()
	result, err := enforcer.Apply(context.Background(), intent, decision)
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if result.Status != adaptiveqos.StatusAccepted || result.Message != "applied" {
		t.Fatalf("unexpected ACK result: %+v", result)
	}
}

func TestApplyTimesOutWaitingForAck(t *testing.T) {
	listener := listenUDP(t)
	defer listener.Close()

	enforcer := New(Config{
		Endpoint:   listener.LocalAddr().String(),
		Timeout:    20 * time.Millisecond,
		WaitForAck: true,
	})
	intent, decision := testIntentAndDecision()

	_, err := enforcer.Apply(context.Background(), intent, decision)
	if err == nil || !strings.Contains(err.Error(), "read UDP RAN reply") {
		t.Fatalf("Apply() error = %v, want ACK read timeout", err)
	}
}

func TestApplyRejectsMissingEndpoint(t *testing.T) {
	intent, decision := testIntentAndDecision()

	for _, enforcer := range []*Enforcer{nil, New(Config{})} {
		_, err := enforcer.Apply(context.Background(), intent, decision)
		if err == nil || !strings.Contains(err.Error(), "endpoint is not configured") {
			t.Fatalf("Apply() error = %v, want missing endpoint error", err)
		}
	}
}

func TestApplyRejectsInvalidEndpoint(t *testing.T) {
	enforcer := New(Config{Endpoint: "127.0.0.1:not-a-port", Timeout: time.Second})
	intent, decision := testIntentAndDecision()

	_, err := enforcer.Apply(context.Background(), intent, decision)
	if err == nil || !strings.Contains(err.Error(), "dial UDP RAN") {
		t.Fatalf("Apply() error = %v, want dial error", err)
	}
}

func listenUDP(t *testing.T) *net.UDPConn {
	t.Helper()
	listener, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatalf("ListenUDP() error = %v", err)
	}
	return listener
}

func readRequest(t *testing.T, listener *net.UDPConn) (ranapi.Request, *net.UDPAddr) {
	t.Helper()
	if err := listener.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatalf("SetReadDeadline() error = %v", err)
	}
	buf := make([]byte, udpResponseSize)
	n, peer, err := listener.ReadFromUDP(buf)
	if err != nil {
		t.Fatalf("ReadFromUDP() error = %v", err)
	}
	var request ranapi.Request
	if err := json.Unmarshal(buf[:n], &request); err != nil {
		t.Fatalf("decode UDP RAN request: %v", err)
	}
	return request, peer
}

func testIntentAndDecision() (adaptiveqos.Intent, adaptiveqos.Decision) {
	return adaptiveqos.Intent{
			RequestID:  "req-udp-1",
			Flow:       adaptiveqos.FlowSelector{RNTI: 11222, QFI: 5},
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
		}}
}
