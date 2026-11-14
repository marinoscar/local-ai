package router

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/audit"
	"github.com/marinoscar/local-ai/agent/internal/catalog"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/events"
	"github.com/marinoscar/local-ai/agent/internal/state"
)

// Engine adapters (E11, issue #508; llama-router.md section 4.4, "Engines").
//
// The orchestrator is the one path that loads and unloads models, and since E11 a model is not always
// a llama-server child: an engine acestep model (music) runs in localai-music.service. Every engine
// is reached through an EngineAdapter. The llama adapter wraps the router calls the orchestrator
// always made (loadOne, stopOne, or a restart of the service in single mode), so a llama-only node
// behaves exactly as before; the other adapters are passed in OrchestratorOptions.Engines.
//
// What is shared across engines, under the one modelOpsMu:
//
//   - one fit check: the planner sees every resident model of every engine (graphics memory and RAM
//     summed over all of them; the count rule per engine: models_max for llama, one for the others);
//   - one LRU eviction order: a music model may be evicted for a language model and the reverse;
//   - one rollback: what a failed load evicted is started again on its own engine.
//
// G_others (the graphics memory the hardware sampler attributes to other programs) includes the
// music engine's own memory, because the sampler only knows the llama cgroup. The planner counts that
// model explicitly, so its measured (or declared) memory is taken out of G_others first.

// Codes of the engine adapters (llama-router.md section 8).
const (
	// CodeEngineStart: the engine's unit failed to start (500).
	CodeEngineStart Code = doctor.LLM040
	// CodeEngineNotReady: the engine did not become ready in time (504).
	CodeEngineNotReady Code = doctor.LLM041
	// CodeEngineNotFreed: the engine did not stop cleanly or did not free its graphics memory.
	CodeEngineNotFreed Code = doctor.LLM042
	// CodeEngineMissing: the model's engine is not installed on this node (422).
	CodeEngineMissing Code = doctor.LLM043
)

// Kinds of failure of an engine step (details.cause), next to those of wait.go.
const (
	kindEngineStart    = "engine_start"     // the engine's unit did not start
	kindEngineNotReady = "engine_not_ready" // the engine did not answer ready in time
	kindEngineStop     = "engine_stop"      // the engine did not stop or free its memory
)

// EngineModel is a model an engine holds now: starting (Ready false) or ready.
type EngineModel struct {
	ID    string
	Ready bool
	// VRAMMiB is the graphics memory the engine reports for the model; 0 when it does not know.
	VRAMMiB int64
}

// EngineStart is one start of a model on an engine.
type EngineStart struct {
	Model *catalog.Model
	// Dir is the model's directory on the content root that holds a verified copy.
	Dir string
	// Timeout bounds the wait for "ready".
	Timeout time.Duration
}

// EngineError is a failed engine step. Code is LAI-LLM-040 (did not start), 041 (not ready in time)
// or 042 (did not stop or free its memory); Cause is a short word for logs and details.cause.
type EngineError struct {
	Code  Code
	Cause string
	Err   error
}

// Error implements error.
func (e *EngineError) Error() string {
	s := string(e.Code) + ": engine " + e.Cause
	if e.Err != nil {
		s += ": " + e.Err.Error()
	}
	return s
}

// Unwrap exposes the cause.
func (e *EngineError) Unwrap() error { return e.Err }

// EngineAdapter runs the models of one catalog engine. internal/svc/acestep implements it for the
// music engine. The orchestrator calls it only while it holds modelOpsMu, so an adapter need not
// serialise its own calls against the orchestrator; it must be safe for concurrent reads.
type EngineAdapter interface {
	// Engine is the catalog engine the adapter runs ("acestep").
	Engine() string
	// Installed is nil when the engine's runtime is on this node; otherwise why not (LAI-LLM-043).
	Installed() error
	// Resident lists the models the engine holds now. An engine that is stopped holds none.
	Resident(ctx context.Context) ([]EngineModel, error)
	// Start loads a model and waits until the engine is ready, or until the timeout; an error is an
	// *EngineError (LAI-LLM-040 or 041). A model of the same engine that runs is replaced.
	Start(ctx context.Context, req EngineStart) error
	// Stop unloads the model (id "" means whatever runs) and waits until the engine has freed its
	// memory; an error is an *EngineError (LAI-LLM-042). Stopping an engine that runs nothing is a
	// no-op.
	Stop(ctx context.Context, id string) error
}

