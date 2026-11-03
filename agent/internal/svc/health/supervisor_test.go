package health_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/events"
	"github.com/marinoscar/local-ai/agent/internal/services"
	"github.com/marinoscar/local-ai/agent/internal/svc/health"
)

var bg = context.Background()

// kiwixOK returns a rig with kiwix up for an hour and answering, after the first probe.
func kiwixOK(t *testing.T, o rigOpts) *rig {
	t.Helper()
	o.units = append(o.units, "kiwix")
	r := newRig(t, o)
	r.setUnit("kiwix", services.ActiveActive, time.Hour)
	r.probe["kiwix"].set(func(p *fakeProbe) {
		p.readiness = health.Readiness{Detail: "1 books", Books: []health.Book{{ContentID: "book-alpha", Name: "alpha_en_all", Root: "internal"}}}
	})
	r.sup.CheckNow(bg)
	return r
}

func TestOKRecord(t *testing.T) {
	r := kiwixOK(t, rigOpts{})
	rec := r.record("kiwix")
	if rec.State != health.StateOK || !rec.Healthy {
		t.Fatalf("state %s healthy %v", rec.State, rec.Healthy)
	}
	if rec.Unit != services.UnitKiwix || rec.Active != "active" || rec.Sub != "running" || !rec.HasUnit() {
		t.Errorf("unit fields: %+v", rec)
	}
	if rec.Detail != "1 books" || len(rec.Books) != 1 || rec.Books[0].ContentID != "book-alpha" {
		t.Errorf("readiness fields: %+v", rec)
	}
	if !rec.LastCheck.Equal(t0) || !rec.Since.Equal(t0.Add(-time.Hour)) || !rec.Enabled {
		t.Errorf("times: last_check %v since %v enabled %v", rec.LastCheck, rec.Since, rec.Enabled)
	}
	if rec.Problems == nil || len(rec.Problems) != 0 {
		t.Errorf("problems %#v, want an empty non-nil slice", rec.Problems)
	}
	// The first observation is the baseline, so the only event is the probe's starting > ok.
	if got := r.pub.transitions(); !equal(got, []string{"kiwix starting>ok"}) {
		t.Errorf("events %v", got)
	}
}

func TestRestartAfterThreeLivenessFailures(t *testing.T) {
	r := kiwixOK(t, rigOpts{})
	r.pub.reset()
	r.probe["kiwix"].set(func(p *fakeProbe) { p.live = errDown })

	r.tick(30 * time.Second)
	if got := r.state("kiwix"); got != health.StateDegraded {
		t.Fatalf("after 1 failure: %s", got)
	}
	r.tick(30 * time.Second)
	if got := r.state("kiwix"); got != health.StateDegraded || r.restarts(services.UnitKiwix) != 0 {
		t.Fatalf("after 2 failures: %s, %d restarts", got, r.restarts(services.UnitKiwix))
	}
	r.tick(30 * time.Second)
	if n := r.restarts(services.UnitKiwix); n != 1 {
		t.Fatalf("after 3 failures: %d restarts, want 1", n)
	}
	rec := r.record("kiwix")
	if rec.State != health.StateStarting {
		t.Fatalf("after the restart: %s", rec.State)
	}
	if got := r.codes("kiwix"); !equal(got, []string{health.CodeRestarted}) || rec.Problems[0].Severity != doctor.Info {
		t.Errorf("problems after the restart: %+v", rec.Problems)
	}

	// The service answers again: starting > ok, and the restart stays on the record as an info finding.
	r.probe["kiwix"].set(func(p *fakeProbe) { p.live = nil })
	r.tick(10 * time.Second)
	if got := r.state("kiwix"); got != health.StateOK {
		t.Fatalf("after recovery: %s", got)
	}
	if got := r.codes("kiwix"); !equal(got, []string{health.CodeRestarted}) {
		t.Errorf("problems after recovery: %v", got)
	}

	// Exactly one event per transition, with the code only while the new state has a problem.
	want := []string{"kiwix ok>degraded", "kiwix degraded>starting", "kiwix starting>ok"}
	if got := r.pub.transitions(); !equal(got, want) {
		t.Fatalf("events %v, want %v", got, want)
	}
	evs := r.pub.all()
	if evs[0].Code != "" || evs[1].Code != health.CodeRestarted || evs[2].Code != "" {
		t.Errorf("codes %q %q %q", evs[0].Code, evs[1].Code, evs[2].Code)
	}
	if evs[1].Unit != services.UnitKiwix || evs[1].Active != "active" || evs[1].Healthy || !evs[2].Healthy {
		t.Errorf("payload %+v / %+v", evs[1], evs[2])
	}

	// The metric counts the restart and the log line is the documented one.
	if got := r.metrics.Counters()[health.MetricServiceRestart+`{service="kiwix"}`]; got != int64(1) {
		t.Errorf("restart metric %v", got)
	}
	var found bool
	for _, l := range r.logLines() {
		if l["msg"] == "service restarted" {
			found = true
			if l["level"] != "WARN" || l["component"] != "health" || l["service"] != "kiwix" || l["code"] != health.CodeRestarted || l["failures"] != float64(3) {
				t.Errorf("log line %v", l)
			}
		}
	}
	if !found {
		t.Error("no 'service restarted' log line")
	}
}

