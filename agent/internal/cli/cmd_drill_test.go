package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/api"
	"github.com/marinoscar/local-ai/agent/internal/cli/fake"
	"github.com/marinoscar/local-ai/agent/internal/events"
)

// Tests of `aictl drill` (AICTL-10, aictl.md section 4.17, drill.md section 3.1) against a fake agent
// that answers POST /drill, publishes the drill.* events and serves the results document.

const drillRunID = "drl-20261101T043512Z-9c1e"

type drillRig struct {
	*mgRig
	status  int
	body    string
	onStart func()
}

func newDrillRig(t *testing.T) *drillRig {
	t.Helper()
	r := &drillRig{mgRig: newMgRig(t, fake.Options{}), status: 202, body: `{"run_id":"` + drillRunID + `"}`}
	r.events()
	r.agent().Handle(http.MethodPost, "/drill", func(w http.ResponseWriter, _ *http.Request) {
		if r.onStart != nil {
			r.onStart()
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-LocalAI-Op-Id", "op_drill1")
		w.WriteHeader(r.status)
		io.WriteString(w, r.body)
	})
	return r
}

func (r *drillRig) started(trigger string) {
	r.ev.Publish(events.TypeDrillStarted, "op_drill1", events.DrillStarted{RunID: drillRunID, Trigger: trigger})
}

func (r *drillRig) checkDone(id, result, code string) {
	r.ev.Publish(events.TypeDrillCheckDone, "op_drill1", events.DrillCheckDone{RunID: drillRunID, ID: id, Result: result, Code: code})
}

func (r *drillRig) done(overall string, ready bool) {
	r.ev.Publish(events.TypeDrillDone, "op_drill1", events.DrillDone{RunID: drillRunID, Overall: overall, SurvivalReady: ready, Trigger: "manual"})
}

// serveDoc makes GET /drill answer with a results document.
func (r *drillRig) serveDoc(doc string) {
	r.agent().Respond(http.MethodGet, "/drill", 200, doc)
}

// countOf counts the requests of a method whose path is exactly path (no query).
func (r *drillRig) countOf(method, path string) int {
	n := 0
	for _, q := range r.agent().Requests() {
		if q.Method == method && q.Path == path {
			n++
		}
	}
	return n
}

type fxCheck struct {
	id, area, result, code string
	ms                     int64
}

// drillDocJSON builds a results document like drill.md section 3.4. Times come from the clock, three
// hours back, so "3 hours ago" is what the report says.
func drillDocJSON(overall, state string, ready bool, trigger string, checks ...fxCheck) string {
	counts := map[string]int{"pass": 0, "warn": 0, "fail": 0, "skip": 0}
	var cs []map[string]any
	for _, c := range checks {
		counts[c.result]++
		m := map[string]any{"id": c.id, "area": c.area, "result": c.result, "ms": c.ms, "details": map[string]any{}}
		if c.code != "" {
			m["code"] = c.code
		}
		cs = append(cs, m)
	}
	at := time.Now().Add(-3 * time.Hour).UTC().Format(time.RFC3339)
	doc := map[string]any{
		"schema_version": 1, "run_id": drillRunID, "trigger": trigger, "node_id": "ai-n3", "agent_version": "0.4.0",
		"catalog_version": "2026.11.02-1", "mode": "online", "quick": false, "started_at": at, "finished_at": at,
		"overall": overall, "survival_ready": ready, "counts": counts, "checks": cs,
		"restored":      map[string]any{"loaded_models": []string{}, "default_model": nil, "ok": true},
		"network_fence": map[string]any{"enabled": true, "blocked_dials": 0},
	}
	if state != "" {
		doc["state"] = state
	}
	b, _ := json.Marshal(doc)
	return string(b)
}

var (
	passChecks = []fxCheck{
		{"drl.fence", "network", "pass", "", 80},
		{"drl.llm.answer.qwen3.5-9b-q4km", "llm", "pass", "", 2100},
		{"drl.kiwix.search", "library", "pass", "", 400},
		{"drl.maps.tile", "maps", "pass", "", 300},
	}
	warnChecks = append(append([]fxCheck{}, passChecks...), fxCheck{"drl.stt.es", "speech", "warn", "LAI-DRL-031", 8200})
	failChecks = append(append([]fxCheck{}, passChecks...),
		fxCheck{"drl.stt.es", "speech", "warn", "LAI-DRL-031", 8200},
		fxCheck{"drl.tts.en", "speech", "fail", "LAI-DRL-034", 900},
		fxCheck{"drl.kit.present", "kit", "skip", "", 0})
)

// publishRun publishes what the agent publishes for a run of these checks.
func (r *drillRig) publishRun(overall string, ready bool, checks []fxCheck) {
	r.started("manual")
	for _, c := range checks {
		r.checkDone(c.id, c.result, c.code)
	}
	r.done(overall, ready)
}

// ---- the report --------------------------------------------------------------------------------------

func TestDrillFollowsToTheEnd(t *testing.T) {
	cases := []struct {
		name    string
		overall string
		ready   bool
		checks  []fxCheck
		exit    int
		out     []string
	}{
		{"pass", "pass", true, passChecks, 0, []string{
			"✓ The internet is blocked during the test", "✓ Brain answered (qwen3.5-9b-q4km) · 2.1 s", "✓ Library search worked",
			"✓ Map tiles were served", "✓ Drill passed.", "✓ Survival mode is ready on this node."}},
		{"pass but not everything was covered", "pass", false, passChecks, 0, []string{"✓ Drill passed.", "i This drill did not cover everything survival mode needs"}},
		{"warn", "warn", true, warnChecks, ExitWarnings, []string{
			"! Speech to text (Spanish): Speech recognition is less accurate than expected.  [LAI-DRL-031]", "! Drill passed with 1 warning."}},
		{"fail", "fail", false, failChecks, ExitErrors, []string{
			"✗ Text to speech (English): The voice failed.  [LAI-DRL-034]", "→ Fix: Run `aictl doctor`.",
			"✗ Drill failed: 1 check did not pass.", "i The rebuild kit is present (skipped)"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newDrillRig(t)
			r.onStart = func() { r.publishRun(tc.overall, tc.ready, tc.checks) }
			r.serveDoc(drillDocJSON(tc.overall, "", tc.ready, "manual", tc.checks...))
			got := r.run(mgArgs("drill", "--wait"))
			if got.code != tc.exit {
				t.Fatalf("exit %d, want %d\n%s\n%s", got.code, tc.exit, got.out, got.errOut)
			}
			all := strings.Join(strings.Fields(got.out), " ")
			for _, want := range tc.out {
				if !strings.Contains(all, strings.Join(strings.Fields(want), " ")) {
					t.Errorf("stdout lacks %q:\n%s", want, got.out)
				}
			}
			if b := r.lastBody(http.MethodPost, "/drill"); b != `{"only":[],"quick":false,"trigger":"manual"}` {
				t.Errorf("body %s", b)
			}
		})
	}
}

func TestDrillTheVerdictFollowsOverallNotTheCounts(t *testing.T) {
	// aictl.md section 3.4: 0 pass, 8 warn, 9 fail, whatever the event said about each check.
	for overall, want := range map[string]int{"pass": 0, "warn": ExitWarnings, "fail": ExitErrors} {
		r := newDrillRig(t)
		r.onStart = func() { r.publishRun(overall, true, passChecks) }
		r.serveDoc(drillDocJSON(overall, "", true, "manual", passChecks...))
		if got := r.run(mgArgs("drill", "--wait")); got.code != want {
			t.Errorf("%s: exit %d, want %d", overall, got.code, want)
		}
	}
}

func TestDrillRunGoldens(t *testing.T) {
	run := func(name string, lang string, overall string, ready bool, checks []fxCheck) {
		r := newDrillRig(t)
		r.onStart = func() { r.publishRun(overall, ready, checks) }
		r.serveDoc(drillDocJSON(overall, "", ready, "manual", checks...))
		args := []string{"drill", "--wait"}
		if lang != "" {
			args = append([]string{"--lang", lang}, args...)
		}
		checkGolden(t, name, runResult(r.run(mgArgs(args...))))
	}
	run("drill.pass.en", "", "pass", true, passChecks)
	run("drill.warn.en", "", "warn", true, warnChecks)
	run("drill.fail.en", "", "fail", false, failChecks)
	run("drill.warn.es", "es", "warn", true, warnChecks)
	run("drill.fail.es", "es", "fail", false, failChecks)
}

func TestDrillJSONIsTheResultsDocument(t *testing.T) {
	r := newDrillRig(t)
	r.onStart = func() { r.publishRun("warn", true, warnChecks) }
	doc := drillDocJSON("warn", "", true, "manual", warnChecks...)
	r.serveDoc(doc)
	got := r.run(mgArgs("--json", "drill"))
	if got.code != ExitWarnings || got.errOut != "" {
		t.Fatalf("%+v", got)
	}
	var a, b map[string]any
	if err := json.Unmarshal([]byte(got.out), &a); err != nil {
		t.Fatalf("%v\n%s", err, got.out)
	}
	_ = json.Unmarshal([]byte(doc), &b)
	if a["run_id"] != drillRunID || a["overall"] != "warn" || len(a["checks"].([]any)) != len(b["checks"].([]any)) {
		t.Errorf("%v", a)
	}
}

func TestDrillUnrecognisedCheckIdIsShownAsIs(t *testing.T) {
	r := newDrillRig(t)
	cs := []fxCheck{{"drl.future.thing", "x", "pass", "", 10}}
	r.onStart = func() { r.publishRun("pass", true, cs) }
	r.serveDoc(drillDocJSON("pass", "", true, "manual", cs...))
	if got := r.run(mgArgs("drill", "--wait")); got.code != 0 || !strings.Contains(got.out, "✓ drl.future.thing") {
		t.Fatalf("%+v", got)
	}
}

// ---- the request ------------------------------------------------------------------------------------

func TestDrillRequestBody(t *testing.T) {
	cases := []struct {
		args []string
		body string
	}{
		{[]string{"drill", "--wait"}, `{"only":[],"quick":false,"trigger":"manual"}`},
		{[]string{"drill", "--quick", "--wait"}, `{"only":[],"quick":true,"trigger":"manual"}`},
		{[]string{"drill", "--only", "drl.stt", "--only", "drl.kiwix.search", "--wait"}, `{"only":["drl.stt","drl.kiwix.search"],"quick":false,"trigger":"manual"}`},
		{[]string{"drill", "--scheduled"}, `{"only":[],"quick":false,"trigger":"scheduled"}`},
		{[]string{"drill", "--scheduled", "--quick"}, `{"only":[],"quick":true,"trigger":"scheduled"}`},
	}
	for _, tc := range cases {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			r := newDrillRig(t)
			r.onStart = func() { r.publishRun("pass", true, passChecks) }
			r.serveDoc(drillDocJSON("pass", "", true, "manual", passChecks...))
			if got := r.run(mgArgs(tc.args...)); got.code != 0 {
				t.Fatalf("%+v", got)
			}
			if b := r.lastBody(http.MethodPost, "/drill"); b != tc.body {
				t.Errorf("body %s, want %s", b, tc.body)
			}
		})
	}
}

