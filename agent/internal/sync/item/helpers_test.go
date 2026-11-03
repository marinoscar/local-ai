package item

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/ark"
	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/sync/download"
	"github.com/marinoscar/local-ai/agent/internal/sync/journal"
	"github.com/marinoscar/local-ai/agent/internal/sync/manifest"
	"github.com/marinoscar/local-ai/agent/internal/sync/retention"
)

var bg = context.Background()

// epoch is the one clock reading the tests share between what they feed in and what they expect.
var epoch = time.Date(2026, 11, 2, 18, 4, 11, 0, time.UTC)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *clock { return &clock{t: epoch} }

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(time.Second)
	return c.t
}

// genData is deterministic content of n bytes: no hash literal is ever written in the repo.
func genData(n int, seed int64) []byte {
	b := make([]byte, n)
	rand.New(rand.NewSource(seed)).Read(b)
	return b
}

func sha(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

var errCrash = errors.New("test: the process was killed here")

// crashFS is the real filesystem behind an operation counter: every mutating operation (and every
// write, sync and chmod of a file) is one numbered step. With limit > 0 the "process" dies at step
// limit, either before the operation happens (after == false) or right after it happened
// (after == true); from then on every call of the filesystem fails, reads and removals included, so
// nothing lands after the kill, exactly as after kill -9. It implements the filesystem of the item
// machine, the journal and retention, so one counter covers everything that writes.
type crashFS struct {
	base  OSFS
	strip string
	// journalDir is the journal's directory: its internals are not numbered (the journal has its own
	// fault tests); only the visible commit of an items.json update (the rename) and the append of a
	// run-log line (the write) are steps, so a kill lands between a journal event and its side effect.
	journalDir string

	mu         sync.Mutex
	n          int
	limit      int
	after      bool
	pendingDie bool
	dead       bool
	ops        []string
}

func newCrashFS(strip string) *crashFS { return &crashFS{strip: strip} }

func (c *crashFS) rel(p string) string {
	if c.journalDir != "" && (p == c.journalDir || strings.HasPrefix(p, c.journalDir+"/")) {
		return p // kept whole: tick recognises the journal by its directory
	}
	return strings.TrimPrefix(p, c.strip)
}

// arm makes the process die at the k-th numbered step from now.
func (c *crashFS) arm(k int, after bool) {
	c.mu.Lock()
	c.limit, c.after = c.n+k, after
	c.mu.Unlock()
}

func (c *crashFS) isDead() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.dead
}

func (c *crashFS) alive() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.dead {
		return errCrash
	}
	return nil
}

// tick numbers one mutating operation. A nil result means the caller performs it (and must call
// done afterwards).
func (c *crashFS) tick(desc string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.dead {
		return errCrash
	}
	if c.journalDir != "" && strings.Contains(desc, c.journalDir) {
		// a journal internal: counted only when it is the commit of items.json or a run-log append
		isCommit := strings.HasPrefix(desc, "rename ") && strings.Contains(desc, "-> "+c.journalDir+"/items.json") && !strings.Contains(desc, ".bak")
		isAppend := strings.HasPrefix(desc, "write ") && strings.Contains(desc, c.journalDir+"/runs/")
		if !isCommit && !isAppend {
			return nil
		}
	}
	c.n++
	c.ops = append(c.ops, desc)
	if c.limit > 0 && c.n == c.limit {
		if !c.after {
			c.dead = true
			return errCrash
		}
		c.pendingDie = true
	}
	return nil
}

func (c *crashFS) done() {
	c.mu.Lock()
	if c.pendingDie {
		c.dead, c.pendingDie = true, false
	}
	c.mu.Unlock()
}

func (c *crashFS) steps() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n
}

func (c *crashFS) opLog() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.ops...)
}

type crashFile struct {
	config.File
	c    *crashFS
	path string
}

func (f *crashFile) Write(p []byte) (int, error) {
	if err := f.c.tick("write " + f.c.rel(f.path)); err != nil {
		return 0, err
	}
	defer f.c.done()
	return f.File.Write(p)
}

func (f *crashFile) Sync() error {
	if err := f.c.tick("sync " + f.c.rel(f.path)); err != nil {
		return err
	}
	defer f.c.done()
	return f.File.Sync()
}

func (f *crashFile) Chmod(m os.FileMode) error {
	if err := f.c.tick("chmod " + f.c.rel(f.path)); err != nil {
		return err
	}
	defer f.c.done()
	return f.File.Chmod(m)
}

