package ark

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/execx"
)

// This file is the blkid seam of the drive detector (ark-sync.md section 4.1.2 steps 3 and 5). The
// detector talks to a BlockProber; BlkidProber is the real one and runs blkid(8) through execx
// (fixed argv, no shell, absolute path, a timeout and an output cap). Tests use a fake prober, or a
// BlkidProber over execx/fake.

// BlkidTimeout is the timeout of one blkid call (ark-sync.md section 4.1.2: 5 s).
const BlkidTimeout = 5 * time.Second

// blkidMaxOut caps what one blkid call may print. A scan lists a few devices; 256 KiB is far more.
const blkidMaxOut = 256 << 10

// ArkLabel is the filesystem label of an ark drive. The match is exact and case-sensitive:
// "AI-ARK2" and "ai-ark" are not ark drives.
const ArkLabel = "AI-ARK"

// BlockInfo is what blkid knows about one block device.
type BlockInfo struct {
	// Device is the device node that was probed ("/dev/sdb1").
	Device string
	// Label, UUID and Type are blkid's LABEL, UUID and TYPE. Any of them is "" when the device has
	// no such tag (no filesystem signature at all gives an all-empty BlockInfo).
	Label, UUID, Type string
}

// Errors of the block prober.
var (
	// ErrBlkidTimeout: blkid did not answer within BlkidTimeout and was killed.
	ErrBlkidTimeout = errors.New("ark: blkid timeout")
	// ErrBlkidUnavailable: the blkid program could not be found or started.
	ErrBlkidUnavailable = errors.New("ark: blkid is not available")
	// ErrBadDevice: the device argument is not an absolute /dev path. Nothing was run.
	ErrBadDevice = errors.New("ark: not a /dev device path")
)

// BlockProber reads block-device tags. The detector never runs a program itself.
type BlockProber interface {
	// Probe reads the tags of one device (blkid -p -o export <device>). A device with no recognised
	// signature is a zero BlockInfo with Device set and a nil error. A timeout is ErrBlkidTimeout;
	// any other failure is a plain error. Probe reads the device's superblock only.
	Probe(ctx context.Context, device string) (BlockInfo, error)
	// FindByLabel lists the devices whose LABEL is label (blkid -t LABEL=<label> -o device), sorted
	// as blkid printed them. No match is an empty list and a nil error.
	FindByLabel(ctx context.Context, label string) ([]string, error)
}

// BlkidProber is the real BlockProber: blkid(8) through an execx.Runner.
type BlkidProber struct {
	// Runner runs the program; execx.Exec{} in the agent, execx/fake in tests.
	Runner execx.Runner
	// Path is the absolute path of blkid (execx.Lookup("blkid")).
	Path string
	// Timeout overrides BlkidTimeout (tests).
	Timeout time.Duration
}

// UnavailableProber is the BlockProber of a node that has no blkid: every call fails with Err. The
// detector then lists no drive and logs why on each pass.
type UnavailableProber struct{ Err error }

// Probe implements BlockProber.
func (u UnavailableProber) Probe(context.Context, string) (BlockInfo, error) {
	return BlockInfo{}, u.err()
}

// FindByLabel implements BlockProber.
func (u UnavailableProber) FindByLabel(context.Context, string) ([]string, error) {
	return nil, u.err()
}

func (u UnavailableProber) err() error {
	if u.Err != nil {
		return u.Err
	}
	return ErrBlkidUnavailable
}

// NewBlkidProber looks blkid up in the fixed PATH and returns a prober over runner. When blkid is
// not installed it returns ErrBlkidUnavailable (the agent then runs without drive detection and says
// so).
func NewBlkidProber(runner execx.Runner) (*BlkidProber, error) {
	p, err := execx.Lookup("blkid")
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBlkidUnavailable, err)
	}
	return &BlkidProber{Runner: runner, Path: p}, nil
}

func (b *BlkidProber) timeout() time.Duration {
	if b.Timeout > 0 {
		return b.Timeout
	}
	return BlkidTimeout
}

// blkid exit codes (blkid(8)): 0 found, 2 nothing found (for -p: no signature; for -t: no match), 4
// usage or other error, 8 ambivalent probing result (-p only).
const (
	blkidNothing = 2
)

func (b *BlkidProber) run(ctx context.Context, args ...string) (execx.Result, error) {
	res, err := b.Runner.Run(ctx, execx.Cmd{Path: b.Path, Args: args, Timeout: b.timeout(), MaxOut: blkidMaxOut})
	switch {
	case errors.Is(err, execx.ErrTimeout):
		return res, ErrBlkidTimeout
	case errors.Is(err, execx.ErrInvalid):
		return res, fmt.Errorf("%w: %v", ErrBlkidUnavailable, err)
	case err != nil:
		return res, err
	}
	return res, nil
}

// Probe implements BlockProber with `blkid -p -o export <device>`.
func (b *BlkidProber) Probe(ctx context.Context, device string) (BlockInfo, error) {
	if !validDevicePath(device) {
		return BlockInfo{}, fmt.Errorf("%w: %q", ErrBadDevice, device)
	}
	res, err := b.run(ctx, "-p", "-o", "export", device)
	if err != nil {
		return BlockInfo{}, err
	}
	switch res.ExitCode {
	case 0:
	case blkidNothing:
		return BlockInfo{Device: device}, nil
	default:
		return BlockInfo{}, fmt.Errorf("blkid exited %d: %s", res.ExitCode, firstLine(res.Stderr))
	}
	info := ParseBlkidExport(res.Stdout)
	info.Device = device
	return info, nil
}

// FindByLabel implements BlockProber with `blkid -t LABEL=<label> -o device`.
func (b *BlkidProber) FindByLabel(ctx context.Context, label string) ([]string, error) {
	if label == "" || strings.ContainsAny(label, "\x00\n") {
		return nil, fmt.Errorf("ark: bad label %q", label)
	}
	res, err := b.run(ctx, "-t", "LABEL="+label, "-o", "device")
	if err != nil {
		return nil, err
	}
	switch res.ExitCode {
	case 0:
	case blkidNothing:
		return nil, nil
	default:
		return nil, fmt.Errorf("blkid exited %d: %s", res.ExitCode, firstLine(res.Stderr))
	}
	var out []string
	sc := bufio.NewScanner(bytes.NewReader(res.Stdout))
	for sc.Scan() {
		if d := strings.TrimSpace(sc.Text()); validDevicePath(d) {
			out = append(out, d)
		}
	}
	return out, nil
}

// ParseBlkidExport reads the KEY=VALUE lines of `blkid -o export` and keeps LABEL, UUID and TYPE.
// Other keys (DEVNAME, PTTYPE, USAGE, ...) and lines without "=" are ignored; the first occurrence
// of a key wins.
func ParseBlkidExport(out []byte) BlockInfo {
	var info BlockInfo
	seen := map[string]bool{}
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), "=")
		if !ok || seen[k] {
			continue
		}
		switch k {
		case "LABEL":
			info.Label = v
		case "UUID":
			info.UUID = v
		case "TYPE":
			info.Type = v
		default:
			continue
		}
		seen[k] = true
	}
	return info
}

// validDevicePath accepts "/dev/<something>" with no NUL, newline or "..": the only values the
// detector ever hands to blkid.
func validDevicePath(p string) bool {
	if !strings.HasPrefix(p, "/dev/") || len(p) == len("/dev/") {
		return false
	}
	return !strings.ContainsAny(p, "\x00\n") && !strings.Contains(p, "..")
}

func firstLine(b []byte) string {
	s := strings.TrimSpace(string(b))
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}
