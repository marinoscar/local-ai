package services

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"
)

// The systemd D-Bus API the Manager uses (agent.md section 4.3.2): the destination is
// org.freedesktop.systemd1 and the manager object is /org/freedesktop/systemd1.
const (
	ifaceUnit    = "org.freedesktop.systemd1.Unit"
	ifaceService = "org.freedesktop.systemd1.Service"

	methodStartUnit   = "StartUnit"
	methodStopUnit    = "StopUnit"
	methodRestartUnit = "RestartUnit"

	methodEnableUnitFiles  = "EnableUnitFiles"
	methodDisableUnitFiles = "DisableUnitFiles"
	methodMaskUnitFiles    = "MaskUnitFiles"
	methodUnmaskUnitFiles  = "UnmaskUnitFiles"

	// jobMode is the mode of every StartUnit, StopUnit and RestartUnit: queued jobs of the same unit
	// are replaced.
	jobMode = "replace"
	// jobDone is the only JobRemoved result that means success.
	jobDone = "done"

	signalJobRemoved        = "JobRemoved"
	signalPropertiesChanged = "PropertiesChanged"
)

// Signal is the part of a systemd signal the Manager reads: JobRemoved (Job, Unit, Result) and the
// PropertiesChanged of a unit object (Path).
type Signal struct {
	// Name is signalJobRemoved or signalPropertiesChanged.
	Name string
	// Path is the object path a PropertiesChanged came from.
	Path string
	// Job, Unit and Result are the fields of a JobRemoved.
	Job, Unit, Result string
}

// Bus is one connection to systemd's D-Bus API, reduced to what the Manager needs. The real one is
// dialed by DialSystemBus (dbus_real.go, the only file that imports godbus); internal/services/fake
// has a scripted one. The adapter owns the wire details: argument lists, variants, match rules.
type Bus interface {
	// Job calls Manager.StartUnit, StopUnit or RestartUnit (method) with (unit, "replace") and
	// returns the job's object path.
	Job(ctx context.Context, method, unit string) (job string, err error)
	// LoadUnit calls Manager.LoadUnit and returns the unit's object path. Unlike GetUnit it works
	// for a unit that is not loaded, and gives a not-found stub for one with no file.
	LoadUnit(ctx context.Context, unit string) (path string, err error)
	// UnitFiles calls EnableUnitFiles, DisableUnitFiles, MaskUnitFiles or UnmaskUnitFiles for one
	// unit, with the flags of agent.md section 4.3.2.
	UnitFiles(ctx context.Context, method, unit string) error
	// Reload calls Manager.Reload (daemon-reload).
	Reload(ctx context.Context) error
	// Properties returns org.freedesktop.DBus.Properties.GetAll(iface) of the object at path, with
	// the variants unwrapped.
	Properties(ctx context.Context, path, iface string) (map[string]any, error)
	// Watch asks for the PropertiesChanged signals of the unit objects at paths.
	Watch(ctx context.Context, paths []string) error
	// Signals returns a channel fed with the JobRemoved signals and the PropertiesChanged signals of
	// the watched paths, and a cancel function that stops the feed. It is buffered; a call made
	// after Signals never misses a signal that follows it.
	Signals() (<-chan Signal, func())
	// Done is closed when the connection is lost or closed.
	Done() <-chan struct{}
	// Close closes the connection.
	Close() error
}

// DBus is the Manager that talks to systemd over D-Bus (agent.md section 4.3.2), the default.
type DBus struct {
	bus Bus
	opt options
	log *slog.Logger
}

// NewDBus returns a D-Bus Manager over bus.
func NewDBus(bus Bus, opts ...Option) *DBus {
	o := newOptions(opts)
	l := o.log
	if l == nil {
		l = slog.Default()
	}
	return &DBus{bus: bus, opt: o, log: l.With("component", "services")}
}

// Done is closed when the underlying connection is lost.
func (d *DBus) Done() <-chan struct{} { return d.bus.Done() }

// Close closes the underlying connection.
func (d *DBus) Close() error { return d.bus.Close() }

// State implements Manager.
func (d *DBus) State(ctx context.Context, unit string) (UnitState, error) {
	ctx, cancel := context.WithTimeout(ctx, d.opt.callTimeout)
	defer cancel()
	path, err := d.bus.LoadUnit(ctx, unit)
	if err != nil {
		return UnitState{}, d.mapErr(ctx, err, VerbState, unit)
	}
	return d.read(ctx, unit, path)
}

