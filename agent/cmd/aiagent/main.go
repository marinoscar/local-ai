// Command aiagent is the Local AI node agent and, invoked as "aictl", its CLI: one static,
// multi-call binary (docs/specs/agent.md section 3.1, ADR-0001).
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/marinoscar/local-ai/agent/internal/app"
	"github.com/marinoscar/local-ai/agent/internal/cli"
)

const usage = `usage:
  aiagent serve                      run the daemon
  aiagent ctl [args...]              run the CLI (same as aictl)
  aiagent version [--json]           print the build version
  aiagent migrate llama-qwen [...]   legacy migration (not implemented yet)
  aiagent update-rollback-check      internal (not implemented yet)
`

func main() {
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
		if err := app.Run(ctx); err != nil {
			fmt.Fprintf(stderr, "aiagent: %v\n", err)
			return 1
		}
		return 0
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
		if len(args) >= 3 && args[2] == "llama-qwen" {
			fmt.Fprintln(stderr, "aiagent migrate llama-qwen: not implemented yet (ROUTER-09)")
			return 1
		}
		fmt.Fprint(stderr, usage)
		return 2
	case "update-rollback-check":
		fmt.Fprintln(stderr, "aiagent update-rollback-check: not implemented yet (AGENT-19)")
		return 1
	default:
		fmt.Fprint(stderr, usage)
		return 2
	}
}
