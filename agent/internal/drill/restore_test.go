package drill

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"reflect"
	"slices"
	"sync"
	"testing"
)

// simModels is a stateful fake of the model layer: a router with models_max slots, an eviction rule
// (least recently loaded first) and an operation log.
type simModels struct {
	mu       sync.Mutex
	resident []string // in load order, oldest first
	def      string
	max      int
	failLoad map[string]error
	failDef  error
	log      []string
	// readErr makes Resident fail.
	readErr error
}

func newSim(max int, def string, loaded ...string) *simModels {
	return &simModels{resident: slices.Clone(loaded), def: def, max: max, failLoad: map[string]error{}}
}

func (s *simModels) Resident(context.Context) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.readErr != nil {
		return nil, s.readErr
	}
	return slices.Clone(s.resident), nil
}

func (s *simModels) Default() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.def
}

func (s *simModels) Load(_ context.Context, id string, evict bool) (LoadInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.log = append(s.log, fmt.Sprintf("load %s evict=%v", id, evict))
	if err := s.failLoad[id]; err != nil {
		return LoadInfo{}, &ModelError{Code: "LAI-LLM-012", Message: "did not start", Err: err}
	}
	if slices.Contains(s.resident, id) {
		return LoadInfo{AlreadyLoaded: true}, nil
	}
	for len(s.resident) >= s.max {
		if !evict {
			return LoadInfo{}, &ModelError{Code: "LAI-LLM-011", Message: "no room"}
		}
		s.resident = s.resident[1:]
	}
	s.resident = append(s.resident, id)
	return LoadInfo{LoadMS: 1200}, nil
}

func (s *simModels) Unload(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.log = append(s.log, "unload "+id)
	i := slices.Index(s.resident, id)
	if i < 0 {
		return errors.New("not loaded")
	}
	s.resident = slices.Delete(s.resident, i, i+1)
	return nil
}

func (s *simModels) SetDefault(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.log = append(s.log, "default "+id)
	if s.failDef != nil {
		return s.failDef
	}
	s.def = id
	return nil
}

func (s *simModels) loaded() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.resident)
}

func (s *simModels) ops() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.log)
}

type simWhisper struct {
	mu      sync.Mutex
	running bool
	starts  int
	err     error
}

func (w *simWhisper) Running(context.Context) (bool, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.running, nil
}

