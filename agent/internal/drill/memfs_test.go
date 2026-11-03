package drill

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/marinoscar/local-ai/agent/internal/state"
)

// memFS is an in-memory drill.FS with symlinks and failure injection.
type memFS struct {
	mu    sync.Mutex
	nodes map[string]*memNode
	// fail is consulted before every operation ("write", "rename", "symlink", "remove", "mkdir",
	// "readdir"); a non-nil error is returned instead of doing it.
	fail func(op, path string) error
	ops  []string
}

type memNode struct {
	data   []byte
	link   string
	isLink bool
	mode   os.FileMode
	uid    int
	gid    int
}

func newMemFS() *memFS { return &memFS{nodes: map[string]*memNode{}} }

func (m *memFS) check(op, path string) error {
	m.ops = append(m.ops, op+" "+filepath.Base(path))
	if m.fail != nil {
		return m.fail(op, path)
	}
	return nil
}

func (m *memFS) seed(path string, data string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nodes[path] = &memNode{data: []byte(data), mode: 0o640}
}

// resolve follows symlinks; the target is relative to the link's directory.
func (m *memFS) resolve(path string) (*memNode, string, bool) {
	for i := 0; i < 8; i++ {
		n, ok := m.nodes[path]
		if !ok {
			return nil, path, false
		}
		if !n.isLink {
			return n, path, true
		}
		t := n.link
		if !filepath.IsAbs(t) {
			t = filepath.Join(filepath.Dir(path), t)
		}
		path = t
	}
	return nil, path, false
}

func (m *memFS) names(dir string) []string {
	var out []string
	for p := range m.nodes {
		if filepath.Dir(p) == dir {
			out = append(out, filepath.Base(p))
		}
	}
	sort.Strings(out)
	return out
}

// listing returns the file names of dir for assertions.
func (m *memFS) listing(dir string) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.names(dir)
}

func (m *memFS) ReadFile(name string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n, _, ok := m.resolve(name)
	if !ok {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}
	return append([]byte(nil), n.data...), nil
}

type memFile struct {
	fs   *memFS
	path string
	buf  []byte
	mode os.FileMode
	uid  int
	gid  int
}

func (f *memFile) Write(p []byte) (int, error) {
	f.fs.mu.Lock()
	defer f.fs.mu.Unlock()
	if err := f.fs.check("write", f.path); err != nil {
		return 0, err
	}
	f.buf = append(f.buf, p...)
	return len(p), nil
}
func (f *memFile) Sync() error               { return nil }
func (f *memFile) Chmod(m os.FileMode) error { f.mode = m; return nil }
func (f *memFile) Chown(uid, gid int) error  { f.uid, f.gid = uid, gid; return nil }
func (f *memFile) Close() error {
	f.fs.mu.Lock()
	defer f.fs.mu.Unlock()
	f.fs.nodes[f.path] = &memNode{data: f.buf, mode: f.mode, uid: f.uid, gid: f.gid}
	return nil
}

func (m *memFS) OpenFile(name string, flag int, perm os.FileMode) (state.File, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if flag&os.O_EXCL != 0 {
		if _, ok := m.nodes[name]; ok {
			return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrExist}
		}
	}
	return &memFile{fs: m, path: name, mode: perm, uid: -1, gid: -1}, nil
}

func (m *memFS) Rename(oldpath, newpath string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.check("rename", newpath); err != nil {
		return err
	}
	n, ok := m.nodes[oldpath]
	if !ok {
		return &fs.PathError{Op: "rename", Path: oldpath, Err: fs.ErrNotExist}
	}
	m.nodes[newpath] = n
	delete(m.nodes, oldpath)
	return nil
}

func (m *memFS) Link(oldname, newname string) error { return fmt.Errorf("memFS: Link unsupported") }

func (m *memFS) Remove(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.check("remove", name); err != nil {
		return err
	}
	if _, ok := m.nodes[name]; !ok {
		return &fs.PathError{Op: "remove", Path: name, Err: fs.ErrNotExist}
	}
	delete(m.nodes, name)
	return nil
}

func (m *memFS) ReadDirNames(dir string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.check("readdir", dir); err != nil {
		return nil, err
	}
	return m.names(dir), nil
}

func (m *memFS) SyncDir(string) error { return nil }

func (m *memFS) Exists(name string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.nodes[name]
	return ok, nil
}

func (m *memFS) Symlink(oldname, newname string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.check("symlink", newname); err != nil {
		return err
	}
	m.nodes[newname] = &memNode{link: oldname, isLink: true}
	return nil
}

func (m *memFS) Readlink(name string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n, ok := m.nodes[name]
	if !ok || !n.isLink {
		return "", &fs.PathError{Op: "readlink", Path: name, Err: fs.ErrNotExist}
	}
	return n.link, nil
}

func (m *memFS) MkdirAll(path string, perm os.FileMode) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.check("mkdir", path)
}

var _ FS = (*memFS)(nil)

func hasPrefixName(names []string, prefix string) int {
	n := 0
	for _, s := range names {
		if strings.HasPrefix(s, prefix) {
			n++
		}
	}
	return n
}