func (d *DBus) read(ctx context.Context, unit, path string) (UnitState, error) {
	up, err := d.bus.Properties(ctx, path, ifaceUnit)
	if err != nil {
		return UnitState{}, d.mapErr(ctx, err, VerbState, unit)
	}
	st := UnitState{
		Unit:                 unit,
		ActiveState:          asString(up["ActiveState"]),
		SubState:             asString(up["SubState"]),
		UnitFileState:        asString(up["UnitFileState"]),
		ActiveEnterTimestamp: asTime(up["ActiveEnterTimestamp"]),
	}
	// Only a service has the Service interface; a timer answers GetAll(Service) with an error.
	if strings.HasSuffix(unit, ".service") {
		sp, err := d.bus.Properties(ctx, path, ifaceService)
		if err != nil {
			return UnitState{}, d.mapErr(ctx, err, VerbState, unit)
		}
		st.MainPID = asInt(sp["MainPID"])
		st.NRestarts = asInt(sp["NRestarts"])
		st.ExecMainStatus = asInt(sp["ExecMainStatus"])
		st.ExecMainExitTimestamp = asTime(sp["ExecMainExitTimestamp"])
	}
	if st.ActiveState == "" {
		return UnitState{}, fmt.Errorf("services: D-Bus state of %s has no ActiveState", unit)
	}
	return st, nil
}

// mapErr turns a bus error into the Manager's errors: a deadline becomes ErrTimeout, a dead
// connection ErrBusLost; ctx is the context the call ran under.
func (d *DBus) mapErr(ctx context.Context, err error, verb Verb, unit string) error {
	select {
	case <-d.bus.Done():
		return fmt.Errorf("%w: %s %s: %v", ErrBusLost, verb, unit, err)
	default:
	}
	if ctx.Err() == context.DeadlineExceeded {
		return fmt.Errorf("%w: %s %s", ErrTimeout, verb, unit)
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return fmt.Errorf("services: %s %s: %w", verb, unit, err)
}

// job runs StartUnit, StopUnit or RestartUnit and waits for its JobRemoved: the result must be "done",
// anything else is a *JobError carrying systemd's result string. The signal feed is opened before the
// call, so a job that finishes at once is not missed.
func (d *DBus) job(ctx context.Context, verb Verb, method, unit string) error {
	ctx, cancel := context.WithTimeout(ctx, d.opt.jobTimeout)
	defer cancel()
	sigs, stop := d.bus.Signals()
	defer stop()
	job, err := d.bus.Job(ctx, method, unit)
	if err != nil {
		return d.mapErr(ctx, err, verb, unit)
	}
	for {
		select {
		case s, ok := <-sigs:
			if !ok {
				return fmt.Errorf("%w: %s %s", ErrBusLost, verb, unit)
			}
			if s.Name != signalJobRemoved || s.Job != job {
				continue
			}
			if s.Result == jobDone {
				return nil
			}
			return &JobError{Unit: unit, Verb: verb, Result: s.Result}
		case <-d.bus.Done():
			return fmt.Errorf("%w: %s %s", ErrBusLost, verb, unit)
		case <-ctx.Done():
			if ctx.Err() == context.DeadlineExceeded {
				return fmt.Errorf("%w: %s %s: job %s did not finish", ErrTimeout, verb, unit, job)
			}
			return ctx.Err()
		}
	}
}

// Start implements Manager.
func (d *DBus) Start(ctx context.Context, unit string) error {
	return d.job(ctx, VerbStart, methodStartUnit, unit)
}

// Stop implements Manager.
func (d *DBus) Stop(ctx context.Context, unit string) error {
	return d.job(ctx, VerbStop, methodStopUnit, unit)
}

// Restart implements Manager.
func (d *DBus) Restart(ctx context.Context, unit string) error {
	return d.job(ctx, VerbRestart, methodRestartUnit, unit)
}

// unitFiles runs one of the *UnitFiles methods and then Reload: systemd documents that the changes
// take effect after a reload, and `systemctl enable` does it by itself, so both backends behave the
// same.
func (d *DBus) unitFiles(ctx context.Context, verb Verb, method, unit string) error {
	ctx, cancel := context.WithTimeout(ctx, d.opt.callTimeout)
	defer cancel()
	if err := d.bus.UnitFiles(ctx, method, unit); err != nil {
		return d.mapErr(ctx, err, verb, unit)
	}
	if err := d.bus.Reload(ctx); err != nil {
		return d.mapErr(ctx, err, verb, unit)
	}
	return nil
}

// Enable implements Manager.
func (d *DBus) Enable(ctx context.Context, unit string) error {
	return d.unitFiles(ctx, VerbEnable, methodEnableUnitFiles, unit)
}

// Disable implements Manager.
func (d *DBus) Disable(ctx context.Context, unit string) error {
	return d.unitFiles(ctx, VerbDisable, methodDisableUnitFiles, unit)
}

// Mask implements Manager.
func (d *DBus) Mask(ctx context.Context, unit string) error {
	return d.unitFiles(ctx, VerbMask, methodMaskUnitFiles, unit)
}

// Unmask implements Manager.
func (d *DBus) Unmask(ctx context.Context, unit string) error {
	return d.unitFiles(ctx, VerbUnmask, methodUnmaskUnitFiles, unit)
}

// DaemonReload implements Manager.
func (d *DBus) DaemonReload(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, d.opt.callTimeout)
	defer cancel()
	if err := d.bus.Reload(ctx); err != nil {
		return d.mapErr(ctx, err, VerbDaemonReload, "")
	}
	return nil
}

