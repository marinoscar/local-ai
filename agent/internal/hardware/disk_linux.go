//go:build linux

package hardware

import "syscall"

// osStatfs is the real StatfsFunc: free is Bavail times Bsize (what a non-root process can use).
func osStatfs(path string) (total, free int64, err error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0, err
	}
	bsize := int64(st.Bsize) //nolint:unconvert // the field type differs between architectures
	return int64(st.Blocks) * bsize, int64(st.Bavail) * bsize, nil
}
