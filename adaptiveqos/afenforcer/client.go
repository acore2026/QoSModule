package afenforcer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// client talks to a PCF PolicyAuthorization app-session collection.
//
// Requests use the 3GPP TS 29.514 AppSessionContext shape accepted by the real
// free5gc PCF. The wire handling stays isolated here so authentication,
// discovery, notification and response normalization can evolve separately
// from policy generation.
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
	return &client{endpoint: cfg.PCFEndpoint, httpc: httpc}
}

// createResponse is the legacy/mock response subset used when a JSON body is
// available. A real PCF may identify the session only through Location.
type createResponse struct {
	AppSessionID string `json:"app_session_id"`
	Status       string `json:"status"`
	Message      string `json:"message,omitempty"`
	ErrorCode    string `json:"error_code,omitempty"`
}

func (c *client) createAppSession(ctx context.Context, body []byte) (string, int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return "", 0, nil, fmt.Errorf("create app-session request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpc.Do(req)
	if err != nil {
		return "", 0, nil, fmt.Errorf("call PCF: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	ref := extractAppSessionRef(resp, raw)
	return ref, resp.StatusCode, raw, nil
}

func (c *client) terminateAppSession(ctx context.Context, ref string) (int, []byte, error) {
	if ref == "" {
		return 0, nil, fmt.Errorf("empty app-session ref")
	}
	// The collection URL ends with /app-sessions; terminate by DELETE on the
	// member URL /app-sessions/{ref}.
	url := strings.TrimRight(c.endpoint, "/") + "/" + ref
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, url, nil)
	if err != nil {
		return 0, nil, fmt.Errorf("terminate app-session request: %w", err)
	}
	resp, err := c.httpc.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("terminate PCF: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, raw, nil
}

// extractAppSessionRef pulls the app-session reference from either a Location
// header (real PCF: /app-sessions/{ref}) or the JSON body (mockpcf).
func extractAppSessionRef(resp *http.Response, raw []byte) string {
	if loc := resp.Header.Get("Location"); loc != "" {
		if idx := strings.LastIndex(loc, "/"); idx >= 0 {
			return loc[idx+1:]
		}
		return loc
	}
	var parsed createResponse
	if json.Unmarshal(raw, &parsed) == nil && parsed.AppSessionID != "" {
		return parsed.AppSessionID
	}
	return ""
}
