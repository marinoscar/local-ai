package doctor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/events"
)

// DefaultInterval is how often the fast checks run (agent.md section 4.13).
const DefaultInterval = 60 * time.Second

// Alert is an active problem, keyed by its code. Title and Fix are English; In localizes them.
type Alert struct {
	Code     string         `json:"code"`
	Severity Severity       `json:"severity"`
	Title    string         `json:"title"`
	Fix      string         `json:"fix"`
	Since    time.Time      `json:"since"`
	Details  map[string]any `json:"details,omitempty"`
}

// In returns a copy of a with Title and Fix in lang (anything but "es" leaves English).
func (a Alert) In(lang string) Alert {
	if title, fix, ok := Text(a.Code, lang); ok {
		a.Title, a.Fix = title, fix
	}
	return a
}

// Publisher is what the manager needs from the event bus: *events.Bus implements it.
type Publisher interface {
	Publish(eventType, opID string, data any) error
}

// Errors of Raise.
var (
	// ErrUnknownCode: the code is not in the table, or is retired.
	ErrUnknownCode = errors.New("doctor: code is not in the code table or is retired")
	// ErrBadSeverity: WithSeverity got a value other than info, warning, error or critical.
	ErrBadSeverity = errors.New("doctor: invalid severity")
)

// RaiseOption adjusts Raise.
type RaiseOption func(*raiseOpts)

type raiseOpts struct{ severity Severity }

// WithSeverity overrides the default severity of the code, for the codes that escalate or relax
// (LAI-SEC-004 is critical at a lockout, LAI-AGT-003 when defaults were used).
func WithSeverity(s Severity) RaiseOption { return func(o *raiseOpts) { o.severity = s } }

// ManagerOptions configures a Manager.
type ManagerOptions struct {
	// Registry holds the checks; default DefaultRegistry.
	Registry *Registry
	// Bus receives alert.raised and alert.cleared; nil means no events (tests).
	Bus Publisher
	// Interval between fast-check passes; default DefaultInterval.
	Interval time.Duration
	// Now stamps Alert.Since; default time.Now.
	Now func() time.Time
	// Logger receives the notice of a refused event; default slog.Default.
	Logger *slog.Logger
}

// Manager runs the fast checks every Interval and keeps the active alerts: a problem result raises
// (or keeps) the alert of its code, a pass clears it, and each transition is published as
// alert.raised / alert.cleared. Event-driven alerts (LAI-AGT-016, LAI-SEC-009) are raised and
// cleared by Raise and Clear. Alerts live in memory only: after a restart the first pass derives
// them again.
//
// An alert is held by up to two sources, the checks and the callers of Raise, and stays active while
// either holds it: a clean pass never clears an alert somebody raised by hand, and Clear never
// clears one a check still reports.
type Manager struct {
	reg      *Registry
	bus      Publisher
	interval time.Duration
	now      func() time.Time
	log      *slog.Logger

	mu     sync.Mutex
	alerts map[string]*alertState

	runMu  sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
}

type alertState struct {
	Alert
	byCheck bool
	byEvent bool
	// published is the severity of the last alert.raised, so an escalation is announced again.
	published Severity
}

// NewManager returns a Manager. It does not run anything until Start (or Tick).
func NewManager(o ManagerOptions) *Manager {
	m := &Manager{reg: o.Registry, bus: o.Bus, interval: o.Interval, now: o.Now, log: o.Logger, alerts: map[string]*alertState{}}
	if m.reg == nil {
		m.reg = DefaultRegistry
	}
	if m.interval <= 0 {
		m.interval = DefaultInterval
	}
	if m.now == nil {
		m.now = time.Now
	}
	if m.log == nil {
		m.log = slog.Default()
	}
	m.log = m.log.With("component", "doctor")
	return m
}

// Active returns the active alerts, worst first, then oldest first, then by code. The caller owns
// the slice and the details maps. /status, /portal/status, the heartbeat and the state snapshot
// read it.
func (m *Manager) Active() []Alert {
	m.mu.Lock()
	out := make([]Alert, 0, len(m.alerts))
	for _, s := range m.alerts {
		a := s.Alert
		a.Details = copyDetails(a.Details)
		out = append(out, a)
	}
	m.mu.Unlock()
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Severity.Rank() != b.Severity.Rank() {
			return a.Severity.Rank() > b.Severity.Rank()
		}
		if !a.Since.Equal(b.Since) {
			return a.Since.Before(b.Since)
		}
		return a.Code < b.Code
	})
	return out
}

