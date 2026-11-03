package build

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/execx"
)

// TestWhisperRealBuild builds whisper.cpp for real, with the real programs, from a kit-style source
// tarball: the CI job `whisper-build` (.github/workflows/whisper-build.yml) runs it inside a network
// namespace (`unshare -n`) at the pinned release, and an operator can run it by hand. It is skipped
// unless LOCALAI_WHISPER_KIT_TARBALL names a file called whisper.cpp-<40 hex>.tar.gz (the layout of
// kit/src/, one top-level directory):
//
//	LOCALAI_WHISPER_KIT_TARBALL   the source tarball (required)
//	LOCALAI_WHISPER_BACKEND       cpu (default), vulkan or cuda
//	LOCALAI_WHISPER_MODEL         a ggml model file; with it the smoke test also transcribes a sample
//	LOCALAI_WHISPER_SAMPLE        a 16 kHz mono WAV of English speech (default: samples/jfk.wav of the source)
//	LOCALAI_EXPECT_NO_NETWORK=1   fail unless the network really is unreachable
//
// Nothing is activated: no service is restarted and no unit is touched. The build runs as the caller
// (a CI runner has no localai user).
func TestWhisperRealBuild(t *testing.T) {
	tarball := os.Getenv("LOCALAI_WHISPER_KIT_TARBALL")
	if tarball == "" {
		t.Skip("set LOCALAI_WHISPER_KIT_TARBALL to build whisper.cpp for real (see the comment of this test)")
	}
	m := regexp.MustCompile(`^whisper\.cpp-([0-9a-f]{40})\.tar\.gz$`).FindStringSubmatch(filepath.Base(tarball))
	if m == nil {
		t.Fatalf("the tarball %q is not named whisper.cpp-<40 hex>.tar.gz", tarball)
	}
	commit := m[1]
	backend := os.Getenv("LOCALAI_WHISPER_BACKEND")
	if backend == "" {
		backend = BackendCPU
	}
	if os.Getenv("LOCALAI_EXPECT_NO_NETWORK") == "1" {
		if c, err := net.DialTimeout("tcp", "1.1.1.1:443", 2*time.Second); err == nil {
			_ = c.Close()
			t.Fatal("the network is reachable: this build must run with it disabled")
		}
	}

	// A kit: the tarball and the digest the kit records for it.
	data, err := os.ReadFile(tarball)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	kitRoot := t.TempDir()
	srcDir := filepath.Join(kitRoot, "kit", "src")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	name := filepath.Base(tarball)
	if err := os.WriteFile(filepath.Join(srcDir, name), data, 0o644); err != nil {
		t.Fatal(err)
	}
	manifest := fmt.Sprintf(`{"schema_version":1,"files":[{"kind":"source_tarball","path":%q,"sha256":%q}]}`, name, hex.EncodeToString(sum[:]))
	if err := os.WriteFile(filepath.Join(srcDir, "MANIFEST.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}

	root, scratch := t.TempDir(), t.TempDir()
	opt := Options{
		Recipe: WhisperRecipe, Root: filepath.Join(root, "whisper.cpp"), ScratchDir: scratch,
		KitRoots: func() []string { return []string{kitRoot} }, CompileUser: CompileAsCaller,
		Defaults: func() Defaults { return Defaults{Commit: commit, Backend: backend, LlamaBackend: backend} },
		BuiltBy:  "aiagent test",
		Exec:     execx.Exec{},
	}
	model, sample := os.Getenv("LOCALAI_WHISPER_MODEL"), os.Getenv("LOCALAI_WHISPER_SAMPLE")
	if model != "" {
		opt.SmokeModel = func() (string, bool) { return model, true }
	}
	if sample != "" {
		opt.SmokeAudio = func() ([]byte, error) { return os.ReadFile(sample) }
	}
	mgr := New(opt)

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Minute)
	defer cancel()
	rep := mgr.PreflightReport(ctx, Request{FromKit: true, Offline: true})
	for _, c := range rep.Checks {
		t.Logf("preflight %-8s ok=%v %s", c.ID, c.OK, c.Detail)
	}
	if !rep.OK() {
		t.Fatalf("the preflight failed: %v", failedIDs(rep))
	}
	res, err := mgr.Build(ctx, Request{FromKit: true, Offline: true, NoActivate: true})
	if err != nil {
		if e, ok := err.(*Error); ok {
			t.Fatalf("%s: %s\n%v\n%v", e.Code, e.Message, e.Details["log_path"], e.Details["log_tail"])
		}
		t.Fatal(err)
	}
	t.Logf("build %s, warnings %v", res.BuildID, res.Warnings)
	if res.BuildID != commit[:9]+"-"+backend || res.Switched {
		t.Errorf("result = %+v", res)
	}
	for _, n := range WhisperRecipe.Programs {
		p := filepath.Join(res.Path, "bin", n)
		fi, err := os.Stat(p)
		if err != nil || fi.Mode().Perm()&0o111 == 0 || fi.Size() == 0 {
			t.Fatalf("%s: %v %v", p, fi, err)
		}
		libs, err := SharedWhisperLibs(OSFS{}, p)
		if err != nil || len(libs) != 0 {
			t.Errorf("%s needs %v (%v): the build must be static", n, libs, err)
		}
	}
	info := readBuildInfo(t, filepath.Join(res.Path, "BUILD.json"))
	t.Logf("BUILD.json: %+v", info)
	want := []string{"static", "help"}
	if model != "" {
		want = append(want, "transcribe")
	}
	if strings.Join(info.Smoke.Checks, ",") != strings.Join(want, ",") {
		t.Errorf("smoke checks = %v, want %v", info.Smoke.Checks, want)
	}
	if _, err := os.Stat(filepath.Join(scratch, "whisper.cpp-"+commit)); err == nil {
		t.Error("the scratch source tree was left behind")
	}
}
