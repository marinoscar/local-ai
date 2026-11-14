package router

import (
	"context"
	"errors"
	"net/http"

	"github.com/marinoscar/local-ai/agent/internal/events"
	"github.com/marinoscar/local-ai/agent/internal/state"
)

// Llama loads with other engines in the way (E11).

// stopEvicted stops one model the plan of a llama load evicts, on its engine.
func (op *loadOp) stopEvicted(ctx context.Context, id string) *loadFailure {
	if eng := op.engineOf[id]; eng != "" {
		return op.o.stopAny(ctx, op.in, eng, id)
	}
	return op.o.stopOne(ctx, id, op.o.opt.Timing.EvictTimeout)
}

// planEnginesSingle is the cross-engine part of a load in single mode. The running llama model stops
// with the restart, so only the other engines can be in the way: when the new model does not fit
// next to what they hold, they are evicted first (unload_others), else the load is refused
// (LAI-LLM-011).
func (o *Orchestrator) planEnginesSingle(ctx context.Context, op *loadOp, present bool) error {
	st := o.opt.State.Snapshot()
	eng, engMiB := o.engineResidents(ctx, op.in, st)
	if len(eng) == 0 {
		return nil
	}
	m := op.model
	plan, _, perr := op.in.Fit.withoutEngines(engMiB).Plan(Candidate{ID: m.ID, Requirements: m.Requirements, Present: present}, eng, op.req.UnloadOthers)
	if perr != nil {
		var pe *PlanError
		if errors.As(perr, &pe) {
			return errNoRoom(m.ID, pe.Reason)
		}
		return opErr(CodeNoRoom, http.StatusConflict, perr.Error(), map[string]any{"model": m.ID}, perr)
	}
	op.engineOf = map[string]string{}
	for _, l := range eng {
		op.engineOf[l.ID] = l.Engine
	}
	op.evict = plan
	return nil
}

// runSingleEngines runs a load in single mode: the models of other engines in the way stop first;
// when the load then fails, they are started again after the single-mode rollback.
func (op *loadOp) runSingleEngines(ctx context.Context) (LoadResult, error) {
	o := op.o
	var evicted []string
	for _, e := range op.evict {
		if f := op.stopEvicted(ctx, e); f != nil {
			o.log.Warn("could not evict a model", "op_id", op.opID, "model", e, "phase", "evict", "cause", f.kind)
			o.restoreAll(ctx, op.in, op.opID, evicted, op.engineOf)
			if f.kind == kindInterrupted {
				o.setLastLoad(op.model.ID, op.opID, state.LoadInterrupted, 0)
				return LoadResult{ID: op.model.ID, OpID: op.opID, Unloaded: []string{}}, ErrInterrupted
			}
			o.setLastLoad(op.model.ID, op.opID, state.LoadFailed, 0)
			oe := errEngineFailed(op.model.ID, f, nil)
			oe.Details["op_id"] = op.opID
			return LoadResult{ID: op.model.ID, OpID: op.opID, Unloaded: []string{}}, oe
		}
		evicted = append(evicted, e)
		o.publish(events.TypeModelUnloaded, op.opID, events.ModelUnloaded{Model: e, Reason: events.UnloadEvicted})
	}
	res, err := op.runSingle(ctx)
	if err != nil && len(evicted) > 0 && !errors.Is(err, ErrInterrupted) {
		o.restoreAll(ctx, op.in, op.opID, evicted, op.engineOf)
	}
	if err == nil {
		res.Unloaded = append(res.Unloaded, evicted...)
	}
	return res, err
}
