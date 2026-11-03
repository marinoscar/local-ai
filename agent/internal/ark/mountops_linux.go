//go:build linux

package ark

import (
	"os"

	"golang.org/x/sys/unix"
)

// OSMountOps is the real MountOps.
type OSMountOps struct{}

// Syncfs implements MountOps.
func (OSMountOps) Syncfs(mount string) error {
	f, err := os.Open(mount)
	if err != nil {
		return err
	}
	defer f.Close()
	return unix.Syncfs(int(f.Fd()))
}

// Unmount implements MountOps.
func (OSMountOps) Unmount(mount string) error { return unix.Unmount(mount, 0) }
