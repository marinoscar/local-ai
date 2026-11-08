package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"

	"github.com/marinoscar/local-ai/agent/internal/app"
	"github.com/marinoscar/local-ai/agent/internal/cli/local"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/router/migrate"
	"github.com/marinoscar/local-ai/agent/internal/router/migrate/host"
)

// migrateUsage is the help of `aiagent migrate` (llama-router.md section 4.9, aictl.md section 4.28).
const migrateUsage = `usage:
  aiagent migrate [llama-qwen] [--dry-run] [--rollback] [--accept-catalog-flags] [--restore-files] [--yes]

  aiagent migrate                  runs every pending migration (today: llama-qwen); exits 0 when there is nothing to do
  aiagent migrate llama-qwen       replace the old llama unit (llama-qwen.service, or llama-coder-next.service
                                   when that one serves) with localai-llama.service in place (root only)
    --dry-run                      print every step and change nothing; exit 0 only if all would pass
    --rollback                     put the old llama unit back (R1 to R6)
    --accept-catalog-flags         use the catalog's settings where the old unit differs
    --restore-files                with --rollback: move the model files back to their old paths
    --yes                          do not ask for confirmation (the installer passes it)
`

// migrateFlags are the options of the command.
type migrateFlags struct {
	// target is "llama-qwen", or "" for every pending migration.
	target                                 string
	dryRun, rollback, accept, restore, yes bool
}

func parseMigrateArgs(args []string) (migrateFlags, error) {
	var f migrateFlags
	for _, a := range args {
		switch a {
		case "llama-qwen":
			f.target = a
		case "--dry-run":
			f.dryRun = true
		case "--rollback":
			f.rollback = true
		case "--accept-catalog-flags":
			f.accept = true
		case "--restore-files":
			f.restore = true
		case "--yes", "-y":
			f.yes = true
		default:
			return f, fmt.Errorf("unknown argument %q", a)
		}
	}
	if f.restore && !f.rollback {
		return f, errors.New("--restore-files only makes sense with --rollback")
	}
	if f.rollback && f.accept {
		return f, errors.New("--accept-catalog-flags does not apply to --rollback")
	}
	if f.rollback && f.target == "" {
		return f, errors.New("--rollback needs the migration's name: aiagent migrate llama-qwen --rollback")
	}
	return f, nil
}

// migrator is the part of migrate.Engine the command uses; tests replace newMigrator with a fake.
type migrator interface {
	Pending(ctx context.Context) (bool, string, error)
	Prepare(ctx context.Context) (*migrate.Plan, error)
	RenderPlan(w io.Writer, p *migrate.Plan)
	RollbackSteps(restoreFiles bool) []string
	Run(ctx context.Context, p *migrate.Plan) (*migrate.Result, error)
	Rollback(ctx context.Context) (*migrate.Result, error)
}

// newMigrator builds the real engine on this machine: it must be root, takes the lock that every command
// changing node files takes, and opens node.yaml and state.json. The second result releases them.
var newMigrator = func(ctx context.Context, f migrateFlags, stdout io.Writer) (migrator, func(), error) {
	if os.Geteuid() != 0 {
		return nil, nil, &migrateError{code: doctor.CLI006, msg: "aiagent migrate must be run as root", exit: 1}
	}
	env := local.DefaultEnv(app.Version)
	release := func() {}
	if !f.dryRun {
		rel, err := local.Lock(env)
		if err != nil {
			return nil, nil, &migrateError{code: doctor.AGT022, msg: err.Error(), exit: 1}
		}
		release = rel
	}
	m, err := host.Assemble(ctx, env, app.Version, f.accept, f.restore)
	if err != nil {
		release()
		return nil, nil, err
	}
	m.Options.Out = stdout
	return migrate.New(m.Options), func() { m.Close(); release() }, nil
}

// migrateStdin is where the confirmation is read from; migrateInteractive says whether it is a person.
var (
	migrateStdin       io.Reader = os.Stdin
	migrateInteractive           = func() bool { return term.IsTerminal(int(os.Stdin.Fd())) }
)

// migrateRolledBack is set when the last runMigrate ended in a failure that the engine rolled back; `aiagent
// setup migrate` reads it to tell the installer (exit setupExitRolledBack).
var migrateRolledBack bool

type migrateError struct {
	code, msg string
	exit      int
}

func (e *migrateError) Error() string { return e.code + ": " + e.msg }

