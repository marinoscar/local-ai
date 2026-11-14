package router

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/audit"
	"github.com/marinoscar/local-ai/agent/internal/catalog"
	"github.com/marinoscar/local-ai/agent/internal/state"
)

// The engine adapters (E11, #508): one fit check, one LRU eviction order and one rollback across the
// llama router and the music engine, under modelOpsMu. The node is the rig's ai-n3 (8 GB card, 300
// MiB held by others, 512 MiB margin, models_max 1) with two music models: music-s (3000 MiB) fits
// next to a 4000 MiB language model, music-l (6000 MiB) does not.

// fakeEngine is an EngineAdapter in memory.
type fakeEngine struct {
	mu        sync.Mutex
	installed error
	resident  map[string]bool // id -> ready
	vram      int64
	startErr  error
	stopErr   error
	// startKeeps leaves a failed start resident (the process is still there).
	startKeeps bool
	calls      []string
	onStart    func(id string)
	onStop     func()
}

func newFakeEngine() *fakeEngine { return &fakeEngine{resident: map[string]bool{}} }

func (f *fakeEngine) Engine() string { return catalog.EngineAcestep }

func (f *fakeEngine) Installed() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.installed
}

func (f *fakeEngine) Resident(context.Context) ([]EngineModel, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []EngineModel
	for id, ready := range f.resident {
		out = append(out, EngineModel{ID: id, Ready: ready, VRAMMiB: f.vram})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (f *fakeEngine) Start(_ context.Context, req EngineStart) error {
	f.mu.Lock()
	f.calls = append(f.calls, "start "+req.Model.ID)
	err, keeps, hook := f.startErr, f.startKeeps, f.onStart
	f.resident = map[string]bool{}
	if err == nil || keeps {
		f.resident[req.Model.ID] = err == nil
	}
	f.mu.Unlock()
	if hook != nil {
		hook(req.Model.ID)
	}
	return err
}

func (f *fakeEngine) Stop(_ context.Context, id string) error {
	if f.onStop != nil {
		f.onStop()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "stop "+id)
	if f.stopErr != nil {
		// The real adapter records the model unloaded when it stopped but did not free its memory.
		f.resident = map[string]bool{}
		return f.stopErr
	}
	f.resident = map[string]bool{}
	return nil
}

func (f *fakeEngine) set(id string) {
	f.mu.Lock()
	f.resident = map[string]bool{id: true}
	f.mu.Unlock()
}

func (f *fakeEngine) log() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func (f *fakeEngine) holding() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for id := range f.resident {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

func musicModel(id string, vram int64) catalog.Model {
	return catalog.Model{ID: id, Engine: catalog.EngineAcestep, Roles: []string{"music"}, Requirements: req(vram, 2000, "cuda")}
}

func withMusic(e *fakeEngine) func(*orCfg) {
	return func(c *orCfg) {
		c.extra = append(c.extra, musicModel("music-s", 3000), musicModel("music-l", 6000))
		c.engines = append(c.engines, e)
	}
}

// musicRig is an orRig with the fake music engine; the music models are present on the internal root.
func musicRig(t *testing.T, mut ...func(*orCfg)) (*orRig, *fakeEngine) {
	e := newFakeEngine()
	return newOrRig(t, append([]func(*orCfg){withMusic(e)}, mut...)...), e
}

func TestEngineLoadFitsNextToALanguageModel(t *testing.T) {
	r, e := musicRig(t)
	r.load("m-a")
	res, err := r.o.Load(bg, "music-s", true, true, sysActor)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "loaded" || len(res.Unloaded) != 0 {
		t.Errorf("result: %+v", res)
	}
	eqStrings(t, "engine calls", e.log(), []string{"start music-s"})
	eqStrings(t, "router calls", r.calls(), nil)
	eqStrings(t, "llama loaded", r.loadedNow(), []string{"m-a"})
	eqStrings(t, "events", r.evs(), []string{"loading music-s unloading=[]", "loaded music-s"})
	if ll := r.lastLoad(); ll.ID != "music-s" || ll.Result != state.LoadOK {
		t.Errorf("last_load: %+v", ll)
	}
	if a := r.aud.events(); len(a) != 1 || a[0].Action != audit.ActionModelLoad || a[0].Result != audit.ResultOK {
		t.Errorf("audit: %+v", a)
	}
	if _, held := r.o.Lock().Holder(); held {
		t.Error("the lock is still held")
	}
}

func TestEngineLoadEvictsALanguageModel(t *testing.T) {
	r, e := musicRig(t)
	r.load("m-a")
	res, err := r.o.Load(bg, "music-l", true, true, sysActor)
	if err != nil {
		t.Fatal(err)
	}
	eqStrings(t, "unloaded", res.Unloaded, []string{"m-a"})
	eqStrings(t, "router calls", r.calls(), []string{"unload m-a"})
	eqStrings(t, "engine calls", e.log(), []string{"start music-l"})
	eqStrings(t, "events", r.evs(), []string{"loading music-l unloading=[m-a]", "unloaded m-a evicted", "loaded music-l"})
}

func TestEngineLoadWithoutUnloadOthersIsRefused(t *testing.T) {
	r, e := musicRig(t)
	r.load("m-a")
	_, err := r.o.Load(bg, "music-l", true, false, sysActor)
	if oe := opErrOf(t, err); oe.Code != CodeNoRoom || oe.HTTPStatus() != http.StatusConflict {
		t.Fatalf("err = %v", err)
	}
	eqStrings(t, "engine calls", e.log(), nil)
}

func TestLanguageModelLoadEvictsMusic(t *testing.T) {
	r, e := musicRig(t)
	e.set("music-l")
	res, err := r.o.Load(bg, "m-a", true, true, sysActor)
	if err != nil {
		t.Fatal(err)
	}
	eqStrings(t, "unloaded", res.Unloaded, []string{"music-l"})
	eqStrings(t, "engine calls", e.log(), []string{"stop music-l"})
	eqStrings(t, "router calls", r.calls(), []string{"load m-a"})
	eqStrings(t, "events", r.evs(), []string{"loading m-a unloading=[music-l]", "unloaded music-l evicted", "loaded m-a"})
}

func TestLanguageModelLoadKeepsMusicThatFits(t *testing.T) {
	r, e := musicRig(t)
	e.set("music-s")
	if _, err := r.o.Load(bg, "m-a", true, true, sysActor); err != nil {
		t.Fatal(err)
	}
	eqStrings(t, "engine calls", e.log(), nil)
	eqStrings(t, "music", e.holding(), []string{"music-s"})
}

func TestEngineLoadBusyAnswers409(t *testing.T) {
	r, e := musicRig(t)
	hold, err := r.o.Lock().TryAcquire(HolderOther)
	if err != nil {
		t.Fatal(err)
	}
	defer hold.Release()
	_, err = r.o.Load(bg, "music-s", true, true, sysActor)
	oe := opErrOf(t, err)
	if oe.Code != CodeBusy || oe.HTTPStatus() != http.StatusConflict || oe.Details["op_id"] != hold.OpID {
		t.Fatalf("err = %v %+v", err, oe.Details)
	}
	eqStrings(t, "engine calls", e.log(), nil)
}

func TestEngineStartFailsRollsBack(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		code   Code
		status int
		result string
	}{
		{"start fails", &EngineError{Code: CodeEngineStart, Cause: "unit"}, CodeEngineStart, http.StatusInternalServerError, state.LoadFailed},
		{"never ready", &EngineError{Code: CodeEngineNotReady, Cause: "health"}, CodeEngineNotReady, http.StatusGatewayTimeout, state.LoadTimeout},
		{"killed while starting", &EngineError{Code: CodeEngineStart, Cause: "exited"}, CodeEngineStart, http.StatusInternalServerError, state.LoadFailed},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, e := musicRig(t)
			r.load("m-a")
			e.startErr, e.startKeeps = c.err, true
			_, err := r.o.Load(bg, "music-l", true, true, sysActor)
			oe := opErrOf(t, err)
			if oe.Code != c.code || oe.HTTPStatus() != c.status || oe.Details["rolled_back"] != true {
				t.Fatalf("err = %v %+v", err, oe.Details)
			}
			eqStrings(t, "restored", oe.Details["restored"].([]string), []string{"m-a"})
			eqStrings(t, "engine calls", e.log(), []string{"start music-l", "stop "})
			eqStrings(t, "music", e.holding(), nil)
			eqStrings(t, "router calls", r.calls(), []string{"unload m-a", "load m-a"})
			eqStrings(t, "llama loaded", r.loadedNow(), []string{"m-a"})
			evs := r.evs()
			if last := evs[len(evs)-1]; !strings.HasPrefix(last, "failed music-l "+string(c.code)+" rolled_back=true restored=[m-a]") {
				t.Errorf("events: %q", evs)
			}
			if ll := r.lastLoad(); ll.ID != "music-l" || ll.Result != c.result {
				t.Errorf("last_load: %+v", ll)
			}
			if r.o.Counters().Rollbacks != 1 {
				t.Errorf("counters: %+v", r.o.Counters())
			}
		})
	}
}

