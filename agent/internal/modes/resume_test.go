package modes_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/events"
	"github.com/marinoscar/local-ai/agent/internal/modes"
	"github.com/marinoscar/local-ai/agent/internal/state"
	statefake "github.com/marinoscar/local-ai/agent/internal/state/fake"
)

// policyEffects are the steady-state effects of a mode (agent.md 4.2.4).
func policyEffects(m modes.Mode) []string {
	if m == modes.Online {
		return []string{"tailscale.on", "link.start", "prober.start"}
	}
	return []string{"tailscale.off"}
}

func (r *rig) resume() modes.Result {
	r.t.Helper()
	res, err := r.eng.Resume(context.Background())
	if err != nil {
		r.t.Fatal(err)
	}
	return res
}

// dryRunWrites counts the synchronous state writes of one uninterrupted transition.
func dryRunWrites(t *testing.T, from, to modes.Mode) int {
	t.Helper()
	var ks *killStore
	r := newRig(t, withMode(from), withWrap(func(st *state.Store) modes.StateStore {
		ks = newKillStore(st, 0, false)
		return ks
	}))
	r.mustRequest(to, true)
	return ks.writes()
}

// The agent is killed (the goroutine dies at once, as with kill -9) just before or just after each
// synchronous state write of a transition. A new agent over the same disk, after a power-cut style
// reboot of the filesystem, resumes at the recorded step, re-runs only what the recorded step and the
// steps after it need, and ends in the target mode with the transition cleared.
func TestKillAtEveryWriteResumesIdempotently(t *testing.T) {
	for _, from := range allModes {
		for _, to := range allModes {
			if from == to {
				continue
			}
			total := dryRunWrites(t, from, to)
			if total < len(modes.Steps(to)) {
				t.Fatalf("%s->%s: only %d writes for %d steps", from, to, total, len(modes.Steps(to)))
			}
			for k := 1; k <= total; k++ {
				for _, before := range []bool{false, true} {
					when := "after"
					if before {
						when = "before"
					}
					t.Run(fmt.Sprintf("%s_to_%s/%s_write_%d", from, to, when, k), func(t *testing.T) {
						killAndResume(t, from, to, k, before)
					})
				}
			}
		}
	}
}

func killAndResume(t *testing.T, from, to modes.Mode, k int, before bool) {
	fs := statefake.New()
	var ks *killStore
	r1 := newRig(t, withMode(from), withFS(fs), withWrap(func(st *state.Store) modes.StateStore {
		ks = newKillStore(st, k, before)
		return ks
	}))
	go func() {
		// A kill inside begin ends this goroutine; a kill later ends the engine's own.
		_, _ = r1.eng.Request(context.Background(), to, modes.RequestOptions{Actor: operator})
	}()
	select {
	case <-ks.crashed:
	case <-time.After(5 * time.Second):
		t.Fatal("the kill point was never reached")
	}
	r1.abandon = true
	_ = r1.st.Close()
	fs.Reboot() // bytes that were never fsynced are gone

	r2 := newRig(t, withFS(fs), noSeed())
	snap := r2.st.Snapshot()
	mt := snap.ModeTransition
	if snap.Mode != from && snap.Mode != to {
		t.Fatalf("recovered mode %q", snap.Mode)
	}
	r2.resume()
	r2.idle()

	got := without(r2.env.Effects(), "portal.drop")
	final := r2.st.Snapshot()
	switch {
	case mt != nil:
		if snap.Mode != from {
			t.Fatalf("transition recorded but mode already %s", snap.Mode)
		}
		i := indexOf(modes.Steps(to), mt.Step)
		if i < 0 {
			t.Fatalf("recorded step %q is not a step of %s", mt.Step, to)
		}
		if want := effectsFrom(to, i); !equalStrings(got, want) {
			t.Fatalf("resume at %s ran\n got  %s\n want %s", mt.Step, joined(got), joined(want))
		}
		if final.Mode != to {
			t.Fatalf("resumed transition ended in %s, want %s", final.Mode, to)
		}
		if !strings.Contains(r2.logs.String(), modes.CodeResumed) {
			t.Fatalf("no %s in the log", modes.CodeResumed)
		}
	default:
		// Nothing recorded: either the request never became durable (still the old mode) or the
		// persist write already landed (the new one). Only the steady-state policy is applied.
		want := policyEffects(snap.Mode)
		if !equalStrings(got, want) {
			t.Fatalf("no transition recorded (mode %s): ran %s, want policy %s", snap.Mode, joined(got), joined(want))
		}
		if final.Mode != snap.Mode {
			t.Fatalf("policy application changed the mode to %s", final.Mode)
		}
	}
	if final.ModeTransition != nil || final.PendingMode != nil {
		t.Fatalf("leftovers: %v / %v", final.ModeTransition, final.PendingMode)
	}
	if r2.eng.Current() != final.Mode {
		t.Fatal("engine and state disagree")
	}
}

