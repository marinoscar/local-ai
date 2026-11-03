package cli

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/audit"
	"github.com/marinoscar/local-ai/agent/internal/cli/fake"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/router/build"
)

// Tests of `aictl rebuild-llama` (AICTL-09, aictl.md section 4.23). The engine is replaced by a fake that
// records its options and requests (its own behaviour is covered in internal/router/build); the node is
// a temporary directory. Commits are built at run time.

const lShort = "f3586e66b"

var lCommit = lShort + strings.Repeat("1", 31)

const lDevice = "CUDA0: NVIDIA GeForce RTX 2070 with Max-Q Design (7981 MiB, 7012 MiB free)"

// fakeLlamaEngine is the engine the command talks to (the one of the whisper tests serves both).
type fakeLlamaEngine = fakeWhisperEngine

type llamaFix struct {
	*localFixture
	eng  *fakeLlamaEngine
	host *hostFix
	made int
	// sig is what a Ctrl-C is sent on; ticks drives the heartbeat.
	sig   chan struct{}
	ticks chan time.Time
}

func newLlamaFix(t *testing.T) *llamaFix {
	t.Helper()
	f := &llamaFix{localFixture: newLocalFixture(t), sig: make(chan struct{}, 4), ticks: make(chan time.Time)}
	f.host = setKitHost(t, "noble", "amd64")
	f.eng = &fakeLlamaEngine{
		in: build.Inputs{Commit: lCommit, ShortCommit: lShort, Backend: "cuda", CudaArch: "75", Jobs: 5, Activate: true},
		res: build.Result{BuildID: lShort + "-cuda", Switched: true, PreviousBuild: "0a1b2c3d4-cuda", DefaultModel: "qwen3.5-9b-q4km",
			BuildJSON: build.BuildInfo{BuildID: lShort + "-cuda", Commit: lCommit, Backend: "cuda", CudaArch: "75", CMakeFlags: []string{"-DGGML_CUDA=ON"},
				SHA256: map[string]string{"llama-server": "a1", "llama-cli": "b2", "llama-bench": "c3"},
				Smoke:  build.Smoke{Version: "version: 1 (" + lShort + ")", Devices: []string{lDevice}}}},
	}
	oldNew, oldUser, oldSig, oldHB, oldClock := llamaNewEngine, llamaCompileUser, llamaInterrupts, llamaHeartbeat, llamaClock
	llamaNewEngine = func(opt build.Options) llamaEngine {
		f.made++
		f.eng.opt = opt
		return f.eng
	}
	llamaCompileUser = build.CompileAsCaller
	llamaInterrupts = func() (<-chan struct{}, func()) { return f.sig, func() {} }
	llamaHeartbeat = func() (<-chan time.Time, func()) { return f.ticks, func() {} }
	t.Cleanup(func() {
		llamaNewEngine, llamaCompileUser, llamaInterrupts, llamaHeartbeat, llamaClock = oldNew, oldUser, oldSig, oldHB, oldClock
	})
	return f
}

func (f *llamaFix) run(t *testing.T, args ...string) runResult {
	t.Helper()
	return runCLI(t, DefaultRegistry, localOpts(nil), args...)
}

