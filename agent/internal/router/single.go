package router

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/catalog"
	"github.com/marinoscar/local-ai/agent/internal/events"
	"github.com/marinoscar/local-ai/agent/internal/state"
)

// Single-model fallback mode (llama-router.md section 4.8, ADR-0002 risk mitigation). With
// node.yaml llama.mode single, localai-llama.service is a classic one-model llama-server for the
// default model. "Load X" is then a restart of the AI service with a unit rewritten for X, so the
// orchestrator's load, unload and status take the paths of this file, and ApplyMode switches
// between the two modes. Nothing here leaves the machine: every call is to 127.0.0.1.

// HolderMode is the kind of a modelOpsMu holder that switches llama between router and single mode
// (BusyError.Kind, and the details.running of a 409 LAI-AGT-022).
const HolderMode = "llama_mode"

// Kinds of failure of a single-mode load that the router path does not have. Like the others of
// wait.go they go into details.cause, so they are part of the interface.
const (
	kindUnit          = "unit"           // the unit could not be backed up, rendered, written or reloaded
	kindRestartFailed = "restart_failed" // systemctl restart failed
)

// SingleUnits is what single mode needs of the unit generator: render and install the unit, and
// take and put back a backup. *UnitGenerator implements it.
type SingleUnits interface {
	UnitRegenerator
	Backup(ctx context.Context) (UnitBackup, error)
	Restore(ctx context.Context, b UnitBackup) error
}

var _ SingleUnits = (*UnitGenerator)(nil)

// singleRun is the bookkeeping of one load in single mode.
type singleRun struct {
	// root is the content root the model's files are on.
	root string
	// prev is the model the server serves before the load ("" when it serves none or is down);
	// prevDefault is the default in force before.
	prev        string
	prevDefault string
	// bk is the backup of the unit; changed says the unit file was replaced and restarted that
	// the service was (or was being) restarted: what a rollback has to undo.
	bk        UnitBackup
	haveBk    bool
	changed   bool
	restarted bool
}

// singleView is what a one-model server says: /health answers, and /v1/models lists its model.
type singleView struct {
	Healthy bool
	Models  []ModelStatus
	Err     error
}

// ready is true when the server is up and lists a model.
func (v singleView) ready() bool {
	if !v.Healthy || v.Err != nil {
		return false
	}
	for _, m := range v.Models {
		if m.Status.Resident() {
			return true
		}
	}
	return false
}

// serving reports whether the server serves id (as its id or an alias).
func (v singleView) serving(id string) bool {
	if !v.ready() {
		return false
	}
	for _, m := range v.Models {
		if m.Status.Resident() && m.serves(id) {
			return true
		}
	}
	return false
}

// current is the id the server serves, "" when it is down or lists none.
func (v singleView) current() string {
	if !v.ready() {
		return ""
	}
	for _, m := range v.Models {
		if m.Status.Resident() {
			return m.ID
		}
	}
	return ""
}

func (v singleView) ids() []string {
	var out []string
	if v.ready() {
		for _, m := range v.Models {
			if m.Status.Resident() {
				out = append(out, m.ID)
			}
		}
	}
	return out
}

// singleView reads the one-model server: GET /health, then GET /v1/models. A server that does not
// answer /health is not an error here, it is simply not loaded.
func (o *Orchestrator) singleView(ctx context.Context) singleView {
	if err := o.opt.Router.Health(ctx); err != nil {
		return singleView{Err: err}
	}
	l, ok := o.opt.Router.(SingleLister)
	if !ok {
		return singleView{Healthy: true, Err: errors.New("router: the client cannot list a one-model server")}
	}
	models, err := l.V1Models(ctx)
	return singleView{Healthy: true, Models: models, Err: err}
}

// singleStatus is the status of a model in single mode: loaded when /health is 200 and /v1/models
// lists the id (aliases included), unloaded otherwise.
func (o *Orchestrator) singleStatus(ctx context.Context, id string) Status {
	if o.singleView(ctx).serving(id) {
		return StatusLoaded
	}
	return StatusUnloaded
}

// singleDefault is the model single mode serves: the recorded default, else (survival mode) the
// profile's survival default, else the profile's default.
func singleDefault(in Inputs, st state.State) string {
	if st.Models.Default != nil && *st.Models.Default != "" {
		return *st.Models.Default
	}
	if in.Profile == nil {
		return ""
	}
	if in.Survival && in.Profile.Survival.DefaultModel != "" {
		return in.Profile.Survival.DefaultModel
	}
	return in.Profile.DefaultModel
}

