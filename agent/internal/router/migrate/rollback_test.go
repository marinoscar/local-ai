package migrate

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
)

func migrated(t *testing.T, o worldOpts) *world {
	t.Helper()
	w := newWorld(t, o)
	if _, err := w.run(w.engine()); err != nil {
		t.Fatalf("%v\n%s", err, w.out.String())
	}
	assertMigrated(t, w, "setup")
	w.sd.calls = nil
	w.out = &lockedBuf{}
	return w
}

// interruptedAt returns a world whose migration was killed right after step was recorded, and rebooted.
func interruptedAt(t *testing.T, o worldOpts, step string) *world {
	t.Helper()
	at := 0
	ref := newWorld(t, o)
	e := ref.engine()
	e.afterSave = func(p Progress) {
		if p.Step == step && p.Rollback == "" {
			at = ref.k.count()
		}
	}
	if _, err := ref.run(e); err != nil {
		t.Fatal(err)
	}
	w := newWorld(t, o)
	w.k.at, w.k.after = at, true
	if _, err := w.run(w.engine()); err == nil {
		t.Fatal("the run was not killed")
	}
	reboot(w)
	return w
}

func rollbackOf(w *world, restore bool) (*Result, error) {
	return w.engine(func(o *Options) { o.RestoreFiles = restore }).Rollback(context.Background())
}

func indexOf(calls []string, call string) int {
	for i, c := range calls {
		if c == call {
			return i
		}
	}
	return -1
}

// TestRollbackAfterAMigrationRunsR1ToR6InOrder is the whole rollback, with the order of its systemd calls.
func TestRollbackAfterAMigrationRunsR1ToR6InOrder(t *testing.T) {
	w := migrated(t, worldOpts{hasCurrent: true, crossFS: true, withDraft: true, dropIn: true})
	res, err := rollbackOf(w, false)
	if err != nil || res.State != StateRolledBack {
		t.Fatalf("%+v %v\n%s", res, err, w.out.String())
	}
	assertRolledBack(t, w, "rollback")
	// R1 stop and disable the new service, R2 unmask and reload, R5 enable and start the old one.
	order := []string{"stop " + NewUnitName, "disable " + NewUnitName, "unmask " + LegacyUnitName, "daemon-reload ", "enable " + LegacyUnitName, "start " + LegacyUnitName}
	last := -1
	for _, c := range order {
		i := indexOf(w.sd.calls, c)
		if i < 0 || i <= last {
			t.Fatalf("%q is out of order or missing in %v", c, w.sd.calls)
		}
		last = i
	}
	// R2: the unit file and the drop-in are back, from the backup.
	if got, _ := w.fs.content("/etc/systemd/system/llama-qwen.service.d/vk.conf"); !strings.Contains(string(got), "GGML_VK_SUBALLOCATION_BLOCK_SIZE") {
		t.Fatal("the drop-in was not restored")
	}
	// R4 without --restore-files: the links keep the old -m paths valid, nothing moved.
	if tgt, err := w.fs.Readlink(oldModel); err != nil || tgt != newModel {
		t.Fatalf("the old model path: %q %v", tgt, err)
	}
	// R6: Tailscale Serve was not touched, and the message says what happened.
	for _, c := range w.execCalls {
		if strings.HasPrefix(c, "tailscale ") && !strings.Contains(c, "serve status") {
			t.Fatalf("tailscale: %s", c)
		}
	}
	for _, want := range []string{"R1 ", "R2 ", "R3 ", "R4 ", "R5 ", "R6 ", "Rolled back:", "Tailscale Serve was never changed"} {
		if !strings.Contains(w.out.String(), want) {
			t.Errorf("the output lacks %q:\n%s", want, w.out.String())
		}
	}
	if !strings.Contains(strings.Join(w.audits, "\n"), "migration.llama_qwen.rolled_back ok") {
		t.Fatalf("audit: %v", w.audits)
	}
	// Rolled back twice: nothing to do the second time, and the disk does not change.
	snap := w.fs.snapshot()
	res, err = rollbackOf(w, false)
	if err != nil || res.State != "already_rolled_back" || w.fs.snapshot() != snap {
		t.Fatalf("second rollback: %+v %v", res, err)
	}
}

