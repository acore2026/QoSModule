package adaptiveqos

import (
	"context"
	"errors"
	"testing"
)

func TestBurstPolicyMatchesDesignExample(t *testing.T) {
	policy := NewBurstPolicy(DefaultBurstPolicyConfig())
	decision, err := policy.Generate(context.Background(), Intent{
		RequestID:  "req-1",
		Flow:       FlowSelector{RNTI: 11222, QFI: 1},
		ULBurst:    BurstDemand{SizeKB: 1024, DurationMS: 100},
		DLBurst:    BurstDemand{SizeKB: 2048, DurationMS: 100},
		E2EDelayMS: 160,
	}, DefaultRANLimits())
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}

	if got, want := decision.MBRULKbps, uint64(81920); got != want {
		t.Fatalf("MBRULKbps = %d, want %d", got, want)
	}
	if got, want := decision.MBRDLKbps, uint64(163840); got != want {
		t.Fatalf("MBRDLKbps = %d, want %d", got, want)
	}
	if got, want := decision.GBRULKbps, uint64(81920); got != want {
		t.Fatalf("GBRULKbps = %d, want %d", got, want)
	}
	if got, want := decision.Calculation.Target.GBRDLKbps, uint64(163840); got != want {
		t.Fatalf("target GBRDLKbps = %d, want %d", got, want)
	}
	if got, want := decision.GBRDLKbps, uint64(100000); got != want {
		t.Fatalf("clipped GBRDLKbps = %d, want %d", got, want)
	}
	if got, want := decision.PDBMS, uint64(100); got != want {
		t.Fatalf("PDBMS = %d, want %d", got, want)
	}
	if got, want := decision.Priority, uint8(3); got != want {
		t.Fatalf("Priority = %d, want %d", got, want)
	}
}

// GBR is computed from burst_duration, not transit delay. Explicit transit
// delay values must not influence GBR.
func TestBurstPolicyGBRIgnoresTransitDelay(t *testing.T) {
	policy := NewBurstPolicy(DefaultBurstPolicyConfig())
	decision, err := policy.Generate(context.Background(), Intent{
		Flow:             FlowSelector{RNTI: 1, QFI: 2},
		ULBurst:          BurstDemand{SizeKB: 100, DurationMS: 100},
		DLBurst:          BurstDemand{SizeKB: 200, DurationMS: 100},
		E2EDelayMS:       160,
		ULTransitDelayMS: 50,
		DLTransitDelayMS: 80,
	}, Limits{})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	// GBR = burst_size / burst_duration (transit delay ignored).
	if got, want := decision.GBRULKbps, uint64(8000); got != want {
		t.Fatalf("GBRULKbps = %d, want %d", got, want)
	}
	if got, want := decision.GBRDLKbps, uint64(16000); got != want {
		t.Fatalf("GBRDLKbps = %d, want %d", got, want)
	}
	// MBR = burst_size / e2e_delay, enforcement raises to GBR when dur < e2e.
	if got, want := decision.MBRULKbps, uint64(8000); got != want {
		t.Fatalf("MBRULKbps = %d, want %d", got, want)
	}
	if got, want := decision.MBRDLKbps, uint64(16000); got != want {
		t.Fatalf("MBRDLKbps = %d, want %d", got, want)
	}
}

func TestBurstPolicyRejectsEachMissingRequiredBurstField(t *testing.T) {
	policy := NewBurstPolicy(DefaultBurstPolicyConfig())
	valid := Intent{
		Flow:       FlowSelector{RNTI: 1, QFI: 2},
		ULBurst:    BurstDemand{SizeKB: 100, DurationMS: 50},
		E2EDelayMS: 160,
	}
	tests := []struct {
		name   string
		mutate func(*Intent)
	}{
		{name: "ul burst size", mutate: func(intent *Intent) { intent.ULBurst.SizeKB = 0 }},
		{name: "ul burst duration", mutate: func(intent *Intent) { intent.ULBurst.DurationMS = 0 }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			intent := valid
			tt.mutate(&intent)
			_, err := policy.Generate(context.Background(), intent, DefaultRANLimits())
			if !errors.Is(err, ErrInvalidIntent) {
				t.Fatalf("Generate() error = %v, want ErrInvalidIntent", err)
			}
		})
	}
}

func TestBurstPolicyAllowsMissingDLBurst(t *testing.T) {
	policy := NewBurstPolicy(DefaultBurstPolicyConfig())
	decision, err := policy.Generate(context.Background(), Intent{
		Flow:       FlowSelector{RNTI: 1, QFI: 2},
		ULBurst:    BurstDemand{SizeKB: 100, DurationMS: 50},
		E2EDelayMS: 160,
	}, DefaultRANLimits())
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if decision.MBRULKbps == 0 || decision.GBRULKbps == 0 {
		t.Fatalf("UL QoS was not calculated: %+v", decision)
	}
	if decision.MBRDLKbps != 0 || decision.GBRDLKbps != 0 || decision.Calculation.DLTransitDelayMS != 0 {
		t.Fatalf("DL QoS should be omitted when DL burst is missing: %+v", decision)
	}
}

