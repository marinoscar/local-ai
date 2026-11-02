package router

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"path"
	"regexp"
	"sort"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/catalog"
	"github.com/marinoscar/local-ai/agent/internal/config"
)

var updateGolden = flag.Bool("update", false, "rewrite the golden files of testdata/golden")

// sum is the hex sha256 of s, computed at run time so no test carries a digest literal.
func sum(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// fileFor is a catalog file entry whose sha256 is derived from its model and name.
func fileFor(id, kind, name string) catalog.ModelFile {
	return catalog.ModelFile{Kind: kind, Name: name, SizeBytes: 1024, SHA256: sum(id + "/" + name)}
}

// llamaModel builds an engine: llama catalog model with the given files and llama block.
func llamaModel(id string, roles []string, vram, ram int64, files []catalog.ModelFile, l *catalog.Llama) catalog.Model {
	return catalog.Model{
		ID: id, DisplayName: id, Engine: "llama", Roles: roles,
		Files: files, Requirements: req(vram, ram), Llama: l,
	}
}

func all() catalog.IntOrAll      { return catalog.IntOrAll{Set: true, All: true} }
func num(n int) catalog.IntOrAll { return catalog.IntOrAll{Set: true, N: n} }

// ---- an in-memory config.FS with fault injection ----

type memEntry struct {
	data     []byte
	mode     os.FileMode
	dir      bool
	uid, gid int
	mtime    time.Time
}

// memFS is a small in-memory config.FS. Paths are absolute and slash separated.
type memFS struct {
	mu      sync.Mutex
	entries map[string]*memEntry
	// failOp makes the named operation fail: open, write, sync, rename, remove, syncdir, readfile, stat.
	failOp map[string]error
	// failPath makes any operation on a path with this prefix fail (readfile and stat).
	failPath map[string]error
	// diskFull makes every write fail with ENOSPC once the data written exceeds this many bytes
	// in total (-1: no limit).
	diskFull int
	written  int
}

func newMemFS() *memFS {
	m := &memFS{entries: map[string]*memEntry{}, failOp: map[string]error{}, failPath: map[string]error{}, diskFull: -1}
	m.entries["/"] = &memEntry{dir: true, mode: 0o755}
	m.entries["/etc"] = &memEntry{dir: true, mode: 0o755}
	m.entries["/etc/localai"] = &memEntry{dir: true, mode: 0o755}
	return m
}

func (m *memFS) mkdirs(p string) {
	for d := path.Dir(p); d != "/" && d != "."; d = path.Dir(d) {
		if _, ok := m.entries[d]; !ok {
			m.entries[d] = &memEntry{dir: true, mode: 0o755}
		}
	}
}

// put creates or replaces a file (and its parents) without going through the fault hooks.
func (m *memFS) put(p string, data []byte, mode os.FileMode) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.mkdirs(p)
	m.entries[p] = &memEntry{data: append([]byte(nil), data...), mode: mode}
}

func (m *memFS) get(p string) ([]byte, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.entries[p]
	if !ok || e.dir {
		return nil, false
	}
	return append([]byte(nil), e.data...), true
}

func (m *memFS) has(p string) bool { _, ok := m.get(p); return ok }

// names lists the files directly under dir.
func (m *memFS) names(dir string) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for p, e := range m.entries {
		if !e.dir && path.Dir(p) == dir {
			out = append(out, path.Base(p))
		}
	}
	sort.Strings(out)
	return out
}

func (m *memFS) hit(op string) error {
	if err := m.failOp[op]; err != nil {
		return err
	}
	return nil
}

type memInfo struct {
	name string
	e    *memEntry
}

func (i memInfo) Name() string       { return i.name }
func (i memInfo) Size() int64        { return int64(len(i.e.data)) }
func (i memInfo) ModTime() time.Time { return i.e.mtime }
func (i memInfo) IsDir() bool        { return i.e.dir }
func (i memInfo) Sys() any           { return &syscall.Stat_t{Uid: uint32(i.e.uid), Gid: uint32(i.e.gid)} }
func (i memInfo) Mode() os.FileMode {
	if i.e.dir {
		return os.ModeDir | i.e.mode
	}
	return i.e.mode
}

type memFile struct {
	fs   *memFS
	path string
}

func (f *memFile) Write(p []byte) (int, error) {
	f.fs.mu.Lock()
	defer f.fs.mu.Unlock()
	if err := f.fs.hit("write"); err != nil {
		return 0, err
	}
	if f.fs.diskFull >= 0 && f.fs.written+len(p) > f.fs.diskFull {
		return 0, &os.PathError{Op: "write", Path: f.path, Err: syscall.ENOSPC}
	}
	f.fs.written += len(p)
	e := f.fs.entries[f.path]
	e.data = append(e.data, p...)
	return len(p), nil
}
func (f *memFile) Sync() error { return f.fs.hit("sync") }
func (f *memFile) Chmod(mode os.FileMode) error {
	f.fs.mu.Lock()
	defer f.fs.mu.Unlock()
	f.fs.entries[f.path].mode = mode
	return nil
}
func (f *memFile) Chown(uid, gid int) error {
	f.fs.mu.Lock()
	defer f.fs.mu.Unlock()
	e := f.fs.entries[f.path]
	if uid >= 0 {
		e.uid = uid
	}
	if gid >= 0 {
		e.gid = gid
	}
	return nil
}
func (f *memFile) Close() error { return nil }

