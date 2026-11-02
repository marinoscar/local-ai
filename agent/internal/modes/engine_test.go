package modes_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/audit"
	"github.com/marinoscar/local-ai/agent/internal/events"
	"github.com/marinoscar/local-ai/agent/internal/modes"
	"github.com/marinoscar/local-ai/agent/internal/modes/fake"
	"github.com/marinoscar/local-ai/agent/internal/services"
	"github.com/marinoscar/local-ai/agent/internal/state"
	statefake "github.com/marinoscar/local-ai/agent/internal/state/fake"
)

func TestStepLists(t *testing.T) {
	want := map[modes.Mode]string{
		modes.Offline:  "link_stop,sync_cancel,prober_stop,tailscale_off,persist,emit",
		modes.Survival: "link_stop,sync_cancel,prober_stop,tailscale_off,survival_models,survival_services,persist,emit",
		modes.Online:   "tailscale_on,link_start,prober_start,persist,emit,sync_catchup",
	}
	for m, w := range want {
		if got := strings.Join(modes.Steps(m), ","); got != w {
			t.Errorf("Steps(%s) = %s, want %s", m, got, w)
		}
	}
	if modes.Steps("bogus") != nil {
		t.Error("Steps of an invalid mode must be nil")
	}
}

// All six directed transitions run the exact step order of agent.md 4.2.1 and end committed.
func TestDirectedTransitions(t *testing.T) {
	for _, from := range allModes {
		for _, to := range allModes {
			if from == to {
				continue
			}
			t.Run(fmt.Sprintf("%s_to_%s", from, to), func(t *testing.T) {
				r := newRig(t, withMode(from))
				res := r.mustRequest(to, true)
				if !res.Done || res.Target != to || res.From != from || res.OpID == "" {
					t.Fatalf("result = %+v", res)
				}
				if got, want := without(r.env.Effects(), "portal.drop"), effectsFrom(to, 0); !equalStrings(got, want) {
					t.Fatalf("effects:\n got  %s\n want %s", joined(got), joined(want))
				}
				snap := r.st.Snapshot()
				if snap.Mode != to || snap.ModeTransition != nil || snap.PendingMode != nil {
					t.Fatalf("state mode=%s transition=%v pending=%v", snap.Mode, snap.ModeTransition, snap.PendingMode)
				}
				if snap.ModeChangedAt == nil || !snap.ModeChangedAt.Equal(t0) {
					t.Fatalf("mode_changed_at = %v", snap.ModeChangedAt)
				}
				if r.eng.Current() != to {
					t.Fatalf("Current = %s", r.eng.Current())
				}

				var started []string
				for _, p := range r.env.Events.OfType(events.TypeModeStep) {
					ms := p.Data.(events.ModeStep)
					if ms.Target != string(to) || p.OpID != res.OpID {
						t.Errorf("bad step event %+v op %s", ms, p.OpID)
					}
					if ms.State == "started" {
						started = append(started, ms.Step)
					}
				}
				if !equalStrings(started, modes.Steps(to)) {
					t.Errorf("step events %v, want %v", started, modes.Steps(to))
				}
				ch := r.env.Events.OfType(events.TypeModeChanged)
				if len(ch) != 1 {
					t.Fatalf("mode.changed events: %d", len(ch))
				}
				mc := ch[0].Data.(events.ModeChanged)
				if mc.From != string(from) || mc.To != string(to) || mc.OpID != res.OpID || mc.Warnings == nil || len(mc.Warnings) != 0 {
					t.Errorf("mode.changed = %+v", mc)
				}
				aud := r.env.Audit.Events()
				if len(aud) != 1 || aud[0].Action != audit.ActionModeChange || aud[0].Result != audit.ResultOK ||
					aud[0].Actor.Kind != audit.KindOperator || aud[0].Actor.Via != "socket" || aud[0].OpID != res.OpID ||
					aud[0].Target["from"] != string(from) || aud[0].Target["to"] != string(to) {
					t.Errorf("audit = %+v", aud)
				}
			})
		}
	}
}

