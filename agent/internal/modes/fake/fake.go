// Package fake holds the test doubles of internal/modes: scriptable stand-ins for the cloud link,
// sync, the prober, Tailscale, the model loader, systemd, the Portal session store, the event bus and
// the audit log. Each records its calls in one shared Recorder, so a test can assert the exact order
// of side effects across dependencies, and each call can be scripted to succeed, fail, panic, return
// warnings or block. None of them touches a real unit, a tailnet or a model.
package fake

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/audit"
	"github.com/marinoscar/local-ai/agent/internal/modes"
	"github.com/marinoscar/local-ai/agent/internal/services"
)

var (
	_ modes.LinkController      = (*Link)(nil)
	_ modes.SyncController      = (*Sync)(nil)
	_ modes.Prober              = (*Prober)(nil)
	_ modes.TailscaleController = (*Tailscale)(nil)
	_ modes.ModelLoader         = (*Models)(nil)
	_ modes.ServiceStarter      = (*Services)(nil)
	_ modes.PortalSessions      = (*Portal)(nil)
	_ modes.Publisher           = (*Events)(nil)
	_ modes.AuditWriter         = (*Audit)(nil)
)

// Recorder is the shared, ordered log of calls ("link.stop", "tailscale.off", ...).
type Recorder struct {
	mu    sync.Mutex
	calls []string
}

// Add appends a call.
func (r *Recorder) Add(call string) {
	r.mu.Lock()
	r.calls = append(r.calls, call)
	r.mu.Unlock()
}

// Calls returns the recorded calls, oldest first.
func (r *Recorder) Calls() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.calls...)
}

// Reset forgets the calls.
func (r *Recorder) Reset() {
	r.mu.Lock()
	r.calls = nil
	r.mu.Unlock()
}

// Count returns how many recorded calls equal call.
func (r *Recorder) Count(call string) int {
	n := 0
	for _, c := range r.Calls() {
		if c == call {
			n++
		}
	}
	return n
}

// Behavior scripts one fake method. The zero value succeeds at once.
type Behavior struct {
	mu sync.Mutex
	// Err is returned by the call.
	Err error
	// Warnings are returned by calls that return warnings.
	Warnings []modes.Warning
	// Panic, when non-nil, is panicked with.
	Panic any
	// Hook runs first, with the behavior unlocked; it may block (honoring ctx) to hold a step open.
	// A non-nil error is returned instead of Err.
	Hook func(ctx context.Context) error
}

// Set replaces the scripted outcome under the lock.
func (b *Behavior) Set(err error, ws ...modes.Warning) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.Err, b.Warnings, b.Panic = err, ws, nil
}

// SetPanic makes the call panic with v.
func (b *Behavior) SetPanic(v any) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.Panic = v
}

// SetHook installs a hook.
func (b *Behavior) SetHook(h func(ctx context.Context) error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.Hook = h
}

func (b *Behavior) run(ctx context.Context) ([]modes.Warning, error) {
	b.mu.Lock()
	hook, pnc, err, ws := b.Hook, b.Panic, b.Err, append([]modes.Warning(nil), b.Warnings...)
	b.mu.Unlock()
	if hook != nil {
		if herr := hook(ctx); herr != nil {
			return nil, herr
		}
	}
	if pnc != nil {
		panic(pnc)
	}
	return ws, err
}

// Link is a modes.LinkController.
type Link struct {
	Rec           *Recorder
	StopB, StartB Behavior
}

// Stop implements modes.LinkController.
func (l *Link) Stop(ctx context.Context) error {
	l.Rec.Add("link.stop")
	_, err := l.StopB.run(ctx)
	return err
}

// Start implements modes.LinkController.
func (l *Link) Start(ctx context.Context) error {
	l.Rec.Add("link.start")
	_, err := l.StartB.run(ctx)
	return err
}

// Sync is a modes.SyncController.
type Sync struct {
	Rec     *Recorder
	CancelB Behavior
}

// CancelGraceful implements modes.SyncController.
func (s *Sync) CancelGraceful(ctx context.Context) error {
	s.Rec.Add("sync.cancel")
	_, err := s.CancelB.run(ctx)
	return err
}

// ScheduleCatchup implements modes.SyncController.
func (s *Sync) ScheduleCatchup(in time.Duration) { s.Rec.Add("sync.catchup " + in.String()) }

// Prober is a modes.Prober.
type Prober struct{ Rec *Recorder }

// Start implements modes.Prober.
func (p *Prober) Start() { p.Rec.Add("prober.start") }

// Stop implements modes.Prober.
func (p *Prober) Stop() { p.Rec.Add("prober.stop") }

// Tailscale is a modes.TailscaleController.
type Tailscale struct {
	Rec       *Recorder
	OnB, OffB Behavior
}

// On implements modes.TailscaleController.
func (t *Tailscale) On(ctx context.Context) ([]modes.Warning, error) {
	t.Rec.Add("tailscale.on")
	return t.OnB.run(ctx)
}

// Off implements modes.TailscaleController.
func (t *Tailscale) Off(ctx context.Context) ([]modes.Warning, error) {
	t.Rec.Add("tailscale.off")
	return t.OffB.run(ctx)
}

