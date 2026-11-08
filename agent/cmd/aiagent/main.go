// Command aiagent is the Local AI node agent and, invoked as "aictl", its CLI: one static,
// multi-call binary (docs/specs/agent.md section 3.1, ADR-0001).
package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/marinoscar/local-ai/agent/internal/app"
	"github.com/marinoscar/local-ai/agent/internal/cli"
	"github.com/marinoscar/local-ai/agent/internal/update"
)

const usage = `usage:
  aiagent serve                      run the daemon
  aiagent ctl [args...]              run the CLI (same as aictl)
  aiagent version [--json]           print the build version
  aiagent migrate [llama-qwen] [...] legacy migration: replace llama-qwen.service in place (root; --dry-run, --rollback)
  aiagent setup <step> [flags]       internal installer steps (root only; see aiagent setup)
  aiagent update-rollback-check     internal: run by the rollback timer (agent.md section 4.10)
`

// serve is `aiagent serve`; a test replaces it so that no test starts the real daemon on the machine.
var serve = app.RunServe

func main() {
	rootUmask()
	ctx, stop := app.SignalContext(context.Background())
	defer stop()
	code := run(ctx, os.Args, os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

// run dispatches on the basename of args[0] (agent.md section 3.1). Only the basename "aictl"
// selects the CLI; any other name (aiagent, aiagent-linux-amd64, ...) is the agent.
func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	if filepath.Base(args[0]) == "aictl" {
		return cli.Main(args[1:], stdout, stderr)
	}
	if len(args) < 2 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	switch args[1] {
	case "serve":
		return serve(ctx, stderr)
	case "ctl":
		return cli.Main(args[2:], stdout, stderr)
	case "version":
		switch rest := args[2:]; {
		case len(rest) == 0:
			fmt.Fprintln(stdout, app.VersionLine())
			return 0
		case len(rest) == 1 && rest[0] == "--json":
			fmt.Fprintln(stdout, app.VersionJSON())
			return 0
		}
		fmt.Fprint(stderr, usage)
		return 2
	case "migrate":
		return runMigrate(ctx, args[2:], stdout, stderr)
	case "setup":
		return runSetup(args[2:], stdout, stderr)
	case "update-rollback-check":
		if len(args) != 2 {
			fmt.Fprint(stderr, usage)
			return 2
		}
		return runRollbackCheck(ctx, stderr)
	default:
		fmt.Fprint(stderr, usage)
		return 2
	}
}

// rollbackRunner builds the Runner of update-rollback-check: the real filesystem, systemctl and the canonical
// paths. Tests replace it.
var rollbackRunner = func(stderr io.Writer) *update.Runner {
	return &update.Runner{
		Paths:   update.DefaultPaths(),
		Current: app.Version,
		Log:     slog.New(slog.NewJSONHandler(stderr, nil)),
	}
}

// runRollbackCheck is `aiagent update-rollback-check`, started by the transient timer from aiagent.prev when
// an update was not confirmed in time (agent.md section 4.10 step 10). Exit 0 when there was nothing to do or
// the rollback was started, 1 when it could not be.
func runRollbackCheck(ctx context.Context, stderr io.Writer) int {
	out, err := rollbackRunner(stderr).RollbackCheck(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "aiagent update-rollback-check: %s: %s\n", update.CodeOf(err), update.MessageOf(err))
		return 1
	}
	fmt.Fprintf(stderr, "aiagent update-rollback-check: %s\n", out)
	return 0
}
