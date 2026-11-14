package modes_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/events"
	"github.com/marinoscar/local-ai/agent/internal/modes"
	"github.com/marinoscar/local-ai/agent/internal/state"
)

// A survival request preempts a running transition after at most the step that is running.
func TestSurvivalPreemptsOfflineAfterAtMostOneStep(t *testing.T) {
	r := newRig(t, withMode(modes.Online))
	b := newBlocker()
	r.env.Sync.CancelB.SetHook(b.hook)

	// the preempted caller waits, and learns who replaced its transition
	waited := make(chan modes.Result, 1)
	started := make(chan modes.Result, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		res, err := r.eng.Request(ctx, modes.Offline, modes.RequestOptions{Wait: true, Actor: operator})
		if err != nil {
			t.Errorf("offline request: %v", err)
		}
		waited <- res
	}()
	_ = started
	b.waitEntered(t) // link_stop is done, sync_cancel is running

	surv := r.mustRequest(modes.Survival, false)
	if surv.OpID == "" || surv.Joined || surv.From != modes.Online || surv.Target != modes.Survival {
		t.Fatalf("survival result %+v", surv)
	}
	// accepted means durable: a crash now would still run it at the next start
	if p := r.st.Snapshot().PendingMode; p == nil || *p != modes.Survival {
		t.Fatalf("pending_mode = %v", p)
	}
	// a second survival request joins it; anything else is busy
	again := r.mustRequest(modes.Survival, false)
	if !again.Joined || again.OpID != surv.OpID {
		t.Fatalf("second survival request %+v", again)
	}
	if _, err := r.request(modes.Online, false); err == nil {
		t.Fatal("online accepted while preempting")
	}
	b.open()
	r.idle()

	want := []string{
		"link.stop", "sync.cancel", // the offline transition, stopped after the running step
		"link.stop", "sync.cancel", "prober.stop", "tailscale.off", "engines.stop", "models.load", "service.start kiwix", "service.start whisper",
	}
	if got := r.env.Effects(); !equalStrings(got, want) {
		t.Fatalf("effects:\n got  %s\n want %s", joined(got), joined(want))
	}
	snap := r.st.Snapshot()
	if snap.Mode != modes.Survival || snap.ModeTransition != nil || snap.PendingMode != nil {
		t.Fatalf("state %s / %v / %v", snap.Mode, snap.ModeTransition, snap.PendingMode)
	}
	res := <-waited
	if !res.Preempted || res.PreemptedBy != surv.OpID || res.Done {
		t.Fatalf("preempted caller got %+v", res)
	}
	mc := r.env.Events.OfType(events.TypeModeChanged)
	if len(mc) != 1 {
		t.Fatalf("mode.changed events: %d", len(mc))
	}
	if ev := mc[0].Data.(events.ModeChanged); ev.From != "online" || ev.To != "survival" || ev.OpID != surv.OpID {
		t.Fatalf("mode.changed %+v", ev)
	}
	aud := r.env.Audit.Events()
	if len(aud) != 1 || aud[0].OpID != surv.OpID {
		t.Fatalf("audit %+v", aud)
	}
}

// Preempting a transition to online starts survival from the mode the node was really in.
func TestSurvivalPreemptsOnlineFromOffline(t *testing.T) {
	r := newRig(t, withMode(modes.Offline))
	b := newBlocker()
	r.env.Link.StartB.SetHook(b.hook)
	first := r.mustRequest(modes.Online, false)
	b.waitEntered(t)
	surv := r.mustRequest(modes.Survival, false)
	b.open()
	r.idle()
	if first.OpID == surv.OpID || surv.From != modes.Offline {
		t.Fatalf("%+v / %+v", first, surv)
	}
	if r.env.Rec.Count("prober.start") != 0 {
		t.Fatal("the online transition went on after the preemption")
	}
	mc := r.env.Events.OfType(events.TypeModeChanged)[0].Data.(events.ModeChanged)
	if mc.From != "offline" || mc.To != "survival" {
		t.Fatalf("mode.changed %+v", mc)
	}
}

