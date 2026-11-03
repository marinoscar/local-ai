package drill

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/events"
)

// gate is a check that blocks until released or until its context ends.
type gate struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
	ctxErr  chan error
}

func newGate() *gate {
	return &gate{started: make(chan struct{}), release: make(chan struct{}), ctxErr: make(chan error, 1)}
}

func (g *gate) check(id string, order int) Check {
	return Check{ID: id, Order: order, Run: func(ctx context.Context, _ *Env) Outcome {
		g.once.Do(func() { close(g.started) })
		select {
		case <-g.release:
			return Outcome{Result: ResultPass}
		case <-ctx.Done():
			g.ctxErr <- ctx.Err()
			return Outcome{Result: ResultFail}
		}
	}}
}

// TestGoldenDocument: fake checks produce a document identical in shape to drill.md section 3.4.
func TestGoldenDocument(t *testing.T) {
	r := newRig(t)
	adv := func(ms int) { r.clock.Advance(time.Duration(ms) * time.Millisecond) }
	r.add(Check{ID: "drl.llm.answer.qwen3.5-9b-q4km", Area: "llm", Critical: true, Order: 5, Timeout: 300 * time.Second,
		Run: func(context.Context, *Env) Outcome {
			adv(41230)
			return Outcome{Result: ResultPass, Details: map[string]any{"load_ms": 28400, "tokens_per_s": 31.2, "answer": "READY"}}
		}})
	r.add(Check{ID: "drl.vlm.photo.vlm-small", Area: "vision", Critical: true, Order: 6, Timeout: 300 * time.Second,
		Run: func(context.Context, *Env) Outcome {
			adv(63120)
			return Outcome{Result: ResultPass, Details: map[string]any{"answer": "7", "load_ms": 35100}}
		}})
	r.add(Check{ID: "drl.stt.es", Area: "speech", Critical: true, Order: 10,
		Run: func(context.Context, *Env) Outcome {
			adv(2900)
			return Outcome{Result: ResultWarn, Code: doctor.DRL031, Details: map[string]any{
				"wer": 0.33, "text": "el veloz murciélago hindú comía feliz cardillo y kiwi", "language": "es"}}
		}})
	r.add(Check{ID: "drl.kit.rebuild_dry_run", Area: "kit", Critical: true, Order: 18,
		Run: func(context.Context, *Env) Outcome { adv(1800); return Outcome{Result: ResultPass} }})

	res, _ := r.startWait(StartRequest{Trigger: TriggerScheduled})
	raw, err := r.eng.Get(res.RunID)
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile("testdata/results.golden.json")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, want) {
		t.Fatalf("document differs from testdata/results.golden.json\n--- got\n%s\n--- want\n%s", raw, want)
	}

	// The shape: exactly the top-level keys of drill.md section 3.4, and the same bytes through Latest.
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		t.Fatal(err)
	}
	var keys []string
	for k := range top {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	wantKeys := []string{"agent_version", "catalog_version", "checks", "counts", "finished_at", "mode", "network_fence", "node_id",
		"overall", "quick", "restored", "run_id", "schema_version", "started_at", "survival_ready", "trigger"}
	if !reflect.DeepEqual(keys, wantKeys) {
		t.Fatalf("top-level keys = %v, want %v", keys, wantKeys)
	}
	latest, err := r.eng.Latest()
	if err != nil || !bytes.Equal(latest, raw) {
		t.Fatalf("Latest differs from the run document (%v)", err)
	}
}

