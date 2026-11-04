//go:build !linux

package ark

// OSStatfs is the real StatfsFunc on Linux; elsewhere the size is unknown.
func OSStatfs(string) (size, free int64, err error) { return 0, 0, ErrFSTypeUnsupported }
