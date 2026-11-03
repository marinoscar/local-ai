package migrate

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/config"
)

// errKilled is what every operation returns once the simulated process has been killed.
var errKilled = errors.New("simulated kill")

// killer kills the "process" at the n-th mutating operation: that operation (and every later one, on
// every fake) fails with errKilled.
type killer struct {
	mu    sync.Mutex
	n     int
	at    int
	after bool
	dead  bool
}

// tick is called before every mutating operation; it returns errKilled when the operation must not
// happen. With after set the process dies when the operation AFTER the n-th begins, which is the same as
// dying right after the n-th returned (nothing in between mutates anything).
func (k *killer) tick() error {
	if k == nil {
		return nil
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.dead {
		return errKilled
	}
	k.n++
	if k.at > 0 && ((!k.after && k.n == k.at) || (k.after && k.n == k.at+1)) {
		k.dead = true
		return errKilled
	}
	return nil
}

// done is called after a mutating operation succeeded; kills never land here (see tick).
func (k *killer) done() error { return nil }

func (k *killer) count() int {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.n
}

type kind int

const (
	kFile kind = iota
	kDir
	kLink
)

type mnode struct {
	kind     kind
	data     []byte
	mode     os.FileMode
	uid, gid int
	target   string
	dev      uint64
}

// memFS is an in-memory filesystem with symbolic links, directories, owners and several device numbers
// (mounts): a rename across two devices fails with EXDEV, as on a real machine.
type memFS struct {
	mu     sync.Mutex
	nodes  map[string]*mnode
	mounts map[string]uint64 // path prefix -> st_dev; "/" is 1
	free   map[uint64]uint64 // free bytes per device
	k      *killer
	// failWrite makes a write to a path containing this text fail (a full disk).
	failWrite string
}

func newMemFS() *memFS {
	m := &memFS{nodes: map[string]*mnode{}, mounts: map[string]uint64{"/": 1}, free: map[uint64]uint64{}}
	m.nodes["/"] = &mnode{kind: kDir, mode: 0o755, dev: 1}
	return m
}

var _ FS = (*memFS)(nil)

func (m *memFS) devOf(p string) uint64 {
	best, dev := "", uint64(1)
	for prefix, d := range m.mounts {
		if (p == prefix || strings.HasPrefix(p, strings.TrimRight(prefix, "/")+"/")) && len(prefix) > len(best) {
			best, dev = prefix, d
		}
	}
	return dev
}

func clean(p string) string { return path.Clean(p) }

// mount puts a path prefix on its own device.
func (m *memFS) mount(prefix string, dev uint64, free uint64) {
	m.mounts[prefix] = dev
	m.free[dev] = free
}

// mkParents creates the directories above p (no tick: this is the test's own setup or part of an
// operation that already ticked).
func (m *memFS) mkParents(p string) {
	d := path.Dir(p)
	if d == p {
		return
	}
	if _, ok := m.nodes[d]; !ok {
		m.mkParents(d)
		m.nodes[d] = &mnode{kind: kDir, mode: 0o755, dev: m.devOf(d)}
	}
}

// put creates a regular file for a test, with its parents.
func (m *memFS) put(p string, data []byte, mode os.FileMode) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p = clean(p)
	m.mkParents(p)
	m.nodes[p] = &mnode{kind: kFile, data: append([]byte(nil), data...), mode: mode, dev: m.devOf(p)}
}

// content returns the bytes of a file, following symbolic links.
func (m *memFS) content(p string) ([]byte, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, n, err := m.resolve(p)
	if err != nil || n.kind != kFile {
		return nil, false
	}
	return append([]byte(nil), n.data...), true
}

func (m *memFS) has(p string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.nodes[clean(p)]
	return ok
}

// resolve follows symbolic links in every component of p, the last one included (up to 16 hops).
func (m *memFS) resolve(p string) (string, *mnode, error) {
	p = clean(p)
	hops := 0
restart:
	parts := strings.Split(strings.TrimPrefix(p, "/"), "/")
	cur := "/"
	for i, part := range parts {
		if part == "" {
			continue
		}
		next := path.Join(cur, part)
		n, ok := m.nodes[next]
		if !ok {
			return next, nil, &fs.PathError{Op: "stat", Path: next, Err: fs.ErrNotExist}
		}
		if n.kind == kLink {
			if hops++; hops > 16 {
				return next, nil, &fs.PathError{Op: "stat", Path: next, Err: errors.New("too many levels of symbolic links")}
			}
			t := n.target
			if t == "/dev/null" {
				return t, &mnode{kind: kFile, mode: 0o666, dev: 1}, nil
			}
			if !path.IsAbs(t) {
				t = path.Join(cur, t)
			}
			p = clean(path.Join(append([]string{t}, parts[i+1:]...)...))
			goto restart
		}
		cur = next
	}
	n, ok := m.nodes[cur]
	if !ok {
		return cur, nil, &fs.PathError{Op: "stat", Path: cur, Err: fs.ErrNotExist}
	}
	return cur, n, nil
}