// adapter returns the adapter of a non-llama engine.
func (o *Orchestrator) adapter(engine string) (EngineAdapter, bool) {
	for _, a := range o.opt.Engines {
		if a.Engine() == engine {
			return a, true
		}
	}
	return nil, false
}

// ---- the llama adapter ---------------------------------------------------------------------

// llamaEngine is the llama-server side seen as an engine: in router mode a load is POST
// /models/load and the wait of llama-router.md section 4.4 step 6 (loadOne), an unload POST
// /models/unload and its wait (stopOne); in single mode the one model stops with the service
// (Stopper) and comes back with a restart (Restarter). It holds no state of its own.
type llamaEngine struct {
	o  *Orchestrator
	in Inputs
}

func (o *Orchestrator) llama(in Inputs) llamaEngine { return llamaEngine{o: o, in: in} }

func (l llamaEngine) single() bool { return l.in.Llama.Mode == LlamaModeSingle }

// Resident lists the running llama models with their effective graphics memory.
func (l llamaEngine) Resident(ctx context.Context) ([]EngineModel, error) {
	if l.single() {
		var out []EngineModel
		for _, id := range l.o.singleView(ctx).ids() {
			out = append(out, EngineModel{ID: id, Ready: true})
		}
		return out, nil
	}
	statuses, err := l.o.opt.Router.Models(ctx)
	if err != nil {
		return nil, err
	}
	var out []EngineModel
	for _, s := range statuses {
		if !s.Status.Running() {
			continue
		}
		em := EngineModel{ID: s.ID, Ready: s.Status.Resident()}
		if v := s.EffectiveMiB(); v != nil && *v > 0 {
			em.VRAMMiB = *v
		}
		out = append(out, em)
	}
	return out, nil
}

// start loads a llama model again (a rollback or a workload): it plans nothing.
func (l llamaEngine) start(ctx context.Context, id string) *loadFailure {
	if l.single() {
		if l.o.opt.Restarter == nil {
			return &loadFailure{kind: kindRestartFailed, err: errors.New("router: no restarter")}
		}
		if err := l.o.opt.Restarter.RestartLlama(ctx); err != nil {
			return &loadFailure{kind: kindRestartFailed, err: err}
		}
		return l.o.waitSingleReady(ctx, id, l.o.loadTimeout(l.in, id))
	}
	return l.o.loadOne(ctx, id, l.o.loadTimeout(l.in, id), nil)
}

// stop stops a llama model (evicted by another engine's model, or a workload).
func (l llamaEngine) stop(ctx context.Context, id string) *loadFailure {
	if l.single() {
		if l.o.opt.Stopper == nil {
			return &loadFailure{kind: kindRejected, err: errors.New("router: no stopper"), victim: id}
		}
		if err := l.o.opt.Stopper.StopLlama(ctx); err != nil {
			return &loadFailure{kind: kindRejected, err: err, victim: id}
		}
		return nil
	}
	return l.o.stopOne(ctx, id, l.o.opt.Timing.EvictTimeout)
}

// ---- one view of every engine ----------------------------------------------------------------

// engineFailure turns an adapter's error into a loadFailure for the orchestrator's bookkeeping.
func engineFailure(err error, victim string) *loadFailure {
	if errors.Is(err, ErrInterrupted) {
		return interrupted()
	}
	kind := kindEngineStart
	var ee *EngineError
	if errors.As(err, &ee) {
		switch ee.Code {
		case CodeEngineNotReady:
			kind = kindEngineNotReady
		case CodeEngineNotFreed:
			kind = kindEngineStop
		}
	}
	if victim != "" {
		kind = kindEngineStop
	}
	return &loadFailure{kind: kind, err: err, victim: victim}
}

