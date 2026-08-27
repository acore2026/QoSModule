package smfenforcer

import (
	"context"
	"encoding/json"
	"testing"

	adaptiveqos "github.com/acore2026/adaptive-qos"
)

// TestBuildOAMBodyIncludesBurstMS 验证 smfenforcer 下发给 SMF OAM 的请求体包含
// burst_ms 字段(mock-ran 当 SMF 时用它驱动 sendrate 状态机; 真实 SMF 忽略)。
func TestBuildOAMBodyIncludesBurstMS(t *testing.T) {
	e := New(DefaultConfig())
	intent := adaptiveqos.Intent{
		RequestID: "req-1",
		Flow:      adaptiveqos.FlowSelector{UEAddress: "10.0.0.1", QFI: 4, RNTI: 1725},
		ULBurst:   adaptiveqos.BurstDemand{SizeKB: 1000, DurationMS: 7000},
		E2EDelayMS: 160,
	}
	limits := adaptiveqos.DefaultRANLimits()
	decision, err := adaptiveqos.NewBurstPolicy(adaptiveqos.DefaultBurstPolicyConfig()).Generate(context.Background(), intent, limits)
	if err != nil {
		t.Fatalf("policy generate: %v", err)
	}
	body, err := e.buildOAMBody(intent, decision)
	if err != nil {
		t.Fatalf("buildOAMBody: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	burst, ok := parsed["burst_ms"]
	if !ok {
		t.Fatalf("burst_ms missing in OAM body: %s", body)
	}
	// burstDurationMs = max(UL,DL) = 7000 (intent.ULBurst.DurationMS)
	if got, _ := burst.(float64); int(got) != 7000 {
		t.Fatalf("burst_ms = %v, want 7000; body=%s", burst, body)
	}
	// 确认 gbr_ul 为 "N bps" 字符串格式
	if gbr, _ := parsed["gbr_ul"].(string); gbr == "" {
		t.Fatalf("gbr_ul empty, want \"N bps\"; body=%s", body)
	}
	t.Logf("OAM body: %s", body)
}
