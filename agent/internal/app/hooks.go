package app

import (
	"context"
	"fmt"
	"sync"

	"github.com/marinoscar/local-ai/agent/internal/api"
	"github.com/marinoscar/local-ai/agent/internal/cloud"
	"github.com/marinoscar/local-ai/agent/internal/cloud/jobs"
	"github.com/marinoscar/local-ai/agent/internal/modelsapi"
)

// The extension points of the daemon (issue AGENT-20). Slices that are not merged yet plug into the
// startup with one registration each, from an init function of the package that owns them or from the
// cmd that builds the binary, and never by editing start.go:
//
//	app.RegisterUpdateHook("agent.update", fn)      step 15 (self-update health, #37)
//	app.RegisterPostStart("sync.timer", fn)         step 16 (sync timer, drill alerts, ...)
//	app.RegisterWorker("sync.drives", build)        a Worker started at step 16, stopped in shutdown step 2
//	app.RegisterRoutes("drill.alerts", add)         routes added before the listeners start (#69)
//	app.UseClaimPlane(build)                        the cloud command plane (#36)
//	app.UseSyncEngine(build)                        the ark sync engine behind the model routes
//	app.UseSnapshotSources(build)                   the ark, sync and drill sources of the cloud snapshot
//
// Each takes the *Components the daemon has built so far, so a hook reaches the state store, the event
// bus, the audit log, the orchestrator, the cloud link and every other subsystem without a global. A
// hook runs in the phase its name says, in registration order; a hook that fails is logged and the
// startup goes on (a degraded step never aborts the agent, agent.md section 4.1).

// HookFunc is a startup hook.
type HookFunc func(ctx context.Context, c *Components) error

// WorkerFactory builds a Worker from the components. It runs at step 16; a nil Worker with a nil error
// means "nothing to run on this node".
type WorkerFactory func(c *Components) (Worker, error)

// RouteHook adds routes to the registry the API server is built from.
type RouteHook func(reg *api.Registry, c *Components) error

// ClaimPlane is what the cloud command plane (#36) gives the link: the loop the link starts when it
// becomes linked, and the count of commands in flight for the vitals and GET /cloud.
type ClaimPlane struct {
	Loop     cloud.ClaimLoop
	Inflight func() (count int, info map[string]any)
}

// AgentUpdate is what the self-update (AGENT-19) gives the command plane: the updater behind the
// agent.update job and, optionally, the function that takes over an agent.update entry of
// inflight.json that an earlier process left behind (it settles it after the post-update health
// check).
type AgentUpdate struct {
	Updater jobs.AgentUpdater
	Recover func(ctx context.Context, e cloud.InflightEntry) bool
}

// SnapshotSources are the optional sources of the cloud state snapshot besides the status builder.
// The ark and sync slices implement ArkSource and SyncSource; the drill slice implements DrillSource.
type SnapshotSources struct {
	Ark   cloud.ArkSource
	Sync  cloud.SyncSource
	Drill cloud.DrillSource
}

type namedHook struct {
	name string
	fn   HookFunc
}

type namedWorker struct {
	name  string
	build WorkerFactory
}

type namedRoutes struct {
	name string
	add  RouteHook
}

// Hooks is a registry of the extension points. The daemon uses Env.Hooks, or DefaultHooks when that
// is nil; a test builds its own with NewHooks, so registrations never leak between tests.
type Hooks struct {
	mu        sync.Mutex
	update    []namedHook
	postStart []namedHook
	workers   []namedWorker
	routes    []namedRoutes
	claim     func(c *Components) ClaimPlane
	agentUpd  func(c *Components) AgentUpdate
	sync      func(c *Components) modelsapi.SyncEngine
	snapshot  func(c *Components) SnapshotSources
}

// NewHooks returns an empty registry.
func NewHooks() *Hooks { return &Hooks{} }

// DefaultHooks is the registry the package-level Register functions add to, like http.DefaultServeMux.
var DefaultHooks = NewHooks()

func dup(kind, name string, have func(string) bool) error {
	if name == "" {
		return fmt.Errorf("app: a %s hook needs a name", kind)
	}
	if have(name) {
		return fmt.Errorf("app: %s hook %q is registered twice", kind, name)
	}
	return nil
}

// RegisterUpdateHook adds a step 15 hook: "pending self-update? run the post-update health check"
// (agent.md section 4.1 step 15, #37). It runs under the step's 60 s budget.
func (h *Hooks) RegisterUpdateHook(name string, fn HookFunc) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := dup("update", name, func(n string) bool {
		for _, x := range h.update {
			if x.name == n {
				return true
			}
		}
		return false
	}); err != nil {
		return err
	}
	if fn == nil {
		return fmt.Errorf("app: update hook %q has no function", name)
	}
	h.update = append(h.update, namedHook{name, fn})
	return nil
}

// RegisterPostStart adds a step 16 hook (the sync timer, the drill hooks, ...). The hooks run after
// the doctor, the prober and the registered workers are up.
func (h *Hooks) RegisterPostStart(name string, fn HookFunc) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := dup("post-start", name, func(n string) bool {
		for _, x := range h.postStart {
			if x.name == n {
				return true
			}
		}
		return false
	}); err != nil {
		return err
	}
	if fn == nil {
		return fmt.Errorf("app: post-start hook %q has no function", name)
	}
	h.postStart = append(h.postStart, namedHook{name, fn})
	return nil
}

