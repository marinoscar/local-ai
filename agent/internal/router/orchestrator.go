package router

import (
	"context"
	"errors"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/audit"
	"github.com/marinoscar/local-ai/agent/internal/catalog"
	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/state"
)

// Timing defaults of the orchestrator (llama-router.md sections 4.4 and 4.5).
const (
	// DefaultLoadTimeout is llama.load_timeout_seconds when node.yaml does not say (300 s); the
	// weights on an ark drive get twice that.
	DefaultLoadTimeout = 300 * time.Second
	// DefaultLoadPoll is the cadence of GET /models while a model loads.
	DefaultLoadPoll = time.Second
	// DefaultEvictPoll is the cadence of GET /models while a model unloads.
	DefaultEvictPoll = 500 * time.Millisecond
	// DefaultEvictTimeout bounds the wait for one unload that is part of a load or a rollback.
	DefaultEvictTimeout = 30 * time.Second
	// DefaultUnloadHang is how long an explicit unload waits before the router counts as hung and
	// llama is restarted (LAI-LLM-031). The router gives a child 10 s and then kills it, so this is
	// far beyond a normal stop.
	DefaultUnloadHang = 60 * time.Second
	// DefaultDownGrace is how long the router may fail to answer while a model loads before the
	// load counts as failed: a llama restart takes a few seconds.
	DefaultDownGrace = 20 * time.Second
	// DefaultRestartWait bounds the wait for the router to answer after the orchestrator restarted
	// llama (llama-router.md section 4.6) and the boot restore's wait for /health at agent start.
	DefaultRestartWait = 120 * time.Second
	// DefaultHealthWait bounds the wait for /health after RestartAndRestore restarted llama
	// (llama-router.md section 4.11).
	DefaultHealthWait = 60 * time.Second
	// DefaultProgressEvery is the shortest gap between two forwarded progress events.
	DefaultProgressEvery = time.Second
	// loadedNotSeenPolls is how many polls in a row may show a model we asked to load as
	// "unloaded" without ever having shown "loading" before it counts as a failure.
	loadedNotSeenPolls = 3
)

// Timing groups the waits of the orchestrator. A zero field takes its default; tests shorten them.
type Timing struct {
	LoadPoll, EvictPoll, EvictTimeout, UnloadHang, DownGrace, RestartWait, ProgressEvery time.Duration
	// HealthWait is how long RestartAndRestore waits for /health after the restart.
	HealthWait time.Duration
}

func (t Timing) withDefaults() Timing {
	def := func(v *time.Duration, d time.Duration) {
		if *v <= 0 {
			*v = d
		}
	}
	def(&t.LoadPoll, DefaultLoadPoll)
	def(&t.EvictPoll, DefaultEvictPoll)
	def(&t.EvictTimeout, DefaultEvictTimeout)
	def(&t.UnloadHang, DefaultUnloadHang)
	def(&t.DownGrace, DefaultDownGrace)
	def(&t.RestartWait, DefaultRestartWait)
	def(&t.ProgressEvery, DefaultProgressEvery)
	def(&t.HealthWait, DefaultHealthWait)
	return t
}

// StateStore is the part of *state.Store the orchestrator uses.
type StateStore interface {
	Snapshot() state.State
	Mutate(fn func(*state.State) error) error
	MutateSync(fn func(*state.State) error) error
}

// AuditWriter is the audit log; *audit.Log implements it.
type AuditWriter interface {
	Write(ev audit.Event) error
}

// ProgressSource gives the last load progress of a loading model; *Watcher implements it.
type ProgressSource interface {
	LatestProgress(id string) (Progress, bool)
}

// LlamaRestarter restarts localai-llama.service and waits for the job to finish. It is the escape
// of an unload that hangs (llama-router.md section 4.5); internal/services.Manager.Restart with
// services.UnitLlama implements it.
type LlamaRestarter interface {
	RestartLlama(ctx context.Context) error
}

// LlamaStopper stops localai-llama.service and waits for the job to finish; a forced unload in
// single mode uses it (llama-router.md section 4.8). internal/services.Manager.Stop with
// services.UnitLlama implements it.
type LlamaStopper interface {
	StopLlama(ctx context.Context) error
}

// Regenerator renders models.ini and applies it to the router; *Generator implements it. SetDefault
// uses it to write the new load-on-startup flag.
type Regenerator interface {
	Generate(ctx context.Context, in Inputs) (Result, error)
}

