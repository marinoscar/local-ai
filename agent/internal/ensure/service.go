package ensure

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/audit"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
)

// Errors a caller maps to a response.
var (
	// ErrSyncRunning: a sync, verify or import run holds the run slot, so the kit may be changing.
	ErrSyncRunning = errors.New("ensure: a sync run is active")
	// ErrModeChanging: a mode change is in flight.
	ErrModeChanging = errors.New("ensure: a mode change is in flight")
)

// ConfirmError is returned by Start for a confirmation that names no step waiting for one.
type ConfirmError struct {
	ID     string
	Reason string // unknown_step | not_waiting
}

func (e *ConfirmError) Error() string {
	return fmt.Sprintf("ensure: cannot confirm %q: %s", e.ID, e.Reason)
}

// Deps are what the Service needs.
type Deps struct {
	// Settings is the ensure block of node.yaml now.
	Settings func() Settings
	// Wants is what the profile and node.yaml want installed now.
	Wants    func(ctx context.Context) (Wants, error)
	Planner  *Planner
	Executor *Executor
	Journal  *Journal
	// Gate returns nil when a pass may start (ErrSyncRunning, ErrModeChanging or another error otherwise).
	// nil means always.
	Gate func() error
	// Alert raises or clears a doctor alert (LAI-KIT-051, LAI-KIT-052).
	Alert func(code string, raise bool, details map[string]any)
	Audit Auditor
	Clock Clock
	Log   *slog.Logger
	// NewRunID makes the id of a pass ("ens-" plus a time and random part).
	NewRunID func() string
	// PlanTTL is how long an idle plan is reused by GET (default 20 s).
	PlanTTL time.Duration
	// Retry is how long a trigger waits when the gate is closed, before it asks again (default 30 s) and
	// MaxRetries how many times (default 20).
	Retry      time.Duration
	MaxRetries int
}

// Service is the engine: the one pass at a time, its triggers and the picture the routes show.
type Service struct {
	d Deps

	mu       sync.Mutex
	active   *activeRun
	cached   *Plan
	cachedAt time.Time
	// scheduled is true while a trigger is waiting to start a pass.
	scheduled bool
	rootCtx   context.Context
	wg        sync.WaitGroup

	trig chan triggerReq
}

type triggerReq struct {
	t     Trigger
	after time.Duration
}

type activeRun struct {
	run      Run
	steps    []Step
	selected []string
	ctx      context.Context
	cancel   context.CancelFunc
	done     chan struct{}
}

// New returns a Service. Run must be called for the triggers to work; Start and View work without it.
func New(d Deps) *Service {
	if d.PlanTTL <= 0 {
		d.PlanTTL = 20 * time.Second
	}
	if d.Retry <= 0 {
		d.Retry = 30 * time.Second
	}
	if d.MaxRetries <= 0 {
		d.MaxRetries = 20
	}
	if d.Log == nil {
		d.Log = slog.Default()
	}
	if d.NewRunID == nil {
		d.NewRunID = func() string { return fmt.Sprintf("ens-%d", d.Clock.Now().UnixNano()) }
	}
	return &Service{d: d, trig: make(chan triggerReq, 16), rootCtx: context.Background()}
}

func (s *Service) log() *slog.Logger { return s.d.Log.With("component", "ensure") }

// ---- the picture --------------------------------------------------------------------------------

func (s *Service) plan(ctx context.Context, fresh bool) (Plan, error) {
	s.mu.Lock()
	if !fresh && s.cached != nil && s.d.Clock.Now().Sub(s.cachedAt) < s.d.PlanTTL {
		p := *s.cached
		s.mu.Unlock()
		return p, nil
	}
	s.mu.Unlock()
	w, err := s.d.Wants(ctx)
	if err != nil {
		return Plan{}, err
	}
	p, err := s.d.Planner.Plan(ctx, PlanInput{Auto: s.d.Settings().Auto, Wants: w, PendingRestart: s.d.Journal.PendingRestart()})
	if err != nil {
		return p, err
	}
	s.mu.Lock()
	s.cached, s.cachedAt = &p, s.d.Clock.Now()
	s.mu.Unlock()
	return p, nil
}

func (s *Service) invalidate() {
	s.mu.Lock()
	s.cached = nil
	s.mu.Unlock()
}