func TestAsyncRequestReturnsDurablyRecordedTransition(t *testing.T) {
	r := newRig(t, withMode(modes.Online))
	b := newBlocker()
	r.env.Link.StopB.SetHook(b.hook)
	res := r.mustRequest(modes.Offline, false)
	if res.Done || res.Step != modes.StepLinkStop || res.From != modes.Online || res.OpID == "" || !res.StartedAt.Equal(t0) {
		t.Fatalf("result = %+v", res)
	}
	b.waitEntered(t)
	mt := r.st.Snapshot().ModeTransition
	if mt == nil || mt.OpID != res.OpID || mt.Step != modes.StepLinkStop || mt.Attempts[modes.StepLinkStop] != 1 ||
		mt.Target != modes.Offline || mt.From != modes.Online || mt.Actor.Kind != "operator" || mt.Actor.Via != "socket" {
		t.Fatalf("recorded transition = %+v", mt)
	}
	info := r.eng.Info()
	if info.Mode != modes.Online || info.Effective != modes.Offline || info.Transition == nil || info.Transition.Step != modes.StepLinkStop {
		t.Fatalf("info = %+v", info)
	}
	b.open()
	r.idle()
	if r.eng.Current() != modes.Offline {
		t.Fatal("transition did not finish")
	}
}

func TestAlreadyAndReapply(t *testing.T) {
	r := newRig(t, withMode(modes.Offline))
	res := r.mustRequest(modes.Offline, false)
	if !res.Already || res.OpID != "" || res.Target != modes.Offline {
		t.Fatalf("already: %+v", res)
	}
	if len(r.env.Effects()) != 0 {
		t.Fatalf("an already-in-mode request ran %v", r.env.Effects())
	}
	ctx := context.Background()
	res, err := r.eng.Request(ctx, modes.Offline, modes.RequestOptions{Wait: true, Reapply: true, Actor: operator})
	if err != nil || !res.Done || res.Already || res.From != modes.Offline {
		t.Fatalf("reapply: %+v %v", res, err)
	}
	if got, want := r.env.Effects(), effectsFrom(modes.Offline, 0); !equalStrings(got, want) {
		t.Fatalf("reapply effects %v, want %v", got, want)
	}
	if r.st.Snapshot().Mode != modes.Offline {
		t.Fatal("mode changed")
	}
}

func TestInvalidTarget(t *testing.T) {
	r := newRig(t)
	if _, err := r.request("bogus", false); !errors.Is(err, modes.ErrInvalidTarget) {
		t.Fatalf("err = %v", err)
	}
}

func TestBusyForNonSurvivalRequest(t *testing.T) {
	r := newRig(t, withMode(modes.Online))
	b := newBlocker()
	r.env.Sync.CancelB.SetHook(b.hook)
	first := r.mustRequest(modes.Offline, false)
	b.waitEntered(t)
	for _, target := range []modes.Mode{modes.Online, modes.Offline} {
		_, err := r.request(target, false)
		var busy *modes.BusyError
		if !errors.As(err, &busy) || busy.OpID != first.OpID || busy.Target != modes.Offline {
			t.Fatalf("request %s while running: %v", target, err)
		}
	}
	b.open()
	r.idle()
	if _, err := r.request(modes.Online, false); err != nil {
		t.Fatalf("after the transition: %v", err)
	}
	r.idle()
}

func TestWaitContextEndsButTransitionContinues(t *testing.T) {
	r := newRig(t, withMode(modes.Online))
	b := newBlocker()
	r.env.Link.StopB.SetHook(b.hook)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errc := make(chan error, 1)
	go func() {
		_, err := r.eng.Request(ctx, modes.Offline, modes.RequestOptions{Wait: true, Actor: operator})
		errc <- err
	}()
	b.waitEntered(t) // the transition is running and blocked: the client gives up now
	cancel()
	if err := <-errc; !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	b.open()
	r.idle()
	if r.eng.Current() != modes.Offline {
		t.Fatal("a client that stopped waiting must not stop the transition")
	}
}

