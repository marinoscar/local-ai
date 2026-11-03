package app

import (
	"context"
	"sync"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/modes"
)

// The shutdown budgets of agent.md section 4.12. The steps whose deadlines are independent (3, 4, 5
// and the drill's restore) run concurrently, so the sum below the group is what is left: 13 s for the
// group, 2 s for the orchestrator, 5 s for the server and 3 s to flush, inside the total of 25 s that
// leaves systemd's 30 s TimeoutStopSec five seconds of slack.
const (
	ShutdownBudget      = 25 * time.Second
	shutdownWorkerStop  = 2 * time.Second
	shutdownLinkBudget  = 13 * time.Second
	shutdownModeBudget  = 10 * time.Second
	shutdownSyncBudget  = 10 * time.Second
	shutdownDrillBudget = 10 * time.Second
	shutdownModelBudget = 2 * time.Second
	shutdownHTTPBudget  = 5 * time.Second
	shutdownFlushBudget = 3 * time.Second
)

// shutdowner is a subsystem that stops within a context.
type shutdowner interface {
	Shutdown(ctx context.Context) error
}

// syncCanceller is the sync engine's graceful cancel (modes.SyncController without the scheduler).
type syncCanceller interface {
	CancelGraceful(ctx context.Context) error
}

// httpStopper is the API server as shutdown sees it.
type httpStopper interface {
	SetMutationsOpen(open bool)
	Stop(ctx context.Context) error
}

// shutdownSet is what the shutdown stops, as interfaces, so a test fills it with fakes. Every field
// is optional: a daemon that never got as far as building a subsystem has nothing to stop there.
type shutdownSet struct {
	link   shutdowner    // cloud link, shutdown variant (step 3)
	engine shutdowner    // mode engine (step 4)
	syncer syncCanceller // sync engine (step 5)
	drill  shutdowner    // drill engine: aborts the run and restores the node
	orch   shutdowner    // model orchestrator (step 6)
	server httpStopper   // step 7
	bus    interface{ Close() }
	state  interface{ Close() error }
	audit  interface{ Close() error }
	prober interface{ Stop() }
}

// ShutdownStep is what the shutdown recorded of one step.
type ShutdownStep struct {
	N        int
	Name     string
	Duration time.Duration
	Err      error
	TimedOut bool
}

// ShutdownReport is the outcome of Shutdown.
type ShutdownReport struct {
	Steps    []ShutdownStep
	Duration time.Duration
}

// Shutdown is agent.md section 4.12 (issue AGENT-20), the reverse of the startup:
//
//  1. STOPPING=1; stopping; the mutating routes answer 503 LAI-AGT-024 and the mode engine stops
//     accepting, so a POST /mode/survival is queued as state.pending_mode and runs at the next start;
//  2. the periodic workers stop (prober, reconcilers, doctor, hardware sampler, watchers);
//  3. the cloud link shuts down without deregistering (13 s), 4. a running mode step may finish (10 s)
//     and the recorded step stays for the resume, 5. a running sync is cancelled gracefully (10 s),
//     and the drill, if one runs, restores the node (10 s): independent, so they run concurrently;
//  6. the orchestrator stops watching the loads it has in progress without aborting a router call;
//  7. the server stops (SSE streams say goodbye, in-flight requests drain for 5 s, the socket file is
//     removed), then the event bus closes;
//  8. the state store and the audit log are flushed.
//
// The whole shutdown has a budget of 25 s on the daemon's clock; a step that runs past it is abandoned,
// never waited for. Children (llama, kiwix, whisper) are never stopped. It is safe to call twice.
func (d *Daemon) Shutdown() ShutdownReport {
	var rep ShutdownReport
	d.shutdown1.Do(func() { rep = d.shutdown() })
	return rep
}

