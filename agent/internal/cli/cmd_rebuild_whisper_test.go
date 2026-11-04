package cli

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/audit"
	"github.com/marinoscar/local-ai/agent/internal/cli/fake"
	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/router/build"
	"github.com/marinoscar/local-ai/agent/internal/sync/manifest"
)

// Tests of `aictl rebuild-whisper` (SVC-05, aictl.md section 4.23a). The engine is replaced by a fake
// that records its options and requests (its own behaviour is covered in internal/router/build); the
// node is a temporary directory.

// commit9 and wCommit are a short and a full commit built at run time.
const wShort = "1a2b3c4d5"

var wCommit = wShort + strings.Repeat("0", 31)

// fakeWhisperEngine is the engine the command talks to.
type fakeWhisperEngine struct {
	mu       sync.Mutex
	opt      build.Options
	in       build.Inputs
	inErr    error
	res      build.Result
	err      error
	progress []build.Progress
	onBuild  func(ctx context.Context, opt build.Options)
	reqs     []build.Request
	resolved []build.Request
}

func (e *fakeWhisperEngine) Resolve(_ context.Context, req build.Request) (build.Inputs, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.resolved = append(e.resolved, req)
	return e.in, e.inErr
}

func (e *fakeWhisperEngine) Build(ctx context.Context, req build.Request) (build.Result, error) {
	e.mu.Lock()
	e.reqs = append(e.reqs, req)
	hook, progress := e.onBuild, e.progress
	e.mu.Unlock()
	for _, p := range progress {
		if req.Progress != nil {
			req.Progress(p)
		}
	}
	if hook != nil {
		hook(ctx, e.opt)
	}
	return e.res, e.err
}

type whisperFix struct {
	*localFixture
	eng  *fakeWhisperEngine
	host *hostFix
	made int
	// probed are the URLs the speech probe was asked.
	probed   []string
	probeErr error
}

func newWhisperFix(t *testing.T) *whisperFix {
	t.Helper()
	f := &whisperFix{localFixture: newLocalFixture(t)}
	f.host = setKitHost(t, "noble", "amd64") // the computer's Ubuntu release and processor, for the exporter and the ready-made builds
	f.eng = &fakeWhisperEngine{
		in:  build.Inputs{Commit: wCommit, ShortCommit: wShort, Backend: "cuda", CudaArch: "75", Jobs: 5, Activate: true},
		res: build.Result{BuildID: wShort + "-cuda", Switched: true, PreviousBuild: "0a1b2c3d4-cuda"},
	}
	oldNew, oldUser, oldProbe := whisperNewEngine, whisperCompileUser, whisperProbeFn
	whisperNewEngine = func(opt build.Options) whisperEngine {
		f.made++
		f.eng.opt = opt
		return f.eng
	}
	whisperCompileUser = build.CompileAsCaller
	whisperProbeFn = func(_ context.Context, u string) error {
		f.probed = append(f.probed, u)
		return f.probeErr
	}
	t.Cleanup(func() { whisperNewEngine, whisperCompileUser, whisperProbeFn = oldNew, oldUser, oldProbe })
	return f
}

func (f *whisperFix) run(t *testing.T, args ...string) runResult {
	t.Helper()
	return runCLI(t, DefaultRegistry, localOpts(nil), args...)
}