func (f *llamaFix) nodeYAML(t *testing.T, s string) {
	t.Helper()
	if err := os.WriteFile(f.env.Paths.NodeYAML(), []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (f *llamaFix) lastReq(t *testing.T) build.Request {
	t.Helper()
	if len(f.eng.reqs) == 0 {
		t.Fatal("the engine was not asked to build")
	}
	return f.eng.reqs[len(f.eng.reqs)-1]
}

func (f *llamaFix) kitRoot() string { return f.env.Paths.InternalRoot }

func (f *llamaFix) makeKit(t *testing.T) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(f.kitRoot(), "kit"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func (f *llamaFix) state(t *testing.T, body string) {
	t.Helper()
	if err := os.WriteFile(f.env.Paths.StateFile(), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func llamaBuildErr(code string, stage build.Stage, msg string, kv ...any) *build.Error {
	e := &build.Error{Code: code, Stage: stage, Message: msg, Details: map[string]any{"stage": string(stage)}}
	for i := 0; i+1 < len(kv); i += 2 {
		e.Details[kv[i].(string)] = kv[i+1]
	}
	return e
}

// ---- the happy path and its words ---------------------------------------------------------------

func TestRebuildLlamaSuccessLines(t *testing.T) {
	f := newLlamaFix(t)
	f.eng.progress = []build.Progress{
		{Stage: build.StageConfigure, Message: "Building llama.cpp " + lShort + " for cuda (sm 75), 5 parallel jobs"},
		{Stage: build.StageBuild, Message: "Compiling (this takes a while)"},
	}
	r := f.run(t, "rebuild-llama", "--yes")
	if r.code != 0 {
		t.Fatalf("exit %d\n%s\n%s", r.code, r.out, r.errOut)
	}
	checkGolden(t, "rebuild-llama.en", r)
	if n := strings.Count(unwrap(r.errOut), "Building llama.cpp"); n != 1 {
		t.Errorf("the plan line is printed %d times, want once:\n%s", n, r.errOut)
	}
	if !strings.Contains(unwrap(r.errOut), "… Compiling (this takes a while)") {
		t.Errorf("progress goes to stderr: %q", r.errOut)
	}
	es := f.run(t, "--lang", "es", "rebuild-llama", "--yes")
	if es.code != 0 {
		t.Fatalf("exit %d\n%s\n%s", es.code, es.out, es.errOut)
	}
	checkGolden(t, "rebuild-llama.es", es)
}

func TestRebuildLlamaSpecExampleLinesAreExact(t *testing.T) {
	f := newLlamaFix(t)
	r := f.run(t, "rebuild-llama", "--yes")
	out := unwrap(r.out)
	for _, want := range []string{
		"✓ Build " + lShort + "-cuda passed its test (CUDA0: NVIDIA GeForce RTX 2070 with Max-Q Design).",
		"✓ Switched to the new build. qwen3.5-9b-q4km is loaded again.",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout lacks %q:\n%s", want, r.out)
		}
	}
	if want := "… Building llama.cpp " + lShort + " for cuda (sm 75), 5 parallel jobs  (about 20–40 minutes)"; !strings.Contains(unwrap(r.errOut), strings.Join(strings.Fields(want), " ")) {
		t.Errorf("stderr lacks the plan line:\n%s", r.errOut)
	}
}

func TestRebuildLlamaNamesTheModelWithItsDisplayName(t *testing.T) {
	f := newLlamaFix(t)
	rig := newMgRig(t, fake.Options{})
	rig.agent().Respond("GET", "/models", 200, `{"default":"qwen3.5-9b-q4km","models":[{"id":"qwen3.5-9b-q4km","display_name":"Qwen 3.5 9B","engine":"llama"}]}`)
	rig.agent().Respond("GET", "/mode", 200, `{"mode":"online"}`)
	got := rig.run(mgArgs("rebuild-llama", "--yes"))
	if got.code != 0 || !strings.Contains(unwrap(got.out), "✓ Switched to the new build. Qwen 3.5 9B is loaded again.") {
		t.Fatalf("exit %d\n%s\n%s", got.code, got.out, got.errOut)
	}
	_ = f
}

func TestRebuildLlamaWithoutADefaultModelStillSaysItSwitched(t *testing.T) {
	f := newLlamaFix(t)
	f.eng.res.DefaultModel = ""
	f.eng.res.Warnings = []string{"No default model is set, so the new build was only checked for answering /health."}
	r := f.run(t, "rebuild-llama", "--yes")
	if r.code != 0 || !strings.Contains(r.out, "✓ Switched to the new build.\n") || !strings.Contains(unwrap(r.errOut), "! No default model is set") {
		t.Fatalf("exit %d\n%s\n%s", r.code, r.out, r.errOut)
	}
}

func TestRebuildLlamaNoActivateSaysItWasNotSwitchedOn(t *testing.T) {
	f := newLlamaFix(t)
	f.eng.res.Switched = false
	r := f.run(t, "rebuild-llama", "--no-activate", "--yes")
	if r.code != 0 || !strings.Contains(unwrap(r.out), "passed its test (CUDA0: NVIDIA GeForce RTX 2070 with Max-Q Design). It was not switched on (--no-activate): the build in use did not change.") ||
		strings.Contains(r.out, "Switched") {
		t.Fatalf("exit %d\n%s\n%s", r.code, r.out, r.errOut)
	}
	if !f.lastReq(t).NoActivate {
		t.Error("--no-activate did not reach the engine")
	}
}

func TestRebuildLlamaJSONIsTheResultDocument(t *testing.T) {
	f := newLlamaFix(t)
	r := f.run(t, "--json", "rebuild-llama", "--yes")
	if r.code != 0 || r.errOut != "" {
		t.Fatalf("exit %d\n%s\n%s", r.code, r.out, r.errOut)
	}
	var doc struct {
		BuildID       string `json:"build_id"`
		Switched      bool   `json:"switched"`
		PreviousBuild string `json:"previous_build"`
		BuildJSON     struct {
			BuildID    string            `json:"build_id"`
			Commit     string            `json:"commit"`
			Backend    string            `json:"backend"`
			CudaArch   string            `json:"cuda_arch"`
			CMakeFlags []string          `json:"cmake_flags"`
			SHA256     map[string]string `json:"sha256"`
			Smoke      struct {
				Devices []string `json:"devices"`
			} `json:"smoke"`
		} `json:"build_json"`
	}
	if err := json.Unmarshal([]byte(r.out), &doc); err != nil {
		t.Fatalf("%v: %q", err, r.out)
	}
	if doc.BuildID != lShort+"-cuda" || !doc.Switched || doc.PreviousBuild != "0a1b2c3d4-cuda" || doc.BuildJSON.Commit != lCommit ||
		doc.BuildJSON.CudaArch != "75" || len(doc.BuildJSON.SHA256) != 3 || len(doc.BuildJSON.Smoke.Devices) != 1 {
		t.Errorf("doc = %+v", doc)
	}
}

// ---- flags: the table of AICTL-09 ---------------------------------------------------------------

func TestRebuildLlamaFlagsMapToTheEngineRequest(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want func(r build.Request) bool
	}{
		{"nothing: the engine takes node.yaml", nil, func(r build.Request) bool {
			return r.Commit == "" && r.Backend == "" && r.CudaArch == "" && r.Jobs == 0 && r.Keep == 0 && !r.NoActivate && !r.FromKit && !r.Offline && !r.DryRun && r.SourceDir == "" && r.Prebuilt == ""
		}},
		{"commit is lower-cased", []string{"--commit", "F3586E66B"}, func(r build.Request) bool { return r.Commit == "f3586e66b" }},
		{"a full commit", []string{"--commit", lCommit}, func(r build.Request) bool { return r.Commit == lCommit }},
		{"backend", []string{"--backend", "vulkan"}, func(r build.Request) bool { return r.Backend == "vulkan" }},
		{"one compute capability", []string{"--cuda-arch", "75"}, func(r build.Request) bool { return r.CudaArch == "75" }},
		{"two compute capabilities", []string{"--cuda-arch", "75;86", "--backend", "cuda"}, func(r build.Request) bool { return r.CudaArch == "75;86" && r.Backend == "cuda" }},
		{"jobs", []string{"--jobs", "3"}, func(r build.Request) bool { return r.Jobs == 3 }},
		{"keep", []string{"--keep", "4"}, func(r build.Request) bool { return r.Keep == 4 }},
		{"from the kit means offline too", []string{"--from-kit"}, func(r build.Request) bool { return r.FromKit && r.Offline && r.SourceDir == "" }},
		{"a source folder", []string{"--source", "/tmp/llama-src"}, func(r build.Request) bool { return r.SourceDir == "/tmp/llama-src" && !r.FromKit && !r.Offline }},
		{"no activation", []string{"--no-activate"}, func(r build.Request) bool { return r.NoActivate }},
		{"the default activates", []string{"--activate"}, func(r build.Request) bool { return !r.NoActivate }},
		{"-y is --yes", []string{"-y"}, func(r build.Request) bool { return true }},
		{"everything at once", []string{"--commit", "abcdef1", "--backend", "cuda", "--cuda-arch", "75;86", "--jobs", "2", "--keep", "3", "--no-activate", "--from-kit"},
			func(r build.Request) bool {
				return r.Commit == "abcdef1" && r.Backend == "cuda" && r.CudaArch == "75;86" && r.Jobs == 2 && r.Keep == 3 && r.NoActivate && r.FromKit && r.Offline
			}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newLlamaFix(t)
			f.makeKit(t)
			args := append([]string{"rebuild-llama"}, tc.args...)
			if !contains(tc.args, "-y") {
				args = append(args, "--yes")
			}
			if r := f.run(t, args...); r.code != 0 {
				t.Fatalf("exit %d\n%s\n%s", r.code, r.out, r.errOut)
			}
			if got := f.lastReq(t); !tc.want(got) {
				t.Errorf("request = %+v", got)
			}
			if f.made != 1 {
				t.Errorf("engines made = %d", f.made)
			}
		})
	}
}

func TestRebuildLlamaDefaultsComeFromNodeYAML(t *testing.T) {
	f := newLlamaFix(t)
	f.nodeYAML(t, "schema_version: 1\nllama:\n  commit: \""+lCommit+"\"\n  backend: vulkan\n  cuda_arch: \"89\"\nports:\n  llama: 8123\n")
	if r := f.run(t, "rebuild-llama", "--yes"); r.code != 0 {
		t.Fatalf("exit %d\n%s", r.code, r.errOut)
	}
	node := f.eng.opt.Node()
	if node.Commit != lCommit || node.Backend != "vulkan" || node.CudaArch == nil || *node.CudaArch != "89" {
		t.Errorf("node = %+v", node)
	}
	// The real engine, given those options, resolves the defaults the same way.
	in, err := build.New(f.eng.opt).Resolve(context.Background(), build.Request{})
	if err != nil || in.Commit != lCommit || in.ShortCommit != lShort || in.Backend != "vulkan" || in.CudaArch != "" {
		t.Errorf("inputs = %+v, %v", in, err)
	}
	in, err = build.New(f.eng.opt).Resolve(context.Background(), build.Request{Backend: "cuda"})
	if err != nil || in.Backend != "cuda" || in.CudaArch != "89" {
		t.Errorf("inputs for cuda = %+v, %v", in, err)
	}
	in, err = build.New(f.eng.opt).Resolve(context.Background(), build.Request{Backend: "cuda", CudaArch: "75;86", Commit: "abcdef1"})
	if err != nil || in.CudaArch != "75;86" || in.Commit != "abcdef1" {
		t.Errorf("inputs for flags = %+v, %v", in, err)
	}
}

func TestRebuildLlamaWithoutNodeYAMLNeedsTheFlags(t *testing.T) {
	f := newLlamaFix(t)
	if r := f.run(t, "rebuild-llama", "--yes", "--commit", lShort, "--backend", "cpu"); r.code != 0 {
		t.Fatalf("exit %d\n%s", r.code, r.errOut)
	}
	if n := f.eng.opt.Node(); n.Commit != "" || n.Backend != "" || n.CudaArch != nil {
		t.Errorf("node = %+v, want none", n)
	}
	if r := f.lastReq(t); r.Commit != lShort || r.Backend != "cpu" {
		t.Errorf("request = %+v", r)
	}
}

// ---- usage errors come before anything is touched -----------------------------------------------

func TestRebuildLlamaUsageErrors(t *testing.T) {
	tests := []struct {
		name string
		args []string
		in   string // a part of the message
	}{
		{"a bad commit", []string{"--commit", "xyz"}, "7 to 40 lowercase hex"},
		{"a commit that is too short", []string{"--commit", "abc12"}, "7 to 40 lowercase hex"},
		{"a bad backend", []string{"--backend", "gpu"}, "not one of cuda, vulkan or cpu"},
		{"auto is not a llama backend", []string{"--backend", "auto"}, "not one of cuda, vulkan or cpu"},
		{"a compute capability with the dot", []string{"--cuda-arch", "7.5"}, "must look like 75 or 75;86"},
		{"a compute capability list with commas", []string{"--cuda-arch", "75,86"}, "must look like 75 or 75;86"},
		{"a compute capability for another backend", []string{"--cuda-arch", "75", "--backend", "vulkan"}, "--cuda-arch only applies to --backend cuda"},
		{"source and kit", []string{"--source", "/x", "--from-kit"}, "--source and --from-kit cannot be used together"},
		{"activate and no-activate", []string{"--activate", "--no-activate"}, "--activate and --no-activate cannot be used together"},
		{"prebuilt with a source", []string{"--prebuilt", "x.tar.gz", "--source", "/x"}, "--prebuilt cannot be used with --source or --from-kit"},
		{"prebuilt with the kit", []string{"--prebuilt", "x.tar.gz", "--from-kit"}, "--prebuilt cannot be used with --source or --from-kit"},
		{"jobs of zero or less", []string{"--jobs", "-2"}, "--jobs must be a whole number of 1 or more"},
		{"keep that is not a number", []string{"--keep", "many"}, "invalid value"},
		{"keep that is too large", []string{"--keep", "999"}, "--keep must be a whole number from 1 to 20"},
		{"keep of less than one", []string{"--keep", "-1"}, "--keep must be a whole number from 1 to 20"},
		{"an extra argument", []string{"now"}, "Unexpected argument"},
		{"an unknown flag", []string{"--no-such-flag"}, "no-such-flag"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newLlamaFix(t)
			r := f.run(t, append([]string{"rebuild-llama", "--yes"}, tc.args...)...)
			if r.code != ExitUsage || !strings.Contains(r.errOut, "["+CodeUsage+"]") || !strings.Contains(unwrap(r.errOut), tc.in) {
				t.Fatalf("exit %d\n%s", r.code, r.errOut)
			}
			if f.made != 0 || len(f.eng.reqs) != 0 {
				t.Error("the engine was made before the flags were checked")
			}
			f.untouched()
		})
	}
}

func TestRebuildLlamaNeedsRoot(t *testing.T) {
	f := newLlamaFix(t)
	o := localOpts(nil)
	o.euid = 1000
	r := runCLI(t, DefaultRegistry, o, "rebuild-llama", "--yes")
	if r.code != ExitDenied || !strings.Contains(r.errOut, "["+CodeNeedRoot+"]") || f.made != 0 {
		t.Fatalf("exit %d\n%s", r.code, r.errOut)
	}
	r = runCLI(t, DefaultRegistry, o, "rebuild-llama", "--dry-run")
	if r.code != ExitDenied || f.made != 0 {
		t.Errorf("--dry-run needs root as well: exit %d\n%s", r.code, r.errOut)
	}
	f.untouched()
}

// ---- the confirmation ---------------------------------------------------------------------------

func TestRebuildLlamaConfirmation(t *testing.T) {
	t.Run("a person who says no changes nothing", func(t *testing.T) {
		f := newLlamaFix(t)
		r := runCLI(t, DefaultRegistry, localOpts(fake.Typed(true, "n")), "rebuild-llama")
		if r.code != ExitRefused || !strings.Contains(r.out, "Nothing was changed.") || len(f.eng.reqs) != 0 {
			t.Fatalf("exit %d\n%s\n%s builds=%d", r.code, r.out, r.errOut, len(f.eng.reqs))
		}
		if !strings.Contains(unwrap(r.errOut), "Build llama.cpp "+lShort+" for cuda and switch the model service to it?") {
			t.Errorf("prompt = %q", r.errOut)
		}
	})
	t.Run("a person who says yes builds", func(t *testing.T) {
		f := newLlamaFix(t)
		r := runCLI(t, DefaultRegistry, localOpts(fake.Typed(true, "y")), "rebuild-llama")
		if r.code != 0 || len(f.eng.reqs) != 1 {
			t.Fatalf("exit %d\n%s\n%s", r.code, r.out, r.errOut)
		}
	})
	t.Run("no terminal and no --yes asks nothing and builds nothing", func(t *testing.T) {
		f := newLlamaFix(t)
		r := f.run(t, "rebuild-llama")
		if r.code != ExitUsage || !strings.Contains(r.errOut, "["+CodeNeedConfirm+"]") || len(f.eng.reqs) != 0 {
			t.Fatalf("exit %d\n%s", r.code, r.errOut)
		}
	})
	t.Run("--json without --yes cannot ask", func(t *testing.T) {
		f := newLlamaFix(t)
		r := runCLI(t, DefaultRegistry, localOpts(fake.Typed(true, "y")), "--json", "rebuild-llama")
		if r.code != ExitUsage || len(f.eng.reqs) != 0 {
			t.Fatalf("exit %d\n%s", r.code, r.out)
		}
	})
	t.Run("--no-activate says the service is not restarted", func(t *testing.T) {
		newLlamaFix(t)
		r := runCLI(t, DefaultRegistry, localOpts(fake.Typed(true, "n")), "rebuild-llama", "--no-activate")
		if r.code != ExitRefused || !strings.Contains(unwrap(r.errOut), "the model service is not restarted") {
			t.Fatalf("exit %d\n%s", r.code, r.errOut)
		}
	})
	t.Run("the resolve error comes before the question", func(t *testing.T) {
		f := newLlamaFix(t)
		f.eng.inErr = llamaBuildErr(doctor.LLM018, build.StageResolve, "No llama.cpp commit is set. Pass --commit or set llama.commit in node.yaml.", "cause", "no_commit")
		term := fake.Typed(true, "y")
		r := runCLI(t, DefaultRegistry, localOpts(term), "rebuild-llama")
		if r.code != ExitFailed || term.Reads() != 0 || !strings.Contains(unwrap(r.errOut), "No llama.cpp commit is set") {
			t.Fatalf("exit %d reads %d\n%s", r.code, term.Reads(), r.errOut)
		}
	})
}

// ---- --dry-run ----------------------------------------------------------------------------------

func llamaDryReport(ok bool) *build.Report {
	rep := &build.Report{Commit: lCommit, Backend: "cuda", CudaArch: "75", Jobs: 5, Source: "kit-tarball", Checks: []build.Check{
		{ID: build.CheckInputs, Title: "Build inputs (commit, backend, CUDA architecture)", OK: true, Detail: "llama.cpp " + lShort + " for cuda (sm 75)"},
		{ID: build.CheckSource, Title: "Source of llama.cpp " + lShort, OK: true, Detail: "kit tarball /srv/localai/kit/src/llama.cpp-x.tar.gz (sha256 matches the kit manifest)"},
	}}
	if !ok {
		rep.Checks = append(rep.Checks, build.Check{ID: build.CheckTools, Title: "Build tools (gcc, g++, cmake 3.21 or newer, ninja, pkg-config)", Code: doctor.KIT010, Detail: "not installed: ninja, pkg-config"})
		rep.MissingTools, rep.Packages = []string{"ninja", "pkg-config"}, []string{"ninja-build", "pkg-config"}
		rep.InstallCommand = build.InstallDebsCommand(rep.Packages)
	}
	return rep
}

func TestRebuildLlamaDryRun(t *testing.T) {
	t.Run("every check is printed and a pass exits 0 without asking", func(t *testing.T) {
		f := newLlamaFix(t)
		f.eng.res = build.Result{Preflight: llamaDryReport(true), DryRun: true}
		term := fake.Typed(true, "y")
		r := runCLI(t, DefaultRegistry, localOpts(term), "rebuild-llama", "--dry-run")
		if r.code != 0 || term.Reads() != 0 {
			t.Fatalf("exit %d reads %d\n%s\n%s", r.code, term.Reads(), r.out, r.errOut)
		}
		checkGolden(t, "rebuild-llama.dry-run.en", r)
		if req := f.lastReq(t); !req.DryRun || len(f.eng.resolved) != 0 {
			t.Errorf("a dry run only asks the engine for the preflight: %+v resolved %d", req, len(f.eng.resolved))
		}
		if f.eng.opt.Exporter != nil {
			t.Error("a dry run must not set up an export")
		}
		es := runCLI(t, DefaultRegistry, localOpts(nil), "--lang", "es", "rebuild-llama", "--dry-run")
		if es.code != 0 {
			t.Fatalf("exit %d\n%s", es.code, es.errOut)
		}
		checkGolden(t, "rebuild-llama.dry-run.es", es)
	})
	t.Run("a missing tool exits 1 and prints the exact install command", func(t *testing.T) {
		f := newLlamaFix(t)
		f.eng.res = build.Result{Preflight: llamaDryReport(false), DryRun: true}
		f.eng.err = llamaBuildErr(doctor.KIT010, build.StagePreflight, "Build tools: not installed: ninja, pkg-config")
		r := f.run(t, "rebuild-llama", "--dry-run")
		if r.code != ExitFailed {
			t.Fatalf("exit %d\n%s\n%s", r.code, r.out, r.errOut)
		}
		checkGolden(t, "rebuild-llama.dry-run.fail.en", r)
		if !strings.Contains(r.out, "`sudo aictl kit install-debs --packages ninja-build,pkg-config`") {
			t.Errorf("the install command is missing:\n%s", r.out)
		}
		es := runCLI(t, DefaultRegistry, localOpts(nil), "--lang", "es", "rebuild-llama", "--dry-run")
		if es.code != ExitFailed || !strings.Contains(es.out, "`sudo aictl kit install-debs --packages ninja-build,pkg-config`") || !strings.Contains(unwrap(es.out), "No está listo") {
			t.Errorf("es: exit %d\n%s", es.code, es.out)
		}
	})
	t.Run("--json is the report", func(t *testing.T) {
		f := newLlamaFix(t)
		f.eng.res = build.Result{Preflight: llamaDryReport(true), DryRun: true}
		r := f.run(t, "--json", "rebuild-llama", "--dry-run")
		var doc struct {
			Commit string `json:"commit"`
			Checks []struct {
				ID string `json:"id"`
				OK bool   `json:"ok"`
			} `json:"checks"`
		}
		if r.code != 0 || json.Unmarshal([]byte(r.out), &doc) != nil || doc.Commit != lCommit || len(doc.Checks) != 2 || !doc.Checks[0].OK {
			t.Fatalf("exit %d: %q %q", r.code, r.out, r.errOut)
		}
	})
	t.Run("--json with a failed check is the error envelope", func(t *testing.T) {
		f := newLlamaFix(t)
		f.eng.res = build.Result{Preflight: llamaDryReport(false), DryRun: true}
		f.eng.err = llamaBuildErr(doctor.KIT010, build.StagePreflight, "Build tools: not installed: ninja, pkg-config",
			"install_command", build.InstallDebsCommand([]string{"ninja-build", "pkg-config"}))
		r := f.run(t, "--json", "rebuild-llama", "--dry-run")
		var doc struct {
			Error struct {
				Code    string
				Details map[string]any
			}
		}
		if r.code != ExitFailed || json.Unmarshal([]byte(r.out), &doc) != nil || doc.Error.Code != doctor.KIT010 || doc.Error.Details["install_command"] != "sudo aictl kit install-debs --packages ninja-build,pkg-config" {
			t.Fatalf("exit %d: %q %q", r.code, r.out, r.errOut)
		}
	})
	t.Run("an engine that cannot even check says why", func(t *testing.T) {
		f := newLlamaFix(t)
		f.eng.err = llamaBuildErr(doctor.LLM018, build.StageResolve, "No llama.cpp commit is set. Pass --commit or set llama.commit in node.yaml.", "cause", "no_commit")
		r := f.run(t, "rebuild-llama", "--dry-run")
		if r.code != ExitFailed || !strings.Contains(r.errOut, "[LAI-LLM-018]") || !strings.Contains(unwrap(r.errOut), "No llama.cpp commit is set") {
			t.Fatalf("exit %d\n%s", r.code, r.errOut)
		}
	})
	t.Run("--from-kit --dry-run asks the offline preflight", func(t *testing.T) {
		f := newLlamaFix(t)
		f.makeKit(t)
		f.eng.res = build.Result{Preflight: llamaDryReport(true), DryRun: true}
		if r := f.run(t, "rebuild-llama", "--from-kit", "--dry-run"); r.code != 0 {
			t.Fatalf("exit %d\n%s\n%s", r.code, r.out, r.errOut)
		}
		if req := f.lastReq(t); !req.DryRun || !req.FromKit || !req.Offline {
			t.Errorf("request = %+v", req)
		}
	})
}

// ---- errors: the code, the plain sentence, the fix, the exit code -------------------------------

func TestRebuildLlamaErrors(t *testing.T) {
	tests := []struct {
		name string
		err  error
		exit int
		code string
		in   []string
	}{
		{"building failed", llamaBuildErr(doctor.LLM018, build.StageBuild, "Building llama.cpp failed: cmake exited with status 1.", "log_path", "/opt/localai/llama.cpp/builds/x.tmp.failed/build.log"),
			ExitFailed, doctor.LLM018, []string{"Building llama.cpp failed", "The log is in /opt/localai/llama.cpp/builds/x.tmp.failed/build.log.", "install the missing package"}},
		{"the new build does not work and the old one is back", llamaBuildErr(doctor.LLM019, build.StageCheck, "The new llama.cpp build did not work; the previous build is still in use.",
			"cause", "health_timeout", "rolled_back", true, "previous_build", "0a1b2c3d4-cuda"),
			ExitFailed, doctor.LLM019, []string{"did not work; the previous build is still in use", "Report the build log"}},
		{"the new build does not work and the old one could not be put back", llamaBuildErr(doctor.LLM019, build.StageCheck, "The new llama.cpp build did not work, and the previous build could not be put back.",
			"cause", "load_timeout", "rolled_back", false),
			ExitFailed, doctor.LLM019, []string{"could not be put back", "sudo aictl restart llama"}},
		{"offline and no source anywhere", llamaBuildErr(doctor.KIT002, build.StageSource, "The kit and this computer do not have the llama.cpp version abc1234. Sync the kit with internet, or choose a commit that is in the kit.", "cause", "no_source"),
			ExitFailed, doctor.KIT002, []string{"do not have the llama.cpp version abc1234", "Fix: Sync the kit with internet, or choose a commit that is in the kit."}},
		{"a damaged kit source", llamaBuildErr(doctor.KIT001, build.StageSource, "A llama.cpp source package of the kit is damaged."), ExitFailed, doctor.KIT001, []string{"damaged", "aictl kit verify --full"}},
		{"the agent stayed busy", llamaBuildErr(doctor.AGT022, build.StageSwitch, "The agent is busy with another model operation; the new build was not activated.", "op_id", "op_1"),
			ExitRefused, doctor.AGT022, []string{"The agent is busy"}},
		{"no compute capability", llamaBuildErr(doctor.HW019, build.StageResolve, "The CUDA architecture of the GPU could not be detected. Set llama.cuda_arch in node.yaml and run sudo aictl rebuild-llama."), ExitFailed, doctor.HW019, []string{"could not be detected"}},
		{"build tools missing", llamaBuildErr(doctor.KIT010, build.StagePreflight, "Build tools: not installed: ninja. Fix: sudo aictl kit install-debs --packages ninja-build", "install_command", "sudo aictl kit install-debs --packages ninja-build"),
			ExitFailed, doctor.KIT010, []string{"not installed: ninja", "sudo aictl kit install-debs --packages ninja-build"}},
		{"not enough disk", llamaBuildErr(doctor.KIT014, build.StagePreflight, "Disk space: 1.2 GB free in /opt/localai, 5 GB are needed."), ExitFailed, doctor.KIT014, []string{"5 GB are needed"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newLlamaFix(t)
			f.eng.err = tc.err
			r := f.run(t, "rebuild-llama", "--yes")
			if r.code != tc.exit || !strings.Contains(r.errOut, "["+tc.code+"]") {
				t.Fatalf("exit %d\n%s", r.code, r.errOut)
			}
			for _, want := range tc.in {
				if !strings.Contains(unwrap(r.errOut), want) {
					t.Errorf("stderr lacks %q:\n%s", want, r.errOut)
				}
			}
			if strings.Contains(r.out, "passed its test") {
				t.Error("a failed build printed the success line")
			}
		})
	}
}

func TestRebuildLlamaPostSwitchFailureGolden(t *testing.T) {
	f := newLlamaFix(t)
	f.eng.err = llamaBuildErr(doctor.LLM019, build.StageCheck, "The new llama.cpp build did not work; the previous build is still in use.",
		"cause", "completion_failed", "rolled_back", true, "previous_build", "0a1b2c3d4-cuda", "log_path", "/opt/localai/llama.cpp/builds/"+lShort+"-cuda/build.log")
	r := f.run(t, "rebuild-llama", "--yes")
	if r.code != ExitFailed {
		t.Fatalf("exit %d\n%s", r.code, r.errOut)
	}
	checkGolden(t, "rebuild-llama.llm019.en", r)
	es := runCLI(t, DefaultRegistry, localOpts(nil), "--lang", "es", "rebuild-llama", "--yes")
	if es.code != ExitFailed || strings.Contains(es.errOut, "did not work") {
		t.Fatalf("es: exit %d\n%s", es.code, es.errOut)
	}
	checkGolden(t, "rebuild-llama.llm019.es", es)
}

func TestRebuildLlamaErrorInSpanishNamesTheInstallCommand(t *testing.T) {
	f := newLlamaFix(t)
	f.eng.err = llamaBuildErr(doctor.KIT010, build.StagePreflight, "Build tools: not installed: ninja.", "install_command", "sudo aictl kit install-debs --packages ninja-build")
	r := f.run(t, "--lang", "es", "rebuild-llama", "--yes")
	if r.code != ExitFailed || !strings.Contains(unwrap(r.errOut), "Faltan herramientas de compilación") || !strings.Contains(unwrap(r.errOut), "Ejecute: `sudo aictl kit install-debs --packages ninja-build`.") {
		t.Fatalf("exit %d\n%s", r.code, r.errOut)
	}
}

func TestRebuildLlamaErrorJSONCarriesTheDetails(t *testing.T) {
	f := newLlamaFix(t)
	f.eng.err = llamaBuildErr(doctor.LLM019, build.StageCheck, "The new llama.cpp build did not work; the previous build is still in use.",
		"cause", "health_timeout", "rolled_back", true, "build_id", lShort+"-cuda", "previous_build", "0a1b2c3d4-cuda", "log_path", "/x/build.log", "log_tail", "line")
	r := f.run(t, "--json", "rebuild-llama", "--yes")
	var doc struct {
		Error struct {
			Code    string
			Message string
			Details map[string]any
		}
	}
	if r.code != ExitFailed || json.Unmarshal([]byte(r.out), &doc) != nil || doc.Error.Code != doctor.LLM019 {
		t.Fatalf("exit %d: %q %q", r.code, r.out, r.errOut)
	}
	d := doc.Error.Details
	if d["cause"] != "health_timeout" || d["rolled_back"] != true || d["previous_build"] != "0a1b2c3d4-cuda" || d["log_path"] != "/x/build.log" || d["stage"] != "check" {
		t.Errorf("details = %v", d)
	}
	if _, leaked := d["log_tail"]; leaked {
		t.Error("the log tail is for the log, not for the envelope")
	}
}

// ---- offline ------------------------------------------------------------------------------------

func TestRebuildLlamaOfflineWithoutALocalSourceIsKit002(t *testing.T) {
	// The engine decides that nothing has the commit; the command asks it to stay offline.
	for _, tc := range []struct {
		name  string
		args  []string
		setup func(f *llamaFix, t *testing.T)
	}{
		{"--from-kit with a kit", []string{"--from-kit"}, func(f *llamaFix, t *testing.T) { f.makeKit(t) }},
		{"--from-kit with no kit at all", []string{"--from-kit"}, func(f *llamaFix, t *testing.T) {}},
		{"survival mode", nil, func(f *llamaFix, t *testing.T) { f.state(t, `{"mode":"survival"}`) }},
		{"offline mode", nil, func(f *llamaFix, t *testing.T) { f.state(t, `{"mode":"offline"}`) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newLlamaFix(t)
			tc.setup(f, t)
			f.eng.err = llamaBuildErr(doctor.KIT002, build.StageSource, "The kit and this computer do not have the llama.cpp version "+lShort+". Sync the kit with internet, or choose a commit that is in the kit.", "cause", "no_source")
			r := f.run(t, append([]string{"rebuild-llama", "--yes"}, tc.args...)...)
			if r.code != ExitFailed || !strings.Contains(r.errOut, "[LAI-KIT-002]") {
				t.Fatalf("exit %d\n%s", r.code, r.errOut)
			}
			if strings.Contains(r.errOut, "LAI-LLM-018") || strings.Contains(r.errOut, "LAI-KIT-040") {
				t.Errorf("only LAI-KIT-002 may be reported:\n%s", r.errOut)
			}
			if req := f.lastReq(t); !req.Offline {
				t.Errorf("the engine was not told to stay offline: %+v", req)
			}
		})
	}
}

func TestRebuildLlamaTheNodesModeDecidesWhetherGitHubMayBeUsed(t *testing.T) {
	tests := []struct {
		name        string
		state       string
		wantOffline bool
		wantInfo    string
	}{
		{"online", `{"mode":"online"}`, false, ""},
		{"offline", `{"mode":"offline"}`, true, "This computer is in offline mode: only the source on this computer and in the rebuild kit is used, never the internet."},
		{"survival", `{"mode":"survival"}`, true, "This computer is in survival mode"},
		{"unknown: no state file", "", false, ""},
		{"unknown: unreadable state file", `{not json`, false, ""},
		{"unknown: a mode that does not exist", `{"mode":"sleepy"}`, false, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newLlamaFix(t)
			if tc.state != "" {
				f.state(t, tc.state)
			}
			r := f.run(t, "rebuild-llama", "--yes")
			if r.code != 0 {
				t.Fatalf("exit %d\n%s", r.code, r.errOut)
			}
			if got := f.lastReq(t).Offline; got != tc.wantOffline {
				t.Errorf("Offline = %v, want %v", got, tc.wantOffline)
			}
			if tc.wantInfo != "" && !strings.Contains(unwrap(r.out), tc.wantInfo) {
				t.Errorf("stdout lacks %q:\n%s", tc.wantInfo, r.out)
			}
			if tc.wantInfo == "" && strings.Contains(r.out, "never the internet") {
				t.Errorf("an online node was told it is offline:\n%s", r.out)
			}
		})
	}
	t.Run("the agent's answer wins over state.json", func(t *testing.T) {
		f := newLlamaFix(t)
		f.state(t, `{"mode":"online"}`)
		rig := newMgRig(t, fake.Options{})
		rig.agent().Respond("GET", "/mode", 200, `{"mode":"survival"}`)
		got := rig.run(mgArgs("rebuild-llama", "--yes"))
		if got.code != 0 || !f.lastReq(t).Offline {
			t.Fatalf("exit %d offline=%v\n%s", got.code, f.lastReq(t).Offline, got.errOut)
		}
	})
	t.Run("--source never needs the mode", func(t *testing.T) {
		f := newLlamaFix(t)
		f.state(t, `{"mode":"survival"}`)
		if r := f.run(t, "rebuild-llama", "--yes", "--source", "/tmp/src"); r.code != 0 || f.lastReq(t).Offline {
			t.Fatalf("exit %d request %+v\n%s", r.code, f.lastReq(t), r.errOut)
		}
	})
}

