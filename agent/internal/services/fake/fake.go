// Package fake holds the test doubles of internal/services: a Manager that keeps unit states in
// memory (used by the watcher, the API handlers and the mode engine tests), and a scripted Bus for the
// D-Bus Manager (bus.go). Neither touches systemd.
package fake

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/services"
)

// Call is one Manager call the fake recorded.
type Call struct {
	Verb services.Verb
	Unit string // empty for daemon-reload
}

// Manager is an in-memory services.Manager. A unit nobody configured is inactive/dead. By default a
// verb has its natural effect (start and restart make the unit active, stop makes it inactive); tests
// change that with Fail, Freeze and Set. It is safe for concurrent use.
type Manager struct {
	mu     sync.Mutex
	now    func() time.Time
	states map[string]services.UnitState
	calls  []Call
	fails  map[string]error
	frozen map[string]bool
	subs   []*subscription
	// Hook, when set, runs at the start of every verb call (not State), with the manager unlocked.
	// A non-nil error is returned instead of the verb's effect.
	Hook func(verb services.Verb, unit string) error
}

type subscription struct {
	units map[string]bool
	ch    chan services.UnitState
	ctx   context.Context

	sendMu sync.Mutex // held while sending, so the channel is never closed under a sender
	closed bool
}

// New returns an empty Manager whose clock is time.Now.
func New() *Manager {
	return &Manager{now: time.Now, states: map[string]services.UnitState{}, fails: map[string]error{}, frozen: map[string]bool{}}
}

// SetClock replaces the clock that stamps ActiveEnterTimestamp when a unit starts.
func (m *Manager) SetClock(now func() time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.now = now
}

func failKey(verb services.Verb, unit string) string { return string(verb) + " " + unit }

// Fail makes the verb on the unit return err, with no effect, until Fail is called with nil. A unit of
// "" matches every unit.
func (m *Manager) Fail(verb services.Verb, unit string, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err == nil {
		delete(m.fails, failKey(verb, unit))
		return
	}
	m.fails[failKey(verb, unit)] = err
}

// Freeze makes the verbs on the unit succeed with no effect (the unit never reaches the state the
// caller waits for), until Unfreeze.
func (m *Manager) Freeze(unit string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.frozen[unit] = true
}

// Unfreeze undoes Freeze.
func (m *Manager) Unfreeze(unit string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.frozen, unit)
}

// Set stores a state and sends it to the subscribers of the unit.
func (m *Manager) Set(st services.UnitState) {
	m.mu.Lock()
	m.states[st.Unit] = st
	subs := m.matching(st.Unit)
	m.mu.Unlock()
	m.send(subs, st)
}

// Calls returns the recorded calls, oldest first.
func (m *Manager) Calls() []Call {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]Call(nil), m.calls...)
}

// Reset forgets the recorded calls.
func (m *Manager) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = nil
}

func (m *Manager) matching(unit string) []*subscription {
	var out []*subscription
	for _, s := range m.subs {
		if s.units[unit] {
			out = append(out, s)
		}
	}
	return out
}

func (m *Manager) send(subs []*subscription, st services.UnitState) {
	for _, s := range subs {
		s.sendMu.Lock()
		if !s.closed {
			select {
			case s.ch <- st:
			case <-s.ctx.Done():
			}
		}
		s.sendMu.Unlock()
	}
}

func (m *Manager) stateLocked(unit string) services.UnitState {
	if st, ok := m.states[unit]; ok {
		return st
	}
	return services.UnitState{Unit: unit, ActiveState: services.ActiveInactive, SubState: "dead"}
}

// State implements services.Manager.
func (m *Manager) State(_ context.Context, unit string) (services.UnitState, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, Call{services.VerbState, unit})
	if err := m.fails[failKey(services.VerbState, unit)]; err != nil {
		return services.UnitState{}, err
	}
	if err := m.fails[failKey(services.VerbState, "")]; err != nil {
		return services.UnitState{}, err
	}
	return m.stateLocked(unit), nil
}