// TestRollbackRestoresARegularKeyFileWithTheCurrentKey: R3 copies the key that is current now, so a key
// rotated after the migration is the one the old service uses.
func TestRollbackRestoresARegularKeyFileWithTheCurrentKey(t *testing.T) {
	w := migrated(t, worldOpts{hasCurrent: true})
	rotated := sha256Hex([]byte("a key rotated after the migration"))
	w.fs.put("/etc/localai/api-key", []byte(rotated), 0o600)
	if _, err := rollbackOf(w, false); err != nil {
		t.Fatalf("%v\n%s", err, w.out.String())
	}
	fi, err := w.fs.Lstat("/etc/llama-qwen/api-key")
	if err != nil || !fi.Mode().IsRegular() || fi.Mode().Perm() != 0o600 {
		t.Fatalf("the old key path: %v %v", fi, err)
	}
	if got, _ := w.fs.content("/etc/llama-qwen/api-key"); string(got) != rotated {
		t.Fatal("the old key path does not hold the CURRENT key")
	}
	if got, _ := w.fs.content("/etc/localai/api-key"); string(got) != rotated {
		t.Fatal("the current key was changed")
	}
	if strings.Contains(w.out.String(), rotated) {
		t.Fatal("the key was printed")
	}
	// The directory stays root 0700 and the file root-owned.
	w.fs.mu.Lock()
	n := w.fs.nodes["/etc/llama-qwen/api-key"]
	w.fs.mu.Unlock()
	if n.uid != 0 || n.gid != 0 {
		t.Fatalf("owner %d:%d", n.uid, n.gid)
	}
	// With the current key unusable, the old path is left alone and the rollback says why (SEC-010).
	w = migrated(t, worldOpts{hasCurrent: true})
	w.fs.put("/etc/localai/api-key", []byte("broken"), 0o600)
	_, err = rollbackOf(w, false)
	var me *Error
	if !errors.As(err, &me) || me.Code != doctor.SEC010 || me.Step != "R3" {
		t.Fatalf("err = %v", err)
	}
	if tgt, err := w.fs.Readlink("/etc/llama-qwen/api-key"); err != nil || tgt != "/etc/localai/api-key" {
		t.Fatal("the old key path was changed although it could not be restored")
	}
}

func TestRollbackRestoreFilesMovesTheModelBackOnTheSameFilesystem(t *testing.T) {
	w := migrated(t, worldOpts{hasCurrent: true, withDraft: true})
	if _, err := rollbackOf(w, true); err != nil {
		t.Fatalf("%v\n%s", err, w.out.String())
	}
	for _, name := range []string{"Model-One-Q4.gguf", "draft.gguf"} {
		old := "/home/labuser/models/m1/" + name
		fi, err := w.fs.Lstat(old)
		if err != nil || !fi.Mode().IsRegular() {
			t.Fatalf("%s is not a regular file again: %v %v", old, fi, err)
		}
		if w.fs.has("/srv/localai/models/model-one/" + name) {
			t.Fatalf("%s is still under /srv/localai", name)
		}
		w.fs.mu.Lock()
		n := w.fs.nodes[old]
		w.fs.mu.Unlock()
		if n.uid != 1000 || n.gid != 1000 {
			t.Fatalf("%s is owned by %d:%d, not by the old unit's user", old, n.uid, n.gid)
		}
	}
	if w.fs.has("/srv/localai/models/model-one/MANIFEST.json") {
		t.Fatal("the manifest of files that are not there any more is still present")
	}
	assertRolledBack(t, w, "restore files")
	// And the migration can be run again after that.
	w.out = &lockedBuf{}
	if _, err := w.run(w.engine()); err != nil {
		t.Fatalf("%v\n%s", err, w.out.String())
	}
	assertMigrated(t, w, "migrated again after --restore-files")
}

