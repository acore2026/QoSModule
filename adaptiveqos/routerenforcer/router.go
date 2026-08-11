package routerenforcer

import (
	"context"
	"fmt"

	adaptiveqos "github.com/acore2026/adaptive-qos"
)

// Mode selects which enforcer handles a request.
type Mode string

const (
	// ModeRAN routes to the gNB-HTTP enforcer (ranapi.Client).
	ModeRAN Mode = "ran"
	// ModeRanUDP routes to the gNB-UDP enforcer (udpranenforcer).
	ModeRanUDP Mode = "ran-udp"
	// ModeNGAP routes to the AF/PCF enforcer (core-network NGAP path).
	ModeNGAP Mode = "ngap"
	// ModeAuto tries RAN first and falls back to NGAP on failure.
	ModeAuto Mode = "auto"
)

// ParseMode validates a mode string.
func ParseMode(s string) (Mode, error) {
	switch s {
	case "", "ran":
		return ModeRAN, nil
	case "ran-udp":
		return ModeRanUDP, nil
	case "ngap":
		return ModeNGAP, nil
	case "auto":
		return ModeAuto, nil
	default:
		return "", fmt.Errorf("unknown core mode %q (want ran|ran-udp|ngap|auto)", s)
	}
}

// RouterEnforcer dispatches an Apply to one of its enforcers based on Mode.
// It implements the adaptiveqos.Enforcer interface so it can replace a single
// enforcer in QoSHandler without changing the pipeline.
type RouterEnforcer struct {
	ranEnforcer   adaptiveqos.Enforcer
	udpRanEnforcer adaptiveqos.Enforcer
	afEnforcer    adaptiveqos.Enforcer
	mode          Mode
}

// New builds a RouterEnforcer. Any enforcer may be nil if the deployment
// only uses some modes (e.g. ngap-only for the closed gNB).
func New(ran, udpRan, af adaptiveqos.Enforcer, mode Mode) *RouterEnforcer {
	return &RouterEnforcer{ranEnforcer: ran, udpRanEnforcer: udpRan, afEnforcer: af, mode: mode}
}

func (r *RouterEnforcer) Apply(ctx context.Context, intent adaptiveqos.Intent, decision adaptiveqos.Decision) (adaptiveqos.ApplyResult, error) {
	if r == nil {
		return adaptiveqos.ApplyResult{}, fmt.Errorf("router enforcer is nil")
	}
	switch r.mode {
	case ModeRAN:
		if r.ranEnforcer == nil {
			return adaptiveqos.ApplyResult{}, fmt.Errorf("ran mode selected but RAN enforcer is nil")
		}
		return r.ranEnforcer.Apply(ctx, intent, decision)
	case ModeRanUDP:
		if r.udpRanEnforcer == nil {
			return adaptiveqos.ApplyResult{}, fmt.Errorf("ran-udp mode selected but UDP RAN enforcer is nil")
		}
		return r.udpRanEnforcer.Apply(ctx, intent, decision)
	case ModeNGAP:
		if r.afEnforcer == nil {
			return adaptiveqos.ApplyResult{}, fmt.Errorf("ngap mode selected but AF enforcer is nil")
		}
		return r.afEnforcer.Apply(ctx, intent, decision)
	case ModeAuto:
		// Try HTTP RAN first, then UDP RAN, then NGAP.
		if r.ranEnforcer != nil {
			if res, err := r.ranEnforcer.Apply(ctx, intent, decision); err == nil && res.Status == adaptiveqos.StatusAccepted {
				return res, nil
			}
		}
		if r.udpRanEnforcer != nil {
			if res, err := r.udpRanEnforcer.Apply(ctx, intent, decision); err == nil && res.Status == adaptiveqos.StatusAccepted {
				return res, nil
			}
		}
		if r.afEnforcer == nil {
			return adaptiveqos.ApplyResult{}, fmt.Errorf("auto mode fell back but AF enforcer is nil")
		}
		return r.afEnforcer.Apply(ctx, intent, decision)
	default:
		return adaptiveqos.ApplyResult{}, fmt.Errorf("unknown mode %q", r.mode)
	}
}
