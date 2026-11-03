package cli

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/cli/progress"
	agentevents "github.com/marinoscar/local-ai/agent/internal/events"
)

// What `aictl ark sync --scheduled --wait` adds to a followed sync (ark-sync.md section 3.7, aictl.md
// section 4.10, SYNC-14). The exit codes, the skip rule (exit 0 with `i Sync skipped: <reason>.`) and the
// final summary are the shared run lifecycle of cmd_ark_run.go; this file holds the two things only the
// unit needs:
//
//   - a line in the journal for every item that settles, so `journalctl -u localai-sync` says what
//     happened to each one and not just the totals;
//   - a bounded stop: systemd sends SIGTERM and waits TimeoutStopSec (60 s) before it kills, so aictl
//     asks the agent to cancel (DELETE /ark/sync) and then keeps printing until the run says it stopped
//     or scheduledStopGrace has passed.

// scheduledStopGrace is how long aictl waits for the run to report that it stopped after the cancel was
// sent. It is shorter than the unit's TimeoutStopSec=60s, so aictl leaves of its own accord with a clear
// line and is never the process systemd has to kill. It is a variable only for the tests.
var scheduledStopGrace = 50 * time.Second

// arkItemLine prints the journal line of one settled item of a scheduled run:
//
//	✓ wikipedia-es-nopic: downloaded (2026-10)
//	✓ docs-bundle: already up to date (catalog-2026.11.02-1)
//	! stackexchange-electronics: left out, budget
//	! kit-llama-cpp: failed, checksum mismatch [LAI-SYN-020]
//
// progressed says that bytes moved for the item during this run (it was fetched, not found current).
func (c *Call) arkItemLine(d agentevents.SyncItemDone, progressed bool) {
	where := d.Item
	if d.Root != "" && d.Root != "internal" {
		where = d.Root + "/" + d.Item
	}
	tail := ""
	if d.Code != "" {
		tail = " [" + d.Code + "]"
	}
	why := ""
	if d.Reason != "" {
		why = ", " + d.Reason
	}
	switch d.Result {
	case "verified":
		verb := pick(c.Lang, "already up to date", "ya estaba al día")
		if progressed {
			verb = pick(c.Lang, "downloaded", "descargado")
		}
		ver := ""
		if d.Version != "" {
			ver = " (" + d.Version + ")"
		}
		c.Out.OK(where + ": " + verb + ver)
	case "failed":
		c.Out.Warn(where + ": " + pick(c.Lang, "failed", "falló") + why + tail)
	case "quarantined":
		c.Out.Warn(where + ": " + pick(c.Lang, "damaged, set aside", "dañado, apartado") + why + tail)
	case "skipped":
		c.Out.Warn(where + ": " + pick(c.Lang, "left out", "omitido") + why + tail)
	default:
		c.Out.Info(where + ": " + d.Result + why + tail)
	}
}

// stopGuard makes the stop of a scheduled sync bounded. It owns the interrupt channel of the follow:
// every SIGINT or SIGTERM is forwarded to the waiter, and once the cancel has gone out a timer sends
// one more wake-up when the grace is over, so the waiter asks the hook again and the hook says leave.
type stopGuard struct {
	grace   time.Duration
	out     chan struct{}
	expired atomic.Bool
	armOnce sync.Once
	mu      sync.Mutex
	timer   *time.Timer
}

// newStopGuard starts the forwarder of the signals. stop ends it and the timer.
func newStopGuard(grace time.Duration) (g *stopGuard, stop func()) {
	sig, stopSig := Interrupts()
	g = &stopGuard{grace: grace, out: make(chan struct{}, 4)}
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-sig:
				g.wake()
			case <-done:
				return
			}
		}
	}()
	var once sync.Once
	return g, func() {
		once.Do(func() {
			stopSig()
			close(done)
			g.mu.Lock()
			if g.timer != nil {
				g.timer.Stop()
			}
			g.mu.Unlock()
		})
	}
}

func (g *stopGuard) wake() {
	select {
	case g.out <- struct{}{}:
	default: // a wake-up is already waiting to be read
	}
}

// arm starts the grace period, once: the cancel has been sent.
func (g *stopGuard) arm() {
	g.armOnce.Do(func() {
		g.mu.Lock()
		g.timer = time.AfterFunc(g.grace, func() {
			g.expired.Store(true)
			g.wake()
		})
		g.mu.Unlock()
	})
}

// scheduledInterrupt is the interrupt hook of a scheduled sync. The first signal sends DELETE
// /ark/sync at once (nobody is there to ask) and starts the grace; a signal after that, or the same
// signal arriving twice (the process context and the signal channel both see it), only keeps waiting,
// until the grace is over: then aictl leaves, the run is still going to stop on its own, and its
// partial downloads are kept.
func (c *Call) scheduledInterrupt(ctx context.Context, cl *Client, g *stopGuard, cancelled *bool) progress.InterruptAction {
	if g.expired.Load() {
		c.Out.Warn(pick(c.Lang,
			fmt.Sprintf("The sync did not stop within %d seconds. aictl is leaving; the agent will stop it by itself and keep the partial downloads.", int(g.grace/time.Second)),
			fmt.Sprintf("La sincronización no se detuvo en %d segundos. aictl se va; el agente la detendrá solo y conservará las descargas parciales.", int(g.grace/time.Second))))
		return progress.Leave
	}
	if *cancelled {
		return progress.KeepWaiting
	}
	if _, err := cl.Do(ctx, http.MethodDelete, "/ark/sync", nil, nil); err != nil {
		return progress.Leave
	}
	*cancelled = true
	g.arm()
	return progress.KeepWaiting
}