func (f *crashFile) Chown(uid, gid int) error {
	if err := f.c.tick("chown " + f.c.rel(f.path)); err != nil {
		return err
	}
	defer f.c.done()
	return f.File.Chown(uid, gid)
}

func (f *crashFile) Close() error { return f.File.Close() }

func (c *crashFS) OpenFile(name string, flag int, perm os.FileMode) (config.File, error) {
	if flag&(os.O_WRONLY|os.O_RDWR|os.O_CREATE|os.O_APPEND|os.O_TRUNC) != 0 {
		if err := c.tick("open " + c.rel(name)); err != nil {
			return nil, err
		}
		defer c.done()
	} else if err := c.alive(); err != nil {
		return nil, err
	}
	f, err := c.base.OpenFile(name, flag, perm)
	if err != nil {
		return nil, err
	}
	return &crashFile{File: f, c: c, path: name}, nil
}

func (c *crashFS) ReadFile(name string) ([]byte, error) {
	if err := c.alive(); err != nil {
		return nil, err
	}
	return c.base.ReadFile(name)
}

func (c *crashFS) Stat(name string) (os.FileInfo, error) {
	if err := c.alive(); err != nil {
		return nil, err
	}
	return c.base.Stat(name)
}

func (c *crashFS) Lstat(name string) (os.FileInfo, error) {
	if err := c.alive(); err != nil {
		return nil, err
	}
	return c.base.Lstat(name)
}

func (c *crashFS) ReadDir(name string) ([]os.DirEntry, error) {
	if err := c.alive(); err != nil {
		return nil, err
	}
	return c.base.ReadDir(name)
}

func (c *crashFS) OpenRead(name string) (io.ReadCloser, error) {
	if err := c.alive(); err != nil {
		return nil, err
	}
	return c.base.OpenRead(name)
}

func (c *crashFS) DropCache(name string) error {
	if err := c.alive(); err != nil {
		return err
	}
	return c.base.DropCache(name)
}

func (c *crashFS) Rename(oldpath, newpath string) error {
	if err := c.tick("rename " + c.rel(oldpath) + " -> " + c.rel(newpath)); err != nil {
		return err
	}
	defer c.done()
	return c.base.Rename(oldpath, newpath)
}

func (c *crashFS) Exchange(oldpath, newpath string) error {
	if err := c.tick("exchange " + c.rel(oldpath) + " <-> " + c.rel(newpath)); err != nil {
		return err
	}
	defer c.done()
	return c.base.Exchange(oldpath, newpath)
}

func (c *crashFS) Remove(name string) error {
	if err := c.tick("remove " + c.rel(name)); err != nil {
		return err
	}
	defer c.done()
	return c.base.Remove(name)
}

func (c *crashFS) RemoveAll(name string) error {
	if err := c.tick("removeall " + c.rel(name)); err != nil {
		return err
	}
	defer c.done()
	return os.RemoveAll(name)
}

func (c *crashFS) Chmod(name string, mode os.FileMode) error {
	if err := c.tick("chmod " + c.rel(name)); err != nil {
		return err
	}
	defer c.done()
	return c.base.Chmod(name, mode)
}

func (c *crashFS) Chown(name string, uid, gid int) error {
	if err := c.tick("chown " + c.rel(name)); err != nil {
		return err
	}
	defer c.done()
	return c.base.Chown(name, uid, gid)
}

func (c *crashFS) SyncDir(dir string) error {
	if err := c.tick("syncdir " + c.rel(dir)); err != nil {
		return err
	}
	defer c.done()
	return c.base.SyncDir(dir)
}

func (c *crashFS) MkdirAll(path string, perm os.FileMode) error {
	if err := c.tick("mkdir " + c.rel(path)); err != nil {
		return err
	}
	defer c.done()
	return c.base.MkdirAll(path, perm)
}

var (
	_ FS           = (*crashFS)(nil)
	_ journal.FS   = (*crashFS)(nil)
	_ retention.FS = (*crashFS)(nil)
)

