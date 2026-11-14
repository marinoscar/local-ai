package router

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/audit"
	"github.com/marinoscar/local-ai/agent/internal/catalog"
	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/events"
	"github.com/marinoscar/local-ai/agent/internal/state"
)

// Workloads (E11, issue #509; llama-router.md section 4.16, agent.md section 3.8.34): a named set of
// models the node switches to with one action. Activation diffs what is loaded against the set,
// unloads what is not in it (never a survival model in survival mode), loads what is missing, each
// through its engine, under modelOpsMu, and restores the previous set when anything fails.

// Codes of the workloads (llama-router.md section 8).
const (
	CodeWorkloadUnknown  Code = doctor.LLM050
	CodeWorkloadNoFit    Code = doctor.LLM051
	CodeWorkloadFailed   Code = doctor.LLM052
	CodeWorkloadSurvival Code = doctor.LLM053
	// CodeWorkloadModel is LAI-CAT-020: a workload names a model the catalog does not have.
	CodeWorkloadModel Code = doctor.CAT020
)

// HolderWorkload is the kind of a modelOpsMu holder that activates a workload.
const HolderWorkload = "workload"

// Workload is one resolved workload.
type Workload struct {
	Name   string
	Label  catalog.WorkloadLabel
	Models []string
}

// DroppedWorkload is a workload left out because it names an unknown model (LAI-CAT-020).
type DroppedWorkload struct {
	Name    string
	Unknown []string
}

// ResolveWorkloads merges the profile's workloads with node.yaml's (same name: replaced as a whole;
// new name: added) and drops every workload that names a model the catalog does not have. The result
// is sorted by name: the YAML maps keep no order.
func ResolveWorkloads(cat *catalog.Catalog, profile *catalog.Profile, node map[string]config.Workload) ([]Workload, []DroppedWorkload) {
	nodeWs := make(map[string]catalog.Workload, len(node))
	for name, w := range node {
		nodeWs[name] = catalog.Workload{Label: catalog.WorkloadLabel{EN: w.Label.EN, ES: w.Label.ES}, Models: w.Models}
	}
	merged := catalog.EffectiveWorkloads(profile, nodeWs)
	var out []Workload
	var dropped []DroppedWorkload
	for _, name := range catalog.WorkloadNames(merged) {
		w := merged[name]
		var unknown []string
		for _, id := range w.Models {
			if _, ok := findModel(cat, id); !ok {
				unknown = append(unknown, id)
			}
		}
		if len(unknown) > 0 {
			dropped = append(dropped, DroppedWorkload{Name: name, Unknown: unknown})
			continue
		}
		out = append(out, Workload{Name: name, Label: w.Label, Models: dedupe(w.Models)})
	}
	return out, dropped
}

