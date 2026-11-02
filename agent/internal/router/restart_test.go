package router

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/api"
	"github.com/marinoscar/local-ai/agent/internal/router/fake"
	"github.com/marinoscar/local-ai/agent/internal/services"
	"github.com/marinoscar/local-ai/agent/internal/state"
)

// restarting makes the restarter do what systemd does to the router: every child is gone, and the
// models flagged load-on-startup load again. m-a is the default and carries the flag.
func (r *orRig) restarting() {
	r.routerLoadsDefault()
	r.rs.hook = func() { r.f.Restart() }
}

func (r *orRig) restartOK() RestartResult {
	r.t.Helper()
	res, err := r.o.RestartAndRestore(bg, sysActor)
	if err != nil {
		r.t.Fatalf("RestartAndRestore: %v", err)
	}
	return res
}

func TestRestartAndRestoreRestoresDefaultFirstThenMostRecent(t *testing.T) {
	r := newOrRig(t, orMax(3))
	r.restarting()
	r.load("m-a", "m-b", "m-c") // desired: m-c, m-b, m-a (m-c used last)
	var heldDuring string
	hook := r.rs.hook
	r.rs.hook = func() {
		if h, ok := r.o.Lock().Holder(); ok {
			heldDuring = h.Kind
		}
		hook()
	}
	res := r.restartOK()
	if r.rs.calls() != 1 {
		t.Errorf("llama was restarted %d times", r.rs.calls())
	}
	if heldDuring != HolderRestart {
		t.Errorf("the lock holder during the restart was %q", heldDuring)
	}
	// The router brought the default back by itself; the agent loads the rest, most recent first.
	eqStrings(t, "router calls", r.calls(), []string{"load m-c", "load m-b"})
	eqStrings(t, "restored", res.Restored, []string{"m-a", "m-c", "m-b"})
	eqStrings(t, "loaded", r.loadedNow(), []string{"m-a", "m-b", "m-c"})
	if len(res.Skipped) != 0 || res.OpID == "" {
		t.Errorf("result: %+v", res)
	}
	if _, held := r.o.Lock().Holder(); held {
		t.Error("the lock is still held")
	}
	if len(r.sup.started) != 1 || r.sup.started[0] != res.OpID || r.sup.anyActive() {
		t.Errorf("watcher suppression: %v", r.sup.started)
	}
	eqStrings(t, "desired_loaded", r.snap().Models.DesiredLoaded, []string{"m-c", "m-b", "m-a"})
	eqStrings(t, "events", r.evs(), []string{"loaded m-c", "loaded m-b"})
}

func TestRestartAndRestoreLoadsTheDefaultItselfWhenTheRouterDoesNot(t *testing.T) {
	r := newOrRig(t, orMax(2))
	r.rs.hook = func() { r.f.Restart() } // no load-on-startup flag
	r.load("m-b", "m-a")                 // desired: m-a, m-b
	res := r.restartOK()
	eqStrings(t, "router calls", r.calls(), []string{"load m-a", "load m-b"})
	eqStrings(t, "restored", res.Restored, []string{"m-a", "m-b"})
}

func TestRestartAndRestoreNeverEvicts(t *testing.T) {
	r := newOrRig(t) // models_max 1
	r.restarting()
	r.load("m-b")
	r.seed(func(s *state.State) { s.Models.DesiredLoaded = []string{"m-b", "m-c"} })
	res := r.restartOK()
	eqStrings(t, "restored", res.Restored, []string{"m-a"})
	eqStrings(t, "skipped", skipIDs(res.Skipped), []string{"m-b:no_room", "m-c:no_room"})
	if r.countCalls("unload") != 0 || r.countCalls("load") != 0 {
		t.Errorf("router calls: %v", r.calls())
	}
	// desired_loaded is not rewritten by a restore: a model that had no room is still wanted.
	eqStrings(t, "desired_loaded", r.snap().Models.DesiredLoaded, []string{"m-b", "m-c"})
}

func TestRestartAndRestoreWhenTheLockIsBusy(t *testing.T) {
	r := newOrRig(t)
	r.restarting()
	hold, err := r.o.Lock().TryAcquire(HolderLoad)
	if err != nil {
		t.Fatal(err)
	}
	defer hold.Release()
	_, err = r.o.RestartAndRestore(bg, sysActor)
	oe := opErrOf(t, err)
	if oe.Code != CodeBusy || oe.Status != http.StatusConflict || oe.Details["op_id"] != hold.OpID {
		t.Errorf("error: %+v", oe)
	}
	if r.rs.calls() != 0 {
		t.Error("llama was restarted although the lock was busy")
	}
}