// singleUnitInputs are the unit inputs of single mode for a model.
func (o *Orchestrator) singleUnitInputs(in Inputs, m *catalog.Model, root string) UnitInputs {
	var u UnitInputs
	if o.opt.UnitBase != nil {
		u = o.opt.UnitBase()
	}
	u.Mode = LlamaModeSingle
	if in.Profile != nil {
		u.Router = in.Profile.Router
	}
	u.Single = &SingleModel{Model: m, Root: root, Aliases: in.Llama.LegacyAliases[m.ID]}
	return u
}

func errSingleNotReady() *OpError {
	return opErr(CodePresetFailed, http.StatusInternalServerError,
		"Single-model mode is not set up on this computer: the AI service unit cannot be rewritten.", map[string]any{"mode": LlamaModeSingle}, nil)
}

// errSingleLast is the refusal of an unload in single mode: the service always runs one model.
func errSingleLast(id string) *OpError {
	return opErr(CodeRefused, http.StatusConflict,
		"In single-model mode the AI service always runs exactly one model. Use force to stop it.",
		map[string]any{"model": id, "mode": LlamaModeSingle, "requires": "force"}, nil)
}

// ---- load ----------------------------------------------------------------------------------

// prepareSingle is the snapshot step of a load in single mode (the lock is held): a model the server
// serves already answers at once; otherwise the load is planned. The fit rules that apply are the
// static ones: the running model stops with the restart, so what it holds now is not in the way.
func (o *Orchestrator) prepareSingle(ctx context.Context, op *loadOp) (*LoadResult, error) {
	m, in := op.model, op.in
	if o.opt.Units == nil || o.opt.Restarter == nil {
		return nil, errSingleNotReady()
	}
	view := o.singleView(ctx)
	if view.serving(m.ID) {
		o.touch(m.ID)
		o.log.Info("model already loaded", "op_id", op.opID, "model", m.ID, "phase", "check", "mode", LlamaModeSingle)
		return &LoadResult{ID: m.ID, Status: "loaded", AlreadyLoaded: true, Unloaded: []string{}, OpID: op.opID}, nil
	}
	root, _ := o.locate(in, m)
	st := o.opt.State.Snapshot()
	op.sg = &singleRun{root: root, prev: view.current(), prevDefault: singleDefault(in, st)}
	op.timeout = o.loadTimeout(in, m.ID)
	o.log.Info("model load planned", "op_id", op.opID, "model", m.ID, "phase", "plan", "mode", LlamaModeSingle, "previous", op.sg.prev)
	o.setLastLoad(m.ID, op.opID, state.LoadInProgress, 0)
	return nil, nil
}

// runSingle is steps 5 to 8 of a load in single mode: back up the unit, render it for the model,
// daemon-reload, restart, wait for /health and the model in /v1/models, and either record the new
// default or roll back to the previous unit.
func (op *loadOp) runSingle(ctx context.Context) (LoadResult, error) {
	o, id, sg := op.o, op.model.ID, op.sg
	res := LoadResult{ID: id, Status: "loaded", OpID: op.opID, Unloaded: []string{}}
	var stopping []string
	if sg.prev != "" && sg.prev != id {
		stopping = []string{sg.prev}
	}
	// A load in single mode is a restart: what runs stops now and nothing answers until the new
	// model is ready, so the downtime is the load time. The event says so, for the UI to show it.
	o.publish(events.TypeModelLoading, op.opID, events.ModelLoading{Model: id, Unloading: nz(stopping), OpID: op.opID, Restart: true})

	t := o.clock.Now()
	if f := o.singleSwitch(ctx, op); f != nil {
		o.log.Warn("model load failed", "op_id", op.opID, "model", id, "phase", "load", "mode", LlamaModeSingle, "cause", f.kind,
			"duration_ms", ms(o.clock.Now().Sub(t)))
		return op.fail(ctx, f, nil)
	}
	res.LoadMS = ms(o.clock.Now().Sub(t))
	res.Unloaded = append(res.Unloaded, stopping...)
	op.succeedSingle(ctx, res.LoadMS, stopping)
	return res, nil
}

