// Package subproc runs the helper programs of ark sync that are not git or apt (today the pmtiles CLI)
// the way ark-sync.md section 6 requires: a fixed argv with no shell, a scrubbed environment (PATH, HOME
// and a few fixed variables, nothing inherited), an optional drop to the service user, a timeout, a
// bounded output and a clean cancel (the whole process group is killed).
//
// It is a thin layer over execx, the only package allowed to import os/exec. What it adds is the
// environment rule and a result shaped for a producer: the exit status, the bounded output, and an error
// that tells a timeout, a cancel and a program that could not start apart. A non-zero exit status is NOT
// an error here (the caller decides what it means).
package subproc

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/marinoscar/local-ai/agent/internal/execx"
)

// Errors of Run. A cancel returns the context's own error.
var (
	// ErrInvalid wraps every refusal to start a malformed Spec (nothing was started).
	ErrInvalid = errors.New("subproc: invalid command")
	// ErrTimeout is returned when Spec.Timeout expired and the process group was killed.
	ErrTimeout = errors.New("subproc: the command timed out")
	// ErrStart is returned when the program could not be started at all.
	ErrStart = errors.New("subproc: the command could not be started")
)

// Spec describes one program run.
type Spec struct {
	// Bin is the absolute path of the program (from execx.Lookup or a fixed install path).
	Bin string
	// Args is the argv after the program name. It is never interpreted by a shell.
	Args []string
	// Home becomes HOME. Required: a tool must never find a real user's configuration.
	Home string
	// Env adds KEY=VALUE variables to the scrubbed environment (HTTPS_PROXY for the throttling proxy).
	// PATH, HOME and LC_ALL cannot be overridden.
	Env []string
	// Credential, when set, runs the program as that user and group (the caller must be root).
	Credential *execx.Credential
	// Timeout is required and bounds the whole run.
	Timeout time.Duration
	// MaxOut caps stdout and stderr, each; default DefaultMaxOut.
	MaxOut int64
}

// DefaultMaxOut is the output cap when Spec.MaxOut is 0.
const DefaultMaxOut = 1 << 20

// Result is what a finished program produced.
type Result struct {
	// ExitCode is the exit status; 0 is success.
	ExitCode int
	// Stdout and Stderr are the bounded output as text, invalid UTF-8 replaced.
	Stdout, Stderr string
	// Truncated: an output stream hit MaxOut.
	Truncated bool
	// Duration is how long the program ran.
	Duration time.Duration
}

// OK reports a zero exit status.
func (r Result) OK() bool { return r.ExitCode == 0 }

// Tail is the last max runes of stderr (stdout when stderr is empty) with control characters turned
// into spaces: what is safe to put in an error or a log line.
func (r Result) Tail(max int) string {
	s := r.Stderr
	if strings.TrimSpace(s) == "" {
		s = r.Stdout
	}
	s = strings.Map(func(c rune) rune {
		if unicode.IsControl(c) {
			return ' '
		}
		return c
	}, strings.ToValidUTF8(s, "?"))
	s = strings.Join(strings.Fields(s), " ")
	if rs := []rune(s); len(rs) > max {
		s = "..." + string(rs[len(rs)-max:])
	}
	return s
}

var reservedEnv = map[string]bool{"PATH": true, "HOME": true, "LC_ALL": true}

// Environment returns the scrubbed environment of a Spec: PATH is the fixed one, HOME is Spec.Home,
// the locale is C, and Spec.Env follows. It refuses a malformed or reserved entry.
func (s Spec) Environment() ([]string, error) {
	if s.Home == "" || !filepath.IsAbs(s.Home) {
		return nil, fmt.Errorf("%w: HOME %q must be an absolute path", ErrInvalid, s.Home)
	}
	env := []string{"PATH=" + execx.FixedPath, "HOME=" + s.Home, "LC_ALL=C"}
	for _, kv := range s.Env {
		k, _, ok := strings.Cut(kv, "=")
		switch {
		case !ok || k == "" || strings.ContainsRune(kv, 0):
			return nil, fmt.Errorf("%w: malformed environment entry", ErrInvalid)
		case reservedEnv[k]:
			return nil, fmt.Errorf("%w: %s cannot be set", ErrInvalid, k)
		}
		for _, c := range k {
			if !(c == '_' || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')) {
				return nil, fmt.Errorf("%w: environment name %q", ErrInvalid, k)
			}
		}
		env = append(env, kv)
	}
	return env, nil
}

// Run starts the program through runner and waits for it. It returns ErrTimeout when the timeout
// expired, the context's error when ctx ended (the process group was killed, whatever it had written
// stays on disk), ErrStart when the program could not be started, and a nil error for every program
// that ran to an exit status, zero or not. A nil runner means the real one, execx.Exec.
func Run(ctx context.Context, runner execx.Runner, s Spec) (Result, error) {
	if runner == nil {
		runner = execx.Exec{}
	}
	env, err := s.Environment()
	if err != nil {
		return Result{}, err
	}
	maxOut := s.MaxOut
	if maxOut <= 0 {
		maxOut = DefaultMaxOut
	}
	res, err := runner.Run(ctx, execx.Cmd{
		Path: s.Bin, Args: s.Args, Env: env, Timeout: s.Timeout, MaxOut: maxOut, Credential: s.Credential,
	})
	out := Result{
		ExitCode: res.ExitCode, Stdout: strings.ToValidUTF8(string(res.Stdout), "?"), Stderr: strings.ToValidUTF8(string(res.Stderr), "?"),
		Truncated: res.Truncated, Duration: res.Duration,
	}
	switch {
	case err == nil:
		return out, nil
	case ctx.Err() != nil:
		return out, ctx.Err()
	case errors.Is(err, execx.ErrTimeout):
		return out, fmt.Errorf("%w after %s", ErrTimeout, s.Timeout)
	case errors.Is(err, execx.ErrInvalid):
		return out, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	return out, fmt.Errorf("%w: %v", ErrStart, err)
}
