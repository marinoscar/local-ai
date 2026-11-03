package bench

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/events"
	"github.com/marinoscar/local-ai/agent/internal/execx"
	"github.com/marinoscar/local-ai/agent/internal/hardware"
	hwfake "github.com/marinoscar/local-ai/agent/internal/hardware/fake"
)

func TestRunHappyPath(t *testing.T) {
	r := newRig(t, "other-vlm")
	res, err := r.run()
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// The procedure of llama-router.md section 4.13, in order: unload everything, load the model
	// alone, unload it for llama-bench, put the previous models back.
	wantOps := []string{"unload other-vlm", "load " + testModel, "unload " + testModel, "load other-vlm"}
	if got := r.w.Ops(); !sameStrings(got, wantOps) {
		t.Errorf("operations = %v, want %v", got, wantOps)
	}
	if got := r.w.Loaded(); !sameStrings(got, []string{"other-vlm"}) {
		t.Errorf("loaded after the run = %v, want the previous models", got)
	}
	if !sameStrings(res.Restored, []string{"other-vlm"}) || len(res.RestoreFailed) != 0 {
		t.Errorf("restored = %v, failed = %v", res.Restored, res.RestoreFailed)
	}
	if n := r.sleepCount(BaselineInterval); n != BaselineSamples-1 {
		t.Errorf("baseline waits = %d, want %d (5 samples, 1 s apart)", n, BaselineSamples-1)
	}

	// The request is the one of step 3.
	if r.chat.calls != 1 || r.chat.last.Model != testModel || r.chat.last.MaxTokens != ChatMaxTokens {
		t.Errorf("chat = %d calls, %+v", r.chat.calls, r.chat.last)
	}

	// The documented numbers: 210 idle, 6791 peak, 6581 measured, ceil(6581 x 1.10) = 7240.
	for name, c := range map[string]struct {
		got  *int64
		want int64
	}{
		"baseline":  {res.BaselineVRAMMiB, 210},
		"peak":      {res.PeakVRAMMiB, 6791},
		"measured":  {res.MeasuredVRAMMiB, 6581},
		"suggested": {res.SuggestedMinVRAMMiB, 7240},
	} {
		if c.got == nil || *c.got != c.want {
			t.Errorf("%s = %v, want %d", name, c.got, c.want)
		}
	}
	if res.PP512TPS != 1450.2 || res.TG128TPS != 38.4 {
		t.Errorf("pp512 = %v, tg128 = %v", res.PP512TPS, res.TG128TPS)
	}
	if res.ServerTGTPS == nil || *res.ServerTGTPS != 41.0 || res.MTPAcceptance == nil || *res.MTPAcceptance != 0.71 {
		t.Errorf("server tg = %v, acceptance = %v", res.ServerTGTPS, res.MTPAcceptance)
	}
	if res.Ctx != 32768 || res.Model != testModel || res.Node != "ai-n3" || res.Build != testBuild {
		t.Errorf("identity fields: %+v", res)
	}

	// llama-bench was run once on the preset's weights with the preset's layers and flash attention.
	calls := r.exec.Calls()
	if len(calls) != 1 || calls[0].Path != DefaultBenchBinary || calls[0].Timeout != BenchTimeout {
		t.Fatalf("exec calls = %+v", calls)
	}
	wantArgs := "-m " + testWeights + " -ngl 99 -fa 1 -p 512 -n 128 -r 3 -o json"
	if got := strings.Join(calls[0].Args, " "); got != wantArgs {
		t.Errorf("llama-bench args = %q, want %q", got, wantArgs)
	}

	// The file: <id>-<ts>.json, 0644, with exactly the documented field names.
	wantPath := filepath.Join(r.dir, testModel+"-20261102T103000Z.json")
	if res.Path != wantPath {
		t.Fatalf("Path = %q, want %q", res.Path, wantPath)
	}
	st, err := os.Stat(wantPath)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o644 {
		t.Errorf("file mode = %v, want 0644", st.Mode().Perm())
	}
	raw, _ := os.ReadFile(wantPath)
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatalf("result file is not JSON: %v", err)
	}
	for _, k := range []string{"model", "node", "build", "gpu", "ctx", "baseline_vram_mib", "peak_vram_mib",
		"measured_vram_mib", "suggested_min_vram_mib", "pp512_tps", "tg128_tps", "server_tg_tps",
		"mtp_acceptance", "measured_at"} {
		if _, ok := fields[k]; !ok {
			t.Errorf("result file has no %q: %s", k, raw)
		}
	}
	if _, ok := fields["disturbed"]; ok {
		t.Errorf("an undisturbed result must not carry disturbed: %s", raw)
	}
	if fields["measured_at"] != "2026-11-02T10:30:00Z" || fields["gpu"] != testGPUName {
		t.Errorf("measured_at = %v, gpu = %v", fields["measured_at"], fields["gpu"])
	}

	// The YAML for the catalog PR.
	wantSnippet := "requirements:\n  min_vram_mib: 7240   # measured on RTX 2070 Max-Q 8 GB, build f3586e66b-cuda, 2026-11-02\n"
	if got := res.Snippet(); got != wantSnippet {
		t.Errorf("Snippet:\n%s\nwant:\n%s", got, wantSnippet)
	}
}

