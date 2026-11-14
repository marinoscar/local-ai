//go:build !linux

package music

// statfsFree is unknown off Linux: the disk guard is skipped there.
func statfsFree(string) (int64, error) { return -1, nil }