func TestRestartAndRestoreWaitsForTheLockWhenAskedTo(t *testing.T) {
	r := newOrRig(t)
	r.restarting()
	hold, err := r.o.Lock().TryAcquire(HolderLoad)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := r.o.RestartWith(bg, RestartRequest{Actor: sysActor, LockWait: DefaultLockWait})
		done <- err
	}()
	hold.Release()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if r.rs.calls() != 1 {
		t.Errorf("restarts: %d", r.rs.calls())
	}
}

func TestRestartAndRestoreRestartFails(t *testing.T) {
	r := newOrRig(t)
	r.rs.err = errors.New("job failed")
	r.load("m-a")
	r.o.opt.Builds = buildStub{id: "b2"}
	_, err := r.o.RestartAndRestore(bg, sysActor)
	oe := opErrOf(t, err)
	if oe.Code != CodeLlamaDown || oe.Status != http.StatusInternalServerError || oe.Details["cause"] != "restart_failed" || oe.Details["op_id"] == "" {
		t.Errorf("error: %+v", oe)
	}
	if !errors.Is(err, r.rs.err) {
		t.Error("the cause is lost")
	}
	if r.snap().Llama.Build != "" {
		t.Errorf("the build was recorded after a failed restart: %q", r.snap().Llama.Build)
	}
	if _, held := r.o.Lock().Holder(); held {
		t.Error("the lock is still held")
	}
}

func TestRestartAndRestoreHealthNeverComes(t *testing.T) {
	r := newOrRig(t)
	r.rs.hook = func() { r.f.Down(true) }
	r.load("m-a")
	start := r.clk.Now()
	res, err := r.o.RestartAndRestore(bg, sysActor)
	oe := opErrOf(t, err)
	if oe.Code != CodeLlamaDown || oe.Details["cause"] != "health_timeout" || oe.Details["phase"] != "restart" {
		t.Errorf("error: %+v", oe)
	}
	if res.OpID == "" || oe.Details["op_id"] != res.OpID {
		t.Errorf("op ids: %q %v", res.OpID, oe.Details["op_id"])
	}
	want := r.o.opt.Timing.HealthWait
	if got := r.clk.Now().Sub(start); got < want || got > want+5*time.Second {
		t.Errorf("waited %v, want about %v", got, want)
	}
	if _, held := r.o.Lock().Holder(); held {
		t.Error("the lock is still held")
	}
}

func TestRestartAndRestoreDefaultThatDoesNotStartRaisesLLM013(t *testing.T) {
	r := newOrRig(t, orMax(2))
	r.rs.hook = func() { r.f.Restart() }
	r.f.RejectLoad("m-a", http.StatusInternalServerError)
	r.load("m-b", "m-a")
	res := r.restartOK()
	eqStrings(t, "restored", res.Restored, []string{"m-b"})
	if len(res.Skipped) != 1 || res.Skipped[0].ID != "m-a" || res.Skipped[0].Reason != kindRejected {
		t.Errorf("skipped: %+v", res.Skipped)
	}
	if !r.alerts.has("raise", CodeDefaultNotStarted, "") {
		t.Errorf("LAI-LLM-013 was not raised: %+v", r.alerts.events)
	}
	if r.alerts.has("raise", CodeNothingLoaded, "*") {
		t.Error("LAI-LLM-012 was raised although a model is loaded")
	}
}

func TestRestartAndRestoreDefaultStuckLoading(t *testing.T) {
	r := newOrRig(t, orMax(2))
	r.rs.hook = func() { r.f.Restart(); r.f.Set("m-a", fake.Loading) } // loads for ever
	r.load("m-a", "m-b")
	start := r.clk.Now()
	res := r.restartOK()
	if got := r.clk.Now().Sub(start); got < 300*time.Second {
		t.Errorf("gave up after %v, before the load timeout", got)
	}
	eqStrings(t, "restored", res.Restored, []string{"m-b"})
	if res.Skipped[0].Code != CodeLoadTimeout {
		t.Errorf("skipped: %+v", res.Skipped)
	}
	// The default that never finished is stopped, so that it does not hold memory for ever.
	if got := r.f.Status("m-a"); got != fake.Unloaded {
		t.Errorf("m-a is %s", got)
	}
}

func TestRestartAndRestoreNothingLoadsRaisesLLM012ButSucceeds(t *testing.T) {
	r := newOrRig(t)
	r.rs.hook = func() { r.f.Restart() }
	for _, id := range []string{"m-a", "m-b"} {
		r.f.RejectLoad(id, http.StatusInternalServerError)
	}
	r.load("m-b")
	res := r.restartOK()
	if len(res.Restored) != 0 {
		t.Errorf("restored %v", res.Restored)
	}
	if !r.alerts.has("raise", CodeNothingLoaded, "") {
		t.Errorf("LAI-LLM-012 was not raised: %+v", r.alerts.events)
	}
}

