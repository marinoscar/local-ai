package acestep

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
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
	"github.com/marinoscar/local-ai/agent/internal/untar"
	"github.com/marinoscar/local-ai/agent/internal/untar/untartest"
)

// fakeBuild plays python3 -m venv, pip and the self-test: venv makes bin/python, and failAt makes the call whose
// argv contains it exit 1. The Python tarball itself is a real one, unpacked in-process.
type fakeBuild struct {
	calls  []string
	failAt string
}

func (f *fakeBuild) Run(_ context.Context, c execx.Cmd) (execx.Result, error) {
	key := filepath.Base(c.Path) + " " + strings.Join(c.Args, " ")
	f.calls = append(f.calls, key)
	if f.failAt != "" && strings.Contains(key, f.failAt) {
		return execx.Result{ExitCode: 1, Stderr: []byte("boom")}, nil
	}
	switch {
	case len(c.Args) == 3 && c.Args[1] == "venv":
		_ = os.MkdirAll(filepath.Join(c.Args[2], "bin"), 0o755)
		_ = os.WriteFile(filepath.Join(c.Args[2], "bin", "python"), []byte("#!/bin/sh\n"), 0o755)
	}
	return execx.Result{}, nil
}

func sum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// wheelhouseFixture writes a version folder of the kit's wheelhouse and returns it with its recorded sums.
func wheelhouseFixture(t *testing.T) Wheelhouse {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "engines", "acestep", "1.5.0-ab12cd34")
	files := map[string]string{
		"python/cpython-3.12.16+20261110-x86_64-unknown-linux-gnu-install_only.tar.gz": string(untartest.PythonTarball()),
		"wheels/torch-2.10.0+cu128-cp312-cp312-manylinux_2_28_x86_64.whl":              "torch",
		"wheels/localai_music_shim-0.1.0-py3-none-any.whl":                             "shim",
		WheelhouseLock: "torch==2.10.0+cu128 --hash=sha256:x\n",
	}
	w := Wheelhouse{Dir: dir, Sums: map[string]string{}}
	for rel, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		w.Sums[rel] = sum([]byte(body))
	}
	return w
}

func newInstaller(t *testing.T, r execx.Runner) Installer {
	return Installer{Root: filepath.Join(t.TempDir(), "acestep"), Runner: r,
		LookPath: func(n string) (string, error) { return "/usr/bin/" + n, nil },
		Now:      func() time.Time { return time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC) }}
}

func TestInstallBuildsInPlaceAndMovesCurrent(t *testing.T) {
	f := &fakeBuild{}
	in := newInstaller(t, f)
	wh := wheelhouseFixture(t)
	out, err := in.Install(context.Background(), wh, "1.5.0-ab12cd34", "cuda")
	if err != nil || out.Unchanged || out.Version != "1.5.0-ab12cd34" {
		t.Fatalf("install = %+v, %v", out, err)
	}
	cur, err := (Runtime{Root: in.Root, Backend: func() string { return "cuda" }}).Current()
	if err != nil || cur.Version != "1.5.0-ab12cd34" || cur.Backend != "cuda" || cur.LockSHA256 != wh.Sums[WheelhouseLock] ||
		cur.Python != "cpython-3.12.16+20261110-x86_64-unknown-linux-gnu-install_only.tar.gz" {
		t.Fatalf("current = %+v, %v", cur, err)
	}
	final := filepath.Join(in.Root, "1.5.0-ab12cd34")
	joined := strings.Join(f.calls, "\n")
	for _, want := range []string{
		"python3 -m venv " + filepath.Join(final, "venv"),
		"python -m pip install --no-index --find-links " + filepath.Join(wh.Dir, "wheels") + " --require-hashes -r " + filepath.Join(wh.Dir, WheelhouseLock),
		"python -m localai_music_shim --self-test",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("no %q in:\n%s", want, joined)
		}
	}
	if _, err := os.Stat(filepath.Join(final, incompleteMarker)); !os.IsNotExist(err) {
		t.Error("the finished runtime still carries the incomplete marker")
	}
	// Idempotent: the same version and backend runs nothing.
	n := len(f.calls)
	out, err = in.Install(context.Background(), wh, "1.5.0-ab12cd34", "cuda")
	if err != nil || !out.Unchanged || len(f.calls) != n {
		t.Fatalf("second install = %+v, %v (%d calls)", out, err, len(f.calls)-n)
	}
}

