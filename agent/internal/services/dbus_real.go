package services

import (
	"context"
	"fmt"
	"sync"

	"github.com/godbus/dbus/v5"
)

// This file is the only one that imports godbus: it adapts a *dbus.Conn to the Bus seam. It cannot be
// exercised without a system bus, so it is kept thin; the pure parts (argument lists, signal
// translation) are tested, and the rest is covered by the manual test on a real node (agent.md 9.3).

const (
	systemdDest  = "org.freedesktop.systemd1"
	systemdPath  = dbus.ObjectPath("/org/freedesktop/systemd1")
	managerIface = "org.freedesktop.systemd1.Manager"
	propsIface   = "org.freedesktop.DBus.Properties"

	sigNameJobRemoved        = managerIface + "." + signalJobRemoved
	sigNamePropertiesChanged = propsIface + "." + signalPropertiesChanged

	// signalBuffer is how many signals a subscriber may fall behind before the feed drops them.
	signalBuffer = 256
)

// DialSystemBus opens a private connection to the system bus and checks that systemd answers on it:
// it subscribes to systemd's signals, which fails when the bus is up but systemd is not PID 1 (a
// container with a bare dbus-daemon). It is the Dialer of production.
func DialSystemBus(ctx context.Context) (Bus, error) {
	type result struct {
		conn *dbus.Conn
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		c, err := dbus.ConnectSystemBus()
		ch <- result{c, err}
	}()
	var conn *dbus.Conn
	select {
	case r := <-ch:
		if r.err != nil {
			return nil, r.err
		}
		conn = r.conn
	case <-ctx.Done():
		go func() {
			if r := <-ch; r.conn != nil {
				_ = r.conn.Close()
			}
		}()
		return nil, ctx.Err()
	}
	b := &realBus{conn: conn, in: make(chan *dbus.Signal, signalBuffer), subs: map[chan Signal]struct{}{}, watched: map[string]bool{}}
	conn.Signal(b.in)
	if err := b.init(ctx); err != nil {
		_ = conn.Close()
		return nil, err
	}
	go b.pump()
	return b, nil
}

type realBus struct {
	conn *dbus.Conn
	in   chan *dbus.Signal

	mu      sync.Mutex
	subs    map[chan Signal]struct{}
	watched map[string]bool
	closed  bool
}

func (b *realBus) manager() dbus.BusObject { return b.conn.Object(systemdDest, systemdPath) }

// init adds the JobRemoved match and subscribes to systemd's signals: systemd sends them only to a
// client that called Manager.Subscribe.
func (b *realBus) init(ctx context.Context) error {
	if err := b.conn.AddMatchSignalContext(ctx,
		dbus.WithMatchInterface(managerIface), dbus.WithMatchMember(signalJobRemoved), dbus.WithMatchObjectPath(systemdPath)); err != nil {
		return fmt.Errorf("services: add JobRemoved match: %w", err)
	}
	if err := b.manager().CallWithContext(ctx, managerIface+".Subscribe", 0).Err; err != nil {
		return fmt.Errorf("services: systemd Subscribe: %w", err)
	}
	return nil
}

// translate converts a godbus signal into the Manager's Signal; false means it is not one we read.
func translate(s *dbus.Signal) (Signal, bool) {
	switch s.Name {
	case sigNameJobRemoved:
		// JobRemoved(uint32 id, object job, string unit, string result)
		if len(s.Body) != 4 {
			return Signal{}, false
		}
		job, ok1 := s.Body[1].(dbus.ObjectPath)
		unit, ok2 := s.Body[2].(string)
		result, ok3 := s.Body[3].(string)
		if !ok1 || !ok2 || !ok3 {
			return Signal{}, false
		}
		return Signal{Name: signalJobRemoved, Job: string(job), Unit: unit, Result: result}, true
	case sigNamePropertiesChanged:
		return Signal{Name: signalPropertiesChanged, Path: string(s.Path)}, true
	}
	return Signal{}, false
}