// llama-router.md section 4.15.3 step 4: an eviction that ends in LAI-LLM-042 is a warning, not a
// load failure.
func TestMusicThatWillNotFreeItsMemoryIsAWarning(t *testing.T) {
	r, e := musicRig(t)
	e.set("music-l")
	e.stopErr = &EngineError{Code: CodeEngineNotFreed, Cause: "vram"}
	res, err := r.o.Load(bg, "m-a", true, true, sysActor)
	if err != nil {
		t.Fatal(err)
	}
	eqStrings(t, "unloaded", res.Unloaded, []string{"music-l"})
	eqStrings(t, "router calls", r.calls(), []string{"load m-a"})
	if !r.alerts.has("raise", CodeEngineNotFreed, catalog.EngineAcestep) {
		t.Error("LAI-LLM-042 was not raised")
	}
}

func TestMusicThatWillNotStopFailsTheLanguageModelLoad(t *testing.T) {
	r, e := musicRig(t)
	e.set("music-l")
	e.stopErr = errors.New("dbus gone")
	_, err := r.o.Load(bg, "m-a", true, true, sysActor)
	oe := opErrOf(t, err)
	if oe.Code != CodeEngineNotFreed || oe.Details["unloading"] != "music-l" {
		t.Fatalf("err = %v %+v", err, oe.Details)
	}
	eqStrings(t, "router calls", r.calls(), nil)
}