func (f *whisperFix) nodeYAML(t *testing.T, s string) {
	t.Helper()
	if err := os.WriteFile(f.env.Paths.NodeYAML(), []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (f *whisperFix) lastReq(t *testing.T) build.Request {
	t.Helper()
	if len(f.eng.reqs) == 0 {
		t.Fatal("the engine was not asked to build")
	}
	return f.eng.reqs[len(f.eng.reqs)-1]
}

// ---- the happy path and its words ---------------------------------------------------------------

func TestRebuildWhisperSuccessLine(t *testing.T) {
	f := newWhisperFix(t)
	f.eng.progress = []build.Progress{{Stage: build.StageConfigure, Message: "Building whisper.cpp 1a2b3c4d5 for cuda (sm 75), 5 parallel jobs"}, {Stage: build.StageBuild, Message: "Compiling (this takes a while)"}}
	r := f.run(t, "rebuild-whisper", "--yes")
	if r.code != 0 {
		t.Fatalf("exit %d\n%s\n%s", r.code, r.out, r.errOut)
	}
	want := "✓ Build 1a2b3c4d5-cuda of whisper.cpp passed its test. Speech-to-text is running again."
	if !strings.Contains(unwrap(r.out), want) {
		t.Errorf("stdout %q lacks %q", r.out, want)
	}
	if !strings.Contains(unwrap(r.out), "Building whisper.cpp 1a2b3c4d5 for cuda (sm 75), 5 parallel jobs") {
		t.Errorf("the plan line is missing: %q", r.out)
	}
	if !strings.Contains(r.errOut, "… Compiling (this takes a while)") {
		t.Errorf("progress goes to stderr: %q", r.errOut)
	}
	req := f.lastReq(t)
	if req.Commit != "" || req.Backend != "auto" || req.FromKit || req.Offline || req.DryRun || req.NoActivate || req.SourceDir != "" {
		t.Errorf("request = %+v", req)
	}
	if f.made != 1 {
		t.Errorf("engines made = %d", f.made)
	}
}

func TestRebuildWhisperSpanish(t *testing.T) {
	f := newWhisperFix(t)
	r := f.run(t, "--lang", "es", "rebuild-whisper", "--yes")
	if r.code != 0 || !strings.Contains(unwrap(r.out), "La versión 1a2b3c4d5-cuda de whisper.cpp superó su prueba. El reconocimiento de voz está en marcha otra vez.") {
		t.Fatalf("exit %d\n%s\n%s", r.code, r.out, r.errOut)
	}
}

func TestRebuildWhisperPassesItsFlagsToTheEngine(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want func(r build.Request) bool
	}{
		{"commit and backend", []string{"--commit", "ABCDEF1", "--backend", "cpu"}, func(r build.Request) bool { return r.Commit == "abcdef1" && r.Backend == "cpu" }},
		{"from the kit means offline too", []string{"--from-kit"}, func(r build.Request) bool { return r.FromKit && r.Offline && r.SourceDir == "" }},
		{"a source folder", []string{"--source", "/tmp/whisper-src"}, func(r build.Request) bool { return r.SourceDir == "/tmp/whisper-src" && !r.FromKit && !r.Offline }},
		{"no activation", []string{"--no-activate"}, func(r build.Request) bool { return r.NoActivate }},
		{"the default activates", []string{"--activate"}, func(r build.Request) bool { return !r.NoActivate }},
		{"auto", []string{"--backend", "auto"}, func(r build.Request) bool { return r.Backend == "auto" }},
		{"-y is --yes", []string{"-y"}, func(r build.Request) bool { return true }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newWhisperFix(t)
			args := append([]string{"rebuild-whisper"}, tc.args...)
			if !contains(tc.args, "-y") {
				args = append(args, "--yes")
			}
			// the kit root has to exist for --from-kit
			if err := os.MkdirAll(filepath.Join(f.env.Paths.InternalRoot, "kit"), 0o755); err != nil {
				t.Fatal(err)
			}
			if r := f.run(t, args...); r.code != 0 {
				t.Fatalf("exit %d\n%s\n%s", r.code, r.out, r.errOut)
			}
			if got := f.lastReq(t); !tc.want(got) {
				t.Errorf("request = %+v", got)
			}
		})
	}
}

func TestRebuildWhisperNoActivateSaysItWasNotSwitchedOn(t *testing.T) {
	f := newWhisperFix(t)
	f.eng.res.Switched = false
	r := f.run(t, "rebuild-whisper", "--no-activate", "--yes")
	if r.code != 0 || !strings.Contains(unwrap(r.out), "Build 1a2b3c4d5-cuda of whisper.cpp passed its test. It was not switched on (--no-activate)") ||
		strings.Contains(r.out, "running again") {
		t.Fatalf("exit %d\n%s\n%s", r.code, r.out, r.errOut)
	}
}

func TestRebuildWhisperWarningsGoToStderrAndExitStaysZero(t *testing.T) {
	f := newWhisperFix(t)
	f.eng.res.Warnings = []string{"The new build was not asked to transcribe a sample, so only its --help was tested: no speech model is available to test it with."}
	r := f.run(t, "rebuild-whisper", "--yes")
	if r.code != 0 || !strings.Contains(unwrap(r.errOut), "! The new build was not asked to transcribe a sample") {
		t.Fatalf("exit %d\n%s\n%s", r.code, r.out, r.errOut)
	}
}

func TestRebuildWhisperJSONIsTheResultDocument(t *testing.T) {
	f := newWhisperFix(t)
	f.eng.res.BuildJSON = build.BuildInfo{BuildID: wShort + "-cuda", Commit: wCommit, Backend: "cuda", SHA256: map[string]string{}}
	r := f.run(t, "--json", "rebuild-whisper", "--yes")
	if r.code != 0 || r.errOut != "" {
		t.Fatalf("exit %d\n%s\n%s", r.code, r.out, r.errOut)
	}
	var doc struct {
		BuildID       string `json:"build_id"`
		Switched      bool   `json:"switched"`
		PreviousBuild string `json:"previous_build"`
		BuildJSON     struct {
			Backend string `json:"backend"`
		} `json:"build_json"`
	}
	if err := json.Unmarshal([]byte(r.out), &doc); err != nil || doc.BuildID != wShort+"-cuda" || !doc.Switched || doc.PreviousBuild != "0a1b2c3d4-cuda" || doc.BuildJSON.Backend != "cuda" {
		t.Fatalf("%v: %q", err, r.out)
	}
}