// View is the picture of the node now, in the language asked for.
func (s *Service) View(ctx context.Context, lang string) View {
	s.mu.Lock()
	if a := s.active; a != nil {
		v := s.liveView(a, lang)
		s.mu.Unlock()
		return v
	}
	scheduled := s.scheduled
	s.mu.Unlock()
	return s.idleView(ctx, lang, scheduled)
}

func (s *Service) liveView(a *activeRun, lang string) View {
	steps := append([]Step(nil), a.steps...)
	p := Progress{Total: len(a.selected)}
	sel := map[string]bool{}
	for _, id := range a.selected {
		sel[id] = true
	}
	for _, st := range steps {
		if !sel[st.ID] {
			continue
		}
		switch st.Status {
		case StatusDone, StatusFailed, StatusCancelled, StatusOK:
			p.Done++
		case StatusRunning:
			p.Current = st.ID
		}
	}
	run := a.run
	return s.compose(StateInstalling, steps, p, &run, false, lang)
}

func (s *Service) idleView(ctx context.Context, lang string, scheduled bool) View {
	auto := s.d.Settings().Auto
	plan, err := s.plan(ctx, false)
	var last *Run
	var lastSteps []Step
	if r, st, ok := s.d.Journal.Last(); ok {
		last, lastSteps = &r, st
	}
	if err != nil {
		s.log().Warn("could not work out what is missing", "error", err.Error())
		v := View{State: StateNeedsYou, Auto: auto, Steps: []ViewStep{}, Run: last, CheckedAt: s.d.Clock.Now().UTC(),
			Message:    pick(lang, "Needs you: I could not check what this computer is missing.", "Necesita su ayuda: no pude comprobar qué le falta a esta computadora."),
			NextAction: NextAction{Kind: NextFix, Text: pick(lang, "Run `aictl doctor` and read the first problem.", "Ejecute `aictl doctor` y lea el primer problema.")}}
		return v
	}
	steps := overlayFailures(plan.Steps, last, lastSteps)
	state := stateOf(steps, auto, scheduled)
	return s.compose(state, steps, Progress{}, last, false, lang)
}

// overlayFailures shows a step that failed in the latest pass, and still needs work, as failed (with its
// code and sentence) until the next pass tries again.
func overlayFailures(plan []Step, last *Run, lastSteps []Step) []Step {
	out := append([]Step(nil), plan...)
	if last == nil || last.FinishedAt == nil {
		return out
	}
	failed := map[string]Step{}
	for _, st := range lastSteps {
		if st.Status == StatusFailed {
			failed[st.ID] = st
		}
	}
	for i, st := range out {
		f, ok := failed[st.ID]
		if !ok || (st.Status != StatusPending && st.Status != StatusWaiting && st.Status != StatusNeedsConfirm) {
			continue
		}
		out[i].Status, out[i].Code, out[i].Error = StatusFailed, f.Code, f.Error
		out[i].StartedAt, out[i].FinishedAt = f.StartedAt, f.FinishedAt
	}
	return out
}

// stateOf is the one word for an idle node.
func stateOf(steps []Step, auto Auto, scheduled bool) State {
	work, waiting, pending := 0, 0, 0
	for _, st := range steps {
		switch st.Status {
		case StatusOK, StatusDone:
			continue
		case StatusWaiting:
			waiting++
		case StatusPending:
			pending++
		}
		work++
	}
	switch {
	case work == 0:
		return StateComplete
	case auto == AutoOff && waiting > 0:
		return StateOff
	case pending > 0 && scheduled:
		return StateInstalling
	}
	return StateNeedsYou
}

func (s *Service) compose(state State, steps []Step, p Progress, run *Run, dry bool, lang string) View {
	v := View{State: state, Auto: s.d.Settings().Auto, Progress: p, Run: run, DryRun: dry, CheckedAt: s.d.Clock.Now().UTC(), Steps: make([]ViewStep, 0, len(steps))}
	if state != StateInstalling {
		p.Total = countWork(steps)
		v.Progress = p
	}
	for _, st := range steps {
		v.Steps = append(v.Steps, ViewStep{Step: st, Title: Title(st.ID, lang), Detail: detailOf(st, lang)})
	}
	v.Message = messageFor(state, steps, v.Progress, lang)
	if state == StateInstalling && p.Total == 0 {
		v.Message = pick(lang, "Installing what's missing", "Instalando lo que falta")
	}
	v.NextAction = nextFor(state, steps, lang)
	return v
}

