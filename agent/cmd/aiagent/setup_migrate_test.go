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

func TestSetupMigrate(t *testing.T) {
	oldE, oldN, oldM := geteuid, newMigrator, migrateInteractive
	defer func() { geteuid, newMigrator, migrateInteractive = oldE, oldN, oldM }()

	rolled := &migrate.Error{Code: doctor.LLM018, Step: migrate.M7, Message: "health check failed", RolledBack: true}
	tests := []struct {
		name     string
		euid     int
		args     []string
		pending  bool
		runErr   error
		want     int
		inOut    string // must appear on stdout
		inErr    string // must appear on stderr
		wantCall string // engine calls joined
	}{
		{"no step", 0, nil, false, nil, 2, "", "usage:", ""},
		{"unknown step", 0, []string{"bogus"}, false, nil, 2, "", "unknown step", ""},
		{"not root", 1000, []string{"migrate"}, false, nil, 77, "", "must run as root", ""},
		{"bad option", 0, []string{"migrate", "--x"}, false, nil, 2, "", "unknown argument", ""},
		{"nothing pending", 0, []string{"migrate"}, false, nil, 0, "Nothing to migrate.", "", "pending close"},
		{"pending and done", 0, []string{"migrate"}, true, nil, 0, "finished: ok", "", "pending prepare render run close"},
		{"failed, rolled back", 0, []string{"migrate"}, true, rolled, 20, "", "running again", "pending prepare render run close"},
		{"failed, not rolled back", 0, []string{"migrate"}, true, errors.New("boom"), 1, "FAILED (exit 1)", "boom", "pending prepare render run close"},
		{"json output", 0, []string{"migrate", "--json"}, false, nil, 0, `"ok":true,"rolled_back":false,"exit":0`, "", "pending close"},
		{"dry run", 0, []string{"migrate", "--dry-run"}, true, nil, 0, "Dry run: nothing was changed.", "", "pending prepare render close"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			geteuid = func() int { return tc.euid }
			migrateInteractive = func() bool { return false }
			fm := &fakeMigrator{pending: tc.pending, why: "llama-qwen.service", runErr: tc.runErr,
				plan: &migrate.Plan{Mode: migrate.ModeFresh}}
			newMigrator = func(context.Context, migrateFlags, io.Writer) (migrator, func(), error) {
				return fm, func() { fm.calls = append(fm.calls, "close") }, nil
			}
			var out, errb bytes.Buffer
			got := runSetup(tc.args, &out, &errb)
			if got != tc.want {
				t.Fatalf("exit = %d, want %d\nstdout: %s\nstderr: %s", got, tc.want, out.String(), errb.String())
			}
			if !strings.Contains(out.String(), tc.inOut) {
				t.Errorf("stdout lacks %q: %s", tc.inOut, out.String())
			}
			if !strings.Contains(errb.String(), tc.inErr) {
				t.Errorf("stderr lacks %q: %s", tc.inErr, errb.String())
			}
			if g := strings.Join(fm.calls, " "); g != tc.wantCall {
				t.Errorf("engine calls = %q, want %q", g, tc.wantCall)
			}
		})
	}
}
