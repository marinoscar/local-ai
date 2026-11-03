package router

import (
	"context"
	"errors"
	"net/http"
	"runtime"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/router/fake"
	"github.com/marinoscar/local-ai/agent/internal/state"
)

// desire records a desired_loaded set, most recently used first, with last_used one hour apart.
func (r *orRig) desire(ids ...string) {
	r.t.Helper()
	r.seed(func(s *state.State) {
		if s.Models.LastUsed == nil {
			s.Models.LastUsed = map[string]time.Time{}
		}
		s.Models.DesiredLoaded = append([]string(nil), ids...)
		for i, id := range ids {
			s.Models.LastUsed[id] = fixedTime().Add(-time.Duration(i+1) * time.Hour)
		}
	})
}

// routerLoadsDefault flags m-a as load-on-startup, the way models.ini does for the default.
func (r *orRig) routerLoadsDefault() {
	r.f.Add(fake.Model{ID: "m-a", LoadOnStartup: true})
}

func (r *orRig) restore() RestoreResult {
	r.t.Helper()
	res, err := r.o.RestoreModels(bg)
	if err != nil {
		r.t.Fatalf("RestoreModels: %v", err)
	}
	return res
}

func skipIDs(s []RestoreSkip) []string {
	out := []string{}
	for _, x := range s {
		out = append(out, x.ID+":"+x.Reason)
	}
	return out
}

func TestRestoreDefaultLoadedByTheRouterIsNotLoadedAgain(t *testing.T) {
	r := newOrRig(t)
	r.f.Set("m-a", fake.Loaded)
	res := r.restore()
	if res.Default != "m-a" || res.DefaultState != DefaultStateLoaded {
		t.Errorf("default: %+v", res)
	}
	eqStrings(t, "router calls", r.calls(), nil)
	eqStrings(t, "loaded", res.Loaded, []string{"m-a"})
	eqStrings(t, "started", res.Started, nil)
	if r.alerts.has("raise", CodeDefaultNotStarted, "*") || r.alerts.has("raise", CodeNoDefault, "*") {
		t.Errorf("an alert was raised: %+v", r.alerts.events)
	}
	if !r.alerts.has("clear", CodeDefaultNotStarted, "") || !r.alerts.has("clear", CodeNoDefault, "") {
		t.Errorf("the alerts were not cleared: %+v", r.alerts.events)
	}
}

func TestRestoreWaitsForTheRouterToFinishLoadingTheDefault(t *testing.T) {
	r := newOrRig(t)
	r.routerLoadsDefault()
	r.f.Restart() // the router starts loading m-a on its own
	start := r.clk.Now()
	res := r.restore()
	if res.DefaultState != DefaultStateLoaded {
		t.Errorf("state: %q", res.DefaultState)
	}
	eqStrings(t, "router calls", r.calls(), nil)
	eqStrings(t, "loaded", r.loadedNow(), []string{"m-a"})
	if waited := r.clk.Now().Sub(start); waited < 5*time.Second || waited > 8*time.Second {
		t.Errorf("waited %v for a 5 s load", waited)
	}
}

func TestRestoreLoadsADefaultTheRouterDidNotLoad(t *testing.T) {
	r := newOrRig(t)
	res := r.restore()
	if res.DefaultState != DefaultStateStarted {
		t.Errorf("state: %q", res.DefaultState)
	}
	eqStrings(t, "router calls", r.calls(), []string{"load m-a"})
	eqStrings(t, "started", res.Started, []string{"m-a"})
	eqStrings(t, "loaded", res.Loaded, []string{"m-a"})
	if ll := r.lastLoad(); ll.ID != "m-a" || ll.Result != state.LoadOK {
		t.Errorf("last_load: %+v", ll)
	}
	a := r.aud.events()
	if len(a) != 1 || a[0].Actor.Kind != "system" {
		t.Errorf("audit: %+v", a)
	}
}

