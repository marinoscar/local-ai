//go:build unix

package run

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func flockExclusive(f *os.File) error {
	for {
		err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if !errors.Is(err, unix.EINTR) {
			return err
		}
	}
}

func flockRelease(f *os.File) error { return unix.Flock(int(f.Fd()), unix.LOCK_UN) }

func isWouldBlock(err error) bool {
	return errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN)
}
