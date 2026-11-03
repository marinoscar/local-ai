package kit

import (
	"errors"
	"io"
	"io/fs"
	"os"

	"github.com/marinoscar/local-ai/agent/internal/config"
)

// VerifyFS is the disk the verifier reads: the filesystem of the installers plus DropCache. The
// verifier never uses it directly: it wraps it in a read-only view first (ReadOnly), so that no code
// path of a verification can write, rename or delete a byte of the kit, whatever the FS behind it is.
type VerifyFS interface {
	FS
	// DropCache asks the kernel to forget the cached pages of a file (posix_fadvise DONTNEED); best
	// effort. OSFS implements it.
	DropCache(name string) error
}

var _ VerifyFS = OSFS{}

// sequentialOpener is implemented by an FS that can say "one sequential read" to the kernel.
type sequentialOpener interface {
	OpenSequential(name string) (io.ReadCloser, error)
}

// cacheDropper is implemented by an FS that can drop the page cache of a file.
type cacheDropper interface {
	DropCache(name string) error
}

// ErrReadOnly is what every write through a read-only view returns: a verification never changes the
// kit (rebuild-kit.md section 4.4; sync's quarantine path is the only thing that moves a damaged file).
var ErrReadOnly = errors.New("kit: verification never writes")

// ReadOnly wraps fsys so that every write fails with ErrReadOnly: a file opened for writing, a
// rename, a removal, a chmod or chown, a directory sync, a folder created or removed, a symbolic link.
// Reads, listings and DropCache pass through.
func ReadOnly(fsys VerifyFS) VerifyFS { return readOnlyFS{fsys} }

type readOnlyFS struct{ VerifyFS }

func denied(op, name string) error { return &fs.PathError{Op: op, Path: name, Err: ErrReadOnly} }

func (r readOnlyFS) OpenFile(name string, flag int, perm os.FileMode) (config.File, error) {
	if flag&(os.O_WRONLY|os.O_RDWR|os.O_APPEND|os.O_CREATE|os.O_TRUNC) != 0 {
		return nil, denied("open", name)
	}
	return r.VerifyFS.OpenFile(name, flag, perm)
}
func (readOnlyFS) Rename(oldpath, _ string) error            { return denied("rename", oldpath) }
func (readOnlyFS) Remove(name string) error                  { return denied("remove", name) }
func (readOnlyFS) Chmod(name string, _ os.FileMode) error    { return denied("chmod", name) }
func (readOnlyFS) Chown(name string, _, _ int) error         { return denied("chown", name) }
func (readOnlyFS) SyncDir(dir string) error                  { return denied("sync", dir) }
func (readOnlyFS) MkdirAll(path string, _ os.FileMode) error { return denied("mkdir", path) }
func (readOnlyFS) RemoveAll(path string) error               { return denied("remove", path) }
func (readOnlyFS) Symlink(_, newname string) error           { return denied("symlink", newname) }

// OpenSequential passes the sequential-read hint of the FS behind the view, when it has one.
func (r readOnlyFS) OpenSequential(name string) (io.ReadCloser, error) {
	return openForHash(r.VerifyFS, name)
}

// openForHash opens name for one sequential read.
func openForHash(fsys FS, name string) (io.ReadCloser, error) {
	if so, ok := fsys.(sequentialOpener); ok {
		return so.OpenSequential(name)
	}
	return fsys.Open(name)
}

// dropCache is best effort: a failed fadvise only means the next read may come from the cache.
func dropCache(fsys FS, name string) {
	if d, ok := fsys.(cacheDropper); ok {
		_ = d.DropCache(name)
	}
}