// fakeFetcher is the downloader of the tests: it writes the .part from a table of contents, checks the
// expected digest like the real one, and records every call. Hooks let a test fail, cancel or corrupt.
type fakeFetcher struct {
	mu sync.Mutex
	// data is the content by file name (the FileJob.Name).
	data map[string][]byte
	// fetched and committed record the file names, in order.
	fetched, committed []string
	// err makes Fetch of a name fail with the error (after writing nothing).
	err map[string]error
	// onFetch runs before a Fetch writes anything; its error is returned as is.
	onFetch func(name string) error
	// corrupt flips a byte of the .part after it was written and "hashed": the digest it reports is
	// the digest of the clean data, as when the disk returns other bytes than were written.
	corrupt map[string]bool
	// resumedFrom is reported as Result.ResumedFrom.
	resumedFrom map[string]int64
	// ensureErr and purged drive EnsureSpace.
	ensureErr error
	purged    int64
	// progress is the progress callback, called once with the full size.
	progress bool
}

func newFetcher(data map[string][]byte) *fakeFetcher {
	return &fakeFetcher{data: data, err: map[string]error{}, corrupt: map[string]bool{}, resumedFrom: map[string]int64{}}
}

func (f *fakeFetcher) Fetch(ctx context.Context, j download.FileJob, cp download.Checkpointer) (download.Result, error) {
	f.mu.Lock()
	f.fetched = append(f.fetched, j.Name)
	hook, ferr := f.onFetch, f.err[j.Name]
	data := f.data[j.Name]
	f.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return download.Result{}, err
	}
	if hook != nil {
		if err := hook(j.Name); err != nil {
			return download.Result{}, err
		}
	}
	if ferr != nil {
		return download.Result{}, ferr
	}
	f.mu.Lock()
	corrupt, resumed := f.corrupt[j.Name], f.resumedFrom[j.Name]
	f.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(j.PartPath()), 0o700); err != nil {
		return download.Result{}, err
	}
	out := append([]byte(nil), data...)
	sum := sha(data)
	if corrupt && len(out) > 0 {
		out[len(out)/2] ^= 0xff
	}
	if err := os.WriteFile(j.PartPath(), out, 0o600); err != nil {
		return download.Result{}, err
	}
	res := download.Result{SHA256: sum, Bytes: int64(len(data)), Part: j.PartPath(), ResumedFrom: resumed, Attempts: 1}
	if j.ExpectedSHA256 != "" && sum != j.ExpectedSHA256 {
		res.Mismatch, res.Code = true, download.CodeChecksum
		return res, &download.Error{Kind: download.KindMismatch, Code: download.CodeChecksum, Permanent: true, Err: errors.New("digest mismatch")}
	}
	if j.OnProgress != nil {
		j.OnProgress(int64(len(data)))
	}
	if cp != nil {
		_ = cp.Save(j.Name, download.Checkpoint{Offset: int64(len(data)), ShaState: "x", URL: j.URL})
	}
	return res, nil
}

func (f *fakeFetcher) Commit(j download.FileJob, cp download.Checkpointer) (string, error) {
	f.mu.Lock()
	f.committed = append(f.committed, j.Name)
	f.mu.Unlock()
	if err := os.Rename(j.PartPath(), j.FinalPath()); err != nil {
		return "", err
	}
	if cp != nil {
		_ = cp.Clear(j.Name)
	}
	return j.FinalPath(), nil
}

func (f *fakeFetcher) EnsureSpace(ctx context.Context, rootID, rootPath string, need, minFree int64) (int64, error) {
	return f.purged, f.ensureErr
}

func (f *fakeFetcher) fetchCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.fetched)
}

// env is one temp node: a root, a journal, an index, retention, a fetcher and a machine.
type env struct {
	t       testing.TB
	base    string
	root    ark.Root
	jdir    string
	fsys    *crashFS
	jr      *journal.Journal
	rec     journal.Recovery
	run     *journal.Run
	idx     *ark.Index
	ret     *retention.Retention
	fetch   *fakeFetcher
	m       *Machine
	clk     *clock
	vals    *Validators
	reread  bool
	gate    Gate
	cancels []func()
}

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

// newEnv builds a node in a fresh temp dir.
func newEnv(t testing.TB) *env {
	t.Helper()
	e := bareEnv(t, t.TempDir())
	if err := os.MkdirAll(e.root.Path, 0o755); err != nil {
		t.Fatal(err)
	}
	e.open(newCrashFS(e.base), nil)
	return e
}

var (
	baselineMu   sync.Mutex
	baselineDirs []string
)

