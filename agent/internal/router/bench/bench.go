// Package bench is the measurement engine of `aictl bench <model>` (llama-router.md section 4.13,
// ROUTER-10): it loads one model alone at its real context, measures the graphics memory it needs
// during a real request, measures throughput with llama-bench, writes a JSON result and hands back
// the numbers for a catalog PR (catalog.md section 2: min_vram_mib is measured, not guessed).
//
// The engine runs in the aictl process, as root. It drives the agent through the Agent seam (load,
// unload, the model list and the /events stream, which AICTL-05 implements over the agent's HTTP
// API) and samples GPU memory in-process through hardware.Sampler.SampleGPUs every 250 ms, so the
// 1-sample-per-2-s limit of GET /hardware?fresh=1 does not apply. It cannot hold modelOpsMu for the
// run (the lock lives in the agent process), so it watches /events and marks the result
// "disturbed" when a model operation that is not its own happens meanwhile; a disturbed result
// carries no suggestion.
//
// The package owns no command surface: flag parsing, the confirmation, the human and --json output
// and the exit codes belong to AICTL-05 (aictl.md section 4.24). Nothing here is wired into the
// agent; the package imports neither internal/api nor internal/cli, only seams.
package bench

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/events"
	"github.com/marinoscar/local-ai/agent/internal/execx"
	"github.com/marinoscar/local-ai/agent/internal/hardware"
)

// Paths, cadences and limits of llama-router.md section 4.13.
const (
	// DefaultDir is where the result files go (overview.md, canonical paths).
	DefaultDir = "/var/lib/localai/bench"
	// DefaultBenchBinary is the llama-bench of the active llama.cpp build.
	DefaultBenchBinary = "/opt/localai/llama.cpp/current/bin/llama-bench"
	// DefaultBuildLink is the symlink whose target names the active build ("<commit>-<backend>").
	DefaultBuildLink = "/opt/localai/llama.cpp/current"

	// BaselineSamples is how many samples make the baseline U0, BaselineInterval apart.
	BaselineSamples  = 5
	BaselineInterval = time.Second
	// SampleInterval is the cadence of the memory samples during the request.
	SampleInterval = 250 * time.Millisecond
	// BenchTimeout bounds the llama-bench run.
	BenchTimeout = 15 * time.Minute
	// ChatTimeout bounds the one chat completion.
	ChatTimeout = 5 * time.Minute
	// RestoreTimeout bounds the reload of one previously loaded model.
	RestoreTimeout = 6 * time.Minute
	// ChatMaxTokens is max_tokens of the measured request.
	ChatMaxTokens = 64

	// marginPct is the safety margin of the suggestion: ceil(measured x 1.10).
	marginPct = 110

	// resultFileMode is the mode of the result file (root 0644); the directory is 0755.
	resultFileMode os.FileMode = 0o644
	resultDirMode  os.FileMode = 0o755
	// maxBenchOut caps the llama-bench output kept in memory.
	maxBenchOut = 1 << 20
)

// Doctor codes of the bench. They alias internal/doctor/codes.go, the one file allowed to spell a
// "LAI-..." literal (scripts/check-doctor-codes.sh).
const (
	// CodeBenchFailed: the benchmark failed (LAI-LLM-025).
	CodeBenchFailed = doctor.LLM025
	// CodeBusy: another model operation is running (LAI-AGT-022; exit 6 at the CLI).
	CodeBusy = doctor.AGT022
	// CodeUnknownModel: the model id is not in the catalog (LAI-AGT-021).
	CodeUnknownModel = doctor.AGT021
	// CodeFilesMissing: the model files are missing or not verified (LAI-LLM-008).
	CodeFilesMissing = doctor.LLM008
)

// Model statuses of GET /models (agent.md section 3.8.7) the engine cares about.
const (
	StatusLoaded   = "loaded"
	StatusLoading  = "loading"
	StatusSleeping = "sleeping"
)

