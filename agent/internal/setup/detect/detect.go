// Package detect reads the facts the installer's preflight needs (installer.md section 4.3 P1): the
// Ubuntu release, the architecture, whether this is a container, the GPU, RAM, free disk, listening
// ports, the clock and whether the machine is a laptop. Everything goes through Env, so a test supplies
// fake files and fake command output and never touches the machine.
package detect

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/execx"
)

// Env is everything detect may look at.
type Env struct {
	// Exec runs a program found in the fixed PATH. err != nil means it is missing or could not start;
	// a program that ran and failed gives a non-zero exit and err == nil.
	Exec func(ctx context.Context, name string, args ...string) (stdout string, exit int, err error)
	// ReadFile reads a file (absolute path).
	ReadFile func(name string) ([]byte, error)
	// Glob lists paths matching a pattern.
	Glob func(pattern string) ([]string, error)
	// Getenv reads an environment variable.
	Getenv func(key string) string
	// EUID is the effective user id.
	EUID int
	// Now is the clock.
	Now func() time.Time
	// AvailBytes is the free space of the filesystem that holds path.
	AvailBytes func(path string) (uint64, error)
}

// ErrNotFound is what a fake Exec returns for a program that is not installed.
var ErrNotFound = errors.New("detect: program not found")

// Real is the Env for this machine.
func Real() Env {
	return Env{
		Exec: func(ctx context.Context, name string, args ...string) (string, int, error) {
			p, err := execx.Lookup(name)
			if err != nil {
				return "", -1, err
			}
			res, err := execx.Exec{}.Run(ctx, execx.Cmd{Path: p, Args: args, Timeout: 20 * time.Second, MaxOut: 1 << 20})
			if err != nil {
				return string(res.Stdout), -1, err
			}
			return string(res.Stdout), res.ExitCode, nil
		},
		ReadFile: os.ReadFile,
		Glob:     filepath.Glob,
		Getenv:   os.Getenv,
		EUID:     os.Geteuid(),
		Now:      time.Now,
		AvailBytes: func(path string) (uint64, error) {
			var st syscall.Statfs_t
			if err := syscall.Statfs(path, &st); err != nil {
				return 0, err
			}
			return st.Bavail * uint64(st.Bsize), nil //nolint:unconvert
		},
	}
}

// read returns the text of a file.
func (e Env) read(name string) (string, bool) {
	if e.ReadFile == nil {
		return "", false
	}
	b, err := e.ReadFile(name)
	if err != nil {
		return "", false
	}
	return string(b), true
}

// run is Exec that tolerates a nil Exec.
func (e Env) run(ctx context.Context, name string, args ...string) (string, int, error) {
	if e.Exec == nil {
		return "", -1, ErrNotFound
	}
	return e.Exec(ctx, name, args...)
}

func (e Env) glob(p string) []string {
	if e.Glob == nil {
		return nil
	}
	m, err := e.Glob(p)
	if err != nil {
		return nil
	}
	return m
}
