// Package modes is the node's mode engine (agent.md section 4.2, modes.md): the online, offline and
// survival state machine as ordered, idempotent, resumable steps.
//
// Doomsday-critical properties, each proven by a test:
//
//   - Before every step the engine writes mode_transition.step and the step's attempt counter with
//     state.MutateSync (fsync), so a kill -9 at any instant resumes at the recorded step
//     (Engine.Resume). A step that was started three times and never finished is skipped.
//   - persist sets the mode and clears mode_transition in one write.
//   - Survival never fails as a whole: every step error, timeout and panic becomes a warning, and a
//     survival request preempts any other transition after at most one step.
//   - One transition at a time; anything but survival asked meanwhile is refused (BusyError).
//
// Everything outside the engine (the cloud link, sync, the prober, Tailscale, the model loader,
// systemd) sits behind the interfaces of types.go with fakes in internal/modes/fake, so the engine is
// tested without a real unit, tailnet or model. The engine never imports internal/api; the routes
// live there and see the engine through a small interface.
package modes

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/state"
)

// Engine runs mode transitions. Create it with New, call Resume at startup step 13 and Open at the end
// of step 12 (AGENT-20), and Shutdown while stopping.
type Engine struct {
	d           Deps
	clock       config.Clock
	log         *slog.Logger
	newOpID     func() string
	maxAttempts int
	timeouts    map[string]time.Duration
	retryEvery  time.Duration
	catchupAt   time.Duration
	catchupIn   time.Duration
	suggestAt   time.Duration

	rootCtx context.Context
	cancel  context.CancelFunc

	mode atomic.Value // Mode: the committed mode, mirrored from state.mode

	// mu is modeMu of agent.md section 4.9: it guards the fields below and is never held across
	// I/O, a step or a call to a dependency. "One transition at a time" is running != nil.
	mu          sync.Mutex
	running     *run
	idle        chan struct{} // open while running != nil; closed when the engine becomes idle
	accepting   bool
	closing     bool
	queuedOp    string
	queuedActor state.Actor
	wg          sync.WaitGroup

	subMu   sync.Mutex
	subs    map[int]chan Change
	nextSub int

	tsSlot chan struct{} // capacity 1: serializes the TailscaleController calls

	retryMu     sync.Mutex
	retryCancel context.CancelFunc
	retryDone   chan struct{}
}

// run is one transition (or one steady-state policy application) in flight.
type run struct {
	t        Transition
	policy   bool // steady-state application: no mode_transition record, no events, no persist
	begun    bool // the initial mode_transition record is written
	startIdx int
	done     chan struct{}
	result   Result // set before done is closed

	preempt *run // guarded by Engine.mu: the survival run that replaces this one

	aborted string // "preempt" or "closing" when execute stopped early; set by the runner only
}

// New returns an engine over d. It reads the committed mode from the state store; it does not touch
// anything else until Resume, Open or Request.
func New(d Deps, o Options) (*Engine, error) {
	if d.State == nil {
		return nil, errors.New("modes: Deps.State is required")
	}
	e := &Engine{
		d:           d,
		clock:       o.Clock,
		log:         o.Logger,
		newOpID:     o.NewOpID,
		maxAttempts: o.MaxAttempts,
		retryEvery:  o.TailscaleRetry,
		catchupAt:   o.CatchupAfter,
		catchupIn:   o.CatchupIn,
		suggestAt:   o.SuggestSurvivalAfter,
		subs:        map[int]chan Change{},
		tsSlot:      make(chan struct{}, 1),
	}
	if e.clock == nil {
		e.clock = config.RealClock{}
	}
	if e.log == nil {
		e.log = slog.New(slog.DiscardHandler)
	}
	e.log = e.log.With("component", "modes")
	if e.newOpID == nil {
		e.newOpID = randomOpID
	}
	if e.maxAttempts <= 0 {
		e.maxAttempts = DefaultMaxAttempts
	}
	if e.retryEvery <= 0 {
		e.retryEvery = DefaultTailscaleRetry
	}
	if e.catchupAt <= 0 {
		e.catchupAt = DefaultCatchupAfter
	}
	if e.catchupIn <= 0 {
		e.catchupIn = DefaultCatchupIn
	}
	if e.suggestAt <= 0 {
		e.suggestAt = DefaultSuggestSurvivalAfter
	}
	e.timeouts = make(map[string]time.Duration, len(DefaultTimeouts))
	for k, v := range DefaultTimeouts {
		e.timeouts[k] = v
	}
	for k, v := range o.Timeouts {
		e.timeouts[k] = v
	}
	e.rootCtx, e.cancel = context.WithCancel(context.Background())
	m := d.State.Snapshot().Mode
	if !m.Valid() {
		m = Offline
	}
	e.mode.Store(m)
	return e, nil
}