// Survival entry never fails: every step error, panic and timeout is a warning and the mode is entered.
func TestSurvivalCompletesWhenEveryStepErrors(t *testing.T) {
	boom := errors.New("boom")
	r := newRig(t, withMode(modes.Online))
	r.env.Link.StopB.Set(boom)
	r.env.Sync.CancelB.Set(boom)
	r.env.Tailscale.OffB.Set(boom)
	r.env.Models.LoadB.Set(boom)
	r.env.Services.Fail("kiwix", boom)
	r.env.Services.Fail("whisper", boom)
	res := r.mustRequest(modes.Survival, true)
	if !res.Done || res.Target != modes.Survival {
		t.Fatalf("result = %+v", res)
	}
	for _, code := range []string{modes.CodeStepFailed, modes.CodeTailscaleServe, modes.CodeNoModel, services.CodeLibraryNotRunning, services.CodeSpeechNotRunning} {
		if !hasCode(res.Warnings, code) {
			t.Errorf("warnings %v lack %s", codesOf(res.Warnings), code)
		}
	}
	snap := r.st.Snapshot()
	if snap.Mode != modes.Survival || snap.ModeTransition != nil {
		t.Fatalf("state = %s / %v", snap.Mode, snap.ModeTransition)
	}
	mc := r.env.Events.OfType(events.TypeModeChanged)[0].Data.(events.ModeChanged)
	if len(mc.Warnings) != len(res.Warnings) {
		t.Errorf("mode.changed warnings %v vs result %v", mc.Warnings, res.Warnings)
	}
	for _, p := range r.env.Events.OfType(events.TypeModeStep) {
		if ms := p.Data.(events.ModeStep); ms.State == "failed" && ms.Code == "" {
			t.Errorf("failed step event without a code: %+v", ms)
		}
	}
	// the retry of tailscale_off is running; stop it with the rig
}

// Each failure style of each fallible step (error, panic, timeout) ends in a warning, for every target.
func TestEveryStepFailureStyleBecomesAWarning(t *testing.T) {
	type tc struct {
		target modes.Mode
		step   string
		code   string
		script func(r *rig, style string, bl *blocker)
	}
	hookOrSet := func(b *fake.Behavior) func(style string, bl *blocker) {
		return func(style string, bl *blocker) {
			switch style {
			case "error":
				b.Set(errors.New("boom"))
			case "panic":
				b.SetPanic("kaboom")
			case "timeout":
				b.SetHook(bl.hook)
			}
		}
	}
	cases := []tc{
		{modes.Offline, modes.StepLinkStop, modes.CodeStepFailed, func(r *rig, s string, bl *blocker) { hookOrSet(&r.env.Link.StopB)(s, bl) }},
		{modes.Survival, modes.StepSyncCancel, modes.CodeStepFailed, func(r *rig, s string, bl *blocker) { hookOrSet(&r.env.Sync.CancelB)(s, bl) }},
		{modes.Offline, modes.StepTailscaleOff, modes.CodeTailscaleServe, func(r *rig, s string, bl *blocker) { hookOrSet(&r.env.Tailscale.OffB)(s, bl) }},
		{modes.Survival, modes.StepSurvivalModels, modes.CodeNoModel, func(r *rig, s string, bl *blocker) { hookOrSet(&r.env.Models.LoadB)(s, bl) }},
		{modes.Online, modes.StepTailscaleOn, modes.CodeTailscaleServe, func(r *rig, s string, bl *blocker) { hookOrSet(&r.env.Tailscale.OnB)(s, bl) }},
		{modes.Online, modes.StepLinkStart, modes.CodeStepFailed, func(r *rig, s string, bl *blocker) { hookOrSet(&r.env.Link.StartB)(s, bl) }},
	}
	for _, c := range cases {
		for _, style := range []string{"error", "panic", "timeout"} {
			t.Run(fmt.Sprintf("%s_%s_%s", c.target, c.step, style), func(t *testing.T) {
				from := modes.Offline
				if c.target == modes.Offline {
					from = modes.Online
				}
				r := newRig(t, withMode(from))
				bl := newBlocker()
				defer bl.open()
				c.script(r, style, bl)
				done := make(chan modes.Result, 1)
				go func() {
					res, _ := r.request(c.target, true)
					done <- res
				}()
				if style == "timeout" {
					bl.waitEntered(t)
					waitFor(t, "the step timer", func() bool { return r.clk.Tickers() > 0 })
					r.clk.Advance(time.Hour)
				}
				var res modes.Result
				select {
				case res = <-done:
				case <-time.After(60 * time.Second):
					t.Fatal("the transition hung")
				}
				if !res.Done || !hasCode(res.Warnings, c.code) {
					t.Fatalf("result %+v lacks %s", res, c.code)
				}
				if r.st.Snapshot().Mode != c.target {
					t.Fatalf("mode = %s", r.st.Snapshot().Mode)
				}
			})
		}
	}
}

