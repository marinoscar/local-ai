package router

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/audit"
	"github.com/marinoscar/local-ai/agent/internal/catalog"
	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/events"
	"github.com/marinoscar/local-ai/agent/internal/router/fake"
	"github.com/marinoscar/local-ai/agent/internal/state"
	statefake "github.com/marinoscar/local-ai/agent/internal/state/fake"
)

// ---- a virtual clock ----

// vClock is a config.Clock that only moves when the orchestrator sleeps (see orRig.sleep), so a
// 300 s load timeout takes no time in a test. The fake router reads it too.
type vClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *vClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *vClock) NewTicker(time.Duration) config.Ticker { panic("not used") }

func (c *vClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// ---- recorders ----

type auditRec struct {
	mu   sync.Mutex
	list []audit.Event
}

func (a *auditRec) Write(ev audit.Event) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.list = append(a.list, ev)
	return nil
}

func (a *auditRec) events() []audit.Event {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]audit.Event(nil), a.list...)
}

// suppressRec records Suppress and its release, and which operations are suppressed right now.
type suppressRec struct {
	mu       sync.Mutex
	started  []string
	released []string
	active   map[string]bool
}

func (s *suppressRec) Suppress(opID string) func() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active == nil {
		s.active = map[string]bool{}
	}
	s.started = append(s.started, opID)
	s.active[opID] = true
	return func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.released = append(s.released, opID)
		delete(s.active, opID)
	}
}

func (s *suppressRec) anyActive() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.active) > 0
}

// restarter is a LlamaRestarter that records calls and runs a hook (the test restarts the fake).
type restarter struct {
	mu   sync.Mutex
	n    int
	err  error
	hook func()
}

func (r *restarter) RestartLlama(context.Context) error {
	r.mu.Lock()
	r.n++
	err, hook := r.err, r.hook
	r.mu.Unlock()
	if hook != nil {
		hook()
	}
	return err
}

func (r *restarter) calls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.n
}

// presenceStub says every model is present, except those in missing; a model in drive is present
// on the drive only.
type presenceStub struct {
	missing map[string]bool
	drive   map[string]bool
}

func (p presenceStub) Present(root string, m *catalog.Model) bool {
	if p.missing[m.ID] {
		return false
	}
	if p.drive[m.ID] {
		return root == orDrive
	}
	return root == rootInternal
}

const orDrive = "/mnt/ark"

type progressStub struct {
	mu sync.Mutex
	p  map[string]Progress
}

func (p *progressStub) LatestProgress(id string) (Progress, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	v, ok := p.p[id]
	return v, ok
}

func (p *progressStub) set(id string, v float64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.p == nil {
		p.p = map[string]Progress{}
	}
	p.p[id] = Progress{Model: id, Status: StatusLoading, Value: v, HasValue: true}
}

// ---- the rig ----

// orCfg describes the node an orRig runs.
type orCfg struct {
	env         Env
	profile     *catalog.Profile
	survival    bool
	presence    presenceStub
	roots       Roots
	llama       config.LlamaBlock
	noRestarter bool
	loadTime    time.Duration
	// extra are catalog models next to orCatalog (E11: music models); engines the engine adapters.
	extra   []catalog.Model
	engines []EngineAdapter
}

// orRig is an Orchestrator over the HTTP fake router, a real state store on an in-memory filesystem,
// a virtual clock and recorders. The node has four small models m-a to m-d; every one fits alone
// (4000 MiB of an 8192 MiB card), and how many fit together is the Env's models_max.
type orRig struct {
	t      *testing.T
	f      *fake.Router
	client *Client
	clk    *vClock
	fs     *statefake.MemFS
	st     *state.Store
	pub    *pubRec
	alerts *alertLog
	aud    *auditRec
	sup    *suppressRec
	prog   *progressStub
	rs     *restarter
	logs   *logBuf
	o      *Orchestrator
	cfg    *orCfg

	sleeps atomic.Int64
	mu     sync.Mutex
	hook   func(n int)
}

const orStatePath = "/var/lib/localai/state.json"

func orModels() []catalog.Model {
	var out []catalog.Model
	for _, id := range []string{"m-a", "m-b", "m-c", "m-d"} {
		out = append(out, llamaModel(id, nil, 4000, 5000, nil, nil))
	}
	return out
}