// Step names the stage of the procedure an Error happened in.
type Step string

// Steps of llama-router.md section 4.13.
const (
	StepPrecheck   Step = "precheck"   // 1: model present, no operation running, events watched
	StepUnload     Step = "unload"     // 2: unload everything
	StepBaseline   Step = "baseline"   // 2: U0
	StepLoad       Step = "load"       // 3: load alone
	StepMeasure    Step = "measure"    // 3: the chat request and the memory curve
	StepThroughput Step = "throughput" // 4: llama-bench
	StepWrite      Step = "write"      // 6: the result file
)

// Op is the answer of a load or an unload: the operation id the agent gave it. The engine needs it
// to tell its own events from someone else's on /events.
type Op struct{ ID string }

// ModelInfo is what the engine needs to know about one model.
type ModelInfo struct {
	ID string
	// Engine is the catalog engine ("llama", "whisper", "piper"); "" is taken as llama.
	Engine string
	// Present and Verified are the agent's flags: a complete, verified copy of the files exists.
	Present, Verified bool
	// Status is the model's status (the Status* constants; "absent", "stored", "unloaded", "failed"
	// and "n/a" are the others).
	Status string
	// Preset is the model's INI section as GET /models/{id} returns it ("preset"); the engine reads
	// the weights path, the context, the GPU layers and flash attention from it (ParsePreset).
	Preset string
	// EffectiveMiB is the router's memory.effective of the model in MiB (llama-router.md section
	// 3.6) when it reports it. It is recorded in the result, never used.
	EffectiveMiB *int64
}

// ErrUnknownModel is what Agent.Model returns (wrapped is fine) for a 404 LAI-AGT-021.
var ErrUnknownModel = errors.New("bench: unknown model")

// BusyError says that another model operation is running (409 LAI-AGT-022). OpID is the running
// operation's id when the agent said it. An Agent implementation returns it (or wraps it) for that
// answer, and Run returns it as it is when the refusal came before anything was changed.
type BusyError struct{ OpID string }

// Error implements error.
func (e *BusyError) Error() string {
	if e.OpID == "" {
		return "bench: another model operation is running"
	}
	return "bench: another model operation is running (" + e.OpID + ")"
}

// Agent is the agent's API as the engine uses it. AICTL-05 implements it over the HTTP client.
//
// Load must be POST /models/{id}/load with {"wait": true, "unload_others": false}, and Unload
// POST /models/{id}/unload with {} (no force): the engine never evicts models it did not unload
// itself. A 409 LAI-AGT-022 must come back as *BusyError.
type Agent interface {
	// Models is GET /models.
	Models(ctx context.Context) ([]ModelInfo, error)
	// Model is GET /models/{id}, with the preset text. An unknown id wraps ErrUnknownModel.
	Model(ctx context.Context, id string) (ModelInfo, error)
	// Load loads a model and waits until it is loaded.
	Load(ctx context.Context, id string) (Op, error)
	// Unload unloads a model.
	Unload(ctx context.Context, id string) (Op, error)
	// Events opens GET /events?types=model.* (live events only). The channel closes when ctx ends or
	// the stream breaks.
	Events(ctx context.Context) (<-chan events.Event, error)
}

// ChatRequest is the measured request.
type ChatRequest struct {
	Model     string
	Prompt    string
	MaxTokens int
}

// ChatResult is what the engine reads from the chat completion response.
type ChatResult struct {
	// PredictedPerSecond is timings.predicted_per_second: the server's generation speed, with
	// speculative decoding when the model uses it (server_tg_tps).
	PredictedPerSecond float64
	// DraftN and DraftAccepted are timings.draft_n and timings.draft_n_accepted when the server
	// reports them (zero otherwise).
	DraftN, DraftAccepted int64
}

// Chatter sends the chat completion to the router (127.0.0.1:8080 with the node's key). It lives
// behind a seam because the engine must not hold the key.
type Chatter interface {
	Chat(ctx context.Context, req ChatRequest) (ChatResult, error)
}

