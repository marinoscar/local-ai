package prep

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/execx"
)

// Tools is every disk program the preparation runs. Each method runs one fixed argv (no shell, no
// argument a person typed) and refuses a device that is not ValidDevice before running anything.
// ExecTools is the real one; tests use a fake.
type Tools interface {
	// ListBlock runs lsblk and parses its JSON.
	ListBlock(ctx context.Context) ([]BlockDevice, error)
	// Wipe erases the partition table and filesystem signatures of dev: wipefs --all <dev>.
	Wipe(ctx context.Context, dev string) error
	// Partition writes a new GPT with one partition named AI-ARK that fills the disk:
	// parted --script <disk> mklabel gpt mkpart AI-ARK ext4 1MiB 100%.
	Partition(ctx context.Context, disk string) error
	// Settle waits for udev to create the device nodes: udevadm settle --timeout=30.
	Settle(ctx context.Context) error
	// MakeFS formats part as ext4 labelled AI-ARK:
	// mkfs.ext4 -L AI-ARK -m 0 -E lazy_itable_init=0,lazy_journal_init=0 <part>.
	MakeFS(ctx context.Context, part string) error
	// Check runs fsck.ext4 -f -y -C 1 <dev>, reports its progress lines to progress and returns the
	// exit code of fsck (CheckResult reads it).
	Check(ctx context.Context, dev string, progress func(CheckProgress)) (int, error)
	// NewUUID gives the filesystem on dev a new random UUID: tune2fs -U random <dev>.
	NewUUID(ctx context.Context, dev string) error
	// Mount mounts dev: udisksctl mount --no-user-interaction -b <dev>. A device that is already
	// mounted is not an error.
	Mount(ctx context.Context, dev string) error
	// Unmount unmounts dev: udisksctl unmount --no-user-interaction -b <dev>.
	Unmount(ctx context.Context, dev string) error
}

// ToolError is a disk program that could not run or did not succeed.
type ToolError struct {
	// Tool is the program's name (wipefs, parted, mkfs.ext4, ...).
	Tool string
	// ExitCode is its exit status; -1 when it did not run.
	ExitCode int
	// Reason is the first line it printed on stderr, cut down ("" when nothing).
	Reason string
	Err    error
}

// Error implements error.
func (e *ToolError) Error() string {
	s := e.Tool + " failed"
	if e.ExitCode >= 0 {
		s += " (exit " + strconv.Itoa(e.ExitCode) + ")"
	}
	if e.Reason != "" {
		s += ": " + e.Reason
	}
	if e.Err != nil {
		s += ": " + e.Err.Error()
	}
	return s
}

// Unwrap returns the underlying error.
func (e *ToolError) Unwrap() error { return e.Err }

// ErrBadDevice is returned, before anything runs, for a device path that is not ValidDevice.
var ErrBadDevice = errors.New("prep: not a plain /dev device")

// ErrMissing wraps a program that is not installed.
var ErrMissing = execx.ErrNotFound

// Timeouts of the programs. Formatting with lazy_itable_init=0 writes every inode table at once, and a
// full check of a big drive reads all of it: both can take a long time on a slow USB disk.
const (
	lsblkTimeout = 15 * time.Second
	wipeTimeout  = time.Minute
	partTimeout  = 2 * time.Minute
	settleWait   = 40 * time.Second
	mkfsTimeout  = 3 * time.Hour
	fsckTimeout  = 6 * time.Hour
	tuneTimeout  = time.Hour
	mountTimeout = time.Minute
	maxToolOut   = 1 << 20
)

// ExecTools runs the real programs.
type ExecTools struct {
	// Run runs the short programs; nil is execx.Exec.
	Run execx.Runner
	// Stream runs fsck, whose progress is read as it goes; nil is execx.Exec.
	Stream execx.Streamer
	// Lookup finds a program in the fixed PATH; nil is execx.Lookup.
	Lookup func(string) (string, error)
}

func (t ExecTools) runner() execx.Runner {
	if t.Run != nil {
		return t.Run
	}
	return execx.Exec{}
}

func (t ExecTools) lookup(name string) (string, error) {
	if t.Lookup != nil {
		return t.Lookup(name)
	}
	return execx.Lookup(name)
}

// run runs name with args and returns its output, or a *ToolError.
func (t ExecTools) run(ctx context.Context, name string, timeout time.Duration, args ...string) (execx.Result, error) {
	path, err := t.lookup(name)
	if err != nil {
		return execx.Result{}, &ToolError{Tool: name, ExitCode: -1, Err: err}
	}
	res, err := t.runner().Run(ctx, execx.Cmd{
		Path: path, Args: args, Timeout: timeout, MaxOut: maxToolOut,
		// An empty stdin: a program that asks a question gets "no" and stops.
		Stdin: strings.NewReader(""),
	})
	if err != nil {
		return res, &ToolError{Tool: name, ExitCode: -1, Err: err}
	}
	if res.ExitCode != 0 {
		return res, &ToolError{Tool: name, ExitCode: res.ExitCode, Reason: firstLine(res.Stderr, res.Stdout)}
	}
	return res, nil
}

