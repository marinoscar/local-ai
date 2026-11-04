package ark

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// This file finds who holds a drive open (ark-sync.md section 4.1.5 step 6): it walks /proc/*/fd and
// /proc/*/cwd for paths under the mount point, as root, and reports the process name and pid. The walk
// is read-only and goes through the ProcFS seam, so the tests run it on a fake /proc.

// Kinds of Blocker.
const (
	// BlockerProcess is a program that has a file open on the drive, or whose working directory is on
	// it (a shell somebody left in the drive's folder).
	BlockerProcess = "process"
	// BlockerModel is a loaded model whose only copy is on the drive.
	BlockerModel = "model"
	// BlockerSync is this agent's own transfer or check that has not let go of the drive yet.
	BlockerSync = "sync"
)

// maxBlockers bounds the list a refusal carries. A drive held by hundreds of worker processes is one
// problem, not a hundred.
const maxBlockers = 32

// Blocker is something that stops a drive from being ejected.
type Blocker struct {
	// Kind is BlockerProcess, BlockerModel or BlockerSync.
	Kind string `json:"kind"`
	// Name is the program (the process name), the model id or the display name of the brain.
	Name string `json:"name"`
	// PID is the process id; 0 for a model, which has none the caller can act on.
	PID int `json:"pid"`
}

// ProcFS is the part of /proc the blocker walk reads. OSProc is the real one.
type ProcFS interface {
	// PIDs lists the process ids (the numeric entries of /proc).
	PIDs() ([]int, error)
	// FDLinks returns the targets of /proc/<pid>/fd/* and of /proc/<pid>/cwd. A link that cannot be read
	// (the process ended, or the caller may not look: ENOENT, EACCES) is left out, not an error; an
	// error is only for a process whose directories cannot be listed at all.
	FDLinks(pid int) ([]string, error)
	// Comm is the process name (/proc/<pid>/comm).
	Comm(pid int) (string, error)
}

// OSProc is the real ProcFS.
type OSProc struct {
	// Root is the proc mount; default /proc. Tests point it at a directory they built.
	Root string
}

func (p OSProc) root() string {
	if p.Root == "" {
		return "/proc"
	}
	return p.Root
}

// PIDs implements ProcFS.
func (p OSProc) PIDs() ([]int, error) {
	entries, err := os.ReadDir(p.root())
	if err != nil {
		return nil, err
	}
	var pids []int
	for _, e := range entries {
		if pid, err := strconv.Atoi(e.Name()); err == nil && pid > 0 {
			pids = append(pids, pid)
		}
	}
	return pids, nil
}

// FDLinks implements ProcFS.
func (p OSProc) FDLinks(pid int) ([]string, error) {
	dir := filepath.Join(p.root(), strconv.Itoa(pid))
	var out []string
	if t, err := os.Readlink(filepath.Join(dir, "cwd")); err == nil {
		out = append(out, t)
	}
	fds, err := os.ReadDir(filepath.Join(dir, "fd"))
	if err != nil {
		// A process that ended between the listing and now, or one the caller may not look at: the
		// cwd (if it could be read) is all there is.
		return out, nil
	}
	for _, fd := range fds {
		if t, err := os.Readlink(filepath.Join(dir, "fd", fd.Name())); err == nil {
			out = append(out, t)
		}
	}
	return out, nil
}

// Comm implements ProcFS.
func (p OSProc) Comm(pid int) (string, error) {
	b, err := os.ReadFile(filepath.Join(p.root(), strconv.Itoa(pid), "comm"))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

// UnderMount reports whether target is the mount point or a path below it. The comparison is on whole
// path elements ("/media/u/AI-ARK2" is not under "/media/u/AI-ARK"), and the kernel's " (deleted)"
// suffix of a file that was unlinked while open is dropped. Targets that are not paths
// ("socket:[123]", "anon_inode:[eventfd]", "pipe:[9]") are never under a mount.
func UnderMount(mount, target string) bool {
	mount = filepath.Clean(mount)
	if mount == "." || mount == "/" || !filepath.IsAbs(mount) || !strings.HasPrefix(target, "/") {
		return false
	}
	target = strings.TrimSuffix(target, " (deleted)")
	target = filepath.Clean(target)
	return target == mount || strings.HasPrefix(target, mount+"/")
}

// FindBlockers walks the processes and returns one Blocker per process that has a file open under
// mount or whose working directory is under it, sorted by pid and bounded by maxBlockers. A process
// that vanishes or cannot be read during the walk is skipped (a race, or no permission), never an
// error. The agent's own pid (self) is reported as kind sync with the name "sync": it is the agent's
// own transfer or check, and the person can only wait for it. An error is returned only when the list
// of processes itself cannot be read.
func FindBlockers(proc ProcFS, mount string, self int) ([]Blocker, error) {
	pids, err := proc.PIDs()
	if err != nil {
		return nil, err
	}
	sort.Ints(pids)
	var out []Blocker
	for _, pid := range pids {
		links, err := proc.FDLinks(pid)
		if err != nil {
			continue
		}
		held := false
		for _, l := range links {
			if UnderMount(mount, l) {
				held = true
				break
			}
		}
		if !held {
			continue
		}
		if pid == self {
			out = append(out, Blocker{Kind: BlockerSync, Name: "sync", PID: pid})
		} else {
			name, err := proc.Comm(pid)
			if err != nil || name == "" {
				name = "pid " + strconv.Itoa(pid)
			}
			out = append(out, Blocker{Kind: BlockerProcess, Name: name, PID: pid})
		}
		if len(out) == maxBlockers {
			break
		}
	}
	return out, nil
}
