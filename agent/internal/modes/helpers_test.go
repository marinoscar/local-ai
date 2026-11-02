package modes_test

import (
	"bytes"
	"context"
	"log/slog"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	cfake "github.com/marinoscar/local-ai/agent/internal/config/fake"
	"github.com/marinoscar/local-ai/agent/internal/modes"
	"github.com/marinoscar/local-ai/agent/internal/modes/fake"
	"github.com/marinoscar/local-ai/agent/internal/state"
	statefake "github.com/marinoscar/local-ai/agent/internal/state/fake"
)

const statePath = "/var/lib/localai/state.json"

var t0 = time.Date(2026, 11, 2, 15, 0, 0, 0, time.UTC)

var allModes = []modes.Mode{modes.Online, modes.Offline, modes.Survival}

// syncBuf is a goroutine-safe log sink.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// openStore opens the state file over fs with the fake clock.
func openStore(t testing.TB, fs *statefake.MemFS, clk *cfake.Clock) *state.Store {
	t.Helper()
	st, err := state.Open(statePath, state.Options{FS: fs, AgentVersion: "test", Now: clk.Now})
	if err != nil {
		t.Fatal(err)
	}
	return st
}

type rigCfg struct {
	mode     state.Mode
	seed     func(*state.State)
	wrap     func(*state.Store) modes.StateStore
	opts     modes.Options
	deps     func(*modes.Deps)
	skipOpen bool
	noSeed   bool
	fs       *statefake.MemFS
}

func withMode(m state.Mode) func(*rigCfg)         { return func(c *rigCfg) { c.mode = m } }
func withSeed(f func(*state.State)) func(*rigCfg) { return func(c *rigCfg) { c.seed = f } }
func withDeps(f func(*modes.Deps)) func(*rigCfg)  { return func(c *rigCfg) { c.deps = f } }
func notOpen() func(*rigCfg)                      { return func(c *rigCfg) { c.skipOpen = true } }

// noSeed keeps the state file exactly as it is on the filesystem (a restart).
func noSeed() func(*rigCfg) { return func(c *rigCfg) { c.noSeed = true } }

// withFS runs the rig over an existing filesystem.
func withFS(fs *statefake.MemFS) func(*rigCfg) { return func(c *rigCfg) { c.fs = fs } }
func withWrap(f func(*state.Store) modes.StateStore) func(*rigCfg) {
	return func(c *rigCfg) { c.wrap = f }
}

// rig is an engine over a real state store on an in-memory filesystem, a manual clock and the fakes.
type rig struct {
	t    *testing.T
	fs   *statefake.MemFS
	st   *state.Store
	clk  *cfake.Clock
	env  *fake.Env
	eng  *modes.Engine
	logs *syncBuf
	// abandon marks an engine that was "killed": it is not shut down at the end of the test.
	abandon bool
}

