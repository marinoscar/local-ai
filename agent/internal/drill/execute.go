package drill

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/events"
)

// runState is everything one run accumulates between its start and its end.
type runState struct {
	ar      *activeRun
	req     StartRequest
	checks  []Check
	doc     *Document
	log     *slog.Logger
	started time.Time

	captured    Captured
	haveCapture bool
	fenceOn     bool
	// stoppedBy is the id of the check whose Outcome.StopRun ended the main phase.
	stoppedBy string
}

// execute is the run, in its own goroutine. Whatever happens in the main phase (an abort, a stop of
// the agent, a panic) finalize runs on the way out: it restores the node, runs the post-restore
// checks, writes the document and clears drill.in_progress.
func (e *Engine) execute(ar *activeRun, req StartRequest, checks []Check) {
	defer e.release(ar)
	started := e.now()
	rs := &runState{
		ar: ar, req: req, checks: checks, started: started,
		log: e.log.With("run_id", ar.id),
		doc: &Document{
			SchemaVersion: SchemaVersion, RunID: ar.id, Trigger: req.Trigger,
			NodeID: e.d.NodeID, AgentVersion: e.d.AgentVersion, CatalogVersion: e.catalogVersion(),
			Mode: e.modeName(), Quick: req.Quick, StartedAt: started,
			Checks: []CheckResult{},
		},
	}
	ar.setDoc(rs.doc)
	defer e.finalize(rs)

	rs.log.Info("drill started", "trigger", string(req.Trigger), "quick", req.Quick, "checks", len(checks))
	e.begin(rs)
	e.mainPhase(rs)
}

// begin captures the node's state, persists drill.in_progress and the first checkpoint, announces the
// run and turns the fence on.
func (e *Engine) begin(rs *runState) {
	ctx := rs.ar.ctx
	if e.d.Restorer != nil {
		c, err := e.d.Restorer.Capture(context.WithoutCancel(ctx))
		if err != nil {
			// Nothing was changed yet, but nothing can be proved restored either: the document says
			// restored.ok false and the node is left as it is.
			rs.log.Error("could not capture the node state before the drill", "error", err.Error())
		} else {
			rs.captured, rs.haveCapture = c.normalized(), true
		}
	}
	rs.captured = rs.captured.normalized()
	if err := e.block.setInProgress(InProgress{RunID: rs.ar.id, StartedAt: rs.started, Captured: rs.captured}); err != nil {
		rs.log.Error("could not record drill.in_progress", "error", err.Error())
	}
	e.checkpoint(rs)
	e.publish(events.TypeDrillStarted, rs.ar.opID, events.DrillStarted{RunID: rs.ar.id, Trigger: string(rs.req.Trigger), Quick: rs.req.Quick})
	if e.d.Fence != nil {
		e.d.Fence.Enable()
		rs.fenceOn = true
	}
}

func (e *Engine) checkpoint(rs *runState) {
	snap := rs.ar.snapshot()
	if snap == nil {
		return
	}
	// The checkpoint is read by Recover: it is marked aborted, so a crash needs no further edit.
	snap.State = StateAborted
	snap.Overall = OverallAborted
	if err := e.d.Results.Checkpoint(snap); err != nil {
		rs.log.Warn("could not write the drill checkpoint", "error", err.Error())
	}
}

// mainPhase runs the PhaseMain checks in order. A check interrupted by an abort is dropped, not
// recorded: the run is over.
func (e *Engine) mainPhase(rs *runState) {
	ctx := rs.ar.ctx
	for _, c := range rs.checks {
		if c.Phase != PhaseMain {
			continue
		}
		if ctx.Err() != nil {
			return
		}
		if rs.stoppedBy != "" {
			e.record(rs, record{
				CheckResult: CheckResult{ID: c.ID, Area: c.Area, Result: ResultSkip, Details: map[string]any{"reason": "run_stopped", "by": rs.stoppedBy}},
				critical:    c.Critical,
			})
			continue
		}
		rec, stop, interrupted := e.runCheck(ctx, c, rs, nil)
		if interrupted {
			return
		}
		e.record(rs, rec)
		if stop {
			rs.stoppedBy = c.ID
		}
	}
}

