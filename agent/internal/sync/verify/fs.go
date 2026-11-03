package verify

import (
	"io"
	"os"

	"github.com/marinoscar/local-ai/agent/internal/config"
)

// FS is the filesystem seam of the re-verification: config.FS (manifests are rewritten with the atomic
// writer, directories are listed and fsynced) plus what a quarantine and a hash need. OSFS is the real
// one; the tests wrap it to inject bit rot, read errors, a full disk and a read-only root.
//
// Nothing here deletes: a copy that failed is moved into <root>/.staging/quarantine, and
// internal/sync/retention is the only package that removes anything (rule R1).
type FS interface {
	config.FS
	// MkdirAll creates a directory and its parents.
	MkdirAll(path string, perm os.FileMode) error
	// OpenRead opens a file for one sequential read. The real implementation asks the kernel for
	// POSIX_FADV_SEQUENTIAL and drops the pages it has read behind it (POSIX_FADV_DONTNEED), so
	// checking a drive does not push the weights of a loaded model out of the page cache (ark-sync.md
	// section 4.13).
	OpenRead(name string) (io.ReadCloser, error)
}

// OSFS is the real filesystem.
type OSFS struct{ config.OSFS }

// MkdirAll implements FS.
func (OSFS) MkdirAll(path string, perm os.FileMode) error { return os.MkdirAll(path, perm) }

var _ FS = OSFS{}
