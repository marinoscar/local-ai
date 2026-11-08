// Package prep prepares, repairs and re-identifies an AI-ARK drive (ark-sync.md section 4.17, issue
// #434): the disk work behind `aictl ark format`, `aictl ark repair` and `aictl ark new-id`.
//
// Safety rules, each one tested:
//
//   - Every disk program runs with a fixed argv through the Tools interface (ExecTools over
//     execx.Runner and execx.Streamer); tests use a fake and never touch a disk.
//   - A device path is never typed by a person: it comes from the JSON of lsblk (ParseLsblk), and
//     every Tools method refuses a path that is not a plain /dev/<name> node (ValidDevice).
//   - Offered (Classify) lists only whole disks that are removable or on USB, that hold no mounted
//     filesystem and no swap, that carry no active device-mapper, RAID or LVM child, and that are not
//     the disk of /, /boot, swap or a Local AI data directory. The computer's own disk is never offered.
//
// The package decides nothing about people: the questions (the number, the admin PIN, the word ERASE)
// are aictl's (internal/cli/cmd_ark_prep.go).
package prep

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// LsblkColumns are the columns ListBlock asks lsblk for, in this order.
const LsblkColumns = "NAME,PATH,TYPE,SIZE,MODEL,SERIAL,TRAN,RM,HOTPLUG,LABEL,FSTYPE,UUID,MOUNTPOINTS"

// BlockDevice is one node of `lsblk --json --bytes --output LsblkColumns`, with its children
// (partitions, and what sits on them: crypt, lvm, raid).
type BlockDevice struct {
	Name      string
	Path      string
	Type      string // disk, part, crypt, lvm, raid1, loop, rom, ...
	Size      int64
	Model     string
	Serial    string
	Tran      string // usb, sata, nvme, ...
	Removable bool
	Hotplug   bool
	Label     string
	FSType    string
	UUID      string
	// Mountpoints are the non-empty mount points ("[SWAP]" for an active swap).
	Mountpoints []string
	Children    []BlockDevice
}

// lsblkFlex accepts what different util-linux versions print: a boolean as true/false or "1"/"0", a
// number as a number or a string, a null string, a single MOUNTPOINT or a MOUNTPOINTS list.
type lsblkNode struct {
	Name        flexString   `json:"name"`
	Path        flexString   `json:"path"`
	Type        flexString   `json:"type"`
	Size        flexInt      `json:"size"`
	Model       flexString   `json:"model"`
	Serial      flexString   `json:"serial"`
	Tran        flexString   `json:"tran"`
	RM          flexBool     `json:"rm"`
	Hotplug     flexBool     `json:"hotplug"`
	Label       flexString   `json:"label"`
	FSType      flexString   `json:"fstype"`
	UUID        flexString   `json:"uuid"`
	Mountpoint  flexString   `json:"mountpoint"`
	Mountpoints []flexString `json:"mountpoints"`
	Children    []lsblkNode  `json:"children"`
}

type flexString string

func (s *flexString) UnmarshalJSON(b []byte) error {
	if bytes.Equal(b, []byte("null")) {
		*s = ""
		return nil
	}
	var v string
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	*s = flexString(strings.TrimSpace(v))
	return nil
}

type flexBool bool

func (f *flexBool) UnmarshalJSON(b []byte) error {
	switch strings.Trim(string(b), `"`) {
	case "true", "1":
		*f = true
	case "false", "0", "null", "":
		*f = false
	default:
		return fmt.Errorf("prep: %s is not a boolean", b)
	}
	return nil
}

type flexInt int64

func (f *flexInt) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "null" || s == "" {
		*f = 0
		return nil
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return fmt.Errorf("prep: size %s is not a number of bytes", b)
	}
	*f = flexInt(n)
	return nil
}

// ParseLsblk reads the JSON of lsblk. A device without a path gets "/dev/<name>". Anything that is not
// the documented shape is an error: a disk list that cannot be read offers nothing.
func ParseLsblk(raw []byte) ([]BlockDevice, error) {
	var doc struct {
		BlockDevices *[]lsblkNode `json:"blockdevices"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("prep: the disk list of lsblk cannot be read: %w", err)
	}
	if doc.BlockDevices == nil {
		return nil, fmt.Errorf("prep: the disk list of lsblk has no blockdevices")
	}
	out := make([]BlockDevice, 0, len(*doc.BlockDevices))
	for _, n := range *doc.BlockDevices {
		out = append(out, n.device())
	}
	return out, nil
}

func (n lsblkNode) device() BlockDevice {
	d := BlockDevice{
		Name: string(n.Name), Path: string(n.Path), Type: string(n.Type), Size: int64(n.Size),
		Model: string(n.Model), Serial: string(n.Serial), Tran: string(n.Tran),
		Removable: bool(n.RM), Hotplug: bool(n.Hotplug),
		Label: string(n.Label), FSType: string(n.FSType), UUID: string(n.UUID),
	}
	if d.Path == "" && d.Name != "" {
		d.Path = "/dev/" + d.Name
	}
	seen := map[string]bool{}
	add := func(m flexString) {
		if s := string(m); s != "" && !seen[s] {
			seen[s] = true
			d.Mountpoints = append(d.Mountpoints, s)
		}
	}
	add(n.Mountpoint)
	for _, m := range n.Mountpoints {
		add(m)
	}
	for _, c := range n.Children {
		d.Children = append(d.Children, c.device())
	}
	return d
}

// Find returns the device with path p anywhere in the tree, and the whole disk that holds it.
func Find(devs []BlockDevice, p string) (dev, disk BlockDevice, ok bool) {
	for _, top := range devs {
		if d, found := findIn(top, p); found {
			return d, top, true
		}
	}
	return BlockDevice{}, BlockDevice{}, false
}

func findIn(d BlockDevice, p string) (BlockDevice, bool) {
	if d.Path == p {
		return d, true
	}
	for _, c := range d.Children {
		if f, ok := findIn(c, p); ok {
			return f, true
		}
	}
	return BlockDevice{}, false
}

// allMounts lists every mount point in d and below.
func allMounts(d BlockDevice) []string {
	out := append([]string(nil), d.Mountpoints...)
	for _, c := range d.Children {
		out = append(out, allMounts(c)...)
	}
	return out
}

// Mounted reports whether d or anything below it is mounted (swap counts).
func Mounted(d BlockDevice) bool { return len(allMounts(d)) > 0 }

// Partitions are the direct children of a disk of type part.
func Partitions(disk BlockDevice) []BlockDevice {
	var out []BlockDevice
	for _, c := range disk.Children {
		if c.Type == "part" {
			out = append(out, c)
		}
	}
	return out
}