// OrchestratorOptions configures NewOrchestrator. Router, State and Inputs are required.
type OrchestratorOptions struct {
	Router Router
	State  StateStore
	// Inputs returns the facts an operation decides from: the catalog, the profile, node.yaml's
	// llama block, the mounted roots, the fit environment (a fresh hardware snapshot) and the mode.
	// It is called once per operation. It is the same function the Generator's caller uses;
	// StateDefault is ignored (the orchestrator reads state.models.default itself).
	Inputs func() Inputs
	// Presence decides "present on a root"; default ManifestPresence over the real filesystem.
	Presence Presence
	// Lock is modelOpsMu. Share it with the Generator (GeneratorOptions.Ops) and with key rotation
	// and llama restart; default a private one.
	Lock *OpLock
	// Generator is used by SetDefault; nil makes SetDefault change the state only.
	Generator Regenerator
	// Publisher receives model.loading, model.loaded, model.unloaded, model.failed and
	// model.default_changed; Audit the model.load, model.unload and model.set_default lines; Alerts
	// LAI-LLM-012 and LAI-LLM-031. Each may be nil.
	Publisher Publisher
	Audit     AuditWriter
	Alerts    AlertSink
	// Suppress is Watcher.Suppress: it keeps the watcher from reporting the operation's own
	// unloads as crashes. nil skips it.
	Suppress func(opID string) (release func())
	// Progress forwards load progress (the watcher); nil forwards none.
	Progress ProgressSource
	// Restarter restarts llama after an unload hangs, and for RestartAndRestore; nil leaves the
	// hang as an error and makes RestartAndRestore refuse.
	Restarter LlamaRestarter
	// Builds tells which llama.cpp build is installed (current/BUILD.json); RestartAndRestore
	// records it in state.llama.build. nil skips that.
	Builds BuildSource
	// Units, UnitBase and Stopper are single mode's (llama-router.md section 4.8). Units renders
	// the unit (*UnitGenerator); UnitBase supplies what the node fixes for the unit (Port, Binary;
	// Mode, Router and Single are filled in by the orchestrator), default the generator's defaults;
	// Stopper stops the unit for a forced unload. Without Units or Restarter a load in single mode
	// is refused (LAI-LLM-007) and ApplyMode does nothing.
	Units    SingleUnits
	UnitBase func() UnitInputs
	Stopper  LlamaStopper
	// Engines are the adapters of the engines other than llama (E11: acestep, the music engine). A
	// model of an engine without an adapter is refused (whisper and piper run as services).
	Engines []EngineAdapter
	Timing  Timing
	// Clock measures every wait; Sleep waits between polls (tests advance a manual clock in it).
	// The default Sleep returns early when the orchestrator shuts down.
	Clock  config.Clock
	Sleep  func(ctx context.Context, d time.Duration)
	Logger *slog.Logger
}

// Counters is what /status?counters=1 and the heartbeat report (llama-router.md section 7).
type Counters struct {
	LoadsOK, LoadsFailed, Rollbacks int64
	// Models holds the load statistics of every model loaded since the agent started.
	Models map[string]LoadStats
}

// LoadStats are the successful loads of one model.
type LoadStats struct {
	Count  int64
	LastMS int64
	MeanMS int64
}

// Orchestrator is the one path that loads and unloads models on the node (llama-router.md sections
// 4.4 and 4.5): aictl, the desktop, the Portal, the /chat/ picker, the cloud and the mode engine
// all call Load, Unload and SetDefault, so every operation is serialised by modelOpsMu, planned with
// the fit rules, audited, announced with events and rolled back when it fails. It is safe for
// concurrent use.
type Orchestrator struct {
	opt   OrchestratorOptions
	log   *slog.Logger
	clock config.Clock
	lock  *OpLock

	closing   chan struct{}
	closeOnce sync.Once
	wg        sync.WaitGroup
	defaultMu sync.Mutex

	cmu      sync.Mutex
	counters Counters
	sums     map[string]int64
}