func TestFailuresWhileStartingAreIgnored(t *testing.T) {
	r := newRig(t, rigOpts{units: []string{"kiwix"}})
	r.probe["kiwix"].set(func(p *fakeProbe) { p.live = errDown })
	r.setUnit("kiwix", services.ActiveActive, 10*time.Second) // started 10 s ago
	r.pub.reset()

	// 4 rounds at 20, 30, 40 and 50 s after the start: all inside the 60 s of grace.
	for i := 0; i < 4; i++ {
		r.tick(10 * time.Second)
		if got := r.state("kiwix"); got != health.StateStarting {
			t.Fatalf("round %d: %s, want starting", i, got)
		}
	}
	if r.restarts(services.UnitKiwix) != 0 || len(r.pub.all()) != 0 {
		t.Fatalf("restarts %d events %v", r.restarts(services.UnitKiwix), r.pub.transitions())
	}
	// 60 s after the start the next failure counts, and starting is over.
	r.tick(10 * time.Second)
	if got := r.state("kiwix"); got != health.StateDegraded {
		t.Fatalf("after the grace: %s", got)
	}
	if r.restarts(services.UnitKiwix) != 0 {
		t.Error("one counted failure restarted the unit")
	}
	// A probe that passes during the grace ends it at once.
	r2 := newRig(t, rigOpts{units: []string{"whisper"}})
	r2.setUnit("whisper", services.ActiveActive, 5*time.Second)
	r2.sup.CheckNow(bg)
	if got := r2.state("whisper"); got != health.StateOK {
		t.Fatalf("a passing probe in the grace: %s", got)
	}
}