// orCatalog is the models of the node: the four llama models and a speech model, which the
// orchestrator must refuse to load.
func orCatalog() []catalog.Model {
	m := catalog.Model{ID: "whisper-x", Engine: "whisper", Requirements: req(0, 2000)}
	return append(orModels(), m)
}

func orProfile() *catalog.Profile {
	return &catalog.Profile{
		ID: "test", Router: routerBlock(1, 512),
		Include:      catalog.Include{Models: []string{"m-a", "m-b", "m-c", "m-d"}},
		DefaultModel: "m-a",
		Survival:     catalog.Survival{Models: []string{"m-c", "m-d"}, DefaultModel: "m-c"},
	}
}

// withMax gives the node room for n models at once.
func orMax(n int) func(*orCfg) {
	return func(c *orCfg) {
		c.env.ModelsMax = n
		c.env.GTotalMiB = 24000
		c.profile.Router.ModelsMax = n
	}
}

func orSurvival() func(*orCfg) { return func(c *orCfg) { c.survival = true } }

func newOrRig(t *testing.T, mut ...func(*orCfg)) *orRig {
	t.Helper()
	cfg := &orCfg{
		env:      NewEnv(snapAiN3(), "cuda", routerBlock(1, 512), 0),
		profile:  orProfile(),
		roots:    Roots{Internal: rootInternal, Drives: []string{orDrive}},
		llama:    config.LlamaBlock{LoadTimeoutSeconds: 300},
		loadTime: 5 * time.Second,
	}
	for _, m := range mut {
		if m != nil {
			m(cfg)
		}
	}
	r := &orRig{
		t: t, cfg: cfg, clk: &vClock{t: fixedTime()}, fs: statefake.New(),
		pub: &pubRec{}, alerts: &alertLog{}, aud: &auditRec{}, sup: &suppressRec{}, prog: &progressStub{},
		rs: &restarter{}, logs: &logBuf{},
	}
	bearer := secret(1)
	r.f = fake.New(bearer)
	t.Cleanup(r.f.Close)
	r.f.SetClock(r.clk.Now)
	r.f.SetLoadTime(cfg.loadTime)
	for _, m := range orModels() {
		r.f.Add(fake.Model{ID: m.ID})
	}
	c, err := NewClient(Options{BaseURL: r.f.URL(), Keys: StaticKey(bearer), Timeout: 2 * time.Second, Logger: r.logs.logger()})
	if err != nil {
		t.Fatal(err)
	}
	r.client = c
	st, err := state.Open(orStatePath, state.Options{FS: r.fs, AgentVersion: "test", Now: r.clk.Now})
	if err != nil {
		t.Fatal(err)
	}
	r.st = st
	t.Cleanup(func() { _ = st.Close() })

	opt := OrchestratorOptions{
		Router: c, State: st,
		Inputs:    r.inputs,
		Presence:  cfg.presence,
		Lock:      NewOpLock(nil, r.clk.Now),
		Publisher: r.pub, Audit: r.aud, Alerts: r.alerts, Suppress: r.sup.Suppress, Progress: r.prog,
		Engines: cfg.engines,
		Timing:  Timing{UnloadHang: 60 * time.Second, RestartWait: 10 * time.Second},
		Clock:   r.clk, Sleep: r.sleep, Logger: r.logs.logger(),
	}
	if !cfg.noRestarter {
		opt.Restarter = r.rs
	}
	o, err := NewOrchestrator(opt)
	if err != nil {
		t.Fatal(err)
	}
	r.o = o
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = o.Shutdown(ctx)
		if strings.Contains(r.logs.String(), bearer) {
			t.Errorf("the key is in the logs")
		}
	})
	return r
}

func (r *orRig) inputs() Inputs {
	return Inputs{
		NodeID: "ai-test", Catalog: &catalog.Catalog{Version: "test", Models: append(orCatalog(), r.cfg.extra...)},
		Profile: r.cfg.profile, Llama: r.cfg.llama, Roots: r.cfg.roots, Fit: r.cfg.env, Survival: r.cfg.survival,
	}
}