func TestRestoreLoadsTheDesiredSetMostRecentFirstWithoutEvicting(t *testing.T) {
	r := newOrRig(t, orMax(4))
	r.f.Set("m-a", fake.Loaded)
	// Stored order is not the order of use: m-c was used last.
	r.desire("m-b", "m-c", "m-d")
	r.seed(func(s *state.State) { s.Models.LastUsed["m-c"] = fixedTime().Add(-time.Minute) })
	res := r.restore()
	eqStrings(t, "router calls", r.calls(), []string{"load m-c", "load m-b", "load m-d"})
	eqStrings(t, "loaded", res.Loaded, []string{"m-a", "m-c", "m-b", "m-d"})
	eqStrings(t, "started", res.Started, []string{"m-c", "m-b", "m-d"})
	if r.countCalls("unload") != 0 {
		t.Errorf("boot evicted a model: %v", r.calls())
	}
	eqStrings(t, "desired_loaded", r.snap().Models.DesiredLoaded, []string{"m-d", "m-b", "m-c", "m-a"})
}

func TestRestoreStopsAtModelsMax(t *testing.T) {
	r := newOrRig(t, orMax(2))
	r.f.Set("m-a", fake.Loaded)
	r.desire("m-b", "m-c")
	res := r.restore()
	eqStrings(t, "router calls", r.calls(), []string{"load m-b"})
	eqStrings(t, "skipped", skipIDs(res.Skipped), []string{"m-c:no_room"})
	if res.Skipped[0].Code != CodeNoRoom {
		t.Errorf("code %s", res.Skipped[0].Code)
	}
	eqStrings(t, "loaded", r.loadedNow(), []string{"m-a", "m-b"})
}

func TestRestoreNeverEvictsForTheDefault(t *testing.T) {
	// The agent restarts while the operator's own choice is loaded and the default does not fit
	// beside it: nothing failed to start, so no alert, and nothing is unloaded.
	r := newOrRig(t)
	r.load("m-d")
	res := r.restore()
	if res.DefaultState != DefaultStateNoRoom {
		t.Errorf("state: %q", res.DefaultState)
	}
	eqStrings(t, "router calls", r.calls(), nil)
	eqStrings(t, "loaded", r.loadedNow(), []string{"m-d"})
	if r.alerts.has("raise", CodeDefaultNotStarted, "*") {
		t.Error("LAI-LLM-013 was raised for a default that only had no room")
	}
	eqStrings(t, "desired_loaded", r.snap().Models.DesiredLoaded, []string{"m-d"})
}

func TestRestoreDefaultThatCannotLoadRaisesTheAlertAndTheRestIsStillRestored(t *testing.T) {
	cases := []struct {
		name  string
		cfg   func(c *orCfg)
		setup func(r *orRig)
		code  Code
		cause string
	}{
		{"files missing", func(c *orCfg) { c.presence.missing = map[string]bool{"m-a": true} }, func(*orRig) {}, CodeFilesMissing, "not_loadable"},
		{"child crashes", nil, func(r *orRig) { r.f.FailLoad("m-a", 137) }, CodeNothingLoaded, "failed"},
		{"router refuses", nil, func(r *orRig) { r.f.RejectLoad("m-a", http.StatusInternalServerError) }, CodeNothingLoaded, "rejected"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newOrRig(t, orMax(2), tc.cfg)
			tc.setup(r)
			r.desire("m-b")
			res := r.restore()
			if res.DefaultState != DefaultStateFailed || len(res.Skipped) != 1 {
				t.Fatalf("result: %+v", res)
			}
			if sk := res.Skipped[0]; sk.ID != "m-a" || sk.Code != tc.code || sk.Reason != tc.cause {
				t.Errorf("skip: %+v", sk)
			}
			if !r.alerts.has("raise", CodeDefaultNotStarted, "") {
				t.Errorf("LAI-LLM-013 was not raised: %+v", r.alerts.events)
			}
			eqStrings(t, "loaded", r.loadedNow(), []string{"m-b"})
			eqStrings(t, "loaded in the result", res.Loaded, []string{"m-b"})
		})
	}
}

func TestRestoreDefaultStuckLoadingPastTheTimeout(t *testing.T) {
	r := newOrRig(t, orMax(2))
	r.f.Set("m-a", fake.Loading) // loading, and never finishes
	r.desire("m-b")
	start := r.clk.Now()
	res := r.restore()
	if res.DefaultState != DefaultStateFailed {
		t.Errorf("state: %q", res.DefaultState)
	}
	if got := r.clk.Now().Sub(start); got < 300*time.Second || got > 310*time.Second {
		t.Errorf("waited %v, want about the 300 s load timeout", got)
	}
	if sk := res.Skipped[0]; sk.ID != "m-a" || sk.Code != CodeLoadTimeout || sk.Reason != kindTimeout {
		t.Errorf("skip: %+v", sk)
	}
	if !r.alerts.has("raise", CodeDefaultNotStarted, "") {
		t.Error("LAI-LLM-013 was not raised")
	}
	// The stuck default is not loaded a second time, and the rest still comes back.
	eqStrings(t, "router calls", r.calls(), []string{"load m-b"})
}

