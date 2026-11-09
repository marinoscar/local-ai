package checks

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/drill"
	"github.com/marinoscar/local-ai/agent/internal/netx"
	"github.com/marinoscar/local-ai/agent/internal/state"
)

// memState is an in-memory drill.StateStore.
type memState struct {
	mu sync.Mutex
	st state.State
}

func (m *memState) Snapshot() state.State {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.st.Clone()
}

func (m *memState) MutateSync(fn func(*state.State) error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return fn(&m.st)
}

// link records the order of Pause and Resume.
type link struct {
	mu     sync.Mutex
	events []string
}

func (l *link) Pause()  { l.add("pause") }
func (l *link) Resume() { l.add("resume") }
func (l *link) add(s string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, s)
}

type rig struct {
	t    *testing.T
	fz   *netx.Fence
	link *link
	reg  *drill.Registry
	eng  *drill.Engine
}

func newRig(t *testing.T) *rig {
	t.Helper()
	r := &rig{t: t, fz: netx.NewFence(), link: &link{}, reg: drill.NewRegistry()}
	eng, err := drill.New(drill.Deps{
		Registry: r.reg, Results: drill.NewResults(drill.ResultsOptions{Dir: t.TempDir()}),
		State:  &memState{st: state.State{SchemaVersion: state.SchemaVersion, Mode: state.ModeOnline}},
		Fence:  &drill.PausingFence{Link: r.link, Inner: &NetFence{Fence: r.fz}},
		NodeID: "ai-n3", AgentVersion: "test", CatalogVersion: func() string { return "c1" },
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	r.eng = eng
	return r
}

func (r *rig) add(c drill.Check) {
	r.t.Helper()
	if err := r.reg.Add(c); err != nil {
		r.t.Fatal(err)
	}
}

// fence adds check 1 over this rig's fence and a real fenced transport.
func (r *rig) fence() {
	r.add(Fence(FenceConfig{Active: r.fz.Active, Transport: netx.Via(r.fz.NewTransport("agent", nil), "agent")}))
}

func (r *rig) run() drill.Document {
	r.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	res, err := r.eng.Start(ctx, drill.StartRequest{Trigger: drill.TriggerManual})
	if err != nil {
		r.t.Fatal(err)
	}
	if err := r.eng.Wait(ctx); err != nil {
		r.t.Fatal(err)
	}
	raw, err := r.eng.Get(res.RunID)
	if err != nil {
		r.t.Fatal(err)
	}
	var d drill.Document
	if err := json.Unmarshal(raw, &d); err != nil {
		r.t.Fatal(err)
	}
	return d
}

func byID(d drill.Document, id string) drill.CheckResult {
	for _, c := range d.Checks {
		if c.ID == id {
			return c
		}
	}
	return drill.CheckResult{}
}

func TestRunWithBothChecksPassesWithAQuietFence(t *testing.T) {
	r := newRig(t)
	n := newNode(t)
	r.fence()
	r.add(n.lint("dist-clean").Check())
	d := r.run()
	if d.Overall != drill.OverallPass || !d.SurvivalReady || d.Counts.Pass != 2 {
		t.Fatalf("doc = overall %s ready %v counts %+v checks %+v", d.Overall, d.SurvivalReady, d.Counts, d.Checks)
	}
	if !d.NetworkFence.Enabled || d.NetworkFence.BlockedDials != 0 || d.NetworkFence.Code != "" {
		t.Errorf("network_fence = %+v", d.NetworkFence)
	}
	if got := r.link.events; len(got) != 2 || got[0] != "pause" || got[1] != "resume" {
		t.Errorf("the cloud link must be paused for the run and resumed after: %v", got)
	}
	if r.fz.Active() {
		t.Error("the fence is still on after the run")
	}
}

func TestADialDuringTheRunTurnsItIntoAWarningNamingTheCaller(t *testing.T) {
	r := newRig(t)
	n := newNode(t)
	r.fence()
	r.add(n.lint("dist-clean").Check())
	tr := r.fz.NewTransport("catalog.refresh", nil)
	r.add(drill.Check{ID: "drl.sneaky", Area: "x", Order: 3, Run: func(ctx context.Context, _ *drill.Env) drill.Outcome {
		req, _ := http.NewRequestWithContext(netx.WithCaller(ctx, "sync.resolver"), http.MethodGet, "https://huggingface.co/api/models", nil)
		_, _ = (&http.Client{Transport: tr}).Do(req) // fails: the fence refuses, the feature shrugs
		return drill.Outcome{Result: drill.ResultPass}
	}})
	d := r.run()
	if d.Overall != drill.OverallWarn || d.NetworkFence.BlockedDials != 1 || d.NetworkFence.Code != doctor.DRL091 {
		t.Fatalf("overall=%s fence=%+v", d.Overall, d.NetworkFence)
	}
	if len(d.NetworkFence.Callers) != 1 || d.NetworkFence.Callers[0] != "huggingface.co:443 (sync.resolver)" {
		t.Errorf("callers = %q", d.NetworkFence.Callers)
	}
	f := byID(d, "drl.fence")
	if f.Result != drill.ResultWarn || f.Code != doctor.DRL091 || f.Details["blocked_count"] != float64(1) ||
		f.Details["blocked"] != "huggingface.co:443 (sync.resolver)" {
		t.Errorf("fence check = %+v", f)
	}
	if byID(d, "drl.sneaky").Result != drill.ResultPass {
		t.Error("the feature's own check is not what turns the run into a warning")
	}
}

func TestABrokenFenceAbortsTheMainPhaseButStillRestoresAndResumes(t *testing.T) {
	r := newRig(t)
	n := newNode(t)
	r.add(Fence(FenceConfig{
		Active: func() bool { return true },
		Transport: rtFunc(func(rq *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Body: http.NoBody, Request: rq}, nil
		}),
	}))
	r.add(n.lint("dist-clean").Check())
	d := r.run()
	f := byID(d, "drl.fence")
	if f.Result != drill.ResultFail || f.Code != doctor.DRL091 || d.Overall != drill.OverallFail {
		t.Fatalf("fence check = %+v overall %s", f, d.Overall)
	}
	lint := byID(d, OfflineLintID)
	if lint.Result != drill.ResultSkip || lint.Details["reason"] != "run_stopped" || lint.Details["by"] != "drl.fence" {
		t.Errorf("the check after a broken fence = %+v, want skipped run_stopped", lint)
	}
	if d.SurvivalReady {
		t.Error("a run whose critical check was skipped is not survival ready")
	}
	if got := r.link.events; len(got) != 2 || got[1] != "resume" {
		t.Errorf("link = %v", got)
	}
	if r.fz.Active() {
		t.Error("the fence is still on")
	}
}

func TestALintFailureFailsTheRunWithTheCodeAndTheSettingNamed(t *testing.T) {
	r := newRig(t)
	n := newNode(t)
	n.copy("models.ini.hf-repo", n.ini)
	r.fence()
	r.add(n.lint("dist-clean").Check())
	d := r.run()
	lint := byID(d, OfflineLintID)
	if d.Overall != drill.OverallFail || lint.Result != drill.ResultFail || lint.Code != doctor.DRL010 || d.SurvivalReady {
		t.Fatalf("overall=%s lint=%+v ready=%v", d.Overall, lint, d.SurvivalReady)
	}
	if s, _ := lint.Details["remote"].(string); s == "" {
		t.Errorf("details = %v", lint.Details)
	}
	if byID(d, "drl.fence").Result != drill.ResultPass {
		t.Error("check 1 is independent of check 2")
	}
}

func TestOnlyCheckTwoStillRunsUnderTheFence(t *testing.T) {
	r := newRig(t)
	n := newNode(t)
	r.fence()
	r.add(n.lint("dist-clean").Check())
	ctx := context.Background()
	res, err := r.eng.Start(ctx, drill.StartRequest{Only: []string{OfflineLintID}})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.eng.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	raw, _ := r.eng.Get(res.RunID)
	var d drill.Document
	_ = json.Unmarshal(raw, &d)
	if len(d.Checks) != 1 || d.Checks[0].ID != OfflineLintID || !d.NetworkFence.Enabled {
		t.Fatalf("doc = %+v", d)
	}
}
