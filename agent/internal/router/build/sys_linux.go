//go:build linux

package build

import (
	"errors"
	"os"
	"syscall"
)

// osFreeBytes is the real FreeBytes: the bytes the filesystem holding path can still give (Bavail
// times Bsize).
func osFreeBytes(path string) (uint64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, err
	}
	return st.Bavail * uint64(st.Bsize), nil //nolint:unconvert // Bsize is int32 or int64 by platform
}

// errLockHeld says that another process holds the build lock.
var errLockHeld = errors.New("build: another build holds the lock")

// lockFile takes an exclusive, non-blocking flock on path (created when absent). The lock is
// released when the returned function is called or the process exits, so a crashed build never
// leaves a stale lock behind.
func lockFile(path string) (release func(), err error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, errLockHeld
		}
		return nil, err
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}
