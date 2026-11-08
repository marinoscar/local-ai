package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/router/migrate"
)

// fakeMigrator records what the command asked of the engine.
type fakeMigrator struct {
	pending    bool
	why        string
	plan       *migrate.Plan
	prepareErr error
	runErr     error
	rollErr    error
	calls      []string
}

func (f *fakeMigrator) Pending(context.Context) (bool, string, error) {
	f.calls = append(f.calls, "pending")
	return f.pending, f.why, nil
}

func (f *fakeMigrator) Prepare(context.Context) (*migrate.Plan, error) {
	f.calls = append(f.calls, "prepare")
	return f.plan, f.prepareErr
}

func (f *fakeMigrator) RenderPlan(w io.Writer, p *migrate.Plan) {
	f.calls = append(f.calls, "render")
	io.WriteString(w, "M0 preflight: ok\n")
}

func (f *fakeMigrator) RollbackSteps(bool) []string { return []string{"R1 stop", "R2 unmask"} }

func (f *fakeMigrator) Run(context.Context, *migrate.Plan) (*migrate.Result, error) {
	f.calls = append(f.calls, "run")
	return &migrate.Result{State: "done"}, f.runErr
}

func (f *fakeMigrator) Rollback(context.Context) (*migrate.Result, error) {
	f.calls = append(f.calls, "rollback")
	return &migrate.Result{State: "rolled_back"}, f.rollErr
}

func withMigrator(t *testing.T, m *fakeMigrator, interactive bool, stdin string, args ...string) (int, string, string) {
	t.Helper()
	oldN, oldI, oldS := newMigrator, migrateInteractive, migrateStdin
	var gotFlags migrateFlags
	newMigrator = func(_ context.Context, f migrateFlags, _ io.Writer) (migrator, func(), error) {
		gotFlags = f
		return m, func() { m.calls = append(m.calls, "close") }, nil
	}
	migrateInteractive = func() bool { return interactive }
	migrateStdin = strings.NewReader(stdin)
	t.Cleanup(func() { newMigrator, migrateInteractive, migrateStdin = oldN, oldI, oldS })
	var out, errb bytes.Buffer
	code := run(context.Background(), append([]string{"aiagent", "migrate"}, args...), &out, &errb)
	_ = gotFlags
	return code, out.String(), errb.String()
}

func okPlan() *migrate.Plan { return &migrate.Plan{Mode: migrate.ModeFresh} }

func badPlan() *migrate.Plan {
	return &migrate.Plan{Mode: migrate.ModeFresh, Problems: []migrate.Problem{{Code: doctor.LLM017, Cause: "unmatched_file", Message: "model file not in the catalog: x.gguf"}}}
}

func has(calls []string, c string) bool {
	for _, v := range calls {
		if v == c {
			return true
		}
	}
	return false
}