func dedupe(ids []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

func findWorkload(in Inputs, name string) (Workload, bool) {
	for _, w := range in.Workloads {
		if w.Name == name {
			return w, true
		}
	}
	return Workload{}, false
}

// ---- what is loaded --------------------------------------------------------------------------

// residentSet is every model loaded (or loading) on every engine, with its engine ("" for llama).
type residentSet struct {
	ids      []string // most recently used first
	engineOf map[string]string
}

func (r residentSet) has(id string) bool { _, ok := r.engineOf[id]; return ok }

func (o *Orchestrator) residents(ctx context.Context, in Inputs) (residentSet, error) {
	rs := residentSet{engineOf: map[string]string{}}
	llama, err := o.llama(in).Resident(ctx)
	if err != nil {
		return rs, err
	}
	for _, em := range llama {
		rs.engineOf[em.ID] = ""
		rs.ids = append(rs.ids, em.ID)
	}
	for _, a := range o.opt.Engines {
		ems, err := a.Resident(ctx)
		if err != nil {
			o.log.Warn("could not read what an engine holds", "engine", a.Engine(), "error", err)
			continue
		}
		for _, em := range ems {
			rs.engineOf[em.ID] = a.Engine()
			rs.ids = append(rs.ids, em.ID)
		}
	}
	sortMRU(rs.ids, o.opt.State.Snapshot().Models.LastUsed)
	return rs, nil
}

// survivalKept are the models a workload never unloads: the profile's survival list, in survival mode.
func survivalKept(in Inputs) map[string]bool {
	keep := map[string]bool{}
	if in.Survival && in.Profile != nil {
		for _, id := range in.Profile.Survival.Models {
			keep[id] = true
		}
	}
	return keep
}

// exactly reports whether the loaded set is exactly the workload (survival models kept beside it in
// survival mode do not count against it).
func exactly(rs residentSet, w Workload, kept map[string]bool) bool {
	in := map[string]bool{}
	for _, id := range w.Models {
		if !rs.has(id) {
			return false
		}
		in[id] = true
	}
	for _, id := range rs.ids {
		if !in[id] && !kept[id] {
			return false
		}
	}
	return true
}

// ---- the fit of a set ------------------------------------------------------------------------

// workloadFit checks that every model of the set may load here and that the set fits together, with
// an empty keep-set (plus the survival models in survival mode). missing lists the models with no
// verified copy; err is the refusal (LAI-LLM-051, 043 or 008).
func (o *Orchestrator) workloadFit(in Inputs, w Workload) (missing []string, err *OpError) {
	var members []Loaded
	llamaMax := in.modelsMax()
	if in.Llama.Mode == LlamaModeSingle {
		llamaMax = 1
	}
	counts := map[string]int{}
	var noFit []string
	for _, id := range w.Models {
		m, ok := findModel(in.Catalog, id)
		if !ok {
			return nil, opErr(CodeWorkloadModel, http.StatusUnprocessableEntity, "The workload names a model this catalog does not have.",
				map[string]any{"name": w.Name, "model": id}, nil)
		}
		if m.Engine != catalog.EngineLlama {
			a, ok := o.adapter(m.Engine)
			if !ok {
				return nil, errNotLlama(id, m.Engine)
			}
			if ierr := a.Installed(); ierr != nil {
				return nil, errEngineMissing(id, m.Engine, ierr)
			}
		}
		_, present := o.locate(in, m)
		if !present {
			missing = append(missing, id)
		}
		if ok, why := in.Fit.Loadable(m, present); !ok {
			if why != nil && why.Code == CodeFilesMissing {
				continue // reported in missing; the error below names them
			}
			noFit = append(noFit, id)
		}
		counts[engineOf(m.Engine)]++
		members = append(members, Loaded{ID: id, Engine: m.Engine, Requirements: m.Requirements})
	}
	if len(missing) > 0 {
		e := opErr(CodeFilesMissing, http.StatusUnprocessableEntity, "Some models of this workload are not on this computer yet.",
			map[string]any{"name": w.Name, "models": missing}, nil)
		return missing, e
	}
	if counts[catalog.EngineLlama] > llamaMax || counts[catalog.EngineAcestep] > 1 {
		noFit = append(noFit, w.Models...)
	}
	if len(noFit) == 0 && len(members) > 0 {
		keep := append([]Loaded(nil), members[:len(members)-1]...)
		for id := range survivalKept(in) {
			if !contains1(w.Models, id) {
				if m, ok := findModel(in.Catalog, id); ok {
					keep = append(keep, Loaded{ID: id, Engine: m.Engine, Requirements: m.Requirements})
				}
			}
		}
		last := members[len(members)-1]
		env := in.Fit
		env.ModelsMax = 1 << 10 // the count rule is checked above, per engine
		if s := env.DynamicFit(Candidate{ID: last.ID, Engine: last.Engine, Requirements: last.Requirements}, keep, nil); s.GPU || s.RAM {
			noFit = append(noFit, w.Models...)
		}
	}
	if len(noFit) > 0 {
		return nil, opErr(CodeWorkloadNoFit, http.StatusUnprocessableEntity, "This workload does not fit this computer's graphics memory.",
			map[string]any{"name": w.Name, "models": dedupe(noFit)}, nil)
	}
	return nil, nil
}

// ---- GET /workloads --------------------------------------------------------------------------

// WorkloadView is one workload of GET /workloads.
type WorkloadView struct {
	Name    string
	Label   catalog.WorkloadLabel
	Models  []string
	Active  bool
	Fits    bool
	Missing []string
}

// WorkloadsView is the body of GET /workloads.
type WorkloadsView struct {
	Workloads []WorkloadView
	// Active is the last activated workload while it is still exactly loaded, else "".
	Active string
}

// Workloads lists the node's workloads with what is loaded now. A router that does not answer makes
// every workload inactive; the list is still given.
func (o *Orchestrator) Workloads(ctx context.Context) WorkloadsView {
	in := o.opt.Inputs()
	rs, err := o.residents(ctx, in)
	kept := survivalKept(in)
	st := o.opt.State.Snapshot()
	var v WorkloadsView
	for _, w := range in.Workloads {
		missing, ferr := o.workloadFit(in, w)
		wv := WorkloadView{Name: w.Name, Label: w.Label, Models: append([]string{}, w.Models...), Fits: ferr == nil,
			Missing: append([]string{}, missing...), Active: err == nil && exactly(rs, w, kept)}
		if wv.Active && st.Workloads != nil && st.Workloads.Active == w.Name {
			v.Active = w.Name
		}
		v.Workloads = append(v.Workloads, wv)
	}
	return v
}

// ---- POST /workloads/{name}/activate ---------------------------------------------------------

// WorkloadRequest is one activation.
type WorkloadRequest struct {
	Name  string
	Force bool
	// Wait false answers as soon as the lock is held (202) and runs the rest in the background.
	Wait  bool
	Actor audit.Actor
	// LockWait is how long to wait for modelOpsMu (0: do not wait; the cloud job waits DefaultLockWait).
	LockWait time.Duration
	// OnDone receives the outcome of a background activation.
	OnDone func(WorkloadResult, error)
}

// WorkloadResult is the outcome of an activation.
type WorkloadResult struct {
	OpID     string
	Accepted bool
	Active   string
	Loaded   []string
	Unloaded []string
}

func errSingleWorkload(name string) *OpError {
	return opErr(CodeRefused, http.StatusConflict,
		"Workloads switch models in router mode; this computer runs the AI service in single-model mode.",
		map[string]any{"name": name, "mode": LlamaModeSingle}, nil)
}

// ActivateWorkload is llama-router.md section 4.16. Refusals: 404 LAI-LLM-050, 409 LAI-LLM-053
// (survival without force), 409 LAI-AGT-023 (music in survival, or single-model mode), 422
// LAI-LLM-051 / 043 / 008, 409 LAI-AGT-022 (busy). A failed activation is 500 LAI-LLM-052 with the
// previous set restored, or 500 LAI-LLM-012 when nothing could be restored.
func (o *Orchestrator) ActivateWorkload(ctx context.Context, req WorkloadRequest) (WorkloadResult, error) {
	if o.isClosing() {
		return WorkloadResult{}, ErrInterrupted
	}
	in := o.opt.Inputs()
	fail := func(opID string, err error) (WorkloadResult, error) {
		o.auditWorkload(ctx, req, opID, err)
		return WorkloadResult{OpID: opID}, err
	}
	w, ok := findWorkload(in, req.Name)
	if !ok {
		return fail("", opErr(CodeWorkloadUnknown, http.StatusNotFound, "There is no workload with this name.", map[string]any{"name": req.Name}, nil))
	}
	if in.Survival && !req.Force {
		return fail("", opErr(CodeWorkloadSurvival, http.StatusConflict,
			"This computer is in survival mode. Confirm the change to switch anyway; the survival models stay loaded.",
			map[string]any{"name": w.Name, "requires": "force"}, nil))
	}
	for _, id := range w.Models {
		if m, ok := findModel(in.Catalog, id); ok && m.Engine == catalog.EngineAcestep && in.Survival {
			return fail("", errEngineSurvival(id))
		}
	}
	if in.Llama.Mode == LlamaModeSingle {
		return fail("", errSingleWorkload(w.Name))
	}
	if _, ferr := o.workloadFit(in, w); ferr != nil {
		return fail("", ferr)
	}
	if err := o.opt.Router.Health(ctx); err != nil {
		return fail("", errRouter(err))
	}
	hold, err := o.lock.AcquireWait(ctx, HolderWorkload, req.LockWait)
	if err != nil {
		var busy *BusyError
		if errors.As(err, &busy) {
			return fail("", errBusy(busy))
		}
		return WorkloadResult{}, err
	}
	unsup := o.suppress(hold.OpID)
	opctx := context.WithoutCancel(ctx)
	done := make(chan struct{})
	var res WorkloadResult
	var rerr error
	o.wg.Add(1)
	go func() {
		defer o.wg.Done()
		defer close(done)
		res, rerr = o.runWorkload(opctx, in, w, hold.OpID)
		unsup()
		hold.Release()
		o.auditWorkload(opctx, req, hold.OpID, rerr)
		if !req.Wait && req.OnDone != nil {
			req.OnDone(res, rerr)
		}
	}()
	if !req.Wait {
		return WorkloadResult{OpID: hold.OpID, Accepted: true}, nil
	}
	select {
	case <-done:
		return res, rerr
	case <-ctx.Done():
		return WorkloadResult{OpID: hold.OpID}, ctx.Err()
	}
}

func (o *Orchestrator) auditWorkload(ctx context.Context, req WorkloadRequest, opID string, err error) {
	if o.opt.Audit == nil {
		return
	}
	force := "false"
	if req.Force {
		force = "true"
	}
	ev := audit.Event{Action: audit.ActionWorkloadActivate, Result: audit.ResultOK, Actor: req.Actor,
		Target: map[string]string{"name": req.Name, "force": force}, OpID: opID, ReqID: reqIDFrom(ctx)}
	if err != nil {
		ev.Result = audit.ResultFail
		var oe *OpError
		if errors.As(err, &oe) {
			ev.Code = string(oe.Code)
		}
	}
	if werr := o.opt.Audit.Write(ev); werr != nil {
		o.log.Error("could not write the audit line", "action", ev.Action, "error", werr)
	}
}

// failureCode is the doctor code of the first failure of an activation (details.cause).
func workloadCause(f *loadFailure) Code {
	switch f.kind {
	case kindTimeout:
		return CodeLoadTimeout
	case kindEngineStart:
		return CodeEngineStart
	case kindEngineNotReady:
		return CodeEngineNotReady
	case kindEngineStop:
		return CodeEngineNotFreed
	case kindRouterDown:
		return CodeRouterDown
	}
	return CodeModelFailed
}

// runWorkload is steps 3 to 7: diff, unload, load, and the rollback or the bookkeeping.
func (o *Orchestrator) runWorkload(ctx context.Context, in Inputs, w Workload, opID string) (WorkloadResult, error) {
	res := WorkloadResult{OpID: opID, Loaded: []string{}, Unloaded: []string{}}
	prev, err := o.residents(ctx, in)
	if err != nil {
		oe := errRouter(err)
		o.publish(events.TypeWorkloadDone, opID, events.WorkloadDone{OpID: opID, Name: w.Name, Result: events.WorkloadFailed, Code: string(oe.Code)})
		return res, oe
	}
	kept := survivalKept(in)
	var unload, load []string
	for _, id := range prev.ids {
		if !contains1(w.Models, id) && !kept[id] {
			unload = append(unload, id)
		}
	}
	// Least recently used first, as an eviction.
	for i, j := 0, len(unload)-1; i < j; i, j = i+1, j-1 {
		unload[i], unload[j] = unload[j], unload[i]
	}
	for _, id := range w.Models {
		if !prev.has(id) {
			load = append(load, id)
		}
	}
	total := len(unload) + len(load)
	done := 0
	o.log.Info("workload activation planned", "op_id", opID, "workload", w.Name, "unload", unload, "load", load)

	var unloaded, loaded []string
	var failure *loadFailure
	for _, id := range unload {
		o.publish(events.TypeWorkloadProgress, opID, events.WorkloadProgress{OpID: opID, Name: w.Name, Phase: events.WorkloadUnloading, Model: id, Done: done, Total: total})
		if f := o.stopAny(ctx, in, prev.engineOf[id], id); f != nil {
			failure = f
			break
		}
		done++
		unloaded = append(unloaded, id)
		o.publish(events.TypeModelUnloaded, opID, events.ModelUnloaded{Model: id, Reason: events.UnloadEvicted})
	}
	if failure == nil {
		for _, id := range load {
			o.publish(events.TypeWorkloadProgress, opID, events.WorkloadProgress{OpID: opID, Name: w.Name, Phase: events.WorkloadLoading, Model: id, Done: done, Total: total})
			o.publish(events.TypeModelLoading, opID, events.ModelLoading{Model: id, Unloading: []string{}, OpID: opID})
			m, _ := findModel(in.Catalog, id)
			eng := ""
			if m != nil && m.Engine != catalog.EngineLlama {
				eng = m.Engine
			}
			t := o.clock.Now()
			if f := o.startAny(ctx, in, eng, id); f != nil {
				failure = f
				o.log.Warn("a workload model did not load", "op_id", opID, "workload", w.Name, "model", id, "cause", f.kind)
				// What failed may still hold memory.
				_ = o.stopAny(ctx, in, eng, id)
				break
			}
			done++
			loaded = append(loaded, id)
			o.publish(events.TypeModelLoaded, opID, events.ModelLoaded{Model: id, LoadMS: ms(o.clock.Now().Sub(t))})
		}
	}

	if failure != nil {
		return o.workloadRollback(ctx, in, w, opID, prev, unloaded, loaded, failure)
	}

	now := o.clock.Now().UTC().Truncate(time.Second)
	after, _ := o.residents(ctx, in)
	o.persist("workload", func(st *state.State) error {
		if st.Models.LastUsed == nil {
			st.Models.LastUsed = map[string]time.Time{}
		}
		for _, id := range w.Models {
			st.Models.LastUsed[id] = now
		}
		var desired []string
		for _, id := range after.ids {
			if after.engineOf[id] == "" {
				desired = append(desired, id)
			}
		}
		st.Models.DesiredLoaded = nz(desired)
		st.Workloads = &state.Workloads{Active: w.Name, ActivatedAt: now}
		return nil
	})
	active := w.Name
	o.clear(CodeNothingLoaded, "")
	o.publish(events.TypeWorkloadDone, opID, events.WorkloadDone{OpID: opID, Name: w.Name, Result: events.WorkloadOK, Active: &active})
	o.log.Info("workload activated", "op_id", opID, "workload", w.Name, "loaded", loaded, "unloaded", unloaded)
	res.Active, res.Loaded, res.Unloaded = w.Name, nz(loaded), nz(unloaded)
	return res, nil
}

// workloadRollback is step 6: stop what this activation loaded, start the previous set again (most
// recently used first, each on its engine, each once), then the default and the survival list when
// nothing is loaded.
func (o *Orchestrator) workloadRollback(ctx context.Context, in Inputs, w Workload, opID string, prev residentSet,
	unloaded, loaded []string, f *loadFailure) (WorkloadResult, error) {
	res := WorkloadResult{OpID: opID, Loaded: []string{}, Unloaded: []string{}}
	if f.kind == kindInterrupted {
		return res, ErrInterrupted
	}
	o.countRollback()
	for _, id := range loaded {
		m, _ := findModel(in.Catalog, id)
		eng := ""
		if m != nil && m.Engine != catalog.EngineLlama {
			eng = m.Engine
		}
		if sf := o.stopAny(ctx, in, eng, id); sf != nil {
			o.log.Warn("could not stop a workload model during the rollback", "op_id", opID, "model", id, "cause", sf.kind)
		} else {
			o.publish(events.TypeModelUnloaded, opID, events.ModelUnloaded{Model: id, Reason: events.UnloadRollback})
		}
	}
	restored := o.restoreAll(ctx, in, opID, unloaded, prev.engineOf)
	if o.noneLoaded(ctx) {
		tried := map[string]bool{}
		for _, id := range unloaded {
			tried[id] = true
		}
		for _, c := range o.fallbackOrder(ctx, in, o.opt.State.Snapshot(), tried) {
			if lf := o.loadOne(ctx, c, o.loadTimeout(in, c), nil); lf == nil {
				restored = append(restored, c)
				break
			}
		}
	}
	cause := workloadCause(f)
	d := map[string]any{"name": w.Name, "op_id": opID, "cause": string(cause), "restored": append([]string{}, restored...)}
	if f.victim != "" {
		d["model"] = f.victim
	}
	var oe *OpError
	if o.noneLoaded(ctx) && !o.anyEngineHolds(ctx) {
		o.raise(CodeNothingLoaded, "", map[string]any{"workload": w.Name})
		oe = opErr(CodeNothingLoaded, http.StatusInternalServerError, "No model could be started, not even the previous ones.", d, f)
	} else {
		oe = opErr(CodeWorkloadFailed, http.StatusInternalServerError,
			"The workload could not be switched on. The previous models were started again.", d, f)
	}
	o.publish(events.TypeWorkloadDone, opID, events.WorkloadDone{OpID: opID, Name: w.Name, Result: events.WorkloadRolledBack, Code: string(oe.Code)})
	o.log.Error("workload activation failed", "op_id", opID, "workload", w.Name, "code", string(oe.Code), "cause", string(cause), "restored", restored)
	return res, oe
}

// anyEngineHolds reports whether a non-llama engine holds a model.
func (o *Orchestrator) anyEngineHolds(ctx context.Context) bool {
	for _, a := range o.opt.Engines {
		if ems, err := a.Resident(ctx); err == nil && len(ems) > 0 {
			return true
		}
	}
	return false
}