// Raise raises (or keeps) the alert of code on behalf of an event, outside any check. details are
// shown with the alert; a second Raise of an active code replaces them and keeps Since. A severity
// other than the code's default comes from WithSeverity. Only codes of the table can be raised.
func (m *Manager) Raise(code string, details map[string]any, opts ...RaiseOption) error {
	var o raiseOpts
	for _, f := range opts {
		f(&o)
	}
	if o.severity != "" && !o.severity.Valid() {
		return fmt.Errorf("%w: %q", ErrBadSeverity, o.severity)
	}
	if !Emittable(code) {
		return fmt.Errorf("%w: %q", ErrUnknownCode, code)
	}
	c, _ := Lookup(code)
	sev := o.severity
	if sev == "" {
		sev = c.Severity
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.raiseLocked(code, sev, details, false)
	return nil
}

// Clear withdraws the event-driven hold on code. The alert disappears (alert.cleared) unless a check
// still reports it. Clearing a code that is not active does nothing.
func (m *Manager) Clear(code string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.alerts[code]
	if !ok || !s.byEvent {
		return
	}
	s.byEvent = false
	if !s.byCheck {
		m.clearLocked(s)
	}
}

// Tick runs one pass of the fast checks and applies it to the alerts; Start calls it every
// Interval. It returns the report of the pass. A pass cut short by ctx changes nothing.
func (m *Manager) Tick(ctx context.Context) Report {
	rep, _ := m.reg.Run(ctx, RunOptions{FastOnly: true, Lang: LangEN, Now: m.now})
	if ctx.Err() != nil {
		return rep
	}
	m.apply(rep)
	return rep
}

// apply raises what the pass found and clears what it no longer finds.
func (m *Manager) apply(rep Report) {
	found := map[string]Result{}
	for _, r := range rep.Results { // worst first, so the first result of a code wins
		if _, dup := found[r.Code]; !dup {
			found[r.Code] = r
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	codes := make([]string, 0, len(found))
	for code := range found {
		codes = append(codes, code)
	}
	sort.Strings(codes)
	for _, code := range codes {
		r := found[code]
		m.raiseLocked(code, r.Severity, r.Details, true)
	}
	var gone []*alertState
	for code, s := range m.alerts {
		if _, still := found[code]; !still && s.byCheck {
			s.byCheck = false
			if !s.byEvent {
				gone = append(gone, s)
			}
		}
	}
	sort.Slice(gone, func(i, j int) bool { return gone[i].Code < gone[j].Code })
	for _, s := range gone {
		m.clearLocked(s)
	}
}

func (m *Manager) raiseLocked(code string, sev Severity, details map[string]any, byCheck bool) {
	c, _ := Lookup(code)
	s, ok := m.alerts[code]
	if !ok {
		s = &alertState{Alert: Alert{Code: code, Since: m.now().UTC().Truncate(time.Second)}}
		m.alerts[code] = s
	}
	if byCheck {
		s.byCheck = true
	} else {
		s.byEvent = true
	}
	s.Severity = sev
	s.Title, s.Fix = c.Title.EN, c.Fix.EN
	s.Details = copyDetails(details)
	if s.published != sev {
		s.published = sev
		m.publish(events.TypeAlertRaised, s)
	}
}

func (m *Manager) clearLocked(s *alertState) {
	delete(m.alerts, s.Code)
	m.publish(events.TypeAlertCleared, s)
}

func (m *Manager) publish(typ string, s *alertState) {
	if m.bus == nil {
		return
	}
	data := events.Alert{Code: s.Code, Severity: string(s.Severity), Title: s.Title, Fix: s.Fix}
	if err := m.bus.Publish(typ, "", data); err != nil {
		m.log.Warn("alert event refused", "type", typ, "code", s.Code, "error", err)
	}
}

// Name implements the app worker interface.
func (m *Manager) Name() string { return "doctor" }

// Start runs a pass at once and then one every Interval, in a goroutine, and returns. The loop ends
// when ctx is cancelled or Stop is called. A second Start is an error.
func (m *Manager) Start(ctx context.Context) error {
	m.runMu.Lock()
	defer m.runMu.Unlock()
	if m.cancel != nil {
		return errors.New("doctor: manager already started")
	}
	loopCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	m.cancel, m.done = cancel, done
	go func() {
		defer close(done)
		ticker := time.NewTicker(m.interval)
		defer ticker.Stop()
		m.Tick(loopCtx)
		for {
			select {
			case <-loopCtx.Done():
				return
			case <-ticker.C:
				m.Tick(loopCtx)
			}
		}
	}()
	return nil
}

// Stop ends the loop and waits for it, within ctx. A pass in progress is cancelled and changes
// nothing.
func (m *Manager) Stop(ctx context.Context) error {
	m.runMu.Lock()
	cancel, done := m.cancel, m.done
	m.cancel, m.done = nil, nil
	m.runMu.Unlock()
	if cancel == nil {
		return nil
	}
	cancel()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func copyDetails(d map[string]any) map[string]any {
	if len(d) == 0 {
		return nil
	}
	out := make(map[string]any, len(d))
	for k, v := range d {
		out[k] = v
	}
	return out
}