// Models is a modes.ModelLoader. It records the skip list of every call.
type Models struct {
	Rec   *Recorder
	LoadB Behavior

	mu    sync.Mutex
	skips [][]string
}

// LoadSurvivalSet implements modes.ModelLoader.
func (m *Models) LoadSurvivalSet(ctx context.Context, skip []string) ([]modes.Warning, error) {
	m.mu.Lock()
	m.skips = append(m.skips, append([]string(nil), skip...))
	m.mu.Unlock()
	m.Rec.Add("models.load")
	return m.LoadB.run(ctx)
}

// Skips returns the skip list of each call, oldest first.
func (m *Models) Skips() [][]string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([][]string(nil), m.skips...)
}

// Services is a modes.ServiceStarter. Fail makes the start of a service (by short name) fail.
type Services struct {
	Rec *Recorder

	mu   sync.Mutex
	fail map[string]error
	Hook func(ctx context.Context, name string) error
}

// Fail makes starting the named service return err (nil clears it).
func (s *Services) Fail(name string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail == nil {
		s.fail = map[string]error{}
	}
	if err == nil {
		delete(s.fail, name)
		return
	}
	s.fail[name] = err
}

// Do implements modes.ServiceStarter.
func (s *Services) Do(ctx context.Context, svc services.Service, action services.Action) (services.UnitState, error) {
	s.Rec.Add(fmt.Sprintf("service.%s %s", action, svc.Name))
	s.mu.Lock()
	err, hook := s.fail[svc.Name], s.Hook
	s.mu.Unlock()
	if hook != nil {
		if herr := hook(ctx, svc.Name); herr != nil {
			return services.UnitState{}, herr
		}
	}
	if err != nil {
		return services.UnitState{}, err
	}
	return services.UnitState{Unit: svc.Unit, ActiveState: services.ActiveActive, SubState: "running"}, nil
}

// Portal is a modes.PortalSessions that counts DropAll calls.
type Portal struct {
	Rec *Recorder

	mu    sync.Mutex
	drops int
}

// DropAll implements modes.PortalSessions.
func (p *Portal) DropAll() {
	p.mu.Lock()
	p.drops++
	p.mu.Unlock()
	p.Rec.Add("portal.drop")
}

// Drops returns how many times DropAll was called.
func (p *Portal) Drops() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.drops
}

// Published is one event the Events fake saw.
type Published struct {
	Type string
	OpID string
	Data any
}

// Events is a modes.Publisher that records what it is given.
type Events struct {
	mu  sync.Mutex
	all []Published
}

// Publish implements modes.Publisher.
func (e *Events) Publish(typ, opID string, data any) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.all = append(e.all, Published{Type: typ, OpID: opID, Data: data})
	return nil
}

// All returns every published event, oldest first.
func (e *Events) All() []Published {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]Published(nil), e.all...)
}

// OfType returns the events of one type.
func (e *Events) OfType(typ string) []Published {
	var out []Published
	for _, p := range e.All() {
		if p.Type == typ {
			out = append(out, p)
		}
	}
	return out
}

// Audit is a modes.AuditWriter that records what it is given.
type Audit struct {
	mu  sync.Mutex
	evs []audit.Event
	Err error
}

// Write implements modes.AuditWriter.
func (a *Audit) Write(ev audit.Event) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.evs = append(a.evs, ev)
	return a.Err
}

// Events returns the audit records, oldest first.
func (a *Audit) Events() []audit.Event {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]audit.Event(nil), a.evs...)
}

// Env bundles one of each fake over one Recorder.
type Env struct {
	Rec       *Recorder
	Link      *Link
	Sync      *Sync
	Prober    *Prober
	Tailscale *Tailscale
	Models    *Models
	Services  *Services
	Portal    *Portal
	Events    *Events
	Audit     *Audit
}

// NewEnv returns a fresh Env.
func NewEnv() *Env {
	rec := &Recorder{}
	return &Env{
		Rec: rec, Link: &Link{Rec: rec}, Sync: &Sync{Rec: rec}, Prober: &Prober{Rec: rec},
		Tailscale: &Tailscale{Rec: rec}, Models: &Models{Rec: rec}, Services: &Services{Rec: rec},
		Portal: &Portal{Rec: rec}, Events: &Events{}, Audit: &Audit{},
	}
}

// Deps returns engine dependencies wired to the fakes over the given state store.
func (e *Env) Deps(st modes.StateStore) modes.Deps {
	return modes.Deps{
		State: st, Events: e.Events, Audit: e.Audit, Link: e.Link, Sync: e.Sync, Prober: e.Prober,
		Tailscale: e.Tailscale, Models: e.Models, Services: e.Services, Portal: e.Portal,
	}
}

// Effects returns the recorded calls without the ones that are not effects on the outside world
// (none today); it exists so tests read well.
func (e *Env) Effects() []string { return e.Rec.Calls() }

// Joined renders calls as one string for failure messages.
func Joined(calls []string) string { return strings.Join(calls, ", ") }
