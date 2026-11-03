package build

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/kit"
)

// The round trip of KIT-05: a build is exported by kit.PrebuiltExporter (the hook of the build
// manager) and restored by Request.Prebuilt on another node, for llama.cpp and for whisper.cpp.

// kitExporter is the real exporter over a temporary content root.
func kitExporter(h *harness, program, root string) *kit.PrebuiltExporter {
	return &kit.PrebuiltExporter{
		FS: kit.OSFS{}, Runner: h.exec, LookPath: h.opts.Lookup,
		Roots:   func() []string { return []string{root} },
		Program: program, Codename: "resolute", Arch: "amd64",
		NodeID: func() string { return "node-a" }, Kernel: func() string { return "6.14.0-test" },
	}
}

// exported returns the only archive of a root and the digest its manifest records.
func exported(t *testing.T, root, program string) (string, kit.PrebuiltRecord) {
	t.Helper()
	files := kit.ListPrebuilt(kit.OSFS{}, []string{root}, program)
	if len(files) != 1 {
		t.Fatalf("exports on the root = %d, want 1", len(files))
	}
	return files[0].Path, files[0].Record
}

func sameFiles(t *testing.T, a, b string, names ...string) {
	t.Helper()
	for _, n := range names {
		x, err1 := os.ReadFile(filepath.Join(a, n))
		y, err2 := os.ReadFile(filepath.Join(b, n))
		if err1 != nil || err2 != nil || !bytes.Equal(x, y) {
			t.Errorf("%s differs between the node that built it and the node that restored it (%v %v)", n, err1, err2)
		}
	}
}

func TestLlamaExportThenRestoreRoundTrip(t *testing.T) {
	expRoot := t.TempDir()
	a := newHarness(t)
	a.req.Commit = testSHA
	a.opts.Exporter = kitExporter(a, kit.ProgramLlama, expRoot)
	built, err := a.build()
	if err != nil || !built.Switched {
		t.Fatalf("build: %+v %v", built, err)
	}
	if len(built.Warnings) != 0 {
		t.Fatalf("the export warned: %v", built.Warnings)
	}
	file, rec := exported(t, expRoot, kit.ProgramLlama)
	if filepath.Base(file) != "llama.cpp-"+testShort9+"-cuda-resolute-amd64-sm75.tar.gz" {
		t.Fatalf("file = %s", file)
	}

	// Another node, another folder tree: restore from the file and the digest of the manifest.
	restoreRoot := t.TempDir()
	b := newHarness(t)
	b.req = Request{Prebuilt: file, PrebuiltSHA256: rec.SHA256}
	b.opts.Exporter = kitExporter(b, kit.ProgramLlama, restoreRoot)
	res, err := b.build()
	if err != nil {
		t.Fatal(err)
	}
	if !res.Restored || !res.Switched || res.BuildID != testID || res.PreviousBuild != oldID {
		t.Fatalf("restore = %+v", res)
	}
	sameFiles(t, filepath.Join(a.root, "builds", testID), filepath.Join(b.root, "builds", testID), "BUILD.json", "bin/llama-server", "bin/llama-cli", "bin/llama-bench")
	if !b.exec.ran("llama-server --version") || len(b.probe.completions) != 1 {
		t.Errorf("the restored build was not smoke tested and checked: %v", b.exec.lines())
	}
	for _, c := range b.exec.all() {
		if n := filepath.Base(c.Path); n == "cmake" || n == "git" {
			t.Errorf("a restore ran %s", line(c))
		}
	}
	// The restored node exports it again, and the bytes are the same ones (the archive is reproducible).
	_, rec2 := exported(t, restoreRoot, kit.ProgramLlama)
	if rec2.SHA256 != rec.SHA256 {
		t.Errorf("re-export differs: %s vs %s", rec2.SHA256, rec.SHA256)
	}
}

func TestWhisperExportThenRestoreRoundTrip(t *testing.T) {
	expRoot := t.TempDir()
	a := newWhisperEnv(t)
	a.tarball(testSHA, "src", "")
	a.opts.Exporter = kitExporter(a.harness, kit.ProgramWhisper, expRoot)
	built, err := a.build()
	if err != nil || !built.Switched {
		t.Fatalf("build: %+v %v", built, err)
	}
	if len(built.Warnings) != 0 {
		t.Fatalf("the export warned: %v", built.Warnings)
	}
	file, rec := exported(t, expRoot, kit.ProgramWhisper)
	if filepath.Base(file) != "whisper.cpp-"+testShort9+"-cuda-resolute-amd64-sm75.tar.gz" {
		t.Fatalf("file = %s", file)
	}

	b := newWhisperEnv(t)
	b.req = Request{Prebuilt: file, PrebuiltSHA256: rec.SHA256}
	res, err := b.build()
	if err != nil {
		t.Fatal(err)
	}
	if !res.Restored || !res.Switched || res.BuildID != testID {
		t.Fatalf("restore = %+v", res)
	}
	sameFiles(t, filepath.Join(a.root(), "builds", testID), filepath.Join(b.root(), "builds", testID), "BUILD.json", "bin/whisper-server", "bin/whisper-cli")
	if !b.exec.ran("whisper-server --help") || !b.exec.ran("whisper-cli --help") {
		t.Errorf("the restored build was not smoke tested: %v", b.exec.lines())
	}
	if len(b.svc.calls) != 1 || b.svc.calls[0] != "whisper" || b.checks != 1 {
		t.Errorf("the speech service was not restarted and checked: %v %d", b.svc.calls, b.checks)
	}
	for _, c := range b.exec.all() {
		if n := filepath.Base(c.Path); n == "cmake" || n == "git" {
			t.Errorf("a restore ran %s", line(c))
		}
	}
}

func TestRestoreOfAChangedArchiveIsRefusedAndPlacesNothing(t *testing.T) {
	expRoot := t.TempDir()
	a := newHarness(t)
	a.req.Commit = testSHA
	a.opts.Exporter = kitExporter(a, kit.ProgramLlama, expRoot)
	if _, err := a.build(); err != nil {
		t.Fatal(err)
	}
	file, rec := exported(t, expRoot, kit.ProgramLlama)
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	flipped := append([]byte(nil), data...)
	flipped[len(flipped)/2] ^= 0xff
	tampered := filepath.Join(t.TempDir(), filepath.Base(file))
	if err := os.WriteFile(tampered, flipped, 0o644); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name   string
		digest string
	}{
		{"with the digest of the manifest", rec.SHA256},
		{"without it, the archive itself gives it away", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := newHarness(t)
			b.req = Request{Prebuilt: tampered, PrebuiltSHA256: tc.digest}
			_, err := b.build()
			var be *Error
			if !errors.As(err, &be) || be.Code != doctor.KIT001 {
				t.Fatalf("err = %v, want LAI-KIT-001", err)
			}
			if b.exists("builds/" + testID) {
				t.Error("nothing may be placed from a changed archive")
			}
			if b.link() != "builds/"+oldID {
				t.Errorf("current = %q, must stay on the build in use", b.link())
			}
		})
	}
}