// A step that ignores its context and never returns is abandoned at its timeout, not waited for.
func TestStepThatIgnoresItsContextIsAbandoned(t *testing.T) {
	r := newRig(t, withMode(modes.Online))
	stuck := make(chan struct{})
	defer close(stuck)
	entered := make(chan struct{}, 1)
	r.env.Link.StopB.SetHook(func(context.Context) error {
		entered <- struct{}{}
		<-stuck
		return nil
	})
	done := make(chan modes.Result, 1)
	go func() { res, _ := r.request(modes.Offline, true); done <- res }()
	<-entered
	waitFor(t, "the step timer", func() bool { return r.clk.Tickers() > 0 })
	r.clk.Advance(modes.DefaultTimeouts[modes.StepLinkStop])
	select {
	case res := <-done:
		if !hasCode(res.Warnings, modes.CodeStepFailed) || r.eng.Current() != modes.Offline {
			t.Fatalf("result %+v mode %s", res, r.eng.Current())
		}
	case <-time.After(60 * time.Second):
		t.Fatal("the engine waited for a stuck step")
	}
}

// persist writes the mode and clears mode_transition in one write: no write on disk ever shows the
// new mode with a transition still recorded, or the old mode with no transition during the run.
func TestPersistIsOneAtomicWrite(t *testing.T) {
	for _, to := range []modes.Mode{modes.Offline, modes.Survival, modes.Online} {
		t.Run(string(to), func(t *testing.T) {
			var rs *recStore
			r := newRig(t, withMode(modes.Offline), withWrap(func(st *state.Store) modes.StateStore {
				rs = &recStore{Store: st}
				return rs
			}))
			from := modes.Offline
			if to == modes.Offline {
				from = modes.Online
				if _, err := r.request(modes.Online, true); err != nil {
					t.Fatal(err)
				}
				rs.mu.Lock()
				rs.snaps = nil
				rs.mu.Unlock()
			}
			r.mustRequest(to, true)
			snaps := rs.snapshots()
			flips := 0
			for i, s := range snaps {
				switch {
				case s.Mode == to && s.ModeTransition != nil:
					t.Errorf("write %d: new mode with a transition still recorded", i)
				case s.Mode == from && s.ModeTransition == nil:
					t.Errorf("write %d: old mode and no transition recorded", i)
				case s.Mode == to && s.ModeTransition == nil:
					flips++
				}
			}
			if flips != 1 {
				t.Errorf("the mode flipped in %d writes, want 1", flips)
			}
			if last := snaps[len(snaps)-1]; last.Mode != to || last.ModeTransition != nil {
				t.Errorf("last write = %s / %v", last.Mode, last.ModeTransition)
			}
		})
	}
}

func TestUnwritableStoreKeepsModeInMemoryWithWarning(t *testing.T) {
	r := newRig(t, withMode(modes.Online))
	// from here every write fails: the mode must still be entered
	r.fs.Hook = func(op statefake.Op) error {
		if op.Kind == "write" || op.Kind == "open" || op.Kind == "rename" {
			return errNoSpace
		}
		return nil
	}
	res := r.mustRequest(modes.Offline, true)
	if !hasCode(res.Warnings, modes.CodeStateUnwritable) {
		t.Fatalf("warnings %v", codesOf(res.Warnings))
	}
	if r.eng.Current() != modes.Offline || r.st.Snapshot().Mode != modes.Offline {
		t.Fatal("mode not entered in memory")
	}
}

func TestPortalSessionsDroppedOnLeavingSurvivalOnly(t *testing.T) {
	r := newRig(t, withMode(modes.Online))
	r.mustRequest(modes.Survival, true)
	if r.env.Portal.Drops() != 0 {
		t.Fatal("dropped while entering survival")
	}
	r.mustRequest(modes.Offline, true)
	if r.env.Portal.Drops() != 1 {
		t.Fatalf("drops = %d after leaving survival", r.env.Portal.Drops())
	}
	r.mustRequest(modes.Online, true)
	if r.env.Portal.Drops() != 1 {
		t.Fatalf("drops = %d after offline to online", r.env.Portal.Drops())
	}
	r.mustRequest(modes.Survival, true)
	r.mustRequest(modes.Online, true)
	if r.env.Portal.Drops() != 2 {
		t.Fatalf("drops = %d after survival to online", r.env.Portal.Drops())
	}
}