// singleSwitch does the work of the restart. It returns nil when the model is served.
func (o *Orchestrator) singleSwitch(ctx context.Context, op *loadOp) *loadFailure {
	sg := op.sg
	bk, err := o.opt.Units.Backup(ctx)
	if err != nil {
		return &loadFailure{kind: kindUnit, err: err}
	}
	sg.bk, sg.haveBk = bk, true
	ur, err := o.opt.Units.Generate(ctx, o.singleUnitInputs(op.in, op.model, sg.root))
	sg.changed = ur.Written
	if err != nil {
		return &loadFailure{kind: kindUnit, err: err}
	}
	if ur.ReloadErr != nil {
		return &loadFailure{kind: kindUnit, err: ur.ReloadErr}
	}
	sg.restarted = true
	if err := o.opt.Restarter.RestartLlama(ctx); err != nil {
		if errors.Is(err, ErrInterrupted) {
			return interrupted()
		}
		return &loadFailure{kind: kindRestartFailed, err: err}
	}
	return o.waitSingleReady(ctx, op.model.ID, op.timeout)
}

// waitSingleReady polls until /health answers and /v1/models lists want (any model when want is
// empty), at most timeout. A server that answers with another model than the one asked for has
// finished loading, so waiting longer cannot help: that is not_listed at once.
func (o *Orchestrator) waitSingleReady(ctx context.Context, want string, timeout time.Duration) *loadFailure {
	start := o.clock.Now()
	for {
		v := o.singleView(ctx)
		now := o.clock.Now()
		if v.ready() {
			if want == "" || v.serving(want) {
				return nil
			}
			return &loadFailure{kind: kindNotListed}
		}
		if ctx.Err() != nil {
			return &loadFailure{kind: kindRouterDown, err: ctx.Err()}
		}
		if now.Sub(start) >= timeout {
			return &loadFailure{kind: kindTimeout, err: v.Err}
		}
		if !o.pause(ctx, o.opt.Timing.LoadPoll) {
			return interrupted()
		}
	}
}

// succeedSingle is step 7: the loaded model is the default.
func (op *loadOp) succeedSingle(ctx context.Context, loadMS int64, stopped []string) {
	o, id, sg := op.o, op.model.ID, op.sg
	now := o.clock.Now().UTC().Truncate(time.Second)
	def := id
	o.persist("load result", func(st *state.State) error {
		if st.Models.LastUsed == nil {
			st.Models.LastUsed = map[string]time.Time{}
		}
		st.Models.LastUsed[id] = now
		st.Models.DesiredLoaded = []string{id}
		st.Models.Default = &def
		st.Models.LastLoad = &state.LastLoad{ID: id, OpID: op.opID, Result: state.LoadOK, LoadMS: loadMS, At: now}
		return nil
	})
	o.countLoad(id, true, loadMS)
	o.clear(CodeNothingLoaded, "")
	o.clear(CodeDefaultNotStarted, "")
	o.unitApplied()
	for _, p := range stopped {
		o.publish(events.TypeModelUnloaded, op.opID, events.ModelUnloaded{Model: p, Reason: events.UnloadEvicted})
	}
	o.publish(events.TypeModelLoaded, op.opID, events.ModelLoaded{Model: id, LoadMS: loadMS})
	if sg.prevDefault != id {
		o.publish(events.TypeModelDefaultChanged, "", events.ModelDefaultChanged{Model: id, Previous: sg.prevDefault})
	}
	o.log.Info("model loaded", "op_id", op.opID, "model", id, "phase", "load", "mode", LlamaModeSingle, "duration_ms", loadMS)
}

// singleRollback is step 8 in single mode: put the previous unit back, restart the service and wait
// for it. What is loaded afterwards is read from the server; nothing loaded is LAI-LLM-012. As in
// router mode a rollback is never rolled back: it gets one try.
func (o *Orchestrator) singleRollback(ctx context.Context, op *loadOp) rollbackResult {
	var rb rollbackResult
	sg := op.sg
	o.countRollback()
	t := o.clock.Now()

	restored := true
	if sg.haveBk && sg.changed {
		if err := o.opt.Units.Restore(ctx, sg.bk); err != nil {
			restored = false
			o.log.Error("could not put the previous llama unit back", "op_id", op.opID, "phase", "rollback", "error", err)
		}
	}
	if sg.restarted && restored {
		if err := o.opt.Restarter.RestartLlama(ctx); err != nil {
			if errors.Is(err, ErrInterrupted) {
				rb.interrupted = true
				return rb
			}
			o.log.Error("could not restart the AI service after the rollback", "op_id", op.opID, "phase", "rollback", "error", err)
		} else if f := o.waitSingleReady(ctx, sg.prev, o.loadTimeout(op.in, sg.prev)); f != nil && f.kind == kindInterrupted {
			rb.interrupted = true
			return rb
		}
		o.unitApplied()
	}
	v := o.singleView(ctx)
	rb.loaded = v.ids()
	if v.ready() && !v.serving(op.model.ID) {
		rb.restored = []string{v.current()}
	}
	o.log.Info("rollback done", "op_id", op.opID, "model", op.model.ID, "phase", "rollback", "mode", LlamaModeSingle,
		"restored", rb.restored, "duration_ms", ms(o.clock.Now().Sub(t)))
	return rb
}

