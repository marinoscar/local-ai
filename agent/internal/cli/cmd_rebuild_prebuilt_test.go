package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/cli/fake"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/execx"
	"github.com/marinoscar/local-ai/agent/internal/kit"
	"github.com/marinoscar/local-ai/agent/internal/router/build"
)

// Tests of the ready-made build half of `aictl rebuild-whisper` (--prebuilt, the offer, the export hook)
// and of the words shared with `aictl rebuild-llama` (KIT-05). The engine is the fake of
// cmd_rebuild_whisper_test.go; the exports are real files made by kit.PrebuiltExporter.

// hostFix is this computer as the kit commands see it.
type hostFix struct {
	osr, opt, goarch string
}

// noRunner refuses every program: an export of a CPU build runs none, and the tests that reach nvidia-smi
// answer through their own runner.
type noRunner struct{}

func (noRunner) Run(_ context.Context, c execx.Cmd) (execx.Result, error) {
	return execx.Result{}, errors.New("no such program in this test: " + c.Path)
}

func setKitHost(t *testing.T, codename, goarch string) *hostFix {
	t.Helper()
	h := &hostFix{osr: filepath.Join(t.TempDir(), "os-release"), opt: t.TempDir(), goarch: goarch}
	kitWrite(t, h.osr, "NAME=Ubuntu\nVERSION_CODENAME="+codename+"\n")
	old, oldK := kitHostFn, kitKernelFn
	kitHostFn = func() kitHost {
		return kitHost{FS: kit.OSFS{}, Runner: noRunner{}, LookPath: func(n string) (string, error) { return "/usr/bin/" + n, nil },
			OSRelease: h.osr, GOARCH: h.goarch, OptRoot: h.opt}
	}
	kitKernelFn = func() string { return "6.14.0-test" }
	t.Cleanup(func() { kitHostFn, kitKernelFn = old, oldK })
	return h
}

func shaHex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// exportSpec describes a build to export into a root.
type exportSpec struct {
	program, backend, cudaArch, commit, codename, arch, builtAt string
}

func (e exportSpec) withDefaults() exportSpec {
	if e.program == "" {
		e.program = kit.ProgramWhisper
	}
	if e.backend == "" {
		e.backend = "cuda"
	}
	if e.cudaArch == "" && e.backend == "cuda" {
		e.cudaArch = "75"
	}
	if e.commit == "" {
		e.commit = wCommit
	}
	if e.codename == "" {
		e.codename = "noble"
	}
	if e.arch == "" {
		e.arch = "amd64"
	}
	if e.builtAt == "" {
		e.builtAt = "2026-11-02T10:00:00Z"
	}
	return e
}

// buildDir writes a build folder as the engine leaves it: BUILD.json with the digest of every program.
func buildDir(t *testing.T, e exportSpec) string {
	t.Helper()
	e = e.withDefaults()
	return buildDirAt(t, filepath.Join(t.TempDir(), "builds", e.commit[:9]+"-"+e.backend), e)
}

// buildDirAt is buildDir in a folder of the caller's choosing.
func buildDirAt(t *testing.T, dir string, e exportSpec) string {
	t.Helper()
	e = e.withDefaults()
	progs := []string{"whisper-server", "whisper-cli"}
	if e.program == kit.ProgramLlama {
		progs = []string{"llama-server", "llama-cli", "llama-bench"}
	}
	sums := map[string]string{}
	for _, p := range progs {
		body := []byte("program " + p + " " + e.commit)
		kitWrite(t, filepath.Join(dir, "bin", p), string(body))
		sums[p] = shaHex(body)
	}
	doc := map[string]any{"build_id": e.commit[:9] + "-" + e.backend, "commit": e.commit, "backend": e.backend, "cmake_flags": []string{"-DGGML_NATIVE=ON"},
		"built_at": e.builtAt, "sha256": sums, "smoke": map[string]any{"version": "version: 1 (test)"}}
	if e.backend == "cuda" {
		doc["cuda_arch"] = e.cudaArch
		doc["nvcc"] = "12.4.131"
	}
	raw, _ := json.MarshalIndent(doc, "", "  ")
	kitWrite(t, filepath.Join(dir, "BUILD.json"), string(raw))
	return dir
}

