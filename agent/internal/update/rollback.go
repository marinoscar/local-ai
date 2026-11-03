package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/execx"
	"github.com/marinoscar/local-ai/agent/internal/logx"
)

// Pending is /var/lib/localai/update/pending.json (root 0600): the marker of an update in progress. It exists
// from the moment the new binary is installed until its health check has decided (agent.md section 4.10).
// An update in progress lives here and not in state.json, so `aictl update` never writes state.json.
type Pending struct {
	From         string    `json:"from"`
	To           string    `json:"to"`
	StartedAt    time.Time `json:"started_at"`
	Deadline     time.Time `json:"deadline"`
	BootAttempts int       `json:"boot_attempts"`
	// Job is the cloud command to settle once the outcome is known; its claim token never leaves this file
	// and the inflight file.
	Job *JobRef `json:"job,omitempty"`
	// RolledBack is set by the rollback; the restored old agent sees it and reports it.
	RolledBack bool `json:"rolled_back,omitempty"`
	// Reason says why it was rolled back: health, boot_attempts or timer. Not in the spec's example; additive.
	Reason string `json:"reason,omitempty"`
}

// ErrPendingCorrupt is a marker that cannot be parsed.
var ErrPendingCorrupt = errors.New("update: pending.json is not valid")

// readPending returns the marker, or nil when there is none.
func (r *Runner) readPending() (*Pending, error) {
	data, err := readLimited(r.FS, r.Paths.Pending(), 64<<10)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var p Pending
	if err := json.Unmarshal(data, &p); err != nil || p.To == "" || p.From == "" {
		return nil, ErrPendingCorrupt
	}
	return &p, nil
}

// writePending writes the marker atomically, root 0600, fsynced.
func (r *Runner) writePending(p Pending) error {
	data, err := jsonMarshal(p)
	if err != nil {
		return err
	}
	if err := r.FS.MkdirAll(r.Paths.UpdateDir, 0o750); err != nil {
		return err
	}
	if err := config.WriteFileAtomicFS(r.FS, r.Paths.Pending(), data, 0o600, -1, -1); err != nil && !errors.Is(err, config.ErrNotDurable) {
		return err
	}
	return nil
}

