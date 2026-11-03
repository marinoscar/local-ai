package drill

import (
	"context"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/events"
)

// alertBus records the alert events the doctor manager publishes.
type alertBus struct {
	mu  sync.Mutex
	log []string
}

func (b *alertBus) Publish(typ, _ string, data any) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	a := data.(events.Alert)
	b.log = append(b.log, typ+" "+a.Code+" "+a.Severity)
	return nil
}

func (b *alertBus) events() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.log...)
}

// alertRig is an engine whose observer is a reporter, and a doctor manager over the alert checks.
type alertRig struct {
	*rig
	rep *Reporter
	mgr *doctor.Manager
	bus *alertBus
	// outcome is the result the "drl.probe" check returns.
	mu      sync.Mutex
	outcome Outcome
	// hold, when set, makes the "drl.hold" check wait until the run is aborted.
	hold *gate
}

// holdNext makes the next run hang in drl.hold, and returns the gate that says it started.
func (a *alertRig) holdNext() *gate {
	g := newGate()
	a.mu.Lock()
	a.hold = g
	a.mu.Unlock()
	return g
}

func (a *alertRig) holding() *gate {
	a.mu.Lock()
	defer a.mu.Unlock()
	g := a.hold
	a.hold = nil
	return g
}

// abortHeldRun aborts the run that hangs in drl.hold and waits for it to end.
func (a *alertRig) abortHeldRun(g *gate) {
	a.t.Helper()
	<-g.started
	if _, err := a.eng.Abort(""); err != nil {
		a.t.Fatal(err)
	}
	a.wait()
	a.mgr.Tick(context.Background())
}

func (a *alertRig) set(res Result, code string) {
	a.mu.Lock()
	a.outcome = Outcome{Result: res, Code: code}
	a.mu.Unlock()
}

func newAlertRig(t *testing.T) *alertRig {
	t.Helper()
	a := &alertRig{bus: &alertBus{}}
	r := newRig(t, func(r *rig, d *Deps) {
		rep, err := NewReporter(ReporterOptions{Results: r.results, State: r.state, Clock: r.clock})
		if err != nil {
			t.Fatal(err)
		}
		a.rep = rep
		d.Observer = rep
	})
	a.rig = r
	r.add(Check{ID: "drl.probe", Area: "test", Order: 1, Critical: true, Run: func(context.Context, *Env) Outcome {
		a.mu.Lock()
		defer a.mu.Unlock()
		return a.outcome
	}})
	r.add(Check{ID: "drl.hold", Area: "test", Order: 2, Run: func(ctx context.Context, env *Env) Outcome {
		g := a.holding()
		if g == nil {
			return Outcome{Result: ResultPass}
		}
		return g.check("drl.hold", 2).Run(ctx, env)
	}})
	reg := doctor.NewRegistry()
	if err := RegisterAlertChecks(reg, a.rep, r.clock); err != nil {
		t.Fatal(err)
	}
	a.mgr = doctor.NewManager(doctor.ManagerOptions{Registry: reg, Bus: a.bus, Now: r.clock.Now})
	if err := a.rep.Load(); err != nil {
		t.Fatal(err)
	}
	a.set(ResultPass, "")
	return a
}

// run starts a run of the given trigger and waits for it; the doctor manager ticks after it, like its
// 60-second pass would.
func (a *alertRig) run(trigger Trigger) *Document {
	a.t.Helper()
	_, d := a.startWait(StartRequest{Trigger: trigger})
	a.mgr.Tick(context.Background())
	return d
}

func (a *alertRig) active() map[string]doctor.Alert {
	out := map[string]doctor.Alert{}
	for _, al := range a.mgr.Active() {
		out[al.Code] = al
	}
	return out
}

func idsOf(v any) string {
	list, _ := v.([]any)
	var out []string
	for _, x := range list {
		out = append(out, x.(string))
	}
	return strings.Join(out, ",")
}