// snapshot is the whole filesystem as text, to compare before and after (a dry run changes nothing).
func (m *memFS) snapshot() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var keys []string
	for k := range m.nodes {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		n := m.nodes[k]
		fmt.Fprintf(&b, "%s kind=%d mode=%o uid=%d gid=%d dev=%d target=%s data=%x\n", k, n.kind, n.mode, n.uid, n.gid, n.dev, n.target, n.data)
	}
	return b.String()
}

// ---- fileinfo ---------------------------------------------------------------------------------------

type minfo struct {
	name string
	n    *mnode
}

func (i minfo) Name() string { return i.name }
func (i minfo) Size() int64  { return int64(len(i.n.data)) }
func (i minfo) Mode() os.FileMode {
	switch i.n.kind {
	case kDir:
		return fs.ModeDir | i.n.mode
	case kLink:
		return fs.ModeSymlink | 0o777
	}
	return i.n.mode
}
func (i minfo) ModTime() time.Time { return time.Time{} }
func (i minfo) IsDir() bool        { return i.n.kind == kDir }
func (i minfo) Sys() any           { return nil }

type mentry struct{ minfo }

func (e mentry) Type() fs.FileMode          { return e.minfo.Mode().Type() }
func (e mentry) Info() (fs.FileInfo, error) { return e.minfo, nil }

// ---- config.FS --------------------------------------------------------------------------------------

func (m *memFS) Stat(name string) (os.FileInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, n, err := m.resolve(name)
	if err != nil {
		return nil, err
	}
	return minfo{path.Base(p), n}, nil
}

func (m *memFS) Lstat(name string) (os.FileInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p := clean(name)
	n, ok := m.nodes[p]
	if !ok {
		return nil, &fs.PathError{Op: "lstat", Path: p, Err: fs.ErrNotExist}
	}
	return minfo{path.Base(p), n}, nil
}

func (m *memFS) ReadFile(name string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, n, err := m.resolve(name)
	if err != nil {
		return nil, err
	}
	if n.kind != kFile {
		return nil, &fs.PathError{Op: "read", Path: name, Err: syscall.EISDIR}
	}
	return append([]byte(nil), n.data...), nil
}

func (m *memFS) ReadDir(name string) ([]os.DirEntry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, n, err := m.resolve(name)
	if err != nil {
		return nil, err
	}
	if n.kind != kDir {
		return nil, &fs.PathError{Op: "readdir", Path: name, Err: syscall.ENOTDIR}
	}
	var names []string
	for k := range m.nodes {
		if k != p && path.Dir(k) == p {
			names = append(names, k)
		}
	}
	sort.Strings(names)
	var out []os.DirEntry
	for _, k := range names {
		out = append(out, mentry{minfo{path.Base(k), m.nodes[k]}})
	}
	return out, nil
}

func (m *memFS) Rename(oldpath, newpath string) error {
	if err := m.k.tick(); err != nil {
		return err
	}
	m.mu.Lock()
	op, np := clean(oldpath), clean(newpath)
	n, ok := m.nodes[op]
	if !ok {
		m.mu.Unlock()
		return &os.LinkError{Op: "rename", Old: oldpath, New: newpath, Err: fs.ErrNotExist}
	}
	if m.devOf(op) != m.devOf(np) {
		m.mu.Unlock()
		return &os.LinkError{Op: "rename", Old: oldpath, New: newpath, Err: syscall.EXDEV}
	}
	if _, ok := m.nodes[path.Dir(np)]; !ok {
		m.mu.Unlock()
		return &os.LinkError{Op: "rename", Old: oldpath, New: newpath, Err: fs.ErrNotExist}
	}
	if n.kind == kDir {
		// move the subtree
		for k, v := range m.nodes {
			if k == op || strings.HasPrefix(k, op+"/") {
				nk := np + strings.TrimPrefix(k, op)
				m.nodes[nk] = v
				delete(m.nodes, k)
			}
		}
	} else {
		m.nodes[np] = n
		delete(m.nodes, op)
	}
	m.mu.Unlock()
	return m.k.done()
}

func (m *memFS) Remove(name string) error {
	if err := m.k.tick(); err != nil {
		return err
	}
	m.mu.Lock()
	p := clean(name)
	n, ok := m.nodes[p]
	if !ok {
		m.mu.Unlock()
		return &fs.PathError{Op: "remove", Path: p, Err: fs.ErrNotExist}
	}
	if n.kind == kDir {
		for k := range m.nodes {
			if k != p && strings.HasPrefix(k, p+"/") {
				m.mu.Unlock()
				return &fs.PathError{Op: "remove", Path: p, Err: syscall.ENOTEMPTY}
			}
		}
	}
	delete(m.nodes, p)
	m.mu.Unlock()
	return m.k.done()
}

func (m *memFS) Chmod(name string, mode os.FileMode) error {
	if err := m.k.tick(); err != nil {
		return err
	}
	m.mu.Lock()
	_, n, err := m.resolve(name)
	if err == nil {
		n.mode = mode.Perm()
	}
	m.mu.Unlock()
	if err != nil {
		return err
	}
	return m.k.done()
}