// opAlphabet is the base32 alphabet of ids (agent.md section 2): a-z and 2-7.
const opAlphabet = "abcdefghijklmnopqrstuvwxyz234567"

// randomOpID returns "op_" plus 12 random base32 characters.
func randomOpID() string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	id := make([]byte, 0, 15)
	id = append(id, "op_"...)
	for _, c := range b {
		id = append(id, opAlphabet[c&31])
	}
	return string(id)
}

// Request asks for a transition to target.
//
//   - Target already current and no Reapply: Result.Already, nothing runs.
//   - Another transition running: a request for survival preempts it (it stops after its current
//     step and survival starts from the original mode) or joins a survival already running; any
//     other target is a *BusyError.
//   - The engine is not accepting (before Open, or shutting down): a survival request is recorded
//     as state.pending_mode and answered Queued; anything else is ErrNotAccepting.
//   - Otherwise the transition is recorded in state.json and started in the background. With Wait,
//     Request blocks until it ends or ctx ends; the transition goes on when ctx ends first.
//
// Request never uses ctx for the transition itself, so a client that disconnects cannot stop it.
func (e *Engine) Request(ctx context.Context, target Mode, o RequestOptions) (Result, error) {
	if !target.Valid() {
		return Result{}, ErrInvalidTarget
	}
	r, res, err := e.submit(target, o, "")
	if err != nil || r == nil || !o.Wait {
		return res, err
	}
	select {
	case <-r.done:
		return finalResult(res, r), nil
	case <-ctx.Done():
		return res, ctx.Err()
	}
}

func finalResult(res Result, r *run) Result {
	out := r.result
	out.Joined = res.Joined
	if out.OpID == "" {
		out.OpID = res.OpID
	}
	return out
}

// resultOf describes a run for Result. Caller holds Engine.mu: begin rewrites From and StartedAt.
func resultOf(r *run, joined bool) Result {
	return Result{OpID: r.t.OpID, From: r.t.From, Target: r.t.Target, Step: firstStep(r.t.Target), StartedAt: r.t.StartedAt, Joined: joined}
}

func firstStep(target Mode) string {
	if s := stepsFor(target); len(s) > 0 {
		return s[0].name
	}
	return ""
}

func (e *Engine) committed() Mode {
	m, _ := e.mode.Load().(Mode)
	return m
}

func (e *Engine) newRun(target Mode, o RequestOptions, from Mode, forceOp string) *run {
	id := forceOp
	if id == "" {
		id = e.newOpID()
	}
	actor := o.Actor
	if actor.Kind == "" {
		actor = state.Actor{Kind: "system"}
	}
	return &run{
		t: Transition{
			OpID: id, From: from, Target: target, StartedAt: e.clock.Now().UTC().Truncate(time.Second),
			Attempts: map[string]int{}, Actor: actor, Reapply: o.Reapply,
		},
		done: make(chan struct{}),
	}
}

// setRunningLocked makes r the running transition and counts its goroutine. Caller holds mu.
func (e *Engine) setRunningLocked(r *run) {
	if e.running == nil {
		e.idle = make(chan struct{})
	}
	e.running = r
	e.wg.Add(1)
}

