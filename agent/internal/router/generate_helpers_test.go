package router

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/catalog"
	"github.com/marinoscar/local-ai/agent/internal/config"
)

// ---- recorders and stubs ----

type alertEvent struct{ Op, Code, Subject string }

type alertLog struct {
	mu     sync.Mutex
	events []alertEvent
}

func (a *alertLog) Raise(code, subject string, details map[string]any) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.events = append(a.events, alertEvent{"raise", code, subject})
}

func (a *alertLog) Clear(code, subject string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.events = append(a.events, alertEvent{"clear", code, subject})
}

// has reports whether the log holds an event of op for code (and subject, when not "*").
func (a *alertLog) has(op string, code Code, subject string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, e := range a.events {
		if e.Op == op && e.Code == string(code) && (subject == "*" || e.Subject == subject) {
			return true
		}
	}
	return false
}

func (a *alertLog) reset() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.events = nil
}

type memIni struct {
	mu  sync.Mutex
	sha string
	err error
}

func (m *memIni) IniSHA256() string { m.mu.Lock(); defer m.mu.Unlock(); return m.sha }
func (m *memIni) SetIniSHA256(s string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	m.sha = s
	return nil
}

type pubLog struct {
	mu   sync.Mutex
	list []string
}

func (p *pubLog) Publish(typ, opID string, data any) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.list = append(p.list, fmt.Sprintf("%s %+v", typ, data))
	return nil
}

func (p *pubLog) take() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := p.list
	p.list = nil
	return out
}

// scriptRouter is a Router with scripted statuses that records the calls Generate makes. A Reload
// applies onReload to the statuses (the real router unloads a model whose preset changed).
type scriptRouter struct {
	mu       sync.Mutex
	statuses []ModelStatus
	err      error // returned by every call while set
	calls    []string
	// reloadFails makes the reload call itself fail.
	reloadFails bool
	// onReload edits the statuses when a reload is served; nil leaves them alone.
	onReload func(statuses []ModelStatus) []ModelStatus
	loadErr  map[string]error
}

func (s *scriptRouter) set(statuses ...ModelStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.statuses = statuses
}

func (s *scriptRouter) record(c string) { s.calls = append(s.calls, c) }

func (s *scriptRouter) callList() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.calls...)
}

func (s *scriptRouter) Health(ctx context.Context) error { return s.err }
func (s *scriptRouter) Models(ctx context.Context) ([]ModelStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return nil, s.err
	}
	return append([]ModelStatus(nil), s.statuses...), nil
}
func (s *scriptRouter) Reload(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.record("reload")
	if s.err != nil {
		return s.err
	}
	if s.reloadFails {
		return errors.New("reload failed")
	}
	if s.onReload != nil {
		s.statuses = s.onReload(append([]ModelStatus(nil), s.statuses...))
	}
	return nil
}
func (s *scriptRouter) Load(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.record("load " + id)
	if err := s.loadErr[id]; err != nil {
		return err
	}
	for i := range s.statuses {
		if s.statuses[i].ID == id {
			s.statuses[i].Status = StatusLoading
		}
	}
	return nil
}
func (s *scriptRouter) Unload(ctx context.Context, id string) error { return nil }
func (s *scriptRouter) Progress(ctx context.Context) (<-chan Progress, error) {
	return nil, errors.New("not used")
}
func (s *scriptRouter) Props(ctx context.Context, id string) (Props, error) { return Props{}, nil }
func (s *scriptRouter) ChatSmoke(ctx context.Context, model string) error   { return nil }

var _ Router = (*scriptRouter)(nil)

func st(id string, status Status) ModelStatus {
	return ModelStatus{ID: id, Status: status, Source: SourcePreset}
}

type opsLock struct {
	mu      sync.Mutex
	err     error
	held    int
	acquire int
}