func TestRunWithNothingLoadedRestoresNothing(t *testing.T) {
	r := newRig(t)
	res, err := r.run()
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := r.w.Ops(); !sameStrings(got, []string{"load " + testModel, "unload " + testModel}) {
		t.Errorf("operations = %v", got)
	}
	if len(res.Restored) != 0 || len(r.w.Loaded()) != 0 {
		t.Errorf("restored = %v, loaded = %v", res.Restored, r.w.Loaded())
	}
}

func TestRunWhereTheModelWasLoadedBefore(t *testing.T) {
	r := newRig(t, testModel, "other-vlm")
	res, err := r.run()
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// Both are put back, in their original order, and the model is not unloaded twice.
	if got := r.w.Loaded(); !sameStrings(got, []string{testModel, "other-vlm"}) {
		t.Errorf("loaded after the run = %v", got)
	}
	if !sameStrings(res.Restored, []string{testModel, "other-vlm"}) {
		t.Errorf("restored = %v", res.Restored)
	}
}

func TestRunFailureRestoresPreviousModels(t *testing.T) {
	cases := []struct {
		name     string
		setup    func(r *rig)
		wantStep Step
	}{
		{"llama-bench exits non-zero", func(r *rig) {
			r.exec.Expect(benchKey(), execx.Result{ExitCode: 1, Stderr: []byte("ggml_cuda_init: failed\nCUDA error: out of memory")}, nil)
		}, StepThroughput},
		{"llama-bench times out", func(r *rig) {
			r.exec.Expect(benchKey(), execx.Result{}, execx.ErrTimeout)
		}, StepThroughput},
		{"llama-bench prints no results", func(r *rig) {
			r.exec.Expect(benchKey(), execx.Result{Stdout: []byte(`[]`)}, nil)
		}, StepThroughput},
		{"the chat request fails with the model loaded", func(r *rig) {
			r.chat.err = errors.New("connection refused")
		}, StepMeasure},
		{"the model does not load", func(r *rig) {
			r.agent.failOp["load "+testModel] = errors.New("LAI-LLM-009: failed to start")
		}, StepLoad},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t, "other-vlm", "second")
			c.setup(r)
			res, err := r.run()
			if err == nil || res != nil {
				t.Fatalf("Run = %v, %v; want a failure and no result", res, err)
			}
			e := asBench(t, err)
			if e.Code != CodeBenchFailed || e.Step != c.wantStep {
				t.Errorf("error = %+v, want code %s at %s", e, CodeBenchFailed, c.wantStep)
			}
			if got := r.w.Loaded(); !sameStrings(got, []string{"other-vlm", "second"}) {
				t.Errorf("loaded after the failure = %v, want the previous models back", got)
			}
			if files, _ := filepath.Glob(filepath.Join(r.dir, "*")); len(files) != 0 {
				t.Errorf("a failed run wrote %v", files)
			}
		})
	}
}

