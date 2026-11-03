package drill

import (
	"context"
	"errors"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/events"
	"github.com/marinoscar/local-ai/agent/internal/modes"
)

func TestPreconditionsEvaluate(t *testing.T) {
	type probes struct {
		mode     modes.Info
		ark      bool
		busy     bool
		busyErr  error
		battery  BatteryStatus
		hasBatt  bool
		skipBusy bool
	}
	online := modes.Info{Mode: modes.Online, Effective: modes.Online}
	for _, tc := range []struct {
		name  string
		p     probes
		code  string // "" = no refusal
		retry bool
	}{
		{"online and idle", probes{mode: online}, "", false},
		{"offline mode is fine", probes{mode: modes.Info{Mode: modes.Offline, Effective: modes.Offline}}, "", false},
		{"survival", probes{mode: modes.Info{Mode: modes.Survival, Effective: modes.Survival}}, doctor.DRL003, false},
		{"heading to survival", probes{mode: modes.Info{Mode: modes.Online, Effective: modes.Survival}}, doctor.DRL003, false},
		{"an ark run is active", probes{mode: online, ark: true}, doctor.DRL002, true},
		{"llama busy", probes{mode: online, busy: true}, doctor.DRL004, true},
		{"llama busy but the deferral ran out", probes{mode: online, busy: true, skipBusy: true}, "", false},
		{"a router that cannot answer is not busy", probes{mode: online, busyErr: errors.New("connection refused")}, "", false},
		{"battery 49 percent on battery", probes{mode: online, battery: BatteryStatus{Percent: 49, OnBattery: true}, hasBatt: true}, doctor.DRL005, false},
		{"battery exactly 50 percent", probes{mode: online, battery: BatteryStatus{Percent: 50, OnBattery: true}, hasBatt: true}, "", false},
		{"battery low but charging", probes{mode: online, battery: BatteryStatus{Percent: 10, OnBattery: false}, hasBatt: true}, "", false},
		{"no battery on a desktop", probes{mode: online, battery: BatteryStatus{Percent: 0, OnBattery: true}, hasBatt: false}, "", false},
		{"survival beats everything", probes{mode: modes.Info{Mode: modes.Survival, Effective: modes.Survival}, ark: true, busy: true, battery: BatteryStatus{Percent: 1, OnBattery: true}, hasBatt: true}, doctor.DRL003, false},
		{"sync beats busy and battery", probes{mode: online, ark: true, busy: true, battery: BatteryStatus{Percent: 1, OnBattery: true}, hasBatt: true}, doctor.DRL002, true},
		{"busy beats battery", probes{mode: online, busy: true, battery: BatteryStatus{Percent: 1, OnBattery: true}, hasBatt: true}, doctor.DRL004, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := Preconditions{
				Mode:  &fakeMode{info: tc.p.mode},
				Ark:   &fakeArk{active: tc.p.ark},
				Llama: &fakeLlama{busy: tc.p.busy, err: tc.p.busyErr},
				Power: &fakePower{st: tc.p.battery, ok: tc.p.hasBatt},
			}
			ref := p.Evaluate(context.Background(), preOpts{skipBusy: tc.p.skipBusy})
			if tc.code == "" {
				if ref != nil {
					t.Fatalf("refused: %+v", ref)
				}
				return
			}
			if ref == nil || ref.Code != tc.code || ref.Retry != tc.retry || ref.Reason == "" {
				t.Fatalf("refusal = %+v, want code %s retry %v", ref, tc.code, tc.retry)
			}
		})
	}
	if ref := (Preconditions{}).Evaluate(context.Background(), preOpts{}); ref != nil {
		t.Fatalf("nil probes must never refuse: %+v", ref)
	}
	var skipped = &fakeLlama{busy: true}
	if ref := (Preconditions{Llama: skipped}).Evaluate(context.Background(), preOpts{skipBusy: true}); ref != nil || skipped.calls != 0 {
		t.Fatal("skipBusy must not even ask llama")
	}
}

// refusal sets up each precondition on a rig.
var refusals = []struct {
	name   string
	setup  func(*rig)
	code   string
	retry  bool
	reason string
}{
	{"survival", func(r *rig) { r.mode.set(modes.Survival) }, doctor.DRL003, false, "survival_mode"},
	{"ark run active", func(r *rig) { r.ark.set(true) }, doctor.DRL002, true, "ark_run_active"},
	{"llama busy", func(r *rig) { r.llama.set(true) }, doctor.DRL004, true, "llama_busy"},
	{"low battery", func(r *rig) { r.power.st, r.power.ok = BatteryStatus{Percent: 30, OnBattery: true}, true }, doctor.DRL005, false, "on_battery"},
}