func TestMigrateCommand(t *testing.T) {
	cases := []struct {
		name        string
		m           *fakeMigrator
		interactive bool
		stdin       string
		args        []string
		wantCode    int
		outHas      string
		errHas      string
		wantRun     bool
		wantRoll    bool
	}{
		{name: "dry run that would pass", m: &fakeMigrator{plan: okPlan()}, args: []string{"llama-qwen", "--dry-run"}, wantCode: 0, outHas: "Dry run: nothing was changed."},
		{name: "dry run that would fail", m: &fakeMigrator{plan: badPlan()}, args: []string{"llama-qwen", "--dry-run"}, wantCode: 1},
		{name: "no argument and nothing pending", m: &fakeMigrator{}, wantCode: 0, outHas: "Nothing to migrate."},
		{name: "no argument with a pending migration", m: &fakeMigrator{pending: true, why: "llama-qwen.service is installed", plan: okPlan()}, args: []string{"--yes"}, wantCode: 0, outHas: "Pending: llama-qwen.service is installed.", wantRun: true},
		{name: "refused plan prints the code and the fix", m: &fakeMigrator{plan: badPlan()}, args: []string{"llama-qwen", "--yes"}, wantCode: 1, errHas: "What to do:"},
		{name: "no terminal and no --yes does not touch the node", m: &fakeMigrator{plan: okPlan()}, args: []string{"llama-qwen"}, wantCode: 2, errHas: "LAI-CLI-007"},
		{name: "--yes runs without asking", m: &fakeMigrator{plan: okPlan()}, args: []string{"llama-qwen", "--yes"}, wantCode: 0, wantRun: true},
		{name: "a person types yes", m: &fakeMigrator{plan: okPlan()}, interactive: true, stdin: "yes\n", args: []string{"llama-qwen"}, wantCode: 0, outHas: "Type yes to continue", wantRun: true},
		{name: "a person types something else", m: &fakeMigrator{plan: okPlan()}, interactive: true, stdin: "y\n", args: []string{"llama-qwen"}, wantCode: 1, outHas: "Nothing was changed."},
		{name: "end of input is a no", m: &fakeMigrator{plan: okPlan()}, interactive: true, stdin: "", args: []string{"llama-qwen"}, wantCode: 1, outHas: "Nothing was changed."},
		{name: "a failed run prints its code", m: &fakeMigrator{plan: okPlan(), runErr: &migrate.Error{Code: doctor.LLM029, Step: "M8", Message: "the contract broke", RolledBack: true}},
			args: []string{"llama-qwen", "--yes"}, wantCode: 1, wantRun: true, errHas: "The old llama service is running again"},
		{name: "a finished migration is not confirmed again", m: &fakeMigrator{plan: &migrate.Plan{Mode: migrate.ModeDone}}, args: []string{"llama-qwen"}, wantCode: 0, wantRun: true},
		{name: "a failed preflight read", m: &fakeMigrator{prepareErr: errors.New("boom")}, args: []string{"llama-qwen", "--yes"}, wantCode: 1, errHas: "boom"},
		{name: "rollback dry run", m: &fakeMigrator{}, args: []string{"llama-qwen", "--rollback", "--dry-run"}, wantCode: 0, outHas: "R1 stop"},
		{name: "rollback with --yes", m: &fakeMigrator{}, args: []string{"llama-qwen", "--rollback", "--yes", "--restore-files"}, wantCode: 0, wantRoll: true},
		{name: "rollback needs a confirmation too", m: &fakeMigrator{}, args: []string{"llama-qwen", "--rollback"}, wantCode: 2, errHas: "LAI-CLI-007"},
		{name: "a failed rollback", m: &fakeMigrator{rollErr: &migrate.Error{Code: doctor.LLM029, Step: "R5", Message: "the old service did not answer"}},
			args: []string{"llama-qwen", "--rollback", "--yes"}, wantCode: 1, wantRoll: true, errHas: "R5"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			code, out, errs := withMigrator(t, c.m, c.interactive, c.stdin, c.args...)
			if code != c.wantCode {
				t.Fatalf("exit %d, want %d\nstdout: %s\nstderr: %s", code, c.wantCode, out, errs)
			}
			if !strings.Contains(out, c.outHas) || !strings.Contains(errs, c.errHas) {
				t.Fatalf("stdout %q (want %q)\nstderr %q (want %q)", out, c.outHas, errs, c.errHas)
			}
			if has(c.m.calls, "run") != c.wantRun || has(c.m.calls, "rollback") != c.wantRoll {
				t.Fatalf("calls %v", c.m.calls)
			}
			if code == 2 && (has(c.m.calls, "run") || has(c.m.calls, "rollback")) {
				t.Fatalf("an unconfirmed command changed the node: %v", c.m.calls)
			}
			if !has(c.m.calls, "close") {
				t.Fatalf("the migrator was not released: %v", c.m.calls)
			}
		})
	}
}

func TestMigrateArguments(t *testing.T) {
	good := map[string]migrateFlags{
		"llama-qwen --dry-run":                    {target: "llama-qwen", dryRun: true},
		"llama-qwen --rollback --restore-files":   {target: "llama-qwen", rollback: true, restore: true},
		"llama-qwen --accept-catalog-flags --yes": {target: "llama-qwen", accept: true, yes: true},
		"":          {},
		"--dry-run": {dryRun: true},
	}
	for in, want := range good {
		got, err := parseMigrateArgs(strings.Fields(in))
		if err != nil || got != want {
			t.Errorf("%q: %+v %v", in, got, err)
		}
	}
	for _, in := range []string{
		"llama-qwen --restore-files", "llama-qwen --rollback --accept-catalog-flags", "--rollback", "other-thing", "llama-qwen --nope",
	} {
		if _, err := parseMigrateArgs(strings.Fields(in)); err == nil {
			t.Errorf("%q was accepted", in)
		}
	}
}

func TestMigrateNeedsRoot(t *testing.T) {
	code, _, errs := runCmd(t, context.Background(), "aiagent", "migrate", "llama-qwen", "--dry-run")
	if code != 1 || !strings.Contains(errs, "LAI-CLI-006") || !strings.Contains(errs, "sudo aictl") {
		t.Fatalf("exit %d: %s", code, errs)
	}
}