func TestRunRestoresAfterCancellation(t *testing.T) {
	r := newRig(t, "other-vlm")
	ctx, cancel := context.WithCancel(context.Background())
	r.chat.before = cancel // Ctrl-C while the request is running
	r.chat.err = context.Canceled
	res, err := r.bench().Run(ctx, testModel)
	if err == nil || res != nil {
		t.Fatalf("Run = %v, %v; want a failure", res, err)
	}
	if got := r.w.Loaded(); !sameStrings(got, []string{"other-vlm"}) {
		t.Errorf("loaded after Ctrl-C = %v; the previous model must be back", got)
	}
}

func TestRunRestoreIsBestEffort(t *testing.T) {
	r := newRig(t, "first", "second", "third")
	r.agent.failOp["load first"] = errors.New("LAI-LLM-009")
	res, err := r.run()
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// The measurement stands; the one model that could not be restored is named, the others are back.
	if !sameStrings(res.RestoreFailed, []string{"first"}) || !sameStrings(res.Restored, []string{"second", "third"}) {
		t.Errorf("restored = %v, failed = %v", res.Restored, res.RestoreFailed)
	}
	if got := r.w.Loaded(); !sameStrings(got, []string{"second", "third"}) {
		t.Errorf("loaded = %v", got)
	}
	if res.SuggestedMinVRAMMiB == nil {
		t.Error("a failed restore does not invalidate the measurement")
	}
}

func TestRunDisturbed(t *testing.T) {
	cases := []struct {
		name      string
		inject    func(a *fakeAgent)
		disturbed bool
	}{
		{"nothing else happened", func(a *fakeAgent) {}, false},
		{"model.default_changed is not an operation on the GPU", func(a *fakeAgent) { a.emit(events.TypeModelDefaultChanged, "op_someoneelse") }, false},
		{"unrelated events are ignored", func(a *fakeAgent) { a.emit(events.TypeModeChanged, "op_someoneelse") }, false},
		{"another load", func(a *fakeAgent) { a.emit(events.TypeModelLoading, "op_someoneelse") }, true},
		{"another unload", func(a *fakeAgent) { a.emit(events.TypeModelUnloaded, "op_someoneelse") }, true},
		{"a model failed", func(a *fakeAgent) { a.emit(events.TypeModelFailed, "op_someoneelse") }, true},
		{"a crash reported by the watcher (no operation id)", func(a *fakeAgent) { a.emit(events.TypeModelUnloaded, "") }, true},
		{"the agent restarted", func(a *fakeAgent) { a.emit(events.TypeResync, "") }, true},
		{"the agent is shutting down", func(a *fakeAgent) { a.emit(events.TypeShutdown, "") }, true},
		{"the event stream broke", func(a *fakeAgent) { a.breakStream() }, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t, "other-vlm")
			r.chat.before = func() { c.inject(r.agent) }
			res, err := r.run()
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if res.Disturbed != c.disturbed {
				t.Fatalf("Disturbed = %v, want %v", res.Disturbed, c.disturbed)
			}
			raw, _ := os.ReadFile(res.Path)
			var f map[string]any
			_ = json.Unmarshal(raw, &f)
			if c.disturbed {
				// No suggestion in the result, in the accessor or in the snippet; the measurements stay.
				if f["disturbed"] != true || f["suggested_min_vram_mib"] != nil {
					t.Errorf("file = %s", raw)
				}
				if _, _, ok := res.SuggestedMiB(); ok || res.Snippet() != "" || res.SuggestedMinVRAMMiB != nil {
					t.Errorf("a disturbed run made a suggestion: %q", res.Snippet())
				}
				if res.MeasuredVRAMMiB == nil || *res.MeasuredVRAMMiB != 6581 {
					t.Errorf("measured = %v; the numbers are still recorded", res.MeasuredVRAMMiB)
				}
			} else if f["suggested_min_vram_mib"] != float64(7240) {
				t.Errorf("file = %s", raw)
			}
		})
	}
}

