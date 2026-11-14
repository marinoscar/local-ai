package modes

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/audit"
	"github.com/marinoscar/local-ai/agent/internal/events"
	"github.com/marinoscar/local-ai/agent/internal/services"
	"github.com/marinoscar/local-ai/agent/internal/state"
)

const typeModeStep = events.TypeModeStep

func eventsModeStep(target, step, st, code string) events.ModeStep {
	return events.ModeStep{Target: target, Step: step, State: st, Code: code}
}

// stepFn is a step: idempotent and safe to re-run from scratch (agent.md section 4.2.1). It receives a
// copy of the transition, honors ctx, and returns warnings; an error is turned into the step's
// failure warning by the engine, and the transition goes on.
type stepFn func(e *Engine, ctx context.Context, t *Transition) ([]Warning, error)

// stepDef is a row of the step table.
type stepDef struct {
	name string
	fn   stepFn
	// failCode is the warning raised when the step errors, panics or times out.
	failCode string
	// post marks the steps after persist: mode_transition is already cleared, so they are not
	// recorded and not resumed.
	post bool
}

func (s stepDef) failMessage(reason string) string {
	switch s.failCode {
	case CodeTailscaleServe:
		return "Tailscale Serve could not be changed."
	case CodeNoModel:
		return "No model could be started."
	}
	return fmt.Sprintf("Step %s %s.", s.name, reason)
}

var (
	defLinkStop       = stepDef{name: StepLinkStop, fn: (*Engine).stepLinkStop, failCode: CodeStepFailed}
	defSyncCancel     = stepDef{name: StepSyncCancel, fn: (*Engine).stepSyncCancel, failCode: CodeStepFailed}
	defProberStop     = stepDef{name: StepProberStop, fn: (*Engine).stepProberStop, failCode: CodeStepFailed}
	defTailscaleOff   = stepDef{name: StepTailscaleOff, fn: (*Engine).stepTailscaleOff, failCode: CodeTailscaleServe}
	defEnginesStop    = stepDef{name: StepEnginesStop, fn: (*Engine).stepEnginesStop, failCode: CodeStepFailed}
	defSurvivalModels = stepDef{name: StepSurvivalModels, fn: (*Engine).stepSurvivalModels, failCode: CodeNoModel}
	defSurvivalSvcs   = stepDef{name: StepSurvivalService, fn: (*Engine).stepSurvivalServices, failCode: CodeStepFailed}
	defTailscaleOn    = stepDef{name: StepTailscaleOn, fn: (*Engine).stepTailscaleOn, failCode: CodeTailscaleServe}
	defLinkStart      = stepDef{name: StepLinkStart, fn: (*Engine).stepLinkStart, failCode: CodeStepFailed}
	defProberStart    = stepDef{name: StepProberStart, fn: (*Engine).stepProberStart, failCode: CodeStepFailed}
	defPersist        = stepDef{name: StepPersist, fn: (*Engine).stepPersist, failCode: CodeStepFailed}
	defEmit           = stepDef{name: StepEmit, fn: (*Engine).stepEmit, failCode: CodeStepFailed, post: true}
	defSyncCatchup    = stepDef{name: StepSyncCatchup, fn: (*Engine).stepSyncCatchup, failCode: CodeStepFailed, post: true}
)

// stepsFor is the step list of a transition to target (agent.md section 4.2.1).
func stepsFor(target Mode) []stepDef {
	switch target {
	case Offline:
		return []stepDef{defLinkStop, defSyncCancel, defProberStop, defTailscaleOff, defPersist, defEmit}
	case Survival:
		return []stepDef{defLinkStop, defSyncCancel, defProberStop, defTailscaleOff, defEnginesStop, defSurvivalModels, defSurvivalSvcs, defPersist, defEmit}
	case Online:
		return []stepDef{defTailscaleOn, defLinkStart, defProberStart, defPersist, defEmit, defSyncCatchup}
	}
	return nil
}

// policySteps are the steady-state effects of a mode, applied when the agent starts with no
// transition recorded (agent.md section 4.2.4): no step replay, no persist.
func policySteps(m Mode) []stepDef {
	if m == Online {
		return []stepDef{defTailscaleOn, defLinkStart, defProberStart}
	}
	return []stepDef{defTailscaleOff}
}

func (r *run) steps() []stepDef {
	if r.policy {
		return policySteps(r.t.Target)
	}
	return stepsFor(r.t.Target)
}