// engineResidents is what the non-llama engines hold, as the planner sees it, and the graphics
// memory those models take that the hardware sampler counts in G_others. An adapter that cannot be
// read counts as holding nothing: a language model must never be refused because the music engine
// does not answer (doomsday first).
func (o *Orchestrator) engineResidents(ctx context.Context, in Inputs, st state.State) (loaded []Loaded, othersMiB int64) {
	for _, a := range o.opt.Engines {
		models, err := a.Resident(ctx)
		if err != nil {
			o.log.Warn("could not read what an engine holds", "engine", a.Engine(), "error", err)
			continue
		}
		for _, em := range models {
			l := Loaded{ID: em.ID, Engine: a.Engine(), LastUsed: st.Models.LastUsed[em.ID]}
			if m, ok := findModel(in.Catalog, em.ID); ok {
				l.Requirements = m.Requirements
			}
			if em.VRAMMiB > 0 {
				v := em.VRAMMiB
				l.EffectiveMiB = &v
			}
			othersMiB += l.UsedVRAM()
			loaded = append(loaded, l)
		}
	}
	return loaded, othersMiB
}

// withoutEngines is the Env with the engines' own graphics memory taken out of G_others.
func (e Env) withoutEngines(engineMiB int64) Env {
	e.GOthersMiB = nonNegative(e.GOthersMiB - engineMiB)
	return e
}

// stopAny stops a model on its engine ("" or llama: the router).
func (o *Orchestrator) stopAny(ctx context.Context, in Inputs, engine, id string) *loadFailure {
	if a, ok := o.adapter(engine); ok {
		if err := a.Stop(ctx, id); err != nil {
			return engineFailure(err, id)
		}
		return nil
	}
	return o.llama(in).stop(ctx, id)
}

// startAny starts a model on its engine again (a rollback or a workload).
func (o *Orchestrator) startAny(ctx context.Context, in Inputs, engine, id string) *loadFailure {
	a, ok := o.adapter(engine)
	if !ok {
		return o.llama(in).start(ctx, id)
	}
	m, found := findModel(in.Catalog, id)
	if !found {
		return &loadFailure{kind: kindNotListed}
	}
	root, present := o.locate(in, m)
	if !present {
		return &loadFailure{kind: kindNotListed}
	}
	if err := a.Start(ctx, EngineStart{Model: m, Dir: itemDir(root, m), Timeout: o.loadTimeout(in, id)}); err != nil {
		return engineFailure(err, "")
	}
	return nil
}

func errEngineMissing(id, engine string, cause error) *OpError {
	return opErr(CodeEngineMissing, http.StatusUnprocessableEntity,
		"The program this model needs is not installed on this computer.", map[string]any{"model": id, "engine": engine}, cause)
}

func errEngineSurvival(id string) *OpError {
	return opErr(CodeRefused, http.StatusConflict,
		"Music does not run in survival mode: the graphics memory is kept for the survival models.",
		map[string]any{"model": id, "mode": string(state.ModeSurvival)}, nil)
}

// errEngineFailed is the final answer of an engine load that failed and was rolled back.
func errEngineFailed(id string, f *loadFailure, restored []string) *OpError {
	d := map[string]any{"model": id, "rolled_back": true, "restored": append([]string{}, restored...), "cause": f.kind}
	if f.victim != "" {
		d["unloading"] = f.victim
	}
	switch f.kind {
	case kindEngineNotReady, kindTimeout:
		return opErr(CodeEngineNotReady, http.StatusGatewayTimeout,
			"The model did not become ready in time. The previous models were started again.", d, f.err)
	case kindEngineStop, kindUnloadTimeout:
		return opErr(CodeEngineNotFreed, http.StatusInternalServerError,
			"A model in the way did not stop or free its graphics memory. The previous models were started again.", d, f.err)
	case kindEngineStart:
		return opErr(CodeEngineStart, http.StatusInternalServerError,
			"The program for this model did not start. The previous models were started again.", d, f.err)
	}
	return opErr(CodeModelFailed, http.StatusInternalServerError,
		"The model failed to start. The previous models were started again.", d, f.err)
}

