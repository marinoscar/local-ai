package fake

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/marinoscar/local-ai/agent/internal/services"
)

// Bus is a scripted services.Bus: it records every call, answers from the unit properties and job
// results a test configured, and delivers signals on demand. It never opens a connection.
type Bus struct {
	mu       sync.Mutex
	calls    []string
	props    map[string]map[string]map[string]any // unit -> interface -> properties
	results  map[string]string                    // "<method> <unit>" -> JobRemoved result
	silent   map[string]bool                      // "<method> <unit>" -> no JobRemoved ever
	failures map[string]error                     // method -> error
	subs     map[chan services.Signal]struct{}
	watched  []string
	jobSeq   int
	done     chan struct{}
	lost     bool
}

const (
	ifaceUnit    = "org.freedesktop.systemd1.Unit"
	ifaceService = "org.freedesktop.systemd1.Service"
)

// NewBus returns a Bus on which every job finishes with "done" at once.
func NewBus() *Bus {
	return &Bus{
		props:    map[string]map[string]map[string]any{},
		results:  map[string]string{},
		silent:   map[string]bool{},
		failures: map[string]error{},
		subs:     map[chan services.Signal]struct{}{},
		done:     make(chan struct{}),
	}
}

func jobKey(method, unit string) string { return method + " " + unit }

// JobResult sets the JobRemoved result of a method on a unit ("done", "failed", "timeout", ...).
func (b *Bus) JobResult(method, unit, result string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.results[jobKey(method, unit)] = result
}

// NeverFinish makes the job of a method on a unit start but never send JobRemoved.
func (b *Bus) NeverFinish(method, unit string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.silent[jobKey(method, unit)] = true
}

// Fail makes a bus method ("StartUnit", "LoadUnit", "Reload", "GetAll", "Watch", "EnableUnitFiles", ...)
// return err; nil clears it.
func (b *Bus) Fail(method string, err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err == nil {
		delete(b.failures, method)
		return
	}
	b.failures[method] = err
}

// SetUnit stores the properties GetAll returns for a unit: the Unit interface and, for a service, the
// Service interface.
func (b *Bus) SetUnit(unit string, unitProps, serviceProps map[string]any) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.props[unit] = map[string]map[string]any{ifaceUnit: unitProps, ifaceService: serviceProps}
}

// Calls returns the recorded calls as "<Method> <argument>" lines, oldest first.
func (b *Bus) Calls() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.calls...)
}

// Watched returns the object paths of the Watch calls.
func (b *Bus) Watched() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.watched...)
}

// Emit delivers a signal to every open Signals feed.
func (b *Bus) Emit(s services.Signal) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.subs {
		select {
		case ch <- s:
		default:
		}
	}
}

// Lose simulates a dropped connection: Done closes and every feed ends.
func (b *Bus) Lose() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.lost {
		return
	}
	b.lost = true
	close(b.done)
	for ch := range b.subs {
		close(ch)
	}
	b.subs = map[chan services.Signal]struct{}{}
}

// call records a call and returns the failure scripted for the method, or ErrClosed after Lose.
func (b *Bus) call(method, arg string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.calls = append(b.calls, method+" "+arg)
	if b.lost {
		return errors.New("dbus: connection closed")
	}
	return b.failures[method]
}

// Job implements services.Bus.
func (b *Bus) Job(_ context.Context, method, unit string) (string, error) {
	if err := b.call(method, unit); err != nil {
		return "", err
	}
	b.mu.Lock()
	b.jobSeq++
	job := fmt.Sprintf("/org/freedesktop/systemd1/job/%d", b.jobSeq)
	key := jobKey(method, unit)
	result, ok := b.results[key]
	if !ok {
		result = "done"
	}
	silent := b.silent[key]
	b.mu.Unlock()
	if !silent {
		// Delivered before Job returns, like a job that finishes at once: the caller must already
		// be listening.
		b.Emit(services.Signal{Name: "JobRemoved", Job: job, Unit: unit, Result: result})
	}
	return job, nil
}

// LoadUnit implements services.Bus.
func (b *Bus) LoadUnit(_ context.Context, unit string) (string, error) {
	if err := b.call("LoadUnit", unit); err != nil {
		return "", err
	}
	return services.UnitPath(unit), nil
}

// UnitFiles implements services.Bus.
func (b *Bus) UnitFiles(_ context.Context, method, unit string) error {
	return b.call(method, unit)
}

// Reload implements services.Bus.
func (b *Bus) Reload(context.Context) error { return b.call("Reload", "") }

// Properties implements services.Bus.
func (b *Bus) Properties(_ context.Context, path, iface string) (map[string]any, error) {
	if err := b.call("GetAll", path+" "+iface); err != nil {
		return nil, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for unit, ifaces := range b.props {
		if services.UnitPath(unit) == path {
			p, ok := ifaces[iface]
			if !ok {
				return nil, fmt.Errorf("fake bus: no interface %s on %s", iface, unit)
			}
			return p, nil
		}
	}
	// A unit nobody configured is what systemd calls a not-found stub: inactive, dead.
	if iface == ifaceUnit {
		return map[string]any{"ActiveState": "inactive", "SubState": "dead", "UnitFileState": ""}, nil
	}
	return map[string]any{}, nil
}

// Watch implements services.Bus.
func (b *Bus) Watch(_ context.Context, paths []string) error {
	if err := b.call("Watch", fmt.Sprint(paths)); err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.watched = append(b.watched, paths...)
	return nil
}

// Signals implements services.Bus.
func (b *Bus) Signals() (<-chan services.Signal, func()) {
	ch := make(chan services.Signal, 64)
	b.mu.Lock()
	if b.lost {
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

// Done implements services.Bus.
func (b *Bus) Done() <-chan struct{} { return b.done }

// Close implements services.Bus.
func (b *Bus) Close() error {
	b.Lose()
	return nil
}

var _ services.Bus = (*Bus)(nil)
