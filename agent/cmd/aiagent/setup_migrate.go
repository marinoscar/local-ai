package main

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/app"
)

const setupMigrateUsage = `usage:
  aiagent setup migrate [--dry-run] [--json]

Runs "aiagent migrate --yes" (every pending migration; today llama-qwen) and adds the installer's log lines.
Exit: 0 done or nothing to do, 1 failed or refused, 2 bad usage, 20 failed and rolled back (the old service
is running again), 77 not root.
`

// setupMigrateNow is the clock of the log lines; a test replaces it.
var setupMigrateNow = time.Now

// Exit codes of `aiagent setup migrate` (installer.md section 3.5). Root is checked by runSetup (exit 77).
const (
	setupExitUsage      = 2
	setupExitRolledBack = 20 // the migration failed and its own rollback restored llama-qwen.service
)

// setupMigrate is the `migrate` step of `aiagent setup`.
func setupMigrate(args []string, stdout, stderr io.Writer) int {
	ctx, stop := app.SignalContext(context.Background())
	defer stop()
	return runSetupMigrate(ctx, args, stdout, stderr)
}

// runSetupMigrate is `aiagent setup migrate`. It adds no migration logic: it calls runMigrate with --yes (the
// installer has already asked the person) and reports the outcome in a form the installer can map to LAI-INS-033.
func runSetupMigrate(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	var dry, asJSON bool
	for _, a := range args {
		switch a {
		case "--dry-run":
			dry = true
		case "--json":
			asJSON = true
		default:
			fmt.Fprintf(stderr, "aiagent setup migrate: unknown argument %q\n\n%s", a, setupMigrateUsage)
			return setupExitUsage
		}
	}
	margs := []string{"--yes"}
	if dry {
		margs = append(margs, "--dry-run")
	}
	say := func(format string, a ...any) {
		w := stdout
		if asJSON {
			w = stderr // stdout carries the JSON document alone
		}
		fmt.Fprintf(w, "%s [setup migrate] %s\n", setupMigrateNow().UTC().Format("2006-01-02T15:04:05Z"), fmt.Sprintf(format, a...))
	}
	say("starting: aiagent migrate %v", margs)
	code := runMigrate(ctx, margs, stdout, stderr)
	rolled := code != 0 && migrateRolledBack
	if rolled {
		code = setupExitRolledBack
	}
	switch {
	case code == 0:
		say("finished: ok")
	case rolled:
		say("finished: FAILED and rolled back; the old llama-qwen.service is running again")
	default:
		say("finished: FAILED (exit %d); read the messages above", code)
	}
	if asJSON {
		fmt.Fprintf(stdout, "{\"step\":\"migrate\",\"dry_run\":%t,\"ok\":%t,\"rolled_back\":%t,\"exit\":%d}\n", dry, code == 0, rolled, code)
	}
	return code
}