func TestAggregationTable(t *testing.T) {
	rec := func(res Result, critical, neutral bool) record {
		return record{CheckResult: CheckResult{Result: res}, critical: critical, neutral: neutral}
	}
	for _, tc := range []struct {
		name    string
		recs    []record
		overall string
		ready   bool
		counts  Counts
	}{
		{"all pass", []record{rec("pass", true, false), rec("pass", false, false)}, "pass", true, Counts{Pass: 2}},
		{"a warn", []record{rec("pass", true, false), rec("warn", false, false)}, "warn", true, Counts{Pass: 1, Warn: 1}},
		{"a fail beats a warn", []record{rec("warn", false, false), rec("fail", false, false), rec("pass", true, false)}, "fail", true, Counts{Pass: 1, Warn: 1, Fail: 1}},
		{"a critical warn is still ready", []record{rec("warn", true, false)}, "warn", true, Counts{Warn: 1}},
		{"a critical fail is not ready", []record{rec("pass", true, false), rec("fail", true, false)}, "fail", false, Counts{Pass: 1, Fail: 1}},
		{"a non-critical fail leaves it ready", []record{rec("pass", true, false), rec("fail", false, false)}, "fail", true, Counts{Pass: 1, Fail: 1}},
		{"a critical skip is not ready", []record{rec("pass", true, false), rec("skip", true, false)}, "pass", false, Counts{Pass: 1, Skip: 1}},
		{"a critical skip by quick does not count against it", []record{rec("pass", true, false), rec("skip", true, true)}, "pass", true, Counts{Pass: 1, Skip: 1}},
		{"a non-critical skip is harmless", []record{rec("pass", true, false), rec("skip", false, false)}, "pass", true, Counts{Pass: 1, Skip: 1}},
		{"no critical check ran: nothing proves readiness", []record{rec("pass", false, false)}, "pass", false, Counts{Pass: 1}},
		{"only a quick-skipped critical check: nothing proves readiness", []record{rec("skip", true, true)}, "pass", false, Counts{Skip: 1}},
		{"no checks", nil, "pass", false, Counts{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			overall, ready, counts := aggregate(tc.recs)
			if overall != tc.overall || ready != tc.ready || counts != tc.counts {
				t.Fatalf("aggregate = %s %v %+v, want %s %v %+v", overall, ready, counts, tc.overall, tc.ready, tc.counts)
			}
		})
	}
}

func TestLifecycleEventsStateAndMetrics(t *testing.T) {
	r := newRig(t)
	r.add(passing("drl.a", 1))
	r.add(Check{ID: "drl.b", Area: "x", Order: 2, FailCode: doctor.DRL011, Run: func(context.Context, *Env) Outcome {
		return Outcome{Result: ResultWarn, Code: doctor.DRL031}
	}})
	res, d := r.startWait(StartRequest{Trigger: TriggerManual, OpID: "op_abc"})

	if !regexp.MustCompile(`^drl-20261101T043512Z-[0-9a-f]{4}$`).MatchString(res.RunID) {
		t.Fatalf("run id = %q", res.RunID)
	}
	if d.Overall != OverallWarn || d.Trigger != TriggerManual || d.NodeID != "ai-n3" || d.Mode != "online" {
		t.Fatalf("document header = %+v", d)
	}
	evs := r.pub.events()
	var types []string
	for _, e := range evs {
		types = append(types, e.Typ)
		if e.OpID != "op_abc" {
			t.Errorf("%s carries op id %q, want op_abc", e.Typ, e.OpID)
		}
	}
	wantTypes := []string{events.TypeDrillStarted, events.TypeDrillCheckDone, events.TypeDrillCheckDone, events.TypeDrillDone}
	if !reflect.DeepEqual(types, wantTypes) {
		t.Fatalf("event order = %v, want %v", types, wantTypes)
	}
	if got := evs[0].Data.(events.DrillStarted); got != (events.DrillStarted{RunID: res.RunID, Trigger: "manual", Quick: false}) {
		t.Errorf("drill.started = %+v", got)
	}
	if got := evs[1].Data.(events.DrillCheckDone); got != (events.DrillCheckDone{RunID: res.RunID, ID: "drl.a", Result: "pass"}) {
		t.Errorf("first drill.check_done = %+v", got)
	}
	if got := evs[2].Data.(events.DrillCheckDone); got != (events.DrillCheckDone{RunID: res.RunID, ID: "drl.b", Result: "warn", Code: doctor.DRL031}) {
		t.Errorf("second drill.check_done = %+v", got)
	}
	if got := evs[3].Data.(events.DrillDone); got != (events.DrillDone{RunID: res.RunID, Overall: "warn", SurvivalReady: false, Trigger: "manual"}) {
		t.Errorf("drill.done = %+v", got)
	}

	blk := r.state.block(t)
	if string(blk["in_progress"]) != "null" {
		t.Errorf("drill.in_progress = %s after the run, want null", blk["in_progress"])
	}
	if string(blk["last_result"]) != `"warn"` || string(blk["last_run_at"]) != `"2026-11-01T04:35:12Z"` || string(blk["_owner"]) != `"drill.md"` {
		t.Errorf("drill block = %v", blk)
	}
	if v, ok := r.metrics.get(MetricLastOverall); !ok || v != 1 {
		t.Errorf("%s = %v %v, want 1", MetricLastOverall, v, ok)
	}
	if v, _ := r.metrics.get(MetricLastFinished); v != float64(t0.Unix()) {
		t.Errorf("%s = %v", MetricLastFinished, v)
	}
	if _, ok := r.metrics.get(MetricCheckMS + "{drl.a}"); !ok {
		t.Error("no localai_drill_check_ms for drl.a")
	}
	if id, running := r.eng.Running(); running || id != "" {
		t.Errorf("Running() = %q %v after the run", id, running)
	}
	if r.fence.isEnabled() || r.fence.enables != 1 || r.fence.disables != 1 {
		t.Errorf("fence enables=%d disables=%d enabled=%v", r.fence.enables, r.fence.disables, r.fence.isEnabled())
	}
}