// ---- starting a pass ----------------------------------------------------------------------------

// StartRequest is POST /ensure after the route decoded it.
type StartRequest struct {
	DryRun bool
	// Confirm names the steps the caller confirms (only steps that wait for it).
	Confirm []string
	Actor   audit.Actor
	Trigger Trigger
	Lang    string
}

// Start begins a pass now. It returns at once; the pass runs in the background. started is false for a dry
// run, when there is nothing to do, and when a pass is already running (the running pass's picture is
// returned: asking twice is not an error).
func (s *Service) Start(ctx context.Context, req StartRequest) (v View, started bool, err error) {
	if req.Trigger == "" {
		req.Trigger = TriggerManual
	}
	s.mu.Lock()
	if a := s.active; a != nil {
		v := s.liveView(a, req.Lang)
		s.mu.Unlock()
		return v, false, nil
	}
	s.mu.Unlock()

	plan, err := s.plan(ctx, true)
	if err != nil {
		return View{}, false, err
	}
	confirm := map[string]bool{}
	for _, id := range req.Confirm {
		st, ok := plan.Step(id)
		switch {
		case !KnownStep(id):
			return View{}, false, &ConfirmError{ID: id, Reason: "unknown_step"}
		case !ok || st.Status != StatusNeedsConfirm:
			return View{}, false, &ConfirmError{ID: id, Reason: "not_waiting"}
		}
		confirm[id] = true
	}
	selected := selectSteps(plan.Steps, s.d.Settings().Auto, true, confirm)
	if req.DryRun {
		steps := markPlanned(plan.Steps, selected)
		return s.compose(stateOf(steps, s.d.Settings().Auto, false), steps, Progress{Total: len(selected)}, nil, true, req.Lang), false, nil
	}
	if len(selected) == 0 {
		return s.idleView(ctx, req.Lang, false), false, nil
	}
	if s.d.Gate != nil {
		if err := s.d.Gate(); err != nil {
			return View{}, false, err
		}
	}
	a, err := s.begin(plan, selected, req.Trigger)
	if err != nil {
		return View{}, false, err
	}
	if a == nil { // another caller won the race
		return s.View(ctx, req.Lang), false, nil
	}
	s.wg.Add(1)
	go s.pass(a, req.Actor, confirm)
	s.mu.Lock()
	v = s.liveView(a, req.Lang)
	s.mu.Unlock()
	return v, true, nil
}

// markPlanned shows, for a dry run, which steps a real run would do now.
func markPlanned(steps []Step, selected []string) []Step {
	out := append([]Step(nil), steps...)
	sel := map[string]bool{}
	for _, id := range selected {
		sel[id] = true
	}
	for i := range out {
		if sel[out[i].ID] && out[i].Status == StatusWaiting {
			out[i].Status = StatusPending
		}
	}
	return out
}

// selectSteps picks the steps a pass runs, in plan order. An automatic pass (manual false) runs the pending
// ones when ensure.auto is on. A manual pass also runs the waiting ones. A step that needs a confirmation
// runs only when named in confirm, and only in a manual pass. A blocked step never runs.
func selectSteps(steps []Step, auto Auto, manual bool, confirm map[string]bool) []string {
	var ids []string
	for _, st := range steps {
		switch st.Status {
		case StatusPending:
			if auto == AutoOn || manual {
				ids = append(ids, st.ID)
			}
		case StatusWaiting:
			if manual {
				ids = append(ids, st.ID)
			}
		case StatusNeedsConfirm:
			if manual && confirm[st.ID] {
				ids = append(ids, st.ID)
			}
		}
	}
	return ids
}

func (s *Service) begin(plan Plan, selected []string, t Trigger) (*activeRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active != nil {
		return nil, nil
	}
	run := Run{ID: s.d.NewRunID(), Trigger: t, StartedAt: s.d.Clock.Now().UTC()}
	steps := append([]Step(nil), plan.Steps...)
	sel := map[string]bool{}
	for _, id := range selected {
		sel[id] = true
	}
	for i := range steps {
		if sel[steps[i].ID] && steps[i].Status == StatusWaiting {
			steps[i].Status = StatusPending
		}
	}
	if err := s.d.Journal.Begin(run, steps); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(s.rootCtx)
	a := &activeRun{run: run, steps: steps, selected: selected, ctx: ctx, cancel: cancel, done: make(chan struct{})}
	s.active = a
	s.scheduled = false
	return a, nil
}