// ---- a load on another engine ----------------------------------------------------------------

// engineOp is one load of a non-llama model past the check and lock phases.
type engineOp struct {
	o       *Orchestrator
	a       EngineAdapter
	req     LoadRequest
	in      Inputs
	model   *catalog.Model
	dir     string
	opID    string
	hold    Hold
	timeout time.Duration
	// evict are the models to stop first, least recently used first; engineOf maps each to its engine
	// ("" for llama).
	evict    []string
	engineOf map[string]string
	warn     *Reason
	started  time.Time
}

// loadEngine is LoadWith for a model of another engine than llama. The sequence mirrors the llama
// one: check without the lock (the engine is installed, the model fits and is present, not in
// survival mode), take modelOpsMu, read every engine, plan with one GPU budget, then evict, start
// and wait, and roll back on failure.
func (o *Orchestrator) loadEngine(ctx context.Context, req LoadRequest, in Inputs, m *catalog.Model, a EngineAdapter) (LoadResult, error) {
	op, early, err := o.prepareEngine(ctx, req, in, m, a)
	if err != nil || early != nil {
		var res LoadResult
		if early != nil {
			res = *early
		}
		o.auditWrite(ctx, audit.ActionModelLoad, req.Actor, req.ID, res.OpID, err)
		return res, err
	}
	opctx := context.WithoutCancel(ctx)
	done := make(chan struct{})
	var res LoadResult
	var rerr error
	o.wg.Add(1)
	go func() {
		defer o.wg.Done()
		defer close(done)
		res, rerr = op.run(opctx)
		op.hold.Release()
		o.auditWrite(opctx, audit.ActionModelLoad, req.Actor, req.ID, op.opID, rerr)
		if !req.Wait && req.OnDone != nil {
			req.OnDone(res, rerr)
		}
	}()
	if !req.Wait {
		return LoadResult{ID: req.ID, Status: "loading", OpID: op.opID, Accepted: true, Warning: op.warn, Unloaded: []string{}}, nil
	}
	select {
	case <-done:
		return res, rerr
	case <-ctx.Done():
		return LoadResult{OpID: op.opID}, ctx.Err()
	}
}