func TestRunRefusals(t *testing.T) {
	cases := []struct {
		name     string
		setup    func(r *rig)
		wantCode string
		wantBusy bool
	}{
		{"unknown model", func(r *rig) { r.agent.modelInfo.ID = "other" }, CodeUnknownModel, false},
		{"not a llama model", func(r *rig) { r.agent.modelInfo.Engine = "whisper" }, CodeBenchFailed, false},
		{"files missing", func(r *rig) { r.agent.modelInfo.Present = false }, CodeFilesMissing, false},
		{"files not verified", func(r *rig) { r.agent.modelInfo.Verified = false }, CodeFilesMissing, false},
		{"preset without a model file", func(r *rig) { r.agent.modelInfo.Preset = "[m-9b]\nc = 4096\n" }, CodeBenchFailed, false},
		{"preset without a context", func(r *rig) { r.agent.modelInfo.Preset = "[m-9b]\nmodel = " + testWeights + "\n" }, CodeBenchFailed, false},
		{"the model list cannot be read", func(r *rig) { r.agent.modelsErr = errors.New("agent down") }, CodeBenchFailed, false},
		{"events cannot be watched", func(r *rig) { r.agent.eventsErr = errors.New("agent down") }, CodeBenchFailed, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t, "other-vlm")
			c.setup(r)
			res, err := r.run()
			if err == nil || res != nil {
				t.Fatalf("Run = %v, %v; want a refusal", res, err)
			}
			if e := asBench(t, err); e.Code != c.wantCode || e.Step != StepPrecheck {
				t.Errorf("error = %+v, want %s at precheck", e, c.wantCode)
			}
			// Nothing was changed: no operation, the model is still loaded, nothing was written.
			if ops := r.w.Ops(); len(ops) != 0 {
				t.Errorf("operations = %v; a refusal must not touch the models", ops)
			}
			if got := r.w.Loaded(); !sameStrings(got, []string{"other-vlm"}) {
				t.Errorf("loaded = %v", got)
			}
		})
	}
}

func TestRunRefusesWhileAModelOperationRuns(t *testing.T) {
	r := newRig(t, "other-vlm")
	r.agent.status["third"] = StatusLoading
	res, err := r.run()
	var busy *BusyError
	if res != nil || !errors.As(err, &busy) {
		t.Fatalf("Run = %v, %v; want *BusyError", res, err)
	}
	if ops := r.w.Ops(); len(ops) != 0 {
		t.Errorf("operations = %v; nothing may be unloaded while an operation runs", ops)
	}
}

func TestRunBusyDuringTheRun(t *testing.T) {
	// Another operation starts after the pre-check: the agent answers 409 LAI-AGT-022 to our unload.
	r := newRig(t, "other-vlm")
	r.agent.failOp["unload other-vlm"] = &BusyError{OpID: "op_aaaaaaaaaaa2"}
	res, err := r.run()
	if res != nil {
		t.Fatalf("result %+v", res)
	}
	e := asBench(t, err)
	var busy *BusyError
	if e.Code != CodeBusy || !errors.As(err, &busy) || busy.OpID != "op_aaaaaaaaaaa2" || e.Step != StepUnload {
		t.Errorf("error = %+v", e)
	}
	if got := r.w.Loaded(); !sameStrings(got, []string{"other-vlm"}) {
		t.Errorf("loaded = %v", got)
	}
}

func TestRunStopsWhenAModelStaysLoaded(t *testing.T) {
	r := newRig(t, "other-vlm")
	r.agent.stuck["other-vlm"] = true
	_, err := r.run()
	if e := asBench(t, err); e.Step != StepUnload {
		t.Errorf("error = %+v, want a failure at unload", e)
	}
	if r.chat.calls != 0 {
		t.Error("the model must not be measured with another model in memory")
	}
}

