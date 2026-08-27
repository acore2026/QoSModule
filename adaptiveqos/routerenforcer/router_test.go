package routerenforcer

import (
	"bytes"
	"context"
	"errors"
	"log"
	"strings"
	"testing"

	adaptiveqos "github.com/acore2026/adaptive-qos"
)

// fakeEnforcer 记录是否被调用, 返回预设结果。
type fakeEnforcer struct {
	called  bool
	result  adaptiveqos.ApplyResult
	err     error
}

func (f *fakeEnforcer) Apply(_ context.Context, _ adaptiveqos.Intent, _ adaptiveqos.Decision) (adaptiveqos.ApplyResult, error) {
	f.called = true
	return f.result, f.err
}

func newFake(status string, err error) *fakeEnforcer {
	return &fakeEnforcer{result: adaptiveqos.ApplyResult{Status: status}, err: err}
}

// 1) UDP 成功 → 不走 mock-ran 也不走 SMF。
func TestAutoUDPSuccessNoFallback(t *testing.T) {
	udp := newFake(adaptiveqos.StatusAccepted, nil)
	ran := newFake(adaptiveqos.StatusAccepted, nil) // mock-ran
	smf := newFake(adaptiveqos.StatusAccepted, nil)
	r := New(ran, udp, smf, ModeAuto, nil)
	res, err := r.Apply(context.Background(), adaptiveqos.Intent{}, adaptiveqos.Decision{})
	if err != nil || res.Status != adaptiveqos.StatusAccepted {
		t.Fatalf("err=%v status=%s", err, res.Status)
	}
	if !udp.called || ran.called || smf.called {
		t.Fatalf("called: udp=%v ran=%v smf=%v (want udp only)", udp.called, ran.called, smf.called)
	}
}

// 2) UDP 失败 → 回退 mock-ran; mock-ran 成功则不再走 SMF。
func TestAutoUDPFailFallsBackToMockRan(t *testing.T) {
	udp := newFake("", errors.New("udp no gNB reply"))
	ran := newFake(adaptiveqos.StatusAccepted, nil) // mock-ran 成功
	smf := newFake(adaptiveqos.StatusAccepted, nil)
	r := New(ran, udp, smf, ModeAuto, nil)
	res, err := r.Apply(context.Background(), adaptiveqos.Intent{}, adaptiveqos.Decision{})
	if err != nil || res.Status != adaptiveqos.StatusAccepted {
		t.Fatalf("err=%v status=%s", err, res.Status)
	}
	if !udp.called || !ran.called {
		t.Fatalf("udp和mock-ran都应被调用: udp=%v ran=%v", udp.called, ran.called)
	}
	if smf.called {
		t.Fatal("mock-ran 成功后不应再走 SMF")
	}
}

// 3) UDP 失败 + mock-ran 失败 → 回退真 SMF。
func TestAutoMockRanFailFallsBackToSMF(t *testing.T) {
	udp := newFake("", errors.New("udp fail"))
	ran := newFake("", errors.New("mock-ran down")) // mock-ran 也失败
	smf := newFake(adaptiveqos.StatusAccepted, nil)
	r := New(ran, udp, smf, ModeAuto, nil)
	res, err := r.Apply(context.Background(), adaptiveqos.Intent{}, adaptiveqos.Decision{})
	if err != nil || res.Status != adaptiveqos.StatusAccepted {
		t.Fatalf("err=%v status=%s (应回退到 SMF)", err, res.Status)
	}
	if !udp.called || !ran.called || !smf.called {
		t.Fatalf("三档都应被调用: udp=%v ran=%v smf=%v", udp.called, ran.called, smf.called)
	}
}

// 4) 三档全失败 → 返回 SMF 的错误。
func TestAutoAllFailReturnsError(t *testing.T) {
	udp := newFake("", errors.New("udp fail"))
	ran := newFake("", errors.New("mock-ran fail"))
	smf := newFake("", errors.New("smf fail"))
	r := New(ran, udp, smf, ModeAuto, nil)
	_, err := r.Apply(context.Background(), adaptiveqos.Intent{}, adaptiveqos.Decision{})
	if err == nil {
		t.Fatal("三档全失败应返回 error")
	}
}

// 5) mock-ran 返回 REJECTED(非 err) 也触发回退到 SMF。
func TestAutoMockRanRejectedFallsBackToSMF(t *testing.T) {
	udp := newFake("", errors.New("udp fail"))
	ran := newFake(adaptiveqos.StatusRejected, nil) // mock-ran 显式拒绝
	smf := newFake(adaptiveqos.StatusAccepted, nil)
	r := New(ran, udp, smf, ModeAuto, nil)
	res, _ := r.Apply(context.Background(), adaptiveqos.Intent{}, adaptiveqos.Decision{})
	if res.Status != adaptiveqos.StatusAccepted {
		t.Fatalf("status=%s want ACCEPTED (from smf)", res.Status)
	}
	if !smf.called {
		t.Fatal("mock-ran REJECTED 应回退到 SMF")
	}
}

// 6) 日志: auto 各档分阶段计时行确实打出, 含 stage 名/结果/done|fallback。
func TestAutoLogsStageTiming(t *testing.T) {
	var buf bytes.Buffer
	logger := log.New(&buf, "", 0)
	udp := newFake("", errors.New("udp i/o timeout")) // 失败档
	ran := newFake(adaptiveqos.StatusAccepted, nil)   // mock-ran 成功
	smf := newFake(adaptiveqos.StatusAccepted, nil)
	r := New(ran, udp, smf, ModeAuto, logger)
	r.Apply(context.Background(), adaptiveqos.Intent{}, adaptiveqos.Decision{})
	out := buf.String()
	// 第1档应记录失败回退, 第2档记录成功完成, 第3档不应被打到
	for _, want := range []string{
		"auto 1/3 udp-ran",
		"err:",
		"-> fallback",
		"auto 2/3 mock-ran",
		"ACCEPTED",
		"-> done",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("log missing %q; got:\n%s", want, out)
		}
	}
	if strings.Contains(out, "auto 3/3 smf") {
		t.Errorf("smf 第3档不应被触达, 但日志出现:\n%s", out)
	}
}

// 7) nil logger 时静默不 panic。
func TestAutoNilLoggerNoPanic(t *testing.T) {
	udp := newFake("", errors.New("udp fail"))
	ran := newFake(adaptiveqos.StatusAccepted, nil)
	r := New(ran, udp, nil, ModeAuto, nil) // logger=nil
	if _, err := r.Apply(context.Background(), adaptiveqos.Intent{}, adaptiveqos.Decision{}); err != nil {
		t.Fatalf("nil logger 不应 panic 或报错, got err=%v", err)
	}
}