// ---- the confirmation ---------------------------------------------------------------------------

func TestRebuildWhisperConfirmation(t *testing.T) {
	t.Run("a person who says no changes nothing", func(t *testing.T) {
		f := newWhisperFix(t)
		r := runCLI(t, DefaultRegistry, localOpts(fake.Typed(true, "n")), "rebuild-whisper")
		if r.code != ExitRefused || !strings.Contains(r.out, "Nothing was changed.") || len(f.eng.reqs) != 0 {
			t.Fatalf("exit %d\n%s\n%s builds=%d", r.code, r.out, r.errOut, len(f.eng.reqs))
		}
		if !strings.Contains(unwrap(r.errOut), "Build whisper.cpp 1a2b3c4d5 for cuda and switch the speech service to it?") {
			t.Errorf("prompt = %q", r.errOut)
		}
	})
	t.Run("a person who says yes builds", func(t *testing.T) {
		f := newWhisperFix(t)
		r := runCLI(t, DefaultRegistry, localOpts(fake.Typed(true, "y")), "rebuild-whisper")
		if r.code != 0 || len(f.eng.reqs) != 1 {
			t.Fatalf("exit %d\n%s\n%s", r.code, r.out, r.errOut)
		}
	})
	t.Run("no terminal and no --yes asks nothing and builds nothing", func(t *testing.T) {
		f := newWhisperFix(t)
		r := f.run(t, "rebuild-whisper")
		if r.code != ExitUsage || !strings.Contains(r.errOut, "["+CodeNeedConfirm+"]") || len(f.eng.reqs) != 0 {
			t.Fatalf("exit %d\n%s", r.code, r.errOut)
		}
	})
	t.Run("--no-activate says the service is not restarted", func(t *testing.T) {
		f := newWhisperFix(t)
		r := runCLI(t, DefaultRegistry, localOpts(fake.Typed(true, "n")), "rebuild-whisper", "--no-activate")
		if r.code != ExitRefused || !strings.Contains(unwrap(r.errOut), "the speech service is not restarted") {
			t.Fatalf("exit %d\n%s", r.code, r.errOut)
		}
		_ = f
	})
}

// ---- usage errors come before anything is touched -----------------------------------------------

