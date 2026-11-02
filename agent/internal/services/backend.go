package services

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/execx"
)

// Backend names, as logged in services.backend and returned by Auto.Backend.
const (
	BackendDBus      = "dbus"
	BackendSystemctl = "systemctl"
)

// Dialer opens a connection to systemd's D-Bus API. DialSystemBus is the real one. It must fail when
// the bus is unreachable or systemd does not answer on it.
type Dialer func(ctx context.Context) (Bus, error)

// Auto is the Manager the agent runs with (agent.md section 4.3.2): D-Bus when the system bus is
// reachable at start, systemctl otherwise. When the D-Bus connection is lost it is re-dialed, and
// while it is down every call transparently goes through systemctl. The backend in use is logged as
// services.backend=dbus|systemctl at start and at every change.
type Auto struct {
	dial Dialer
	cmd  *Systemctl
	opt  options
	log  *slog.Logger

	mu       sync.Mutex
	d        *DBus
	lastDial time.Time
	backend  string
}

// NewAuto chooses the backend: it dials D-Bus once, bounded by the call timeout, and falls back to
// systemctl (run is the execx Runner for it) when that fails. It never returns an error: with neither
// backend working, the calls fail, which the callers handle like any systemd error.
func NewAuto(ctx context.Context, dial Dialer, run execx.Runner, opts ...Option) *Auto {
	o := newOptions(opts)
	l := o.log
	if l == nil {
		l = slog.Default()
	}
	a := &Auto{dial: dial, cmd: NewSystemctl(run, opts...), opt: o, log: l.With("component", "services")}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.dialLocked(ctx)
	return a
}

// Backend returns the backend that serves calls now: "dbus" or "systemctl".
func (a *Auto) Backend() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.backend
}

// Close closes the D-Bus connection, if any.
func (a *Auto) Close() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.d == nil {
		return nil
	}
	err := a.d.Close()
	a.d = nil
	return err
}

// dialLocked tries to open the D-Bus connection and records the backend. a.mu must be held.
func (a *Auto) dialLocked(ctx context.Context) {
	a.lastDial = time.Now()
	dctx, cancel := context.WithTimeout(ctx, a.opt.callTimeout)
	defer cancel()
	var (
		bus Bus
		err error
	)
	if a.dial != nil {
		bus, err = a.dial(dctx)
	} else {
		err = errors.New("no D-Bus dialer")
	}
	if err != nil {
		a.setBackend(BackendSystemctl, "system bus not reachable: "+err.Error())
		return
	}
	a.d = NewDBus(bus, a.optionsList()...)
	a.setBackend(BackendDBus, "system bus reachable")
}

func (a *Auto) optionsList() []Option {
	o := a.opt
	return []Option{WithTimeouts(o.jobTimeout, o.callTimeout), WithLogger(a.log), WithClock(o.clock), WithPollEvery(o.pollEvery)}
}

func (a *Auto) setBackend(name, why string) {
	if a.backend == name {
		return
	}
	level := slog.LevelInfo
	if a.backend == BackendDBus {
		level = slog.LevelWarn // a working connection was lost
	}
	a.backend = name
	a.log.Log(context.Background(), level, "services backend selected", "services.backend", name, "reason", why)
}

// current returns the D-Bus Manager when the connection is up, re-dialing it when it is down and the
// redial interval has passed; nil means "use systemctl".
func (a *Auto) current(ctx context.Context) *DBus {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.d != nil {
		select {
		case <-a.d.Done():
			a.dropLocked(a.d, "connection lost")
		default:
			return a.d
		}
	}
	if time.Since(a.lastDial) < a.opt.redialEvery {
		return nil
	}
	a.dialLocked(ctx)
	return a.d
}

// drop discards d when it is still the current connection, and switches to systemctl.
func (a *Auto) drop(d *DBus, why string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.dropLocked(d, why)
}

func (a *Auto) dropLocked(d *DBus, why string) {
	if a.d != d || d == nil {
		return
	}
	_ = d.Close()
	a.d = nil
	a.lastDial = time.Now()
	a.setBackend(BackendSystemctl, why)
}

// lost reports whether err means the D-Bus connection is gone, as opposed to a failure of the call.
func lost(d *DBus, err error) bool {
	if errors.Is(err, ErrBusLost) {
		return true
	}
	select {
	case <-d.Done():
		return true
	default:
		return false
	}
}