func TestRunGraphicsMemoryDidNotRise(t *testing.T) {
	r := newRig(t)
	// A sampler that never sees the request: memory never rises above the baseline.
	flat := &flatSampler{gpus: r.gpu.gpus, used: baseUsedMiB}
	b, err := New(Deps{Agent: r.agent, Chat: &instantChat{}, GPU: flat, Exec: r.exec}, r.opts)
	if err != nil {
		t.Fatal(err)
	}
	_, err = b.Run(context.Background(), testModel)
	if e := asBench(t, err); e.Step != StepMeasure {
		t.Errorf("error = %+v, want a failure at measure", e)
	}
}

type flatSampler struct {
	gpus []hardware.GPU
	used int64
}

func (f *flatSampler) SampleGPUs(context.Context) ([]hardware.GPU, error) {
	out := append([]hardware.GPU(nil), f.gpus...)
	out[0].VRAMUsedMiB = f.used
	return out, nil
}

type instantChat struct{}

func (instantChat) Chat(context.Context, ChatRequest) (ChatResult, error) { return ChatResult{}, nil }

func TestRunWriteFailureStillRestores(t *testing.T) {
	r := newRig(t, "other-vlm")
	file := filepath.Join(r.dir, "not-a-dir")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	r.opts.Dir = file // MkdirAll fails: a file is in the way
	_, err := r.run()
	if e := asBench(t, err); e.Step != StepWrite {
		t.Errorf("error = %+v, want a failure at write", e)
	}
	if got := r.w.Loaded(); !sameStrings(got, []string{"other-vlm"}) {
		t.Errorf("loaded = %v", got)
	}
}

func TestRunNoDraftCounters(t *testing.T) {
	cases := []struct {
		name    string
		chat    ChatResult
		fromLog func(context.Context, time.Time) (int64, int64, bool)
		want    *float64
	}{
		{"no speculative decoding", ChatResult{PredictedPerSecond: 30}, nil, nil},
		{"counters in the response", ChatResult{PredictedPerSecond: 30, DraftN: 3, DraftAccepted: 2}, nil, ptr(0.67)},
		{"counters in the router log", ChatResult{PredictedPerSecond: 30}, func(context.Context, time.Time) (int64, int64, bool) { return 71, 100, true }, ptr(0.71)},
		{"the log does not know", ChatResult{PredictedPerSecond: 30}, func(context.Context, time.Time) (int64, int64, bool) { return 0, 0, false }, nil},
		{"the response wins over the log", ChatResult{PredictedPerSecond: 30, DraftN: 10, DraftAccepted: 10}, func(context.Context, time.Time) (int64, int64, bool) { return 1, 100, true }, ptr(1.0)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t)
			r.chat.result = c.chat
			r.opts.DraftFromLog = c.fromLog
			res, err := r.run()
			if err != nil {
				t.Fatal(err)
			}
			if (res.MTPAcceptance == nil) != (c.want == nil) || (c.want != nil && *res.MTPAcceptance != *c.want) {
				t.Errorf("acceptance = %v, want %v", deref(res.MTPAcceptance), deref(c.want))
			}
		})
	}
}

func ptr[T any](v T) *T { return &v }

func deref(f *float64) any {
	if f == nil {
		return nil
	}
	return *f
}

func TestRunRecordsRouterEffectiveMemory(t *testing.T) {
	r := newRig(t)
	r.agent.modelInfo.EffectiveMiB = ptr[int64](6400)
	res, err := r.run()
	if err != nil {
		t.Fatal(err)
	}
	if res.EffectiveVRAMMiB == nil || *res.EffectiveVRAMMiB != 6400 {
		t.Errorf("effective = %v", res.EffectiveVRAMMiB)
	}
	// Recorded, not used: the suggestion is still from the measurement.
	if *res.SuggestedMinVRAMMiB != 7240 {
		t.Errorf("suggested = %d", *res.SuggestedMinVRAMMiB)
	}
}

