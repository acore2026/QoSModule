package target

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"time"

	adaptiveqos "github.com/acore2026/adaptive-qos"
	"github.com/acore2026/adaptive-qos/masqueapi"
	"github.com/acore2026/adaptive-qos/smfenforcer"
	"github.com/acore2026/adaptive-qos/ranapi"
	"github.com/acore2026/adaptive-qos/routerenforcer"
	"github.com/acore2026/adaptive-qos/udpranenforcer"
)

type QoSConfig struct {
	RANEndpoint string
	RANTimeout  time.Duration
	Policy      adaptiveqos.BurstPolicyConfig
	Limits      adaptiveqos.Limits
	RANDefaults ranapi.RequestDefaults
	HTTPClient  *http.Client
	Logger      *log.Logger

	// CoreMode routes enforcement: "ran" → gNB-HTTP, "ran-udp" → gNB-UDP, "ngap" → SMF OAM, "auto" → ran/ran-udp/smf in order.
	CoreMode string
	// SMFConfig configures the SMF OAM enforcer (方案 A) used when CoreMode is ngap/auto.
	SMFConfig smfenforcer.Config
	// UDPRANConfig configures the UDP RAN enforcer used when CoreMode is ran-udp/auto.
	UDPRANConfig udpranenforcer.Config
}

type QoSHandler struct {
	processor *adaptiveqos.Processor
	logger    *log.Logger
}

func NewQoSHandler(cfg QoSConfig) (*QoSHandler, error) {
	if cfg.Limits == (adaptiveqos.Limits{}) {
		cfg.Limits = adaptiveqos.DefaultRANLimits()
	}
	if cfg.Policy == (adaptiveqos.BurstPolicyConfig{}) {
		cfg.Policy = adaptiveqos.DefaultBurstPolicyConfig()
	}

	mode, err := routerenforcer.ParseMode(cfg.CoreMode)
	if err != nil {
		return nil, err
	}

	// RAN enforcer (gNB-HTTP). Optional in ngap-only deployments.
	var ranEnforcer adaptiveqos.Enforcer
	if cfg.RANEndpoint != "" {
		ranClient := ranapi.NewClient(cfg.RANEndpoint, cfg.RANTimeout)
		if cfg.HTTPClient != nil {
			ranClient.HTTPClient = cfg.HTTPClient
		}
		if cfg.RANDefaults != (ranapi.RequestDefaults{}) {
			ranClient.Defaults = cfg.RANDefaults
		}
		ranEnforcer = ranClient
	}

	// SMF OAM enforcer (方案 A: SMF /qos-update → AMF → gNB). Built when the
	// mode needs it and the SMF endpoint is set.
	var smfEnforcer adaptiveqos.Enforcer
	if (mode == routerenforcer.ModeNGAP || mode == routerenforcer.ModeAuto) && cfg.SMFConfig.SMFEndpoint != "" {
		smfCfg := cfg.SMFConfig
		if smfCfg.HTTPClient == nil {
			smfCfg.HTTPClient = cfg.HTTPClient
		}
		smfCfg.Logger = cfg.Logger
		smfEnforcer = smfenforcer.New(smfCfg)
	}

	// UDP RAN enforcer. Built when the mode needs it and UDP endpoint is set.
	var udpRanEnforcer adaptiveqos.Enforcer
	if (mode == routerenforcer.ModeRanUDP || mode == routerenforcer.ModeAuto) && cfg.UDPRANConfig.Endpoint != "" {
		udpCfg := cfg.UDPRANConfig
		if udpCfg.Logger == nil {
			udpCfg.Logger = cfg.Logger
		}
		udpRanEnforcer = udpranenforcer.New(udpCfg)
	}

	switch mode {
	case routerenforcer.ModeRAN:
		if ranEnforcer == nil {
			return nil, errors.New("ran mode selected but RAN endpoint is empty")
		}
	case routerenforcer.ModeRanUDP:
		if udpRanEnforcer == nil {
			return nil, errors.New("ran-udp mode selected but UDP RAN endpoint is empty")
		}
	case routerenforcer.ModeNGAP:
		if smfEnforcer == nil {
			return nil, errors.New("ngap mode selected but SMF endpoint is empty")
		}
	case routerenforcer.ModeAuto:
		if ranEnforcer == nil && udpRanEnforcer == nil && smfEnforcer == nil {
			return nil, errors.New("auto mode needs at least one of RAN / UDP RAN / SMF endpoint")
		}
	}

	router := routerenforcer.New(ranEnforcer, udpRanEnforcer, smfEnforcer, mode)
	return &QoSHandler{
		processor: &adaptiveqos.Processor{
			Policy:         adaptiveqos.NewBurstPolicy(cfg.Policy),
			LimitsProvider: adaptiveqos.StaticLimits{Value: cfg.Limits},
			Enforcer:       router,
		},
		logger: cfg.Logger,
	}, nil
}