// record adds a result to the run, logs it, publishes drill.check_done, sets the metric and writes
// the checkpoint.
func (e *Engine) record(rs *runState, rec record) {
	rs.ar.addRecord(rec)
	rs.log.Info("check done", "check_id", rec.ID, "result", string(rec.Result), "ms", rec.MS, "code", rec.Code)
	if e.d.Metrics != nil {
		e.d.Metrics.Set(MetricCheckMS, map[string]string{"check": rec.ID}, float64(rec.MS))
	}
	e.publish(events.TypeDrillCheckDone, rs.ar.opID, events.DrillCheckDone{RunID: rs.ar.id, ID: rec.ID, Result: string(rec.Result), Code: rec.Code})
	e.checkpoint(rs)
}

// env builds the read-only environment of a check.
func (e *Engine) env(rs *runState, restored *Restored) *Env {
	return &Env{
		RunID: rs.ar.id, Trigger: rs.req.Trigger, Quick: rs.req.Quick, Mode: rs.doc.Mode,
		Actor: e.actor(), Now: e.clock.Now, Log: rs.log, Restored: restored,
	}
}

// runCheck runs one check with its timeout. It returns interrupted when the run was aborted while the
// check ran. The timeout, a panic and an invalid outcome are failures with the check's FailCode.
func (e *Engine) runCheck(ctx context.Context, c Check, rs *runState, restored *Restored) (rec record, stop, interrupted bool) {
	base := CheckResult{ID: c.ID, Area: c.Area}
	if rs.req.Quick && c.SkipInQuick {
		base.Result, base.Details = ResultSkip, map[string]any{"reason": "quick"}
		return record{CheckResult: base, critical: c.Critical, neutral: true}, false, false
	}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	tk := e.clock.NewTicker(timeout)
	defer tk.Stop()
	env := e.env(rs, restored)
	start := e.clock.Now()

	done := make(chan Outcome, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				done <- Outcome{Result: ResultFail, Code: c.FailCode, Details: map[string]any{"reason": "panic", "panic": clip(fmt.Sprint(r), 200)}}
			}
		}()
		done <- c.Run(cctx, env)
	}()

	var out Outcome
	select {
	case out = <-done:
	case <-tk.C():
		cancel()
		out = Outcome{Result: ResultFail, Code: c.FailCode, Details: map[string]any{"reason": "timeout", "timeout_ms": timeout.Milliseconds()}}
	case <-ctx.Done():
		cancel()
		return record{}, false, true
	}
	if ctx.Err() != nil {
		return record{}, false, true
	}
	if !out.Result.valid() {
		out = Outcome{Result: ResultFail, Code: c.FailCode, Details: map[string]any{"reason": "invalid_result", "result": clip(string(out.Result), 40)}}
	}
	if out.Result == ResultPass {
		out.Code = ""
	}
	base.Result, base.Code, base.Details = out.Result, out.Code, out.Details
	base.MS = e.clock.Now().Sub(start).Milliseconds()
	return record{CheckResult: base, critical: c.Critical}, out.StopRun, false
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// finalize ends a run, whatever way the main phase ended. In order: the state restore (always, with a
// context an abort cannot cancel), the post-restore checks (only for a run that was not aborted), the
// fence report, the aggregation, the document and latest.json, state.json (drill.last_run_at and
// last_result, then drill.in_progress cleared: the restore is done and the document is on disk
// before the crash marker goes), the metrics and drill.done.
func (e *Engine) finalize(rs *runState) {
	panicked := false
	if r := recover(); r != nil {
		panicked = true
		rs.log.Error("drill ended by a bug in the engine", "panic", clip(fmt.Sprint(r), 200))
	}
	ar := rs.ar
	cause := context.Cause(ar.ctx)
	aborted := ar.ctx.Err() != nil

	restored := e.restore(rs)
	rs.doc.Restored = restored

	if !aborted && !panicked {
		e.postPhase(rs, restored)
	}

	var report FenceReport
	if rs.fenceOn {
		report = e.d.Fence.Disable()
	}
	rs.doc.NetworkFence = NetworkFence{Enabled: rs.fenceOn, BlockedDials: report.BlockedDials}

	recs := annotateFence(ar.records(), report)
	rs.doc.Checks = checkResults(recs)
	overall, ready, counts := aggregate(recs)
	rs.doc.Counts = counts
	if report.BlockedDials > 0 {
		rs.doc.NetworkFence.Code = CodeFenceDial
		rs.doc.NetworkFence.Callers = append([]string(nil), report.Callers...)
		if overall == OverallPass {
			overall = OverallWarn
		}
	}
	finished := e.now()
	rs.doc.FinishedAt = &finished
	switch {
	case panicked:
		rs.doc.State, rs.doc.Overall, rs.doc.SurvivalReady = StateAborted, OverallAborted, false
		rs.doc.Code, rs.doc.Reason = CodeInternal, "the drill ended because of an internal error"
	case aborted:
		rs.doc.State, rs.doc.Overall, rs.doc.SurvivalReady = StateAborted, OverallAborted, false
		rs.doc.Reason = abortReason(cause)
	default:
		rs.doc.Overall, rs.doc.SurvivalReady = overall, ready
		if restored != nil && !restored.OK {
			// A node left without its normal brain is a failed drill, whatever the checks said.
			rs.doc.Overall = OverallFail
		}
	}
	if restored != nil && !restored.OK && rs.doc.Code == "" {
		rs.doc.Code = CodeRestoreFailed
	}

	if err := e.d.Results.Save(rs.doc); err != nil {
		rs.log.Error("could not save the drill results", "error", err.Error())
	}
	if rs.doc.State != StateAborted {
		if err := e.block.setLast(finished, rs.doc.Overall); err != nil {
			rs.log.Error("could not record drill.last_result", "error", err.Error())
		}
	}
	if err := e.block.clearInProgress(); err != nil {
		rs.log.Error("could not clear drill.in_progress", "error", err.Error())
	}
	if rs.doc.State != StateAborted && e.d.Metrics != nil {
		e.d.Metrics.Set(MetricLastOverall, nil, overallMetric(rs.doc.Overall))
		e.d.Metrics.Set(MetricLastFinished, nil, float64(finished.Unix()))
	}
	// The reporter hears of the document before drill.done goes out, so a listener that reacts to the
	// event (the desktop opening Status, the cloud link pushing a snapshot) already finds the alert and
	// the snapshot block of this run.
	e.observe(rs.doc)
	e.publish(events.TypeDrillDone, ar.opID, doneEvent(rs.doc))
	rs.log.Info("drill finished", "overall", rs.doc.Overall, "survival_ready", rs.doc.SurvivalReady,
		"pass", counts.Pass, "warn", counts.Warn, "fail", counts.Fail, "skip", counts.Skip, "code", rs.doc.Code)
}