func TestInProgressIsSetDuringTheRunAndCleared(t *testing.T) {
	r := newRig(t)
	var seen map[string]json.RawMessage
	r.add(Check{ID: "drl.peek", Area: "x", Order: 1, Run: func(context.Context, *Env) Outcome {
		seen = r.state.block(t)
		return Outcome{Result: ResultPass}
	}})
	res, _ := r.startWait(StartRequest{})
	var p InProgress
	if err := json.Unmarshal(seen["in_progress"], &p); err != nil {
		t.Fatalf("in_progress during the run = %s", seen["in_progress"])
	}
	want := InProgress{RunID: res.RunID, StartedAt: t0, Captured: r.rest.captured}
	if !reflect.DeepEqual(p, want) {
		t.Fatalf("in_progress = %+v, want %+v", p, want)
	}
	if string(r.state.block(t)["in_progress"]) != "null" {
		t.Fatal("in_progress must be null after the run")
	}
}

func TestStatePreservesUnknownKeysOfTheDrillBlock(t *testing.T) {
	r := newRig(t)
	r.state.setBlock(`{"_owner":"drill.md","future_field":{"a":1},"last_result":"pass"}`)
	r.add(passing("drl.a", 1))
	r.startWait(StartRequest{})
	blk := r.state.block(t)
	if string(blk["future_field"]) != `{"a":1}` {
		t.Fatalf("an unknown key of the drill block was lost: %v", blk)
	}
}

func TestTimeoutFailsWithItsOwnCodeAndTheRunContinues(t *testing.T) {
	r := newRig(t)
	g := newGate()
	hang := g.check("drl.hang", 1)
	hang.FailCode, hang.Timeout, hang.Critical = doctor.DRL020, 300*time.Second, true
	r.add(hang)
	r.add(passing("drl.after", 2))

	res, err := r.eng.Start(context.Background(), StartRequest{})
	if err != nil {
		t.Fatal(err)
	}
	<-g.started // the engine made the timer before it launched the check
	r.clock.Advance(300 * time.Second)
	r.wait()

	if err := <-g.ctxErr; !errors.Is(err, context.Canceled) {
		t.Fatalf("the hung check's context ended with %v, want canceled", err)
	}
	d := r.doc(res.RunID)
	if len(d.Checks) != 2 {
		t.Fatalf("checks = %+v", d.Checks)
	}
	h := d.Checks[0]
	if h.Result != ResultFail || h.Code != doctor.DRL020 || h.MS != 300_000 || h.Details["reason"] != "timeout" || h.Details["timeout_ms"] != float64(300_000) {
		t.Fatalf("timed-out check = %+v", h)
	}
	if d.Checks[1].Result != ResultPass {
		t.Fatal("the run must continue after a timeout")
	}
	if d.Overall != OverallFail || d.SurvivalReady {
		t.Fatalf("overall %s ready %v: a critical timeout fails the run", d.Overall, d.SurvivalReady)
	}
	if len(r.rest.restoreCalls()) != 1 {
		t.Fatal("the restore must run after a timeout")
	}
}