// A kill that drops the unsynced tail of a write must leave a loadable file at every instant: the
// file the next agent reads is the old or the new record, never defaults.
func TestKillMidWriteNeverLosesTheRecordedTransition(t *testing.T) {
	for n := 1; n <= 40; n++ {
		fs := statefake.New()
		r1 := newRig(t, withMode(modes.Online), withFS(fs))
		fs.CrashAfter(n)
		_, _ = r1.request(modes.Survival, false)
		time.Sleep(5 * time.Millisecond)
		r1.abandon = true
		_ = r1.st.Close()
		fs.Reboot()
		r2 := newRig(t, withFS(fs), noSeed())
		snap := r2.st.Snapshot()
		if len(r2.st.Findings()) != 0 {
			t.Fatalf("crash after %d ops: findings %+v", n, r2.st.Findings())
		}
		if snap.Mode != modes.Online && snap.Mode != modes.Survival {
			t.Fatalf("crash after %d ops: mode %q", n, snap.Mode)
		}
		if mt := snap.ModeTransition; mt != nil && (mt.Target != modes.Survival || mt.OpID == "") {
			t.Fatalf("crash after %d ops: corrupt transition %+v", n, mt)
		}
	}
}

// ---- the 3-attempt rule ---------------------------------------------------------------------------

func seedTransition(target, from modes.Mode, step string, attempts map[string]int, ws ...state.TransitionWarning) func(*state.State) {
	return func(s *state.State) {
		s.Mode = from
		s.ModeTransition = &state.ModeTransition{
			OpID: "op_resume000001", From: from, Target: target, Step: step, StartedAt: t0.Add(-time.Minute),
			Attempts: attempts, Warnings: append([]state.TransitionWarning{}, ws...),
			Actor: state.Actor{Kind: "anonymous", Via: "portal"},
		}
	}
}

func fromOf(target modes.Mode) modes.Mode {
	if target == modes.Online {
		return modes.Offline
	}
	return modes.Online
}

func TestResumeSkipsAStepStartedThreeTimes(t *testing.T) {
	for _, target := range allModes {
		for _, step := range modes.Steps(target) {
			if step == modes.StepPersist || step == modes.StepEmit || step == modes.StepSyncCatchup {
				continue
			}
			t.Run(fmt.Sprintf("%s_%s", target, step), func(t *testing.T) {
				r := newRig(t, withMode(fromOf(target)), withSeed(seedTransition(target, fromOf(target), step, map[string]int{step: 3})))
				res := r.resume()
				if res.OpID != "op_resume000001" || res.Target != target {
					t.Fatalf("resume result %+v", res)
				}
				r.idle()
				got := without(r.env.Effects(), "portal.drop")
				i := indexOf(modes.Steps(target), step)
				if want := effectsFrom(target, i+1); !equalStrings(got, want) {
					t.Fatalf("skipping %s ran\n got  %s\n want %s", step, joined(got), joined(want))
				}
				mc := r.env.Events.OfType(events.TypeModeChanged)
				if len(mc) != 1 {
					t.Fatalf("mode.changed events: %d", len(mc))
				}
				ev := mc[0].Data.(events.ModeChanged)
				if !hasCode(ev.Warnings, modes.CodeStepFailed) || ev.OpID != "op_resume000001" || ev.From != string(fromOf(target)) {
					t.Fatalf("mode.changed = %+v", ev)
				}
				found := false
				for _, w := range ev.Warnings {
					if strings.Contains(w.Message, step) && strings.Contains(w.Message, "3 tries") {
						found = true
					}
				}
				if !found {
					t.Fatalf("no skip warning naming %s: %+v", step, ev.Warnings)
				}
				if r.eng.Current() != target {
					t.Fatalf("mode %s", r.eng.Current())
				}
			})
		}
	}
}