// GPUSampler reads the GPUs once, in-process. hardware.Sampler (the real Service and hardware/fake)
// implements it.
type GPUSampler interface {
	SampleGPUs(ctx context.Context) ([]hardware.GPU, error)
}

// Deps are the seams of the engine. Agent, Chat and GPU are required; Procs only for a CPU-only
// node; Exec defaults to execx.Exec.
type Deps struct {
	Agent Agent
	Chat  Chatter
	GPU   GPUSampler
	Procs Procs
	Exec  execx.Runner
}

// Options configures a Bench. Every field is optional.
type Options struct {
	// NodeID is node.id, for the result.
	NodeID string
	// Backend is llama.backend of node.yaml (cuda, vulkan or cpu). Empty means detected from the
	// GPUs of the first baseline sample.
	Backend string
	// Build is the active build id ("f3586e66b-cuda"). Empty means the target of BuildLink.
	Build string
	// Dir is where the result is written. Default DefaultDir.
	Dir string
	// BenchBinary is llama-bench. Default DefaultBenchBinary.
	BenchBinary string
	// BuildLink is the active build symlink. Default DefaultBuildLink.
	BuildLink string
	// Env is the environment of llama-bench (the llama.env of node.yaml, so the benchmark sees the
	// GPU the way llama-server does). nil means execx's minimal environment.
	Env []string
	// DraftFromLog supplies the draft acceptance from the router log when the chat response has no
	// draft counters: accepted and drafted tokens since the given time. ok is false when unknown.
	DraftFromLog func(ctx context.Context, since time.Time) (accepted, drafted int64, ok bool)

	// Timeouts. Defaults BenchTimeout, ChatTimeout and RestoreTimeout.
	BenchTimeout, ChatTimeout, RestoreTimeout time.Duration

	// Test hooks. Nil means the real thing.
	Now      func() time.Time
	Sleep    func(ctx context.Context, d time.Duration) error
	Readlink func(path string) (string, error)
}

// Bench runs benchmarks. It is safe to reuse, but one Run at a time makes sense: the machine is
// the shared resource.
type Bench struct {
	d Deps
	o Options
}

