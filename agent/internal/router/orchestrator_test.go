package router

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/audit"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/modes"
	"github.com/marinoscar/local-ai/agent/internal/router/fake"
	"github.com/marinoscar/local-ai/agent/internal/state"
)

var _ modes.ModelLoader = (*Orchestrator)(nil)

func TestLoadSuccessWithoutEviction(t *testing.T) {
	r := newOrRig(t)
	res, err := r.o.Load(bg, "m-b", true, true, audit.Operator(audit.ViaBearer))
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "loaded" || res.AlreadyLoaded || res.Accepted || len(res.Unloaded) != 0 || res.LoadMS != 5000 {
		t.Errorf("result: %+v", res)
	}
	if !strings.HasPrefix(res.OpID, "op_") || len(res.OpID) != 15 {
		t.Errorf("op id %q is not op_ plus 12 characters", res.OpID)
	}
	eqStrings(t, "router calls", r.calls(), []string{"load m-b"})
	eqStrings(t, "events", r.evs(), []string{"loading m-b unloading=[]", "loaded m-b"})

	ll := r.lastLoad()
	if ll.ID != "m-b" || ll.OpID != res.OpID || ll.Result != state.LoadOK || ll.LoadMS != 5000 {
		t.Errorf("last_load: %+v", ll)
	}
	s := r.snap().Models
	eqStrings(t, "desired_loaded", s.DesiredLoaded, []string{"m-b"})
	if !s.LastUsed["m-b"].Equal(r.clk.Now().Truncate(time.Second)) {
		t.Errorf("last_used[m-b] = %v, now %v", s.LastUsed["m-b"], r.clk.Now())
	}

	a := r.aud.events()
	if len(a) != 1 || a[0].Action != audit.ActionModelLoad || a[0].Result != audit.ResultOK || a[0].OpID != res.OpID || a[0].Target["model"] != "m-b" || a[0].Actor.Kind != audit.KindOperator {
		t.Errorf("audit: %+v", a)
	}
	if got := r.sup.started; len(got) != 1 || got[0] != res.OpID || len(r.sup.released) != 1 || r.sup.anyActive() {
		t.Errorf("the watcher was not suppressed exactly for the op: %v %v", r.sup.started, r.sup.released)
	}
	if _, held := r.o.Lock().Holder(); held {
		t.Error("the lock is still held")
	}
	c := r.o.Counters()
	if c.LoadsOK != 1 || c.LoadsFailed != 0 || c.Rollbacks != 0 || c.Models["m-b"] != (LoadStats{Count: 1, LastMS: 5000, MeanMS: 5000}) {
		t.Errorf("counters: %+v", c)
	}
}

func TestLoadEvictsTheLeastRecentlyUsed(t *testing.T) {
	r := newOrRig(t)
	r.load("m-a")
	res, err := r.o.Load(bg, "m-b", true, true, sysActor)
	if err != nil {
		t.Fatal(err)
	}
	eqStrings(t, "unloaded", res.Unloaded, []string{"m-a"})
	eqStrings(t, "router calls", r.calls(), []string{"unload m-a", "load m-b"})
	eqStrings(t, "events", r.evs(), []string{"loading m-b unloading=[m-a]", "unloaded m-a evicted", "loaded m-b"})
	eqStrings(t, "loaded", r.loadedNow(), []string{"m-b"})
	eqStrings(t, "desired_loaded", r.snap().Models.DesiredLoaded, []string{"m-b"})
}

func TestLoadEvictsInLRUOrderWhenSeveralModelsAreLoaded(t *testing.T) {
	r := newOrRig(t, orMax(3))
	r.load("m-a", "m-b", "m-c") // m-a is the oldest
	res, err := r.o.Load(bg, "m-d", true, true, sysActor)
	if err != nil {
		t.Fatal(err)
	}
	eqStrings(t, "unloaded", res.Unloaded, []string{"m-a"})
	eqStrings(t, "loaded", r.loadedNow(), []string{"m-b", "m-c", "m-d"})
	eqStrings(t, "desired_loaded (most recent first)", r.snap().Models.DesiredLoaded, []string{"m-d", "m-c", "m-b"})
}

