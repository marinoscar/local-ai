//go:build !linux

package verify

import (
	"io"
	"os"
)

// OpenRead implements FS. There is no page-cache advice off Linux.
func (OSFS) OpenRead(name string) (io.ReadCloser, error) { return os.Open(name) }
