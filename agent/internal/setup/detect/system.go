package detect

import (
	"context"
	"strconv"
	"strings"
)

// OSRelease is the part of /etc/os-release the installer needs.
type OSRelease struct {
	ID        string
	VersionID string
	Codename  string
}

// ParseOSRelease reads KEY=value lines (quotes optional).
func ParseOSRelease(text string) OSRelease {
	var r OSRelease
	for _, line := range strings.Split(text, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		v = strings.Trim(strings.TrimSpace(v), `"'`)
		switch k {
		case "ID":
			r.ID = v
		case "VERSION_ID":
			r.VersionID = v
		case "VERSION_CODENAME":
			r.Codename = v
		}
	}
	return r
}

// SupportedUbuntu reports whether the release is an Ubuntu LTS the installer supports (24.04, 26.04).
// Interim releases such as 25.10 and every other distribution are refused.
func (r OSRelease) SupportedUbuntu() bool {
	return r.ID == "ubuntu" && (r.VersionID == "24.04" || r.VersionID == "26.04")
}

// OS reads /etc/os-release.
func (e Env) OS() (OSRelease, bool) {
	t, ok := e.read("/etc/os-release")
	if !ok {
		return OSRelease{}, false
	}
	return ParseOSRelease(t), true
}

// Arch is `dpkg --print-architecture`, "" when it cannot be asked.
func (e Env) Arch(ctx context.Context) string {
	out, code, err := e.run(ctx, "dpkg", "--print-architecture")
	if err != nil || code != 0 {
		return ""
	}
	return strings.TrimSpace(out)
}

// SupportedArch reports amd64 or arm64.
func SupportedArch(a string) bool { return a == "amd64" || a == "arm64" }

// Container reports the container kind from `systemd-detect-virt --container`: "" for none, or when
// the tool is missing (the check then cannot prove a container, so it passes).
func (e Env) Container(ctx context.Context) string {
	out, _, err := e.run(ctx, "systemd-detect-virt", "--container")
	if err != nil {
		return ""
	}
	s := strings.TrimSpace(out)
	if s == "none" {
		return ""
	}
	return s
}

// Laptop is the result of laptop detection.
type Laptop struct {
	IsLaptop  bool
	OnBattery bool
	Reason    string
}

// laptopChassis are the DMI chassis types of portable computers: Portable, Laptop, Notebook, Hand Held,
// Convertible, Detachable.
var laptopChassis = map[int]bool{8: true, 9: true, 10: true, 14: true, 31: true, 32: true}

// DetectLaptop applies installer.md section 4.3 P1: chassis_type in {8,9,10,14,31,32} or any
// power_supply of type Battery. OnBattery is true when a battery reports Discharging, or when a mains
// supply exists and none is online.
func (e Env) DetectLaptop() Laptop {
	var l Laptop
	if t, ok := e.read("/sys/class/dmi/id/chassis_type"); ok {
		if n, err := strconv.Atoi(strings.TrimSpace(t)); err == nil && laptopChassis[n] {
			l.IsLaptop, l.Reason = true, "chassis type "+strings.TrimSpace(t)
		}
	}
	mainsOnline, mainsSeen, discharging := false, false, false
	for _, dir := range e.glob("/sys/class/power_supply/*") {
		t, _ := e.read(dir + "/type")
		switch strings.TrimSpace(t) {
		case "Battery":
			if !l.IsLaptop {
				l.IsLaptop, l.Reason = true, "has a battery"
			}
			if st, _ := e.read(dir + "/status"); strings.TrimSpace(st) == "Discharging" {
				discharging = true
			}
		case "Mains":
			mainsSeen = true
			if on, _ := e.read(dir + "/online"); strings.TrimSpace(on) == "1" {
				mainsOnline = true
			}
		}
	}
	if l.IsLaptop {
		l.OnBattery = discharging || (mainsSeen && !mainsOnline)
	}
	return l
}

// MemTotalMiB reads MemTotal from /proc/meminfo (0 when unknown).
func (e Env) MemTotalMiB() int64 {
	t, ok := e.read("/proc/meminfo")
	if !ok {
		return 0
	}
	return ParseMemTotalMiB(t)
}

// ParseMemTotalMiB parses the MemTotal line (kB) into MiB.
func ParseMemTotalMiB(text string) int64 {
	for _, line := range strings.Split(text, "\n") {
		if !strings.HasPrefix(line, "MemTotal:") {
			continue
		}
		f := strings.Fields(strings.TrimPrefix(line, "MemTotal:"))
		if len(f) == 0 {
			return 0
		}
		kb, err := strconv.ParseInt(f[0], 10, 64)
		if err != nil || kb < 0 {
			return 0
		}
		return kb / 1024
	}
	return 0
}

// Clock is the clock check input: the year and whether NTP says it is synchronised.
type Clock struct {
	Year     int
	NTPKnown bool
	NTPSync  bool
}

// Clock asks timedatectl and the system time.
func (e Env) Clock(ctx context.Context) Clock {
	c := Clock{}
	if e.Now != nil {
		c.Year = e.Now().UTC().Year()
	}
	out, code, err := e.run(ctx, "timedatectl", "show", "-p", "NTPSynchronized")
	if err == nil && code == 0 {
		c.NTPKnown = true
		c.NTPSync = strings.TrimSpace(out) == "NTPSynchronized=yes"
	}
	return c
}

// OK is the P1 rule: year >= 2026. Only the year decides: an offline machine never synchronises, and
// the warning (LAI-INS-017) never blocks.
func (c Clock) OK() bool { return c.Year >= 2026 }

// DiskAvail is the free space of path, falling back to / when path cannot be measured.
func (e Env) DiskAvail(path string) (avail uint64, used string, ok bool) {
	if e.AvailBytes == nil {
		return 0, path, false
	}
	if n, err := e.AvailBytes(path); err == nil {
		return n, path, true
	}
	if path != "/" {
		if n, err := e.AvailBytes("/"); err == nil {
			return n, "/", true
		}
	}
	return 0, path, false
}