func TestRollbackRestoreFilesLeavesAFileOnAnotherFilesystem(t *testing.T) {
	w := migrated(t, worldOpts{hasCurrent: true, crossFS: true})
	if _, err := rollbackOf(w, true); err != nil {
		t.Fatalf("%v\n%s", err, w.out.String())
	}
	if tgt, err := w.fs.Readlink(oldModel); err != nil || tgt != newModel {
		t.Fatalf("the old path: %q %v", tgt, err)
	}
	if !w.fs.has("/srv/localai/models/model-one/MANIFEST.json") {
		t.Fatal("the manifest was removed although the files stayed")
	}
	if !strings.Contains(w.out.String(), "is on another filesystem") {
		t.Fatalf("no explanation:\n%s", w.out.String())
	}
	assertRolledBack(t, w, "restore files across filesystems")
}

// TestRollbackBeforeTheOldUnitWasRetired: --rollback on a migration that stopped early (the unit file
// is still there, nothing is masked).
func TestRollbackOfAnInterruptedMigration(t *testing.T) {
	w := newWorld(t, worldOpts{hasCurrent: true})
	w.rend.err = errors.New("generator failed")
	if _, err := w.run(w.engine()); err == nil {
		t.Fatal("expected a failure at M4")
	}
	w.rend.err = nil
	snap := w.fs.snapshot()
	_ = snap
	res, err := rollbackOf(w, false)
	if err != nil || res.State != StateRolledBack {
		t.Fatalf("%+v %v\n%s", res, err, w.out.String())
	}
	assertRolledBack(t, w, "rollback of an interrupted migration")
}

func TestRollbackWithNothingToRollBack(t *testing.T) {
	w := newWorld(t, worldOpts{hasCurrent: true})
	before := w.fs.snapshot()
	res, err := rollbackOf(w, false)
	if err != nil || res.State != "already_rolled_back" {
		t.Fatalf("%+v %v", res, err)
	}
	if w.fs.snapshot() != before || len(w.sd.calls) != 0 || w.agent.stops != 0 {
		t.Fatal("a rollback with nothing to roll back changed something")
	}
	// The unit is gone and there is no record: say so.
	w = newWorld(t, worldOpts{hasCurrent: true})
	_ = w.fs.Remove("/etc/systemd/system/llama-qwen.service")
	_, err = rollbackOf(w, false)
	if CodeOf(err) != doctor.LLM029 {
		t.Fatalf("err = %v", err)
	}
}

func TestRollbackWithoutTheBackupOfTheUnitSaysSo(t *testing.T) {
	w := migrated(t, worldOpts{hasCurrent: true})
	_ = w.fs.Remove("/var/lib/localai/migration/llama-qwen/llama-qwen.service")
	_, err := rollbackOf(w, false)
	var me *Error
	if !errors.As(err, &me) || me.Step != "R2" || !strings.Contains(me.Error(), "restore it by hand") {
		t.Fatalf("err = %v", err)
	}
	// The new service was already stopped by R1 and the migration directory says where it stopped.
	if pr := lastProgress(t, w); pr.Rollback != "R1" {
		t.Fatalf("progress %+v", pr)
	}
}

func TestRollbackStepR5MustSeeTheOldServiceAnswerAndAContinuesAfterAFailure(t *testing.T) {
	// The old service never comes up: R5 times out, the progress stays at R4, the agent is still started.
	w := migrated(t, worldOpts{hasCurrent: true})
	w.sd.fail["start "+LegacyUnitName] = errors.New("Job failed")
	_, err := rollbackOf(w, false)
	var me *Error
	if !errors.As(err, &me) || me.Step != "R5" || me.Code != doctor.LLM029 {
		t.Fatalf("err = %v", err)
	}
	pr := lastProgress(t, w)
	if pr.Rollback != "R4" || !w.agent.active {
		t.Fatalf("progress %+v agent %v", pr, w.agent.active)
	}
	if w.st.entry.State != StateRollingBack || w.st.entry.Step != "R4" {
		t.Fatalf("state %+v", w.st.entry)
	}
	// Running the migration (no flag) continues the rollback; it does not go forward again.
	w.out = &lockedBuf{}
	res, err := w.run(w.engine())
	if err != nil || res.State != StateRolledBack {
		t.Fatalf("%+v %v\n%s", res, err, w.out.String())
	}
	assertRolledBack(t, w, "continued rollback")

	// An old service that answers without a key is not accepted either (R5 demands the 401).
	w = migrated(t, worldOpts{hasCurrent: true})
	w.legacyNoAuth = true
	_, err = rollbackOf(w, false)
	if !errors.As(err, &me) || me.Step != "R5" || !strings.Contains(me.Error(), "not 401") {
		t.Fatalf("err = %v", err)
	}
}

