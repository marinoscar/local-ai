package drill

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/cloud"
	"github.com/marinoscar/local-ai/agent/internal/events"
	"github.com/marinoscar/local-ai/agent/internal/statusapi"
)

// docAt builds a document of a completed run that finished at fin.
func finishedDoc(id string, trig Trigger, overall string, fin time.Time, checks ...CheckResult) *Document {
	started := fin.Add(-30 * time.Minute)
	d := &Document{
		SchemaVersion: SchemaVersion, RunID: id, Trigger: trig, NodeID: "ai-n3", AgentVersion: "0.4.0",
		CatalogVersion: "2026.11.02-1", Mode: "online", StartedAt: started, FinishedAt: &fin,
		Overall: overall, SurvivalReady: overall != OverallFail, Checks: checks,
		Restored:     &Restored{LoadedModels: []string{"qwen3.5-9b-q4km"}, DefaultModel: "qwen3.5-9b-q4km", OK: true},
		NetworkFence: NetworkFence{Enabled: true},
	}
	if d.Checks == nil {
		d.Checks = []CheckResult{}
	}
	for _, c := range d.Checks {
		switch c.Result {
		case ResultPass:
			d.Counts.Pass++
		case ResultWarn:
			d.Counts.Warn++
		case ResultFail:
			d.Counts.Fail++
		case ResultSkip:
			d.Counts.Skip++
		}
	}
	return d
}

func manualAt(id, result string, fin time.Time, notes string) *Document {
	return &Document{
		SchemaVersion: SchemaVersion, RunID: id, Trigger: TriggerManualPlug, NodeID: "ai-n3", AgentVersion: "0.4.0",
		Mode: "online", StartedAt: fin, FinishedAt: &fin, Overall: result, Checks: []CheckResult{}, Notes: notes,
	}
}

func abortedAt(id string, fin time.Time) *Document {
	d := finishedDoc(id, TriggerManual, OverallAborted, fin)
	d.State, d.SurvivalReady, d.Reason = StateAborted, false, "aborted by request"
	return d
}

var (
	chkPass = CheckResult{ID: "drl.config.offline_lint", Area: "config", Result: ResultPass, MS: 12}
	chkWarn = CheckResult{ID: "drl.stt.es", Area: "speech", Result: ResultWarn, Code: "LAI-DRL-031", MS: 2900,
		Details: map[string]any{"wer": 0.33, "text": "el veloz murciélago hindú comía feliz cardillo y kiwi", "language": "es"}}
	chkFail = CheckResult{ID: "drl.kiwix.search", Area: "kiwix", Result: ResultFail, Code: "LAI-DRL-030", MS: 40}
)

func newReporter(t *testing.T, r *rig, opts ...func(*ReporterOptions)) *Reporter {
	t.Helper()
	o := ReporterOptions{Results: r.results, State: r.state, Clock: r.clock}
	for _, f := range opts {
		f(&o)
	}
	rep, err := NewReporter(o)
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

func saveDocs(t *testing.T, r *rig, docs ...*Document) {
	t.Helper()
	for _, d := range docs {
		if err := r.results.Save(d); err != nil {
			t.Fatal(err)
		}
	}
}

func TestNewReporterNeedsResults(t *testing.T) {
	if _, err := NewReporter(ReporterOptions{}); err == nil {
		t.Fatal("a reporter without a results store must be refused")
	}
}

func TestNoticeForAndTheDoneEvent(t *testing.T) {
	fin := t0
	sched := func(overall string) *Document {
		return finishedDoc("drl-20261101T043512Z-0001", TriggerScheduled, overall, fin)
	}
	for _, tc := range []struct {
		name   string
		doc    *Document
		want   bool
		enHas  string
		esHas  string
		wantTr string
	}{
		{"scheduled pass", sched(OverallPass), true, "everything works offline", "todo funciona sin internet", "scheduled"},
		{"scheduled warn", sched(OverallWarn), true, "found problems. Open Status.", "Abra Estado.", "scheduled"},
		{"scheduled fail", sched(OverallFail), true, "found problems. Open Status.", "Abra Estado.", "scheduled"},
		{"manual run has none", finishedDoc("drl-20261101T043512Z-0001", TriggerManual, OverallPass, fin), false, "", "", "manual"},
		{"aborted scheduled run has none", func() *Document {
			d := sched(OverallAborted)
			d.State = StateAborted
			return d
		}(), false, "", "", "scheduled"},
		{"manual_plug entry has none", manualAt("drl-20261101T043512Z-0001-manual", "pass", fin, ""), false, "", "", "manual_plug"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n, ok := NoticeFor(tc.doc)
			if ok != tc.want {
				t.Fatalf("NoticeFor ok = %v, want %v", ok, tc.want)
			}
			ev := doneEvent(tc.doc)
			if ev.Trigger != tc.wantTr || ev.RunID != tc.doc.RunID || ev.Overall != tc.doc.Overall {
				t.Fatalf("event = %+v", ev)
			}
			if !tc.want {
				if ev.Notice != nil {
					t.Fatalf("event carries a notice: %+v", ev.Notice)
				}
				return
			}
			if !strings.Contains(n.EN, tc.enHas) || !strings.Contains(n.ES, tc.esHas) || ev.Notice == nil || *ev.Notice != n {
				t.Fatalf("notice = %+v, event notice = %+v", n, ev.Notice)
			}
			raw, _ := json.Marshal(ev)
			if !strings.Contains(string(raw), `"notice":{"en":`) {
				t.Fatalf("event JSON = %s", raw)
			}
		})
	}
	if _, ok := NoticeFor(nil); ok {
		t.Fatal("a nil document has no notice")
	}
}