// NewOrchestrator returns an Orchestrator.
func NewOrchestrator(opt OrchestratorOptions) (*Orchestrator, error) {
	switch {
	case opt.Router == nil:
		return nil, errors.New("router: the orchestrator needs a Router")
	case opt.State == nil:
		return nil, errors.New("router: the orchestrator needs a StateStore")
	case opt.Inputs == nil:
		return nil, errors.New("router: the orchestrator needs an Inputs function")
	}
	if opt.Presence == nil {
		opt.Presence = ManifestPresence{FS: config.OSFS{}}
	}
	if opt.Lock == nil {
		opt.Lock = NewOpLock(nil, nil)
	}
	opt.Timing = opt.Timing.withDefaults()
	o := &Orchestrator{
		opt: opt, lock: opt.Lock, clock: opt.Clock,
		closing: make(chan struct{}), sums: map[string]int64{},
	}
	o.counters.Models = map[string]LoadStats{}
	if o.clock == nil {
		o.clock = config.RealClock{}
	}
	if opt.Sleep == nil {
		o.opt.Sleep = o.realSleep
	}
	l := opt.Logger
	if l == nil {
		l = slog.Default()
	}
	o.log = l.With("component", "router")
	return o, nil
}

type reqIDKey struct{}

// WithReqID returns a context that carries the id of the HTTP request that asked for an operation;
// the orchestrator writes it into the audit line (agent.md section 4.11). A caller with no request
// (the mode engine, a cloud job) does not set it.
func WithReqID(ctx context.Context, reqID string) context.Context {
	return context.WithValue(ctx, reqIDKey{}, reqID)
}

func reqIDFrom(ctx context.Context) string {
	s, _ := ctx.Value(reqIDKey{}).(string)
	return s
}

// Lock returns modelOpsMu, for the callers that must hold it themselves (key rotation, llama
// restart, the Generator's reload).
func (o *Orchestrator) Lock() *OpLock { return o.lock }

// Counters returns a copy of the counters.
func (o *Orchestrator) Counters() Counters {
	o.cmu.Lock()
	defer o.cmu.Unlock()
	c := o.counters
	c.Models = make(map[string]LoadStats, len(o.counters.Models))
	for k, v := range o.counters.Models {
		c.Models[k] = v
	}
	return c
}

// Shutdown stops the operations that are waiting without aborting any call the router already
// accepted (agent.md section 4.12 step 6): a running load stops being watched, state.models.last_load
// is left as "interrupted", and no rollback runs (the router keeps loading on its own). It returns
// when the operations have recorded that, or when ctx ends.
func (o *Orchestrator) Shutdown(ctx context.Context) error {
	o.closeOnce.Do(func() { close(o.closing) })
	return o.Wait(ctx)
}

// Wait returns when every operation that runs in the background (a load with wait false) has
// finished, or when ctx ends.
func (o *Orchestrator) Wait(ctx context.Context) error {
	done := make(chan struct{})
	go func() {
		o.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (o *Orchestrator) isClosing() bool {
	select {
	case <-o.closing:
		return true
	default:
		return false
	}
}

// realSleep waits d, or until the caller's context or the orchestrator's shutdown ends it.
func (o *Orchestrator) realSleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-o.closing:
	case <-t.C:
	}
}

// pause waits between two polls. It is false when the orchestrator shut down: the caller stops.
func (o *Orchestrator) pause(ctx context.Context, d time.Duration) bool {
	if o.isClosing() {
		return false
	}
	o.opt.Sleep(ctx, d)
	return !o.isClosing()
}

// ---- facts about the node ------------------------------------------------------------------

func findModel(c *catalog.Catalog, id string) (*catalog.Model, bool) {
	if c == nil {
		return nil, false
	}
	for i := range c.Models {
		if c.Models[i].ID == id {
			return &c.Models[i], true
		}
	}
	return nil, false
}

// locate returns the root the model's files are on (the internal root first, then the drives) and
// whether a complete copy exists.
func (o *Orchestrator) locate(in Inputs, m *catalog.Model) (root string, present bool) {
	for _, r := range in.Roots.Ordered() {
		if o.opt.Presence.Present(r, m) {
			return r, true
		}
	}
	return "", false
}

// loadTimeout is T of llama-router.md section 4.4 step 6: llama.load_timeout_seconds (300 s by
// default), doubled when the weights are on an ark drive.
func (o *Orchestrator) loadTimeout(in Inputs, id string) time.Duration {
	t := time.Duration(in.Llama.LoadTimeoutSeconds) * time.Second
	if t <= 0 {
		t = DefaultLoadTimeout
	}
	if m, ok := findModel(in.Catalog, id); ok {
		if root, present := o.locate(in, m); present && root != in.Roots.Internal {
			t *= 2
		}
	}
	return t
}