func TestAlertRaiseAndClearTransitions(t *testing.T) {
	a := newAlertRig(t)

	// Nothing ran yet, nothing to say.
	a.mgr.Tick(context.Background())
	if len(a.active()) != 0 {
		t.Fatalf("alerts = %+v", a.active())
	}

	// A failed run raises LAI-DRL-001 as an error with the failing check ids and codes.
	a.set(ResultFail, doctor.DRL030)
	failed := a.run(TriggerScheduled)
	al, ok := a.active()[doctor.DRL001]
	if !ok || al.Severity != doctor.Error {
		t.Fatalf("alerts = %+v", a.active())
	}
	if al.Details["run_id"] != failed.RunID || al.Details["overall"] != "fail" || idsOf(al.Details["failed_check_ids"]) != "drl.probe" || idsOf(al.Details["failed_checks"]) != doctor.DRL030 {
		t.Fatalf("details = %+v", al.Details)
	}
	if got := al.In("es"); got.Title != "El último simulacro encontró problemas" {
		t.Fatalf("Spanish title = %q", got.Title)
	}

	// A warning run keeps the code and lowers it to a warning (announced again: the severity changed).
	a.set(ResultWarn, doctor.DRL031)
	a.clock.Advance(time.Hour)
	a.run(TriggerManual)
	al = a.active()[doctor.DRL001]
	if al.Severity != doctor.Warning || idsOf(al.Details["failed_checks"]) != doctor.DRL031 || al.Details["overall"] != "warn" {
		t.Fatalf("alert = %+v", al)
	}

	// The next passing run clears it.
	a.set(ResultPass, "")
	a.clock.Advance(time.Hour)
	a.run(TriggerScheduled)
	if len(a.active()) != 0 {
		t.Fatalf("the passing run must clear the alert: %+v", a.active())
	}
	want := []string{
		"alert.raised " + doctor.DRL001 + " error",
		"alert.raised " + doctor.DRL001 + " warning",
		"alert.cleared " + doctor.DRL001 + " warning",
	}
	if got := a.bus.events(); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("alert events = %v\nwant %v", got, want)
	}
}

func TestAnAbortedRunAndAManualPlugEntryNeitherRaiseNorClearTheAlert(t *testing.T) {
	a := newAlertRig(t)

	// A pass run, then an aborted one: no alert (an aborted run is not a drill result).
	a.run(TriggerScheduled)
	g := a.holdNext()
	res, err := a.eng.Start(context.Background(), StartRequest{Trigger: TriggerManual})
	if err != nil {
		t.Fatal(err)
	}
	a.abortHeldRun(g)
	if d := a.doc(res.RunID); d.State != StateAborted {
		t.Fatalf("document = %+v", d)
	}
	if len(a.active()) != 0 {
		t.Fatalf("an aborted run must not raise: %+v", a.active())
	}

	// A failed run raises; an aborted run after it does not clear; a passing manual_plug entry does not clear.
	a.set(ResultFail, doctor.DRL030)
	a.clock.Advance(time.Hour)
	a.run(TriggerScheduled)
	if _, ok := a.active()[doctor.DRL001]; !ok {
		t.Fatal("the failed run must raise")
	}
	g = a.holdNext()
	if _, err := a.eng.Start(context.Background(), StartRequest{Trigger: TriggerManual}); err != nil {
		t.Fatal(err)
	}
	a.abortHeldRun(g)
	if _, ok := a.active()[doctor.DRL001]; !ok {
		t.Fatal("an aborted run must not clear the alert")
	}
	a.clock.Advance(time.Hour)
	if _, err := a.eng.RecordManual(context.Background(), ManualRequest{Result: ManualPass}); err != nil {
		t.Fatal(err)
	}
	a.mgr.Tick(context.Background())
	if _, ok := a.active()[doctor.DRL001]; !ok {
		t.Fatal("a manual_plug entry must not clear the alert")
	}

	// And a failed manual_plug entry raises nothing on a clean node.
	b := newAlertRig(t)
	b.run(TriggerScheduled)
	if _, err := b.eng.RecordManual(context.Background(), ManualRequest{Result: ManualFail}); err != nil {
		t.Fatal(err)
	}
	b.mgr.Tick(context.Background())
	if len(b.active()) != 0 {
		t.Fatalf("a manual_plug failure is reported to the cloud, not raised here: %+v", b.active())
	}
}

func TestAlertIsRaisedAtOnceWhenTheReporterNotifiesTheManager(t *testing.T) {
	a := newAlertRig(t)
	// The daemon wiring: OnChange ticks the doctor manager, so no 60-second wait.
	rep, err := NewReporter(ReporterOptions{Results: a.results, State: a.state, Clock: a.clock,
		OnChange: func() { a.mgr.Tick(context.Background()) }})
	if err != nil {
		t.Fatal(err)
	}
	a.rep.Observe(finishedDoc("drl-20261101T000000Z-0001", TriggerScheduled, OverallPass, t0.Add(-time.Hour), chkPass))
	// Swap the source the checks read: a manager over the new reporter.
	reg := doctor.NewRegistry()
	if err := RegisterAlertChecks(reg, rep, a.clock); err != nil {
		t.Fatal(err)
	}
	a.mgr = doctor.NewManager(doctor.ManagerOptions{Registry: reg, Bus: a.bus, Now: a.clock.Now})
	rep.Observe(finishedDoc("drl-20261101T043512Z-0002", TriggerScheduled, OverallFail, t0, chkFail))
	al, ok := a.active()[doctor.DRL001]
	if !ok || al.Severity != doctor.Error || idsOf(al.Details["failed_check_ids"]) != "drl.kiwix.search" {
		t.Fatalf("alerts = %+v", a.active())
	}
	rep.Observe(finishedDoc("drl-20261102T043512Z-0003", TriggerScheduled, OverallPass, t0.Add(24*time.Hour), chkPass))
	if len(a.active()) != 0 {
		t.Fatalf("alerts = %+v", a.active())
	}
}

