//go:build !linux

package host

import (
	"errors"
	"os"
)

// The migration only runs on Linux; this keeps other platforms building.
func devOf(os.FileInfo) (uint64, error) { return 0, errors.New("host: only supported on linux") }

func freeBytes(string) (uint64, error) { return 0, errors.New("host: only supported on linux") }
