package kiwix

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/ark"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/execx"
	"github.com/marinoscar/local-ai/agent/internal/sync/manifest"
)

// fakeIndex is a Finder over a fixed list of copies.
type fakeIndex struct{ copies map[string][]ark.Copy }

func (f *fakeIndex) ItemIDs() []string {
	var ids []string
	for id := range f.copies {
		ids = append(ids, id)
	}
	return ids
}
func (f *fakeIndex) Copies(id string) []ark.Copy { return f.copies[id] }

// manageRunner acts like kiwix-manage: it writes one <book> per path into the library file; a path
// containing "damaged" fails the run. It records every call.
type manageRunner struct {
	mu    sync.Mutex
	calls [][]string
	creds []*execx.Credential
	fail  string // when set, every run exits 1 with this stderr
	count atomic.Int32
}

func (m *manageRunner) Run(_ context.Context, c execx.Cmd) (execx.Result, error) {
	m.count.Add(1)
	m.mu.Lock()
	m.calls = append(m.calls, c.Args)
	m.creds = append(m.creds, c.Credential)
	m.mu.Unlock()
	if m.fail != "" {
		return execx.Result{ExitCode: 1, Stderr: []byte(m.fail)}, nil
	}
	var b strings.Builder
	b.WriteString("<?xml version=\"1.0\"?>\n<library version=\"20110515\">\n")
	for _, p := range c.Args[2:] {
		if strings.Contains(p, "damaged") {
			return execx.Result{ExitCode: 1, Stderr: []byte("cannot open " + p)}, nil
		}
		b.WriteString(` <book id="` + filepath.Base(p) + `" path="` + p + `"/>` + "\n")
	}
	b.WriteString("</library>\n")
	if err := os.WriteFile(c.Args[0], []byte(b.String()), 0o600); err != nil {
		return execx.Result{}, err
	}
	return execx.Result{}, nil
}

type fakeAlerts struct {
	mu     sync.Mutex
	active map[string]bool
}

func newAlerts() *fakeAlerts { return &fakeAlerts{active: map[string]bool{}} }
func (a *fakeAlerts) Raise(code string, _ map[string]any, _ ...doctor.RaiseOption) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.active[code] = true
	return nil
}
func (a *fakeAlerts) Clear(code string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.active, code)
}
func (a *fakeAlerts) has(code string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.active[code]
}

// rig is a lab with two roots on disk.
type rig struct {
	t      *testing.T
	roots  []ark.Root
	idx    *fakeIndex
	run    *manageRunner
	alerts *fakeAlerts
	lib    *Library
	state  string
}

