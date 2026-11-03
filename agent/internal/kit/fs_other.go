//go:build !linux

package kit

import (
	"io"
	"os"
)

// DropCache implements VerifyFS; there is no portable equivalent, so it does nothing.
func (OSFS) DropCache(string) error { return nil }

// OpenSequential opens a file for one sequential read.
func (OSFS) OpenSequential(name string) (io.ReadCloser, error) { return os.Open(name) }