func TestLoadAlreadyLoaded(t *testing.T) {
	r := newOrRig(t)
	r.load("m-a")
	res, err := r.o.Load(bg, "m-a", true, true, sysActor)
	if err != nil {
		t.Fatal(err)
	}
	if !res.AlreadyLoaded || res.Status != "loaded" || res.OpID == "" {
		t.Errorf("result: %+v", res)
	}
	if got := r.calls(); len(got) != 0 {
		t.Errorf("an already loaded model caused router calls: %v", got)
	}
	if !r.snap().Models.LastUsed["m-a"].Equal(r.clk.Now().Truncate(time.Second)) {
		t.Errorf("last_used was not refreshed: %v", r.snap().Models.LastUsed["m-a"])
	}
	if r.sup.anyActive() {
		t.Error("the watcher is still suppressed")
	}
	if a := r.aud.events(); len(a) != 1 || a[0].Result != audit.ResultOK {
		t.Errorf("audit: %+v", a)
	}
}

func TestLoadWaitsForALoadStartedBySomeoneElse(t *testing.T) {
	r := newOrRig(t)
	r.f.Set("m-b", fake.Loading) // held open
	r.setHook(func(n int) {
		if n == 2 {
			r.f.Set("m-b", fake.Loaded)
		}
	})
	res, err := r.o.Load(bg, "m-b", true, true, sysActor)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "loaded" || res.AlreadyLoaded {
		t.Errorf("result: %+v", res)
	}
	if n := r.countCalls("load"); n != 0 {
		t.Errorf("a load started by someone else was started again (%d requests)", n)
	}
	if ll := r.lastLoad(); ll.Result != state.LoadOK {
		t.Errorf("last_load: %+v", ll)
	}
}

func TestLoadNoRoomWithoutUnloadOthers(t *testing.T) {
	r := newOrRig(t)
	r.load("m-a")
	_, err := r.o.Load(bg, "m-b", true, false, sysActor)
	oe := opErrOf(t, err)
	if oe.Code != CodeNoRoom || oe.HTTPStatus() != http.StatusConflict {
		t.Errorf("got %s %d", oe.Code, oe.HTTPStatus())
	}
	if got := r.calls(); len(got) != 0 {
		t.Errorf("router calls: %v", got)
	}
	if r.snap().Models.LastLoad != nil {
		t.Errorf("last_load was written for a refused load: %+v", r.snap().Models.LastLoad)
	}
	if _, held := r.o.Lock().Holder(); held || r.sup.anyActive() {
		t.Error("lock or suppression leaked")
	}
	if a := r.aud.events(); len(a) != 1 || a[0].Result != audit.ResultFail || a[0].Code != string(CodeNoRoom) {
		t.Errorf("audit: %+v", a)
	}
}

func TestLoadRefusals(t *testing.T) {
	cases := []struct {
		name   string
		mut    func(*orCfg)
		id     string
		router func(*orRig)
		code   Code
		status int
	}{
		{"unknown model", nil, "nope", nil, doctor.AGT021, 404},
		{"not a llama model", nil, "whisper-x", nil, doctor.CAT005, 422},
		{"files missing", func(c *orCfg) { c.presence.missing = map[string]bool{"m-b": true} }, "m-b", nil, doctor.LLM008, 422},
		{"router down", nil, "m-b", func(r *orRig) { r.f.Down(true) }, doctor.LLM004, 502},
		{"router rejects our key", nil, "m-b", func(r *orRig) { r.f.SetKey(secret(2)) }, doctor.LLM005, 502},
		{"not in the router's list", nil, "m-b", func(r *orRig) { r.f.Remove("m-b") }, doctor.LLM007, 500},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newOrRig(t, tc.mut)
			if tc.router != nil {
				tc.router(r)
			}
			_, err := r.o.Load(bg, tc.id, true, true, sysActor)
			oe := opErrOf(t, err)
			if oe.Code != tc.code || oe.HTTPStatus() != tc.status {
				t.Errorf("got %s %d, want %s %d", oe.Code, oe.HTTPStatus(), tc.code, tc.status)
			}
			if r.countCalls("load") != 0 || r.countCalls("unload") != 0 {
				t.Errorf("router calls: %v", r.calls())
			}
			if r.snap().Models.LastLoad != nil {
				t.Errorf("last_load was written: %+v", r.snap().Models.LastLoad)
			}
			if _, held := r.o.Lock().Holder(); held || r.sup.anyActive() {
				t.Error("lock or suppression leaked")
			}
		})
	}
}

func TestLoadAnUnfittingModelIsRefusedBeforeAnything(t *testing.T) {
	r := newOrRig(t, func(c *orCfg) { c.env.GTotalMiB = 3000 })
	_, err := r.o.Load(bg, "m-b", true, true, sysActor)
	oe := opErrOf(t, err)
	if oe.Code != CodeNotFit || oe.HTTPStatus() != 422 {
		t.Errorf("got %s %d", oe.Code, oe.HTTPStatus())
	}
	if len(r.f.Requests()) != 0 {
		t.Errorf("the router was called: %v", r.f.Requests())
	}
}

