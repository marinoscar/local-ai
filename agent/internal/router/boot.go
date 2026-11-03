package router

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/audit"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/events"
	"github.com/marinoscar/local-ai/agent/internal/state"
)

// Doctor codes of the boot and restart paths (llama-router.md sections 4.6, 4.11 and 8). Like the
// other codes of this package they alias internal/doctor/codes.go, the one file that spells a
// "LAI-..." literal.
const (
	// CodeDefaultNotStarted: the default model did not start at boot (or after a llama restart).
	CodeDefaultNotStarted Code = doctor.LLM013
	// CodeNoDefault: no default model is set or available. The generator raises it too; the boot
	// restore keeps it in step with what the router lists.
	CodeNoDefault = CodePresetNoDefault
	// CodeLlamaDown: localai-llama did not restart, or did not answer after it was restarted.
	CodeLlamaDown Code = doctor.LLM001
)

// DefaultReconcileBudget is the time step 12 of the agent's startup may take (agent.md section 4.1).
const DefaultReconcileBudget = 30 * time.Second

// ErrReconcileBudget is reported by Reconcile when the budget ran out before every step finished.
var ErrReconcileBudget = errors.New("router: the llama reconcile ran out of time")

// ---- Reconcile (agent startup step 12) ---------------------------------------------------

// UnitRegenerator renders and installs localai-llama.service; *UnitGenerator implements it. It runs
// daemon-reload itself when the file changed.
type UnitRegenerator interface {
	Generate(ctx context.Context, in UnitInputs) (UnitResult, error)
}

// HealthChecker is the part of the router client Reconcile needs: GET /health.
type HealthChecker interface {
	Health(ctx context.Context) error
}

// LlamaUnitState is what Reconcile reads of localai-llama.service.
type LlamaUnitState struct {
	// Running is true when the unit is active, activating or reloading: a start would change
	// nothing, so Reconcile does not ask for one.
	Running bool
	// Enabled is true when the unit starts at boot.
	Enabled bool
}

// LlamaUnit is the systemd seam of Reconcile for localai-llama.service. internal/services.Manager
// with services.UnitLlama implements it (UnitState.Active, Enabled and Manager.Start); the wiring
// (AGENT-20) writes the three-line adapter.
type LlamaUnit interface {
	State(ctx context.Context) (LlamaUnitState, error)
	// Start starts the unit and waits for the job to finish.
	Start(ctx context.Context) error
}

// ReconcileOptions configures NewReconciler. Inputs is required; a nil Units, Models, Router or Unit
// makes its part a no-op.
type ReconcileOptions struct {
	// Units renders the unit; Models renders models.ini and the environment drop-in and applies them
	// to a running router (the reload-safety rule of llama-router.md section 4.2).
	Units  UnitRegenerator
	Models Regenerator
	// Inputs returns the facts both generators decide from, once per Reconcile. A UnitInputs.Mode of
	// single leaves models.ini alone (llama-router.md section 4.8).
	Inputs func() (Inputs, UnitInputs)
	Router HealthChecker
	Unit   LlamaUnit
	// Budget bounds a whole Reconcile; default DefaultReconcileBudget (30 s).
	Budget time.Duration
	Logger *slog.Logger
}

// Reconciler is agent startup step 12 (agent.md section 4.1, llama-router.md section 4.6).
type Reconciler struct {
	opt ReconcileOptions
	log *slog.Logger
}

// NewReconciler returns a Reconciler.
func NewReconciler(opt ReconcileOptions) (*Reconciler, error) {
	if opt.Inputs == nil {
		return nil, errors.New("router: the reconciler needs an Inputs function")
	}
	if opt.Budget <= 0 {
		opt.Budget = DefaultReconcileBudget
	}
	l := opt.Logger
	if l == nil {
		l = slog.Default()
	}
	return &Reconciler{opt: opt, log: l.With("component", "router")}, nil
}

