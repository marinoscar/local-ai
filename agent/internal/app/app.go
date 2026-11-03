// Package app wires the daemon: Serve runs the startup sequence of agent.md section 4.1 and the
// graceful shutdown of section 4.12 over the subsystems of the other packages (daemon.go, start.go,
// stop.go, build.go), reports readiness to systemd (sdnotify.go, watchdog.go) and offers the
// extension points a slice that is not merged yet plugs into with one registration (hooks.go).
//
// App below is the generic ordered Worker runner of AGENT-01: workers start in registration order and
// stop in reverse order under one shared context. The Daemon uses the Worker interface for its
// periodic workers; this package holds no domain logic of its own, only wiring and the adapters that
// connect the interfaces of one package to the values of another.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"
)

// Worker is one long-lived part of the daemon (agent.md section 3.1).
//
// Start must return once the worker is running: long-lived work belongs in goroutines that exit
// when ctx (the shared root context) is cancelled. Stop is called after ctx is cancelled and must
// release the worker's resources within the budget it is given.
type Worker interface {
	Name() string
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
}

// DefaultStopBudget bounds each worker's Stop. Per-worker budgets arrive with AGENT-20.
const DefaultStopBudget = 10 * time.Second

// App starts registered workers in order and stops them in reverse order.
type App struct {
	workers    []Worker
	stopBudget time.Duration
	log        *slog.Logger
}

// Option configures an App.
type Option func(*App)

// WithStopBudget sets how long each worker's Stop may take before it is logged and abandoned.
func WithStopBudget(d time.Duration) Option { return func(a *App) { a.stopBudget = d } }

// WithLogger sets the logger (defaults to slog.Default; AGENT-04 installs the real handler).
func WithLogger(l *slog.Logger) Option { return func(a *App) { a.log = l } }

// New returns an App with no workers.
func New(opts ...Option) *App {
	a := &App{stopBudget: DefaultStopBudget, log: slog.Default()}
	for _, o := range opts {
		o(a)
	}
	return a
}

// Register adds a worker. Registration order is start order. It must be called before Run.
func (a *App) Register(w Worker) { a.workers = append(a.workers, w) }

// Run starts every worker in order, waits until ctx is cancelled, then stops them in reverse order.
//
// If a worker fails to start, the workers already started are stopped in reverse order and Run
// returns the start error. A worker whose Stop fails or exceeds the stop budget is logged and
// abandoned; shutdown continues. Run returns nil after a clean or best-effort shutdown.
func (a *App) Run(ctx context.Context) error {
	root, cancel := context.WithCancel(ctx)
	defer cancel()

	started := make([]Worker, 0, len(a.workers))
	for _, w := range a.workers {
		if err := w.Start(root); err != nil {
			a.log.Error("worker failed to start", "worker", w.Name(), "error", err)
			cancel()
			a.stopAll(started)
			return fmt.Errorf("start %s: %w", w.Name(), err)
		}
		started = append(started, w)
		a.log.Info("worker started", "worker", w.Name())
	}

	<-root.Done()
	a.stopAll(started)
	return nil
}

func (a *App) stopAll(started []Worker) {
	for i := len(started) - 1; i >= 0; i-- {
		w := started[i]
		// A fresh context: the root context is already cancelled, which must not shorten the budget.
		stopCtx, cancel := context.WithTimeout(context.Background(), a.stopBudget)
		done := make(chan error, 1)
		go func() { done <- w.Stop(stopCtx) }()
		select {
		case err := <-done:
			if err != nil && !errors.Is(err, context.Canceled) {
				a.log.Error("worker stop failed", "worker", w.Name(), "error", err)
			} else {
				a.log.Info("worker stopped", "worker", w.Name())
			}
		case <-stopCtx.Done():
			a.log.Error("worker did not stop within its budget; abandoned", "worker", w.Name(), "budget", a.stopBudget.String())
		}
		cancel()
	}
}

// SignalContext returns a context cancelled on SIGTERM or SIGINT (what systemd and Ctrl-C send).
func SignalContext(parent context.Context) (context.Context, context.CancelFunc) {
	return signal.NotifyContext(parent, syscall.SIGTERM, os.Interrupt)
}