func TestSyncCatchup(t *testing.T) {
	old := t0.Add(-25 * time.Hour).Format(time.RFC3339)
	recent := t0.Add(-time.Hour).Format(time.RFC3339)
	cases := []struct {
		name string
		sync json.RawMessage
		want bool
	}{
		{"never synced", nil, true},
		{"older than 24 h", json.RawMessage(`{"_owner":"ark-sync.md","last_success_at":"` + old + `"}`), true},
		{"recent", json.RawMessage(`{"_owner":"ark-sync.md","last_success_at":"` + recent + `"}`), false},
		{"malformed", json.RawMessage(`{"last_success_at": 7}`), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t, withMode(modes.Offline), withSeed(func(s *state.State) { s.Sync = c.sync }))
			r.mustRequest(modes.Online, true)
			got := r.env.Rec.Count("sync.catchup 5m0s") == 1
			if got != c.want {
				t.Fatalf("catchup scheduled = %v, want %v (calls %v)", got, c.want, r.env.Effects())
			}
		})
	}
	// only online schedules one
	r := newRig(t, withMode(modes.Online))
	r.mustRequest(modes.Offline, true)
	if r.env.Rec.Count("sync.catchup 5m0s") != 0 {
		t.Fatal("offline scheduled a sync")
	}
}

func TestProberStopSetsConnectivityNotChecked(t *testing.T) {
	r := newRig(t, withMode(modes.Online), withSeed(func(s *state.State) { s.Connectivity.Value = state.ConnFull }))
	r.mustRequest(modes.Offline, true)
	if got := r.st.Snapshot().Connectivity.Value; got != state.ConnNotChecked {
		t.Fatalf("connectivity = %q", got)
	}
}

func TestSurvivalServicesIncludeProfileRequirements(t *testing.T) {
	r := newRig(t, withMode(modes.Online), withDeps(func(d *modes.Deps) {
		d.SurvivalServices = func() []string { return []string{"whisper", "nonesuch"} }
	}))
	res := r.mustRequest(modes.Survival, true)
	calls := r.env.Effects()
	if r.env.Rec.Count("service.start kiwix") != 1 || r.env.Rec.Count("service.start whisper") != 1 {
		t.Fatalf("calls %v", calls)
	}
	if !hasCode(res.Warnings, modes.CodeStepFailed) {
		t.Fatalf("an unknown required service must warn: %v", codesOf(res.Warnings))
	}
}

func TestNilDependenciesAreNoOps(t *testing.T) {
	r := newRig(t, withMode(modes.Online), withDeps(func(d *modes.Deps) {
		*d = modes.Deps{State: d.State}
	}))
	for _, m := range []modes.Mode{modes.Survival, modes.Online, modes.Offline} {
		res := r.mustRequest(m, true)
		if !res.Done || len(res.Warnings) != 0 || r.eng.Current() != m {
			t.Fatalf("to %s: %+v", m, res)
		}
	}
	if _, err := modes.New(modes.Deps{}, modes.Options{}); err == nil {
		t.Fatal("New without a state store must fail")
	}
}

// ---- tailscale_off background retry ---------------------------------------------------------------

func TestTailscaleOffRetriesInBackgroundUntilItWorks(t *testing.T) {
	r := newRig(t, withMode(modes.Online))
	r.env.Tailscale.OffB.Set(errors.New("serve busy"))
	res := r.mustRequest(modes.Offline, true)
	if !hasCode(res.Warnings, modes.CodeTailscaleServe) || r.eng.Current() != modes.Offline {
		t.Fatalf("result %+v", res)
	}
	if n := r.env.Rec.Count("tailscale.off"); n != 1 {
		t.Fatalf("offs = %d", n)
	}
	waitFor(t, "the retry timer", func() bool { return r.clk.Tickers() == 1 })
	for i := 2; i <= 3; i++ {
		r.clk.Advance(modes.DefaultTailscaleRetry)
		waitFor(t, "a retry", func() bool { return r.env.Rec.Count("tailscale.off") == i })
	}
	r.env.Tailscale.OffB.Set(nil)
	r.clk.Advance(modes.DefaultTailscaleRetry)
	waitFor(t, "the fourth try", func() bool { return r.env.Rec.Count("tailscale.off") == 4 })
	waitFor(t, "the retry to stop", func() bool { return r.clk.Tickers() == 0 })
	// The retry's ticker is gone (asserted above), so nothing can fire: no waiting needed.
	r.clk.Advance(10 * modes.DefaultTailscaleRetry)
	if n := r.env.Rec.Count("tailscale.off"); n != 4 {
		t.Fatalf("retried after success: %d", n)
	}
}