func (o *Orchestrator) prepareEngine(ctx context.Context, req LoadRequest, in Inputs, m *catalog.Model, a EngineAdapter) (*engineOp, *LoadResult, error) {
	if in.Survival {
		return nil, nil, errEngineSurvival(m.ID)
	}
	if err := a.Installed(); err != nil {
		return nil, nil, errEngineMissing(m.ID, m.Engine, err)
	}
	root, present := o.locate(in, m)
	if ok, why := in.Fit.Loadable(m, present); !ok {
		return nil, nil, errNotLoadable(m.ID, why)
	}
	single := in.Llama.Mode == LlamaModeSingle
	if !single {
		if err := o.opt.Router.Health(ctx); err != nil {
			return nil, nil, errRouter(err)
		}
	}
	hold, err := o.lock.AcquireWait(ctx, HolderLoad, req.LockWait)
	if err != nil {
		var busy *BusyError
		if errors.As(err, &busy) {
			return nil, nil, errBusy(busy)
		}
		return nil, nil, err
	}
	keep := false
	defer func() {
		if !keep {
			hold.Release()
		}
	}()
	op := &engineOp{o: o, a: a, req: req, in: in, model: m, dir: itemDir(root, m), opID: hold.OpID, hold: hold,
		engineOf: map[string]string{}, started: o.clock.Now(), timeout: o.loadTimeout(in, m.ID)}

	if ems, rerr := a.Resident(ctx); rerr == nil && readyIn(ems, m.ID) {
		o.touch(m.ID)
		o.log.Info("model already loaded", "op_id", op.opID, "model", m.ID, "engine", m.Engine, "phase", "check")
		return nil, &LoadResult{ID: m.ID, Status: "loaded", AlreadyLoaded: true, Unloaded: []string{}, OpID: op.opID}, nil
	}
	st := o.opt.State.Snapshot()
	var loaded []Loaded
	if single {
		for _, id := range o.singleView(ctx).ids() {
			l := Loaded{ID: id, Engine: catalog.EngineLlama, LastUsed: st.Models.LastUsed[id]}
			if lm, ok := findModel(in.Catalog, id); ok {
				l.Requirements = lm.Requirements
			}
			loaded = append(loaded, l)
		}
	} else {
		statuses, err := o.opt.Router.Models(ctx)
		if err != nil {
			return nil, nil, errRouter(err)
		}
		loaded = loadedFrom(in, st, statuses, "")
	}
	eng, engMiB := o.engineResidents(ctx, in, st)
	for _, l := range eng {
		op.engineOf[l.ID] = l.Engine
	}
	loaded = append(loaded, eng...)
	plan, warn, perr := in.Fit.withoutEngines(engMiB).Plan(
		Candidate{ID: m.ID, Engine: m.Engine, Requirements: m.Requirements, Present: present}, loaded, req.UnloadOthers)
	if perr != nil {
		var pe *PlanError
		if errors.As(perr, &pe) {
			return nil, nil, errNoRoom(m.ID, pe.Reason)
		}
		return nil, nil, opErr(CodeNoRoom, http.StatusConflict, perr.Error(), map[string]any{"model": m.ID}, perr)
	}
	if single && o.opt.Stopper == nil {
		for _, e := range plan {
			if op.engineOf[e] == "" {
				return nil, nil, errNoRoom(m.ID, reasonNoRoom())
			}
		}
	}
	op.evict, op.warn = plan, warn
	o.log.Info("model load planned", "op_id", op.opID, "model", m.ID, "engine", m.Engine, "phase", "plan", "evict", op.evict)
	o.setLastLoad(m.ID, op.opID, state.LoadInProgress, 0)
	keep = true
	return op, nil, nil
}

func readyIn(ems []EngineModel, id string) bool {
	for _, em := range ems {
		if em.ID == id && em.Ready {
			return true
		}
	}
	return false
}

// run evicts, starts and waits, and rolls back on failure.
func (op *engineOp) run(ctx context.Context) (LoadResult, error) {
	o, id := op.o, op.model.ID
	res := LoadResult{ID: id, Status: "loaded", OpID: op.opID, Unloaded: []string{}, Warning: op.warn}
	o.publish(events.TypeModelLoading, op.opID, events.ModelLoading{Model: id, Unloading: nz(op.evict), OpID: op.opID})
	var evicted []string
	for _, e := range op.evict {
		t := o.clock.Now()
		if f := o.stopAny(ctx, op.in, op.engineOf[e], e); f != nil {
			if f.kind != kindInterrupted && f.victim == "" {
				f.victim = e
			}
			o.log.Warn("could not evict a model", "op_id", op.opID, "model", e, "phase", "evict", "cause", f.kind,
				"duration_ms", ms(o.clock.Now().Sub(t)))
			return op.fail(ctx, f, evicted)
		}
		evicted = append(evicted, e)
		o.publish(events.TypeModelUnloaded, op.opID, events.ModelUnloaded{Model: e, Reason: events.UnloadEvicted})
		o.log.Info("model evicted", "op_id", op.opID, "model", e, "phase", "evict", "duration_ms", ms(o.clock.Now().Sub(t)))
	}
	res.Unloaded = append([]string{}, evicted...)

	t := o.clock.Now()
	if err := op.a.Start(ctx, EngineStart{Model: op.model, Dir: op.dir, Timeout: op.timeout}); err != nil {
		f := engineFailure(err, "")
		o.log.Warn("model load failed", "op_id", op.opID, "model", id, "engine", op.model.Engine, "phase", "load", "cause", f.kind,
			"duration_ms", ms(o.clock.Now().Sub(t)))
		return op.fail(ctx, f, evicted)
	}
	res.LoadMS = ms(o.clock.Now().Sub(t))
	now := o.clock.Now().UTC().Truncate(time.Second)
	o.persist("load result", func(st *state.State) error {
		if st.Models.LastUsed == nil {
			st.Models.LastUsed = map[string]time.Time{}
		}
		st.Models.LastUsed[id] = now
		st.Models.LastLoad = &state.LastLoad{ID: id, OpID: op.opID, Result: state.LoadOK, LoadMS: res.LoadMS, At: now}
		return nil
	})
	o.countLoad(id, true, res.LoadMS)
	o.clear(CodeEngineNotFreed, op.model.Engine)
	o.publish(events.TypeModelLoaded, op.opID, events.ModelLoaded{Model: id, LoadMS: res.LoadMS})
	o.log.Info("model loaded", "op_id", op.opID, "model", id, "engine", op.model.Engine, "phase", "load", "duration_ms", res.LoadMS)
	return res, nil
}