func newRig(t *testing.T) *rig {
	t.Helper()
	base := t.TempDir()
	r := &rig{t: t, idx: &fakeIndex{copies: map[string][]ark.Copy{}}, run: &manageRunner{}, alerts: newAlerts(), state: filepath.Join(base, "state", "kiwix")}
	r.roots = []ark.Root{
		{ID: "internal", Kind: ark.KindInternal, Path: filepath.Join(base, "internal")},
		{ID: "ark-0000aaaa", Kind: ark.KindArkDrive, Path: filepath.Join(base, "drive", "localai"), MountID: 5},
	}
	tool := filepath.Join(base, "kiwix-manage")
	if err := os.WriteFile(tool, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	r.lib = &Library{
		Finder: r.idx, Roots: func() []ark.Root { return r.roots }, Runner: r.run, Tool: tool, StateDir: r.state, Alerts: r.alerts,
	}
	return r
}

// addZIM puts a ZIM file for item id on root index ri and records the copy.
func (r *rig) addZIM(ri int, id, book, file, sortKey string) string {
	r.t.Helper()
	root := r.roots[ri]
	dir := filepath.Join(root.Path, "kiwix", id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		r.t.Fatal(err)
	}
	p := filepath.Join(dir, file)
	if err := os.WriteFile(p, []byte("zim"), 0o644); err != nil {
		r.t.Fatal(err)
	}
	src, _ := json.Marshal(book)
	m := manifest.Manifest{
		ItemID: id, ItemKind: manifest.KindKiwix, Version: sortKey, SortKey: sortKey,
		Source: manifest.Source{Type: "kiwix", Fields: map[string]json.RawMessage{"book": src}},
		Files:  []manifest.File{{Kind: "zim", Path: file}},
	}
	r.idx.copies[id] = append(r.idx.copies[id], ark.Copy{RootID: root.ID, Dir: dir, Files: []string{p}, Manifest: m})
	return p
}

func (r *rig) merged() string {
	r.t.Helper()
	b, err := os.ReadFile(filepath.Join(r.state, LibraryFile))
	if err != nil {
		r.t.Fatal(err)
	}
	return string(b)
}

func books(t *testing.T, s string) int {
	t.Helper()
	n, err := countBooks([]byte(s))
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestTwoRootsMergedAbsolutePerRootRelative(t *testing.T) {
	r := newRig(t)
	a := r.addZIM(0, "survival-first-aid", "medicine_en", "medicine_en_2024-08.zim", "2024-08")
	b := r.addZIM(1, "survival-water", "water_en", "water_en_2024-08.zim", "2024-08")
	res, err := r.lib.Regenerate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Books != 2 || !res.Written {
		t.Fatalf("result %+v", res)
	}
	m := r.merged()
	if books(t, m) != 2 || !strings.Contains(m, `path="`+a+`"`) || !strings.Contains(m, `path="`+b+`"`) {
		t.Fatalf("merged library wrong:\n%s", m)
	}
	fi, err := os.Stat(filepath.Join(r.state, LibraryFile))
	if err != nil || fi.Mode().Perm() != 0o644 {
		t.Fatalf("mode %v err %v", fi, err)
	}
	if _, err := os.Stat(filepath.Join(r.state, "library.xml.tmp")); !os.IsNotExist(err) {
		t.Fatalf("temp file left behind: %v", err)
	}
	for i, want := range []string{`path="survival-first-aid/medicine_en_2024-08.zim"`, `path="survival-water/water_en_2024-08.zim"`} {
		rr, err := r.lib.RegenerateRoot(context.Background(), r.roots[i].ID)
		if err != nil || !rr.Written || rr.Books != 1 {
			t.Fatalf("root %d: %+v %v", i, rr, err)
		}
		data, err := os.ReadFile(filepath.Join(r.roots[i].Path, "kiwix", LibraryFile))
		if err != nil {
			t.Fatal(err)
		}
		if books(t, string(data)) != 1 || !strings.Contains(string(data), want) || strings.Contains(string(data), r.roots[i].Path) {
			t.Fatalf("per-root %d library wrong:\n%s", i, data)
		}
	}
}

func TestBestCopyOnlyAndDuplicateNameKeepsNewest(t *testing.T) {
	r := newRig(t)
	old := r.addZIM(0, "wiki-old", "wiki_en", "wiki_en_2024-01.zim", "2024-01")
	nw := r.addZIM(1, "wiki-new", "wiki_en", "wiki_en_2025-06.zim", "2025-06")
	// A second, worse copy of the same item must not appear in the merged library.
	r.addZIM(1, "wiki-old", "wiki_en", "wiki_en_2024-01.zim", "2024-01")
	if _, err := r.lib.Regenerate(context.Background()); err != nil {
		t.Fatal(err)
	}
	m := r.merged()
	if books(t, m) != 1 || !strings.Contains(m, nw) || strings.Contains(m, old) {
		t.Fatalf("expected only the newest copy:\n%s", m)
	}
}

func TestNonKiwixItemsIgnored(t *testing.T) {
	r := newRig(t)
	r.idx.copies["model-x"] = []ark.Copy{{RootID: "internal", Manifest: manifest.Manifest{ItemKind: manifest.KindModel, Files: []manifest.File{{Kind: "gguf"}}}, Files: []string{"/x.gguf"}}}
	res, err := r.lib.Regenerate(context.Background())
	if err != nil || !res.Empty {
		t.Fatalf("%+v %v", res, err)
	}
}

func TestEmptyThenNonEmptyAlerts(t *testing.T) {
	r := newRig(t)
	res, err := r.lib.Regenerate(context.Background())
	if err != nil || !res.Empty || res.Books != 0 {
		t.Fatalf("%+v %v", res, err)
	}
	if books(t, r.merged()) != 0 || !r.alerts.has(doctor.SVC001) {
		t.Fatal("empty library must be written and LAI-SVC-001 raised")
	}
	if r.run.count.Load() != 0 {
		t.Fatal("kiwix-manage must not run for an empty library")
	}
	r.addZIM(0, "a", "a_en", "a_en_2024.zim", "2024")
	if res, err = r.lib.Regenerate(context.Background()); err != nil || res.Books != 1 {
		t.Fatalf("%+v %v", res, err)
	}
	if r.alerts.has(doctor.SVC001) {
		t.Fatal("LAI-SVC-001 must clear once there is a book")
	}
}

func TestFailureKeepsOldLibrary(t *testing.T) {
	cases := []struct {
		name   string
		break_ func(r *rig)
	}{
		{"exit non-zero", func(r *rig) { r.run.fail = "boom" }},
		{"kiwix-manage missing", func(r *rig) { r.lib.Tool = filepath.Join(r.state, "no-such-tool") }},
		{"only a bad book", func(r *rig) {}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t)
			r.addZIM(0, "a", "a_en", "a_en_2024.zim", "2024")
			if _, err := r.lib.Regenerate(context.Background()); err != nil {
				t.Fatal(err)
			}
			before := r.merged()
			r.addZIM(1, "b", "b_en", "b_en_damaged.zim", "2024")
			if tc.name != "only a bad book" {
				tc.break_(r)
			} else {
				// only the damaged book: remove the good one so nothing is usable
				delete(r.idx.copies, "a")
			}
			_, err := r.lib.Regenerate(context.Background())
			if err == nil {
				t.Fatal("expected an error")
			}
			if tc.name == "kiwix-manage missing" && !errors.Is(err, ErrToolMissing) {
				t.Fatalf("want ErrToolMissing, got %v", err)
			}
			if !r.alerts.has(doctor.SVC005) {
				t.Fatal("LAI-SVC-005 not raised")
			}
			if got := r.merged(); got != before {
				t.Fatalf("old library was not kept:\n%s", got)
			}
		})
	}
}