func TestTailscaleOffWarningCodeAlsoRetries(t *testing.T) {
	r := newRig(t, withMode(modes.Online))
	r.env.Tailscale.OffB.Set(nil, modes.Warning{Code: modes.CodeTailscaleStillServing, Message: "still serving"})
	res := r.mustRequest(modes.Survival, true)
	if !hasCode(res.Warnings, modes.CodeTailscaleStillServing) {
		t.Fatal("controller warning lost")
	}
	waitFor(t, "the retry timer", func() bool { return r.clk.Tickers() == 1 })
}

func TestGoingOnlineCancelsTheRetryAndNeverOverlapsOff(t *testing.T) {
	r := newRig(t, withMode(modes.Online))
	r.env.Tailscale.OffB.Set(errors.New("serve busy"))
	r.mustRequest(modes.Offline, true)
	waitFor(t, "the retry timer", func() bool { return r.clk.Tickers() == 1 })
	r.mustRequest(modes.Online, true)
	if r.clk.Tickers() != 0 {
		t.Fatalf("retry still running: %d tickers", r.clk.Tickers())
	}
	before := r.env.Rec.Count("tailscale.off")
	r.clk.Advance(5 * modes.DefaultTailscaleRetry) // no ticker is left to fire
	if r.env.Rec.Count("tailscale.off") != before {
		t.Fatal("a retry ran after going online")
	}
}

// ---- policy surface -------------------------------------------------------------------------------

func TestOnlineFollowsTheRequestedModeNotTheCommit(t *testing.T) {
	r := newRig(t, withMode(modes.Online))
	if !r.eng.Online() {
		t.Fatal("online node reports not online")
	}
	b := newBlocker()
	r.env.Sync.CancelB.SetHook(b.hook)
	r.mustRequest(modes.Offline, false)
	b.waitEntered(t)
	if r.eng.Online() || r.eng.Current() != modes.Online || r.eng.Effective() != modes.Offline {
		t.Fatalf("during online->offline: online=%v current=%s effective=%s", r.eng.Online(), r.eng.Current(), r.eng.Effective())
	}
	b.open()
	r.idle()
	if r.eng.Online() {
		t.Fatal("offline node reports online")
	}
	bl := newBlocker()
	r.env.Link.StartB.SetHook(bl.hook)
	r.mustRequest(modes.Online, false)
	bl.waitEntered(t)
	if !r.eng.Online() || r.eng.Current() != modes.Offline {
		t.Fatalf("during offline->online: online=%v current=%s", r.eng.Online(), r.eng.Current())
	}
	bl.open()
	r.idle()
}

func TestSubscribeDeliversCommittedChanges(t *testing.T) {
	r := newRig(t, withMode(modes.Online))
	ch, cancel := r.eng.Subscribe()
	res := r.mustRequest(modes.Survival, true)
	select {
	case c := <-ch:
		if c.From != modes.Online || c.To != modes.Survival || c.OpID != res.OpID || !c.At.Equal(t0) {
			t.Fatalf("change = %+v", c)
		}
	case <-time.After(60 * time.Second):
		t.Fatal("no change delivered")
	}
	cancel()
	r.mustRequest(modes.Offline, true)
	select {
	case c := <-ch:
		t.Fatalf("delivered after cancel: %+v", c)
	default:
	}
}

func TestSlowSubscriberNeverBlocksTheEngine(t *testing.T) {
	r := newRig(t, withMode(modes.Online))
	ch, cancel := r.eng.Subscribe()
	defer cancel()
	targets := []modes.Mode{modes.Offline, modes.Online}
	for i := 0; i < 30; i++ {
		r.mustRequest(targets[i%2], true)
	}
	// the buffer holds the latest changes, oldest dropped
	n := len(ch)
	if n == 0 || n > 8 {
		t.Fatalf("buffered %d changes", n)
	}
	var last modes.Change
	for i := 0; i < n; i++ {
		last = <-ch
	}
	if last.To != modes.Online {
		t.Fatalf("latest change lost: %+v", last)
	}
}

