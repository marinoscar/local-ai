package migrate

import (
	"context"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/state"
)

// worldChecks are the doctor checks over a world's systemd, state and filesystem.
type snapState struct{ w *world }

func (s snapState) Snapshot() state.State {
	st := state.State{Migration: map[string]state.MigrationEntry{}}
	if s.w.st.has {
		st.Migration[StateKey] = s.w.st.entry
	}
	return st
}

func codesOf(rs []doctor.Result) []string {
	var out []string
	for _, r := range rs {
		out = append(out, r.Code)
	}
	return out
}

func runChecks(w *world) map[string][]string {
	out := map[string][]string{}
	for _, c := range Checks(CheckSources{Systemd: w.sd, State: snapState{w}, Paths: DefaultPaths(""), Lstat: w.fs.Lstat, Readlink: w.fs.Readlink}) {
		out[c.ID] = codesOf(c.Run(context.Background()))
	}
	return out
}

func want(t *testing.T, got map[string][]string, legacy, interrupted, key string) {
	t.Helper()
	for id, code := range map[string]string{CheckLegacyService: legacy, CheckInterrupted: interrupted, CheckLegacyKeyLink: key} {
		g := got[id]
		switch {
		case code == "" && len(g) != 0, code != "" && (len(g) != 1 || g[0] != code):
			t.Errorf("%s = %v, want %q", id, g, code)
		}
	}
}

func TestDoctorChecksFollowTheNodeThroughTheMigration(t *testing.T) {
	w := newWorld(t, worldOpts{hasCurrent: true})
	// A legacy node: the old service runs and is enabled (LAI-LLM-016); nothing else to say.
	want(t, runChecks(w), doctor.LLM016, "", "")

	// Interrupted at M3: the old service still runs, and the migration is unfinished (LAI-LLM-029).
	w = interruptedAt(t, worldOpts{hasCurrent: true}, M3)
	want(t, runChecks(w), doctor.LLM016, doctor.LLM029, "")

	// Migrated: the old service is masked, the key path is a link: all quiet.
	w = migrated(t, worldOpts{hasCurrent: true})
	want(t, runChecks(w), "", "", "")

	// Somebody put a regular file back at the old key path (LAI-SEC-015), or a link to another file.
	w.fs.put("/etc/llama-qwen/api-key", []byte(w.key), 0o600)
	want(t, runChecks(w), "", "", doctor.SEC015)
	_ = w.fs.Remove("/etc/llama-qwen/api-key")
	_ = w.fs.Symlink("/etc/somewhere/else", "/etc/llama-qwen/api-key")
	want(t, runChecks(w), "", "", doctor.SEC015)
	_ = w.fs.Remove("/etc/llama-qwen/api-key")
	want(t, runChecks(w), "", "", "") // the old path is gone: nothing reads it

	// The migration command repairs the link and the finding goes away.
	w.fs.put("/etc/llama-qwen/api-key", []byte(w.key), 0o600)
	if _, err := w.run(w.engine()); err != nil {
		t.Fatal(err)
	}
	want(t, runChecks(w), "", "", "")

	// Rolled back: the old service runs on purpose, and the doctor says so (LLM-016); the key path is
	// a regular file but the migration is not "done", so no SEC-015.
	w = migrated(t, worldOpts{hasCurrent: true})
	if _, err := rollbackOf(w, false); err != nil {
		t.Fatal(err)
	}
	want(t, runChecks(w), doctor.LLM016, "", "")

	// A rollback that was cut short is an interrupted migration too.
	w = migrated(t, worldOpts{hasCurrent: true})
	w.sd.fail["start "+LegacyUnitName] = context.DeadlineExceeded
	_, _ = rollbackOf(w, false)
	if got := runChecks(w); len(got[CheckInterrupted]) != 1 || got[CheckInterrupted][0] != doctor.LLM029 {
		t.Fatalf("a half rollback: %v", got)
	}
}

func TestDoctorCheckDetailsAndRegistration(t *testing.T) {
	w := newWorld(t, worldOpts{hasCurrent: true})
	w.st.entry = state.MigrationEntry{State: StateInProgress, Step: M4, At: time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)}
	w.st.has = true
	src := CheckSources{Systemd: w.sd, State: snapState{w}, Paths: DefaultPaths("")}
	for _, c := range Checks(src) {
		for _, r := range c.Run(context.Background()) {
			if r.Details == nil {
				t.Errorf("%s: no details", c.ID)
			}
			if c.ID == CheckInterrupted && (r.Details["step"] != M4 || r.Details["state"] != StateInProgress) {
				t.Errorf("details: %v", r.Details)
			}
			if _, _, ok := doctor.Text(r.Code, "en"); !ok {
				t.Errorf("%s is not in the doctor table", r.Code)
			}
		}
	}
	reg := doctor.NewRegistry()
	if err := RegisterChecks(reg, src); err != nil {
		t.Fatal(err)
	}
	if err := RegisterChecks(reg, src); err == nil {
		t.Fatal("registering twice must fail")
	}
	// Without sources there are no checks.
	if n := len(Checks(CheckSources{})); n != 0 {
		t.Fatalf("%d checks", n)
	}
}
