// Package fake is an in-memory filesystem for testing internal/audit. It implements audit.FS and
// can fail on demand: a full disk (with partial writes), a failing fsync, an unopenable path, a
// failing rename. Every write and sync is recorded, in order, so a test can prove that each line
// was fsync'd.
package fake

import (
	"errors"
	"io/fs"
	"os"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/audit"
)

// FS is an in-memory filesystem of flat paths. The zero value is not usable; use New.
type FS struct {
	mu    sync.Mutex
	files map[string]*node
	ops   []string

	// DiskFree is the number of bytes that can still be written; a negative value means
	// unlimited. A write larger than DiskFree is written in part and fails with ENOSPC.
	DiskFree int64
	// OpenErr, SyncErr, RenameErr and RemoveErr, when non-nil, make that operation fail. OpenErr
	// and RenameErr see the path (the old path for a rename).
	OpenErr   func(path string) error
	SyncErr   error
	RenameErr func(path string) error
	RemoveErr func(path string) error
}

type node struct {
	data []byte
	mode fs.FileMode
}

// New returns an empty filesystem with unlimited space.
func New() *FS { return &FS{files: map[string]*node{}, DiskFree: -1} }

// Put creates or replaces a file with the given content and mode.
func (f *FS) Put(path string, data []byte, mode fs.FileMode) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.files[path] = &node{data: append([]byte(nil), data...), mode: mode}
}

// Content returns a file's bytes, and whether it exists.
func (f *FS) Content(path string) ([]byte, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	n, ok := f.files[path]
	if !ok {
		return nil, false
	}
	return append([]byte(nil), n.data...), true
}

// Mode returns a file's permission bits, and whether it exists.
func (f *FS) Mode(path string) (fs.FileMode, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	n, ok := f.files[path]
	if !ok {
		return 0, false
	}
	return n.mode, true
}

// Names returns every path, sorted.
func (f *FS) Names() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.files))
	for p := range f.files {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// Ops returns the recorded operations in order: "open <path>", "write <path> <n>", "sync <path>",
// "rename <old> <new>", "remove <path>". A failed write records the bytes actually written.
func (f *FS) Ops() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.ops...)
}

// Count returns how many recorded operations start with prefix (for example "sync ").
func (f *FS) Count(prefix string) int {
	n := 0
	for _, op := range f.Ops() {
		if strings.HasPrefix(op, prefix) {
			n++
		}
	}
	return n
}

func (f *FS) record(op string) { f.ops = append(f.ops, op) }

// File is an open handle. It keeps pointing at the same data after a rename, as on POSIX.
type File struct {
	fs   *FS
	path string
	n    *node
	done bool
}

// OpenFile opens path for appending (only O_APPEND|O_WRONLY|O_CREATE use is modelled).
func (f *FS) OpenFile(name string, flag int, perm fs.FileMode) (audit.File, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("open " + name)
	if f.OpenErr != nil {
		if err := f.OpenErr(name); err != nil {
			return nil, &fs.PathError{Op: "open", Path: name, Err: err}
		}
	}
	n, ok := f.files[name]
	if !ok {
		if flag&os.O_CREATE == 0 {
			return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
		}
		n = &node{mode: perm}
		f.files[name] = n
	}
	return &File{fs: f, path: name, n: n}, nil
}

// Rename moves a file, replacing the target.
func (f *FS) Rename(oldpath, newpath string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("rename " + oldpath + " " + newpath)
	if f.RenameErr != nil {
		if err := f.RenameErr(oldpath); err != nil {
			return &os.LinkError{Op: "rename", Old: oldpath, New: newpath, Err: err}
		}
	}
	n, ok := f.files[oldpath]
	if !ok {
		return &os.LinkError{Op: "rename", Old: oldpath, New: newpath, Err: fs.ErrNotExist}
	}
	delete(f.files, oldpath)
	f.files[newpath] = n
	return nil
}

// Remove deletes a file.
func (f *FS) Remove(name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("remove " + name)
	if f.RemoveErr != nil {
		if err := f.RemoveErr(name); err != nil {
			return &fs.PathError{Op: "remove", Path: name, Err: err}
		}
	}
	if _, ok := f.files[name]; !ok {
		return &fs.PathError{Op: "remove", Path: name, Err: fs.ErrNotExist}
	}
	delete(f.files, name)
	return nil
}

// Write appends p, in part and with ENOSPC when the disk is full.
func (h *File) Write(p []byte) (int, error) {
	f := h.fs
	f.mu.Lock()
	defer f.mu.Unlock()
	if h.done {
		return 0, os.ErrClosed
	}
	n := len(p)
	var err error
	if f.DiskFree >= 0 && int64(n) > f.DiskFree {
		n, err = int(f.DiskFree), syscall.ENOSPC
	}
	h.n.data = append(h.n.data, p[:n]...)
	if f.DiskFree >= 0 {
		f.DiskFree -= int64(n)
	}
	f.record("write " + h.path + " " + itoa(n))
	return n, err
}

// Sync records an fsync, or fails with FS.SyncErr.
func (h *File) Sync() error {
	f := h.fs
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("sync " + h.path)
	return f.SyncErr
}

// Close closes the handle.
func (h *File) Close() error {
	h.fs.mu.Lock()
	defer h.fs.mu.Unlock()
	h.done = true
	h.fs.record("close " + h.path)
	return nil
}

// Stat describes the file.
func (h *File) Stat() (fs.FileInfo, error) {
	h.fs.mu.Lock()
	defer h.fs.mu.Unlock()
	return info{name: h.path, size: int64(len(h.n.data)), mode: h.n.mode}, nil
}

// Chmod sets the permission bits.
func (h *File) Chmod(mode fs.FileMode) error {
	h.fs.mu.Lock()
	defer h.fs.mu.Unlock()
	h.n.mode = mode
	h.fs.record("chmod " + h.path)
	return nil
}

type info struct {
	name string
	size int64
	mode fs.FileMode
}

func (i info) Name() string       { return i.name }
func (i info) Size() int64        { return i.size }
func (i info) Mode() fs.FileMode  { return i.mode }
func (i info) ModTime() time.Time { return time.Time{} }
func (i info) IsDir() bool        { return false }
func (i info) Sys() any           { return nil }

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for ; n > 0; n /= 10 {
		i--
		b[i] = byte('0' + n%10)
	}
	return string(b[i:])
}

// ErrDisk is a convenient error for OpenErr and friends.
var ErrDisk = errors.New("input/output error")
