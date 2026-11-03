package migrate

import (
	"context"
	"errors"
	"fmt"
	"path"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/audit"
)

// Modes of a run, decided by Prepare from what progress.json says.
const (
	// ModeFresh starts a migration (nothing recorded, or a finished rollback).
	ModeFresh = "fresh"
	// ModeResume continues an interrupted migration from the recorded step.
	ModeResume = "resume"
	// ModeDone is a finished migration: a run only repairs the key link.
	ModeDone = "done"
	// ModeRollback continues an interrupted rollback.
	ModeRollback = "rollback"
)

// Result is the outcome of a run.
type Result struct {
	// State is done, rolled_back or already_done.
	State string `json:"state"`
	// ModelID and Alias are the model and the legacy name that still works.
	ModelID string `json:"model_id,omitempty"`
	Alias   string `json:"alias,omitempty"`
	// DowntimeSeconds is t_loaded - t0; zero when the old service was not running.
	DowntimeSeconds float64 `json:"downtime_seconds"`
	// KeyFingerprint is the key's first and last four characters.
	KeyFingerprint string `json:"key_fingerprint,omitempty"`
	// ServeUnchanged is true when Tailscale Serve is exactly as it was.
	ServeUnchanged bool `json:"serve_unchanged"`
	// ClientsToUpdate is always empty: that is the point of the migration.
	ClientsToUpdate []string `json:"clients_to_update"`
	Build           string   `json:"build,omitempty"`
	Warnings        []string `json:"warnings,omitempty"`
	FinishedAt      string   `json:"finished_at,omitempty"`
}

// Pending reports whether this node has a migration to run: an interrupted one, or a legacy unit that is
// there and not masked. `aiagent migrate` with no argument runs the legacy migration only when this is
// true, and exits 0 when it is false.
func (e *Engine) Pending(ctx context.Context) (bool, string, error) {
	prog, ok, err := e.readProgress()
	if err != nil {
		return true, "progress.json is unreadable", nil
	}
	if ok {
		switch prog.direction() {
		case dirForward:
			return true, "an interrupted migration (last step " + orStr(prog.Step, "none") + ")", nil
		case dirRollingBack:
			return true, "an interrupted rollback", nil
		}
	}
	fi, lerr := e.fs.Lstat(e.paths.LegacyUnitFile())
	if lerr != nil || !fi.Mode().IsRegular() {
		return false, "", nil
	}
	return true, LegacyUnitName + " is installed", nil
}

// Prepare decides what a run does and returns the plan: a fresh preflight, the plan of an interrupted
// migration or rollback, or the plan of a finished one. It changes nothing.
func (e *Engine) Prepare(ctx context.Context) (*Plan, error) {
	prog, ok, err := e.readProgress()
	if err != nil {
		return nil, err
	}
	mode := ModeFresh
	if ok {
		switch prog.direction() {
		case dirForward:
			mode = ModeResume
		case dirDone:
			mode = ModeDone
		case dirRollingBack:
			mode = ModeRollback
		}
	}
	var p *Plan
	if mode == ModeFresh {
		if p, err = e.Preflight(ctx); err != nil {
			return nil, err
		}
	} else if p, err = e.loadPlan(); err != nil {
		return nil, err
	}
	p.Mode, p.Progress, p.Resume = mode, prog, mode != ModeFresh
	e.prog = prog
	return p, nil
}

// Run does what Prepare planned. A forward run stops the agent (the single writer rule), goes through M1
// to M10 and starts the agent again; M6 to M8 failing rolls back by itself. It returns the Result of the
// run, or an *Error.
func (e *Engine) Run(ctx context.Context, p *Plan) (*Result, error) {
	switch p.Mode {
	case ModeDone:
		return e.repairDone(ctx, p)
	case ModeFresh:
		if !p.OK() {
			return nil, problemsError(p.Problems)
		}
	}
	if p.Mode == ModeFresh {
		// A new migration: forget the report of an earlier one, write the plan, and only then the
		// progress, so that a progress file always has a plan beside it.
		_ = e.removeFile(e.paths.Result())
		if err := e.savePlan(p); err != nil {
			return nil, err
		}
		if err := e.saveProgress(Progress{AgentStopped: e.prog.AgentStopped}); err != nil {
			return nil, err
		}
		e.auditLine("migration.llama_qwen.start", audit.ResultOK, "", map[string]string{"model": p.ModelID, "unit": LegacyUnitName})
	}
	if err := e.stopAgent(ctx); err != nil {
		return nil, err
	}
	defer e.startAgent(context.WithoutCancel(ctx))
	if p.Mode == ModeRollback {
		return e.finishRollback(ctx, p)
	}
	return e.forward(ctx, p)
}

