//go:build linux

package kit

import (
	"io"
	"os"

	"golang.org/x/sys/unix"
)

// DropCache implements VerifyFS with posix_fadvise(POSIX_FADV_DONTNEED): the kernel forgets the
// cached pages of the file, so the next read comes from the disk (a re-hash that read the page cache
// would prove nothing about the disk) and a verification does not evict the cache of a loaded model
// (ark-sync.md section 4.13). It opens the file read-only.
func (OSFS) DropCache(name string) error {
	f, err := os.Open(name)
	if err != nil {
		return err
	}
	defer f.Close()
	return unix.Fadvise(int(f.Fd()), 0, 0, unix.FADV_DONTNEED)
}

// OpenSequential opens a file for one sequential read and says so to the kernel (POSIX_FADV_SEQUENTIAL).
func (OSFS) OpenSequential(name string) (io.ReadCloser, error) {
	f, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	_ = unix.Fadvise(int(f.Fd()), 0, 0, unix.FADV_SEQUENTIAL)
	return f, nil
}
