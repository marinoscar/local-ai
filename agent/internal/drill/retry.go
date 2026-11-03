package drill

import (
	"context"
	"errors"
	"time"
)

// The retry policy of a scheduled run a precondition held back (drill.md sections 4.1 and 5). The
// timers live in the agent, not in systemd, and the schedule is persisted in state.json
// (drill.deferred), so an agent restart keeps it.
const (
	// SyncRetryEvery and SyncRetryWindow: a sync that is running is retried daily for 7 days, and
	// then given up (the 45 day alert, LAI-DRL-006, covers a node that never gets its drill).
	SyncRetryEvery  = 24 * time.Hour
	SyncRetryWindow = 7 * 24 * time.Hour
	// BusyRetryEvery and BusyRetryWindow: a busy llama is retried every 2 hours for 48 hours, and
	// then the drill runs anyway as --quick.
	BusyRetryEvery  = 2 * time.Hour
	BusyRetryWindow = 48 * time.Hour
)

func retryPolicy(code string) (every, window time.Duration) {
	if code == CodeBusy {
		return BusyRetryEvery, BusyRetryWindow
	}
	return SyncRetryEvery, SyncRetryWindow
}

// refuse answers a start a precondition refused: a manual run is a *RefusedError; a scheduled run is
// recorded as a skipped document.
func (e *Engine) refuse(req StartRequest, retry *deferral, ref *Refusal) (StartResult, error) {
	if req.Trigger == TriggerManual {
		e.log.Info("drill refused", "code", ref.Code, "reason", ref.Reason)
		return StartResult{}, &RefusedError{Refusal: *ref}
	}
	return e.skipScheduled(req, retry, ref), nil
}

// skipScheduled records a scheduled run as skipped and plans its retry. The skipped document is
// written before Start returns, so the CLI that asked can read the reason at once.
func (e *Engine) skipScheduled(req StartRequest, retry *deferral, ref *Refusal) StartResult {
	now := e.now()
	var retryAt *time.Time
	reason := ref.Reason
	if ref.Retry {
		next, gaveUp := e.planDeferral(now, req, retry, ref)
		if gaveUp {
			reason += " (given up after " + describeWindow(ref.Code) + ")"
		} else {
			retryAt = &next
		}
	} else if e.block.deferral() != nil {
		// Something that is not retried (survival, battery, another drill) ends the wait.
		if err := e.block.setDeferral(nil); err != nil {
			e.log.Warn("could not clear the drill deferral", "error", err.Error())
		}
	}

	running, _ := e.Running()
	id := e.newRunID(now, running)
	doc := &Document{
		SchemaVersion: SchemaVersion, RunID: id, Trigger: req.Trigger, State: StateSkipped,
		NodeID: e.d.NodeID, AgentVersion: e.d.AgentVersion, CatalogVersion: e.catalogVersion(),
		Mode: e.modeName(), Quick: req.Quick, StartedAt: now, FinishedAt: &now,
		Overall: OverallSkipped, Checks: []CheckResult{},
		Code: ref.Code, Reason: reason, RetryAt: retryAt,
	}
	if err := e.d.Results.Save(doc); err != nil {
		e.log.Error("could not save the skipped drill document", "run_id", id, "error", err.Error())
	}
	attrs := []any{"run_id", id, "code", ref.Code, "reason", reason}
	if retryAt != nil {
		attrs = append(attrs, "retry_at", *retryAt)
	}
	e.log.Info("drill skipped", attrs...)
	return StartResult{RunID: id, State: StartSkipped, Code: ref.Code, Reason: reason, RetryAt: retryAt}
}

func describeWindow(code string) string {
	if code == CodeBusy {
		return "48 hours"
	}
	return "7 days"
}

// planDeferral persists the retry schedule for a refusal that is retried and returns the next attempt.
// A refusal with the code of the chain in progress extends that chain (the first attempt time and the
// attempt count are kept); another code starts a new one. gaveUp is true when the window is over: the
// deferral is cleared and nothing is scheduled.
func (e *Engine) planDeferral(now time.Time, req StartRequest, cur *deferral, ref *Refusal) (next time.Time, gaveUp bool) {
	every, window := retryPolicy(ref.Code)
	d := &deferral{Code: ref.Code, Quick: req.Quick, Only: req.Only, FirstAt: now}
	if cur != nil && cur.Code == ref.Code {
		d.FirstAt, d.Attempts = cur.FirstAt, cur.Attempts
	}
	d.Attempts++
	d.DeadlineAt = d.FirstAt.Add(window)
	if !now.Before(d.DeadlineAt) {
		if err := e.block.setDeferral(nil); err != nil {
			e.log.Warn("could not clear the drill deferral", "error", err.Error())
		}
		return time.Time{}, true
	}
	next = now.Add(every)
	if next.After(d.DeadlineAt) {
		next = d.DeadlineAt
	}
	d.NextRetryAt = next
	if err := e.block.setDeferral(d); err != nil {
		e.log.Warn("could not persist the drill deferral", "error", err.Error())
	}
	return next, false
}

// RetryDue starts the deferred scheduled run when its time has come. It reports whether an attempt was
// made, and what it did. The wiring calls it from RunRetries, and once at startup after Recover.
func (e *Engine) RetryDue(ctx context.Context) (StartResult, bool, error) {
	d := e.block.deferral()
	if d == nil || e.now().Before(d.NextRetryAt) {
		return StartResult{}, false, nil
	}
	res, err := e.start(ctx, StartRequest{Trigger: TriggerScheduled, Quick: d.Quick, Only: d.Only}, d)
	var invalid *InvalidRequestError
	if errors.As(err, &invalid) {
		// A schedule that can never start (a check that no longer exists) must not retry forever.
		if cerr := e.block.setDeferral(nil); cerr != nil {
			e.log.Warn("could not clear the drill deferral", "error", cerr.Error())
		}
	}
	return res, true, err
}

// RunRetries calls RetryDue on every tick until ctx ends. The caller owns the ticker (a minute is
// plenty: the shortest retry interval is 2 hours), so a test feeds the channel by hand.
func (e *Engine) RunRetries(ctx context.Context, ticks <-chan time.Time) {
	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-ticks:
			if !ok {
				return
			}
			if res, tried, err := e.RetryDue(ctx); err != nil {
				e.log.Warn("scheduled drill retry failed", "error", err.Error())
			} else if tried {
				e.log.Info("scheduled drill retried", "run_id", res.RunID, "state", string(res.State))
			}
		}
	}
}

// StartRetries runs RunRetries over a ticker of the engine's clock, every interval.
func (e *Engine) StartRetries(ctx context.Context, every time.Duration) {
	tk := e.clock.NewTicker(every)
	defer tk.Stop()
	e.RunRetries(ctx, tk.C())
}