// Rollback is `--rollback`: R1 to R6, from any state after the migration started (or finished). With
// nothing to roll back it says so and succeeds.
func (e *Engine) Rollback(ctx context.Context) (*Result, error) {
	prog, ok, err := e.readProgress()
	if err != nil {
		return nil, err
	}
	if !ok || prog.direction() == dirFresh {
		if e.legacyIntact() {
			e.say("Nothing to roll back: the migration did not run, %s is as it was.", LegacyUnitName)
			return &Result{State: "already_rolled_back"}, nil
		}
		return nil, fail(CodeInterrupted, "R2", "there is no migration record (%s) and %s is gone: restore it by hand from the backup, if one exists", e.paths.Progress(), LegacyUnitName)
	}
	e.prog = prog
	p, perr := e.loadPlan()
	if perr != nil {
		return nil, perr
	}
	if err := e.stopAgent(ctx); err != nil {
		return nil, err
	}
	defer e.startAgent(context.WithoutCancel(ctx))
	cause := "requested"
	if prog.Rollback != "" {
		cause = orStr(prog.Cause, cause)
	}
	return e.rollback(ctx, p, cause, e.restoreFiles || prog.RestoreFiles, false)
}

// legacyIntact is true when the old unit is a regular, non-masked unit file.
func (e *Engine) legacyIntact() bool {
	fi, err := e.fs.Lstat(e.paths.LegacyUnitFile())
	return err == nil && fi.Mode().IsRegular()
}

// ---- the single writer ----------------------------------------------------------------------------

// stopAgent stops localai-agent.service so that node.yaml, state.json and models.ini have one writer
// (llama-router.md section 4.9). The fact is recorded first, so a killed run is still known to have
// stopped it and the run that resumes starts it again.
func (e *Engine) stopAgent(ctx context.Context) error {
	if e.agent == nil {
		return nil
	}
	active, err := e.agent.Active(ctx)
	if err != nil {
		return wrap(CodeInterrupted, "", err, "cannot tell whether %s runs", "localai-agent.service")
	}
	if !active && !e.prog.AgentStopped {
		return nil
	}
	if !e.prog.AgentStopped {
		pr := e.prog
		pr.AgentStopped = true
		if err := e.saveProgress(pr); err != nil {
			return err
		}
	}
	if active {
		e.say("Stopping localai-agent.service while the migration runs (it is started again at the end) ...")
		if err := e.agent.Stop(ctx); err != nil {
			return wrap(CodeInterrupted, "", err, "cannot stop localai-agent.service")
		}
	}
	return nil
}

// startAgent starts the agent again when this migration stopped it.
func (e *Engine) startAgent(ctx context.Context) {
	if e.agent == nil || !e.prog.AgentStopped {
		return
	}
	if err := e.agent.Start(ctx); err != nil {
		e.warnf("localai-agent.service could not be started: %v. Start it with: sudo systemctl start localai-agent", err)
		return
	}
	pr := e.prog
	pr.AgentStopped = false
	if err := e.saveProgress(pr); err != nil {
		e.warnf("%v", err)
	}
}

// ---- the forward run ------------------------------------------------------------------------------

func (e *Engine) forward(ctx context.Context, p *Plan) (*Result, error) {
	if err := e.settleBeforeSwitch(ctx, p); err != nil {
		return nil, err
	}
	start := stepIndex(forwardSteps, e.prog.Step) + 1
	if p.Mode == ModeResume {
		e.say("Resuming from step %s.", forwardSteps[min(start, len(forwardSteps)-1)])
	}
	for _, id := range forwardSteps[start:] {
		e.say("%s %s ...", id, stepTitle[id])
		err := e.step(ctx, id, p)
		if err != nil {
			return nil, e.failed(ctx, p, id, err)
		}
		pr := e.prog
		pr.Step = id
		if serr := e.saveProgress(pr); serr != nil {
			return nil, serr
		}
	}
	return e.result(p), nil
}