func TestLoadFailedRollsBackAndRestoresTheEvicted(t *testing.T) {
	r := newOrRig(t)
	r.load("m-a")
	r.f.FailLoad("m-b", 137)
	_, err := r.o.Load(bg, "m-b", true, true, sysActor)
	oe := opErrOf(t, err)
	if oe.Code != CodeModelFailed || oe.HTTPStatus() != 500 {
		t.Fatalf("got %s %d", oe.Code, oe.HTTPStatus())
	}
	if oe.Details["rolled_back"] != true || oe.Details["exit_code"] != 137 || oe.Details["cause"] != kindFailed {
		t.Errorf("details: %v", oe.Details)
	}
	if got, _ := oe.Details["restored"].([]string); len(got) != 1 || got[0] != "m-a" {
		t.Errorf("restored: %v", oe.Details["restored"])
	}
	if oe.Details["op_id"] == "" {
		t.Error("no op_id in the details")
	}
	eqStrings(t, "loaded after the rollback", r.loadedNow(), []string{"m-a"})
	eqStrings(t, "router calls", r.calls(), []string{"unload m-a", "load m-b", "load m-a"})
	eqStrings(t, "events", r.evs(), []string{
		"loading m-b unloading=[m-a]", "unloaded m-a evicted", "loaded m-a",
		"failed m-b " + doctor.LLM009 + " rolled_back=true restored=[m-a]",
	})
	ll := r.lastLoad()
	if ll.Result != state.LoadFailed || ll.ID != "m-b" {
		t.Errorf("last_load: %+v", ll)
	}
	eqStrings(t, "desired_loaded", r.snap().Models.DesiredLoaded, []string{"m-a"})
	c := r.o.Counters()
	if c.LoadsFailed != 1 || c.Rollbacks != 1 || c.LoadsOK != 0 {
		t.Errorf("counters: %+v", c)
	}
	if a := r.aud.events(); len(a) != 1 || a[0].Result != audit.ResultFail || a[0].Code != doctor.LLM009 {
		t.Errorf("audit: %+v", a)
	}
	if r.alerts.has("raise", CodeNothingLoaded, "*") {
		t.Error("LAI-LLM-012 raised although the previous model came back")
	}
	if _, held := r.o.Lock().Holder(); held || r.sup.anyActive() {
		t.Error("lock or suppression leaked")
	}
}

func TestLoadTimeoutRollsBack(t *testing.T) {
	r := newOrRig(t)
	r.load("m-a")
	// m-b never gets ready: whenever the orchestrator sleeps, the model is held in "loading".
	r.setHook(func(int) {
		if r.f.Status("m-b") == fake.Loading {
			r.f.Set("m-b", fake.Loading)
		}
	})
	start := r.clk.Now()
	_, err := r.o.Load(bg, "m-b", true, true, sysActor)
	oe := opErrOf(t, err)
	if oe.Code != CodeLoadTimeout || oe.HTTPStatus() != http.StatusGatewayTimeout || oe.Details["rolled_back"] != true || oe.Details["cause"] != kindTimeout {
		t.Fatalf("got %s %d %v", oe.Code, oe.HTTPStatus(), oe.Details)
	}
	if waited := r.clk.Now().Sub(start); waited < 300*time.Second || waited > 345*time.Second {
		t.Errorf("waited %v for a 300 s timeout", waited)
	}
	eqStrings(t, "loaded", r.loadedNow(), []string{"m-a"})
	// The model that timed out was stopped before the previous one was restored.
	eqStrings(t, "router calls", r.calls(), []string{"unload m-a", "load m-b", "unload m-b", "load m-a"})
	if ll := r.lastLoad(); ll.Result != state.LoadTimeout {
		t.Errorf("last_load: %+v", ll)
	}
	if got := r.evs(); !contains1(got, "unloaded m-b rollback") {
		t.Errorf("events: %q", got)
	}
}

func TestLoadTimeoutIsDoubledOnAnArkDrive(t *testing.T) {
	r := newOrRig(t, func(c *orCfg) { c.presence.drive = map[string]bool{"m-b": true} })
	r.setHook(func(int) {
		if r.f.Status("m-b") == fake.Loading {
			r.f.Set("m-b", fake.Loading)
		}
	})
	start := r.clk.Now()
	_, err := r.o.Load(bg, "m-b", true, true, sysActor)
	if oe := opErrOf(t, err); oe.Code != CodeLoadTimeout {
		t.Fatalf("got %s", oe.Code)
	}
	if waited := r.clk.Now().Sub(start); waited < 600*time.Second {
		t.Errorf("waited only %v for a model on a drive (want 600 s)", waited)
	}
}

