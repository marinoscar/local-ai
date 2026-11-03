package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/cloud"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/state"
	"github.com/marinoscar/local-ai/agent/internal/update"
)

// The executor of agent.update (agent.md section 4.8.6 and section 4.10; AGENT-19). It is the adapter between
// the claim loop and internal/update:
//
//   - Update (the jobs.AgentUpdater of Deps.AgentUpdate) runs steps 1 to 8 in the daemon with the command's
//     claim token written into pending.json, and returns Outcome{Defer: true}: the claim loop settles
//     nothing, the inflight entry stays, and systemd replaces this process.
//   - PostStart is startup step 15: the new process runs the health check (update.Runner.Startup) and then
//     settles the command it finds in inflight.json.
//   - Recover is ClaimerDeps.Recover: the claim loop hands it every inflight entry an earlier process left;
//     for agent.update it takes the entry over and settles it once the outcome is known.
//
// An outcome is settled through the Claimer's SettleResult or SettleFailure, which use the persisted plane
// and claim token, remove the entry and return cloud.ErrNotLinked while the link cannot talk; the settle is
// then retried until the lease is long gone.

// UpdateRunner is the part of *update.Runner the executor uses; the tests fake it.
type UpdateRunner interface {
	Update(ctx context.Context, req update.Request) (update.Result, error)
	Startup(ctx context.Context) (update.StartupResult, error)
	StartupDone() <-chan struct{}
	LastStartup() update.StartupResult
	LastResult() *state.UpdateLast
}

// UpdateSettler settles a command an earlier process left in inflight.json: *cloud.Claimer implements it.
type UpdateSettler interface {
	SettleResult(ctx context.Context, e cloud.InflightEntry, result any) error
	SettleFailure(ctx context.Context, e cloud.InflightEntry, code, message string) error
}

// Statuses of the agent.update result (cloud-app.md section 3.7).
const (
	UpdateStatusUpdated        = "updated"
	UpdateStatusRolledBack     = "rolled_back"
	UpdateStatusAlreadyCurrent = "already_current"
)

// Defaults of the settle-after-restart timing.
const (
	defaultAwaitStartup = 3 * time.Minute
	defaultSettleRetry  = 15 * time.Second
	settleSlack         = time.Minute
)

// AgentUpdate is the agent.update executor. Create it with its fields set, pass it as Deps.AgentUpdate and as
// the Recover hook, call Bind with the Claimer once it exists, and call PostStart at startup step 15.
type AgentUpdate struct {
	Runner UpdateRunner
	// Inflight is inflight.json: the claim token of a running command is read from it.
	Inflight cloud.InflightStore
	// Clock drives the waits; nil is the wall clock.
	Clock update.Clock
	// AwaitStartup is how long Recover waits for the health check to decide; default 3 minutes.
	AwaitStartup time.Duration
	// SettleRetry is the wait between settle attempts while the link is down; default 15 seconds.
	SettleRetry time.Duration
	Logger      *slog.Logger

	mu       sync.Mutex
	settler  UpdateSettler
	settling map[string]bool
	wg       sync.WaitGroup
}

var _ AgentUpdater = (*AgentUpdate)(nil)

// Bind gives the executor the Claimer. It is called once, before the first command can be settled.
func (a *AgentUpdate) Bind(s UpdateSettler) {
	a.mu.Lock()
	a.settler = s
	a.mu.Unlock()
}

func (a *AgentUpdate) clock() update.Clock {
	if a.Clock == nil {
		return update.RealClock{}
	}
	return a.Clock
}

func (a *AgentUpdate) log() *slog.Logger {
	if a.Logger != nil {
		return a.Logger.With("component", "cloud")
	}
	return slog.Default().With("component", "cloud")
}