func TestRestoreSurvivalModeWithNoModelLoadedLoadsTheSurvivalSet(t *testing.T) {
	r := newOrRig(t, orMax(2), orSurvival())
	r.desire("m-b") // the desired set is ignored: the survival set replaces it
	res := r.restore()
	if !res.Survival || res.DefaultState != DefaultStateSurvival {
		t.Errorf("result: %+v", res)
	}
	eqStrings(t, "router calls", r.calls(), []string{"load m-c", "load m-d"})
	eqStrings(t, "loaded", r.loadedNow(), []string{"m-c", "m-d"})
	if len(res.Loaded) != 2 || len(res.Warnings) != 0 {
		t.Errorf("warnings: %+v", res.Warnings)
	}
}

func TestRestoreSurvivalModeFallsBackToTheNextSurvivalModel(t *testing.T) {
	r := newOrRig(t, orSurvival())
	r.f.FailLoad("m-c", 1)
	res := r.restore()
	eqStrings(t, "loaded", r.loadedNow(), []string{"m-d"})
	if len(res.Warnings) != 1 || res.Warnings[0].Code != string(CodeSurvivalFallback) {
		t.Errorf("warnings: %+v", res.Warnings)
	}
}

func TestRestoreSurvivalModeWithAModelAlreadyLoadedKeepsIt(t *testing.T) {
	r := newOrRig(t, orSurvival())
	r.f.Set("m-c", fake.Loaded) // the router loaded the survival default itself
	res := r.restore()
	if res.Survival {
		t.Error("the survival set was applied although a model was loaded")
	}
	eqStrings(t, "loaded", r.loadedNow(), []string{"m-c"})
	eqStrings(t, "unload calls", r.calls(), nil)
}

func TestRestoreWithoutADefaultRaisesLLM014AndRestoresTheRest(t *testing.T) {
	r := newOrRig(t, orMax(2), func(c *orCfg) {
		c.profile.DefaultModel = ""
		c.profile.Survival.DefaultModel = ""
	})
	r.desire("m-b")
	res := r.restore()
	if res.Default != "" || res.DefaultState != DefaultStateNone {
		t.Errorf("default: %+v", res)
	}
	if !r.alerts.has("raise", CodeNoDefault, "") {
		t.Errorf("LAI-LLM-014 was not raised: %+v", r.alerts.events)
	}
	eqStrings(t, "loaded", r.loadedNow(), []string{"m-b"})
	if r.alerts.has("raise", CodeDefaultNotStarted, "*") {
		t.Error("LAI-LLM-013 was raised although no default exists")
	}
}

func TestRestoreUsesTheStateDefaultOverTheProfileDefault(t *testing.T) {
	r := newOrRig(t)
	d := "m-b"
	r.seed(func(s *state.State) { s.Models.Default = &d })
	res := r.restore()
	if res.Default != "m-b" {
		t.Errorf("default %q", res.Default)
	}
	eqStrings(t, "router calls", r.calls(), []string{"load m-b"})
}

func TestRestoreReattachesToAModelThatIsAlreadyLoading(t *testing.T) {
	r := newOrRig(t, orMax(2))
	r.f.Set("m-a", fake.Loaded)
	r.f.Set("m-b", fake.Loading) // the router continued a load while the agent restarted
	r.setHook(func(n int) {
		if n == 2 {
			r.f.Set("m-b", fake.Loaded)
		}
	})
	r.desire("m-b")
	res := r.restore()
	eqStrings(t, "router calls", r.calls(), nil)
	eqStrings(t, "loaded", res.Loaded, []string{"m-a", "m-b"})
	eqStrings(t, "started", res.Started, nil)
}