func (m *memFS) OpenFile(name string, flag int, perm os.FileMode) (config.File, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.hit("open"); err != nil {
		return nil, err
	}
	if _, ok := m.entries[name]; ok && flag&os.O_EXCL != 0 {
		return nil, os.ErrExist
	}
	if _, ok := m.entries[path.Dir(name)]; !ok {
		return nil, &os.PathError{Op: "open", Path: name, Err: os.ErrNotExist}
	}
	m.entries[name] = &memEntry{mode: perm}
	return &memFile{fs: m, path: name}, nil
}

func (m *memFS) ReadFile(name string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.hit("readfile"); err != nil {
		return nil, err
	}
	for p, err := range m.failPath {
		if strings.HasPrefix(name, p) {
			return nil, err
		}
	}
	e, ok := m.entries[name]
	if !ok || e.dir {
		return nil, &os.PathError{Op: "open", Path: name, Err: os.ErrNotExist}
	}
	return append([]byte(nil), e.data...), nil
}

func (m *memFS) Stat(name string) (os.FileInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.hit("stat"); err != nil {
		return nil, err
	}
	for p, err := range m.failPath {
		if strings.HasPrefix(name, p) {
			return nil, err
		}
	}
	e, ok := m.entries[name]
	if !ok {
		return nil, &os.PathError{Op: "stat", Path: name, Err: os.ErrNotExist}
	}
	return memInfo{name: path.Base(name), e: e}, nil
}

func (m *memFS) Lstat(name string) (os.FileInfo, error) { return m.Stat(name) }

func (m *memFS) ReadDir(name string) ([]os.DirEntry, error) { return nil, errors.New("not used") }

func (m *memFS) Rename(oldpath, newpath string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.hit("rename"); err != nil {
		return err
	}
	e, ok := m.entries[oldpath]
	if !ok {
		return os.ErrNotExist
	}
	delete(m.entries, oldpath)
	m.entries[newpath] = e
	return nil
}

func (m *memFS) Remove(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.hit("remove"); err != nil {
		return err
	}
	if _, ok := m.entries[name]; !ok {
		return &os.PathError{Op: "remove", Path: name, Err: os.ErrNotExist}
	}
	delete(m.entries, name)
	return nil
}

func (m *memFS) Chmod(name string, mode os.FileMode) error { return nil }
func (m *memFS) Chown(name string, uid, gid int) error     { return nil }
func (m *memFS) SyncDir(dir string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.hit("syncdir")
}

func (m *memFS) MkdirAll(p string, perm os.FileMode) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.hit("mkdir"); err != nil {
		return err
	}
	for d := p; d != "/" && d != "."; d = path.Dir(d) {
		if _, ok := m.entries[d]; !ok {
			m.entries[d] = &memEntry{dir: true, mode: perm}
		}
	}
	return nil
}

var _ config.FS = (*memFS)(nil)

// ---- models on a fake disk ----

// installModel puts a model's files and a verified MANIFEST.json under root.
func installModel(fs *memFS, root string, m catalog.Model) {
	var files []map[string]any
	for _, f := range m.Files {
		fs.put(modelDir(root, m.ID)+"/"+f.Name, []byte("gguf"), 0o644)
		files = append(files, map[string]any{"kind": f.Kind, "path": f.Name, "size_bytes": f.SizeBytes, "sha256": f.SHA256})
	}
	man, _ := json.Marshal(map[string]any{
		"schema_version": 1, "item_id": m.ID, "item_kind": "model", "state": "verified",
		"verified_at": "2026-11-02T03:41:10Z", "files": files,
	})
	fs.put(modelDir(root, m.ID)+"/MANIFEST.json", man, 0o644)
}

// ---- the two lab layouts of llama-router.md sections 3.5.2 and 3.5.3 ----

const (
	rootInternal = "/srv/localai"
	fixedNow     = "2026-11-02T15:04:05Z"
)

// lab is the ai-n3 node on a fake disk with every model installed on the internal root; the
// effective-set tests use it as is, the generator tests wrap it in a world.
type lab struct {
	t            *testing.T
	fs           *memFS
	models       []catalog.Model
	profile      *catalog.Profile
	fit          Env
	roots        Roots
	node         string
	overrides    config.ProfileOverrides
	llama        config.LlamaBlock
	stateDefault string
	survival     bool
}

func newLab(t *testing.T) *lab {
	t.Helper()
	l := &lab{
		t: t, fs: newMemFS(), models: aiN3Models(), profile: aiN3Profile(),
		fit: NewEnv(snapAiN3(), "cuda", routerBlock(1, 512), 0), node: "ai-n3",
		roots: Roots{Internal: rootInternal},
	}
	for _, m := range l.models {
		installModel(l.fs, rootInternal, m)
	}
	return l
}