func TestBudgetExhaustionLeavesTheServiceDown(t *testing.T) {
	for _, tc := range []struct {
		mode string
		want doctor.Severity
	}{{"survival", doctor.Error}, {"online", doctor.Warning}, {"offline", doctor.Warning}, {"", doctor.Warning}} {
		t.Run("mode "+tc.mode, func(t *testing.T) {
			r := kiwixOK(t, rigOpts{})
			r.mode = tc.mode
			r.probe["kiwix"].set(func(p *fakeProbe) { p.live = errDown })
			for i := 0; i < 60 && r.state("kiwix") != health.StateDown; i++ {
				r.tick(30 * time.Second)
			}
			if got := r.state("kiwix"); got != health.StateDown {
				t.Fatalf("never went down: %s", got)
			}
			if n := r.restarts(services.UnitKiwix); n != health.DefaultMaxRestarts {
				t.Fatalf("%d restarts, want %d", n, health.DefaultMaxRestarts)
			}
			rec := r.record("kiwix")
			if len(rec.Problems) < 2 || rec.Problems[0].Code != health.CodeGaveUp || rec.Problems[0].Severity != tc.want {
				t.Fatalf("problems %+v, want %s first with severity %s", rec.Problems, health.CodeGaveUp, tc.want)
			}
			if rec.Problems[1].Code != health.CodeLibraryNotRunning {
				t.Errorf("second problem %+v", rec.Problems[1])
			}
			last := r.pub.all()[len(r.pub.all())-1]
			if last.To != "down" || last.Code != health.CodeGaveUp || last.Healthy {
				t.Errorf("last event %+v", last)
			}

			// The 4th restart does not happen, however long it fails.
			before := len(r.pub.all())
			for i := 0; i < 12; i++ {
				r.tick(30 * time.Second)
			}
			if n := r.restarts(services.UnitKiwix); n != health.DefaultMaxRestarts {
				t.Errorf("%d restarts after more failures", n)
			}
			if len(r.pub.all()) != before {
				t.Errorf("events while down: %v", r.pub.transitions()[before:])
			}

			// The same finding reaches the doctor with the effective severity.
			var got doctor.Severity
			for _, res := range r.sup.Checks()[0].Run(bg) {
				if res.Code == health.CodeGaveUp {
					got = res.Severity
				}
			}
			if got != tc.want {
				t.Errorf("doctor severity %q, want %q", got, tc.want)
			}

			// The budget is a sliding window: an hour later the service may be restarted again.
			r.probe["kiwix"].set(func(p *fakeProbe) { p.live = errDown })
			r.tick(time.Hour)
			if n := r.restarts(services.UnitKiwix); n != health.DefaultMaxRestarts+1 {
				t.Errorf("%d restarts an hour later, want %d", n, health.DefaultMaxRestarts+1)
			}
		})
	}
}

func TestFailedUnitIsRestartedUnderTheSameBudget(t *testing.T) {
	r := newRig(t, rigOpts{units: []string{"whisper"}})
	r.setUnit("whisper", services.ActiveActive, time.Hour)
	r.sup.CheckNow(bg)
	r.pub.reset()

	for i := 1; i <= health.DefaultMaxRestarts; i++ {
		r.clock.Advance(time.Minute)
		r.setUnit("whisper", services.ActiveFailed, 0)
		if n := r.restarts(services.UnitWhisper); n != i {
			t.Fatalf("failure %d: %d restarts", i, n)
		}
		if got := r.state("whisper"); got != health.StateStarting {
			t.Fatalf("failure %d: state %s after the restart", i, got)
		}
	}
	// The next failure finds the budget spent.
	r.clock.Advance(time.Minute)
	r.setUnit("whisper", services.ActiveFailed, 0)
	if n := r.restarts(services.UnitWhisper); n != health.DefaultMaxRestarts {
		t.Fatalf("%d restarts, want %d", n, health.DefaultMaxRestarts)
	}
	rec := r.record("whisper")
	if rec.State != health.StateDown || rec.Problems[0].Code != health.CodeGaveUp || rec.Problems[1].Code != health.CodeSpeechNotRunning {
		t.Fatalf("record %+v", rec)
	}
	if rec.Active != "failed" {
		t.Errorf("active %q", rec.Active)
	}
	want := []string{"whisper ok>down", "whisper down>starting"}
	for i := 1; i < health.DefaultMaxRestarts; i++ {
		want = append(want, "whisper starting>down", "whisper down>starting")
	}
	want = append(want, "whisper starting>down")
	if got := r.pub.transitions(); !equal(got, want) {
		t.Errorf("events\n got %v\nwant %v", got, want)
	}
}