func TestLoadCrashMidLoad(t *testing.T) {
	r := newOrRig(t)
	r.load("m-a")
	r.f.SetLoadTime(30 * time.Second)
	r.setHook(func(n int) {
		if n == 3 { // a few polls into the load
			r.f.Crash("m-b", 139)
		}
	})
	_, err := r.o.Load(bg, "m-b", true, true, sysActor)
	oe := opErrOf(t, err)
	if oe.Code != CodeModelFailed || oe.Details["exit_code"] != 139 || oe.Details["rolled_back"] != true {
		t.Fatalf("got %s %v", oe.Code, oe.Details)
	}
	eqStrings(t, "loaded", r.loadedNow(), []string{"m-a"})
}

func TestLoadRouterRestartMidLoadIsAFailureWithoutExitCode(t *testing.T) {
	r := newOrRig(t)
	r.load("m-a")
	r.f.SetLoadTime(30 * time.Second)
	r.setHook(func(n int) {
		if n == 3 {
			r.f.Restart() // every child is gone; nothing has "failed"
		}
	})
	_, err := r.o.Load(bg, "m-b", true, true, sysActor)
	oe := opErrOf(t, err)
	if oe.Code != CodeModelFailed || oe.Details["cause"] != kindFailed {
		t.Fatalf("got %s %v", oe.Code, oe.Details)
	}
	if _, has := oe.Details["exit_code"]; has {
		t.Errorf("an exit code was invented: %v", oe.Details)
	}
	eqStrings(t, "loaded", r.loadedNow(), []string{"m-a"})
}

func TestLoadRouterRequestRejected(t *testing.T) {
	r := newOrRig(t)
	r.load("m-a")
	r.f.RejectLoad("m-b", 500)
	_, err := r.o.Load(bg, "m-b", true, true, sysActor)
	oe := opErrOf(t, err)
	if oe.Code != CodeModelFailed || oe.Details["cause"] != kindRejected || oe.Details["rolled_back"] != true {
		t.Fatalf("got %s %v", oe.Code, oe.Details)
	}
	eqStrings(t, "loaded", r.loadedNow(), []string{"m-a"})
}

func TestLoadVictimThatWillNotUnloadFailsWithNothingNewLoaded(t *testing.T) {
	r := newOrRig(t)
	r.load("m-a")
	r.f.Stick("m-a", true)
	start := r.clk.Now()
	_, err := r.o.Load(bg, "m-b", true, true, sysActor)
	oe := opErrOf(t, err)
	if oe.Code != CodeModelFailed || oe.Details["cause"] != kindUnloadTimeout || oe.Details["unloading"] != "m-a" {
		t.Fatalf("got %s %v", oe.Code, oe.Details)
	}
	if waited := r.clk.Now().Sub(start); waited < 30*time.Second || waited > 35*time.Second {
		t.Errorf("waited %v for the 30 s unload timeout", waited)
	}
	if r.countCalls("load") != 0 {
		t.Errorf("a model was loaded although the victim did not stop: %v", r.calls())
	}
	// The victim is still the loaded model, so this is a rollback to the previous state.
	eqStrings(t, "loaded", r.loadedNow(), []string{"m-a"})
	if oe.Details["rolled_back"] != true {
		t.Errorf("details: %v", oe.Details)
	}
}

func TestRollbackFallsBackToTheDefaultThenTheSurvivalList(t *testing.T) {
	// m-a was loaded and evicted; m-b fails; m-a and the default m-c fail; m-d, the second survival
	// model, comes up.
	r := newOrRig(t, orSurvival())
	r.load("m-a")
	r.f.FailLoad("m-b", 1)
	r.f.FailLoad("m-a", 1)
	r.f.FailLoad("m-c", 1)
	r.seed(func(s *state.State) { d := "m-c"; s.Models.Default = &d })
	_, err := r.o.Load(bg, "m-b", true, true, sysActor)
	oe := opErrOf(t, err)
	if oe.Code != CodeModelFailed || oe.Details["rolled_back"] != true {
		t.Fatalf("got %s %v", oe.Code, oe.Details)
	}
	if got, _ := oe.Details["restored"].([]string); len(got) != 1 || got[0] != "m-d" {
		t.Errorf("restored: %v", oe.Details["restored"])
	}
	eqStrings(t, "loaded", r.loadedNow(), []string{"m-d"})
	eqStrings(t, "router calls", r.calls(), []string{
		"unload m-a", "load m-b", // m-b fails
		"load m-a", // the evicted model: fails
		"load m-c", // the default: fails
		"load m-d", // the survival list: m-c is skipped, it was tried
	})
	eqStrings(t, "desired_loaded", r.snap().Models.DesiredLoaded, []string{"m-d"})
}