// exportInto exports a build into the root and returns the file and its record.
func exportInto(t *testing.T, root string, e exportSpec) (string, kit.PrebuiltRecord) {
	t.Helper()
	e = e.withDefaults()
	ex := &kit.PrebuiltExporter{
		FS: kit.OSFS{}, Roots: func() []string { return []string{root} }, Program: e.program, Codename: e.codename, Arch: e.arch,
		NodeID: func() string { return "node-test" }, Kernel: func() string { return "6.14.0-test" },
	}
	res, err := ex.Export(context.Background(), buildDir(t, e))
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(kit.PrebuiltDir(root), res.File), res.Record
}

func (f *whisperFix) kitRoot() string { return f.env.Paths.InternalRoot }

// ---- --prebuilt ---------------------------------------------------------------------------------

func TestRebuildWhisperPrebuiltRestoresTheFile(t *testing.T) {
	f := newWhisperFix(t)
	file, rec := exportInto(t, f.kitRoot(), exportSpec{})
	f.eng.res = build.Result{BuildID: wShort + "-cuda", Switched: true, PreviousBuild: "0a1b2c3d4-cuda", Restored: true}
	r := f.run(t, "rebuild-whisper", "--prebuilt", file, "--yes")
	if r.code != 0 {
		t.Fatalf("exit %d\n%s\n%s", r.code, r.out, r.errOut)
	}
	checkGolden(t, "rebuild-whisper.prebuilt.en", r)
	req := f.lastReq(t)
	if req.Prebuilt != file || req.PrebuiltSHA256 != rec.SHA256 || req.Backend != "cuda" || req.FromKit || req.SourceDir != "" {
		t.Errorf("request = %+v", req)
	}
	if len(f.eng.resolved) != 1 || f.eng.resolved[0].Commit != wCommit || f.eng.resolved[0].Prebuilt != "" {
		t.Errorf("the inputs were resolved with %+v", f.eng.resolved)
	}
	es := f.run(t, "--lang", "es", "rebuild-whisper", "--prebuilt", file, "--yes")
	if es.code != 0 {
		t.Fatalf("exit %d\n%s\n%s", es.code, es.out, es.errOut)
	}
	checkGolden(t, "rebuild-whisper.prebuilt.es", es)
}

func TestRebuildWhisperPrebuiltDryRunOnlyVerifies(t *testing.T) {
	f := newWhisperFix(t)
	file, _ := exportInto(t, f.kitRoot(), exportSpec{})
	f.eng.res = build.Result{BuildID: wShort + "-cuda", DryRun: true, Restored: true}
	r := f.run(t, "rebuild-whisper", "--prebuilt", file, "--dry-run")
	if r.code != 0 || !strings.Contains(unwrap(r.out), "is intact and fits this computer. Nothing was changed.") || strings.Contains(r.errOut, "Restore the ready-made") {
		t.Fatalf("exit %d\n%s\n%s", r.code, r.out, r.errOut)
	}
	if req := f.lastReq(t); !req.DryRun || req.Prebuilt != file {
		t.Errorf("request = %+v", req)
	}
	if f.eng.opt.Exporter != nil {
		t.Error("a dry run must not set up an export")
	}
}