func TestDefaultTimeoutIsThirtySeconds(t *testing.T) {
	r := newRig(t)
	g := newGate()
	c := g.check("drl.hang", 1)
	c.FailCode = doctor.DRL030
	r.add(c)
	res, _ := r.eng.Start(context.Background(), StartRequest{})
	<-g.started
	r.clock.Advance(29 * time.Second)
	select {
	case <-g.ctxErr:
		t.Fatal("the check timed out before 30 s")
	default:
	}
	r.clock.Advance(time.Second)
	r.wait()
	if d := r.doc(res.RunID); d.Checks[0].Details["timeout_ms"] != float64(30_000) {
		t.Fatalf("details = %v", d.Checks[0].Details)
	}
}

func TestPanicInACheckIsAFailureAndTheRunContinues(t *testing.T) {
	r := newRig(t)
	r.add(Check{ID: "drl.boom", Area: "x", Order: 1, FailCode: doctor.DRL034, Run: func(context.Context, *Env) Outcome {
		panic("kaboom " + strings.Repeat("x", 500))
	}})
	r.add(passing("drl.after", 2))
	_, d := r.startWait(StartRequest{})
	b := d.Checks[0]
	if b.Result != ResultFail || b.Code != doctor.DRL034 || b.Details["reason"] != "panic" {
		t.Fatalf("panicking check = %+v", b)
	}
	if got := b.Details["panic"].(string); !strings.HasPrefix(got, "kaboom") || len(got) > 210 {
		t.Fatalf("panic text = %q (clipped?)", got)
	}
	if d.Checks[1].Result != ResultPass || d.State == StateAborted {
		t.Fatal("a panic in a check must not end the run")
	}
	if len(r.rest.restoreCalls()) != 1 {
		t.Fatal("the restore must run after a panic in a check")
	}
}

func TestInvalidOutcomeResultIsAFailure(t *testing.T) {
	r := newRig(t)
	r.add(Check{ID: "drl.odd", Area: "x", Order: 1, FailCode: doctor.DRL021, Run: func(context.Context, *Env) Outcome {
		return Outcome{Result: "great"}
	}})
	r.add(Check{ID: "drl.passcode", Area: "x", Order: 2, Run: func(context.Context, *Env) Outcome {
		return Outcome{Result: ResultPass, Code: doctor.DRL031}
	}})
	_, d := r.startWait(StartRequest{})
	if c := d.Checks[0]; c.Result != ResultFail || c.Code != doctor.DRL021 || c.Details["reason"] != "invalid_result" {
		t.Fatalf("invalid result = %+v", c)
	}
	if d.Checks[1].Code != "" {
		t.Fatalf("a pass must carry no code, got %q", d.Checks[1].Code)
	}
}

func TestOnlyFilterRunsJustTheSelectedChecks(t *testing.T) {
	r := newRig(t)
	var ran []string
	var mu sync.Mutex
	for i, id := range []string{"drl.fence", "drl.stt.en", "drl.stt.es", "drl.tts.en"} {
		id := id
		r.add(Check{ID: id, Area: "x", Order: i + 1, Critical: true, Run: func(context.Context, *Env) Outcome {
			mu.Lock()
			ran = append(ran, id)
			mu.Unlock()
			return Outcome{Result: ResultPass}
		}})
	}
	_, d := r.startWait(StartRequest{Only: []string{"drl.fence", "drl.stt"}})
	if !reflect.DeepEqual(ran, []string{"drl.fence", "drl.stt.en", "drl.stt.es"}) || len(d.Checks) != 3 {
		t.Fatalf("ran %v, document has %d checks", ran, len(d.Checks))
	}
	if !r.fence.isEnabled() && r.fence.enables != 1 {
		t.Fatal("the fence covers every run, whatever --only selects")
	}
	_, d = r.startWait(StartRequest{Only: []string{"drl.fence"}})
	if len(d.Checks) != 1 || d.Checks[0].ID != "drl.fence" {
		t.Fatalf("--only drl.fence reported %+v", d.Checks)
	}
}

