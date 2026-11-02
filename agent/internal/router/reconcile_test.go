package router

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// llamaUnitStub is the unit of the reconcile tests.
type llamaUnitStub struct {
	mu       sync.Mutex
	state    LlamaUnitState
	stateErr error
	startErr error
	starts   int
}

func (u *llamaUnitStub) State(context.Context) (LlamaUnitState, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.state, u.stateErr
}

func (u *llamaUnitStub) Start(context.Context) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.starts++
	return u.startErr
}

func (u *llamaUnitStub) started() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.starts
}

// recWorld is a Reconciler over the real unit and preset generators on fake disks, a scripted
// router and a unit stub.
type recWorld struct {
	t    *testing.T
	w    *world
	uw   *unitWorld
	unit *llamaUnitStub
	uin  UnitInputs
	rec  *Reconciler
}

func newRecWorld(t *testing.T, mut ...func(*ReconcileOptions)) *recWorld {
	t.Helper()
	w := newWorld(t)
	w.router, w.ops = &scriptRouter{}, &opsLock{}
	w.build()
	rw := &recWorld{t: t, w: w, uw: newUnitWorld(t), unit: &llamaUnitStub{state: LlamaUnitState{Enabled: true}}, uin: routerIn(1, 512)}
	opt := ReconcileOptions{
		Units: rw.uw.gen, Models: w.gen, Router: w.router, Unit: rw.unit,
		Inputs: func() (Inputs, UnitInputs) { return w.inputs(), rw.uin },
	}
	for _, f := range mut {
		f(&opt)
	}
	rec, err := NewReconciler(opt)
	if err != nil {
		t.Fatal(err)
	}
	rw.rec = rec
	return rw
}

func (rw *recWorld) reconcile() ReconcileResult {
	rw.t.Helper()
	res, err := rw.rec.Reconcile(context.Background())
	if err != nil {
		rw.t.Fatalf("Reconcile: %v", err)
	}
	return res
}

func (rw *recWorld) routerDown(down bool) {
	rw.w.router.mu.Lock()
	defer rw.w.router.mu.Unlock()
	if down {
		rw.w.router.err = ErrDown
	} else {
		rw.w.router.err = nil
	}
}

func (rw *recWorld) routerCalls() []string { return rw.w.router.callList() }

func (rw *recWorld) resetCalls() {
	rw.w.router.mu.Lock()
	rw.w.router.calls = nil
	rw.w.router.mu.Unlock()
}

func TestReconcileFirstBootWritesFilesAndStartsADownRouter(t *testing.T) {
	rw := newRecWorld(t)
	rw.routerDown(true)
	res := rw.reconcile()
	if !res.Unit.Written || !res.Models.Written || !res.UnitRan || !res.ModelsRan {
		t.Errorf("result: %+v", res)
	}
	if !res.Started || rw.unit.started() != 1 || res.RouterUp {
		t.Errorf("started %v (%d), router up %v", res.Started, rw.unit.started(), res.RouterUp)
	}
	// The unit is written before the router is started, so it starts with the new files.
	if rw.uw.sys.n != 1 {
		t.Errorf("daemon-reload ran %d times", rw.uw.sys.n)
	}
	rw.uw.unitText()
	rw.w.iniText()
}

func TestReconcileUnitChangeRunsExactlyOneDaemonReload(t *testing.T) {
	rw := newRecWorld(t)
	rw.reconcile()
	rw.uw.sys.n = 0
	rw.uin = routerIn(2, 512) // models_max is in the unit's command line
	res := rw.reconcile()
	if !res.Unit.Written || !res.Unit.Reloaded || rw.uw.sys.n != 1 {
		t.Errorf("unit written %v reloaded %v, daemon-reloads %d", res.Unit.Written, res.Unit.Reloaded, rw.uw.sys.n)
	}
	if res.Models.Written || res.Models.Reloaded {
		t.Errorf("the model list changed too: %+v", res.Models)
	}
}

func TestReconcileNothingChangedDoesNothing(t *testing.T) {
	rw := newRecWorld(t)
	rw.reconcile()
	rw.uw.sys.n = 0
	rw.resetCalls()
	res := rw.reconcile()
	if res.Unit.Written || res.Models.Written || res.Started || !res.RouterUp {
		t.Errorf("result: %+v", res)
	}
	if rw.uw.sys.n != 0 || rw.unit.started() != 0 || len(rw.routerCalls()) != 0 {
		t.Errorf("daemon-reloads %d, starts %d, router calls %v", rw.uw.sys.n, rw.unit.started(), rw.routerCalls())
	}
}

func TestReconcileIniChangeWithTheRouterUpTellsItToReload(t *testing.T) {
	rw := newRecWorld(t)
	_ = rw.w.fs.Remove(modelDir(rootInternal, "small-vlm-q4km") + "/MANIFEST.json") // not on disk yet
	rw.reconcile()
	rw.w.router.set(st("qwen3.5-9b-q4km", StatusLoaded))
	installModel(rw.w.fs, rootInternal, rw.w.models[1])
	rw.resetCalls()
	rw.uw.sys.n = 0
	res := rw.reconcile()
	if !res.Models.Written || !res.Models.Reloaded {
		t.Fatalf("models: %+v", res.Models)
	}
	equalStrings(t, rw.routerCalls(), []string{"reload"})
	if rw.uw.sys.n != 0 || rw.unit.started() != 0 {
		t.Errorf("daemon-reloads %d, starts %d", rw.uw.sys.n, rw.unit.started())
	}
}

