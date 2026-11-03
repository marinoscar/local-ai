package cloud

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/audit"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/logx"
)

// jobRun is one command the agent holds: from the moment its assignment is accepted into a slot
// until everything about it is finished (settled, inflight entry removed, snapshot pushed).
type jobRun struct {
	id       string
	fullType string
	plane    string
	token    string
	started  time.Time

	handler Handler

	// lease is guarded by Claimer.mu.
	lease time.Time

	settleMu sync.Mutex
	settled  bool

	leaseLost atomic.Bool

	// cancelRun cancels the handler's context; stopRenew ends the renewal loop.
	ctlMu     sync.Mutex
	cancel    context.CancelFunc
	stopRenew context.CancelFunc

	// renewExited closes when the renewal loop has returned (it is started for every command that
	// reaches the run step); the run waits for it before it settles, so no renewal can follow a result.
	renewExited chan struct{}

	done chan struct{}
}

// claimSettle reports whether the caller is the one that settles the command: true exactly once.
func (j *jobRun) claimSettle() bool {
	j.settleMu.Lock()
	defer j.settleMu.Unlock()
	if j.settled {
		return false
	}
	j.settled = true
	return true
}

func (j *jobRun) isSettled() bool {
	j.settleMu.Lock()
	defer j.settleMu.Unlock()
	return j.settled
}

func (j *jobRun) cancelRun() {
	j.ctlMu.Lock()
	c := j.cancel
	j.ctlMu.Unlock()
	if c != nil {
		c()
	}
}

func (j *jobRun) stopRenewing() {
	j.ctlMu.Lock()
	c := j.stopRenew
	j.ctlMu.Unlock()
	if c != nil {
		c()
	}
}

// entryLocked is the inflight entry of j; the caller holds Claimer.mu.
func (j *jobRun) entryLocked() InflightEntry {
	return InflightEntry{JobID: j.id, Type: j.fullType, Plane: j.plane, ClaimToken: j.token, LeaseExpiresAt: j.lease, StartedAt: j.started}
}

// start takes a slot for a and runs it in the background. A command whose id is already in flight
// is ignored: commands are at most once, so a repeated delivery is never run twice.
func (c *Claimer) start(link ClaimLink, s Session, a assignment) {
	name := strings.TrimPrefix(a.Type, TypePrefix)
	var h Handler
	if strings.HasPrefix(a.Type, TypePrefix) {
		h, _ = c.d.Handlers.Lookup(name)
	}
	lim, _ := TypeLimits(name)
	now := c.clk.Now()
	lease := a.Lease
	if lease.IsZero() {
		lease = now.Add(lim.Lease)
	}
	j := &jobRun{
		id: a.ID, fullType: a.Type, plane: s.Plane, token: a.Token, started: now, lease: lease,
		handler: h, done: make(chan struct{}), renewExited: make(chan struct{}),
	}
	c.mu.Lock()
	if _, dup := c.jobs[a.ID]; dup {
		c.mu.Unlock()
		c.log.Warn("the cloud delivered a command twice; the second copy is ignored", "job_id", a.ID)
		return
	}
	c.jobs[a.ID] = j
	c.mu.Unlock()
	go c.execute(link, s, j, a, name, lim)
}

// finish ends a job's life in the registry and gives its slot back.
func (c *Claimer) finish(j *jobRun) {
	c.mu.Lock()
	delete(c.jobs, j.id)
	c.mu.Unlock()
	close(j.done)
	c.freeSlotSignal()
}

