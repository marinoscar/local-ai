package router

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/audit"
	"github.com/marinoscar/local-ai/agent/internal/events"
	"github.com/marinoscar/local-ai/agent/internal/state"
)

// UnloadRequest is one call of UnloadWith.
type UnloadRequest struct {
	ID string
	// Force unloads the last loaded model in survival mode (agent.md section 3.8.9).
	Force bool
	Actor audit.Actor
	// LockWait is how long to wait for modelOpsMu; 0 means do not wait.
	LockWait time.Duration
}

// UnloadResult is the body of the 200 of POST /models/{id}/unload (agent.md section 3.8.9).
type UnloadResult struct {
	ID              string
	AlreadyUnloaded bool
	OpID            string
	// Restarted is true when the model would not stop and llama was restarted (LAI-LLM-031);
	// Restored are the models loaded again afterwards.
	Restarted bool
	Restored  []string
}

// Unload unloads a model: llama-router.md section 4.5. It is UnloadWith with no lock wait.
func (o *Orchestrator) Unload(ctx context.Context, id string, force bool, actor audit.Actor) (UnloadResult, error) {
	return o.UnloadWith(ctx, UnloadRequest{ID: id, Force: force, Actor: actor})
}

// UnloadWith unloads a model. It takes modelOpsMu like a load, asks the router to stop the model
// and polls until it is gone. The default model may be unloaded (state.models.default is
// unchanged). In survival mode unloading the last loaded model is refused (409 LAI-AGT-023) unless
// Force is set. A router that still reports the model running after DefaultUnloadHang is hung: llama
// is restarted, the other models of desired_loaded are loaded again and LAI-LLM-031 is raised; the
// unload then counts as done. As for a load, the work after the lock is taken cannot be cancelled by
// the caller, only by Shutdown.
func (o *Orchestrator) UnloadWith(ctx context.Context, req UnloadRequest) (UnloadResult, error) {
	if o.isClosing() {
		return UnloadResult{}, ErrInterrupted
	}
	in := o.opt.Inputs()
	m, inCatalog := findModel(in.Catalog, req.ID)
	if inCatalog && m.Engine != "llama" {
		err := errNotLlama(req.ID, m.Engine)
		o.auditWrite(ctx, audit.ActionModelUnload, req.Actor, req.ID, "", err)
		return UnloadResult{}, err
	}
	fail := func(err error) (UnloadResult, error) {
		o.auditWrite(ctx, audit.ActionModelUnload, req.Actor, req.ID, "", err)
		return UnloadResult{}, err
	}
	if err := o.opt.Router.Health(ctx); err != nil {
		return fail(errRouter(err))
	}
	hold, err := o.lock.AcquireWait(ctx, HolderUnload, req.LockWait)
	if err != nil {
		var busy *BusyError
		if errors.As(err, &busy) {
			return fail(errBusy(busy))
		}
		return UnloadResult{}, err
	}
	unsup := o.suppress(hold.OpID)
	opctx := context.WithoutCancel(ctx)
	done := make(chan struct{})
	var res UnloadResult
	var rerr error
	o.wg.Add(1)
	go func() {
		defer o.wg.Done()
		defer close(done)
		res, rerr = o.runUnload(opctx, in, req, hold.OpID, inCatalog)
		unsup()
		hold.Release()
		o.auditWrite(ctx, audit.ActionModelUnload, req.Actor, req.ID, hold.OpID, rerr)
	}()
	select {
	case <-done:
		return res, rerr
	case <-ctx.Done():
		return UnloadResult{OpID: hold.OpID}, ctx.Err()
	}
}