func TestReconcileIniChangeWithTheRouterDownIsLeftForTheStart(t *testing.T) {
	rw := newRecWorld(t)
	rw.reconcile()
	rw.routerDown(true)
	rw.resetCalls()
	_ = rw.w.fs.Remove(modelDir(rootInternal, "small-vlm-q4km") + "/MANIFEST.json")
	res := rw.reconcile()
	if !res.Models.Written || res.Models.Reloaded || !res.Started {
		t.Errorf("result: %+v", res)
	}
	for _, c := range rw.routerCalls() {
		if c == "reload" {
			t.Error("a reload was sent to a router that is down")
		}
	}
}

func TestReconcileStartDecisions(t *testing.T) {
	tests := []struct {
		name      string
		down      bool
		state     LlamaUnitState
		stateErr  error
		startErr  error
		wantStart int
		wantErr   bool
	}{
		{"router up", false, LlamaUnitState{Enabled: true}, nil, nil, 0, false},
		{"down, enabled and stopped", true, LlamaUnitState{Enabled: true}, nil, nil, 1, false},
		{"down, enabled and already starting", true, LlamaUnitState{Enabled: true, Running: true}, nil, nil, 0, false},
		{"down and not enabled", true, LlamaUnitState{}, nil, nil, 0, false},
		{"unit state unreadable", true, LlamaUnitState{}, errors.New("no bus"), nil, 0, true},
		{"start fails", true, LlamaUnitState{Enabled: true}, nil, errors.New("job failed"), 1, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rw := newRecWorld(t)
			rw.unit.state, rw.unit.stateErr, rw.unit.startErr = tc.state, tc.stateErr, tc.startErr
			rw.routerDown(tc.down)
			res, err := rw.rec.Reconcile(context.Background())
			if (err != nil) != tc.wantErr {
				t.Errorf("error: %v", err)
			}
			if rw.unit.started() != tc.wantStart {
				t.Errorf("starts: %d, want %d", rw.unit.started(), tc.wantStart)
			}
			if res.Started != (tc.wantStart == 1 && tc.startErr == nil) {
				t.Errorf("Started = %v", res.Started)
			}
			if tc.startErr != nil && !errors.Is(err, tc.startErr) {
				t.Errorf("the start error is lost: %v", err)
			}
		})
	}
}

func TestReconcileGoesOnAfterAFailedStep(t *testing.T) {
	rw := newRecWorld(t)
	rw.routerDown(true)
	rw.uin = UnitInputs{Binary: "relative/llama-server", Router: routerBlock(1, 512)} // cannot be a unit
	res, err := rw.rec.Reconcile(context.Background())
	if err == nil || res.UnitErr == nil {
		t.Fatalf("no unit error: %v", err)
	}
	if !res.Models.Written || !res.Started {
		t.Errorf("the model list and the start did not run: %+v", res)
	}
}

func TestReconcileSingleModeLeavesTheModelListAlone(t *testing.T) {
	rw := newRecWorld(t, func(o *ReconcileOptions) { o.Units = nil })
	rw.uin = UnitInputs{Mode: LlamaModeSingle}
	rw.routerDown(true)
	res := rw.reconcile()
	if res.ModelsRan {
		t.Error("models.ini was generated in single mode")
	}
	if rw.w.fs.has(DefaultPaths("").INI) {
		t.Error("models.ini was written in single mode")
	}
	if !res.Started {
		t.Error("a down single-mode unit was not started")
	}
}

func TestReconcileBudget(t *testing.T) {
	blocked := blockingUnits{}
	rw := newRecWorld(t, func(o *ReconcileOptions) {
		o.Units = blocked
		o.Budget = 20 * time.Millisecond
	})
	rw.routerDown(true)
	res, err := rw.rec.Reconcile(context.Background())
	if !errors.Is(err, ErrReconcileBudget) || !res.TimedOut {
		t.Fatalf("error %v, timed out %v", err, res.TimedOut)
	}
	if res.ModelsRan || res.Started || rw.unit.started() != 0 {
		t.Errorf("steps ran after the budget: %+v", res)
	}
}

func TestReconcileCancelledCallerIsNotATimeout(t *testing.T) {
	rw := newRecWorld(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res, err := rw.rec.Reconcile(ctx)
	if !errors.Is(err, context.Canceled) || res.TimedOut || errors.Is(err, ErrReconcileBudget) {
		t.Errorf("error %v, timed out %v", err, res.TimedOut)
	}
}

func TestReconcileWithNothingConfiguredIsANoOp(t *testing.T) {
	rec, err := NewReconciler(ReconcileOptions{Inputs: func() (Inputs, UnitInputs) { return Inputs{}, UnitInputs{} }})
	if err != nil {
		t.Fatal(err)
	}
	res, err := rec.Reconcile(context.Background())
	if err != nil || res.UnitRan || res.ModelsRan || res.Started {
		t.Errorf("result %+v err %v", res, err)
	}
}

func TestNewReconcilerValidation(t *testing.T) {
	if _, err := NewReconciler(ReconcileOptions{}); err == nil {
		t.Error("a reconciler without Inputs was accepted")
	}
}

// blockingUnits is a UnitRegenerator that returns when its context ends.
type blockingUnits struct{}

func (blockingUnits) Generate(ctx context.Context, _ UnitInputs) (UnitResult, error) {
	<-ctx.Done()
	return UnitResult{}, ctx.Err()
}
