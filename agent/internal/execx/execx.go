// Package execx is the ONLY package allowed to import os/exec (agent.md sections 3.2 and 6;
// internal/execx/guard_test.go enforces it). Every external program the agent runs goes through
// a Runner with a fixed argv, an absolute binary path, a timeout and an output cap. There is no
// shell: shell binaries are refused outright.
package execx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// FixedPath is the only search path: binaries are looked up here and nowhere else, and a child that
// is not given an explicit environment gets exactly this PATH.
const FixedPath = "/usr/sbin:/usr/bin:/sbin:/bin"

var (
	// ErrInvalid wraps every refusal to run a malformed Cmd (nothing was started).
	ErrInvalid = errors.New("execx: invalid command")
	// ErrTimeout is returned when the per-call timeout expired and the process group was killed.
	ErrTimeout = errors.New("execx: command timed out")
	// ErrNotFound is returned by Lookup when no executable matches inside FixedPath.
	ErrNotFound = errors.New("execx: executable not found in the fixed PATH")
)

// Cmd describes one program run.
type Cmd struct {
	Path    string        // absolute path, normally from Lookup
	Args    []string      // fixed argv after the program name; never interpreted by a shell
	Timeout time.Duration // required, > 0
	MaxOut  int64         // required, > 0: cap in bytes for stdout and for stderr, each
	Env     []string      // nil = minimal environment (PATH=FixedPath only)
	Stdin   io.Reader     // optional
	// Credential, when set, runs the program as this user and group with no supplementary groups
	// (the caller must be root; the start fails otherwise). `aictl key copy` uses it to run the
	// clipboard tool as $SUDO_USER.
	Credential *Credential
}

// Credential is the user and group a program runs as.
type Credential struct{ UID, GID uint32 }

// Result is what a finished run produced. A non-zero ExitCode is NOT an error: callers decide.
type Result struct {
	Stdout    []byte
	Stderr    []byte
	ExitCode  int
	Truncated bool // stdout or stderr hit MaxOut; the first MaxOut bytes are kept
	Duration  time.Duration
}

// Runner runs programs. The real one is Exec; tests use execx/fake.
type Runner interface {
	Run(ctx context.Context, c Cmd) (Result, error)
}

// Exec is the real Runner.
type Exec struct{}

var shells = map[string]bool{
	"sh": true, "bash": true, "dash": true, "zsh": true, "ksh": true, "csh": true, "tcsh": true, "fish": true, "ash": true,
}

func (c Cmd) validate() error {
	switch {
	case c.Path == "" || !filepath.IsAbs(c.Path) || filepath.Clean(c.Path) != c.Path:
		return fmt.Errorf("%w: path %q must be absolute and clean", ErrInvalid, c.Path)
	case shells[filepath.Base(c.Path)]:
		return fmt.Errorf("%w: %q is a shell; commands are run with a fixed argv, never through a shell", ErrInvalid, c.Path)
	case c.Timeout <= 0:
		return fmt.Errorf("%w: a positive timeout is required", ErrInvalid)
	case c.MaxOut <= 0:
		return fmt.Errorf("%w: a positive output cap is required", ErrInvalid)
	}
	for _, a := range c.Args {
		if strings.ContainsRune(a, 0) {
			return fmt.Errorf("%w: argument contains a NUL byte", ErrInvalid)
		}
	}
	return nil
}

// capBuffer keeps the first max bytes and silently discards the rest, so a chatty child never
// blocks on a full pipe or exhausts memory.
type capBuffer struct {
	buf       bytes.Buffer
	max       int64
	truncated bool
}

func (b *capBuffer) Write(p []byte) (int, error) {
	if room := b.max - int64(b.buf.Len()); room > 0 {
		if int64(len(p)) > room {
			b.buf.Write(p[:room])
			b.truncated = true
		} else {
			b.buf.Write(p)
		}
	} else if len(p) > 0 {
		b.truncated = true
	}
	return len(p), nil
}

// Run starts the program in its own process group, enforces the timeout (killing the whole group)
// and caps its output. It returns ErrInvalid for a malformed Cmd, ErrTimeout when the timeout
// expired, ctx.Err() when ctx was cancelled, and the start error if the program could not start.
func (Exec) Run(ctx context.Context, c Cmd) (Result, error) {
	if err := c.validate(); err != nil {
		return Result{}, err
	}
	runCtx, cancel := context.WithTimeout(ctx, c.Timeout)
	defer cancel()

	cmd := exec.CommandContext(runCtx, c.Path, c.Args...)
	cmd.Env = c.Env
	if cmd.Env == nil {
		cmd.Env = []string{"PATH=" + FixedPath}
	}
	cmd.Stdin = c.Stdin
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if c.Credential != nil {
		cmd.SysProcAttr.Credential = &syscall.Credential{Uid: c.Credential.UID, Gid: c.Credential.GID}
	}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 2 * time.Second // do not hang on pipes a grandchild might still hold
	stdout, stderr := &capBuffer{max: c.MaxOut}, &capBuffer{max: c.MaxOut}
	cmd.Stdout, cmd.Stderr = stdout, stderr

	start := time.Now()
	err := cmd.Run()
	res := Result{
		Stdout:    stdout.buf.Bytes(),
		Stderr:    stderr.buf.Bytes(),
		Truncated: stdout.truncated || stderr.truncated,
		Duration:  time.Since(start),
	}
	if cmd.ProcessState != nil {
		res.ExitCode = cmd.ProcessState.ExitCode()
	}

	switch {
	case errors.Is(runCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil:
		return res, fmt.Errorf("%w after %s: %s", ErrTimeout, c.Timeout, c.Path)
	case ctx.Err() != nil:
		return res, ctx.Err()
	}
	// A program that forks into the background and keeps our pipes open (xclip, wl-copy) has
	// still finished successfully: Go reports ErrWaitDelay once WaitDelay passed with the pipes
	// open and the process itself exited with status 0.
	if errors.Is(err, exec.ErrWaitDelay) {
		err = nil
	}
	var exitErr *exec.ExitError
	if err != nil && !errors.As(err, &exitErr) {
		return res, fmt.Errorf("execx: run %s: %w", c.Path, err) // could not start, etc.
	}
	return res, nil
}

// Lookup resolves a program name to an absolute path inside FixedPath only.
//
// It refuses relative names with a directory part ("./x", "bin/x"), names containing "..", and
// absolute names outside the fixed directories. A bare name ("systemctl") is searched in the fixed
// directories in order.
func Lookup(name string) (string, error) { return lookupIn(strings.Split(FixedPath, ":"), name) }

func lookupIn(dirs []string, name string) (string, error) {
	if name == "" || strings.ContainsRune(name, 0) || strings.Contains(name, "..") {
		return "", fmt.Errorf("%w: invalid name %q", ErrInvalid, name)
	}
	if strings.Contains(name, "/") {
		if !filepath.IsAbs(name) {
			return "", fmt.Errorf("%w: relative path %q is refused", ErrInvalid, name)
		}
		clean := filepath.Clean(name)
		for _, d := range dirs {
			if filepath.Dir(clean) == d && isExecutable(clean) {
				return clean, nil
			}
		}
		return "", fmt.Errorf("%w: %q is outside the fixed PATH", ErrNotFound, name)
	}
	for _, d := range dirs {
		if p := filepath.Join(d, name); isExecutable(p) {
			return p, nil
		}
	}
	return "", fmt.Errorf("%w: %q", ErrNotFound, name)
}

func isExecutable(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular() && fi.Mode().Perm()&0o111 != 0
}