// ---- the options the command gives the engine ---------------------------------------------------

func TestRebuildLlamaOptions(t *testing.T) {
	f := newLlamaFix(t)
	f.makeKit(t)
	f.nodeYAML(t, "schema_version: 1\nllama:\n  backend: cuda\n")
	if r := f.run(t, "rebuild-llama", "--yes"); r.code != 0 {
		t.Fatalf("exit %d\n%s", r.code, r.errOut)
	}
	opt := f.eng.opt

	if opt.Recipe.Name != "llama.cpp" || opt.Recipe.Service != "llama" || opt.Recipe.BuildFailedCode != doctor.LLM018 || opt.Recipe.UnusableCode != doctor.LLM019 {
		t.Errorf("recipe = %+v", opt.Recipe.Name)
	}
	if want := filepath.Join(f.host.opt, "llama.cpp"); opt.Root != want {
		t.Errorf("root = %q, want %q", opt.Root, want)
	}
	if opt.CompileUser != build.CompileAsCaller || opt.Arch != "amd64" {
		t.Errorf("compile user %q arch %q", opt.CompileUser, opt.Arch)
	}
	if got := opt.KitRoots(); !reflect.DeepEqual(got, []string{f.env.Paths.InternalRoot}) {
		t.Errorf("kit roots = %v", got)
	}
	var names []string
	for _, s := range opt.Sources {
		names = append(names, s.Name())
	}
	if want := []string{"kit-tarball", "kit-mirror", "github"}; !reflect.DeepEqual(names, want) {
		t.Errorf("sources = %v, want the kit first and the explicit online clone last: %v", names, want)
	}
	for name, ok := range map[string]bool{
		"Agent": opt.Agent != nil, "Probe": opt.Probe != nil, "Recorder": opt.Recorder != nil, "NodeConfig": opt.NodeConfig != nil,
		"Exporter": opt.Exporter != nil, "SmokeModel": opt.SmokeModel != nil, "Audit": opt.Audit != nil, "Apt": opt.Apt != nil,
		"PreviousBuild": opt.PreviousBuild != nil, "Logger": opt.Logger != nil, "Node": opt.Node != nil,
	} {
		if !ok {
			t.Errorf("the seam %s is not wired", name)
		}
	}
	if opt.Services != nil || opt.Defaults != nil || opt.ServiceCheck != nil || opt.RecordCommit != nil {
		t.Error("llama's own seams (Agent, Probe, NodeConfig) are used, not the whisper ones")
	}
	if !strings.HasPrefix(opt.BuiltBy, "aictl ") {
		t.Errorf("built_by = %q", opt.BuiltBy)
	}
}

