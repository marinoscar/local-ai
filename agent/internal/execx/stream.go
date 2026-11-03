package execx

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"syscall"
	"time"
)

// This file is the streaming half of execx: Run collects a program's whole output, Start hands the
// caller stdout as it is produced. Piper uses it so that playback can begin before synthesis ends
// (companion-services.md section 4.3.2). Everything Run guarantees holds here too: an absolute
// binary, a fixed argv, no shell, a timeout that kills the whole process group, a capped stderr and
// an explicit environment. A streaming program additionally gets a working directory and resource
// limits.

// Resource limits a streaming program can be given (a subset of setrlimit(2)).
const (
	RlimitAS     = 1 // RLIMIT_AS: address space in bytes
	RlimitCPU    = 2 // RLIMIT_CPU: CPU seconds
	RlimitNOFILE = 3 // RLIMIT_NOFILE: open files
)

// Rlimit is one resource limit; soft and hard are both set to Limit.
type Rlimit struct {
	Resource int // RlimitAS, RlimitCPU or RlimitNOFILE
	Limit    uint64
}

// ErrRlimit wraps a failure to apply a resource limit; the program is killed and never runs
// unlimited.
var ErrRlimit = errors.New("execx: cannot apply a resource limit")

// StreamCmd describes one streaming program run. Unlike Cmd, stdout is not collected: the caller
// reads it from Process.Stdout.
type StreamCmd struct {
	Path string   // absolute, clean path; never a shell
	Args []string // fixed argv after the program name
	// Env is the exact environment; nil means PATH=FixedPath only.
	Env []string
	// Dir is the working directory; empty means the agent's.
	Dir   string
	Stdin io.Reader // optional
	// Credential runs the program as this user and group with no supplementary groups; the agent
	// must be root for it to work.
	Credential *Credential
	// Rlimits are applied to the child right after it starts.
	Rlimits []Rlimit
	// Timeout is required: when it expires the process group is killed (Wait returns ErrTimeout).
	Timeout time.Duration
	// MaxStderr is required: the number of stderr bytes kept (the first ones).
	MaxStderr int64
}

func (c StreamCmd) validate() error {
	if c.MaxStderr <= 0 {
		return fmt.Errorf("%w: a positive stderr cap is required", ErrInvalid)
	}
	// Reuse the checks of Cmd (path, shell, timeout, NUL in arguments) with a placeholder output cap.
	return Cmd{Path: c.Path, Args: c.Args, Timeout: c.Timeout, MaxOut: 1}.validate()
}

// StreamResult is what a finished streaming run left behind.
type StreamResult struct {
	ExitCode        int
	Stderr          []byte // the first MaxStderr bytes
	StderrTruncated bool
	Duration        time.Duration
}

// Process is a running streaming program.
type Process interface {
	// Stdout is the program's standard output. The caller reads it to the end (or gives up and calls
	// Kill) before Wait.
	Stdout() io.Reader
	// Kill sends SIGKILL to the whole process group. It is safe to call at any time, repeatedly and
	// after the program exited; stdout then reaches its end.
	Kill()
	// Wait waits for the program, releases its resources and reports how it ended. It returns
	// ErrTimeout when the timeout expired and the group was killed, the context's error when the
	// context given to Start ended first, and nil for any exit status (see StreamResult.ExitCode).
	// Call it exactly once, after Stdout was read to its end or after Kill.
	Wait() (StreamResult, error)
}

// Streamer starts streaming programs. The real one is Exec; tests use execx/fake.
type Streamer interface {
	Start(ctx context.Context, c StreamCmd) (Process, error)
}

type streamProc struct {
	cmd     *exec.Cmd
	stdout  io.ReadCloser
	stderr  *capBuffer
	ctx     context.Context
	run     context.Context
	cancel  context.CancelFunc
	start   time.Time
	timeout time.Duration
}

// Start validates c, starts the program in its own process group, applies the resource limits and
// returns at once. The process lives until it exits, the timeout expires, ctx ends or Kill is
// called; Wait must be called to release it.
func (Exec) Start(ctx context.Context, c StreamCmd) (Process, error) {
	if err := c.validate(); err != nil {
		return nil, err
	}
	runCtx, cancel := context.WithTimeout(ctx, c.Timeout)
	cmd := exec.CommandContext(runCtx, c.Path, c.Args...)
	cmd.Env = c.Env
	if cmd.Env == nil {
		cmd.Env = []string{"PATH=" + FixedPath}
	}
	cmd.Dir = c.Dir
	cmd.Stdin = c.Stdin
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if c.Credential != nil {
		cmd.SysProcAttr.Credential = &syscall.Credential{Uid: c.Credential.UID, Gid: c.Credential.GID}
	}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 2 * time.Second
	stderr := &capBuffer{max: c.MaxStderr}
	cmd.Stderr = stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("execx: stdout pipe: %w", err)
	}
	start := time.Now()
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("execx: start %s: %w", c.Path, err)
	}
	p := &streamProc{cmd: cmd, stdout: stdout, stderr: stderr, ctx: ctx, run: runCtx, cancel: cancel, start: start, timeout: c.Timeout}
	for _, rl := range c.Rlimits {
		if err := setRlimit(cmd.Process.Pid, rl); err != nil {
			p.Kill()
			_, _ = p.Wait()
			return nil, fmt.Errorf("%w: %v", ErrRlimit, err)
		}
	}
	return p, nil
}

func (p *streamProc) Stdout() io.Reader { return p.stdout }

func (p *streamProc) Kill() {
	if p.cmd.Process != nil {
		// The group may be gone already: ESRCH is fine.
		_ = syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL)
	}
}

func (p *streamProc) Wait() (StreamResult, error) {
	defer p.cancel()
	err := p.cmd.Wait()
	res := StreamResult{Stderr: p.stderr.buf.Bytes(), StderrTruncated: p.stderr.truncated, Duration: time.Since(p.start)}
	if p.cmd.ProcessState != nil {
		res.ExitCode = p.cmd.ProcessState.ExitCode()
	}
	// Only a program that was killed counts as timed out or cancelled: one that finished on its own
	// just before the deadline or the cancellation is a normal exit.
	if p.killed() {
		switch {
		case errors.Is(p.run.Err(), context.DeadlineExceeded) && p.ctx.Err() == nil:
			return res, fmt.Errorf("%w after %s: %s", ErrTimeout, p.timeout, p.cmd.Path)
		case p.ctx.Err() != nil:
			return res, p.ctx.Err()
		}
	}
	if errors.Is(err, exec.ErrWaitDelay) {
		err = nil
	}
	var exitErr *exec.ExitError
	if err != nil && !errors.As(err, &exitErr) {
		return res, fmt.Errorf("execx: wait %s: %w", p.cmd.Path, err)
	}
	return res, nil
}

// killed reports whether the program ended on a signal (our SIGKILL of the group).
func (p *streamProc) killed() bool {
	if p.cmd.ProcessState == nil {
		return false
	}
	ws, ok := p.cmd.ProcessState.Sys().(syscall.WaitStatus)
	return ok && ws.Signaled()
}

var _ Streamer = Exec{}
