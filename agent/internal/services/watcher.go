package services

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/config"
)

// Health states of a service (companion-services.md section 4.6). The watcher reports the four that
// follow from systemd and the health probe; missing_content and disabled belong to the companion
// services' own supervisor.
const (
	// HealthOK: active and answering.
	HealthOK = "ok"
	// HealthStarting: the first minute after a start, or active and not probed yet; failures do not count.
	HealthStarting = "starting"
	// HealthDegraded: active, and the last probe failed less than a minute ago.
	HealthDegraded = "degraded"
	// HealthDown: not running, or active and not answering for over a minute (then with a code).
	HealthDown = "down"
)

// Defaults of the watcher.
const (
	// DefaultUnhealthyAfter is how long a service may be active and not answering before its "not
	// answering" code is raised (agent.md section 4.3.3).
	DefaultUnhealthyAfter = 60 * time.Second
	// DefaultStartingGrace is the time after a start during which a failing probe only means "starting".
	DefaultStartingGrace = 60 * time.Second
	// DefaultHealthTick is how often the health loop wakes; each service is probed when its own
	// interval has passed (15 s for llama, 30 s for the others).
	DefaultHealthTick = 15 * time.Second
)

// LastExit is how the unit's main process last ended.
type LastExit struct {
	Code int
	At   time.Time
}

// Status is the watcher's view of one service: the row of GET /services and of the services array of
// GET /status (agent.md sections 3.8.4 and 3.8.22).
type Status struct {
	// Name and Unit identify the service.
	Name, Unit string
	// Active and Sub are systemd's ActiveState and SubState.
	Active, Sub string
	// Since is when the unit became active; zero when it is not running.
	Since time.Time
	// Restarts is systemd's NRestarts.
	Restarts int
	// Healthy means active and answering.
	Healthy bool
	// Health is one of HealthOK, HealthStarting, HealthDegraded and HealthDown.
	Health string
	// Enabled reports whether the unit starts at boot.
	Enabled bool
	// LastExit is nil when the main process never exited.
	LastExit *LastExit
	// Code is the "not answering" doctor code while Health is HealthDown because of an unanswered probe.
	Code string
}

// StateEvent is the payload of the service.state event (agent.md section 3.10).
type StateEvent struct {
	Name    string `json:"name"`
	Unit    string `json:"unit"`
	From    string `json:"from"`
	To      string `json:"to"`
	Active  string `json:"active"`
	Sub     string `json:"sub"`
	Healthy bool   `json:"healthy"`
	Code    string `json:"code,omitempty"`
}

// Publisher receives service.state events. AGENT-08's event bus implements it.
type Publisher interface {
	PublishServiceState(StateEvent)
}

// AlertSink is the alert manager hook (AGENT-11): Raise reports a doctor code for a service, Clear
// withdraws it. Raise is called once per problem, not on every probe.
type AlertSink interface {
	Raise(code, service string, details map[string]any)
	Clear(code, service string)
}

// WatcherOptions configures NewWatcher. Manager and Prober are required.
type WatcherOptions struct {
	Manager Manager
	Prober  Prober
	// Ports are the loopback ports the probes use.
	Ports Ports
	// Publisher and Alerts are optional.
	Publisher Publisher
	Alerts    AlertSink
	// Clock defaults to the wall clock.
	Clock  config.Clock
	Logger *slog.Logger
	// UnhealthyAfter, StartingGrace and Tick override the defaults (tests).
	UnhealthyAfter time.Duration
	StartingGrace  time.Duration
	Tick           time.Duration
}

type entry struct {
	svc   Service
	probe Probe

	st     UnitState
	loaded bool

	checked        bool // probed since the unit last became active
	healthy        bool // the last probe passed
	lastCheck      time.Time
	unhealthySince time.Time

	published bool   // the baseline below is set; the first observation sends no event
	health    string // the last health state
	active    string
	sub       string
	code      string // the "not answering" code while down because of it
	alerted   string // the code currently raised with the alert sink
}

