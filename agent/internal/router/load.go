package router

import (
	"context"
	"errors"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/audit"
	"github.com/marinoscar/local-ai/agent/internal/catalog"
	"github.com/marinoscar/local-ai/agent/internal/events"
	"github.com/marinoscar/local-ai/agent/internal/state"
)

// LoadRequest is one call of LoadWith.
type LoadRequest struct {
	// ID is the catalog model id.
	ID string
	// Wait false answers as soon as the operation is planned and runs the rest in the background
	// (the API's 202); the outcome is told by the events, state.models.last_load and OnDone.
	Wait bool
	// UnloadOthers lets the planner evict models to make room (the API default is true). Without
	// it, a load that does not fit is refused with LAI-LLM-011.
	UnloadOthers bool
	// Actor is who asked; it goes to the audit line.
	Actor audit.Actor
	// LockWait is how long to wait for modelOpsMu. 0 means do not wait: busy answers 409. Cloud
	// jobs and survival_models pass DefaultLockWait.
	LockWait time.Duration
	// OnDone, when set, receives the outcome of a background operation (Wait false). It runs on
	// the operation's goroutine after the lock is released.
	OnDone func(LoadResult, error)
}

// LoadResult is the outcome of a load: the body of 200 and 202 of POST /models/{id}/load
// (agent.md section 3.8.8).
type LoadResult struct {
	ID string
	// Status is "loaded", or "loading" for the 202 of a request with Wait false.
	Status string
	// AlreadyLoaded: nothing was done, the model was loaded already.
	AlreadyLoaded bool
	// LoadMS is how long the router took to load the model (not counting evictions).
	LoadMS int64
	// Unloaded are the models evicted to make room, in the order they were stopped.
	Unloaded []string
	// OpID names the operation in events, audit lines and logs.
	OpID string
	// Accepted is true for the 202 answer.
	Accepted bool
	// Warning is LAI-HW-005 or LAI-HW-006 when the load went ahead although other programs hold
	// the memory (llama-router.md section 4.3.3); nil otherwise.
	Warning *Reason
}

// Load loads a model: llama-router.md section 4.4. It is LoadWith with no lock wait. Errors are
// *OpError (see its documentation) except a cancelled ctx and ErrInterrupted.
func (o *Orchestrator) Load(ctx context.Context, id string, wait, unloadOthers bool, actor audit.Actor) (LoadResult, error) {
	return o.LoadWith(ctx, LoadRequest{ID: id, Wait: wait, UnloadOthers: unloadOthers, Actor: actor})
}

// loadOp is one load past the check and lock phases. It owns the lock and the watcher suppression
// until run ends.
type loadOp struct {
	o       *Orchestrator
	req     LoadRequest
	in      Inputs
	model   *catalog.Model
	opID    string
	hold    Hold
	unsup   func()
	timeout time.Duration
	// evict lists the models to stop, least recently used first; waitOnly means the model is
	// already loading (started by someone else) and only has to be waited for.
	evict    []string
	waitOnly bool
	warn     *Reason
	started  time.Time
}

func (op *loadOp) release() {
	op.unsup()
	op.hold.Release()
}