func TestInstallRefusesADamagedWheelhouse(t *testing.T) {
	for _, tc := range []struct {
		name   string
		break_ func(t *testing.T, wh Wheelhouse)
	}{
		{"a flipped wheel", func(t *testing.T, wh Wheelhouse) {
			_ = os.WriteFile(filepath.Join(wh.Dir, "wheels", "localai_music_shim-0.1.0-py3-none-any.whl"), []byte("shiM"), 0o644)
		}},
		{"a changed lock", func(t *testing.T, wh Wheelhouse) {
			_ = os.WriteFile(filepath.Join(wh.Dir, WheelhouseLock), []byte("torch==9\n"), 0o644)
		}},
		{"a recorded wheel missing", func(t *testing.T, wh Wheelhouse) {
			_ = os.Remove(filepath.Join(wh.Dir, "wheels", "localai_music_shim-0.1.0-py3-none-any.whl"))
		}},
		{"an unrecorded wheel", func(t *testing.T, wh Wheelhouse) {
			_ = os.WriteFile(filepath.Join(wh.Dir, "wheels", "evil-1.0-py3-none-any.whl"), []byte("x"), 0o644)
		}},
		{"no Python", func(t *testing.T, wh Wheelhouse) {
			p := filepath.Join(wh.Dir, "python", "cpython-3.12.16+20261110-x86_64-unknown-linux-gnu-install_only.tar.gz")
			_ = os.Remove(p)
			delete(wh.Sums, "python/cpython-3.12.16+20261110-x86_64-unknown-linux-gnu-install_only.tar.gz")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeBuild{}
			in := newInstaller(t, f)
			wh := wheelhouseFixture(t)
			tc.break_(t, wh)
			if _, err := in.Install(context.Background(), wh, "1.5.0-ab12cd34", "cuda"); !errors.Is(err, ErrWheelhouseDamaged) {
				t.Fatalf("err = %v", err)
			}
			if len(f.calls) != 0 {
				t.Errorf("programs ran: %v", f.calls)
			}
			if _, ok := in.Current(); ok {
				t.Error("current was set")
			}
			if _, err := os.Stat(filepath.Join(in.Root, "1.5.0-ab12cd34")); !os.IsNotExist(err) {
				t.Error("a version folder was made from a damaged wheelhouse")
			}
		})
	}
}

func TestInstallFailureKeepsThePreviousRuntime(t *testing.T) {
	for _, step := range []string{"-m venv", "pip install", "--self-test"} {
		t.Run(step, func(t *testing.T) {
			f := &fakeBuild{}
			in := newInstaller(t, f)
			wh := wheelhouseFixture(t)
			if _, err := in.Install(context.Background(), wh, "1.4.0-00000000", "cuda"); err != nil {
				t.Fatal(err)
			}
			f.failAt = step
			if _, err := in.Install(context.Background(), wh, "1.5.0-ab12cd34", "cuda"); err == nil {
				t.Fatal("the failure was not reported")
			}
			if cur, ok := in.Current(); !ok || cur.Version != "1.4.0-00000000" {
				t.Errorf("current = %+v, %v", cur, ok)
			}
			if _, err := os.Stat(filepath.Join(in.Root, "1.5.0-ab12cd34")); !os.IsNotExist(err) {
				t.Error("the failed build was left behind")
			}
		})
	}
}