func TestRebuildLlamaWithoutAKitHasNoKitRoots(t *testing.T) {
	f := newLlamaFix(t)
	if r := f.run(t, "rebuild-llama", "--yes"); r.code != 0 {
		t.Fatalf("exit %d\n%s", r.code, r.errOut)
	}
	var names []string
	for _, s := range f.eng.opt.Sources {
		names = append(names, s.Name())
	}
	if len(f.eng.opt.KitRoots()) != 0 || !reflect.DeepEqual(names, []string{"kit-tarball", "github"}) {
		t.Errorf("kit roots %v sources %v", f.eng.opt.KitRoots(), names)
	}
}

func TestRebuildLlamaRecordsTheCommitInNodeYAML(t *testing.T) {
	f := newLlamaFix(t)
	f.nodeYAML(t, "# the node\nschema_version: 1\nnode:\n  id: ai-n3\nprofile: small-8gb\nllama:\n  backend: cpu # keep this comment\n  commit: \"0a1b2c3d4\"\n")
	if r := f.run(t, "rebuild-llama", "--yes"); r.code != 0 {
		t.Fatalf("exit %d\n%s", r.code, r.errOut)
	}
	if err := f.eng.opt.NodeConfig.RecordLlamaCommit(lCommit); err != nil {
		t.Fatal(err)
	}
	got := readFile(t, f.env.Paths.NodeYAML())
	for _, want := range []string{"# the node", "# keep this comment", "backend: cpu", lCommit, "id: ai-n3"} {
		if !strings.Contains(got, want) {
			t.Errorf("node.yaml lacks %q:\n%s", want, got)
		}
	}
	if readLlamaSettings(f.env).Llama.Commit != lCommit {
		t.Error("the new commit was not read back")
	}
	// A node.yaml that would become invalid is refused and left alone.
	f.nodeYAML(t, "schema_version: 1\nports:\n  agent: 1\n  llama: 1\n")
	before := readFile(t, f.env.Paths.NodeYAML())
	if err := f.eng.opt.NodeConfig.RecordLlamaCommit(lCommit); err == nil {
		t.Error("an invalid node.yaml was rewritten")
	}
	if readFile(t, f.env.Paths.NodeYAML()) != before {
		t.Error("node.yaml changed")
	}
}

