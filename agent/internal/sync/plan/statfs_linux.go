//go:build linux

package plan

import "syscall"

// OSStatfs is the real StatfsFunc: the filesystem size and Bavail times Bsize, the bytes a process
// that is not root may still use (what sync can actually write).
func OSStatfs(path string) (total, free int64, err error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0, err
	}
	bsize := int64(st.Bsize) //nolint:unconvert // the field type differs between architectures
	return int64(st.Blocks) * bsize, int64(st.Bavail) * bsize, nil
}