func TestDrillWithNoTerminalDoesNotWaitUnlessAsked(t *testing.T) {
	r := newDrillRig(t)
	got := r.run(mgArgs("drill"))
	checkRun(t, followOut{got.code, flat(got.out) + "\n", got.errOut}, 0, "✓ Drill started (run "+drillRunID+"). It takes a while; see the result with `aictl drill show`.\n", "")
	for _, q := range r.agent().Requests() {
		if strings.HasPrefix(q.Path, "/events") {
			t.Errorf("a non-waiting drill opened the event stream")
		}
	}
	es := newDrillRig(t).run(mgArgs("--lang", "es", "drill"))
	if es.code != 0 || !strings.Contains(flat(es.out), "Simulacro iniciado (ejecución "+drillRunID+")") {
		t.Fatalf("%+v", es)
	}
	checkRun(t, newDrillRig(t).run(mgArgs("-q", "drill")), 0, "", "")
}

func TestDrillOnATerminalFollows(t *testing.T) {
	r := newDrillRig(t)
	r.onStart = func() { r.publishRun("pass", true, passChecks) }
	r.serveDoc(drillDocJSON("pass", "", true, "manual", passChecks...))
	got := r.run(mgArgs("drill").withTerm())
	if got.code != 0 || !strings.Contains(got.out, "✓ Drill passed.") {
		t.Fatalf("%+v", got)
	}
}