func TestResumeRunsAStepStartedTwice(t *testing.T) {
	r := newRig(t, withMode(modes.Online), withSeed(seedTransition(modes.Survival, modes.Online, modes.StepSurvivalModels, map[string]int{modes.StepLinkStop: 1, modes.StepSurvivalModels: 2})))
	r.resume()
	r.idle()
	if got, want := r.env.Effects(), effectsFrom(modes.Survival, indexOf(modes.Steps(modes.Survival), modes.StepSurvivalModels)); !equalStrings(without(got, "portal.drop"), want) {
		t.Fatalf("effects %v", got)
	}
	ev := r.env.Events.OfType(events.TypeModeChanged)[0].Data.(events.ModeChanged)
	if hasCode(ev.Warnings, modes.CodeStepFailed) {
		t.Fatalf("a step with two attempts was skipped: %+v", ev.Warnings)
	}
}

func TestResumeNeverSkipsPersist(t *testing.T) {
	r := newRig(t, withMode(modes.Online), withSeed(seedTransition(modes.Offline, modes.Online, modes.StepPersist, map[string]int{modes.StepPersist: 9})))
	r.resume()
	r.idle()
	if r.eng.Current() != modes.Offline || r.st.Snapshot().ModeTransition != nil {
		t.Fatal("a transition whose persist crashed repeatedly must still commit")
	}
}

func TestResumeKeepsEarlierWarningsAndAttempts(t *testing.T) {
	earlier := state.TransitionWarning{Code: modes.CodeNoModel, Message: "No model could be started."}
	r := newRig(t, withMode(modes.Online), withSeed(seedTransition(modes.Survival, modes.Online, modes.StepSurvivalService, map[string]int{modes.StepTailscaleOff: 1}, earlier)))
	b := newBlocker()
	r.env.Services.Hook = func(ctx context.Context, _ string) error { return b.hook(ctx) }
	r.resume()
	b.waitEntered(t)
	mt := r.st.Snapshot().ModeTransition
	if mt == nil || mt.Attempts[modes.StepSurvivalService] != 1 || mt.Attempts[modes.StepTailscaleOff] != 1 || len(mt.Warnings) != 1 || mt.Warnings[0].Code != modes.CodeNoModel {
		t.Fatalf("recorded = %+v", mt)
	}
	b.open()
	r.idle()
	ev := r.env.Events.OfType(events.TypeModeChanged)[0].Data.(events.ModeChanged)
	if !hasCode(ev.Warnings, modes.CodeNoModel) {
		t.Fatalf("earlier warning lost: %+v", ev.Warnings)
	}
}

func TestResumeIgnoresAnUnknownRecordedStep(t *testing.T) {
	r := newRig(t, withMode(modes.Online), withSeed(seedTransition(modes.Offline, modes.Online, "from_a_newer_agent", nil)))
	r.resume()
	r.idle()
	if got, want := r.env.Effects(), effectsFrom(modes.Offline, 0); !equalStrings(got, want) {
		t.Fatalf("effects %v", got)
	}
}

func TestResumeDropsATransitionWithAnInvalidTarget(t *testing.T) {
	r := newRig(t, withMode(modes.Offline), withSeed(func(s *state.State) {
		s.ModeTransition = &state.ModeTransition{OpID: "op_x", Target: "sideways", Step: "x"}
	}))
	r.resume()
	r.idle()
	if r.eng.Current() != modes.Offline || !equalStrings(r.env.Effects(), policyEffects(modes.Offline)) {
		t.Fatalf("mode %s effects %v", r.eng.Current(), r.env.Effects())
	}
}

// ---- crash-skip of the model that was loading ----------------------------------------------------

func TestResumeSkipsTheModelThatWasLoadingWhenTheAgentDied(t *testing.T) {
	r := newRig(t, withMode(modes.Online), withSeed(func(s *state.State) {
		seedTransition(modes.Survival, modes.Online, modes.StepSurvivalModels, map[string]int{modes.StepSurvivalModels: 1})(s)
		s.Models.LastLoad = &state.LastLoad{ID: "model-x", OpID: "op_load", Result: state.LoadInProgress, At: t0}
	}))
	b := newBlocker()
	r.env.Models.LoadB.SetHook(b.hook)
	r.resume()
	b.waitEntered(t)
	if sk := r.env.Models.Skips(); len(sk) != 1 || !equalStrings(sk[0], []string{"model-x"}) {
		t.Fatalf("skips = %v", sk)
	}
	raw := r.st.Snapshot().ModeTransition.Extra["skip_models"]
	if string(raw) != `["model-x"]` {
		t.Fatalf("skip list not persisted: %s", raw)
	}
	b.open()
	r.idle()
	ev := r.env.Events.OfType(events.TypeModeChanged)[0].Data.(events.ModeChanged)
	if !hasCode(ev.Warnings, modes.CodeModelFallback) {
		t.Fatalf("no %s warning: %+v", modes.CodeModelFallback, ev.Warnings)
	}
	if r.eng.Current() != modes.Survival {
		t.Fatal("transition did not complete")
	}
}

