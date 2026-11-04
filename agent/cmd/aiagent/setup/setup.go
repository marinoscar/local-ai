// Package setup is `aiagent setup <step>`, the installer's internal helper (installer.md section 3.5).
// install-node.sh calls one step per job so the logic is Go, table-tested and dry-run capable, and the
// shell only orders the phases. It is not part of the public aictl surface.
//
// This file holds the dispatcher and the seams (root, systemctl, group lookup, randomness) that tests
// replace; the INST-06 steps node-yaml, units and api-key live in their own files and register here.
package setup

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/user"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/execx"
)

// Exit codes of every step (installer.md section 3.5).
const (
	ExitOK      = 0
	ExitFailed  = 1
	ExitUsage   = 2
	ExitNotRoot = 77
)

// Systemctl is the part of systemd the steps use. The real one runs systemctl; tests record calls.
type Systemctl interface {
	DaemonReload(ctx context.Context) error
	Enable(ctx context.Context, units ...string) error
	EnableNow(ctx context.Context, units ...string) error
}

// Env is everything a step touches outside its own arguments.
type Env struct {
	// Root is the filesystem root ("" means "/"); only tests and LAI_INSTALLER_DEV runs set it.
	Root           string
	Stdin          io.Reader
	Stdout, Stderr io.Writer
	Systemctl      Systemctl
	// IsRoot reports whether the process may change the system; a dry run never needs it.
	IsRoot func() bool
	// GroupID returns the gid of a group, ok false when it does not exist (a dev machine).
	GroupID func(name string) (gid int, ok bool)
	// Random is the entropy source for the API key; default crypto/rand.
	Random io.Reader
}

// Step is one `aiagent setup <name>`. args excludes the step name.
type Step func(ctx context.Context, e *Env, args []string) int

var steps = map[string]Step{}

func register(name string, s Step) { steps[name] = s }

// Names returns the registered step names, sorted.
func Names() []string {
	out := make([]string, 0, len(steps))
	for n := range steps {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Run dispatches `aiagent setup <step> ...` (args excludes "setup") against the real system, or against
// $LAI_SETUP_ROOT when LAI_INSTALLER_DEV=1.
func Run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	e := &Env{
		Stdin: stdin, Stdout: stdout, Stderr: stderr,
		Systemctl: execSystemctl{},
		IsRoot:    func() bool { return os.Geteuid() == 0 },
		GroupID:   osGroupID,
	}
	if os.Getenv("LAI_INSTALLER_DEV") == "1" {
		e.Root = os.Getenv("LAI_SETUP_ROOT")
	}
	return RunEnv(ctx, e, args)
}

// RunEnv is Run with an explicit Env (tests).
func RunEnv(ctx context.Context, e *Env, args []string) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		fmt.Fprintf(e.Stderr, "usage: aiagent setup <step> [--dry-run] [step flags]\nsteps: %s\n", strings.Join(Names(), ", "))
		if len(args) == 0 {
			return ExitUsage
		}
		return ExitOK
	}
	s, ok := steps[args[0]]
	if !ok {
		fmt.Fprintf(e.Stderr, "aiagent setup: unknown step %q (steps: %s)\n", args[0], strings.Join(Names(), ", "))
		return ExitUsage
	}
	return s(ctx, e, args[1:])
}

// needRoot is the common gate: a real change needs root (exit 77), a dry run does not.
func needRoot(e *Env, dry bool, step string) bool {
	if dry || e.IsRoot == nil || e.IsRoot() {
		return true
	}
	fmt.Fprintf(e.Stderr, "aiagent setup %s: this step changes the system, so it must run as root. Run it with sudo.\n", step)
	return false
}

func (e *Env) say(format string, a ...any) { fmt.Fprintf(e.Stdout, format+"\n", a...) }

func (e *Env) groupID(name string) (int, bool) {
	if e.GroupID == nil {
		return 0, false
	}
	return e.GroupID(name)
}

type execSystemctl struct{}

func (execSystemctl) run(ctx context.Context, args ...string) error {
	path, err := execx.Lookup("systemctl")
	if err != nil {
		return fmt.Errorf("systemctl: %w", err)
	}
	res, err := execx.Exec{}.Run(ctx, execx.Cmd{Path: path, Args: args, Timeout: 2 * time.Minute, MaxOut: 64 << 10})
	if err != nil {
		return fmt.Errorf("systemctl %s: %w", strings.Join(args, " "), err)
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("systemctl %s exited %d: %s", strings.Join(args, " "), res.ExitCode, strings.TrimSpace(string(res.Stderr)))
	}
	return nil
}
func (x execSystemctl) DaemonReload(ctx context.Context) error { return x.run(ctx, "daemon-reload") }
func (x execSystemctl) Enable(ctx context.Context, u ...string) error {
	return x.run(ctx, append([]string{"enable"}, u...)...)
}
func (x execSystemctl) EnableNow(ctx context.Context, u ...string) error {
	return x.run(ctx, append([]string{"enable", "--now"}, u...)...)
}

func osGroupID(name string) (int, bool) {
	g, err := user.LookupGroup(name)
	if err != nil {
		return 0, false
	}
	n, err := strconv.Atoi(g.Gid)
	return n, err == nil
}
