package modes

import "github.com/marinoscar/local-ai/agent/internal/state"

// This file is the read-only policy surface other subsystems use (agent.md section 4.2.5): the cloud
// link, the prober and the scheduled sync check Online() at every iteration, not only at start; the
// HTTP layer reads HTTP().

// Current returns the committed mode (state.mode). It changes when a transition's persist step
// commits, and never blocks.
func (e *Engine) Current() Mode { return e.committed() }

// effectiveLocked is the mode the node is heading to. Caller holds mu.
func (e *Engine) effectiveLocked() Mode {
	if r := e.running; r != nil && !r.policy {
		if r.preempt != nil {
			return Survival
		}
		return r.t.Target
	}
	return e.committed()
}

// Effective returns the mode the node is heading to: the target of the running transition, else the
// committed mode.
func (e *Engine) Effective() Mode {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.effectiveLocked()
}

// Online reports whether online policy applies right now: the link, the prober and the scheduled sync
// run only then. It follows the requested mode, not the commit: it turns false at the first step of a
// transition away from online (so nothing starts a request while the link is being stopped) and true
// at the first step of a transition to online (so the link started by link_start is not told to
// idle until persist).
func (e *Engine) Online() bool { return e.Effective() == Online }

// Subscribe returns a channel that receives a Change each time the committed mode changes, and a
// function that cancels the subscription. The channel is buffered (8); a subscriber that does not
// keep up loses the oldest changes, never blocks the engine. Read Current() for the present value.
func (e *Engine) Subscribe() (<-chan Change, func()) {
	ch := make(chan Change, 8)
	e.subMu.Lock()
	id := e.nextSub
	e.nextSub++
	e.subs[id] = ch
	e.subMu.Unlock()
	return ch, func() {
		e.subMu.Lock()
		delete(e.subs, id)
		e.subMu.Unlock()
	}
}

func (e *Engine) notify(c Change) {
	e.subMu.Lock()
	defer e.subMu.Unlock()
	for _, ch := range e.subs {
		for {
			select {
			case ch <- c:
			default:
				select {
				case <-ch: // drop the oldest and retry
				default:
				}
				continue
			}
			break
		}
	}
}

// HTTP returns the mode-dependent HTTP flags of the committed mode: GET / redirects to /portal and
// /chat/ is anonymous only in survival (AGENT-16 consumes them).
func (e *Engine) HTTP() HTTPPolicy {
	s := e.committed() == Survival
	return HTTPPolicy{RedirectRootToPortal: s, ChatAnonymous: s}
}

// Info returns the picture GET /mode shows.
func (e *Engine) Info() Info {
	snap := e.d.State.Snapshot()
	e.mu.Lock()
	eff := e.effectiveLocked()
	e.mu.Unlock()

	mode := e.committed()
	info := Info{Mode: mode, Effective: eff, Since: snap.ModeChangedAt, Connectivity: snap.Connectivity.Value}
	if info.Connectivity == "" {
		info.Connectivity = state.ConnNotChecked
	}
	if mt := snap.ModeTransition; mt != nil {
		t := fromState(mt)
		info.Transition = &TransitionInfo{
			OpID: t.OpID, From: t.From, Target: t.Target, Step: t.Step, StartedAt: t.StartedAt,
			Warnings: append([]Warning{}, t.Warnings...),
		}
	}
	if mode == Online && eff == Online && snap.Connectivity.Value == state.ConnNone && snap.Connectivity.NoneSince != nil {
		info.SuggestSurvival = e.clock.Now().Sub(*snap.Connectivity.NoneSince) > e.suggestAt
	}
	return info
}
