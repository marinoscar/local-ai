package ensure

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/audit"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/kit"
)

var bg = context.Background()

func (r *rig) startManual(confirm ...string) (View, bool) {
	r.t.Helper()
	v, started, err := r.svc.Start(bg, StartRequest{Confirm: confirm, Actor: audit.Admin(audit.ViaSocket), Lang: "en"})
	if err != nil {
		r.t.Fatal(err)
	}
	r.svc.Wait()
	return v, started
}

func eq(t *testing.T, got, want []string) {
	t.Helper()
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("\n got %q\nwant %q", got, want)
	}
}

func TestAPassInstallsEverythingOneAtATimeInOrder(t *testing.T) {
	r := newRig(t)
	r.machine.active["kiwix"] = true // the library service runs, so a replaced kiwix-tools waits for a restart
	r.build()
	v, started := r.startManual()
	if !started || v.State != StateInstalling || v.Progress.Total != 5 {
		t.Fatalf("start: started=%v %+v", started, v)
	}
	eq(t, r.inst.Calls(), []string{
		"packages:/srv/localai/kit:cmake,curl,git",
		"tool:kiwix-tools:/srv/localai/kit",
		"tool:pmtiles:/srv/localai/kit",
		"tool:piper:/srv/localai/kit",
		"restore:/srv/localai/kit/prebuilt/whisper.tar.zst",
	})
	end := r.svc.View(bg, "en")
	if end.State != StateNeedsYou || end.Steps[len(end.Steps)-1].ID != IDRestartKiwix {
		// kiwix-tools was installed, so the running library service waits for a restart that needs a confirmation
		t.Fatalf("after the pass: %s %v", end.State, end.Message)
	}
	if end.Run == nil || end.Run.Result != ResultOK || end.Run.Trigger != TriggerManual {
		t.Errorf("run %+v", end.Run)
	}
	for _, s := range end.Steps {
		if s.ID != IDRestartKiwix && s.Status != StatusOK {
			t.Errorf("%s is %s", s.ID, s.Status)
		}
	}
}

func TestASecondPassChangesNothing(t *testing.T) {
	r := newRig(t).build()
	r.startManual()
	before := r.inst.Calls()
	r.machine.active = map[string]bool{} // the library service is not running: no restart to offer
	_, started := r.startManual()
	if started {
		t.Error("a pass was started with nothing to do")
	}
	if len(r.inst.Calls()) != len(before) {
		t.Errorf("a second pass installed again: %v", r.inst.Calls()[len(before):])
	}
	if v := r.svc.View(bg, "en"); v.State != StateComplete {
		t.Errorf("state %s: %s", v.State, v.Message)
	}
}

func TestAFailedStepIsTriedAgainAtTheNextPassNotInALoop(t *testing.T) {
	r := newRig(t).build()
	r.inst.fail["tool:pmtiles"] = codedErr{doctor.KIT047}
	r.startManual()

	if n := countPrefix(r.inst.Calls(), "tool:pmtiles"); n != 1 {
		t.Fatalf("pmtiles was tried %d times in one pass", n)
	}
	v := r.svc.View(bg, "en")
	if v.State != StateNeedsYou || !strings.HasPrefix(v.Message, "Needs you: pmtiles could not be installed") {
		t.Fatalf("%s / %s", v.State, v.Message)
	}
	if v.Run.Result != ResultPartial {
		t.Errorf("result %s", v.Run.Result)
	}
	var failed ViewStep
	for _, s := range v.Steps {
		if s.ID == IDPmtiles {
			failed = s
		}
	}
	if failed.Status != StatusFailed || failed.Code != doctor.KIT047 || failed.Error == "" {
		t.Errorf("failed step %+v", failed)
	}
	if v.NextAction.Kind != NextFix || v.NextAction.Step != IDPmtiles {
		t.Errorf("next %+v", v.NextAction)
	}
	if !r.alerts.Raised(doctor.KIT051) {
		t.Error("LAI-KIT-051 was not raised")
	}
	// The next pass tries only what is still missing, and succeeds.
	delete(r.inst.fail, "tool:pmtiles")
	r.startManual()
	if n := countPrefix(r.inst.Calls(), "tool:pmtiles"); n != 2 {
		t.Errorf("pmtiles was tried %d times after two passes", n)
	}
	if n := countPrefix(r.inst.Calls(), "tool:kiwix-tools"); n != 1 {
		t.Errorf("kiwix-tools was installed %d times", n)
	}
	if r.alerts.Raised(doctor.KIT051) {
		t.Error("LAI-KIT-051 was not cleared")
	}
}