func TestBurstPolicyRejectsPartialDLBurst(t *testing.T) {
	policy := NewBurstPolicy(DefaultBurstPolicyConfig())
	tests := []Intent{
		{Flow: FlowSelector{RNTI: 1, QFI: 2}, ULBurst: BurstDemand{SizeKB: 100, DurationMS: 50}, DLBurst: BurstDemand{SizeKB: 200}, E2EDelayMS: 160},
		{Flow: FlowSelector{RNTI: 1, QFI: 2}, ULBurst: BurstDemand{SizeKB: 100, DurationMS: 50}, DLBurst: BurstDemand{DurationMS: 50}, E2EDelayMS: 160},
	}
	for _, intent := range tests {
		_, err := policy.Generate(context.Background(), intent, DefaultRANLimits())
		if !errors.Is(err, ErrInvalidIntent) {
			t.Fatalf("Generate() error = %v, want ErrInvalidIntent", err)
		}
	}
}

func TestBurstPolicyRejectsMissingE2EDelay(t *testing.T) {
	policy := NewBurstPolicy(DefaultBurstPolicyConfig())
	_, err := policy.Generate(context.Background(), Intent{
		Flow:    FlowSelector{RNTI: 1, QFI: 2},
		ULBurst: BurstDemand{SizeKB: 100, DurationMS: 50},
	}, DefaultRANLimits())
	if !errors.Is(err, ErrInvalidIntent) {
		t.Fatalf("Generate() error = %v, want ErrInvalidIntent", err)
	}
}

// When burst_duration > e2e_delay (typical streaming traffic), MBR
// (burst_size/e2e) naturally exceeds GBR (burst_size/duration) without
// needing enforcement.
func TestBurstPolicyEnsuresMBRAtLeastGBR(t *testing.T) {
	policy := NewBurstPolicy(DefaultBurstPolicyConfig())
	// burst_duration=3000ms >> e2e=160ms
	decision, err := policy.Generate(context.Background(), Intent{
		Flow:       FlowSelector{RNTI: 1, QFI: 1},
		ULBurst:    BurstDemand{SizeKB: 671, DurationMS: 3000},
		DLBurst:    BurstDemand{SizeKB: 671, DurationMS: 3000},
		E2EDelayMS: 160,
	}, DefaultRANLimits())
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}

	// MBR = 671*8000/160 = 33550; GBR = 671*8000/3000 = 1790.
	if got, want := decision.Calculation.Target.MBRULKbps, uint64(33550); got != want {
		t.Fatalf("target MBRULKbps = %d, want %d", got, want)
	}
	if got, want := decision.Calculation.Target.GBRULKbps, uint64(1790); got != want {
		t.Fatalf("target GBRULKbps = %d, want %d", got, want)
	}
	// MBR > GBR naturally; applied values inherit this.
	if decision.MBRULKbps < decision.GBRULKbps {
		t.Fatalf("applied MBRULKbps = %d < GBRULKbps = %d", decision.MBRULKbps, decision.GBRULKbps)
	}
	if decision.MBRDLKbps < decision.GBRDLKbps {
		t.Fatalf("applied MBRDLKbps = %d < GBRDLKbps = %d", decision.MBRDLKbps, decision.GBRDLKbps)
	}
	if got, want := decision.MBRULKbps, uint64(33550); got != want {
		t.Fatalf("applied MBRULKbps = %d, want %d", got, want)
	}
	if got, want := decision.GBRULKbps, uint64(1790); got != want {
		t.Fatalf("applied GBRULKbps = %d, want %d", got, want)
	}
}

// When burst_duration < e2e_delay, GBR > MBR. Enforcement raises MBR to
// GBR at the target level so MBR >= GBR holds even after GBR is clipped
// to the RAN limit.
func TestBurstPolicyMBRNotLessThanClippedGBR(t *testing.T) {
	policy := NewBurstPolicy(DefaultBurstPolicyConfig())
	// burst_size=2048, burst_duration=100ms, e2e=160ms
	// MBR_raw = 2048*8000/160 = 102400; GBR_target = 2048*8000/100 = 163840
	decision, err := policy.Generate(context.Background(), Intent{
		Flow:       FlowSelector{RNTI: 1, QFI: 1},
		ULBurst:    BurstDemand{SizeKB: 2048, DurationMS: 100},
		E2EDelayMS: 160,
	}, DefaultRANLimits())
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if decision.MBRULKbps < decision.GBRULKbps {
		t.Fatalf("MBRULKbps = %d < GBRULKbps = %d", decision.MBRULKbps, decision.GBRULKbps)
	}
	// target MBR raised to 163840, applied MBR = 163840 (uncapped), applied GBR = 100000 (capped).
	if got, want := decision.Calculation.Target.MBRULKbps, uint64(163840); got != want {
		t.Fatalf("target MBRULKbps = %d, want %d", got, want)
	}
	if got, want := decision.MBRULKbps, uint64(163840); got != want {
		t.Fatalf("applied MBRULKbps = %d, want %d", got, want)
	}
	if got, want := decision.GBRULKbps, uint64(100000); got != want {
		t.Fatalf("applied GBRULKbps = %d, want %d", got, want)
	}
}
