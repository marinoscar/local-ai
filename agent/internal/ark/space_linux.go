//go:build linux

package ark

import "syscall"

// OSStatfs is the real StatfsFunc: statfs(2) on path. size is blocks x block size and free is the
// space available to an unprivileged writer (f_bavail), as the issue asks for.
func OSStatfs(path string) (size, free int64, err error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0, err
	}
	bs := int64(st.Bsize)                                    //nolint:unconvert // the field type differs between architectures
	return int64(st.Blocks) * bs, int64(st.Bavail) * bs, nil //nolint:unconvert
}