func TestDamagedZIMSkippedOthersKept(t *testing.T) {
	r := newRig(t)
	good := r.addZIM(0, "a", "a_en", "a_en_2024.zim", "2024")
	bad := r.addZIM(1, "b", "b_en", "b_en_damaged.zim", "2024")
	res, err := r.lib.Regenerate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Books != 1 || len(res.Skipped) != 1 || res.Skipped[0] != bad {
		t.Fatalf("%+v", res)
	}
	m := r.merged()
	if !strings.Contains(m, good) || strings.Contains(m, bad) || books(t, m) != 1 {
		t.Fatalf("merged:\n%s", m)
	}
	if r.alerts.has(doctor.SVC005) {
		t.Fatal("a skipped book is not a failed rebuild")
	}
}

func TestRootUnpluggedMidRun(t *testing.T) {
	r := newRig(t)
	r.addZIM(0, "a", "a_en", "a_en_2024.zim", "2024")
	b := r.addZIM(1, "b", "b_en", "b_en_2024.zim", "2024")
	if res, err := r.lib.Regenerate(context.Background()); err != nil || res.Books != 2 {
		t.Fatalf("%+v %v", res, err)
	}
	// The drive disappears: its files are gone although the index still lists them.
	if err := os.Remove(b); err != nil {
		t.Fatal(err)
	}
	res, err := r.lib.Regenerate(context.Background())
	if err != nil || res.Books != 1 || !res.Written {
		t.Fatalf("%+v %v", res, err)
	}
	if strings.Contains(r.merged(), b) {
		t.Fatal("the removed copy is still listed")
	}
	// The root leaves the root list before the index is rebuilt.
	r.roots = r.roots[:1]
	r.idx.copies["a"][0].RootID = "internal"
	rr, err := r.lib.RegenerateRoot(context.Background(), "ark-0000aaaa")
	if err != nil || !rr.NoWrite {
		t.Fatalf("unmounted root: %+v %v", rr, err)
	}
}

func TestPerRootNoWriteCases(t *testing.T) {
	r := newRig(t)
	r.addZIM(1, "b", "b_en", "b_en_2024.zim", "2024")
	r.roots[1].ReadOnly = true
	rr, err := r.lib.RegenerateRoot(context.Background(), "ark-0000aaaa")
	if err != nil || !rr.NoWrite {
		t.Fatalf("read-only root: %+v %v", rr, err)
	}
	if _, err := os.Stat(filepath.Join(r.roots[1].Path, "kiwix", LibraryFile)); !os.IsNotExist(err) {
		t.Fatal("a read-only root must not be written")
	}
	rr, err = r.lib.RegenerateRoot(context.Background(), "internal")
	if err != nil || !rr.NoWrite {
		t.Fatalf("root without books: %+v %v", rr, err)
	}
}

