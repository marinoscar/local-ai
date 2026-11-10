package jobs

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/api"
	"github.com/marinoscar/local-ai/agent/internal/audit"
	"github.com/marinoscar/local-ai/agent/internal/cloud"
	"github.com/marinoscar/local-ai/agent/internal/cloud/fake"
	"github.com/marinoscar/local-ai/agent/internal/hardware"
	"github.com/marinoscar/local-ai/agent/internal/modelsapi"
	"github.com/marinoscar/local-ai/agent/internal/modes"
	"github.com/marinoscar/local-ai/agent/internal/router"
	"github.com/marinoscar/local-ai/agent/internal/services"
	"github.com/marinoscar/local-ai/agent/internal/state"
	"github.com/marinoscar/local-ai/agent/internal/statusapi"
)

var t0 = time.Date(2026, 11, 2, 15, 0, 0, 0, time.UTC)

// fakeClock lets the cloud/fake manual clock satisfy cloud.Clock.
type fakeClock struct{ *fake.Clock }

func (c fakeClock) NewTimer(d time.Duration) cloud.Timer { return c.Clock.NewTimer(d) }

// quietLogger is what a handler logs to in these tests.
func job(id string) cloud.Job {
	return cloud.Job{ID: id, Type: "localai.test", Actor: audit.Cloud(id), Logger: discardLogger()}
}

// fakes holds one fake of every dependency and the Deps that wires them.
type fakes struct {
	router   *fakeRouter
	models   *fakeModels
	ark      *fakeArk
	modes    *fakeModes
	services *fakeServices
	logs     *fakeLogs
	catalog  *fakeCatalog
	link     *fakeLink
	status   *fakeStatus
	state    *fakeState
	hw       *fakeHardware
	ensure   *fakeEnsure
	clock    *fake.Clock
}

func newFakes() *fakes {
	f := &fakes{
		router: &fakeRouter{}, models: &fakeModels{}, ark: &fakeArk{}, modes: &fakeModes{}, services: &fakeServices{},
		logs: &fakeLogs{}, catalog: &fakeCatalog{}, link: &fakeLink{}, state: &fakeState{}, hw: &fakeHardware{}, ensure: &fakeEnsure{},
		clock: fake.NewClock(t0),
	}
	f.modes.info = modes.Info{Mode: modes.Online, Effective: modes.Online}
	f.status = &fakeStatus{}
	return f
}

func (f *fakes) deps() Deps {
	return Deps{
		Router: f.router, Models: f.models, Ark: f.ark, Modes: f.modes, Services: f.services, Logs: f.logs,
		Catalog: f.catalog, Link: f.link, Status: f.status, State: f.state, Hardware: f.hw,
		Ensure: f.ensure, Clock: fakeClock{f.clock},
	}
}

// handler returns the handler of a type built over the fakes.
func (f *fakes) handler(t *testing.T, name string) cloud.Handler {
	t.Helper()
	return f.handlerOf(t, f.deps(), name)
}

func (f *fakes) handlerOf(t *testing.T, d Deps, name string) cloud.Handler {
	t.Helper()
	for _, h := range Handlers(d) {
		if h.Type() == name {
			return h
		}
	}
	t.Fatalf("no handler for %s", name)
	return nil
}

// run decodes params and runs the handler of a type for a job.
func (f *fakes) run(t *testing.T, name string, params string) (cloud.Outcome, error) {
	t.Helper()
	return f.runCtx(t, context.Background(), name, params)
}

func (f *fakes) runCtx(t *testing.T, ctx context.Context, name string, params string) (cloud.Outcome, error) {
	t.Helper()
	h := f.handler(t, name)
	p, err := h.Decode(json.RawMessage(params))
	if err != nil {
		t.Fatalf("decode %s %s: %v", name, params, err)
	}
	return h.Run(ctx, job("job-1"), p)
}

// resultJSON marshals a result and checks it against the cloud's result schema of the type.
func resultJSON(t *testing.T, name string, out cloud.Outcome) string {
	t.Helper()
	raw, err := json.Marshal(out.Result)
	if err != nil {
		t.Fatal(err)
	}
	if probs := fake.ValidateResult("localai."+name, raw); len(probs) > 0 {
		t.Errorf("the result of %s does not match the cloud's schema: %v\n%s", name, probs, raw)
	}
	return string(raw)
}

// ---------------------------------------------------------------------------------------------------

type fakeRouter struct {
	mu        sync.Mutex
	loadReqs  []router.LoadRequest
	unloadReq []router.UnloadRequest
	defaults  []string
	actors    []audit.Actor
	load      router.LoadResult
	unload    router.UnloadResult
	setRes    router.DefaultResult
	loadErr   error
	unloadErr error
	setErr    error
	onLoad    func()
}

func (r *fakeRouter) LoadWith(_ context.Context, req router.LoadRequest) (router.LoadResult, error) {
	r.mu.Lock()
	r.loadReqs = append(r.loadReqs, req)
	res, err, on := r.load, r.loadErr, r.onLoad
	r.mu.Unlock()
	if on != nil {
		on()
	}
	return res, err
}

func (r *fakeRouter) UnloadWith(_ context.Context, req router.UnloadRequest) (router.UnloadResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.unloadReq = append(r.unloadReq, req)
	return r.unload, r.unloadErr
}

func (r *fakeRouter) SetDefault(_ context.Context, id string, actor audit.Actor) (router.DefaultResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.defaults = append(r.defaults, id)
	r.actors = append(r.actors, actor)
	return r.setRes, r.setErr
}

