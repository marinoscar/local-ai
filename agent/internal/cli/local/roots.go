package local

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/execx"
)

// RootKind is the kind of a content root (ark-sync.md section 3.2).
type RootKind string

const (
	// KindInternal is /srv/localai.
	KindInternal RootKind = "internal"
	// KindDrive is <mount>/localai of a mounted AI-ARK drive.
	KindDrive RootKind = "drive"
)

// arkLabel is the filesystem label of an ark drive (ark-sync.md section 4.1.2).
const arkLabel = "AI-ARK"

// Root is a content root that may hold a kit.
type Root struct {
	Kind RootKind
	// Path is the content root: /srv/localai, or <mount>/localai.
	Path string
	// Mount is the drive's mount point ("" for the internal root).
	Mount string
	// ReadOnly says the drive is mounted read-only (it can be read, never written).
	ReadOnly bool
}

// KitCatalog is <root>/kit/catalog, the last good catalog of the kit (rebuild-kit.md section 3.1).
func (r Root) KitCatalog() string { return filepath.Join(r.Path, "kit", "catalog") }

// KitConfig is <root>/kit/config, where `backup-config --to-ark` writes.
func (r Root) KitConfig() string { return filepath.Join(r.Path, "kit", "config") }

// Mount is one line of /proc/self/mountinfo.
type Mount struct {
	Point    string
	FSType   string
	Source   string
	ReadOnly bool
}

// ParseMountinfo reads /proc/self/mountinfo: mount id, parent id, major:minor, root, mount point,
// mount options, optional fields up to a lone "-", then fstype, source and super options. Octal
// escapes in paths (\040 for a space) are decoded. A line that does not have this shape is skipped.
func ParseMountinfo(r io.Reader) []Mount {
	var out []Mount
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		sep := -1
		for i := 6; i < len(f); i++ {
			if f[i] == "-" {
				sep = i
				break
			}
		}
		if len(f) < 7 || sep < 0 || sep+2 >= len(f) {
			continue
		}
		m := Mount{Point: unescapeMount(f[4]), FSType: f[sep+1], Source: unescapeMount(f[sep+2])}
		m.ReadOnly = hasOption("ro", append([]string{f[5]}, f[sep+3:]...)...)
		out = append(out, m)
	}
	return out
}

func hasOption(want string, groups ...string) bool {
	for _, g := range groups {
		for _, o := range strings.Split(g, ",") {
			if o == want {
				return true
			}
		}
	}
	return false
}

// unescapeMount decodes the \NNN octal escapes the kernel writes for space, tab, newline and
// backslash in a path.
func unescapeMount(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) && isOctal(s[i+1]) && isOctal(s[i+2]) && isOctal(s[i+3]) {
			b.WriteByte((s[i+1]-'0')<<6 | (s[i+2]-'0')<<3 | (s[i+3] - '0'))
			i += 3
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func isOctal(c byte) bool { return c >= '0' && c <= '7' }

// excluded are the mount points that are never ark drives (ark-sync.md section 4.1.2 step 3).
func excluded(point string) bool {
	for _, p := range []string{"/proc", "/sys", "/dev", "/snap"} {
		if point == p || strings.HasPrefix(point, p+"/") {
			return true
		}
	}
	return strings.HasPrefix(point, "/run/user/") && strings.Contains(point, "/gvfs")
}

// FindRoots lists the content roots that may hold a kit, in the order the commands use them: the
// internal root first (when it exists), then every mounted AI-ARK drive sorted by mount point.
//
// A mount is a drive when it is ext4, <mount>/localai is a directory and either its ROOT.json says
// kind "ark_drive" or the filesystem label is exactly AI-ARK (blkid, when it can run). A drive that
// has no localai/ directory is not initialised (`aictl ark init`) and is never used.
func FindRoots(ctx context.Context, e Env) []Root {
	var roots []Root
	internal := e.Paths.InternalRoot
	if fi, err := os.Stat(internal); err == nil && fi.IsDir() {
		roots = append(roots, Root{Kind: KindInternal, Path: internal})
	}
	f, err := os.Open(e.Paths.Mountinfo)
	if err != nil {
		return roots
	}
	defer f.Close()
	var drives []Root
	seen := map[string]bool{}
	for _, m := range ParseMountinfo(f) {
		if m.FSType != "ext4" || m.Point == "/" || excluded(m.Point) || seen[m.Point] {
			continue
		}
		path := filepath.Join(m.Point, "localai")
		if path == filepath.Clean(internal) {
			continue
		}
		if fi, err := os.Stat(path); err != nil || !fi.IsDir() {
			continue
		}
		if !isArkDrive(ctx, e, path, m.Source) {
			continue
		}
		seen[m.Point] = true
		drives = append(drives, Root{Kind: KindDrive, Path: path, Mount: m.Point, ReadOnly: m.ReadOnly})
	}
	sort.Slice(drives, func(i, j int) bool { return drives[i].Mount < drives[j].Mount })
	return append(roots, drives...)
}

// isArkDrive applies the identity rule of FindRoots.
func isArkDrive(ctx context.Context, e Env, rootPath, source string) bool {
	if data, err := os.ReadFile(filepath.Join(rootPath, "ROOT.json")); err == nil && len(data) < 1<<16 {
		var rj struct {
			Kind string `json:"kind"`
		}
		if json.Unmarshal(data, &rj) == nil && rj.Kind == "ark_drive" {
			return true
		}
	}
	return blkidLabel(ctx, e, source) == arkLabel
}

// blkidLabel returns the filesystem label of source, "" when it cannot be read (no blkid, not a
// device, timeout).
func blkidLabel(ctx context.Context, e Env, source string) string {
	if e.Runner == nil || e.LookPath == nil || !strings.HasPrefix(source, "/dev/") {
		return ""
	}
	path, err := e.LookPath("blkid")
	if err != nil {
		return ""
	}
	res, err := e.Runner.Run(ctx, execx.Cmd{Path: path, Args: []string{"-p", "-o", "export", source}, Timeout: 5 * time.Second, MaxOut: 64 << 10})
	if err != nil || res.ExitCode != 0 {
		return ""
	}
	for _, line := range strings.Split(string(res.Stdout), "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "LABEL="); ok {
			return v
		}
	}
	return ""
}