func TestRebuildWhisperPrebuiltIsRefusedWhenItDoesNotFit(t *testing.T) {
	tests := []struct {
		name   string
		spec   exportSpec
		inputs build.Inputs
		goarch string
		args   []string
		want   []string
		golden string
	}{
		{"another Ubuntu release", exportSpec{codename: "resolute"}, build.Inputs{}, "amd64", nil,
			[]string{"Ubuntu version: the build is for resolute, this computer has noble"}, "rebuild-whisper.prebuilt.release"},
		{"another processor", exportSpec{arch: "arm64"}, build.Inputs{}, "amd64", nil,
			[]string{"processor: the build is for arm64, this computer has amd64"}, ""},
		{"another backend", exportSpec{backend: "vulkan"}, build.Inputs{}, "amd64", nil,
			[]string{"backend: the build is for vulkan, this computer has cuda"}, ""},
		{"another graphics chip", exportSpec{cudaArch: "86"}, build.Inputs{}, "amd64", nil,
			[]string{"graphics chip (CUDA architecture): the build is for 86, this computer has 75"}, ""},
		{"another commit than asked for", exportSpec{}, build.Inputs{}, "amd64", []string{"--commit", "abcdef1"},
			[]string{"version of the source: the build is for 1a2b3c4d5, this computer has abcdef1"}, ""},
		{"several differences are all named", exportSpec{codename: "resolute", cudaArch: "86"}, build.Inputs{}, "amd64", nil,
			[]string{"Ubuntu version: the build is for resolute", "graphics chip (CUDA architecture): the build is for 86"}, ""},
		{"a llama build for the speech command", exportSpec{program: kit.ProgramLlama}, build.Inputs{}, "amd64", nil,
			[]string{"program: the build is for llama.cpp, this computer has whisper.cpp"}, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newWhisperFix(t)
			f.host.goarch = tc.goarch
			file, _ := exportInto(t, f.kitRoot(), tc.spec)
			// Another export the kit has, to be named in the message.
			exportInto(t, f.kitRoot(), exportSpec{backend: "cpu", commit: "0a1b2c3d4" + strings.Repeat("0", 31)})
			r := f.run(t, append([]string{"rebuild-whisper", "--prebuilt", file, "--yes"}, tc.args...)...)
			if r.code != ExitFailed || !strings.Contains(r.errOut, "["+doctor.KIT048+"]") {
				t.Fatalf("exit %d\n%s", r.code, r.errOut)
			}
			for _, w := range tc.want {
				if !strings.Contains(unwrap(r.errOut), w) {
					t.Errorf("stderr lacks %q:\n%s", w, r.errOut)
				}
			}
			if !strings.Contains(unwrap(r.errOut), "Nothing was changed.") || !strings.Contains(unwrap(r.errOut), "The kit has: whisper.cpp-0a1b2c3d4-cpu-noble-amd64.tar.gz.") {
				t.Errorf("the message must say nothing changed and what the kit has:\n%s", r.errOut)
			}
			if len(f.eng.reqs) != 0 {
				t.Error("a build that does not fit reached the engine")
			}
			if lines := f.auditLines(); len(lines) != 0 {
				t.Errorf("a refused restore must not be audited as a switch: %v", lines)
			}
			if tc.golden != "" {
				checkGolden(t, tc.golden+".en", r)
				es := f.run(t, append([]string{"--lang", "es", "rebuild-whisper", "--prebuilt", file, "--yes"}, tc.args...)...)
				checkGolden(t, tc.golden+".es", es)
			}
		})
	}
}

func TestRebuildWhisperPrebuiltWithAnotherBackendUsesWhatTheNodeResolves(t *testing.T) {
	// The node has no NVIDIA chip (the engine resolves cpu) and the file is for cuda: refused, naming both.
	f := newWhisperFix(t)
	f.eng.in = build.Inputs{Commit: wCommit, ShortCommit: wShort, Backend: "cpu", Jobs: 4, Activate: true}
	file, _ := exportInto(t, f.kitRoot(), exportSpec{})
	r := f.run(t, "rebuild-whisper", "--prebuilt", file, "--yes")
	if r.code != ExitFailed || !strings.Contains(unwrap(r.errOut), "backend: the build is for cuda, this computer has cpu") {
		t.Fatalf("exit %d\n%s", r.code, r.errOut)
	}
}

func TestRebuildWhisperPrebuiltThatTheKitDoesNotListIsDistrusted(t *testing.T) {
	tests := []struct {
		name string
		edit func(t *testing.T, file string)
	}{
		{"the list is missing", func(t *testing.T, file string) { _ = os.Remove(filepath.Join(filepath.Dir(file), "MANIFEST.json")) }},
		{"the list is damaged", func(t *testing.T, file string) {
			kitWrite(t, filepath.Join(filepath.Dir(file), "MANIFEST.json"), "{broken")
		}},
		{"the file is not in the list", func(t *testing.T, file string) {
			kitWrite(t, filepath.Join(filepath.Dir(file), "MANIFEST.json"), `{"schema":"localai.prebuilt-manifest","schema_version":1,"files":[]}`)
		}},
		{"the file changed size", func(t *testing.T, file string) {
			b, _ := os.ReadFile(file)
			_ = os.WriteFile(file, b[:len(b)-2], 0o644)
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newWhisperFix(t)
			file, _ := exportInto(t, f.kitRoot(), exportSpec{})
			tc.edit(t, file)
			r := f.run(t, "rebuild-whisper", "--prebuilt", file, "--yes")
			if r.code != ExitFailed || !strings.Contains(r.errOut, "["+doctor.KIT001+"]") || !strings.Contains(unwrap(r.errOut), "cannot be trusted. Nothing was changed.") {
				t.Fatalf("exit %d\n%s", r.code, r.errOut)
			}
			if len(f.eng.reqs) != 0 {
				t.Error("a file the kit does not vouch for reached the engine")
			}
		})
	}
}