// fail stops what started, starts again what was evicted (most recently used first) and answers.
func (op *engineOp) fail(ctx context.Context, f *loadFailure, evicted []string) (LoadResult, error) {
	o, id := op.o, op.model.ID
	res := LoadResult{ID: id, OpID: op.opID, Unloaded: []string{}}
	if f.kind == kindInterrupted {
		o.setLastLoad(id, op.opID, state.LoadInterrupted, 0)
		return res, ErrInterrupted
	}
	o.countRollback()
	if err := op.a.Stop(ctx, ""); err != nil {
		o.log.Warn("the engine did not stop after a failed load", "op_id", op.opID, "model", id, "engine", op.model.Engine,
			"code", string(CodeEngineNotFreed), "error", err)
		o.raise(CodeEngineNotFreed, op.model.Engine, map[string]any{"model": id})
	}
	restored := o.restoreAll(ctx, op.in, op.opID, evicted, op.engineOf)
	result := state.LoadFailed
	if f.kind == kindEngineNotReady || f.timeout() {
		result = state.LoadTimeout
	}
	o.setLastLoad(id, op.opID, result, 0)
	oe := errEngineFailed(id, f, restored)
	o.countLoad(id, false, 0)
	o.publish(events.TypeModelFailed, op.opID, events.ModelFailed{
		Model: id, Code: string(oe.Code), Message: oe.Message, RolledBack: true, Restored: append([]string{}, restored...),
	})
	o.log.Error("model load failed", "op_id", op.opID, "model", id, "engine", op.model.Engine, "phase", "rollback",
		"code", string(oe.Code), "restored", restored, "duration_ms", ms(o.clock.Now().Sub(op.started)))
	oe.Details["op_id"] = op.opID
	return res, oe
}

// restoreAll starts the models of list again, most recently used first, each on its engine. Each
// one gets one try; a failure is logged and the next one is tried. It returns those that came back.
func (o *Orchestrator) restoreAll(ctx context.Context, in Inputs, opID string, list []string, engineOf map[string]string) []string {
	order := append([]string(nil), list...)
	sortMRU(order, o.opt.State.Snapshot().Models.LastUsed)
	var restored []string
	for _, e := range order {
		t := o.clock.Now()
		if f := o.startAny(ctx, in, engineOf[e], e); f != nil {
			if f.kind == kindInterrupted {
				break
			}
			o.log.Warn("could not restore a model", "op_id", opID, "model", e, "phase", "rollback", "cause", f.kind)
			continue
		}
		restored = append(restored, e)
		o.publish(events.TypeModelLoaded, opID, events.ModelLoaded{Model: e, LoadMS: ms(o.clock.Now().Sub(t))})
	}
	return restored
}

// ---- an unload on another engine -------------------------------------------------------------

