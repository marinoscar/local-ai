package health

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/events"
	"github.com/marinoscar/local-ai/agent/internal/services"
)

// Defaults of the supervisor (companion-services.md section 4.6).
const (
	// DefaultTick is how often the supervisor wakes. Each service is probed when its own interval
	// (30 s) has passed, and on every tick while it is starting and has not answered yet, so a
	// service that just started is "ok" within one tick of answering.
	DefaultTick = 10 * time.Second
	// DefaultStartingGrace is how long after a start a failing probe only means "starting".
	DefaultStartingGrace = 60 * time.Second
	// DefaultFailureThreshold is how many consecutive liveness failures of an active unit restart it.
	DefaultFailureThreshold = 3
)

// Options configures New. Specs is required; Manager is required when a Spec has a unit.
type Options struct {
	// Manager is the systemd seam (services.NewGuard over the D-Bus or systemctl Manager).
	Manager services.Manager
	// Specs are the supervised services: kiwix, whisper, piper and maps, as their items register them.
	Specs []Spec
	// Publisher receives service.state; *events.Bus implements it. Optional.
	Publisher services.Publisher
	// Mode returns the current mode name; "survival" makes LAI-SVC-051 an error instead of a warning.
	// Nil means never survival.
	Mode func() string
	// RequireServices returns the profile's survival.require_services (short names). Nil means none.
	RequireServices func() []string
	// Metrics receives the restart counter. Optional.
	Metrics *Metrics
	// Clock defaults to the wall clock.
	Clock  config.Clock
	Logger *slog.Logger

	// Tick, StartingGrace, FailureThreshold, MaxRestarts, RestartWindow and ProbeTimeout override the
	// defaults (tests).
	Tick             time.Duration
	StartingGrace    time.Duration
	FailureThreshold int
	MaxRestarts      int
	RestartWindow    time.Duration
	ProbeTimeout     time.Duration
}

type entry struct {
	spec   Spec
	budget *Budget

	initialized bool
	enabled     bool
	content     Content

	st     services.UnitState
	loaded bool
	gen    int // bumped on every start and stop: a probe that straddles one is discarded

	checked    bool // probed since the unit last started
	live       bool // the last probe passed
	failures   int  // consecutive liveness failures that count
	lastCheck  time.Time
	failCode   string
	readiness  Readiness
	startedAt  time.Time // when the unit last started (observed by us or by systemd)
	gaveUp     bool      // the budget refused a restart the service needed
	restarting bool

	published bool // the baseline below is set; the first observation sends no event
	state     State
	active    string
	sub       string
	problems  []Problem
}

