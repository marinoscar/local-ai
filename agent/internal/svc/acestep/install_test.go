package acestep

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/execx"
)

// fakeBuild plays tar, python3 -m venv, pip and the self-test: tar unpacks a wheels folder and a lock
// file, venv makes bin/python, and failAt makes one program exit 1.
type fakeBuild struct {
	calls  []string
	failAt string
}

func (f *fakeBuild) Run(_ context.Context, c execx.Cmd) (execx.Result, error) {
	name := filepath.Base(c.Path)
	key := name + " " + strings.Join(c.Args, " ")
	f.calls = append(f.calls, key)
	if f.failAt != "" && strings.Contains(key, f.failAt) {
		return execx.Result{ExitCode: 1, Stderr: []byte("boom")}, nil
	}
	switch {
	case name == "tar":
		dst := c.Args[len(c.Args)-2]
		_ = os.MkdirAll(filepath.Join(dst, BundleWheels), 0o755)
		_ = os.WriteFile(filepath.Join(dst, BundleLock), []byte("torch==2.5.0 --hash=sha256:x\n"), 0o644)
	case name == "python3" && len(c.Args) == 3 && c.Args[1] == "venv":
		_ = os.MkdirAll(filepath.Join(c.Args[2], "bin"), 0o755)
		_ = os.WriteFile(filepath.Join(c.Args[2], "bin", "python"), []byte("#!/bin/sh\n"), 0o755)
	}
	return execx.Result{}, nil
}

func bundle(t *testing.T) (path, sum string) {
	t.Helper()
	path = filepath.Join(t.TempDir(), BundleName("1.5.0", "cuda"))
	data := []byte("not really zstd")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256(data)
	return path, hex.EncodeToString(h[:])
}

func newInstaller(t *testing.T, r execx.Runner) Installer {
	return Installer{Root: filepath.Join(t.TempDir(), "acestep"), Runner: r,
		LookPath: func(n string) (string, error) { return "/usr/bin/" + n, nil },
		Now:      func() time.Time { return time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC) }}
}

func TestInstallBuildsAndMovesCurrent(t *testing.T) {
	f := &fakeBuild{}
	in := newInstaller(t, f)
	b, sum := bundle(t)
	out, err := in.Install(context.Background(), b, sum, "1.5.0", "cuda")
	if err != nil || out.Unchanged || out.Version != "1.5.0" {
		t.Fatalf("install = %+v, %v", out, err)
	}
	cur, err := (Runtime{Root: in.Root, Backend: func() string { return "cuda" }}).Current()
	if err != nil || cur.Version != "1.5.0" || cur.Backend != "cuda" || cur.LockSHA256 == "" {
		t.Fatalf("current = %+v, %v", cur, err)
	}
	joined := strings.Join(f.calls, "\n")
	for _, want := range []string{"tar --zstd -xf", "python3 -m venv", "-m pip install --no-index --find-links", "--require-hashes", "-m localai_music_shim --self-test"} {
		if !strings.Contains(joined, want) {
			t.Errorf("no %q in:\n%s", want, joined)
		}
	}
	// Idempotent: the same version and backend runs nothing.
	n := len(f.calls)
	out, err = in.Install(context.Background(), b, sum, "1.5.0", "cuda")
	if err != nil || !out.Unchanged || len(f.calls) != n {
		t.Fatalf("second install = %+v, %v (%d calls)", out, err, len(f.calls)-n)
	}
}

func TestInstallChecksumMismatchInstallsNothing(t *testing.T) {
	f := &fakeBuild{}
	in := newInstaller(t, f)
	b, _ := bundle(t)
	if _, err := in.Install(context.Background(), b, strings.Repeat("0", 64), "1.5.0", "cuda"); !errors.Is(err, ErrBundleDamaged) {
		t.Fatalf("err = %v", err)
	}
	if len(f.calls) != 0 {
		t.Errorf("programs ran: %v", f.calls)
	}
	if _, ok := in.Current(); ok {
		t.Error("current was set")
	}
}

func TestInstallFailureKeepsThePreviousRuntime(t *testing.T) {
	for _, step := range []string{"tar", "venv", "pip install", "--self-test"} {
		t.Run(step, func(t *testing.T) {
			f := &fakeBuild{}
			in := newInstaller(t, f)
			b, sum := bundle(t)
			if _, err := in.Install(context.Background(), b, sum, "1.4.0", "cuda"); err != nil {
				t.Fatal(err)
			}
			f.failAt = step
			if _, err := in.Install(context.Background(), b, sum, "1.5.0", "cuda"); err == nil {
				t.Fatal("the failure was not reported")
			}
			if cur, ok := in.Current(); !ok || cur.Version != "1.4.0" {
				t.Errorf("current = %+v, %v", cur, ok)
			}
			if _, err := os.Stat(filepath.Join(in.Root, ".1.5.0.partial")); !os.IsNotExist(err) {
				t.Error("the partial build was left behind")
			}
		})
	}
}

func TestInstallAfterAnInterruptedRun(t *testing.T) {
	f := &fakeBuild{}
	in := newInstaller(t, f)
	b, sum := bundle(t)
	// What a killed run left: a partial directory with a half-made venv.
	partial := filepath.Join(in.Root, ".1.5.0.partial", "venv", "bin")
	if err := os.MkdirAll(partial, 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(partial, "stale"), []byte("x"), 0o644)
	if _, err := in.Install(context.Background(), b, sum, "1.5.0", "cuda"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(in.Root, "1.5.0", "venv", "bin", "stale")); !os.IsNotExist(err) {
		t.Error("the leftover of the interrupted run was kept")
	}
}

func TestFindBundle(t *testing.T) {
	kit := t.TempDir()
	for _, v := range []string{"1.4.0", "1.10.0", "1.5.0"} {
		d := filepath.Join(kit, "engines", "acestep", v)
		_ = os.MkdirAll(d, 0o755)
		_ = os.WriteFile(filepath.Join(d, BundleName(v, "cuda")), []byte("x"), 0o644)
	}
	if p, v := FindBundle(kit, "cuda"); v != "1.10.0" || !strings.HasSuffix(p, BundleName("1.10.0", "cuda")) {
		t.Errorf("FindBundle = %s %s", p, v)
	}
	if _, v := FindBundle(kit, "vulkan"); v != "" {
		t.Errorf("vulkan = %s", v)
	}
}