func checkDevice(name, dev string) error {
	if !ValidDevice(dev) {
		return &ToolError{Tool: name, ExitCode: -1, Err: fmt.Errorf("%w: %q", ErrBadDevice, dev)}
	}
	return nil
}

// ListBlock implements Tools.
func (t ExecTools) ListBlock(ctx context.Context) ([]BlockDevice, error) {
	res, err := t.run(ctx, "lsblk", lsblkTimeout, "--json", "--bytes", "--output", LsblkColumns)
	if err != nil {
		return nil, err
	}
	return ParseLsblk(res.Stdout)
}

// Wipe implements Tools.
func (t ExecTools) Wipe(ctx context.Context, dev string) error {
	if err := checkDevice("wipefs", dev); err != nil {
		return err
	}
	_, err := t.run(ctx, "wipefs", wipeTimeout, "--all", dev)
	return err
}

// Partition implements Tools.
func (t ExecTools) Partition(ctx context.Context, disk string) error {
	if err := checkDevice("parted", disk); err != nil {
		return err
	}
	_, err := t.run(ctx, "parted", partTimeout, "--script", disk, "mklabel", "gpt", "mkpart", Label, "ext4", "1MiB", "100%")
	return err
}

// Settle implements Tools.
func (t ExecTools) Settle(ctx context.Context) error {
	_, err := t.run(ctx, "udevadm", settleWait, "settle", "--timeout=30")
	return err
}

// MakeFS implements Tools.
func (t ExecTools) MakeFS(ctx context.Context, part string) error {
	if err := checkDevice("mkfs.ext4", part); err != nil {
		return err
	}
	_, err := t.run(ctx, "mkfs.ext4", mkfsTimeout, "-L", Label, "-m", "0", "-E", "lazy_itable_init=0,lazy_journal_init=0", part)
	return err
}

// NewUUID implements Tools.
func (t ExecTools) NewUUID(ctx context.Context, dev string) error {
	if err := checkDevice("tune2fs", dev); err != nil {
		return err
	}
	_, err := t.run(ctx, "tune2fs", tuneTimeout, "-U", "random", dev)
	return err
}

// Mount implements Tools.
func (t ExecTools) Mount(ctx context.Context, dev string) error {
	if err := checkDevice("udisksctl", dev); err != nil {
		return err
	}
	res, err := t.run(ctx, "udisksctl", mountTimeout, "mount", "--no-user-interaction", "-b", dev)
	var te *ToolError
	if errors.As(err, &te) && te.ExitCode > 0 && strings.Contains(string(res.Stderr), "AlreadyMounted") {
		return nil // the desktop mounted it first: that is what we wanted
	}
	return err
}

// Unmount implements Tools.
func (t ExecTools) Unmount(ctx context.Context, dev string) error {
	if err := checkDevice("udisksctl", dev); err != nil {
		return err
	}
	_, err := t.run(ctx, "udisksctl", mountTimeout, "unmount", "--no-user-interaction", "-b", dev)
	return err
}

// Check implements Tools. fsck's exit code is its answer, so a non-zero exit is not a *ToolError here:
// CheckResult reads it. Only a program that could not run or was stopped is an error.
func (t ExecTools) Check(ctx context.Context, dev string, progress func(CheckProgress)) (int, error) {
	if err := checkDevice("fsck.ext4", dev); err != nil {
		return -1, err
	}
	path, err := t.lookup("fsck.ext4")
	if err != nil {
		return -1, &ToolError{Tool: "fsck.ext4", ExitCode: -1, Err: err}
	}
	s := t.Stream
	if s == nil {
		s = execx.Exec{}
	}
	proc, err := s.Start(ctx, execx.StreamCmd{
		Path: path, Args: []string{"-f", "-y", "-C", "1", dev}, Timeout: fsckTimeout, MaxStderr: 64 << 10,
		Stdin: strings.NewReader(""),
	})
	if err != nil {
		return -1, &ToolError{Tool: "fsck.ext4", ExitCode: -1, Err: err}
	}
	sc := bufio.NewScanner(proc.Stdout())
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		if p, ok := ParseCheckProgress(sc.Text()); ok && progress != nil {
			progress(p)
		}
	}
	res, err := proc.Wait()
	if err != nil {
		return -1, &ToolError{Tool: "fsck.ext4", ExitCode: -1, Reason: firstLine(res.Stderr), Err: err}
	}
	return res.ExitCode, nil
}

// firstLine is the first non-empty line of the first output that has one, cut to 200 bytes.
func firstLine(outs ...[]byte) string {
	for _, b := range outs {
		for _, l := range strings.Split(string(b), "\n") {
			if l = strings.TrimSpace(l); l != "" {
				if len(l) > 200 {
					l = l[:200]
				}
				return l
			}
		}
	}
	return ""
}

var _ Tools = ExecTools{}
