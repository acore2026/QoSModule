package udpranenforcer

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"time"

	adaptiveqos "github.com/acore2026/adaptive-qos"
	"github.com/acore2026/adaptive-qos/ranapi"
)

// Enforcer pushes QoS to a gNB over UDP, using the same JSON payload as
// ranapi.Client (HTTP). It is the third RouterEnforcer path (mode "ran-udp")
// for gNBs that expose a UDP QoS interface instead of HTTP.
type Enforcer struct {
	endpoint   string
	timeout    time.Duration
	defaults   ranapi.RequestDefaults
	waitForAck bool
	logger     *log.Logger
}

// Config configures the UDP RAN enforcer.
type Config struct {
	Endpoint   string                 // gNB UDP address, e.g. "10.88.0.3:9999"
	Timeout    time.Duration
	Defaults   ranapi.RequestDefaults
	WaitForAck bool // whether the gNB sends a UDP reply
	Logger     *log.Logger
}

// New builds a UDP RAN enforcer from cfg.
func New(cfg Config) *Enforcer {
	if cfg.Timeout <= 0 {
		cfg.Timeout = 3 * time.Second
	}
	if cfg.Defaults == (ranapi.RequestDefaults{}) {
		cfg.Defaults = ranapi.DefaultRequestDefaults()
	}
	return &Enforcer{
		endpoint:   cfg.Endpoint,
		timeout:    cfg.Timeout,
		defaults:   cfg.Defaults,
		waitForAck: cfg.WaitForAck,
		logger:     cfg.Logger,
	}
}

const udpResponseSize = 1 << 16

func (e *Enforcer) Apply(ctx context.Context, intent adaptiveqos.Intent, decision adaptiveqos.Decision) (adaptiveqos.ApplyResult, error) {
	if e == nil || e.endpoint == "" {
		return adaptiveqos.ApplyResult{}, fmt.Errorf("UDP RAN endpoint is not configured")
	}

	// 1. Build the same JSON payload as the HTTP path.
	request := ranapi.BuildRequest(intent, decision, e.defaults)
	body, err := json.Marshal(request)
	if err != nil {
		return adaptiveqos.ApplyResult{}, fmt.Errorf("encode UDP RAN request: %w", err)
	}

	// 2. Dial UDP and send.
	conn, err := net.Dial("udp", e.endpoint)
	if err != nil {
		return adaptiveqos.ApplyResult{}, fmt.Errorf("dial UDP RAN: %w", err)
	}
	defer conn.Close()

	deadline := time.Now().Add(e.timeout)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}
	_ = conn.SetWriteDeadline(deadline)

	if _, err := conn.Write(body); err != nil {
		return adaptiveqos.ApplyResult{}, fmt.Errorf("send UDP RAN: %w", err)
	}

	result := adaptiveqos.ApplyResult{}

	// 3. Optionally wait for a UDP reply.
	if e.waitForAck {
		_ = conn.SetReadDeadline(time.Now().Add(e.timeout))
		buf := make([]byte, udpResponseSize)
		n, err := conn.Read(buf)
		if err != nil {
			return adaptiveqos.ApplyResult{}, fmt.Errorf("read UDP RAN reply: %w", err)
		}
		raw := buf[:n]
		result.RawResponse = raw
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
			result.Status = adaptiveqos.StatusAccepted
		}
	} else {
		// fire-and-forget: assume accepted on successful send.
		result.Status = adaptiveqos.StatusAccepted
		result.RawResponse, _ = json.Marshal(map[string]any{
			"request_id": intent.RequestID,
			"status":     adaptiveqos.StatusAccepted,
		})
	}

	if e.logger != nil {
		e.logger.Printf("udp ran apply request_id=%s rnti=%d qfi=%d mbr_ul=%d mbr_dl=%d gbr_ul=%d gbr_dl=%d endpoint=%s status=%s ack=%v",
			intent.RequestID, intent.Flow.RNTI, intent.Flow.QFI,
			decision.MBRULKbps, decision.MBRDLKbps,
			decision.GBRULKbps, decision.GBRDLKbps,
			e.endpoint, result.Status, e.waitForAck)
	}
	return result, nil
}