// verb records a call, applies the failure or the effect, and tells the subscribers.
func (m *Manager) verb(verb services.Verb, unit string, effect func(*services.UnitState, time.Time)) error {
	m.mu.Lock()
	m.calls = append(m.calls, Call{verb, unit})
	hook := m.Hook
	m.mu.Unlock()
	if hook != nil {
		if err := hook(verb, unit); err != nil {
			return err
		}
	}
	m.mu.Lock()
	if err := m.fails[failKey(verb, unit)]; err != nil {
		m.mu.Unlock()
		return err
	}
	if err := m.fails[failKey(verb, "")]; err != nil {
		m.mu.Unlock()
		return err
	}
	if effect == nil || m.frozen[unit] {
		m.mu.Unlock()
		return nil
	}
	st := m.stateLocked(unit)
	effect(&st, m.now())
	m.states[unit] = st
	subs := m.matching(unit)
	m.mu.Unlock()
	m.send(subs, st)
	return nil
}

func activate(st *services.UnitState, now time.Time) {
	st.ActiveState, st.SubState = services.ActiveActive, "running"
	st.MainPID = 4242
	st.ActiveEnterTimestamp = now
}

// Start implements services.Manager.
func (m *Manager) Start(_ context.Context, unit string) error {
	return m.verb(services.VerbStart, unit, activate)
}

// Stop implements services.Manager.
func (m *Manager) Stop(_ context.Context, unit string) error {
	return m.verb(services.VerbStop, unit, func(st *services.UnitState, now time.Time) {
		st.ActiveState, st.SubState, st.MainPID = services.ActiveInactive, "dead", 0
		st.ExecMainExitTimestamp = now
	})
}

// Restart implements services.Manager.
func (m *Manager) Restart(_ context.Context, unit string) error {
	return m.verb(services.VerbRestart, unit, activate)
}

// Enable implements services.Manager.
func (m *Manager) Enable(_ context.Context, unit string) error {
	return m.verb(services.VerbEnable, unit, func(st *services.UnitState, _ time.Time) { st.UnitFileState = "enabled" })
}

// Disable implements services.Manager.
func (m *Manager) Disable(_ context.Context, unit string) error {
	return m.verb(services.VerbDisable, unit, func(st *services.UnitState, _ time.Time) { st.UnitFileState = "disabled" })
}

// Mask implements services.Manager.
func (m *Manager) Mask(_ context.Context, unit string) error {
	return m.verb(services.VerbMask, unit, func(st *services.UnitState, _ time.Time) { st.UnitFileState = "masked" })
}

// Unmask implements services.Manager.
func (m *Manager) Unmask(_ context.Context, unit string) error {
	return m.verb(services.VerbUnmask, unit, func(st *services.UnitState, _ time.Time) { st.UnitFileState = "disabled" })
}

// DaemonReload implements services.Manager.
func (m *Manager) DaemonReload(context.Context) error {
	return m.verb(services.VerbDaemonReload, "", nil)
}

// Subscribe implements services.Manager: the current state of each unit is sent first.
func (m *Manager) Subscribe(ctx context.Context, units []string) (<-chan services.UnitState, error) {
	m.mu.Lock()
	m.calls = append(m.calls, Call{services.VerbSubscribe, fmt.Sprint(units)})
	if err := m.fails[failKey(services.VerbSubscribe, "")]; err != nil {
		m.mu.Unlock()
		return nil, err
	}
	s := &subscription{units: map[string]bool{}, ch: make(chan services.UnitState, 64), ctx: ctx}
	for _, u := range units {
		s.units[u] = true
		s.ch <- m.stateLocked(u)
	}
	m.subs = append(m.subs, s)
	m.mu.Unlock()
	go func() {
		<-ctx.Done()
		m.mu.Lock()
		for i, x := range m.subs {
			if x == s {
				m.subs = append(m.subs[:i], m.subs[i+1:]...)
				break
			}
		}
		m.mu.Unlock()
		s.sendMu.Lock()
		s.closed = true
		close(s.ch)
		s.sendMu.Unlock()
	}()
	return s.ch, nil
}

var _ services.Manager = (*Manager)(nil)