func TestRunBackendsAndGPUSelection(t *testing.T) {
	amd := hardware.GPU{Vendor: hardware.VendorAMD, Name: "AMD Radeon Example APU", UnifiedMemory: true, VRAMTotalMiB: 62464}
	nv := hardware.GPU{Vendor: hardware.VendorNVIDIA, Name: testGPUName, VRAMTotalMiB: 7982}
	cases := []struct {
		name     string
		gpus     []hardware.GPU
		backend  string
		wantName string
		wantErr  bool
	}{
		{"detected cuda on a hybrid laptop uses the NVIDIA card", []hardware.GPU{amd, nv}, "", testGPUName, false},
		{"configured vulkan on a hybrid laptop uses the AMD memory", []hardware.GPU{amd, nv}, hardware.BackendVulkan, "AMD Radeon Example APU", false},
		{"configured cuda without an NVIDIA GPU", []hardware.GPU{amd}, hardware.BackendCUDA, "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t)
			// The fake world reports its memory on the first GPU; point it at the one that counts.
			r.opts.Backend = c.backend
			r.gpu = &fakeSampler{w: r.w, gpus: reorder(c.gpus, c.backend)}
			res, err := r.run()
			if c.wantErr {
				if e := asBench(t, err); e.Step != StepBaseline {
					t.Errorf("error = %+v", e)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if res.GPU == nil || *res.GPU != c.wantName {
				t.Errorf("gpu = %v, want %q", res.GPU, c.wantName)
			}
		})
	}
}

// reorder puts the GPU the backend counts first, so the fake world's memory lands on it.
func reorder(gpus []hardware.GPU, backend string) []hardware.GPU {
	if backend == "" {
		backend = detectBackend(gpus)
	}
	fit := hardware.Snapshot{GPUs: gpus}.FitGPUs(backend)
	var rest []hardware.GPU
	for _, g := range gpus {
		in := false
		for _, f := range fit {
			if f.Name == g.Name {
				in = true
			}
		}
		if !in {
			rest = append(rest, g)
		}
	}
	return append(fit, rest...)
}

func TestBaselineIsTheMedian(t *testing.T) {
	gpu := func(used int64) []hardware.GPU {
		return []hardware.GPU{{Vendor: hardware.VendorNVIDIA, VRAMUsedMiB: used}}
	}
	hw := hwfake.New(hardware.Snapshot{})
	// The first sample is taken by the caller; four more follow. One of the five is an outlier.
	hw.QueueGPUReads(gpu(900), gpu(210), gpu(215), gpu(205), gpu(212))
	r := newRig(t)
	b, err := New(Deps{Agent: r.agent, Chat: r.chat, GPU: hw, Exec: r.exec}, r.opts)
	if err != nil {
		t.Fatal(err)
	}
	ru := &run{b: b, model: testModel, own: map[string]bool{}}
	first, ferr := hw.SampleGPUs(context.Background())
	got, err := ru.baselineVRAM(context.Background(), hardware.BackendCUDA, first, ferr)
	if err != nil {
		t.Fatal(err)
	}
	if got != 212 {
		t.Errorf("baseline = %d, want the median 212 of 900 210 215 205 212", got)
	}
}

func TestBaselineNeedsEnoughSamples(t *testing.T) {
	hw := hwfake.New(hardware.Snapshot{})
	r := newRig(t)
	b, _ := New(Deps{Agent: r.agent, Chat: r.chat, GPU: hw, Exec: r.exec}, r.opts)
	ru := &run{b: b, model: testModel, own: map[string]bool{}}
	hw.FailGPUReads(errors.New("nvidia-smi timed out"))
	first := []hardware.GPU{{Vendor: hardware.VendorNVIDIA, VRAMUsedMiB: 210}}
	_, err := ru.baselineVRAM(context.Background(), hardware.BackendCUDA, first, nil)
	if err == nil || !strings.Contains(err.Error(), "1 of 5") {
		t.Errorf("err = %v, want only 1 of 5 baseline samples", err)
	}
}

func TestSampleDuringSkipsFailedSamples(t *testing.T) {
	r := newRig(t)
	b := r.bench()
	ru := &run{b: b, model: testModel, own: map[string]bool{}}
	n := 0
	release := make(chan struct{})
	req := func() error { <-release; return nil }
	read := func(context.Context) (int64, error) {
		n++
		switch n {
		case 1:
			return 0, errors.New("probe timed out")
		case 2:
			return 500, nil
		case 3:
			return 900, nil
		case 4:
			close(release)
			return 700, nil
		}
		return 600, nil
	}
	peak, err := ru.sampleDuring(context.Background(), req, read)
	if err != nil || peak != 900 {
		t.Errorf("peak = %d, err = %v; want 900 (the failed sample skipped)", peak, err)
	}
	if r.sleepCount(SampleInterval) == 0 {
		t.Error("the loop must wait SampleInterval (250 ms) between samples")
	}
}

func TestSampleDuringAllFailed(t *testing.T) {
	r := newRig(t)
	ru := &run{b: r.bench(), model: testModel, own: map[string]bool{}}
	_, err := ru.sampleDuring(context.Background(), func() error { return nil },
		func(context.Context) (int64, error) { return 0, errors.New("no GPU") })
	if err == nil || !strings.Contains(err.Error(), "no memory sample") {
		t.Errorf("err = %v", err)
	}
}

func TestSuggestRounding(t *testing.T) {
	cases := []struct{ measured, want int64 }{
		{6581, 7240}, // the documented example: 7239.1 rounds up
		{1000, 1100}, // exact, no extra MiB
		{909, 1000},  // 999.9
		{910, 1001},  // 1001.0 exactly: 910 x 1.1 = 1001
		{911, 1003},  // 1002.1 rounds up
		{1, 2},       // never below the measurement
		{0, 0},
		{12345, 13580}, // 13579.5
		{62464, 68711}, // 68710.4
	}
	for _, c := range cases {
		if got := Suggest(c.measured); got != c.want {
			t.Errorf("Suggest(%d) = %d, want %d", c.measured, got, c.want)
		}
	}
}

func TestMedian(t *testing.T) {
	cases := []struct {
		in   []int64
		want int64
	}{
		{nil, 0}, {[]int64{7}, 7}, {[]int64{3, 1, 2}, 2}, {[]int64{4, 1, 3, 2}, 3}, {[]int64{210, 210, 900, 205, 215}, 210},
	}
	for _, c := range cases {
		in := append([]int64(nil), c.in...)
		if got := median(c.in); got != c.want {
			t.Errorf("median(%v) = %d, want %d", in, got, c.want)
		}
	}
}

func TestBuildFromTheSymlink(t *testing.T) {
	cases := []struct {
		name   string
		build  string
		link   func(string) (string, error)
		expect string
	}{
		{"explicit", "x-cuda", nil, "x-cuda"},
		{"the target's base name", "", func(string) (string, error) { return "/opt/localai/llama.cpp/builds/f3586e66b-cuda/", nil }, "f3586e66b-cuda"},
		{"unreadable", "", func(string) (string, error) { return "", errors.New("no such file") }, "unknown"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t)
			r.opts.Build = c.build
			if c.link != nil {
				r.opts.Readlink = c.link
			}
			if got := r.bench().build(); got != c.expect {
				t.Errorf("build = %q, want %q", got, c.expect)
			}
		})
	}
}

func TestNewRequiresSeams(t *testing.T) {
	r := newRig(t)
	for name, d := range map[string]Deps{
		"agent": {Chat: r.chat, GPU: r.gpu},
		"chat":  {Agent: r.agent, GPU: r.gpu},
		"gpu":   {Agent: r.agent, Chat: r.chat},
	} {
		if _, err := New(d, Options{}); err == nil {
			t.Errorf("New without %s succeeded", name)
		}
	}
}

func TestFileName(t *testing.T) {
	got := FileName("qwen3.5-9b-q4km", time.Date(2026, 11, 2, 10, 30, 0, 0, time.FixedZone("x", -6*3600)))
	if got != "qwen3.5-9b-q4km-20261102T163000Z.json" {
		t.Errorf("FileName = %q (the timestamp is UTC)", got)
	}
}