// Update implements AgentUpdater: steps 1 to 8 of agent.md section 4.10 with the job's claim token kept in the
// marker. A release that is not newer is answered at once (already_current); an installed update returns
// Defer, and the new process settles the command.
func (a *AgentUpdate) Update(ctx context.Context, job cloud.Job, version string) (cloud.Outcome, error) {
	entry, ok, err := a.entry(job.ID)
	if err != nil || !ok {
		// Without the entry the new process could not settle the command: refuse before touching anything.
		return cloud.Outcome{}, jobErr(doctor.AGT099, "The update needs a record that survives the restart, and it could not be read.")
	}
	res, err := a.Runner.Update(ctx, update.Request{
		Version: version,
		Job:     &update.JobRef{JobID: job.ID, ClaimToken: entry.ClaimToken},
		Actor:   job.Actor,
	})
	if err != nil {
		return cloud.Outcome{}, &cloud.JobError{Code: update.CodeOf(err), Message: update.MessageOf(err)}
	}
	if res.Status == update.StatusRestarting {
		return cloud.Outcome{Defer: true}, nil
	}
	return cloud.Outcome{Result: AgentUpdateResult{FromVersion: res.From, ToVersion: res.To, Status: UpdateStatusAlreadyCurrent}}, nil
}

// PostStart is startup step 15 (agent.md section 4.1): the post-update health check, then the settle of the
// command that waits for it. It returns after the check (at most about a minute); the settle runs in the
// background and is retried while the cloud link is not up.
func (a *AgentUpdate) PostStart(ctx context.Context) error {
	res, err := a.Runner.Startup(ctx)
	if err != nil {
		a.log().Warn("the post-update check failed", "code", update.CodeOf(err), "error", err.Error())
	}
	if res.Job == nil {
		return err
	}
	if e, ok, lerr := a.entry(res.Job.JobID); lerr == nil && ok {
		a.track(ctx, e)
	} else {
		a.log().Info("the cloud command of the update has no inflight entry any more", "job_id", res.Job.JobID)
	}
	return err
}

// Recover is the ClaimerDeps.Recover hook: it takes over the agent.update entries an earlier process left in
// inflight.json (the claim loop would otherwise leave them untouched) and settles them once the health check
// has decided. Every other type is declined, so the claim loop reports it as interrupted (LAI-AGT-017).
func (a *AgentUpdate) Recover(ctx context.Context, e cloud.InflightEntry) bool {
	if e.Type != cloud.TypePrefix+cloud.TypeAgentUpdate {
		return false
	}
	a.track(ctx, e)
	return true
}

func (a *AgentUpdate) entry(jobID string) (cloud.InflightEntry, bool, error) {
	entries, err := a.Inflight.Load()
	if err != nil {
		return cloud.InflightEntry{}, false, err
	}
	for _, e := range entries {
		if e.JobID == jobID {
			return e, true, nil
		}
	}
	return cloud.InflightEntry{}, false, nil
}

// track starts, once per command, the goroutine that waits for the outcome and settles the command.
func (a *AgentUpdate) track(ctx context.Context, e cloud.InflightEntry) {
	a.mu.Lock()
	if a.settling == nil {
		a.settling = map[string]bool{}
	}
	if a.settling[e.JobID] {
		a.mu.Unlock()
		return
	}
	a.settling[e.JobID] = true
	a.mu.Unlock()
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		a.settle(context.WithoutCancel(ctx), e)
	}()
}

// Wait blocks until every settle in progress has ended (tests, and a graceful shutdown that wants to see the
// command settled). A settle waiting for the link ends when the lease is long over or the clock's Sleep fails.
func (a *AgentUpdate) Wait() { a.wg.Wait() }

// verdict is how an update command ends: a result or a failure.
type verdict struct {
	result any
	code   string
	msg    string
	// skip: this process is being replaced by the rollback; the restored agent settles.
	skip bool
}