func TestDrillUsageErrorsMakeNoRequest(t *testing.T) {
	for _, args := range [][]string{
		{"drill", "extra"},
		{"drill", "--only", "bad id"},
		{"drill", "--only", "../etc"},
		{"drill", "--cancel", "--quick"},
		{"drill", "--cancel", "--last"},
		{"drill", "--last", "--scheduled"},
		{"drill", "--bogus"},
	} {
		r := newDrillRig(t)
		got := r.run(mgArgs(args...))
		if got.code != ExitUsage || !strings.Contains(got.errOut, "[LAI-CLI-005]") || len(r.requestLines()) != 0 {
			t.Errorf("%v: %+v requests %v", args, got, r.requestLines())
		}
	}
}

// ---- a drill that does not start --------------------------------------------------------------------

func TestDrillScheduledSkippedExitsZero(t *testing.T) {
	skipped := `{"run_id":"` + drillRunID + `","state":"skipped","code":"LAI-DRL-003","reason":"survival mode"}`
	t.Run("the agent answers 202 with state skipped", func(t *testing.T) {
		r := newDrillRig(t)
		r.body = skipped
		got := r.run(mgArgs("drill", "--scheduled"))
		checkRun(t, got, 0, "i Drill skipped: survival mode.\n", "")
		if r.agent().Count("/events?types=drill.*") > 1 {
			t.Errorf("the skip should not wait for events")
		}
	})
	t.Run("Spanish uses the text of the code", func(t *testing.T) {
		r := newDrillRig(t)
		r.body = skipped
		got := r.run(mgArgs("--lang", "es", "drill", "--scheduled"))
		checkRun(t, got, 0, "i Simulacro omitido: El simulacro no se ejecuta en modo supervivencia.\n", "")
	})
	t.Run("a refusal of a scheduled start is a skip too", func(t *testing.T) {
		for _, code := range []string{"LAI-DRL-002", "LAI-DRL-003", "LAI-DRL-004", "LAI-DRL-005"} {
			r := newDrillRig(t)
			r.agent().RespondError(http.MethodPost, "/drill", 409, code, "Not now.", "Wait.", nil)
			got := r.run(mgArgs("drill", "--scheduled"))
			if got.code != 0 || !strings.HasPrefix(got.out, "i Drill skipped: ") || got.errOut != "" {
				t.Errorf("%s: %+v", code, got)
			}
		}
	})
	t.Run("--json prints the answer", func(t *testing.T) {
		r := newDrillRig(t)
		r.body = skipped
		checkRun(t, r.run(mgArgs("--json", "drill", "--scheduled")), 0, skipped+"\n", "")
	})
	t.Run("a skipped run is never exit 3", func(t *testing.T) {
		r := newDrillRig(t)
		r.body = skipped
		if got := r.run(mgArgs("drill", "--scheduled", "--wait")); got.code == ExitUnreachable {
			t.Fatalf("%+v", got)
		}
	})
}

