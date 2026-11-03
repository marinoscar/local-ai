package build

import (
	"io"
	"os"
	"time"
)

// File is a file opened for reading: the ELF check needs ReadAt, the copy needs Read.
type File interface {
	io.Reader
	io.ReaderAt
	io.Closer
}

// FS is the filesystem seam of the build manager (agent.md section 3.2: every external dependency
// sits behind an interface). OSFS is the real one; tests wrap it to inject faults (a rename or a
// symlink that fails, a disk that fills up).
type FS interface {
	MkdirAll(path string, perm os.FileMode) error
	RemoveAll(path string) error
	Rename(oldpath, newpath string) error
	Symlink(target, link string) error
	Readlink(path string) (string, error)
	ReadFile(path string) ([]byte, error)
	WriteFile(path string, data []byte, perm os.FileMode) error
	Open(path string) (File, error)
	// Create truncates or creates the file with exactly perm (the umask does not apply).
	Create(path string, perm os.FileMode) (io.WriteCloser, error)
	ReadDir(path string) ([]os.DirEntry, error)
	Stat(path string) (os.FileInfo, error)
	Lstat(path string) (os.FileInfo, error)
	Chtimes(path string, atime, mtime time.Time) error
}

// OSFS is the real filesystem.
type OSFS struct{}

var _ FS = OSFS{}

// MkdirAll implements FS.
func (OSFS) MkdirAll(path string, perm os.FileMode) error { return os.MkdirAll(path, perm) }

// RemoveAll implements FS.
func (OSFS) RemoveAll(path string) error { return os.RemoveAll(path) }

// Rename implements FS.
func (OSFS) Rename(oldpath, newpath string) error { return os.Rename(oldpath, newpath) }

// Symlink implements FS.
func (OSFS) Symlink(target, link string) error { return os.Symlink(target, link) }

// Readlink implements FS.
func (OSFS) Readlink(path string) (string, error) { return os.Readlink(path) }

// ReadFile implements FS.
func (OSFS) ReadFile(path string) ([]byte, error) { return os.ReadFile(path) }

// WriteFile implements FS.
func (OSFS) WriteFile(path string, data []byte, perm os.FileMode) error {
	if err := os.WriteFile(path, data, perm); err != nil {
		return err
	}
	return os.Chmod(path, perm)
}

// Open implements FS.
func (OSFS) Open(path string) (File, error) { return os.Open(path) }

// Create implements FS.
func (OSFS) Create(path string, perm os.FileMode) (io.WriteCloser, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
	if err != nil {
		return nil, err
	}
	if err := f.Chmod(perm); err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}

// ReadDir implements FS.
func (OSFS) ReadDir(path string) ([]os.DirEntry, error) { return os.ReadDir(path) }

// Stat implements FS.
func (OSFS) Stat(path string) (os.FileInfo, error) { return os.Stat(path) }

// Lstat implements FS.
func (OSFS) Lstat(path string) (os.FileInfo, error) { return os.Lstat(path) }

// Chtimes implements FS.
func (OSFS) Chtimes(path string, atime, mtime time.Time) error { return os.Chtimes(path, atime, mtime) }