func TestRebuildWhisperPrebuiltFileThatIsNotThere(t *testing.T) {
	f := newWhisperFix(t)
	r := f.run(t, "rebuild-whisper", "--prebuilt", filepath.Join(t.TempDir(), "none.tar.gz"), "--yes")
	if r.code != ExitFailed || !strings.Contains(r.errOut, "["+doctor.KIT001+"]") || len(f.eng.reqs) != 0 {
		t.Fatalf("exit %d\n%s", r.code, r.errOut)
	}
}

func TestRebuildWhisperPrebuiltCopyOutsideTheKitIsWarnedAbout(t *testing.T) {
	f := newWhisperFix(t)
	file, _ := exportInto(t, f.kitRoot(), exportSpec{})
	data, _ := os.ReadFile(file)
	outside := filepath.Join(t.TempDir(), filepath.Base(file))
	if err := os.WriteFile(outside, data, 0o644); err != nil {
		t.Fatal(err)
	}
	r := f.run(t, "rebuild-whisper", "--prebuilt", outside, "--yes")
	if r.code != 0 || !strings.Contains(unwrap(r.errOut), "is not in a kit's list of ready-made builds, so only the checksums inside it vouch for it") {
		t.Fatalf("exit %d\n%s\n%s", r.code, r.out, r.errOut)
	}
	if req := f.lastReq(t); req.Prebuilt != outside || req.PrebuiltSHA256 != "" {
		t.Errorf("no archive digest is known for a copy outside the kit: %+v", req)
	}
}

func TestRebuildWhisperPrebuiltConfirmationAndEngineErrors(t *testing.T) {
	t.Run("a person who says no changes nothing", func(t *testing.T) {
		f := newWhisperFix(t)
		file, _ := exportInto(t, f.kitRoot(), exportSpec{})
		r := runCLI(t, DefaultRegistry, localOpts(fake.Typed(true, "n")), "rebuild-whisper", "--prebuilt", file)
		if r.code != ExitRefused || !strings.Contains(r.out, "Nothing was changed.") || len(f.eng.reqs) != 0 {
			t.Fatalf("exit %d\n%s\n%s", r.code, r.out, r.errOut)
		}
		if !strings.Contains(unwrap(r.errOut), "Restore the ready-made whisper.cpp build 1a2b3c4d5 and switch to it? Nothing is compiled; it takes a moment.") {
			t.Errorf("prompt = %q", r.errOut)
		}
	})
	t.Run("no terminal and no --yes asks nothing", func(t *testing.T) {
		f := newWhisperFix(t)
		file, _ := exportInto(t, f.kitRoot(), exportSpec{})
		r := f.run(t, "rebuild-whisper", "--prebuilt", file)
		if r.code != ExitUsage || !strings.Contains(r.errOut, "["+CodeNeedConfirm+"]") || len(f.eng.reqs) != 0 {
			t.Fatalf("exit %d\n%s", r.code, r.errOut)
		}
	})
	t.Run("--no-activate says it is not switched to", func(t *testing.T) {
		f := newWhisperFix(t)
		file, _ := exportInto(t, f.kitRoot(), exportSpec{})
		r := runCLI(t, DefaultRegistry, localOpts(fake.Typed(true, "n")), "rebuild-whisper", "--prebuilt", file, "--no-activate")
		if !strings.Contains(unwrap(r.errOut), "without switching to it?") {
			t.Fatalf("prompt = %q", r.errOut)
		}
	})
	t.Run("the engine refuses a damaged file", func(t *testing.T) {
		f := newWhisperFix(t)
		file, _ := exportInto(t, f.kitRoot(), exportSpec{})
		f.eng.err = &build.Error{Code: doctor.KIT001, Stage: build.StageSource, Message: "The prebuilt build is damaged: bin/whisper-cli does not match the sha256 in BUILD.json. Run `aictl kit verify --full`.",
			Details: map[string]any{"cause": "prebuilt_tampered", "file": "whisper-cli"}}
		r := f.run(t, "rebuild-whisper", "--prebuilt", file, "--yes")
		if r.code != ExitFailed || !strings.Contains(r.errOut, "["+doctor.KIT001+"]") || !strings.Contains(unwrap(r.errOut), "bin/whisper-cli does not match the sha256 in BUILD.json") {
			t.Fatalf("exit %d\n%s", r.code, r.errOut)
		}
	})
	t.Run("--json is the result document", func(t *testing.T) {
		f := newWhisperFix(t)
		file, _ := exportInto(t, f.kitRoot(), exportSpec{})
		r := f.run(t, "--json", "rebuild-whisper", "--prebuilt", file, "--yes")
		if r.code != 0 || r.errOut != "" {
			t.Fatalf("exit %d\n%s\n%s", r.code, r.out, r.errOut)
		}
		var doc struct {
			BuildID  string `json:"build_id"`
			Switched bool   `json:"switched"`
		}
		if err := json.Unmarshal([]byte(r.out), &doc); err != nil || !doc.Switched || doc.BuildID != wShort+"-cuda" {
			t.Fatalf("%v: %q", err, r.out)
		}
	})
}