func (d *Daemon) shutdown() ShutdownReport {
	clock := d.env.Clock
	began := clock.Now()
	d.ensureLogger()
	total, cancelTotal := withDeadline(context.Background(), clock, ShutdownBudget)
	defer cancelTotal()
	var rep ShutdownReport
	var repMu sync.Mutex
	add := func(s ShutdownStep) {
		repMu.Lock()
		rep.Steps = append(rep.Steps, s)
		repMu.Unlock()
		if s.Err != nil || s.TimedOut {
			d.log.Warn("shutdown step degraded", "step", s.N, "name", s.Name, "error", errString(s.Err), "timed_out", s.TimedOut)
		}
	}
	// timed runs fn under its own budget inside the total and records it.
	timedIn := func(parent context.Context, n int, name string, budget time.Duration, fn func(ctx context.Context) error) {
		t0 := clock.Now()
		ctx, cancel := withDeadline(parent, clock, budget)
		err := fn(ctx)
		over := timedOut(ctx) || timedOut(parent)
		cancel()
		add(ShutdownStep{N: n, Name: name, Duration: clock.Now().Sub(t0), Err: err, TimedOut: over})
	}
	timed := func(n int, name string, budget time.Duration, fn func(ctx context.Context) error) {
		timedIn(total, n, name, budget, fn)
	}

	// 1
	d.stopping.Store(true)
	if err := d.env.Notifier.Stopping(); err != nil {
		d.log.Warn("sd_notify STOPPING failed", "error", err.Error())
	}
	_ = d.env.Notifier.Status("stopping")
	if d.bootCancel != nil {
		d.bootCancel() // the background startup steps stop starting new work
	}
	st := d.stoppers
	if st.server != nil {
		st.server.SetMutationsOpen(false)
	}
	d.log.Info("agent stopping")
	// The mode engine stops accepting now (its 10 s run from here); the wait is step 4.
	engineDone := make(chan ShutdownStep, 1)
	if st.engine != nil {
		go func() {
			t0 := clock.Now()
			ctx, cancel := withDeadline(total, clock, shutdownModeBudget)
			err := st.engine.Shutdown(ctx)
			over := timedOut(ctx)
			cancel()
			engineDone <- ShutdownStep{N: 4, Name: "mode_transition", Duration: clock.Now().Sub(t0), Err: err, TimedOut: over}
		}()
	} else {
		engineDone <- ShutdownStep{N: 4, Name: "mode_transition"}
	}
	add(ShutdownStep{N: 1, Name: "stopping"})

	// 2: periodic workers, last started first.
	timed(2, "periodic_workers", shutdownWorkerStop, func(ctx context.Context) error {
		if st.prober != nil {
			st.prober.Stop()
		}
		d.mu.Lock()
		ws := append([]Worker(nil), d.periodic...)
		d.periodic = nil
		d.mu.Unlock()
		for i := len(ws) - 1; i >= 0; i-- {
			w := ws[i]
			if err := runWithin(ctx, func() error { return w.Stop(ctx) }); err != nil {
				d.log.Warn("worker did not stop cleanly", "worker", w.Name(), "error", err.Error())
			}
		}
		return nil
	})

	// 3, 4, 5 and the drill: concurrent.
	var wg sync.WaitGroup
	run := func(n int, name string, budget time.Duration, s shutdowner) {
		if s == nil {
			return
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			timed(n, name, budget, func(ctx context.Context) error { return runWithin(ctx, func() error { return s.Shutdown(ctx) }) })
		}()
	}
	run(3, "cloud_link", shutdownLinkBudget, st.link)
	run(5, "drill", shutdownDrillBudget, st.drill)
	if st.syncer != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			timed(5, "sync", shutdownSyncBudget, func(ctx context.Context) error {
				return runWithin(ctx, func() error { return st.syncer.CancelGraceful(ctx) })
			})
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		s := <-engineDone
		add(s)
	}()
	wg.Wait()

	// The long-lived context ends once nothing is using it any more.
	if d.cancel != nil {
		d.cancel()
	}

	// 6: the background startup steps stop starting work (their context was cancelled at step 1) and
	// are given the model budget to return; then the orchestrator stops watching what it has in
	// progress. A restore that is mid-load keeps its router call: the router keeps loading on its own.
	if st.orch != nil {
		timed(6, "model_operations", shutdownModelBudget, func(ctx context.Context) error {
			if err := runWithin(ctx, func() error { d.bg.Wait(); return nil }); err != nil {
				d.log.Warn("a background startup step is still running; not waiting for it")
			}
			return runWithin(ctx, func() error { return st.orch.Shutdown(ctx) })
		})
	}
	// 7
	if st.server != nil {
		timed(7, "http_server", shutdownHTTPBudget, func(ctx context.Context) error { return st.server.Stop(ctx) })
	}
	if st.bus != nil {
		st.bus.Close()
	}
	// 8: the flush is not clipped by the total: losing state.json (a pending survival request, the
	// recorded mode step) is worse than leaving a few seconds late. It has its own 3 s, so the
	// worst case is 28 s, still under systemd's 30 s.
	timedIn(context.Background(), 8, "flush", shutdownFlushBudget, func(ctx context.Context) error {
		var first error
		if st.state != nil {
			if err := runWithin(ctx, st.state.Close); err != nil {
				first = err
			}
		}
		if st.audit != nil {
			if err := runWithin(ctx, st.audit.Close); err != nil && first == nil {
				first = err
			}
		}
		for _, c := range d.closers { // the D-Bus connection
			if err := c.Close(); err != nil && first == nil {
				first = err
			}
		}
		return first
	})

	rep.Duration = clock.Now().Sub(began)
	d.log.Info("agent.stopped", "duration_ms", rep.Duration.Milliseconds())
	return rep
}

// runWithin runs fn and returns its result, or ctx's error when ctx ends first: a subsystem that
// ignores its context must not hold the shutdown past its budget. The abandoned call keeps running
// in its goroutine until the process exits, which is the point of a budget.
func runWithin(ctx context.Context, fn func() error) error {
	res := make(chan error, 1)
	go func() { res <- fn() }()
	select {
	case err := <-res:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// compile-time check: the mode engine satisfies the shutdown seam.
var _ shutdowner = (*modes.Engine)(nil)
