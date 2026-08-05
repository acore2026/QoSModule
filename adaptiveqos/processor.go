package adaptiveqos

import (
	"context"
	"errors"
	"fmt"
)

var (
	ErrLimitsUnavailable = errors.New("qos limits unavailable")
	ErrEnforcementFailed = errors.New("qos enforcement failed")
)

type Processor struct {
	Policy         Policy
	LimitsProvider LimitsProvider
	Enforcer       Enforcer
}

func (p *Processor) Process(ctx context.Context, intent Intent) (Outcome, error) {
	if p == nil || p.Policy == nil {
		return Outcome{}, fmt.Errorf("%w: policy is not configured", ErrInvalidIntent)
	}
	if p.LimitsProvider == nil {
		return Outcome{}, fmt.Errorf("%w: provider is not configured", ErrLimitsUnavailable)
	}
	if p.Enforcer == nil {
		return Outcome{}, fmt.Errorf("%w: enforcer is not configured", ErrEnforcementFailed)
	}

	limits, err := p.LimitsProvider.Limits(ctx, intent.Flow)
	if err != nil {
		return Outcome{}, fmt.Errorf("%w: %v", ErrLimitsUnavailable, err)
	}
	decision, err := p.Policy.Generate(ctx, intent, limits)
	if err != nil {
		return Outcome{}, err
	}
	applied, err := p.Enforcer.Apply(ctx, intent, decision)
	if err != nil {
		return Outcome{}, fmt.Errorf("%w: %v", ErrEnforcementFailed, err)
	}
	return Outcome{Intent: intent, Decision: decision, Apply: applied}, nil
}