func TestRollbackNeverLoopsWhenEveryLoadFails(t *testing.T) {
	r := newOrRig(t, orSurvival())
	r.load("m-a")
	for _, id := range []string{"m-a", "m-b", "m-c", "m-d"} {
		r.f.RejectLoad(id, 500)
	}
	_, err := r.o.Load(bg, "m-b", true, true, sysActor)
	oe := opErrOf(t, err)
	if oe.Code != CodeNothingLoaded || oe.HTTPStatus() != 500 || oe.Details["rolled_back"] != false {
		t.Fatalf("got %s %d %v", oe.Code, oe.HTTPStatus(), oe.Details)
	}
	// One try per model: the requested one, then the evicted one, then the default (m-a, already
	// tried, is skipped) and the survival list. No model is tried twice.
	got := r.calls()
	seen := map[string]int{}
	for _, c := range got {
		if strings.HasPrefix(c, "load ") {
			seen[c]++
		}
	}
	for c, n := range seen {
		if n != 1 {
			t.Errorf("%q was sent %d times: %v", c, n, got)
		}
	}
	if len(seen) != 4 {
		t.Errorf("expected one try for each of the four models, got %v", got)
	}
	if !r.alerts.has("raise", CodeNothingLoaded, "*") {
		t.Error("LAI-LLM-012 was not raised")
	}
	if len(r.loadedNow()) != 0 {
		t.Errorf("loaded: %v", r.loadedNow())
	}
	// With nothing loaded the recorded set is kept, so the next boot retries it.
	eqStrings(t, "desired_loaded", r.snap().Models.DesiredLoaded, []string{"m-a"})
	if ll := r.lastLoad(); ll.Result != state.LoadFailed {
		t.Errorf("last_load: %+v", ll)
	}
	if got := r.evs(); !contains1(got, "failed m-b "+doctor.LLM012+" rolled_back=false restored=[]") {
		t.Errorf("events: %q", got)
	}
	// A later success clears the alert.
	r.alerts.reset()
	r.f.RejectLoad("m-b", 0)
	if _, err := r.o.Load(bg, "m-b", true, true, sysActor); err != nil {
		t.Fatal(err)
	}
	if !r.alerts.has("clear", CodeNothingLoaded, "*") {
		t.Error("LAI-LLM-012 was not cleared by a successful load")
	}
}

func TestRollbackWalksNoSurvivalListOutsideSurvivalMode(t *testing.T) {
	r := newOrRig(t) // online
	r.load("m-a")
	r.f.FailLoad("m-b", 1)
	r.f.FailLoad("m-a", 1)
	_, err := r.o.Load(bg, "m-b", true, true, sysActor)
	if oe := opErrOf(t, err); oe.Code != CodeNothingLoaded {
		t.Fatalf("got %s", oe.Code)
	}
	for _, c := range r.calls() {
		if c == "load m-c" || c == "load m-d" {
			t.Errorf("the survival list was walked outside survival mode: %v", r.calls())
		}
	}
}

func TestRouterDiesMidLoad(t *testing.T) {
	r := newOrRig(t)
	r.load("m-a")
	r.f.SetLoadTime(time.Hour)
	r.setHook(func(n int) {
		if n == 2 {
			r.f.Down(true)
		}
	})
	start := r.clk.Now()
	_, err := r.o.Load(bg, "m-b", true, true, sysActor)
	oe := opErrOf(t, err)
	if oe.Code != CodeNothingLoaded || oe.Details["cause"] != kindRouterDown {
		t.Fatalf("got %s %v", oe.Code, oe.Details)
	}
	if waited := r.clk.Now().Sub(start); waited > 40*time.Second {
		t.Errorf("waited %v for a router that is gone (grace is 20 s)", waited)
	}
	if !r.alerts.has("raise", CodeNothingLoaded, "*") {
		t.Error("LAI-LLM-012 was not raised")
	}
}

func TestRouterDownForAShortWhileDuringALoadIsTolerated(t *testing.T) {
	r := newOrRig(t)
	r.f.SetLoadTime(10 * time.Second)
	r.setHook(func(n int) {
		switch n {
		case 2:
			r.f.Down(true)
		case 5:
			r.f.Down(false)
		}
	})
	if _, err := r.o.Load(bg, "m-b", true, true, sysActor); err != nil {
		t.Fatalf("a restart of a few seconds failed the load: %v", err)
	}
}

