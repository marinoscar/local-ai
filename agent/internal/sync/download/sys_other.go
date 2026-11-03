//go:build !linux

package download

import (
	"errors"
	"os"
)

// The agent only runs on Linux; these keep other platforms building.

func fdatasync(f *os.File) error { return f.Sync() }

func isSyncDirUnsupported(error) bool { return true }

func isNoSpace(error) bool { return false }

func isIO(error) bool { return false }

func isReadOnlyFS(error) bool { return false }

func isDenied(error) bool { return false }

func statfsFree(string) (int64, int64, error) {
	return 0, 0, errors.New("download: statfs is only supported on linux")
}
