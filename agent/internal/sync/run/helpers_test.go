package run

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/ark"
	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/events"
	"github.com/marinoscar/local-ai/agent/internal/sync/item"
	"github.com/marinoscar/local-ai/agent/internal/sync/journal"
	"github.com/marinoscar/local-ai/agent/internal/sync/plan"
)

var bg = context.Background()

var epoch = time.Date(2026, 11, 2, 18, 4, 11, 0, time.UTC)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func sha(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// fakeClock is a manual clock: time moves only when a test advances it, and AfterFunc callbacks run
// in the advancing goroutine when their time comes.
type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*fakeTimer
}

type fakeTimer struct {
	at   time.Time
	f    func()
	done bool
}

func newFakeClock() *fakeClock { return &fakeClock{now: epoch} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) AfterFunc(d time.Duration, f func()) func() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &fakeTimer{at: c.now.Add(d), f: f}
	c.timers = append(c.timers, t)
	return func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		was := !t.done
		t.done = true
		return was
	}
}

// Advance moves time forward and fires the timers that are due, in order.
func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	var due []*fakeTimer
	for _, t := range c.timers {
		if !t.done && !t.at.After(c.now) {
			t.done = true
			due = append(due, t)
		}
	}
	c.mu.Unlock()
	sort.Slice(due, func(i, j int) bool { return due[i].at.Before(due[j].at) })
	for _, t := range due {
		t.f()
	}
}

func (c *fakeClock) pending() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, t := range c.timers {
		if !t.done {
			n++
		}
	}
	return n
}

// recorder is the event bus of the tests.
type recorder struct {
	mu  sync.Mutex
	evs []recorded
}

type recorded struct {
	Type string
	Data any
}

func (r *recorder) Publish(typ, opID string, data any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.evs = append(r.evs, recorded{typ, data})
	return nil
}

func (r *recorder) of(typ string) []recorded {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []recorded
	for _, e := range r.evs {
		if e.Type == typ {
			out = append(out, e)
		}
	}
	return out
}

func (r *recorder) itemDone() []events.SyncItemDone {
	var out []events.SyncItemDone
	for _, e := range r.of(events.TypeSyncItemDone) {
		out = append(out, e.Data.(events.SyncItemDone))
	}
	return out
}

func (r *recorder) done() []events.SyncDone {
	var out []events.SyncDone
	for _, e := range r.of(events.TypeSyncDone) {
		out = append(out, e.Data.(events.SyncDone))
	}
	return out
}

func (r *recorder) progress() []events.SyncProgress {
	var out []events.SyncProgress
	for _, e := range r.of(events.TypeSyncProgress) {
		out = append(out, e.Data.(events.SyncProgress))
	}
	return out
}

// fakePlanner returns canned plans and roots.
type fakePlanner struct {
	mu    sync.Mutex
	roots []ark.Root
	plans []plan.RootPlan
	items map[string]plan.CatalogItem
	err   error
	reqs  []PlanRequest
}

func (p *fakePlanner) Roots() []ark.Root {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]ark.Root(nil), p.roots...)
}

func (p *fakePlanner) Plan(ctx context.Context, req PlanRequest) (Plans, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.reqs = append(p.reqs, req)
	if p.err != nil {
		return Plans{}, p.err
	}
	out := Plans{CatalogVersion: "2026.11.02-1", MinFree: map[string]int64{}, Item: func(id string) (plan.CatalogItem, bool) {
		it, ok := p.items[id]
		return it, ok
	}}
	want := map[string]bool{}
	for _, id := range req.RootIDs {
		want[id] = true
	}
	for _, rp := range p.plans {
		if want[rp.Root] {
			out.Plans = append(out.Plans, rp)
			out.MinFree[rp.Root] = 1
		}
	}
	return out, nil
}

func (p *fakePlanner) lastRequest() PlanRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.reqs[len(p.reqs)-1]
}

// fakeMachine is the item machine of the engine tests: it records what starts, how many run at once
// (overall and per root), and lets a test decide each item's outcome and when it ends.
type fakeMachine struct {
	mu      sync.Mutex
	started []string
	cur     int
	maxCur  int
	perRoot map[string]int
	maxRoot map[string]int
	// beh decides the outcome; it may block on ctx or a channel.
	beh func(ctx context.Context, job item.Job) item.Result
	// startedCh gets "root/item" when an item starts running (buffered by the test).
	startedCh chan string
	recovered []journal.Intent
	recoverFn func(journal.Intent) (item.Result, bool)
}

func newFakeMachine() *fakeMachine {
	return &fakeMachine{perRoot: map[string]int{}, maxRoot: map[string]int{}, startedCh: make(chan string, 256)}
}

func verified(job item.Job, version string) item.Result {
	return item.Result{Root: job.Root.ID, Item: job.Ref.ID, Version: version, State: journal.StateVerified}
}

func (m *fakeMachine) Run(ctx context.Context, run *journal.Run, job item.Job) item.Result {
	key := job.Root.ID + "/" + job.Ref.ID
	m.mu.Lock()
	m.started = append(m.started, key)
	m.cur++
	m.maxCur = max(m.maxCur, m.cur)
	m.perRoot[job.Root.ID]++
	m.maxRoot[job.Root.ID] = max(m.maxRoot[job.Root.ID], m.perRoot[job.Root.ID])
	beh := m.beh
	m.mu.Unlock()
	m.startedCh <- key
	defer func() {
		m.mu.Lock()
		m.cur--
		m.perRoot[job.Root.ID]--
		m.mu.Unlock()
	}()
	if beh == nil {
		return verified(job, "v1")
	}
	return beh(ctx, job)
}