// ReconcileResult is what one Reconcile did.
type ReconcileResult struct {
	// Unit is the unit generator's result; UnitRan is false when there is no generator, UnitErr is
	// a failure to bring the unit file to its inputs.
	Unit    UnitResult
	UnitRan bool
	UnitErr error
	// Models is the preset generator's result (its Reloaded field says the router was told to
	// re-read the file); ModelsRan is false when there is no generator or the node runs llama in
	// single mode.
	Models    Result
	ModelsRan bool
	ModelsErr error
	// RouterUp is true when /health answered after the files were brought up to date.
	RouterUp bool
	// UnitState is what the unit looked like when the router did not answer; StateErr is why it
	// could not be read.
	UnitState LlamaUnitState
	StateErr  error
	// Started is true when Reconcile started the unit; StartErr is why that failed.
	Started  bool
	StartErr error
	// TimedOut is true when the budget ran out.
	TimedOut bool
}

// Reconcile brings llama to what the node's inputs call for (agent.md section 4.1, step 12):
//
//  1. render the unit; when its file changed the unit generator runs daemon-reload (once);
//  2. render models.ini and the drop-in; when the file changed and the router is up the generator
//     tells it to re-read the file under the reload-safety rule (GET /models?reload=1), unloading
//     and reloading only the models whose preset changed;
//  3. when the router does not answer and the unit is enabled and not running, start it.
//
// Each step runs even when an earlier one failed: a unit that cannot be written must not keep the
// router from getting its model list, and a router that is down must be started whatever happened
// to the files. The whole call is bounded by the budget (30 s). The error joins what failed; it is
// nil when everything worked. The caller opens the mutating-route gate when Reconcile returns, with
// or without an error.
//
// Reconcile never restarts a running llama: a unit or environment change that needs one raises
// LAI-AGT-016 (in the generators) and waits for the operator.
func (r *Reconciler) Reconcile(ctx context.Context) (ReconcileResult, error) {
	var res ReconcileResult
	bctx, cancel := context.WithTimeout(ctx, r.opt.Budget)
	defer cancel()
	in, uin := r.opt.Inputs()
	var errs []error

	if r.opt.Units != nil && bctx.Err() == nil {
		res.UnitRan = true
		res.Unit, res.UnitErr = r.opt.Units.Generate(bctx, uin)
		if res.UnitErr != nil {
			r.log.Error("could not bring the llama unit up to date", "phase", "reconcile", "error", res.UnitErr)
			errs = append(errs, res.UnitErr)
		}
	}
	single := uin.Mode == LlamaModeSingle
	if r.opt.Models != nil && !single && bctx.Err() == nil {
		res.ModelsRan = true
		res.Models, res.ModelsErr = r.opt.Models.Generate(bctx, in)
		if res.ModelsErr != nil {
			r.log.Error("could not bring models.ini up to date", "phase", "reconcile", "error", res.ModelsErr)
			errs = append(errs, res.ModelsErr)
		}
	}
	r.startIfDown(bctx, &res)
	if res.StartErr != nil {
		errs = append(errs, res.StartErr)
	}
	if bctx.Err() != nil && ctx.Err() == nil {
		res.TimedOut = true
		errs = append(errs, ErrReconcileBudget)
		r.log.Error("the llama reconcile ran out of time", "phase", "reconcile", "budget_ms", ms(r.opt.Budget))
	} else if err := ctx.Err(); err != nil {
		errs = append(errs, err)
	}
	r.log.Info("llama reconciled", "phase", "reconcile",
		"unit_written", res.Unit.Written, "unit_reloaded", res.Unit.Reloaded,
		"ini_written", res.Models.Written, "router_reloaded", res.Models.Reloaded,
		"router_up", res.RouterUp, "started", res.Started)
	return res, errors.Join(errs...)
}

// startIfDown is step 3: the router does not answer, the unit is enabled and not running.
func (r *Reconciler) startIfDown(ctx context.Context, res *ReconcileResult) {
	if r.opt.Router == nil || ctx.Err() != nil {
		return
	}
	if r.opt.Router.Health(ctx) == nil {
		res.RouterUp = true
		return
	}
	if r.opt.Unit == nil {
		return
	}
	us, err := r.opt.Unit.State(ctx)
	if err != nil {
		res.StateErr = err
		res.StartErr = fmt.Errorf("router: could not read the state of the llama unit: %w", err)
		r.log.Error("could not read the state of the llama unit", "phase", "reconcile", "error", err)
		return
	}
	res.UnitState = us
	switch {
	case us.Running:
		r.log.Info("the router does not answer yet; the unit is starting", "phase", "reconcile")
	case !us.Enabled:
		r.log.Warn("the router is down and the llama unit is not enabled; leaving it stopped", "phase", "reconcile")
	default:
		if err := r.opt.Unit.Start(ctx); err != nil {
			res.StartErr = fmt.Errorf("router: could not start the llama unit: %w", err)
			r.log.Error("could not start the llama unit", "phase", "reconcile", "error", err)
			return
		}
		res.Started = true
		r.log.Info("the router was down; the llama unit was started", "phase", "reconcile")
	}
}

