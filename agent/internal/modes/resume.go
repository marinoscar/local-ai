package modes

import (
	"context"

	"github.com/marinoscar/local-ai/agent/internal/state"
)

// Open starts accepting requests; AGENT-20 calls it when startup step 12 ends (agent.md section
// 4.1 step 12, the end of the mutating-route gate). A survival request queued before that, in this
// process (with its op id) or by a previous one (state.pending_mode), starts now: it runs under the
// op id it was answered with when that is known, and preempts whatever step 13 started. Open does
// not wait for the transition.
func (e *Engine) Open() (Result, error) {
	e.mu.Lock()
	if e.closing {
		e.mu.Unlock()
		return Result{}, ErrClosed
	}
	e.accepting = true
	op, actor := e.queuedOp, e.queuedActor
	e.queuedOp, e.queuedActor = "", state.Actor{}
	e.mu.Unlock()

	pending := e.d.State.Snapshot().PendingMode
	if pending == nil && op == "" {
		return Result{}, nil
	}
	if pending != nil && *pending != Survival {
		e.setPending(false) // only survival can be queued; anything else is stale
		if op == "" {
			return Result{}, nil
		}
	}
	if actor.Kind == "" {
		actor = state.Actor{Kind: "system", Via: "pending_mode"}
	}
	_, res, err := e.submit(Survival, RequestOptions{Actor: actor}, op)
	return res, err
}

// Resume is startup step 13 (agent.md section 4.2.4). It runs in the background and returns when the
// work has started (use WaitIdle to wait for it).
//
//   - mode_transition recorded: log LAI-AGT-007 and continue the transition at the recorded step.
//     A step that has started three times is skipped with LAI-AGT-008. For survival, the model whose
//     load was in progress when the agent died (state.models.last_load) is skipped.
//   - none: apply the steady-state policy of state.mode, as a run like any other (so a request
//     during it is refused or preempts it): online reconciles Tailscale, the link and the prober;
//     offline and survival only remove our Tailscale listeners.
//
// A call while something already runs does nothing and returns that operation.
func (e *Engine) Resume(ctx context.Context) (Result, error) {
	snap := e.d.State.Snapshot()

	e.mu.Lock()
	if e.closing {
		e.mu.Unlock()
		return Result{}, ErrClosed
	}
	if cur := e.running; cur != nil {
		res := Result{OpID: cur.t.OpID, From: cur.t.From, Target: cur.t.Target, Joined: true}
		e.mu.Unlock()
		return res, nil
	}

	var r *run
	if mt := snap.ModeTransition; mt != nil && mt.Target.Valid() {
		t := fromState(mt)
		if !t.From.Valid() {
			t.From = snap.Mode
		}
		t.Resumed = true
		if t.Target == Survival {
			if ll := snap.Models.LastLoad; ll != nil && ll.Result == state.LoadInProgress && ll.ID != "" && !containsString(t.SkipModels, ll.ID) {
				t.SkipModels = append(t.SkipModels, ll.ID)
			}
		}
		r = &run{t: t, begun: true, startIdx: stepIndex(t.Target, t.Step), done: make(chan struct{})}
	} else {
		mode := e.committed()
		r = e.newRun(mode, RequestOptions{Actor: state.Actor{Kind: "system", Via: "startup"}}, mode, "")
		r.policy, r.begun = true, true
	}
	e.setRunningLocked(r)
	e.mu.Unlock()

	if r.policy {
		e.log.Info("applying mode policy", "mode", string(r.t.Target))
	} else {
		e.log.Info("mode transition resumed", "code", CodeResumed, "target", string(r.t.Target), "step", r.t.Step, "attempts", r.t.Attempts, "op_id", r.t.OpID)
	}
	go e.loop(r)
	return Result{OpID: r.t.OpID, From: r.t.From, Target: r.t.Target}, nil
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
