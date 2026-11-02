// Package hardware samples what the machine has and how it is doing: GPU and its memory, RAM, CPU,
// disks, battery and AC, temperatures and OOM kills (agent.md section 4.4).
//
// Every probe reads files through an fs.FS rooted at "/" (paths carry no leading slash) or runs a
// program through execx (fixed argv, 3 s timeout, 1 MiB output cap), so tests feed sysfs and procfs
// trees from testdata and scripted nvidia-smi output from the execx fake. Nothing here needs a GPU.
//
// The Sampler refreshes GPU, RAM, CPU, thermal and power every 10 s and disks and OOM detection
// every 60 s. A probe that fails keeps its last value with Stale set; nothing here blocks the API.
// Results (the LAI-HW codes) come from Sampler.Results and Sampler.Checks.
package hardware

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"strconv"
	"strings"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/execx"
)

// Probe limits (agent.md section 4.4: every probe is a fixed argv with a 3 s timeout and 1 MiB cap).
const (
	// ExecTimeout is the default deadline of one external program.
	ExecTimeout = 3 * time.Second
	// ExecMaxOut is the output cap of one external program.
	ExecMaxOut int64 = 1 << 20
)

// Backend names (agent.md section 4.4.3).
const (
	BackendCUDA   = "cuda"
	BackendVulkan = "vulkan"
	BackendCPU    = "cpu"
)

// Vendor names.
const (
	VendorNVIDIA = "nvidia"
	VendorAMD    = "amd"
)

// ErrToolMissing is wrapped by the error of a probe whose program is not installed. A missing
// optional tool is never a failure of the sampler; callers test for it with errors.Is.
var ErrToolMissing = errors.New("hardware: tool not installed")

// env is what every probe needs: the filesystem and the program runner.
type env struct {
	fs          fs.FS
	run         execx.Runner
	look        func(name string) (string, error)
	execTimeout time.Duration
}

// exec runs a program by name with a fixed argv and the probe limits. A program that is not in the
// fixed PATH yields an error wrapping ErrToolMissing; a non-zero exit code is a result (callers
// decide), a timeout or a start failure is an error.
func (e *env) exec(ctx context.Context, name string, args ...string) (execx.Result, error) {
	path, err := e.look(name)
	if err != nil {
		return execx.Result{}, fmt.Errorf("%w: %s", ErrToolMissing, name)
	}
	ctx, cancel := context.WithTimeout(ctx, e.execTimeout)
	defer cancel()
	res, err := e.run.Run(ctx, execx.Cmd{Path: path, Args: args, Timeout: e.execTimeout, MaxOut: ExecMaxOut})
	if err == nil && ctx.Err() != nil {
		// A runner that ignores its Cmd.Timeout (a fake) must not stall the sampler either.
		err = fmt.Errorf("%w: %s", execx.ErrTimeout, name)
	}
	return res, err
}

// execOK is exec plus "exit code 0": any other outcome is an error that carries the first line of
// stderr (never the whole output).
func (e *env) execOK(ctx context.Context, name string, args ...string) ([]byte, error) {
	res, err := e.exec(ctx, name, args...)
	if err != nil {
		return nil, err
	}
	if res.ExitCode != 0 {
		return res.Stdout, fmt.Errorf("hardware: %s exited %d: %s", name, res.ExitCode, firstLine(res.Stderr))
	}
	return res.Stdout, nil
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

// read reads a file (sysfs, procfs) from the rooted filesystem.
func (e *env) read(name string) ([]byte, error) { return fs.ReadFile(e.fs, name) }

// readTrim reads a small text file and trims it.
func (e *env) readTrim(name string) (string, error) {
	b, err := e.read(name)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

// readInt reads a decimal (or 0x-prefixed hexadecimal) integer file.
func (e *env) readInt(name string) (int64, error) {
	s, err := e.readTrim(name)
	if err != nil {
		return 0, err
	}
	v, err := strconv.ParseInt(s, 0, 64)
	if err != nil {
		return 0, fmt.Errorf("hardware: %s: %q is not an integer", name, s)
	}
	return v, nil
}

func (e *env) exists(name string) bool {
	_, err := fs.Stat(e.fs, name)
	return err == nil
}

// round1 rounds to one decimal, so JSON never carries 61.00000000001.
func round1(v float64) float64 {
	if v < 0 {
		return -float64(int64(-v*10+0.5)) / 10
	}
	return float64(int64(v*10+0.5)) / 10
}

const mib = 1 << 20

func ptr[T any](v T) *T { return &v }
