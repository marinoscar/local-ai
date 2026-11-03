//go:build !linux

package build

import "errors"

// The agent only runs on Linux; these keep other platforms building.

func osFreeBytes(string) (uint64, error) {
	return 0, errors.New("build: free space is only read on Linux")
}

var errLockHeld = errors.New("build: another build holds the lock")

func lockFile(string) (func(), error) { return func() {}, nil }