func TestRebuildLlamaAuditsThroughTheLocalLog(t *testing.T) {
	f := newLlamaFix(t)
	if r := f.run(t, "rebuild-llama", "--yes"); r.code != 0 {
		t.Fatalf("exit %d\n%s", r.code, r.errOut)
	}
	w := f.eng.opt.Audit
	if err := w.Write(audit.Event{Action: audit.ActionLlamaBuildSwitch, Result: audit.ResultOK, Target: map[string]string{"build": lShort + "-cuda"}}); err != nil {
		t.Fatal(err)
	}
	lines := f.auditLines()
	if len(lines) != 1 || lines[0]["action"] != "llama.build.switch" || lines[0]["result"] != "ok" {
		t.Fatalf("audit = %v", lines)
	}
	if actor, _ := lines[0]["actor"].(map[string]any); actor["kind"] != "cli-root" {
		t.Errorf("actor = %v", lines[0]["actor"])
	}
	// A rebuild from scratch has no state folder yet: nothing to append to, and no failure.
	if err := os.RemoveAll(f.env.Paths.StateDir); err != nil {
		t.Fatal(err)
	}
	if err := w.Write(audit.Event{Action: audit.ActionLlamaBuildSwitch, Result: audit.ResultOK}); err != nil {
		t.Errorf("a node without a state folder must not fail: %v", err)
	}
}