func TestDrillManualRefusedIsExit6(t *testing.T) {
	for _, code := range []string{"LAI-DRL-002", "LAI-DRL-003", "LAI-DRL-004", "LAI-DRL-005"} {
		for _, wait := range []bool{true, false} {
			r := newDrillRig(t)
			r.agent().RespondError(http.MethodPost, "/drill", 409, code, "Not now.", "Wait.", nil)
			args := []string{"drill"}
			if wait {
				args = append(args, "--wait")
			}
			got := r.run(mgArgs(args...))
			if got.code != ExitRefused || got.out != "" || !strings.Contains(got.errOut, "["+code+"]") {
				t.Errorf("%s wait=%v: %+v", code, wait, got)
			}
		}
	}
	t.Run("an unknown check is the agent's 400, exit 2", func(t *testing.T) {
		r := newDrillRig(t)
		r.agent().RespondError(http.MethodPost, "/drill", 400, "LAI-AGT-020", "No such check.", "Run aictl drill show.", map[string]any{"reason": "unknown_check"})
		got := r.run(mgArgs("drill", "--only", "drl.nope"))
		if got.code != ExitUsage || !strings.Contains(got.errOut, "[LAI-AGT-020]") {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("the agent is down: exit 3", func(t *testing.T) {
		got := newMgRig(t, fake.Options{NoSocket: true, NoTCP: true}).run(mgArgs("drill"))
		if got.code != ExitUnreachable {
			t.Fatalf("%+v", got)
		}
	})
}

// ---- stopping a drill --------------------------------------------------------------------------------

func TestDrillCancel(t *testing.T) {
	t.Run("DELETE /drill, exit 0", func(t *testing.T) {
		r := newDrillRig(t)
		r.agent().Respond(http.MethodDelete, "/drill", 202, `{"run_id":"`+drillRunID+`"}`)
		got := r.run(mgArgs("drill", "--cancel"))
		checkRun(t, got, 0, "✓ The drill is stopping. The agent puts everything back as it was.\n", "")
		if r.countOf(http.MethodDelete, "/drill") != 1 || r.countOf(http.MethodPost, "/drill") != 0 {
			t.Errorf("requests %v", r.requestLines())
		}
	})
	t.Run("no drill runs: exit 5", func(t *testing.T) {
		r := newDrillRig(t)
		r.agent().RespondError(http.MethodDelete, "/drill", 404, "LAI-AGT-021", "No drill is running.", "Nothing to do.", nil)
		got := r.run(mgArgs("drill", "--cancel"))
		if got.code != ExitNotFound || !strings.Contains(got.errOut, "[LAI-AGT-021]") {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("--json and Spanish", func(t *testing.T) {
		r := newDrillRig(t)
		r.agent().Respond(http.MethodDelete, "/drill", 202, `{"run_id":"`+drillRunID+`"}`)
		checkRun(t, r.run(mgArgs("--json", "drill", "--cancel")), 0, `{"run_id":"`+drillRunID+`"}`+"\n", "")
		es := r.run(mgArgs("--lang", "es", "drill", "--cancel"))
		if es.code != 0 || !strings.Contains(flat(es.out), "El simulacro se está deteniendo") {
			t.Fatalf("%+v", es)
		}
	})
}

// A waiting drill that is stopped (SIGTERM from systemctl stop localai-drill, or Ctrl-C) sends the same
// call. The signal is sent once the client has registered for it and posted the request; the test
// waits for the DELETE the client sends, never for a clock.
func TestDrillSignalWhileWaitingCancels(t *testing.T) {
	cancelled := func(r *drillRig) {
		r.agent().Respond(http.MethodDelete, "/drill", 202, `{"run_id":"`+drillRunID+`"}`)
	}
	waitForRun := func(t *testing.T, r *drillRig) {
		untilTrue(t, "the start request", func() bool { return r.countOf(http.MethodPost, "/drill") == 1 })
		r.ev.WaitActive(1)
	}
	abortedDoc := drillDocJSON("aborted", "aborted", false, "manual", passChecks[:2]...)

	t.Run("--wait with no terminal: the DELETE goes out at once and aictl waits for the stop", func(t *testing.T) {
		r := newDrillRig(t)
		cancelled(r)
		r.serveDoc(abortedDoc)
		ch, _ := r.runAsync(mgArgs("drill", "--wait"))
		waitForRun(t, r)
		sendSIGINT(t)
		untilTrue(t, "DELETE /drill", func() bool { return r.countOf(http.MethodDelete, "/drill") == 1 })
		r.done("aborted", false)
		got := finish(t, ch)
		if got.code != ExitInterrupted || !strings.Contains(got.out, "i Drill was stopped before it finished.") {
			t.Fatalf("%+v", got)
		}
		var methods []string
		for _, q := range r.agent().Requests() {
			if q.Path == "/drill" {
				methods = append(methods, q.Method)
			}
		}
		if strings.Join(methods, ",") != "POST,DELETE" {
			t.Errorf("requests %v", methods)
		}
	})
	t.Run("--scheduled under systemd", func(t *testing.T) {
		r := newDrillRig(t)
		cancelled(r)
		r.serveDoc(abortedDoc)
		ch, _ := r.runAsync(mgArgs("drill", "--scheduled"))
		waitForRun(t, r)
		sendSIGINT(t)
		untilTrue(t, "DELETE /drill", func() bool { return r.countOf(http.MethodDelete, "/drill") == 1 })
		r.done("aborted", false)
		if got := finish(t, ch); got.code != ExitInterrupted {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("on a terminal Ctrl-C asks first: yes cancels", func(t *testing.T) {
		r := newDrillRig(t)
		cancelled(r)
		r.serveDoc(abortedDoc)
		ch, errb := r.runAsync(mgArgs("drill", "--wait").withTerm("y"))
		waitForRun(t, r)
		sendSIGINT(t)
		untilTrue(t, "the question", func() bool { return strings.Contains(errb.String(), "Cancel the drill too? [y/N] ") })
		untilTrue(t, "DELETE /drill", func() bool { return r.countOf(http.MethodDelete, "/drill") == 1 })
		r.done("aborted", false)
		if got := finish(t, ch); got.code != ExitInterrupted {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("on a terminal Ctrl-C asks first: no leaves the drill running and sends nothing", func(t *testing.T) {
		r := newDrillRig(t)
		cancelled(r)
		ch, errb := r.runAsync(mgArgs("--lang", "es", "drill", "--wait").withTerm("n"))
		waitForRun(t, r)
		sendSIGINT(t)
		untilTrue(t, "the question", func() bool { return strings.Contains(errb.String(), "¿Cancelar también el simulacro? [s/N] ") })
		got := finish(t, ch)
		if got.code != ExitInterrupted {
			t.Fatalf("%+v", got)
		}
		if r.countOf(http.MethodDelete, "/drill") != 0 {
			t.Errorf("a cancel was sent: %v", r.requestLines())
		}
	})
}

// A lost event stream: the run ended while aictl was away, and GET /drill?run_id= says so.
func TestDrillRereadsTheRunAfterAResync(t *testing.T) {
	r := newDrillRig(t)
	r.serveDoc(drillDocJSON("warn", "", true, "manual", warnChecks...))
	ch, _ := r.runAsync(mgArgs("drill", "--wait"))
	untilTrue(t, "the start request", func() bool { return r.countOf(http.MethodPost, "/drill") == 1 })
	r.ev.WaitActive(1)
	r.started("manual")
	r.ev.Restart(false)
	got := finish(t, ch)
	if got.code != ExitWarnings || !strings.Contains(got.out, "! Drill passed with 1 warning.") {
		t.Fatalf("%+v", got)
	}
}

// ---- drill show -------------------------------------------------------------------------------------

func TestDrillShow(t *testing.T) {
	cases := []struct {
		name string
		doc  string
		exit int
		out  []string
		not  []string
	}{
		{"pass", drillDocJSON("pass", "", true, "scheduled", passChecks...), 0,
			[]string{"✓ The drill of 3 hours ago (scheduled) passed.", "✓ Survival mode is ready on this node."}, []string{"✓ Library search worked"}},
		{"warn", drillDocJSON("warn", "", true, "manual", warnChecks...), ExitWarnings,
			[]string{"! The drill of 3 hours ago (manual) passed with 1 warning.", "! Speech to text (Spanish): Speech recognition is less accurate than expected.  [LAI-DRL-031]",
				"→ Fix: Nothing urgent. A bigger whisper model helps."}, []string{"✓ Library search worked"}},
		{"fail", drillDocJSON("fail", "", false, "scheduled", failChecks...), ExitErrors,
			[]string{"✗ The drill of 3 hours ago (scheduled) failed: 1 check did not pass.", "[LAI-DRL-034]", "[LAI-DRL-031]"}, nil},
		{"aborted", drillDocJSON("aborted", "aborted", false, "manual"), ExitErrors,
			[]string{"i The drill of 3 hours ago (manual) was stopped before it finished."}, nil},
		{"skipped", drillDocJSON("skipped", "skipped", false, "scheduled"), 0,
			[]string{"i The drill of 3 hours ago (scheduled) was skipped."}, nil},
		{"running", drillDocJSON("running", "running", false, "manual"), 0,
			[]string{"… The drill of 3 hours ago (manual) is still running."}, nil},
		{"restore failed", strings.Replace(drillDocJSON("fail", "", false, "scheduled", passChecks...), `"ok":true`, `"ok":false`, 1), ExitErrors,
			[]string{"[LAI-DRL-090]", "The drill couldn't restore the normal brain"}, nil},
		{"something used the internet", strings.Replace(drillDocJSON("warn", "", true, "scheduled", passChecks...), `"blocked_dials":0`, `"blocked_dials":2`, 1), ExitWarnings,
			[]string{"[LAI-DRL-091]"}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newDrillRig(t)
			r.serveDoc(tc.doc)
			got := r.run(mgArgs("drill", "show"))
			if got.code != tc.exit {
				t.Fatalf("exit %d, want %d\n%s\n%s", got.code, tc.exit, got.out, got.errOut)
			}
			all := flat(got.out)
			for _, want := range tc.out {
				if !strings.Contains(all, flat(want)) {
					t.Errorf("lacks %q:\n%s", want, got.out)
				}
			}
			for _, no := range tc.not {
				if strings.Contains(all, flat(no)) {
					t.Errorf("must not list %q:\n%s", no, got.out)
				}
			}
			if r.lastBodyFree() {
				t.Errorf("show must not send a body")
			}
			// --json is the document unchanged, with the same exit code.
			j := r.run(mgArgs("--json", "drill", "show"))
			if j.code != tc.exit || strings.TrimSpace(j.out) != tc.doc || j.errOut != "" {
				t.Errorf("json: %+v", j)
			}
		})
	}
}

// lastBodyFree reports whether a request carried a body (show reads only).
func (r *drillRig) lastBodyFree() bool {
	for _, q := range r.agent().Requests() {
		if q.Method == http.MethodGet && q.Body != "" {
			return true
		}
	}
	return false
}

func TestDrillShowARun(t *testing.T) {
	r := newDrillRig(t)
	r.serveDoc(drillDocJSON("pass", "", true, "manual", passChecks...))
	got := r.run(mgArgs("drill", "show", drillRunID))
	if got.code != 0 {
		t.Fatalf("%+v", got)
	}
	if lines := r.requestLines(); len(lines) != 1 || lines[0] != "GET /drill?run_id="+drillRunID {
		t.Errorf("requests %v", lines)
	}
	if last := r.run(mgArgs("drill", "--last")); last.code != 0 || last.out != r.run(mgArgs("drill", "show")).out {
		t.Errorf("--last is not drill show: %+v", last)
	}
	for _, bad := range []string{"../x", "run 1", "drl-", "x"} {
		if got := newDrillRig(t).run(mgArgs("drill", "show", bad)); got.code != ExitUsage || !strings.Contains(got.errOut, "[LAI-CLI-005]") {
			t.Errorf("%q: %+v", bad, got)
		}
	}
	if got := r.run(mgArgs("drill", "show", drillRunID, "x")); got.code != ExitUsage {
		t.Errorf("two ids: %+v", got)
	}
}

func TestDrillShowNothingToShow(t *testing.T) {
	r := newDrillRig(t)
	r.agent().RespondError(http.MethodGet, "/drill", 404, "LAI-AGT-021", "No drill has run yet.", "Run aictl drill.", nil)
	got := r.run(mgArgs("drill", "show"))
	if got.code != ExitNotFound || !strings.Contains(got.errOut, "[LAI-AGT-021]") {
		t.Fatalf("%+v", got)
	}
}

func TestDrillShowManualRecord(t *testing.T) {
	r := newDrillRig(t)
	doc := strings.Replace(drillDocJSON("pass", "", false, "manual_plug"), `"checks":null`, `"checks":[]`, 1)
	doc = strings.Replace(doc, `"overall":"pass"`, `"overall":"pass","notes":"Unplugged the charger and it kept answering."`, 1)
	r.serveDoc(doc)
	got := r.run(mgArgs("drill", "show"))
	if got.code != 0 || !strings.Contains(flat(got.out), `The manual "pull the plug" drill of 3 hours ago was recorded as passed.`) ||
		!strings.Contains(got.out, "Notes: Unplugged the charger and it kept answering.") {
		t.Fatalf("%+v", got)
	}
	fail := newDrillRig(t)
	fail.serveDoc(strings.Replace(doc, `"overall":"pass"`, `"overall":"fail"`, 1))
	if got := fail.run(mgArgs("drill", "show")); got.code != ExitErrors || !strings.Contains(got.out, "✗ The manual") {
		t.Fatalf("%+v", got)
	}
}

func TestDrillShowGoldens(t *testing.T) {
	show := func(name, lang, doc string) {
		r := newDrillRig(t)
		r.serveDoc(doc)
		args := []string{"drill", "show"}
		if lang != "" {
			args = append([]string{"--lang", lang}, args...)
		}
		checkGolden(t, name, runResult(r.run(mgArgs(args...))))
	}
	show("drill.show.warn.en", "", drillDocJSON("warn", "", true, "scheduled", warnChecks...))
	show("drill.show.fail.en", "", drillDocJSON("fail", "", false, "scheduled", failChecks...))
	show("drill.show.fail.es", "es", drillDocJSON("fail", "", false, "scheduled", failChecks...))
	show("drill.show.skipped.en", "", strings.Replace(drillDocJSON("skipped", "skipped", false, "scheduled"), `"counts"`, `"reason":"survival mode","counts"`, 1))
}

// ---- drill record-manual ---------------------------------------------------------------------------

func TestDrillRecordManual(t *testing.T) {
	newRig := func(t *testing.T) *drillRig {
		r := newDrillRig(t)
		r.agent().Respond(http.MethodPost, "/drill/manual", 201, `{"run_id":"drl-20261102T150000Z-ab12-manual"}`)
		return r
	}
	t.Run("the documented body", func(t *testing.T) {
		r := newRig(t)
		got := r.run(mgArgs("drill", "record-manual", "--result", "pass", "--notes", "Unplugged the AC.", "--at", "2026-11-02T10:00:00-05:00"))
		checkRun(t, followOut{got.code, flat(got.out) + "\n", got.errOut}, 0, "✓ The manual drill was recorded as passed (run drl-20261102T150000Z-ab12-manual).\n", "")
		if b := r.lastBody(http.MethodPost, "/drill/manual"); b != `{"at":"2026-11-02T15:00:00Z","notes":"Unplugged the AC.","result":"pass"}` {
			t.Errorf("body %s", b)
		}
	})
	t.Run("only the result is required", func(t *testing.T) {
		r := newRig(t)
		got := r.run(mgArgs("--lang", "es", "drill", "record-manual", "--result", "fail"))
		if got.code != 0 || !strings.Contains(flat(got.out), "El simulacro manual se registró como fallido") {
			t.Fatalf("%+v", got)
		}
		if b := r.lastBody(http.MethodPost, "/drill/manual"); b != `{"result":"fail"}` {
			t.Errorf("body %s", b)
		}
	})
	t.Run("--json is the answer", func(t *testing.T) {
		checkRun(t, newRig(t).run(mgArgs("--json", "drill", "record-manual", "--result", "pass")), 0, `{"run_id":"drl-20261102T150000Z-ab12-manual"}`+"\n", "")
	})
	usage := [][]string{
		{"drill", "record-manual"},
		{"drill", "record-manual", "--result", "maybe"},
		{"drill", "record-manual", "--notes", "x"},
		{"drill", "record-manual", "--result", "pass", "--notes", strings.Repeat("x", 2001)},
		{"drill", "record-manual", "--result", "pass", "--notes", "bad\x07note"},
		{"drill", "record-manual", "--result", "pass", "--at", "yesterday"},
		{"drill", "record-manual", "--result", "pass", "extra"},
	}
	for _, args := range usage {
		t.Run("exit 2: "+strings.Join(args[2:], " ")[:min(24, len(strings.Join(args[2:], " ")))], func(t *testing.T) {
			r := newRig(t)
			got := r.run(mgArgs(args...))
			if got.code != ExitUsage || !strings.Contains(got.errOut, "[LAI-CLI-005]") || len(r.requestLines()) != 0 {
				t.Fatalf("%+v requests %v", got, r.requestLines())
			}
		})
	}
	t.Run("2000 characters are fine, counted as characters", func(t *testing.T) {
		r := newRig(t)
		if got := r.run(mgArgs("drill", "record-manual", "--result", "pass", "--notes", strings.Repeat("é", 2000))); got.code != 0 {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("the agent refuses the time: exit 2 with its code", func(t *testing.T) {
		r := newRig(t)
		r.agent().RespondError(http.MethodPost, "/drill/manual", 400, "LAI-AGT-020", "That time is in the future.", "Use a time in the past.", map[string]any{"field": "at", "reason": "in_the_future"})
		got := r.run(mgArgs("drill", "record-manual", "--result", "pass", "--at", "2099-01-01T00:00:00Z"))
		if got.code != ExitUsage || !strings.Contains(got.errOut, "[LAI-AGT-020]") {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("an operator needs the admin PIN", func(t *testing.T) {
		a := newAdminAgent(t)
		a.h.agent.Handle(http.MethodPost, "/drill/manual", func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("X-LocalAI-Pin-Grant") == testGrant {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(201)
				io.WriteString(w, `{"run_id":"drl-20261102T150000Z-ab12-manual"}`)
				return
			}
			fake.WriteEnvelope(w, 403, api.CodeAdminRequired, "This action needs the admin PIN.", "Enter the admin PIN.", map[string]any{"pin_required": true})
		})
		r := &mgRig{t: t, h: a.h}
		got := r.run(mgArgs("drill", "record-manual", "--result", "pass").withTerm(testPIN))
		if got.code != 0 || a.verifies() != 1 {
			t.Fatalf("%+v verifies %d", got, a.verifies())
		}
		secretFree(t, testPIN, got.out, got.errOut)
	})
	t.Run("without a terminal or --pin-stdin the PIN cannot be asked", func(t *testing.T) {
		r := newRig(t)
		r.agent().RespondError(http.MethodPost, "/drill/manual", 403, api.CodeAdminRequired, "This action needs the admin PIN.", "Enter the admin PIN.", map[string]any{"pin_required": true})
		got := r.run(mgArgs("drill", "record-manual", "--result", "pass"))
		if got.code != ExitUsage && got.code != ExitDenied || !strings.Contains(got.errOut, "[LAI-CLI-008]") {
			t.Fatalf("%+v", got)
		}
	})
}

// ---- the names of the checks --------------------------------------------------------------------------

var checkIDInSpec = regexp.MustCompile("`(drl\\.[a-z_.<>]+)`")

// Every check id of drill.md section 4.2 has a plain-language name in both languages, so a new check
// cannot be added to the spec and shown as a bare id.
func TestDrillEveryCheckOfTheSpecHasAName(t *testing.T) {
	spec, err := os.ReadFile("../../../docs/specs/drill.md")
	if err != nil {
		t.Fatal(err)
	}
	text := string(spec)
	start := strings.Index(text, "### 4.2 Checks")
	end := strings.Index(text, "### 4.3 ")
	if start < 0 || end < start {
		t.Fatal("section 4.2 of drill.md not found")
	}
	n := 0
	for _, line := range strings.Split(text[start:end], "\n") {
		if !strings.HasPrefix(line, "| ") || strings.HasPrefix(line, "| #") || strings.HasPrefix(line, "|---") {
			continue
		}
		cell := strings.SplitN(line, "|", 4)[2] // the Id column
		for _, m := range checkIDInSpec.FindAllStringSubmatch(cell, -1) {
			id := strings.NewReplacer("<id>", "some-model", "<lang>", "en").Replace(m[1])
			n++
			for _, l := range []Lang{LangEN, LangES} {
				if drillCheckLabel(l, id) == id {
					t.Errorf("check %s has no %s name", m[1], l)
				}
			}
		}
	}
	if n < 20 {
		t.Errorf("only %d check ids were read from the spec", n)
	}
}