func TestRestoreWaitsForAnOperatorsLoadInsteadOfFailing(t *testing.T) {
	r := newOrRig(t)
	hold, err := r.o.Lock().TryAcquire(HolderLoad)
	if err != nil {
		t.Fatal(err)
	}
	type out struct {
		res RestoreResult
		err error
	}
	done := make(chan out, 1)
	go func() {
		res, err := r.o.RestoreModels(bg)
		done <- out{res, err}
	}()
	// RestoreModels asks /health once and the load's check asks it again before it waits for the lock.
	for deadline := time.Now().Add(10 * time.Second); r.f.Calls("GET", "/health") < 2; runtime.Gosched() {
		if time.Now().After(deadline) {
			t.Fatal("the restore never reached the load")
		}
	}
	hold.Release()
	o := <-done
	if o.err != nil || o.res.DefaultState != DefaultStateStarted {
		t.Fatalf("result %+v err %v", o.res, o.err)
	}
	eqStrings(t, "loaded", r.loadedNow(), []string{"m-a"})
}

func TestRestoreKeepsTheDesiredSetWholeWhileItRuns(t *testing.T) {
	r := newOrRig(t, orMax(3))
	r.desire("m-b", "m-c")
	var seen [][]string
	r.setHook(func(int) {
		if r.f.Status("m-b") == fake.Loading {
			seen = append(seen, r.snap().Models.DesiredLoaded)
		}
	})
	r.restore()
	if len(seen) == 0 {
		t.Fatal("the hook never saw m-b loading")
	}
	for _, d := range seen {
		if !contains1(d, "m-c") {
			t.Errorf("desired_loaded lost m-c while m-b loaded: %v", d)
		}
	}
	eqStrings(t, "desired_loaded at the end", r.snap().Models.DesiredLoaded, []string{"m-c", "m-b", "m-a"})
}

func TestRestoreThatLoadsNothingLeavesTheDesiredSetForTheNextBoot(t *testing.T) {
	r := newOrRig(t, orMax(2))
	for _, id := range []string{"m-a", "m-b", "m-c"} {
		r.f.RejectLoad(id, http.StatusInternalServerError)
	}
	r.desire("m-b", "m-c")
	res := r.restore()
	if len(res.Loaded) != 0 {
		t.Fatalf("loaded %v", res.Loaded)
	}
	eqStrings(t, "desired_loaded", r.snap().Models.DesiredLoaded, []string{"m-b", "m-c"})
	if !r.alerts.has("raise", CodeDefaultNotStarted, "") || !r.alerts.has("raise", CodeNothingLoaded, "") {
		t.Errorf("alerts: %+v", r.alerts.events)
	}
}

func TestRestoreSkipsAModelThatCannotBeUsed(t *testing.T) {
	r := newOrRig(t, orMax(3))
	r.f.Set("m-a", fake.Loaded)
	r.desire("gone", "whisper-x", "m-b")
	res := r.restore()
	got := skipIDs(res.Skipped)
	eqStrings(t, "skipped", got, []string{"gone:not_in_catalog", "whisper-x:not_llama"})
	eqStrings(t, "loaded", r.loadedNow(), []string{"m-a", "m-b"})
}

func TestRestoreWaitsForTheRouterToAnswer(t *testing.T) {
	r := newOrRig(t)
	r.f.Down(true)
	r.setHook(func(n int) {
		if n == 4 {
			r.f.Down(false)
		}
	})
	res := r.restore()
	if res.DefaultState != DefaultStateStarted {
		t.Errorf("state %q", res.DefaultState)
	}
}

func TestRestoreRouterNeverAnswers(t *testing.T) {
	r := newOrRig(t)
	r.f.Down(true)
	start := r.clk.Now()
	_, err := r.o.RestoreModels(bg)
	oe := opErrOf(t, err)
	if oe.Code != CodeLlamaDown || oe.Details["cause"] != "health_timeout" {
		t.Errorf("error: %v", oe)
	}
	want := r.o.opt.Timing.RestartWait
	if got := r.clk.Now().Sub(start); got < want || got > want+5*time.Second {
		t.Errorf("waited %v, want about %v", got, want)
	}
	eqStrings(t, "router calls", r.calls(), nil)
}

func TestBootWaitsAreTheSpecifiedOnes(t *testing.T) {
	tm := Timing{}.withDefaults()
	if tm.RestartWait != 120*time.Second || tm.HealthWait != 60*time.Second {
		t.Errorf("boot wait %v, restart wait %v; want 120 s and 60 s", tm.RestartWait, tm.HealthWait)
	}
	if DefaultReconcileBudget != 30*time.Second {
		t.Errorf("reconcile budget %v", DefaultReconcileBudget)
	}
}

