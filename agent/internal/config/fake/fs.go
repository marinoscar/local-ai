package fake

import (
	"errors"
	"os"
	"sync"

	"github.com/marinoscar/local-ai/agent/internal/config"
)

// ErrInjected is the error a FaultFS returns for an injected failure.
var ErrInjected = errors.New("fake: injected failure")

// FaultFS wraps a real config.FS (normally config.OSFS in a temp dir) and fails named operations:
// "open", "write", "sync", "chown", "chmod", "close", "rename", "remove", "syncdir", "readfile",
// "stat".
//
// With Crash set, the first injected failure also means "the process died here": every later Remove
// fails too, so the temp file an interrupted write left behind stays on disk, exactly as after a
// kill -9.
type FaultFS struct {
	config.FS
	Crash bool

	mu   sync.Mutex
	fail map[string]int // operation -> remaining failures (-1 = always)
	dead bool
}

// New wraps inner.
func New(inner config.FS) *FaultFS { return &FaultFS{FS: inner, fail: map[string]int{}} }

// FailOn makes the next n calls of op fail (n < 0: all of them).
func (f *FaultFS) FailOn(op string, n int) *FaultFS {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fail[op] = n
	return f
}

// Reset clears all injected failures and the crash state (the process "restarted").
func (f *FaultFS) Reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fail = map[string]int{}
	f.dead = false
}

func (f *FaultFS) hit(op string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.dead && op == "remove" {
		return ErrInjected
	}
	n, ok := f.fail[op]
	if !ok || n == 0 {
		return nil
	}
	if n > 0 {
		f.fail[op] = n - 1
	}
	if f.Crash {
		f.dead = true
	}
	return ErrInjected
}

// OpenFile implements config.FS.
func (f *FaultFS) OpenFile(name string, flag int, perm os.FileMode) (config.File, error) {
	if err := f.hit("open"); err != nil {
		return nil, err
	}
	file, err := f.FS.OpenFile(name, flag, perm)
	if err != nil {
		return nil, err
	}
	return &faultFile{File: file, fs: f}, nil
}

// ReadFile implements config.FS.
func (f *FaultFS) ReadFile(name string) ([]byte, error) {
	if err := f.hit("readfile"); err != nil {
		return nil, err
	}
	return f.FS.ReadFile(name)
}

// Stat implements config.FS.
func (f *FaultFS) Stat(name string) (os.FileInfo, error) {
	if err := f.hit("stat"); err != nil {
		return nil, err
	}
	return f.FS.Stat(name)
}

// Rename implements config.FS.
func (f *FaultFS) Rename(oldpath, newpath string) error {
	if err := f.hit("rename"); err != nil {
		return err
	}
	return f.FS.Rename(oldpath, newpath)
}

// Remove implements config.FS.
func (f *FaultFS) Remove(name string) error {
	if err := f.hit("remove"); err != nil {
		return err
	}
	return f.FS.Remove(name)
}

// SyncDir implements config.FS.
func (f *FaultFS) SyncDir(dir string) error {
	if err := f.hit("syncdir"); err != nil {
		return err
	}
	return f.FS.SyncDir(dir)
}

type faultFile struct {
	config.File
	fs *FaultFS
}

func (f *faultFile) Write(p []byte) (int, error) {
	if err := f.fs.hit("write"); err != nil {
		return 0, err
	}
	return f.File.Write(p)
}

func (f *faultFile) Sync() error {
	if err := f.fs.hit("sync"); err != nil {
		return err
	}
	return f.File.Sync()
}

func (f *faultFile) Chown(uid, gid int) error {
	if err := f.fs.hit("chown"); err != nil {
		return err
	}
	return f.File.Chown(uid, gid)
}

func (f *faultFile) Chmod(mode os.FileMode) error {
	if err := f.fs.hit("chmod"); err != nil {
		return err
	}
	return f.File.Chmod(mode)
}

func (f *faultFile) Close() error {
	if err := f.fs.hit("close"); err != nil {
		_ = f.File.Close()
		return err
	}
	return f.File.Close()
}