func TestDisabledIsNeverProbedOrRestarted(t *testing.T) {
	on := false
	r := newRig(t, rigOpts{units: []string{"kiwix"}, enabled: map[string]func() bool{"kiwix": func() bool { return on }}})
	r.setUnit("kiwix", services.ActiveFailed, 0)
	for i := 0; i < 5; i++ {
		r.tick(30 * time.Second)
	}
	r.sup.CheckNow(bg)
	rec := r.record("kiwix")
	if rec.State != health.StateDisabled || len(rec.Problems) != 0 {
		t.Fatalf("record %+v", rec)
	}
	if live, content, ready, deep := r.probe["kiwix"].calls(); live+content+ready+deep != 0 {
		t.Errorf("a disabled service was probed: %d %d %d %d", live, content, ready, deep)
	}
	if r.restarts(services.UnitKiwix) != 0 {
		t.Error("a disabled service was restarted")
	}
	if got := r.sup.Deep(bg); len(got) != 0 {
		t.Errorf("deep check ran on a disabled service: %v", got)
	}

	// Switching it on in node.yaml takes effect on the next tick.
	on = true
	r.setUnit("kiwix", services.ActiveActive, time.Hour)
	r.tick(time.Second)
	if got := r.state("kiwix"); got != health.StateOK {
		t.Fatalf("after enabling: %s", got)
	}
	if got := r.pub.transitions(); !equal(got, []string{"kiwix disabled>ok"}) {
		t.Errorf("events %v", got)
	}
}

func TestMissingContentIsNeverRestarted(t *testing.T) {
	r := newRig(t, rigOpts{units: []string{"kiwix"}})
	r.probe["kiwix"].set(func(p *fakeProbe) { p.content = health.Content{Missing: true, Code: doctor.SVC001} })

	for _, active := range []string{services.ActiveFailed, services.ActiveInactive, services.ActiveActive} {
		r.setUnit("kiwix", active, time.Hour)
		for i := 0; i < 4; i++ {
			r.tick(30 * time.Second)
		}
		rec := r.record("kiwix")
		if rec.State != health.StateMissingContent || len(rec.Problems) != 1 || rec.Problems[0] != (health.Problem{Code: doctor.SVC001, Severity: doctor.Error}) {
			t.Fatalf("unit %s: record %+v", active, rec)
		}
		if r.restarts(services.UnitKiwix) != 0 {
			t.Fatalf("unit %s: restarted", active)
		}
	}
	if live, _, _, _ := r.probe["kiwix"].calls(); live != 0 {
		t.Errorf("liveness ran %d times with nothing to serve", live)
	}
	if got := r.pub.transitions(); len(got) != 0 {
		t.Errorf("the state never changed, yet events %v", got)
	}

	// Once the content is back, a unit that is stopped is down: stopped on purpose by the operator,
	// so still not restarted.
	r.probe["kiwix"].set(func(p *fakeProbe) { p.content = health.Content{} })
	r.setUnit("kiwix", services.ActiveInactive, 0)
	r.tick(30 * time.Second)
	rec := r.record("kiwix")
	if rec.State != health.StateDown || rec.Problems[0].Code != health.CodeLibraryNotRunning {
		t.Fatalf("stopped unit: %+v", rec)
	}
	if r.restarts(services.UnitKiwix) != 0 {
		t.Error("a unit stopped by the operator was restarted")
	}
	if got := r.pub.all(); len(got) != 1 || got[0].From != "missing_content" || got[0].To != "down" || got[0].Code != health.CodeLibraryNotRunning {
		t.Errorf("events %+v", got)
	}
}

func TestReadinessProblemsDecideDegraded(t *testing.T) {
	for _, tc := range []struct {
		name     string
		problems []health.Problem
		want     health.State
		codes    []string
	}{
		{"none", nil, health.StateOK, []string{}},
		{"info only", []health.Problem{{Code: doctor.SVC013, Severity: doctor.Info}}, health.StateOK, []string{doctor.SVC013}},
		{"warning", []health.Problem{{Code: doctor.SVC004, Severity: doctor.Warning}}, health.StateDegraded, []string{doctor.SVC004}},
		{"info and warning, worst first", []health.Problem{{Code: doctor.SVC013, Severity: doctor.Info}, {Code: doctor.SVC004, Severity: doctor.Warning}}, health.StateDegraded, []string{doctor.SVC004, doctor.SVC013}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t, rigOpts{units: []string{"whisper"}})
			r.setUnit("whisper", services.ActiveActive, time.Hour)
			r.probe["whisper"].set(func(p *fakeProbe) {
				p.readiness = health.Readiness{Model: "whisper-small", Device: "cpu", Problems: tc.problems}
			})
			r.sup.CheckNow(bg)
			rec := r.record("whisper")
			if rec.State != tc.want || !equal(r.codes("whisper"), tc.codes) {
				t.Fatalf("state %s codes %v, want %s %v", rec.State, r.codes("whisper"), tc.want, tc.codes)
			}
			if rec.Model != "whisper-small" || rec.Device != "cpu" || rec.Books != nil || rec.Voices != nil {
				t.Errorf("fields %+v", rec)
			}
		})
	}
}