// Supervisor is the health supervisor (an app.Worker): it keeps one state record per companion
// service, restarts failed units under a budget, publishes service.state on every change and serves
// the records to GET /services, GET /status, the doctor and the cloud heartbeat.
type Supervisor struct {
	opt   Options
	log   *slog.Logger
	clock config.Clock

	mu      sync.Mutex // guards every entry
	ordered []*entry
	byName  map[string]*entry
	byUnit  map[string]*entry

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// New returns a Supervisor. It refuses a Spec without a name or probe, llama, a duplicate name or
// unit, and a unit-backed Spec without a Manager.
func New(opt Options) (*Supervisor, error) {
	if opt.Tick <= 0 {
		opt.Tick = DefaultTick
	}
	if opt.StartingGrace <= 0 {
		opt.StartingGrace = DefaultStartingGrace
	}
	if opt.FailureThreshold <= 0 {
		opt.FailureThreshold = DefaultFailureThreshold
	}
	if opt.ProbeTimeout <= 0 {
		opt.ProbeTimeout = DefaultProbeTimeout
	}
	s := &Supervisor{opt: opt, clock: opt.Clock, byName: map[string]*entry{}, byUnit: map[string]*entry{}}
	if s.clock == nil {
		s.clock = config.RealClock{}
	}
	l := opt.Logger
	if l == nil {
		l = slog.Default()
	}
	s.log = l.With("component", "health")
	for _, sp := range opt.Specs {
		if err := sp.validate(); err != nil {
			return nil, err
		}
		if _, dup := s.byName[sp.Name]; dup {
			return nil, fmt.Errorf("health: service %q is registered twice", sp.Name)
		}
		e := &entry{spec: sp, budget: NewBudget(opt.MaxRestarts, opt.RestartWindow)}
		if sp.Unit != "" {
			if opt.Manager == nil {
				return nil, fmt.Errorf("health: service %q has a unit and the supervisor has no Manager", sp.Name)
			}
			if _, dup := s.byUnit[sp.Unit]; dup {
				return nil, fmt.Errorf("health: unit %q is supervised twice", sp.Unit)
			}
			s.byUnit[sp.Unit] = e
		}
		s.byName[sp.Name] = e
		s.ordered = append(s.ordered, e)
	}
	sort.SliceStable(s.ordered, func(i, j int) bool { return Order(s.ordered[i].spec.Name) < Order(s.ordered[j].spec.Name) })
	return s, nil
}

// Name implements app.Worker.
func (s *Supervisor) Name() string { return "svc-health" }

// Start implements app.Worker: it subscribes to the units once (Manager.Subscribe) and starts the loop
// that combines the unit changes with the probe tick.
func (s *Supervisor) Start(ctx context.Context) error {
	ctx, s.cancel = context.WithCancel(ctx)
	var ch <-chan services.UnitState
	if len(s.byUnit) > 0 {
		units := make([]string, 0, len(s.byUnit))
		for _, e := range s.ordered {
			if e.spec.Unit != "" {
				units = append(units, e.spec.Unit)
			}
		}
		var err error
		if ch, err = s.opt.Manager.Subscribe(ctx, units); err != nil {
			s.cancel()
			return fmt.Errorf("health: subscribe to the service units: %w", err)
		}
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.loop(ctx, ch)
	}()
	return nil
}

// Stop implements app.Worker: it ends the loop and waits for it within ctx.
func (s *Supervisor) Stop(ctx context.Context) error {
	if s.cancel != nil {
		s.cancel()
	}
	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Supervisor) loop(ctx context.Context, ch <-chan services.UnitState) {
	t := s.clock.NewTicker(s.opt.Tick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case st, ok := <-ch:
			if !ok {
				ch = nil // the subscription ended; the tick carries on
				continue
			}
			s.Observe(ctx, st)
		case <-t.C():
			s.Tick(ctx)
		}
	}
}

// Tick runs one round: every service is checked, and probed when its interval has passed. The loop
// calls it on every tick; tests call it instead of waiting.
func (s *Supervisor) Tick(ctx context.Context) {
	for _, e := range s.ordered {
		if ctx.Err() != nil {
			return
		}
		s.step(ctx, e, false)
	}
}