func TestSurvivalRequestJoinsARunningSurvival(t *testing.T) {
	r := newRig(t, withMode(modes.Online))
	b := newBlocker()
	r.env.Models.LoadB.SetHook(b.hook)
	// The waiting caller starts the transition itself, so it is attached by construction (nothing to
	// race against); a second, non-waiting request then joins it while the step is parked.
	type outcome struct {
		res modes.Result
		err error
	}
	waited := make(chan outcome, 1)
	go func() {
		res, err := r.request(modes.Survival, true)
		waited <- outcome{res, err}
	}()
	b.waitEntered(t)
	joined := r.mustRequest(modes.Survival, false)
	if !joined.Joined || joined.OpID == "" {
		t.Fatalf("joined %+v", joined)
	}
	b.open()
	w := <-waited
	if w.err != nil || !w.res.Done || w.res.OpID != joined.OpID {
		t.Fatalf("waiter %+v / %v, want Done under %s", w.res, w.err, joined.OpID)
	}
	if r.env.Rec.Count("models.load") != 1 {
		t.Fatal("a joined request started a second transition")
	}
}

func TestSurvivalWhileSurvivalIsLeavingRestartsIt(t *testing.T) {
	r := newRig(t, withMode(modes.Survival))
	b := newBlocker()
	r.env.Sync.CancelB.SetHook(b.hook) // survival -> offline stops here
	r.mustRequest(modes.Offline, false)
	b.waitEntered(t)
	res := r.mustRequest(modes.Survival, false)
	if res.From != modes.Survival || res.Joined {
		t.Fatalf("result %+v", res)
	}
	b.open()
	r.idle()
	if r.eng.Current() != modes.Survival || r.env.Rec.Count("models.load") != 1 {
		t.Fatalf("mode %s, loads %d", r.eng.Current(), r.env.Rec.Count("models.load"))
	}
}

// A survival request survives the agent dying before the preempted transition stops: it is on disk as
// pending_mode and runs at the next start.
func TestPreemptedSurvivalRunsAfterARestart(t *testing.T) {
	fs := newFS()
	r1 := newRig(t, withMode(modes.Online), withFS(fs))
	b := newBlocker()
	r1.env.Sync.CancelB.SetHook(b.hook)
	r1.mustRequest(modes.Offline, false)
	b.waitEntered(t)
	surv := r1.mustRequest(modes.Survival, false)
	r1.abandon = true // killed here: the old transition never noticed the preemption
	_ = r1.st.Close()
	fs.Reboot()

	r2 := newRig(t, withFS(fs), noSeed(), notOpen())
	snap := r2.st.Snapshot()
	if snap.ModeTransition == nil || snap.ModeTransition.Target != modes.Offline || snap.PendingMode == nil {
		t.Fatalf("state after the kill: %v / %v", snap.ModeTransition, snap.PendingMode)
	}
	r2.resume()                              // step 13 resumes the offline transition ...
	if _, err := r2.eng.Open(); err != nil { // ... and the pending survival preempts it
		t.Fatal(err)
	}
	r2.idle()
	if r2.eng.Current() != modes.Survival {
		t.Fatalf("mode %s", r2.eng.Current())
	}
	if p := r2.st.Snapshot().PendingMode; p != nil {
		t.Fatalf("pending_mode left: %v", *p)
	}
	_ = surv
}

// ---- queued survival requests ---------------------------------------------------------------------

func TestSurvivalRequestBeforeOpenIsQueuedAndRunsAtOpen(t *testing.T) {
	r := newRig(t, withMode(modes.Online), notOpen())
	res := r.mustRequest(modes.Survival, false)
	if !res.Queued || res.OpID == "" || res.Target != modes.Survival {
		t.Fatalf("queued result %+v", res)
	}
	if p := r.st.Snapshot().PendingMode; p == nil || *p != modes.Survival {
		t.Fatalf("pending_mode = %v", p)
	}
	if len(r.env.Effects()) != 0 {
		t.Fatalf("ran before Open: %v", r.env.Effects())
	}
	again := r.mustRequest(modes.Survival, false)
	if !again.Queued || again.OpID != res.OpID {
		t.Fatalf("second queued request %+v", again)
	}
	if _, err := r.request(modes.Offline, false); !errors.Is(err, modes.ErrNotAccepting) {
		t.Fatalf("offline before Open: %v", err)
	}
	if _, err := r.eng.Open(); err != nil {
		t.Fatal(err)
	}
	r.idle()
	if r.eng.Current() != modes.Survival {
		t.Fatalf("mode %s", r.eng.Current())
	}
	ev := r.env.Events.OfType(events.TypeModeChanged)[0].Data.(events.ModeChanged)
	if ev.OpID != res.OpID {
		t.Fatalf("ran under %s, answered with %s", ev.OpID, res.OpID)
	}
	if p := r.st.Snapshot().PendingMode; p != nil {
		t.Fatal("pending_mode not cleared")
	}
}

