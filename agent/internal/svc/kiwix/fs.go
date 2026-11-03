package kiwix

import (
	"io"
	"os"
)

// FS is the slice of the filesystem the installer uses. It is a seam: the real one is OSFS, tests
// wrap it to inject a full disk or a failing rename. Everything the installer writes goes through
// it; nothing else in the package touches the disk.
type FS interface {
	// Open opens a file for reading and seeking (the kit tarball).
	Open(name string) (ReadFile, error)
	// OpenFile creates or opens a file for writing.
	OpenFile(name string, flag int, perm os.FileMode) (File, error)
	ReadFile(name string) ([]byte, error)
	MkdirAll(path string, perm os.FileMode) error
	Lstat(name string) (os.FileInfo, error)
	Readlink(name string) (string, error)
	Symlink(oldname, newname string) error
	Rename(oldpath, newpath string) error
	RemoveAll(path string) error
	// SyncDir fsyncs a directory so a rename or a create inside it is durable.
	SyncDir(dir string) error
}

// ReadFile is a file opened for reading.
type ReadFile interface {
	io.Reader
	io.Seeker
	io.Closer
}

// File is a file opened for writing.
type File interface {
	io.Writer
	io.Closer
	Chmod(mode os.FileMode) error
	Sync() error
}

// OSFS is the real filesystem.
type OSFS struct{}

// Open implements FS.
func (OSFS) Open(name string) (ReadFile, error) { return os.Open(name) }

// OpenFile implements FS.
func (OSFS) OpenFile(name string, flag int, perm os.FileMode) (File, error) {
	return os.OpenFile(name, flag, perm)
}

// ReadFile implements FS.
func (OSFS) ReadFile(name string) ([]byte, error) { return os.ReadFile(name) }

// MkdirAll implements FS.
func (OSFS) MkdirAll(path string, perm os.FileMode) error { return os.MkdirAll(path, perm) }

// Lstat implements FS.
func (OSFS) Lstat(name string) (os.FileInfo, error) { return os.Lstat(name) }

// Readlink implements FS.
func (OSFS) Readlink(name string) (string, error) { return os.Readlink(name) }

// Symlink implements FS.
func (OSFS) Symlink(oldname, newname string) error { return os.Symlink(oldname, newname) }

// Rename implements FS.
func (OSFS) Rename(oldpath, newpath string) error { return os.Rename(oldpath, newpath) }

// RemoveAll implements FS.
func (OSFS) RemoveAll(path string) error { return os.RemoveAll(path) }

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
