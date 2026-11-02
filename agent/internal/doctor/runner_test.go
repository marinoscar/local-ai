package doctor

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// okCheck passes. problemCheck returns the results given. They are the fakes of the runner tests.
func okCheck(id, area string, fast bool) Check {
	return Check{ID: id, Area: area, Fast: fast, Run: func(context.Context) []Result { return nil }}
}

func problemCheck(id, area string, fast bool, res ...Result) Check {
	return Check{ID: id, Area: area, Fast: fast, Run: func(context.Context) []Result { return res }}
}

func newReg(t *testing.T, checks ...Check) *Registry {
	t.Helper()
	r := NewRegistry()
	for _, c := range checks {
		if err := r.Add(c); err != nil {
			t.Fatal(err)
		}
	}
	return r
}

func mustRun(t *testing.T, r *Registry, o RunOptions) Report {
	t.Helper()
	rep, err := r.Run(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

func codesOf(rep Report) []string {
	var out []string
	for _, r := range rep.Results {
		out = append(out, r.Check+"="+r.Code)
	}
	return out
}

func TestRunSummaryAndShape(t *testing.T) {
	r := newReg(t,
		okCheck("agt.config", "AGT", true),
		okCheck("llm.router_health", "LLM", false),
		problemCheck("hw.battery", "HW", true, Result{Code: HW012, Details: map[string]any{"battery_percent": 64}}),
		problemCheck("hw.disk", "HW", true, Result{Code: HW008}),
		problemCheck("net.funnel", "NET", false, Result{Code: NET009}),
		problemCheck("agt.upgrade", "AGT", false, Result{Code: AGT015}), // info
		// An ok result (no code) and an empty slice both mean "passed".
		problemCheck("net.loopback_only", "NET", false, Result{}),
	)
	rep := mustRun(t, r, RunOptions{})

	// hw.battery is a warning, hw.disk (HW-008) and net.funnel (NET-009) are critical, agt.upgrade is info.
	want := Summary{OK: 3, Info: 1, Warning: 1, Critical: 2}
	if rep.Summary != want {
		t.Errorf("summary = %+v, want %+v", rep.Summary, want)
	}
	if got, want := rep.Passed, []string{"agt.config", "llm.router_health", "net.loopback_only"}; !reflect.DeepEqual(got, want) {
		t.Errorf("passed = %v, want %v", got, want)
	}
	if len(rep.Results) != 4 {
		t.Fatalf("results = %v, want 4 problems", codesOf(rep))
	}
	// Worst first, ties by check id.
	for i := 1; i < len(rep.Results); i++ {
		a, b := rep.Results[i-1], rep.Results[i]
		if a.Severity.Rank() < b.Severity.Rank() || (a.Severity == b.Severity && a.Check > b.Check) {
			t.Errorf("results out of order at %d: %v", i, codesOf(rep))
		}
	}
	last := rep.Results[len(rep.Results)-1]
	if last.Code != AGT015 || last.Severity != Info {
		t.Errorf("last result = %+v, want the info result", last)
	}

	var bat Result
	for _, res := range rep.Results {
		if res.Check == "hw.battery" {
			bat = res
		}
	}
	if bat.Code != HW012 || bat.Severity != Warning || bat.Title != "Running on battery" ||
		!strings.HasPrefix(bat.Fix, "Plug in the charger.") || bat.Details["battery_percent"] != 64 {
		t.Errorf("battery result = %+v", bat)
	}
}

func TestReportJSONShape(t *testing.T) {
	r := newReg(t, okCheck("agt.config", "AGT", true))
	rep := mustRun(t, r, RunOptions{Now: func() time.Time { return time.Date(2026, 11, 2, 8, 0, 30, 999, time.UTC) }})
	b, err := json.Marshal(rep)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"ran_at", "duration_ms", "summary", "results", "passed"} {
		if _, ok := m[k]; !ok {
			t.Errorf("missing key %q in %s", k, b)
		}
	}
	if m["ran_at"] != "2026-11-02T08:00:30Z" {
		t.Errorf("ran_at = %v, want UTC to the second", m["ran_at"])
	}
	if res, ok := m["results"].([]any); !ok || len(res) != 0 {
		t.Errorf("results = %v, want an empty array (not null)", m["results"])
	}
	empty := mustRun(t, NewRegistry(), RunOptions{})
	eb, _ := json.Marshal(empty)
	if !strings.Contains(string(eb), `"results":[]`) || !strings.Contains(string(eb), `"passed":[]`) {
		t.Errorf("an empty run must encode empty arrays: %s", eb)
	}
}

func TestRunFiltersByAreaAndFast(t *testing.T) {
	r := newReg(t,
		problemCheck("hw.battery", "HW", true, Result{Code: HW012}),
		problemCheck("hw.smart", "HW", false, Result{Code: HW008}),
		problemCheck("llm.router_health", "LLM", true, Result{Code: LLM004}),
		problemCheck("net.dns", "NET", false, Result{Code: NET013}),
	)
	tests := []struct {
		name string
		opts RunOptions
		want []string // checks that ran (passed or not)
	}{
		{"all", RunOptions{}, []string{"hw.battery", "hw.smart", "llm.router_health", "net.dns"}},
		{"one area", RunOptions{Areas: []string{"LLM"}}, []string{"llm.router_health"}},
		{"area is case-insensitive", RunOptions{Areas: []string{"llm"}}, []string{"llm.router_health"}},
		{"repeatable", RunOptions{Areas: []string{"HW", "NET"}}, []string{"hw.battery", "hw.smart", "net.dns"}},
		{"blank area entries are ignored", RunOptions{Areas: []string{"", " "}}, []string{"hw.battery", "hw.smart", "llm.router_health", "net.dns"}},
		{"fast only", RunOptions{FastOnly: true}, []string{"hw.battery", "llm.router_health"}},
		{"fast and area", RunOptions{FastOnly: true, Areas: []string{"HW"}}, []string{"hw.battery"}},
		{"area without checks", RunOptions{Areas: []string{"DSK"}}, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rep := mustRun(t, r, tc.opts)
			var ran []string
			for _, res := range rep.Results {
				ran = append(ran, res.Check)
			}
			ran = append(ran, rep.Passed...)
			if len(ran) != len(tc.want) {
				t.Fatalf("ran %v, want %v", ran, tc.want)
			}
			have := map[string]bool{}
			for _, id := range ran {
				have[id] = true
			}
			for _, id := range tc.want {
				if !have[id] {
					t.Errorf("%s did not run (ran %v)", id, ran)
				}
			}
		})
	}

	for _, bad := range []string{"XYZ", "LAI-HW", "hw.battery"} {
		if _, err := r.Run(context.Background(), RunOptions{Areas: []string{"HW", bad}}); !errors.Is(err, ErrInvalidArea) {
			t.Errorf("area %q: err = %v, want ErrInvalidArea", bad, err)
		}
	}
}