// stepIndex returns the index of the recorded step among the recorded (pre-persist) steps of target,
// or 0 when the name is unknown or is a post-persist step (re-running from the start is idempotent).
func stepIndex(target Mode, step string) int {
	for i, s := range stepsFor(target) {
		if s.name == step && !s.post {
			return i
		}
	}
	return 0
}

func (e *Engine) stepLinkStop(ctx context.Context, _ *Transition) ([]Warning, error) {
	if e.d.Link == nil {
		return nil, nil
	}
	return nil, e.d.Link.Stop(ctx)
}

func (e *Engine) stepSyncCancel(ctx context.Context, _ *Transition) ([]Warning, error) {
	if e.d.Sync == nil {
		return nil, nil
	}
	return nil, e.d.Sync.CancelGraceful(ctx)
}

func (e *Engine) stepProberStop(_ context.Context, _ *Transition) ([]Warning, error) {
	if e.d.Prober != nil {
		e.d.Prober.Stop()
	}
	now := e.clock.Now().UTC()
	return nil, e.d.State.Mutate(func(s *state.State) error {
		if s.Connectivity.Value == state.ConnNotChecked {
			return nil
		}
		ts := now
		s.Connectivity.Value = state.ConnNotChecked
		s.Connectivity.Since = &ts
		s.Connectivity.NoneSince = nil
		return nil
	})
}

func (e *Engine) stepTailscaleOff(ctx context.Context, _ *Transition) ([]Warning, error) {
	ws, err := e.tailscaleOff(ctx)
	if err != nil || needsRetry(ws) {
		e.startOffRetry()
	} else {
		e.stopOffRetry()
	}
	return ws, err
}

// stepEnginesStop stops the music engine (every engine other than llama) so its graphics memory is
// free for the survival models. It never fails survival.
func (e *Engine) stepEnginesStop(ctx context.Context, _ *Transition) ([]Warning, error) {
	if s, ok := e.d.Models.(EngineStopper); ok && e.d.Models != nil {
		return s.StopEnginesForSurvival(ctx), nil
	}
	return nil, nil
}

func (e *Engine) stepSurvivalModels(ctx context.Context, t *Transition) ([]Warning, error) {
	var ws []Warning
	if len(t.SkipModels) > 0 {
		ws = append(ws, Warning{Code: CodeModelFallback, Message: "A model that was loading when the agent stopped is skipped: " + strings.Join(t.SkipModels, ", ") + "."})
	}
	if e.d.Models == nil {
		return ws, nil
	}
	lw, err := e.d.Models.LoadSurvivalSet(ctx, append([]string(nil), t.SkipModels...))
	return append(ws, lw...), err
}

