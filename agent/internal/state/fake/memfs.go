// Package fake is the in-memory filesystem for tests of the state store (agent.md section 3.2). It
// implements state.FS and adds what real disks make hard to test: failure injection per operation
// (ENOSPC, EROFS, EACCES), a simulated kill after N operations, and a Reboot that drops every byte
// that was written but never fsynced.
package fake

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"

	"github.com/marinoscar/local-ai/agent/internal/state"
)

var _ state.FS = (*MemFS)(nil)

// ErrCrashed is returned by every operation after the simulated kill point, until Reboot.
var ErrCrashed = errors.New("fake: process killed")

// Operation kinds recorded in Op.Kind.
const (
	KindRead    = "read"
	KindOpen    = "open"
	KindWrite   = "write"
	KindChown   = "chown"
	KindChmod   = "chmod"
	KindSync    = "sync"
	KindClose   = "close"
	KindRename  = "rename"
	KindLink    = "link"
	KindRemove  = "remove"
	KindSyncDir = "syncdir"
	KindReadDir = "readdir"
	KindExists  = "exists"
)

// Op is one filesystem call. Path is the file (for rename and link, the destination).
type Op struct {
	Kind string
	Path string
}

type node struct {
	data     []byte
	synced   int // bytes that survive a Reboot
	mode     os.FileMode
	uid, gid int
}

// MemFS is a flat in-memory filesystem: every directory exists implicitly, files are inodes that
// several paths can link to.
type MemFS struct {
	mu    sync.Mutex
	files map[string]*node
	ops   []Op

	// Hook, when set, is called before every operation; a non-nil error makes the operation fail
	// without effect. It runs with the filesystem unlocked, so it may block (to hold a write in flight).
	Hook func(Op) error
	// NoLink makes Link fail with EPERM, like a filesystem without hard links.
	NoLink bool

	crashAt int // 0 = never; else the number of mutating operations allowed before the kill
	mutOps  int
	crashed bool
}

// New returns an empty MemFS.
func New() *MemFS { return &MemFS{files: map[string]*node{}} }

// Seed creates (or replaces) a durable file, as if it was there before the test started.
func (m *MemFS) Seed(path string, data []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p := filepath.Clean(path)
	m.files[p] = &node{data: append([]byte(nil), data...), synced: len(data), mode: 0o640, uid: -1, gid: -1}
}

// Contents returns a file's bytes and whether it exists.
func (m *MemFS) Contents(path string) ([]byte, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n, ok := m.files[filepath.Clean(path)]
	if !ok {
		return nil, false
	}
	return append([]byte(nil), n.data...), true
}

// Mode returns a file's permission bits and owner (-1 when never chown'ed).
func (m *MemFS) Mode(path string) (mode os.FileMode, uid, gid int, ok bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n, found := m.files[filepath.Clean(path)]
	if !found {
		return 0, 0, 0, false
	}
	return n.mode, n.uid, n.gid, true
}

// Names lists the file names in dir.
func (m *MemFS) Names(dir string) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.namesLocked(dir)
}

func (m *MemFS) namesLocked(dir string) []string {
	dir = filepath.Clean(dir)
	var out []string
	for p := range m.files {
		if filepath.Dir(p) == dir {
			out = append(out, filepath.Base(p))
		}
	}
	sort.Strings(out)
	return out
}

// Ops returns the recorded calls in order.
func (m *MemFS) Ops() []Op {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]Op(nil), m.ops...)
}

// ResetOps forgets the recorded calls.
func (m *MemFS) ResetOps() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ops = nil
}

// Kinds returns the Kind of every recorded call whose path contains pathPart, in order.
func (m *MemFS) Kinds(pathPart string) []string {
	var out []string
	for _, o := range m.Ops() {
		if strings.Contains(o.Path, pathPart) {
			out = append(out, o.Kind)
		}
	}
	return out
}

// CrashAfter arranges a simulated kill: after n more mutating operations (open, write, chown, chmod,
// sync, close, rename, link, remove, syncdir) have succeeded, the next one and every later one fails
// with ErrCrashed and has no effect. Reboot clears it. n = 0 kills before the first operation.
func (m *MemFS) CrashAfter(n int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.crashAt = n + 1
	m.mutOps = 0
	m.crashed = false
}

// MutatingOps returns how many mutating operations ran since the last CrashAfter or Reboot.
func (m *MemFS) MutatingOps() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.mutOps
}

// Reboot ends a simulated kill and models power loss: every file keeps only the bytes that were
// fsynced, and files whose last path was removed stay gone.
func (m *MemFS) Reboot() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.crashAt, m.mutOps, m.crashed = 0, 0, false
	seen := map[*node]bool{}
	for _, n := range m.files {
		if !seen[n] {
			seen[n] = true
			n.data = n.data[:n.synced]
		}
	}
}

func isMutating(kind string) bool {
	switch kind {
	case KindRead, KindReadDir, KindExists:
		return false
	}
	return true
}

// begin records the op, runs the hook and applies the crash rule. It returns with m.mu NOT held when
// it returns an error, and held when it returns nil.
func (m *MemFS) begin(kind, path string) error {
	op := Op{Kind: kind, Path: filepath.Clean(path)}
	m.mu.Lock()
	hook := m.Hook
	m.mu.Unlock()
	if hook != nil {
		if err := hook(op); err != nil {
			m.mu.Lock()
			m.ops = append(m.ops, op)
			m.mu.Unlock()
			return &os.PathError{Op: kind, Path: op.Path, Err: unwrapErrno(err)}
		}
	}
	m.mu.Lock()
	if m.crashed {
		m.mu.Unlock()
		return ErrCrashed
	}
	m.ops = append(m.ops, op)
	if isMutating(kind) {
		m.mutOps++
		if m.crashAt > 0 && m.mutOps >= m.crashAt {
			m.crashed = true
			m.mu.Unlock()
			return ErrCrashed
		}
	}
	return nil
}

