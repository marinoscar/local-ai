package journal_test

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/ark"
	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/config/fake"
	"github.com/marinoscar/local-ai/agent/internal/sync/journal"
	"github.com/marinoscar/local-ai/agent/internal/sync/manifest"
)

// epoch is the one clock reading the tests share between what they feed in and what they expect.
var epoch = time.Date(2026, 11, 2, 18, 4, 11, 0, time.UTC)

// clock is an injectable clock that advances one second per reading, so records written later sort
// later and no test ever sleeps.
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

// sha is a stand-in sha256 (the hash of a name): no hash literal is ever written in the repo.
func sha(name string) string {
	sum := sha256.Sum256([]byte(name))
	return hex.EncodeToString(sum[:])
}

// tfs is the journal.FS of the tests: the real filesystem in a temp dir behind the fault injector.
type tfs struct{ *fake.FaultFS }

func newFS() *tfs { return &tfs{fake.New(config.OSFS{})} }

func (f *tfs) MkdirAll(path string, perm os.FileMode) error { return os.MkdirAll(path, perm) }

var _ journal.FS = (*tfs)(nil)

// counter is a deterministic random source: 0001, 0002, ...
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

func opts(t testing.TB, fsys journal.FS) journal.Options {
	t.Helper()
	return journal.Options{FS: fsys, Dir: filepath.Join(t.TempDir(), "sync"), Now: newClock().Now, Rand: (&counter{}).Read, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

func readFile(t testing.TB, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func writeFile(t testing.TB, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// putModel writes a verified llama model on a root and returns it as a ScannedItem.
func putModel(t testing.TB, root ark.Root, id, version string) ark.ScannedItem {
	t.Helper()
	rel, err := ark.ItemDir(ark.ItemRef{Kind: manifest.KindModel, Engine: ark.EngineLlama, ID: id})
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root.Path, filepath.FromSlash(rel))
	writeFile(t, filepath.Join(dir, id+".gguf"), "weights")
	m := manifest.Manifest{
		SchemaVersion: 1, ItemID: id, ItemKind: manifest.KindModel, Engine: ark.EngineLlama, CatalogVersion: "2026.11.02-1",
		Version: version, SortKey: "2026-11-01", State: manifest.StateVerified, Source: manifest.Source{Type: "url"},
		Files:      []manifest.File{{Kind: "weights", Path: id + ".gguf", SizeBytes: 7, SHA256: sha(id), SHA256Source: "catalog"}},
		TotalBytes: 7, DownloadedAt: epoch, VerifiedAt: epoch, LastReverifiedAt: epoch, Trust: manifest.TrustPinned,
	}
	if err := manifest.Write(config.OSFS{}, manifest.Path(dir), m); err != nil {
		t.Fatal(err)
	}
	return ark.ScannedItem{RootID: root.ID, RelDir: rel, Dir: dir, Manifest: m}
}

func internalRoot(t testing.TB) ark.Root {
	t.Helper()
	return ark.NewInternalRoot(filepath.Join(t.TempDir(), "srv"), "ark-full")
}

func driveRoot(t testing.TB, id string, mountID int) ark.Root {
	t.Helper()
	r, err := ark.NewDriveRoot(ark.DriveInfo{Mount: filepath.Join(t.TempDir(), "mnt"), FSUUID: id[len("ark-"):] + "-0000-0000-0000-000000000000", MountID: mountID}, "")
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func scanOf(fsys config.FS, roots ...ark.Root) journal.ScanFunc {
	return journal.ScanRoots(fsys, func() []ark.Root { return roots })
}