func TestUnitlessServices(t *testing.T) {
	r := newRig(t, rigOpts{unitless: []string{"piper", "maps"}})
	if got := r.state("piper"); got != health.StateStarting {
		t.Fatalf("before the first probe: %s", got)
	}
	r.probe["piper"].set(func(p *fakeProbe) {
		p.readiness = health.Readiness{Voices: []string{"voice-en", "voice-es"}}
	})
	r.probe["maps"].set(func(p *fakeProbe) {
		p.readiness = health.Readiness{Maps: []string{"map-alpha"}, Problems: []health.Problem{{Code: doctor.SVC032, Severity: doctor.Warning}}}
	})
	r.sup.CheckNow(bg)
	piper, maps := r.record("piper"), r.record("maps")
	if piper.State != health.StateOK || piper.HasUnit() || piper.Unit != "" || piper.Active != "" || len(piper.Voices) != 2 || piper.Books != nil {
		t.Errorf("piper %+v", piper)
	}
	if maps.State != health.StateDegraded || !equal(r.codes("maps"), []string{doctor.SVC032}) || len(maps.Maps) != 1 {
		t.Errorf("maps %+v", maps)
	}

	// A broken binary is down at once (nothing to restart), with the code the probe names.
	r.probe["piper"].set(func(p *fakeProbe) { p.live = health.Coded(doctor.SVC020, errDown) })
	r.sup.CheckNow(bg, "piper")
	piper = r.record("piper")
	if piper.State != health.StateDown || !equal(r.codes("piper"), []string{doctor.SVC020}) {
		t.Errorf("piper %+v", piper)
	}
	if len(r.mgr.Calls()) != 0 {
		t.Errorf("a service without a unit touched systemd: %v", r.mgr.Calls())
	}
	ev := r.pub.all()
	last := ev[len(ev)-1]
	if last.Name != "piper" || last.Unit != "" || last.From != "ok" || last.To != "down" || last.Code != doctor.SVC020 || last.Active != "" {
		t.Errorf("event %+v", last)
	}
	// The event carries "unit": null for a service without a unit.
	raw, err := json.Marshal(last)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	if v, ok := m["unit"]; !ok || v != nil {
		t.Errorf("unit in %s", raw)
	}
}

func TestSixStatesAreReachable(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(r *rig)
		want  health.State
	}{
		{"ok", func(r *rig) { r.setUnit("kiwix", services.ActiveActive, time.Hour); r.sup.CheckNow(bg) }, health.StateOK},
		{"degraded", func(r *rig) {
			r.setUnit("kiwix", services.ActiveActive, time.Hour)
			r.probe["kiwix"].set(func(p *fakeProbe) {
				p.readiness = health.Readiness{Problems: []health.Problem{{Code: doctor.SVC004, Severity: doctor.Warning}}}
			})
			r.sup.CheckNow(bg)
		}, health.StateDegraded},
		{"down", func(r *rig) { r.setUnit("kiwix", services.ActiveInactive, 0); r.sup.CheckNow(bg) }, health.StateDown},
		{"missing_content", func(r *rig) {
			r.probe["kiwix"].set(func(p *fakeProbe) { p.content = health.Content{Missing: true, Code: doctor.SVC001} })
			r.setUnit("kiwix", services.ActiveInactive, 0)
			r.sup.CheckNow(bg)
		}, health.StateMissingContent},
		{"disabled", func(r *rig) { r.setUnit("kiwix", services.ActiveActive, time.Hour); r.sup.CheckNow(bg) }, health.StateDisabled},
		{"starting", func(r *rig) { r.setUnit("kiwix", services.ActiveActivating, 0); r.sup.CheckNow(bg) }, health.StateStarting},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := rigOpts{units: []string{"kiwix"}}
			if tc.want == health.StateDisabled {
				o.enabled = map[string]func() bool{"kiwix": func() bool { return false }}
			}
			r := newRig(t, o)
			tc.setup(r)
			if got := r.state("kiwix"); got != tc.want || !got.Valid() {
				t.Errorf("state %s, want %s", got, tc.want)
			}
			if rec := r.record("kiwix"); rec.Healthy != (tc.want == health.StateOK) {
				t.Errorf("healthy %v", rec.Healthy)
			}
		})
	}
	if len(health.States) != 6 {
		t.Errorf("%d states", len(health.States))
	}
}