func TestPendingModeFromAPreviousRunStartsAtOpen(t *testing.T) {
	r := newRig(t, withMode(modes.Online), notOpen(), withSeed(func(s *state.State) {
		m := modes.Survival
		s.PendingMode = &m
	}))
	if _, err := r.eng.Open(); err != nil {
		t.Fatal(err)
	}
	r.idle()
	if r.eng.Current() != modes.Survival {
		t.Fatalf("mode %s", r.eng.Current())
	}
	aud := r.env.Audit.Events()
	if len(aud) != 1 || aud[0].Actor.Via != "pending_mode" {
		t.Fatalf("audit %+v", aud)
	}
}

func TestQueuedSurvivalPreemptsTheStartupPolicyRun(t *testing.T) {
	r := newRig(t, withMode(modes.Online), notOpen())
	b := newBlocker()
	r.env.Tailscale.OnB.SetHook(b.hook)
	r.resume() // step 13 begins applying the online policy ...
	b.waitEntered(t)
	r.mustRequest(modes.Survival, false) // ... a request arrives before step 12 ended
	if _, err := r.eng.Open(); err != nil {
		t.Fatal(err)
	}
	b.open()
	r.idle()
	if r.eng.Current() != modes.Survival || r.env.Rec.Count("link.start") != 0 {
		t.Fatalf("mode %s effects %v", r.eng.Current(), r.env.Effects())
	}
}

func TestQueuedRequestWhenAlreadyInSurvivalAnswersAlready(t *testing.T) {
	r := newRig(t, withMode(modes.Survival), notOpen())
	res := r.mustRequest(modes.Survival, false)
	if !res.Already || res.Queued {
		t.Fatalf("result %+v", res)
	}
	if r.st.Snapshot().PendingMode != nil {
		t.Fatal("pending_mode written for nothing")
	}
}

func TestStalePendingModeIsClearedWithoutRunning(t *testing.T) {
	r := newRig(t, withMode(modes.Survival), notOpen(), withSeed(func(s *state.State) {
		m := modes.Survival
		s.PendingMode = &m
	}))
	if _, err := r.eng.Open(); err != nil {
		t.Fatal(err)
	}
	r.idle()
	if len(r.env.Effects()) != 0 || r.st.Snapshot().PendingMode != nil {
		t.Fatalf("effects %v pending %v", r.env.Effects(), r.st.Snapshot().PendingMode)
	}
}

// ---- shutdown -------------------------------------------------------------------------------------

func TestShutdownLetsTheCurrentStepFinishThenLeavesTheRestForResume(t *testing.T) {
	fs := newFS()
	r1 := newRig(t, withMode(modes.Online), withFS(fs))
	b := newBlocker()
	r1.env.Sync.CancelB.SetHook(b.hook)
	op := r1.mustRequest(modes.Offline, false).OpID
	b.waitEntered(t)

	done := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		done <- r1.eng.Shutdown(ctx)
	}()
	// Shutdown has begun once a non-survival request is refused as "not accepting" (before that it
	// is "busy"); both answers are free of side effects.
	waitFor(t, "Shutdown to start", func() bool {
		_, err := r1.request(modes.Online, false)
		return errors.Is(err, modes.ErrNotAccepting)
	})
	b.open() // the running step finishes inside the budget
	if err := <-done; err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if got, want := r1.env.Effects(), []string{"link.stop", "sync.cancel"}; !equalStrings(got, want) {
		t.Fatalf("effects after shutdown %v, want %v", got, want)
	}
	mt := r1.st.Snapshot().ModeTransition
	if mt == nil || mt.Step != modes.StepSyncCancel || mt.OpID != op {
		t.Fatalf("recorded transition %+v", mt)
	}
	if r1.st.Snapshot().Mode != modes.Online {
		t.Fatal("mode committed during shutdown")
	}
	if len(r1.env.Events.OfType(events.TypeModeChanged)) != 0 {
		t.Fatal("mode.changed published for an interrupted transition")
	}
	r1.abandon = true
	_ = r1.st.Close()
	fs.Reboot()

	r2 := newRig(t, withFS(fs), noSeed())
	r2.resume()
	r2.idle()
	if r2.eng.Current() != modes.Offline {
		t.Fatalf("mode %s", r2.eng.Current())
	}
	if got, want := r2.env.Effects(), effectsFrom(modes.Offline, indexOf(modes.Steps(modes.Offline), modes.StepSyncCancel)); !equalStrings(got, want) {
		t.Fatalf("resumed effects %v, want %v", got, want)
	}
}