// ---- the offer of a file that fits exactly -------------------------------------------------------

func TestRebuildWhisperOffersAMatchingReadyMadeBuild(t *testing.T) {
	prompt := "The kit has a ready-made build for exactly this computer (whisper.cpp-1a2b3c4d5-cuda-noble-amd64-sm75.tar.gz). Use it instead of compiling?"
	t.Run("yes restores it instead of compiling", func(t *testing.T) {
		f := newWhisperFix(t)
		file, rec := exportInto(t, f.kitRoot(), exportSpec{})
		// First answer: use it; second: switch to it.
		r := runCLI(t, DefaultRegistry, localOpts(fake.Typed(true, "y", "y")), "rebuild-whisper")
		if r.code != 0 || !strings.Contains(unwrap(r.errOut), prompt) {
			t.Fatalf("exit %d\n%s\n%s", r.code, r.out, r.errOut)
		}
		if req := f.lastReq(t); req.Prebuilt != file || req.PrebuiltSHA256 != rec.SHA256 {
			t.Errorf("request = %+v", req)
		}
		if !strings.Contains(unwrap(r.out), "Restored from whisper.cpp-1a2b3c4d5-cuda-noble-amd64-sm75.tar.gz; nothing was compiled.") {
			t.Errorf("stdout = %q", r.out)
		}
	})
	t.Run("no builds as before", func(t *testing.T) {
		f := newWhisperFix(t)
		exportInto(t, f.kitRoot(), exportSpec{})
		r := runCLI(t, DefaultRegistry, localOpts(fake.Typed(true, "n", "y")), "rebuild-whisper")
		if r.code != 0 {
			t.Fatalf("exit %d\n%s\n%s", r.code, r.out, r.errOut)
		}
		if req := f.lastReq(t); req.Prebuilt != "" {
			t.Errorf("a refused offer must compile: %+v", req)
		}
	})
	t.Run("--yes does not choose for the operator, it says how", func(t *testing.T) {
		f := newWhisperFix(t)
		file, _ := exportInto(t, f.kitRoot(), exportSpec{})
		r := f.run(t, "rebuild-whisper", "--yes")
		if r.code != 0 || f.lastReq(t).Prebuilt != "" {
			t.Fatalf("exit %d\n%s\n%s", r.code, r.out, r.errOut)
		}
		want := "A ready-made build that fits this computer is in the kit: to use it instead of compiling, run `sudo aictl rebuild-whisper --prebuilt " + file + "`."
		if !strings.Contains(unwrap(r.out), want) {
			t.Errorf("stdout lacks the hint %q:\n%s", want, r.out)
		}
	})
	t.Run("json says nothing and compiles", func(t *testing.T) {
		f := newWhisperFix(t)
		exportInto(t, f.kitRoot(), exportSpec{})
		r := f.run(t, "--json", "rebuild-whisper", "--yes")
		if r.code != 0 || r.errOut != "" || f.lastReq(t).Prebuilt != "" || strings.Contains(r.out, "ready-made") {
			t.Fatalf("exit %d\n%s\n%s", r.code, r.out, r.errOut)
		}
	})
	t.Run("a source folder skips the offer", func(t *testing.T) {
		f := newWhisperFix(t)
		exportInto(t, f.kitRoot(), exportSpec{})
		r := runCLI(t, DefaultRegistry, localOpts(fake.Typed(true, "y")), "rebuild-whisper", "--source", "/tmp/whisper-src")
		if r.code != 0 || strings.Contains(r.errOut, "ready-made") || f.lastReq(t).Prebuilt != "" {
			t.Fatalf("exit %d\n%s\n%s", r.code, r.out, r.errOut)
		}
	})
	// Nothing near is offered: another release, another chip, another commit, another backend, another program.
	for _, tc := range []struct {
		name string
		spec exportSpec
	}{
		{"another Ubuntu release", exportSpec{codename: "resolute"}},
		{"another chip", exportSpec{cudaArch: "86"}},
		{"another commit", exportSpec{commit: "0a1b2c3d4" + strings.Repeat("0", 31)}},
		{"another backend", exportSpec{backend: "vulkan"}},
		{"another program", exportSpec{program: kit.ProgramLlama}},
	} {
		t.Run("not offered: "+tc.name, func(t *testing.T) {
			f := newWhisperFix(t)
			exportInto(t, f.kitRoot(), tc.spec)
			r := f.run(t, "rebuild-whisper", "--yes")
			if r.code != 0 || strings.Contains(r.out+r.errOut, "ready-made") || f.lastReq(t).Prebuilt != "" {
				t.Fatalf("exit %d\n%s\n%s", r.code, r.out, r.errOut)
			}
		})
	}
}

