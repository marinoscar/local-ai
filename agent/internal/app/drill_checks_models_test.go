package app

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/audit"
	"github.com/marinoscar/local-ai/agent/internal/drill"
	"github.com/marinoscar/local-ai/agent/internal/router"
	routerfake "github.com/marinoscar/local-ai/agent/internal/router/fake"
	"github.com/marinoscar/local-ai/agent/internal/services"
	svcfake "github.com/marinoscar/local-ai/agent/internal/services/fake"
	"github.com/marinoscar/local-ai/agent/internal/state"
)

// ---- adapters ----------------------------------------------------------------------------------

type fakeOrch struct {
	load    router.LoadRequest
	loadRes router.LoadResult
	loadErr error
	unload  router.UnloadRequest
	unErr   error
	def     string
	defErr  error
}

func (f *fakeOrch) LoadWith(_ context.Context, req router.LoadRequest) (router.LoadResult, error) {
	f.load = req
	return f.loadRes, f.loadErr
}
func (f *fakeOrch) UnloadWith(_ context.Context, req router.UnloadRequest) (router.UnloadResult, error) {
	f.unload = req
	return router.UnloadResult{}, f.unErr
}
func (f *fakeOrch) SetDefault(_ context.Context, id string, _ audit.Actor) (router.DefaultResult, error) {
	f.def = id
	return router.DefaultResult{}, f.defErr
}

type fakeStatuses struct {
	list []router.ModelStatus
	err  error
}

func (f fakeStatuses) Models(context.Context) ([]router.ModelStatus, error) { return f.list, f.err }

type fakeStateView struct{ st state.State }

func (f fakeStateView) Snapshot() state.State { return f.st }

func status(id string, s router.Status) router.ModelStatus {
	return router.ModelStatus{ID: id, Status: s}
}