func TestShutdownAbandonsAStepThatOutlastsTheBudget(t *testing.T) {
	r := newRig(t, withMode(modes.Online))
	b := newBlocker()
	r.env.Link.StopB.SetHook(b.hook)
	r.mustRequest(modes.Offline, false)
	b.waitEntered(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	err := r.eng.Shutdown(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Shutdown: %v", err)
	}
	if mt := r.st.Snapshot().ModeTransition; mt == nil || mt.Step != modes.StepLinkStop {
		t.Fatalf("recorded %+v", mt)
	}
	if r.st.Snapshot().Mode != modes.Online {
		t.Fatal("mode committed")
	}
}

func TestSurvivalDuringShutdownIsQueuedAndOtherRequestsRefused(t *testing.T) {
	r := newRig(t, withMode(modes.Online))
	if err := r.eng.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	res, err := r.request(modes.Survival, false)
	if err != nil || !res.Queued || res.OpID == "" {
		t.Fatalf("survival during shutdown: %+v %v", res, err)
	}
	if p := r.st.Snapshot().PendingMode; p == nil || *p != modes.Survival {
		t.Fatalf("pending_mode = %v", p)
	}
	if _, err := r.request(modes.Offline, false); !errors.Is(err, modes.ErrNotAccepting) {
		t.Fatalf("offline during shutdown: %v", err)
	}
	if _, err := r.eng.Open(); !errors.Is(err, modes.ErrClosed) {
		t.Fatalf("Open after Shutdown: %v", err)
	}
	if _, err := r.eng.Resume(context.Background()); !errors.Is(err, modes.ErrClosed) {
		t.Fatalf("Resume after Shutdown: %v", err)
	}
}

func TestShutdownStopsTheTailscaleRetry(t *testing.T) {
	r := newRig(t, withMode(modes.Online))
	r.env.Tailscale.OffB.Set(errors.New("serve busy"))
	r.mustRequest(modes.Offline, true)
	waitFor(t, "the retry timer", func() bool { return r.clk.Tickers() == 1 })
	if err := r.eng.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if r.clk.Tickers() != 0 {
		t.Fatal("retry survived shutdown")
	}
}

// Leaving survival over a plain TCP operator is the API's rule; the engine itself has none, and a
// survival request is never refused for another reason (agent.md 3.8.16).
func TestSurvivalIsNeverRefusedWhateverIsRunning(t *testing.T) {
	for _, running := range []modes.Mode{modes.Online, modes.Offline, modes.Survival} {
		for _, committed := range allModes {
			t.Run(fmt.Sprintf("%s_while_%s_from_%s", modes.Survival, running, committed), func(t *testing.T) {
				r := newRig(t, withMode(committed))
				b := newBlocker()
				r.env.Link.StopB.SetHook(b.hook)
				r.env.Link.StartB.SetHook(b.hook)
				r.env.Tailscale.OnB.SetHook(b.hook)
				if running != committed || running == modes.Survival {
					r.mustRequest(running, false)
					// Survival requested while already in survival is "already": no step, no hook.
					if !(running == modes.Survival && committed == modes.Survival) {
						b.waitEntered(t)
					}
				}
				if _, err := r.request(modes.Survival, false); err != nil {
					t.Fatalf("survival refused: %v", err)
				}
				b.open()
				r.idle()
				if r.eng.Current() != modes.Survival {
					t.Fatalf("mode %s", r.eng.Current())
				}
			})
		}
	}
}