func (m *fakeMachine) Recover(ctx context.Context, run *journal.Run, in journal.Intent) (item.Result, bool) {
	m.mu.Lock()
	m.recovered = append(m.recovered, in)
	fn := m.recoverFn
	m.mu.Unlock()
	if fn == nil {
		return item.Result{}, false
	}
	return fn(in)
}

func (m *fakeMachine) order() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.started...)
}

// rig is one engine over a temp node with every collaborator faked.
type rig struct {
	t     testing.TB
	dir   string
	jr    *journal.Journal
	rec   journal.Recovery
	eng   *Engine
	clk   *fakeClock
	evs   *recorder
	plnr  *fakePlanner
	mach  *fakeMachine
	set   Settings
	net   NetState
	batt  bool
	deps  Deps
	roots map[string]ark.Root
}

const (
	driveID = "ark-0a0b0c0d"
	driveB  = "ark-0e0f1011"
)

type rigOpt func(*rig)

func newRig(t testing.TB, opts ...rigOpt) *rig {
	t.Helper()
	r := &rig{t: t, dir: t.TempDir(), clk: newFakeClock(), evs: &recorder{}, mach: newFakeMachine(), set: DefaultSettings(), net: NetState{Mode: plan.ModeOnline, Online: true}, roots: map[string]ark.Root{}}
	r.plnr = &fakePlanner{items: map[string]plan.CatalogItem{}}
	r.addRoot(ark.Root{ID: ark.InternalID, Kind: ark.KindInternal, Path: filepath.Join(r.dir, "srv"), State: ark.StateReady})
	for _, o := range opts {
		o(r)
	}
	jdir := filepath.Join(r.dir, "var")
	jr, rec, err := journal.Open(bg, journal.Options{Dir: jdir, Now: r.clk.Now, Logger: quiet})
	if err != nil {
		t.Fatal(err)
	}
	r.jr, r.rec = jr, rec
	r.deps = Deps{
		Journal: jr, Machine: r.mach, Planner: r.plnr, Copies: noCopies{}, FS: config.OSFS{}, JournalDir: jdir,
		Settings: func() Settings { return r.set }, Net: func() NetState { return r.net },
		Power: PowerFunc(func() bool { return r.batt }), Events: r.evs, Clock: r.clk, Logger: quiet,
		Priority:  func(bool) error { return nil },
		Lock:      FileLock{Path: filepath.Join(r.dir, "lock")},
		Retention: nil,
	}
	return r
}

func (r *rig) addRoot(root ark.Root) {
	if root.Path != "" {
		_ = os.MkdirAll(root.Path, 0o755)
	}
	r.roots[root.ID] = root
	r.plnr.roots = append(r.plnr.roots, root)
}

func (r *rig) start() *Engine {
	r.t.Helper()
	e, err := New(r.deps)
	if err != nil {
		r.t.Fatal(err)
	}
	r.eng = e
	return e
}

type noCopies struct{}

func (noCopies) Copies(string) []ark.Copy { return nil }

// modelItem is a catalog model of one pinned file, for the planner's Item lookup.
func modelItem(id string) plan.CatalogItem {
	return plan.CatalogItem{ID: id, Kind: "model", Engine: "llama"}
}

// row is a download row of a plan.
func row(id string, need int64) plan.ItemPlan {
	return plan.ItemPlan{ID: id, Kind: "model", Action: plan.ActionDownload, ToVersion: "v1", NeedBytes: need, FinalBytes: need, Source: plan.SourceNetwork}
}

// setPlans gives the planner one plan per root with the given rows, and the items in the catalog.
func (r *rig) setPlans(rows map[string][]plan.ItemPlan) {
	r.plnr.mu.Lock()
	defer r.plnr.mu.Unlock()
	r.plnr.plans = nil
	ids := make([]string, 0, len(rows))
	for id := range rows {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, rootID := range ids {
		r.plnr.plans = append(r.plnr.plans, plan.RootPlan{Root: rootID, Items: rows[rootID], MinFreeBytes: 1})
		for _, rw := range rows[rootID] {
			r.plnr.items[rw.ID] = modelItem(rw.ID)
		}
	}
}

// waitEnd waits for the run to end and returns its final status.
func (r *rig) waitEnd(id string) RunStatus {
	r.t.Helper()
	if err := r.eng.Wait(bg, id); err != nil {
		r.t.Fatal(err)
	}
	st, ok := r.eng.Status()
	if !ok || st.RunID != id || st.FinishedAt == nil {
		r.t.Fatalf("run %s did not end: %+v", id, st)
	}
	return st
}

func (r *rig) startRun(req StartRequest) string {
	r.t.Helper()
	got, err := r.eng.Start(bg, req)
	if err != nil {
		r.t.Fatalf("Start: %v", err)
	}
	return got.RunID
}

func recvN(t testing.TB, ch chan string, n int) []string {
	t.Helper()
	var out []string
	for i := 0; i < n; i++ {
		select {
		case k := <-ch:
			out = append(out, k)
		case <-time.After(10 * time.Second):
			t.Fatalf("only %d of %d items started: %v", i, n, out)
		}
	}
	return out
}

// expectNone asserts that nothing else started (the channel is empty after the run settled).
func expectNone(t testing.TB, ch chan string) {
	t.Helper()
	select {
	case k := <-ch:
		t.Fatalf("%s started unexpectedly", k)
	default:
	}
}

func itoa(n int) string { return strconv.Itoa(n) }

func writeFile(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o644)
}