func TestStateChangeWithoutRestartOfActiveSub(t *testing.T) {
	// A unit that goes active > activating > active (systemd auto-restart) is starting again, and the
	// failures of its first minute do not count.
	r := kiwixOK(t, rigOpts{})
	r.pub.reset()
	r.setUnit("kiwix", services.ActiveActivating, 0)
	if got := r.state("kiwix"); got != health.StateStarting {
		t.Fatalf("activating: %s", got)
	}
	r.probe["kiwix"].set(func(p *fakeProbe) { p.live = errDown })
	r.setUnit("kiwix", services.ActiveActive, 0)
	r.tick(10 * time.Second)
	if got := r.state("kiwix"); got != health.StateStarting {
		t.Fatalf("after the auto-restart: %s", got)
	}
	if r.restarts(services.UnitKiwix) != 0 {
		t.Error("the supervisor restarted a unit systemd had just restarted")
	}
}

func TestRestartErrorCountsAgainstTheBudget(t *testing.T) {
	r := kiwixOK(t, rigOpts{})
	r.mgr.Fail(services.VerbRestart, services.UnitKiwix, errDown)
	r.probe["kiwix"].set(func(p *fakeProbe) { p.live = errDown })
	for i := 0; i < 20; i++ {
		r.tick(30 * time.Second)
	}
	if n := r.restarts(services.UnitKiwix); n != health.DefaultMaxRestarts {
		t.Fatalf("%d attempts, want %d", n, health.DefaultMaxRestarts)
	}
	rec := r.record("kiwix")
	if rec.State != health.StateDown || rec.Problems[0].Code != health.CodeGaveUp {
		t.Errorf("record %+v", rec)
	}
}

func TestHeartbeatReducedForm(t *testing.T) {
	r := newRig(t, rigOpts{units: []string{"kiwix", "whisper"}, unitless: []string{"piper"}})
	r.setUnit("kiwix", services.ActiveActive, time.Hour)
	r.setUnit("whisper", services.ActiveInactive, 0)
	r.sup.CheckNow(bg)
	raw, err := json.Marshal(r.sup.Reduced(bg))
	if err != nil {
		t.Fatal(err)
	}
	want := `[{"name":"kiwix","state":"ok"},{"name":"whisper","state":"down"},{"name":"piper","state":"ok"}]`
	if string(raw) != want {
		t.Errorf("heartbeat %s\nwant %s", raw, want)
	}
}

func TestFlaggedInSurvival(t *testing.T) {
	r := newRig(t, rigOpts{units: []string{"kiwix", "whisper"}, unitless: []string{"piper", "maps"}, require: []string{"kiwix", "whisper", "piper", "maps", "kiwix", "llama"}})
	r.setUnit("kiwix", services.ActiveActive, time.Hour)
	r.setUnit("whisper", services.ActiveInactive, 0)
	r.probe["maps"].set(func(p *fakeProbe) { p.content = health.Content{Missing: true, Code: doctor.SVC030} })
	r.sup.CheckNow(bg)

	r.mode = "online"
	if got := r.sup.Flagged(bg); len(got) != 0 {
		t.Errorf("flagged outside survival: %v", got)
	}
	r.mode = health.ModeSurvival
	if got := r.sup.Flagged(bg); !equal(got, []string{"whisper", "maps"}) {
		t.Errorf("flagged %v, want whisper and maps (kiwix and piper are ok, llama is not ours)", got)
	}
}