func TestProgressIsForwardedAtMostOncePerSecond(t *testing.T) {
	r := newOrRig(t)
	r.f.SetLoadTime(6 * time.Second)
	r.setHook(func(n int) { r.prog.set("m-b", float64(n)/10) })
	if _, err := r.o.Load(bg, "m-b", true, true, sysActor); err != nil {
		t.Fatal(err)
	}
	var progress []string
	for _, e := range r.evs() {
		if strings.Contains(e, "progress=") {
			progress = append(progress, e)
		}
	}
	if len(progress) == 0 || len(progress) > 6 {
		t.Errorf("progress events: %q", progress)
	}
	if len(progress) > 0 && !strings.HasPrefix(progress[0], "loading m-b unloading=[] progress=0.") {
		t.Errorf("first progress event: %q", progress[0])
	}
}

func TestWaitFalseAnswersAtOnceAndFinishesInTheBackground(t *testing.T) {
	r := newOrRig(t)
	r.load("m-a")
	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	r.setHook(func(n int) {
		if n == 1 {
			entered <- struct{}{}
			<-release
		}
	})
	type done struct {
		res LoadResult
		err error
	}
	ch := make(chan done, 1)
	res, err := r.o.LoadWith(bg, LoadRequest{ID: "m-b", Wait: false, UnloadOthers: true, Actor: sysActor,
		OnDone: func(res LoadResult, err error) { ch <- done{res, err} }})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Accepted || res.Status != "loading" || res.OpID == "" {
		t.Fatalf("answer: %+v", res)
	}
	<-entered
	// The operation holds the lock while it runs in the background.
	if h, held := r.o.Lock().Holder(); !held || h.OpID != res.OpID {
		t.Errorf("holder: %+v %v", h, held)
	}
	_, err = r.o.Load(bg, "m-c", true, true, sysActor)
	oe := opErrOf(t, err)
	if oe.Code != CodeBusy || oe.HTTPStatus() != http.StatusConflict || oe.Details["op_id"] != res.OpID {
		t.Errorf("a second load while one runs: %s %v", oe.Code, oe.Details)
	}
	var be *BusyError
	if !errors.As(err, &be) || be.OpID != res.OpID {
		t.Errorf("BusyError is not reachable: %v", err)
	}
	close(release)
	d := <-ch
	if d.err != nil || d.res.Status != "loaded" || d.res.OpID != res.OpID {
		t.Errorf("OnDone: %+v %v", d.res, d.err)
	}
	if err := r.o.Wait(bg); err != nil {
		t.Fatal(err)
	}
	eqStrings(t, "events", r.evs(), []string{"loading m-b unloading=[m-a]", "unloaded m-a evicted", "loaded m-b"})
	eqStrings(t, "loaded", r.loadedNow(), []string{"m-b"})
	if a := r.aud.events(); len(a) != 2 || a[0].OpID != "" && a[0].Code != string(CodeBusy) {
		t.Logf("audit: %+v", a)
	}
}

func TestBusyLockAnswers409WithTheHoldersOpID(t *testing.T) {
	r := newOrRig(t)
	hold, err := r.o.Lock().TryAcquire(HolderOther)
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.o.Load(bg, "m-b", true, true, sysActor)
	oe := opErrOf(t, err)
	if oe.Code != CodeBusy || oe.HTTPStatus() != 409 || oe.Details["op_id"] != hold.OpID {
		t.Fatalf("got %s %d %v", oe.Code, oe.HTTPStatus(), oe.Details)
	}
	if r.countCalls("load") != 0 {
		t.Error("a load ran while the lock was held")
	}
	_, err = r.o.Unload(bg, "m-a", false, sysActor)
	if oe := opErrOf(t, err); oe.Code != CodeBusy {
		t.Errorf("unload: %s", oe.Code)
	}
	hold.Release()
	if _, err := r.o.Load(bg, "m-b", true, true, sysActor); err != nil {
		t.Fatalf("after the lock was released: %v", err)
	}
}

func TestLockWaitWaitsForTheHolder(t *testing.T) {
	r := newOrRig(t)
	hold, _ := r.o.Lock().TryAcquire(HolderOther)
	go func() {
		time.Sleep(30 * time.Millisecond)
		hold.Release()
	}()
	res, err := r.o.LoadWith(bg, LoadRequest{ID: "m-b", Wait: true, UnloadOthers: true, Actor: sysActor, LockWait: 5 * time.Second})
	if err != nil || res.Status != "loaded" {
		t.Fatalf("%+v %v", res, err)
	}
}