func (a *AgentUpdate) settle(ctx context.Context, e cloud.InflightEntry) {
	var v verdict
	if a.awaitDecision(ctx) {
		v = a.verdictOf(e)
	} else {
		v = verdict{code: doctor.AGT017, msg: "The agent restarted during the update, and what happened to it is not known."}
	}
	if v.skip {
		return
	}
	a.deliver(ctx, e, v)
}

// awaitDecision waits (on the injected clock) until the post-update check has decided.
func (a *AgentUpdate) awaitDecision(ctx context.Context) bool {
	limit := a.AwaitStartup
	if limit <= 0 {
		limit = defaultAwaitStartup
	}
	clk := a.clock()
	deadline := clk.Now().Add(limit)
	done := a.Runner.StartupDone()
	for {
		select {
		case <-done:
			return true
		default:
		}
		if clk.Now().After(deadline) || clk.Sleep(ctx, time.Second) != nil {
			return false
		}
	}
}

// verdictOf maps the decision of the post-update check (or, after a crash between recording the outcome and
// settling, state.update.last) to the end of the command.
func (a *AgentUpdate) verdictOf(e cloud.InflightEntry) verdict {
	res := a.Runner.LastStartup()
	if res.Job != nil && res.Job.JobID == e.JobID {
		switch res.Kind {
		case update.StartupUpdated:
			return verdict{result: AgentUpdateResult{FromVersion: res.From, ToVersion: res.To, Status: UpdateStatusUpdated}}
		case update.StartupRolledBack:
			return rolledBack(res.From, res.To)
		case update.StartupRollingBack, update.StartupInterrupted:
			return verdict{skip: true}
		}
	}
	if last := a.Runner.LastResult(); last != nil && last.At.After(e.StartedAt) {
		switch last.Result {
		case state.UpdateOK:
			return verdict{result: AgentUpdateResult{FromVersion: last.From, ToVersion: last.To, Status: UpdateStatusUpdated}}
		case state.UpdateRolledBack:
			return rolledBack(last.From, last.To)
		case state.UpdateFailed:
			return verdict{code: doctor.AGT009, msg: "The update failed."}
		}
	}
	return verdict{code: doctor.AGT017, msg: "The agent restarted during the update, and what happened to it is not known."}
}

// rolledBack is the failure of a command whose update was rolled back: LAI-AGT-009, with the result shape
// {from_version, to_version, status: "rolled_back"} as the details of the message (the failure body of the
// command plane carries one text).
func rolledBack(from, to string) verdict {
	details, _ := json.Marshal(AgentUpdateResult{FromVersion: from, ToVersion: to, Status: UpdateStatusRolledBack})
	return verdict{code: doctor.AGT009, msg: fmt.Sprintf("The update to %s failed its health check and %s was restored. %s", to, from, details)}
}

// deliver settles the command, retrying while the link cannot talk, until the lease is long over.
func (a *AgentUpdate) deliver(ctx context.Context, e cloud.InflightEntry, v verdict) {
	retry := a.SettleRetry
	if retry <= 0 {
		retry = defaultSettleRetry
	}
	clk := a.clock()
	for {
		a.mu.Lock()
		s := a.settler
		a.mu.Unlock()
		err := cloud.ErrNotLinked
		if s != nil {
			if v.result != nil {
				err = s.SettleResult(ctx, e, v.result)
			} else {
				err = s.SettleFailure(ctx, e, v.code, v.msg)
			}
		}
		if err == nil {
			a.log().Info("the update command is settled", "job_id", e.JobID, "failed", v.result == nil)
			return
		}
		if !errors.Is(err, cloud.ErrNotLinked) {
			a.log().Warn("the update command could not be settled", "job_id", e.JobID, "error", err.Error())
			return
		}
		if clk.Now().After(e.LeaseExpiresAt.Add(settleSlack)) || clk.Sleep(ctx, retry) != nil {
			a.log().Warn("the update command was not settled before its lease ended; the entry stays", "job_id", e.JobID)
			return
		}
	}
}