var stepTitle = map[string]string{
	M0: "plan", M1: "build", M2: "key", M3: "models", M4: "config", M5: "pre-switch checks",
	M6: "stop the old service", M7: "start the new service", M8: "verify the contract",
	M9: "commit", M10: "report",
}

func (e *Engine) step(ctx context.Context, id string, p *Plan) error {
	switch id {
	case M0:
		return nil // the plan was written before the first step
	case M1:
		return e.m1Build(ctx, p)
	case M2:
		return e.m2Key(ctx, p)
	case M3:
		return e.m3Models(ctx, p)
	case M4:
		return e.m4Config(ctx, p)
	case M5:
		return e.m5Checks(ctx, p)
	case M6:
		return e.m6StopOld(ctx, p)
	case M7:
		return e.m7StartNew(ctx, p)
	case M8:
		return e.m8Verify(ctx, p)
	case M9:
		return e.m9Commit(ctx, p)
	case M10:
		return e.m10Report(ctx, p)
	}
	return fmt.Errorf("unknown step %s", id)
}

// failed turns the failure of a step into the Error the caller sees. A failure of M6, M7 or M8 happens
// with the old service stopped or about to be, so it rolls back by itself (llama-router.md section
// 4.9.3); any other failure leaves the old service serving and the progress where it was, so running the
// migration again continues.
func (e *Engine) failed(ctx context.Context, p *Plan, id string, err error) error {
	var me *Error
	if !errors.As(err, &me) {
		me = wrap(CodeInterrupted, id, err, "step %s failed", id)
	}
	if me.Step == "" {
		me.Step = id
	}
	switch id {
	case M6, M7, M8:
		e.say("Step %s failed: %s", id, me.Error())
		e.say("Rolling back: the old service is started again ...")
		// A Ctrl-C must not leave the node without a service: the rollback ignores it.
		_, rerr := e.rollback(context.WithoutCancel(ctx), p, me.Message, false, true)
		if rerr != nil {
			var re *Error
			if errors.As(rerr, &re) {
				re.Details = merge(re.Details, map[string]any{"failed_step": id, "cause": me.Message})
			}
			return rerr
		}
		me.RolledBack = true
		me.Code = CodeInterrupted
		me.Details = merge(me.Details, map[string]any{"rolled_back": true})
		return me
	}
	if me.Code == "" {
		me.Code = CodeInterrupted
	}
	e.auditLine("migration.llama_qwen.step", audit.ResultFail, me.Code, map[string]string{"step": id})
	return me
}

func merge(a, b map[string]any) map[string]any {
	if a == nil {
		a = map[string]any{}
	}
	for k, v := range b {
		a[k] = v
	}
	return a
}

// settleBeforeSwitch makes the state consistent after a kill: before M6 completed the old service is the
// one that serves, so if a run was killed after it had stopped the old service and before the new one
// started, the old one is started again first (llama-router.md section 4.9.4, "if neither unit is
// active ... else llama-qwen"); a resume from M6 on goes forward.
func (e *Engine) settleBeforeSwitch(ctx context.Context, p *Plan) error {
	if p.Mode != ModeResume || e.prog.T0 == nil || stepIndex(forwardSteps, e.prog.Step) >= stepIndex(forwardSteps, M6) {
		return nil
	}
	old, err := e.legacyActive(ctx)
	if err != nil {
		return wrap(CodeInterrupted, "", err, "cannot read the state of %s", LegacyUnitName)
	}
	newSt, err := e.sd.State(ctx, NewUnitName)
	if err != nil {
		return wrap(CodeInterrupted, "", err, "cannot read the state of %s", NewUnitName)
	}
	if old || newSt.Active() {
		return nil
	}
	e.say("The previous run stopped %s and did not finish: starting it again before going on (%s).", LegacyUnitName, CodeInterrupted)
	if err := e.sd.Start(ctx, LegacyUnitName); err != nil {
		return wrap(CodeInterrupted, M6, err, "cannot start %s again", LegacyUnitName)
	}
	if err := e.waitOldAnswers(ctx, e.timing.RollbackWait); err != nil {
		return err
	}
	pr := e.prog
	pr.T0 = nil
	return e.saveProgress(pr)
}

