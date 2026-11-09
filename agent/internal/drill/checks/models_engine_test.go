package checks_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/catalog"
	"github.com/marinoscar/local-ai/agent/internal/drill"
	"github.com/marinoscar/local-ai/agent/internal/drill/checks"
	"github.com/marinoscar/local-ai/agent/internal/drill/media"
	"github.com/marinoscar/local-ai/agent/internal/router"
	"github.com/marinoscar/local-ai/agent/internal/state"
)

// stateStore is an in-memory drill.StateStore.
type stateStore struct {
	mu sync.Mutex
	st state.State
}

func (s *stateStore) Snapshot() state.State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.st.Clone()
}

func (s *stateStore) MutateSync(fn func(*state.State) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return fn(&s.st)
}

// simRouter is a router with models_max slots as a drill.ModelOps, plus the chat endpoint: it answers
// READY to the text prompt and 7 to the photo.
type simRouter struct {
	mu       sync.Mutex
	resident []string
	max      int
	def      string
	failLoad map[string]bool
	wrong    bool
	asked    []string
}

func (s *simRouter) Resident(context.Context) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.resident), nil
}
func (s *simRouter) Default() string { return s.def }
func (s *simRouter) Load(_ context.Context, id string, evict bool) (drill.LoadInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failLoad[id] {
		return drill.LoadInfo{}, &drill.ModelError{Code: "LAI-LLM-012", Message: "did not start"}
	}
	if slices.Contains(s.resident, id) {
		return drill.LoadInfo{AlreadyLoaded: true}, nil
	}
	for len(s.resident) >= s.max {
		if !evict {
			return drill.LoadInfo{}, &drill.ModelError{Code: "LAI-LLM-011", Message: "no room"}
		}
		s.resident = s.resident[1:]
	}
	s.resident = append(s.resident, id)
	return drill.LoadInfo{LoadMS: 1000}, nil
}
func (s *simRouter) Unload(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := slices.Index(s.resident, id)
	if i < 0 {
		return errors.New("not loaded")
	}
	s.resident = slices.Delete(s.resident, i, i+1)
	return nil
}
func (s *simRouter) SetDefault(_ context.Context, id string) error { s.def = id; return nil }

func (s *simRouter) Complete(_ context.Context, req router.CompletionRequest) (router.Completion, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !slices.Contains(s.resident, req.Model) {
		return router.Completion{}, errors.New("model is not loaded")
	}
	s.asked = append(s.asked, req.Model)
	text := "READY"
	if len(req.Image) > 0 {
		text = "7"
	}
	if s.wrong {
		text = "I do not know"
	}
	return router.Completion{Text: text, TokensPerSecond: 25, HasTimings: true}, nil
}

type presentAll struct{}

func (presentAll) Present(string, *catalog.Model) bool { return true }

func model(id string, vram int64, roles ...string) catalog.Model {
	return catalog.Model{ID: id, Engine: "llama", Roles: roles,
		Requirements: catalog.Requirements{Backends: []string{"cuda", "cpu"}, MinVRAMMiB: vram, MinRAMMiB: 4000}}
}

func newEngine(t *testing.T, sim *simRouter) (*drill.Engine, *stateStore) {
	t.Helper()
	ms := []catalog.Model{model("text-a", 5000, "chat"), model("vis-c", 5200, "chat", "vision"), model("text-z", 1000, "chat")}
	prof := catalog.Profile{ID: "p", Router: catalog.Router{ModelsMax: 1}, DefaultModel: "text-a",
		Survival: catalog.Survival{Models: []string{"text-a", "vis-c"}, DefaultModel: "text-a"}}
	in := router.Inputs{
		Catalog: catalog.New("v1", ms, nil, []catalog.Profile{prof}), Profile: &prof,
		Roots: router.Roots{Internal: "/srv/localai"},
		Fit:   router.Env{Backend: "cuda", GTotalMiB: 8192, RTotalMiB: 16384, RAvailMiB: 12000, MarginMiB: 512, ModelsMax: 1},
	}
	deps := checks.ModelDeps{Models: sim, Chat: sim, Inputs: func() router.Inputs { return in }, Presence: presentAll{}, Photo: media.SevenPNG()}
	reg := drill.NewRegistry()
	if err := checks.Register(reg, deps); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	rs, err := drill.NewStateRestorer(sim, nil, log)
	if err != nil {
		t.Fatal(err)
	}
	st := &stateStore{st: state.State{SchemaVersion: state.SchemaVersion, Mode: state.ModeOnline}}
	eng, err := drill.New(drill.Deps{
		Registry: reg, Results: drill.NewResults(drill.ResultsOptions{Dir: t.TempDir()}), State: st, Restorer: rs,
		NodeID: "ai-n3", Logger: log,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = eng.Shutdown(ctx)
	})
	return eng, st
}

func runDrill(t *testing.T, eng *drill.Engine, req drill.StartRequest) drill.Document {
	t.Helper()
	res, err := eng.Start(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := eng.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	raw, err := eng.Get(res.RunID)
	if err != nil {
		t.Fatal(err)
	}
	var d drill.Document
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatal(err)
	}
	return d
}