func TestRollbackStopsTheAgentAndStartsItAgain(t *testing.T) {
	w := migrated(t, worldOpts{hasCurrent: true})
	w.agent.stops, w.agent.starts = 0, 0
	if _, err := rollbackOf(w, false); err != nil {
		t.Fatal(err)
	}
	if w.agent.stops != 1 || w.agent.starts != 1 || !w.agent.active {
		t.Fatalf("stops=%d starts=%d", w.agent.stops, w.agent.starts)
	}
}

func TestPending(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t, worldOpts{hasCurrent: true})
	if p, why, _ := w.engine().Pending(ctx); !p || !strings.Contains(why, "llama-qwen.service") {
		t.Fatalf("a legacy node: %v %q", p, why)
	}
	// Interrupted: killed after M3.
	w = interruptedAt(t, worldOpts{hasCurrent: true}, M3)
	if p, why, _ := w.engine().Pending(ctx); !p || !strings.Contains(why, "interrupted") {
		t.Fatalf("interrupted: %v %q", p, why)
	}
	// Done: nothing pending.
	w = migrated(t, worldOpts{hasCurrent: true})
	if p, _, _ := w.engine().Pending(ctx); p {
		t.Fatal("a migrated node has nothing pending")
	}
	// A node with no legacy unit at all.
	w = newWorld(t, worldOpts{hasCurrent: true})
	_ = w.fs.Remove("/etc/systemd/system/llama-qwen.service")
	if p, _, _ := w.engine().Pending(ctx); p {
		t.Fatal("no legacy unit, nothing pending")
	}
	// A rolled back node can be migrated again.
	w = migrated(t, worldOpts{hasCurrent: true})
	if _, err := rollbackOf(w, false); err != nil {
		t.Fatal(err)
	}
	if p, _, _ := w.engine().Pending(ctx); !p {
		t.Fatal("a rolled back node is pending again")
	}
	// A rollback in progress.
	w = migrated(t, worldOpts{hasCurrent: true})
	w.sd.fail["start "+LegacyUnitName] = errors.New("x")
	_, _ = rollbackOf(w, false)
	if p, why, _ := w.engine().Pending(ctx); !p || !strings.Contains(why, "rollback") {
		t.Fatalf("%v %q", p, why)
	}
	// A damaged progress file is reported, not ignored.
	w = newWorld(t, worldOpts{hasCurrent: true})
	w.fs.put("/var/lib/localai/migration/llama-qwen/progress.json", []byte("{"), 0o600)
	if p, why, _ := w.engine().Pending(ctx); !p || !strings.Contains(why, "unreadable") {
		t.Fatalf("%v %q", p, why)
	}
	if _, err := w.engine().Prepare(ctx); CodeOf(err) != doctor.LLM029 {
		t.Fatalf("a damaged progress file: %v", err)
	}
}

func TestResumeNeedsItsPlan(t *testing.T) {
	w := interruptedAt(t, worldOpts{hasCurrent: true}, M3)
	_ = w.fs.Remove("/var/lib/localai/migration/llama-qwen/plan.json")
	_, err := w.engine().Prepare(context.Background())
	if CodeOf(err) != doctor.LLM029 || !strings.Contains(err.Error(), "--rollback") {
		t.Fatalf("err = %v", err)
	}
}