func TestEngineDoneEventCarriesTheNoticeOnlyForAScheduledRun(t *testing.T) {
	r := newRig(t)
	r.add(passing("drl.a", 1))
	_, sched := r.startWait(StartRequest{Trigger: TriggerScheduled})
	_, man := r.startWait(StartRequest{Trigger: TriggerManual})
	var dones []events.DrillDone
	for _, e := range r.pub.events() {
		if e.Typ == events.TypeDrillDone {
			dones = append(dones, e.Data.(events.DrillDone))
		}
	}
	if len(dones) != 2 {
		t.Fatalf("drill.done events = %+v", dones)
	}
	if dones[0].RunID != sched.RunID || dones[0].Trigger != "scheduled" || dones[0].Notice == nil ||
		dones[0].Notice.EN != "Monthly check finished: everything works offline." {
		t.Fatalf("scheduled done = %+v", dones[0])
	}
	if dones[1].RunID != man.RunID || dones[1].Trigger != "manual" || dones[1].Notice != nil {
		t.Fatalf("manual done = %+v", dones[1])
	}
}

func TestMapResultsDropsNodeIDNotesAndUnknownFieldsAndFiltersDetails(t *testing.T) {
	fin := t0
	d := finishedDoc("drl-20261101T043512Z-9c1e", TriggerScheduled, OverallWarn, fin, chkPass, chkWarn)
	d.Notes = "must not travel"
	d.Reason, d.Code = "x", "LAI-DRL-091"
	d.Checks[0].Details = map[string]any{
		"load_ms": 28400, "ratio": 0.5, "ok": true, "none": nil, "answer": "READY",
		"long":   strings.Repeat("é", 700),
		"list":   []string{"a", "b"},
		"object": map[string]any{"a": 1},
		"nan":    math.NaN(),
		"inf":    math.Inf(1),
		"count":  uint16(7),
		"when":   time.Unix(1, 0),
	}
	raw := mapResults(d)
	if raw == nil {
		t.Fatal("a completed run must map")
	}
	// Strict: decoding with unknown fields refused proves nothing outside DrillResultsV1 is present.
	var res cloud.DrillResults
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&res); err != nil {
		t.Fatalf("results are not strict DrillResultsV1: %v\n%s", err, raw)
	}
	for _, banned := range []string{"node_id", "notes", "must not travel", "reason", `"state"`, "retry_at", `"code":"LAI-DRL-091"`} {
		if strings.Contains(string(raw), banned) {
			t.Errorf("results carry %q: %s", banned, raw)
		}
	}
	if res.RunID != d.RunID || res.Trigger != "scheduled" || res.Overall != "warn" || res.Mode != "online" ||
		res.StartedAt != "2026-11-01T04:05:12Z" || res.FinishedAt != "2026-11-01T04:35:12Z" || res.CatalogVersion == nil || *res.CatalogVersion != "2026.11.02-1" {
		t.Fatalf("header = %+v", res)
	}
	if res.Counts != (cloud.DrillCounts{Pass: 1, Warn: 1}) || len(res.Checks) != 2 || !res.NetworkFence.Enabled {
		t.Fatalf("counts %+v checks %d fence %+v", res.Counts, len(res.Checks), res.NetworkFence)
	}
	got := res.Checks[0].Details
	want := map[string]any{"load_ms": float64(28400), "ratio": 0.5, "ok": true, "none": nil, "answer": "READY", "count": float64(7)}
	for k, v := range want {
		if gv, ok := got[k]; !ok || gv != v {
			t.Errorf("details[%q] = %v (present %v), want %v", k, gv, ok, v)
		}
	}
	for _, k := range []string{"list", "object", "nan", "inf", "when"} {
		if _, ok := got[k]; ok {
			t.Errorf("details[%q] must be dropped", k)
		}
	}
	if s, _ := got["long"].(string); len([]rune(s)) != 500 {
		t.Errorf("a long string is cut to 500 characters, got %d", len([]rune(s)))
	}
	if res.Restored.DefaultModel == nil || *res.Restored.DefaultModel != "qwen3.5-9b-q4km" || !res.Restored.OK || len(res.Restored.LoadedModels) != 1 {
		t.Fatalf("restored = %+v", res.Restored)
	}
}