func TestRunNeverMoreThanFourChecksAtOnce(t *testing.T) {
	var cur, peak atomic.Int32
	var checks []Check
	for i := 0; i < 12; i++ {
		id := "agt.slow" + string(rune('a'+i))
		checks = append(checks, Check{ID: id, Area: "AGT", Run: func(context.Context) []Result {
			n := cur.Add(1)
			for {
				p := peak.Load()
				if n <= p || peak.CompareAndSwap(p, n) {
					break
				}
			}
			time.Sleep(40 * time.Millisecond)
			cur.Add(-1)
			return nil
		}})
	}
	rep := mustRun(t, newReg(t, checks...), RunOptions{})
	if len(rep.Passed) != 12 {
		t.Fatalf("passed = %d, want 12", len(rep.Passed))
	}
	if p := peak.Load(); p > MaxConcurrent {
		t.Errorf("peak concurrency = %d, want at most %d", p, MaxConcurrent)
	} else if p < MaxConcurrent {
		t.Errorf("peak concurrency = %d, want the cap of %d to be used", p, MaxConcurrent)
	}
}

func TestRunTimeoutPanicAndUnknownCode(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	r := newReg(t,
		problemCheck("agt.fine", "AGT", false, Result{Code: AGT015}),
		// Honours its context: returns when the timeout fires.
		Check{ID: "net.polite", Area: "NET", Timeout: 30 * time.Millisecond, Run: func(ctx context.Context) []Result {
			<-ctx.Done()
			return []Result{{Code: NET013}}
		}},
		// Ignores its context entirely: the runner must abandon it.
		Check{ID: "hw.stuck", Area: "HW", Timeout: 30 * time.Millisecond, Run: func(context.Context) []Result {
			<-release
			return nil
		}},
		Check{ID: "llm.panics", Area: "LLM", Run: func(context.Context) []Result {
			panic("kaboom\nsecond line with detail")
		}},
		problemCheck("svc.invents", "SVC", false, Result{Code: unknownCode("SVC")}),
		problemCheck("kit.retired", "KIT", false, Result{Code: KIT022}),
	)
	start := time.Now()
	rep := mustRun(t, r, RunOptions{})
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("the run took %v: a stuck check must not hold it", d)
	}

	byCheck := map[string]Result{}
	for _, res := range rep.Results {
		byCheck[res.Check] = res
	}
	if res := byCheck["agt.fine"]; res.Code != AGT015 {
		t.Errorf("a healthy check must be unaffected by the others: %+v", res)
	}
	for id, reason := range map[string]string{
		"net.polite": "timeout", // its own result is replaced: the check was abandoned at the deadline
		"hw.stuck":   "timeout",
		"llm.panics": "panic: kaboom",
	} {
		res, ok := byCheck[id]
		if !ok || res.Code != AGT099 || res.Severity != Error {
			t.Errorf("%s: result = %+v, want LAI-AGT-099 (error)", id, res)
			continue
		}
		if res.Details["check"] != id || res.Details["reason"] != reason {
			t.Errorf("%s: details = %+v, want check=%s reason=%q", id, res.Details, id, reason)
		}
	}
	for _, id := range []string{"svc.invents", "kit.retired"} {
		res := byCheck[id]
		if res.Code != AGT099 || !strings.HasPrefix(res.Details["reason"].(string), "unknown code ") {
			t.Errorf("%s: %+v, want AGT-099 for a code outside the table", id, res)
		}
	}
	if len(rep.Passed) != 0 {
		t.Errorf("passed = %v, want none: a timed-out check did not pass", rep.Passed)
	}
}

