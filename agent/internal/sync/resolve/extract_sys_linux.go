//go:build linux

package resolve

import (
	"errors"
	"syscall"
)

// isNoSpace is ENOSPC or EDQUOT: the disk or the user's quota is full.
func isNoSpace(err error) bool {
	return errors.Is(err, syscall.ENOSPC) || errors.Is(err, syscall.EDQUOT)
}
