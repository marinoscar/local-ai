package config

import (
	"io"
	"os"
	"time"
)

// File is the part of *os.File the atomic writer needs.
type File interface {
	io.Writer
	Sync() error
	Chmod(mode os.FileMode) error
	Chown(uid, gid int) error
	Close() error
}

// FS is the filesystem seam of this package (agent.md 3.2: every external dependency sits behind an
// interface with a fake). OSFS is the real one; internal/config/fake adds fault injection.
type FS interface {
	OpenFile(name string, flag int, perm os.FileMode) (File, error)
	ReadFile(name string) ([]byte, error)
	Stat(name string) (os.FileInfo, error)
	Lstat(name string) (os.FileInfo, error)
	ReadDir(name string) ([]os.DirEntry, error)
	Rename(oldpath, newpath string) error
	Remove(name string) error
	Chmod(name string, mode os.FileMode) error
	Chown(name string, uid, gid int) error
	// SyncDir fsyncs a directory so a rename inside it is durable.
	SyncDir(dir string) error
}

// OSFS is the real filesystem.
type OSFS struct{}

// OpenFile implements FS.
func (OSFS) OpenFile(name string, flag int, perm os.FileMode) (File, error) {
	return os.OpenFile(name, flag, perm)
}

// ReadFile implements FS.
func (OSFS) ReadFile(name string) ([]byte, error) { return os.ReadFile(name) }

// Stat implements FS.
func (OSFS) Stat(name string) (os.FileInfo, error) { return os.Stat(name) }

// Lstat implements FS.
func (OSFS) Lstat(name string) (os.FileInfo, error) { return os.Lstat(name) }

// ReadDir implements FS.
func (OSFS) ReadDir(name string) ([]os.DirEntry, error) { return os.ReadDir(name) }

// Rename implements FS.
func (OSFS) Rename(oldpath, newpath string) error { return os.Rename(oldpath, newpath) }

// Remove implements FS.
func (OSFS) Remove(name string) error { return os.Remove(name) }

// Chmod implements FS.
func (OSFS) Chmod(name string, mode os.FileMode) error { return os.Chmod(name, mode) }

// Chown implements FS.
func (OSFS) Chown(name string, uid, gid int) error { return os.Chown(name, uid, gid) }

// SyncDir implements FS.
func (OSFS) SyncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	serr := d.Sync()
	if cerr := d.Close(); serr == nil {
		serr = cerr
	}
	return serr
}

// Clock is the time seam of the reload loop. RealClock is the real one; internal/config/fake has a
// manual clock for tests.
type Clock interface {
	Now() time.Time
	NewTicker(d time.Duration) Ticker
}

// Ticker is a time.Ticker behind an interface.
type Ticker interface {
	C() <-chan time.Time
	Stop()
}

// RealClock is the wall clock.
type RealClock struct{}

// Now implements Clock.
func (RealClock) Now() time.Time { return time.Now() }

// NewTicker implements Clock.
func (RealClock) NewTicker(d time.Duration) Ticker { return realTicker{time.NewTicker(d)} }

type realTicker struct{ t *time.Ticker }

func (r realTicker) C() <-chan time.Time { return r.t.C }
func (r realTicker) Stop()               { r.t.Stop() }