func TestStuckCheckIsNotStartedTwice(t *testing.T) {
	release := make(chan struct{})
	var starts atomic.Int32
	r := newReg(t, Check{ID: "hw.stuck", Area: "HW", Timeout: 20 * time.Millisecond, Run: func(context.Context) []Result {
		starts.Add(1)
		<-release
		return nil
	}})
	for i := 0; i < 3; i++ {
		rep := mustRun(t, r, RunOptions{})
		if len(rep.Results) != 1 || rep.Results[0].Code != AGT099 || rep.Results[0].Details["reason"] != "timeout" {
			t.Fatalf("run %d: %+v", i, rep.Results)
		}
	}
	if n := starts.Load(); n != 1 {
		t.Errorf("the check was started %d times while the first run was stuck, want 1", n)
	}
	close(release)
	deadline := time.Now().Add(2 * time.Second)
	for {
		rep := mustRun(t, r, RunOptions{})
		if len(rep.Passed) == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the check never recovered after its goroutine returned")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestRunDefaultTimeoutIsTenSeconds(t *testing.T) {
	var deadline time.Time
	r := newReg(t, Check{ID: "agt.probe", Area: "AGT", Run: func(ctx context.Context) []Result {
		deadline, _ = ctx.Deadline()
		return nil
	}})
	start := time.Now()
	mustRun(t, r, RunOptions{})
	if d := deadline.Sub(start); d < 9*time.Second || d > 11*time.Second {
		t.Errorf("default check deadline = %v, want about %v", d, DefaultTimeout)
	}
}

func TestRunBudgetCutsTheWholeRun(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	r := newReg(t,
		problemCheck("hw.quick", "HW", true, Result{Code: HW012}),
		Check{ID: "hw.slow", Area: "HW", Fast: true, Timeout: 30 * time.Second, Run: func(context.Context) []Result {
			<-release
			return nil
		}},
	)
	start := time.Now()
	rep := mustRun(t, r, RunOptions{FastOnly: true, Budget: 400 * time.Millisecond})
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("run took %v with a 400 ms budget", d)
	}
	byCheck := map[string]Result{}
	for _, res := range rep.Results {
		byCheck[res.Check] = res
	}
	if byCheck["hw.quick"].Code != HW012 {
		t.Errorf("the quick check should have finished inside the budget: %+v", rep.Results)
	}
	if res := byCheck["hw.slow"]; res.Code != AGT099 || res.Details["reason"] != "timeout" {
		t.Errorf("hw.slow = %+v, want AGT-099 timeout", res)
	}
}