func (h *QoSHandler) Handle(ctx context.Context, message Message) ([]byte, error) {
	request, recognized, err := masqueapi.Decode(message.Payload, message.ClientIP)
	if !recognized {
		h.logf("qos request unrecognized client_ip=%s bytes=%d", message.ClientIP, len(message.Payload))
		return masqueapi.ErrorFeedback("", "INVALID_PARAM", errors.New("unsupported QoS request format")), nil
	}
	if err != nil {
		h.logf("qos request rejected request_id=%s client_ip=%s error_code=INVALID_PARAM error=%v", request.RequestID, message.ClientIP, err)
		return masqueapi.ErrorFeedback(request.RequestID, "INVALID_PARAM", err), nil
	}
	if h == nil || h.processor == nil {
		return masqueapi.ErrorFeedback(request.RequestID, "INTERNAL_ERROR", errors.New("QoS processor is not configured")), nil
	}

	intent := request.Intent()
	h.logf(
		"qos request accepted request_id=%s rnti=%d qfi=%d client_ip=%s ul_burst_size=%d ul_burst_duration=%d dl_burst_size=%d dl_burst_duration=%d e2e_delay=%d ul_transit_delay=%d dl_transit_delay=%d",
		intent.RequestID,
		intent.Flow.RNTI,
		intent.Flow.QFI,
		message.ClientIP,
		intent.ULBurst.SizeKB,
		intent.ULBurst.DurationMS,
		intent.DLBurst.SizeKB,
		intent.DLBurst.DurationMS,
		intent.E2EDelayMS,
		intent.ULTransitDelayMS,
		intent.DLTransitDelayMS,
	)
	outcome, err := h.processor.Process(ctx, intent)
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
		h.logf("qos request failed request_id=%s error_code=%s error=%v", request.RequestID, code, err)
		return masqueapi.ErrorFeedback(request.RequestID, code, err), nil
	}
	h.logf(
		"qos ran apply completed request_id=%s status=%s error_code=%s http_status=%d mbr_ul=%d mbr_dl=%d gbr_ul=%d gbr_dl=%d pdb=%d priority=%d target_mbr_ul=%d target_mbr_dl=%d target_gbr_ul=%d target_gbr_dl=%d target_pdb=%d response_bytes=%d",
		request.RequestID,
		outcome.Apply.Status,
		outcome.Apply.ErrorCode,
		outcome.Apply.HTTPStatus,
		outcome.Decision.MBRULKbps,
		outcome.Decision.MBRDLKbps,
		outcome.Decision.GBRULKbps,
		outcome.Decision.GBRDLKbps,
		outcome.Decision.PDBMS,
		outcome.Decision.Priority,
		outcome.Decision.Calculation.Target.MBRULKbps,
		outcome.Decision.Calculation.Target.MBRDLKbps,
		outcome.Decision.Calculation.Target.GBRULKbps,
		outcome.Decision.Calculation.Target.GBRDLKbps,
		outcome.Decision.Calculation.Target.PDBMS,
		len(outcome.Apply.RawResponse),
	)
	if len(outcome.Apply.RawResponse) > 0 {
		return outcome.Apply.RawResponse, nil
	}

	h.logf("qos ran apply returned empty payload request_id=%s status=%s", request.RequestID, outcome.Apply.Status)
	return masqueapi.ErrorFeedback(
		request.RequestID,
		"EMPTY_RAN_RESPONSE",
		fmt.Errorf("RAN returned status %s without a response payload", outcome.Apply.Status),
	), nil
}

func (h *QoSHandler) logf(format string, args ...any) {
	if h == nil || h.logger == nil {
		return
	}
	h.logger.Printf(format, args...)
}