// pump fans the connection's signals out to the subscribers; it ends when the connection closes.
func (b *realBus) pump() {
	for s := range b.in {
		sig, ok := translate(s)
		if !ok {
			continue
		}
		b.mu.Lock()
		for ch := range b.subs {
			select {
			case ch <- sig:
			default: // a subscriber that stopped reading loses signals instead of stalling the bus
			}
		}
		b.mu.Unlock()
	}
	b.mu.Lock()
	b.closed = true
	for ch := range b.subs {
		close(ch)
	}
	b.subs = map[chan Signal]struct{}{}
	b.mu.Unlock()
}

func (b *realBus) Signals() (<-chan Signal, func()) {
	ch := make(chan Signal, signalBuffer)
	b.mu.Lock()
	if b.closed {
		close(ch)
	} else {
		b.subs[ch] = struct{}{}
	}
	b.mu.Unlock()
	return ch, func() {
		b.mu.Lock()
		delete(b.subs, ch)
		b.mu.Unlock()
	}
}

func (b *realBus) Done() <-chan struct{} { return b.conn.Context().Done() }

func (b *realBus) Close() error { return b.conn.Close() }

func (b *realBus) Job(ctx context.Context, method, unit string) (string, error) {
	var path dbus.ObjectPath
	if err := b.manager().CallWithContext(ctx, managerIface+"."+method, 0, unit, jobMode).Store(&path); err != nil {
		return "", err
	}
	return string(path), nil
}

func (b *realBus) LoadUnit(ctx context.Context, unit string) (string, error) {
	var path dbus.ObjectPath
	if err := b.manager().CallWithContext(ctx, managerIface+".LoadUnit", 0, unit).Store(&path); err != nil {
		return "", err
	}
	return string(path), nil
}

// unitFilesArgs is the argument list of the *UnitFiles methods (agent.md section 4.3.2):
// EnableUnitFiles([name], runtime=false, force=true), DisableUnitFiles([name], runtime=false),
// MaskUnitFiles([name], runtime=false, force=true), UnmaskUnitFiles([name], runtime=false).
func unitFilesArgs(method, unit string) ([]any, error) {
	names := []string{unit}
	switch method {
	case methodEnableUnitFiles, methodMaskUnitFiles:
		return []any{names, false, true}, nil
	case methodDisableUnitFiles, methodUnmaskUnitFiles:
		return []any{names, false}, nil
	}
	return nil, fmt.Errorf("services: %q is not a unit-file method", method)
}

func (b *realBus) UnitFiles(ctx context.Context, method, unit string) error {
	args, err := unitFilesArgs(method, unit)
	if err != nil {
		return err
	}
	return b.manager().CallWithContext(ctx, managerIface+"."+method, 0, args...).Err
}

func (b *realBus) Reload(ctx context.Context) error {
	return b.manager().CallWithContext(ctx, managerIface+".Reload", 0).Err
}

func (b *realBus) Properties(ctx context.Context, path, iface string) (map[string]any, error) {
	var vars map[string]dbus.Variant
	obj := b.conn.Object(systemdDest, dbus.ObjectPath(path))
	if err := obj.CallWithContext(ctx, propsIface+".GetAll", 0, iface).Store(&vars); err != nil {
		return nil, err
	}
	out := make(map[string]any, len(vars))
	for k, v := range vars {
		out[k] = v.Value()
	}
	return out, nil
}

func (b *realBus) Watch(ctx context.Context, paths []string) error {
	for _, p := range paths {
		b.mu.Lock()
		have := b.watched[p]
		b.mu.Unlock()
		if have {
			continue
		}
		if err := b.conn.AddMatchSignalContext(ctx,
			dbus.WithMatchObjectPath(dbus.ObjectPath(p)), dbus.WithMatchInterface(propsIface), dbus.WithMatchMember(signalPropertiesChanged)); err != nil {
			return fmt.Errorf("services: add PropertiesChanged match for %s: %w", p, err)
		}
		b.mu.Lock()
		b.watched[p] = true
		b.mu.Unlock()
	}
	return nil
}

var _ Bus = (*realBus)(nil)