func TestLockWaitGivesUp(t *testing.T) {
	r := newOrRig(t)
	hold, _ := r.o.Lock().TryAcquire(HolderOther)
	defer hold.Release()
	_, err := r.o.LoadWith(bg, LoadRequest{ID: "m-b", Wait: true, UnloadOthers: true, Actor: sysActor, LockWait: 30 * time.Millisecond})
	if oe := opErrOf(t, err); oe.Code != CodeBusy || oe.Details["op_id"] != hold.OpID {
		t.Fatalf("got %v", err)
	}
}

func TestACallerThatGivesUpLeavesTheOperationRunning(t *testing.T) {
	r := newOrRig(t)
	r.load("m-a")
	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	r.setHook(func(n int) {
		if n == 1 {
			entered <- struct{}{}
			<-release
		}
	})
	ctx, cancel := context.WithCancel(bg)
	errCh := make(chan error, 1)
	go func() {
		_, err := r.o.Load(ctx, "m-b", true, true, sysActor)
		errCh <- err
	}()
	<-entered
	cancel()
	if err := <-errCh; !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
	close(release)
	if err := r.o.Wait(bg); err != nil {
		t.Fatal(err)
	}
	// The abandoned operation still finished: the node is not left half way.
	eqStrings(t, "loaded", r.loadedNow(), []string{"m-b"})
	if ll := r.lastLoad(); ll.Result != state.LoadOK {
		t.Errorf("last_load: %+v", ll)
	}
	if _, held := r.o.Lock().Holder(); held {
		t.Error("the lock is still held")
	}
}

func TestInProgressIsDurableBeforeAnythingIsEvicted(t *testing.T) {
	r := newOrRig(t)
	r.load("m-a")
	var onDisk string
	var unloadsSeen int
	r.setHook(func(n int) {
		if n != 1 {
			return
		}
		// Power loss right now: only fsynced bytes survive.
		r.fs.Reboot()
		raw, _ := r.fs.Contents(orStatePath)
		onDisk = string(raw)
		unloadsSeen = r.countCalls("unload")
	})
	if _, err := r.o.Load(bg, "m-b", true, true, sysActor); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(onDisk, `"result": "in_progress"`) && !strings.Contains(onDisk, `"result":"in_progress"`) {
		t.Errorf("last_load in_progress was not on disk when the first model was being evicted:\n%s", onDisk)
	}
	if unloadsSeen != 1 {
		t.Errorf("hook ran after %d unloads", unloadsSeen)
	}
}

func TestShutdownMidLoadLeavesTheRouterAloneAndRecordsInterrupted(t *testing.T) {
	r := newOrRig(t)
	r.load("m-a")
	r.f.SetLoadTime(time.Hour)
	var once sync.Once
	reached := make(chan struct{})
	r.setHook(func(n int) {
		if n == 3 {
			once.Do(func() { close(reached) })
		}
	})
	errCh := make(chan error, 1)
	go func() {
		_, err := r.o.Load(bg, "m-b", true, true, sysActor)
		errCh <- err
	}()
	// Real sleep is replaced by the hook, so wait for the third poll and shut down.
	<-reached
	ctx, cancel := context.WithTimeout(bg, 5*time.Second)
	defer cancel()
	if err := r.o.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-errCh; !errors.Is(err, ErrInterrupted) {
		t.Fatalf("got %v", err)
	}
	if ll := r.lastLoad(); ll.Result != state.LoadInterrupted || ll.ID != "m-b" {
		t.Errorf("last_load: %+v", ll)
	}
	// No rollback: the router keeps doing what it was asked.
	eqStrings(t, "router calls", r.calls(), []string{"unload m-a", "load m-b"})
	for _, e := range r.evs() {
		if strings.HasPrefix(e, "failed") {
			t.Errorf("model.failed was published for an interrupted load: %s", e)
		}
	}
	if _, held := r.o.Lock().Holder(); held || r.sup.anyActive() {
		t.Error("lock or suppression leaked")
	}
	// After the shutdown nothing new starts waiting.
	if _, err := r.o.Load(bg, "m-c", true, true, sysActor); err == nil {
		t.Error("a load after Shutdown should not complete")
	}
}