func results(d drill.Document) map[string]drill.CheckResult {
	m := map[string]drill.CheckResult{}
	for _, c := range d.Checks {
		m[c.ID] = c
	}
	return m
}

// The whole model part of a drill on one engine: a node with text-a loaded runs checks 5, 6, 7 and 22
// and is left with text-a loaded.
func TestFullRunPassesAndLeavesTheNodeAsItWas(t *testing.T) {
	sim := &simRouter{max: 1, def: "text-a", resident: []string{"text-a"}, failLoad: map[string]bool{}}
	eng, st := newEngine(t, sim)
	d := runDrill(t, eng, drill.StartRequest{})

	if d.Overall != drill.OverallPass || !d.SurvivalReady {
		t.Fatalf("overall %q ready %v: %+v", d.Overall, d.SurvivalReady, d.Checks)
	}
	got := results(d)
	for _, id := range []string{"drl.llm.answer.text-a", "drl.llm.answer.vis-c", "drl.vlm.photo.vis-c", "drl.llm.fit", "drl.restore"} {
		if got[id].Result != drill.ResultPass {
			t.Errorf("%s = %+v", id, got[id])
		}
	}
	if d.Restored == nil || !d.Restored.OK || !slices.Equal(d.Restored.LoadedModels, []string{"text-a"}) || d.Restored.DefaultModel != "text-a" {
		t.Fatalf("restored = %+v", d.Restored)
	}
	if !slices.Equal(sim.resident, []string{"text-a"}) {
		t.Fatalf("the node is left with %v", sim.resident)
	}
	if p := st.Snapshot().Drill; len(p) == 0 || !json.Valid(p) {
		t.Fatalf("drill block = %s", p)
	}
}

func TestQuickRunTestsOnlyTheDefaultModel(t *testing.T) {
	sim := &simRouter{max: 1, def: "text-a", resident: []string{"vis-c"}, failLoad: map[string]bool{}}
	eng, _ := newEngine(t, sim)
	d := runDrill(t, eng, drill.StartRequest{Quick: true})
	got := results(d)
	if got["drl.llm.answer.text-a"].Result != drill.ResultPass {
		t.Fatalf("the default is tested: %+v", got["drl.llm.answer.text-a"])
	}
	for _, id := range []string{"drl.llm.answer.vis-c", "drl.vlm.photo.vis-c"} {
		if c := got[id]; c.Result != drill.ResultSkip || c.Details["reason"] != "quick" {
			t.Errorf("%s = %+v, want skipped by --quick", id, c)
		}
	}
	if !d.SurvivalReady {
		t.Fatal("a critical check skipped by --quick does not count against survival_ready")
	}
	if !slices.Equal(sim.asked, []string{"text-a"}) {
		t.Fatalf("models asked in --quick: %v", sim.asked)
	}
	if !slices.Equal(sim.resident, []string{"vis-c"}) {
		t.Fatalf("restore left %v, want [vis-c]", sim.resident)
	}
}

func TestAModelThatDoesNotLoadFailsItsCheckAndTheRestoreStillRuns(t *testing.T) {
	sim := &simRouter{max: 1, def: "vis-c", resident: []string{"vis-c"}, failLoad: map[string]bool{"text-a": true}}
	eng, _ := newEngine(t, sim)
	d := runDrill(t, eng, drill.StartRequest{})
	got := results(d)
	if c := got["drl.llm.answer.text-a"]; c.Result != drill.ResultFail || c.Code != "LAI-DRL-020" {
		t.Fatalf("text-a = %+v", c)
	}
	if got["drl.llm.answer.vis-c"].Result != drill.ResultPass || got["drl.vlm.photo.vis-c"].Result != drill.ResultPass {
		t.Fatalf("the next model continues: %+v", d.Checks)
	}
	if d.Overall != drill.OverallFail || d.SurvivalReady {
		t.Fatalf("overall %q ready %v", d.Overall, d.SurvivalReady)
	}
	if d.Restored == nil || !d.Restored.OK || got["drl.restore"].Result != drill.ResultPass {
		t.Fatalf("restored = %+v", d.Restored)
	}
}

func TestWrongAnswersFailWith022(t *testing.T) {
	sim := &simRouter{max: 1, def: "text-a", resident: []string{"text-a"}, failLoad: map[string]bool{}, wrong: true}
	eng, _ := newEngine(t, sim)
	d := runDrill(t, eng, drill.StartRequest{})
	got := results(d)
	if got["drl.llm.answer.text-a"].Code != "LAI-DRL-022" || got["drl.vlm.photo.vis-c"].Code != "LAI-DRL-023" {
		t.Fatalf("checks = %+v", d.Checks)
	}
}

func TestOnlyTheFitCheckChangesNothing(t *testing.T) {
	sim := &simRouter{max: 1, def: "text-a", resident: []string{"text-a"}, failLoad: map[string]bool{}}
	eng, _ := newEngine(t, sim)
	d := runDrill(t, eng, drill.StartRequest{Only: []string{"drl.llm.fit"}})
	if len(d.Checks) != 1 || d.Checks[0].Result != drill.ResultPass || len(sim.asked) != 0 {
		t.Fatalf("checks = %+v asked %v", d.Checks, sim.asked)
	}
}
