package host

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/ensure"
	"github.com/marinoscar/local-ai/agent/internal/execx"
	"github.com/marinoscar/local-ai/agent/internal/kit"
	"github.com/marinoscar/local-ai/agent/internal/svc/acestep"
	"github.com/marinoscar/local-ai/agent/internal/untar/untartest"
)

// The engine.acestep step over a kit fixture: the wheelhouse kit-engine-acestep-cuda at kit/engines/acestep/ (a
// tree item, as ark sync writes it), the quick kit check, the install from it with the programs faked.

const whVersion = "1.5.0-ab12cd34"

func wheelhouseFiles(version string) map[string][]byte {
	return map[string][]byte{
		version + "/python/cpython-3.12.16+20261110-x86_64-unknown-linux-gnu-install_only.tar.gz": untartest.PythonTarball(),
		version + "/wheels/torch-2.10.0+cu128-cp312-cp312-manylinux_2_28_x86_64.whl":              []byte("torch"),
		version + "/wheels/localai_music_shim-0.1.0-py3-none-any.whl":                             []byte("shim"),
		version + "/requirements.lock":                                                            []byte("torch==2.10.0+cu128 --hash=sha256:x\n"),
	}
}

// engineRunner fakes venv (bin/python) and pip; the Python tarball is a real one, unpacked in-process.
func engineRunner(fail string) func(c execx.Cmd) (execx.Result, error) {
	return func(c execx.Cmd) (execx.Result, error) {
		argv := filepath.Base(c.Path) + " " + strings.Join(c.Args, " ")
		if fail != "" && strings.Contains(argv, fail) {
			return execx.Result{ExitCode: 1, Stderr: []byte("boom")}, nil
		}
		switch {
		case len(c.Args) == 3 && c.Args[1] == "venv":
			_ = os.MkdirAll(filepath.Join(c.Args[2], "bin"), 0o755)
			_ = os.WriteFile(filepath.Join(c.Args[2], "bin", "python"), []byte("#!/bin/sh\n"), 0o755)
		}
		return execx.Result{}, nil
	}
}

func TestEngineInstallsTheMusicRuntimeFromTheKitWheelhouse(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	if _, _, err := f.host.Engine(ctx, "acestep", "cuda"); !errors.Is(err, ensure.ErrKitMissing) {
		t.Fatalf("no wheelhouse: err = %v", err)
	}
	f.kit.Tree("engines/acestep", "kit-engine-acestep-cuda", wheelhouseFiles(whVersion))
	// A new host: the quick kit check is cached for a while.
	f.build(func() []kit.StatusRoot { return []kit.StatusRoot{{ID: "internal", Path: f.kit.Root, Internal: true}} })
	src, version, err := f.host.Engine(ctx, "acestep", "cuda")
	if err != nil || version != whVersion || src.KitDir != f.kit.Dir || src.File != filepath.Join(f.kit.Dir, "engines", "acestep", whVersion) ||
		src.SHA256 != sha([]byte("torch==2.10.0+cu128 --hash=sha256:x\n")) {
		t.Fatalf("Engine = %+v %q %v", src, version, err)
	}
	// Another backend has no wheelhouse in this kit.
	if _, _, err := f.host.Engine(ctx, "acestep", "vulkan"); !errors.Is(err, ensure.ErrKitMissing) {
		t.Fatalf("vulkan: err = %v", err)
	}
	if v := f.host.EngineVersion("acestep", "cuda"); v != "" {
		t.Fatalf("installed before the install: %q", v)
	}
	f.runner.fn = engineRunner("")
	out, err := f.host.InstallEngine(ctx, "acestep", src, version, "cuda")
	if err != nil || out.Version != whVersion || out.Unchanged {
		t.Fatalf("install = %+v, %v", out, err)
	}
	if v := f.host.EngineVersion("acestep", "cuda"); v != whVersion {
		t.Fatalf("EngineVersion = %q", v)
	}
	b, err := acestep.ReadBuild(filepath.Join(f.opt, "acestep", "current"))
	if err != nil || b.Backend != "cuda" || b.LockSHA256 != src.SHA256 {
		t.Fatalf("BUILD.json = %+v, %v", b, err)
	}
	// Idempotent.
	if out, err := f.host.InstallEngine(ctx, "acestep", src, version, "cuda"); err != nil || !out.Unchanged {
		t.Fatalf("second install = %+v, %v", out, err)
	}
}

func TestEngineRefusesADamagedWheelhouse(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.kit.Tree("engines/acestep", "kit-engine-acestep-cuda", wheelhouseFiles(whVersion))
	src, version, err := f.host.Engine(ctx, "acestep", "cuda")
	if err != nil {
		t.Fatal(err)
	}
	// A flipped byte of the same size passes the quick kit check; the install re-hashes and refuses.
	f.kit.Corrupt("engines/acestep/" + whVersion + "/wheels/localai_music_shim-0.1.0-py3-none-any.whl")
	f.runner.fn = engineRunner("")
	if _, err := f.host.InstallEngine(ctx, "acestep", src, version, "cuda"); !errors.Is(err, ensure.ErrKitDamaged) {
		t.Fatalf("err = %v, want the kit's copy damaged", err)
	}
	if v := f.host.EngineVersion("acestep", "cuda"); v != "" {
		t.Fatalf("a damaged wheelhouse was installed: %q", v)
	}
	// A record that does not match its manifest is damaged, not missing.
	if err := os.WriteFile(filepath.Join(f.kit.Dir, "engines", "acestep", "SHA256SUMS"), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := wheelhouse(f.kit.Dir, "acestep", "cuda"); !errors.Is(err, ensure.ErrKitDamaged) {
		t.Fatalf("damaged sums: err = %v", err)
	}
}
