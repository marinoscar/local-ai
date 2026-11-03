package download

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"syscall"
	"testing"
	"time"
)

// epoch is read once per test binary and shared between the served fixtures (Last-Modified, a
// Retry-After date) and the clock the downloader reads, so they can never disagree.
var epoch = time.Date(2026, 11, 2, 18, 4, 11, 0, time.UTC)

// genData returns n deterministic pseudo-random bytes.
func genData(n int, seed uint64) []byte {
	r := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(r.UintN(256))
	}
	return b
}

// sum is the sha256 of data, computed at run time (no hash literal is ever written in the repo).
func sum(data []byte) string {
	s := sha256.Sum256(data)
	return hex.EncodeToString(s[:])
}

// waitFor polls cond until it holds. The generous limit only bounds a failing test; a passing one
// returns as soon as the client has APPLIED the change it waits for.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

// ---------------------------------------------------------------- clock

// fakeClock is the downloader's Clock. After(d) (the backoff waits) advances the time at once when
// auto is set, recording the delay; AfterFunc timers (the stall watchdog) fire only when the time
// is advanced past them.
type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	auto   bool
	sleeps []time.Duration
	timers []*ftimer
}

type ftimer struct {
	c      *fakeClock
	at     time.Time
	f      func()
	active bool
}

func newFakeClock(auto bool) *fakeClock { return &fakeClock{now: epoch, auto: auto} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	c.sleeps = append(c.sleeps, d)
	ch := make(chan time.Time, 1)
	if c.auto {
		fire := c.advanceLocked(d)
		ch <- c.now
		c.mu.Unlock()
		runAll(fire)
		return ch
	}
	tm := &ftimer{c: c, at: c.now.Add(d), active: true}
	tm.f = func() { ch <- c.Now() }
	c.timers = append(c.timers, tm)
	c.mu.Unlock()
	return ch
}

func (c *fakeClock) AfterFunc(d time.Duration, f func()) Timer {
	c.mu.Lock()
	defer c.mu.Unlock()
	tm := &ftimer{c: c, at: c.now.Add(d), f: f, active: true}
	c.timers = append(c.timers, tm)
	return tm
}

func (t *ftimer) Stop() bool {
	t.c.mu.Lock()
	defer t.c.mu.Unlock()
	was := t.active
	t.active = false
	return was
}

func (t *ftimer) Reset(d time.Duration) bool {
	t.c.mu.Lock()
	defer t.c.mu.Unlock()
	was := t.active
	t.active, t.at = true, t.c.now.Add(d)
	return was
}

func (c *fakeClock) advanceLocked(d time.Duration) []func() {
	c.now = c.now.Add(d)
	var fire []func()
	for _, tm := range c.timers {
		if tm.active && !tm.at.After(c.now) {
			tm.active = false
			fire = append(fire, tm.f)
		}
	}
	return fire
}

func runAll(fs []func()) {
	for _, f := range fs {
		f()
	}
}

// advance moves the time forward and fires the timers that came due.
func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	fire := c.advanceLocked(d)
	c.mu.Unlock()
	runAll(fire)
}

// armed reports how many AfterFunc timers are waiting to fire.
func (c *fakeClock) armed() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, tm := range c.timers {
		if tm.active {
			n++
		}
	}
	return n
}

func (c *fakeClock) delays() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]time.Duration(nil), c.sleeps...)
}

// ---------------------------------------------------------------- filesystem

// faultFS is the real filesystem behind fault injection: a byte budget after which writes fail
// (ENOSPC by default, until healed), an I/O error on demand, a kill after N bytes, and counters.
type faultFS struct {
	OSFS
	mu       sync.Mutex
	written  int64 // cumulative bytes written through any .part
	limit    int64 // -1 = unlimited; else the cumulative bytes allowed
	writeErr error // what a write beyond the limit returns (default ENOSPC)
	killAt   int64 // -1 = off; else Goexit once this many cumulative bytes were written
	mkdirErr error
	syncErr  error
	renames  []string
}

func newFaultFS() *faultFS { return &faultFS{limit: -1, killAt: -1, writeErr: syscall.ENOSPC} }

func (f *faultFS) setLimit(n int64, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.limit = n
	if err != nil {
		f.writeErr = err
	}
}

func (f *faultFS) heal() { f.setLimit(-1, nil) }

func (f *faultFS) setKill(n int64) {
	f.mu.Lock()
	f.killAt = n
	f.mu.Unlock()
}

func (f *faultFS) total() int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.written
}

func (f *faultFS) MkdirAll(path string, perm os.FileMode) error {
	f.mu.Lock()
	err := f.mkdirErr
	f.mu.Unlock()
	if err != nil {
		return &os.PathError{Op: "mkdir", Path: path, Err: err}
	}
	return f.OSFS.MkdirAll(path, perm)
}

