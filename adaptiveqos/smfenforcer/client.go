package smfenforcer

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"time"
)

// client talks to the fork SMF OAM qos-update endpoint.
type client struct {
	endpoint string
	httpc    *http.Client
}

func newClient(cfg Config) *client {
	httpc := cfg.HTTPClient
	if httpc == nil {
		httpc = &http.Client{Timeout: cfg.Timeout}
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 5 * time.Second
	}
	return &client{endpoint: cfg.SMFEndpoint, httpc: httpc}
}

// qosUpdateResponse models both the success body emitted by HandleOAMQoSUpdate
// (status/request_id/ue_ip/supi/qfi/five_qi/amf_cause) and the ProblemDetails
// error body (title/detail/cause) in one struct; absent fields stay zero.
type qosUpdateResponse struct {
	Status    string `json:"status"`
	RequestID string `json:"request_id"`
	UEIP      string `json:"ue_ip"`
	Supi      string `json:"supi"`
	QFI       uint8  `json:"qfi"`
	FiveQI    uint8  `json:"five_qi"`
	AMFCause  string `json:"amf_cause"`
	// error fields (ProblemDetails)
	Title  string `json:"title,omitempty"`
	Detail string `json:"detail,omitempty"`
	Cause  string `json:"cause,omitempty"`
}

func (c *client) postQoSUpdate(ctx context.Context, body []byte) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return 0, nil, fmt.Errorf("create qos-update request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpc.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("call SMF: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, raw, nil
}
