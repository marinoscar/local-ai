package drill

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/config/fake"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/modes"
)

var scheduled = StartRequest{Trigger: TriggerScheduled}

func (r *rig) retryDue() (StartResult, bool, error) {
	r.t.Helper()
	return r.eng.RetryDue(context.Background())
}

// TestSyncDeferralRetriesDailyForSevenDaysThenGivesUp: drill.md section 4.1.
func TestSyncDeferralRetriesDailyForSevenDaysThenGivesUp(t *testing.T) {
	r := newRig(t)
	r.add(passing("drl.a", 1))
	r.ark.set(true)

	first, err := r.eng.Start(context.Background(), scheduled)
	if err != nil || first.State != StartSkipped || first.RetryAt == nil || !first.RetryAt.Equal(t0.Add(24*time.Hour)) {
		t.Fatalf("first attempt = %+v, %v", first, err)
	}
	d := r.eng.block.deferral()
	if d == nil || d.Code != doctor.DRL002 || d.Attempts != 1 || !d.FirstAt.Equal(t0) || !d.DeadlineAt.Equal(t0.Add(7*24*time.Hour)) || !d.NextRetryAt.Equal(t0.Add(24*time.Hour)) {
		t.Fatalf("deferral = %+v", d)
	}
	if _, tried, _ := r.retryDue(); tried {
		t.Fatal("a retry before its time must do nothing")
	}
	r.clock.Advance(24*time.Hour - time.Second)
	if _, tried, _ := r.retryDue(); tried {
		t.Fatal("a retry one second early must do nothing")
	}
	r.clock.Advance(time.Second)

	for day := 1; day <= 7; day++ {
		res, tried, err := r.retryDue()
		if err != nil || !tried || res.State != StartSkipped || res.Code != doctor.DRL002 {
			t.Fatalf("day %d retry = %+v tried=%v err=%v", day, res, tried, err)
		}
		if day < 7 {
			want := t0.Add(time.Duration(day+1) * 24 * time.Hour)
			if res.RetryAt == nil || !res.RetryAt.Equal(want) {
				t.Fatalf("day %d: next retry %v, want %v", day, res.RetryAt, want)
			}
			if got := r.eng.block.deferral(); got == nil || got.Attempts != day+1 || !got.FirstAt.Equal(t0) {
				t.Fatalf("day %d: deferral = %+v", day, got)
			}
			r.clock.Advance(24 * time.Hour)
			continue
		}
		// Day 7 is the deadline: the chain ends.
		if res.RetryAt != nil || !strings.Contains(res.Reason, "given up after 7 days") {
			t.Fatalf("day 7 must give up: %+v", res)
		}
	}
	if r.eng.block.deferral() != nil {
		t.Fatal("a chain that gave up leaves no deferral behind")
	}
	if _, tried, _ := r.retryDue(); tried {
		t.Fatal("nothing is left to retry")
	}
	// The documents of the skipped attempts never accumulate: one is kept.
	if n := hasPrefixName(r.fs.listing(testDir), "drl-"); n != 1 {
		t.Fatalf("%d documents kept after 8 skipped attempts", n)
	}
}

func TestSyncDeferralRunsWhenTheSyncEnds(t *testing.T) {
	r := newRig(t)
	r.add(passing("drl.a", 1))
	r.ark.set(true)
	_, _ = r.eng.Start(context.Background(), scheduled)
	r.clock.Advance(24 * time.Hour)
	r.ark.set(false)

	res, tried, err := r.retryDue()
	if err != nil || !tried || res.State != StartRunning {
		t.Fatalf("retry = %+v tried=%v err=%v", res, tried, err)
	}
	r.wait()
	d := r.doc(res.RunID)
	if d.Trigger != TriggerScheduled || d.Overall != OverallPass || d.Quick {
		t.Fatalf("retried run = %+v", d)
	}
	if r.eng.block.deferral() != nil {
		t.Fatal("a run that started ends the deferral")
	}
}