// TestMapResultsHoldsOnlyIdsCodesNumbersAndTheFixedStrings is the guard of implementation note 1 of
// the issue: whatever a check puts in its details, the snapshot's results hold only check ids, codes,
// numbers, booleans and the fixed test strings that appear in the allowlist below.
func TestMapResultsHoldsOnlyIdsCodesNumbersAndTheFixedStrings(t *testing.T) {
	fin := t0
	checks := []CheckResult{
		{ID: "drl.llm.answer.qwen3.5-9b-q4km", Area: "llm", Result: ResultPass, MS: 41230, Details: map[string]any{"load_ms": 28400, "tokens_per_s": 31.2, "answer": "READY"}},
		{ID: "drl.vlm.photo.small-vlm", Area: "vision", Result: ResultPass, MS: 63120, Details: map[string]any{"answer": "7", "load_ms": 35100}},
		chkWarn,
		{ID: "drl.kit.rebuild_dry_run", Area: "kit", Result: ResultFail, Code: "LAI-DRL-071", MS: 1800,
			Details: map[string]any{"failing": []string{"/home/someone/private/path"}, "reason": "preflight"}},
	}
	d := finishedDoc("drl-20261101T043512Z-9c1e", TriggerScheduled, OverallFail, fin, checks...)
	d.Notes = "private note about a person"
	raw := mapResults(d)
	var tree any
	if err := json.Unmarshal(raw, &tree); err != nil {
		t.Fatal(err)
	}
	allowed := map[string]bool{
		// header and enums
		"drl-20261101T043512Z-9c1e": true, "scheduled": true, "0.4.0": true, "2026.11.02-1": true, "online": true,
		"2026-11-01T04:05:12Z": true, "2026-11-01T04:35:12Z": true, "fail": true, "warn": true, "pass": true,
		// check ids, areas, codes
		"drl.llm.answer.qwen3.5-9b-q4km": true, "llm": true, "drl.vlm.photo.small-vlm": true, "vision": true,
		"drl.stt.es": true, "speech": true, "LAI-DRL-031": true, "drl.kit.rebuild_dry_run": true, "kit": true, "LAI-DRL-071": true,
		// the fixed test strings
		"READY": true, "7": true, "el veloz murciélago hindú comía feliz cardillo y kiwi": true, "es": true, "preflight": true,
		// models
		"qwen3.5-9b-q4km": true,
	}
	var walk func(path string, v any)
	walk = func(path string, v any) {
		switch x := v.(type) {
		case map[string]any:
			for k, vv := range x {
				walk(path+"."+k, vv)
			}
		case []any:
			for _, vv := range x {
				walk(path+"[]", vv)
			}
		case string:
			if !allowed[x] {
				t.Errorf("results carry the string %q at %s", x, path)
			}
		}
	}
	walk("", tree)
}