// submit is Request without the wait. It returns the run to wait on (nil when the answer is final).
func (e *Engine) submit(target Mode, o RequestOptions, forceOp string) (*run, Result, error) {
	e.mu.Lock()
	if !e.accepting {
		e.mu.Unlock()
		if target != Survival {
			return nil, Result{}, ErrNotAccepting
		}
		return e.queueSurvival(o)
	}
	cur := e.running
	mode := e.committed()
	if cur != nil {
		if target != Survival {
			busy := &BusyError{OpID: cur.t.OpID, Target: cur.t.Target}
			e.mu.Unlock()
			return nil, Result{}, busy
		}
		switch {
		case cur.preempt != nil:
			pre := cur.preempt
			res := resultOf(pre, true)
			e.mu.Unlock()
			return pre, res, nil
		case cur.policy && mode == Survival && !o.Reapply:
			e.mu.Unlock()
			return nil, Result{Already: true, From: mode, Target: Survival}, nil
		case !cur.policy && cur.t.Target == Survival:
			res := resultOf(cur, true)
			e.mu.Unlock()
			return cur, res, nil
		}
		pre := e.newRun(Survival, o, mode, forceOp)
		cur.preempt = pre
		res := resultOf(pre, false)
		e.mu.Unlock()
		// The request is accepted: make it durable before answering, so a crash before the running
		// transition stops cannot lose it (Open drains pending_mode at the next start).
		e.setPending(true)
		return pre, res, nil
	}
	if mode == target && !o.Reapply {
		e.mu.Unlock()
		if target == Survival {
			e.setPending(false)
		}
		return nil, Result{Already: true, From: mode, Target: target}, nil
	}
	r := e.newRun(target, o, mode, forceOp)
	e.setRunningLocked(r)
	e.mu.Unlock()
	e.begin(r)
	go e.loop(r)
	e.mu.Lock()
	res := resultOf(r, false)
	e.mu.Unlock()
	return r, res, nil
}

// queueSurvival records a survival request received while the engine is not accepting (startup before
// step 12, shutdown). The op id is kept in memory so Open can run the transition under it.
func (e *Engine) queueSurvival(o RequestOptions) (*run, Result, error) {
	snap := e.d.State.Snapshot()
	if snap.Mode == Survival && snap.ModeTransition == nil && !o.Reapply {
		return nil, Result{Already: true, From: Survival, Target: Survival}, nil
	}
	e.mu.Lock()
	if e.queuedOp == "" {
		e.queuedOp = e.newOpID()
		e.queuedActor = o.Actor
	}
	id := e.queuedOp
	e.mu.Unlock()
	e.setPending(true)
	return nil, Result{OpID: id, Target: Survival, Queued: true}, nil
}

// setPending writes or clears state.pending_mode.
func (e *Engine) setPending(on bool) {
	err := e.d.State.MutateSync(func(s *state.State) error {
		if on {
			m := Survival
			s.PendingMode = &m
		} else {
			s.PendingMode = nil
		}
		return nil
	})
	if err != nil {
		e.log.Error("pending_mode not written", "error", err.Error())
	}
}

// begin writes the initial mode_transition record of a run (durable before Request answers 202).
func (e *Engine) begin(r *run) {
	if r.policy || r.begun {
		return
	}
	r.begun = true
	from := e.d.State.Snapshot().Mode
	if !from.Valid() {
		from = Offline
	}
	e.mu.Lock()
	r.t.From = from
	r.t.StartedAt = e.clock.Now().UTC().Truncate(time.Second)
	e.mu.Unlock()
	e.record(r, firstStep(r.t.Target), false)
}

// record writes the transition as it stands, with step as the current step and, when count is set,
// that step's attempt counter incremented. It is the "before step k" write of agent.md section 4.2.2.
func (e *Engine) record(r *run, step string, count bool) {
	t := &r.t
	t.Step = step
	if count {
		t.Attempts[step]++
	}
	snap := t.clone()
	err := e.d.State.MutateSync(func(s *state.State) error {
		s.ModeTransition = toState(snap, s.ModeTransition)
		if snap.Target == Survival {
			s.PendingMode = nil
		}
		return nil
	})
	if err != nil {
		e.log.Error("mode_transition not written", "step", step, "error", err.Error())
	}
}