// run calls viaDBus when D-Bus is up. A lost connection is dropped and the call is repeated through
// systemctl; any other error is the call's own and is returned as it is.
func (a *Auto) run(ctx context.Context, viaDBus func(*DBus) error, viaCmd func() error) error {
	if d := a.current(ctx); d != nil {
		err := viaDBus(d)
		if err == nil || !lost(d, err) {
			return err
		}
		a.drop(d, "connection lost: "+err.Error())
	}
	return viaCmd()
}

// State implements Manager.
func (a *Auto) State(ctx context.Context, unit string) (UnitState, error) {
	var st UnitState
	err := a.run(ctx,
		func(d *DBus) (err error) { st, err = d.State(ctx, unit); return },
		func() (err error) { st, err = a.cmd.State(ctx, unit); return })
	return st, err
}

// Start implements Manager.
func (a *Auto) Start(ctx context.Context, unit string) error {
	return a.run(ctx, func(d *DBus) error { return d.Start(ctx, unit) }, func() error { return a.cmd.Start(ctx, unit) })
}

// Stop implements Manager.
func (a *Auto) Stop(ctx context.Context, unit string) error {
	return a.run(ctx, func(d *DBus) error { return d.Stop(ctx, unit) }, func() error { return a.cmd.Stop(ctx, unit) })
}

// Restart implements Manager.
func (a *Auto) Restart(ctx context.Context, unit string) error {
	return a.run(ctx, func(d *DBus) error { return d.Restart(ctx, unit) }, func() error { return a.cmd.Restart(ctx, unit) })
}

// Enable implements Manager.
func (a *Auto) Enable(ctx context.Context, unit string) error {
	return a.run(ctx, func(d *DBus) error { return d.Enable(ctx, unit) }, func() error { return a.cmd.Enable(ctx, unit) })
}

// Disable implements Manager.
func (a *Auto) Disable(ctx context.Context, unit string) error {
	return a.run(ctx, func(d *DBus) error { return d.Disable(ctx, unit) }, func() error { return a.cmd.Disable(ctx, unit) })
}

// Mask implements Manager.
func (a *Auto) Mask(ctx context.Context, unit string) error {
	return a.run(ctx, func(d *DBus) error { return d.Mask(ctx, unit) }, func() error { return a.cmd.Mask(ctx, unit) })
}

// Unmask implements Manager.
func (a *Auto) Unmask(ctx context.Context, unit string) error {
	return a.run(ctx, func(d *DBus) error { return d.Unmask(ctx, unit) }, func() error { return a.cmd.Unmask(ctx, unit) })
}

// DaemonReload implements Manager.
func (a *Auto) DaemonReload(ctx context.Context) error {
	return a.run(ctx, func(d *DBus) error { return d.DaemonReload(ctx) }, func() error { return a.cmd.DaemonReload(ctx) })
}

// Subscribe implements Manager. The subscription outlives a D-Bus outage: while the connection is
// down it polls through systemctl, and once the connection is back it subscribes to the signals again.
// The state of every unit is re-read at each switch, and only a change is sent.
func (a *Auto) Subscribe(ctx context.Context, units []string) (<-chan UnitState, error) {
	out := make(chan UnitState, 16)
	units = append([]string(nil), units...)
	go func() {
		defer close(out)
		last := make(map[string]UnitState, len(units))
		forward := func(ch <-chan UnitState) bool {
			for st := range ch {
				if prev, ok := last[st.Unit]; ok && prev.Same(st) {
					continue
				}
				last[st.Unit] = st
				select {
				case out <- st:
				case <-ctx.Done():
					return false
				}
			}
			return ctx.Err() == nil
		}
		for ctx.Err() == nil {
			if d := a.current(ctx); d != nil {
				ch, err := d.Subscribe(ctx, units)
				if err == nil {
					if !forward(ch) {
						return
					}
					a.drop(d, "D-Bus subscription ended")
				} else {
					a.drop(d, "D-Bus subscription failed: "+err.Error())
				}
			}
			// Poll through systemctl until it is time to try D-Bus again.
			pctx, cancel := context.WithTimeout(ctx, a.opt.redialEvery)
			ch, _ := a.cmd.Subscribe(pctx, units)
			ok := forward(ch)
			cancel()
			if !ok {
				return
			}
		}
	}()
	return out, nil
}

var _ Manager = (*Auto)(nil)
