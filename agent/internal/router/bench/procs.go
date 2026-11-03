package bench

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"strconv"
	"strings"
)

// Procs reads the processes of the router's service. It is only used on a CPU-only node, where
// there is no graphics memory to measure and min_ram_mib is suggested from the resident set of the
// model's child process (llama-router.md section 4.13 step 7).
type Procs interface {
	// PIDs lists every process of the router's service (its cgroup).
	PIDs(ctx context.Context) ([]int, error)
	// VmRSSKiB is the VmRSS of a process in KiB.
	VmRSSKiB(pid int) (int64, error)
}

// DefaultUnit is the router's systemd unit.
const DefaultUnit = "localai-llama.service"

// procFS is Procs over a filesystem rooted at "/" (cgroup v2 and /proc).
type procFS struct {
	fsys fs.FS
	unit string
}

// NewProcFS returns the Procs of a unit ("" means DefaultUnit) read from fsys, a filesystem rooted
// at "/" (os.DirFS("/") on a real machine, an fstest.MapFS in tests).
func NewProcFS(fsys fs.FS, unit string) Procs {
	if unit == "" {
		unit = DefaultUnit
	}
	return &procFS{fsys: fsys, unit: unit}
}

// PIDs implements Procs.
func (p *procFS) PIDs(context.Context) ([]int, error) {
	b, err := fs.ReadFile(p.fsys, "sys/fs/cgroup/system.slice/"+p.unit+"/cgroup.procs")
	if err != nil {
		return nil, fmt.Errorf("read the processes of %s: %w", p.unit, err)
	}
	var out []int
	for _, f := range strings.Fields(string(b)) {
		if n, err := strconv.Atoi(f); err == nil && n > 0 {
			out = append(out, n)
		}
	}
	return out, nil
}

// VmRSSKiB implements Procs.
func (p *procFS) VmRSSKiB(pid int) (int64, error) {
	b, err := fs.ReadFile(p.fsys, "proc/"+strconv.Itoa(pid)+"/status")
	if err != nil {
		return 0, err
	}
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		rest, ok := strings.CutPrefix(sc.Text(), "VmRSS:")
		if !ok {
			continue
		}
		f := strings.Fields(rest)
		if len(f) == 0 {
			break
		}
		n, err := strconv.ParseInt(f[0], 10, 64)
		if err != nil {
			return 0, err
		}
		return n, nil
	}
	return 0, errors.New("no VmRSS line (a kernel thread or an exited process)")
}

// rssTracker sums the VmRSS of the processes that appeared after it was created: the model's
// child, since every model was unloaded before.
type rssTracker struct {
	p    Procs
	base map[int]bool
}

func newRSSTracker(ctx context.Context, p Procs) (*rssTracker, error) {
	pids, err := p.PIDs(ctx)
	if err != nil {
		return nil, err
	}
	base := make(map[int]bool, len(pids))
	for _, pid := range pids {
		base[pid] = true
	}
	return &rssTracker{p: p, base: base}, nil
}

// sample returns the summed VmRSS of the new processes in MiB (rounded up). It fails when there is
// no new process yet or none could be read.
func (t *rssTracker) sample(ctx context.Context) (int64, error) {
	pids, err := t.p.PIDs(ctx)
	if err != nil {
		return 0, err
	}
	var kib int64
	found := 0
	for _, pid := range pids {
		if t.base[pid] {
			continue
		}
		v, err := t.p.VmRSSKiB(pid)
		if err != nil {
			continue // it exited between the two reads
		}
		kib += v
		found++
	}
	if found == 0 {
		return 0, errors.New("no model process found")
	}
	return (kib + 1023) / 1024, nil
}