// baselineEnv is a node whose directory outlives the test that made it: scenarios build it once and
// clone it for every trial. TestMain removes the directories.
func baselineEnv(t testing.TB) *env {
	t.Helper()
	dir, err := os.MkdirTemp("", "item-baseline-")
	if err != nil {
		t.Fatal(err)
	}
	baselineMu.Lock()
	baselineDirs = append(baselineDirs, dir)
	baselineMu.Unlock()
	e := bareEnv(t, dir)
	if err := os.MkdirAll(e.root.Path, 0o755); err != nil {
		t.Fatal(err)
	}
	e.open(newCrashFS(e.base), nil)
	return e
}

func TestMain(m *testing.M) {
	code := m.Run()
	for _, d := range baselineDirs {
		_ = os.RemoveAll(d)
	}
	os.Exit(code)
}

// bareEnv is an env over base that has not been opened yet.
func bareEnv(t testing.TB, base string) *env {
	e := &env{t: t, base: base, reread: true, clk: newClock(), vals: NewValidators()}
	e.root = ark.Root{ID: ark.InternalID, Kind: ark.KindInternal, Path: filepath.Join(base, "srv"), Profile: "profile", State: ark.StateReady}
	e.jdir = filepath.Join(base, "var")
	return e
}

// cloneEnv is a copy of a quiet node: the disk of src (a root and a journal) in a new temp dir, opened
// on a counting filesystem. It makes the many trials of the kill test cheap (no install each time).
func cloneEnv(t testing.TB, src *env) *env {
	t.Helper()
	dst := t.TempDir()
	if err := os.CopyFS(dst, os.DirFS(src.base)); err != nil {
		t.Fatal(err)
	}
	e := bareEnv(t, dst)
	e.fetch = newFetcher(map[string][]byte{})
	for k, v := range src.fetch.data {
		e.fetch.data[k] = v
	}
	e.clk = &clock{t: src.clk.t}
	e.open(newCrashFS(e.base), e.fetch)
	return e
}

// open (re)builds everything over the disk as it is, with fsys as the filesystem: a restart of the agent.
func (e *env) open(fsys *crashFS, fetch *fakeFetcher) {
	e.t.Helper()
	e.fsys = fsys
	fsys.journalDir = e.jdir
	if fetch == nil {
		fetch = newFetcher(map[string][]byte{})
		if e.fetch != nil {
			fetch.data = e.fetch.data
		}
	}
	e.fetch = fetch
	jopts := journal.Options{FS: fsys, Dir: e.jdir, Now: e.clk.Now, Logger: quiet, Rand: (&counter{}).Read,
		Scan: journal.ScanRoots(fsys, func() []ark.Root { return []ark.Root{e.root} })}
	jr, rec, err := journal.Open(bg, jopts)
	if err != nil {
		e.t.Fatalf("journal.Open: %v", err)
	}
	e.jr, e.rec = jr, rec
	e.idx = ark.NewIndex(fsys)
	if _, err := e.idx.Rebuild(bg, []ark.Root{e.root}); err != nil {
		e.t.Fatal(err)
	}
	ret, err := retention.New(retention.Deps{
		FS: fsys, Roots: func() []ark.Root { return []ark.Root{e.root} }, Copies: e.idx, Journal: jr.Items, Index: e.idx, Items: jr.Items,
		Now: e.clk.Now, Logger: quiet,
	})
	if err != nil {
		e.t.Fatal(err)
	}
	e.ret = ret
	m, err := New(Deps{
		FS: fsys, Journal: jr, Fetcher: e.fetch, Index: e.idx, Remover: ret, Roots: func() []ark.Root { return []ark.Root{e.root} },
		Validators: e.vals, VerifyReread: func() bool { return e.reread }, Now: e.clk.Now, NodeID: "ai-test", AgentVersion: "0.0.0-test", Logger: quiet,
	})
	if err != nil {
		e.t.Fatal(err)
	}
	e.m = m
	e.run = nil
}

type counter struct {
	mu sync.Mutex
	n  uint16
}

func (c *counter) Read(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.n++
	p[0], p[1] = byte(c.n>>8), byte(c.n)
	return 2, nil
}

func (e *env) startRun() *journal.Run {
	e.t.Helper()
	run, err := e.jr.Runs.Start()
	if err != nil {
		e.t.Fatalf("Runs.Start: %v", err)
	}
	e.run = run
	return run
}

// content is what a version consists of: file name -> bytes.
type content map[string][]byte