func abortReason(cause error) string {
	if cause == nil {
		return errAborted.Error()
	}
	return cause.Error()
}

func overallMetric(overall string) float64 {
	switch overall {
	case OverallWarn:
		return 1
	case OverallFail:
		return 2
	}
	return 0
}

// restore restores the captured state. nil means no restorer is wired. A restore that errors, or a
// capture that failed, is restored.ok false: the node might be left without its normal brain.
func (e *Engine) restore(rs *runState) *Restored {
	if e.d.Restorer == nil {
		return nil
	}
	if !rs.haveCapture {
		return &Restored{LoadedModels: []string{}, OK: false}
	}
	// An abort must not cancel the restore it asks for.
	r, err := e.d.Restorer.Restore(context.WithoutCancel(rs.ar.ctx), rs.captured)
	if r.LoadedModels == nil {
		r.LoadedModels = []string{}
	}
	if err != nil {
		rs.log.Error("the drill could not restore the node state", "error", err.Error())
		r.OK = false
	}
	return &r
}

// postPhase runs the PhasePostRestore checks (drl.restore).
func (e *Engine) postPhase(rs *runState, restored *Restored) {
	ctx := rs.ar.ctx
	for _, c := range rs.checks {
		if c.Phase != PhasePostRestore {
			continue
		}
		if ctx.Err() != nil {
			return
		}
		rec, _, interrupted := e.runCheck(ctx, c, rs, restored)
		if interrupted {
			return
		}
		e.record(rs, rec)
	}
}

func (e *Engine) catalogVersion() string {
	if e.d.CatalogVersion == nil {
		return ""
	}
	return e.d.CatalogVersion()
}

func (e *Engine) modeName() string {
	if e.d.Mode == nil {
		return "online"
	}
	return strings.ToLower(string(e.d.Mode.Info().Mode))
}
