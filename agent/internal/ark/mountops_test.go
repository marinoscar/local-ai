package ark_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/ark"
)

// fakeSys is sysfs: /sys/class/block links, and the set of paths that exist.
type fakeSys struct {
	links map[string]string
	files map[string]bool
}

func (f fakeSys) EvalSymlinks(p string) (string, error) {
	v, ok := f.links[p]
	if !ok || v == "" {
		return "", errors.New("no such sysfs entry")
	}
	return v, nil
}

func (f fakeSys) Exists(p string) bool { return f.files[p] }

// newFakeSys lays the devices out the way the kernel does: the link of a partition ends in
// .../<disk>/<partition> and the partition's directory has a "partition" file; a whole disk has none.
func newFakeSys() fakeSys {
	usb := "/sys/devices/pci0000:00/0000:00:14.0/usb2/2-1/2-1:1.0/host6/target6:0:0/6:0:0:0/block/"
	nvme := "/sys/devices/pci0000:00/0000:00:1d.0/0000:3d:00.0/nvme/nvme1/"
	mmc := "/sys/devices/pci0000:00/0000:00:1a.0/mmc_host/mmc0/mmc0:0001/block/"
	links := map[string]string{
		"/sys/class/block/sdb1":       usb + "sdb/sdb1",
		"/sys/class/block/sdb":        usb + "sdb",
		"/sys/class/block/sdc1":       usb + "sdc/sdc1",
		"/sys/class/block/nvme1n1":    nvme + "nvme1n1",
		"/sys/class/block/nvme1n1p1":  nvme + "nvme1n1/nvme1n1p1",
		"/sys/class/block/mmcblk0p1":  mmc + "mmcblk0/mmcblk0p1",
		"/sys/class/block/dm-0":       "/sys/devices/virtual/block/dm-0",
		"/sys/class/block/loop3":      "/sys/devices/virtual/block/loop3",
		"/sys/class/block/md0":        "/sys/devices/virtual/block/md0",
		"/sys/class/block/unreadable": "",
	}
	files := map[string]bool{}
	for _, name := range []string{"sdb1", "sdc1", "nvme1n1p1", "mmcblk0p1"} {
		files[links["/sys/class/block/"+name]+"/partition"] = true
	}
	return fakeSys{links: links, files: files}
}

// TestWholeDisk: the disk that holds a partition, found the way the kernel lays sysfs out; nothing for a
// volume that is not a physical disk.
func TestWholeDisk(t *testing.T) {
	sys := newFakeSys()
	for _, tc := range []struct{ dev, want string }{
		{"/dev/sdb1", "/dev/sdb"},
		{"/dev/sdb", "/dev/sdb"},
		{"/dev/sdc1", "/dev/sdc"},
		{"/dev/nvme1n1p1", "/dev/nvme1n1"},
		{"/dev/nvme1n1", "/dev/nvme1n1"}, // the namespace is the disk, not its controller
		{"/dev/mmcblk0p1", "/dev/mmcblk0"},
		{"/dev/dm-0", ""},         // LUKS or LVM: nothing to power off
		{"/dev/loop3", ""},        // a loop device
		{"/dev/md0", ""},          // a software RAID
		{"/dev/missing", ""},      // sysfs does not know it
		{"/dev/unreadable", ""},   // the link cannot be resolved
		{"/dev/mapper/crypt", ""}, // not in sysfs under that name
		{"sdb1", ""},              // not a /dev node
		{"/tmp/sdb1", ""},         // not a /dev node
		{"/dev/", ""},             // no name
		{"/dev/sdb1\x00x", ""},    // a NUL byte
		{"", ""},
	} {
		if got := ark.WholeDisk(sys, tc.dev); got != tc.want {
			t.Errorf("WholeDisk(%q) = %q, want %q", tc.dev, got, tc.want)
		}
	}
}

// TestOSSysResolvesRealLinks: the real SysFS follows a symbolic link and reports existence.
func TestOSSysResolvesRealLinks(t *testing.T) {
	tmp := t.TempDir()
	target := tmp + "/devices/block/sdb"
	writeFile(t, target+"/partition", "1\n")
	if got, err := (ark.OSSys{}).EvalSymlinks(target + "/../sdb"); err != nil || !strings.HasSuffix(got, "/devices/block/sdb") {
		t.Errorf("EvalSymlinks = %q, %v", got, err)
	}
	if !(ark.OSSys{}).Exists(target+"/partition") || (ark.OSSys{}).Exists(target+"/nope") {
		t.Error("Exists is wrong")
	}
}