func TestQuickSkipsTheChecksThatSayso(t *testing.T) {
	r := newRig(t)
	r.add(Check{ID: "drl.fast", Area: "x", Order: 1, Critical: true, Run: run0})
	r.add(Check{ID: "drl.hash", Area: "files", Order: 2, Critical: true, SkipInQuick: true, Run: func(context.Context, *Env) Outcome {
		t.Error("a quick run must not run a SkipInQuick check")
		return Outcome{Result: ResultFail}
	}})
	_, d := r.startWait(StartRequest{Quick: true})
	if !d.Quick || d.Checks[1].Result != ResultSkip || d.Checks[1].Details["reason"] != "quick" {
		t.Fatalf("quick document = %+v", d)
	}
	if d.Counts.Skip != 1 || d.Overall != OverallPass || !d.SurvivalReady {
		t.Fatalf("a quick skip must not make the run unready: %+v", d)
	}

	// The same check runs in a full run.
	r2 := newRig(t)
	ran := false
	r2.add(Check{ID: "drl.hash", Area: "files", Order: 1, Critical: true, SkipInQuick: true, Run: func(context.Context, *Env) Outcome {
		ran = true
		return Outcome{Result: ResultPass}
	}})
	r2.startWait(StartRequest{})
	if !ran {
		t.Fatal("a full run must run the check")
	}
}

func TestStopRunSkipsTheRestButStillRestoresAndRunsPostRestoreChecks(t *testing.T) {
	r := newRig(t)
	r.add(Check{ID: "drl.fence", Area: "net", Order: 1, Critical: true, Run: func(context.Context, *Env) Outcome {
		return Outcome{Result: ResultFail, Code: doctor.DRL091, StopRun: true}
	}})
	r.add(Check{ID: "drl.never", Area: "x", Order: 2, Critical: true, Run: func(context.Context, *Env) Outcome {
		t.Error("a check after StopRun ran")
		return Outcome{Result: ResultPass}
	}})
	var sawRestored *Restored
	r.add(Check{ID: "drl.restore", Area: "state", Order: 22, Phase: PhasePostRestore, Critical: true, Run: func(_ context.Context, env *Env) Outcome {
		sawRestored = env.Restored
		return Outcome{Result: ResultPass}
	}})
	_, d := r.startWait(StartRequest{})
	if d.Checks[1].Result != ResultSkip || d.Checks[1].Details["reason"] != "run_stopped" || d.Checks[1].Details["by"] != "drl.fence" {
		t.Fatalf("check after StopRun = %+v", d.Checks[1])
	}
	if sawRestored == nil || !sawRestored.OK {
		t.Fatalf("the post-restore check must see the restore: %+v", sawRestored)
	}
	if len(d.Checks) != 3 || d.Checks[2].ID != "drl.restore" || d.Overall != OverallFail || d.State == StateAborted {
		t.Fatalf("document = %+v", d)
	}
}

func TestMainChecksSeeNoRestoredAndAReadOnlyEnv(t *testing.T) {
	r := newRig(t)
	r.add(Check{ID: "drl.env", Area: "x", Order: 1, Run: func(_ context.Context, env *Env) Outcome {
		if env.Restored != nil || env.RunID == "" || env.Trigger != TriggerManual || env.Mode != "online" || env.Now == nil || env.Log == nil {
			t.Errorf("env = %+v", env)
		}
		if env.Actor.Kind != "system" {
			t.Errorf("the checks' actor is %q, want the system actor (no token inside the agent)", env.Actor.Kind)
		}
		return Outcome{Result: ResultPass}
	}})
	r.startWait(StartRequest{})
}