type fakeModels struct {
	mu        sync.Mutex
	detail    modelsapi.ModelDetail
	getErr    error
	downloads []modelsapi.DownloadRequest
	accepted  modelsapi.DownloadAccepted
	dlErr     error
	deletes   []modelsapi.DeleteRequest
	deleteFn  func(req modelsapi.DeleteRequest, call int) (modelsapi.DeleteResult, error)
	rotates   []modelsapi.KeyRotateRequest
	rotated   modelsapi.KeyRotated
	rotateFn  func(call int) (modelsapi.KeyRotated, error)
}

func (m *fakeModels) Get(_ context.Context, id string) (modelsapi.ModelDetail, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d := m.detail
	d.ID = id
	return d, m.getErr
}

func (m *fakeModels) Download(_ context.Context, req modelsapi.DownloadRequest) (modelsapi.DownloadAccepted, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.downloads = append(m.downloads, req)
	return m.accepted, m.dlErr
}

func (m *fakeModels) Delete(_ context.Context, req modelsapi.DeleteRequest) (modelsapi.DeleteResult, error) {
	m.mu.Lock()
	m.deletes = append(m.deletes, req)
	n, fn := len(m.deletes), m.deleteFn
	m.mu.Unlock()
	if fn == nil {
		return modelsapi.DeleteResult{ID: req.ID}, nil
	}
	return fn(req, n)
}

func (m *fakeModels) RotateKey(_ context.Context, req modelsapi.KeyRotateRequest) (modelsapi.KeyRotated, error) {
	m.mu.Lock()
	m.rotates = append(m.rotates, req)
	n, fn, res := len(m.rotates), m.rotateFn, m.rotated
	m.mu.Unlock()
	if fn != nil {
		return fn(n)
	}
	return res, nil
}

type fakeArk struct {
	mu       sync.Mutex
	syncs    []ArkRequest
	verifies []ArkRequest
	syncRes  ArkSyncStarted
	syncErr  error
	verRes   ArkVerifyStarted
	verErr   error
}

func (a *fakeArk) StartSync(_ context.Context, req ArkRequest) (ArkSyncStarted, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.syncs = append(a.syncs, req)
	return a.syncRes, a.syncErr
}

func (a *fakeArk) StartVerify(_ context.Context, req ArkRequest) (ArkVerifyStarted, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.verifies = append(a.verifies, req)
	return a.verRes, a.verErr
}

type fakeModes struct {
	mu   sync.Mutex
	info modes.Info
	reqs []modes.Mode
	opts []modes.RequestOptions
	err  error
}

func (m *fakeModes) Request(_ context.Context, target modes.Mode, o modes.RequestOptions) (modes.Result, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reqs = append(m.reqs, target)
	m.opts = append(m.opts, o)
	return modes.Result{Target: target}, m.err
}

func (m *fakeModes) Info() modes.Info {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.info
}

type fakeServices struct {
	mu   sync.Mutex
	reqs []api.ServiceActionRequest
	fn   func(call int) (api.ServiceActionResult, error)
	res  api.ServiceActionResult
}

func (s *fakeServices) RunAction(_ context.Context, req api.ServiceActionRequest) (api.ServiceActionResult, error) {
	s.mu.Lock()
	s.reqs = append(s.reqs, req)
	n, fn := len(s.reqs), s.fn
	res := s.res
	s.mu.Unlock()
	if fn != nil {
		return fn(n)
	}
	res.Name = req.Name
	return res, nil
}

type fakeLogs struct {
	mu    sync.Mutex
	calls []logCall
	tail  services.JournalTail
	err   error
}

type logCall struct {
	Unit  string
	Lines int
	Since time.Time
}

func (l *fakeLogs) Tail(_ context.Context, unit string, lines int, since time.Time) (services.JournalTail, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls = append(l.calls, logCall{unit, lines, since})
	return l.tail, l.err
}

type fakeCatalog struct {
	mu     sync.Mutex
	actors []audit.Actor
	body   statusapi.CatalogRefreshBody
	err    error
}

func (c *fakeCatalog) RefreshCatalog(_ context.Context, actor audit.Actor) (statusapi.CatalogRefreshBody, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.actors = append(c.actors, actor)
	return c.body, c.err
}

type fakeLink struct {
	mu     sync.Mutex
	pushes int
	keys   int
	res    cloud.StateRefresh
	err    error
	keyErr error
	order  *[]string
}

func (l *fakeLink) PushNow(context.Context) (cloud.StateRefresh, error) {
	l.mu.Lock()
	l.pushes++
	res, err := l.res, l.err
	if l.order != nil {
		*l.order = append(*l.order, "state")
	}
	l.mu.Unlock()
	return res, err
}

func (l *fakeLink) Push(context.Context) error {
	l.mu.Lock()
	l.keys++
	err := l.keyErr
	if l.order != nil {
		*l.order = append(*l.order, "key")
	}
	l.mu.Unlock()
	return err
}

type fakeStatus struct {
	mu sync.Mutex
	st statusapi.Status
}

func (s *fakeStatus) Build(context.Context, bool, statusapi.BuildOptions) statusapi.Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.st
}

func (s *fakeStatus) edit(fn func(*statusapi.Status)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn(&s.st)
}

type fakeState struct {
	mu sync.Mutex
	st state.State
}

func (s *fakeState) Snapshot() state.State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.st
}

type fakeHardware struct {
	mu      sync.Mutex
	refresh int
}

func (h *fakeHardware) Refresh(context.Context) hardware.Snapshot {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.refresh++
	return hardware.Snapshot{}
}

func i64(n int64) *int64   { return &n }
func str(s string) *string { return &s }

type fakeEnsure struct {
	mu     sync.Mutex
	actors []audit.Actor
	res    EnsureStarted
	err    error
}

func (f *fakeEnsure) StartEnsure(_ context.Context, actor audit.Actor) (EnsureStarted, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.actors = append(f.actors, actor)
	return f.res, f.err
}
