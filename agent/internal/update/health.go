package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/audit"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/logx"
	"github.com/marinoscar/local-ai/agent/internal/netx"
	"github.com/marinoscar/local-ai/agent/internal/state"
)

// StartupKind is what the post-start check decided.
type StartupKind string

// The decisions of Startup.
const (
	// StartupNone: there was no update in progress.
	StartupNone StartupKind = "none"
	// StartupUpdated: the new binary passed its health check; the update is confirmed.
	StartupUpdated StartupKind = "updated"
	// StartupRolledBack: this is the restored old binary (or the update did not take); the update failed.
	StartupRolledBack StartupKind = "rolled_back"
	// StartupRollingBack: the new binary failed its health check and a rollback was started; this process is
	// about to be replaced by the old binary, which reports it.
	StartupRollingBack StartupKind = "rolling_back"
	// StartupInterrupted: the process was told to stop while it was checking; the marker stays and the next
	// start (or the timer) decides.
	StartupInterrupted StartupKind = "interrupted"
)

// StartupResult is the decision of Startup.
type StartupResult struct {
	Kind     StartupKind
	From, To string
	// Reason is why a rollback happened: health, boot_attempts or timer.
	Reason string
	// Job is the cloud command waiting for the outcome, nil when the update was started from the CLI.
	Job *JobRef
	// Err is the last health problem for StartupRollingBack.
	Err error
}

// Startup is startup step 15 (agent.md section 4.1, section 4.10 steps 9 and 10): with no marker it does
// nothing. With a marker it decides what became of the update:
//
//   - the marker says rolled_back (or this process is not the target version): this is the restored old agent
//     (or the update never took). It records state.update.last = rolled_back, deletes the marker, stops the
//     timer, raises LAI-AGT-009, audits agent.update.rolled_back and emits update.state.
//   - otherwise this is the new agent: boot_attempts is incremented; at MaxBootAttempts it rolls back at once;
//     else Probe must pass within the health window. Success records ok, deletes the marker, stops the timer,
//     audits agent.update.ok and emits update.state. A failed probe starts the rollback.
//
// It never settles a cloud command: the result says which command waits (Job) and how it ended, and the
// cloud executor settles it with the persisted claim token (internal/cloud/jobs). The decision is also
// published through StartupDone and LastStartup for the code that took the inflight entry over.
func (r *Runner) Startup(ctx context.Context) (res StartupResult, err error) {
	r.init()
	defer func() {
		if res.Kind != StartupInterrupted {
			r.finishStartup(res)
		}
	}()
	p, err := r.readPending()
	switch {
	case errors.Is(err, ErrPendingCorrupt):
		r.Log.Warn("pending.json is not valid and is removed")
		_ = r.removePending()
		r.stopTimer(ctx)
		return StartupResult{Kind: StartupNone}, nil
	case err != nil:
		return StartupResult{Kind: StartupNone}, fail(doctor.AGT099, "health", "The update record could not be read.", err)
	case p == nil:
		return StartupResult{Kind: StartupNone}, nil
	}
	res = StartupResult{From: p.From, To: p.To, Job: p.Job, Reason: p.Reason}

	if p.RolledBack || !sameVersion(r.Current, p.To) {
		// The old binary runs again (or the new one never started): the update did not take.
		if res.Reason == "" {
			res.Reason = ReasonHealth
		}
		res.Kind = StartupRolledBack
		r.concludeRollback(ctx, *p, res.Reason)
		return res, nil
	}

	p.BootAttempts++
	if werr := r.writePending(*p); werr != nil {
		r.Log.Warn("boot_attempts could not be recorded", "error", logx.ScrubString(werr.Error()))
	}
	if p.BootAttempts >= MaxBootAttempts {
		res.Kind, res.Reason = StartupRollingBack, ReasonBootAttempts
		res.Err = fmt.Errorf("the new agent has started %d times without confirming the update", p.BootAttempts)
		return res, r.startRollback(ctx, *p, ReasonBootAttempts)
	}

	switch herr := r.healthy(ctx); {
	case herr == nil:
		res.Kind = StartupUpdated
		r.concludeOK(ctx, *p)
		return res, nil
	case ctx.Err() != nil:
		res.Kind = StartupInterrupted
		return res, nil
	default:
		res.Kind, res.Reason, res.Err = StartupRollingBack, ReasonHealth, herr
		r.Log.Error("the new agent failed its health check", "error", logx.ScrubString(herr.Error()))
		return res, r.startRollback(ctx, *p, ReasonHealth)
	}
}

// healthy runs Probe until it passes or the window is over. The waits use the injected clock.
func (r *Runner) healthy(ctx context.Context) error {
	if r.Probe == nil {
		return nil
	}
	deadline := r.Clock.Now().Add(r.HealthWindow)
	for {
		err := r.Probe(ctx)
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if r.Clock.Now().Add(r.HealthInterval).After(deadline) {
			return fmt.Errorf("not healthy within %s: %w", r.HealthWindow, err)
		}
		if serr := r.Clock.Sleep(ctx, r.HealthInterval); serr != nil {
			return serr
		}
	}
}

// startRollback restores the previous binary and restarts; the new process dies in the restart.
func (r *Runner) startRollback(ctx context.Context, p Pending, reason string) error {
	// The rollback must not depend on the context of a shutting-down process.
	ctx = context.WithoutCancel(ctx)
	return r.rollback(ctx, p, reason)
}