// execute is the execute(a) of agent.md section 4.8.6, steps 1 to 7.
func (c *Claimer) execute(link ClaimLink, s Session, j *jobRun, a assignment, name string, lim Limits) {
	defer c.finish(j)
	logger := c.log.With("job_id", j.id, "type", printable(j.fullType, 64))
	logger.Info("a cloud command was claimed", "attempts", a.Attempts)

	// 1. Policy: localai.<name> with <name> in cloud.allow and in the set this build can run.
	h := j.handler
	if !strings.HasPrefix(a.Type, TypePrefix) || h == nil || !slices.Contains(s.Allow, name) {
		c.refuseNow(link, j, doctor.CLD057, "this node does not allow "+printable(a.Type, 64), logger)
		return
	}
	// 2. Decode the params strictly.
	params, err := h.Decode(a.Params)
	if err != nil {
		var de *DecodeError
		msg := "params: is not valid"
		if errors.As(err, &de) {
			msg = de.Error()
		}
		c.refuseNow(link, j, doctor.CLD058, msg, logger)
		return
	}

	// The stop path may have settled the command while it was being decoded: it is not run then.
	if j.isSettled() {
		return
	}
	// 3. Persist the inflight entry, then 4. renew the lease while the operation runs.
	c.mu.Lock()
	entry := j.entryLocked()
	c.mu.Unlock()
	if perr := c.putInflight(entry); perr != nil {
		logger.Warn("inflight.json could not be written", "error", logx.ScrubString(perr.Error()))
		if n, ok := h.(interface{ NeedsInflight() bool }); ok && n.NeedsInflight() {
			c.refuseNow(link, j, doctor.AGT099, "The command needs a record that survives a restart, and it could not be written.", logger)
			return
		}
	}
	renewCtx, stopRenew := context.WithCancel(context.Background())
	j.ctlMu.Lock()
	j.stopRenew = stopRenew
	j.ctlMu.Unlock()
	go c.renewLoop(renewCtx, link, j, a.RenewEvery, lim.Lease)

	// 5. Run with principal cloud, under the type's hard cap.
	runCtx, cancel := context.WithTimeout(context.Background(), lim.HardCap)
	j.ctlMu.Lock()
	j.cancel = cancel
	j.ctlMu.Unlock()
	job := Job{ID: j.id, Type: a.Type, Attempts: a.Attempts, Plane: s.Plane, Actor: audit.Cloud(j.id), Logger: logger}
	out, runErr := c.runHandler(runCtx, h, job, params, logger)
	cancel()
	stopRenew()
	<-j.renewExited

	// 6. Settle exactly once: if the stop path got there first (LAI-CLD-063), the outcome is dropped.
	if !j.claimSettle() {
		logger.Info("a command finished after it was settled; its outcome is dropped")
		return
	}
	if runErr == nil && out.Defer {
		// agent.update: the process is about to be replaced; the entry stays for the next one.
		c.writeAudit(j.id, j.fullType, audit.Result("deferred"), "")
		logger.Info("a command continues after the restart")
		return
	}
	c.settleOutcome(link, j, out, runErr, logger)

	// 7. Remove the inflight entry, run what follows the settle and push a fresh snapshot.
	c.removeInflight(j.id)
	if out.After != nil && runErr == nil {
		c.runAfter(out.After, logger)
		return // mode.offline: the transition ends with a snapshot of its own (link_stop)
	}
	pctx, pcancel := context.WithTimeout(context.Background(), callTimeout)
	defer pcancel()
	if _, perr := link.PushNow(pctx); perr != nil {
		logger.Debug("the snapshot after a command was not sent", "error", errText(perr))
	}
}

// refuseNow settles a command the node will not run: failure with a code, nothing executed.
func (c *Claimer) refuseNow(link ClaimLink, j *jobRun, code, msg string, logger *slog.Logger) {
	if !j.claimSettle() {
		return
	}
	logger.Warn("a cloud command was refused", "code", code)
	link.Counters().Failed.Add(1)
	ctx, cancel := context.WithTimeout(context.Background(), 4*callTimeout)
	defer cancel()
	c.sendFailure(ctx, link, j.plane, j.id, j.token, failureText(code, msg), len(settleRetries)+1)
	c.writeAudit(j.id, j.fullType, audit.ResultDenied, code)
	c.removeInflight(j.id)
}

// runHandler runs h, turning a panic into an error: a handler that crashes fails its command and
// nothing else.
func (c *Claimer) runHandler(ctx context.Context, h Handler, job Job, params any, logger *slog.Logger) (out Outcome, err error) {
	defer func() {
		if r := recover(); r != nil {
			logger.Error("a cloud command handler panicked", "panic", logx.ScrubString(fmt.Sprint(r)), "stack", logx.ScrubString(string(debug.Stack())))
			out = Outcome{}
			err = &JobError{Code: doctor.AGT099, Message: "The command failed with an internal error."}
		}
	}()
	return h.Run(ctx, job, params)
}

// settleOutcome sends the result or the failure of a finished command and records the audit line.
func (c *Claimer) settleOutcome(link ClaimLink, j *jobRun, out Outcome, runErr error, logger *slog.Logger) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*callTimeout+30*time.Second)
	defer cancel()
	attempts := len(settleRetries) + 1
	if runErr == nil {
		raw, merr := json.Marshal(out.Result)
		if merr == nil && len(raw) > MaxResultBytes {
			merr = errors.New("the result is too large")
		}
		if merr == nil {
			link.Counters().Succeeded.Add(1)
			c.sendResult(ctx, link, j.plane, j.id, j.fullType, j.token, raw, attempts)
			c.writeAudit(j.id, j.fullType, audit.ResultOK, "")
			logger.Info("a cloud command finished", "result", "ok")
			return
		}
		runErr = &JobError{Code: doctor.AGT099, Message: "The result of the command could not be sent: " + merr.Error()}
	}
	code, msg := CodeAndMessage(runErr)
	link.Counters().Failed.Add(1)
	c.sendFailure(ctx, link, j.plane, j.id, j.token, failureText(code, msg), attempts)
	c.writeAudit(j.id, j.fullType, audit.ResultFail, code)
	logger.Info("a cloud command finished", "result", "fail", "code", code)
}