// runMigrate is `aiagent migrate`. Exit 0: done, nothing to do, a dry run that would pass. 1: it failed
// or was refused (the code and the fix are on stderr). 2: bad usage or no confirmation.
func runMigrate(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	migrateRolledBack = false
	f, err := parseMigrateArgs(args)
	if err != nil {
		fmt.Fprintf(stderr, "aiagent migrate: %v (%s)\n\n%s", err, doctor.CLI005, migrateUsage)
		return 2
	}
	m, closeFn, err := newMigrator(ctx, f, stdout)
	if err != nil {
		return reportMigrateError(stderr, err)
	}
	defer closeFn()
	if f.rollback {
		return runRollback(ctx, f, m, stdout, stderr)
	}
	if f.target == "" {
		pending, why, err := m.Pending(ctx)
		if err != nil {
			return reportMigrateError(stderr, err)
		}
		if !pending {
			fmt.Fprintln(stdout, "Nothing to migrate.")
			return 0
		}
		fmt.Fprintf(stdout, "Pending: %s.\n", why)
	}
	plan, err := m.Prepare(ctx)
	if err != nil {
		return reportMigrateError(stderr, err)
	}
	m.RenderPlan(stdout, plan)
	if f.dryRun {
		if !plan.OK() {
			return 1
		}
		fmt.Fprintln(stdout, "\nDry run: nothing was changed.")
		return 0
	}
	if !plan.OK() {
		return reportMigrateError(stderr, migrateProblems(plan))
	}
	if plan.Mode == migrate.ModeFresh || plan.Mode == migrate.ModeResume {
		if code := confirmMigrate(f, "Change this node as described above?", stdout, stderr); code != 0 {
			return code
		}
	}
	if _, err := m.Run(ctx, plan); err != nil {
		return reportMigrateError(stderr, err)
	}
	return 0
}

func migrateProblems(p *migrate.Plan) error {
	e := &migrate.Error{Code: p.Problems[0].Code, Step: migrate.M0, Message: p.Problems[0].Message}
	return e
}

func runRollback(ctx context.Context, f migrateFlags, m migrator, stdout, stderr io.Writer) int {
	if f.dryRun {
		fmt.Fprintln(stdout, "Rollback of the llama-qwen migration, step by step:")
		for _, l := range m.RollbackSteps(f.restore) {
			fmt.Fprintln(stdout, l)
		}
		fmt.Fprintln(stdout, "\nDry run: nothing was changed.")
		return 0
	}
	for _, l := range m.RollbackSteps(f.restore) {
		fmt.Fprintln(stdout, l)
	}
	if code := confirmMigrate(f, "Roll the node back to the old llama service?", stdout, stderr); code != 0 {
		return code
	}
	if _, err := m.Rollback(ctx); err != nil {
		return reportMigrateError(stderr, err)
	}
	return 0
}

// confirmMigrate asks for a yes at a terminal; --yes skips it; without a terminal and without --yes the
// node is not touched (LAI-CLI-007).
func confirmMigrate(f migrateFlags, question string, stdout, stderr io.Writer) int {
	if f.yes {
		return 0
	}
	if !migrateInteractive() {
		fmt.Fprintf(stderr, "%s: %s\nWhat to do: run it in a terminal, or add --yes.\n", doctor.CLI007, "this command changes the node and needs a yes/no confirmation")
		return 2
	}
	fmt.Fprintf(stdout, "\n%s Type yes to continue: ", question)
	line, err := bufio.NewReader(migrateStdin).ReadString('\n')
	if err != nil && line == "" {
		fmt.Fprintln(stdout)
		fmt.Fprintln(stdout, "Nothing was changed.")
		return 1
	}
	if strings.ToLower(strings.TrimSpace(line)) != "yes" {
		fmt.Fprintln(stdout, "Nothing was changed.")
		return 1
	}
	return 0
}

// reportMigrateError prints an error as `CODE: title`, the message and the fix, and returns the exit code.
func reportMigrateError(stderr io.Writer, err error) int {
	code := migrate.CodeOf(err)
	var me *migrateError
	exit := 1
	if errors.As(err, &me) {
		code, exit = me.code, me.exit
	}
	var e *migrate.Error
	if errors.As(err, &e) {
		fmt.Fprintf(stderr, "aiagent migrate: %s\n", e.Error())
		if e.RolledBack {
			migrateRolledBack = true
			fmt.Fprintln(stderr, "The old llama service is running again, as before. Nothing else has to be done.")
		}
	} else {
		fmt.Fprintf(stderr, "aiagent migrate: %v\n", err)
	}
	if code != "" {
		if title, fix, ok := doctor.Text(code, "en"); ok {
			fmt.Fprintf(stderr, "%s: %s\nWhat to do: %s\n", code, title, fix)
		}
	}
	return exit
}