// ---- RestoreModels (agent startup step 14) -----------------------------------------------

// States of the default model after RestoreModels (RestoreResult.DefaultState).
const (
	// DefaultStateNone: no model qualifies as the default (LAI-LLM-014).
	DefaultStateNone = "none"
	// DefaultStateLoaded: the default was loaded when the restore looked (or became loaded while
	// the router finished loading it): load-on-startup did its work, so nothing was called.
	DefaultStateLoaded = "loaded"
	// DefaultStateStarted: the agent loaded the default itself.
	DefaultStateStarted = "started"
	// DefaultStateFailed: the default did not start (LAI-LLM-013).
	DefaultStateFailed = "failed"
	// DefaultStateNoRoom: another model is loaded and the default does not fit next to it; boot
	// never evicts, so it was left alone (no alert).
	DefaultStateNoRoom = "no_room"
	// DefaultStateSurvival: survival mode with no model loaded: the survival set replaced the
	// default and the desired set.
	DefaultStateSurvival = "survival"
	// DefaultStateSingle: llama runs in single mode; it loads its model by itself.
	DefaultStateSingle = "single"
)

// RestoreSkip is a model of the restore set that is not loaded after the restore, and why.
type RestoreSkip struct {
	ID string
	// Code is the doctor code of the reason (LAI-LLM-011 no room, LAI-CAT-005 or LAI-LLM-008 not
	// loadable, LAI-LLM-009 or LAI-LLM-010 a load that failed or timed out, ...).
	Code Code
	// Reason is a short key: no_room, not_loadable, not_in_catalog, not_llama, no_preset,
	// router_down, or the cause of a failed load (rejected, failed, timeout, not_listed).
	Reason string
}

// RestoreResult is what a boot restore did (RestoreModels).
type RestoreResult struct {
	// Default is the effective default ("" when there is none) and DefaultState what became of it.
	Default      string
	DefaultState string
	// Loaded are the models that are loaded at the end: the default first, then the rest of the
	// restore set in order, then any other model that is loaded.
	Loaded []string
	// Started are the models this call loaded itself.
	Started []string
	// Skipped are the models of the restore set that are not loaded, and why.
	Skipped []RestoreSkip
	// Survival is true when the survival set was applied; Warnings are its LAI-LLM-020 and
	// LAI-LLM-021 notices.
	Survival bool
	Warnings []events.Warning
}

func newRestoreResult() RestoreResult {
	return RestoreResult{Loaded: []string{}, Started: []string{}, Skipped: []RestoreSkip{}}
}

