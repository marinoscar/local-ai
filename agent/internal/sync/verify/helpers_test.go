package verify

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/ark"
	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/sync/journal"
	"github.com/marinoscar/local-ai/agent/internal/sync/manifest"
	"github.com/marinoscar/local-ai/agent/internal/sync/retention"
)

var bg = context.Background()

var epoch = time.Date(2026, 11, 2, 18, 4, 11, 0, time.UTC)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func sha(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// clock is a manual clock the faulty filesystem advances: opening a file for reading costs readCost,
// so the budget tests need no sleeps.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// faultFS is the real filesystem with the faults of a dying disk: reads that cost time, reads that
// fail, a stream with a flipped bit that is not on the disk, a full disk, a root that went read-only.
type faultFS struct {
	OSFS
	clk      *clock
	readCost time.Duration

	mu       sync.Mutex
	opens    map[string]int // OpenRead calls per path
	readErr  map[string]error
	flip     map[string]bool // the stream of this path has a flipped byte
	mkdirErr error
	renameOf map[string]error // Rename of this source path fails
	// cancel is called while the file at midFile is being read (a process killed in the middle of it).
	cancel  context.CancelFunc
	midFile string
}

func newFaultFS(clk *clock) *faultFS {
	return &faultFS{clk: clk, opens: map[string]int{}, readErr: map[string]error{}, flip: map[string]bool{}, renameOf: map[string]error{}}
}

func (f *faultFS) OpenRead(name string) (io.ReadCloser, error) {
	f.mu.Lock()
	f.opens[name]++
	rerr, flip := f.readErr[name], f.flip[name]
	mid := f.midFile == name
	f.mu.Unlock()
	if f.clk != nil && f.readCost > 0 {
		f.clk.Advance(f.readCost)
	}
	rc, err := f.OSFS.OpenRead(name)
	if err != nil {
		return nil, err
	}
	return &faultReader{rc: rc, err: rerr, flip: flip, cancelMid: mid, cancel: f.cancel}, nil
}

func (f *faultFS) opened(name string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.opens[name]
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

func (f *faultFS) Rename(oldpath, newpath string) error {
	f.mu.Lock()
	err := f.renameOf[oldpath]
	f.mu.Unlock()
	if err != nil {
		return &os.LinkError{Op: "rename", Old: oldpath, New: newpath, Err: err}
	}
	return f.OSFS.Rename(oldpath, newpath)
}

type faultReader struct {
	rc        io.ReadCloser
	err       error
	flip      bool
	cancelMid bool
	cancel    context.CancelFunc
	n         int64
}

func (r *faultReader) Read(p []byte) (int, error) {
	if r.err != nil && r.n > 0 {
		return 0, r.err
	}
	n, err := r.rc.Read(p)
	if n > 0 && r.flip && r.n == 0 {
		p[0] ^= 0x01
	}
	r.n += int64(n)
	if r.cancelMid && r.n > 0 && r.cancel != nil {
		r.cancel()
	}
	return n, err
}

func (r *faultReader) Close() error { return r.rc.Close() }

var errEIO = syscall.EIO

// journalFake is the verify.Journal and the retention.Journal view, recording what is written.
type journalFake struct {
	mu    sync.Mutex
	items map[string]journal.Item
}

func newJournalFake() *journalFake { return &journalFake{items: map[string]journal.Item{}} }

func (j *journalFake) Get(root, item string) (journal.Item, bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	i, ok := j.items[journal.Key(root, item)]
	return i, ok
}

func (j *journalFake) Update(root, item string, fn func(*journal.Item)) (journal.Item, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	k := journal.Key(root, item)
	it := j.items[k]
	it.RootID, it.ItemID = root, item
	fn(&it)
	j.items[k] = it
	return it, nil
}

func (j *journalFake) Delete(root, item string) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	delete(j.items, journal.Key(root, item))
	return nil
}

func (j *journalFake) set(root, item string, state journal.ItemState) {
	j.mu.Lock()
	j.items[journal.Key(root, item)] = journal.Item{RootID: root, ItemID: item, State: state}
	j.mu.Unlock()
}

// rig is a node with an internal root and a drive, a real best-copy index and a real retention.
type rig struct {
	t     testing.TB
	roots []ark.Root
	idx   *ark.Index
	jr    *journalFake
	ret   *retention.Retention
	clk   *clock
	fs    *faultFS
	rv    *Reverifier
}

const driveID = "ark-0a0b0c0d"

func newRig(t testing.TB) *rig {
	t.Helper()
	base := t.TempDir()
	clk := &clock{t: epoch}
	r := &rig{t: t, jr: newJournalFake(), clk: clk, fs: newFaultFS(clk)}
	r.roots = []ark.Root{
		{ID: ark.InternalID, Kind: ark.KindInternal, Path: filepath.Join(base, "srv"), State: ark.StateReady},
		{ID: driveID, Kind: ark.KindArkDrive, Path: filepath.Join(base, "drive", "localai"), State: ark.StateReady, MountID: 3},
	}
	for _, ro := range r.roots {
		if err := os.MkdirAll(ro.Path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	r.idx = ark.NewIndex(config.OSFS{})
	ret, err := retention.New(retention.Deps{
		FS: retention.OSFS{}, Roots: r.rootList, Copies: r.idx, Journal: r.jr, Index: r.idx, Items: r.jr, Now: clk.Now, Logger: quiet,
	})
	if err != nil {
		t.Fatal(err)
	}
	r.ret = ret
	r.rv = r.reverifier(ret)
	return r
}

func (r *rig) rootList() []ark.Root { return r.roots }

func (r *rig) reverifier(rs Restorer) *Reverifier {
	rv, err := New(Deps{FS: r.fs, Roots: r.rootList, Index: r.idx, Restore: rs, Journal: r.jr, Now: r.clk.Now, Logger: quiet})
	if err != nil {
		r.t.Fatal(err)
	}
	return rv
}

func (r *rig) root(id string) ark.Root {
	for _, ro := range r.roots {
		if ro.ID == id {
			return ro
		}
	}
	r.t.Fatalf("no root %s", id)
	return ark.Root{}
}

func (r *rig) setRoot(id string, fn func(*ark.Root)) {
	for i := range r.roots {
		if r.roots[i].ID == id {
			fn(&r.roots[i])
			r.rebuild()
			return
		}
	}
	r.t.Fatalf("no root %s", id)
}

func (r *rig) rebuild() {
	if _, err := r.idx.Rebuild(bg, r.roots); err != nil {
		r.t.Fatal(err)
	}
}

// body is the content of the file of an item, so tests can tell versions apart.
func body(item, version, name string) []byte {
	return []byte(strings.Repeat(name+" of "+item+" "+version+" ", 64))
}

// install puts a verified model copy with the given files on a root, last re-verified at lastRe.
func (r *rig) install(rootID, itemID, version string, lastRe time.Time, names ...string) string {
	r.t.Helper()
	if len(names) == 0 {
		names = []string{"w.gguf"}
	}
	dir := filepath.Join(r.root(rootID).Path, "models", itemID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		r.t.Fatal(err)
	}
	m := manifest.Manifest{
		SchemaVersion: 1, ItemID: itemID, ItemKind: "model", Engine: "llama", Version: version, SortKey: version, State: "verified",
		Source: manifest.Source{Type: "huggingface"}, Trust: manifest.TrustPinned,
		DownloadedAt: lastRe, VerifiedAt: lastRe, LastReverifiedAt: lastRe,
	}
	for _, n := range names {
		b := body(itemID, version, n)
		if err := os.WriteFile(filepath.Join(dir, n), b, 0o644); err != nil {
			r.t.Fatal(err)
		}
		m.Files = append(m.Files, manifest.File{Kind: "weights", Path: n, SizeBytes: int64(len(b)), SHA256: sha(b)})
		m.TotalBytes += int64(len(b))
	}
	if err := manifest.Write(config.OSFS{}, manifest.Path(dir), m); err != nil {
		r.t.Fatal(err)
	}
	r.rebuild()
	return dir
}

// installDocs puts a verified directory item (the manuals) on a root.
func (r *rig) installDocs(rootID string, lastRe time.Time, files map[string]string) string {
	r.t.Helper()
	dir := filepath.Join(r.root(rootID).Path, "docs")
	var sums []manifest.Sum
	var total int64
	for rel, content := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			r.t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			r.t.Fatal(err)
		}
		sums = append(sums, manifest.Sum{SHA256: sha([]byte(content)), Path: rel})
		total += int64(len(content))
	}
	if err := manifest.WriteSums(config.OSFS{}, dir, sums); err != nil {
		r.t.Fatal(err)
	}
	tree, err := manifest.TreeSHA256(sums)
	if err != nil {
		r.t.Fatal(err)
	}
	m := manifest.Manifest{
		SchemaVersion: 1, ItemID: "manuals", ItemKind: "docs", Version: "d1", SortKey: "d1", State: "verified",
		Source: manifest.Source{Type: "repo_docs"}, Trust: manifest.TrustComputed,
		TreeSHA256: tree, FileCount: len(files), TotalBytes: total,
		DownloadedAt: lastRe, VerifiedAt: lastRe, LastReverifiedAt: lastRe,
	}
	if err := manifest.Write(config.OSFS{}, manifest.Path(dir), m); err != nil {
		r.t.Fatal(err)
	}
	r.rebuild()
	return dir
}

// trash puts a superseded version in .trash/<item>/<version>/ with its manifest.
func (r *rig) trash(rootID, itemID, version string, verifiedAt time.Time) string {
	r.t.Helper()
	dir := filepath.Join(r.root(rootID).Path, ".trash", itemID, version)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		r.t.Fatal(err)
	}
	b := body(itemID, version, "w.gguf")
	if err := os.WriteFile(filepath.Join(dir, "w.gguf"), b, 0o644); err != nil {
		r.t.Fatal(err)
	}
	m := manifest.Manifest{
		SchemaVersion: 1, ItemID: itemID, ItemKind: "model", Engine: "llama", Version: version, SortKey: version, State: "verified",
		Source: manifest.Source{Type: "huggingface"}, Trust: manifest.TrustPinned,
		Files:        []manifest.File{{Kind: "weights", Path: "w.gguf", SizeBytes: int64(len(b)), SHA256: sha(b)}},
		TotalBytes:   int64(len(b)),
		DownloadedAt: verifiedAt, VerifiedAt: verifiedAt, LastReverifiedAt: verifiedAt,
	}
	if err := manifest.Write(config.OSFS{}, manifest.Path(dir), m); err != nil {
		r.t.Fatal(err)
	}
	return dir
}

// rot flips one byte of a file in place: same size, so the cheap load check still passes and only a
// re-hash notices.
func rot(t testing.TB, path string) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	b[1000%len(b)] ^= 0xFF
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func (r *rig) lastReverified(rootID, itemID string) time.Time {
	r.t.Helper()
	for _, c := range r.idx.Copies(itemID) {
		if c.RootID == rootID {
			return c.Manifest.LastReverifiedAt
		}
	}
	r.t.Fatalf("no copy of %s on %s", itemID, rootID)
	return time.Time{}
}

func (r *rig) onDisk(rootID, itemID string) manifest.Manifest {
	r.t.Helper()
	m, err := manifest.Read(config.OSFS{}, manifest.Path(filepath.Join(r.root(rootID).Path, "models", itemID)))
	if err != nil {
		r.t.Fatal(err)
	}
	return m
}

func pathExists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

// snapshot lists every path under dir with the size and a hash of the content of every file, so a test
// can prove a pass wrote nothing.
func snapshot(t testing.TB, dir string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		if d.IsDir() {
			out = append(out, rel+"/")
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		out = append(out, rel+" "+sha(b))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(out)
	return out
}

func equal(a, b []string) bool {
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