func (r *Runner) removePending() error {
	if err := r.FS.Remove(r.Paths.Pending()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// clearStalePending removes a marker that nobody acted on long after its deadline (the rollback timer could
// not run), so one stuck marker cannot refuse every later update. A corrupt marker is removed too.
func (r *Runner) clearStalePending() error {
	p, err := r.readPending()
	switch {
	case errors.Is(err, ErrPendingCorrupt):
		r.Log.Warn("pending.json is not valid and is removed")
		return r.removePending()
	case err != nil:
		return fail(doctor.AGT099, "resolve", "The update record could not be read.", err)
	case p == nil:
		return nil
	}
	if r.Clock.Now().After(p.Deadline.Add(staleMarkerAfter)) {
		r.Log.Warn("a stale pending.json is removed", "from", p.From, "to", p.To)
		return r.removePending()
	}
	return nil
}

// arm is step 7: write the marker (fsynced), then start the transient timer that rolls the update back when
// nobody confirms it. A timer left over from an earlier update is stopped first (systemd-run refuses a unit
// name that exists). Both happen BEFORE the restart.
func (r *Runner) arm(ctx context.Context, p Pending) error {
	const msg = "The rollback guard of the update could not be armed, so the update was not applied."
	if err := r.writePending(p); err != nil {
		return fail(doctor.AGT009, "arm", msg, err)
	}
	r.stopTimer(ctx)
	args := []string{
		"--unit=localai-agent-rollback",
		fmt.Sprintf("--on-active=%ds", int(r.RollbackAfter.Seconds())),
		"--timer-property=AccuracySec=1s",
		r.Paths.Prev(), "update-rollback-check",
	}
	res, err := r.Exec.Run(ctx, execx.Cmd{Path: r.bin("systemd-run"), Args: args, Timeout: 10 * time.Second, MaxOut: 16 << 10})
	if err != nil {
		return fail(doctor.AGT009, "arm", msg, err)
	}
	if res.ExitCode != 0 {
		return fail(doctor.AGT009, "arm", msg, fmt.Errorf("systemd-run exit status %d: %s", res.ExitCode, firstLine(res.Stderr)))
	}
	return nil
}

// stopTimer stops the rollback timer and its service; it is best effort (a unit that does not exist is the
// normal case).
func (r *Runner) stopTimer(ctx context.Context) {
	for _, unit := range []string{timerUnit, serviceUnit} {
		res, err := r.Exec.Run(ctx, execx.Cmd{Path: r.bin("systemctl"), Args: []string{"stop", unit}, Timeout: 10 * time.Second, MaxOut: 16 << 10})
		if err != nil {
			r.Log.Debug("the rollback timer could not be stopped", "unit", unit, "error", logx.ScrubString(err.Error()))
		} else if res.ExitCode != 0 {
			r.Log.Debug("the rollback timer was not stopped", "unit", unit, "exit", res.ExitCode)
		}
	}
}

// restart is step 8: `systemctl --no-block restart localai-agent.service`. --no-block returns at once, so
// the process that asks (the agent itself, for a cloud update) is not waiting for its own death, and
// llama and the other units are not touched.
func (r *Runner) restart(ctx context.Context) error {
	const msg = "The agent could not be restarted, so the update was not applied."
	res, err := r.Exec.Run(ctx, execx.Cmd{Path: r.bin("systemctl"), Args: []string{"--no-block", "restart", agentUnit}, Timeout: 10 * time.Second, MaxOut: 16 << 10})
	if err != nil {
		return fail(doctor.AGT009, "restart", msg, err)
	}
	if res.ExitCode != 0 {
		return fail(doctor.AGT009, "restart", msg, fmt.Errorf("systemctl exit status %d: %s", res.ExitCode, firstLine(res.Stderr)))
	}
	return nil
}

// bin resolves systemctl or systemd-run: an override of the Runner, else the fixed PATH of execx.
func (r *Runner) bin(name string) string {
	switch {
	case name == "systemctl" && r.Systemctl != "":
		return r.Systemctl
	case name == "systemd-run" && r.SystemdRun != "":
		return r.SystemdRun
	}
	if p, err := execx.Lookup(name); err == nil {
		return p
	}
	return "/usr/bin/" + name
}

func firstLine(b []byte) string {
	s := string(b)
	for i, c := range s {
		if c == '\n' {
			s = s[:i]
			break
		}
	}
	return truncate(logx.ScrubString(s), 120)
}

// rollback is step 10: restore aiagent.prev as aiagent, rewrite the marker with rolled_back, and restart the
// agent. The old binary that starts reads the marker, reports the rollback and deletes it (Startup). The
// restart is --no-block and may kill the caller (the new agent rolling itself back).
func (r *Runner) rollback(ctx context.Context, p Pending, reason string) error {
	if err := r.restorePrev(); err != nil {
		return fail(doctor.AGT009, "rollback", "The previous agent could not be restored.", err)
	}
	p.RolledBack, p.Reason = true, reason
	if err := r.writePending(p); err != nil {
		return fail(doctor.AGT009, "rollback", "The rollback could not be recorded.", err)
	}
	r.Log.Warn("the update is rolled back", "from", p.From, "to", p.To, "reason", reason)
	return r.restart(ctx)
}

// RollbackOutcome is what RollbackCheck did.
type RollbackOutcome string

// The outcomes of RollbackCheck.
const (
	// RollbackNone: there is no marker, so the update was confirmed (or never happened). Nothing to do.
	RollbackNone RollbackOutcome = "none"
	// RollbackDone: the previous binary was restored and the restart asked for.
	RollbackDone RollbackOutcome = "rolled_back"
	// RollbackRetried: a rollback was already recorded and its restart is asked for again.
	RollbackRetried RollbackOutcome = "restart_retried"
)

// RollbackCheck is `aiagent update-rollback-check`, run by the transient timer from aiagent.prev when the
// update was not confirmed in time (agent.md section 4.10 step 10). With no marker the new agent confirmed
// the update and there is nothing to do. With a marker the new agent never passed its health check, so the
// previous binary is restored and the agent restarted.
func (r *Runner) RollbackCheck(ctx context.Context) (RollbackOutcome, error) {
	r.init()
	p, err := r.readPending()
	switch {
	case errors.Is(err, ErrPendingCorrupt):
		// Nothing is known about the update. Restore the previous binary if there is one: it is the version
		// that ran before the update, so it is the safe one.
		r.Log.Warn("pending.json is not valid; the previous agent is restored")
		if rerr := r.restorePrev(); rerr != nil {
			return RollbackNone, fail(doctor.AGT009, "rollback", "The previous agent could not be restored.", rerr)
		}
		_ = r.removePending()
		return RollbackDone, r.restart(ctx)
	case err != nil:
		return RollbackNone, fail(doctor.AGT099, "rollback", "The update record could not be read.", err)
	case p == nil:
		r.Log.Info("no update is pending; nothing to roll back")
		return RollbackNone, nil
	case p.RolledBack:
		r.Log.Warn("a rollback is recorded but the agent has not reported it; the restart is asked for again")
		return RollbackRetried, r.restart(ctx)
	}
	if err := r.rollback(ctx, *p, ReasonTimer); err != nil {
		return RollbackNone, err
	}
	return RollbackDone, nil
}

// Reasons of a rollback.
const (
	ReasonHealth       = "health"
	ReasonBootAttempts = "boot_attempts"
	ReasonTimer        = "timer"
)
