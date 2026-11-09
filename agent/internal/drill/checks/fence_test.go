package checks

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/drill"
	"github.com/marinoscar/local-ai/agent/internal/netx"
)

type rtFunc func(*http.Request) (*http.Response, error)

func (f rtFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// realFenceCheck is check 1 over a real fenced transport and a private fence.
func realFenceCheck(f *netx.Fence) drill.Check {
	return Fence(FenceConfig{Active: f.Active, Transport: netx.Via(f.NewTransport("agent", nil), "agent")})
}

func TestFenceCheckPassesWhenTheFenceRefusesTheDial(t *testing.T) {
	f := netx.NewFence()
	l := f.Acquire()
	out := run(t, realFenceCheck(f))
	rep := l.Release()
	if out.Result != drill.ResultPass || out.Code != "" || out.StopRun {
		t.Fatalf("outcome = %+v", out)
	}
	if out.Details["target"] != FenceTarget || out.Details["refused"] != true {
		t.Errorf("details = %v", out.Details)
	}
	if rep.Blocked != 0 {
		t.Errorf("the check's own probe was counted as a blocked dial: %+v", rep)
	}
}

func TestFenceCheckFailsAndStopsTheRunWhenADialSucceeds(t *testing.T) {
	called := 0
	rt := rtFunc(func(r *http.Request) (*http.Response, error) {
		called++
		if r.URL.Host != FenceTarget || r.URL.Scheme != "https" {
			t.Errorf("probe URL = %s", r.URL)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
	})
	out := run(t, Fence(FenceConfig{Active: func() bool { return true }, Transport: rt}))
	if called != 1 || out.Result != drill.ResultFail || out.Code != doctor.DRL091 || !out.StopRun {
		t.Fatalf("called=%d outcome=%+v", called, out)
	}
	if out.Details["reason"] != "dial_succeeded" || out.Details["status"] != 200 {
		t.Errorf("details = %v", out.Details)
	}
}

func TestFenceCheckFailsWhenNoFenceIsOnWithoutDialing(t *testing.T) {
	rt := rtFunc(func(*http.Request) (*http.Response, error) {
		t.Error("nothing may be dialed when the fence is off: the dial would leave the machine")
		return nil, errors.New("unreachable")
	})
	out := run(t, Fence(FenceConfig{Active: func() bool { return false }, Transport: rt}))
	if out.Result != drill.ResultFail || !out.StopRun || out.Details["reason"] != "fence_inactive" {
		t.Fatalf("outcome = %+v", out)
	}
}

func TestFenceCheckFailsWhenTheDialFailsForAnotherReason(t *testing.T) {
	rt := rtFunc(func(*http.Request) (*http.Response, error) { return nil, errors.New("dial tcp: lookup failed") })
	out := run(t, Fence(FenceConfig{Active: func() bool { return true }, Transport: rt}))
	if out.Result != drill.ResultFail || !out.StopRun || out.Details["reason"] != "not_refused_by_the_fence" {
		t.Fatalf("outcome = %+v", out)
	}
}

func TestFenceCheckProbeCarriesTheCallerAndProbeMark(t *testing.T) {
	f := netx.NewFence()
	l := f.Acquire()
	defer l.Release()
	var seen string
	rt := rtFunc(func(r *http.Request) (*http.Response, error) {
		seen = netx.CallerFrom(r.Context())
		_, err := f.Wrap("x", nil)(r.Context(), "tcp", r.URL.Host)
		return nil, err
	})
	out := run(t, Fence(FenceConfig{Active: f.Active, Transport: rt}))
	if seen != "drill.fence" || out.Result != drill.ResultPass {
		t.Fatalf("caller=%q outcome=%+v", seen, out)
	}
	if rep := l.Release(); rep.Blocked != 0 {
		t.Errorf("probe counted: %+v", rep)
	}
}

func TestCheckRowsAreRegistrable(t *testing.T) {
	reg := drill.NewRegistry()
	for _, c := range []drill.Check{Fence(FenceConfig{}), OfflineLint{}.Check()} {
		if err := reg.Add(c); err != nil {
			t.Fatalf("%s: %v", c.ID, err)
		}
	}
	got, err := reg.Select(context.Background(), nil)
	if err != nil || len(got) != 2 || got[0].ID != "drl.fence" || got[1].ID != "drl.config.offline_lint" {
		t.Fatalf("checks = %v err=%v", got, err)
	}
	if got[0].Critical || !got[1].Critical || got[0].Order != 1 || got[1].Order != 2 {
		t.Errorf("order or star wrong: %+v %+v", got[0], got[1])
	}
	if got[0].FailCode != doctor.DRL091 || got[1].FailCode != doctor.DRL010 {
		t.Errorf("fail codes = %s %s", got[0].FailCode, got[1].FailCode)
	}
}

func TestNetFenceEnableDisableAndNesting(t *testing.T) {
	f := netx.NewFence()
	nf := &NetFence{Fence: f}
	if f.Active() {
		t.Fatal("on before Enable")
	}
	nf.Enable()
	if !f.Active() {
		t.Fatal("off after Enable")
	}
	dial := f.Wrap("catalog.refresh", nil)
	ctx := netx.WithCaller(context.Background(), "sync.resolver")
	_, _ = dial(ctx, "tcp", "huggingface.co:443")
	_, _ = dial(ctx, "tcp", "huggingface.co:443")
	_, _ = dial(context.Background(), "tcp", "api.example.test:443")

	outer := f.Acquire() // someone else holds the fence too
	rep := nf.Disable()
	if !f.Active() {
		t.Fatal("the drill's Disable turned off a fence another holder still has")
	}
	outer.Release()
	if f.Active() {
		t.Fatal("on after every holder released")
	}
	if rep.BlockedDials != 3 {
		t.Fatalf("blocked = %d", rep.BlockedDials)
	}
	want := []string{"huggingface.co:443 (sync.resolver) x2", "api.example.test:443 (catalog.refresh)"}
	if strings.Join(rep.Callers, "|") != strings.Join(want, "|") {
		t.Errorf("callers = %q, want %q", rep.Callers, want)
	}
	// Disable with nothing held is harmless.
	if r := nf.Disable(); r.BlockedDials != 0 || f.Active() {
		t.Errorf("empty disable = %+v active=%v", r, f.Active())
	}
	// Two runs in a row start clean.
	nf.Enable()
	if r := nf.Disable(); r.BlockedDials != 0 {
		t.Errorf("second run blocked = %d", r.BlockedDials)
	}
}

func TestNetFenceReleasedByDeferAfterAPanic(t *testing.T) {
	f := netx.NewFence()
	nf := &NetFence{Fence: f}
	func() {
		defer func() { _ = recover() }()
		nf.Enable()
		defer nf.Disable()
		panic("a check blew up")
	}()
	if f.Active() {
		t.Fatal("a panic left the fence on")
	}
}

func TestDefaultNetFenceUsesTheAgentFence(t *testing.T) {
	nf := &NetFence{}
	nf.Enable()
	if !netx.Default.Active() {
		t.Fatal("the default NetFence must hold netx.Default")
	}
	nf.Disable()
	if netx.Default.Active() {
		t.Fatal("still on")
	}
}