func (f *faultFS) OpenPart(name string) (PartFile, error) {
	p, err := f.OSFS.OpenPart(name)
	if err != nil {
		return nil, err
	}
	return &faultPart{PartFile: p, fs: f}, nil
}

func (f *faultFS) Rename(a, b string) error {
	f.mu.Lock()
	f.renames = append(f.renames, filepath.Base(b))
	f.mu.Unlock()
	return f.OSFS.Rename(a, b)
}

type faultPart struct {
	PartFile
	fs *faultFS
}

func (p *faultPart) Write(b []byte) (int, error) {
	f := p.fs
	f.mu.Lock()
	allow := int64(len(b))
	var failErr error
	if f.limit >= 0 && f.written+allow > f.limit {
		allow = max(f.limit-f.written, 0)
		failErr = f.writeErr
	}
	kill := false
	if f.killAt >= 0 && f.written+allow >= f.killAt {
		allow = max(f.killAt-f.written, 0)
		kill = true
	}
	f.mu.Unlock()
	n := 0
	if allow > 0 {
		var err error
		n, err = p.PartFile.Write(b[:allow])
		if err != nil {
			return n, err
		}
	}
	f.mu.Lock()
	f.written += int64(n)
	f.mu.Unlock()
	if kill {
		runtime.Goexit() // the process "dies" here: no checkpoint, no cleanup beyond defers
	}
	if failErr != nil {
		return n, &os.PathError{Op: "write", Path: "part", Err: failErr}
	}
	return n, nil
}

func (p *faultPart) Sync() error {
	p.fs.mu.Lock()
	err := p.fs.syncErr
	p.fs.mu.Unlock()
	if err != nil {
		return &os.PathError{Op: "sync", Path: "part", Err: err}
	}
	return p.PartFile.Sync()
}

// ---------------------------------------------------------------- server

type reqRec struct {
	Method, Range, IfRange, Auth, UA, AcceptEnc, Path string
}

// testServer serves data like a real file server (http.ServeContent: Range, If-Range, 416) unless
// noRange, and records every request. handler, when set, may take over a request.
type testServer struct {
	*httptest.Server
	data    []byte
	etag    string
	lastMod time.Time
	noRange bool
	handler func(n int, w http.ResponseWriter, r *http.Request) bool

	mu   sync.Mutex
	reqs []reqRec
}

func newTestServer(t *testing.T, data []byte) *testServer {
	t.Helper()
	s := &testServer{data: data, etag: `"v1"`, lastMod: epoch.Add(-time.Hour)}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.Close)
	return s
}

func (s *testServer) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	n := len(s.reqs)
	s.reqs = append(s.reqs, reqRec{
		Method: r.Method, Range: r.Header.Get("Range"), IfRange: r.Header.Get("If-Range"),
		Auth: r.Header.Get("Authorization"), UA: r.Header.Get("User-Agent"),
		AcceptEnc: r.Header.Get("Accept-Encoding"), Path: r.URL.Path,
	})
	h, etag, noRange, data, lm := s.handler, s.etag, s.noRange, s.data, s.lastMod
	s.mu.Unlock()
	if h != nil && h(n, w, r) {
		return
	}
	if etag != "" {
		w.Header().Set("ETag", etag)
	}
	if noRange {
		w.Header().Set("Last-Modified", lm.UTC().Format(http.TimeFormat))
		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		_, _ = w.Write(data)
		return
	}
	http.ServeContent(w, r, "file.bin", lm, bytes.NewReader(data))
}

func (s *testServer) requests() []reqRec {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]reqRec(nil), s.reqs...)
}

func (s *testServer) count() int { return len(s.requests()) }

func (s *testServer) setData(data []byte, etag string) {
	s.mu.Lock()
	s.data, s.etag = data, etag
	s.mu.Unlock()
}

// ---------------------------------------------------------------- environment

// recBucket records what the downloader draws.
type recBucket struct {
	mu   sync.Mutex
	n    int64
	hook func(total int64) // called after each draw, before the context is checked
}

func (r *recBucket) Wait(ctx context.Context, n int) error {
	r.mu.Lock()
	r.n += int64(n)
	total, hook := r.n, r.hook
	r.mu.Unlock()
	if hook != nil {
		hook(total)
	}
	return ctx.Err()
}

func (r *recBucket) total() int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.n
}

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

// freeProbe is the FreeFunc: a settable amount of free space.
type freeProbe struct {
	mu    sync.Mutex
	free  int64
	total int64
	err   error
}

func (p *freeProbe) get(string) (int64, int64, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.total, p.free, p.err
}

func (p *freeProbe) set(free int64) {
	p.mu.Lock()
	p.free = free
	p.mu.Unlock()
}

