package adaptiveqos

import (
	"context"
	"errors"
	"fmt"
	"math"
)

var ErrInvalidIntent = errors.New("invalid qos intent")

type BurstPolicyConfig struct {
	DefaultPDBMS          uint64
	Priority              uint8
	TransitDelayRatio     float64
	DefaultTransitDelayMS uint64
}

func DefaultBurstPolicyConfig() BurstPolicyConfig {
	return BurstPolicyConfig{
		DefaultPDBMS:          100,
		Priority:              3,
		TransitDelayRatio:     0.8,
		DefaultTransitDelayMS: 100,
	}
}

type BurstPolicy struct {
	cfg BurstPolicyConfig
}

func NewBurstPolicy(cfg BurstPolicyConfig) *BurstPolicy {
	defaults := DefaultBurstPolicyConfig()
	if cfg.DefaultPDBMS == 0 {
		cfg.DefaultPDBMS = defaults.DefaultPDBMS
	}
	if cfg.Priority == 0 {
		cfg.Priority = defaults.Priority
	}
	if cfg.TransitDelayRatio <= 0 {
		cfg.TransitDelayRatio = defaults.TransitDelayRatio
	}
	if cfg.DefaultTransitDelayMS == 0 {
		cfg.DefaultTransitDelayMS = defaults.DefaultTransitDelayMS
	}
	return &BurstPolicy{cfg: cfg}
}

func (p *BurstPolicy) Generate(_ context.Context, intent Intent, limits Limits) (Decision, error) {
	if p == nil {
		return Decision{}, fmt.Errorf("%w: policy is nil", ErrInvalidIntent)
	}
	if err := p.validate(intent); err != nil {
		return Decision{}, err
	}

	ulTransit := p.transitDelay(intent.ULTransitDelayMS, intent.E2EDelayMS)
	var dlTransit uint64
	if intent.DLBurst.Complete() {
		dlTransit = p.transitDelay(intent.DLTransitDelayMS, intent.E2EDelayMS)
	}

	target := QoSValues{
		MBRULKbps: rateKbps(intent.ULBurst.SizeKB, intent.ULBurst.DurationMS),
		GBRULKbps: rateKbps(intent.ULBurst.SizeKB, ulTransit),
		PDBMS:     pdbFromBudget(intent.E2EDelayMS, p.cfg.DefaultPDBMS),
		Priority:  p.cfg.Priority,
	}
	if intent.DLBurst.Complete() {
		target.MBRDLKbps = rateKbps(intent.DLBurst.SizeKB, intent.DLBurst.DurationMS)
		target.GBRDLKbps = rateKbps(intent.DLBurst.SizeKB, dlTransit)
	}

	applied := QoSValues{
		MBRULKbps: limits.MBRUL.Clip(target.MBRULKbps),
		GBRULKbps: limits.GBRUL.Clip(target.GBRULKbps),
		PDBMS:     limits.PDB.Clip(target.PDBMS),
		Priority:  uint8(limits.Priority.Clip(uint64(target.Priority))),
	}
	if intent.DLBurst.Complete() {
		applied.MBRDLKbps = limits.MBRDL.Clip(target.MBRDLKbps)
		applied.GBRDLKbps = limits.GBRDL.Clip(target.GBRDLKbps)
	}

	return Decision{
		QoSValues: applied,
		Calculation: Calculation{
			Target:           target,
			ULTransitDelayMS: ulTransit,
			DLTransitDelayMS: dlTransit,
		},
	}, nil
}

func (p *BurstPolicy) validate(intent Intent) error {
	if intent.Flow.RNTI > 65_535 {
		return fmt.Errorf("%w: rnti must be in [0,65535]", ErrInvalidIntent)
	}
	if intent.Flow.QFI > 63 {
		return fmt.Errorf("%w: qfi must be in [0,63]", ErrInvalidIntent)
	}
	switch {
	case intent.E2EDelayMS == 0:
		return fmt.Errorf("%w: e2e_delay is required", ErrInvalidIntent)
	case intent.ULBurst.SizeKB == 0:
		return fmt.Errorf("%w: ul_burst_size is required", ErrInvalidIntent)
	case intent.ULBurst.DurationMS == 0:
		return fmt.Errorf("%w: ul_burst_duration is required", ErrInvalidIntent)
	}
	if intent.DLBurst.Present() && !intent.DLBurst.Complete() {
		return fmt.Errorf("%w: dl_burst_size and dl_burst_duration must both be provided", ErrInvalidIntent)
	}
	return nil
}

func (p *BurstPolicy) transitDelay(explicit, e2e uint64) uint64 {
	if explicit > 0 {
		return explicit
	}
	if e2e > 0 {
		if inferred := uint64(math.Round(float64(e2e) * p.cfg.TransitDelayRatio)); inferred > 0 {
			return inferred
		}
	}
	return p.cfg.DefaultTransitDelayMS
}

func rateKbps(sizeKB, durationMS uint64) uint64 {
	return divideRoundedUp(sizeKB, 8_000, durationMS)
}

func pdbFromBudget(e2eDelayMS, fallback uint64) uint64 {
	if e2eDelayMS == 0 {
		return fallback
	}
	return uint64(math.Round(float64(e2eDelayMS) * 0.625))
}

func divideRoundedUp(value, multiplier, divisor uint64) uint64 {
	if divisor == 0 {
		return 0
	}
	if value > math.MaxUint64/multiplier {
		return math.MaxUint64
	}
	numerator := value * multiplier
	return (numerator + divisor - 1) / divisor
}