func TestHTTPPolicyFlags(t *testing.T) {
	r := newRig(t, withMode(modes.Online))
	if r.eng.HTTP() != (modes.HTTPPolicy{}) {
		t.Fatal("flags set outside survival")
	}
	r.mustRequest(modes.Survival, true)
	if p := r.eng.HTTP(); !p.RedirectRootToPortal || !p.ChatAnonymous {
		t.Fatalf("policy = %+v", p)
	}
	r.mustRequest(modes.Offline, true)
	if r.eng.HTTP() != (modes.HTTPPolicy{}) {
		t.Fatal("flags still set after leaving survival")
	}
}

func TestInfoConnectivityAndSuggestion(t *testing.T) {
	none := t0.Add(-31 * time.Minute)
	r := newRig(t, withMode(modes.Online), withSeed(func(s *state.State) {
		s.Connectivity.Value = state.ConnNone
		s.Connectivity.NoneSince = &none
	}))
	info := r.eng.Info()
	if info.Connectivity != "none" || !info.SuggestSurvival || info.Mode != modes.Online || info.Transition != nil {
		t.Fatalf("info = %+v", info)
	}
	recent := t0.Add(-29 * time.Minute)
	_ = r.st.Mutate(func(s *state.State) error { s.Connectivity.NoneSince = &recent; return nil })
	if r.eng.Info().SuggestSurvival {
		t.Fatal("suggested survival after only 29 minutes")
	}
	_ = r.st.Mutate(func(s *state.State) error { s.Connectivity.Value = ""; return nil })
	if got := r.eng.Info().Connectivity; got != "not_checked" {
		t.Fatalf("empty connectivity = %q", got)
	}
	// never suggested outside online
	_ = r.st.Mutate(func(s *state.State) error {
		s.Connectivity.Value = state.ConnNone
		s.Connectivity.NoneSince = &none
		return nil
	})
	r.mustRequest(modes.Offline, true)
	if r.eng.Info().SuggestSurvival {
		t.Fatal("suggested survival while offline")
	}
}

// ---- concurrency ----------------------------------------------------------------------------------

func TestConcurrentRequestsKeepOneTransitionAtATime(t *testing.T) {
	r := newRig(t, withMode(modes.Online))
	var running, maxRunning int32
	var mu sync.Mutex
	track := func(context.Context) error {
		mu.Lock()
		running++
		if running > maxRunning {
			maxRunning = running
		}
		mu.Unlock()
		time.Sleep(time.Millisecond)
		mu.Lock()
		running--
		mu.Unlock()
		return nil
	}
	r.env.Link.StopB.SetHook(track)
	r.env.Link.StartB.SetHook(track)
	r.env.Sync.CancelB.SetHook(track)

	var wg sync.WaitGroup
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			target := allModes[i%3]
			_, err := r.request(target, i%2 == 0)
			var busy *modes.BusyError
			if err != nil && !errors.As(err, &busy) {
				t.Errorf("request %s: %v", target, err)
			}
		}(i)
	}
	wg.Wait()
	r.idle()
	if maxRunning > 1 {
		t.Fatalf("%d steps ran at once: two transitions overlapped", maxRunning)
	}
	snap := r.st.Snapshot()
	if snap.ModeTransition != nil || snap.PendingMode != nil || !snap.Mode.Valid() || snap.Mode != r.eng.Current() {
		t.Fatalf("inconsistent end state: %s / %v / %v", snap.Mode, snap.ModeTransition, snap.PendingMode)
	}
}

func TestManyConcurrentSurvivalRequestsEndInSurvival(t *testing.T) {
	r := newRig(t, withMode(modes.Online))
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := r.request(modes.Survival, false); err != nil {
				t.Errorf("survival request refused: %v", err)
			}
		}()
	}
	wg.Wait()
	r.idle()
	if r.eng.Current() != modes.Survival {
		t.Fatalf("mode = %s", r.eng.Current())
	}
}

var errNoSpace = errors.New("no space left on device")