// result builds the report of a finished migration and writes result.json.
func (e *Engine) result(p *Plan) *Result {
	r := &Result{
		State: StateDone, ModelID: p.ModelID, Alias: p.legacyAlias(), KeyFingerprint: p.KeyFingerprint,
		ClientsToUpdate: []string{}, Warnings: p.Warnings, FinishedAt: e.clock.Now().UTC().Format(time.RFC3339),
	}
	if e.prog.T0 != nil && e.prog.TLoaded != nil {
		r.DowntimeSeconds = e.prog.TLoaded.Sub(*e.prog.T0).Seconds()
	}
	// M8 passed, so Tailscale Serve is what it was (a change would have rolled the migration back).
	r.ServeUnchanged = true
	r.Build = path.Base(e.readLinkOr(e.paths.Current()))
	return r
}

// repairDone is a run on a finished migration: it only makes the old key path a link again (LAI-SEC-015).
func (e *Engine) repairDone(ctx context.Context, p *Plan) (*Result, error) {
	fixed, err := e.ensureKeyLink()
	if err != nil {
		return nil, err
	}
	if fixed {
		e.say("The old key path %s is a link to %s again.", e.paths.LegacyKey, e.paths.NewKey)
		e.auditLine("migration.llama_qwen.key_link", audit.ResultOK, "", nil)
	} else {
		e.say("The migration is already done: nothing to do.")
	}
	return &Result{State: "already_done", ModelID: p.ModelID, Alias: p.legacyAlias(), KeyFingerprint: p.KeyFingerprint, ClientsToUpdate: []string{}}, nil
}

// ensureKeyLink makes /etc/llama-qwen/api-key a symbolic link to /etc/localai/api-key when that key is
// valid and the old path is a different thing. fixed is true when it changed the path.
func (e *Engine) ensureKeyLink() (fixed bool, err error) {
	fi, lerr := e.fs.Lstat(e.paths.LegacyKey)
	if lerr != nil {
		if isNotExist(lerr) {
			return false, nil // the old key path is gone: nothing reads it
		}
		return false, wrap(CodeInterrupted, M9, lerr, "cannot look at %s", e.paths.LegacyKey)
	}
	if isLink(fi) {
		if t, err := e.fs.Readlink(e.paths.LegacyKey); err == nil && t == e.paths.NewKey {
			return false, nil
		}
	}
	if _, err := e.readKey(e.paths.NewKey); err != nil {
		return false, fail(CodeBadKey, M9, "%s is missing or not a valid key: the old key path is left alone", e.paths.NewKey)
	}
	return true, e.linkKey()
}

// linkKey replaces the old key path by a symbolic link to the current key: the link is made under a
// temporary name and renamed over the file, so the path is never missing.
func (e *Engine) linkKey() error {
	tmp := e.paths.LegacyKey[:len(e.paths.LegacyKey)-len("api-key")] + "api-key.lnk-tmp"
	if err := e.removeFile(tmp); err != nil {
		return wrap(CodeInterrupted, M9, err, "cannot clear %s", tmp)
	}
	if err := e.fs.Symlink(e.paths.NewKey, tmp); err != nil {
		return wrap(CodeInterrupted, M9, err, "cannot create the key link")
	}
	if err := e.fs.Rename(tmp, e.paths.LegacyKey); err != nil {
		_ = e.fs.Remove(tmp)
		return wrap(CodeInterrupted, M9, err, "cannot put the key link in place")
	}
	return syncDir(e.fs, e.paths.LegacyKeyDir())
}

// syncDir fsyncs a directory; the durability of a rename is best effort here, so a failure is ignored.
func syncDir(f FS, dir string) error {
	_ = f.SyncDir(dir)
	return nil
}