func TestRestartAndRestoreEmptySetRaisesNothing(t *testing.T) {
	r := newOrRig(t, func(c *orCfg) { c.profile.DefaultModel = ""; c.profile.Survival.DefaultModel = "" })
	r.rs.hook = func() { r.f.Restart() }
	res := r.restartOK()
	if len(res.Restored) != 0 || r.alerts.has("raise", CodeNothingLoaded, "*") {
		t.Errorf("result %+v alerts %+v", res, r.alerts.events)
	}
	if !r.alerts.has("raise", CodeNoDefault, "") {
		t.Error("LAI-LLM-014 was not raised")
	}
}

func TestRestartAndRestoreSurvivalListWhenNothingElseLoads(t *testing.T) {
	r := newOrRig(t, orMax(2), orSurvival())
	r.rs.hook = func() { r.f.Restart() }
	r.f.RejectLoad("m-a", http.StatusInternalServerError)
	res := r.restartOK()
	eqStrings(t, "router calls", r.calls(), []string{"load m-a", "load m-c", "load m-d"})
	eqStrings(t, "restored", res.Restored, []string{"m-c", "m-d"})
}

func TestRestartAndRestoreOutsideSurvivalIgnoresTheSurvivalList(t *testing.T) {
	r := newOrRig(t, orMax(2))
	r.rs.hook = func() { r.f.Restart() }
	r.f.RejectLoad("m-a", http.StatusInternalServerError)
	res := r.restartOK()
	if len(res.Restored) != 0 || r.countCalls("load m-c") != 0 {
		t.Errorf("restored %v calls %v", res.Restored, r.calls())
	}
}

func TestRestartAndRestoreRecordsTheBuild(t *testing.T) {
	tests := []struct {
		name      string
		before    string
		prev      *string
		src       BuildSource
		wantBuild string
		wantPrev  string
	}{
		{"switched", "b1-cuda", nil, buildStub{id: "b2-cuda"}, "b2-cuda", "b1-cuda"},
		{"first record", "", nil, buildStub{id: "b1-cuda"}, "b1-cuda", ""},
		{"unchanged", "b1-cuda", nil, buildStub{id: "b1-cuda"}, "b1-cuda", ""},
		{"unreadable keeps the record", "b1-cuda", nil, buildStub{err: errors.New("gone")}, "b1-cuda", ""},
		{"no source", "b1-cuda", nil, nil, "b1-cuda", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := newOrRig(t)
			r.rs.hook = func() { r.f.Restart() }
			r.seed(func(s *state.State) { s.Llama.Build = tc.before })
			r.o.opt.Builds = tc.src
			r.restartOK()
			l := r.snap().Llama
			prev := ""
			if l.PreviousBuild != nil {
				prev = *l.PreviousBuild
			}
			if l.Build != tc.wantBuild || prev != tc.wantPrev {
				t.Errorf("build %q previous %q, want %q and %q", l.Build, prev, tc.wantBuild, tc.wantPrev)
			}
		})
	}
}

func TestRestartAndRestoreSingleModeOnlyRestarts(t *testing.T) {
	r := newOrRig(t, func(c *orCfg) { c.llama.Mode = LlamaModeSingle })
	r.load("m-a")
	res := r.restartOK()
	if r.rs.calls() != 1 || len(res.Restored) != 0 || r.countCalls("load") != 0 {
		t.Errorf("restarts %d result %+v calls %v", r.rs.calls(), res, r.calls())
	}
}

func TestRestartAndRestoreRefusals(t *testing.T) {
	t.Run("no restarter", func(t *testing.T) {
		r := newOrRig(t, func(c *orCfg) { c.noRestarter = true })
		if _, err := r.o.RestartAndRestore(bg, sysActor); err == nil {
			t.Error("no error")
		}
	})
	t.Run("after shutdown", func(t *testing.T) {
		r := newOrRig(t)
		ctx, cancel := context.WithTimeout(bg, 5*time.Second)
		defer cancel()
		_ = r.o.Shutdown(ctx)
		if _, err := r.o.RestartAndRestore(bg, sysActor); !errors.Is(err, ErrInterrupted) {
			t.Errorf("error: %v", err)
		}
		if r.rs.calls() != 0 {
			t.Error("llama was restarted after shutdown")
		}
	})
}