func TestPerRootWriteFailureIsReturnedNotFatalToMerged(t *testing.T) {
	r := newRig(t)
	r.addZIM(1, "b", "b_en", "b_en_2024.zim", "2024")
	// <root>/kiwix/library.xml is a directory: the atomic rename cannot replace it.
	if err := os.MkdirAll(filepath.Join(r.roots[1].Path, "kiwix", LibraryFile, "x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := r.lib.RegenerateRoot(context.Background(), "ark-0000aaaa"); err == nil {
		t.Fatal("expected a write error")
	}
	if r.alerts.has(doctor.SVC005) {
		t.Fatal("a per-root failure must not raise LAI-SVC-005")
	}
	if _, err := r.lib.Regenerate(context.Background()); err != nil {
		t.Fatalf("merged library must still work: %v", err)
	}
}

func TestUnchangedInputDoesNotRunAgain(t *testing.T) {
	r := newRig(t)
	r.addZIM(0, "a", "a_en", "a_en_2024.zim", "2024")
	if _, err := r.lib.Regenerate(context.Background()); err != nil {
		t.Fatal(err)
	}
	n := r.run.count.Load()
	res, err := r.lib.Regenerate(context.Background())
	if err != nil || res.Written || res.Books != 1 || r.run.count.Load() != n {
		t.Fatalf("%+v %v runs %d->%d", res, err, n, r.run.count.Load())
	}
	// A library file deleted by hand is rebuilt.
	if err := os.Remove(filepath.Join(r.state, LibraryFile)); err != nil {
		t.Fatal(err)
	}
	if res, err = r.lib.Regenerate(context.Background()); err != nil || !res.Written {
		t.Fatalf("%+v %v", res, err)
	}
}

func TestRunsAsServiceUser(t *testing.T) {
	r := newRig(t)
	r.addZIM(0, "a", "a_en", "a_en_2024.zim", "2024")
	cred := &execx.Credential{UID: uint32(os.Getuid()), GID: uint32(os.Getgid())}
	r.lib.Cred = cred
	if _, err := r.lib.Regenerate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(r.run.creds) == 0 || r.run.creds[0] != cred {
		t.Fatal("kiwix-manage must run with the service credential")
	}
	a := r.run.calls[0]
	if a[1] != "add" || !strings.HasSuffix(a[0], "library.xml.tmp") {
		t.Fatalf("argv %v", a)
	}
}

func TestStateDirCreatedWorldReadable(t *testing.T) {
	old := syscall.Umask(0o077)
	defer syscall.Umask(old)
	r := newRig(t)
	r.addZIM(0, "a", "a_en", "a_en_2024.zim", "2024")
	if _, err := r.lib.Regenerate(context.Background()); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(r.state)
	if err != nil || fi.Mode().Perm() != 0o755 {
		t.Fatalf("state dir %v %v", fi, err)
	}
}

// ---- the debouncer ------------------------------------------------------------------------------

type countingRegen struct {
	merged, roots atomic.Int32
	started       chan struct{}
	release       chan struct{}
}

func (c *countingRegen) Regenerate(context.Context) (Result, error) {
	c.merged.Add(1)
	if c.started != nil {
		c.started <- struct{}{}
		<-c.release
	}
	return Result{}, nil
}
func (c *countingRegen) RegenerateRoot(context.Context, string) (RootResult, error) {
	c.roots.Add(1)
	return RootResult{}, nil
}

func TestBurstOfTriggersRunsAtMostTwice(t *testing.T) {
	c := &countingRegen{started: make(chan struct{}, 8), release: make(chan struct{})}
	s := NewService(c)
	s.Settle = time.Millisecond
	s.Interval = 50 * time.Millisecond
	s.Roots = func() []ark.Root { return []ark.Root{{ID: "internal"}} }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()

	s.Trigger()
	<-c.started // the first run is in progress
	for i := 0; i < 4; i++ {
		s.Trigger() // arrive during the run and the wait after it
	}
	c.release <- struct{}{}
	<-c.started // the one follow-up run: a trigger during a run is not lost
	close(c.release)
	time.Sleep(200 * time.Millisecond)
	cancel()
	<-done
	if n := c.merged.Load(); n != 2 {
		t.Fatalf("5 triggers must give 2 runs, got %d", n)
	}
	if c.roots.Load() != 2 {
		t.Fatalf("per-root libraries refreshed %d times", c.roots.Load())
	}
}

func TestServiceHonoursEnabled(t *testing.T) {
	c := &countingRegen{}
	s := NewService(c)
	s.Enabled = func() bool { return false }
	s.Once(context.Background())
	if c.merged.Load() != 0 {
		t.Fatal("a disabled kiwix block must not regenerate")
	}
}