func TestAbortRestoresAndMarksTheRunAborted(t *testing.T) {
	r := newRig(t)
	g := newGate()
	r.add(passing("drl.first", 1))
	r.add(g.check("drl.hang", 2))
	r.add(passing("drl.never", 3))

	res, err := r.eng.Start(context.Background(), StartRequest{OpID: "op_x"})
	if err != nil {
		t.Fatal(err)
	}
	<-g.started
	if id, running := r.eng.Running(); !running || id != res.RunID {
		t.Fatalf("Running() = %q %v", id, running)
	}
	id, err := r.eng.Abort("")
	if err != nil || id != res.RunID {
		t.Fatalf("Abort = %q, %v", id, err)
	}
	r.wait()

	d := r.doc(res.RunID)
	if d.State != StateAborted || d.Overall != OverallAborted || d.SurvivalReady || d.FinishedAt == nil {
		t.Fatalf("aborted document = %+v", d)
	}
	if d.Reason != "aborted by request" {
		t.Fatalf("reason = %q", d.Reason)
	}
	if len(d.Checks) != 1 || d.Checks[0].ID != "drl.first" {
		t.Fatalf("an aborted run keeps the checks that finished and drops the interrupted one: %+v", d.Checks)
	}
	if d.Restored == nil || !d.Restored.OK {
		t.Fatalf("restored = %+v", d.Restored)
	}
	calls := r.rest.restoreCalls()
	if len(calls) != 1 || !reflect.DeepEqual(calls[0], r.rest.captured) {
		t.Fatalf("restore calls = %+v", calls)
	}
	if r.rest.restoreCtxErr != nil {
		t.Fatalf("the restore ran under a cancelled context: %v", r.rest.restoreCtxErr)
	}
	if r.fence.isEnabled() || r.fence.disables != 1 {
		t.Fatal("the fence must be off after an abort")
	}
	if string(r.state.block(t)["in_progress"]) != "null" {
		t.Fatal("in_progress must be cleared after an abort")
	}
	if _, ok := r.state.block(t)["last_result"]; ok {
		t.Fatal("an aborted run is not a drill result: last_result must not be written")
	}
	if _, ok := r.metrics.get(MetricLastOverall); ok {
		t.Fatal("an aborted run must not set localai_drill_last_overall")
	}
	evs := r.pub.events()
	last := evs[len(evs)-1]
	if last.Typ != events.TypeDrillDone || last.Data.(events.DrillDone) != (events.DrillDone{RunID: res.RunID, Overall: "aborted", Trigger: "manual"}) {
		t.Fatalf("last event = %+v", last)
	}
	for _, e := range evs {
		if e.Typ == events.TypeDrillCheckDone && e.Data.(events.DrillCheckDone).ID == "drl.hang" {
			t.Fatal("the interrupted check must not be reported")
		}
	}
	if _, running := r.eng.Running(); running {
		t.Fatal("no drill must be running after the abort")
	}
}

func TestAbortErrors(t *testing.T) {
	r := newRig(t)
	if _, err := r.eng.Abort(""); !errors.Is(err, ErrNoRun) {
		t.Fatalf("Abort with no drill = %v, want ErrNoRun", err)
	}
	g := newGate()
	r.add(g.check("drl.hang", 1))
	res, _ := r.eng.Start(context.Background(), StartRequest{})
	<-g.started
	if _, err := r.eng.Abort("drl-20200101T000000Z-aaaa"); !errors.Is(err, ErrNoRun) {
		t.Fatalf("Abort of another run = %v, want ErrNoRun", err)
	}
	if id, err := r.eng.Abort(res.RunID); err != nil || id != res.RunID {
		t.Fatalf("Abort by id = %q, %v", id, err)
	}
	if id, err := r.eng.Abort(""); err != nil || id != res.RunID {
		t.Fatalf("a second Abort is idempotent: %q, %v", id, err)
	}
	r.wait()
	if _, err := r.eng.Abort(""); !errors.Is(err, ErrNoRun) {
		t.Fatalf("Abort after the end = %v, want ErrNoRun", err)
	}
}

func TestShutdownAbortsRestoresAndRefusesNewRuns(t *testing.T) {
	r := newRig(t)
	g := newGate()
	r.add(g.check("drl.hang", 1))
	res, _ := r.eng.Start(context.Background(), StartRequest{})
	<-g.started
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := r.eng.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	d := r.doc(res.RunID)
	if d.State != StateAborted || d.Reason != "the agent was stopping" || d.Restored == nil || !d.Restored.OK {
		t.Fatalf("document after Shutdown = %+v", d)
	}
	if _, err := r.eng.Start(context.Background(), StartRequest{}); !errors.Is(err, ErrClosing) {
		t.Fatalf("Start after Shutdown = %v, want ErrClosing", err)
	}
}

