package cli

import (
	"encoding/json"
	"flag"
	"fmt"

	"github.com/marinoscar/local-ai/agent/internal/catalog"
)

// The `aictl catalog` group. Only `lint` exists so far (CAT-01, aictl.md 4.11.1): it is a [local]
// command, so it runs in-process with no agent, no root and no network (except --online), and it
// never builds the agent client. It returns the two exit codes of the catalog.md section 6
// contract: 0 valid, 2 invalid. refresh belongs to AICTL-06 and registers here as a stub so the
// group's help is complete; restore and use --test are AICTL-07 (cmd_catalog_local.go).

// lintCatalog is catalog.Lint; tests replace it to observe the options.
var lintCatalog = catalog.Lint

func init() {
	Register(&Command{
		Name:    "catalog",
		Summary: "Check and manage the catalog of models and ark content.",
		Sub: []*Command{
			{
				Name:    "lint",
				Summary: "Validate a catalog directory (models.yaml, ark.yaml, profiles/, CATALOG_VERSION).",
				Usage:   "[--online] [--json] <dir>",
				Description: `Exit 0: valid. Exit 2: invalid (or bad usage).
With --online every URL is HEAD-checked and sizes and hashes are compared (needs network).
With --json it prints {"valid": bool, "problems": [...]} instead of text.
Specification: docs/specs/catalog.md section 6, docs/specs/aictl.md section 4.11.1`,
				Marks: MarkLocal,
				Flags: func(fs *flag.FlagSet) {
					fs.Bool("online", false, "also HEAD-check every URL and compare sizes and hashes (needs network)")
					fs.Bool("json", false, "print the report as one JSON document")
				},
				Run: catalogLint,
			},
			catalogRestoreCommand(),
			catalogUseCommand(),
		},
	})
}

func catalogLint(c *Call) error {
	switch len(c.Args) {
	case 0:
		return c.Err(CodeUsage, "a catalog directory is required")
	case 1:
	default:
		return c.Err(CodeUsage, fmt.Sprintf("unexpected argument %q (one directory only)", c.Args[1]))
	}
	dir := c.Args[0]
	asJSON := c.Global.JSON || c.Bool("json")

	rep := lintCatalog(dir, catalog.LintOptions{Online: c.Bool("online"), Context: c.Ctx})
	printLintReport(c, dir, rep, asJSON)
	if rep.Valid() {
		return nil
	}
	return ExitWith(ExitUsage) // exit 2: the catalog is invalid (aictl.md 3.4, catalog.md 6)
}

// printLintReport prints a lint result the way `catalog lint` does (aictl.md 4.11.1): one line per
// problem and a summary, or the --json document, on stdout. `catalog use --test` shares it.
func printLintReport(c *Call, dir string, rep catalog.Report, asJSON bool) {
	if asJSON {
		out, err := json.Marshal(rep)
		if err != nil { // cannot happen: the report is plain data
			out = []byte(`{"valid":false,"problems":[]}`)
		}
		fmt.Fprintln(c.Stdout, string(out))
		return
	}
	for _, line := range rep.FormatLines(dir) {
		fmt.Fprintln(c.Stdout, line)
	}
	switch n := len(rep.Problems); {
	case n == 0:
		fmt.Fprintf(c.Stdout, "✓ Catalog %s is valid (%d models, %d content items, %d profiles).\n", rep.Version, rep.Models, rep.Content, rep.Profiles)
	case n == 1:
		fmt.Fprintln(c.Stdout, "✗ 1 problem found.")
	default:
		fmt.Fprintf(c.Stdout, "✗ %d problems found.\n", n)
	}
}
