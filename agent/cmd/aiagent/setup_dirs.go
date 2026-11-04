package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"

	"github.com/marinoscar/local-ai/agent/internal/app"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/setup/dirs"
)

func init() { setupSteps["dirs"] = runSetupDirs }

// newDirsSystem is the machine `setup dirs` works on; tests replace it.
var newDirsSystem = func() dirs.System { return dirs.NewOSSystem() }

const setupDirsUsage = `usage: aiagent setup dirs [--dry-run] [--check] [--json] [--no-polkit] [--desktop-user <name>] [--node-id <id>] [--profile <id>]

Creates the localai user and the localai-operators group, every folder of the node with its exact owner and
mode, the /srv/localai content root with its ROOT.json, and (unless --no-polkit) the polkit rule that lets
ai-desktop start the agent. Wrong owners and modes are corrected and the old values are printed.
  --dry-run   print what would change; change nothing
  --check     like --dry-run, and exit 1 if anything would change (the installer's "already done?" test)
`

func runSetupDirs(args []string, stdout, stderr io.Writer) int {
	ctx := context.Background()
	fs := flag.NewFlagSet("setup dirs", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var (
		dry, check, asJSON, noPolkit bool
		desktop, nodeID, profile     string
	)
	fs.BoolVar(&dry, "dry-run", false, "")
	fs.BoolVar(&check, "check", false, "")
	fs.BoolVar(&asJSON, "json", false, "")
	fs.BoolVar(&noPolkit, "no-polkit", false, "")
	fs.StringVar(&desktop, "desktop-user", "", "")
	fs.StringVar(&nodeID, "node-id", "", "")
	fs.StringVar(&profile, "profile", "", "")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		fmt.Fprintf(stderr, "aiagent setup dirs: bad arguments (%s)\n\n%s", doctor.CLI005, setupDirsUsage)
		return 2
	}
	dry = dry || check
	rep, err := dirs.Run(ctx, newDirsSystem(), dirs.Options{
		DesktopUser: desktop, NoPolkit: noPolkit, DryRun: dry,
		NodeID: nodeID, AgentVersion: app.Version, Profile: profile,
	})
	if asJSON {
		b, _ := json.MarshalIndent(rep, "", "  ")
		fmt.Fprintln(stdout, string(b))
	} else {
		prefix := ""
		if dry {
			prefix = "[dry-run] would "
		}
		for _, c := range rep.Changes {
			if dry {
				fmt.Fprintf(stdout, "%s%s\n", prefix, c.String())
			} else {
				fmt.Fprintf(stdout, "done: %s\n", c.String())
			}
		}
		if len(rep.Changes) == 0 {
			fmt.Fprintln(stdout, "Users, folders and permissions are already correct.")
		}
		for _, n := range rep.Notes {
			fmt.Fprintf(stdout, "note: %s\n", n)
		}
	}
	if err != nil {
		fmt.Fprintf(stderr, "ERROR %s: could not finish setting up users and folders: %v\n", doctor.INS035, err)
		return 1
	}
	if check && rep.Pending() {
		return 1
	}
	return 0
}
