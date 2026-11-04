package download

import (
	"github.com/marinoscar/local-ai/agent/internal/fsperm"
	"io"
	"os"
)

// FS is the filesystem seam of the downloader. It is deliberately small and has no Remove: the
// downloader never deletes anything (ark-sync.md section 4.8: a run never deletes a .part or a
// verified copy to make room), it truncates its own .part and renames it, and that is all. Tests
// wrap OSFS to inject ENOSPC, EIO and a kill.
type FS interface {
	// MkdirAll creates a directory and its parents.
	MkdirAll(path string, perm os.FileMode) error
	// OpenPart opens (creating it when missing, never truncating) the .part file for reading and
	// writing, mode 0600 until the item is swapped into place.
	OpenPart(name string) (PartFile, error)
	// OpenRead opens an existing file for reading (the source of a local copy).
	OpenRead(name string) (ReadFile, error)
	// Stat returns the file's info (following symlinks).
	Stat(name string) (os.FileInfo, error)
	// Rename moves a file within one filesystem, replacing the destination atomically.
	Rename(oldpath, newpath string) error
	// SyncDir fsyncs a directory so a rename inside it is durable.
	SyncDir(dir string) error
}

// PartFile is the .part file being written.
type PartFile interface {
	io.Writer
	io.Seeker
	// Truncate sets the length of the file.
	Truncate(size int64) error
	// Sync makes the written data durable (fdatasync).
	Sync() error
	Close() error
}

// ReadFile is a file read for a local copy.
type ReadFile interface {
	io.Reader
	io.Seeker
	io.Closer
}

// OSFS is the real filesystem.
type OSFS struct{}

// MkdirAll implements FS.
func (OSFS) MkdirAll(path string, perm os.FileMode) error { return fsperm.MkdirAll(path, perm) }

// OpenPart implements FS.
func (OSFS) OpenPart(name string) (PartFile, error) {
	f, err := os.OpenFile(name, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	return osPart{f}, nil
}

// OpenRead implements FS.
func (OSFS) OpenRead(name string) (ReadFile, error) { return os.Open(name) }

// Stat implements FS.
func (OSFS) Stat(name string) (os.FileInfo, error) { return os.Stat(name) }

// Rename implements FS.
func (OSFS) Rename(oldpath, newpath string) error { return os.Rename(oldpath, newpath) }

// SyncDir implements FS.
func (OSFS) SyncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	if err := d.Sync(); err != nil && !isSyncDirUnsupported(err) {
		return err
	}
	return nil
}

// osPart is *os.File with Sync replaced by fdatasync (fdatasync_linux.go).
type osPart struct{ *os.File }

func (p osPart) Sync() error { return fdatasync(p.File) }
