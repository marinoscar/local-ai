package ensure

import (
	"context"
	"errors"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/audit"
)

// engine.acestep (E11, #511): the music runtime from the kit's wheel bundle.

// The fakes' engine parts live here so the E11 additions stay in one file.
var (
	engineVersions = map[*fakeMachine]string{}
	engineOffers   = map[*fakeKit]toolOffer{}
)

func (m *fakeMachine) EngineVersion(engine, _ string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return engineVersions[m]
}

func (k *fakeKit) Engine(context.Context, string, string) (Source, string, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	o, ok := engineOffers[k]
	if !ok {
		return Source{}, "", ErrKitMissing
	}
	if o.err != nil {
		return Source{}, "", o.err
	}
	s := k.src0()
	s.File, s.SHA256 = s.KitDir+"/engines/acestep/"+o.version+"/acestep-"+o.version+"-cuda.tar.zst", "sum"
	return s, o.version, nil
}

func (f *fakeInstaller) InstallEngine(ctx context.Context, engine string, src Source, version, backend string) (ToolOutcome, error) {
	f.record("engine:" + engine + ":" + version + ":" + backend)
	if err := f.gate(ctx, "engine"); err != nil {
		return ToolOutcome{}, err
	}
	f.m.mu.Lock()
	prev := engineVersions[f.m]
	engineVersions[f.m] = version
	f.m.mu.Unlock()
	return ToolOutcome{Version: version, Previous: prev}, nil
}

func musicWants() Wants {
	return WantsFor([]string{"kit-engine-acestep-cuda"}, Facts{Backend: "cuda", MusicWanted: true})
}

func TestWantsForMusic(t *testing.T) {
	if w := musicWants(); w.EngineBackend != "cuda" {
		t.Errorf("wants %+v", w)
	}
	if w := WantsFor([]string{"kit-engine-acestep-cuda"}, Facts{Backend: "cuda"}); w.EngineBackend != "" {
		t.Error("music not wanted: no engine step")
	}
	if w := WantsFor([]string{"kit-engine-acestep-cuda"}, Facts{Backend: "vulkan", MusicWanted: true}); w.EngineBackend != "" {
		t.Error("no bundle for this backend: no engine step")
	}
}

func TestEngineStepPlan(t *testing.T) {
	cases := []struct {
		name      string
		installed string
		offer     *toolOffer
		status    Status
		code      string
	}{
		{"kit has it, nothing installed", "", &toolOffer{version: "1.5.0"}, StatusPending, ""},
		{"already installed (idempotent)", "1.5.0", &toolOffer{version: "1.5.0"}, StatusOK, ""},
		{"a newer bundle", "1.4.0", &toolOffer{version: "1.5.0"}, StatusPending, ""},
		{"no bundle and nothing installed", "", nil, StatusBlocked, ReasonKitMissing},
		{"damaged bundle", "", &toolOffer{err: ErrKitDamaged}, StatusBlocked, ReasonKitDamaged},
		{"no bundle, a runtime in use", "1.4.0", nil, StatusOK, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t).build()
			engineVersions[r.machine] = c.installed
			if c.offer != nil {
				engineOffers[r.kit] = *c.offer
			}
			t.Cleanup(func() { delete(engineVersions, r.machine); delete(engineOffers, r.kit) })
			p, err := r.planner().Plan(context.Background(), PlanInput{Auto: AutoOn, Wants: musicWants()})
			if err != nil {
				t.Fatal(err)
			}
			s := stepOf(t, p, IDEngineAcestep)
			if s.Status != c.status || (c.code != "" && s.Reason != c.code) || s.NeedsConfirmation {
				t.Errorf("step %+v", s)
			}
		})
	}
}

func TestEngineStepRuns(t *testing.T) {
	r := newRig(t).build()
	engineOffers[r.kit] = toolOffer{version: "1.5.0"}
	t.Cleanup(func() { delete(engineVersions, r.machine); delete(engineOffers, r.kit) })
	ex := &Executor{Installer: r.inst, Kit: r.kit, Audit: r.audit, Clock: r.clock}
	step := Step{ID: IDEngineAcestep, Kind: KindEngine, Target: "acestep", Item: "kit-engine-acestep-cuda", Status: StatusPending}
	res := ex.Run(context.Background(), step, audit.System(), "run-1")
	if res.Err != nil || res.Step.Status != StatusDone || res.Step.Version != "1.5.0" {
		t.Fatalf("result %+v", res)
	}
	if calls := r.inst.Calls(); len(calls) != 1 || calls[0] != "engine:acestep:1.5.0:cuda" {
		t.Errorf("calls %v", calls)
	}
	// A bundle that fails its check right before the install is LAI-KIT-001 and installs nothing.
	engineOffers[r.kit] = toolOffer{err: ErrKitDamaged}
	res = ex.Run(context.Background(), step, audit.System(), "run-2")
	if !errors.Is(res.Err, ErrKitDamaged) || res.Step.Status != StatusFailed || len(r.inst.Calls()) != 1 {
		t.Fatalf("damaged: %+v calls %v", res, r.inst.Calls())
	}
	// Off the allowlist: another target is refused before anything runs.
	bad := step
	bad.Target = "torch"
	if res := ex.Run(context.Background(), bad, audit.System(), "run-3"); !errors.Is(res.Err, ErrNotAllowed) {
		t.Fatalf("not allowed: %+v", res)
	}
}
