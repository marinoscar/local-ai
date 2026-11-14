//go:build linux

package music

import (
	"os"
	"path/filepath"
	"syscall"
)

// statfsFree is the space an unprivileged writer may use on the filesystem that holds path (or its
// nearest existing parent).
func statfsFree(path string) (int64, error) {
	for {
		if _, err := os.Stat(path); err == nil {
			break
		}
		parent := filepath.Dir(path)
		if parent == path {
			break
		}
		path = parent
	}
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return -1, err
	}
	return int64(st.Bavail) * int64(st.Bsize), nil
}