// runAfter runs the step that follows a settle (mode.offline's transition), recovering from a panic.
func (c *Claimer) runAfter(fn func(context.Context), logger *slog.Logger) {
	defer func() {
		if r := recover(); r != nil {
			logger.Error("the step after a cloud command panicked", "panic", logx.ScrubString(fmt.Sprint(r)))
		}
	}()
	fn(context.Background())
}

// writeAudit writes the cloud.job line: the type, the result and the actor {kind: cloud, job_id}.
func (c *Claimer) writeAudit(jobID, typ string, result audit.Result, code string) {
	if c.d.Audit == nil {
		return
	}
	ev := audit.Event{
		Action: audit.ActionCloudJob, Result: result, Code: code, Actor: audit.Cloud(jobID),
		Target: map[string]string{"type": typ},
	}
	if err := c.d.Audit.Write(ev); err != nil {
		// The audit log mirrors every line to the journal, so the record exists anyway.
		c.log.Warn("audit write failed", "action", ev.Action, "error", err.Error())
	}
}

// ---------------------------------------------------------------------------------------------------
// inflight.json

// mutateInflight applies fn to the set of entries and saves it. The set starts from the file, so
// entries an earlier process left (agent.update) survive until their owner removes them.
func (c *Claimer) mutateInflight(fn func(m map[string]InflightEntry)) error {
	c.fileMu.Lock()
	defer c.fileMu.Unlock()
	if !c.fileLoaded {
		c.fileEntries = map[string]InflightEntry{}
		if entries, err := c.d.Inflight.Load(); err == nil {
			for _, e := range entries {
				c.fileEntries[e.JobID] = e
			}
		}
		c.fileLoaded = true
	}
	fn(c.fileEntries)
	list := make([]InflightEntry, 0, len(c.fileEntries))
	for _, e := range c.fileEntries {
		list = append(list, e)
	}
	sort.Slice(list, func(a, b int) bool {
		if !list[a].StartedAt.Equal(list[b].StartedAt) {
			return list[a].StartedAt.Before(list[b].StartedAt)
		}
		return list[a].JobID < list[b].JobID
	})
	return c.d.Inflight.Save(list)
}

func (c *Claimer) putInflight(e InflightEntry) error {
	return c.mutateInflight(func(m map[string]InflightEntry) { m[e.JobID] = e })
}

// removeInflight drops the entry of a command; the file goes when it is the last one.
func (c *Claimer) removeInflight(jobID string) {
	if err := c.mutateInflight(func(m map[string]InflightEntry) { delete(m, jobID) }); err != nil {
		c.log.Warn("inflight.json could not be updated", "error", logx.ScrubString(err.Error()))
	}
}

// recoverOnce reports the commands an earlier process left in inflight.json, the first time the loop
// starts in this process (agent.md section 4.8.6, step 6): an entry whose lease may still be valid
// is failed with LAI-AGT-017 (the command is never run again), any other entry is discarded. An
// entry the Recover hook takes (agent.update, AGENT-19) is left to it; without a hook an
// agent.update entry stays in the file.
func (c *Claimer) recoverOnce(ctx context.Context, link ClaimLink) {
	c.mu.Lock()
	done := c.recovered
	c.recovered = true
	c.mu.Unlock()
	if done {
		return
	}
	entries, err := c.d.Inflight.Load()
	if err != nil {
		if errors.Is(err, ErrInflightCorrupt) {
			c.log.Warn("inflight.json is not valid and is discarded")
			c.fileMu.Lock()
			c.fileLoaded = false
			c.fileMu.Unlock()
			if rerr := c.d.Inflight.Save(nil); rerr != nil {
				c.log.Warn("inflight.json could not be removed", "error", logx.ScrubString(rerr.Error()))
			}
		}
		return
	}
	for _, e := range entries {
		if ctx.Err() != nil {
			return
		}
		if c.d.Recover != nil && c.d.Recover(ctx, e) {
			continue
		}
		if e.Type == TypePrefix+TypeAgentUpdate {
			c.log.Info("an agent.update entry is left in inflight.json for the update code", "job_id", e.JobID)
			continue
		}
		if c.clk.Now().Before(e.LeaseExpiresAt) {
			c.log.Warn("a cloud command was interrupted because the agent restarted", "code", doctor.AGT017, "type", printable(e.Type, 64), "job_id", e.JobID)
			rctx, cancel := context.WithTimeout(ctx, recoverCall)
			c.sendFailure(rctx, link, e.Plane, e.JobID, e.ClaimToken, failureText(doctor.AGT017, "the agent restarted during the command"), 1)
			cancel()
			link.Counters().Failed.Add(1)
			c.writeAudit(e.JobID, e.Type, audit.ResultFail, doctor.AGT017)
		}
		c.removeInflight(e.JobID)
	}
}