// RestoreModels is agent startup step 14 (agent.md section 4.1, llama-router.md section 4.6). It
// runs in the background after Reconcile and never evicts a model:
//
//  1. wait for the router's /health, at most 120 s (Timing.RestartWait). The router may have
//     started loading the default by itself (load-on-startup), and that counts;
//  2. wait for the default to leave "loading", at most its load timeout. A model that is loading
//     already is simply waited for, never loaded twice;
//  3. in survival mode with no model loaded, load the survival set (LoadSurvivalSet) and stop: the
//     survival set replaces the desired set;
//  4. if the default is not loaded, load it through the orchestrator, waiting for the lock up to
//     DefaultLockWait so that an operator's load that is in progress is not refused. A default that
//     does not start (not loadable, a failed load, a load that timed out) raises LAI-LLM-013 and
//     the restore goes on;
//  5. load the rest of state.models.desired_loaded, most recently used first, while models_max and
//     the dynamic fit allow, with unload_others false. A model that does not fit is skipped, not
//     an error.
//
// The effective default is computed from the router's own preset list (EffectiveDefault); when it
// is empty LAI-LLM-014 is raised, and cleared when a default exists. The error is nil whenever the
// restore ran, even when models were skipped (see RestoreResult); it is set when the router never
// answered (LAI-LLM-001), when it could not be read, and for a shutdown or a cancelled ctx.
//
// Models loaded here take the same path as an operator's load: events, audit lines (actor system),
// counters and state.models.last_load. state.models.desired_loaded is kept whole while the restore
// runs (a crash in the middle must not forget the models still waiting) and ends as what is loaded,
// as after any load; when nothing could be loaded it is left as it was, so the next boot retries.
func (o *Orchestrator) RestoreModels(ctx context.Context) (RestoreResult, error) {
	res := newRestoreResult()
	if o.isClosing() {
		return res, ErrInterrupted
	}
	in := o.opt.Inputs()
	if in.Llama.Mode == LlamaModeSingle {
		res.DefaultState = DefaultStateSingle
		return res, nil
	}
	t0 := o.clock.Now()
	if !o.waitHealth(ctx, o.opt.Timing.RestartWait) {
		return res, o.healthFailure(ctx, "boot", "")
	}
	statuses, err := o.opt.Router.Models(ctx)
	if err != nil {
		return res, errRouter(err)
	}
	st := o.opt.State.Snapshot()
	def := o.effectiveDefault(in, st, statuses)
	res.Default = def

	// 2. The router's own load of the default.
	var waited *loadFailure
	if s, ok := statusByID(statuses)[def]; ok && def != "" && s.Status == StatusLoading {
		o.log.Info("the default model is loading; waiting for it", "model", def, "phase", "boot")
		waited = o.waitLoaded(ctx, def, o.loadTimeout(in, def), nil)
		if waited != nil && waited.kind == kindInterrupted {
			return res, ErrInterrupted
		}
		if err := ctx.Err(); err != nil {
			return res, err
		}
		if statuses, err = o.opt.Router.Models(ctx); err != nil {
			return res, errRouter(err)
		}
	}

	// 3. Survival mode with nothing loaded: the survival set replaces everything else.
	if in.Survival && !anyResident(statuses) {
		o.log.Warn("survival mode and no model is loaded; loading the survival set", "phase", "boot")
		warns, serr := o.LoadSurvivalSet(ctx, nil)
		switch {
		case serr == nil:
			res.Survival, res.Warnings, res.DefaultState = true, warns, DefaultStateSurvival
			if waited != nil {
				o.defaultFailed(def, waited.kind)
			}
			o.finishRestore(ctx, &res, st, nil, t0)
			return res, nil
		case errors.Is(serr, ErrInterrupted):
			return res, ErrInterrupted
		case ctx.Err() != nil:
			return res, ctx.Err()
		}
		// No survival list, or the router stopped answering: try the normal path.
		o.log.Warn("the survival set could not be applied; restoring the default instead", "phase", "boot", "error", serr)
	}

	// 4. The default.
	order := restoreOrder(def, st.Models.DesiredLoaded, st.Models.LastUsed)
	rest := order
	res.DefaultState = DefaultStateNone
	if def != "" {
		rest = order[1:]
		if err := o.ensureDefault(ctx, &res, in, def, waited); err != nil {
			return res, err
		}
		o.keepDesired(ctx, rest)
	}

	// 5. The rest of the desired set.
	for i, id := range rest {
		if err := o.restoreDesired(ctx, &res, in, id, rest[i+1:]); err != nil {
			return res, err
		}
	}
	o.finishRestore(ctx, &res, st, order, t0)
	return res, nil
}

