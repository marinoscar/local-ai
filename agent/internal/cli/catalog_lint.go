package cli

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"

	"github.com/marinoscar/local-ai/agent/internal/catalog"
)

// The `aictl catalog` group. Only `lint` exists so far (CAT-01, aictl.md 4.11.1): it is a [local]
// command, so it runs in-process with no agent, no root and no network (except --online), and it
// never builds the agent client. It returns the two exit codes of the catalog.md section 6
// contract: 0 valid, 2 invalid. refresh, restore and use --test belong to AICTL-06/07 and register
// here as stubs so the group's help is complete.

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
			{Name: "restore", Summary: "Put the previous catalog back (not implemented yet).", Marks: MarkLocal | MarkRoot,
				Run: notImplemented("catalog restore", "AICTL-07")},
			{Name: "use", Summary: "Install a catalog directory after testing it (not implemented yet).", Usage: "<dir> --test",
				Marks: MarkLocal | MarkRoot,
				Flags: func(fs *flag.FlagSet) { fs.Bool("test", false, "lint the directory first") },
				Run:   notImplemented("catalog use", "AICTL-07")},
		},
	})
}

// notImplemented is the Run of a command a later issue owns. It is an untyped error: exit 1.
func notImplemented(name, item string) func(*Call) error {
	return func(*Call) error {
		return fmt.Errorf("aictl %s: not implemented yet (%s)", name, item)
	}
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
	if asJSON {
		out, err := json.Marshal(rep)
		if err != nil { // cannot happen: the report is plain data
			return errors.New("aictl catalog lint: " + err.Error())
		}
		fmt.Fprintln(c.Stdout, string(out))
	} else {
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
	if rep.Valid() {
		return nil
	}
	return ExitWith(ExitUsage) // exit 2: the catalog is invalid (aictl.md 3.4, catalog.md 6)
}
