package target

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	adaptiveqos "github.com/acore2026/adaptive-qos"
	"github.com/acore2026/adaptive-qos/masqueapi"
	"github.com/acore2026/adaptive-qos/ranapi"
)

type QoSConfig struct {
	RANEndpoint string
	RANTimeout  time.Duration
	Policy      adaptiveqos.BurstPolicyConfig
	Limits      adaptiveqos.Limits
	HTTPClient  *http.Client
}

type QoSHandler struct {
	processor *adaptiveqos.Processor
}

func NewQoSHandler(cfg QoSConfig) (*QoSHandler, error) {
	if cfg.RANEndpoint == "" {
		return nil, errors.New("RAN endpoint is required")
	}
	if cfg.Limits == (adaptiveqos.Limits{}) {
		cfg.Limits = adaptiveqos.DefaultRANLimits()
	}
	if cfg.Policy == (adaptiveqos.BurstPolicyConfig{}) {
		cfg.Policy = adaptiveqos.DefaultBurstPolicyConfig()
	}
	ranClient := ranapi.NewClient(cfg.RANEndpoint, cfg.RANTimeout)
	if cfg.HTTPClient != nil {
		ranClient.HTTPClient = cfg.HTTPClient
	}
	return &QoSHandler{
		processor: &adaptiveqos.Processor{
			Policy:         adaptiveqos.NewBurstPolicy(cfg.Policy),
			LimitsProvider: adaptiveqos.StaticLimits{Value: cfg.Limits},
			Enforcer:       ranClient,
		},
	}, nil
}

func (h *QoSHandler) Handle(ctx context.Context, message Message) ([]byte, error) {
	request, recognized, err := masqueapi.Decode(message.Payload, message.ClientIP)
	if !recognized {
		return masqueapi.ErrorFeedback("", "INVALID_PARAM", errors.New("unsupported QoS request format")), nil
	}
	if err != nil {
		return masqueapi.ErrorFeedback(request.RequestID, "INVALID_PARAM", err), nil
	}
	if h == nil || h.processor == nil {
		return masqueapi.ErrorFeedback(request.RequestID, "INTERNAL_ERROR", errors.New("QoS processor is not configured")), nil
	}

	outcome, err := h.processor.Process(ctx, request.Intent())
	if err != nil {
		code := "INTERNAL_ERROR"
		switch {
		case errors.Is(err, adaptiveqos.ErrInvalidIntent):
			code = "INVALID_PARAM"
		case errors.Is(err, adaptiveqos.ErrLimitsUnavailable):
			code = "LIMITS_UNAVAILABLE"
		case errors.Is(err, adaptiveqos.ErrEnforcementFailed):
			code = "RAN_UNAVAILABLE"
		}
		return masqueapi.ErrorFeedback(request.RequestID, code, err), nil
	}
	if len(outcome.Apply.RawResponse) > 0 {
		return outcome.Apply.RawResponse, nil
	}

	return masqueapi.ErrorFeedback(
		request.RequestID,
		"EMPTY_RAN_RESPONSE",
		fmt.Errorf("RAN returned status %s without a response payload", outcome.Apply.Status),
	), nil
}
