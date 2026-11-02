package bench

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/events"
	"github.com/marinoscar/local-ai/agent/internal/execx"
	execfake "github.com/marinoscar/local-ai/agent/internal/execx/fake"
	"github.com/marinoscar/local-ai/agent/internal/hardware"
)

const (
	testModel     = "m-9b"
	testWeights   = "/srv/localai/models/m-9b/m-9b-Q4_K_M.gguf"
	testGPUName   = "NVIDIA GeForce RTX 2070 with Max-Q Design"
	testBuild     = "f3586e66b-cuda"
	baseUsedMiB   = 210  // what an idle GPU holds
	loadedMiB     = 5800 // weights, once loaded
	requestMiB    = 781  // KV cache and compute buffers, only during a request
	chatSamples   = 3    // samples taken while the fake request runs
	benchJSONOK   = `[{"n_prompt":512,"n_gen":0,"avg_ts":1450.21,"stddev_ts":3.1},{"n_prompt":0,"n_gen":128,"avg_ts":38.41,"stddev_ts":0.2}]`
	testPresetFmt = "[%s]\nmodel = %s\nc = 32768\nn-gpu-layers = 99\nflash-attn = on\n"
)

var testNow = time.Date(2026, 11, 2, 10, 30, 0, 0, time.UTC)

// world is the machine the fakes share: which models are loaded and how much graphics memory is in
// use. Nothing in it depends on time.
type world struct {
	mu       sync.Mutex
	loaded   []string
	chatting bool
	samples  int // samples taken while chatting
	cond     *sync.Cond
	opSeq    int
	ops      []string // "load m", "unload m", in call order
}

func newWorld(loaded ...string) *world {
	w := &world{loaded: append([]string(nil), loaded...)}
	w.cond = sync.NewCond(&w.mu)
	return w
}

func (w *world) isLoaded(id string) bool {
	for _, l := range w.loaded {
		if l == id {
			return true
		}
	}
	return false
}

func (w *world) usedMiB() int64 {
	used := int64(baseUsedMiB)
	used += int64(len(w.loaded)) * loadedMiB
	if w.chatting {
		used += requestMiB
	}
	return used
}

func (w *world) Loaded() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.loaded...)
}

func (w *world) Ops() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.ops...)
}

// fakeAgent implements Agent over a world.
type fakeAgent struct {
	w        *world
	events   chan events.Event
	evMu     sync.Mutex
	evClosed bool

	mu        sync.Mutex
	modelInfo ModelInfo
	failOp    map[string]error
	stuck     map[string]bool // unload of this id succeeds but the model stays loaded
	status    map[string]string
	eventsErr error
	modelsErr error
}

func newFakeAgent(w *world) *fakeAgent {
	return &fakeAgent{
		w:      w,
		events: make(chan events.Event, 64),
		modelInfo: ModelInfo{
			ID: testModel, Engine: "llama", Present: true, Verified: true,
			Preset: fmt.Sprintf(testPresetFmt, testModel, testWeights),
		},
		failOp: map[string]error{},
		stuck:  map[string]bool{},
		status: map[string]string{},
	}
}

func (a *fakeAgent) emit(typ, opID string) {
	a.evMu.Lock()
	defer a.evMu.Unlock()
	if a.evClosed {
		return // the stream broke: later events are lost
	}
	a.events <- events.Event{Type: typ, OpID: opID, Node: "node-a"}
}

// breakStream closes the event channel, as a dropped SSE connection does.
func (a *fakeAgent) breakStream() {
	a.evMu.Lock()
	defer a.evMu.Unlock()
	if !a.evClosed {
		a.evClosed = true
		close(a.events)
	}
}

func (a *fakeAgent) Models(context.Context) ([]ModelInfo, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.modelsErr != nil {
		return nil, a.modelsErr
	}
	a.w.mu.Lock()
	defer a.w.mu.Unlock()
	seen := map[string]bool{}
	var out []ModelInfo
	add := func(id string) {
		if seen[id] {
			return
		}
		seen[id] = true
		st := "unloaded"
		if a.w.isLoaded(id) {
			st = StatusLoaded
		}
		if s, ok := a.status[id]; ok {
			st = s
		}
		out = append(out, ModelInfo{ID: id, Engine: "llama", Present: true, Verified: true, Status: st})
	}
	add(testModel)
	for _, id := range a.w.loaded {
		add(id)
	}
	for id := range a.status {
		add(id)
	}
	return out, nil
}

func (a *fakeAgent) Model(_ context.Context, id string) (ModelInfo, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if id != a.modelInfo.ID {
		return ModelInfo{}, fmt.Errorf("model %q: %w", id, ErrUnknownModel)
	}
	return a.modelInfo, nil
}

func (a *fakeAgent) nextOp() Op {
	a.w.opSeq++
	return Op{ID: fmt.Sprintf("op_%012d", a.w.opSeq)}
}

func (a *fakeAgent) Load(_ context.Context, id string) (Op, error) {
	a.mu.Lock()
	fail := a.failOp["load "+id]
	a.mu.Unlock()
	a.w.mu.Lock()
	defer a.w.mu.Unlock()
	a.w.ops = append(a.w.ops, "load "+id)
	if fail != nil {
		return Op{}, fail
	}
	op := a.nextOp()
	if !a.w.isLoaded(id) {
		a.w.loaded = append(a.w.loaded, id)
	}
	a.emit(events.TypeModelLoading, op.ID)
	a.emit(events.TypeModelLoaded, op.ID)
	return op, nil
}