// purger is the SpaceMaker: it frees a fixed amount (and runs a hook), recording its calls.
type purger struct {
	mu    sync.Mutex
	calls []int64
	free  int64
	onRun func()
	probe *freeProbe
}

func (p *purger) Purge(_ context.Context, _ string, need int64) (int64, error) {
	p.mu.Lock()
	p.calls = append(p.calls, need)
	p.mu.Unlock()
	if p.onRun != nil {
		p.onRun()
	}
	if p.probe != nil && p.free > 0 {
		p.probe.set(p.free)
	}
	return p.free, nil
}

func (p *purger) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.calls)
}

const (
	testBuf   = 4 << 10
	testCkpt  = 32 << 10
	testSize  = 200 << 10 // not a multiple of the block or the checkpoint step
	testRoot  = "internal"
	testItem  = "item-a"
	testVer   = "v1"
	testName  = "model.bin"
	testToken = "tok-" + "SECRET-not-real"
)

// env is one test's downloader and everything injected into it.
type env struct {
	t      *testing.T
	root   string
	fs     *faultFS
	clk    *fakeClock
	cp     *MemoryCheckpointer
	logs   *syncBuf
	probe  *freeProbe
	bucket *recBucket
	dl     *Downloader
	opts   Options
}

func newEnv(t *testing.T, mod func(*Options)) *env {
	t.Helper()
	e := &env{
		t: t, root: t.TempDir(), fs: newFaultFS(), clk: newFakeClock(true), cp: &MemoryCheckpointer{},
		logs: &syncBuf{}, probe: &freeProbe{free: 1 << 40, total: 1 << 41}, bucket: &recBucket{},
	}
	e.opts = Options{
		FS: e.fs, Clock: e.clk, Free: e.probe.get, Bucket: e.bucket,
		HTTP:   NewHTTPClient(ClientConfig{Version: "9.9.9", Token: func() string { return testToken }, AuthHosts: []string{"127.0.0.1"}}),
		Logger: slog.New(slog.NewTextHandler(e.logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		// jitter factor exactly 1: the delays are the schedule
		Backoff:         Backoff{Rand: func() float64 { return 0.5 }},
		CheckpointEvery: testCkpt, SpaceRecheckEvery: 64 << 10, BufSize: testBuf, ProgressReset: 64 << 10,
	}
	if mod != nil {
		mod(&e.opts)
	}
	e.dl = New(e.opts)
	return e
}

// rebuild creates a new Downloader on the same filesystem, checkpoints and root: "the process
// restarted".
func (e *env) rebuild() { e.dl = New(e.opts) }

func (e *env) job(url string, data []byte) FileJob {
	return FileJob{
		RootID: testRoot, Root: e.root, Item: testItem, Version: testVer, Name: testName, URL: url,
		ExpectedSize: int64(len(data)), ExpectedSHA256: sum(data),
	}
}

func (e *env) partPath() string {
	return FileJob{Root: e.root, Item: testItem, Version: testVer, Name: testName}.PartPath()
}
func (e *env) finalPath() string {
	return FileJob{Root: e.root, Item: testItem, Version: testVer, Name: testName}.FinalPath()
}

func (e *env) readPart() []byte {
	e.t.Helper()
	b, err := os.ReadFile(e.partPath())
	if err != nil {
		e.t.Fatal(err)
	}
	return b
}

// seedPart plants a .part holding data[:partLen] with a checkpoint at ckptOffset (hash state of
// data[:ckptOffset]), as a previous run would have left it.
func (e *env) seedPart(url string, data []byte, partLen, ckptOffset int) {
	e.t.Helper()
	if err := os.MkdirAll(filepath.Dir(e.partPath()), 0o700); err != nil {
		e.t.Fatal(err)
	}
	content := append([]byte(nil), data[:min(partLen, ckptOffset)]...)
	for len(content) < partLen { // bytes past the checkpoint are not trusted: make them wrong
		content = append(content, 0xEE)
	}
	if err := os.WriteFile(e.partPath(), content, 0o600); err != nil {
		e.t.Fatal(err)
	}
	if ckptOffset > 0 {
		h := sha256.New()
		h.Write(data[:ckptOffset])
		st, err := encodeSHA(h)
		if err != nil {
			e.t.Fatal(err)
		}
		_ = e.cp.Save(testName, Checkpoint{Offset: int64(ckptOffset), ShaState: st, ETag: `"v1"`, URL: labelURL(url)})
	}
}

func mustFetch(t *testing.T, e *env, j FileJob) Result {
	t.Helper()
	res, err := e.dl.Fetch(context.Background(), j, e.cp)
	if err != nil {
		t.Fatalf("Fetch: %v\nlogs:\n%s", err, e.logs.String())
	}
	return res
}

func drain(r io.Reader) { _, _ = io.Copy(io.Discard, r) }