func (w *simWhisper) Start(context.Context) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.starts++
	if w.err != nil {
		return w.err
	}
	w.running = true
	return nil
}

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func newRestorer(t *testing.T, m ModelOps, w WhisperUnit) *StateRestorer {
	t.Helper()
	r, err := NewStateRestorer(m, w, quiet())
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestNewStateRestorerNeedsModels(t *testing.T) {
	if _, err := NewStateRestorer(nil, nil, nil); err == nil {
		t.Fatal("a restorer without the model layer must be refused")
	}
}

func TestCaptureRecordsModelsDefaultAndWhisper(t *testing.T) {
	sim := newSim(3, "b", "a", "b", "c")
	w := &simWhisper{running: true}
	c, err := newRestorer(t, sim, w).Capture(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := Captured{LoadedModels: []string{"a", "b", "c"}, DefaultModel: "b", WhisperRunning: true}
	if !reflect.DeepEqual(c, want) {
		t.Fatalf("captured = %+v, want %+v", c, want)
	}
	if len(sim.ops()) != 0 {
		t.Fatalf("Capture must change nothing: %v", sim.ops())
	}

	empty, err := newRestorer(t, newSim(1, ""), nil).Capture(context.Background())
	if err != nil || empty.LoadedModels == nil || len(empty.LoadedModels) != 0 || empty.WhisperRunning {
		t.Fatalf("an empty node captures an empty, non-nil list: %+v, %v", empty, err)
	}
}

func TestCaptureFailsWhenTheModelsCannotBeRead(t *testing.T) {
	sim := newSim(1, "")
	sim.readErr = errors.New("router down")
	if _, err := newRestorer(t, sim, nil).Capture(context.Background()); err == nil {
		t.Fatal("Capture must fail when the router does not answer")
	}
}

// What the drill did to the node between the capture and the restore, for the snapshots of 0, 1 and 3
// loaded models: the drill loads other models (evicting as the router must), and Restore puts the
// node back.
func TestRestoreFromSnapshots(t *testing.T) {
	tests := []struct {
		name      string
		max       int
		before    []string
		def       string
		drillLoad []string // loaded by the drill, in order, with eviction
		wantOps   []string // a subset, in order, of the operations Restore ran
	}{
		{name: "0 loaded", max: 1, before: nil, drillLoad: []string{"x"}, wantOps: []string{"unload x"}},
		{name: "1 loaded, evicted by the drill", max: 1, before: []string{"a"}, def: "a", drillLoad: []string{"x", "y"},
			wantOps: []string{"unload y", "load a evict=false"}},
		{name: "3 loaded, all evicted", max: 3, before: []string{"a", "b", "c"}, def: "b", drillLoad: []string{"x", "y", "z"},
			wantOps: []string{"unload x", "unload y", "unload z", "load a evict=false", "load b evict=false", "load c evict=false"}},
		{name: "3 loaded, one evicted", max: 3, before: []string{"a", "b", "c"}, def: "a", drillLoad: []string{"x"},
			wantOps: []string{"unload x", "load a evict=false"}},
		{name: "the drill changed nothing", max: 3, before: []string{"a", "b"}, def: "a", drillLoad: nil, wantOps: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			sim := newSim(tt.max, tt.def, tt.before...)
			w := &simWhisper{running: true}
			rs := newRestorer(t, sim, w)
			c, err := rs.Capture(ctx)
			if err != nil {
				t.Fatal(err)
			}
			for _, id := range tt.drillLoad {
				if _, err := sim.Load(ctx, id, true); err != nil {
					t.Fatal(err)
				}
			}
			before := len(sim.ops())

			got, err := rs.Restore(ctx, c)
			if err != nil || !got.OK {
				t.Fatalf("Restore = %+v, %v", got, err)
			}
			if !sameSet(sim.loaded(), tt.before) {
				t.Fatalf("loaded after restore = %v, want %v", sim.loaded(), tt.before)
			}
			if !sameSet(got.LoadedModels, tt.before) || got.DefaultModel != tt.def {
				t.Fatalf("restored block = %+v", got)
			}
			ran := sim.ops()[before:]
			var i int
			for _, op := range ran {
				if i < len(tt.wantOps) && op == tt.wantOps[i] {
					i++
				}
			}
			if i != len(tt.wantOps) {
				t.Fatalf("restore ran %v, want %v in order", ran, tt.wantOps)
			}
			// Idempotent: a second restore (the agent crashed after the first) changes nothing.
			again := len(sim.ops())
			if r2, err := rs.Restore(ctx, c); err != nil || !r2.OK || len(sim.ops()) != again {
				t.Fatalf("a second restore must be a no-op: %+v, %v, ops %v", r2, err, sim.ops()[again:])
			}
		})
	}
}

func TestRestoreReloadsInTheOriginalOrder(t *testing.T) {
	ctx := context.Background()
	sim := newSim(3, "", "c", "a", "b")
	rs := newRestorer(t, sim, nil)
	c, _ := rs.Capture(ctx)
	for _, id := range []string{"x", "y", "z"} {
		if _, err := sim.Load(ctx, id, true); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := rs.Restore(ctx, c); err != nil {
		t.Fatal(err)
	}
	var loads []string
	for _, op := range sim.ops() {
		if op == "load c evict=false" || op == "load a evict=false" || op == "load b evict=false" {
			loads = append(loads, op)
		}
	}
	if want := []string{"load c evict=false", "load a evict=false", "load b evict=false"}; !reflect.DeepEqual(loads, want) {
		t.Fatalf("reload order = %v, want %v", loads, want)
	}
}

func TestRestoreResetsTheDefaultAndRestartsWhisper(t *testing.T) {
	ctx := context.Background()
	sim := newSim(1, "a", "a")
	w := &simWhisper{running: true}
	rs := newRestorer(t, sim, w)
	c, _ := rs.Capture(ctx)

	sim.def = "x"
	w.running = false
	got, err := rs.Restore(ctx, c)
	if err != nil || !got.OK || got.DefaultModel != "a" || sim.Default() != "a" {
		t.Fatalf("Restore = %+v, %v (default %q)", got, err, sim.Default())
	}
	if w.starts != 1 || !w.running {
		t.Fatalf("whisper starts = %d running %v", w.starts, w.running)
	}
}

func TestRestoreLeavesWhisperAloneWhenItWasStopped(t *testing.T) {
	ctx := context.Background()
	sim := newSim(1, "", "a")
	w := &simWhisper{running: false}
	rs := newRestorer(t, sim, w)
	c, _ := rs.Capture(ctx)
	if _, err := rs.Restore(ctx, c); err != nil || w.starts != 0 {
		t.Fatalf("starts = %d, err %v", w.starts, err)
	}
}

func TestRestoreFailuresAreReportedAndTheOtherStepsStillRun(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name   string
		break_ func(*simModels, *simWhisper)
	}{
		{"a model does not load", func(s *simModels, _ *simWhisper) { s.failLoad["a"] = errors.New("out of memory") }},
		{"the default cannot be set", func(s *simModels, _ *simWhisper) { s.failDef = errors.New("ini write failed") }},
		{"whisper does not start", func(_ *simModels, w *simWhisper) { w.err = errors.New("unit failed") }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sim := newSim(1, "a", "a")
			w := &simWhisper{running: true}
			rs := newRestorer(t, sim, w)
			c, _ := rs.Capture(ctx)
			// The drill's damage: a evicted, the default moved, whisper stopped.
			if _, err := sim.Load(ctx, "x", true); err != nil {
				t.Fatal(err)
			}
			sim.def = "x"
			w.running = false
			tt.break_(sim, w)

			got, err := rs.Restore(ctx, c)
			if err == nil || got.OK {
				t.Fatalf("a failed restore must say so: %+v, %v", got, err)
			}
			// Every step was still tried.
			if w.starts == 0 {
				t.Error("whisper was not tried after an earlier step failed")
			}
			if got.LoadedModels == nil {
				t.Error("the restored block always carries a list")
			}
		})
	}
}

