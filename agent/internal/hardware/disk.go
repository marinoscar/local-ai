package hardware

import (
	"bufio"
	"bytes"
	"fmt"
	"path"
	"strconv"
	"strings"
)

// Disk roles (agent.md section 3.8.6): one entry per role, per drive for DriveRoot.
const (
	RoleInternalRoot = "internal_root"
	RoleDriveRoot    = "drive_root"
	RoleState        = "state"
	RoleOpt          = "opt"
	RoleEtc          = "etc"
)

// The fixed paths of the node (architecture/overview.md, canonical names).
const (
	InternalRootPath = "/srv/localai"
	StatePath        = "/var/lib/localai"
	OptPath          = "/opt/localai"
	EtcPath          = "/etc/localai"
)

// Disk is one entry of the disks array of GET /hardware.
type Disk struct {
	Path       string `json:"path"`
	Role       string `json:"role"`
	FS         string `json:"fs"`
	TotalBytes int64  `json:"total_bytes"`
	FreeBytes  int64  `json:"free_bytes"`
	// Stale is true when the last statfs failed and these are the previous values.
	Stale bool `json:"stale,omitempty"`
}

// FreePct is the free share of the filesystem; 100 when the size is unknown.
func (d Disk) FreePct() float64 {
	if d.TotalBytes <= 0 {
		return 100
	}
	return float64(d.FreeBytes) * 100 / float64(d.TotalBytes)
}

// DiskTarget is one path to measure and its role.
type DiskTarget struct {
	Path string
	Role string
}

// StatfsFunc returns the size of the filesystem holding path and the bytes a non-root process may
// still use (Bavail times Bsize). The real one is osStatfs; tests pass a fake.
type StatfsFunc func(path string) (total, free int64, err error)

// mount is one line of /proc/self/mountinfo.
type mount struct {
	point  string
	fstype string
}

// parseMountinfo reads mount points and filesystem types. The optional fields end at " - ", after
// which come the type, the source and the super options. Octal escapes (\040) in the mount point
// are decoded.
func parseMountinfo(b []byte) []mount {
	var out []mount
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		pre, post, ok := strings.Cut(line, " - ")
		if !ok {
			continue
		}
		f := strings.Fields(pre)
		g := strings.Fields(post)
		if len(f) < 5 || len(g) < 1 {
			continue
		}
		out = append(out, mount{point: unescapeOctal(f[4]), fstype: g[0]})
	}
	return out
}

func unescapeOctal(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var sb strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+4 <= len(s) {
			if v, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				sb.WriteByte(byte(v))
				i += 3
				continue
			}
		}
		sb.WriteByte(s[i])
	}
	return sb.String()
}

// fsTypeOf returns the type of the mount with the longest mount point that contains p, or "".
func fsTypeOf(mounts []mount, p string) string {
	p = path.Clean(p)
	best, bestLen := "", -1
	for _, m := range mounts {
		mp := path.Clean(m.point)
		if mp == p || mp == "/" || strings.HasPrefix(p, mp+"/") {
			if len(mp) > bestLen {
				best, bestLen = m.fstype, len(mp)
			}
		}
	}
	return best
}

// readDisks measures every target. A target that cannot be measured yields an error in errs under
// its path and no Disk; the sampler keeps the last value for it.
func (e *env) readDisks(statfs StatfsFunc, targets []DiskTarget) (disks []Disk, errs map[string]error) {
	var mounts []mount
	if b, err := e.read("proc/self/mountinfo"); err == nil {
		mounts = parseMountinfo(b)
	}
	errs = map[string]error{}
	for _, t := range targets {
		total, free, err := statfs(t.Path)
		if err != nil {
			errs[t.Path] = fmt.Errorf("hardware: statfs %s: %w", t.Path, err)
			continue
		}
		disks = append(disks, Disk{Path: t.Path, Role: t.Role, FS: fsTypeOf(mounts, t.Path), TotalBytes: total, FreeBytes: free})
	}
	return disks, errs
}
