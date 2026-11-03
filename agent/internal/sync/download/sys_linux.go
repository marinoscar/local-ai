//go:build linux

package download

import (
	"errors"
	"os"
	"syscall"
)

// fdatasync flushes the file's data (not its metadata beyond the size), the call ark-sync.md
// section 4.6 names for a checkpoint.
func fdatasync(f *os.File) error {
	for {
		err := syscall.Fdatasync(int(f.Fd()))
		if !errors.Is(err, syscall.EINTR) {
			return err
		}
	}
}

// isSyncDirUnsupported: some filesystems refuse fsync on a directory (EINVAL); the rename is as
// durable as the filesystem makes it.
func isSyncDirUnsupported(err error) bool { return errors.Is(err, syscall.EINVAL) }

// isNoSpace is ENOSPC or EDQUOT: the disk or the user's quota is full.
func isNoSpace(err error) bool {
	return errors.Is(err, syscall.ENOSPC) || errors.Is(err, syscall.EDQUOT)
}

// isIO is EIO: the drive failed a read or write.
func isIO(err error) bool { return errors.Is(err, syscall.EIO) }

// isReadOnlyFS is EROFS: the filesystem was remounted read-only (errors=remount-ro).
func isReadOnlyFS(err error) bool { return errors.Is(err, syscall.EROFS) }

// isDenied is EACCES or EPERM: the agent may not write there.
func isDenied(err error) bool {
	return errors.Is(err, syscall.EACCES) || errors.Is(err, syscall.EPERM)
}

// statfsFree is the real FreeFunc: the size of the filesystem holding path and the bytes a non-root
// process may still use (Bavail times Bsize).
func statfsFree(path string) (total, free int64, err error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0, err
	}
	bsize := int64(st.Bsize) //nolint:unconvert // the field type differs between architectures
	return int64(st.Blocks) * bsize, int64(st.Bavail) * bsize, nil
}