func TestMapResultsLimitsAndFallbacks(t *testing.T) {
	fin := t0
	many := make([]CheckResult, 0, 130)
	for i := 0; i < 130; i++ {
		many = append(many, CheckResult{ID: "drl.x." + strings.Repeat("a", 120), Area: strings.Repeat("z", 50), Result: ResultPass, MS: -5, Code: "not-a-code"})
	}
	d := finishedDoc("drl-20261101T043512Z-9c1e", TriggerManual, OverallPass, fin, many...)
	d.Mode = "weird"
	d.CatalogVersion = ""
	d.Restored = nil
	d.Counts.Pass = -1
	d.NetworkFence.BlockedDials = -3
	var res cloud.DrillResults
	if err := json.Unmarshal(mapResults(d), &res); err != nil {
		t.Fatal(err)
	}
	if len(res.Checks) != 100 || len(res.Checks[0].ID) != 96 || len(res.Checks[0].Area) != 32 || res.Checks[0].MS != 0 || res.Checks[0].Code != "" {
		t.Fatalf("checks = %d, first %+v", len(res.Checks), res.Checks[0])
	}
	if res.Mode != "online" || res.CatalogVersion != nil || res.Counts.Pass != 0 || res.NetworkFence.BlockedDials != 0 {
		t.Fatalf("result = %+v", res)
	}
	if !res.Restored.OK || res.Restored.DefaultModel != nil || res.Restored.LoadedModels == nil || len(res.Restored.LoadedModels) != 0 {
		t.Fatalf("no restorer means restored ok with no models: %+v", res.Restored)
	}

	// Models that are not valid ids are dropped, an empty default is null, and a failed restore says so.
	d2 := finishedDoc("drl-20261101T043512Z-9c1f", TriggerManual, OverallFail, fin)
	d2.Restored = &Restored{LoadedModels: []string{"Bad Model", "ok-model"}, DefaultModel: "", OK: false}
	var res2 cloud.DrillResults
	if err := json.Unmarshal(mapResults(d2), &res2); err != nil {
		t.Fatal(err)
	}
	if res2.Restored.OK || res2.Restored.DefaultModel != nil || len(res2.Restored.LoadedModels) != 1 || res2.Restored.LoadedModels[0] != "ok-model" {
		t.Fatalf("restored = %+v", res2.Restored)
	}
}

func TestMapResultsRefusesWhatIsNotACompletedRun(t *testing.T) {
	fin := t0
	for name, d := range map[string]*Document{
		"nil":          nil,
		"manual_plug":  manualAt("drl-20261101T043512Z-0001-manual", "pass", fin, ""),
		"aborted":      abortedAt("drl-20261101T043512Z-0002", fin),
		"no finish":    {RunID: "drl-20261101T043512Z-0003", Overall: OverallPass},
		"skipped":      {RunID: "drl-20261101T043512Z-0004", State: StateSkipped, Overall: OverallSkipped, FinishedAt: &fin},
		"odd overall":  {RunID: "drl-20261101T043512Z-0005", Overall: "weird", FinishedAt: &fin},
		"running copy": {RunID: "drl-20261101T043512Z-0006", State: StateRunning, Overall: OverallRunning, FinishedAt: &fin},
	} {
		if raw := mapResults(d); raw != nil {
			t.Errorf("%s was mapped: %s", name, raw)
		}
	}
}