func unwrapErrno(err error) error {
	var e syscall.Errno
	if errors.As(err, &e) {
		return e
	}
	return err
}

// ReadFile implements state.FS.
func (m *MemFS) ReadFile(name string) ([]byte, error) {
	if err := m.begin(KindRead, name); err != nil {
		return nil, err
	}
	defer m.mu.Unlock()
	n, ok := m.files[filepath.Clean(name)]
	if !ok {
		return nil, &os.PathError{Op: "open", Path: name, Err: os.ErrNotExist}
	}
	return append([]byte(nil), n.data...), nil
}

type file struct {
	m    *MemFS
	n    *node
	path string
	done bool
}

// OpenFile implements state.FS (create-exclusive only).
func (m *MemFS) OpenFile(name string, flag int, perm os.FileMode) (state.File, error) {
	if err := m.begin(KindOpen, name); err != nil {
		return nil, err
	}
	defer m.mu.Unlock()
	p := filepath.Clean(name)
	if _, ok := m.files[p]; ok && flag&os.O_EXCL != 0 {
		return nil, &os.PathError{Op: "open", Path: name, Err: os.ErrExist}
	}
	n := &node{mode: perm, uid: -1, gid: -1}
	m.files[p] = n
	return &file{m: m, n: n, path: p}, nil
}

// Write appends to the file.
func (f *file) Write(p []byte) (int, error) {
	if err := f.m.begin(KindWrite, f.path); err != nil {
		return 0, err
	}
	defer f.m.mu.Unlock()
	f.n.data = append(f.n.data, p...)
	return len(p), nil
}

// Sync makes everything written so far survive a Reboot.
func (f *file) Sync() error {
	if err := f.m.begin(KindSync, f.path); err != nil {
		return err
	}
	defer f.m.mu.Unlock()
	f.n.synced = len(f.n.data)
	return nil
}

// Close closes the handle.
func (f *file) Close() error {
	if err := f.m.begin(KindClose, f.path); err != nil {
		return err
	}
	defer f.m.mu.Unlock()
	f.done = true
	return nil
}

// Chmod sets the permission bits.
func (f *file) Chmod(mode os.FileMode) error {
	if err := f.m.begin(KindChmod, f.path); err != nil {
		return err
	}
	defer f.m.mu.Unlock()
	f.n.mode = mode
	return nil
}

// Chown sets the owner.
func (f *file) Chown(uid, gid int) error {
	if err := f.m.begin(KindChown, f.path); err != nil {
		return err
	}
	defer f.m.mu.Unlock()
	f.n.uid, f.n.gid = uid, gid
	return nil
}

// Rename implements state.FS.
func (m *MemFS) Rename(oldpath, newpath string) error {
	if err := m.begin(KindRename, newpath); err != nil {
		return err
	}
	defer m.mu.Unlock()
	o, n := filepath.Clean(oldpath), filepath.Clean(newpath)
	node, ok := m.files[o]
	if !ok {
		return &os.LinkError{Op: "rename", Old: oldpath, New: newpath, Err: os.ErrNotExist}
	}
	delete(m.files, o)
	m.files[n] = node
	return nil
}

// Link implements state.FS.
func (m *MemFS) Link(oldname, newname string) error {
	if err := m.begin(KindLink, newname); err != nil {
		return err
	}
	defer m.mu.Unlock()
	if m.NoLink {
		return &os.LinkError{Op: "link", Old: oldname, New: newname, Err: syscall.EPERM}
	}
	o, n := filepath.Clean(oldname), filepath.Clean(newname)
	node, ok := m.files[o]
	if !ok {
		return &os.LinkError{Op: "link", Old: oldname, New: newname, Err: os.ErrNotExist}
	}
	if _, exists := m.files[n]; exists {
		return &os.LinkError{Op: "link", Old: oldname, New: newname, Err: os.ErrExist}
	}
	m.files[n] = node
	return nil
}

// Remove implements state.FS.
func (m *MemFS) Remove(name string) error {
	if err := m.begin(KindRemove, name); err != nil {
		return err
	}
	defer m.mu.Unlock()
	p := filepath.Clean(name)
	if _, ok := m.files[p]; !ok {
		return &os.PathError{Op: "remove", Path: name, Err: os.ErrNotExist}
	}
	delete(m.files, p)
	return nil
}

// ReadDirNames implements state.FS.
func (m *MemFS) ReadDirNames(dir string) ([]string, error) {
	if err := m.begin(KindReadDir, dir); err != nil {
		return nil, err
	}
	defer m.mu.Unlock()
	return m.namesLocked(dir), nil
}

// SyncDir implements state.FS.
func (m *MemFS) SyncDir(dir string) error {
	if err := m.begin(KindSyncDir, dir); err != nil {
		return err
	}
	m.mu.Unlock()
	return nil
}

// Exists implements state.FS.
func (m *MemFS) Exists(name string) (bool, error) {
	if err := m.begin(KindExists, name); err != nil {
		return false, err
	}
	defer m.mu.Unlock()
	_, ok := m.files[filepath.Clean(name)]
	return ok, nil
}

// String is a compact dump for test failures.
func (m *MemFS) String() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var parts []string
	for p, n := range m.files {
		parts = append(parts, fmt.Sprintf("%s(%d/%d bytes)", p, n.synced, len(n.data)))
	}
	sort.Strings(parts)
	return strings.Join(parts, " ")
}