// unloadEngine is UnloadWith for a model of another engine: take modelOpsMu, stop the model, wait
// for the engine to free its memory. A model the engine does not hold is already unloaded. An
// engine that does not free its memory answers 500 LAI-LLM-042 (the alert stays until a later load
// succeeds).
func (o *Orchestrator) unloadEngine(ctx context.Context, req UnloadRequest, a EngineAdapter) (UnloadResult, error) {
	hold, err := o.lock.AcquireWait(ctx, HolderUnload, req.LockWait)
	if err != nil {
		var busy *BusyError
		if errors.As(err, &busy) {
			err = errBusy(busy)
		}
		o.auditWrite(ctx, audit.ActionModelUnload, req.Actor, req.ID, "", err)
		return UnloadResult{}, err
	}
	defer hold.Release()
	ctx = context.WithoutCancel(ctx)
	res := UnloadResult{ID: req.ID, OpID: hold.OpID, Restored: []string{}}
	ems, rerr := a.Resident(ctx)
	if rerr == nil && !holds(ems, req.ID) {
		res.AlreadyUnloaded = true
		o.auditWrite(ctx, audit.ActionModelUnload, req.Actor, req.ID, hold.OpID, nil)
		return res, nil
	}
	t := o.clock.Now()
	if err := a.Stop(ctx, req.ID); err != nil {
		o.raise(CodeEngineNotFreed, a.Engine(), map[string]any{"model": req.ID})
		oe := opErr(CodeEngineNotFreed, http.StatusInternalServerError,
			"The model was asked to stop but its program did not stop cleanly or free the graphics memory.",
			map[string]any{"model": req.ID, "op_id": hold.OpID}, err)
		o.log.Warn("an engine did not stop cleanly", "op_id", hold.OpID, "model", req.ID, "engine", a.Engine(), "code", string(oe.Code), "error", err)
		o.auditWrite(ctx, audit.ActionModelUnload, req.Actor, req.ID, hold.OpID, oe)
		return res, oe
	}
	o.publish(events.TypeModelUnloaded, hold.OpID, events.ModelUnloaded{Model: req.ID, Reason: events.UnloadRequest})
	o.log.Info("model unloaded", "op_id", hold.OpID, "model", req.ID, "engine", a.Engine(), "duration_ms", ms(o.clock.Now().Sub(t)))
	o.auditWrite(ctx, audit.ActionModelUnload, req.Actor, req.ID, hold.OpID, nil)
	return res, nil
}

func holds(ems []EngineModel, id string) bool {
	for _, em := range ems {
		if em.ID == id {
			return true
		}
	}
	return false
}

// ---- survival --------------------------------------------------------------------------------

// StopEnginesForSurvival stops every non-llama engine (music) before the survival models load
// (modes.md section 2.2, step survival_models): their graphics memory belongs to the survival models.
// It waits up to DefaultLockWait for modelOpsMu. Nothing here can fail survival: an engine that does
// not stop is logged as LAI-LLM-042 and returned as a warning, and the survival models load anyway.
func (o *Orchestrator) StopEnginesForSurvival(ctx context.Context) []events.Warning {
	if len(o.opt.Engines) == 0 {
		return nil
	}
	hold, err := o.lock.AcquireWait(ctx, HolderUnload, DefaultLockWait)
	if err == nil {
		defer hold.Release()
	} else {
		// Busy for a minute: stop the engines anyway. Survival must not wait on a music load.
		o.log.Warn("stopping the engines for survival without modelOpsMu", "error", err)
	}
	var warns []events.Warning
	for _, a := range o.opt.Engines {
		ems, _ := a.Resident(ctx)
		if serr := a.Stop(ctx, ""); serr != nil {
			o.log.Warn("an engine did not stop for survival mode", "engine", a.Engine(), "code", string(CodeEngineNotFreed), "error", serr)
			o.raise(CodeEngineNotFreed, a.Engine(), map[string]any{"engine": a.Engine()})
			warns = append(warns, events.Warning{Code: string(CodeEngineNotFreed),
				Message: "The " + a.Engine() + " engine did not stop cleanly; the survival models load anyway."})
			continue
		}
		for _, em := range ems {
			o.publish(events.TypeModelUnloaded, "", events.ModelUnloaded{Model: em.ID, Reason: events.UnloadRequest})
		}
		o.log.Info("engine stopped for survival mode", "engine", a.Engine())
	}
	return warns
}
