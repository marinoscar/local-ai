//go:build linux

package host

import (
	"errors"
	"os"
	"syscall"
)

func devOf(fi os.FileInfo) (uint64, error) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, errors.New("host: the file system does not report a device number")
	}
	return uint64(st.Dev), nil //nolint:unconvert // the field type differs between architectures
}

func freeBytes(path string) (uint64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, err
	}
	return uint64(st.Bavail) * uint64(st.Bsize), nil //nolint:unconvert // the field types differ between architectures
}