func TestRestoreFailureIsReportedAndFailsTheRun(t *testing.T) {
	r := newRig(t)
	r.rest.restoreErr = errors.New("router refused")
	r.add(passing("drl.a", 1))
	_, d := r.startWait(StartRequest{})
	if d.Restored == nil || d.Restored.OK {
		t.Fatalf("restored = %+v, want ok false", d.Restored)
	}
	if d.Code != doctor.DRL090 || d.Overall != OverallFail {
		t.Fatalf("a restore that failed must fail the run with LAI-DRL-090: %+v", d)
	}
	if string(r.state.block(t)["in_progress"]) != "null" {
		t.Fatal("in_progress is cleared after one restore attempt, or every start would retry forever")
	}
}

func TestCaptureFailureLeavesTheNodeAloneAndSaysRestoredNotOK(t *testing.T) {
	r := newRig(t)
	r.rest.captureErr = errors.New("router down")
	r.add(passing("drl.a", 1))
	_, d := r.startWait(StartRequest{})
	if len(r.rest.restoreCalls()) != 0 {
		t.Fatal("nothing was captured, so nothing can be restored")
	}
	if d.Restored == nil || d.Restored.OK || d.Restored.LoadedModels == nil {
		t.Fatalf("restored = %+v", d.Restored)
	}
}

func TestWithoutRestorerOrFence(t *testing.T) {
	r := newRig(t, withoutRestorer(), withoutFence())
	r.add(passing("drl.a", 1))
	res, _ := r.startWait(StartRequest{})
	raw, _ := r.eng.Get(res.RunID)
	if strings.Contains(string(raw), `"restored"`) {
		t.Fatalf("no restorer wired, yet the document has a restored block:\n%s", raw)
	}
	if !strings.Contains(string(raw), `"network_fence": {
    "enabled": false,
    "blocked_dials": 0
  }`) {
		t.Fatalf("no fence wired: the document must say enabled false:\n%s", raw)
	}
}

func TestBlockedDialsTurnTheRunIntoAWarning(t *testing.T) {
	for _, tc := range []struct {
		name    string
		res     Result
		overall string
	}{{"pass becomes warn", ResultPass, OverallWarn}, {"fail stays fail", ResultFail, OverallFail}} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t)
			r.fence.report = FenceReport{BlockedDials: 2, Callers: []string{"cloud.heartbeat", "catalog.refresh"}}
			res := tc.res
			r.add(Check{ID: "drl.a", Area: "x", Order: 1, Run: func(context.Context, *Env) Outcome { return Outcome{Result: res} }})
			_, d := r.startWait(StartRequest{})
			if d.Overall != tc.overall {
				t.Fatalf("overall = %s, want %s", d.Overall, tc.overall)
			}
			f := d.NetworkFence
			if !f.Enabled || f.BlockedDials != 2 || f.Code != doctor.DRL091 || !reflect.DeepEqual(f.Callers, []string{"cloud.heartbeat", "catalog.refresh"}) {
				t.Fatalf("network_fence = %+v", f)
			}
		})
	}
}

func TestGetServesALiveSnapshotAndASecondStartIsRefused(t *testing.T) {
	r := newRig(t)
	g := newGate()
	r.add(Check{ID: "drl.first", Area: "x", Order: 1, Critical: true, Run: run0})
	r.add(g.check("drl.hang", 2))
	res, _ := r.eng.Start(context.Background(), StartRequest{})
	<-g.started

	raw, err := r.eng.Get(res.RunID)
	if err != nil {
		t.Fatal(err)
	}
	var live Document
	if err := json.Unmarshal(raw, &live); err != nil {
		t.Fatal(err)
	}
	if live.State != StateRunning || live.Overall != OverallRunning || live.FinishedAt != nil || len(live.Checks) != 1 || live.Counts.Pass != 1 {
		t.Fatalf("live snapshot = %+v", live)
	}
	if _, err := r.eng.Latest(); !errors.Is(err, ErrNotFound) {
		t.Fatal("a run in flight is not the latest finished run")
	}

	_, err = r.eng.Start(context.Background(), StartRequest{})
	var refused *RefusedError
	if !errors.As(err, &refused) || refused.Code != doctor.DRL002 || refused.Details["reason"] != "drill_running" || refused.Details["run_id"] != res.RunID {
		t.Fatalf("a second manual start = %v, want LAI-DRL-002 drill_running", err)
	}
	close(g.release)
	r.wait()
	if raw, _ := r.eng.Get(res.RunID); strings.Contains(string(raw), `"running"`) {
		t.Fatal("the final document must not be the live one")
	}
}