func (o *opsLock) Acquire(ctx context.Context) (func(), error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.acquire++
	if o.err != nil {
		return nil, o.err
	}
	o.held++
	return func() { o.mu.Lock(); o.held--; o.mu.Unlock() }, nil
}

type reloaderLog struct {
	n   int
	err error
}

func (r *reloaderLog) DaemonReload(ctx context.Context) error { r.n++; return r.err }

type loaderLog struct {
	ids []string
	err error
}

func (l *loaderLog) LoadHeld(ctx context.Context, id string) error {
	l.ids = append(l.ids, id)
	return l.err
}

// ---- a generator over a world ----

type world struct {
	*lab
	ini    *memIni
	alerts *alertLog
	pub    *pubLog
	sys    *reloaderLog
	router *scriptRouter
	ops    *opsLock
	loader *loaderLog
	gen    *Generator
	clock  *stepClock
}

// stepClock is a settable clock.
type stepClock struct{ t time.Time }

func (c *stepClock) Now() time.Time                        { return c.t }
func (c *stepClock) NewTicker(time.Duration) config.Ticker { panic("not used") }
func (c *stepClock) advance(d time.Duration)               { c.t = c.t.Add(d) }

// newWorld builds the ai-n3 node with every model installed on the internal root.
func newWorld(t *testing.T) *world {
	t.Helper()
	w := &world{
		lab: newLab(t),
		ini: &memIni{}, alerts: &alertLog{}, pub: &pubLog{}, sys: &reloaderLog{}, loader: &loaderLog{},
		clock: &stepClock{t: fixedTime()},
	}
	return w
}

// build creates the generator; call it after adjusting the world.
func (w *world) build(mut ...func(*GeneratorOptions)) *world {
	w.t.Helper()
	opt := GeneratorOptions{
		Paths:     DefaultPaths(""),
		FS:        w.fs,
		MkdirAll:  w.fs.MkdirAll,
		IniState:  w.ini,
		Systemd:   w.sys,
		Alerts:    w.alerts,
		Publisher: w.pub,
		Clock:     w.clock,
		Version:   "0.1.0",
		Sleep:     func(context.Context, time.Duration) {},
	}
	if w.router != nil {
		opt.Router = w.router
	}
	if w.ops != nil {
		opt.Ops = w.ops
	}
	if w.loader != nil {
		opt.Loader = w.loader
	}
	for _, f := range mut {
		f(&opt)
	}
	g, err := NewGenerator(opt)
	if err != nil {
		w.t.Fatal(err)
	}
	w.gen = g
	return w
}

func (w *world) inputs() Inputs {
	return Inputs{
		NodeID: w.node, Catalog: &catalog.Catalog{Version: "2026.11.02-1", Models: w.models},
		Profile: w.profile, Overrides: w.overrides, Llama: w.llama, Roots: w.roots, Fit: w.fit,
		StateDefault: w.stateDefault, Survival: w.survival,
	}
}

func (w *world) generate() (Result, error) {
	w.t.Helper()
	return w.gen.Generate(context.Background(), w.inputs())
}

func (w *world) mustGenerate() Result {
	w.t.Helper()
	res, err := w.generate()
	if err != nil {
		w.t.Fatalf("Generate: %v", err)
	}
	return res
}

func (w *world) iniText() string {
	w.t.Helper()
	b, ok := w.fs.get(DefaultPaths("").INI)
	if !ok {
		w.t.Fatal("models.ini was not written")
	}
	return string(b)
}

// manifestOnDisk writes a verified MANIFEST.json for m on the real filesystem.
func manifestOnDisk(t *testing.T, root string, m catalog.Model) {
	t.Helper()
	fs := newMemFS()
	installModel(fs, root, m)
	raw, _ := fs.get(modelDir(root, m.ID) + "/MANIFEST.json")
	if err := os.WriteFile(modelDir(root, m.ID)+"/MANIFEST.json", raw, 0o644); err != nil {
		t.Fatal(err)
	}
}