// LoadWith is Load with the options a handler or the mode engine needs. The sequence is the
// state machine of llama-router.md section 4.4:
//
//  1. check, without the lock: the model exists and is a llama model, it fits the node and has
//     verified files (404 LAI-AGT-021, 422 LAI-CAT-005, 422 LAI-LLM-008), and the router answers
//     (502 LAI-LLM-004);
//  2. lock: modelOpsMu (409 LAI-AGT-022 with the holder's op_id), watcher suppression;
//  3. snapshot: already loaded answers at once; loading by someone else is waited for;
//  4. plan with Env.Plan (409 LAI-LLM-011), then state.models.last_load = in_progress, fsynced;
//  5. evict, 6. load and wait, 7. bookkeeping, or 8. cleanup and rollback.
//
// Steps 5 to 8 run on a context that the caller cannot cancel: stopping halfway would leave the node
// worse off than finishing. A caller that gives up gets its own context error back and the
// operation completes on its own. Only Shutdown stops it (no rollback; last_load: interrupted).
func (o *Orchestrator) LoadWith(ctx context.Context, req LoadRequest) (LoadResult, error) {
	if o.isClosing() {
		return LoadResult{}, ErrInterrupted
	}
	op, early, err := o.prepareLoad(ctx, req)
	if err != nil || early != nil {
		var res LoadResult
		if early != nil {
			res = *early
		}
		o.auditWrite(audit.ActionModelLoad, req.Actor, req.ID, res.OpID, err)
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
		op.release()
		o.auditWrite(audit.ActionModelLoad, req.Actor, req.ID, op.opID, rerr)
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

// prepareLoad is steps 1 to 4. It returns an operation ready to run (the lock is held), or an
// early answer (already loaded), or an error; in the last two cases nothing is held.
func (o *Orchestrator) prepareLoad(ctx context.Context, req LoadRequest) (*loadOp, *LoadResult, error) {
	in := o.opt.Inputs()
	m, ok := findModel(in.Catalog, req.ID)
	if !ok {
		return nil, nil, errNotFound(req.ID)
	}
	if m.Engine != "llama" {
		return nil, nil, errNotLlama(req.ID, m.Engine)
	}
	_, present := o.locate(in, m)
	if fits, why := in.Fit.StaticFit(Candidate{ID: m.ID, Requirements: m.Requirements, Present: present}); !fits {
		return nil, nil, errNotLoadable(req.ID, why)
	}
	if err := o.opt.Router.Health(ctx); err != nil {
		return nil, nil, errRouter(err)
	}

	hold, err := o.lock.AcquireWait(ctx, HolderLoad, req.LockWait)
	if err != nil {
		var busy *BusyError
		if errors.As(err, &busy) {
			return nil, nil, errBusy(busy)
		}
		return nil, nil, err
	}
	op := &loadOp{o: o, req: req, in: in, model: m, opID: hold.OpID, hold: hold, unsup: o.suppress(hold.OpID), started: o.clock.Now()}
	keep := false
	defer func() {
		if !keep {
			op.release()
		}
	}()

	statuses, err := o.opt.Router.Models(ctx)
	if err != nil {
		return nil, nil, errRouter(err)
	}
	st := o.opt.State.Snapshot()
	cur, listed := statusByID(statuses)[m.ID]
	switch {
	case !listed:
		return nil, nil, errUnknownPreset(m.ID)
	case cur.Status.Resident():
		o.touch(m.ID)
		o.log.Info("model already loaded", "op_id", op.opID, "model", m.ID, "phase", "check")
		return nil, &LoadResult{ID: m.ID, Status: "loaded", AlreadyLoaded: true, Unloaded: []string{}, OpID: op.opID}, nil
	case cur.Status == StatusLoading:
		op.waitOnly = true
	default:
		plan, warn, perr := in.Fit.Plan(Candidate{ID: m.ID, Requirements: m.Requirements, Present: present},
			loadedFrom(in, st, statuses, m.ID), req.UnloadOthers)
		if perr != nil {
			var pe *PlanError
			if errors.As(perr, &pe) {
				return nil, nil, errNoRoom(m.ID, pe.Reason)
			}
			return nil, nil, opErr(CodeNoRoom, 409, perr.Error(), map[string]any{"model": m.ID}, perr)
		}
		op.evict, op.warn = plan, warn
	}
	op.timeout = o.loadTimeout(in, m.ID)
	if op.warn != nil {
		o.log.Warn("loading although other programs hold memory", "op_id", op.opID, "model", m.ID, "phase", "plan", "code", string(op.warn.Code))
	}
	o.log.Info("model load planned", "op_id", op.opID, "model", m.ID, "phase", "plan", "evict", op.evict, "wait_only", op.waitOnly)
	o.setLastLoad(m.ID, op.opID, state.LoadInProgress, 0)
	keep = true
	return op, nil, nil
}

// touch records that a model was just used (state.models.last_used), which orders evictions.
func (o *Orchestrator) touch(id string) {
	now := o.clock.Now().UTC().Truncate(time.Second)
	o.persist("last_used", func(st *state.State) error {
		if st.Models.LastUsed == nil {
			st.Models.LastUsed = map[string]time.Time{}
		}
		st.Models.LastUsed[id] = now
		return nil
	})
}

// run is steps 5 to 8.
func (op *loadOp) run(ctx context.Context) (LoadResult, error) {
	o, id := op.o, op.model.ID
	res := LoadResult{ID: id, Status: "loaded", OpID: op.opID, Unloaded: []string{}, Warning: op.warn}

	o.publish(events.TypeModelLoading, op.opID, events.ModelLoading{Model: id, Unloading: nz(op.evict), OpID: op.opID})
	var evicted []string
	for _, e := range op.evict {
		t := o.clock.Now()
		if f := o.stopOne(ctx, e, o.opt.Timing.EvictTimeout); f != nil {
			o.log.Warn("could not evict a model", "op_id", op.opID, "model", e, "phase", "evict", "cause", f.kind, "duration_ms", ms(o.clock.Now().Sub(t)))
			return op.fail(ctx, f, evicted)
		}
		evicted = append(evicted, e)
		o.publish(events.TypeModelUnloaded, op.opID, events.ModelUnloaded{Model: e, Reason: events.UnloadEvicted})
		o.log.Info("model evicted", "op_id", op.opID, "model", e, "phase", "evict", "duration_ms", ms(o.clock.Now().Sub(t)))
	}
	res.Unloaded = append([]string{}, evicted...)

	loadStart := o.clock.Now()
	fwd := &progressFwd{opID: op.opID, id: id, unloading: op.evict}
	var f *loadFailure
	if op.waitOnly {
		f = o.waitLoaded(ctx, id, op.timeout, fwd)
	} else {
		f = o.loadOne(ctx, id, op.timeout, fwd)
	}
	if f != nil {
		o.log.Warn("model load failed", "op_id", op.opID, "model", id, "phase", "load", "cause", f.kind, "duration_ms", ms(o.clock.Now().Sub(loadStart)))
		return op.fail(ctx, f, evicted)
	}
	res.LoadMS = ms(o.clock.Now().Sub(loadStart))
	op.succeed(ctx, res.LoadMS)
	return res, nil
}

// succeed is step 7.
func (op *loadOp) succeed(ctx context.Context, loadMS int64) {
	o, id := op.o, op.model.ID
	now := o.clock.Now().UTC().Truncate(time.Second)
	statuses, err := o.opt.Router.Models(ctx)
	o.persist("load result", func(st *state.State) error {
		if st.Models.LastUsed == nil {
			st.Models.LastUsed = map[string]time.Time{}
		}
		st.Models.LastUsed[id] = now
		if err == nil {
			st.Models.DesiredLoaded = loadedIDs(statuses, st.Models.LastUsed)
		}
		st.Models.LastLoad = &state.LastLoad{ID: id, OpID: op.opID, Result: state.LoadOK, LoadMS: loadMS, At: now}
		return nil
	})
	o.countLoad(id, true, loadMS)
	o.clear(CodeNothingLoaded, "")
	o.publish(events.TypeModelLoaded, op.opID, events.ModelLoaded{Model: id, LoadMS: loadMS})
	o.log.Info("model loaded", "op_id", op.opID, "model", id, "phase", "load", "duration_ms", loadMS)
}

// fail is step 8: cleanup, rollback, and the final answer. An interrupted operation is left to the
// router instead.
func (op *loadOp) fail(ctx context.Context, f *loadFailure, evicted []string) (LoadResult, error) {
	o, id := op.o, op.model.ID
	res := LoadResult{ID: id, OpID: op.opID, Unloaded: []string{}}
	if f.kind == kindInterrupted {
		o.setLastLoad(id, op.opID, state.LoadInterrupted, 0)
		return res, ErrInterrupted
	}
	rb := o.rollback(ctx, op, evicted)
	if rb.interrupted {
		o.setLastLoad(id, op.opID, state.LoadInterrupted, 0)
		return res, ErrInterrupted
	}
	nothing := len(rb.loaded) == 0
	result := state.LoadFailed
	if f.timeout() {
		result = state.LoadTimeout
	}
	now := o.clock.Now().UTC().Truncate(time.Second)
	o.persist("load result", func(st *state.State) error {
		if !nothing {
			// What is loaded now is what a restart should bring back; with nothing loaded the
			// previous set stays, so the boot reconcile retries it.
			st.Models.DesiredLoaded = rb.loaded
		}
		st.Models.LastLoad = &state.LastLoad{ID: id, OpID: op.opID, Result: result, At: now}
		return nil
	})
	oe := errLoadFailed(id, f, !nothing, rb.restored, nothing)
	o.countLoad(id, false, 0)
	o.publish(events.TypeModelFailed, op.opID, events.ModelFailed{
		Model: id, Code: string(oe.Code), Message: oe.Message, RolledBack: !nothing, Restored: append([]string{}, rb.restored...),
	})
	if nothing {
		o.raise(CodeNothingLoaded, "", map[string]any{"model": id, "cause": f.kind})
	}
	o.log.Error("model load failed", "op_id", op.opID, "model", id, "phase", "rollback", "code", string(oe.Code),
		"restored", rb.restored, "duration_ms", ms(o.clock.Now().Sub(op.started)))
	oe.Details["op_id"] = op.opID
	return res, oe
}

// rollbackResult is what a rollback left on the node.
type rollbackResult struct {
	restored    []string
	loaded      []string
	interrupted bool
}

// rollback is llama-router.md section 4.4 step 8: stop the model that failed, load again every
// model that was evicted (most recently used first), and when no model is loaded then, the effective
// default and, in survival mode, the survival list. A load of the rollback that fails is not rolled
// back again: each candidate gets one try.
func (o *Orchestrator) rollback(ctx context.Context, op *loadOp, evicted []string) rollbackResult {
	var rb rollbackResult
	id := op.model.ID
	o.countRollback()
	t0 := o.clock.Now()

	// 1. Cleanup: the model that failed or timed out must not keep holding memory.
	if statuses, err := o.opt.Router.Models(ctx); err == nil {
		if s, ok := statusByID(statuses)[id]; ok && s.Status.Running() {
			if f := o.stopOne(ctx, id, o.opt.Timing.EvictTimeout); f != nil {
				if f.kind == kindInterrupted {
					rb.interrupted = true
					return rb
				}
				o.log.Warn("could not stop the model that failed", "op_id", op.opID, "model", id, "phase", "rollback", "cause", f.kind)
			} else {
				o.publish(events.TypeModelUnloaded, op.opID, events.ModelUnloaded{Model: id, Reason: events.UnloadRollback})
			}
		}
	}

	// 2. Restore what was evicted, most recently used first.
	st := o.opt.State.Snapshot()
	order := append([]string(nil), evicted...)
	sortMRU(order, st.Models.LastUsed)
	tried := map[string]bool{id: true}
	for _, e := range order {
		tried[e] = true
		o.restoreOne(ctx, op, e, &rb)
		if rb.interrupted {
			return rb
		}
	}

	// 3. Nothing loaded: the default, then in survival mode the survival list.
	if o.noneLoaded(ctx) {
		for _, c := range o.fallbackOrder(ctx, op.in, st, tried) {
			tried[c] = true
			o.restoreOne(ctx, op, c, &rb)
			if rb.interrupted || !o.noneLoaded(ctx) {
				break
			}
		}
	}

	if statuses, err := o.opt.Router.Models(ctx); err == nil {
		rb.loaded = loadedIDs(statuses, o.opt.State.Snapshot().Models.LastUsed)
	}
	o.log.Info("rollback done", "op_id", op.opID, "model", id, "phase", "rollback", "restored", rb.restored, "duration_ms", ms(o.clock.Now().Sub(t0)))
	return rb
}

// restoreOne loads a model again as part of a rollback and records it in rb. A model that does not
// come back is stopped, so it does not hold memory the next candidate needs; rb.interrupted is set
// when the agent shut down.
func (o *Orchestrator) restoreOne(ctx context.Context, op *loadOp, id string, rb *rollbackResult) {
	t := o.clock.Now()
	f := o.loadOne(ctx, id, o.loadTimeout(op.in, id), nil)
	switch {
	case f == nil:
		rb.restored = append(rb.restored, id)
		o.publish(events.TypeModelLoaded, op.opID, events.ModelLoaded{Model: id, LoadMS: ms(o.clock.Now().Sub(t))})
		o.log.Info("model restored", "op_id", op.opID, "model", id, "phase", "rollback", "duration_ms", ms(o.clock.Now().Sub(t)))
	case f.kind == kindInterrupted:
		rb.interrupted = true
	default:
		o.log.Warn("could not restore a model", "op_id", op.opID, "model", id, "phase", "rollback", "cause", f.kind)
		if statuses, err := o.opt.Router.Models(ctx); err == nil {
			if s, ok := statusByID(statuses)[id]; ok && s.Status.Running() {
				_ = o.stopOne(ctx, id, o.opt.Timing.EvictTimeout)
			}
		}
	}
}

// noneLoaded reports whether no model is loaded. A router that does not answer counts as none.
func (o *Orchestrator) noneLoaded(ctx context.Context) bool {
	statuses, err := o.opt.Router.Models(ctx)
	if err != nil {
		return true
	}
	for _, s := range statuses {
		if s.Status.Resident() {
			return false
		}
	}
	return true
}

// fallbackOrder lists the models to try when nothing is loaded after a rollback: the effective
// default, then in survival mode the profile's survival list. Models already tried and models the
// router does not list are left out.
func (o *Orchestrator) fallbackOrder(ctx context.Context, in Inputs, st state.State, tried map[string]bool) []string {
	statuses, err := o.opt.Router.Models(ctx)
	if err != nil {
		return nil
	}
	ids := presetIDs(statuses)
	stateDefault := ""
	if st.Models.Default != nil {
		stateDefault = *st.Models.Default
	}
	var out []string
	add := func(c string) {
		if c != "" && !tried[c] && contains1(ids, c) && !contains1(out, c) {
			out = append(out, c)
		}
	}
	add(EffectiveDefault(ids, stateDefault, in.Profile, in.Survival))
	if in.Survival && in.Profile != nil {
		for _, c := range in.Profile.Survival.Models {
			add(c)
		}
	}
	return out
}

func ms(d time.Duration) int64 { return d.Milliseconds() }
