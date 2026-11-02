package config_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/config/fake"
)

func readString(t testing.TB, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestWriteFileAtomicCreatesAndReplaces(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state")
	for i, content := range []string{"one", "two, longer"} {
		if err := config.WriteFileAtomic(path, []byte(content), 0o640, -1, -1); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
		if got := readString(t, path); got != content {
			t.Errorf("write %d: content %q, want %q", i, got, content)
		}
	}
	if names := listDir(t, dir); len(names) != 1 {
		t.Errorf("only the target may remain, got %v", names)
	}
}

func TestWriteFileAtomicModes(t *testing.T) {
	// 0600 for secrets, 0644 for world-readable files, 0640 for node.yaml (agent.md 3.3).
	for _, mode := range []os.FileMode{0o600, 0o640, 0o644} {
		path := filepath.Join(t.TempDir(), "f")
		if err := config.WriteFileAtomic(path, []byte("x"), mode, -1, -1); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != mode {
			t.Errorf("mode = %04o, want %04o (the umask must not leak in)", info.Mode().Perm(), mode)
		}
	}
}

func TestWriteFileAtomicSetsTheOwnerWhenAsked(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f")
	// Chown to the current ids always succeeds, so this runs unprivileged too.
	if err := config.WriteFileAtomic(path, []byte("x"), 0o600, os.Getuid(), os.Getgid()); err != nil {
		t.Fatal(err)
	}
}

func TestWriteFileAtomicChownFailureLeavesNothing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f")
	ffs := fake.New(config.OSFS{}).FailOn("chown", 1)
	err := config.WriteFileAtomicFS(ffs, path, []byte("x"), 0o600, 0, 0)
	if !errors.Is(err, fake.ErrInjected) {
		t.Fatalf("err = %v, want the injected failure", err)
	}
	if names := listDir(t, dir); len(names) != 0 {
		t.Errorf("a failed write must leave nothing behind, got %v", names)
	}
}

// Every step before the rename fails in turn: the old file is intact and no temp file is visible.
func TestFailureAtAnyStepBeforeRenameKeepsTheOldFileAndLeavesNoTemp(t *testing.T) {
	for _, op := range []string{"open", "write", "sync", "chown", "chmod", "close", "rename"} {
		t.Run(op, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "node.yaml")
			if err := os.WriteFile(path, []byte("old"), 0o640); err != nil {
				t.Fatal(err)
			}
			ffs := fake.New(config.OSFS{}).FailOn(op, 1)
			err := config.WriteFileAtomicFS(ffs, path, []byte("new"), 0o640, os.Getuid(), os.Getgid())
			if !errors.Is(err, fake.ErrInjected) {
				t.Fatalf("err = %v, want the injected failure", err)
			}
			if got := readString(t, path); got != "old" {
				t.Errorf("the old file must be intact, got %q", got)
			}
			if names := listDir(t, dir); len(names) != 1 || names[0] != "node.yaml" {
				t.Errorf("no temp file may stay visible, got %v", names)
			}
		})
	}
}

// A crash is a failure after which even the cleanup cannot run (kill -9): the temp file is on disk,
// the old file is intact, and the next start sweeps the debris.
func TestCrashBetweenWriteAndRenameLeavesTheOldFileAndTheNextStartSweepsTheTemp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "node.yaml")
	if err := os.WriteFile(path, []byte("old"), 0o640); err != nil {
		t.Fatal(err)
	}
	ffs := fake.New(config.OSFS{}).FailOn("rename", 1)
	ffs.Crash = true
	if err := config.WriteFileAtomicFS(ffs, path, []byte("new"), 0o640, -1, -1); err == nil {
		t.Fatal("the crash must surface as an error")
	}
	if got := readString(t, path); got != "old" {
		t.Fatalf("old file must be intact, got %q", got)
	}
	names := listDir(t, dir)
	if len(names) != 2 {
		t.Fatalf("a dead process leaves its temp file; got %v", names)
	}
	for _, n := range names {
		if n != "node.yaml" && !strings.HasPrefix(n, "node.yaml.tmp-") {
			t.Fatalf("unexpected file %q", n)
		}
	}
	removed := config.SweepTemps(config.OSFS{}, path)
	if len(removed) != 1 {
		t.Fatalf("sweep removed %v, want the one temp file", removed)
	}
	if names := listDir(t, dir); len(names) != 1 || names[0] != "node.yaml" {
		t.Errorf("after the sweep only node.yaml remains, got %v", names)
	}
}

func TestSweepTempsOnlyTouchesTheTargetsOwnTemps(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"node.yaml", "node.yaml.bak", "node.yaml.tmp-aa11", "node.yaml.bak.tmp-bb22", "state.json.tmp-cc33", "node.yaml.tmp"} {
		if err := os.WriteFile(filepath.Join(dir, n), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	config.SweepTemps(config.OSFS{}, filepath.Join(dir, "node.yaml"))
	got := strings.Join(listDir(t, dir), " ")
	want := "node.yaml node.yaml.bak node.yaml.bak.tmp-bb22 node.yaml.tmp state.json.tmp-cc33"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestDirectorySyncFailureIsReportedButTheFileIsReplaced(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f")
	ffs := fake.New(config.OSFS{}).FailOn("syncdir", 1)
	err := config.WriteFileAtomicFS(ffs, path, []byte("new"), 0o600, -1, -1)
	if !errors.Is(err, config.ErrNotDurable) {
		t.Fatalf("err = %v, want ErrNotDurable", err)
	}
	if got := readString(t, path); got != "new" {
		t.Errorf("the rename happened, content = %q", got)
	}
	if names := listDir(t, dir); len(names) != 1 {
		t.Errorf("got %v", names)
	}
}

func TestWriteFileAtomicTempIsInTheSameDirectory(t *testing.T) {
	// A rename across filesystems is not atomic, so the temp must sit next to the target.
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "f")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	ffs := fake.New(config.OSFS{}).FailOn("rename", 1)
	ffs.Crash = true
	_ = config.WriteFileAtomicFS(ffs, path, []byte("x"), 0o600, -1, -1)
	if names := listDir(t, dir); len(names) != 1 || names[0] != "sub" {
		t.Errorf("nothing may be created outside the target's directory, got %v", names)
	}
	if names := listDir(t, filepath.Join(dir, "sub")); len(names) != 1 || !strings.HasPrefix(names[0], "f.tmp-") {
		t.Errorf("temp must be next to the target, got %v", names)
	}
}