// Subscribe implements Manager: the matches are added once, the state of every unit is sent at once,
// and each PropertiesChanged of a watched unit re-reads that unit and sends the state when it changed.
// The channel closes when ctx ends or the connection is lost.
func (d *DBus) Subscribe(ctx context.Context, units []string) (<-chan UnitState, error) {
	byPath := make(map[string]string, len(units))
	paths := make([]string, 0, len(units))
	for _, u := range units {
		p := UnitPath(u)
		byPath[p] = u
		paths = append(paths, p)
	}
	sigs, stop := d.bus.Signals()
	wctx, cancel := context.WithTimeout(ctx, d.opt.callTimeout)
	err := d.bus.Watch(wctx, paths)
	if err != nil {
		err = d.mapErr(wctx, err, VerbSubscribe, strings.Join(units, ","))
	}
	cancel()
	if err != nil {
		stop()
		return nil, err
	}
	out := make(chan UnitState, 16)
	go func() {
		defer close(out)
		defer stop()
		last := make(map[string]UnitState, len(units))
		// send reads one unit and sends its state when it changed. It reports false when the
		// subscription must end (ctx done, or the connection is gone).
		send := func(unit string) bool {
			st, err := d.State(ctx, unit)
			if err != nil {
				select {
				case <-d.bus.Done():
					return false
				default:
				}
				d.log.Debug("services: state read failed", "unit", unit, "error", err)
				return ctx.Err() == nil
			}
			if prev, ok := last[unit]; ok && prev.Same(st) {
				return true
			}
			last[unit] = st
			select {
			case out <- st:
				return true
			case <-ctx.Done():
				return false
			}
		}
		for _, u := range units {
			if !send(u) {
				return
			}
		}
		for {
			select {
			case s, ok := <-sigs:
				if !ok {
					return
				}
				if s.Name != signalPropertiesChanged {
					continue
				}
				if u, ok := byPath[s.Path]; ok && !send(u) {
					return
				}
			case <-d.bus.Done():
				return
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, nil
}

var _ Manager = (*DBus)(nil)

// UnitPath is the D-Bus object path of a unit: /org/freedesktop/systemd1/unit/ plus the unit name with
// every byte outside [A-Za-z0-9] written as _ and two lower-case hex digits ("localai-llama.service"
// becomes "localai_2dllama_2eservice"). Computing it avoids a GetUnit call, which fails for a unit
// that is not loaded.
func UnitPath(unit string) string {
	var b strings.Builder
	b.WriteString("/org/freedesktop/systemd1/unit/")
	for i := 0; i < len(unit); i++ {
		c := unit[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('_')
		b.WriteString(strconv.FormatUint(uint64(c)>>4, 16))
		b.WriteString(strconv.FormatUint(uint64(c)&0xf, 16))
	}
	return b.String()
}

func asString(v any) string {
	s, _ := v.(string)
	return s
}

// asInt reads a D-Bus integer of any width.
func asInt(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case int32:
		return int(n)
	case int64:
		return int(n)
	case uint32:
		return int(n)
	case uint64:
		return int(n)
	case int16:
		return int(n)
	case uint16:
		return int(n)
	case uint8:
		return int(n)
	}
	return 0
}

// asTime reads a systemd timestamp: microseconds since the epoch, 0 meaning never.
func asTime(v any) time.Time {
	var us uint64
	switch n := v.(type) {
	case uint64:
		us = n
	case int64:
		if n > 0 {
			us = uint64(n)
		}
	case uint32:
		us = uint64(n)
	}
	if us == 0 {
		return time.Time{}
	}
	return time.UnixMicro(int64(us)).UTC()
}
