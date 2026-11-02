package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/marinoscar/local-ai/agent/internal/catalog"
)

// The `aictl catalog` group. Only `lint` exists so far (CAT-01, aictl.md 4.11.1): it is a [local]
// command, so it runs in-process with no agent, no root and no network (except --online). It is a
// thin hook: the real command framework (AICTL-01: global flags, agent transport, exit-code map)
// does not exist yet, so flags are parsed by hand here and the command returns the two exit codes
// of the catalog.md section 6 contract. refresh, restore and use --test belong to AICTL-06/07.

const catalogHelp = `aictl catalog: the catalog of models and ark content.

Usage:
  aictl catalog lint [--online] [--json] <dir>

lint validates a catalog directory (models.yaml, ark.yaml, profiles/, CATALOG_VERSION).
  --online   also HEAD-check every URL and compare sizes and hashes (needs network)
  --json     print {"valid": bool, "problems": [...]} instead of text
Exit 0: valid. Exit 2: invalid (or bad usage).
Specification: docs/specs/catalog.md section 6, docs/specs/aictl.md section 4.11.1
`

// lintCatalog is catalog.Lint; tests replace it to observe the options.
var lintCatalog = catalog.Lint

func catalogCommand(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, catalogHelp)
		return ExitUsage
	}
	switch args[0] {
	case "--help", "-h", "help":
		fmt.Fprint(stdout, catalogHelp)
		return ExitOK
	case "lint":
		return catalogLintCommand(args[1:], stdout, stderr)
	case "refresh":
		return notImplemented(stderr, "catalog refresh", "AICTL-06")
	case "restore", "use":
		return notImplemented(stderr, "catalog "+args[0], "AICTL-07")
	default:
		fmt.Fprintf(stderr, "aictl catalog: unknown subcommand %q (try 'aictl catalog --help')\n", args[0])
		return ExitUsage
	}
}

func notImplemented(stderr io.Writer, name, item string) int {
	fmt.Fprintf(stderr, "aictl %s: not implemented yet (%s)\n", name, item)
	return 1
}

func catalogLintCommand(args []string, stdout, stderr io.Writer) int {
	var online, asJSON bool
	var dir string
	for _, a := range args {
		switch {
		case a == "--online":
			online = true
		case a == "--json":
			asJSON = true
		case a == "--help" || a == "-h":
			fmt.Fprint(stdout, catalogHelp)
			return ExitOK
		case len(a) > 1 && a[0] == '-':
			fmt.Fprintf(stderr, "aictl catalog lint: unknown flag %q\n\n%s", a, catalogHelp)
			return ExitUsage
		case dir == "":
			dir = a
		default:
			fmt.Fprintf(stderr, "aictl catalog lint: unexpected argument %q (one directory only)\n", a)
			return ExitUsage
		}
	}
	if dir == "" {
		fmt.Fprintf(stderr, "aictl catalog lint: a catalog directory is required\n\n%s", catalogHelp)
		return ExitUsage
	}

	rep := lintCatalog(dir, catalog.LintOptions{Online: online, Context: context.Background()})
	if asJSON {
		out, err := json.Marshal(rep)
		if err != nil { // cannot happen: the report is plain data
			fmt.Fprintf(stderr, "aictl catalog lint: %v\n", err)
			return 1
		}
		fmt.Fprintln(stdout, string(out))
	} else {
		for _, line := range rep.FormatLines(dir) {
			fmt.Fprintln(stdout, line)
		}
		switch n := len(rep.Problems); {
		case n == 0:
			fmt.Fprintf(stdout, "✓ Catalog %s is valid (%d models, %d content items, %d profiles).\n", rep.Version, rep.Models, rep.Content, rep.Profiles)
		case n == 1:
			fmt.Fprintln(stdout, "✗ 1 problem found.")
		default:
			fmt.Fprintf(stdout, "✗ %d problems found.\n", n)
		}
	}
	if rep.Valid() {
		return ExitOK
	}
	return ExitUsage // exit 2: the catalog is invalid (aictl.md 3.4, catalog.md 6)
}
