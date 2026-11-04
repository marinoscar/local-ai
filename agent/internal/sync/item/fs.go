package item

import (
	"errors"
	"github.com/marinoscar/local-ai/agent/internal/fsperm"
	"io"
	"os"

	"github.com/marinoscar/local-ai/agent/internal/config"
)

// ErrExchangeUnsupported: the platform or the filesystem has no renameat2(RENAME_EXCHANGE). A
// directory item cannot be swapped atomically there, so it is failed and the old copy is kept.
var ErrExchangeUnsupported = errors.New("item: RENAME_EXCHANGE is not supported here")

// FS is the filesystem seam of the item state machine. It is config.FS (the atomic writer takes it)
// plus the four things a swap and a verification need that config.FS has not: directories, a streamed
// read, the cache drop of ark-sync.md section 4.5 and renameat2(RENAME_EXCHANGE). OSFS is the real
// one; the tests wrap it to record the order of operations and to stop the "process" at any one.
//
// Deletion never goes through here: the machine moves things to .trash and quarantine, and
// internal/sync/retention deletes (it is the only package allowed to).
type FS interface {
	config.FS
	// MkdirAll creates a directory and its parents.
	MkdirAll(path string, perm os.FileMode) error
	// OpenRead opens a file for a streamed read.
	OpenRead(name string) (io.ReadCloser, error)
	// DropCache asks the kernel to forget the cached pages of a file (posix_fadvise DONTNEED), so a
	// re-read comes from the disk. Best effort: an error is not a verification failure.
	DropCache(name string) error
	// Exchange swaps two paths atomically (renameat2 with RENAME_EXCHANGE). Both must exist.
	Exchange(oldpath, newpath string) error
}

// OSFS is the real filesystem.
type OSFS struct{ config.OSFS }

// MkdirAll implements FS.
func (OSFS) MkdirAll(path string, perm os.FileMode) error { return fsperm.MkdirAll(path, perm) }

// OpenRead implements FS.
func (OSFS) OpenRead(name string) (io.ReadCloser, error) { return os.Open(name) }

var _ FS = OSFS{}

// IsExchangeUnsupported reports whether err is ErrExchangeUnsupported.
func IsExchangeUnsupported(err error) bool { return errors.Is(err, ErrExchangeUnsupported) }