// New validates the seams and returns a Bench.
func New(d Deps, o Options) (*Bench, error) {
	if d.Agent == nil || d.Chat == nil || d.GPU == nil {
		return nil, errors.New("bench: Agent, Chat and GPU are required")
	}
	if d.Exec == nil {
		d.Exec = execx.Exec{}
	}
	if o.Dir == "" {
		o.Dir = DefaultDir
	}
	if o.BenchBinary == "" {
		o.BenchBinary = DefaultBenchBinary
	}
	if o.BuildLink == "" {
		o.BuildLink = DefaultBuildLink
	}
	if o.BenchTimeout <= 0 {
		o.BenchTimeout = BenchTimeout
	}
	if o.ChatTimeout <= 0 {
		o.ChatTimeout = ChatTimeout
	}
	if o.RestoreTimeout <= 0 {
		o.RestoreTimeout = RestoreTimeout
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Sleep == nil {
		o.Sleep = sleepCtx
	}
	if o.Readlink == nil {
		o.Readlink = os.Readlink
	}
	return &Bench{d: d, o: o}, nil
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Error is a failed benchmark: the doctor code, the step, and the cause. A *BusyError cause makes
// the code LAI-AGT-022.
type Error struct {
	Code  string
	Step  Step
	Model string
	Err   error
}

// Error implements error.
func (e *Error) Error() string {
	return fmt.Sprintf("%s: benchmark of %s failed at %s: %v", e.Code, e.Model, e.Step, e.Err)
}

// Unwrap returns the cause.
func (e *Error) Unwrap() error { return e.Err }

// Result is the bench JSON (llama-router.md section 4.13 step 6) plus what the caller needs to
// print it. The VRAM fields are null on a CPU-only node, where peak_rss_mib and
// suggested_min_ram_mib are set instead. A disturbed run has no suggestion.
type Result struct {
	Model string  `json:"model"`
	Node  string  `json:"node"`
	Build string  `json:"build"`
	GPU   *string `json:"gpu"`
	Ctx   int64   `json:"ctx"`

	BaselineVRAMMiB     *int64 `json:"baseline_vram_mib"`
	PeakVRAMMiB         *int64 `json:"peak_vram_mib"`
	MeasuredVRAMMiB     *int64 `json:"measured_vram_mib"`
	SuggestedMinVRAMMiB *int64 `json:"suggested_min_vram_mib"`

	PP512TPS      float64  `json:"pp512_tps"`
	TG128TPS      float64  `json:"tg128_tps"`
	ServerTGTPS   *float64 `json:"server_tg_tps"`
	MTPAcceptance *float64 `json:"mtp_acceptance"`

	MeasuredAt string `json:"measured_at"`
	// Disturbed is true when another model operation happened during the run (or the event stream
	// was lost): the numbers are not trustworthy and no suggestion is made.
	Disturbed bool `json:"disturbed,omitempty"`

	// Additions to the documented shape; all optional.
	EffectiveVRAMMiB   *int64 `json:"effective_vram_mib,omitempty"`
	GPUTotalMiB        *int64 `json:"gpu_total_mib,omitempty"`
	PeakRSSMiB         *int64 `json:"peak_rss_mib,omitempty"`
	SuggestedMinRAMMiB *int64 `json:"suggested_min_ram_mib,omitempty"`

	// Path is the file the result was written to. Restored lists the models loaded again,
	// RestoreFailed those that could not be (the node is then not as it was: the caller must say so,
	// with `aictl load <id>` as the fix).
	Path          string   `json:"-"`
	Restored      []string `json:"-"`
	RestoreFailed []string `json:"-"`
}

// SuggestedMiB returns the catalog suggestion: the key ("min_vram_mib" or "min_ram_mib") and its
// value, or ok false when the run was disturbed.
func (r *Result) SuggestedMiB() (key string, value int64, ok bool) {
	switch {
	case r.Disturbed:
		return "", 0, false
	case r.SuggestedMinVRAMMiB != nil:
		return "min_vram_mib", *r.SuggestedMinVRAMMiB, true
	case r.SuggestedMinRAMMiB != nil:
		return "min_ram_mib", *r.SuggestedMinRAMMiB, true
	}
	return "", 0, false
}

// Suggest is ceil(measured x 1.10) in integer arithmetic (no float rounding surprises).
func Suggest(measuredMiB int64) int64 {
	return (measuredMiB*marginPct + 99) / 100
}

// run is the state of one Run.
type run struct {
	b     *Bench
	model string
	own   map[string]bool // operation ids of this run's own load and unload calls
}

func (r *run) fail(step Step, err error) *Error {
	code := CodeBenchFailed
	var busy *BusyError
	if errors.As(err, &busy) {
		code = CodeBusy
	}
	return &Error{Code: code, Step: step, Model: r.model, Err: err}
}

func (r *run) note(op Op) {
	if op.ID != "" {
		r.own[op.ID] = true
	}
}

// Run benchmarks one model (llama-router.md section 4.13). The confirmation that loaded models
// will be unloaded is the caller's (aictl bench --yes). On failure it returns a nil Result and an
// *Error (or the *BusyError of a refusal before anything was changed); the models that were loaded
// before are restored either way. After a Result, check RestoreFailed.
func (b *Bench) Run(ctx context.Context, id string) (res *Result, err error) {
	r := &run{b: b, model: id, own: map[string]bool{}}

	// Watch /events before looking at anything, so no operation can slip between the check and the
	// first change.
	wctx, cancelWatch := context.WithCancel(ctx)
	defer cancelWatch()
	ch, eerr := b.d.Agent.Events(wctx)
	if eerr != nil {
		return nil, r.fail(StepPrecheck, fmt.Errorf("cannot watch /events: %w", eerr))
	}
	w := startWatch(ch, cancelWatch)
	stopWatch := func() bool {
		w.stop() // ends the watch first, then cancels the stream: see watch.stop
		return w.disturbed(r.own)
	}
	defer func() { // the early-return paths; stop is idempotent
		w.stop()
	}()

	info, ps, prev, perr := r.precheck(ctx, id)
	if perr != nil {
		return nil, perr
	}

	// From here on the node's models are changed: restore them however this ends.
	restored, restoreFailed := []string(nil), []string(nil)
	didRestore := false
	restore := func() {
		if didRestore {
			return
		}
		didRestore = true
		restored, restoreFailed = r.restore(ctx, prev)
	}
	defer func() {
		restore()
		if res != nil {
			res.Restored, res.RestoreFailed = restored, restoreFailed
		}
	}()

	m, merr := r.measure(ctx, id, info, ps, prev)
	if merr != nil {
		return nil, merr
	}
	m.disturbed = stopWatch()

	restore()

	out := r.assemble(id, ps, m)
	path, werr := b.write(out)
	if werr != nil {
		return nil, r.fail(StepWrite, werr)
	}
	out.Path = path
	return out, nil
}

// precheck is step 1: the model is benchable, nothing is running, and the previously loaded
// models are recorded.
func (r *run) precheck(ctx context.Context, id string) (ModelInfo, Preset, []string, error) {
	info, err := r.b.d.Agent.Model(ctx, id)
	if err != nil {
		if errors.Is(err, ErrUnknownModel) {
			return info, Preset{}, nil, &Error{Code: CodeUnknownModel, Step: StepPrecheck, Model: id, Err: err}
		}
		return info, Preset{}, nil, r.fail(StepPrecheck, err)
	}
	if e := info.Engine; e != "" && e != "llama" {
		return info, Preset{}, nil, r.fail(StepPrecheck, fmt.Errorf("%s is a %s model; only llama models can be benchmarked", id, e))
	}
	if !info.Present || !info.Verified {
		return info, Preset{}, nil, &Error{Code: CodeFilesMissing, Step: StepPrecheck, Model: id,
			Err: errors.New("the model files are missing or not verified")}
	}
	ps, err := ParsePreset(info.Preset)
	if err != nil {
		return info, ps, nil, r.fail(StepPrecheck, err)
	}
	models, err := r.b.d.Agent.Models(ctx)
	if err != nil {
		return info, ps, nil, r.fail(StepPrecheck, err)
	}
	var prev []string
	for _, m := range models {
		switch m.Status {
		case StatusLoading:
			// Not wrapped in an Error: nothing was changed, the caller exits 6.
			return info, ps, nil, &BusyError{}
		case StatusLoaded, StatusSleeping:
			prev = append(prev, m.ID)
		}
	}
	return info, ps, prev, nil
}

// restore is step 5: load the models that were loaded before and are not loaded now. It never
// stops at the first failure, and it ignores the cancellation of ctx (Ctrl-C must not leave the
// node empty).
func (r *run) restore(ctx context.Context, prev []string) (restored, failed []string) {
	if len(prev) == 0 {
		return nil, nil
	}
	base := context.WithoutCancel(ctx)
	// Unload the benchmarked model if it is still loaded and was not loaded before (a failure in
	// the middle of step 3).
	was := map[string]bool{}
	for _, id := range prev {
		was[id] = true
	}
	now := r.loadedNow(base)
	if now[r.model] && !was[r.model] {
		uctx, cancel := context.WithTimeout(base, r.b.o.RestoreTimeout)
		_, _ = r.b.d.Agent.Unload(uctx, r.model)
		cancel()
		now = r.loadedNow(base)
	}
	for _, id := range prev {
		if now[id] {
			continue
		}
		lctx, cancel := context.WithTimeout(base, r.b.o.RestoreTimeout)
		_, err := r.b.d.Agent.Load(lctx, id)
		cancel()
		if err != nil {
			failed = append(failed, id)
			continue
		}
		restored = append(restored, id)
	}
	return restored, failed
}

// loadedNow returns the ids that are loaded or sleeping now (empty when the agent cannot say).
func (r *run) loadedNow(ctx context.Context) map[string]bool {
	out := map[string]bool{}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	models, err := r.b.d.Agent.Models(cctx)
	if err != nil {
		return out
	}
	for _, m := range models {
		if m.Status == StatusLoaded || m.Status == StatusSleeping {
			out[m.ID] = true
		}
	}
	return out
}

// measured is what steps 2 to 4 produce.
type measured struct {
	backend     string
	gpuName     string
	gpuTotalMiB int64
	baseline    int64
	peak        int64
	peakRSSMiB  int64
	effective   *int64
	pp512, tg   float64
	serverTG    *float64
	acceptance  *float64
	disturbed   bool
}

// measure is steps 2 to 4.
func (r *run) measure(ctx context.Context, id string, info ModelInfo, ps Preset, prev []string) (*measured, error) {
	b := r.b
	m := &measured{}

	// 2. Unload everything, then check that nothing is left.
	for _, p := range prev {
		op, err := b.d.Agent.Unload(ctx, p)
		if err != nil {
			return nil, r.fail(StepUnload, fmt.Errorf("unload %s: %w", p, err))
		}
		r.note(op)
	}
	if len(prev) > 0 {
		if left := r.loadedNow(ctx); len(left) > 0 {
			return nil, r.fail(StepUnload, errors.New("a model is still loaded after the unload"))
		}
	}

	// Baseline U0: the median of BaselineSamples samples, 1 s apart. The first sample also decides
	// the backend when node.yaml did not.
	gpus, gerr := b.d.GPU.SampleGPUs(ctx)
	m.backend = b.o.Backend
	if m.backend == "" {
		if len(gpus) == 0 {
			if gerr != nil {
				return nil, r.fail(StepBaseline, fmt.Errorf("cannot read the GPU: %w", gerr))
			}
			m.backend = hardware.BackendCPU
		} else {
			m.backend = detectBackend(gpus)
		}
	}
	cpu := m.backend == hardware.BackendCPU

	var rss *rssTracker
	if cpu {
		if b.d.Procs == nil {
			return nil, r.fail(StepBaseline, errors.New("a CPU-only node needs a process reader (Deps.Procs)"))
		}
		var err error
		if rss, err = newRSSTracker(ctx, b.d.Procs); err != nil {
			return nil, r.fail(StepBaseline, err)
		}
	} else {
		var err error
		m.baseline, err = r.baselineVRAM(ctx, m.backend, gpus, gerr)
		if err != nil {
			return nil, r.fail(StepBaseline, err)
		}
		fit := hardware.Snapshot{GPUs: gpus}.FitGPUs(m.backend)
		names := make([]string, 0, len(fit))
		for _, g := range fit {
			names = append(names, g.Name)
			m.gpuTotalMiB += g.VRAMTotalMiB
		}
		m.gpuName = strings.Join(names, ", ")
	}

	// 3. Load alone, then one real request with the memory curve.
	op, err := b.d.Agent.Load(ctx, id)
	if err != nil {
		return nil, r.fail(StepLoad, err)
	}
	r.note(op)

	started := b.o.Now()
	var chat ChatResult
	req := func() error {
		cctx, cancel := context.WithTimeout(ctx, b.o.ChatTimeout)
		defer cancel()
		var err error
		chat, err = b.d.Chat.Chat(cctx, ChatRequest{Model: id, Prompt: Prompt(), MaxTokens: ChatMaxTokens})
		return err
	}
	if cpu {
		peak, err := r.sampleDuring(ctx, req, func(c context.Context) (int64, error) { return rss.sample(c) })
		if err != nil {
			return nil, r.fail(StepMeasure, err)
		}
		if peak <= 0 {
			return nil, r.fail(StepMeasure, errors.New("the model process used no memory"))
		}
		m.peakRSSMiB = peak
	} else {
		peak, err := r.sampleDuring(ctx, req, func(c context.Context) (int64, error) {
			g, err := b.d.GPU.SampleGPUs(c)
			if err != nil {
				return 0, err
			}
			return hardware.Snapshot{GPUs: g}.FitUsedMiB(m.backend), nil
		})
		if err != nil {
			return nil, r.fail(StepMeasure, err)
		}
		if peak <= m.baseline {
			return nil, r.fail(StepMeasure, fmt.Errorf("graphics memory did not rise (%d MiB before, %d MiB peak): another program may have freed memory, or the GPU cannot be read", m.baseline, peak))
		}
		m.peak = peak
	}
	if chat.PredictedPerSecond > 0 {
		v := round(chat.PredictedPerSecond, 1)
		m.serverTG = &v
	}
	m.acceptance = r.acceptance(ctx, chat, started)
	if cur, err := b.d.Agent.Model(ctx, id); err == nil {
		m.effective = cur.EffectiveMiB
	}

	// 4. Throughput: the model is unloaded first, so llama-bench has the GPU to itself.
	uop, err := b.d.Agent.Unload(ctx, id)
	if err != nil {
		return nil, r.fail(StepUnload, fmt.Errorf("unload %s: %w", id, err))
	}
	r.note(uop)
	m.pp512, m.tg, err = b.llamaBench(ctx, ps)
	if err != nil {
		return nil, r.fail(StepThroughput, err)
	}
	return m, nil
}

// acceptance is the share of drafted tokens the model accepted: from the response timings, else
// from the router log, else nil.
func (r *run) acceptance(ctx context.Context, chat ChatResult, since time.Time) *float64 {
	acc, drafted := chat.DraftAccepted, chat.DraftN
	if drafted <= 0 && r.b.o.DraftFromLog != nil {
		if a, d, ok := r.b.o.DraftFromLog(ctx, since); ok {
			acc, drafted = a, d
		}
	}
	if drafted <= 0 {
		return nil
	}
	v := round(float64(acc)/float64(drafted), 2)
	return &v
}

// assemble builds the Result from the measurements (step 6, before the file is written).
func (r *run) assemble(id string, ps Preset, m *measured) *Result {
	b := r.b
	res := &Result{
		Model: id, Node: b.o.NodeID, Build: b.build(), Ctx: ps.Ctx,
		PP512TPS: round(m.pp512, 1), TG128TPS: round(m.tg, 1),
		ServerTGTPS: m.serverTG, MTPAcceptance: m.acceptance,
		MeasuredAt:       b.o.Now().UTC().Format(time.RFC3339),
		Disturbed:        m.disturbed,
		EffectiveVRAMMiB: m.effective,
	}
	if m.backend == hardware.BackendCPU {
		peak := m.peakRSSMiB
		res.PeakRSSMiB = &peak
		if !m.disturbed {
			s := Suggest(peak)
			res.SuggestedMinRAMMiB = &s
		}
		return res
	}
	name, total := m.gpuName, m.gpuTotalMiB
	baseline, peak := m.baseline, m.peak
	measured := peak - baseline
	res.GPU, res.GPUTotalMiB = &name, &total
	res.BaselineVRAMMiB, res.PeakVRAMMiB, res.MeasuredVRAMMiB = &baseline, &peak, &measured
	if !m.disturbed {
		s := Suggest(measured)
		res.SuggestedMinVRAMMiB = &s
	}
	return res
}

// build is the build id of the result.
func (b *Bench) build() string {
	if b.o.Build != "" {
		return b.o.Build
	}
	if t, err := b.o.Readlink(b.o.BuildLink); err == nil && t != "" {
		return filepath.Base(strings.TrimRight(t, "/"))
	}
	return "unknown"
}

// FileName is the result file name: <id>-<YYYYMMDDTHHMMSSZ>.json.
func FileName(id string, at time.Time) string {
	return id + "-" + at.UTC().Format("20060102T150405Z") + ".json"
}

// write stores the result as <Dir>/<id>-<ts>.json (root 0644, replaced atomically).
func (b *Bench) write(res *Result) (string, error) {
	data, err := res.MarshalJSONFile()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(b.o.Dir, resultDirMode); err != nil {
		return "", err
	}
	at, err := time.Parse(time.RFC3339, res.MeasuredAt)
	if err != nil {
		at = b.o.Now()
	}
	path := filepath.Join(b.o.Dir, FileName(res.Model, at))
	if err := config.WriteFileAtomic(path, data, resultFileMode, -1, -1); err != nil {
		return "", err
	}
	return path, nil
}

// watch collects the model events of a run.
//
// A closed channel cannot say when it was closed, so the watch never asks the goroutine: the
// stream is cancelled only by stop, and only after the goroutine has ended. A channel that is
// closed while the goroutine still runs therefore was closed by the stream (a dropped connection,
// the caller's own context), never by the engine's cancel, however late the goroutine got to look.
type watch struct {
	mu       sync.Mutex
	seen     []events.Event
	lost     bool
	stopCh   chan struct{}
	stopOnce sync.Once
	done     chan struct{}
	cancel   func() // cancels the stream; called by stop once the loop has ended
	// gate, when not nil, runs once before the loop looks at anything. Tests use it to hold the
	// goroutine back; it is nil in production.
	gate func()
}

// startWatch watches ch. cancel ends the stream behind it and is called only by stop.
func startWatch(ch <-chan events.Event, cancel func()) *watch {
	return startWatchGated(ch, cancel, nil)
}

func startWatchGated(ch <-chan events.Event, cancel, gate func()) *watch {
	w := &watch{stopCh: make(chan struct{}), done: make(chan struct{}), cancel: cancel, gate: gate}
	go w.loop(ch)
	return w
}

func (w *watch) record(ev events.Event) {
	w.mu.Lock()
	defer w.mu.Unlock()
	switch ev.Type {
	case events.TypeModelLoading, events.TypeModelLoaded, events.TypeModelUnloaded, events.TypeModelFailed:
		w.seen = append(w.seen, ev)
	case events.TypeResync, events.TypeShutdown:
		// The agent restarted or the stream skipped events: what happened meanwhile is unknown.
		w.lost = true
	}
}

func (w *watch) loop(ch <-chan events.Event) {
	defer close(w.done)
	if w.gate != nil {
		w.gate()
	}
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				w.markLost() // the stream broke while the run was going on: nothing cancelled it
				return
			}
			w.record(ev)
		case <-w.stopCh:
			for { // whatever was already delivered counts, and so does a close that already happened
				select {
				case ev, ok := <-ch:
					if !ok {
						w.markLost()
						return
					}
					w.record(ev)
				default:
					return
				}
			}
		}
	}
}

func (w *watch) markLost() {
	w.mu.Lock()
	w.lost = true
	w.mu.Unlock()
}

// stop ends the watch, waits until every event already delivered (and a close that already
// happened) was looked at, and only then cancels the stream. It is idempotent.
func (w *watch) stop() {
	w.stopOnce.Do(func() { close(w.stopCh) })
	<-w.done
	if w.cancel != nil {
		w.cancel()
	}
}

// disturbed reports whether a model event that does not belong to one of the run's own operations
// was seen (an event with no operation id, such as the watcher's crash report, counts), or the
// stream was lost.
func (w *watch) disturbed(own map[string]bool) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.lost {
		return true
	}
	for _, ev := range w.seen {
		if ev.OpID == "" || !own[ev.OpID] {
			return true
		}
	}
	return false
}