func countPrefix(calls []string, prefix string) int {
	n := 0
	for _, c := range calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

func TestEnsureAutoOnlyReportsWhenItIsAskOrOff(t *testing.T) {
	for _, tc := range []struct {
		auto  Auto
		state State
	}{{AutoAsk, StateNeedsYou}, {AutoOff, StateOff}} {
		t.Run(string(tc.auto), func(t *testing.T) {
			r := newRig(t)
			r.auto = tc.auto
			r.build()
			// An automatic pass (a sync that ended, the start of the agent) installs nothing.
			if ok, _ := r.svc.fire(bg, TriggerSync); !ok {
				t.Fatal("the pass did not run")
			}
			r.svc.Wait()
			if calls := r.inst.Calls(); len(calls) != 0 {
				t.Fatalf("installed with ensure.auto=%s: %v", tc.auto, calls)
			}
			v := r.svc.View(bg, "en")
			if v.State != tc.state || v.Auto != tc.auto {
				t.Errorf("state %s auto %s: %s", v.State, v.Auto, v.Message)
			}
			if tc.auto == AutoOff && !strings.Contains(v.Message, "Automatic install is off") {
				t.Errorf("message %q", v.Message)
			}
			for _, s := range v.Steps {
				if s.Status != StatusWaiting {
					t.Errorf("%s is %s, want waiting", s.ID, s.Status)
				}
			}
			if !r.alerts.Raised(doctor.KIT052) {
				t.Error("LAI-KIT-052 was not raised")
			}
			// The button (a manual pass) does install.
			_, started := r.startManual()
			if !started || len(r.inst.Calls()) != 5 {
				t.Errorf("the button did not install: started=%v %v", started, r.inst.Calls())
			}
		})
	}
}

func TestAnAutomaticPassInstallsWhatIsPendingAndNeverWhatNeedsAConfirmation(t *testing.T) {
	r := newRig(t)
	r.kit.prebuiltErr, r.kit.src = ErrKitMissing, true // only a compile would give the speech program
	r.build()
	if ok, _ := r.svc.fire(bg, TriggerStart); !ok {
		t.Fatal("not run")
	}
	r.svc.Wait()
	for _, c := range r.inst.Calls() {
		if c == "build" || strings.HasPrefix(c, "restart") {
			t.Fatalf("an automatic pass ran %q", c)
		}
	}
	if len(r.inst.Calls()) != 4 {
		t.Errorf("calls %v", r.inst.Calls())
	}
	v := r.svc.View(bg, "en")
	if v.State != StateNeedsYou || v.NextAction.Kind != NextConfirm || v.NextAction.Step != IDWhisperBuild {
		t.Errorf("%s %+v", v.State, v.NextAction)
	}
	if !r.alerts.Raised(doctor.KIT052) {
		t.Error("the confirmation is a reason for LAI-KIT-052")
	}
}

func TestCompilingAndRestartingNeedTheCallerToConfirmThem(t *testing.T) {
	setup := func() *rig {
		r := newRig(t)
		r.kit.prebuiltErr, r.kit.src = ErrKitMissing, true
		r.wants.Packages = nil
		r.machine.tools[kit.ToolKiwix] = "3.6.0"
		r.machine.tools[kit.ToolPmtiles] = "1.22"
		r.machine.tools[kit.ToolPiper] = "2023.11.14-2"
		return r.build()
	}
	t.Run("a button press without a confirmation does not compile", func(t *testing.T) {
		r := setup()
		r.startManual() // installs the kiwix update only
		for _, c := range r.inst.Calls() {
			if c == "build" {
				t.Fatal("compiled without a confirmation")
			}
		}
		if len(r.inst.Calls()) != 1 {
			t.Errorf("calls %v", r.inst.Calls())
		}
	})
	t.Run("a confirmation runs exactly that step", func(t *testing.T) {
		r := setup()
		r.startManual()
		r.startManual(IDWhisperBuild)
		eq(t, r.inst.Calls(), []string{"tool:kiwix-tools:/srv/localai/kit", "build"})
		if !r.machine.whisper {
			t.Error("the fake build did not run")
		}
	})
	t.Run("the replaced library program is restarted only when confirmed, and then it is done with", func(t *testing.T) {
		r := setup()
		r.machine.active["kiwix"] = true
		r.startManual()
		v := r.svc.View(bg, "en")
		var restart ViewStep
		for _, s := range v.Steps {
			if s.ID == IDRestartKiwix {
				restart = s
			}
		}
		if restart.Status != StatusNeedsConfirm || !restart.NeedsConfirmation {
			t.Fatalf("restart step %+v", restart)
		}
		if got := r.journal.PendingRestart(); len(got) != 1 || got[0] != "kiwix" {
			t.Fatalf("pending restart %v", got)
		}
		r.startManual(IDRestartKiwix)
		if got := r.journal.PendingRestart(); len(got) != 0 {
			t.Errorf("pending restart %v after the restart", got)
		}
		if last := r.inst.Calls()[len(r.inst.Calls())-1]; last != "restart:kiwix" {
			t.Errorf("calls %v", r.inst.Calls())
		}
	})
	t.Run("a pending restart of a service that is not running is dropped", func(t *testing.T) {
		r := setup()
		r.machine.active["kiwix"] = false
		r.startManual()
		if v := r.svc.View(bg, "en"); v.State != StateNeedsYou {
			// only the compile still waits
			t.Logf("state %s", v.State)
		}
		for _, s := range r.svc.View(bg, "en").Steps {
			if s.ID == IDRestartKiwix {
				t.Error("a restart is offered for a service that is not running")
			}
		}
	})
	t.Run("a confirmation that names nothing waiting is refused", func(t *testing.T) {
		r := setup()
		for id, reason := range map[string]string{"shell.run": "unknown_step", IDKiwix: "not_waiting", IDRestartKiwix: "not_waiting"} {
			_, _, err := r.svc.Start(bg, StartRequest{Confirm: []string{id}})
			var ce *ConfirmError
			if !errors.As(err, &ce) || ce.Reason != reason {
				t.Errorf("%s: %v", id, err)
			}
		}
		if len(r.inst.Calls()) != 0 {
			t.Errorf("calls %v", r.inst.Calls())
		}
	})
}

func TestDryRunChangesNothing(t *testing.T) {
	r := newRig(t)
	r.auto = AutoAsk
	r.build()
	v, started, err := r.svc.Start(bg, StartRequest{DryRun: true, Lang: "en"})
	if err != nil || started {
		t.Fatalf("started=%v err=%v", started, err)
	}
	if !v.DryRun || len(v.Steps) != 5 || v.Run != nil {
		t.Errorf("%+v", v)
	}
	for _, s := range v.Steps {
		if s.Status != StatusPending {
			t.Errorf("a manual run would do %s now, the dry run says %s", s.ID, s.Status)
		}
	}
	if len(r.inst.Calls()) != 0 {
		t.Errorf("calls %v", r.inst.Calls())
	}
	if _, _, ok := r.journal.Last(); ok {
		t.Error("a dry run wrote the journal")
	}
	if len(r.audit.Events()) != 0 {
		t.Error("a dry run was audited")
	}
}

func TestCancelStopsTheRunningStepAndSkipsTheRest(t *testing.T) {
	r := newRig(t)
	r.inst.block["tool:kiwix-tools"] = true
	r.inst.started = make(chan string, 8)
	r.build()
	if _, started, err := r.svc.Start(bg, StartRequest{Lang: "en"}); err != nil || !started {
		t.Fatalf("started=%v err=%v", started, err)
	}
	for k := range r.inst.started { // wait until kiwix-tools is the step that runs
		if k == "tool:kiwix-tools" {
			break
		}
	}
	live := r.svc.View(bg, "en")
	if live.State != StateInstalling || live.Progress.Current != IDKiwix || live.Progress.Done != 1 || live.Progress.Total != 5 {
		t.Fatalf("live %+v", live.Progress)
	}
	if !strings.Contains(live.Message, "(2 of 5)") {
		t.Errorf("message %q", live.Message)
	}
	v := r.svc.Cancel(bg, "en")
	if v.State == StateInstalling {
		t.Errorf("still installing after cancel: %+v", v.Progress)
	}
	if _, _, ok := (func() (Run, []Step, bool) { return r.journal.Last() })(); !ok {
		t.Fatal("no journal")
	}
	run, steps, _ := r.journal.Last()
	if run.Result != ResultCancelled || run.FinishedAt == nil {
		t.Errorf("run %+v", run)
	}
	got := map[string]Status{}
	for _, s := range steps {
		got[s.ID] = s.Status
	}
	if got[IDPackages] != StatusDone || got[IDKiwix] != StatusCancelled || got[IDPmtiles] != StatusCancelled || got[IDWhisperRestore] != StatusCancelled {
		t.Errorf("statuses %v", got)
	}
	eq(t, r.inst.Calls(), []string{"packages:/srv/localai/kit:cmake,curl,git", "tool:kiwix-tools:/srv/localai/kit"})
	// Cancelling again, with nothing running, is not an error.
	if again := r.svc.Cancel(bg, "en"); again.State == StateInstalling {
		t.Error("state after a second cancel")
	}
}

func TestACrashLeavesAJournalThatTheNextStartClosesAndTheNextPassRecoversFrom(t *testing.T) {
	r := newRig(t)
	path := filepath.Join(t.TempDir(), "ensure", JournalFile)
	j, _, err := OpenJournal(nil, path, r.clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	// The agent died while piper was being installed: tools before it are done.
	steps := []Step{
		{ID: IDKiwix, Kind: KindTool, Target: kit.ToolKiwix, Status: StatusDone},
		{ID: IDPmtiles, Kind: KindTool, Target: kit.ToolPmtiles, Status: StatusDone},
		{ID: IDPiper, Kind: KindTool, Target: kit.ToolPiper, Status: StatusRunning},
		{ID: IDWhisperRestore, Kind: KindWhisperRestore, Target: "whisper", Status: StatusPending},
	}
	if err := j.Begin(Run{ID: "ens-crashed", Trigger: TriggerSync, StartedAt: r.clock.Now()}, steps); err != nil {
		t.Fatal(err)
	}
	if err := j.SetPendingRestart("kiwix", true); err != nil {
		t.Fatal(err)
	}

	j2, recovered, err := OpenJournal(nil, path, r.clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	if recovered == nil || recovered.ID != "ens-crashed" {
		t.Fatalf("recovered %+v", recovered)
	}
	run, got, _ := j2.Last()
	if run.Result != ResultFailed || run.FinishedAt == nil {
		t.Errorf("run %+v", run)
	}
	st := map[string]Status{}
	for _, s := range got {
		st[s.ID] = s.Status
	}
	if st[IDKiwix] != StatusDone || st[IDPiper] != StatusFailed || st[IDWhisperRestore] != StatusCancelled {
		t.Errorf("statuses %v", st)
	}
	if rp := j2.PendingRestart(); len(rp) != 1 {
		t.Errorf("the restart that was owed is forgotten: %v", rp)
	}
	// The machine itself says what is left: kiwix-tools and pmtiles are in place; piper and whisper are not.
	r.machine.tools = map[string]string{kit.ToolKiwix: "3.7.0", kit.ToolPmtiles: "1.22"}
	r.machine.missing = nil
	r.journal = j2
	r.svc = r.service(j2)
	r.startManual()
	eq(t, r.inst.Calls(), []string{"tool:piper:/srv/localai/kit", "restore:/srv/localai/kit/prebuilt/whisper.tar.zst"})
}

func TestADamagedJournalIsAnEmptyOne(t *testing.T) {
	r := newRig(t)
	path := filepath.Join(t.TempDir(), JournalFile)
	if err := os.WriteFile(path, []byte("{not json"), 0o640); err != nil {
		t.Fatal(err)
	}
	j, recovered, err := OpenJournal(nil, path, r.clock.Now())
	if err != nil || recovered != nil {
		t.Fatalf("%v %v", recovered, err)
	}
	if _, _, ok := j.Last(); ok {
		t.Error("a damaged journal has a run")
	}
}

func TestAPassWaitsWhileASyncRunsOrAModeChanges(t *testing.T) {
	r := newRig(t).build()
	for _, gate := range []error{ErrSyncRunning, ErrModeChanging} {
		r.gate = gate
		if _, _, err := r.svc.Start(bg, StartRequest{}); !errors.Is(err, gate) {
			t.Errorf("Start with %v: %v", gate, err)
		}
		if ok, wait := r.svc.fire(bg, TriggerSync); ok || wait <= 0 {
			t.Errorf("fire with %v: ok=%v wait=%v", gate, ok, wait)
		}
	}
	if len(r.inst.Calls()) != 0 {
		t.Errorf("calls %v", r.inst.Calls())
	}
	r.gate = nil
	if ok, _ := r.svc.fire(bg, TriggerSync); !ok {
		t.Fatal("the pass did not run once the gate opened")
	}
	r.svc.Wait()
	if len(r.inst.Calls()) != 5 {
		t.Errorf("calls %v", r.inst.Calls())
	}
}

func TestATriggerStartsAnAutomaticPassAfterItsDelay(t *testing.T) {
	r := newRig(t).build()
	ctx, cancel := context.WithCancel(bg)
	done := make(chan struct{})
	go func() { r.svc.Run(ctx); close(done) }()
	r.svc.Trigger(TriggerSync, 10*time.Millisecond)
	r.svc.Trigger(TriggerOnline, 10*time.Millisecond) // merged into the first
	if v := r.svc.View(bg, "en"); v.State != StateInstalling {
		t.Errorf("a pass is scheduled: %s", v.State)
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(r.inst.Calls()) < 5 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	r.svc.Wait()
	cancel()
	<-done
	if len(r.inst.Calls()) != 5 {
		t.Fatalf("calls %v", r.inst.Calls())
	}
	run, _, _ := r.journal.Last()
	if run.Trigger != TriggerSync {
		t.Errorf("trigger %s (a sync that ended names the pass)", run.Trigger)
	}
	if n := countRuns(r.audit.Events()); n != 1 {
		t.Errorf("%d passes ran for two triggers", n)
	}
	for _, e := range r.audit.Events() {
		if e.Action == ActionRun && e.Actor.Kind != audit.KindSystem {
			t.Errorf("the actor of an automatic pass is %s", e.Actor.Kind)
		}
	}
}

func countRuns(evs []audit.Event) int {
	n := 0
	for _, e := range evs {
		if e.Action == ActionRun {
			n++
		}
	}
	return n
}

func TestAnAutomaticPassWithNothingToDoWritesNothing(t *testing.T) {
	r := newRig(t)
	r.machine.tools = map[string]string{kit.ToolKiwix: "3.7.0", kit.ToolPmtiles: "1.22", kit.ToolPiper: "2023.11.14-2"}
	r.machine.missing, r.machine.whisper = nil, true
	r.build()
	r.svc.fire(bg, TriggerStart)
	r.svc.Wait()
	if _, _, ok := r.journal.Last(); ok {
		t.Error("the journal has a run")
	}
	if len(r.audit.Events()) != 0 || len(r.inst.Calls()) != 0 {
		t.Errorf("audit %v calls %v", r.audit.Events(), r.inst.Calls())
	}
	if v := r.svc.View(bg, "en"); v.State != StateComplete {
		t.Errorf("state %s", v.State)
	}
}

func TestAuditTrailOfAPass(t *testing.T) {
	r := newRig(t).build()
	r.inst.fail["tool:piper"] = errors.New("nope")
	r.startManual()
	var steps, runs int
	var run audit.Event
	for _, e := range r.audit.Events() {
		switch e.Action {
		case ActionStep:
			steps++
			if e.Actor.Kind != audit.KindAdmin || e.Target["run_id"] != "ens-test" {
				t.Errorf("step event %+v", e)
			}
		case ActionRun:
			runs++
			run = e
		}
	}
	if steps != 5 || runs != 1 {
		t.Fatalf("%d step lines, %d run lines", steps, runs)
	}
	if run.Result != audit.ResultFail || run.Code != doctor.KIT051 || run.Target["done"] != "4" || run.Target["failed"] != "1" || run.Target["result"] != "partial" {
		t.Errorf("run event %+v", run)
	}
}

func TestTheWordsInEnglishAndSpanish(t *testing.T) {
	r := newRig(t)
	r.auto = AutoAsk
	r.build()
	en := r.svc.View(bg, "en")
	es := r.svc.View(bg, "es-CR")
	if en.Message == es.Message || !strings.HasPrefix(en.Message, "Needs you: ") || !strings.HasPrefix(es.Message, "Necesita su ayuda: ") {
		t.Errorf("\n%s\n%s", en.Message, es.Message)
	}
	if en.NextAction.Kind != NextPressButton || en.NextAction.Text == es.NextAction.Text {
		t.Errorf("%+v / %+v", en.NextAction, es.NextAction)
	}
	for i := range en.Steps {
		if en.Steps[i].Title == es.Steps[i].Title || en.Steps[i].Detail == "" || es.Steps[i].Detail == "" {
			t.Errorf("step %s: %q / %q", en.Steps[i].ID, en.Steps[i].Title, es.Steps[i].Title)
		}
	}
	// Complete and installing, too.
	for _, state := range []State{StateComplete, StateInstalling, StateOff, StateNeedsYou} {
		steps := []Step{{ID: IDKiwix, Status: StatusWaiting}}
		a := messageFor(state, steps, Progress{Done: 1, Total: 3}, "en")
		b := messageFor(state, steps, Progress{Done: 1, Total: 3}, "es")
		if a == b || a == "" || b == "" {
			t.Errorf("%s: %q / %q", state, a, b)
		}
	}
	// A blocked kit item: the next action is a sync, never the button.
	rb := newRig(t)
	rb.kit.tools = map[string]toolOffer{}
	rb.wants = Wants{Tools: map[string]bool{kit.ToolKiwix: true}}
	rb.build()
	vb := rb.svc.View(bg, "en")
	if vb.State != StateNeedsYou || vb.NextAction.Kind != NextSync || !strings.Contains(vb.Message, "the kit does not have kiwix-tools") {
		t.Errorf("%s / %s / %+v", vb.State, vb.Message, vb.NextAction)
	}
}

func TestAnErrorWhilePlanningIsAStateNotAPanic(t *testing.T) {
	r := newRig(t)
	r.machine.pkgErr = errors.New("dpkg-query is missing")
	r.build()
	v := r.svc.View(bg, "en")
	if v.State != StateNeedsYou || !strings.Contains(v.Message, "could not check") || v.Steps == nil {
		t.Errorf("%+v", v)
	}
	if _, _, err := r.svc.Start(bg, StartRequest{}); err == nil {
		t.Error("Start should report the planning error")
	}
}

func TestStartWhileAPassRunsReturnsThatPass(t *testing.T) {
	r := newRig(t)
	r.inst.block["tool:kiwix-tools"] = true
	r.inst.started = make(chan string, 8)
	r.build()
	if _, started, _ := r.svc.Start(bg, StartRequest{}); !started {
		t.Fatal("not started")
	}
	for k := range r.inst.started {
		if k == "tool:kiwix-tools" {
			break
		}
	}
	v, started, err := r.svc.Start(bg, StartRequest{Lang: "en"})
	if err != nil || started || v.State != StateInstalling {
		t.Errorf("second start: started=%v err=%v state=%s", started, err, v.State)
	}
	r.svc.Cancel(bg, "en")
}

func TestThePackageStaysOffline(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		b, _ := os.ReadFile(e.Name())
		for _, bad := range []string{`"net"`, `"net/http"`, `"os/exec"`, `"database/sql"`} {
			if strings.Contains(string(b), bad) {
				t.Errorf("%s imports %s: this package decides, the host package acts", e.Name(), bad)
			}
		}
	}
}