func TestRebuildWhisperUsageErrors(t *testing.T) {
	tests := []struct {
		name string
		args []string
		in   string // a part of the message
	}{
		{"a bad commit", []string{"--commit", "xyz"}, "7 to 40 lowercase hex"},
		{"a commit that is too short", []string{"--commit", "abc12"}, "7 to 40 lowercase hex"},
		{"a bad backend", []string{"--backend", "gpu"}, "not one of auto, cuda, vulkan or cpu"},
		{"source and kit", []string{"--source", "/x", "--from-kit"}, "--source and --from-kit cannot be used together"},
		{"activate and no-activate", []string{"--activate", "--no-activate"}, "--activate and --no-activate cannot be used together"},
		{"an extra argument", []string{"now"}, "Unexpected argument"},
		{"an unknown flag", []string{"--no-such-flag"}, "no-such-flag"},
		{"prebuilt with a source", []string{"--prebuilt", "x.tar.gz", "--from-kit"}, "--prebuilt cannot be used with --source or --from-kit"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newWhisperFix(t)
			r := f.run(t, append([]string{"rebuild-whisper", "--yes"}, tc.args...)...)
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

func TestRebuildWhisperNeedsRoot(t *testing.T) {
	f := newWhisperFix(t)
	o := localOpts(nil)
	o.euid = 1000
	r := runCLI(t, DefaultRegistry, o, "rebuild-whisper", "--yes")
	if r.code != ExitDenied || !strings.Contains(r.errOut, "["+CodeNeedRoot+"]") || f.made != 0 {
		t.Fatalf("exit %d\n%s", r.code, r.errOut)
	}
	f.untouched()
}

func TestRebuildWhisperFromKitWithoutAKitIsKit040(t *testing.T) {
	f := newWhisperFix(t)
	r := f.run(t, "rebuild-whisper", "--from-kit", "--yes")
	if r.code != ExitNotFound || !strings.Contains(r.errOut, "[LAI-KIT-040]") || f.made != 0 {
		t.Fatalf("exit %d\n%s", r.code, r.errOut)
	}
	// Without --from-kit a node with no kit can still build (online, or from --source).
	if r := f.run(t, "rebuild-whisper", "--yes"); r.code != 0 {
		t.Fatalf("exit %d\n%s", r.code, r.errOut)
	}
}

// ---- --dry-run ----------------------------------------------------------------------------------

func dryReport(ok bool) *build.Report {
	rep := &build.Report{Commit: wCommit, Backend: "cuda", CudaArch: "75", Jobs: 5, Source: "kit-tarball", Checks: []build.Check{
		{ID: build.CheckInputs, Title: "Build inputs (commit, backend, CUDA architecture)", OK: true, Detail: "whisper.cpp 1a2b3c4d5 for cuda (sm 75)"},
		{ID: build.CheckSource, Title: "Source of whisper.cpp 1a2b3c4d5", OK: true, Detail: "kit tarball /srv/localai/kit/src/whisper.cpp-x.tar.gz (sha256 matches the kit manifest)"},
	}}
	if !ok {
		rep.Checks = append(rep.Checks, build.Check{ID: build.CheckTools, Title: "Build tools (gcc, g++, cmake 3.21 or newer, ninja, pkg-config)", Code: doctor.KIT010, Detail: "not installed: ninja"})
		rep.MissingTools, rep.Packages = []string{"ninja"}, []string{"ninja-build"}
		rep.InstallCommand = "sudo aictl kit install-debs --packages ninja-build"
	}
	return rep
}

func TestRebuildWhisperDryRun(t *testing.T) {
	t.Run("every check is printed and a pass exits 0 without asking", func(t *testing.T) {
		f := newWhisperFix(t)
		f.eng.res = build.Result{Preflight: dryReport(true), DryRun: true}
		r := f.run(t, "rebuild-whisper", "--dry-run")
		if r.code != 0 {
			t.Fatalf("exit %d\n%s\n%s", r.code, r.out, r.errOut)
		}
		out := unwrap(r.out)
		for _, want := range []string{
			"✓ Build inputs (commit, backend, CUDA architecture): whisper.cpp 1a2b3c4d5 for cuda (sm 75)",
			"✓ Source of whisper.cpp 1a2b3c4d5: kit tarball",
			"✓ Everything needed to rebuild whisper.cpp is in place.",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("stdout lacks %q:\n%s", want, r.out)
			}
		}
		if !f.lastReq(t).DryRun || len(f.eng.resolved) != 0 {
			t.Errorf("a dry run only asks the engine for the preflight: %+v resolved %d", f.lastReq(t), len(f.eng.resolved))
		}
	})
	t.Run("a failed check exits 1 and says how to fix it", func(t *testing.T) {
		f := newWhisperFix(t)
		f.eng.res = build.Result{Preflight: dryReport(false), DryRun: true}
		f.eng.err = &build.Error{Code: doctor.KIT010, Stage: build.StagePreflight, Message: "Build tools: not installed: ninja", Details: map[string]any{}}
		r := f.run(t, "rebuild-whisper", "--dry-run")
		if r.code != ExitFailed {
			t.Fatalf("exit %d\n%s\n%s", r.code, r.out, r.errOut)
		}
		out := unwrap(r.out)
		for _, want := range []string{
			"✓ Build inputs", "✗ Build tools (gcc, g++, cmake 3.21 or newer, ninja, pkg-config): not installed: ninja.  [LAI-KIT-010]",
			"→ Fix: Run `sudo aictl kit install-debs`.",
			"`sudo aictl kit install-debs --packages ninja-build`",
			"Not ready: fix the checks marked above",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("stdout lacks %q:\n%s", want, r.out)
			}
		}
	})
	t.Run("--json is the report", func(t *testing.T) {
		f := newWhisperFix(t)
		f.eng.res = build.Result{Preflight: dryReport(true), DryRun: true}
		r := f.run(t, "--json", "rebuild-whisper", "--dry-run")
		var doc struct {
			Commit string `json:"commit"`
			Checks []struct {
				ID string `json:"id"`
				OK bool   `json:"ok"`
			} `json:"checks"`
		}
		if r.code != 0 || json.Unmarshal([]byte(r.out), &doc) != nil || doc.Commit != wCommit || len(doc.Checks) != 2 || !doc.Checks[0].OK {
			t.Fatalf("exit %d: %q %q", r.code, r.out, r.errOut)
		}
	})
	t.Run("--json with a failed check is the error envelope", func(t *testing.T) {
		f := newWhisperFix(t)
		f.eng.res = build.Result{Preflight: dryReport(false), DryRun: true}
		f.eng.err = &build.Error{Code: doctor.KIT010, Stage: build.StagePreflight, Message: "Build tools: not installed: ninja", Details: map[string]any{"install_command": "sudo aictl kit install-debs --packages ninja-build"}}
		r := f.run(t, "--json", "rebuild-whisper", "--dry-run")
		var doc struct {
			Error struct {
				Code    string
				Details map[string]any
			}
		}
		if r.code != ExitFailed || json.Unmarshal([]byte(r.out), &doc) != nil || doc.Error.Code != doctor.KIT010 || doc.Error.Details["install_command"] == nil {
			t.Fatalf("exit %d: %q %q", r.code, r.out, r.errOut)
		}
	})
	t.Run("an engine that cannot even check says why", func(t *testing.T) {
		f := newWhisperFix(t)
		f.eng.res = build.Result{}
		f.eng.err = &build.Error{Code: doctor.KIT020, Stage: build.StageResolve, Message: "No whisper.cpp commit is set. Pass --commit or set whisper.commit in node.yaml.", Details: map[string]any{}}
		r := f.run(t, "rebuild-whisper", "--dry-run")
		if r.code != ExitFailed || !strings.Contains(r.errOut, "[LAI-KIT-020]") || !strings.Contains(unwrap(r.errOut), "No whisper.cpp commit is set") {
			t.Fatalf("exit %d\n%s", r.code, r.errOut)
		}
	})
}

// ---- errors: the code, the plain sentence, the fix, the exit code -------------------------------

func TestRebuildWhisperErrors(t *testing.T) {
	be := func(code string, stage build.Stage, msg string, kv ...any) error {
		e := &build.Error{Code: code, Stage: stage, Message: msg, Details: map[string]any{"stage": string(stage)}}
		for i := 0; i+1 < len(kv); i += 2 {
			e.Details[kv[i].(string)] = kv[i+1]
		}
		return e
	}
	tests := []struct {
		name string
		err  error
		exit int
		code string
		in   []string
	}{
		{"the compile failed", be(doctor.KIT020, build.StageBuild, "Building whisper.cpp failed: cmake exited with status 1.", "log_path", "/opt/localai/whisper.cpp/builds/x.tmp.failed/build.log"),
			ExitFailed, doctor.KIT020, []string{"Building whisper.cpp failed", "The log is in /opt/localai/whisper.cpp/builds/x.tmp.failed/build.log.", "run `sudo aictl rebuild-whisper --dry-run`"}},
		{"the new build does not work", be(doctor.KIT021, build.StageSmoke, "The new whisper.cpp build did not start: whisper-cli --help exited with status 127.", "cause", "smoke_exit"),
			ExitFailed, doctor.KIT021, []string{"did not start", "Nothing breaks."}},
		{"the commit is not in the kit", be(doctor.KIT002, build.StageSource, "The kit and this computer do not have the whisper.cpp version abc1234. Sync the kit with internet, or choose a commit that is in the kit."),
			ExitFailed, doctor.KIT002, []string{"do not have the whisper.cpp version abc1234", "Fix: Sync the kit with internet, or choose a commit that is in the kit."}},
		{"a damaged kit source", be(doctor.KIT001, build.StageSource, "A whisper.cpp source package of the kit is damaged."), ExitFailed, doctor.KIT001, []string{"damaged", "aictl kit verify --full"}},
		{"the agent is busy", be(doctor.AGT022, build.StageSwitch, "The agent is busy with another model operation; the new build was not activated."),
			ExitRefused, doctor.AGT022, []string{"The agent is busy"}},
		{"no compute capability", be(doctor.HW019, build.StageResolve, "The CUDA architecture of the GPU could not be detected."), ExitFailed, doctor.HW019, []string{"could not be detected"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newWhisperFix(t)
			f.eng.err = tc.err
			r := f.run(t, "rebuild-whisper", "--yes")
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

func TestRebuildWhisperErrorInSpanishUsesTheCodesOwnWords(t *testing.T) {
	f := newWhisperFix(t)
	f.eng.err = &build.Error{Code: doctor.KIT021, Stage: build.StageSmoke, Message: "The new whisper.cpp build did not start.", Details: map[string]any{"log_path": "/x/build.log"}}
	r := f.run(t, "--lang", "es", "rebuild-whisper", "--yes")
	if r.code != ExitFailed || !strings.Contains(unwrap(r.errOut), "La nueva compilación no funciona; se sigue usando la anterior") || strings.Contains(r.errOut, "did not start") ||
		!strings.Contains(unwrap(r.errOut), "El registro está en /x/build.log.") {
		t.Fatalf("exit %d\n%s", r.code, r.errOut)
	}
}

func TestRebuildWhisperErrorJSONCarriesTheDetails(t *testing.T) {
	f := newWhisperFix(t)
	f.eng.err = &build.Error{Code: doctor.KIT021, Stage: build.StageCheck, Message: "The new whisper.cpp build did not work; the previous build is still in use.",
		Details: map[string]any{"cause": "health_timeout", "rolled_back": true, "build_id": wShort + "-cuda", "previous_build": "0a1b2c3d4-cuda", "log_path": "/x/build.log", "log_tail": "line"}}
	r := f.run(t, "--json", "rebuild-whisper", "--yes")
	var doc struct {
		Error struct {
			Code    string
			Message string
			Details map[string]any
		}
	}
	if r.code != ExitFailed || json.Unmarshal([]byte(r.out), &doc) != nil || doc.Error.Code != doctor.KIT021 {
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

func TestRebuildWhisperResolveErrorsAreMappedToo(t *testing.T) {
	f := newWhisperFix(t)
	f.eng.inErr = &build.Error{Code: doctor.KIT020, Stage: build.StageResolve, Message: "The whisper.cpp backend must be cuda, vulkan or cpu.", Details: map[string]any{}}
	r := f.run(t, "rebuild-whisper", "--yes")
	if r.code != ExitFailed || !strings.Contains(r.errOut, "[LAI-KIT-020]") || len(f.eng.reqs) != 0 {
		t.Fatalf("exit %d\n%s", r.code, r.errOut)
	}
}

// ---- the options the command gives the engine ---------------------------------------------------

func TestRebuildWhisperOptions(t *testing.T) {
	f := newWhisperFix(t)
	f.nodeYAML(t, "schema_version: 1\nllama:\n  backend: vulkan\n  cuda_arch: \"89\"\nports:\n  whisper: 8123\nwhisper:\n  enabled: true\n  commit: \""+wCommit+"\"\n  backend: auto\n")
	kitDir := filepath.Join(f.env.Paths.InternalRoot, "kit")
	if err := os.MkdirAll(kitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if r := f.run(t, "rebuild-whisper", "--yes"); r.code != 0 {
		t.Fatalf("exit %d\n%s", r.code, r.errOut)
	}
	opt := f.eng.opt

	if opt.Recipe.Name != "whisper.cpp" || opt.Recipe.Service != "whisper" || opt.Recipe.BuildFailedCode != doctor.KIT020 || opt.Recipe.UnusableCode != doctor.KIT021 {
		t.Errorf("recipe = %+v", opt.Recipe.Name)
	}
	if opt.CompileUser != build.CompileAsCaller {
		t.Errorf("compile user = %q", opt.CompileUser)
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
	d := opt.Defaults()
	if d.Commit != wCommit || d.Backend != "auto" || d.LlamaBackend != "vulkan" || d.CudaArch != "89" {
		t.Errorf("defaults = %+v", d)
	}
	if opt.Services == nil || opt.ServiceCheck == nil || opt.SmokeModel == nil || opt.RecordCommit == nil || opt.Audit == nil {
		t.Errorf("a seam is missing: %+v", opt)
	}
}

func TestRebuildWhisperWithoutNodeYAMLHasNoDefaults(t *testing.T) {
	f := newWhisperFix(t)
	if r := f.run(t, "rebuild-whisper", "--yes", "--commit", wShort, "--backend", "cpu"); r.code != 0 {
		t.Fatalf("exit %d\n%s", r.code, r.errOut)
	}
	d := f.eng.opt.Defaults()
	if d.Commit != "" || d.Backend != "" || d.LlamaBackend != "" || d.CudaArch != "" {
		t.Errorf("defaults = %+v, want none", d)
	}
	if r := f.lastReq(t); r.Commit != wShort || r.Backend != "cpu" {
		t.Errorf("request = %+v", r)
	}
}

func TestRebuildWhisperServiceCheck(t *testing.T) {
	setup := func(t *testing.T, yaml string, withEnv bool) (*whisperFix, build.Options) {
		f := newWhisperFix(t)
		if yaml != "" {
			f.nodeYAML(t, yaml)
		}
		if withEnv {
			if err := os.WriteFile(filepath.Join(f.env.Paths.EtcDir, "whisper.env"), []byte("WHISPER_MODEL=/m\n"), 0o640); err != nil {
				t.Fatal(err)
			}
		}
		if r := f.run(t, "rebuild-whisper", "--yes"); r.code != 0 {
			t.Fatalf("exit %d\n%s", r.code, r.errOut)
		}
		return f, f.eng.opt
	}
	ctx := context.Background()
	t.Run("no whisper.env yet: nothing to check", func(t *testing.T) {
		f, opt := setup(t, "", false)
		if err := opt.ServiceCheck(ctx); !errors.Is(err, build.ErrServiceNotConfigured) || len(f.probed) != 0 {
			t.Errorf("err = %v probed = %v", err, f.probed)
		}
	})
	t.Run("speech switched off: nothing to check", func(t *testing.T) {
		f, opt := setup(t, "whisper:\n  enabled: false\n", true)
		if err := opt.ServiceCheck(ctx); !errors.Is(err, build.ErrServiceNotConfigured) || len(f.probed) != 0 {
			t.Errorf("err = %v probed = %v", err, f.probed)
		}
	})
	t.Run("configured: the speech server is asked on its port", func(t *testing.T) {
		f, opt := setup(t, "ports:\n  whisper: 8123\n", true)
		if err := opt.ServiceCheck(ctx); err != nil || !reflect.DeepEqual(f.probed, []string{"http://127.0.0.1:8123/"}) {
			t.Errorf("err = %v probed = %v", err, f.probed)
		}
	})
	t.Run("the default port is 8081", func(t *testing.T) {
		f, opt := setup(t, "", true)
		if err := opt.ServiceCheck(ctx); err != nil || !reflect.DeepEqual(f.probed, []string{"http://127.0.0.1:8081/"}) {
			t.Errorf("err = %v probed = %v", err, f.probed)
		}
	})
	t.Run("a server that does not answer is a failure", func(t *testing.T) {
		f, opt := setup(t, "", true)
		f.probeErr = errors.New("connection refused")
		if err := opt.ServiceCheck(ctx); err == nil || errors.Is(err, build.ErrServiceNotConfigured) {
			t.Errorf("err = %v", err)
		}
	})
}

func TestRebuildWhisperRecordsTheCommitInNodeYAML(t *testing.T) {
	f := newWhisperFix(t)
	f.nodeYAML(t, "# the node\nschema_version: 1\nnode:\n  id: ai-n3\nprofile: small-8gb\nllama:\n  backend: cpu # keep this comment\nwhisper:\n  enabled: true\n  backend: auto\n")
	if r := f.run(t, "rebuild-whisper", "--yes"); r.code != 0 {
		t.Fatalf("exit %d\n%s", r.code, r.errOut)
	}
	if err := f.eng.opt.RecordCommit(wCommit); err != nil {
		t.Fatal(err)
	}
	got := readFile(t, f.env.Paths.NodeYAML())
	for _, want := range []string{"# the node", "# keep this comment", "backend: cpu", "backend: auto", "commit: " + wCommit} {
		if !strings.Contains(got, want) {
			t.Errorf("node.yaml lacks %q:\n%s", want, got)
		}
	}
	if d := readWhisperSettings(f.env); d.Commit != wCommit || d.Backend != "auto" || !d.Enabled || d.LlamaBackend != "cpu" {
		t.Errorf("settings after the write = %+v", d)
	}
	// A node.yaml that would become invalid is refused and left alone.
	f.nodeYAML(t, "schema_version: 1\nports:\n  agent: 1\n  llama: 1\n")
	before := readFile(t, f.env.Paths.NodeYAML())
	if err := f.eng.opt.RecordCommit(wCommit); err == nil {
		t.Error("an invalid node.yaml was rewritten")
	}
	if readFile(t, f.env.Paths.NodeYAML()) != before {
		t.Error("node.yaml changed")
	}
}

func TestRebuildWhisperSmokeModelIsTheSmallestVerifiedWhisperModel(t *testing.T) {
	f := newWhisperFix(t)
	models := filepath.Join(f.env.Paths.InternalRoot, "speech", "whisper") // overview.md section 3.3
	put := func(id, engine, kind string, size int, state string) {
		dir := filepath.Join(models, id)
		file := "ggml-" + id + ".bin"
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, file), []byte(strings.Repeat("x", size)), 0o644); err != nil {
			t.Fatal(err)
		}
		m := manifest.Manifest{
			SchemaVersion: 1, ItemID: id, ItemKind: manifest.KindModel, Engine: engine, CatalogVersion: "2026.11.02-1",
			Version: "r1", SortKey: "r1", State: state, Source: manifest.Source{Type: "url"},
			Files:      []manifest.File{{Kind: kind, Path: file, SizeBytes: int64(size), SHA256: strings.Repeat("a", 64), SHA256Source: "catalog"}},
			TotalBytes: int64(size), Trust: manifest.TrustPinned,
			DownloadedAt: kitEpoch, VerifiedAt: kitEpoch, LastReverifiedAt: kitEpoch,
			SyncedBy: manifest.SyncedBy{NodeID: "node-a", AgentVersion: "0.4.0", RunID: "run-1"},
		}
		if state == manifest.StateVerified {
			if err := manifest.Write(config.OSFS{}, manifest.Path(dir), m); err != nil {
				t.Fatal(err)
			}
		} else {
			raw, _ := json.Marshal(map[string]any{"schema_version": 1, "item_id": id, "state": state})
			if err := os.WriteFile(manifest.Path(dir), raw, 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, ok := whisperSmokeModel(f.env); ok {
		t.Fatal("a node with no models has no smoke model")
	}
	put("whisper-medium", "whisper", "whisper", 300, manifest.StateVerified)
	put("whisper-small", "whisper", "whisper", 100, manifest.StateVerified)
	put("whisper-tiny", "whisper", "whisper", 10, "downloading")  // not verified: never used
	put("qwen-9b", "llama", "weights", 5, manifest.StateVerified) // not a whisper model
	got, ok := whisperSmokeModel(f.env)
	if want := filepath.Join(models, "whisper-small", "ggml-whisper-small.bin"); !ok || got != want {
		t.Errorf("smoke model = %q %v, want %q", got, ok, want)
	}
}

func TestRebuildWhisperAuditsThroughTheLocalLog(t *testing.T) {
	f := newWhisperFix(t)
	if r := f.run(t, "rebuild-whisper", "--yes"); r.code != 0 {
		t.Fatalf("exit %d\n%s", r.code, r.errOut)
	}
	w := f.eng.opt.Audit
	if err := w.Write(audit.Event{Action: audit.ActionWhisperBuildSwitch, Result: audit.ResultOK, Target: map[string]string{"build": wShort + "-cuda"}}); err != nil {
		t.Fatal(err)
	}
	lines := f.auditLines()
	if len(lines) != 1 || lines[0]["action"] != "whisper.build.switch" || lines[0]["result"] != "ok" {
		t.Fatalf("audit = %v", lines)
	}
	// A rebuild from scratch has no state folder yet: nothing to append to, and no failure.
	if err := os.RemoveAll(f.env.Paths.StateDir); err != nil {
		t.Fatal(err)
	}
	if err := w.Write(audit.Event{Action: audit.ActionWhisperBuildSwitch, Result: audit.ResultOK}); err != nil {
		t.Errorf("a node without a state folder must not fail: %v", err)
	}
}

// ---- the agent: restart through the API ---------------------------------------------------------

func TestWhisperServicesRestartThroughTheAgent(t *testing.T) {
	newRig := func(t *testing.T) (*mgRig, *whisperFix) {
		r := newMgRig(t, fake.Options{})
		f := newWhisperFix(t)
		return r, f
	}
	run := func(r *mgRig, f *whisperFix, hook func(ctx context.Context, svc build.ServiceRestarter)) followOut {
		f.eng.onBuild = func(ctx context.Context, opt build.Options) { hook(ctx, opt.Services) }
		return r.run(mgArgs("rebuild-whisper", "--yes"))
	}
	t.Run("up and restarted: POST /services/whisper/restart with an empty body", func(t *testing.T) {
		r, f := newRig(t)
		r.agent().Respond(http.MethodPost, "/services/whisper/restart", 200, `{"name":"whisper","active":"active","restored_models":[],"op_id":"op_w1"}`)
		var up bool
		var rerr error
		got := run(r, f, func(ctx context.Context, svc build.ServiceRestarter) {
			up = svc.Up(ctx)
			rerr = svc.RestartService(ctx, "whisper")
		})
		if got.code != 0 || !up || rerr != nil {
			t.Fatalf("exit %d up %v err %v\n%s", got.code, up, rerr, got.errOut)
		}
		if b := r.lastBody(http.MethodPost, "/services/whisper/restart"); b != `{}` {
			t.Errorf("body = %s", b)
		}
	})
	t.Run("a busy agent is a BusyError with the operation id", func(t *testing.T) {
		r, f := newRig(t)
		r.agent().RespondError(http.MethodPost, "/services/whisper/restart", 409, doctor.AGT022, "busy", "wait", map[string]any{"op_id": "op_busy"})
		var rerr error
		run(r, f, func(ctx context.Context, svc build.ServiceRestarter) { rerr = svc.RestartService(ctx, "whisper") })
		var busy *build.BusyError
		if !errors.As(rerr, &busy) {
			t.Fatalf("err = %v, want a *build.BusyError", rerr)
		}
	})
	t.Run("another failure is returned as it is", func(t *testing.T) {
		r, f := newRig(t)
		r.agent().RespondError(http.MethodPost, "/services/whisper/restart", 500, doctor.SVC051, "failed", "see logs", nil)
		var rerr error
		run(r, f, func(ctx context.Context, svc build.ServiceRestarter) { rerr = svc.RestartService(ctx, "whisper") })
		var busy *build.BusyError
		if rerr == nil || errors.As(rerr, &busy) || errors.Is(rerr, build.ErrAgentUnavailable) {
			t.Fatalf("err = %v", rerr)
		}
	})
}

func TestWhisperServicesAgentDownIsNotUp(t *testing.T) {
	f := newWhisperFix(t)
	var up bool
	var rerr error
	f.eng.onBuild = func(ctx context.Context, opt build.Options) {
		up = opt.Services.Up(ctx)
		rerr = opt.Services.RestartService(ctx, "whisper")
	}
	// localOpts points at a socket and a port nobody listens on.
	if r := f.run(t, "rebuild-whisper", "--yes"); r.code != 0 {
		t.Fatalf("exit %d\n%s", r.code, r.errOut)
	}
	if up || !errors.Is(rerr, build.ErrAgentUnavailable) {
		t.Errorf("up = %v, err = %v: a down agent makes the switch use systemctl", up, rerr)
	}
}
