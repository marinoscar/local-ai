package router

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/api"
	"github.com/marinoscar/local-ai/agent/internal/audit"
	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/events"
	"github.com/marinoscar/local-ai/agent/internal/services"
	"github.com/marinoscar/local-ai/agent/internal/state"
)

// HolderRestart is the kind of a modelOpsMu holder that restarts llama (BusyError.Kind, and the
// details.running of a 409 LAI-AGT-022).
const HolderRestart = "restart"

// ---- the installed build ---------------------------------------------------------------

// DefaultBuildJSON is the BUILD.json of the build the `current` link points at (llama-router.md
// sections 3.1 and 4.10.3).
const DefaultBuildJSON = "/opt/localai/llama.cpp/current/BUILD.json"

// BuildSource tells which llama.cpp build is installed, by its build id ("f3586e66b-cuda").
type BuildSource interface {
	CurrentBuild() (string, error)
}

// FileBuild reads the build id from BUILD.json.
type FileBuild struct {
	// FS is the filesystem seam; default config.OSFS.
	FS config.FS
	// Path is the file; default DefaultBuildJSON.
	Path string
}

var _ BuildSource = FileBuild{}

var buildIDRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,79}$`)

// CurrentBuild implements BuildSource. A missing file, a file that is not JSON and an id that is not
// a plain name are errors: the recorded build is never replaced by something unreadable.
func (f FileBuild) CurrentBuild() (string, error) {
	fsys, path := f.FS, f.Path
	if fsys == nil {
		fsys = config.OSFS{}
	}
	if path == "" {
		path = DefaultBuildJSON
	}
	raw, err := fsys.ReadFile(path)
	if err != nil {
		return "", err
	}
	var b struct {
		BuildID string `json:"build_id"`
	}
	if err := json.Unmarshal(raw, &b); err != nil {
		return "", fmt.Errorf("router: %s is not valid JSON: %w", path, err)
	}
	if !buildIDRe.MatchString(b.BuildID) {
		return "", fmt.Errorf("router: %s has no usable build_id", path)
	}
	return b.BuildID, nil
}

// refreshBuild records the installed build in state.llama.build; the build it replaces becomes
// previous_build (llama-router.md section 4.10.3). Nothing changes when the build is the same or
// cannot be read.
func (o *Orchestrator) refreshBuild() {
	if o.opt.Builds == nil {
		return
	}
	id, err := o.opt.Builds.CurrentBuild()
	if err != nil {
		o.log.Warn("could not read the installed llama build", "phase", "restart", "error", err)
		return
	}
	changed := false
	o.persist("llama build", func(s *state.State) error {
		if s.Llama.Build == id {
			return nil
		}
		if s.Llama.Build != "" {
			prev := s.Llama.Build
			s.Llama.PreviousBuild = &prev
		}
		s.Llama.Build = id
		changed = true
		return nil
	})
	if changed {
		o.log.Info("the llama build changed", "phase", "restart", "build", id)
	}
}

// ---- RestartAndRestore ------------------------------------------------------------------

// RestartRequest is one call of RestartWith.
type RestartRequest struct {
	// Actor is who asked; it goes to the log (the caller writes the audit line of its own action:
	// key.rotate, service.restart, llama.build.switch).
	Actor audit.Actor
	// LockWait is how long to wait for modelOpsMu; 0 means do not wait (busy is 409 LAI-AGT-022).
	LockWait time.Duration
	// Prepare, when set, runs once modelOpsMu is held and before the unit is restarted, with the
	// id of the operation. POST /key/rotate writes the new key file in it, so that a busy lock
	// leaves the old key untouched and no model operation can run between the write and the
	// restart. An error aborts the restart and is returned as it is (the unit is not touched).
	Prepare func(ctx context.Context, opID string) error
}

// RestartResult is the outcome of RestartAndRestore.
type RestartResult struct {
	// OpID is the operation that held modelOpsMu.
	OpID string
	// Restored are the models loaded at the end that the restore set asked for, default first:
	// the ones the router brought back by itself (load-on-startup) and the ones loaded here. It is
	// the restored_models of POST /services/llama/restart and POST /key/rotate.
	Restored []string
	// Skipped are the models of the restore set that are not loaded, and why.
	Skipped []RestoreSkip
}

// RestartAndRestore restarts localai-llama and brings the models back (llama-router.md section
// 4.11). It is what POST /key/rotate and the llama restart and build switch call, and it is
// RestartWith with no lock wait.
func (o *Orchestrator) RestartAndRestore(ctx context.Context, actor audit.Actor) (RestartResult, error) {
	return o.RestartWith(ctx, RestartRequest{Actor: actor})
}

// RestartWith takes modelOpsMu, restarts the unit through OrchestratorOptions.Restarter, waits for
// the router's /health (60 s, Timing.HealthWait), records the installed build in state.llama.build
// and restores the models: the effective default first (the router starts it by itself through
// load-on-startup, which counts, and is waited for), then state.models.desired_loaded most recently
// used first, always with unload_others false. A model that does not fit or does not start is
// skipped (RestartResult.Skipped); the default that does not start raises LAI-LLM-013. In survival
// mode with no model loaded after that, the survival list is tried in order.
//
// Errors: 409 LAI-AGT-022 when another model operation holds the lock; 500 LAI-LLM-001 when the unit
// could not be restarted (details.cause restart_failed) or the router did not answer within the
// minute (health_timeout). A key rotation that gets one of those still keeps the new key (agent.md
// section 3.8.14). When the restore set was not empty and nothing could be loaded the call still
// succeeds, with empty Restored: LAI-LLM-012 is raised so the operator hears of it.
//
// Like a load, the operation cannot be cancelled by its caller once the lock is held, only by
// Shutdown: stopping halfway would leave llama down. A caller that gives up gets its own context
// error.
func (o *Orchestrator) RestartWith(ctx context.Context, req RestartRequest) (RestartResult, error) {
	if o.isClosing() {
		return RestartResult{}, ErrInterrupted
	}
	if o.opt.Restarter == nil {
		return RestartResult{}, errors.New("router: the orchestrator has no LlamaRestarter")
	}
	hold, err := o.lock.AcquireWait(ctx, HolderRestart, req.LockWait)
	if err != nil {
		var busy *BusyError
		if errors.As(err, &busy) {
			return RestartResult{}, errBusy(busy)
		}
		return RestartResult{}, err
	}
	act := func(c context.Context, opID string) error {
		if req.Prepare != nil {
			if perr := req.Prepare(c, opID); perr != nil {
				return perr
			}
		}
		if rerr := o.opt.Restarter.RestartLlama(c); rerr != nil {
			if errors.Is(rerr, ErrInterrupted) {
				return ErrInterrupted
			}
			return opErr(CodeLlamaDown, 500, "The AI model service could not be restarted.",
				map[string]any{"op_id": opID, "cause": "restart_failed"}, rerr)
		}
		return nil
	}
	return o.runRestart(ctx, hold, hold.OpID, req.Actor, act, true)
}

// runRestart runs the steps of a restart on a context the caller cannot cancel, while the caller
// waits (or gives up). opID names the operation in events and logs. It releases the lock when the
// steps end.
func (o *Orchestrator) runRestart(ctx context.Context, hold Hold, opID string, actor audit.Actor, act func(context.Context, string) error, restore bool) (RestartResult, error) {
	opctx := context.WithoutCancel(ctx)
	done := make(chan struct{})
	var res RestartResult
	var rerr error
	o.wg.Add(1)
	go func() {
		defer o.wg.Done()
		defer close(done)
		unsup := o.suppress(opID)
		res, rerr = o.restartSteps(opctx, opID, actor, act, restore)
		unsup()
		hold.Release()
	}()
	select {
	case <-done:
		return res, rerr
	case <-ctx.Done():
		return RestartResult{OpID: opID}, ctx.Err()
	}
}

func (o *Orchestrator) restartSteps(ctx context.Context, opID string, actor audit.Actor, act func(context.Context, string) error, restore bool) (RestartResult, error) {
	res := RestartResult{OpID: opID, Restored: []string{}, Skipped: []RestoreSkip{}}
	t0 := o.clock.Now()
	in := o.opt.Inputs()
	o.log.Info("llama restart", "op_id", opID, "phase", "restart", "actor", string(actor.Kind))
	if err := act(ctx, opID); err != nil {
		return res, err
	}
	if !restore {
		return res, nil
	}
	o.refreshBuild()
	if !o.waitHealth(ctx, o.opt.Timing.HealthWait) {
		err := o.healthFailure(ctx, "restart", opID)
		o.log.Error("the router did not answer after the restart", "op_id", opID, "phase", "restart", "code", string(CodeLlamaDown))
		return res, err
	}
	if in.Llama.Mode == LlamaModeSingle {
		// A single-model llama-server loads its model by itself; there is no router to restore.
		return res, nil
	}
	if err := o.restoreHeld(ctx, in, opID, &res); err != nil {
		return res, err
	}
	o.log.Info("llama restarted", "op_id", opID, "phase", "restart", "restored", res.Restored,
		"skipped", len(res.Skipped), "duration_ms", ms(o.clock.Now().Sub(t0)))
	return res, nil
}

// restoreHeld brings the restore set back while the caller holds modelOpsMu: it fills res.Restored
// and res.Skipped.
func (o *Orchestrator) restoreHeld(ctx context.Context, in Inputs, opID string, res *RestartResult) error {
	statuses, err := o.opt.Router.Models(ctx)
	if err != nil {
		return errRouter(err)
	}
	st := o.opt.State.Snapshot()
	def := o.effectiveDefault(in, st, statuses)
	order := restoreOrder(def, st.Models.DesiredLoaded, st.Models.LastUsed)

	tried := map[string]bool{}
	for _, id := range order {
		tried[id] = true
		step := o.restoreStep(ctx, in, opID, id)
		if step.interrupted {
			return ErrInterrupted
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if step.skip != nil {
			res.Skipped = append(res.Skipped, *step.skip)
		}
		if id == def {
			switch {
			case step.loaded:
				o.clear(CodeDefaultNotStarted, "")
			case step.skip != nil && step.skip.Code != CodeNoRoom:
				o.defaultFailed(def, step.skip.Reason)
			}
		}
	}
	if def == "" {
		o.clear(CodeDefaultNotStarted, "")
	}

	// Survival mode with nothing loaded: the survival list, in order, until models_max are loaded.
	extra := []string{}
	if in.Survival && in.Profile != nil {
		if now, err := o.opt.Router.Models(ctx); err == nil && !anyResident(now) {
			for _, id := range in.Profile.Survival.Models {
				if tried[id] {
					continue
				}
				if now, err := o.opt.Router.Models(ctx); err != nil || runningCount(now) >= in.modelsMax() {
					break
				}
				tried[id] = true
				step := o.restoreStep(ctx, in, opID, id)
				if step.interrupted {
					return ErrInterrupted
				}
				if step.skip != nil {
					res.Skipped = append(res.Skipped, *step.skip)
				}
				extra = append(extra, id)
			}
		}
	}

	final, err := o.opt.Router.Models(ctx)
	if err != nil {
		return errRouter(err)
	}
	byID := statusByID(final)
	for _, id := range append(order, extra...) {
		if s, ok := byID[id]; ok && s.Status.Resident() {
			res.Restored = append(res.Restored, id)
		}
	}
	switch {
	case anyResident(final):
		o.clear(CodeNothingLoaded, "")
	case len(order)+len(extra) > 0:
		o.raise(CodeNothingLoaded, "", map[string]any{"cause": "restart_restore"})
	}
	return nil
}

// stepResult is what restoreStep did for one model.
type stepResult struct {
	// loaded is true when the model is loaded at the end; started when this step loaded it.
	loaded, started bool
	skip            *RestoreSkip
	interrupted     bool
}

// restoreStep brings one model of the restore set up while the caller holds modelOpsMu: a model
// that is loaded stays, one that is loading is waited for (its load is re-attached to, never
// repeated), any other is loaded when it is a llama model with a preset that fits next to what is
// loaded without evicting anything. A model that fails is stopped again so it does not hold memory
// the next one needs.
func (o *Orchestrator) restoreStep(ctx context.Context, in Inputs, opID, id string) stepResult {
	skip := func(code Code, reason string) stepResult {
		o.log.Warn("could not restore a model", "op_id", opID, "model", id, "phase", "restart", "code", string(code), "cause", reason)
		return stepResult{skip: &RestoreSkip{ID: id, Code: code, Reason: reason}}
	}
	statuses, err := o.opt.Router.Models(ctx)
	if err != nil {
		code := CodeOf(err)
		if code == "" {
			code = CodeRouterDown
		}
		return skip(code, kindRouterDown)
	}
	m, inCatalog := findModel(in.Catalog, id)
	cur, listed := statusByID(statuses)[id]
	var f *loadFailure
	started := false
	switch {
	case !inCatalog:
		return skip(CodeNotFound, "not_in_catalog")
	case m.Engine != "llama":
		return skip(CodeNotFit, "not_llama")
	case !listed:
		return skip(CodePresetFailed, "no_preset")
	case cur.Status.Resident():
		return stepResult{loaded: true}
	case cur.Status == StatusLoading:
		f = o.waitLoaded(ctx, id, o.loadTimeout(in, id), nil)
	default:
		_, present := o.locate(in, m)
		cand := Candidate{ID: m.ID, Requirements: m.Requirements, Present: present}
		if fits, why := in.Fit.StaticFit(cand); !fits {
			return skip(why.Code, "not_loadable")
		}
		st := o.opt.State.Snapshot()
		if _, _, perr := in.Fit.Plan(cand, loadedFrom(in, st, statuses, id), false); perr != nil {
			return skip(CodeNoRoom, "no_room")
		}
		t := o.clock.Now()
		f = o.loadOne(ctx, id, o.loadTimeout(in, id), nil)
		if f == nil {
			started = true
			o.publish(events.TypeModelLoaded, opID, events.ModelLoaded{Model: id, LoadMS: ms(o.clock.Now().Sub(t))})
			o.log.Info("model restored", "op_id", opID, "model", id, "phase", "restart", "duration_ms", ms(o.clock.Now().Sub(t)))
		}
	}
	if f == nil {
		return stepResult{loaded: true, started: started}
	}
	if f.kind == kindInterrupted {
		return stepResult{interrupted: true}
	}
	if now, err := o.opt.Router.Models(ctx); err == nil {
		if s, ok := statusByID(now)[id]; ok && s.Status.Running() {
			_ = o.stopOne(ctx, id, o.opt.Timing.EvictTimeout)
		}
	}
	return skip(failureCode(f), f.kind)
}

// failureCode is the doctor code of a load step that failed.
func failureCode(f *loadFailure) Code {
	switch f.kind {
	case kindTimeout:
		return CodeLoadTimeout
	case kindRouterDown:
		return CodeRouterDown
	case kindNotListed:
		return CodePresetFailed
	}
	return CodeModelFailed
}

// ---- the /services/llama hook ------------------------------------------------------------

// LlamaHook is the api.LlamaRestarter over an Orchestrator: POST /services/llama/{action} runs its
// unit action through it (agent.md section 3.8.22). It takes modelOpsMu without waiting (a busy lock
// is the 409 LAI-AGT-022 with the holder's op id, and the unit is not touched), runs the action, and
// after a start or restart does what RestartAndRestore does: waits for /health, records the build
// and restores the models, returning their ids. A stop restores nothing.
type LlamaHook struct{ o *Orchestrator }

var _ api.LlamaRestarter = (*LlamaHook)(nil)

// NewLlamaHook returns the hook of o.
func NewLlamaHook(o *Orchestrator) *LlamaHook { return &LlamaHook{o: o} }

// Run implements api.LlamaRestarter. An error of op is returned as it is, for the route to map
// (*services.ActionError becomes 502 LAI-LLM-001); a router that did not answer after the action
// is the *api.Error of 500 LAI-LLM-001.
func (h *LlamaHook) Run(ctx context.Context, opID string, action services.Action, op func(context.Context) error) ([]string, error) {
	o := h.o
	if o.isClosing() {
		return nil, apiError(api.InfoFrom(ctx).Lang, ErrInterrupted)
	}
	hold, err := o.lock.TryAcquire(HolderRestart)
	if err != nil {
		var busy *BusyError
		if errors.As(err, &busy) {
			return nil, &api.BusyError{OpID: busy.OpID}
		}
		return nil, err
	}
	act := func(c context.Context, _ string) error { return op(c) }
	res, err := o.runRestart(ctx, hold, opID, audit.Actor{Kind: audit.KindOperator}, act, action != services.ActionStop)
	if err != nil {
		var oe *OpError
		if errors.As(err, &oe) || errors.Is(err, ErrInterrupted) {
			return nil, apiError(api.InfoFrom(ctx).Lang, err)
		}
		return nil, err
	}
	return res.Restored, nil
}
