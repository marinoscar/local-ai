package router

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/events"
)

// Kinds of failure of one load or unload step. They go into details.cause of the error answer and
// into the log, so they are part of the interface: never rename one.
const (
	kindRejected      = "rejected"       // the router refused POST /models/load
	kindFailed        = "failed"         // the child exited (status.failed, or it stopped after loading started)
	kindTimeout       = "timeout"        // the load deadline passed
	kindUnloadTimeout = "unload_timeout" // a model to evict did not stop in time
	kindRouterDown    = "router_down"    // the router stopped answering
	kindNotListed     = "not_listed"     // the router no longer lists the model
	kindInterrupted   = "interrupted"    // the agent shut down
)

// loadFailure is why one step of an operation failed.
type loadFailure struct {
	kind     string
	exitCode *int
	err      error
	// victim is the model that would not unload (kindUnloadTimeout).
	victim string
}

func (f *loadFailure) timeout() bool { return f.kind == kindTimeout }

// Error implements error, so LoadHeld can return a failure as it is.
func (f *loadFailure) Error() string {
	s := "router: load failed (" + f.kind + ")"
	if f.exitCode != nil {
		s += fmt.Sprintf(", exit code %d", *f.exitCode)
	}
	if f.err != nil {
		s += ": " + f.err.Error()
	}
	return s
}

// Unwrap exposes the router error behind the failure.
func (f *loadFailure) Unwrap() error { return f.err }

// failedExit is the failure of a child that exited; the exit code is reported when the router gave one.
func failedExit(s ModelStatus) *loadFailure {
	f := &loadFailure{kind: kindFailed}
	if s.Failed {
		code := s.ExitCode
		f.exitCode = &code
	}
	return f
}

func interrupted() *loadFailure { return &loadFailure{kind: kindInterrupted, err: ErrInterrupted} }

// progressFwd forwards the router's load progress as model.loading events, at most one per
// ProgressEvery (llama-router.md section 4.4 step 6).
type progressFwd struct {
	opID      string
	id        string
	unloading []string
	last      time.Time
}

func (o *Orchestrator) forward(f *progressFwd, now time.Time) {
	if f == nil || o.opt.Progress == nil {
		return
	}
	if !f.last.IsZero() && now.Sub(f.last) < o.opt.Timing.ProgressEvery {
		return
	}
	p, ok := o.opt.Progress.LatestProgress(f.id)
	if !ok || !p.HasValue {
		return
	}
	v := p.Value
	f.last = now
	o.publish(events.TypeModelLoading, f.opID, events.ModelLoading{Model: f.id, Unloading: nz(f.unloading), OpID: f.opID, Progress: &v})
}

func nz(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// alreadyRunning tells the router's 400 "model is already running" from any other refusal.
func alreadyRunning(err error) bool {
	var he *HTTPError
	return errors.As(err, &he) && he.Status == 400 && strings.Contains(strings.ToLower(he.Message), "already")
}

// notRunning tells the router's 400 answer to an unload of a model that is not running.
func notRunning(err error) bool {
	var he *HTTPError
	return errors.As(err, &he) && he.Status == 400
}

// loadOne starts a model and waits until it is loaded (llama-router.md section 4.4 step 6). A model
// the router says is already running is simply waited for.
func (o *Orchestrator) loadOne(ctx context.Context, id string, timeout time.Duration, fwd *progressFwd) *loadFailure {
	if o.isClosing() {
		return interrupted()
	}
	if err := o.opt.Router.Load(ctx, id); err != nil && !alreadyRunning(err) {
		kind := kindRejected
		if errors.Is(err, ErrDown) || errors.Is(err, ErrUnauthorized) {
			kind = kindRouterDown
		}
		return &loadFailure{kind: kind, err: err}
	}
	return o.waitLoaded(ctx, id, timeout, fwd)
}

// waitLoaded polls GET /models until id is loaded. The status "unloaded" with failed true, or after
// the model was seen loading, is a failure (the child exited); a router that does not answer for
// DownGrace is one too; the deadline is a timeout.
func (o *Orchestrator) waitLoaded(ctx context.Context, id string, timeout time.Duration, fwd *progressFwd) *loadFailure {
	t := o.opt.Timing
	start := o.clock.Now()
	var (
		seenLoading bool
		idle        int
		downSince   time.Time
	)
	for {
		statuses, err := o.opt.Router.Models(ctx)
		now := o.clock.Now()
		if err != nil {
			if downSince.IsZero() {
				downSince = now
			}
			if ctx.Err() != nil || now.Sub(downSince) > t.DownGrace {
				return &loadFailure{kind: kindRouterDown, err: err}
			}
		} else {
			downSince = time.Time{}
			s, ok := statusByID(statuses)[id]
			switch {
			case !ok:
				return &loadFailure{kind: kindNotListed}
			case s.Status.Resident():
				return nil
			case s.Status == StatusLoading:
				seenLoading, idle = true, 0
				o.forward(fwd, now)
			default:
				if s.Failed {
					return failedExit(s)
				}
				if idle++; seenLoading || idle >= loadedNotSeenPolls {
					return failedExit(s)
				}
			}
		}
		if now.Sub(start) >= timeout {
			return &loadFailure{kind: kindTimeout}
		}
		if !o.pause(ctx, t.LoadPoll) {
			return interrupted()
		}
	}
}

// unloadState is the outcome of waitUnloaded.
type unloadState int

const (
	unloadDone unloadState = iota
	unloadTimedOut
	unloadInterrupted
)

// waitUnloaded polls GET /models until id is not running any more, at most timeout. A router that
// does not answer cannot confirm it, so the wait goes on until the deadline.
func (o *Orchestrator) waitUnloaded(ctx context.Context, id string, timeout time.Duration) unloadState {
	start := o.clock.Now()
	for {
		statuses, err := o.opt.Router.Models(ctx)
		now := o.clock.Now()
		if err == nil {
			if s, ok := statusByID(statuses)[id]; !ok || !s.Status.Running() {
				return unloadDone
			}
		}
		if now.Sub(start) >= timeout {
			return unloadTimedOut
		}
		if !o.pause(ctx, o.opt.Timing.EvictPoll) {
			return unloadInterrupted
		}
	}
}

// stopOne asks the router to stop a model and waits for it (llama-router.md section 4.4 step 5).
// A model that is not running already counts as stopped.
func (o *Orchestrator) stopOne(ctx context.Context, id string, timeout time.Duration) *loadFailure {
	if err := o.opt.Router.Unload(ctx, id); err != nil && !notRunning(err) {
		kind := kindRejected
		if errors.Is(err, ErrDown) || errors.Is(err, ErrUnauthorized) {
			kind = kindRouterDown
		}
		return &loadFailure{kind: kind, err: err, victim: id}
	}
	switch o.waitUnloaded(ctx, id, timeout) {
	case unloadTimedOut:
		return &loadFailure{kind: kindUnloadTimeout, victim: id}
	case unloadInterrupted:
		return interrupted()
	}
	return nil
}