func newRig(t *testing.T, mods ...func(*rigCfg)) *rig {
	t.Helper()
	cfg := &rigCfg{mode: modes.Online, fs: statefake.New()}
	for _, m := range mods {
		m(cfg)
	}
	clk := cfake.NewClock(t0)
	st := openStore(t, cfg.fs, clk)
	if !cfg.noSeed {
		if err := st.MutateSync(func(s *state.State) error {
			s.Mode = cfg.mode
			if cfg.seed != nil {
				cfg.seed(s)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	env := fake.NewEnv()
	var store modes.StateStore = st
	if cfg.wrap != nil {
		store = cfg.wrap(st)
	}
	deps := env.Deps(store)
	if cfg.deps != nil {
		cfg.deps(&deps)
	}
	logs := &syncBuf{}
	cfg.opts.Clock = clk
	cfg.opts.Logger = slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	eng, err := modes.New(deps, cfg.opts)
	if err != nil {
		t.Fatal(err)
	}
	r := &rig{t: t, fs: cfg.fs, st: st, clk: clk, env: env, eng: eng, logs: logs}
	t.Cleanup(func() {
		if !r.abandon {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_ = eng.Shutdown(ctx)
		}
		_ = st.Close()
	})
	if !cfg.skipOpen {
		if _, err := eng.Open(); err != nil {
			t.Fatal(err)
		}
	}
	return r
}

var operator = state.Actor{Kind: "operator", Via: "socket"}

// request asks for target; with wait it blocks until the transition ends.
func (r *rig) request(target modes.Mode, wait bool) (modes.Result, error) {
	r.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	return r.eng.Request(ctx, target, modes.RequestOptions{Wait: wait, Actor: operator})
}

func (r *rig) mustRequest(target modes.Mode, wait bool) modes.Result {
	r.t.Helper()
	res, err := r.request(target, wait)
	if err != nil {
		r.t.Fatalf("Request(%s): %v", target, err)
	}
	return res
}

func (r *rig) idle() {
	r.t.Helper()
	waitIdle(r.t, r.eng)
}

func waitIdle(t testing.TB, e *modes.Engine) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := e.WaitIdle(ctx); err != nil {
		t.Fatalf("engine did not become idle: %v", err)
	}
}

// waitFor polls cond for up to a minute: the deadline only bounds a hung test, it never paces a passing one.
func waitFor(t testing.TB, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		runtime.Gosched()
		time.Sleep(time.Millisecond)
	}
}

// blocker is a fake hook that parks the call until released (or its ctx ends) and tells the test it
// got there.
type blocker struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func newBlocker() *blocker {
	return &blocker{entered: make(chan struct{}, 16), release: make(chan struct{})}
}

func (b *blocker) hook(ctx context.Context) error {
	b.entered <- struct{}{}
	select {
	case <-b.release:
	case <-ctx.Done():
	}
	return nil
}

func (b *blocker) open() { b.once.Do(func() { close(b.release) }) }

func (b *blocker) waitEntered(t testing.TB) {
	t.Helper()
	select {
	case <-b.entered:
	case <-time.After(60 * time.Second):
		t.Fatal("the step never started")
	}
}

// killStore wraps a store and kills the calling goroutine (runtime.Goexit, the in-process stand-in
// for kill -9: nothing after it runs) at the k-th MutateSync, before or after the write reached disk.
type killStore struct {
	*state.Store
	k       int
	before  bool
	n       atomic.Int32
	crashed chan struct{}
	once    sync.Once
}

func newKillStore(st *state.Store, k int, before bool) *killStore {
	return &killStore{Store: st, k: k, before: before, crashed: make(chan struct{})}
}

func (s *killStore) writes() int { return int(s.n.Load()) }

func (s *killStore) MutateSync(fn func(*state.State) error) error {
	n := int(s.n.Add(1))
	if s.k > 0 && n == s.k && s.before {
		s.die()
	}
	err := s.Store.MutateSync(fn)
	if s.k > 0 && n == s.k && !s.before {
		s.die()
	}
	return err
}

func (s *killStore) die() {
	s.once.Do(func() { close(s.crashed) })
	runtime.Goexit()
}

// recStore records the state after every MutateSync, to check what a reader could ever see on disk.
type recStore struct {
	*state.Store
	mu    sync.Mutex
	snaps []state.State
}

func (s *recStore) MutateSync(fn func(*state.State) error) error {
	err := s.Store.MutateSync(fn)
	snap := s.Store.Snapshot()
	s.mu.Lock()
	s.snaps = append(s.snaps, snap)
	s.mu.Unlock()
	return err
}

func (s *recStore) snapshots() []state.State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]state.State(nil), s.snaps...)
}

// effectsOf is the list of fake calls a step makes with every dependency wired and healthy.
func effectsOf(step string) []string {
	switch step {
	case modes.StepLinkStop:
		return []string{"link.stop"}
	case modes.StepSyncCancel:
		return []string{"sync.cancel"}
	case modes.StepProberStop:
		return []string{"prober.stop"}
	case modes.StepTailscaleOff:
		return []string{"tailscale.off"}
	case modes.StepSurvivalModels:
		return []string{"models.load"}
	case modes.StepSurvivalService:
		return []string{"service.start kiwix", "service.start whisper"}
	case modes.StepTailscaleOn:
		return []string{"tailscale.on"}
	case modes.StepLinkStart:
		return []string{"link.start"}
	case modes.StepProberStart:
		return []string{"prober.start"}
	case modes.StepSyncCatchup:
		return []string{"sync.catchup 5m0s"}
	}
	return nil
}

// effectsFrom is the calls of the steps of target from the step at index i on.
func effectsFrom(target modes.Mode, i int) []string {
	var out []string
	for _, s := range modes.Steps(target)[i:] {
		out = append(out, effectsOf(s)...)
	}
	return out
}

func indexOf(list []string, s string) int {
	for i, v := range list {
		if v == s {
			return i
		}
	}
	return -1
}

func equalStrings(a, b []string) bool {
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

func codesOf(ws []modes.Warning) []string {
	var out []string
	for _, w := range ws {
		out = append(out, w.Code)
	}
	return out
}

func hasCode(ws []modes.Warning, code string) bool {
	for _, w := range ws {
		if w.Code == code {
			return true
		}
	}
	return false
}

func joined(s []string) string { return strings.Join(s, ", ") }

var _ = fake.Joined

func newFS() *statefake.MemFS { return statefake.New() }

// without returns calls minus every entry equal to drop.
func without(calls []string, drop string) []string {
	var out []string
	for _, c := range calls {
		if c != drop {
			out = append(out, c)
		}
	}
	return out
}