func TestRebuildLlamaExportsOnlyWhenItBuilds(t *testing.T) {
	f := newLlamaFix(t)
	if r := f.run(t, "rebuild-llama", "--yes"); r.code != 0 || f.eng.opt.Exporter == nil {
		t.Fatalf("exit %d exporter %v\n%s", r.code, f.eng.opt.Exporter, r.errOut)
	}
	// An unknown Ubuntu release cannot name the file: no export, and no failure.
	f2 := newLlamaFix(t)
	_ = os.WriteFile(f2.host.osr, []byte("NAME=Ubuntu\n"), 0o644)
	if r := f2.run(t, "rebuild-llama", "--yes"); r.code != 0 || f2.eng.opt.Exporter != nil {
		t.Fatalf("exit %d exporter %v\n%s", r.code, f2.eng.opt.Exporter, r.errOut)
	}
}

// ---- Ctrl-C -------------------------------------------------------------------------------------

// answeredTerm tells when the person has answered a question.
type answeredTerm struct {
	Terminal
	once     sync.Once
	answered chan struct{}
}

func (t *answeredTerm) ReadLine(ctx context.Context) (string, error) {
	s, err := t.Terminal.ReadLine(ctx)
	t.once.Do(func() { close(t.answered) })
	return s, err
}

func TestRebuildLlamaCtrlC(t *testing.T) {
	t.Run("yes stops the build and leaves current alone (exit 130)", func(t *testing.T) {
		f := newLlamaFix(t)
		var seen error
		f.eng.err = llamaBuildErr(doctor.LLM019, build.StageBuild, "cancelled", "cause", "cancelled")
		f.eng.onBuild = func(ctx context.Context, _ build.Options) {
			f.sig <- struct{}{}
			<-ctx.Done() // returns only when the person said yes and the guard cancelled the build
			seen = ctx.Err()
		}
		term := fake.Typed(true, "y")
		r := runCLI(t, DefaultRegistry, localOpts(term), "rebuild-llama", "--yes")
		if r.code != ExitInterrupted || !errors.Is(seen, context.Canceled) {
			t.Fatalf("exit %d ctx %v\n%s\n%s", r.code, seen, r.out, r.errOut)
		}
		if !strings.Contains(unwrap(r.errOut), "Stop the build? [y/N]") || !strings.Contains(unwrap(r.out), "The build was stopped. Nothing was changed: the build in use is the same as before.") {
			t.Errorf("stdout %q stderr %q", r.out, r.errOut)
		}
		if strings.Contains(r.errOut, "LAI-LLM-019") || strings.Contains(r.out, "passed its test") {
			t.Error("a stopped build is not a failed one")
		}
	})
	t.Run("no keeps building and the build finishes normally", func(t *testing.T) {
		f := newLlamaFix(t)
		var seen error
		answered := make(chan struct{})
		f.eng.onBuild = func(ctx context.Context, _ build.Options) {
			f.sig <- struct{}{}
			<-answered
			seen = ctx.Err()
		}
		term := &answeredTerm{Terminal: fake.Typed(true, "n"), answered: answered}
		r := runCLI(t, DefaultRegistry, localOpts(term), "rebuild-llama", "--yes")
		if r.code != 0 || seen != nil {
			t.Fatalf("exit %d ctx %v\n%s\n%s", r.code, seen, r.out, r.errOut)
		}
		if !strings.Contains(unwrap(r.errOut), "Stop the build? [y/N]") || !strings.Contains(unwrap(r.errOut), "Carrying on with the build.") || !strings.Contains(r.out, "Switched to the new build") {
			t.Errorf("stdout %q stderr %q", r.out, r.errOut)
		}
	})
	t.Run("Enter alone is no", func(t *testing.T) {
		f := newLlamaFix(t)
		answered := make(chan struct{})
		f.eng.onBuild = func(ctx context.Context, _ build.Options) {
			f.sig <- struct{}{}
			<-answered
		}
		term := &answeredTerm{Terminal: fake.Typed(true, ""), answered: answered}
		if r := runCLI(t, DefaultRegistry, localOpts(term), "rebuild-llama", "--yes"); r.code != 0 {
			t.Fatalf("exit %d\n%s\n%s", r.code, r.out, r.errOut)
		}
	})
	t.Run("Spanish asks in Spanish and accepts si", func(t *testing.T) {
		f := newLlamaFix(t)
		f.eng.err = llamaBuildErr(doctor.LLM019, build.StageBuild, "cancelled")
		f.eng.onBuild = func(ctx context.Context, _ build.Options) { f.sig <- struct{}{}; <-ctx.Done() }
		r := runCLI(t, DefaultRegistry, localOpts(fake.Typed(true, "sí")), "--lang", "es", "rebuild-llama", "--yes")
		if r.code != ExitInterrupted || !strings.Contains(unwrap(r.errOut), "¿Detener la compilación? [s/N]") || !strings.Contains(unwrap(r.out), "Se detuvo la compilación.") {
			t.Fatalf("exit %d\n%s\n%s", r.code, r.out, r.errOut)
		}
	})
	t.Run("without a terminal the signal stops the build at once", func(t *testing.T) {
		f := newLlamaFix(t)
		f.eng.err = llamaBuildErr(doctor.LLM019, build.StageBuild, "cancelled")
		f.eng.onBuild = func(ctx context.Context, _ build.Options) { f.sig <- struct{}{}; <-ctx.Done() }
		r := f.run(t, "rebuild-llama", "--yes")
		if r.code != ExitInterrupted || !strings.Contains(unwrap(r.out), "The build was stopped.") {
			t.Fatalf("exit %d\n%s\n%s", r.code, r.out, r.errOut)
		}
	})
	t.Run("--json: the envelope names the interruption", func(t *testing.T) {
		f := newLlamaFix(t)
		f.eng.err = llamaBuildErr(doctor.LLM019, build.StageBuild, "cancelled")
		f.eng.onBuild = func(ctx context.Context, _ build.Options) { f.sig <- struct{}{}; <-ctx.Done() }
		r := f.run(t, "--json", "rebuild-llama", "--yes")
		var doc struct{ Error struct{ Code string } }
		if r.code != ExitInterrupted || json.Unmarshal([]byte(r.out), &doc) != nil || doc.Error.Code != CodeInterrupted {
			t.Fatalf("exit %d: %q %q", r.code, r.out, r.errOut)
		}
	})
	t.Run("a build that finished anyway is a success", func(t *testing.T) {
		f := newLlamaFix(t)
		f.eng.onBuild = func(ctx context.Context, _ build.Options) { f.sig <- struct{}{}; <-ctx.Done() }
		f.eng.err = nil
		r := f.run(t, "rebuild-llama", "--yes")
		if r.code != 0 || !strings.Contains(r.out, "Switched to the new build") {
			t.Fatalf("exit %d\n%s\n%s", r.code, r.out, r.errOut)
		}
	})
	t.Run("the build keeps the command's deadline, not the Ctrl-C of the process", func(t *testing.T) {
		f := newLlamaFix(t)
		var hasDeadline bool
		f.eng.onBuild = func(ctx context.Context, _ build.Options) { _, hasDeadline = ctx.Deadline() }
		if r := f.run(t, "rebuild-llama", "--yes"); r.code != 0 || !hasDeadline {
			t.Fatalf("exit %d deadline %v", r.code, hasDeadline)
		}
	})
}

