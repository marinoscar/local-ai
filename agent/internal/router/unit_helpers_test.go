package router

import (
	"context"
	"io/fs"
	"os"
	"path"
	"sort"
	"sync"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/config"
)

// unitFS is the in-memory config.FS of the generator tests with the three operations memFS leaves
// out: it lists directories and it applies (and records) Chmod and Chown.
type unitFS struct {
	*memFS
	mu2     sync.Mutex
	chmods  []string
	chowns  []string
	failDir map[string]error // directory -> error of ReadDir
}

func newUnitFS() *unitFS {
	m := newMemFS()
	u := &unitFS{memFS: m, failDir: map[string]error{}}
	_ = m.MkdirAll("/etc/systemd/system", 0o755)
	return u
}

func (u *unitFS) ReadDir(name string) ([]os.DirEntry, error) {
	u.memFS.mu.Lock()
	defer u.memFS.mu.Unlock()
	if err := u.failDir[name]; err != nil {
		return nil, err
	}
	var out []os.DirEntry
	for p, e := range u.memFS.entries {
		if p != name && path.Dir(p) == name {
			out = append(out, fs.FileInfoToDirEntry(memInfo{name: path.Base(p), e: e}))
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Name() < out[b].Name() })
	return out, nil
}

func (u *unitFS) Chmod(name string, mode os.FileMode) error {
	u.memFS.mu.Lock()
	defer u.memFS.mu.Unlock()
	if err := u.memFS.hit("chmod"); err != nil {
		return err
	}
	u.chmods = append(u.chmods, name)
	if e, ok := u.memFS.entries[name]; ok {
		e.mode = mode
	}
	return nil
}

func (u *unitFS) Chown(name string, uid, gid int) error {
	u.memFS.mu.Lock()
	defer u.memFS.mu.Unlock()
	if err := u.memFS.hit("chown"); err != nil {
		return err
	}
	u.chowns = append(u.chowns, name)
	if e, ok := u.memFS.entries[name]; ok {
		if uid >= 0 {
			e.uid = uid
		}
		if gid >= 0 {
			e.gid = gid
		}
	}
	return nil
}

func (u *unitFS) entry(p string) *memEntry {
	u.memFS.mu.Lock()
	defer u.memFS.mu.Unlock()
	return u.memFS.entries[p]
}

var _ config.FS = (*unitFS)(nil)

// memUnit is the UnitHashState of the tests.
type memUnit struct {
	mu  sync.Mutex
	sha string
	err error
}

func (m *memUnit) UnitSHA256() string { m.mu.Lock(); defer m.mu.Unlock(); return m.sha }
func (m *memUnit) SetUnitSHA256(s string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	m.sha = s
	return nil
}

var (
	unitPath  = DefaultUnitPaths("").Unit
	cachePath = DefaultUnitPaths("").CacheDir
)

// unitWorld is a UnitGenerator over a fake disk with the llama binary installed.
type unitWorld struct {
	t      *testing.T
	fs     *unitFS
	st     *memUnit
	sys    *reloaderLog
	alerts *alertLog
	clock  *stepClock
	gen    *UnitGenerator
}

func newUnitWorld(t *testing.T, mut ...func(*UnitGeneratorOptions)) *unitWorld {
	t.Helper()
	w := &unitWorld{t: t, fs: newUnitFS(), st: &memUnit{}, sys: &reloaderLog{}, alerts: &alertLog{}, clock: &stepClock{t: fixedTime()}}
	w.fs.put(DefaultLlamaBinary, []byte("elf"), 0o755)
	opt := UnitGeneratorOptions{
		Paths: DefaultUnitPaths(""), FS: w.fs, MkdirAll: w.fs.MkdirAll,
		State: w.st, Systemd: w.sys, Alerts: w.alerts, Clock: w.clock,
	}
	for _, f := range mut {
		f(&opt)
	}
	g, err := NewUnitGenerator(opt)
	if err != nil {
		t.Fatal(err)
	}
	w.gen = g
	return w
}

func (w *unitWorld) generate(in UnitInputs) (UnitResult, error) {
	w.t.Helper()
	return w.gen.Generate(context.Background(), in)
}

func (w *unitWorld) must(in UnitInputs) UnitResult {
	w.t.Helper()
	res, err := w.generate(in)
	if err != nil {
		w.t.Fatalf("Generate: %v", err)
	}
	return res
}

func (w *unitWorld) unitText() string {
	w.t.Helper()
	b, ok := w.fs.get(unitPath)
	if !ok {
		w.t.Fatal("the unit was not written")
	}
	return string(b)
}

func routerIn(max, margin int) UnitInputs { return UnitInputs{Router: routerBlock(max, margin)} }

func singleIn(m *SingleModel) UnitInputs { return UnitInputs{Mode: LlamaModeSingle, Single: m} }

// singleOnDisk is the ai-n3 default model with its files installed on the fake disk.
func (w *unitWorld) singleOnDisk() *SingleModel {
	sm := singleN3()
	installModel(w.fs.memFS, rootInternal, *sm.Model)
	return sm
}
