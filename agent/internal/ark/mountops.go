package ark

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// This file is the seam of the two system calls the eject needs (ark-sync.md section 4.1.5 step 7):
// syncfs(2) and umount(2). The tests use a fake; the real one is mountops_linux.go. Nothing else in the
// package unmounts anything.

// MountOps flushes and unmounts a filesystem.
type MountOps interface {
	// Syncfs writes every dirty page of the filesystem that holds mount to the disk (syncfs(2)).
	Syncfs(mount string) error
	// Unmount detaches the filesystem mounted at mount (umount(2) with no flags: never lazy, never
	// forced). A filesystem that is still in use fails with an error satisfying errors.Is(err,
	// syscall.EBUSY).
	Unmount(mount string) error
}

// ErrMountOpsUnsupported is what OSMountOps answers on a platform without syncfs and umount.
var ErrMountOpsUnsupported = errors.New("ark: unmounting is not supported on this platform")

// SysFS is the part of sysfs the whole-disk lookup reads. OSSys is the real one.
type SysFS interface {
	// EvalSymlinks resolves path through its symbolic links (filepath.EvalSymlinks).
	EvalSymlinks(path string) (string, error)
	// Exists reports whether path exists.
	Exists(path string) bool
}

// OSSys is the real SysFS.
type OSSys struct{}

// EvalSymlinks implements SysFS.
func (OSSys) EvalSymlinks(path string) (string, error) { return filepath.EvalSymlinks(path) }

// Exists implements SysFS.
func (OSSys) Exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// SysClassBlock is the sysfs directory with one symbolic link per block device.
const SysClassBlock = "/sys/class/block"

// WholeDisk returns the device node of the disk that holds dev ("/dev/sdb1" gives "/dev/sdb"; the same for
// "/dev/nvme1n1p1" and "/dev/mmcblk0p1"), found the way the kernel lays sysfs out: /sys/class/block/<name>
// links to the device's directory, and a partition's directory has a "partition" file and sits inside its
// disk's directory. It returns "" when the answer is not a physical disk that can be powered off: a
// device-mapper or LUKS volume, an md array or a loop device live under /sys/devices/virtual, a path that
// is not a /dev node, or a name sysfs does not know.
func WholeDisk(sys SysFS, dev string) string {
	if !strings.HasPrefix(dev, "/dev/") || strings.ContainsRune(dev, 0) {
		return ""
	}
	name := filepath.Base(dev)
	if name == "." || name == "/" || name == "dev" {
		return ""
	}
	p, err := sys.EvalSymlinks(filepath.Join(SysClassBlock, name))
	if err != nil {
		return ""
	}
	p = filepath.ToSlash(p)
	if strings.Contains(p, "/devices/virtual/") {
		return ""
	}
	if sys.Exists(p + "/partition") {
		return "/dev/" + filepath.Base(filepath.Dir(p))
	}
	return "/dev/" + filepath.Base(p)
}