// loop runs r and then any survival run that preempted it, in one goroutine, so there is never more
// than one runner.
func (e *Engine) loop(r *run) {
	defer e.wg.Done()
	for r != nil {
		e.begin(r)
		e.execute(r)
		r = e.finish(r)
	}
}

// finish ends r: it publishes the result to waiters and hands the engine to the survival run that
// preempted r, if any (not while shutting down: its pending_mode is already on disk).
func (e *Engine) finish(r *run) *run {
	e.mu.Lock()
	next := r.preempt
	if e.closing {
		next = nil
	}
	if r.aborted == "preempt" && next != nil {
		r.result.Preempted = true
		r.result.PreemptedBy = next.t.OpID
	}
	if next != nil {
		e.running = next // the goroutine (and its wg count) carries over
	} else {
		e.running = nil
		close(e.idle)
	}
	e.mu.Unlock()
	close(r.done)
	return next
}

// stopReason says why r must not start another step, or "".
func (e *Engine) stopReason(r *run) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	switch {
	case e.closing || e.rootCtx.Err() != nil:
		return "closing"
	case r.preempt != nil:
		return "preempt"
	}
	return ""
}

// execute runs the steps of r from r.startIdx.
func (e *Engine) execute(r *run) {
	steps := r.steps()
	for i := r.startIdx; i < len(steps); i++ {
		s := steps[i]
		if !s.post {
			if why := e.stopReason(r); why != "" {
				r.aborted = why
				r.result = e.abortedResult(r)
				return
			}
			if !r.policy {
				// A step that started maxAttempts times and never finished is skipped on resume,
				// so a step that kills the agent cannot cause a boot loop (agent.md section 4.2.4).
				if r.t.Resumed && s.name != StepPersist && r.t.Attempts[s.name] >= e.maxAttempts {
					e.addWarnings(r, Warning{Code: CodeStepFailed, Message: fmt.Sprintf("Step %s was skipped after %d tries.", s.name, e.maxAttempts)})
					e.record(r, s.name, false)
					e.publishStep(r, s.name, "failed", CodeStepFailed)
					continue
				}
				e.record(r, s.name, true)
			}
		}
		if e.runStep(r, s) {
			r.aborted = "closing"
			r.result = e.abortedResult(r)
			return
		}
	}
	if r.policy {
		for _, w := range r.t.Warnings {
			e.log.Warn("mode policy applied with a warning", "mode", string(r.t.Target), "code", w.Code)
		}
	}
	r.result = Result{
		OpID: r.t.OpID, From: r.t.From, Target: r.t.Target, Done: true,
		Warnings: append([]Warning{}, r.t.Warnings...),
	}
}

func (e *Engine) abortedResult(r *run) Result {
	res := Result{OpID: r.t.OpID, From: r.t.From, Target: r.t.Target, Warnings: append([]Warning{}, r.t.Warnings...)}
	if r.aborted == "closing" {
		res.Interrupted = true
	}
	return res
}

// outcome of calling a step function.
type outcome int

const (
	outOK outcome = iota
	outError
	outPanic
	outTimeout
	outAborted
)

// runStep runs one step and records what it returned. It reports true when the engine is shutting
// down and the step was abandoned (nothing is recorded: the step stays recorded for resume).
func (e *Engine) runStep(r *run, s stepDef) bool {
	e.publishStep(r, s.name, "started", "")
	tc := r.t.clone()
	ws, out, err := e.callStep(s, &tc)
	if out == outAborted {
		return true
	}
	e.addWarnings(r, ws...)
	if out == outOK {
		e.publishStep(r, s.name, "done", "")
		return false
	}
	reason := map[outcome]string{outError: "failed", outPanic: "panicked", outTimeout: "timed out"}[out]
	attrs := []any{"step", s.name, "target", string(r.t.Target), "op_id", r.t.OpID, "code", s.failCode}
	if err != nil {
		attrs = append(attrs, "error", err.Error())
	}
	e.log.Warn("mode step "+reason, attrs...)
	if !hasCode(ws, s.failCode) {
		e.addWarnings(r, Warning{Code: s.failCode, Message: s.failMessage(reason)})
	}
	e.publishStep(r, s.name, "failed", s.failCode)
	return false
}