func TestAlertFreshnessRaisesAfter45DaysAndAnyFinishedRunClearsIt(t *testing.T) {
	a := newAlertRig(t) // Load stamped watch_since = t0
	tick := func() map[string]doctor.Alert {
		a.mgr.Tick(context.Background())
		return a.active()
	}

	a.clock.Advance(45*24*time.Hour - time.Second)
	if got := tick(); len(got) != 0 {
		t.Fatalf("44 days 23:59:59 is not stale: %+v", got)
	}
	a.clock.Advance(time.Second)
	al, ok := tick()[doctor.DRL006]
	if !ok || al.Severity != doctor.Warning || al.Details["days"] != 45 || al.Details["since"] != "2026-11-01T04:35:12Z" {
		t.Fatalf("alerts = %+v", a.active())
	}
	if got := al.In("es"); got.Title != "Ningún simulacro en 45 días" {
		t.Fatalf("Spanish title = %q", got.Title)
	}

	// A manual_plug entry does not count: the timer counts from a run of the drill itself.
	if _, err := a.eng.RecordManual(context.Background(), ManualRequest{Result: ManualPass}); err != nil {
		t.Fatal(err)
	}
	if _, ok := tick()[doctor.DRL006]; !ok {
		t.Fatal("a manual_plug entry must not clear LAI-DRL-006")
	}

	// Any finished run of any trigger (here a manual --quick one) restarts the 45 days.
	a.run(TriggerManual)
	if got := tick(); len(got) != 0 {
		t.Fatalf("a finished run must clear the alert: %+v", got)
	}
	a.clock.Advance(44 * 24 * time.Hour)
	if got := tick(); len(got) != 0 {
		t.Fatalf("alerts = %+v", got)
	}
	a.clock.Advance(24 * time.Hour)
	if _, ok := tick()[doctor.DRL006]; !ok {
		t.Fatal("45 days after the last run it is stale again")
	}
}

func TestAlertsAreDerivedAgainAfterARestart(t *testing.T) {
	a := newAlertRig(t)
	a.set(ResultFail, doctor.DRL030)
	a.run(TriggerScheduled)
	a.clock.Advance(46 * 24 * time.Hour)
	a.mgr.Tick(context.Background())
	if _, ok := a.active()[doctor.DRL001]; !ok {
		t.Fatal("setup: the failed run must raise")
	}
	if _, ok := a.active()[doctor.DRL006]; !ok {
		t.Fatal("setup: 46 days later it must be stale")
	}

	// A new process: new reporter and manager over the same files and state.
	rep, err := NewReporter(ReporterOptions{Results: a.results, State: a.state, Clock: a.clock})
	if err != nil {
		t.Fatal(err)
	}
	if err := rep.Load(); err != nil {
		t.Fatal(err)
	}
	reg := doctor.NewRegistry()
	if err := RegisterAlertChecks(reg, rep, a.clock); err != nil {
		t.Fatal(err)
	}
	mgr := doctor.NewManager(doctor.ManagerOptions{Registry: reg, Now: a.clock.Now})
	mgr.Tick(context.Background())
	var codes []string
	for _, al := range mgr.Active() {
		codes = append(codes, al.Code)
	}
	sort.Strings(codes)
	if strings.Join(codes, ",") != doctor.DRL001+","+doctor.DRL006 {
		t.Fatalf("alerts after the restart = %v", codes)
	}
}

func TestAlertChecksAreFastAndRegisterOnce(t *testing.T) {
	a := newAlertRig(t)
	checks := AlertChecks(a.rep, nil)
	if len(checks) != 2 || checks[0].ID != CheckLastResult || checks[1].ID != CheckFreshness {
		t.Fatalf("checks = %+v", checks)
	}
	for _, c := range checks {
		if !c.Fast || c.Area != "DRL" {
			t.Errorf("%s must be a fast DRL check", c.ID)
		}
	}
	reg := doctor.NewRegistry()
	if err := RegisterAlertChecks(reg, a.rep, nil); err != nil {
		t.Fatal(err)
	}
	if err := RegisterAlertChecks(reg, a.rep, nil); err == nil {
		t.Fatal("registering twice must fail")
	}
	// The check results carry only the two codes of this slice.
	if r := lastResultFindings(a.rep); len(r) != 0 {
		t.Fatalf("results = %+v", r)
	}
}
