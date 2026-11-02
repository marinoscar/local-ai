// Package cli is the aictl implementation (docs/specs/aictl.md). This is still the AGENT-01 stub: it
// knows help and the local `catalog lint` hook (CAT-01). Real commands and the agent transport arrive
// with AICTL-01 onwards.
package cli

import (
	"fmt"
	"io"
)

// Exit codes (aictl.md section 3.4) used so far.
const (
	ExitOK    = 0
	ExitUsage = 2
)

const helpText = `aictl: command line for the Local AI node agent.

Usage:
  aictl help
  aictl --help
  aictl catalog lint [--online] [--json] <dir>

The real commands (status, doctor, mode, models, ark, ...) are not implemented yet.
Specification: docs/specs/aictl.md
`

// Main runs the CLI with args (without the program name) and returns the exit code.
func Main(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, helpText)
		return ExitUsage
	}
	switch args[0] {
	case "--help", "-h", "help":
		fmt.Fprint(stdout, helpText)
		return ExitOK
	case "catalog":
		return catalogCommand(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "aictl: unknown command %q (try 'aictl --help')\n", args[0])
		return ExitUsage
	}
}