func TestEngineUnload(t *testing.T) {
	r, e := musicRig(t)
	e.set("music-s")
	res, err := r.o.Unload(bg, "music-s", false, sysActor)
	if err != nil || res.AlreadyUnloaded {
		t.Fatalf("unload = %+v, %v", res, err)
	}
	eqStrings(t, "engine calls", e.log(), []string{"stop music-s"})
	eqStrings(t, "events", r.evs(), []string{"unloaded music-s request"})

	res, err = r.o.Unload(bg, "music-s", false, sysActor)
	if err != nil || !res.AlreadyUnloaded {
		t.Fatalf("second unload = %+v, %v", res, err)
	}
}

func TestEngineUnloadThatDoesNotFreeMemoryIs042(t *testing.T) {
	r, e := musicRig(t)
	e.set("music-s")
	e.stopErr = &EngineError{Code: CodeEngineNotFreed, Cause: "vram"}
	res, err := r.o.Unload(bg, "music-s", false, sysActor)
	if err != nil || res.Warning == nil || res.Warning.Code != CodeEngineNotFreed {
		t.Fatalf("unload = %+v, %v", res, err)
	}
	if !r.alerts.has("raise", CodeEngineNotFreed, catalog.EngineAcestep) {
		t.Error("LAI-LLM-042 was not raised")
	}
}