func TestManualRunIsRefusedWithTheCode(t *testing.T) {
	for _, tc := range refusals {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t)
			r.add(passing("drl.a", 1))
			tc.setup(r)
			_, err := r.eng.Start(context.Background(), StartRequest{Trigger: TriggerManual})
			var refused *RefusedError
			if !errors.As(err, &refused) || refused.Code != tc.code || refused.Details["reason"] != tc.reason {
				t.Fatalf("Start = %v, want a refusal with %s (%s)", err, tc.code, tc.reason)
			}
			if _, running := r.eng.Running(); running {
				t.Fatal("a refused run must free the slot")
			}
			if len(r.pub.events()) != 0 || r.fence.enables != 0 || len(r.rest.restoreCalls()) != 0 {
				t.Fatal("a refused run must not announce itself, fence or touch the node")
			}
			if got := r.fs.listing(testDir); len(got) != 0 {
				t.Fatalf("a refused manual run leaves no document, found %v", got)
			}
			if r.eng.block.deferral() != nil {
				t.Fatal("a manual refusal must not schedule a retry")
			}
			// The same refusal clears when the cause is gone.
			r2 := newRig(t)
			r2.add(passing("drl.a", 1))
			if _, err := r2.eng.Start(context.Background(), StartRequest{}); err != nil {
				t.Fatalf("an idle node must start: %v", err)
			}
			r2.wait()
		})
	}
}

func TestScheduledRunIsRecordedAsSkipped(t *testing.T) {
	for _, tc := range refusals {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t)
			r.add(passing("drl.a", 1))
			// A real result already exists: a skipped run must not hide it.
			good, _ := r.startWait(StartRequest{})
			before := len(r.pub.events())
			tc.setup(r)

			res, err := r.eng.Start(context.Background(), StartRequest{Trigger: TriggerScheduled})
			if err != nil {
				t.Fatalf("a scheduled run is never an error: %v", err)
			}
			if res.State != StartSkipped || res.Code != tc.code || res.Reason == "" || res.RunID == "" {
				t.Fatalf("result = %+v", res)
			}
			if (res.RetryAt != nil) != tc.retry {
				t.Fatalf("RetryAt = %v, want retry %v", res.RetryAt, tc.retry)
			}
			d := r.doc(res.RunID)
			if d.State != StateSkipped || d.Overall != OverallSkipped || d.Code != tc.code || d.Reason != res.Reason ||
				d.Trigger != TriggerScheduled || len(d.Checks) != 0 || d.FinishedAt == nil || !d.StartedAt.Equal(*d.FinishedAt) {
				t.Fatalf("skipped document = %+v", d)
			}
			if (d.RetryAt != nil) != tc.retry {
				t.Fatalf("document retry_at = %v", d.RetryAt)
			}
			latest, _ := r.eng.Latest()
			if want, _ := r.eng.Get(good.RunID); string(latest) != string(want) {
				t.Fatal("a skipped run must not become the latest result")
			}
			if len(r.pub.events()) != before || r.fence.enables != 1 || len(r.rest.restoreCalls()) != 1 {
				t.Fatal("a skipped run must publish nothing, fence nothing and restore nothing")
			}
			if _, running := r.eng.Running(); running {
				t.Fatal("a skipped run must free the slot")
			}
			if (r.eng.block.deferral() != nil) != tc.retry {
				t.Fatalf("deferral = %+v, want retry %v", r.eng.block.deferral(), tc.retry)
			}
			// last_run_at and last_result describe real drills only.
			if string(r.state.block(t)["last_run_at"]) != `"2026-11-01T04:35:12Z"` {
				t.Fatalf("a skipped run touched last_run_at: %s", r.state.block(t)["last_run_at"])
			}
		})
	}
}

func TestScheduledRunWhileADrillRunsIsSkippedWithoutRetry(t *testing.T) {
	r := newRig(t)
	g := newGate()
	r.add(g.check("drl.hang", 1))
	first, _ := r.eng.Start(context.Background(), StartRequest{})
	<-g.started
	res, err := r.eng.Start(context.Background(), StartRequest{Trigger: TriggerScheduled})
	if err != nil || res.State != StartSkipped || res.Code != doctor.DRL002 || res.RetryAt != nil {
		t.Fatalf("scheduled during a drill = %+v, %v", res, err)
	}
	if id, running := r.eng.Running(); !running || id != first.RunID {
		t.Fatal("the running drill must be untouched")
	}
	if r.eng.block.deferral() != nil {
		t.Fatal("another drill is not a reason to wait")
	}
	close(g.release)
	r.wait()
}

func TestRunStartsWhenTheCauseIsGone(t *testing.T) {
	r := newRig(t)
	r.add(passing("drl.a", 1))
	r.mode.set(modes.Survival)
	if _, err := r.eng.Start(context.Background(), StartRequest{}); err == nil {
		t.Fatal("survival must refuse")
	}
	r.mode.set(modes.Offline)
	res, err := r.eng.Start(context.Background(), StartRequest{})
	if err != nil || res.State != StartRunning {
		t.Fatalf("offline mode is fine: %+v, %v", res, err)
	}
	r.wait()
	if got := r.pub.types(); got[0] != events.TypeDrillStarted {
		t.Fatalf("events = %v", got)
	}
	if d := r.doc(res.RunID); d.Mode != "offline" {
		t.Fatalf("document mode = %q, want the committed mode", d.Mode)
	}
}
