package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/config"
)

// ProbeTimeout bounds one watchdog probe. A probe that does not answer in this time counts as a hung
// main loop, so the ping is withheld and systemd restarts the agent after WatchdogSec.
const ProbeTimeout = 10 * time.Second

// WatchProbe is one condition the watchdog needs before it pings.
type WatchProbe struct {
	// Name appears in the log line that says why a ping was withheld.
	Name string
	// Run returns nil when the part of the agent is healthy. It must honour ctx.
	Run func(ctx context.Context) error
}

// Watchdog is the worker of agent.md section 3.1 ("Watchdog pinger"): every Every it runs its probes
// and, only if all of them passed, sends WATCHDOG=1 (issue AGENT-20, implementation note 2). A hung
// main loop therefore stops the pings and systemd restarts the agent.
//
// The two probes the daemon registers are the ones the issue names: the HTTP server must answer an
// internal /health request, and the state store's writer must drain. Both are bounded by
// ProbeTimeout, so a probe that blocks forever is a failed probe rather than a stuck watchdog.
type Watchdog struct {
	notify  *Notifier
	clock   config.Clock
	every   time.Duration
	probes  []WatchProbe
	log     *slog.Logger
	timeout time.Duration

	mu      sync.Mutex
	cancel  context.CancelFunc
	done    chan struct{}
	pings   int
	skipped int
}

// NewWatchdog returns a Watchdog that pings through n every `every` (DefaultWatchdogEvery when zero)
// on clock, after the probes pass.
func NewWatchdog(n *Notifier, clock config.Clock, every time.Duration, log *slog.Logger, probes ...WatchProbe) *Watchdog {
	if clock == nil {
		clock = config.RealClock{}
	}
	if every <= 0 {
		every = DefaultWatchdogEvery
	}
	if log == nil {
		log = slog.Default()
	}
	return &Watchdog{notify: n, clock: clock, every: every, probes: probes, log: log.With("component", "watchdog"), timeout: ProbeTimeout}
}

// Name implements Worker.
func (w *Watchdog) Name() string { return "watchdog" }

// Start runs the pinger in a goroutine and returns. It does not ping before the first interval:
// READY=1 has just been sent.
func (w *Watchdog) Start(ctx context.Context) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.cancel != nil {
		return errors.New("watchdog: already started")
	}
	loopCtx, cancel := context.WithCancel(ctx)
	w.cancel, w.done = cancel, make(chan struct{})
	tick := w.clock.NewTicker(w.every)
	go func(done chan struct{}) {
		defer close(done)
		defer tick.Stop()
		for {
			select {
			case <-loopCtx.Done():
				return
			case <-tick.C():
				w.Beat(loopCtx)
			}
		}
	}(w.done)
	return nil
}

// Beat runs the probes once and pings if they all passed. It reports whether a ping was sent. The
// loop calls it on every tick; tests call it directly.
func (w *Watchdog) Beat(ctx context.Context) bool {
	if err := w.check(ctx); err != nil {
		w.mu.Lock()
		w.skipped++
		w.mu.Unlock()
		w.log.Error("watchdog ping withheld: the main loop is not healthy", "error", err.Error())
		return false
	}
	if err := w.notify.Watchdog(); err != nil {
		w.log.Warn("watchdog ping failed", "error", err.Error())
		return false
	}
	w.mu.Lock()
	w.pings++
	w.mu.Unlock()
	return true
}

// check runs every probe, each under its own deadline, and returns the first failure.
func (w *Watchdog) check(ctx context.Context) error {
	for _, p := range w.probes {
		pctx, cancel := withDeadline(ctx, w.clock, w.timeout)
		err := runProbe(pctx, p)
		timed := timedOut(pctx)
		cancel()
		if timed {
			return fmt.Errorf("%s: no answer within %s", p.Name, w.timeout)
		}
		if err != nil {
			return fmt.Errorf("%s: %w", p.Name, err)
		}
	}
	return nil
}

// runProbe runs p.Run and returns when it answers or ctx ends, whichever is first: a probe that
// ignores its context must not hold the watchdog.
func runProbe(ctx context.Context, p WatchProbe) error {
	res := make(chan error, 1)
	go func() { res <- p.Run(ctx) }()
	select {
	case err := <-res:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Stop ends the pinger. After Stop the agent no longer pings, which is what shutdown wants: systemd
// has been told STOPPING=1 and stops watching.
func (w *Watchdog) Stop(ctx context.Context) error {
	w.mu.Lock()
	cancel, done := w.cancel, w.done
	w.cancel, w.done = nil, nil
	w.mu.Unlock()
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

// Counts returns how many pings were sent and how many were withheld.
func (w *Watchdog) Counts() (pings, withheld int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.pings, w.skipped
}