func TestStatusesAndCombine(t *testing.T) {
	r := newRig(t, rigOpts{units: []string{"kiwix"}, unitless: []string{"piper"}})
	r.setUnit("kiwix", services.ActiveInactive, 0)
	r.sup.CheckNow(bg)
	rows := r.sup.Statuses(bg)
	if len(rows) != 2 || rows[0].Name != "kiwix" || rows[0].Health != "down" || rows[0].Healthy || rows[0].Code != health.CodeLibraryNotRunning || !rows[0].Enabled {
		t.Fatalf("rows %+v", rows)
	}
	if rows[1].Name != "piper" || rows[1].Unit != "" || rows[1].Health != "ok" || !rows[1].Healthy {
		t.Errorf("piper row %+v", rows[1])
	}

	watcher := fixedView{{Name: "llama", Unit: services.UnitLlama, Health: "ok", Healthy: true}, {Name: "kiwix", Unit: services.UnitKiwix, Health: "ok", Healthy: true}, {Name: "whisper", Unit: services.UnitWhisper, Health: "down"}}
	got := health.Combine(watcher, nil, r.sup).Snapshot(bg)
	var names []string
	for _, s := range got {
		names = append(names, s.Name+":"+s.Health)
	}
	// The supervisor's kiwix replaces the watcher's, the watcher's whisper and llama stay, in order.
	if want := []string{"llama:ok", "kiwix:down", "whisper:down", "piper:ok"}; !equal(names, want) {
		t.Errorf("combined %v, want %v", names, want)
	}
}

type fixedView []services.Status

func (v fixedView) Snapshot(context.Context) []services.Status { return v }

func TestDoctorChecks(t *testing.T) {
	r := newRig(t, rigOpts{units: []string{"kiwix", "whisper"}, unitless: []string{"piper"}})
	r.setUnit("kiwix", services.ActiveActive, time.Hour)
	r.setUnit("whisper", services.ActiveInactive, 0)
	r.probe["kiwix"].set(func(p *fakeProbe) { p.deep = []health.Problem{{Code: doctor.SVC004, Severity: doctor.Warning}} })
	r.probe["piper"].set(func(p *fakeProbe) { p.deep = []health.Problem{{Code: doctor.SVC023, Severity: doctor.Error}} })
	r.probe["whisper"].set(func(p *fakeProbe) { p.deep = []health.Problem{{Code: doctor.SVC016, Severity: doctor.Warning}} })
	r.sup.CheckNow(bg)

	reg := doctor.NewRegistry()
	if err := r.sup.RegisterDoctor(reg); err != nil {
		t.Fatal(err)
	}
	if err := r.sup.RegisterDoctor(reg); err == nil {
		t.Error("registering twice must fail")
	}
	liveBefore, _, _, _ := r.probe["kiwix"].calls()
	fast, err := reg.Run(bg, doctor.RunOptions{FastOnly: true, Now: func() time.Time { return t0 }})
	if err != nil {
		t.Fatal(err)
	}
	if len(fast.Results) != 1 || fast.Results[0].Code != health.CodeLibraryNotRunning && fast.Results[0].Code != health.CodeSpeechNotRunning {
		t.Fatalf("fast results %+v", fast.Results)
	}
	if fast.Results[0].Code != health.CodeSpeechNotRunning || fast.Results[0].Details["service"] != "whisper" || fast.Results[0].Severity != doctor.Error {
		t.Errorf("fast result %+v", fast.Results[0])
	}
	if live, _, _, deep := r.probe["kiwix"].calls(); deep != 0 || live != liveBefore {
		t.Errorf("the fast check probed: live %d (was %d) deep %d", live, liveBefore, deep)
	}

	full, err := reg.Run(bg, doctor.RunOptions{Now: func() time.Time { return t0 }})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, res := range full.Results {
		svc, _ := res.Details["service"].(string)
		got[res.Code] = svc
	}
	// The deep check ran on kiwix and piper (up) and skipped whisper (down: its record already says so).
	want := map[string]string{health.CodeSpeechNotRunning: "whisper", doctor.SVC004: "kiwix", doctor.SVC023: "piper"}
	if len(got) != len(want) {
		t.Fatalf("results %v, want %v", got, want)
	}
	for c, s := range want {
		if got[c] != s {
			t.Errorf("%s from %q, want %q", c, got[c], s)
		}
	}
	if _, _, _, deep := r.probe["whisper"].calls(); deep != 0 {
		t.Error("deep check ran on a service that is down")
	}
}