// ---- unload --------------------------------------------------------------------------------

// runSingleUnload is an unload in single mode (the lock is held): 409 LAI-AGT-023 unless force, which
// stops the unit. A model the server does not serve is already unloaded.
func (o *Orchestrator) runSingleUnload(ctx context.Context, req UnloadRequest, opID string, inCatalog bool, res UnloadResult) (UnloadResult, error) {
	id := req.ID
	if !o.singleView(ctx).serving(id) {
		if !inCatalog {
			return res, errNotFound(id)
		}
		res.AlreadyUnloaded = true
		return res, nil
	}
	if !req.Force {
		return res, errSingleLast(id)
	}
	if o.opt.Stopper == nil {
		return res, opErr(CodeLlamaDown, http.StatusInternalServerError, "The AI model service could not be stopped.",
			map[string]any{"model": id, "op_id": opID, "cause": "no_stopper"}, nil)
	}
	t := o.clock.Now()
	o.log.Info("model unload", "op_id", opID, "model", id, "phase", "evict", "mode", LlamaModeSingle)
	if err := o.opt.Stopper.StopLlama(ctx); err != nil {
		if errors.Is(err, ErrInterrupted) {
			return res, ErrInterrupted
		}
		return res, opErr(CodeLlamaDown, http.StatusInternalServerError, "The AI model service could not be stopped.",
			map[string]any{"model": id, "op_id": opID, "cause": "stop_failed"}, err)
	}
	o.persist("unload", func(st *state.State) error {
		out := make([]string, 0, len(st.Models.DesiredLoaded))
		for _, x := range st.Models.DesiredLoaded {
			if x != id {
				out = append(out, x)
			}
		}
		st.Models.DesiredLoaded = out
		return nil
	})
	o.publish(events.TypeModelUnloaded, opID, events.ModelUnloaded{Model: id, Reason: events.UnloadRequest})
	o.log.Info("model unloaded", "op_id", opID, "model", id, "phase", "evict", "mode", LlamaModeSingle, "duration_ms", ms(o.clock.Now().Sub(t)))
	return res, nil
}

// ---- the default in single mode --------------------------------------------------------------

// singleSetDefault is the part of SetDefault that differs in single mode: models.ini is not written,
// the unit is brought up to the new default without restarting anything (the generator raises
// LAI-AGT-016 for the changed command line), so a crash restart or the next load serves it.
func (o *Orchestrator) singleSetDefault(ctx context.Context, in Inputs, m *catalog.Model) {
	if o.opt.Units == nil {
		return
	}
	root, _ := o.locate(in, m)
	if _, err := o.opt.Units.Generate(ctx, o.singleUnitInputs(in, m, root)); err != nil {
		o.log.Warn("the llama unit was not rewritten for the new default", "model", m.ID, "mode", LlamaModeSingle, "error", err)
	}
}

// ---- switching between router and single mode -----------------------------------------------

// ModeRequest is one call of ApplyMode.
type ModeRequest struct {
	// LockWait is how long to wait for modelOpsMu; 0 means do not wait (busy is 409 LAI-AGT-022).
	LockWait time.Duration
	// Boot is true at agent start: the server may still be coming up, so ApplyMode first waits for
	// /health (Timing.RestartWait) before it compares the mode it runs in with node.yaml.
	Boot bool
}

// ModeResult is the outcome of ApplyMode.
type ModeResult struct {
	// OpID names the operation; Mode is node.yaml llama.mode.
	OpID string
	Mode string
	// Changed is true when llama was restarted to switch.
	Changed bool
	// Model is the model single mode serves; Restored the models router mode brought back.
	Model    string
	Restored []string
}