// RegisterWorker adds a worker started at step 16 and stopped in reverse registration order at
// shutdown step 2 ("stop periodic workers").
func (h *Hooks) RegisterWorker(name string, build WorkerFactory) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := dup("worker", name, func(n string) bool {
		for _, x := range h.workers {
			if x.name == n {
				return true
			}
		}
		return false
	}); err != nil {
		return err
	}
	if build == nil {
		return fmt.Errorf("app: worker %q has no factory", name)
	}
	h.workers = append(h.workers, namedWorker{name, build})
	return nil
}

// RegisterRoutes adds a route hook, run while the registry is built (before step 10).
func (h *Hooks) RegisterRoutes(name string, add RouteHook) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := dup("routes", name, func(n string) bool {
		for _, x := range h.routes {
			if x.name == n {
				return true
			}
		}
		return false
	}); err != nil {
		return err
	}
	if add == nil {
		return fmt.Errorf("app: route hook %q has no function", name)
	}
	h.routes = append(h.routes, namedRoutes{name, add})
	return nil
}

// UseAgentUpdate sets the self-update the command plane's agent.update job runs (AGENT-19). Without
// it the job is refused with LAI-AGT-023. It runs once, when the claim plane is built; only one may be
// set.
func (h *Hooks) UseAgentUpdate(build func(c *Components) AgentUpdate) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.agentUpd != nil {
		return fmt.Errorf("app: the agent updater is already set")
	}
	h.agentUpd = build
	return nil
}

// UseClaimPlane replaces the daemon's own cloud command plane (the claim loop over the executors of
// internal/cloud/jobs) with another one. It runs once, when the plane is built; only one may be set.
func (h *Hooks) UseClaimPlane(build func(c *Components) ClaimPlane) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.claim != nil {
		return fmt.Errorf("app: the claim plane is already set")
	}
	h.claim = build
	return nil
}

// UseSyncEngine sets the ark sync engine factory. Until one is set the model routes use a stub that
// answers LAI-ARK-001 (no writable content root) and deletes nothing.
func (h *Hooks) UseSyncEngine(build func(c *Components) modelsapi.SyncEngine) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.sync != nil {
		return fmt.Errorf("app: the sync engine is already set")
	}
	h.sync = build
	return nil
}

// UseSnapshotSources sets the factory of the cloud snapshot's ark, sync and drill sources.
func (h *Hooks) UseSnapshotSources(build func(c *Components) SnapshotSources) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.snapshot != nil {
		return fmt.Errorf("app: the snapshot sources are already set")
	}
	h.snapshot = build
	return nil
}

// hookSet is an immutable copy of a registry taken when the daemon starts.
type hookSet struct {
	update, postStart []namedHook
	workers           []namedWorker
	routes            []namedRoutes
	claim             func(c *Components) ClaimPlane
	agentUpd          func(c *Components) AgentUpdate
	sync              func(c *Components) modelsapi.SyncEngine
	snapshot          func(c *Components) SnapshotSources
}

func (h *Hooks) freeze() hookSet {
	if h == nil {
		return hookSet{}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return hookSet{
		update:    append([]namedHook(nil), h.update...),
		postStart: append([]namedHook(nil), h.postStart...),
		workers:   append([]namedWorker(nil), h.workers...),
		routes:    append([]namedRoutes(nil), h.routes...),
		claim:     h.claim, agentUpd: h.agentUpd, sync: h.sync, snapshot: h.snapshot,
	}
}

// The package-level functions add to DefaultHooks and panic on a registration error (a duplicate name
// or a nil function is a programming error found at start, like http.Handle).

func must(err error) {
	if err != nil {
		panic(err)
	}
}

// RegisterUpdateHook adds a step 15 hook to DefaultHooks.
func RegisterUpdateHook(name string, fn HookFunc) { must(DefaultHooks.RegisterUpdateHook(name, fn)) }

// RegisterPostStart adds a step 16 hook to DefaultHooks.
func RegisterPostStart(name string, fn HookFunc) { must(DefaultHooks.RegisterPostStart(name, fn)) }

// RegisterWorker adds a worker to DefaultHooks.
func RegisterWorker(name string, build WorkerFactory) {
	must(DefaultHooks.RegisterWorker(name, build))
}

// RegisterRoutes adds a route hook to DefaultHooks.
func RegisterRoutes(name string, add RouteHook) { must(DefaultHooks.RegisterRoutes(name, add)) }

// UseClaimPlane sets the command plane factory of DefaultHooks.
func UseClaimPlane(build func(c *Components) ClaimPlane) { must(DefaultHooks.UseClaimPlane(build)) }

// UseAgentUpdate sets the self-update of DefaultHooks.
func UseAgentUpdate(build func(c *Components) AgentUpdate) { must(DefaultHooks.UseAgentUpdate(build)) }

// UseSyncEngine sets the sync engine factory of DefaultHooks.
func UseSyncEngine(build func(c *Components) modelsapi.SyncEngine) {
	must(DefaultHooks.UseSyncEngine(build))
}

// UseSnapshotSources sets the snapshot sources factory of DefaultHooks.
func UseSnapshotSources(build func(c *Components) SnapshotSources) {
	must(DefaultHooks.UseSnapshotSources(build))
}
