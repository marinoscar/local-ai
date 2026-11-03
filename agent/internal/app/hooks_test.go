package app

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/api"
	"github.com/marinoscar/local-ai/agent/internal/cloud"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/modelsapi"
)

func noopHook(context.Context, *Components) error { return nil }

func TestHookRegistrationRules(t *testing.T) {
	type reg func(h *Hooks, name string) error
	kinds := map[string]reg{
		"update":     func(h *Hooks, n string) error { return h.RegisterUpdateHook(n, noopHook) },
		"post-start": func(h *Hooks, n string) error { return h.RegisterPostStart(n, noopHook) },
		"worker": func(h *Hooks, n string) error {
			return h.RegisterWorker(n, func(*Components) (Worker, error) { return nil, nil })
		},
		"routes": func(h *Hooks, n string) error {
			return h.RegisterRoutes(n, func(*api.Registry, *Components) error { return nil })
		},
	}
	for kind, add := range kinds {
		t.Run(kind, func(t *testing.T) {
			h := NewHooks()
			if err := add(h, "one"); err != nil {
				t.Fatalf("first registration: %v", err)
			}
			if err := add(h, "one"); err == nil {
				t.Error("a second hook of the same name must be refused")
			}
			if err := add(h, ""); err == nil {
				t.Error("a hook without a name must be refused")
			}
			if err := add(h, "two"); err != nil {
				t.Errorf("another name: %v", err)
			}
		})
	}
	// The same name in two different kinds is fine: the kinds are separate lists.
	h := NewHooks()
	if err := h.RegisterUpdateHook("x", noopHook); err != nil {
		t.Fatal(err)
	}
	if err := h.RegisterPostStart("x", noopHook); err != nil {
		t.Errorf("a post-start hook named like an update hook: %v", err)
	}
	// Nil functions.
	if err := h.RegisterUpdateHook("nil", nil); err == nil {
		t.Error("a nil update hook")
	}
	if err := h.RegisterPostStart("nil", nil); err == nil {
		t.Error("a nil post-start hook")
	}
	if err := h.RegisterWorker("nil", nil); err == nil {
		t.Error("a nil worker factory")
	}
	if err := h.RegisterRoutes("nil", nil); err == nil {
		t.Error("a nil route hook")
	}
	// The singletons.
	if err := h.UseClaimPlane(func(*Components) ClaimPlane { return ClaimPlane{} }); err != nil {
		t.Fatal(err)
	}
	if err := h.UseClaimPlane(func(*Components) ClaimPlane { return ClaimPlane{} }); err == nil {
		t.Error("a second claim plane")
	}
	if err := h.UseAgentUpdate(func(*Components) AgentUpdate { return AgentUpdate{} }); err != nil {
		t.Fatal(err)
	}
	if err := h.UseAgentUpdate(func(*Components) AgentUpdate { return AgentUpdate{} }); err == nil {
		t.Error("a second agent updater")
	}
	if err := h.UseSyncEngine(func(*Components) modelsapi.SyncEngine { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := h.UseSyncEngine(func(*Components) modelsapi.SyncEngine { return nil }); err == nil {
		t.Error("a second sync engine")
	}
	if err := h.UseSnapshotSources(func(*Components) SnapshotSources { return SnapshotSources{} }); err != nil {
		t.Fatal(err)
	}
	if err := h.UseSnapshotSources(func(*Components) SnapshotSources { return SnapshotSources{} }); err == nil {
		t.Error("a second snapshot sources factory")
	}
}

// The package-level functions add to DefaultHooks and panic on a mistake, like http.Handle.
func TestPackageLevelRegistrationUsesDefaultHooks(t *testing.T) {
	saved := DefaultHooks
	DefaultHooks = NewHooks()
	t.Cleanup(func() { DefaultHooks = saved })

	RegisterUpdateHook("u", noopHook)
	RegisterPostStart("p", noopHook)
	RegisterWorker("w", func(*Components) (Worker, error) { return nil, nil })
	RegisterRoutes("r", func(*api.Registry, *Components) error { return nil })
	UseClaimPlane(func(*Components) ClaimPlane { return ClaimPlane{} })
	UseAgentUpdate(func(*Components) AgentUpdate { return AgentUpdate{} })
	UseSyncEngine(func(*Components) modelsapi.SyncEngine { return nil })
	UseSnapshotSources(func(*Components) SnapshotSources { return SnapshotSources{} })
	set := DefaultHooks.freeze()
	if len(set.update) != 1 || len(set.postStart) != 1 || len(set.workers) != 1 || len(set.routes) != 1 ||
		set.claim == nil || set.agentUpd == nil || set.sync == nil || set.snapshot == nil {
		t.Errorf("the registrations did not reach DefaultHooks: %+v", set)
	}
	mustPanic := func(name string, f func()) {
		t.Helper()
		defer func() {
			if recover() == nil {
				t.Errorf("%s: a duplicate registration must panic", name)
			}
		}()
		f()
	}
	mustPanic("update", func() { RegisterUpdateHook("u", noopHook) })
	mustPanic("post-start", func() { RegisterPostStart("p", noopHook) })
	mustPanic("claim plane", func() { UseClaimPlane(func(*Components) ClaimPlane { return ClaimPlane{} }) })

	// A frozen set is a copy: later registrations do not change a running daemon.
	RegisterPostStart("late", noopHook)
	if len(set.postStart) != 1 {
		t.Error("freeze must copy")
	}
	var nilHooks *Hooks
	if got := nilHooks.freeze(); len(got.update) != 0 {
		t.Error("a nil registry freezes to nothing")
	}
}

// recordingWorker is a Worker the hooks add; Start and Stop are recorded in a shared order.
type recordingWorker struct {
	name string
	mu   *sync.Mutex
	log  *[]string
}

func (w recordingWorker) Name() string { return w.name }
func (w recordingWorker) Start(context.Context) error {
	w.mu.Lock()
	*w.log = append(*w.log, "start "+w.name)
	w.mu.Unlock()
	return nil
}
func (w recordingWorker) Stop(context.Context) error {
	w.mu.Lock()
	*w.log = append(*w.log, "stop "+w.name)
	w.mu.Unlock()
	return nil
}

// recordingSync is a sync engine that remembers the downloads it was asked for.
type recordingSync struct {
	mu   sync.Mutex
	runs []string
}

func (s *recordingSync) StartModelDownload(_ context.Context, req modelsapi.SyncDownload) (modelsapi.SyncRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.runs = append(s.runs, req.Model.ID)
	return modelsapi.SyncRun{ID: "run-test-0001"}, nil
}

func (s *recordingSync) DeleteModel(context.Context, modelsapi.SyncDelete) (modelsapi.SyncDeleteResult, error) {
	return modelsapi.SyncDeleteResult{}, nil
}

// Every extension point does its job at its step, with the components built, and a later PR plugs in
// with one registration. This is the contract #36, #37, #69 and the sync epic rely on.
func TestExtensionPointsRunAtTheirStep(t *testing.T) {
	var mu sync.Mutex
	var log []string
	note := func(s string) { mu.Lock(); log = append(log, s); mu.Unlock() }
	var seenAtUpdate, seenAtPost, seenAtClaim *Components
	engine := &recordingSync{}

	r := newRig(t)
	h := r.hooks
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(h.RegisterUpdateHook("agent.update", func(_ context.Context, c *Components) error {
		seenAtUpdate = c
		note("update")
		return nil
	}))
	must(h.RegisterPostStart("sync.timer", func(_ context.Context, c *Components) error {
		seenAtPost = c
		note("post-start")
		return nil
	}))
	must(h.RegisterWorker("w1", func(*Components) (Worker, error) { return recordingWorker{"w1", &mu, &log}, nil }))
	must(h.RegisterWorker("w2", func(*Components) (Worker, error) { return recordingWorker{"w2", &mu, &log}, nil }))
	must(h.RegisterWorker("none", func(*Components) (Worker, error) { return nil, nil })) // nothing to run on this node
	must(h.RegisterWorker("broken", func(*Components) (Worker, error) { return nil, errors.New("cannot build") }))
	must(h.RegisterRoutes("drill.alerts", func(reg *api.Registry, c *Components) error {
		return reg.Add(api.Route{Method: http.MethodGet, Pattern: "/ext/ping", Auth: []api.Level{api.AuthNone},
			Handler: api.HandlerFunc(func(w http.ResponseWriter, r *http.Request) error {
				api.WriteJSON(w, r, 200, map[string]any{"node": c.Config.Get().Node.ID})
				return nil
			})})
	}))
	must(h.RegisterRoutes("broken", func(*api.Registry, *Components) error { return errors.New("no routes") }))
	must(h.UseClaimPlane(func(c *Components) ClaimPlane {
		seenAtClaim = c
		return ClaimPlane{Inflight: func() (int, map[string]any) { return 2, map[string]any{"job_id": "job-1", "type": "model.load"} }}
	}))
	must(h.UseSyncEngine(func(*Components) modelsapi.SyncEngine { return engine }))
	snapshotAsked := false
	must(h.UseSnapshotSources(func(*Components) SnapshotSources { snapshotAsked = true; return SnapshotSources{} }))

	d := r.start()
	r.waitStarted()

	// Step 15 and 16 hooks ran, with the daemon's components.
	for _, want := range []string{"update", "post-start", "start w1", "start w2"} {
		if !contains(log, want, &mu) {
			t.Errorf("%q did not happen: %v", want, log)
		}
	}
	for name, c := range map[string]*Components{"update": seenAtUpdate, "post-start": seenAtPost, "claim": seenAtClaim} {
		if c != d.Components() || c.State == nil || c.Bus == nil || c.Audit == nil || c.Cloud == nil && name != "claim" {
			t.Errorf("the %s hook did not get the built components: %+v", name, c)
		}
	}
	if seenAtClaim.Config == nil || seenAtClaim.State == nil {
		t.Error("the claim plane factory must see the foundations")
	}
	if !snapshotAsked {
		t.Error("the snapshot sources factory was not asked")
	}
	// A failing worker factory and a failing route hook degrade; the agent started.
	if !r.logs.has("a worker could not be built", map[string]any{"worker": "broken"}) {
		t.Errorf("the failed worker factory is not logged:\n%s", r.logText())
	}
	if !r.logs.has("a route hook failed", map[string]any{"hook": "broken"}) {
		t.Errorf("the failed route hook is not logged:\n%s", r.logText())
	}
	// The route hook's route is served.
	if got := r.doAuth("GET", "/ext/ping", nil, false); got.status != 200 || got.json()["node"] != rigNode {
		t.Errorf("GET /ext/ping = %d %s", got.status, got.body)
	}
	// The claim plane's in-flight record reaches GET /cloud.
	cl := r.do("GET", "/cloud", nil)
	infl, _ := cl.json()["inflight"].(map[string]any)
	if cl.status != 200 || infl["job_id"] != "job-1" {
		t.Errorf("GET /cloud = %d %s, want the in-flight record of the claim plane", cl.status, cl.body)
	}
	if n, _ := d.claim.Inflight(); n != 2 {
		t.Errorf("slots used = %d, want 2", n)
	}
	// The sync engine behind the model routes is the registered one, not the stub.
	dl := r.do("POST", "/models/"+modelGamma+"/download", map[string]any{})
	if dl.status != http.StatusAccepted || dl.json()["sync_run_id"] != "run-test-0001" {
		t.Errorf("POST /models/%s/download = %d %s", modelGamma, dl.status, dl.body)
	}
	if len(engine.runs) != 1 || engine.runs[0] != modelGamma {
		t.Errorf("the registered engine saw %v", engine.runs)
	}

	// Shutdown stops the registered workers in reverse order.
	d.Shutdown()
	mu.Lock()
	defer mu.Unlock()
	i1, i2 := indexOf(log, "stop w1"), indexOf(log, "stop w2")
	if i1 < 0 || i2 < 0 || i2 > i1 {
		t.Errorf("workers must stop in reverse registration order: %v", log)
	}
}

func contains(log []string, s string, mu *sync.Mutex) bool {
	mu.Lock()
	defer mu.Unlock()
	return indexOf(log, s) >= 0
}

func indexOf(log []string, s string) int {
	for i, x := range log {
		if x == s {
			return i
		}
	}
	return -1
}

// A daemon whose sync stack could not be built (here: /var/lib/localai/sync is a file, so the journal
// cannot open) degrades to the documented stubs: the model routes refuse a download with LAI-ARK-001
// (no writable content root), the ark sync routes are not served, an alert says why, and the agent
// still starts. With no claim plane registered the claim loop does nothing.
func TestStubsAnswerWhenTheSyncStackIsMissing(t *testing.T) {
	r := newRig(t, func(r *rig) { r.write("var/lib/localai/sync", "not a directory", 0o644) })
	r.start()
	r.waitStarted()
	if r.d.ark != nil || r.d.Components().SyncEngine != nil {
		t.Fatal("the sync stack was built over a journal directory that is a file")
	}
	if !hasAlert(r.d, doctor.AGT099) {
		t.Errorf("no alert says the sync stack is off:\n%s", r.logText())
	}
	dl := r.do("POST", "/models/"+modelGamma+"/download", map[string]any{})
	if dl.status != http.StatusUnprocessableEntity || errCode(dl) != doctor.ARK001 {
		t.Errorf("POST /models/%s/download without a sync engine = %d %s, want 422 %s", modelGamma, dl.status, dl.body, doctor.ARK001)
	}
	if got := r.do("GET", "/ark/sync", nil); got.status != http.StatusNotFound {
		t.Errorf("GET /ark/sync without the stack = %d, want 404 (not served)", got.status)
	}
	if n, info := r.d.claim.Inflight(); n != 0 || info != nil {
		t.Errorf("no claim plane: in flight %d %v", n, info)
	}
	r.d.claim.Start(context.Background()) // all no-ops
	if err := r.d.claim.Stop(context.Background()); err != nil {
		t.Error(err)
	}
	r.d.claim.Probe(context.Background())
	if _, err := (noSyncEngine{}).DeleteModel(context.Background(), modelsapi.SyncDelete{}); !errors.Is(err, modelsapi.ErrNoWritableRoot) {
		t.Errorf("DeleteModel without an engine = %v, want ErrNoWritableRoot (never a plain remove)", err)
	}
}

// A failing update hook or post-start hook is logged and startup still reaches step 17.
func TestFailingHooksDoNotAbortStartup(t *testing.T) {
	r := newRig(t)
	if err := r.hooks.RegisterUpdateHook("update.broken", func(context.Context, *Components) error { return errors.New("health check failed") }); err != nil {
		t.Fatal(err)
	}
	if err := r.hooks.RegisterPostStart("post.broken", func(context.Context, *Components) error { return errors.New("no timer") }); err != nil {
		t.Fatal(err)
	}
	r.start()
	r.waitStarted()
	if !r.logs.has("an update hook failed", map[string]any{"hook": "update.broken"}) || !r.logs.has("a post-start hook failed", map[string]any{"hook": "post.broken"}) {
		t.Errorf("the hook failures are not logged:\n%s", r.logText())
	}
	var failed int
	for _, s := range r.d.Steps() {
		if (s.N == 15 || s.N == 16) && s.Err != nil {
			failed++
		}
	}
	if failed != 2 {
		t.Errorf("steps 15 and 16 should record their hook errors: %+v", r.d.Steps())
	}
}

// A claim plane's loop is the one the link starts: the slot forwards Start, Stop and Probe.
func TestClaimSlotForwards(t *testing.T) {
	loop := &recordingLoop{}
	s := &claimSlot{}
	s.set(ClaimPlane{Loop: loop})
	s.Start(context.Background())
	s.Probe(context.Background())
	if err := s.Stop(context.Background()); err != nil {
		t.Error(err)
	}
	if loop.calls != "start,probe,stop" {
		t.Errorf("calls = %q", loop.calls)
	}
	var _ cloud.ClaimLoop = s
	// A loop without Probe is fine.
	s2 := &claimSlot{}
	s2.set(ClaimPlane{Loop: noProbeLoop{}})
	s2.Probe(context.Background())
	time.Sleep(0)
}

type recordingLoop struct{ calls string }

func (l *recordingLoop) add(s string) {
	if l.calls != "" {
		l.calls += ","
	}
	l.calls += s
}
func (l *recordingLoop) Start(context.Context)      { l.add("start") }
func (l *recordingLoop) Stop(context.Context) error { l.add("stop"); return nil }
func (l *recordingLoop) Probe(context.Context)      { l.add("probe") }

type noProbeLoop struct{}

func (noProbeLoop) Start(context.Context)      {}
func (noProbeLoop) Stop(context.Context) error { return nil }
