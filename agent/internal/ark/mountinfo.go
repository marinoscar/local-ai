package ark

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
)

// This file parses /proc/self/mountinfo (proc(5); ark-sync.md section 4.1.2 step 2). It does no I/O
// of its own: ParseMountInfo reads an io.Reader, and the real file is read by ProcMounts
// (procmounts_linux.go). Nothing here decides what an ark drive is; that is classify.go.

// MountEntry is one line of mountinfo:
//
//	36 35 98:0 /mnt1 /mnt2 rw,noatime shared:1 master:2 - ext3 /dev/root rw,errors=continue
//	(1)(2)(3)  (4)   (5)   (6)        (7)...            (8) (9)  (10)     (11)
type MountEntry struct {
	// MountID (1) and ParentID (2) identify the mount and its parent in the mount tree.
	MountID, ParentID int
	// Major and Minor (3) are the device numbers ("st_dev") of the filesystem.
	Major, Minor int
	// Root (4) is the directory of the filesystem this mount is the root of: "/" for a whole
	// filesystem, "/sub" for a bind mount of a subdirectory.
	Root string
	// MountPoint (5) is where it is mounted, with the octal escapes decoded.
	MountPoint string
	// MountOptions (6) are the per-mount options ("rw", "ro", "noatime", ...).
	MountOptions []string
	// Optional (7) are the optional fields before the separator ("shared:1", "master:2").
	Optional []string
	// FSType (9) is the filesystem type ("ext4", "exfat", "tmpfs", "fuse.sshfs").
	FSType string
	// Source (10) is the mount source: a device ("/dev/sdb1"), or a name for a pseudo filesystem
	// ("tmpfs", "overlay", "host:/export").
	Source string
	// SuperOptions (11) are the per-superblock options ("rw", "ro", "errors=remount-ro", ...).
	SuperOptions []string
}

// DevNum is "major:minor", the key the block-device cache and the sysfs lookups use.
func (e MountEntry) DevNum() string { return fmt.Sprintf("%d:%d", e.Major, e.Minor) }

// MountReadOnly reports whether the per-mount options contain "ro".
func (e MountEntry) MountReadOnly() bool { return hasOption(e.MountOptions, "ro") }

// SuperReadOnly reports whether the superblock options contain "ro".
func (e MountEntry) SuperReadOnly() bool { return hasOption(e.SuperOptions, "ro") }

// ReadOnly reports whether either side says "ro" (a read-only bind mount over a read-write
// filesystem, or a read-write mount of a filesystem the kernel turned read-only).
func (e MountEntry) ReadOnly() bool { return e.MountReadOnly() || e.SuperReadOnly() }

// IsDevice reports whether the source is a block device node ("/dev/..."), as opposed to the name
// a pseudo or network filesystem has (tmpfs, overlay, host:/export, gvfsd-fuse).
func (e MountEntry) IsDevice() bool {
	return strings.HasPrefix(e.Source, "/dev/") && len(e.Source) > len("/dev/")
}

func hasOption(opts []string, want string) bool {
	for _, o := range opts {
		if o == want {
			return true
		}
	}
	return false
}

// ParseError is a line of mountinfo that could not be parsed. ParseMountInfo skips such a line and
// reports it, so one odd line never hides a drive.
type ParseError struct {
	Line int // 1-based
	Err  error
}

// Error implements error.
func (e *ParseError) Error() string { return fmt.Sprintf("mountinfo line %d: %v", e.Line, e.Err) }

// Unwrap returns the cause.
func (e *ParseError) Unwrap() error { return e.Err }

// maxMountInfoLine bounds one line (a mount point is at most PATH_MAX, options are short).
const maxMountInfoLine = 1 << 20

// ParseMountInfo parses the content of /proc/self/mountinfo. Every well-formed line becomes an entry,
// in file order. A malformed line is skipped, and the returned error joins one *ParseError per
// skipped line (errors.As finds them); a caller that got entries may use them and log the error. A
// read failure is returned as is, with the entries parsed so far.
func ParseMountInfo(r io.Reader) ([]MountEntry, error) {
	var (
		out  []MountEntry
		errs []error
	)
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), maxMountInfoLine)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimRight(sc.Text(), "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		e, err := parseMountInfoLine(line)
		if err != nil {
			errs = append(errs, &ParseError{Line: n, Err: err})
			continue
		}
		out = append(out, e)
	}
	if err := sc.Err(); err != nil {
		errs = append(errs, fmt.Errorf("read mountinfo: %w", err))
	}
	return out, errors.Join(errs...)
}

var errShortLine = errors.New("too few fields")

