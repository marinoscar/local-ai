package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/setup/harden"
)

const hardenUsage = `usage:
  aiagent setup harden [--dry-run] [--json] [--hardening] [--no-ssh] [--desktop-user <name>]
  aiagent setup harden --check [--json]
  aiagent setup harden --revert [--dry-run] [--json]

  Laptop hardening (installer.md section 4.3 P4): lid, sleep, GNOME idle, power profile, Wi-Fi power save, SSH.
  Runs only on laptops unless --hardening is given. Never restarts systemd-logind: a reboot applies the lid settings.
    --check   verify only (the LAI-INS-040..044 checks); exit 1 if anything is wrong
    --revert  undo it (used by install-node.sh --uninstall --revert-hardening)
`

type hardenFlags struct {
	dryRun, jsonOut, force, noSSH, check, revert bool
	user, stateDir, root                         string
}

func parseHardenArgs(args []string) (hardenFlags, error) {
	var f hardenFlags
	for i := 0; i < len(args); i++ {
		a := args[i]
		val := func() (string, error) {
			if i+1 >= len(args) {
				return "", fmt.Errorf("%s needs a value", a)
			}
			i++
			return args[i], nil
		}
		var err error
		switch a {
		case "--dry-run":
			f.dryRun = true
		case "--json":
			f.jsonOut = true
		case "--hardening":
			f.force = true
		case "--no-ssh":
			f.noSSH = true
		case "--check":
			f.check = true
		case "--revert":
			f.revert = true
		case "--desktop-user":
			f.user, err = val()
		case "--state-dir":
			f.stateDir, err = val()
		case "--root": // tests and VM dry runs: every path below this folder
			f.root, err = val()
		default:
			err = fmt.Errorf("unknown argument %q", a)
		}
		if err != nil {
			return f, err
		}
	}
	if f.check && f.revert {
		return f, fmt.Errorf("--check and --revert conflict")
	}
	return f, nil
}

// newHardener is replaced by tests so no test touches the machine.
var newHardener = func(f hardenFlags, out io.Writer) *harden.Hardener {
	return harden.New(harden.Paths{Root: f.root, StateDir: f.stateDir},
		harden.Options{DryRun: f.dryRun, Force: f.force, NoSSH: f.noSSH, DesktopUser: f.user}, out)
}

func init() {
	setupSteps["harden"] = func(args []string, stdout, stderr io.Writer) int {
		return runSetupHarden(context.Background(), args, stdout, stderr)
	}
}

func runSetupHarden(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	f, err := parseHardenArgs(args)
	if err != nil {
		fmt.Fprintf(stderr, "aiagent setup harden: %v\n%s", err, hardenUsage)
		return 2
	}
	log := stdout
	if f.jsonOut {
		log = stderr // stdout carries only the JSON
	}
	h := newHardener(f, log)

	if f.check {
		res := h.Verify(ctx)
		st, _ := harden.LoadState(h.Paths, h.Boot)
		if !st.Applied {
			fmt.Fprintln(log, "Laptop hardening has not been applied on this node.")
		}
		for _, r := range res {
			title, fix, _ := doctor.Text(r.Code, "en")
			fmt.Fprintf(log, "%s: %s. %s\n", r.Code, title, fix)
		}
		if f.jsonOut {
			_ = json.NewEncoder(stdout).Encode(map[string]any{"applied": st.Applied, "findings": res})
		}
		if !st.Applied {
			return 1
		}
		for _, r := range res {
			if r.Code != harden.CodeReboot { // a pending reboot is information, not a failure
				return 1
			}
		}
		return 0
	}

	var rep harden.Report
	if f.revert {
		rep, err = h.Revert(ctx)
	} else {
		rep, err = h.Run(ctx)
	}
	if err != nil {
		fmt.Fprintf(stderr, "aiagent setup harden: %v\n", err)
		return 1
	}
	if f.jsonOut {
		_ = json.NewEncoder(stdout).Encode(rep)
	}
	for _, s := range rep.Failed() {
		title, fix, _ := doctor.Text(s.Code, "en")
		if s.Code == "" {
			title, fix = s.Name, "Read the log and run `sudo install-node.sh --repair`."
		}
		fmt.Fprintf(log, "ERROR %s: %s failed: %s. Fix: %s\n", s.Code, title, s.Detail, fix)
	}
	if !f.dryRun {
		for _, line := range rep.Summary {
			fmt.Fprintf(log, "note: %s\n", line)
		}
	}
	if len(rep.Failed()) > 0 {
		return 1
	}
	return 0
}
