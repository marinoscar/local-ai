//go:build !linux

package ark

// OSMountOps is the MountOps of a platform that cannot unmount; every call fails.
type OSMountOps struct{}

// Syncfs implements MountOps.
func (OSMountOps) Syncfs(string) error { return ErrMountOpsUnsupported }

// Unmount implements MountOps.
func (OSMountOps) Unmount(string) error { return ErrMountOpsUnsupported }
