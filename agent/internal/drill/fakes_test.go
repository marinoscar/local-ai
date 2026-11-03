package drill

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/config/fake"
	"github.com/marinoscar/local-ai/agent/internal/modes"
	"github.com/marinoscar/local-ai/agent/internal/state"
)

// t0 is the one instant of the tests; the fake clock starts there.
var t0 = time.Date(2026, 11, 1, 4, 35, 12, 0, time.UTC)

// fakeState is an in-memory StateStore.
type fakeState struct {
	mu   sync.Mutex
	st   state.State
	hook func(*state.State) // called after every mutation, under the lock
	// failMutate makes MutateSync return this error without applying it.
	failMutate error
	mutations  int
}

func newFakeState() *fakeState {
	return &fakeState{st: state.State{SchemaVersion: state.SchemaVersion, Mode: state.ModeOnline}}
}

func (f *fakeState) Snapshot() state.State {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.st.Clone()
}

func (f *fakeState) MutateSync(fn func(*state.State) error) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failMutate != nil {
		return f.failMutate
	}
	f.mutations++
	if err := fn(&f.st); err != nil {
		return err
	}
	if f.hook != nil {
		f.hook(&f.st)
	}
	return nil
}

// block returns the drill block as a map.
func (f *fakeState) block(t *testing.T) map[string]json.RawMessage {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	m := map[string]json.RawMessage{}
	if len(f.st.Drill) == 0 {
		return m
	}
	if err := json.Unmarshal(f.st.Drill, &m); err != nil {
		t.Fatalf("state drill block is not an object: %s", f.st.Drill)
	}
	return m
}

func (f *fakeState) setBlock(raw string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.st.Drill = json.RawMessage(raw)
}

type recEvent struct {
	Typ  string
	OpID string
	Data any
}

type fakePublisher struct {
	mu sync.Mutex
	ev []recEvent
}

func (p *fakePublisher) Publish(typ, opID string, data any) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.ev = append(p.ev, recEvent{typ, opID, data})
	return nil
}

func (p *fakePublisher) events() []recEvent {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]recEvent(nil), p.ev...)
}

func (p *fakePublisher) types() []string {
	var out []string
	for _, e := range p.events() {
		out = append(out, e.Typ)
	}
	return out
}

type fakeFence struct {
	mu       sync.Mutex
	enabled  bool
	enables  int
	disables int
	report   FenceReport
}

func (f *fakeFence) Enable() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.enabled = true
	f.enables++
}

func (f *fakeFence) Disable() FenceReport {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.enabled = false
	f.disables++
	return f.report
}

func (f *fakeFence) isEnabled() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.enabled
}

type fakeRestorer struct {
	mu         sync.Mutex
	captured   Captured
	captureErr error
	restoreErr error
	result     *Restored // nil: echo the captured state
	restores   []Captured
	// restoreCtxErr records ctx.Err() seen by Restore: an abort must not cancel the restore.
	restoreCtxErr error
}

func (r *fakeRestorer) Capture(context.Context) (Captured, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.captured, r.captureErr
}

func (r *fakeRestorer) Restore(ctx context.Context, c Captured) (Restored, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.restores = append(r.restores, c)
	r.restoreCtxErr = ctx.Err()
	if r.result != nil {
		return *r.result, r.restoreErr
	}
	return Restored{LoadedModels: c.LoadedModels, DefaultModel: c.DefaultModel, OK: r.restoreErr == nil}, r.restoreErr
}

func (r *fakeRestorer) restoreCalls() []Captured {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Captured(nil), r.restores...)
}

type fakeMode struct {
	mu   sync.Mutex
	info modes.Info
}

func (m *fakeMode) Info() modes.Info {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.info
}

func (m *fakeMode) set(mode modes.Mode) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.info = modes.Info{Mode: mode, Effective: mode}
}

type fakeArk struct {
	mu     sync.Mutex
	active bool
}

func (a *fakeArk) RunActive() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.active
}

func (a *fakeArk) set(v bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.active = v
}

type fakeLlama struct {
	mu    sync.Mutex
	busy  bool
	err   error
	calls int
}

func (l *fakeLlama) Busy(context.Context) (bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls++
	return l.busy, l.err
}

func (l *fakeLlama) set(v bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.busy = v
}

type fakePower struct {
	mu sync.Mutex
	st BatteryStatus
	ok bool
}

func (p *fakePower) Battery() (BatteryStatus, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.st, p.ok
}

type fakeMetrics struct {
	mu sync.Mutex
	m  map[string]float64
}

