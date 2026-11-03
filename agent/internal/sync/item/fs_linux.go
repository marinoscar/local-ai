//go:build linux

package item

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// DropCache implements FS with posix_fadvise(POSIX_FADV_DONTNEED).
func (OSFS) DropCache(name string) error {
	f, err := os.Open(name)
	if err != nil {
		return err
	}
	defer f.Close()
	return unix.Fadvise(int(f.Fd()), 0, 0, unix.FADV_DONTNEED)
}

// Exchange implements FS with renameat2(RENAME_EXCHANGE).
func (OSFS) Exchange(oldpath, newpath string) error {
	err := unix.Renameat2(unix.AT_FDCWD, oldpath, unix.AT_FDCWD, newpath, unix.RENAME_EXCHANGE)
	if errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EINVAL) || errors.Is(err, unix.ENOTSUP) {
		return ErrExchangeUnsupported
	}
	if err != nil {
		return &os.LinkError{Op: "exchange", Old: oldpath, New: newpath, Err: err}
	}
	return nil
}