func TestFailuresListCodesAndIdsOfFailedAndWarnedChecks(t *testing.T) {
	fin := t0
	d := finishedDoc("drl-20261101T043512Z-9c1e", TriggerScheduled, OverallFail, fin,
		chkPass, chkWarn, chkFail,
		CheckResult{ID: "drl.stt.en", Area: "speech", Result: ResultWarn, Code: "LAI-DRL-031"}, // same code twice
		CheckResult{ID: "drl.files.hash", Area: "files", Result: ResultSkip, Code: "LAI-DRL-012"},
		CheckResult{ID: "drl.nocode", Area: "x", Result: ResultFail},
		CheckResult{ID: "drl.badcode", Area: "x", Result: ResultFail, Code: "bad"},
	)
	d.NetworkFence.Code = "LAI-DRL-091"
	d.Code = "LAI-DRL-090"
	codes, ids := failures(d)
	wantCodes := []string{"LAI-DRL-031", "LAI-DRL-030", "LAI-DRL-091", "LAI-DRL-090"}
	wantIDs := []string{"drl.stt.es", "drl.kiwix.search", "drl.stt.en", "drl.nocode", "drl.badcode"}
	if strings.Join(codes, ",") != strings.Join(wantCodes, ",") || strings.Join(ids, ",") != strings.Join(wantIDs, ",") {
		t.Fatalf("codes %v ids %v", codes, ids)
	}
	if codes, ids := failures(finishedDoc("drl-20261101T043512Z-9c1f", TriggerManual, OverallPass, fin, chkPass)); len(codes) != 0 || len(ids) != 0 || codes == nil || ids == nil {
		t.Fatalf("a pass run lists nothing (and never nil): %v %v", codes, ids)
	}
	// The lists are held to 50.
	var many []CheckResult
	for i := 0; i < 80; i++ {
		many = append(many, CheckResult{ID: "drl.c" + strings.Repeat("x", i%5) + string(rune('a'+i%26)) + string(rune('a'+i/26)), Area: "x", Result: ResultFail, Code: "LAI-DRL-" + threeDigits(100+i)})
	}
	codes, ids = failures(finishedDoc("drl-20261101T043512Z-9c20", TriggerManual, OverallFail, fin, many...))
	if len(codes) != 50 || len(ids) != 50 {
		t.Fatalf("lists = %d and %d", len(codes), len(ids))
	}
}

func threeDigits(n int) string {
	return string([]byte{byte('0' + n/100%10), byte('0' + n/10%10), byte('0' + n%10)})
}

func TestReporterLoadPicksTheNewestCompletedRunAndManualEntryAndIgnoresTheRest(t *testing.T) {
	r := newRig(t)
	day := func(n int) time.Time { return t0.Add(time.Duration(n) * 24 * time.Hour) }
	saveDocs(t, r,
		finishedDoc("drl-20261001T043512Z-0001", TriggerScheduled, OverallPass, day(-30), chkPass),
		finishedDoc("drl-20261015T043512Z-0002", TriggerManual, OverallFail, day(-16), chkFail), // the newest completed run
		manualAt("drl-20261010T150000Z-0003-manual", "fail", day(-21), "older manual"),
		manualAt("drl-20261020T150000Z-0004-manual", "pass", day(-11), "newest manual"),
		abortedAt("drl-20261025T043512Z-0005", day(-7)), // newer than all, but not a result
		&Document{RunID: "drl-20261028T043512Z-0006", State: StateSkipped, Overall: OverallSkipped, FinishedAt: ptr(day(-4)), StartedAt: day(-4), Checks: []CheckResult{}},
	)
	rep := newReporter(t, r)
	if err := rep.Load(); err != nil {
		t.Fatal(err)
	}
	last, ok := rep.LastRun()
	if !ok || last.RunID != "drl-20261015T043512Z-0002" || last.Overall != OverallFail || last.Trigger != TriggerManual ||
		!last.FinishedAt.Equal(day(-16)) || strings.Join(last.FailedChecks, ",") != "LAI-DRL-030" || strings.Join(last.FailedCheckIDs, ",") != "drl.kiwix.search" {
		t.Fatalf("last run = %+v (%v)", last, ok)
	}
	if !rep.Anchor().Equal(day(-16)) {
		t.Fatalf("anchor = %v, want the end of the last completed run", rep.Anchor())
	}
	// The manual entry is newer than the run: the snapshot input is the manual one.
	dr := rep.Drill()
	if dr == nil || !dr.ManualPlug || dr.RunID != "drl-20261020T150000Z-0004-manual" || dr.Result != "pass" || !dr.LastRunAt.Equal(day(-11)) {
		t.Fatalf("drill report = %+v", dr)
	}
}

func ptr[T any](v T) *T { return &v }