type stepResult struct {
	ws       []Warning
	err      error
	panicked bool
}

// safeCall runs fn and turns a panic into a result.
func (e *Engine) safeCall(ctx context.Context, s stepDef, t *Transition) (res stepResult) {
	defer func() {
		if p := recover(); p != nil {
			res = stepResult{panicked: true, err: fmt.Errorf("panic: %v", p)}
		}
	}()
	ws, err := s.fn(e, ctx, t)
	return stepResult{ws: ws, err: err}
}

// callStep runs the step function under its timeout. A step without a timeout (persist, emit,
// sync_catchup) runs inline. A step that ignores its context and outlives the timeout is abandoned,
// not waited for: the transition goes on without it.
func (e *Engine) callStep(s stepDef, t *Transition) ([]Warning, outcome, error) {
	timeout := e.timeouts[s.name]
	if timeout <= 0 {
		res := e.safeCall(e.rootCtx, s, t)
		return classify(res)
	}
	ctx, cancel := context.WithCancel(e.rootCtx)
	defer cancel()
	ch := make(chan stepResult, 1)
	go func() { ch <- e.safeCall(ctx, s, t) }()
	tk := e.clock.NewTicker(timeout)
	defer tk.Stop()
	select {
	case res := <-ch:
		return classify(res)
	case <-tk.C():
		cancel()
		return nil, outTimeout, context.DeadlineExceeded
	case <-e.rootCtx.Done():
		return nil, outAborted, e.rootCtx.Err()
	}
}

func classify(res stepResult) ([]Warning, outcome, error) {
	switch {
	case res.panicked:
		return res.ws, outPanic, res.err
	case res.err != nil:
		return res.ws, outError, res.err
	}
	return res.ws, outOK, nil
}

func hasCode(ws []Warning, code string) bool {
	for _, w := range ws {
		if w.Code == code {
			return true
		}
	}
	return false
}

// addWarnings appends to the run's warnings, dropping exact duplicates.
func (e *Engine) addWarnings(r *run, ws ...Warning) {
next:
	for _, w := range ws {
		for _, have := range r.t.Warnings {
			if have == w {
				continue next
			}
		}
		r.t.Warnings = append(r.t.Warnings, w)
	}
}

// publishStep publishes mode.step (not for steady-state policy runs, which are not mode changes).
func (e *Engine) publishStep(r *run, step, st, code string) {
	if r.policy || e.d.Events == nil {
		return
	}
	data := eventsModeStep(string(r.t.Target), step, st, code)
	if err := e.d.Events.Publish(typeModeStep, r.t.OpID, data); err != nil {
		e.log.Warn("mode.step not published", "error", err.Error())
	}
}

// Shutdown stops accepting requests (a survival request is still queued as pending_mode) and lets the
// running transition finish its current step, waiting until ctx ends; it then abandons the step. The
// recorded step stays in state.json, so the transition resumes at the next start (agent.md section
// 4.12 step 4). It also stops the Tailscale retry. The engine cannot be reused afterwards.
func (e *Engine) Shutdown(ctx context.Context) error {
	e.mu.Lock()
	e.accepting = false
	e.closing = true
	e.mu.Unlock()
	err := e.WaitIdle(ctx)
	e.cancel()
	e.stopOffRetry()
	done := make(chan struct{})
	go func() { e.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
	}
	return err
}

// WaitIdle blocks until no transition is running, or ctx ends.
func (e *Engine) WaitIdle(ctx context.Context) error {
	for {
		e.mu.Lock()
		busy, ch := e.running != nil, e.idle
		e.mu.Unlock()
		if !busy {
			return nil
		}
		select {
		case <-ch:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}