func TestRestoreWhenTheRouterCannotBeRead(t *testing.T) {
	sim := newSim(1, "a", "a")
	rs := newRestorer(t, sim, nil)
	c, _ := rs.Capture(context.Background())
	sim.readErr = errors.New("router down")
	got, err := rs.Restore(context.Background(), c)
	if err == nil || got.OK || got.LoadedModels == nil {
		t.Fatalf("Restore = %+v, %v", got, err)
	}
}

// ---- the engine around the restorer ----------------------------------------------------------

func withSim(sim *simModels, w WhisperUnit) rigOpt {
	return func(r *rig, d *Deps) {
		rs, err := NewStateRestorer(sim, w, quiet())
		if err != nil {
			r.t.Fatal(err)
		}
		d.Restorer = rs
	}
}

// loadingCheck is a check that loads a model like a model check does (with eviction).
func loadingCheck(id string, order int, sim *simModels, model string) Check {
	return Check{ID: id, Area: "llm", Order: order, Run: func(ctx context.Context, _ *Env) Outcome {
		if _, err := sim.Load(ctx, model, true); err != nil {
			return Outcome{Result: ResultFail, Code: CodeRestoreFailed}
		}
		return Outcome{Result: ResultPass}
	}}
}

func TestEngineRestoresAfterACompleteRun(t *testing.T) {
	sim := newSim(1, "a", "a")
	w := &simWhisper{running: true}
	r := newRig(t, withSim(sim, w))
	r.add(loadingCheck("drl.llm.answer.x", 5, sim, "x"))
	r.add(loadingCheck("drl.llm.answer.y", 5, sim, "y"))

	_, d := r.startWait(StartRequest{})
	if d.Restored == nil || !d.Restored.OK || !reflect.DeepEqual(d.Restored.LoadedModels, []string{"a"}) || d.Restored.DefaultModel != "a" {
		t.Fatalf("restored = %+v", d.Restored)
	}
	if got := sim.loaded(); !reflect.DeepEqual(got, []string{"a"}) {
		t.Fatalf("the node is left with %v, want [a]", got)
	}
	if string(r.state.block(t)["in_progress"]) != "null" {
		t.Fatalf("in_progress = %s", r.state.block(t)["in_progress"])
	}
}

// The abort is injected at every check: whichever check is running when the run is aborted, the node
// ends as it was.
func TestEngineRestoresWhenAbortedAtEveryCheck(t *testing.T) {
	const n = 4
	for at := 1; at <= n; at++ {
		t.Run(fmt.Sprintf("abort at check %d", at), func(t *testing.T) {
			sim := newSim(2, "a", "a", "b")
			w := &simWhisper{running: true}
			r := newRig(t, withSim(sim, w))
			for i := 1; i <= n; i++ {
				i := i
				model := fmt.Sprintf("m%d", i)
				r.add(Check{ID: fmt.Sprintf("drl.llm.answer.%s", model), Area: "llm", Order: i, Run: func(ctx context.Context, env *Env) Outcome {
					if _, err := sim.Load(ctx, model, true); err != nil {
						return Outcome{Result: ResultFail}
					}
					if i == at {
						if _, err := r.eng.Abort(env.RunID); err != nil {
							t.Errorf("Abort: %v", err)
						}
						<-ctx.Done()
					}
					return Outcome{Result: ResultPass}
				}})
			}
			res, d := r.startWait(StartRequest{})
			if d.State != StateAborted {
				t.Fatalf("state = %q, want aborted", d.State)
			}
			if d.Restored == nil || !d.Restored.OK {
				t.Fatalf("restored = %+v", d.Restored)
			}
			if !sameSet(sim.loaded(), []string{"a", "b"}) || sim.Default() != "a" {
				t.Fatalf("after the abort at check %d the node has %v default %q (run %s)", at, sim.loaded(), sim.Default(), res.RunID)
			}
			if string(r.state.block(t)["in_progress"]) != "null" {
				t.Fatal("the marker must be cleared")
			}
		})
	}
}