func TestStartValidation(t *testing.T) {
	r := newRig(t)
	r.add(passing("drl.a", 1))
	for _, tc := range []struct {
		name   string
		req    StartRequest
		field  string
		reason string
	}{
		{"unknown trigger", StartRequest{Trigger: "cloud"}, "trigger", "unknown_trigger"},
		{"manual_plug is not startable", StartRequest{Trigger: TriggerManualPlug}, "trigger", "unknown_trigger"},
		{"unknown check", StartRequest{Only: []string{"drl.a", "drl.nope"}}, "only", "unknown_check"},
		{"empty check id", StartRequest{Only: []string{""}}, "only", "only has an empty check id"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := r.eng.Start(context.Background(), tc.req)
			var inv *InvalidRequestError
			if !errors.As(err, &inv) || inv.Field != tc.field || inv.Reason != tc.reason {
				t.Fatalf("Start = %v, want invalid %s/%s", err, tc.field, tc.reason)
			}
			if tc.reason == "unknown_check" && !reflect.DeepEqual(inv.Unknown, []string{"drl.nope"}) {
				t.Fatalf("unknown = %v", inv.Unknown)
			}
		})
	}
	if _, running := r.eng.Running(); running {
		t.Fatal("a rejected request must not leave a run slot taken")
	}
}

func TestRunIDsAreUniqueAndSkipKeptOnes(t *testing.T) {
	r := newRig(t)
	r.add(passing("drl.a", 1))
	// The first id the generator would make is already kept.
	r.fs.seed(testDir+"/drl-20261101T043512Z-0001.json", `{}`)
	res, _ := r.startWait(StartRequest{})
	if res.RunID != "drl-20261101T043512Z-0002" {
		t.Fatalf("run id = %s, want the 0001 collision skipped", res.RunID)
	}
}

func TestSaveFailureDoesNotStopTheRunFromFinishing(t *testing.T) {
	r := newRig(t)
	r.add(passing("drl.a", 1))
	r.fs.fail = func(op, _ string) error {
		if op == "write" {
			return syscall.ENOSPC
		}
		return nil
	}
	res, err := r.eng.Start(context.Background(), StartRequest{})
	if err != nil {
		t.Fatal(err)
	}
	r.wait()
	if string(r.state.block(t)["in_progress"]) != "null" {
		t.Fatal("in_progress must be cleared even when the document cannot be written")
	}
	if got := r.pub.types(); got[len(got)-1] != events.TypeDrillDone {
		t.Fatalf("drill.done must still be published: %v", got)
	}
	if len(r.rest.restoreCalls()) != 1 {
		t.Fatal("the restore must still run")
	}
	if _, err := r.eng.Get(res.RunID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get = %v", err)
	}
}

func TestNewRequiresItsDependencies(t *testing.T) {
	r := newRig(t)
	for name, d := range map[string]Deps{
		"no registry": {Results: r.results, State: r.state},
		"no results":  {Registry: r.reg, State: r.state},
		"no state":    {Registry: r.reg, Results: r.results},
	} {
		if _, err := New(d); err == nil {
			t.Errorf("%s: New accepted it", name)
		}
	}
}

// zeroRand is a broken entropy source: every suffix is 0000.
type zeroRand struct{}

func (zeroRand) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 0
	}
	return len(p), nil
}

func TestRunIDsStayUniqueWithABrokenGenerator(t *testing.T) {
	r := newRig(t, func(_ *rig, d *Deps) { d.Rand = zeroRand{} })
	g := newGate()
	r.add(g.check("drl.hang", 1))
	first, _ := r.eng.Start(context.Background(), StartRequest{})
	<-g.started
	skipped, err := r.eng.Start(context.Background(), StartRequest{Trigger: TriggerScheduled})
	if err != nil || skipped.State != StartSkipped {
		t.Fatalf("scheduled during a drill = %+v, %v", skipped, err)
	}
	if first.RunID == skipped.RunID || !ValidRunID(skipped.RunID) {
		t.Fatalf("ids %s and %s must differ and be valid", first.RunID, skipped.RunID)
	}
	close(g.release)
	r.wait()
}
