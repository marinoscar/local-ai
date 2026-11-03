//go:build linux

package ark

import "syscall"

// statfsType is the real FSType: the f_type of statfs(2). The field is int64 on amd64 and arm64 and
// int32 on 32-bit ARM, so it is narrowed through uint32 (every magic of interest fits in 32 bits).
func statfsType(path string) (uint64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, err
	}
	return uint64(uint32(st.Type)), nil //nolint:unconvert // the field type differs between architectures
}