func TestRestoreSingleModeHasNothingToRestore(t *testing.T) {
	r := newOrRig(t, func(c *orCfg) { c.llama.Mode = LlamaModeSingle })
	r.f.Down(true)
	res, err := r.o.RestoreModels(bg)
	if err != nil || res.DefaultState != DefaultStateSingle {
		t.Errorf("result %+v err %v", res, err)
	}
	if n := len(r.f.Requests()); n != 0 {
		t.Errorf("%d router requests in single mode", n)
	}
}

func TestRestoreAfterShutdownAndOnACancelledContext(t *testing.T) {
	r := newOrRig(t)
	ctx, cancel := context.WithCancel(bg)
	cancel()
	r.f.Down(true)
	if _, err := r.o.RestoreModels(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled: %v", err)
	}
	r.f.Down(false)
	sctx, scancel := context.WithTimeout(bg, 5*time.Second)
	defer scancel()
	_ = r.o.Shutdown(sctx)
	if _, err := r.o.RestoreModels(bg); !errors.Is(err, ErrInterrupted) {
		t.Errorf("after shutdown: %v", err)
	}
}

func TestRestoreOrder(t *testing.T) {
	t0 := fixedTime()
	lu := map[string]time.Time{"b": t0.Add(-1 * time.Hour), "c": t0.Add(-2 * time.Hour), "d": t0.Add(-3 * time.Hour)}
	tests := []struct {
		name    string
		def     string
		desired []string
		want    []string
	}{
		{"default first", "a", []string{"b", "c"}, []string{"a", "b", "c"}},
		{"default dropped from the rest", "b", []string{"b", "c"}, []string{"b", "c"}},
		{"most recent first", "a", []string{"d", "c", "b"}, []string{"a", "b", "c", "d"}},
		{"duplicates", "", []string{"c", "c", "b"}, []string{"b", "c"}},
		{"ties keep the stored order", "", []string{"y", "x"}, []string{"y", "x"}},
		{"nothing", "", nil, []string{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			eqStrings(t, "order", restoreOrder(tc.def, tc.desired, lu), tc.want)
		})
	}
}

func TestFileBuild(t *testing.T) {
	good := `{"build_id":"f3586e66b-cuda","commit":"f3586e66b"}`
	tests := []struct {
		name string
		body string
		want string
		fail bool
	}{
		{"ok", good, "f3586e66b-cuda", false},
		{"no id", `{"commit":"x"}`, "", true},
		{"bad id", `{"build_id":"../etc"}`, "", true},
		{"not json", `build`, "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fs := newMemFS()
			fs.put(DefaultBuildJSON, []byte(tc.body), 0o644)
			got, err := FileBuild{FS: fs}.CurrentBuild()
			if (err != nil) != tc.fail || got != tc.want {
				t.Errorf("got %q, %v", got, err)
			}
		})
	}
	if _, err := (FileBuild{FS: newMemFS()}).CurrentBuild(); err == nil {
		t.Error("a missing file is not an error")
	}
}

func TestSkipFrom(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		code   Code
		reason string
	}{
		{"no room", errNoRoom("m", reasonNoRoom()), CodeNoRoom, "no_room"},
		{"not loadable", errNotLoadable("m", &Reason{Code: CodeFilesMissing}), CodeFilesMissing, "not_loadable"},
		{"not llama", errNotLlama("m", "whisper"), CodeNotFit, "not_llama"},
		{"unknown", errNotFound("m"), CodeNotFound, "not_in_catalog"},
		{"busy", errBusy(&BusyError{OpID: "op_x"}), CodeBusy, "busy"},
		{"load failed", errLoadFailed("m", &loadFailure{kind: kindFailed}, true, nil, false), CodeModelFailed, "failed"},
		{"timeout", errLoadFailed("m", &loadFailure{kind: kindTimeout}, true, nil, false), CodeLoadTimeout, "timeout"},
		{"plain", errors.New("x"), CodeModelFailed, "failed"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sk := skipFrom("m", tc.err)
			if sk.Code != tc.code || sk.Reason != tc.reason || sk.ID != "m" {
				t.Errorf("%+v", sk)
			}
		})
	}
}