func TestNewValidates(t *testing.T) {
	probe := &fakeProbe{}
	for _, tc := range []struct {
		name string
		opt  health.Options
	}{
		{"no name", health.Options{Specs: []health.Spec{{Probe: probe}}}},
		{"llama", health.Options{Specs: []health.Spec{{Name: "llama", Probe: probe}}}},
		{"no probe", health.Options{Specs: []health.Spec{{Name: "piper"}}}},
		{"duplicate", health.Options{Specs: []health.Spec{{Name: "piper", Probe: probe}, {Name: "piper", Probe: probe}}}},
		{"unit without manager", health.Options{Specs: []health.Spec{{Name: "kiwix", Unit: services.UnitKiwix, Probe: probe}}}},
	} {
		if _, err := health.New(tc.opt); err == nil {
			t.Errorf("%s: want an error", tc.name)
		}
	}
	if _, err := health.New(health.Options{}); err != nil {
		t.Errorf("an empty supervisor is fine: %v", err)
	}
}

func TestWorkerSubscribesAndReactsToUnitChanges(t *testing.T) {
	r := newRig(t, rigOpts{units: []string{"kiwix"}})
	r.probe["kiwix"].set(func(p *fakeProbe) { p.readiness = health.Readiness{Detail: "1 books"} })
	// The baseline (the unit is stopped: down) is set before the worker runs, so that every change
	// after it is an event whatever the order the goroutines run in.
	if got := r.state("kiwix"); got != health.StateDown {
		t.Fatalf("baseline %s", got)
	}
	if err := r.sup.Start(bg); err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(bg, 5*time.Second)
		defer cancel()
		if err := r.sup.Stop(ctx); err != nil {
			t.Error(err)
		}
	}()
	if r.sup.Name() != "svc-health" {
		t.Errorf("name %q", r.sup.Name())
	}
	subscribed := false
	for _, c := range r.mgr.Calls() {
		if c.Verb == services.VerbSubscribe {
			subscribed = true
		}
	}
	if !subscribed {
		t.Fatal("the worker did not subscribe to the unit")
	}

	// systemd starts the unit: the supervisor sees it through the subscription, probes at once and
	// reports down > starting > ok without waiting for a tick.
	if err := r.mgr.Start(bg, services.UnitKiwix); err != nil {
		t.Fatal(err)
	}
	var got []string
	for len(got) < 2 {
		select {
		case ev := <-r.pub.ch:
			got = append(got, ev.From+">"+ev.To)
		case <-time.After(10 * time.Second):
			t.Fatalf("events so far %v", got)
		}
	}
	if !equal(got, []string{"down>starting", "starting>ok"}) {
		t.Errorf("events %v", got)
	}
}

func TestServiceStateEventPayloadIsPublishable(t *testing.T) {
	// The bus refuses a payload that is not events.ServiceState: the supervisor must publish the
	// registered struct, never an ad-hoc map.
	bus := events.New(events.Options{})
	defer bus.Close()
	r := newRig(t, rigOpts{units: []string{"kiwix"}, tune: func(o *health.Options) { o.Publisher = bus }})
	r.setUnit("kiwix", services.ActiveActive, time.Hour)
	r.sup.CheckNow(bg)
	if c := bus.Counters(); c.Rejected != 0 || c.Published == 0 {
		t.Errorf("bus counters %+v: want the service.state event accepted and none rejected", c)
	}
}