// TestBusyDeferralRetriesEveryTwoHoursThenRunsQuick: drill.md sections 4.1 and 5.
func TestBusyDeferralRetriesEveryTwoHoursThenRunsQuick(t *testing.T) {
	r := newRig(t)
	ranChecks := 0
	r.add(Check{ID: "drl.a", Area: "x", Order: 1, Critical: true, Run: func(context.Context, *Env) Outcome { ranChecks++; return Outcome{Result: ResultPass} }})
	r.add(Check{ID: "drl.slow", Area: "x", Order: 2, SkipInQuick: true, Run: func(context.Context, *Env) Outcome {
		t.Error("the forced run is --quick")
		return Outcome{Result: ResultPass}
	}})
	r.llama.set(true)

	first, _ := r.eng.Start(context.Background(), scheduled)
	if first.State != StartSkipped || first.Code != doctor.DRL004 || !first.RetryAt.Equal(t0.Add(2*time.Hour)) {
		t.Fatalf("first attempt = %+v", first)
	}
	if d := r.eng.block.deferral(); d == nil || !d.DeadlineAt.Equal(t0.Add(48*time.Hour)) {
		t.Fatalf("deferral = %+v", d)
	}
	for i := 1; i <= 23; i++ {
		r.clock.Advance(2 * time.Hour)
		res, tried, err := r.retryDue()
		if err != nil || !tried || res.State != StartSkipped || res.Code != doctor.DRL004 {
			t.Fatalf("retry %d = %+v tried=%v err=%v", i, res, tried, err)
		}
		if want := t0.Add(time.Duration(2*(i+1)) * time.Hour); !res.RetryAt.Equal(want) {
			t.Fatalf("retry %d: next %v, want %v", i, res.RetryAt, want)
		}
	}
	probes := r.llama.calls
	r.clock.Advance(2 * time.Hour) // t0 + 48 h: the deadline
	res, tried, err := r.retryDue()
	if err != nil || !tried || res.State != StartRunning {
		t.Fatalf("at the deadline the drill must run anyway: %+v tried=%v err=%v", res, tried, err)
	}
	r.wait()
	d := r.doc(res.RunID)
	if !d.Quick || d.Trigger != TriggerScheduled || d.Overall != OverallPass || len(d.Checks) != 2 || d.Checks[1].Result != ResultSkip {
		t.Fatalf("forced run = %+v", d)
	}
	if r.llama.calls != probes {
		t.Fatal("the forced run must not ask whether llama is busy")
	}
	if r.eng.block.deferral() != nil {
		t.Fatal("the chain ends with the forced run")
	}
}

func TestForcedQuickRunStillHonorsTheOtherPreconditions(t *testing.T) {
	r := newRig(t)
	r.add(passing("drl.a", 1))
	r.llama.set(true)
	_, _ = r.eng.Start(context.Background(), scheduled)
	r.clock.Advance(48 * time.Hour)
	r.power.st, r.power.ok = BatteryStatus{Percent: 20, OnBattery: true}, true
	res, tried, _ := r.retryDue()
	if !tried || res.State != StartSkipped || res.Code != doctor.DRL005 || res.RetryAt != nil {
		t.Fatalf("a low battery still skips the forced run: %+v", res)
	}
	if r.eng.block.deferral() != nil {
		t.Fatal("a refusal that is not retried ends the chain")
	}
}