// ---- the heartbeat ------------------------------------------------------------------------------

func TestRebuildLlamaHeartbeat(t *testing.T) {
	base := time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)
	step := func() func() time.Time {
		n := 0
		return func() time.Time { n++; return base.Add(time.Duration(n-1) * 5 * time.Minute) }
	}
	t.Run("a long build says it is still working", func(t *testing.T) {
		f := newLlamaFix(t)
		llamaClock = step()
		f.eng.onBuild = func(ctx context.Context, _ build.Options) { f.ticks <- time.Time{} }
		r := f.run(t, "rebuild-llama", "--yes")
		if r.code != 0 || !strings.Contains(unwrap(r.errOut), "… Still working on the build (5 min so far)") {
			t.Fatalf("exit %d\n%s", r.code, r.errOut)
		}
	})
	t.Run("in Spanish", func(t *testing.T) {
		f := newLlamaFix(t)
		llamaClock = step()
		f.eng.onBuild = func(ctx context.Context, _ build.Options) { f.ticks <- time.Time{} }
		r := f.run(t, "--lang", "es", "rebuild-llama", "--yes")
		if !strings.Contains(unwrap(r.errOut), "… Sigue la compilación (5 min hasta ahora)") {
			t.Fatalf("exit %d\n%s", r.code, r.errOut)
		}
	})
	t.Run("-v shows the last line of the build log", func(t *testing.T) {
		f := newLlamaFix(t)
		llamaClock = step()
		dir := filepath.Join(f.host.opt, "llama.cpp", "builds", lShort+"-cuda.tmp")
		kitWrite(t, filepath.Join(dir, "build.log"), "first line\n[512/900] Building CXX object ggml/src/ggml-cuda/mmq.cu.o\n\n")
		f.eng.onBuild = func(ctx context.Context, _ build.Options) { f.ticks <- time.Time{} }
		r := f.run(t, "-v", "rebuild-llama", "--yes")
		if !strings.Contains(unwrap(r.errOut), "… Build log: [512/900] Building CXX object ggml/src/ggml-cuda/mmq.cu.o") {
			t.Fatalf("exit %d\n%s", r.code, r.errOut)
		}
	})
	t.Run("without -v the log is not shown", func(t *testing.T) {
		f := newLlamaFix(t)
		llamaClock = step()
		kitWrite(t, filepath.Join(f.host.opt, "llama.cpp", "builds", "x.tmp", "build.log"), "secret-looking noise\n")
		f.eng.onBuild = func(ctx context.Context, _ build.Options) { f.ticks <- time.Time{} }
		if r := f.run(t, "rebuild-llama", "--yes"); strings.Contains(r.errOut, "noise") {
			t.Fatalf("the log is shown without -v:\n%s", r.errOut)
		}
	})
	t.Run("--json and --quiet print nothing", func(t *testing.T) {
		f := newLlamaFix(t)
		llamaClock = step()
		f.eng.onBuild = func(ctx context.Context, _ build.Options) {}
		if r := f.run(t, "--json", "rebuild-llama", "--yes"); r.errOut != "" {
			t.Fatalf("stderr %q", r.errOut)
		}
		if r := f.run(t, "-q", "rebuild-llama", "--yes"); strings.Contains(r.errOut, "Still working") {
			t.Fatalf("stderr %q", r.errOut)
		}
	})
}