// ApplyMode brings the running llama to node.yaml llama.mode (llama-router.md section 4.8). The
// config watcher calls it when the mode changes and the agent calls it at start (Boot). When the
// server already runs in that mode it does nothing. Otherwise, holding modelOpsMu:
//
//   - to single: render the unit for the default model, daemon-reload, restart and wait for /health
//     and the model in /v1/models; a failure raises LAI-LLM-013;
//   - to router: render the unit, bring models.ini up to the default (single mode may have moved it),
//     daemon-reload, restart, wait for /health and restore the models as RestartAndRestore does.
//
// LAI-LLM-023 is raised while the unit is in single mode and cleared when router mode is back (the
// unit generator does it). The switch is never rolled back: node.yaml says what the operator wants.
// It does nothing without a unit generator and a restarter.
func (o *Orchestrator) ApplyMode(ctx context.Context, req ModeRequest) (ModeResult, error) {
	var res ModeResult
	if o.isClosing() {
		return res, ErrInterrupted
	}
	if o.opt.Units == nil || o.opt.Restarter == nil {
		return res, nil
	}
	hold, err := o.lock.AcquireWait(ctx, HolderMode, req.LockWait)
	if err != nil {
		var busy *BusyError
		if errors.As(err, &busy) {
			return res, errBusy(busy)
		}
		return res, err
	}
	opctx := context.WithoutCancel(ctx)
	done := make(chan struct{})
	var rerr error
	o.wg.Add(1)
	go func() {
		defer o.wg.Done()
		defer close(done)
		unsup := o.suppress(hold.OpID)
		res, rerr = o.runApplyMode(opctx, hold.OpID, req)
		unsup()
		hold.Release()
	}()
	select {
	case <-done:
		return res, rerr
	case <-ctx.Done():
		return ModeResult{OpID: hold.OpID}, ctx.Err()
	}
}

func (o *Orchestrator) runApplyMode(ctx context.Context, opID string, req ModeRequest) (ModeResult, error) {
	in := o.opt.Inputs()
	target := in.Llama.Mode
	if target == "" {
		target = LlamaModeRouter
	}
	res := ModeResult{OpID: opID, Mode: target, Restored: []string{}}

	if req.Boot {
		o.waitHealth(ctx, o.opt.Timing.RestartWait)
	}
	prober, ok := o.opt.Router.(ServerProber)
	if !ok {
		return res, nil
	}
	running, perr := prober.ServerMode(ctx)
	switch {
	case perr == nil && running == target:
		return res, nil
	case perr != nil && !errors.Is(perr, ErrDown):
		// A refused key or a malformed answer is not cured by a restart.
		return res, errRouter(perr)
	}
	o.log.Info("switching the AI service mode", "op_id", opID, "phase", "mode", "to", target, "from", running)

	if target == LlamaModeSingle {
		return o.applySingle(ctx, in, opID, res)
	}
	return o.applyRouter(ctx, in, opID, res)
}

