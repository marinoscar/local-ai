package drill

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
)

func TestBlockedDialsAreHungOnTheFenceCheckWithFlatDetails(t *testing.T) {
	r := newRig(t)
	r.fence.report = FenceReport{BlockedDials: 3, Callers: []string{"huggingface.co:443 (sync.resolver)", "api.example.test:443 (cloud) x2"}}
	r.add(Check{ID: FenceCheckID, Area: "network", Order: 1, Run: func(context.Context, *Env) Outcome {
		return Outcome{Result: ResultPass, Details: map[string]any{"refused": true}}
	}})
	r.add(Check{ID: "drl.a", Area: "x", Order: 2, Run: run0})
	_, d := r.startWait(StartRequest{})
	var fc CheckResult
	for _, c := range d.Checks {
		if c.ID == FenceCheckID {
			fc = c
		}
	}
	if fc.Result != ResultWarn || fc.Code != doctor.DRL091 {
		t.Fatalf("fence check = %+v, want warn with LAI-DRL-091", fc)
	}
	if fc.Details["blocked_count"] != float64(3) || fc.Details["refused"] != true ||
		fc.Details["blocked"] != "huggingface.co:443 (sync.resolver); api.example.test:443 (cloud) x2" {
		t.Fatalf("details = %v", fc.Details)
	}
	for k, v := range fc.Details {
		switch v.(type) {
		case string, float64, bool, nil: // the document was decoded from JSON
		default:
			t.Errorf("detail %s is a %T: details must be flat", k, v)
		}
	}
	if d.Overall != OverallWarn || d.Counts.Warn != 1 || d.Counts.Pass != 1 || d.NetworkFence.Code != doctor.DRL091 {
		t.Fatalf("overall=%s counts=%+v fence=%+v", d.Overall, d.Counts, d.NetworkFence)
	}
}

func TestBlockedDialsWithoutTheFenceCheckStillWarn(t *testing.T) {
	r := newRig(t)
	r.fence.report = FenceReport{BlockedDials: 1, Callers: []string{"x:1 (y)"}}
	r.add(Check{ID: "drl.a", Area: "x", Order: 1, Run: run0})
	_, d := r.startWait(StartRequest{})
	if d.Overall != OverallWarn || d.Checks[0].Result != ResultPass || d.Checks[0].Details != nil {
		t.Fatalf("doc = %+v", d)
	}
}

func TestFenceDetailsAreBounded(t *testing.T) {
	var callers []string
	for i := 0; i < 100; i++ {
		callers = append(callers, "host-é"+strings.Repeat("x", 20)+".example.test:443 (caller)")
	}
	d := FenceDetails(FenceReport{BlockedDials: 100, Callers: callers})
	s := d["blocked"].(string)
	if len(s) > 500 || !utf8.ValidString(s) || d["blocked_count"] != 100 { // FenceDetails itself returns an int
		t.Fatalf("len=%d valid=%v count=%v", len(s), utf8.ValidString(s), d["blocked_count"])
	}
	if _, ok := FenceDetails(FenceReport{BlockedDials: 2})["blocked"]; ok {
		t.Error("blocked must be absent when no caller is known")
	}
}

func TestSubprocessEnvHasOnlyTheDeadProxy(t *testing.T) {
	got := (&Env{}).SubprocessEnv([]string{"PATH=/usr/bin", "https_proxy=http://corp.example.test:3128", "NO_PROXY=x", "HTTP_PROXY=y"})
	want := []string{"PATH=/usr/bin", "HTTP_PROXY=http://127.0.0.1:9", "HTTPS_PROXY=http://127.0.0.1:9"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("env = %v, want %v", got, want)
	}
}