func TestSkippedModelsAccumulateAcrossRestarts(t *testing.T) {
	r := newRig(t, withMode(modes.Online), withSeed(func(s *state.State) {
		seedTransition(modes.Survival, modes.Online, modes.StepSurvivalModels, map[string]int{modes.StepSurvivalModels: 2})(s)
		s.ModeTransition.Extra = map[string]json.RawMessage{"skip_models": json.RawMessage(`["model-a"]`)}
		s.Models.LastLoad = &state.LastLoad{ID: "model-b", Result: state.LoadInProgress, At: t0}
	}))
	r.resume()
	r.idle()
	if sk := r.env.Models.Skips(); len(sk) != 1 || !equalStrings(sk[0], []string{"model-a", "model-b"}) {
		t.Fatalf("skips = %v", sk)
	}
}

func TestFinishedLoadsAreNotSkipped(t *testing.T) {
	for _, result := range []string{state.LoadOK, state.LoadFailed, state.LoadTimeout, state.LoadInterrupted} {
		r := newRig(t, withMode(modes.Online), withSeed(func(s *state.State) {
			seedTransition(modes.Survival, modes.Online, modes.StepSurvivalModels, map[string]int{modes.StepSurvivalModels: 1})(s)
			s.Models.LastLoad = &state.LastLoad{ID: "model-x", Result: result, At: t0}
		}))
		r.resume()
		r.idle()
		if sk := r.env.Models.Skips(); len(sk) != 1 || len(sk[0]) != 0 {
			t.Fatalf("result %q: skips = %v", result, sk)
		}
	}
}

// ---- steady state at startup ---------------------------------------------------------------------

func TestResumeWithoutTransitionAppliesTheModePolicy(t *testing.T) {
	for _, m := range allModes {
		t.Run(string(m), func(t *testing.T) {
			r := newRig(t, withMode(m))
			r.resume()
			r.idle()
			if got := r.env.Effects(); !equalStrings(got, policyEffects(m)) {
				t.Fatalf("effects %v, want %v", got, policyEffects(m))
			}
			snap := r.st.Snapshot()
			if snap.Mode != m || snap.ModeTransition != nil {
				t.Fatalf("policy application touched the mode record: %s / %v", snap.Mode, snap.ModeTransition)
			}
			if n := len(r.env.Events.All()); n != 0 {
				t.Fatalf("policy application published %d events", n)
			}
			if n := len(r.env.Audit.Events()); n != 0 {
				t.Fatalf("policy application audited %d records", n)
			}
		})
	}
}

func TestSteadyStateRunIsATransitionForConcurrency(t *testing.T) {
	r := newRig(t, withMode(modes.Online))
	b := newBlocker()
	r.env.Tailscale.OnB.SetHook(b.hook)
	r.resume()
	b.waitEntered(t)
	if _, err := r.request(modes.Offline, false); err == nil {
		t.Fatal("a request during the startup policy run must be refused")
	}
	// survival preempts it
	res := r.mustRequest(modes.Survival, false)
	b.open()
	r.idle()
	if r.eng.Current() != modes.Survival || res.From != modes.Online {
		t.Fatalf("mode %s, result %+v", r.eng.Current(), res)
	}
	if r.env.Rec.Count("link.start") != 0 {
		t.Fatal("the online policy went on after survival preempted it")
	}
}

func TestResumeTwiceIsHarmless(t *testing.T) {
	r := newRig(t, withMode(modes.Online), withSeed(seedTransition(modes.Offline, modes.Online, modes.StepLinkStop, nil)))
	b := newBlocker()
	r.env.Link.StopB.SetHook(b.hook)
	first := r.resume()
	b.waitEntered(t)
	second := r.resume()
	if second.OpID != first.OpID {
		t.Fatalf("second resume started %s", second.OpID)
	}
	b.open()
	r.idle()
	if r.env.Rec.Count("link.stop") != 1 {
		t.Fatal("the resumed step ran twice")
	}
}
