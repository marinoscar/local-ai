package prep

import (
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Label is the filesystem label of every ark drive (ark-sync.md section 4.1.2).
const Label = "AI-ARK"

// Swap is how lsblk shows an active swap in MOUNTPOINTS.
const Swap = "[SWAP]"

// DefaultSystemPaths are the directories whose disk is never offered or touched: the system itself,
// and the Local AI data directories of overview.md section 3 (the internal content root, state,
// configuration and binaries). A mount point that is one of them, or above one of them ("/"), makes
// its disk a system disk.
var DefaultSystemPaths = []string{
	"/", "/boot", "/boot/efi", "/usr", "/var", "/home",
	"/srv/localai", "/var/lib/localai", "/etc/localai", "/opt/localai",
}

// SystemPaths returns paths plus, for each one that is a symbolic link, where it points (the internal
// root may be a link to a bigger disk, ark-sync.md section 4.1.1). resolve is filepath.EvalSymlinks in
// the command and a map in tests; nil resolves nothing.
func SystemPaths(paths []string, resolve func(string) (string, error)) []string {
	out := append([]string(nil), paths...)
	if resolve == nil {
		return out
	}
	for _, p := range paths {
		if r, err := resolve(p); err == nil && r != "" && filepath.Clean(r) != filepath.Clean(p) {
			out = append(out, filepath.Clean(r))
		}
	}
	return out
}

var deviceRe = regexp.MustCompile(`^/dev/[A-Za-z0-9][A-Za-z0-9_-]*$`)

// ValidDevice accepts only a plain device node directly under /dev (sdb, sdb1, nvme1n1p1, mmcblk0p1).
// It is checked again by every Tools method, whatever the caller checked.
func ValidDevice(p string) bool { return deviceRe.MatchString(p) }

// Reason says why a disk is not offered.
type Reason string

// The reasons, in the order Classify checks them.
const (
	// ReasonBadPath: lsblk named it with a path that is not a plain /dev node.
	ReasonBadPath Reason = "bad_path"
	// ReasonSystem: it holds /, /boot, swap or a Local AI data directory. The computer's own disk.
	ReasonSystem Reason = "system_disk"
	// ReasonMounted: one of its filesystems is mounted (a desktop opened it).
	ReasonMounted Reason = "mounted"
	// ReasonInUse: something sits on top of it (device mapper, LUKS, RAID, LVM).
	ReasonInUse Reason = "in_use"
	// ReasonInternal: it is neither removable nor on USB, so it is a disk inside the computer.
	ReasonInternal Reason = "not_removable"
	// ReasonNoMedia: it has no size (a card reader with no card).
	ReasonNoMedia Reason = "no_media"
)

// Refused is a disk that is not offered, and why.
type Refused struct {
	Disk   BlockDevice
	Reason Reason
	// Mount is the first mount point that made it refused (ReasonSystem, ReasonMounted).
	Mount string
}

// isSystemMount reports whether mount point m is swap, one of the system paths, or above one.
func isSystemMount(m string, system []string) bool {
	if m == Swap {
		return true
	}
	if !strings.HasPrefix(m, "/") {
		return false
	}
	m = filepath.Clean(m)
	for _, p := range system {
		p = filepath.Clean(p)
		if p == m || m == "/" || strings.HasPrefix(p, m+"/") {
			return true
		}
	}
	return false
}

// systemMount returns the first mount point of d (or below) that makes it a system disk.
func systemMount(d BlockDevice, system []string) string {
	for _, m := range allMounts(d) {
		if isSystemMount(m, system) {
			return m
		}
	}
	return ""
}

// stacked reports whether a non-partition device sits anywhere below d (dm, crypt, lvm, raid, md).
func stacked(d BlockDevice) bool {
	for _, c := range d.Children {
		if c.Type != "part" || stacked(c) {
			return true
		}
	}
	return false
}

// Classify splits the whole disks of devs into those that may be formatted and those that may not.
// Devices that are not disks (loop, rom, a partition at top level) are left out of both. Both lists are
// sorted by path. The checks, in order: bad path, system disk, mounted, stacked, not removable, no
// media. Only a disk that passes all of them is offered.
func Classify(devs []BlockDevice, system []string) (offered []BlockDevice, refused []Refused) {
	for _, d := range devs {
		if d.Type != "disk" {
			continue
		}
		if r, m := refuseReason(d, system); r != "" {
			refused = append(refused, Refused{Disk: d, Reason: r, Mount: m})
			continue
		}
		offered = append(offered, d)
	}
	sort.Slice(offered, func(i, j int) bool { return offered[i].Path < offered[j].Path })
	sort.Slice(refused, func(i, j int) bool { return refused[i].Disk.Path < refused[j].Disk.Path })
	return offered, refused
}

func refuseReason(d BlockDevice, system []string) (Reason, string) {
	if !ValidDevice(d.Path) {
		return ReasonBadPath, ""
	}
	if m := systemMount(d, system); m != "" {
		return ReasonSystem, m
	}
	if ms := allMounts(d); len(ms) > 0 {
		return ReasonMounted, ms[0]
	}
	if stacked(d) {
		return ReasonInUse, ""
	}
	if !d.Removable && !d.Hotplug && d.Tran != "usb" {
		return ReasonInternal, ""
	}
	if d.Size <= 0 {
		return ReasonNoMedia, ""
	}
	return "", ""
}

// SameDisk reports whether b is still the disk a was when it was offered: same path, size, model and
// serial. The format checks it again just before it erases anything, so a disk swapped between the
// list and the answer is never erased.
func SameDisk(a, b BlockDevice) bool {
	return a.Path == b.Path && a.Size == b.Size && a.Model == b.Model && a.Serial == b.Serial && a.Type == "disk" && b.Type == "disk"
}

// DriveProblem says why an existing AI-ARK filesystem cannot be repaired or given a new id here.
type DriveProblem string

// The answers of CheckDrive.
const (
	DriveOK DriveProblem = ""
	// DriveUnknown: lsblk does not list the device.
	DriveUnknown DriveProblem = "unknown_device"
	// DriveNotArk: it is not an ext4 filesystem labelled AI-ARK.
	DriveNotArk DriveProblem = "not_ark"
	// DriveSystem: its disk holds the system or Local AI data (or the device path is not plain).
	DriveSystem DriveProblem = "system_disk"
	// DriveOtherMounts: another filesystem of the same disk is mounted.
	DriveOtherMounts DriveProblem = "other_mounts"
)

// CheckDrive checks an existing AI-ARK filesystem before repair or new-id touches it: dev must be listed
// by lsblk, ext4, labelled AI-ARK, and its disk must not be a system disk nor have any other filesystem
// mounted (its own mount point, ownMount, is allowed; "" allows none).
func CheckDrive(devs []BlockDevice, dev, ownMount string, system []string) DriveProblem {
	if !ValidDevice(dev) {
		return DriveSystem
	}
	d, disk, ok := Find(devs, dev)
	if !ok {
		return DriveUnknown
	}
	// The system check first: the computer's own disk is refused as such, whatever its label.
	if systemMount(disk, system) != "" || !ValidDevice(disk.Path) {
		return DriveSystem
	}
	if d.FSType != "ext4" || d.Label != Label {
		return DriveNotArk
	}
	for _, m := range allMounts(disk) {
		if ownMount == "" || filepath.Clean(m) != filepath.Clean(ownMount) {
			return DriveOtherMounts
		}
	}
	return DriveOK
}
