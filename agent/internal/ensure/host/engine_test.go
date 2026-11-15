package host

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/ensure"
	"github.com/marinoscar/local-ai/agent/internal/sync/manifest"
)

// musicFiles is one runtime version of kit-engine-acestep-cuda as sync writes it (#511).
func musicFiles(version string) map[string][]byte {
	return map[string][]byte{
		version + "/requirements.lock":                                 []byte("torch==2.10.0+cu128 --hash=sha256:x\n"),
		version + "/wheels/torch-2.10.0+cu128-cp312-linux_x86_64.whl":  []byte("torch"),
		version + "/wheels/localai_music_shim-0.1.0-py3-none-any.whl":  []byte("shim"),
		version + "/python/cpython-3.12.16-x86_64-install_only.tar.gz": []byte("python"),
	}
}

func TestEngineIsTheNewestVersionWithEveryFileOfItsRecord(t *testing.T) {
	f := newFixture(t)
	files := musicFiles("1.5.0")
	for k, v := range musicFiles("1.4.0") {
		files[k] = v
	}
	f.kit.Item("engines/acestep/cuda", "kit-engine-acestep-cuda", files)
	src, version, err := f.host.Engine(context.Background(), "acestep", "cuda")
	if err != nil || version != "1.5.0" {
		t.Fatalf("Engine = %+v %q %v", src, version, err)
	}
	if !strings.HasSuffix(src.File, filepath.Join("engines", "acestep", "cuda", "1.5.0", "requirements.lock")) || len(src.Files) != 4 {
		t.Fatalf("source = %+v", src)
	}
	for _, fs := range src.Files {
		if !strings.Contains(fs.Path, "/1.5.0/") || !manifest.IsSHA256(fs.SHA256) {
			t.Errorf("file %+v", fs)
		}
	}
	if _, _, err := f.host.Engine(context.Background(), "acestep", "vulkan"); !errors.Is(err, ensure.ErrKitMissing) {
		t.Errorf("vulkan: %v", err)
	}
}

func TestEngineWithAMissingFileOrNoRecordIsDamaged(t *testing.T) {
	for name, damage := range map[string]func(dir string){
		"a recorded file is missing": func(dir string) {
			_ = os.Remove(filepath.Join(dir, "1.5.0", "wheels", "localai_music_shim-0.1.0-py3-none-any.whl"))
		},
		"no record": func(dir string) { _ = os.Remove(filepath.Join(dir, "MANIFEST.json")) },
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			dir := f.kit.Item("engines/acestep/cuda", "kit-engine-acestep-cuda", musicFiles("1.5.0"))
			damage(dir)
			if _, _, err := f.host.Engine(context.Background(), "acestep", "cuda"); !errors.Is(err, ensure.ErrKitDamaged) {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

func TestInstallEngineChecksEveryFileBeforeRunningAnything(t *testing.T) {
	f := newFixture(t)
	dir := f.kit.Item("engines/acestep/cuda", "kit-engine-acestep-cuda", musicFiles("1.5.0"))
	src, version, err := f.host.Engine(context.Background(), "acestep", "cuda")
	if err != nil {
		t.Fatal(err)
	}
	// A wheel changed after the record was written.
	if err := os.WriteFile(filepath.Join(dir, "1.5.0", "wheels", "torch-2.10.0+cu128-cp312-linux_x86_64.whl"), []byte("tampered"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := f.host.InstallEngine(context.Background(), "acestep", src, version, "cuda"); !errors.Is(err, ensure.ErrKitDamaged) {
		t.Fatalf("err = %v", err)
	}
	if calls := f.runner.Calls(); len(calls) != 0 {
		t.Errorf("programs ran: %v", calls)
	}
}