func (m *memFS) Chown(name string, uid, gid int) error {
	if err := m.k.tick(); err != nil {
		return err
	}
	m.mu.Lock()
	_, n, err := m.resolve(name)
	if err == nil {
		if uid >= 0 {
			n.uid = uid
		}
		if gid >= 0 {
			n.gid = gid
		}
	}
	m.mu.Unlock()
	if err != nil {
		return err
	}
	return m.k.done()
}

func (m *memFS) SyncDir(string) error {
	if err := m.k.tick(); err != nil {
		return err
	}
	return m.k.done()
}

func (m *memFS) MkdirAll(p string, perm os.FileMode) error {
	m.mu.Lock()
	p = clean(p)
	if n, ok := m.nodes[p]; ok {
		m.mu.Unlock()
		if n.kind != kDir {
			return &fs.PathError{Op: "mkdir", Path: p, Err: syscall.ENOTDIR}
		}
		return nil
	}
	m.mu.Unlock()
	if err := m.k.tick(); err != nil {
		return err
	}
	m.mu.Lock()
	m.mkParents(p)
	m.nodes[p] = &mnode{kind: kDir, mode: perm.Perm(), dev: m.devOf(p)}
	m.mu.Unlock()
	return m.k.done()
}

func (m *memFS) Symlink(target, link string) error {
	if err := m.k.tick(); err != nil {
		return err
	}
	m.mu.Lock()
	p := clean(link)
	if _, ok := m.nodes[p]; ok {
		m.mu.Unlock()
		return &os.LinkError{Op: "symlink", Old: target, New: link, Err: fs.ErrExist}
	}
	if _, ok := m.nodes[path.Dir(p)]; !ok {
		m.mu.Unlock()
		return &os.LinkError{Op: "symlink", Old: target, New: link, Err: fs.ErrNotExist}
	}
	m.nodes[p] = &mnode{kind: kLink, target: target, dev: m.devOf(p)}
	m.mu.Unlock()
	return m.k.done()
}

func (m *memFS) Readlink(name string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n, ok := m.nodes[clean(name)]
	if !ok {
		return "", &fs.PathError{Op: "readlink", Path: name, Err: fs.ErrNotExist}
	}
	if n.kind != kLink {
		return "", &fs.PathError{Op: "readlink", Path: name, Err: syscall.EINVAL}
	}
	return n.target, nil
}

func (m *memFS) OpenRead(name string) (io.ReadCloser, error) {
	data, err := m.ReadFile(name)
	if err != nil {
		return nil, err
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (m *memFS) Dev(name string) (uint64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p := clean(name)
	n, ok := m.nodes[p]
	if !ok {
		return 0, &fs.PathError{Op: "stat", Path: p, Err: fs.ErrNotExist}
	}
	return n.dev, nil
}

func (m *memFS) FreeBytes(name string) (uint64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d := m.devOf(clean(name))
	if f, ok := m.free[d]; ok {
		return f, nil
	}
	return 1 << 50, nil
}

// mfile is an open file for writing.
type mfile struct {
	m      *memFS
	path   string
	closed bool
}

func (m *memFS) OpenFile(name string, flag int, perm os.FileMode) (config.File, error) {
	p := clean(name)
	if flag&(os.O_WRONLY|os.O_RDWR) == 0 {
		return nil, errors.New("memfs: OpenFile is for writing")
	}
	if err := m.k.tick(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	if _, ok := m.nodes[path.Dir(p)]; !ok {
		m.mu.Unlock()
		return nil, &fs.PathError{Op: "open", Path: p, Err: fs.ErrNotExist}
	}
	if _, ok := m.nodes[p]; ok && flag&os.O_EXCL != 0 {
		m.mu.Unlock()
		return nil, &fs.PathError{Op: "open", Path: p, Err: fs.ErrExist}
	}
	m.nodes[p] = &mnode{kind: kFile, mode: perm.Perm(), dev: m.devOf(p)}
	m.mu.Unlock()
	if err := m.k.done(); err != nil {
		return nil, err
	}
	return &mfile{m: m, path: p}, nil
}

func (f *mfile) Write(b []byte) (int, error) {
	if err := f.m.k.tick(); err != nil {
		return 0, err
	}
	f.m.mu.Lock()
	n := f.m.nodes[f.path]
	if n == nil {
		f.m.mu.Unlock()
		return 0, fs.ErrNotExist
	}
	if f.m.failWrite != "" && strings.Contains(f.path, f.m.failWrite) {
		f.m.mu.Unlock()
		return 0, syscall.ENOSPC
	}
	n.data = append(n.data, b...)
	f.m.mu.Unlock()
	return len(b), f.m.k.done()
}

func (f *mfile) Sync() error {
	if err := f.m.k.tick(); err != nil {
		return err
	}
	return f.m.k.done()
}

func (f *mfile) Chmod(mode os.FileMode) error { return f.m.Chmod(f.path, mode) }
func (f *mfile) Chown(uid, gid int) error     { return f.m.Chown(f.path, uid, gid) }
func (f *mfile) Close() error                 { f.closed = true; return nil }