func (f *fakeMetrics) Set(name string, labels map[string]string, v float64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.m == nil {
		f.m = map[string]float64{}
	}
	key := name
	if c := labels["check"]; c != "" {
		key += "{" + c + "}"
	}
	f.m[key] = v
}

func (f *fakeMetrics) get(key string) (float64, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.m[key]
	return v, ok
}

// counterRand yields 0x00 0x01, 0x00 0x02, ... so run ids are predictable: -0001, -0002, ...
type counterRand struct {
	mu sync.Mutex
	n  int
	// fixed, when set, is what every read returns (two bytes: one 4-hex suffix, always the same).
	fixed []byte
}

func (c *counterRand) Read(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.fixed != nil {
		copy(p, c.fixed)
		return len(p), nil
	}
	for i := range p {
		p[i] = 0
	}
	c.n++
	if len(p) >= 2 {
		p[0], p[1] = byte(c.n>>8), byte(c.n)
	}
	return len(p), nil
}

var _ io.Reader = (*counterRand)(nil)

// rig is an engine over fakes with a manual clock.
type rig struct {
	t       *testing.T
	eng     *Engine
	reg     *Registry
	results *Results
	fs      *memFS
	state   *fakeState
	clock   *fake.Clock
	pub     *fakePublisher
	fence   *fakeFence
	rest    *fakeRestorer
	mode    *fakeMode
	ark     *fakeArk
	llama   *fakeLlama
	power   *fakePower
	metrics *fakeMetrics
	rnd     *counterRand
}

type rigOpt func(*rig, *Deps)

func withoutFence() rigOpt    { return func(_ *rig, d *Deps) { d.Fence = nil } }
func withoutRestorer() rigOpt { return func(_ *rig, d *Deps) { d.Restorer = nil } }

const testDir = "/var/lib/localai/drill"

func newRig(t *testing.T, opts ...rigOpt) *rig {
	t.Helper()
	r := &rig{
		t: t, reg: NewRegistry(), fs: newMemFS(), state: newFakeState(), clock: fake.NewClock(t0),
		pub: &fakePublisher{}, fence: &fakeFence{}, rest: &fakeRestorer{captured: Captured{LoadedModels: []string{"qwen3.5-9b-q4km"}, DefaultModel: "qwen3.5-9b-q4km", WhisperRunning: true}},
		mode: &fakeMode{}, ark: &fakeArk{}, llama: &fakeLlama{}, power: &fakePower{}, metrics: &fakeMetrics{}, rnd: &counterRand{},
	}
	r.mode.set(modes.Online)
	r.results = NewResults(ResultsOptions{FS: r.fs, Dir: testDir})
	d := Deps{
		Registry: r.reg, State: r.state, Publisher: r.pub, Mode: r.mode, Ark: r.ark, Llama: r.llama, Power: r.power,
		Fence: r.fence, Restorer: r.rest, Metrics: r.metrics,
		NodeID: "ai-n3", AgentVersion: "0.4.0", CatalogVersion: func() string { return "2026.11.02-1" },
		Clock: r.clock, Rand: r.rnd, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	for _, o := range opts {
		o(r, &d)
	}
	d.Results = r.results
	eng, err := New(d)
	if err != nil {
		t.Fatal(err)
	}
	r.eng = eng
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = eng.Shutdown(ctx)
	})
	return r
}

// add registers a check, failing the test on error.
func (r *rig) add(c Check) {
	r.t.Helper()
	if c.Area == "" {
		c.Area = "test"
	}
	if c.Order == 0 {
		c.Order = 1
	}
	if err := r.reg.Add(c); err != nil {
		r.t.Fatal(err)
	}
}

// passing returns a check that passes at once.
func passing(id string, order int) Check {
	return Check{ID: id, Order: order, Run: func(context.Context, *Env) Outcome { return Outcome{Result: ResultPass} }}
}

// startWait starts a run and waits for it to end, returning its decoded document.
func (r *rig) startWait(req StartRequest) (StartResult, *Document) {
	r.t.Helper()
	res, err := r.eng.Start(context.Background(), req)
	if err != nil {
		r.t.Fatalf("Start: %v", err)
	}
	r.wait()
	return res, r.doc(res.RunID)
}

func (r *rig) wait() {
	r.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := r.eng.Wait(ctx); err != nil {
		r.t.Fatalf("the run did not end: %v", err)
	}
}

func (r *rig) doc(runID string) *Document {
	r.t.Helper()
	raw, err := r.eng.Get(runID)
	if err != nil {
		r.t.Fatalf("Get(%s): %v", runID, err)
	}
	var d Document
	if err := json.Unmarshal(raw, &d); err != nil {
		r.t.Fatalf("document of %s is not JSON: %v\n%s", runID, err, raw)
	}
	return &d
}
