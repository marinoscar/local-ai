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

// fakeBuild plays tar, python3 -m venv, pip and the self-test: tar unpacks a python/ folder, venv makes
// bin/python, and failAt makes one program exit 1.
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
		_ = os.MkdirAll(filepath.Join(dst, "python", "bin"), 0o755)
		_ = os.WriteFile(filepath.Join(dst, "python", "bin", "python3"), []byte("#!/bin/sh\n"), 0o755)
	case name == "python3" && len(c.Args) == 3 && c.Args[1] == "venv":
		_ = os.MkdirAll(filepath.Join(c.Args[2], "bin"), 0o755)
		_ = os.WriteFile(filepath.Join(c.Args[2], "bin", "python"), []byte("#!/bin/sh\n"), 0o755)
	}
	return execx.Result{}, nil
}

// kitVersion writes a runtime version into a kit folder: the lock, two wheels and, withPython, the
// interpreter's tarball. It returns the version directory and every file with its sha256.
func kitVersion(t *testing.T, kitDir, version string, withPython bool) (string, []KitFile) {
	t.Helper()
	dir := filepath.Join(KitDir(kitDir, "cuda"), version)
	content := map[string]string{
		KitLock:                         "torch==2.10.0+cu128 --hash=sha256:x\n",
		"wheels/torch-2.10.0-cp312.whl": "torch",
		"wheels/shim-0.1.0.whl":         "shim",
	}
	if withPython {
		content["python/cpython-3.12.16-install_only.tar.gz"] = "python"
	}
	var files []KitFile
	for rel, body := range content {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		h := sha256.Sum256([]byte(body))
		files = append(files, KitFile{Path: p, SHA256: hex.EncodeToString(h[:])})
	}
	return dir, files
}

func newInstaller(t *testing.T, r execx.Runner) Installer {
	return Installer{Root: filepath.Join(t.TempDir(), "acestep"), Runner: r,
		LookPath: func(n string) (string, error) { return "/usr/bin/" + n, nil },
		Now:      func() time.Time { return time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC) }}
}

func TestInstallBuildsAndMovesCurrent(t *testing.T) {
	for _, withPython := range []bool{false, true} {
		t.Run(map[bool]string{false: "system python3", true: "bundled python"}[withPython], func(t *testing.T) {
			f := &fakeBuild{}
			in := newInstaller(t, f)
			dir, files := kitVersion(t, t.TempDir(), "1.5.0", withPython)
			out, err := in.Install(context.Background(), dir, files, "1.5.0", "cuda")
			if err != nil || out.Unchanged || out.Version != "1.5.0" {
				t.Fatalf("install = %+v, %v", out, err)
			}
			cur, err := (Runtime{Root: in.Root, Backend: func() string { return "cuda" }}).Current()
			if err != nil || cur.Version != "1.5.0" || cur.Backend != "cuda" || cur.LockSHA256 == "" {
				t.Fatalf("current = %+v, %v", cur, err)
			}
			joined := strings.Join(f.calls, "\n")
			wants := []string{"-m pip install --no-index --find-links " + filepath.Join(dir, KitWheels), "--require-hashes -r " + filepath.Join(dir, KitLock), "-m localai_music_shim --self-test"}
			if withPython {
				wants = append(wants, "tar -xzf "+filepath.Join(dir, "python"))
				if strings.Contains(joined, "-m venv") || !strings.HasPrefix(cur.Python, "bundled ") {
					t.Errorf("the system python3 was used with a bundled one (python %q):\n%s", cur.Python, joined)
				}
				if _, err := os.Stat(filepath.Join(in.Root, "1.5.0", "venv", "bin", "python3")); err != nil {
					t.Errorf("the bundled python is not the runtime: %v", err)
				}
			} else {
				wants = append(wants, "python3 -m venv")
				if strings.Contains(joined, "tar ") || cur.Python != "python3" {
					t.Errorf("unexpected tar or python %q:\n%s", cur.Python, joined)
				}
			}
			for _, want := range wants {
				if !strings.Contains(joined, want) {
					t.Errorf("no %q in:\n%s", want, joined)
				}
			}
			// Idempotent: the same version and backend runs nothing.
			n := len(f.calls)
			out, err = in.Install(context.Background(), dir, files, "1.5.0", "cuda")
			if err != nil || !out.Unchanged || len(f.calls) != n {
				t.Fatalf("second install = %+v, %v (%d calls)", out, err, len(f.calls)-n)
			}
		})
	}
}