func fixedTime() time.Time { t, _ := time.Parse(time.RFC3339, fixedNow); return t }

// aiN3Models is the small-8gb catalog slice of section 3.5.2.
func aiN3Models() []catalog.Model {
	return []catalog.Model{
		llamaModel("qwen3.5-9b-q4km", []string{"chat", "reasoning", "coder"}, 6500, 8000,
			[]catalog.ModelFile{fileFor("qwen3.5-9b-q4km", "weights", "Qwen3.5-9B-Q4_K_M.gguf")},
			&catalog.Llama{Ctx: 32768, NGPULayers: num(99), FlashAttn: "on", Parallel: 1,
				Speculative: &catalog.Speculative{Type: "draft-mtp", NMax: 3}}),
		llamaModel("small-vlm-q4km", []string{"vision", "chat"}, 3000, 5000,
			[]catalog.ModelFile{
				fileFor("small-vlm-q4km", "weights", "model-Q4_K_M.gguf"),
				fileFor("small-vlm-q4km", "mmproj", "mmproj-F16.gguf"),
			},
			&catalog.Llama{Ctx: 8192, NGPULayers: all(), FlashAttn: "on", Parallel: 1, MmprojFileKind: "mmproj"}),
	}
}

func aiN3Profile() *catalog.Profile {
	return &catalog.Profile{
		ID: "small-8gb", Router: routerBlock(1, 512),
		Include:      catalog.Include{Models: []string{"qwen3.5-9b-q4km", "small-vlm-q4km"}},
		DefaultModel: "qwen3.5-9b-q4km",
		Survival:     catalog.Survival{DefaultModel: "qwen3.5-9b-q4km"},
	}
}

// aiN1Models is the max-128gb catalog slice of section 3.5.3.
func aiN1Models() []catalog.Model {
	return []catalog.Model{
		llamaModel("large-vlm-q4km", []string{"vision", "chat"}, 20000, 24000,
			[]catalog.ModelFile{
				fileFor("large-vlm-q4km", "weights", "model-Q4_K_M.gguf"),
				fileFor("large-vlm-q4km", "mmproj", "mmproj-F16.gguf"),
			},
			&catalog.Llama{Ctx: 16384, NGPULayers: all(), FlashAttn: "auto", Parallel: 1, MmprojFileKind: "mmproj"}),
		llamaModel("qwen3-coder-next-q4km", []string{"coder"}, 50000, 55000,
			[]catalog.ModelFile{
				fileFor("qwen3-coder-next-q4km", "weights", "Qwen3-Coder-Next-Q4_K_M-00002-of-00002.gguf"),
				fileFor("qwen3-coder-next-q4km", "weights", "Qwen3-Coder-Next-Q4_K_M-00001-of-00002.gguf"),
			},
			&catalog.Llama{Ctx: 131072, NGPULayers: num(99), FlashAttn: "auto", Parallel: 1}),
		llamaModel("qwen3.8-27b-q4km", []string{"reasoning", "chat"}, 24000, 26000,
			[]catalog.ModelFile{
				fileFor("qwen3.8-27b-q4km", "weights", "Qwen3.8-27B-Q4_K_M.gguf"),
				fileFor("qwen3.8-27b-q4km", "draft", "mtp-Qwen3.8-27B-Q4_0.gguf"),
			},
			&catalog.Llama{Ctx: 65536, NGPULayers: num(99), FlashAttn: "auto", Parallel: 1,
				Speculative: &catalog.Speculative{Type: "draft-mtp", NMax: 2, DraftFileKind: "draft", DraftNGL: all()}}),
	}
}

func aiN1Profile() *catalog.Profile {
	return &catalog.Profile{
		ID: "max-128gb", Router: routerBlock(3, 2048),
		Include:      catalog.Include{Models: []string{"large-vlm-q4km", "qwen3-coder-next-q4km", "qwen3.8-27b-q4km"}},
		DefaultModel: "qwen3-coder-next-q4km",
	}
}

var shaRe = regexp.MustCompile(`inputs_sha256=[0-9a-f]{64}`)

// normalized hides the one value a golden file cannot hold: the 64-hex inputs hash.
func normalized(b []byte) []byte {
	return shaRe.ReplaceAll(b, []byte("inputs_sha256=<sha256>"))
}

// checkGolden compares got with testdata/golden/<name>, byte for byte after normalized.
func checkGolden(t *testing.T, name string, got []byte) {
	t.Helper()
	p := "testdata/golden/" + name
	got = normalized(got)
	if *updateGolden {
		if err := os.WriteFile(p, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read golden %s (run with -update): %v", p, err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s differs from the golden file.\n--- got ---\n%s\n--- want ---\n%s", name, got, want)
	}
}

func equalStrings(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) == 0 && len(want) == 0 {
		return
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
}

var _ = io.EOF