// Watcher is the service watcher worker (an app.Worker): it subscribes to the three service units,
// probes their health every 15 or 30 seconds and at every state change, tracks restarts and the last
// exit, publishes service.state on change and reports a service that stays active but unanswering for
// over a minute to the alert manager.
type Watcher struct {
	opt     WatcherOptions
	log     *slog.Logger
	clock   config.Clock
	byUnit  map[string]*entry
	ordered []*entry

	mu sync.Mutex // guards every entry

	kick   chan string
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewWatcher returns a Watcher for the controlled services (llama, kiwix, whisper).
func NewWatcher(opt WatcherOptions) *Watcher {
	if opt.UnhealthyAfter <= 0 {
		opt.UnhealthyAfter = DefaultUnhealthyAfter
	}
	if opt.StartingGrace <= 0 {
		opt.StartingGrace = DefaultStartingGrace
	}
	if opt.Tick <= 0 {
		opt.Tick = DefaultHealthTick
	}
	w := &Watcher{opt: opt, clock: opt.Clock, byUnit: map[string]*entry{}, kick: make(chan string, 16)}
	if w.clock == nil {
		w.clock = config.RealClock{}
	}
	l := opt.Logger
	if l == nil {
		l = slog.Default()
	}
	w.log = l.With("component", "services")
	probes := map[string]Probe{}
	for _, p := range Probes(opt.Ports) {
		probes[p.Service] = p
	}
	for _, s := range Controlled {
		e := &entry{svc: s, probe: probes[s.Name]}
		w.byUnit[s.Unit] = e
		w.ordered = append(w.ordered, e)
	}
	return w
}

// Name implements app.Worker.
func (w *Watcher) Name() string { return "services" }

// Start implements app.Worker: it subscribes to the units and starts the state and health loops.
func (w *Watcher) Start(ctx context.Context) error {
	units := make([]string, 0, len(w.ordered))
	for _, e := range w.ordered {
		units = append(units, e.svc.Unit)
	}
	ctx, w.cancel = context.WithCancel(ctx)
	ch, err := w.opt.Manager.Subscribe(ctx, units)
	if err != nil {
		w.cancel()
		return fmt.Errorf("services: subscribe to the service units: %w", err)
	}
	w.wg.Add(2)
	go func() {
		defer w.wg.Done()
		w.stateLoop(ctx, ch)
	}()
	go func() {
		defer w.wg.Done()
		w.healthLoop(ctx)
	}()
	return nil
}

// Stop implements app.Worker: it ends the loops and waits for them within ctx.
func (w *Watcher) Stop(ctx context.Context) error {
	if w.cancel != nil {
		w.cancel()
	}
	done := make(chan struct{})
	go func() { w.wg.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (w *Watcher) stateLoop(ctx context.Context, ch <-chan UnitState) {
	for {
		select {
		case <-ctx.Done():
			return
		case st, ok := <-ch:
			if !ok {
				return
			}
			w.apply(st)
		}
	}
}

func (w *Watcher) healthLoop(ctx context.Context) {
	t := w.clock.NewTicker(w.opt.Tick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case unit := <-w.kick:
			w.check(ctx, unit)
		case <-t.C():
			w.checkDue(ctx)
		}
	}
}

// checkDue probes every active service whose interval has passed.
func (w *Watcher) checkDue(ctx context.Context) {
	now := w.clock.Now()
	var due []string
	w.mu.Lock()
	for _, e := range w.ordered {
		if !e.st.Active() {
			continue
		}
		// Half a tick of slack: a ticker that fires a hair early must not push a probe a whole tick later.
		if !e.checked || now.Sub(e.lastCheck) >= e.probe.Interval-w.opt.Tick/2 {
			due = append(due, e.svc.Unit)
		}
	}
	w.mu.Unlock()
	for _, u := range due {
		w.check(ctx, u)
	}
}

// apply records a unit's state, publishes the change and asks the health loop for a probe when the
// unit has just become active.
func (w *Watcher) apply(st UnitState) {
	w.mu.Lock()
	e, ok := w.byUnit[st.Unit]
	if !ok {
		w.mu.Unlock()
		return
	}
	wasActive, prevEnter := e.st.Active(), e.st.ActiveEnterTimestamp
	e.st, e.loaded = st, true
	restarted := !st.ActiveEnterTimestamp.Equal(prevEnter)
	if !st.Active() || restarted {
		// A stopped or restarted unit starts over: nothing known about its health holds.
		e.checked, e.healthy, e.unhealthySince = false, false, time.Time{}
	}
	ev, alert := w.recompute(e, w.clock.Now())
	w.mu.Unlock()
	w.emit(e.svc, ev, alert)
	if st.Active() && (!wasActive || restarted) {
		select {
		case w.kick <- st.Unit:
		default: // the next tick probes it
		}
	}
}

// check probes one service and records the answer.
func (w *Watcher) check(ctx context.Context, unit string) {
	w.mu.Lock()
	e, ok := w.byUnit[unit]
	if !ok || !e.st.Active() {
		w.mu.Unlock()
		return
	}
	enter, probe := e.st.ActiveEnterTimestamp, e.probe
	w.mu.Unlock()

	err := w.opt.Prober.Check(ctx, probe)
	if ctx.Err() != nil {
		return
	}

	now := w.clock.Now()
	w.mu.Lock()
	if !e.st.Active() || !e.st.ActiveEnterTimestamp.Equal(enter) {
		// The unit changed state while the probe ran: its answer says nothing about the new process.
		w.mu.Unlock()
		return
	}
	e.checked, e.lastCheck, e.healthy = true, now, err == nil
	if err == nil {
		e.unhealthySince = time.Time{}
	} else if e.unhealthySince.IsZero() {
		e.unhealthySince = now
	}
	ev, alert := w.recompute(e, now)
	w.mu.Unlock()
	if err != nil {
		w.log.Debug("service health probe failed", "unit", unit, "error", err.Error())
	}
	w.emit(e.svc, ev, alert)
}

// alertChange is what recompute asks the caller to tell the alert sink.
type alertChange struct {
	clear, raise string
	details      map[string]any
}

// recompute derives the service's health state and returns the event to publish (nil when nothing
// changed, or on the first observation) and the alert change. w.mu must be held.
func (w *Watcher) recompute(e *entry, now time.Time) (*StateEvent, alertChange) {
	var health, code string
	switch {
	case e.st.ActiveState == ActiveActivating:
		health = HealthStarting
	case !e.st.Active():
		health = HealthDown
	case !e.checked:
		health = HealthStarting
	case e.healthy:
		health = HealthOK
	case now.Sub(e.st.ActiveEnterTimestamp) < w.opt.StartingGrace:
		health = HealthStarting
	case !e.unhealthySince.IsZero() && now.Sub(e.unhealthySince) >= w.opt.UnhealthyAfter:
		health, code = HealthDown, NotAnsweringCode(e.svc.Name)
	default:
		health = HealthDegraded
	}
	e.code = code

	var alert alertChange
	if code != e.alerted {
		alert.clear, alert.raise = e.alerted, code
		if code != "" {
			alert.details = map[string]any{"unit": e.svc.Unit, "unhealthy_s": int(now.Sub(e.unhealthySince) / time.Second)}
		}
		e.alerted = code
	}

	var ev *StateEvent
	changed := e.health != health || e.active != e.st.ActiveState || e.sub != e.st.SubState
	if e.published && changed {
		ev = &StateEvent{
			Name: e.svc.Name, Unit: e.svc.Unit, From: e.health, To: health,
			Active: e.st.ActiveState, Sub: e.st.SubState, Healthy: health == HealthOK, Code: code,
		}
	}
	e.published, e.health, e.active, e.sub = true, health, e.st.ActiveState, e.st.SubState
	return ev, alert
}

// emit publishes the event and tells the alert sink, outside the lock.
func (w *Watcher) emit(svc Service, ev *StateEvent, a alertChange) {
	if a.clear != "" && w.opt.Alerts != nil {
		w.opt.Alerts.Clear(a.clear, svc.Name)
	}
	if a.raise != "" {
		w.log.Warn("service is active but not answering", "code", a.raise, "unit", svc.Unit)
		if w.opt.Alerts != nil {
			w.opt.Alerts.Raise(a.raise, svc.Name, a.details)
		}
	}
	if ev == nil {
		return
	}
	w.log.Info("service state changed", "unit", ev.Unit, "from", ev.From, "to", ev.To, "active", ev.Active, "sub", ev.Sub)
	if w.opt.Publisher != nil {
		w.opt.Publisher.PublishServiceState(*ev)
	}
}

// CheckNow probes the named services at once (all of them when none is named) and waits for the
// answers. The API calls it after a start or restart so that the next GET shows the truth, and tests
// use it instead of waiting for the ticker.
func (w *Watcher) CheckNow(ctx context.Context, names ...string) {
	for _, e := range w.ordered {
		if len(names) > 0 && !contains(names, e.svc.Name) {
			continue
		}
		w.check(ctx, e.svc.Unit)
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// Snapshot returns the status of the three services, in the order of Controlled. A service the
// subscription has not reported yet is read from the Manager first, so the answer is never empty
// right after start.
func (w *Watcher) Snapshot(ctx context.Context) []Status {
	for _, e := range w.ordered {
		w.mu.Lock()
		loaded := e.loaded
		w.mu.Unlock()
		if loaded {
			continue
		}
		if st, err := w.opt.Manager.State(ctx, e.svc.Unit); err == nil {
			w.apply(st)
		}
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]Status, 0, len(w.ordered))
	for _, e := range w.ordered {
		out = append(out, e.status())
	}
	return out
}

// status builds the public view of an entry. w.mu must be held.
func (e *entry) status() Status {
	s := Status{
		Name: e.svc.Name, Unit: e.svc.Unit,
		Active: e.st.ActiveState, Sub: e.st.SubState,
		Since: e.st.Since(), Restarts: e.st.NRestarts,
		Healthy: e.health == HealthOK, Health: e.health,
		Enabled: e.st.Enabled(), Code: e.code,
	}
	if !e.loaded {
		s.Active, s.Sub, s.Health = "unknown", "unknown", HealthDown
	}
	if !e.st.ExecMainExitTimestamp.IsZero() {
		s.LastExit = &LastExit{Code: e.st.ExecMainStatus, At: e.st.ExecMainExitTimestamp}
	}
	return s
}