func TestInstallDamagedKitInstallsNothing(t *testing.T) {
	cases := map[string]func(dir string, files []KitFile) []KitFile{
		"checksum mismatch": func(_ string, files []KitFile) []KitFile {
			files[0].SHA256 = strings.Repeat("0", 64)
			return files
		},
		"a file is missing": func(_ string, files []KitFile) []KitFile {
			_ = os.Remove(files[0].Path)
			return files
		},
		"no lock": func(dir string, files []KitFile) []KitFile {
			var out []KitFile
			for _, f := range files {
				if f.Path != filepath.Join(dir, KitLock) {
					out = append(out, f)
				}
			}
			return out
		},
		"two pythons": func(dir string, files []KitFile) []KitFile {
			p := filepath.Join(dir, "python", "cpython-3.12.1-install_only.tar.gz")
			_ = os.WriteFile(p, []byte("python"), 0o644)
			return append(files, KitFile{Path: p, SHA256: files[len(files)-1].SHA256})
		},
	}
	for name, damage := range cases {
		t.Run(name, func(t *testing.T) {
			f := &fakeBuild{}
			in := newInstaller(t, f)
			dir, files := kitVersion(t, t.TempDir(), "1.5.0", true)
			// The python tarball last, so "two pythons" can copy its sum.
			for i, kf := range files {
				if strings.HasSuffix(kf.Path, ".tar.gz") {
					files[i], files[len(files)-1] = files[len(files)-1], files[i]
				}
			}
			if _, err := in.Install(context.Background(), dir, damage(dir, files), "1.5.0", "cuda"); !errors.Is(err, ErrKitDamaged) {
				t.Fatalf("err = %v", err)
			}
			if len(f.calls) != 0 {
				t.Errorf("programs ran: %v", f.calls)
			}
			if _, ok := in.Current(); ok {
				t.Error("current was set")
			}
		})
	}
}

func TestInstallFailureKeepsThePreviousRuntime(t *testing.T) {
	for _, step := range []string{"tar", "venv", "pip install", "--self-test"} {
		t.Run(step, func(t *testing.T) {
			f := &fakeBuild{}
			in := newInstaller(t, f)
			kitDir := t.TempDir()
			withPython := step == "tar"
			dir4, files4 := kitVersion(t, kitDir, "1.4.0", withPython)
			if _, err := in.Install(context.Background(), dir4, files4, "1.4.0", "cuda"); err != nil {
				t.Fatal(err)
			}
			f.failAt = step
			dir, files := kitVersion(t, kitDir, "1.5.0", withPython)
			if _, err := in.Install(context.Background(), dir, files, "1.5.0", "cuda"); err == nil {
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
	dir, files := kitVersion(t, t.TempDir(), "1.5.0", false)
	// What a killed run left: a partial directory with a half-made venv.
	partial := filepath.Join(in.Root, ".1.5.0.partial", "venv", "bin")
	if err := os.MkdirAll(partial, 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(partial, "stale"), []byte("x"), 0o644)
	if _, err := in.Install(context.Background(), dir, files, "1.5.0", "cuda"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(in.Root, "1.5.0", "venv", "bin", "stale")); !os.IsNotExist(err) {
		t.Error("the leftover of the interrupted run was kept")
	}
}

func TestFindVersion(t *testing.T) {
	kit := t.TempDir()
	for _, v := range []string{"1.4.0", "1.10.0", "1.5.0"} {
		kitVersion(t, kit, v, false)
	}
	// A version folder with no lock (a sync that has not finished) is not a version.
	_ = os.MkdirAll(filepath.Join(KitDir(kit, "cuda"), "1.11.0", "wheels"), 0o755)
	if d, v := FindVersion(kit, "cuda"); v != "1.10.0" || d != filepath.Join(KitDir(kit, "cuda"), "1.10.0") {
		t.Errorf("FindVersion = %s %s", d, v)
	}
	if _, v := FindVersion(kit, "vulkan"); v != "" {
		t.Errorf("vulkan = %s", v)
	}
}