// ---- the pass -----------------------------------------------------------------------------------

func (s *Service) pass(a *activeRun, actor audit.Actor, confirm map[string]bool) {
	defer s.wg.Done()
	ctx := a.ctx
	defer close(a.done)
	defer a.cancel()

	setStep := func(st Step) {
		s.mu.Lock()
		for i := range a.steps {
			if a.steps[i].ID == st.ID {
				a.steps[i] = st
			}
		}
		s.mu.Unlock()
		if err := s.d.Journal.Step(st); err != nil {
			s.log().Warn("the journal could not be written", "error", err.Error())
		}
	}
	var done, failed, cancelled int
	for _, id := range a.selected {
		var step Step
		s.mu.Lock()
		for _, st := range a.steps {
			if st.ID == id {
				step = st
			}
		}
		s.mu.Unlock()
		if ctx.Err() != nil {
			step.Status = StatusCancelled
			setStep(step)
			cancelled++
			continue
		}
		if step.Status == StatusNeedsConfirm {
			step.NeedsConfirmation = true // the allowlist row decides; the flag stays true
		}
		running := step
		running.Status = StatusRunning
		now := s.d.Clock.Now().UTC()
		running.StartedAt = &now
		setStep(running)
		res := s.d.Executor.Run(ctx, step, actor, a.run.ID)
		setStep(res.Step)
		switch res.Step.Status {
		case StatusDone:
			done++
			s.afterStep(res.Step)
		case StatusCancelled:
			cancelled++
		default:
			failed++
		}
	}
	result := ResultOK
	switch {
	case cancelled > 0 && ctx.Err() != nil:
		result = ResultCancelled
	case failed > 0 && done > 0:
		result = ResultPartial
	case failed > 0:
		result = ResultFailed
	}
	finished := s.d.Clock.Now().UTC()
	if err := s.d.Journal.Finish(result, finished); err != nil {
		s.log().Warn("the journal could not be closed", "error", err.Error())
	}
	s.auditRun(a, actor, result, done, failed)
	s.mu.Lock()
	a.run.FinishedAt, a.run.Result = &finished, result
	s.active = nil
	s.cached = nil
	s.mu.Unlock()
	s.log().Info("the install pass ended", "run", a.run.ID, "trigger", string(a.run.Trigger), "result", string(result), "done", done, "failed", failed)
	s.refreshAlerts(context.Background())
}

// afterStep remembers what a finished step leaves to do: a replaced kiwix-tools is not used by the running
// library service until it restarts, which only a confirmation does.
func (s *Service) afterStep(st Step) {
	switch st.ID {
	case IDKiwix:
		if st.From != "" || st.Version != "" {
			_ = s.d.Journal.SetPendingRestart("kiwix", true)
		}
	case IDRestartKiwix:
		_ = s.d.Journal.SetPendingRestart("kiwix", false)
	}
}

func (s *Service) auditRun(a *activeRun, actor audit.Actor, result Result, done, failed int) {
	if s.d.Audit == nil {
		return
	}
	res := audit.ResultOK
	code := ""
	if result == ResultFailed || result == ResultPartial {
		res, code = audit.ResultFail, doctor.KIT051
	}
	ev := audit.Event{Action: ActionRun, Result: res, Code: code, Actor: actor, Target: map[string]string{
		"run_id": a.run.ID, "trigger": string(a.run.Trigger), "result": string(result),
		"done": fmt.Sprint(done), "failed": fmt.Sprint(failed),
	}}
	if err := s.d.Audit.Write(ev); err != nil {
		s.log().Warn("audit write failed", "action", ActionRun, "error", err.Error())
	}
}

// refreshAlerts raises or clears the two doctor alerts from the latest pass and a fresh plan.
func (s *Service) refreshAlerts(ctx context.Context) {
	if s.d.Alert == nil {
		return
	}
	plan, err := s.plan(ctx, true)
	if err != nil {
		return
	}
	var last *Run
	var lastSteps []Step
	if r, st, ok := s.d.Journal.Last(); ok {
		last, lastSteps = &r, st
	}
	steps := overlayFailures(plan.Steps, last, lastSteps)
	var failed, needs []string
	auto := s.d.Settings().Auto
	for _, st := range steps {
		switch st.Status {
		case StatusFailed:
			failed = append(failed, st.ID)
		case StatusNeedsConfirm:
			needs = append(needs, st.ID)
		case StatusWaiting:
			needs = append(needs, st.ID)
		case StatusPending:
			// A pending step of an automatic node is about to run: not yet a reason to ask a person. A
			// blocked step has its own codes (LAI-KIT-040, LAI-KIT-001).
			if auto != AutoOn {
				needs = append(needs, st.ID)
			}
		}
	}
	s.d.Alert(doctor.KIT051, len(failed) > 0, map[string]any{"steps": failed})
	s.d.Alert(doctor.KIT052, len(needs) > 0 && len(failed) == 0, map[string]any{"steps": needs})
}