func TestLoadHeldWaitsWithoutTheLockAndWithoutEviction(t *testing.T) {
	r := newOrRig(t)
	hold, _ := r.o.Lock().TryAcquire(HolderReload) // the generator holds it, as in production
	defer hold.Release()
	r.load("m-a")
	if err := r.o.LoadHeld(bg, "m-b"); err != nil {
		// m-b over a loaded m-a is the router's business (it refuses or evicts); the fake loads it.
		t.Fatal(err)
	}
	eqStrings(t, "router calls", r.calls(), []string{"load m-b"})
	r.f.RejectLoad("m-c", 500)
	if err := r.o.LoadHeld(bg, "m-c"); err == nil {
		t.Error("a refused load must be an error")
	}
	// Loading a model that runs already waits for it instead of failing.
	if err := r.o.LoadHeld(bg, "m-b"); err != nil {
		t.Errorf("already running: %v", err)
	}
}

func TestLoadSurvivalSet(t *testing.T) {
	t.Run("loads the list until models_max is reached", func(t *testing.T) {
		r := newOrRig(t, orMax(2), orSurvival())
		r.load("m-a")
		w, err := r.o.LoadSurvivalSet(bg, nil)
		if err != nil || len(w) != 0 {
			t.Fatalf("%v %v", w, err)
		}
		eqStrings(t, "loaded", r.loadedNow(), []string{"m-c", "m-d"})
		// m-c fits next to m-a; for m-d the least recently used one, m-a, goes.
		eqStrings(t, "router calls", r.calls(), []string{"load m-c", "unload m-a", "load m-d"})
	})
	t.Run("one model when the node holds one", func(t *testing.T) {
		r := newOrRig(t, orSurvival())
		w, err := r.o.LoadSurvivalSet(bg, nil)
		if err != nil || len(w) != 0 {
			t.Fatalf("%v %v", w, err)
		}
		eqStrings(t, "loaded", r.loadedNow(), []string{"m-c"})
	})
	t.Run("a model that is loaded counts and is left alone", func(t *testing.T) {
		r := newOrRig(t, orSurvival())
		r.load("m-d")
		if w, err := r.o.LoadSurvivalSet(bg, nil); err != nil || len(w) != 0 {
			t.Fatalf("%v %v", w, err)
		}
		if got := r.calls(); len(got) != 0 {
			t.Errorf("router calls: %v", got)
		}
	})
	t.Run("a failing model is a warning and the next one is used", func(t *testing.T) {
		r := newOrRig(t, orSurvival())
		r.f.FailLoad("m-c", 1)
		w, err := r.o.LoadSurvivalSet(bg, nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(w) != 1 || w[0].Code != doctor.LLM020 || !strings.Contains(w[0].Message, "m-c") {
			t.Errorf("warnings: %+v", w)
		}
		eqStrings(t, "loaded", r.loadedNow(), []string{"m-d"})
	})
	t.Run("skip is honoured", func(t *testing.T) {
		r := newOrRig(t, orSurvival())
		if _, err := r.o.LoadSurvivalSet(bg, []string{"m-c"}); err != nil {
			t.Fatal(err)
		}
		eqStrings(t, "loaded", r.loadedNow(), []string{"m-d"})
		if r.countCalls("load m-c") != 0 {
			t.Error("a skipped model was tried")
		}
	})
	t.Run("nothing loads: LLM-021", func(t *testing.T) {
		r := newOrRig(t, orSurvival())
		for _, id := range []string{"m-a", "m-c", "m-d"} {
			r.f.RejectLoad(id, 500)
		}
		w, err := r.o.LoadSurvivalSet(bg, nil)
		if err != nil {
			t.Fatal(err)
		}
		var codes []string
		for _, x := range w {
			codes = append(codes, x.Code)
		}
		if got := strings.Join(codes, ","); !strings.HasSuffix(got, doctor.LLM021) || !strings.Contains(got, doctor.LLM020) {
			t.Errorf("codes: %s", got)
		}
	})
	t.Run("the router is down: it could not even try", func(t *testing.T) {
		r := newOrRig(t, orSurvival())
		r.f.Down(true)
		if _, err := r.o.LoadSurvivalSet(bg, nil); err == nil {
			t.Error("want an error")
		}
	})
	t.Run("waits for the lock", func(t *testing.T) {
		r := newOrRig(t, orSurvival())
		hold, _ := r.o.Lock().TryAcquire(HolderOther)
		go func() {
			time.Sleep(30 * time.Millisecond)
			hold.Release()
		}()
		if w, err := r.o.LoadSurvivalSet(bg, nil); err != nil || len(w) != 0 {
			t.Fatalf("%v %v", w, err)
		}
		eqStrings(t, "loaded", r.loadedNow(), []string{"m-c"})
	})
}

func TestNewOrchestratorNeedsItsPorts(t *testing.T) {
	if _, err := NewOrchestrator(OrchestratorOptions{}); err == nil {
		t.Error("want an error")
	}
}
