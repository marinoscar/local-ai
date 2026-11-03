package update

import (
	"io"
	"os"

	"github.com/marinoscar/local-ai/agent/internal/config"
)

// FS is the filesystem seam of the update code: the atomic-write FS of internal/config plus what streaming a
// binary needs. OSFS is the real one; tests wrap a temp directory in a fault injecting FS.
type FS interface {
	config.FS
	// Open opens a file for reading.
	Open(name string) (io.ReadCloser, error)
	// MkdirAll creates a directory and its parents.
	MkdirAll(path string, perm os.FileMode) error
	// RemoveAll removes a path and everything below it.
	RemoveAll(path string) error
}

// OSFS is the real filesystem.
type OSFS struct{ config.OSFS }

// Open implements FS.
func (OSFS) Open(name string) (io.ReadCloser, error) { return os.Open(name) }

// MkdirAll implements FS.
func (OSFS) MkdirAll(path string, perm os.FileMode) error { return os.MkdirAll(path, perm) }

// RemoveAll implements FS.
func (OSFS) RemoveAll(path string) error { return os.RemoveAll(path) }

var _ FS = OSFS{}