// sleep is the orchestrator's Sleep: it moves the virtual clock, then lets a test act.
func (r *orRig) sleep(_ context.Context, d time.Duration) {
	n := int(r.sleeps.Add(1))
	r.clk.advance(d)
	r.mu.Lock()
	h := r.hook
	r.mu.Unlock()
	if h != nil {
		h(n)
	}
}

func (r *orRig) setHook(h func(n int)) {
	r.mu.Lock()
	r.hook = h
	r.mu.Unlock()
}

// load puts models in the loaded state, the first one the least recently used (last_used one minute
// apart), and records them as desired_loaded, most recent first.
func (r *orRig) load(ids ...string) {
	r.t.Helper()
	for _, id := range ids {
		r.f.Set(id, fake.Loaded)
	}
	r.seed(func(s *state.State) {
		if s.Models.LastUsed == nil {
			s.Models.LastUsed = map[string]time.Time{}
		}
		var desired []string
		for i, id := range ids {
			s.Models.LastUsed[id] = fixedTime().Add(-time.Duration(len(ids)-i) * time.Hour)
			desired = append([]string{id}, desired...)
		}
		s.Models.DesiredLoaded = desired
	})
}

func (r *orRig) seed(fn func(*state.State)) {
	r.t.Helper()
	if err := r.st.MutateSync(func(s *state.State) error { fn(s); return nil }); err != nil {
		r.t.Fatal(err)
	}
}

func (r *orRig) snap() state.State { return r.st.Snapshot() }

func (r *orRig) lastLoad() state.LastLoad {
	r.t.Helper()
	ll := r.snap().Models.LastLoad
	if ll == nil {
		r.t.Fatal("state.models.last_load is not set")
	}
	return *ll
}

// loadedNow lists the models the fake says are loaded, sorted.
func (r *orRig) loadedNow() []string {
	var out []string
	for _, m := range orModels() {
		if r.f.Status(m.ID) == fake.Loaded {
			out = append(out, m.ID)
		}
	}
	return out
}

// calls lists the load and unload requests the router received, as "load m-a" / "unload m-a".
func (r *orRig) calls() []string {
	var out []string
	for _, q := range r.f.Requests() {
		if q.Method == "POST" && (q.Path == "/models/load" || q.Path == "/models/unload") {
			id := strings.TrimSuffix(strings.TrimPrefix(q.Body, `{"model":"`), `"}`)
			out = append(out, strings.TrimPrefix(q.Path, "/models/")+" "+id)
		}
	}
	return out
}

func (r *orRig) countCalls(prefix string) int {
	n := 0
	for _, c := range r.calls() {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

// evs summarises the events published since the last call.
func (r *orRig) evs() []string {
	var out []string
	for _, e := range r.pub.take() {
		switch d := e.data.(type) {
		case events.ModelLoading:
			s := fmt.Sprintf("loading %s unloading=%v", d.Model, d.Unloading)
			if d.Progress != nil {
				s += fmt.Sprintf(" progress=%.2f", *d.Progress)
			}
			out = append(out, s)
		case events.ModelLoaded:
			out = append(out, "loaded "+d.Model)
		case events.ModelUnloaded:
			out = append(out, fmt.Sprintf("unloaded %s %s", d.Model, d.Reason))
		case events.ModelFailed:
			out = append(out, fmt.Sprintf("failed %s %s rolled_back=%v restored=%v", d.Model, d.Code, d.RolledBack, d.Restored))
		case events.ModelDefaultChanged:
			out = append(out, fmt.Sprintf("default_changed %s previous=%s", d.Model, d.Previous))
		default:
			out = append(out, fmt.Sprintf("%s %v", e.typ, e.data))
		}
	}
	return out
}

// opErrOf returns err as an *OpError or fails the test.
func opErrOf(t *testing.T, err error) *OpError {
	t.Helper()
	var oe *OpError
	if !errors.As(err, &oe) {
		t.Fatalf("want an *OpError, got %T %v", err, err)
	}
	return oe
}

func eqStrings(t *testing.T, what string, got, want []string) {
	t.Helper()
	if len(got) == 0 && len(want) == 0 {
		return
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("%s:\n got  %q\n want %q", what, got, want)
	}
}

var bg = context.Background()

var sysActor = audit.System()