func (o *Orchestrator) runUnload(ctx context.Context, in Inputs, req UnloadRequest, opID string, inCatalog bool) (UnloadResult, error) {
	id := req.ID
	res := UnloadResult{ID: id, OpID: opID, Restored: []string{}}
	statuses, err := o.opt.Router.Models(ctx)
	if err != nil {
		return res, errRouter(err)
	}
	cur, listed := statusByID(statuses)[id]
	if !listed && !inCatalog {
		return res, errNotFound(id)
	}
	if !listed || !cur.Status.Running() {
		res.AlreadyUnloaded = true
		return res, nil
	}
	if in.Survival && !req.Force {
		if ids := loadedIDs(statuses, nil); len(ids) == 1 && ids[0] == id {
			return res, errLastModel(id)
		}
	}

	o.log.Info("model unload", "op_id", opID, "model", id, "phase", "evict")
	t := o.clock.Now()
	if uerr := o.opt.Router.Unload(ctx, id); uerr != nil {
		if notRunning(uerr) {
			res.AlreadyUnloaded = true
			return res, nil
		}
		return res, errRouter(uerr)
	}
	switch o.waitUnloaded(ctx, id, o.opt.Timing.UnloadHang) {
	case unloadInterrupted:
		return res, ErrInterrupted
	case unloadTimedOut:
		o.log.Error("a model would not stop; restarting llama", "op_id", opID, "model", id, "code", string(CodeUnloadHung),
			"duration_ms", ms(o.clock.Now().Sub(t)))
		if err := o.restartAndRestore(ctx, in, id, opID, &res); err != nil {
			return res, err
		}
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
	o.log.Info("model unloaded", "op_id", opID, "model", id, "phase", "evict", "duration_ms", ms(o.clock.Now().Sub(t)))
	return res, nil
}

// restartAndRestore is the answer to an unload that hangs (llama-router.md section 4.5): restart
// llama, wait for the router, load again the other models of desired_loaded while models_max
// allows, and when nothing is loaded after that the default (and in survival mode the survival
// list). It raises LAI-LLM-031.
func (o *Orchestrator) restartAndRestore(ctx context.Context, in Inputs, id, opID string, res *UnloadResult) error {
	if o.opt.Restarter == nil {
		return opErr(CodeUnloadHung, http.StatusGatewayTimeout, "The model did not stop in time.", map[string]any{"model": id, "op_id": opID}, nil)
	}
	if err := o.opt.Restarter.RestartLlama(ctx); err != nil {
		return opErr(CodeRouterDown, http.StatusBadGateway, "The model did not stop and the AI model service could not be restarted.",
			map[string]any{"model": id, "op_id": opID, "cause": "restart_failed"}, err)
	}
	res.Restarted = true
	o.raise(CodeUnloadHung, "", map[string]any{"model": id})
	if !o.waitHealth(ctx, o.opt.Timing.RestartWait) {
		if o.isClosing() {
			return ErrInterrupted
		}
		return opErr(CodeRouterDown, http.StatusBadGateway, "The AI model service did not answer after it was restarted.",
			map[string]any{"model": id, "op_id": opID, "cause": "restart_health"}, nil)
	}

	st := o.opt.State.Snapshot()
	tried := map[string]bool{id: true}
	max := in.modelsMax()
	for _, x := range st.Models.DesiredLoaded {
		if x == id || tried[x] {
			continue
		}
		tried[x] = true
		if o.residentCount(ctx) >= max {
			break
		}
		if f := o.loadOne(ctx, x, o.loadTimeout(in, x), nil); f != nil {
			if f.kind == kindInterrupted {
				return ErrInterrupted
			}
			o.log.Warn("could not restore a model after the restart", "op_id", opID, "model", x, "phase", "rollback", "cause", f.kind)
			continue
		}
		res.Restored = append(res.Restored, x)
	}
	if o.noneLoaded(ctx) {
		for _, c := range o.fallbackOrder(ctx, in, st, tried) {
			if f := o.loadOne(ctx, c, o.loadTimeout(in, c), nil); f == nil {
				res.Restored = append(res.Restored, c)
				break
			} else if f.kind == kindInterrupted {
				return ErrInterrupted
			}
		}
	}
	if o.noneLoaded(ctx) {
		o.raise(CodeNothingLoaded, "", map[string]any{"model": id, "cause": "unload_hung"})
		return opErr(CodeNothingLoaded, http.StatusInternalServerError, "No model could be started after the AI model service was restarted.",
			map[string]any{"model": id, "op_id": opID}, nil)
	}
	return nil
}

// modelsMax is models_max of the profile, 1 when there is no profile (the planner's floor).
func (in Inputs) modelsMax() int { return in.Fit.modelsMax() }

// residentCount counts the loaded or loading models; an unreadable router counts as full so that
// nothing is loaded blindly.
func (o *Orchestrator) residentCount(ctx context.Context) int {
	statuses, err := o.opt.Router.Models(ctx)
	if err != nil {
		return 1 << 20
	}
	n := 0
	for _, s := range statuses {
		if s.Status.Running() {
			n++
		}
	}
	return n
}

// waitHealth polls GET /health until it answers, at most timeout.
func (o *Orchestrator) waitHealth(ctx context.Context, timeout time.Duration) bool {
	start := o.clock.Now()
	for {
		if o.opt.Router.Health(ctx) == nil {
			return true
		}
		if o.clock.Now().Sub(start) >= timeout || !o.pause(ctx, o.opt.Timing.LoadPoll) {
			return false
		}
	}
}