// files returns the sorted File list of a content with the catalog digests pinned.
func (c content) files(t testing.TB, pin bool) []File {
	var out []File
	for _, name := range sortedKeys(c) {
		f := File{Path: name, Kind: "weights", URL: "https://example.test/" + name, Size: int64(len(c[name]))}
		if pin {
			f.SHA256, f.SHA256Source = sha(c[name]), "catalog"
		}
		out = append(out, f)
	}
	return out
}

func sortedKeys(c map[string][]byte) []string {
	var ks []string
	for k := range c {
		ks = append(ks, k)
	}
	for i := 1; i < len(ks); i++ {
		for j := i; j > 0 && ks[j] < ks[j-1]; j-- {
			ks[j], ks[j-1] = ks[j-1], ks[j]
		}
	}
	return ks
}

// fileJob is a model item "qwen-x" at version ver with the given content.
func (e *env) fileJob(ver string, c content, pin bool) Job {
	for name, data := range c {
		e.fetch.data[name] = data
	}
	return Job{
		Root: e.root, Ref: ark.ItemRef{Kind: manifest.KindModel, Engine: ark.EngineLlama, ID: "qwen-x"}, RelDir: "models/qwen-x",
		CatalogVersion: "2026.11.02-1", MinFree: 1,
		Resolution: &Resolution{
			Version: ver, SortKey: ver, Source: manifest.Source{Type: "huggingface"}, Trust: manifest.TrustPinned,
			Files: c.files(e.t, pin), Resolver: "test",
		},
	}
}

// dirJob is a directory item "docs" at version ver whose tree comes from Build.
func (e *env) dirJob(ver string, c content) Job {
	return Job{
		Root: e.root, Ref: ark.ItemRef{Kind: manifest.KindDocs, ID: "docs"}, RelDir: "docs", CatalogVersion: "2026.11.02-1", MinFree: 1,
		Resolution: &Resolution{
			Version: ver, SortKey: ver, Source: manifest.Source{Type: "repo_docs"}, Trust: manifest.TrustComputed, Dir: true, Resolver: "test",
			Build: func(ctx context.Context, tree string) error {
				for _, name := range sortedKeys(c) {
					p := filepath.Join(tree, filepath.FromSlash(name))
					if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
						return err
					}
					if err := os.WriteFile(p, c[name], 0o600); err != nil {
						return err
					}
				}
				return nil
			},
		},
	}
}

// install runs a job to completion on a quiet filesystem and returns the result.
func (e *env) install(job Job) Result {
	e.t.Helper()
	run := e.startRun()
	res := e.m.Run(bg, run, job)
	if res.State != journal.StateVerified {
		e.t.Fatalf("install of %s: %+v", job.Ref.ID, res)
	}
	return res
}

// states reads the run log and returns the state transitions of one item, in order.
func (e *env) states(runID, itemID string) []journal.ItemState {
	e.t.Helper()
	log, err := e.jr.Runs.Read(runID)
	if err != nil {
		e.t.Fatal(err)
	}
	var out []journal.ItemState
	for _, ev := range log.Events {
		if ev.Item == itemID && ev.Ev == journal.EvState {
			out = append(out, ev.To)
		}
	}
	return out
}

func (e *env) events(runID, itemID string) []string {
	e.t.Helper()
	log, err := e.jr.Runs.Read(runID)
	if err != nil {
		e.t.Fatal(err)
	}
	var out []string
	for _, ev := range log.Events {
		if ev.Item != itemID {
			continue
		}
		if ev.Ev == journal.EvState {
			out = append(out, string(ev.To))
		} else if ev.Ev != journal.EvCkpt {
			out = append(out, ev.Ev)
		}
	}
	return out
}

func (e *env) itemDir() string { return filepath.Join(e.root.Path, "models", "qwen-x") }
func (e *env) docsDir() string { return filepath.Join(e.root.Path, "docs") }

// loadItem is what a reader of the root sees in dir: the manifest and its files, as ark.LoadItem does.
func (e *env) loadItem(rel string) (manifest.Manifest, error) {
	it, err := ark.LoadItem(OSFS{}, e.root, rel)
	return it.Manifest, err
}

func readFile(t testing.TB, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func pathExists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

// assertContent checks that dir holds exactly the files of c with their bytes.
func assertContent(t testing.TB, dir string, c content) {
	t.Helper()
	for name, data := range c {
		got, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(name)))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if string(got) != string(data) {
			t.Fatalf("%s has other bytes than expected", name)
		}
	}
}