// CheckNow probes the named services at once (all of them when none is named) and waits for the
// answers.
func (s *Supervisor) CheckNow(ctx context.Context, names ...string) {
	for _, e := range s.ordered {
		if len(names) > 0 && !contains(names, e.spec.Name) {
			continue
		}
		s.step(ctx, e, true)
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

// Observe records a unit's state, as the loop does for every event of Manager.Subscribe, and looks at
// the service at once when its unit just started (probe it) or just failed (restart it under the
// budget). A state of a unit the supervisor does not supervise is ignored.
func (s *Supervisor) Observe(ctx context.Context, st services.UnitState) {
	if e, ok := s.byUnit[st.Unit]; ok {
		s.prime(ctx, e) // the first state of a service is the baseline, not a transition
	}
	if e := s.applyState(st); e != nil {
		s.step(ctx, e, true)
	}
}

// applyState records a unit's state and returns the entry when the supervisor should look at it at
// once: the unit just started, or it is in the failed state.
func (s *Supervisor) applyState(st services.UnitState) *entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.byUnit[st.Unit]
	if !ok {
		return nil
	}
	if s.applyLocked(e, st) {
		return e
	}
	return nil
}

// applyLocked stores st and reports whether the entry needs an immediate step. s.mu must be held.
func (s *Supervisor) applyLocked(e *entry, st services.UnitState) bool {
	prev, had := e.st, e.loaded
	e.st, e.loaded = st, true
	started := st.Active() && (!had || !prev.Active() || !st.ActiveEnterTimestamp.Equal(prev.ActiveEnterTimestamp))
	if !st.Active() || started {
		// A stopped or restarted unit starts over: nothing known about its health holds.
		e.checked, e.live, e.failures, e.failCode, e.readiness = false, false, 0, "", Readiness{}
		e.gen++
	}
	if started {
		e.startedAt = st.ActiveEnterTimestamp
		if e.startedAt.IsZero() {
			e.startedAt = s.clock.Now()
		}
		e.gaveUp = false
	}
	if !st.Active() && st.ActiveState != services.ActiveFailed {
		e.gaveUp = false // stopped on purpose: nothing was left stopped by the budget
	}
	if e.initialized { // before that the enabled flag and the content are unknown: prime sets the baseline
		s.refreshLocked(e, s.clock.Now())
	}
	return started || (st.ActiveState == services.ActiveFailed && (!had || prev.ActiveState != services.ActiveFailed))
}

// load reads the state of a unit the subscription has not reported yet, so a record is never empty
// right after start.
func (s *Supervisor) load(ctx context.Context, e *entry) {
	if e.spec.Unit == "" {
		return
	}
	s.mu.Lock()
	loaded := e.loaded
	s.mu.Unlock()
	if loaded {
		return
	}
	st, err := s.opt.Manager.State(ctx, e.spec.Unit)
	if err != nil {
		return
	}
	s.mu.Lock()
	if !e.loaded {
		s.applyLocked(e, st)
	}
	s.mu.Unlock()
}

// prime loads a record without probing or restarting anything: the enabled flag, the content and the
// unit state. It makes Records answer from the first call.
func (s *Supervisor) prime(ctx context.Context, e *entry) {
	s.mu.Lock()
	done := e.initialized
	s.mu.Unlock()
	if done {
		return
	}
	s.load(ctx, e)
	enabled, content := s.enabledContent(ctx, e)
	s.mu.Lock()
	if !e.initialized {
		e.initialized = true
		s.setEnabledLocked(e, enabled, content)
		s.refreshLocked(e, s.clock.Now())
	}
	s.mu.Unlock()
}

// enabledContent evaluates the two cheap inputs outside the lock.
func (s *Supervisor) enabledContent(ctx context.Context, e *entry) (bool, Content) {
	if !e.spec.enabled() {
		return false, Content{}
	}
	cctx, cancel := context.WithTimeout(ctx, s.opt.ProbeTimeout)
	defer cancel()
	return true, e.spec.Probe.Content(cctx)
}

func (s *Supervisor) setEnabledLocked(e *entry, enabled bool, content Content) {
	if !enabled && e.enabled {
		e.checked, e.live, e.failures, e.failCode, e.gaveUp, e.readiness = false, false, 0, "", false, Readiness{}
		e.gen++
	}
	e.enabled, e.content = enabled, content
	if content.Missing {
		e.gaveUp = false
	}
}

// step looks at one service: it re-reads the enabled flag and the content, runs the liveness and
// readiness probes when they are due (always when force is set), restarts the unit when the rules
// say so, and publishes the state change, if any, once.
func (s *Supervisor) step(ctx context.Context, e *entry, force bool) {
	s.prime(ctx, e)
	enabled, content := s.enabledContent(ctx, e)

	s.mu.Lock()
	s.setEnabledLocked(e, enabled, content)
	unitless := e.spec.Unit == ""
	now := s.clock.Now()
	due := enabled && !content.Missing && (unitless || e.st.Active()) &&
		(force || !e.checked || (e.state == StateStarting && !e.live) || now.Sub(e.lastCheck) >= e.spec.interval()-s.opt.Tick/2)
	gen := e.gen
	s.mu.Unlock()

	var (
		livenessErr error
		readiness   Readiness
		at          time.Time
	)
	if due {
		at = s.clock.Now() // the answer is dated when the probe started
		lctx, cancel := context.WithTimeout(ctx, s.opt.ProbeTimeout)
		livenessErr = e.spec.Probe.Liveness(lctx)
		if livenessErr == nil {
			readiness = e.spec.Probe.Readiness(lctx)
		}
		cancel()
		if ctx.Err() != nil {
			return
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if due && e.gen == gen {
		s.recordLiveness(e, at, livenessErr, readiness)
	}
	if reason := s.restartReason(e); reason != "" {
		s.tryRestart(ctx, e, reason)
	}
	s.refreshLocked(e, s.clock.Now())
}

// recordLiveness stores a probe answer. A failure within the starting grace does not count. s.mu
// must be held.
func (s *Supervisor) recordLiveness(e *entry, at time.Time, err error, rd Readiness) {
	e.checked, e.lastCheck = true, at
	if err == nil {
		e.live, e.failures, e.failCode, e.gaveUp, e.readiness = true, 0, "", false, rd
		return
	}
	e.live, e.failCode, e.readiness = false, codeOf(err), Readiness{}
	if e.spec.Unit != "" && at.Sub(e.startedAt) < s.opt.StartingGrace {
		return
	}
	e.failures++
	s.log.Debug("service liveness probe failed", "service", e.spec.Name, "failures", e.failures, "error", err.Error())
}

// restartReason says why the supervisor should restart the unit now, or "" when it should not: the
// unit is in systemd's failed state, or it is active and failed the threshold of liveness probes in a
// row. A disabled service, one with missing content, one without a unit and one being restarted are
// never restarted. s.mu must be held.
func (s *Supervisor) restartReason(e *entry) string {
	if !e.enabled || e.content.Missing || e.spec.Unit == "" || e.restarting || !e.loaded {
		return ""
	}
	switch {
	case e.st.ActiveState == services.ActiveFailed:
		return "unit_failed"
	case e.st.Active() && e.checked && !e.live && e.failures >= s.opt.FailureThreshold:
		return "liveness"
	}
	return ""
}

// tryRestart restarts the unit when the budget allows it, and otherwise gives up (LAI-SVC-051). It
// releases s.mu during the systemd call and holds it again on return.
func (s *Supervisor) tryRestart(ctx context.Context, e *entry, reason string) {
	now := s.clock.Now()
	if !e.budget.Allow(now) {
		if !e.gaveUp {
			e.gaveUp = true
			s.log.Error("service keeps failing and was left stopped", "service", e.spec.Name, "unit", e.spec.Unit,
				"code", CodeGaveUp, "reason", reason, "restarts", e.budget.Count(now))
		}
		return
	}
	failures := e.failures
	e.restarting = true
	s.mu.Unlock()
	err := s.opt.Manager.Restart(ctx, e.spec.Unit)
	var st services.UnitState
	var stErr error
	if err == nil {
		st, stErr = s.opt.Manager.State(ctx, e.spec.Unit)
	}
	s.mu.Lock()
	e.restarting = false
	now = s.clock.Now()
	e.budget.Record(now) // a failed attempt counts too
	s.opt.Metrics.ServiceRestart(e.spec.Name)
	if err != nil {
		s.log.Warn("service restart failed", "service", e.spec.Name, "unit", e.spec.Unit, "reason", reason, "error", err.Error())
		return
	}
	s.log.Warn("service restarted", "service", e.spec.Name, "code", CodeRestarted, "failures", failures, "reason", reason)
	e.checked, e.live, e.failures, e.failCode, e.readiness = false, false, 0, "", Readiness{}
	e.startedAt = now
	e.gen++
	if stErr == nil {
		s.applyLocked(e, st)
	}
}

// derive computes the health state of an entry. s.mu must be held.
func (s *Supervisor) derive(e *entry) State {
	switch {
	case !e.enabled:
		return StateDisabled
	case e.content.Missing:
		return StateMissingContent
	case e.spec.Unit == "":
		switch {
		case !e.checked:
			return StateStarting
		case !e.live:
			return StateDown
		}
		return s.readyState(e)
	}
	switch {
	case e.st.ActiveState == services.ActiveActivating || e.st.ActiveState == services.ActiveReloading:
		return StateStarting
	case !e.st.Active():
		return StateDown
	case !e.checked:
		return StateStarting
	case e.live:
		return s.readyState(e)
	case s.clock.Now().Sub(e.startedAt) < s.opt.StartingGrace:
		return StateStarting
	case e.failures >= s.opt.FailureThreshold:
		return StateDown
	}
	return StateDegraded
}

// readyState is ok, or degraded when the readiness reported a problem of warning or worse.
func (s *Supervisor) readyState(e *entry) State {
	for _, p := range e.readiness.Problems {
		if p.Severity.Rank() >= doctor.Warning.Rank() {
			return StateDegraded
		}
	}
	return StateOK
}

// defaultSeverity is the table severity of a code (error for one the table does not know).
func defaultSeverity(code string) doctor.Severity {
	if c, ok := doctor.Lookup(code); ok && c.Severity.Valid() {
		return c.Severity
	}
	return doctor.Error
}

// gaveUpSeverity is the severity of LAI-SVC-051: an error in survival mode, a warning otherwise.
func (s *Supervisor) gaveUpSeverity() doctor.Severity {
	if s.opt.Mode != nil && s.opt.Mode() == ModeSurvival {
		return doctor.Error
	}
	return doctor.Warning
}

// problemsFor lists the findings of an entry in the given state: the primary problem first, then by
// severity. s.mu must be held.
func (s *Supervisor) problemsFor(e *entry, state State, now time.Time) []Problem {
	ps := []Problem{}
	seen := map[string]bool{}
	add := func(code string, sev doctor.Severity) {
		if code != "" && !seen[code] {
			seen[code] = true
			ps = append(ps, Problem{Code: code, Severity: sev})
		}
	}
	switch state {
	case StateDisabled:
		return ps
	case StateMissingContent:
		add(e.content.Code, defaultSeverity(e.content.Code))
		return ps
	case StateDown:
		if e.gaveUp {
			add(CodeGaveUp, s.gaveUpSeverity())
		}
		code := e.failCode
		if code == "" {
			code = e.spec.DownCode
		}
		add(code, defaultSeverity(code))
	}
	if e.checked && e.live {
		for _, p := range e.readiness.Problems {
			add(p.Code, p.Severity)
		}
	}
	if e.budget.Count(now) > 0 {
		add(CodeRestarted, doctor.Info)
	}
	sort.SliceStable(ps, func(i, j int) bool { return ps[i].Severity.Rank() > ps[j].Severity.Rank() })
	if state == StateDown && e.gaveUp {
		// The primary problem of a service the budget gave up on is LAI-SVC-051, whatever the
		// severity the mode gives it: the event and the status row carry the first problem.
		for i, p := range ps {
			if p.Code == CodeGaveUp {
				copy(ps[1:i+1], ps[:i])
				ps[0] = p
				break
			}
		}
	}
	return ps
}

// refreshLocked recomputes the state and problems of an entry and publishes service.state when the
// health state changed: one event per transition, carrying the systemd active and sub states at that
// moment (a change of those alone is not a transition). The first observation sets the baseline and
// publishes nothing. s.mu must be held; the bus never blocks, so publishing under the lock keeps the
// events in order.
func (s *Supervisor) refreshLocked(e *entry, now time.Time) {
	if e.spec.Unit != "" && !e.loaded {
		return // the state of the unit is unknown: say nothing yet
	}
	state := s.derive(e)
	problems := s.problemsFor(e, state, now)
	if state == StateOK || state == StateDegraded {
		e.gaveUp = false
	}
	active, sub := "", ""
	if e.spec.Unit != "" {
		active, sub = e.st.ActiveState, e.st.SubState
	}
	prev := e.state
	publish := e.published && e.state != state
	e.published, e.state, e.active, e.sub, e.problems = true, state, active, sub, problems
	if !publish {
		return
	}
	ev := events.ServiceState{Name: e.spec.Name, Unit: e.spec.Unit, From: string(prev), To: string(state),
		Active: active, Sub: sub, Healthy: state == StateOK}
	if state != StateOK && len(problems) > 0 {
		ev.Code = problems[0].Code
	}
	s.log.Info("service state changed", "service", ev.Name, "from", ev.From, "to", ev.To, "code", ev.Code)
	if s.opt.Publisher != nil {
		if err := s.opt.Publisher.Publish(events.TypeServiceState, "", ev); err != nil {
			s.log.Warn("service.state event refused", "error", err.Error())
		}
	}
}

// Records returns the record of every registered service, in the order kiwix, whisper, piper, maps.
// It reads cached values only; a service the subscription has not reported yet is read from systemd
// first.
func (s *Supervisor) Records(ctx context.Context) []Record {
	for _, e := range s.ordered {
		s.prime(ctx, e)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Record, 0, len(s.ordered))
	for _, e := range s.ordered {
		out = append(out, s.recordLocked(e))
	}
	return out
}

func (s *Supervisor) recordLocked(e *entry) Record {
	r := Record{
		Name: e.spec.Name, Unit: e.spec.Unit, State: e.state,
		Healthy: e.state == StateOK, LastCheck: e.lastCheck,
		Problems: append([]Problem{}, e.problems...),
	}
	if r.State == "" {
		r.State = StateStarting
	}
	if e.spec.Unit != "" {
		r.Active, r.Sub = e.st.ActiveState, e.st.SubState
		r.Since, r.Restarts, r.Enabled = e.st.Since(), e.st.NRestarts, e.st.Enabled()
		if !e.loaded {
			r.Active, r.Sub, r.State = "unknown", "unknown", StateDown
		}
		if !e.st.ExecMainExitTimestamp.IsZero() {
			r.LastExit = &services.LastExit{Code: e.st.ExecMainStatus, At: e.st.ExecMainExitTimestamp}
		}
	}
	if e.checked && e.live && e.enabled && !e.content.Missing {
		rd := e.readiness
		r.Detail, r.Model, r.Device = rd.Detail, rd.Model, rd.Device
		// nil stays nil: it means "does not apply to this service".
		if rd.Books != nil {
			r.Books = append([]Book{}, rd.Books...)
		}
		if rd.Voices != nil {
			r.Voices = append([]string{}, rd.Voices...)
		}
		if rd.Maps != nil {
			r.Maps = append([]string{}, rd.Maps...)
		}
	}
	return r
}

// Statuses returns the records as services.Status rows, the form of the /status services array and of
// services.Watcher.Snapshot. Health carries the six states; Code is the first problem of a down service.
func (s *Supervisor) Statuses(ctx context.Context) []services.Status {
	recs := s.Records(ctx)
	out := make([]services.Status, 0, len(recs))
	for _, r := range recs {
		st := services.Status{
			Name: r.Name, Unit: r.Unit, Active: r.Active, Sub: r.Sub, Since: r.Since, Restarts: r.Restarts,
			Healthy: r.Healthy, Health: string(r.State), Enabled: r.Enabled, LastExit: r.LastExit,
		}
		if r.State == StateDown && len(r.Problems) > 0 {
			st.Code = r.Problems[0].Code
		}
		out = append(out, st)
	}
	return out
}

// Snapshot implements the status sources of internal/api and internal/statusapi for the services this
// supervisor owns; Combine merges it with services.Watcher's llama row.
func (s *Supervisor) Snapshot(ctx context.Context) []services.Status { return s.Statuses(ctx) }

// Reduced returns the services as the cloud heartbeat carries them: name and state only.
func (s *Supervisor) Reduced(ctx context.Context) []Reduced {
	recs := s.Records(ctx)
	out := make([]Reduced, 0, len(recs))
	for _, r := range recs {
		out = append(out, Reduced{Name: r.Name, State: r.State})
	}
	return out
}

// Flagged returns, in survival mode, the names of the profile's survival.require_services that are
// down or missing_content, for the Portal status strip (companion-services.md section 4.6). Outside
// survival mode it returns nothing. A required name this supervisor does not know is not flagged here
// (llama's own state is the router's).
func (s *Supervisor) Flagged(ctx context.Context) []string {
	out := []string{}
	if s.opt.Mode == nil || s.opt.Mode() != ModeSurvival || s.opt.RequireServices == nil {
		return out
	}
	byName := map[string]State{}
	for _, r := range s.Records(ctx) {
		byName[r.Name] = r.State
	}
	seen := map[string]bool{}
	for _, name := range s.opt.RequireServices() {
		st, ok := byName[name]
		if !ok || seen[name] {
			continue
		}
		seen[name] = true
		if st == StateDown || st == StateMissingContent {
			out = append(out, name)
		}
	}
	return out
}

// DeepResult is the outcome of one service's deep check.
type DeepResult struct {
	Service  string
	Unit     string
	Problems []Problem
}

// Deep runs the deep check of every service that is up (ok or degraded): the slow checks of
// companion-services.md section 4.6, run on demand by GET /doctor, aictl doctor and the drill, never
// by the tick. A service that is down, disabled or without content is skipped: its record already
// says so.
func (s *Supervisor) Deep(ctx context.Context) []DeepResult {
	var out []DeepResult
	for _, r := range s.Records(ctx) {
		if r.State != StateOK && r.State != StateDegraded {
			continue
		}
		e := s.byName[r.Name]
		if ctx.Err() != nil {
			break
		}
		out = append(out, DeepResult{Service: r.Name, Unit: r.Unit, Problems: e.spec.Probe.Deep(ctx)})
	}
	return out
}