func TestRunLocalizesButNeverTheCode(t *testing.T) {
	r := newReg(t, problemCheck("hw.battery", "HW", true, Result{Code: HW012}))
	en := mustRun(t, r, RunOptions{Lang: "en"}).Results[0]
	es := mustRun(t, r, RunOptions{Lang: "es"}).Results[0]
	fr := mustRun(t, r, RunOptions{Lang: "fr"}).Results[0]
	if en.Code != es.Code || en.Code != HW012 || en.Severity != es.Severity || en.Check != es.Check {
		t.Errorf("code, severity and check must not change: %+v vs %+v", en, es)
	}
	if es.Title != "Funcionando con batería" || !strings.HasPrefix(es.Fix, "Conecte el cargador") {
		t.Errorf("es = %+v", es)
	}
	if fr.Title != en.Title {
		t.Errorf("an unknown language must fall back to English: %+v", fr)
	}
}

func TestResultOverrides(t *testing.T) {
	r := newReg(t, problemCheck("agt.state_intact", "AGT", true,
		// Severity escalation: AGT-003 is a warning, critical when defaults were used.
		Result{Code: AGT003, Severity: Critical},
		// An invalid severity falls back to the default of the code.
		Result{Code: AGT013, Severity: "fatal"},
		// A check's own text wins and is not translated.
		Result{Code: AGT004, Title: "custom title", Fix: "custom fix"},
	))
	rep := mustRun(t, r, RunOptions{Lang: "es"})
	got := map[string]Result{}
	for _, res := range rep.Results {
		got[res.Code] = res
	}
	if got[AGT003].Severity != Critical {
		t.Errorf("AGT-003 severity = %q, want the check's critical", got[AGT003].Severity)
	}
	if got[AGT013].Severity != Warning {
		t.Errorf("AGT-013 severity = %q, want the default warning", got[AGT013].Severity)
	}
	if got[AGT004].Title != "custom title" || got[AGT004].Fix != "custom fix" {
		t.Errorf("AGT-004 = %+v, want the check's own text", got[AGT004])
	}
	if rep.Summary.Critical != 2 || rep.Summary.Warning != 1 {
		t.Errorf("summary = %+v", rep.Summary)
	}
}

func TestRunDoesNotAliasCheckDetails(t *testing.T) {
	shared := map[string]any{"k": "v"}
	r := newReg(t, problemCheck("hw.battery", "HW", true, Result{Code: HW012, Details: shared}))
	rep := mustRun(t, r, RunOptions{})
	rep.Results[0].Details["k"] = "changed"
	if shared["k"] != "v" {
		t.Error("the report shares the check's details map")
	}
}

func TestRegisterValidation(t *testing.T) {
	run := func(context.Context) []Result { return nil }
	tests := []struct {
		name  string
		check Check
		ok    bool
	}{
		{"valid", Check{ID: "llm.router_health", Area: "LLM", Run: run}, true},
		{"no dot", Check{ID: "routerhealth", Area: "LLM", Run: run}, false},
		{"upper case", Check{ID: "LLM.router", Area: "LLM", Run: run}, false},
		{"empty id", Check{Area: "LLM", Run: run}, false},
		{"two dots", Check{ID: "llm.router.health", Area: "LLM", Run: run}, false},
		{"unknown area", Check{ID: "llm.router", Area: "XYZ", Run: run}, false},
		{"lower-case area", Check{ID: "llm.router", Area: "llm", Run: run}, false},
		{"no run", Check{ID: "llm.router", Area: "LLM"}, false},
		{"negative timeout", Check{ID: "llm.router", Area: "LLM", Run: run, Timeout: -1}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := NewRegistry().Add(tc.check)
			if (err == nil) != tc.ok {
				t.Errorf("Add = %v, want ok=%v", err, tc.ok)
			}
		})
	}
	r := NewRegistry()
	if err := r.Add(Check{ID: "llm.router", Area: "LLM", Run: run}); err != nil {
		t.Fatal(err)
	}
	if err := r.Add(Check{ID: "llm.router", Area: "LLM", Run: run}); err == nil {
		t.Error("a duplicate id must be refused")
	}
	if !r.Remove("llm.router") || r.Remove("llm.router") {
		t.Error("Remove must report whether the check existed")
	}
}