func TestInstallAfterAnInterruptedRun(t *testing.T) {
	f := &fakeBuild{}
	in := newInstaller(t, f)
	wh := wheelhouseFixture(t)
	// What a killed run left: the version folder with its marker and a half-made venv, never current.
	final := filepath.Join(in.Root, "1.5.0-ab12cd34")
	if err := os.MkdirAll(filepath.Join(final, "venv", "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(final, incompleteMarker), nil, 0o644)
	_ = os.WriteFile(filepath.Join(final, "venv", "bin", "stale"), []byte("x"), 0o644)
	if _, err := in.Install(context.Background(), wh, "1.5.0-ab12cd34", "cuda"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(final, "venv", "bin", "stale")); !os.IsNotExist(err) {
		t.Error("the leftover of the interrupted run was kept")
	}
}

// A current that points at an unfinished build is never in use, and rebuilding the version current points at
// puts the old folder back when the rebuild fails.
func TestNothingHalfInstalledIsCurrent(t *testing.T) {
	f := &fakeBuild{}
	in := newInstaller(t, f)
	wh := wheelhouseFixture(t)
	if _, err := in.Install(context.Background(), wh, "1.5.0-ab12cd34", "cuda"); err != nil {
		t.Fatal(err)
	}
	final := filepath.Join(in.Root, "1.5.0-ab12cd34")
	_ = os.WriteFile(filepath.Join(final, incompleteMarker), nil, 0o644)
	if _, err := (Runtime{Root: in.Root}).Current(); err == nil {
		t.Fatal("an unfinished runtime counts as installed")
	}
	if _, ok := in.Current(); ok {
		t.Fatal("the installer took an unfinished runtime as current")
	}
	// The rebuild of that version fails: the old folder (marker and all) is back, nothing else changed.
	f.failAt = "pip install"
	if _, err := in.Install(context.Background(), wh, "1.5.0-ab12cd34", "cuda"); err == nil {
		t.Fatal("the failure was not reported")
	}
	if _, err := os.Stat(filepath.Join(final, BuildFile)); err != nil {
		t.Error("the previous folder was not put back")
	}
	// And a rebuild that works replaces it.
	f.failAt = ""
	if _, err := in.Install(context.Background(), wh, "1.5.0-ab12cd34", "cuda"); err != nil {
		t.Fatal(err)
	}
	if _, err := (Runtime{Root: in.Root, Backend: func() string { return "cuda" }}).Current(); err != nil {
		t.Fatalf("after the rebuild: %v", err)
	}
	if _, err := os.Stat(filepath.Join(in.Root, ".1.5.0-ab12cd34.previous")); !os.IsNotExist(err) {
		t.Error("the set-aside folder was left behind")
	}
}

func TestVersionsAndSub(t *testing.T) {
	sums := map[string]string{
		"1.5.0-aa/requirements.lock": "a", "1.5.0-aa/wheels/x.whl": "b",
		"1.10.0-bb/requirements.lock": "c", "1.10.0-bb/python/p.tar.gz": "d",
		"1.9.0-cc/wheels/only.whl": "e",
	}
	if got := strings.Join(Versions(sums), " "); got != "1.10.0-bb 1.5.0-aa" {
		t.Errorf("Versions = %s", got)
	}
	w := Sub("/kit/engines/acestep", sums, "1.5.0-aa")
	if w.Dir != filepath.Join("/kit/engines/acestep", "1.5.0-aa") || len(w.Sums) != 2 || w.Sums["wheels/x.whl"] != "b" {
		t.Errorf("Sub = %+v", w)
	}
}

// A Python tarball that matches its record but is unsafe (a link out of the folder) installs nothing and keeps
// the runtime in use.
func TestInstallAnUnsafePythonTarballKeepsThePreviousRuntime(t *testing.T) {
	f := &fakeBuild{}
	in := newInstaller(t, f)
	wh := wheelhouseFixture(t)
	if _, err := in.Install(context.Background(), wh, "1.4.0-00000000", "cuda"); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	_ = tw.WriteHeader(&tar.Header{Name: "python/bin/python3", Typeflag: tar.TypeSymlink, Linkname: "../../../../etc/passwd"})
	_ = tw.Close()
	_ = gz.Close()
	rel := "python/cpython-3.12.16+20261110-x86_64-unknown-linux-gnu-install_only.tar.gz"
	if err := os.WriteFile(filepath.Join(wh.Dir, filepath.FromSlash(rel)), buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	wh.Sums[rel] = sum(buf.Bytes())
	if _, err := in.Install(context.Background(), wh, "1.5.0-ab12cd34", "cuda"); !errors.Is(err, untar.ErrUnsafe) {
		t.Fatalf("err = %v", err)
	}
	if cur, ok := in.Current(); !ok || cur.Version != "1.4.0-00000000" {
		t.Errorf("current = %+v, %v", cur, ok)
	}
	if _, err := os.Stat(filepath.Join(in.Root, "1.5.0-ab12cd34")); !os.IsNotExist(err) {
		t.Error("the failed build was left behind")
	}
}