func TestEngineRestoresAfterAPanicInACheck(t *testing.T) {
	sim := newSim(1, "a", "a")
	r := newRig(t, withSim(sim, nil))
	r.add(Check{ID: "drl.llm.answer.x", Area: "llm", Order: 5, FailCode: "LAI-DRL-020", Run: func(ctx context.Context, _ *Env) Outcome {
		_, _ = sim.Load(ctx, "x", true)
		panic("boom")
	}})
	r.add(passing("drl.after", 6))
	_, d := r.startWait(StartRequest{})
	if d.Checks[0].Result != ResultFail || d.Checks[1].Result != ResultPass {
		t.Fatalf("checks = %+v", d.Checks)
	}
	if d.Restored == nil || !d.Restored.OK || !reflect.DeepEqual(sim.loaded(), []string{"a"}) {
		t.Fatalf("restored = %+v, node has %v", d.Restored, sim.loaded())
	}
}

func TestEngineMarksTheRunFailedWhenTheRestoreFails(t *testing.T) {
	sim := newSim(1, "a", "a")
	r := newRig(t, withSim(sim, nil))
	r.add(Check{ID: "drl.llm.answer.x", Area: "llm", Order: 5, Run: func(ctx context.Context, _ *Env) Outcome {
		_, _ = sim.Load(ctx, "x", true)
		sim.mu.Lock()
		sim.failLoad["a"] = errors.New("out of memory")
		sim.mu.Unlock()
		return Outcome{Result: ResultPass}
	}})
	_, d := r.startWait(StartRequest{})
	if d.Restored == nil || d.Restored.OK {
		t.Fatalf("restored = %+v", d.Restored)
	}
	if d.Overall != OverallFail || d.Code != CodeRestoreFailed {
		t.Fatalf("overall %q code %q", d.Overall, d.Code)
	}
}

func TestEngineDoesNotChangeTheNodeWhenTheCaptureFails(t *testing.T) {
	sim := newSim(1, "a", "a")
	sim.readErr = errors.New("router down")
	r := newRig(t, withSim(sim, nil))
	r.add(passing("drl.x", 1))
	_, d := r.startWait(StartRequest{})
	if d.Restored == nil || d.Restored.OK {
		t.Fatalf("restored = %+v", d.Restored)
	}
	if len(sim.ops()) != 0 {
		t.Fatalf("nothing may be changed when nothing was captured: %v", sim.ops())
	}
}

// The agent crashes after the drill loaded other models: the marker is in state.json, the node is in
// the drill's state, and Recover at the next start puts it back and marks the run aborted.
func TestRecoverWithTheMarkerPresentRestoresTheNode(t *testing.T) {
	sim := newSim(1, "a", "x") // the drill's x is loaded; a was loaded before the drill
	w := &simWhisper{running: false}
	r := newRig(t, withSim(sim, w))
	r.state.setBlock(`{"in_progress":{"run_id":"drl-20261101T043512Z-0001","started_at":"2026-11-01T04:35:12Z",` +
		`"captured":{"loaded_models":["a"],"default_model":"a","whisper_running":true}}}`)

	if err := r.eng.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := sim.loaded(); !reflect.DeepEqual(got, []string{"a"}) {
		t.Fatalf("after Recover the node has %v, want [a]", got)
	}
	if !w.running {
		t.Fatal("whisper must be started again")
	}
	d := r.doc("drl-20261101T043512Z-0001")
	if d.State != StateAborted || d.Restored == nil || !d.Restored.OK {
		t.Fatalf("document = %+v", d)
	}
	if string(r.state.block(t)["in_progress"]) != "null" {
		t.Fatal("the marker must be cleared")
	}
}

func TestRecoverMarksRestoredFalseWhenTheRestoreFails(t *testing.T) {
	sim := newSim(1, "a", "x")
	sim.failLoad["a"] = errors.New("out of memory")
	r := newRig(t, withSim(sim, nil))
	r.state.setBlock(`{"in_progress":{"run_id":"drl-20261101T043512Z-0002","started_at":"2026-11-01T04:35:12Z",` +
		`"captured":{"loaded_models":["a"],"default_model":"a","whisper_running":false}}}`)
	if err := r.eng.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	d := r.doc("drl-20261101T043512Z-0002")
	if d.Restored == nil || d.Restored.OK || d.Code != CodeRestoreFailed {
		t.Fatalf("document = %+v", d)
	}
	if string(r.state.block(t)["in_progress"]) != "null" {
		t.Fatal("the marker is cleared after one attempt, or every start would retry forever")
	}
}