// applySingle switches to single mode.
func (o *Orchestrator) applySingle(ctx context.Context, in Inputs, opID string, res ModeResult) (ModeResult, error) {
	st := o.opt.State.Snapshot()
	id := singleDefault(in, st)
	m, ok := findModel(in.Catalog, id)
	if !ok || id == "" {
		o.raise(CodeNoDefault, "", map[string]any{"mode": LlamaModeSingle})
		return res, opErr(CodeNoDefault, http.StatusConflict, "There is no default model to run in single-model mode.", map[string]any{"mode": LlamaModeSingle, "op_id": opID}, nil)
	}
	res.Model = id
	if m.Engine != "llama" {
		return res, errNotLlama(id, m.Engine)
	}
	root, present := o.locate(in, m)
	if fits, why := in.Fit.StaticFit(Candidate{ID: m.ID, Requirements: m.Requirements, Present: present}); !fits {
		o.defaultFailed(id, "not_loadable")
		return res, errNotLoadable(id, why)
	}
	o.publish(events.TypeModelLoading, opID, events.ModelLoading{Model: id, Unloading: []string{}, OpID: opID, Restart: true})
	ur, err := o.opt.Units.Generate(ctx, o.singleUnitInputs(in, m, root))
	if err != nil {
		o.defaultFailed(id, kindUnit)
		return res, opErr(CodePresetFailed, http.StatusInternalServerError, "The AI service unit could not be written for single-model mode.",
			map[string]any{"model": id, "op_id": opID, "cause": kindUnit}, err)
	}
	if ur.ReloadErr != nil {
		o.log.Warn("daemon-reload failed while switching to single mode", "op_id", opID, "error", ur.ReloadErr)
	}
	t := o.clock.Now()
	if err := o.opt.Restarter.RestartLlama(ctx); err != nil {
		if errors.Is(err, ErrInterrupted) {
			return res, ErrInterrupted
		}
		o.defaultFailed(id, kindRestartFailed)
		return res, opErr(CodeLlamaDown, http.StatusInternalServerError, "The AI model service could not be restarted.",
			map[string]any{"model": id, "op_id": opID, "cause": kindRestartFailed}, err)
	}
	res.Changed = true
	if f := o.waitSingleReady(ctx, id, o.loadTimeout(in, id)); f != nil {
		if f.kind == kindInterrupted {
			return res, ErrInterrupted
		}
		o.defaultFailed(id, f.kind)
		e := errLoadFailed(id, f, false, nil, false)
		e.Details["op_id"], e.Details["mode"] = opID, LlamaModeSingle
		return res, e
	}
	loadMS := ms(o.clock.Now().Sub(t))
	o.persist("mode switch", func(s *state.State) error {
		s.Models.DesiredLoaded = []string{id}
		return nil
	})
	o.clear(CodeDefaultNotStarted, "")
	o.clear(CodeNothingLoaded, "")
	o.unitApplied()
	o.publish(events.TypeModelLoaded, opID, events.ModelLoaded{Model: id, LoadMS: loadMS})
	o.log.Info("the AI service runs in single-model mode", "op_id", opID, "model", id, "phase", "mode", "duration_ms", loadMS)
	return res, nil
}

// applyRouter switches back to router mode.
func (o *Orchestrator) applyRouter(ctx context.Context, in Inputs, opID string, res ModeResult) (ModeResult, error) {
	if _, err := o.opt.Units.Generate(ctx, o.routerUnitInputs(in)); err != nil {
		return res, opErr(CodePresetFailed, http.StatusInternalServerError, "The AI service unit could not be written for router mode.",
			map[string]any{"op_id": opID, "cause": kindUnit}, err)
	}
	// Single mode left models.ini alone, but loading models there moved the default: write the file
	// before the restart so the router starts with the right load-on-startup flag. The server that
	// still runs is not a router, so the generator finds nothing to tell and takes no lock.
	if o.opt.Generator != nil {
		st := o.opt.State.Snapshot()
		if st.Models.Default != nil {
			in.StateDefault = *st.Models.Default
		}
		if _, err := o.opt.Generator.Generate(ctx, in); err != nil {
			o.log.Warn("models.ini was not brought up to date for router mode", "op_id", opID, "error", err)
		}
	}
	if err := o.opt.Restarter.RestartLlama(ctx); err != nil {
		if errors.Is(err, ErrInterrupted) {
			return res, ErrInterrupted
		}
		return res, opErr(CodeLlamaDown, http.StatusInternalServerError, "The AI model service could not be restarted.",
			map[string]any{"op_id": opID, "cause": kindRestartFailed}, err)
	}
	res.Changed = true
	o.unitApplied()
	o.refreshBuild()
	if !o.waitHealth(ctx, o.opt.Timing.HealthWait) {
		return res, o.healthFailure(ctx, "mode", opID)
	}
	var rr RestartResult
	if err := o.restoreHeld(ctx, in, opID, &rr); err != nil {
		return res, err
	}
	res.Restored = rr.Restored
	o.log.Info("the AI service runs in router mode", "op_id", opID, "phase", "mode", "restored", rr.Restored, "skipped", len(rr.Skipped))
	return res, nil
}

// unitApplied clears the LAI-AGT-016 the unit generator raised for the changed command line: llama
// was restarted by the orchestrator itself, so the running service has the unit on disk.
func (o *Orchestrator) unitApplied() { o.clear(CodeUnitRestart, "llama-unit") }

// routerUnitInputs are the unit inputs of router mode.
func (o *Orchestrator) routerUnitInputs(in Inputs) UnitInputs {
	var u UnitInputs
	if o.opt.UnitBase != nil {
		u = o.opt.UnitBase()
	}
	u.Mode, u.Single = LlamaModeRouter, nil
	if in.Profile != nil {
		u.Router = in.Profile.Router
	}
	return u
}