// ---- cancelling ---------------------------------------------------------------------------------

// Cancel stops the running pass: the step that runs gets a cancelled context (the program it runs is
// killed), the rest are not started. With no pass running it changes nothing. It returns the picture.
func (s *Service) Cancel(ctx context.Context, lang string) View {
	s.mu.Lock()
	a := s.active
	s.mu.Unlock()
	if a != nil {
		a.cancel()
		select {
		case <-a.done:
		case <-time.After(5 * time.Second):
		case <-ctx.Done():
		}
	}
	return s.View(ctx, lang)
}

// ---- triggers -----------------------------------------------------------------------------------

// Trigger asks for an automatic pass after a delay (a sync run that finished, the start of the agent,
// mode_online). Asks that arrive while one is waiting are merged into the earlier one.
func (s *Service) Trigger(t Trigger, after time.Duration) {
	s.mu.Lock()
	s.scheduled = true
	s.cached = nil
	s.mu.Unlock()
	select {
	case s.trig <- triggerReq{t: t, after: after}:
	default: // the channel is full of asks already: they will all be served by one pass
	}
}

// Run serves the triggers until ctx ends, then cancels the running pass and waits for it.
func (s *Service) Run(ctx context.Context) {
	s.mu.Lock()
	s.rootCtx = ctx
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		a := s.active
		s.mu.Unlock()
		if a != nil {
			a.cancel()
		}
		s.wg.Wait()
	}()
	var timer *time.Timer
	var due <-chan time.Time
	var pending Trigger
	retries := 0
	for {
		select {
		case <-ctx.Done():
			if timer != nil {
				timer.Stop()
			}
			return
		case r := <-s.trig:
			if pending == "" || r.t == TriggerSync {
				pending = r.t
			}
			if timer == nil {
				timer = time.NewTimer(r.after)
			} else if due == nil {
				timer.Reset(r.after)
			}
			due = timer.C
		case <-due:
			due = nil
			t := pending
			ok, wait := s.fire(ctx, t)
			if !ok && retries < s.d.MaxRetries {
				retries++
				timer.Reset(wait)
				due = timer.C
				continue
			}
			retries, pending = 0, ""
			s.mu.Lock()
			s.scheduled = false
			s.mu.Unlock()
		}
	}
}

// fire is one automatic pass. ok false means the gate was closed (a sync run, a mode change): ask again
// after wait.
func (s *Service) fire(ctx context.Context, t Trigger) (ok bool, wait time.Duration) {
	if s.d.Gate != nil {
		if err := s.d.Gate(); err != nil {
			s.log().Info("the install pass waits", "reason", err.Error())
			return false, s.d.Retry
		}
	}
	plan, err := s.plan(ctx, true)
	if err != nil {
		s.log().Warn("the install pass could not plan", "error", err.Error())
		return true, 0
	}
	selected := selectSteps(plan.Steps, s.d.Settings().Auto, false, nil)
	if len(selected) == 0 {
		s.refreshAlerts(ctx)
		return true, 0
	}
	a, err := s.begin(plan, selected, t)
	if err != nil || a == nil {
		if err != nil {
			s.log().Warn("the install pass could not start", "error", err.Error())
		}
		return true, 0
	}
	s.wg.Add(1)
	go s.pass(a, audit.System(), nil)
	return true, 0
}

// Wait blocks until the running pass (if any) has ended; tests use it.
func (s *Service) Wait() {
	s.mu.Lock()
	a := s.active
	s.mu.Unlock()
	if a != nil {
		<-a.done
	}
}

// Refresh recomputes the plan and the doctor alerts (after a settings change or at start).
func (s *Service) Refresh(ctx context.Context) {
	s.invalidate()
	s.refreshAlerts(ctx)
}