func parseMountInfoLine(line string) (MountEntry, error) {
	f := strings.Fields(line)
	// Fixed part: id parent maj:min root mountpoint options, then optional fields, then "-", then
	// fstype source superoptions.
	if len(f) < 10 {
		return MountEntry{}, errShortLine
	}
	sep := -1
	for i := 6; i < len(f); i++ {
		if f[i] == "-" {
			sep = i
			break
		}
	}
	if sep < 0 {
		return MountEntry{}, errors.New(`no "-" separator`)
	}
	if len(f)-sep-1 < 3 {
		return MountEntry{}, errors.New("missing fstype, source or super options after the separator")
	}

	var e MountEntry
	var err error
	if e.MountID, err = strconv.Atoi(f[0]); err != nil {
		return MountEntry{}, fmt.Errorf("mount id %q", f[0])
	}
	if e.ParentID, err = strconv.Atoi(f[1]); err != nil {
		return MountEntry{}, fmt.Errorf("parent id %q", f[1])
	}
	maj, min, ok := strings.Cut(f[2], ":")
	if !ok {
		return MountEntry{}, fmt.Errorf("device numbers %q", f[2])
	}
	if e.Major, err = strconv.Atoi(maj); err != nil {
		return MountEntry{}, fmt.Errorf("major %q", maj)
	}
	if e.Minor, err = strconv.Atoi(min); err != nil {
		return MountEntry{}, fmt.Errorf("minor %q", min)
	}
	e.Root = UnescapeMountPath(f[3])
	e.MountPoint = UnescapeMountPath(f[4])
	e.MountOptions = splitOptions(f[5])
	if sep > 6 {
		e.Optional = append([]string(nil), f[6:sep]...)
	}
	e.FSType = f[sep+1]
	e.Source = UnescapeMountPath(f[sep+2])
	e.SuperOptions = splitOptions(strings.Join(f[sep+3:], " "))
	return e, nil
}

func splitOptions(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, ",")
}

// UnescapeMountPath decodes the octal escapes the kernel writes for the characters that would break
// the line: a space is \040, a tab \011, a newline \012, a backslash \134 (and any byte can come as
// \ooo). A backslash that does not start three octal digits is kept as it is.
func UnescapeMountPath(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) && s[i+1] <= '3' && isOctal(s[i+1]) && isOctal(s[i+2]) && isOctal(s[i+3]) {
			b.WriteByte((s[i+1]-'0')<<6 | (s[i+2]-'0')<<3 | (s[i+3] - '0'))
			i += 3
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func isOctal(c byte) bool { return c >= '0' && c <= '7' }

// ignoredMountPrefixes are the trees whose mounts are never ark drives (ark-sync.md section 4.1.2
// step 3). /run/user/*/gvfs is matched separately because of the wildcard.
var ignoredMountPrefixes = []string{"/proc", "/sys", "/dev", "/snap"}

// IsCandidateMount applies the candidate filter of ark-sync.md section 4.1.2 step 3 to a mount
// point: false for anything under /proc, /sys, /dev, /run/user/*/gvfs and /snap. The match is on
// whole path elements ("/devices" is not under "/dev").
func IsCandidateMount(mountPoint string) bool {
	for _, p := range ignoredMountPrefixes {
		if mountPoint == p || strings.HasPrefix(mountPoint, p+"/") {
			return false
		}
	}
	if rest, ok := strings.CutPrefix(mountPoint, "/run/user/"); ok {
		// rest is "<uid>" or "<uid>/...": the second element must be gvfs.
		parts := strings.SplitN(rest, "/", 3)
		if len(parts) >= 2 && parts[1] == "gvfs" {
			return false
		}
	}
	return true
}

// SortMounts orders entries by mount id ascending (mount time order, the best-copy order).
func SortMounts(entries []MountEntry) {
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].MountID < entries[j].MountID })
}

// Candidates returns the mounts that can be an ark drive, in mount id order, one per device:
//
//   - the mount point passes IsCandidateMount;
//   - the source is a block device node (a tmpfs, an overlay, an NFS export or a FUSE mount has no
//     label to read);
//   - the mount is the root of its filesystem (Root "/"): a bind mount of a subdirectory is not the
//     drive;
//   - when the same device (major:minor) is mounted more than once (a bind mount of the whole
//     filesystem, or a second mount of it), only the first mount, by mount id, is kept. The later
//     ones are the same drive, not a clone: a clone has a different device and the same UUID.
func Candidates(entries []MountEntry) []MountEntry {
	sorted := append([]MountEntry(nil), entries...)
	SortMounts(sorted)
	var out []MountEntry
	seen := map[string]bool{}
	for _, e := range sorted {
		if !IsCandidateMount(e.MountPoint) || !e.IsDevice() || e.Root != "/" {
			continue
		}
		if seen[e.DevNum()] {
			continue
		}
		seen[e.DevNum()] = true
		out = append(out, e)
	}
	return out
}