// ensureDefault is step 4 of RestoreModels.
func (o *Orchestrator) ensureDefault(ctx context.Context, res *RestoreResult, in Inputs, def string, waited *loadFailure) error {
	statuses, err := o.opt.Router.Models(ctx)
	if err != nil {
		return errRouter(err)
	}
	if s, ok := statusByID(statuses)[def]; ok && s.Status.Resident() {
		res.DefaultState = DefaultStateLoaded
		return nil
	}
	if waited != nil && waited.timeout() {
		// It has been loading for the whole load timeout. Waiting a second time would only hold
		// the lock (and everything behind it) for another five minutes.
		res.DefaultState = DefaultStateFailed
		res.Skipped = append(res.Skipped, RestoreSkip{ID: def, Code: CodeLoadTimeout, Reason: kindTimeout})
		o.defaultFailed(def, kindTimeout)
		return nil
	}
	_, lerr := o.LoadWith(ctx, LoadRequest{
		ID: def, Wait: true, UnloadOthers: false, Actor: audit.System(), LockWait: DefaultLockWait,
	})
	if lerr == nil {
		res.DefaultState = DefaultStateStarted
		res.Started = append(res.Started, def)
		return nil
	}
	if errors.Is(lerr, ErrInterrupted) {
		return ErrInterrupted
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	skip := skipFrom(def, lerr)
	res.Skipped = append(res.Skipped, skip)
	if skip.Code == CodeNoRoom {
		// Another model holds the room (an agent restart while the operator's own choice is
		// loaded): boot never evicts, and nothing failed to start.
		res.DefaultState = DefaultStateNoRoom
		o.log.Info("the default model does not fit next to the loaded one; boot never evicts", "model", def, "phase", "boot")
		return nil
	}
	res.DefaultState = DefaultStateFailed
	o.defaultFailed(def, skip.Reason)
	return nil
}

// restoreDesired loads one model of the desired set through the orchestrator (step 5 of RestoreModels).
// rest are the models still waiting; they are kept in state.models.desired_loaded.
func (o *Orchestrator) restoreDesired(ctx context.Context, res *RestoreResult, in Inputs, id string, rest []string) error {
	statuses, err := o.opt.Router.Models(ctx)
	if err != nil {
		return errRouter(err)
	}
	cur, listed := statusByID(statuses)[id]
	if listed && cur.Status.Resident() {
		return nil
	}
	reattach := listed && cur.Status == StatusLoading
	if !reattach && runningCount(statuses) >= in.modelsMax() {
		res.Skipped = append(res.Skipped, RestoreSkip{ID: id, Code: CodeNoRoom, Reason: "no_room"})
		return nil
	}
	_, lerr := o.LoadWith(ctx, LoadRequest{
		ID: id, Wait: true, UnloadOthers: false, Actor: audit.System(), LockWait: DefaultLockWait,
	})
	switch {
	case lerr == nil:
		// A load the router began by itself is waited for, not started: it is not ours.
		if !reattach {
			res.Started = append(res.Started, id)
		}
	case errors.Is(lerr, ErrInterrupted):
		return ErrInterrupted
	case ctx.Err() != nil:
		return ctx.Err()
	default:
		skip := skipFrom(id, lerr)
		res.Skipped = append(res.Skipped, skip)
		o.log.Warn("could not restore a model", "model", id, "phase", "boot", "code", string(skip.Code), "cause", skip.Reason)
	}
	o.keepDesired(ctx, rest)
	return nil
}

// keepDesired makes state.models.desired_loaded the loaded models followed by the ones still
// waiting to be restored, so that a crash in the middle of a restore forgets none of them.
func (o *Orchestrator) keepDesired(ctx context.Context, pending []string) {
	if len(pending) == 0 {
		return
	}
	statuses, err := o.opt.Router.Models(ctx)
	if err != nil {
		return
	}
	o.persist("desired set", func(s *state.State) error {
		next := loadedIDs(statuses, s.Models.LastUsed)
		for _, id := range pending {
			if !contains1(next, id) {
				next = append(next, id)
			}
		}
		s.Models.DesiredLoaded = next
		return nil
	})
}

// finishRestore settles what a restore leaves behind: the list of loaded models, the alerts and
// desired_loaded.
func (o *Orchestrator) finishRestore(ctx context.Context, res *RestoreResult, before state.State, order []string, t0 time.Time) {
	statuses, err := o.opt.Router.Models(ctx)
	if err != nil {
		return
	}
	byID := statusByID(statuses)
	seen := map[string]bool{}
	add := func(id string) {
		if id == "" || seen[id] {
			return
		}
		seen[id] = true
		if s, ok := byID[id]; ok && s.Status.Resident() {
			res.Loaded = append(res.Loaded, id)
		}
	}
	add(res.Default)
	for _, id := range order {
		add(id)
	}
	for _, id := range loadedIDs(statuses, o.opt.State.Snapshot().Models.LastUsed) {
		add(id)
	}
	if res.Default == "" || contains1(res.Loaded, res.Default) {
		o.clear(CodeDefaultNotStarted, "")
	}
	if len(res.Loaded) == 0 && len(before.Models.DesiredLoaded) > 0 {
		// Nothing came back: keep the set the next boot should try again.
		want := append([]string(nil), before.Models.DesiredLoaded...)
		o.persist("desired set", func(s *state.State) error {
			s.Models.DesiredLoaded = want
			return nil
		})
	}
	o.log.Info("boot restore done", "phase", "boot", "default", res.Default, "default_state", res.DefaultState,
		"loaded", res.Loaded, "started", res.Started, "skipped", len(res.Skipped), "duration_ms", ms(o.clock.Now().Sub(t0)))
}

// effectiveDefault is the default of llama-router.md section 4.6, computed from the models the
// router lists. It keeps LAI-LLM-014 in step: raised without one, cleared with one.
func (o *Orchestrator) effectiveDefault(in Inputs, st state.State, statuses []ModelStatus) string {
	stateDefault := ""
	if st.Models.Default != nil {
		stateDefault = *st.Models.Default
	}
	ids := presetIDs(statuses)
	def := EffectiveDefault(ids, stateDefault, in.Profile, in.Survival)
	if def == "" {
		o.raise(CodeNoDefault, "", map[string]any{"models": len(ids)})
	} else {
		o.clear(CodeNoDefault, "")
	}
	return def
}

// defaultFailed raises LAI-LLM-013 for a default that did not start.
func (o *Orchestrator) defaultFailed(def, cause string) {
	o.log.Error("the default model did not start", "model", def, "phase", "boot", "code", string(CodeDefaultNotStarted), "cause", cause)
	o.raise(CodeDefaultNotStarted, "", map[string]any{"model": def, "cause": cause})
}

// skipFrom turns the error of a load into a RestoreSkip.
func skipFrom(id string, err error) RestoreSkip {
	var oe *OpError
	if errors.As(err, &oe) {
		reason := ""
		if c, ok := oe.Details["cause"].(string); ok {
			reason = c
		}
		switch oe.Code {
		case CodeNoRoom:
			reason = "no_room"
		case CodeNotFit, CodeFilesMissing:
			reason = "not_loadable"
			if oe.Details["engine"] != nil {
				reason = "not_llama"
			}
		case CodeNotFound:
			reason = "not_in_catalog"
		case CodeBusy:
			reason = "busy"
		}
		if reason == "" {
			reason = "failed"
		}
		return RestoreSkip{ID: id, Code: oe.Code, Reason: reason}
	}
	return RestoreSkip{ID: id, Code: CodeModelFailed, Reason: "failed"}
}

// restoreOrder lists the models a restore brings back: the effective default first, then
// desired_loaded most recently used first (the stored order, which is already most recent first,
// breaks ties), without duplicates.
func restoreOrder(def string, desired []string, lastUsed map[string]time.Time) []string {
	rest := make([]string, 0, len(desired))
	for _, id := range desired {
		if id != def && !contains1(rest, id) {
			rest = append(rest, id)
		}
	}
	sort.SliceStable(rest, func(i, j int) bool { return lastUsed[rest[i]].After(lastUsed[rest[j]]) })
	if def == "" {
		return rest
	}
	return append([]string{def}, rest...)
}

func anyResident(statuses []ModelStatus) bool {
	for _, s := range statuses {
		if s.Status.Resident() {
			return true
		}
	}
	return false
}

func runningCount(statuses []ModelStatus) int {
	n := 0
	for _, s := range statuses {
		if s.Status.Running() {
			n++
		}
	}
	return n
}

// healthFailure is the error of a router that did not answer in time. A shutdown or a cancelled
// caller is reported as such; anything else is LAI-LLM-001, the AI model service is not running.
func (o *Orchestrator) healthFailure(ctx context.Context, phase, opID string) error {
	if o.isClosing() {
		return ErrInterrupted
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	d := map[string]any{"cause": "health_timeout", "phase": phase}
	if opID != "" {
		d["op_id"] = opID
	}
	return opErr(CodeLlamaDown, 500, "The AI model service did not answer in time.", d, nil)
}