func TestDrillModelsResidentFollowsTheDesiredOrder(t *testing.T) {
	st := state.State{}
	st.Models.DesiredLoaded = []string{"c", "a"}
	m := drillModels{
		router: fakeStatuses{list: []router.ModelStatus{
			status("a", router.StatusLoaded), status("b", router.StatusLoaded), status("c", router.StatusSleeping),
			status("d", router.StatusUnloaded), status("e", router.StatusLoading), status("0", router.StatusLoaded),
		}},
		state: fakeStateView{st},
	}
	got, err := m.Resident(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// Desired first (c, a), then the rest by id; unloaded and loading models are not resident.
	if want := []string{"c", "a", "0", "b"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("resident = %v, want %v", got, want)
	}
	none, _ := drillModels{router: fakeStatuses{}, state: fakeStateView{}}.Resident(context.Background())
	if none == nil || len(none) != 0 {
		t.Fatalf("no model loaded is an empty, non-nil list: %#v", none)
	}
	if _, err := (drillModels{router: fakeStatuses{err: errors.New("down")}, state: fakeStateView{}}).Resident(context.Background()); err == nil {
		t.Fatal("a router that does not answer is an error")
	}
}

func TestDrillModelsDefault(t *testing.T) {
	d := "a"
	st := state.State{}
	if got := (drillModels{state: fakeStateView{st}}).Default(); got != "" {
		t.Fatalf("default = %q", got)
	}
	st.Models.Default = &d
	if got := (drillModels{state: fakeStateView{st}}).Default(); got != "a" {
		t.Fatalf("default = %q", got)
	}
}

func TestDrillModelsLoadUsesTheOrchestratorPath(t *testing.T) {
	o := &fakeOrch{loadRes: router.LoadResult{LoadMS: 4100}}
	m := drillModels{orch: o}
	info, err := m.Load(context.Background(), "a", true)
	if err != nil || info.LoadMS != 4100 || info.AlreadyLoaded {
		t.Fatalf("Load = %+v, %v", info, err)
	}
	if r := o.load; r.ID != "a" || !r.Wait || !r.UnloadOthers || r.LockWait != router.DefaultLockWait || r.Actor != audit.System() {
		t.Fatalf("request = %+v: a load waits, waits for the lock and acts as the agent itself", r)
	}
	if _, err := m.Load(context.Background(), "b", false); err != nil || o.load.UnloadOthers {
		t.Fatalf("a restore load must not evict: %+v %v", o.load, err)
	}
}

func TestDrillModelsLoadKeepsTheDoctorCode(t *testing.T) {
	o := &fakeOrch{loadErr: &router.OpError{Code: router.CodeNoRoom, Message: "no room", Details: map[string]any{}}}
	_, err := drillModels{orch: o}.Load(context.Background(), "a", false)
	var me *drill.ModelError
	if !errors.As(err, &me) || me.Code != string(router.CodeNoRoom) {
		t.Fatalf("err = %v, want a ModelError with the code", err)
	}
	plain := errors.New("context canceled")
	o.loadErr = plain
	if _, err := (drillModels{orch: o}).Load(context.Background(), "a", false); !errors.Is(err, plain) {
		t.Fatalf("a plain error stays itself: %v", err)
	}
}

func TestDrillModelsUnloadAndSetDefault(t *testing.T) {
	o := &fakeOrch{}
	m := drillModels{orch: o}
	if err := m.Unload(context.Background(), "a"); err != nil || o.unload.ID != "a" || o.unload.Actor != audit.System() {
		t.Fatalf("Unload: %+v, %v", o.unload, err)
	}
	if err := m.SetDefault(context.Background(), "b"); err != nil || o.def != "b" {
		t.Fatalf("SetDefault: %q, %v", o.def, err)
	}
	o.unErr, o.defErr = errors.New("x"), errors.New("y")
	if m.Unload(context.Background(), "a") == nil || m.SetDefault(context.Background(), "b") == nil {
		t.Fatal("errors must come back")
	}
}

func TestDrillWhisperRunning(t *testing.T) {
	f := svcfake.New()
	w := drillWhisper{m: f}
	for state, want := range map[string]bool{"active": true, "activating": true, "reloading": true, "inactive": false, "failed": false} {
		f.Set(services.UnitState{Unit: services.UnitWhisper, ActiveState: state})
		got, err := w.Running(context.Background())
		if err != nil || got != want {
			t.Errorf("ActiveState %s: running = %v, %v", state, got, err)
		}
	}
	if err := w.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// ---- the daemon ---------------------------------------------------------------------------------

// startDrill starts a drill, waiting while a model operation of the startup still holds llama busy.
func startDrill(t *testing.T, d *Daemon, req drill.StartRequest) drill.Document {
	t.Helper()
	eng := d.Components().Drill
	var res drill.StartResult
	var err error
	for i := 0; i < 100; i++ {
		if res, err = eng.Start(context.Background(), req); err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := eng.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	raw, err := eng.Get(res.RunID)
	if err != nil {
		t.Fatal(err)
	}
	var doc drill.Document
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

// The daemon wires the model checks and the restorer to the real orchestrator: a drill loads the other
// survival model through it and puts the node back as it was. (The fake llama-server answers every
// question with OK, so the answers themselves are wrong here; the wiring is what is under test.)
func TestDaemonDrillRunsModelChecksAndRestoresTheNode(t *testing.T) {
	r := newRig(t)
	d := r.start()
	r.waitStarted()
	r.llama.Set(modelAlpha, routerfake.Loaded)
	r.llama.Set(modelGamma, routerfake.Unloaded)

	doc := startDrill(t, d, drill.StartRequest{})
	got := map[string]drill.CheckResult{}
	for _, c := range doc.Checks {
		got[c.ID] = c
	}
	for _, id := range []string{"drl.llm.answer." + modelAlpha, "drl.llm.answer." + modelGamma, "drl.vlm.photo." + modelGamma, "drl.llm.fit", "drl.restore"} {
		if _, ok := got[id]; !ok {
			t.Errorf("check %s is not registered; checks: %+v", id, doc.Checks)
		}
	}
	if c := got["drl.llm.fit"]; c.Result != drill.ResultPass {
		t.Errorf("fit = %+v", c)
	}
	if c := got["drl.restore"]; c.Result != drill.ResultPass {
		t.Errorf("restore = %+v", c)
	}
	if doc.Restored == nil || !doc.Restored.OK || !reflect.DeepEqual(doc.Restored.LoadedModels, []string{modelAlpha}) {
		t.Fatalf("restored = %+v", doc.Restored)
	}
	if s := r.llama.Status(modelAlpha); s != routerfake.Loaded {
		t.Errorf("%s is %s after the drill, want loaded", modelAlpha, s)
	}
	if s := r.llama.Status(modelGamma); s != routerfake.Unloaded {
		t.Errorf("%s is %s after the drill, want unloaded", modelGamma, s)
	}
}

func TestDaemonDrillQuickTestsOnlyTheDefaultModel(t *testing.T) {
	r := newRig(t)
	d := r.start()
	r.waitStarted()
	r.llama.Set(modelAlpha, routerfake.Loaded)

	doc := startDrill(t, d, drill.StartRequest{Quick: true})
	for _, c := range doc.Checks {
		switch c.ID {
		case "drl.llm.answer." + modelAlpha:
			if c.Result == drill.ResultSkip {
				t.Errorf("the default must be tested: %+v", c)
			}
		case "drl.llm.answer." + modelGamma, "drl.vlm.photo." + modelGamma:
			if c.Result != drill.ResultSkip {
				t.Errorf("%s must be skipped by --quick: %+v", c.ID, c)
			}
		}
	}
	if doc.Restored == nil || !doc.Restored.OK {
		t.Fatalf("restored = %+v", doc.Restored)
	}
}