func TestEngineLoadRefusals(t *testing.T) {
	t.Run("not installed", func(t *testing.T) {
		r, e := musicRig(t)
		e.installed = errors.New("no runtime")
		_, err := r.o.Load(bg, "music-s", true, true, sysActor)
		if oe := opErrOf(t, err); oe.Code != CodeEngineMissing || oe.HTTPStatus() != http.StatusUnprocessableEntity {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("survival mode", func(t *testing.T) {
		r, e := musicRig(t, orSurvival())
		_, err := r.o.Load(bg, "music-s", true, true, sysActor)
		if oe := opErrOf(t, err); oe.Code != CodeRefused || oe.HTTPStatus() != http.StatusConflict {
			t.Fatalf("err = %v", err)
		}
		eqStrings(t, "engine calls", e.log(), nil)
	})
	t.Run("does not fit the card", func(t *testing.T) {
		r, _ := musicRig(t, func(c *orCfg) { c.extra = append(c.extra, musicModel("music-xl", 9000)) })
		_, err := r.o.Load(bg, "music-xl", true, true, sysActor)
		if oe := opErrOf(t, err); oe.Code != CodeNotFit {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("files missing", func(t *testing.T) {
		r, _ := musicRig(t, func(c *orCfg) { c.presence.missing = map[string]bool{"music-s": true} })
		_, err := r.o.Load(bg, "music-s", true, true, sysActor)
		if oe := opErrOf(t, err); oe.Code != CodeFilesMissing {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("whisper still has no adapter", func(t *testing.T) {
		r, _ := musicRig(t)
		_, err := r.o.Load(bg, "whisper-x", true, true, sysActor)
		if oe := opErrOf(t, err); oe.Code != CodeNotFit {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestEngineLoadAlreadyLoaded(t *testing.T) {
	r, e := musicRig(t)
	e.set("music-s")
	res, err := r.o.Load(bg, "music-s", true, true, sysActor)
	if err != nil || !res.AlreadyLoaded {
		t.Fatalf("load = %+v, %v", res, err)
	}
	eqStrings(t, "engine calls", e.log(), nil)
}

func TestEngineLoadInTheBackground(t *testing.T) {
	r, e := musicRig(t)
	done := make(chan error, 1)
	res, err := r.o.LoadWith(bg, LoadRequest{ID: "music-s", UnloadOthers: true, Actor: sysActor,
		OnDone: func(_ LoadResult, err error) { done <- err }})
	if err != nil || !res.Accepted || res.OpID == "" {
		t.Fatalf("load = %+v, %v", res, err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	eqStrings(t, "music", e.holding(), []string{"music-s"})
}

func TestSurvivalStopsMusicBeforeTheSurvivalModels(t *testing.T) {
	r, e := musicRig(t, orSurvival())
	e.set("music-l")
	var callsAtStop []string
	e.onStop = func() { callsAtStop = r.calls() }
	if w := r.o.StopEnginesForSurvival(bg); len(w) != 0 {
		t.Fatalf("engines_stop warnings: %v", w)
	}
	warns, err := r.o.LoadSurvivalSet(bg, nil)
	if err != nil || len(warns) != 0 {
		t.Fatalf("survival = %v, %v", warns, err)
	}
	eqStrings(t, "engine calls", e.log(), []string{"stop "})
	eqStrings(t, "router calls when music stopped", callsAtStop, nil)
	eqStrings(t, "router calls", r.calls(), []string{"load m-c"})
	eqStrings(t, "llama loaded", r.loadedNow(), []string{"m-c"})
}

func TestSurvivalGoesOnWhenMusicWillNotStop(t *testing.T) {
	r, e := musicRig(t, orSurvival())
	e.set("music-s")
	e.stopErr = &EngineError{Code: CodeEngineNotFreed, Cause: "vram"}
	warns := r.o.StopEnginesForSurvival(bg)
	if len(warns) != 1 || warns[0].Code != string(CodeEngineNotFreed) {
		t.Fatalf("warnings: %+v", warns)
	}
	if _, err := r.o.LoadSurvivalSet(bg, nil); err != nil {
		t.Fatal(err)
	}
	eqStrings(t, "llama loaded", r.loadedNow(), []string{"m-c"})
	if !strings.Contains(r.logs.String(), string(CodeEngineNotFreed)) {
		t.Error("LAI-LLM-042 is not in the log")
	}
}

func TestPlanCountsTheEngineOfTheCandidateOnly(t *testing.T) {
	env := Env{Backend: "cuda", GTotalMiB: 24000, MarginMiB: 512, ModelsMax: 1}
	loaded := []Loaded{
		{ID: "music-s", Engine: catalog.EngineAcestep, Requirements: req(3000, 0)},
		{ID: "m-a", Requirements: req(4000, 0)},
	}
	// A llama model next to one llama model and models_max 1: m-a goes, the music model stays.
	evict, _, err := env.Plan(Candidate{ID: "m-b", Requirements: req(4000, 0)}, loaded, true)
	if err != nil || strings.Join(evict, ",") != "m-a" {
		t.Fatalf("evict = %v, %v", evict, err)
	}
	// A second music model replaces the first; the llama model stays.
	evict, _, err = env.Plan(Candidate{ID: "music-l", Engine: catalog.EngineAcestep, Requirements: req(6000, 0)}, loaded, true)
	if err != nil || strings.Join(evict, ",") != "music-s" {
		t.Fatalf("evict = %v, %v", evict, err)
	}
}