// survivalServiceNames are the services survival mode starts: kiwix and whisper, plus the profile's
// survival.require_services (agent.md section 4.2.1), each once.
func (e *Engine) survivalServiceNames() []string {
	names := []string{"kiwix", "whisper"}
	if e.d.SurvivalServices != nil {
		names = append(names, e.d.SurvivalServices()...)
	}
	seen := map[string]bool{}
	var out []string
	for _, n := range names {
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	return out
}

func (e *Engine) stepSurvivalServices(ctx context.Context, _ *Transition) ([]Warning, error) {
	if e.d.Services == nil {
		return nil, nil
	}
	var ws []Warning
	for _, name := range e.survivalServiceNames() {
		svc, ok := services.ByName(name)
		if !ok {
			ws = append(ws, Warning{Code: CodeStepFailed, Message: fmt.Sprintf("The required service %q is unknown.", name)})
			continue
		}
		if _, err := e.d.Services.Do(ctx, svc, services.ActionStart); err != nil {
			code := services.NotRunningCode(name)
			if code == "" {
				code = CodeStepFailed
			}
			e.log.Warn("survival service did not start", "unit", svc.Unit, "code", code, "error", err.Error())
			ws = append(ws, Warning{Code: code, Message: fmt.Sprintf("The %s service did not start.", name)})
		}
	}
	return ws, nil
}

func (e *Engine) stepTailscaleOn(ctx context.Context, _ *Transition) ([]Warning, error) {
	// Going online ends the background removal retry; On must never race an Off.
	e.stopOffRetry()
	return e.tailscaleOn(ctx)
}

func (e *Engine) stepLinkStart(ctx context.Context, _ *Transition) ([]Warning, error) {
	if e.d.Link == nil {
		return nil, nil
	}
	return nil, e.d.Link.Start(ctx)
}

func (e *Engine) stepProberStart(_ context.Context, _ *Transition) ([]Warning, error) {
	if e.d.Prober != nil {
		e.d.Prober.Start()
	}
	return nil, nil
}

// stepPersist commits the mode: state.mode, mode_changed_at and mode_transition = null in one atomic
// write (agent.md section 4.2.1). When the store cannot write, the mode lives in memory and
// LAI-AGT-004 is warned (the store raises the alert itself).
func (e *Engine) stepPersist(_ context.Context, t *Transition) ([]Warning, error) {
	now := e.clock.Now().UTC().Truncate(time.Second)
	var prev Mode
	err := e.d.State.MutateSync(func(s *state.State) error {
		prev = s.Mode
		ts := now
		s.RecordModeChange(ts, t.Target)
		s.Mode = t.Target
		s.ModeChangedAt = &ts
		s.ModeTransition = nil
		if t.Target == Survival {
			s.PendingMode = nil
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	var ws []Warning
	if !e.d.State.Writable() {
		ws = append(ws, Warning{Code: CodeStateUnwritable, Message: "The state file cannot be written; the mode is kept in memory until it can."})
	}
	e.mode.Store(t.Target)
	if prev == Survival && t.Target != Survival && e.d.Portal != nil {
		e.d.Portal.DropAll()
	}
	e.notify(Change{From: prev, To: t.Target, OpID: t.OpID, At: now})
	return ws, nil
}

// stepEmit publishes mode.changed and audits mode.change. It runs after persist, so a crash between
// the two loses the event, never the mode: clients re-read /status on reconnect (resync).
func (e *Engine) stepEmit(_ context.Context, t *Transition) ([]Warning, error) {
	if e.d.Events != nil {
		ev := events.ModeChanged{From: string(t.From), To: string(t.Target), OpID: t.OpID, Warnings: append([]Warning{}, t.Warnings...)}
		if err := e.d.Events.Publish(events.TypeModeChanged, t.OpID, ev); err != nil {
			e.log.Warn("mode.changed not published", "error", err.Error())
		}
	}
	if e.d.Audit != nil {
		ev := audit.Event{
			Action: audit.ActionModeChange,
			Result: audit.ResultOK,
			Actor:  audit.Actor{Kind: audit.ActorKind(t.Actor.Kind), Via: t.Actor.Via},
			Target: map[string]string{"from": string(t.From), "to": string(t.Target)},
			OpID:   t.OpID,
		}
		if err := e.d.Audit.Write(ev); err != nil {
			// The record is already in the journal (the audit log mirrors to slog); the mode stands.
			e.log.Warn("audit write failed", "action", ev.Action, "error", err.Error())
		}
	}
	return nil, nil
}

// stepSyncCatchup schedules a sync in five minutes when the last success is more than 24 hours old
// (or there never was one). The sync engine owns the schedule and checks the mode when it fires.
func (e *Engine) stepSyncCatchup(_ context.Context, _ *Transition) ([]Warning, error) {
	if e.d.Sync == nil {
		return nil, nil
	}
	snap := e.d.State.Snapshot()
	var sync struct {
		LastSuccessAt *time.Time `json:"last_success_at"`
	}
	if len(snap.Sync) > 0 {
		_ = json.Unmarshal(snap.Sync, &sync) // a malformed sub-tree counts as "never"
	}
	if sync.LastSuccessAt == nil || e.clock.Now().Sub(*sync.LastSuccessAt) > e.catchupAt {
		e.d.Sync.ScheduleCatchup(e.catchupIn)
	}
	return nil, nil
}

// ---- Tailscale calls and the background retry --------------------------------------------------

// tsLock takes the one-call-at-a-time lock of the Tailscale controller.
func (e *Engine) tsLock(ctx context.Context) (func(), error) {
	select {
	case e.tsSlot <- struct{}{}:
		return func() { <-e.tsSlot }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (e *Engine) tailscaleOff(ctx context.Context) ([]Warning, error) {
	if e.d.Tailscale == nil {
		return nil, nil
	}
	unlock, err := e.tsLock(ctx)
	if err != nil {
		return nil, err
	}
	defer unlock()
	return e.d.Tailscale.Off(ctx)
}

func (e *Engine) tailscaleOn(ctx context.Context) ([]Warning, error) {
	if e.d.Tailscale == nil {
		return nil, nil
	}
	unlock, err := e.tsLock(ctx)
	if err != nil {
		return nil, err
	}
	defer unlock()
	return e.d.Tailscale.On(ctx)
}

// needsRetry reports whether a removal's warnings say the node may still be published.
func needsRetry(ws []Warning) bool {
	return hasCode(ws, CodeTailscaleServe) || hasCode(ws, CodeTailscaleStillServing)
}

// startOffRetry starts the background retry of tailscale_off (agent.md section 4.2.1): every 60 s
// while the node is offline or in survival, until a removal succeeds. One loop at a time.
func (e *Engine) startOffRetry() {
	e.retryMu.Lock()
	defer e.retryMu.Unlock()
	if e.retryCancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(e.rootCtx)
	done := make(chan struct{})
	e.retryCancel, e.retryDone = cancel, done
	go e.offRetryLoop(ctx, done)
}

// stopOffRetry cancels the retry loop and waits (bounded) for it to end, so a following On never
// overlaps an Off.
func (e *Engine) stopOffRetry() {
	e.retryMu.Lock()
	cancel, done := e.retryCancel, e.retryDone
	e.retryCancel, e.retryDone = nil, nil
	e.retryMu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		e.log.Warn("tailscale retry did not stop in time")
	}
}

func (e *Engine) offRetryLoop(ctx context.Context, done chan struct{}) {
	defer func() {
		e.retryMu.Lock()
		if e.retryDone == done {
			e.retryCancel, e.retryDone = nil, nil
		}
		e.retryMu.Unlock()
		close(done)
	}()
	tk := e.clock.NewTicker(e.retryEvery)
	defer tk.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tk.C():
		}
		if e.Online() {
			return
		}
		cctx, cancel := context.WithTimeout(ctx, e.timeouts[StepTailscaleOff])
		ws, err := e.tailscaleOff(cctx)
		cancel()
		if ctx.Err() != nil {
			return
		}
		if err == nil && !needsRetry(ws) {
			e.log.Info("tailscale listeners removed on retry")
			return
		}
		e.log.Warn("tailscale listeners still not removed", "code", CodeTailscaleServe)
	}
}

// ---- persisted form -----------------------------------------------------------------------------

const extraSkipModels = "skip_models"

// toState converts the working transition to its state.json form, keeping the unknown keys of the
// record already there for the same operation.
func toState(t Transition, old *state.ModeTransition) *state.ModeTransition {
	mt := &state.ModeTransition{
		OpID: t.OpID, From: t.From, Target: t.Target, Step: t.Step, StartedAt: t.StartedAt,
		Attempts: map[string]int{}, Warnings: []state.TransitionWarning{}, Actor: t.Actor,
	}
	for k, v := range t.Attempts {
		mt.Attempts[k] = v
	}
	for _, w := range t.Warnings {
		mt.Warnings = append(mt.Warnings, state.TransitionWarning{Code: w.Code, Message: w.Message})
	}
	if old != nil && old.OpID == t.OpID && len(old.Extra) > 0 {
		mt.Extra = make(map[string]json.RawMessage, len(old.Extra))
		for k, v := range old.Extra {
			mt.Extra[k] = v
		}
	}
	if len(t.SkipModels) > 0 {
		if b, err := json.Marshal(t.SkipModels); err == nil {
			if mt.Extra == nil {
				mt.Extra = map[string]json.RawMessage{}
			}
			mt.Extra[extraSkipModels] = b
		}
	}
	return mt
}

// fromState converts a persisted transition back to the working form.
func fromState(mt *state.ModeTransition) Transition {
	t := Transition{
		OpID: mt.OpID, From: mt.From, Target: mt.Target, Step: mt.Step, StartedAt: mt.StartedAt,
		Attempts: map[string]int{}, Actor: mt.Actor,
	}
	for k, v := range mt.Attempts {
		t.Attempts[k] = v
	}
	for _, w := range mt.Warnings {
		t.Warnings = append(t.Warnings, Warning{Code: w.Code, Message: w.Message})
	}
	if raw, ok := mt.Extra[extraSkipModels]; ok {
		_ = json.Unmarshal(raw, &t.SkipModels)
	}
	return t
}