// concludeOK records a confirmed update.
func (r *Runner) concludeOK(ctx context.Context, p Pending) {
	r.recordLast(state.UpdateOK, p.From, p.To)
	if err := r.removePending(); err != nil {
		r.Log.Warn("pending.json could not be removed", "error", logx.ScrubString(err.Error()))
	}
	r.stopTimer(ctx)
	r.writeAudit(r.Actor, ActionOK, audit.ResultOK, "", map[string]string{"from": p.From, "to": p.To}, "")
	r.emit("", StateOK, p.From, p.To)
	r.Log.Info("the update is confirmed", "from", p.From, "to", p.To)
}

// concludeRollback records a rollback seen by the restored agent: LAI-AGT-009.
func (r *Runner) concludeRollback(ctx context.Context, p Pending, reason string) {
	r.recordLast(state.UpdateRolledBack, p.From, p.To)
	if err := r.removePending(); err != nil {
		r.Log.Warn("pending.json could not be removed", "error", logx.ScrubString(err.Error()))
	}
	r.stopTimer(ctx)
	r.writeAudit(r.Actor, ActionRolledBack, audit.ResultFail, doctor.AGT009,
		map[string]string{"from": p.From, "to": p.To, "reason": reason}, "")
	r.emit("", StateRolledBack, p.From, p.To)
	if r.Alerts != nil {
		if err := r.Alerts.Raise(doctor.AGT009, map[string]any{"from_version": p.From, "to_version": p.To, "reason": reason}); err != nil {
			r.Log.Warn("the update alert could not be raised", "error", logx.ScrubString(err.Error()))
		}
	}
	r.Log.Error("an agent update was rolled back", "code", doctor.AGT009, "from", p.From, "to", p.To, "reason", reason)
}

func (r *Runner) finishStartup(res StartupResult) {
	r.startMu.Lock()
	defer r.startMu.Unlock()
	select {
	case <-r.startDone:
		return
	default:
	}
	r.startResult = res
	close(r.startDone)
}

// StartupDone is closed when Startup has decided (also when there was nothing to decide). The code that takes
// an agent.update inflight entry over waits on it before it settles the command.
func (r *Runner) StartupDone() <-chan struct{} {
	r.init()
	return r.startDone
}

// LastStartup is the decision of Startup; valid once StartupDone is closed.
func (r *Runner) LastStartup() StartupResult {
	r.init()
	r.startMu.Lock()
	defer r.startMu.Unlock()
	return r.startResult
}

// LastResult is state.update.last as the state store holds it, nil when the Runner has no store or no update
// ever ran. It lets a process that crashed between recording the outcome and settling the cloud command still
// report the right result at its next start.
func (r *Runner) LastResult() *state.UpdateLast {
	r.init()
	if r.State == nil {
		return nil
	}
	l := r.State.Snapshot().Update.Last
	if l == nil {
		return nil
	}
	cp := *l
	return &cp
}

// Health is what GET /health answers (agent.md section 3.8.1), the part an updater reads.
type Health struct {
	Version  string `json:"version"`
	Starting bool   `json:"starting"`
}

// WaitOutcome is how WaitForUpdate ended.
type WaitOutcome string

// The outcomes of WaitForUpdate.
const (
	// WaitUpdated: /health reports the target version and starting is false.
	WaitUpdated WaitOutcome = "updated"
	// WaitRolledBack: /health reports the old version again and the marker is gone: the new binary failed
	// and the previous one was restored.
	WaitRolledBack WaitOutcome = "rolled_back"
)

// WaitForUpdate polls fetch (a /health read) until the agent reports the target version with starting
// false (WaitUpdated), or reports the old version with no marker left (WaitRolledBack: the old binary is
// back, LAI-AGT-009), or the timeout passes (an error with LAI-AGT-009). `aictl update` calls it after
// Update returned StatusRestarting. A read that fails is normal while the agent restarts and is retried.
// The waits use the injected clock.
func (r *Runner) WaitForUpdate(ctx context.Context, fetch func(context.Context) (Health, error), from, to string, timeout time.Duration) (WaitOutcome, error) {
	r.init()
	if timeout <= 0 {
		timeout = 120 * time.Second
	}
	deadline := r.Clock.Now().Add(timeout)
	for {
		h, err := fetch(ctx)
		if err == nil {
			switch {
			case h.Version == to && !h.Starting:
				return WaitUpdated, nil
			case h.Version == from && from != to:
				if p, perr := r.readPending(); perr == nil && p == nil {
					return WaitRolledBack, nil
				}
			}
		}
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		if r.Clock.Now().Add(time.Second).After(deadline) {
			return "", fail(doctor.AGT009, "health", "The agent did not report the new version in time. If it does not, the previous version is restored automatically.", err)
		}
		if serr := r.Clock.Sleep(ctx, time.Second); serr != nil {
			return "", serr
		}
	}
}

// FetchHealth reads GET <base>/api/v1/health with client (nil: a client with a 5 s timeout) for
// WaitForUpdate.
func FetchHealth(client *http.Client, base string) func(context.Context) (Health, error) {
	if client == nil {
		client = netx.Client("update.health")
		client.Timeout = 5 * time.Second
	}
	return func(ctx context.Context) (Health, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/v1/health", nil)
		if err != nil {
			return Health{}, err
		}
		resp, err := client.Do(req)
		if err != nil {
			return Health{}, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return Health{}, fmt.Errorf("health answered %d", resp.StatusCode)
		}
		var h Health
		if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&h); err != nil {
			return Health{}, err
		}
		return h, nil
	}
}

// sameVersion compares two version texts by semver precedence, falling back to the text itself.
func sameVersion(a, b string) bool {
	va, ea := ParseVersion(a)
	vb, eb := ParseVersion(b)
	if ea != nil || eb != nil {
		return a == b
	}
	return va.Compare(vb) == 0
}