func TestRestartAndRestoreCallerGivesUpButTheOperationFinishes(t *testing.T) {
	r := newOrRig(t)
	r.rs.hook = func() { r.f.Restart() }
	r.load("m-a")
	ctx, cancel := context.WithCancel(bg)
	entered, release := make(chan struct{}), make(chan struct{})
	inner := r.rs.hook
	r.rs.hook = func() {
		close(entered)
		<-release
		inner()
	}
	done := make(chan error, 1)
	go func() {
		_, err := r.o.RestartAndRestore(ctx, sysActor)
		done <- err
	}()
	<-entered
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("caller got %v", err)
	}
	if _, held := r.o.Lock().Holder(); !held {
		t.Error("the lock was given up with the caller: the restart is still running")
	}
	close(release)
	wctx, wcancel := context.WithTimeout(bg, 5*time.Second)
	defer wcancel()
	if err := r.o.Wait(wctx); err != nil {
		t.Fatal(err)
	}
	if _, held := r.o.Lock().Holder(); held {
		t.Error("the lock is still held")
	}
	eqStrings(t, "loaded", r.loadedNow(), []string{"m-a"})
}

// ---- the /services/llama hook ----

func TestLlamaHookRestart(t *testing.T) {
	r := newOrRig(t, orMax(2))
	r.restarting()
	r.load("m-a", "m-b")
	h := NewLlamaHook(r.o)
	called := false
	restored, err := h.Run(bg, "op_route", services.ActionRestart, func(context.Context) error {
		called = true
		if _, held := r.o.Lock().Holder(); !held {
			t.Error("the unit action ran without the lock")
		}
		r.f.Restart()
		return nil
	})
	if err != nil || !called {
		t.Fatalf("restored %v err %v called %v", restored, err, called)
	}
	eqStrings(t, "restored", restored, []string{"m-a", "m-b"})
	// The route's op id names the operation in the events and for the watcher.
	if len(r.sup.started) != 1 || r.sup.started[0] != "op_route" {
		t.Errorf("suppressed %v", r.sup.started)
	}
	if _, held := r.o.Lock().Holder(); held {
		t.Error("the lock is still held")
	}
}

func TestLlamaHookStartRestoresAndStopDoesNot(t *testing.T) {
	r := newOrRig(t)
	r.restarting()
	r.load("m-a")
	h := NewLlamaHook(r.o)
	restored, err := h.Run(bg, "op_1", services.ActionStop, func(context.Context) error { r.f.Down(true); return nil })
	if err != nil || len(restored) != 0 {
		t.Errorf("stop: %v %v", restored, err)
	}
	if r.sup.anyActive() {
		t.Error("watcher suppression leaked")
	}
	restored, err = h.Run(bg, "op_2", services.ActionStart, func(context.Context) error { r.f.Down(false); r.f.Restart(); return nil })
	if err != nil {
		t.Fatal(err)
	}
	eqStrings(t, "restored", restored, []string{"m-a"})
}

func TestLlamaHookBusyIsTheHandlersBusyError(t *testing.T) {
	r := newOrRig(t)
	hold, _ := r.o.Lock().TryAcquire(HolderUnload)
	defer hold.Release()
	called := false
	_, err := NewLlamaHook(r.o).Run(bg, "op_x", services.ActionRestart, func(context.Context) error { called = true; return nil })
	var busy *api.BusyError
	if !errors.As(err, &busy) || busy.OpID != hold.OpID {
		t.Fatalf("error: %v", err)
	}
	if called {
		t.Error("the unit was touched while the lock was busy")
	}
}

func TestLlamaHookReturnsTheUnitErrorAsItIs(t *testing.T) {
	r := newOrRig(t)
	want := &services.ActionError{Action: services.ActionRestart, Reason: services.ReasonJobFailed}
	restored, err := NewLlamaHook(r.o).Run(bg, "op_x", services.ActionRestart, func(context.Context) error { return want })
	if !errors.Is(err, want) || len(restored) != 0 {
		t.Errorf("restored %v err %v", restored, err)
	}
	if r.countCalls("load") != 0 {
		t.Error("models were restored after a failed unit action")
	}
	if _, held := r.o.Lock().Holder(); held {
		t.Error("the lock is still held")
	}
}

func TestLlamaHookHealthTimeoutIsAnAPIError(t *testing.T) {
	r := newOrRig(t)
	_, err := NewLlamaHook(r.o).Run(bg, "op_x", services.ActionRestart, func(context.Context) error { r.f.Down(true); return nil })
	var ae *api.Error
	if !errors.As(err, &ae) || ae.Code != string(CodeLlamaDown) || ae.Status != http.StatusInternalServerError {
		t.Fatalf("error: %#v", err)
	}
	if ae.Details["cause"] != "health_timeout" {
		t.Errorf("details: %v", ae.Details)
	}
}

type buildStub struct {
	id  string
	err error
}

func (b buildStub) CurrentBuild() (string, error) { return b.id, b.err }