func TestReporterDrillReportShapes(t *testing.T) {
	r := newRig(t)
	rep := newReporter(t, r)
	if err := rep.Load(); err != nil {
		t.Fatal(err)
	}
	if rep.Drill() != nil {
		t.Fatal("no drill ever ran: the block is null")
	}
	if _, ok := rep.LastRun(); ok {
		t.Fatal("no last run")
	}

	// An automatic run gives every field and its results without node_id.
	run := finishedDoc("drl-20261101T043512Z-9c1e", TriggerScheduled, OverallWarn, t0, chkPass, chkWarn)
	rep.Observe(run)
	dr := rep.Drill()
	if dr == nil || dr.ManualPlug || dr.RunID != run.RunID || dr.Result != "warn" || !dr.SurvivalReady || !dr.LastRunAt.Equal(t0) ||
		strings.Join(dr.FailedChecks, ",") != "LAI-DRL-031" || strings.Join(dr.FailedCheckIDs, ",") != "drl.stt.es" || len(dr.ResultsJSON) == 0 {
		t.Fatalf("report = %+v", dr)
	}
	if bytes.Contains(dr.ResultsJSON, []byte("node_id")) {
		t.Fatal("node_id must be dropped")
	}
	// The caller owns the bytes.
	dr.ResultsJSON[0] = 'X'
	if rep.Drill().ResultsJSON[0] != '{' {
		t.Fatal("Drill must hand out a copy of the results")
	}

	// A manual_plug entry newer than the run: only run_id, last_run_at and result change; survival_ready is the
	// run's, the lists are empty and the results are not sent.
	rep.Observe(manualAt("drl-20261102T150000Z-ab12-manual", "fail", t0.Add(35*time.Hour), "notes"))
	dr = rep.Drill()
	if dr == nil || !dr.ManualPlug || dr.RunID != "drl-20261102T150000Z-ab12-manual" || dr.Result != "fail" || !dr.SurvivalReady ||
		!dr.LastRunAt.Equal(t0.Add(35*time.Hour)) || dr.FailedChecks == nil || len(dr.FailedChecks) != 0 || dr.FailedCheckIDs == nil || len(dr.FailedCheckIDs) != 0 || dr.ResultsJSON != nil {
		t.Fatalf("manual report = %+v", dr)
	}
	if strings.Contains(string(mustJSON(t, dr)), "notes") {
		t.Fatal("the notes never leave the node")
	}
	// The last automatic run is still the one the alerts see.
	if last, _ := rep.LastRun(); last.RunID != run.RunID {
		t.Fatalf("a manual_plug entry must not become the last run: %+v", last)
	}

	// A newer automatic run takes over again, with its results.
	next := finishedDoc("drl-20261105T043512Z-0001", TriggerScheduled, OverallPass, t0.Add(4*24*time.Hour), chkPass)
	rep.Observe(next)
	dr = rep.Drill()
	if dr.ManualPlug || dr.RunID != next.RunID || len(dr.ResultsJSON) == 0 || len(dr.FailedChecks) != 0 {
		t.Fatalf("report = %+v", dr)
	}

	// An aborted run, a skipped one and an older run change nothing.
	rep.Observe(abortedAt("drl-20261106T043512Z-0002", t0.Add(5*24*time.Hour)))
	rep.Observe(&Document{RunID: "drl-20261106T043512Z-0003", State: StateSkipped, Overall: OverallSkipped})
	rep.Observe(finishedDoc("drl-20261102T043512Z-0004", TriggerManual, OverallFail, t0.Add(24*time.Hour), chkFail))
	rep.Observe(nil)
	if dr = rep.Drill(); dr.RunID != next.RunID {
		t.Fatalf("report = %+v", dr)
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestReporterManualEntryOnItsOwnHasNoSurvivalReadiness(t *testing.T) {
	r := newRig(t)
	rep := newReporter(t, r)
	rep.Observe(manualAt("drl-20261102T150000Z-ab12-manual", "pass", t0, "ok"))
	dr := rep.Drill()
	if dr == nil || !dr.ManualPlug || dr.SurvivalReady || dr.Result != "pass" || dr.ResultsJSON != nil {
		t.Fatalf("report = %+v", dr)
	}
	if _, ok := rep.LastRun(); ok {
		t.Fatal("a manual_plug entry is not a run of the drill")
	}
}

func TestReporterOnChangeFiresOnlyWhenTheReportChanges(t *testing.T) {
	r := newRig(t)
	var n atomic.Int32
	rep := newReporter(t, r, func(o *ReporterOptions) { o.OnChange = func() { n.Add(1) } })
	if err := rep.Load(); err != nil {
		t.Fatal(err)
	}
	base := n.Load()
	rep.Observe(finishedDoc("drl-20261101T043512Z-0001", TriggerScheduled, OverallPass, t0, chkPass))
	if n.Load() != base+1 {
		t.Fatalf("a new run must notify: %d", n.Load()-base)
	}
	rep.Observe(abortedAt("drl-20261101T053512Z-0002", t0.Add(time.Hour)))
	rep.Observe(finishedDoc("drl-20261031T043512Z-0003", TriggerManual, OverallPass, t0.Add(-24*time.Hour), chkPass)) // older
	if n.Load() != base+1 {
		t.Fatalf("an aborted or older run must not notify: %d", n.Load()-base)
	}
}

func TestReporterLoadStampsWatchSinceOnceAndKeepsTheOtherKeys(t *testing.T) {
	r := newRig(t)
	r.state.setBlock(`{"_owner":"drill.md","last_result":"pass","future_key":{"a":1}}`)
	rep := newReporter(t, r)
	if err := rep.Load(); err != nil {
		t.Fatal(err)
	}
	blk := r.state.block(t)
	var since time.Time
	if err := json.Unmarshal(blk["watch_since"], &since); err != nil || !since.Equal(t0) {
		t.Fatalf("watch_since = %s (%v), want %v", blk["watch_since"], err, t0)
	}
	if string(blk["future_key"]) != `{"a":1}` || string(blk["last_result"]) != `"pass"` {
		t.Fatalf("other keys were not kept: %v", blk)
	}
	// A later load, with the clock moved on, does not move the stamp.
	r.clock.Advance(48 * time.Hour)
	if err := rep.Load(); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(r.state.block(t)["watch_since"], &since); err != nil || !since.Equal(t0) {
		t.Fatalf("watch_since moved to %v", since)
	}
	if !rep.Anchor().Equal(t0) {
		t.Fatalf("with no run, the anchor is watch_since: %v", rep.Anchor())
	}
}

func TestReporterAnchorFallsBackToStateLastRunThenWatchSince(t *testing.T) {
	r := newRig(t)
	last := t0.Add(-10 * 24 * time.Hour)
	r.state.setBlock(`{"last_run_at":"` + last.Format(time.RFC3339) + `","watch_since":"2026-09-01T00:00:00Z"}`)
	rep := newReporter(t, r)
	if !rep.Anchor().Equal(last) {
		t.Fatalf("anchor = %v, want state.last_run_at while the documents are gone", rep.Anchor())
	}
	r.state.setBlock(`{"watch_since":"2026-09-01T00:00:00Z"}`)
	if want := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC); !rep.Anchor().Equal(want) {
		t.Fatalf("anchor = %v", rep.Anchor())
	}
	r.state.setBlock(`{"watch_since":"garbage"}`)
	if !rep.Anchor().IsZero() {
		t.Fatalf("an unreadable stamp is no anchor: %v", rep.Anchor())
	}
	// Without a state store there is no fallback.
	rep2, _ := NewReporter(ReporterOptions{Results: r.results})
	if !rep2.Anchor().IsZero() {
		t.Fatal("no state store, no anchor")
	}
	if err := rep2.Load(); err != nil {
		t.Fatal(err)
	}
}

func TestReporterLoadFailsWhenTheDirectoryCannotBeRead(t *testing.T) {
	r := newRig(t)
	rep := newReporter(t, r)
	r.fs.fail = func(op, _ string) error {
		if op == "readdir" {
			return errors.New("disk unreadable")
		}
		return nil
	}
	if err := rep.Load(); err == nil {
		t.Fatal("an unreadable results directory must be reported")
	}
}

// statusStub is the least a snapshot builder needs from the status view.
type statusStub struct{}

func (statusStub) Build(context.Context, bool, statusapi.BuildOptions) statusapi.Status {
	return statusapi.Status{Node: statusapi.NodeBlock{ID: "ai-n3", AgentVersion: "0.4.0", Backend: "cpu"}}
}

// TestSnapshotCarriesTheDrillBlockWithResultsOnceThenNull is the fake cloud of the acceptance
// criteria: the real snapshot builder over the reporter sends the summary with results once, then
// results null, a manual entry never as results, and a results document the cloud did not accept goes
// out again.
func TestSnapshotCarriesTheDrillBlockWithResultsOnceThenNull(t *testing.T) {
	r := newRig(t)
	rep := newReporter(t, r)
	sb, err := cloud.NewSnapshotBuilder(cloud.SnapshotOptions{
		Status: statusStub{}, Drill: rep, BootID: "5d2f6a54-0f6e-4c53-9d1e-4b6f3f2a7c10", Now: r.clock.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	build := func() *cloud.Built {
		t.Helper()
		b, err := sb.Build(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		return b
	}

	if b := build(); b.Snapshot.Drill != nil {
		t.Fatalf("no drill ran: the block is null, got %+v", b.Snapshot.Drill)
	}

	run := finishedDoc("drl-20261101T043512Z-9c1e", TriggerScheduled, OverallWarn, t0, chkPass, chkWarn)
	rep.Observe(run)

	b1 := build() // the cloud does not accept this one
	d := b1.Snapshot.Drill
	if d == nil || d.RunID != run.RunID || d.Result != "warn" || !d.SurvivalReady || d.LastRunAt != "2026-11-01T04:35:12Z" ||
		strings.Join(d.FailedChecks, ",") != "LAI-DRL-031" || strings.Join(d.FailedCheckIDs, ",") != "drl.stt.es" || d.Results == nil {
		t.Fatalf("drill block = %+v", d)
	}
	if d.Results.RunID != run.RunID || len(d.Results.Checks) != 2 || d.Results.Trigger != "scheduled" {
		t.Fatalf("results = %+v", d.Results)
	}
	// node_id appears once, at the top of the snapshot; neither the block nor the results repeat it.
	if bytes.Count(b1.Body, []byte("node_id")) != 1 {
		t.Fatalf("node_id appears outside the top level: %s", b1.Body)
	}
	// Strict: the whole snapshot decodes with no unknown field.
	var back cloud.StateSnapshot
	dec := json.NewDecoder(bytes.NewReader(b1.Body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&back); err != nil {
		t.Fatalf("the snapshot is not strict: %v", err)
	}

	b2 := build() // not committed: the results go out again
	if b2.Snapshot.Drill.Results == nil {
		t.Fatal("a snapshot the cloud did not accept must send the results again")
	}
	b2.Commit()
	b3 := build()
	if b3.Snapshot.Drill == nil || b3.Snapshot.Drill.Results != nil || b3.Snapshot.Drill.RunID != run.RunID {
		t.Fatalf("after the cloud took the results: %+v", b3.Snapshot.Drill)
	}

	// A manual_plug entry: summary only, survival_ready from the run, empty lists, no results.
	rep.Observe(manualAt("drl-20261102T150000Z-ab12-manual", "fail", t0.Add(35*time.Hour), "private"))
	b4 := build()
	m := b4.Snapshot.Drill
	if m == nil || m.RunID != "drl-20261102T150000Z-ab12-manual" || m.Result != "fail" || !m.SurvivalReady || m.Results != nil ||
		len(m.FailedChecks) != 0 || len(m.FailedCheckIDs) != 0 || m.LastRunAt != "2026-11-02T15:35:12Z" {
		t.Fatalf("manual block = %+v", m)
	}
	if bytes.Contains(b4.Body, []byte("private")) {
		t.Fatal("the notes must not be in the snapshot")
	}
	b4.Commit()

	// The next automatic run sends its results once again.
	next := finishedDoc("drl-20261201T043512Z-0001", TriggerScheduled, OverallPass, t0.Add(30*24*time.Hour), chkPass)
	rep.Observe(next)
	b5 := build()
	if b5.Snapshot.Drill.Results == nil || b5.Snapshot.Drill.Results.RunID != next.RunID {
		t.Fatalf("block = %+v", b5.Snapshot.Drill)
	}
	b5.Commit()
	if b6 := build(); b6.Snapshot.Drill.Results != nil {
		t.Fatal("results go out once")
	}
}