func TestDeferralSurvivesAnAgentRestart(t *testing.T) {
	r := newRig(t)
	r.add(passing("drl.a", 1))
	r.llama.set(true)
	_, _ = r.eng.Start(context.Background(), scheduled)
	raw := string(r.state.Snapshot().Drill)
	if !strings.Contains(raw, `"deferred"`) {
		t.Fatalf("the schedule must be in state.json: %s", raw)
	}

	// A new engine over the same state, a clock that starts after the retry time.
	clock2 := fake.NewClock(t0.Add(3 * time.Hour))
	reg2 := NewRegistry()
	_ = reg2.Add(passing("drl.a", 1))
	llama2 := &fakeLlama{}
	eng2, err := New(Deps{
		Registry: reg2, Results: r.results, State: r.state, Llama: llama2, Clock: clock2, Rand: &counterRand{n: 100},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	res, tried, err := eng2.RetryDue(context.Background())
	if err != nil || !tried || res.State != StartRunning {
		t.Fatalf("the restarted agent must keep the schedule: %+v tried=%v err=%v", res, tried, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := eng2.Wait(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestChangingRefusalCodeStartsANewChain(t *testing.T) {
	r := newRig(t)
	r.add(passing("drl.a", 1))
	r.ark.set(true)
	_, _ = r.eng.Start(context.Background(), scheduled)
	r.clock.Advance(24 * time.Hour)
	r.ark.set(false)
	r.llama.set(true)
	res, tried, _ := r.retryDue()
	if !tried || res.Code != doctor.DRL004 || !res.RetryAt.Equal(r.clock.Now().Add(2*time.Hour)) {
		t.Fatalf("retry = %+v", res)
	}
	d := r.eng.block.deferral()
	if d.Code != doctor.DRL004 || d.Attempts != 1 || !d.FirstAt.Equal(r.clock.Now()) || !d.DeadlineAt.Equal(r.clock.Now().Add(48*time.Hour)) {
		t.Fatalf("a new code starts a new chain: %+v", d)
	}
}

func TestRefusalThatIsNotRetriedEndsTheChain(t *testing.T) {
	r := newRig(t)
	r.add(passing("drl.a", 1))
	r.ark.set(true)
	_, _ = r.eng.Start(context.Background(), scheduled)
	r.clock.Advance(24 * time.Hour)
	r.mode.set(modes.Survival)
	res, tried, _ := r.retryDue()
	if !tried || res.Code != doctor.DRL003 || res.RetryAt != nil {
		t.Fatalf("retry in survival = %+v", res)
	}
	if r.eng.block.deferral() != nil {
		t.Fatal("survival ends the chain: the drill never disturbs an emergency")
	}
}

func TestManualRunEndsAPendingDeferralButAManualQuickRunDoesNot(t *testing.T) {
	for _, tc := range []struct {
		name  string
		req   StartRequest
		clear bool
	}{
		{"manual full run", StartRequest{Trigger: TriggerManual}, true},
		{"manual quick run", StartRequest{Trigger: TriggerManual, Quick: true}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t)
			r.add(passing("drl.a", 1))
			r.llama.set(true)
			_, _ = r.eng.Start(context.Background(), scheduled)
			r.llama.set(false)
			if _, err := r.eng.Start(context.Background(), tc.req); err != nil {
				t.Fatal(err)
			}
			r.wait()
			if cleared := r.eng.block.deferral() == nil; cleared != tc.clear {
				t.Fatalf("deferral cleared = %v, want %v", cleared, tc.clear)
			}
		})
	}
}

func TestDeferralKeepsTheRequestedQuickFlagAndOnly(t *testing.T) {
	r := newRig(t)
	r.add(passing("drl.a", 1))
	r.add(passing("drl.b", 2))
	r.llama.set(true)
	_, _ = r.eng.Start(context.Background(), StartRequest{Trigger: TriggerScheduled, Quick: true, Only: []string{"drl.b"}})
	r.clock.Advance(2 * time.Hour)
	r.llama.set(false)
	res, tried, err := r.retryDue()
	if err != nil || !tried {
		t.Fatalf("retry = %+v %v %v", res, tried, err)
	}
	r.wait()
	if d := r.doc(res.RunID); !d.Quick || len(d.Checks) != 1 || d.Checks[0].ID != "drl.b" {
		t.Fatalf("the retry must repeat the request: %+v", d)
	}
}

func TestADeferralThatCanNeverStartIsDropped(t *testing.T) {
	r := newRig(t)
	r.add(passing("drl.a", 1))
	d := deferral{Code: doctor.DRL004, Only: []string{"drl.gone"}, FirstAt: t0, NextRetryAt: t0, DeadlineAt: t0.Add(48 * time.Hour), Attempts: 3}
	raw, _ := json.Marshal(map[string]any{"deferred": d})
	r.state.setBlock(string(raw))
	_, tried, err := r.retryDue()
	var inv *InvalidRequestError
	if !tried || !errors.As(err, &inv) {
		t.Fatalf("RetryDue = tried %v, %v", tried, err)
	}
	if r.eng.block.deferral() != nil {
		t.Fatal("a schedule that can never start must not retry forever")
	}
}

func TestUnreadableDeferralIsIgnored(t *testing.T) {
	r := newRig(t)
	r.state.setBlock(`{"deferred":"garbage"}`)
	if _, tried, err := r.retryDue(); tried || err != nil {
		t.Fatalf("RetryDue = %v, %v", tried, err)
	}
	r.state.setBlock(`not json`)
	if _, tried, err := r.retryDue(); tried || err != nil {
		t.Fatalf("RetryDue on a broken block = %v, %v", tried, err)
	}
}

func TestRunRetriesFollowsTheTicks(t *testing.T) {
	r := newRig(t)
	r.add(passing("drl.a", 1))
	r.llama.set(true)
	_, _ = r.eng.Start(context.Background(), scheduled)

	ticks := make(chan time.Time)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { r.eng.RunRetries(ctx, ticks); close(done) }()

	ticks <- r.clock.Now() // not due: nothing happens
	r.clock.Advance(2 * time.Hour)
	r.llama.set(false)
	ticks <- r.clock.Now()
	ticks <- r.clock.Now() // accepted only when the loop is back at its select: the retry has been handled
	cancel()
	<-done
	r.wait()
	if n := hasPrefixName(r.fs.listing(testDir), "drl-"); n < 1 {
		t.Fatal("the tick must have started the deferred run")
	}
	if r.eng.block.deferral() != nil {
		t.Fatal("the retried run must have ended the deferral")
	}
	last, _ := r.eng.Latest()
	if !strings.Contains(string(last), `"trigger": "scheduled"`) || strings.Contains(string(last), `"skipped"`) {
		t.Fatalf("latest = %s", last)
	}
	// A closed ticker ends the loop too.
	closed := make(chan time.Time)
	close(closed)
	r.eng.RunRetries(context.Background(), closed)
}

func TestRetryDueWithNothingDeferred(t *testing.T) {
	r := newRig(t)
	res, tried, err := r.retryDue()
	if tried || err != nil || res != (StartResult{}) {
		t.Fatalf("RetryDue = %+v %v %v", res, tried, err)
	}
	if r.state.mutations != 0 {
		t.Fatal("nothing deferred must write nothing")
	}
}
