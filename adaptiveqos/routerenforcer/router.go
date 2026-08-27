package routerenforcer

import (
	"context"
	"fmt"
	"log"
	"time"

	adaptiveqos "github.com/acore2026/adaptive-qos"
)

// Mode selects which enforcer handles a request.
type Mode string

const (
	// ModeRAN routes to the gNB-HTTP enforcer (ranapi.Client).
	ModeRAN Mode = "ran"
	// ModeRanUDP routes to the gNB-UDP enforcer (udpranenforcer).
	ModeRanUDP Mode = "ran-udp"
	// ModeNGAP routes to the SMF OAM enforcer (方案 A: SMF /qos-update →
	// AMF → gNB NGAP). The SMF resolves the PDU session by UE IP.
	ModeNGAP Mode = "ngap"
	// ModeAuto tries UDP RAN first, then mock-ran (HTTP ranapi), then SMF OAM.
	// 三档独立回退: UDP(真 gNB) → mock-ran(本地模拟收 QoS+模拟指标) → SMF(真 SMF/NGAP)。
	// 注: 需 -ran-udp-ack=1, 否则 UDP fire-and-forget 永远"成功"不会回退。
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
	ranEnforcer    adaptiveqos.Enforcer
	udpRanEnforcer adaptiveqos.Enforcer
	smfEnforcer    adaptiveqos.Enforcer
	mode           Mode
	logger         *log.Logger
}

// New builds a RouterEnforcer. Any enforcer may be nil if the deployment
// only uses some modes (e.g. ngap-only for the closed gNB). logger 可为 nil(无日志)。
func New(ran, udpRan, smf adaptiveqos.Enforcer, mode Mode, logger *log.Logger) *RouterEnforcer {
	return &RouterEnforcer{ranEnforcer: ran, udpRanEnforcer: udpRan, smfEnforcer: smf, mode: mode, logger: logger}
}

// logf is nil-safe: 不配置 logger 时静默。
func (r *RouterEnforcer) logf(format string, args ...any) {
	if r == nil || r.logger == nil {
		return
	}
	r.logger.Printf(format, args...)
}

// stageOutcome 返回供日志用的简短结果串。
func stageOutcome(res adaptiveqos.ApplyResult, err error) string {
	if err != nil {
		return "err: " + err.Error()
	}
	if res.Status == adaptiveqos.StatusAccepted {
		return "ACCEPTED"
	}
	return "status=" + string(res.Status)
}

// tryStage 调用 e.Apply 并记录耗时与结果, 返回 (res, err, accepted)。
// accepted = err==nil && Status==ACCEPTED。用于 auto 各档回退判定与日志。
func (r *RouterEnforcer) tryStage(stage string, e adaptiveqos.Enforcer, ctx context.Context, intent adaptiveqos.Intent, decision adaptiveqos.Decision) (adaptiveqos.ApplyResult, error, bool) {
	start := time.Now()
	res, err := e.Apply(ctx, intent, decision)
	elapsed := time.Since(start)
	accepted := err == nil && res.Status == adaptiveqos.StatusAccepted
	tag := "-> done"
	if !accepted {
		tag = "-> fallback"
	}
	r.logf("%s: %s %s %s", stage, elapsed.Round(time.Millisecond), stageOutcome(res, err), tag)
	return res, err, accepted
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
		res, err, _ := r.tryStage("ran", r.ranEnforcer, ctx, intent, decision)
		return res, err
	case ModeRanUDP:
		if r.udpRanEnforcer == nil {
			return adaptiveqos.ApplyResult{}, fmt.Errorf("ran-udp mode selected but UDP RAN enforcer is nil")
		}
		res, err, _ := r.tryStage("ran-udp", r.udpRanEnforcer, ctx, intent, decision)
		return res, err
	case ModeNGAP:
		if r.smfEnforcer == nil {
			return adaptiveqos.ApplyResult{}, fmt.Errorf("ngap mode selected but SMF enforcer is nil")
		}
		res, err, _ := r.tryStage("ngap(smf)", r.smfEnforcer, ctx, intent, decision)
		return res, err
	case ModeAuto:
		// 三档独立回退: UDP(真 gNB) → mock-ran(ran HTTP ranapi) → SMF OAM(真 SMF)。
		// 每档计时记录, 便于定位 auto 回退时延(如 UDP ack 超时 ~3s 才回退 mock-ran)。
		// 需 -ran-udp-ack=1, 否则 UDP fire-and-forget 永远"成功"不会触发回退。
		if r.udpRanEnforcer != nil {
			if res, _, ok := r.tryStage("auto 1/3 udp-ran", r.udpRanEnforcer, ctx, intent, decision); ok {
				return res, nil
			}
		} else {
			r.logf("auto 1/3 udp-ran: skipped (enforcer nil)")
		}
		if r.ranEnforcer != nil {
			if res, _, ok := r.tryStage("auto 2/3 mock-ran", r.ranEnforcer, ctx, intent, decision); ok {
				return res, nil
			}
		} else {
			r.logf("auto 2/3 mock-ran: skipped (enforcer nil)")
		}
		if r.smfEnforcer == nil {
			r.logf("auto 3/3 smf: nil (no fallback left)")
			return adaptiveqos.ApplyResult{}, fmt.Errorf("auto mode fell back but SMF enforcer is nil")
		}
		res, err, _ := r.tryStage("auto 3/3 smf", r.smfEnforcer, ctx, intent, decision)
		return res, err
	default:
		return adaptiveqos.ApplyResult{}, fmt.Errorf("unknown mode %q", r.mode)
	}
}