func TestRegisterPanicsOnABadCheck(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("Register must panic on a bad check, like http.Handle")
		}
	}()
	Register(Check{ID: "bad id"})
}

func TestChecksAreSortedAndRaceFree(t *testing.T) {
	r := NewRegistry()
	var wg sync.WaitGroup
	for _, id := range []string{"net.b", "net.a", "hw.c", "agt.z"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := r.Add(okCheck(id, "AGT", true)); err != nil {
				t.Error(err)
			}
			_, _ = r.Run(context.Background(), RunOptions{})
		}()
	}
	wg.Wait()
	var ids []string
	for _, c := range r.Checks() {
		ids = append(ids, c.ID)
	}
	if want := []string{"agt.z", "hw.c", "net.a", "net.b"}; !reflect.DeepEqual(ids, want) {
		t.Errorf("Checks = %v, want %v", ids, want)
	}
}

func TestRunHonoursACancelledContext(t *testing.T) {
	r := newReg(t, Check{ID: "agt.wait", Area: "AGT", Run: func(ctx context.Context) []Result {
		<-ctx.Done()
		return nil
	}})
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(20 * time.Millisecond); cancel() }()
	start := time.Now()
	rep, err := r.Run(ctx, RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("run did not stop when its context was cancelled")
	}
	if len(rep.Results) != 1 || rep.Results[0].Details["reason"] == "panic" {
		t.Errorf("results = %+v", rep.Results)
	}
}

// Regression for #207. runOne marks a check as running while its goroutine lives, so that a stuck
// check is never started twice. The goroutine used to send its result first and unmark itself in a
// deferred call after it, so the caller could get the result, start the next run at once and find the
// check still marked: it then reported LAI-AGT-099 "timeout" for a check that had just passed, and
// the alert manager raised LAI-AGT-099 instead of what the check found. The mark must be gone by the
// time the result is delivered.
func TestACheckIsNotRunningOnceItsResultIsDelivered(t *testing.T) {
	r := newReg(t,
		okCheck("agt.fine", "AGT", true),
		Check{ID: "agt.panics", Area: "AGT", Fast: true, Run: func(context.Context) []Result { panic("boom") }},
	)
	for i := 0; i < 5000; i++ {
		mustRun(t, r, RunOptions{})
		r.mu.Lock()
		n := len(r.running)
		r.mu.Unlock()
		if n != 0 {
			t.Fatalf("run %d: %d check(s) still marked running after their results were delivered", i, n)
		}
	}
}

// Found by the stress run for #207: a run whose context is already done when the check's goroutine
// starts (Stop, or a budget that ran out) was abandoned by the caller but the goroutine still called
// the check, so a check ran after the manager had stopped. The check must not be called at all.
func TestACheckIsNotCalledOnceItsRunIsAlreadyOver(t *testing.T) {
	var calls atomic.Int32
	c := Check{ID: "hw.battery", Area: "HW", Fast: true, Run: func(context.Context) []Result {
		calls.Add(1)
		return nil
	}}
	r := newReg(t, c)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for i := 0; i < 200; i++ {
		res := r.runOne(ctx, c)
		if len(res) != 1 || res[0].Code != AGT099 {
			t.Fatalf("result = %+v, want the abandoned run's LAI-AGT-099", res)
		}
	}
	waitFor(t, "the abandoned goroutines to finish", func() bool {
		r.mu.Lock()
		defer r.mu.Unlock()
		return len(r.running) == 0
	})
	if n := calls.Load(); n != 0 {
		t.Fatalf("the check was called %d time(s) after its run was over", n)
	}
}

// The same race seen from the outside: a check that passes must pass on every consecutive run.
func TestConsecutiveRunsOfAPassingCheckNeverReportAGT099(t *testing.T) {
	r := newReg(t, okCheck("hw.battery", "HW", true))
	for i := 0; i < 5000; i++ {
		rep := mustRun(t, r, RunOptions{})
		if len(rep.Results) != 0 || len(rep.Passed) != 1 {
			t.Fatalf("run %d: results %v, passed %v, want a pass", i, codesOf(rep), rep.Passed)
		}
	}
}
