//go:build !linux

package resolve

import (
	"errors"
	"syscall"
)

// isNoSpace is ENOSPC: the disk is full.
func isNoSpace(err error) bool { return errors.Is(err, syscall.ENOSPC) }
