//go:build !linux

package ark

func statfsType(string) (uint64, error) { return 0, ErrFSTypeUnsupported }