// ---- the export hook ----------------------------------------------------------------------------

func TestRebuildWhisperSetsUpTheExportOfItsBuild(t *testing.T) {
	f := newWhisperFix(t)
	if err := os.MkdirAll(f.kitRoot(), 0o755); err != nil {
		t.Fatal(err)
	}
	// What the engine does after a switch, while the command runs: the build goes to the exporter.
	var exportErr error
	f.eng.onBuild = func(ctx context.Context, opt build.Options) {
		exportErr = opt.Exporter.ExportBuild(ctx, wShort+"-cuda", buildDir(t, exportSpec{}))
	}
	if r := f.run(t, "rebuild-whisper", "--yes"); r.code != 0 {
		t.Fatalf("exit %d\n%s\n%s", r.code, r.out, r.errOut)
	}
	ex, ok := f.eng.opt.Exporter.(*kit.PrebuiltExporter)
	if !ok || ex == nil {
		t.Fatalf("Options.Exporter = %T", f.eng.opt.Exporter)
	}
	if ex.Program != kit.ProgramWhisper || ex.Codename != "noble" || ex.Arch != "amd64" {
		t.Errorf("exporter = %+v", ex)
	}
	if exportErr != nil {
		t.Fatal(exportErr)
	}
	files := kit.ListPrebuilt(kit.OSFS{}, []string{f.kitRoot()}, kit.ProgramWhisper)
	if len(files) != 1 || files[0].Record.File != "whisper.cpp-1a2b3c4d5-cuda-noble-amd64-sm75.tar.gz" {
		t.Fatalf("exports = %+v", files)
	}
	if got := files[0].Record.Kernel; got != "6.14.0-test" {
		t.Errorf("kernel = %q", got)
	}
}

func TestRebuildWhisperWithAnUnknownUbuntuReleaseExportsNothingButStillBuilds(t *testing.T) {
	f := newWhisperFix(t)
	kitWrite(t, f.host.osr, "NAME=Mystery\n")
	r := f.run(t, "rebuild-whisper", "--yes")
	if r.code != 0 {
		t.Fatalf("exit %d\n%s\n%s", r.code, r.out, r.errOut)
	}
	if f.eng.opt.Exporter != nil {
		t.Errorf("no exporter can name its file without the release: %T", f.eng.opt.Exporter)
	}
}