// loadedFrom builds the planner's view of the running models other than skip.
func loadedFrom(in Inputs, st state.State, statuses []ModelStatus, skip string) []Loaded {
	var out []Loaded
	for _, s := range statuses {
		if s.ID == skip || !s.Status.Running() {
			continue
		}
		l := Loaded{ID: s.ID, LastUsed: st.Models.LastUsed[s.ID], EffectiveMiB: s.EffectiveMiB()}
		if m, ok := findModel(in.Catalog, s.ID); ok {
			l.Requirements = m.Requirements
		}
		out = append(out, l)
	}
	return out
}

func statusByID(statuses []ModelStatus) map[string]ModelStatus {
	m := make(map[string]ModelStatus, len(statuses))
	for _, s := range statuses {
		m[s.ID] = s
	}
	return m
}

// loadedIDs returns the ids of the models that are loaded (or sleeping), most recently used first.
func loadedIDs(statuses []ModelStatus, lastUsed map[string]time.Time) []string {
	var ids []string
	for _, s := range statuses {
		if s.Status.Resident() {
			ids = append(ids, s.ID)
		}
	}
	sortMRU(ids, lastUsed)
	return ids
}

// sortMRU orders ids most recently used first; ties by id.
func sortMRU(ids []string, lastUsed map[string]time.Time) {
	sort.SliceStable(ids, func(i, j int) bool {
		a, b := lastUsed[ids[i]], lastUsed[ids[j]]
		if !a.Equal(b) {
			return a.After(b)
		}
		return ids[i] < ids[j]
	})
}

func contains1(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// ---- state ---------------------------------------------------------------------------------

// persist writes state durably. A state file that cannot be written must not keep a node from
// loading a model (doomsday), so a failure is logged and the operation goes on.
func (o *Orchestrator) persist(what string, fn func(*state.State) error) {
	if err := o.opt.State.MutateSync(fn); err != nil {
		o.log.Warn("could not record model state", "what", what, "error", err)
	}
}

func (o *Orchestrator) setLastLoad(id, opID, result string, loadMS int64) {
	now := o.clock.Now().UTC().Truncate(time.Second)
	o.persist("last_load", func(st *state.State) error {
		st.Models.LastLoad = &state.LastLoad{ID: id, OpID: opID, Result: result, LoadMS: loadMS, At: now}
		return nil
	})
}

// ---- events, audit, alerts -----------------------------------------------------------------

func (o *Orchestrator) publish(typ, opID string, data any) {
	if o.opt.Publisher == nil {
		return
	}
	if err := o.opt.Publisher.Publish(typ, opID, data); err != nil {
		o.log.Error("could not publish the event", "event", typ, "op_id", opID, "error", err)
	}
}

func (o *Orchestrator) auditWrite(ctx context.Context, action string, actor audit.Actor, id, opID string, err error) {
	if o.opt.Audit == nil {
		return
	}
	ev := audit.Event{Action: action, Result: audit.ResultOK, Actor: actor, Target: map[string]string{"model": id}, OpID: opID, ReqID: reqIDFrom(ctx)}
	if err != nil {
		ev.Result = audit.ResultFail
		var oe *OpError
		if errors.As(err, &oe) {
			ev.Code = string(oe.Code)
		}
	}
	if werr := o.opt.Audit.Write(ev); werr != nil {
		o.log.Error("could not write the audit line", "action", action, "error", werr)
	}
}

func (o *Orchestrator) raise(code Code, subject string, details map[string]any) {
	if o.opt.Alerts != nil {
		o.opt.Alerts.Raise(string(code), subject, details)
	}
}

func (o *Orchestrator) clear(code Code, subject string) {
	if o.opt.Alerts != nil {
		o.opt.Alerts.Clear(string(code), subject)
	}
}

func (o *Orchestrator) countLoad(id string, ok bool, ms int64) {
	o.cmu.Lock()
	defer o.cmu.Unlock()
	if !ok {
		o.counters.LoadsFailed++
		return
	}
	o.counters.LoadsOK++
	s := o.counters.Models[id]
	s.Count++
	s.LastMS = ms
	o.sums[id] += ms
	s.MeanMS = o.sums[id] / s.Count
	o.counters.Models[id] = s
}

func (o *Orchestrator) countRollback() {
	o.cmu.Lock()
	o.counters.Rollbacks++
	o.cmu.Unlock()
}

func (o *Orchestrator) suppress(opID string) func() {
	if o.opt.Suppress == nil {
		return func() {}
	}
	return o.opt.Suppress(opID)
}
