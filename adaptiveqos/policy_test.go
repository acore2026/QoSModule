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
	if got, want := decision.GBRULKbps, uint64(64000); got != want {
		t.Fatalf("GBRULKbps = %d, want %d", got, want)
	}
	if got, want := decision.Calculation.Target.GBRDLKbps, uint64(128000); got != want {
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

func TestBurstPolicyPrefersExplicitTransitDelay(t *testing.T) {
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
	if got, want := decision.GBRULKbps, uint64(16000); got != want {
		t.Fatalf("GBRULKbps = %d, want %d", got, want)
	}
	if got, want := decision.GBRDLKbps, uint64(20000); got != want {
		t.Fatalf("GBRDLKbps = %d, want %d", got, want)
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