func (a *fakeAgent) Unload(_ context.Context, id string) (Op, error) {
	a.mu.Lock()
	fail, stuck := a.failOp["unload "+id], a.stuck[id]
	a.mu.Unlock()
	a.w.mu.Lock()
	defer a.w.mu.Unlock()
	a.w.ops = append(a.w.ops, "unload "+id)
	if fail != nil {
		return Op{}, fail
	}
	op := a.nextOp()
	if !stuck {
		kept := a.w.loaded[:0:0]
		for _, l := range a.w.loaded {
			if l != id {
				kept = append(kept, l)
			}
		}
		a.w.loaded = kept
	}
	a.emit(events.TypeModelUnloaded, op.ID)
	return op, nil
}

func (a *fakeAgent) Events(ctx context.Context) (<-chan events.Event, error) {
	if a.eventsErr != nil {
		return nil, a.eventsErr
	}
	return a.events, nil
}

// fakeSampler implements GPUSampler over a world.
type fakeSampler struct {
	w    *world
	gpus []hardware.GPU
	err  error
}

func (s *fakeSampler) SampleGPUs(context.Context) ([]hardware.GPU, error) {
	if s.err != nil {
		return nil, s.err
	}
	s.w.mu.Lock()
	defer s.w.mu.Unlock()
	if s.w.chatting {
		s.w.samples++
		s.w.cond.Broadcast()
	}
	out := append([]hardware.GPU(nil), s.gpus...)
	if len(out) > 0 {
		out[0].VRAMUsedMiB = s.w.usedMiB()
	}
	return out, nil
}

// fakeChat implements Chatter: the request is "running" until the sampler has been read
// chatSamples times, so the curve always contains the request's peak, with no sleeping.
type fakeChat struct {
	w      *world
	result ChatResult
	err    error
	// before runs when the request starts (to inject events, to cancel the context).
	before func()
	calls  int
	last   ChatRequest
}

func (c *fakeChat) Chat(ctx context.Context, req ChatRequest) (ChatResult, error) {
	c.calls++
	c.last = req
	if c.before != nil {
		c.before()
	}
	if c.err != nil {
		return ChatResult{}, c.err
	}
	c.w.mu.Lock()
	c.w.chatting = true
	c.w.samples = 0
	for c.w.samples < chatSamples {
		c.w.cond.Wait()
	}
	c.w.chatting = false
	c.w.mu.Unlock()
	return c.result, nil
}

// rig bundles a Bench with its fakes.
type rig struct {
	t       *testing.T
	w       *world
	agent   *fakeAgent
	gpu     *fakeSampler
	chat    *fakeChat
	exec    *execfake.Runner
	dir     string
	sleeps  []time.Duration
	sleepMu sync.Mutex
	opts    Options
}

func benchKey() string {
	return execfake.Key(DefaultBenchBinary, mustPreset(testModel, testWeights).BenchArgs()...)
}

func mustPreset(id, weights string) Preset {
	p, err := ParsePreset(fmt.Sprintf(testPresetFmt, id, weights))
	if err != nil {
		panic(err)
	}
	return p
}

func newRig(t *testing.T, loaded ...string) *rig {
	t.Helper()
	w := newWorld(loaded...)
	r := &rig{
		t: t, w: w, agent: newFakeAgent(w),
		gpu: &fakeSampler{w: w, gpus: []hardware.GPU{{
			Vendor: hardware.VendorNVIDIA, Name: testGPUName, VRAMTotalMiB: 7982,
		}}},
		chat: &fakeChat{w: w, result: ChatResult{PredictedPerSecond: 41.04, DraftN: 100, DraftAccepted: 71}},
		exec: execfake.New(),
		dir:  t.TempDir(),
	}
	r.exec.Expect(benchKey(), execx.Result{Stdout: []byte(benchJSONOK)}, nil)
	r.opts = Options{
		NodeID: "ai-n3", Build: testBuild, Dir: r.dir,
		Now: func() time.Time { return testNow },
		Sleep: func(ctx context.Context, d time.Duration) error {
			r.sleepMu.Lock()
			r.sleeps = append(r.sleeps, d)
			r.sleepMu.Unlock()
			runtime.Gosched()
			return ctx.Err()
		},
	}
	return r
}

func (r *rig) bench() *Bench {
	r.t.Helper()
	b, err := New(Deps{Agent: r.agent, Chat: r.chat, GPU: r.gpu, Exec: r.exec}, r.opts)
	if err != nil {
		r.t.Fatalf("New: %v", err)
	}
	return b
}

func (r *rig) run() (*Result, error) {
	return r.bench().Run(context.Background(), testModel)
}

func (r *rig) sleepCount(d time.Duration) int {
	r.sleepMu.Lock()
	defer r.sleepMu.Unlock()
	n := 0
	for _, s := range r.sleeps {
		if s == d {
			n++
		}
	}
	return n
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func asBench(t *testing.T, err error) *Error {
	t.Helper()
	var e *Error
	if !errors.As(err, &e) {
		t.Fatalf("error %v is not a *bench.Error", err)
	}
	return e
}
