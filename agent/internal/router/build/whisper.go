package build

import (
	"context"
	"debug/elf"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/audit"
	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/execx"
	"github.com/marinoscar/local-ai/agent/internal/services"
)

// whisper.cpp is the second program the build manager builds (companion-services.md section 4.2.1,
// SVC-05): the same Manager with WhisperRecipe, so it reuses the offline source (the kit tarball or
// mirror), the preflight, the extraction, the CUDA redistributable wiring, the unprivileged compile,
// the install, the atomic switch with its rollback and the retention of the llama build. This file
// holds what is whisper's own: the recipe, its flags, its smoke test and the node.yaml defaults.

const (
	// DefaultWhisperRoot is /opt/localai/whisper.cpp: builds/ and the current symlink live under it.
	DefaultWhisperRoot = "/opt/localai/whisper.cpp"
	// DefaultWhisperCloneURL is the upstream repository of the online source.
	DefaultWhisperCloneURL = "https://github.com/ggml-org/whisper.cpp.git"
	// SourceStemWhisper is the name stem of whisper.cpp in the kit: kit/src/whisper.cpp-<commit>.tar.gz
	// and kit/git/whisper.cpp.git.
	SourceStemWhisper = "whisper.cpp"

	// CodeWhisperBuildFailed: building whisper.cpp failed (LAI-KIT-020).
	CodeWhisperBuildFailed = doctor.KIT020
	// CodeWhisperBuildUnusable: the new whisper.cpp build does not work and the previous one is
	// still in use (LAI-KIT-021).
	CodeWhisperBuildUnusable = doctor.KIT021

	// whisperSampleLanguage is the language of the smoke test's sample recording.
	whisperSampleLanguage = "en"
)

// WhisperRecipe is whisper.cpp (companion-services.md section 4.2.1): whisper-server and whisper-cli
// built static, with the source extracted afresh from the kit tarball into the scratch directory
// every time, and reported under LAI-KIT-020 and LAI-KIT-021.
var WhisperRecipe = Recipe{
	Name:            SourceStemWhisper,
	CloneURL:        DefaultWhisperCloneURL,
	Programs:        []string{"whisper-server", "whisper-cli"},
	Tools:           KitPrerequisites,
	Root:            DefaultWhisperRoot,
	Service:         "whisper",
	Unit:            services.UnitWhisper,
	Flags:           whisperFlags,
	Targets:         []string{"whisper-server", "whisper-cli"},
	ScratchSource:   true,
	KitPreflight:    true,
	AllowAuto:       true,
	BuildFailedCode: CodeWhisperBuildFailed,
	UnusableCode:    CodeWhisperBuildUnusable,
	AuditAction:     audit.ActionWhisperBuildSwitch,
	smoke:           smokeWhisper,
}

// whisperFlags are the configure flags of whisper.cpp (companion-services.md section 4.2.1): static
// libraries, the examples (whisper-server and whisper-cli are examples), no tests, no SDL, then the
// backend's flags. OpenBLAS is not used, to keep the dependencies minimal.
func whisperFlags(in Inputs) []string {
	flags := []string{
		"-DCMAKE_BUILD_TYPE=Release",
		"-DBUILD_SHARED_LIBS=OFF",
		"-DWHISPER_BUILD_EXAMPLES=ON",
		"-DWHISPER_BUILD_TESTS=OFF",
		"-DWHISPER_SDL2=OFF",
		"-DGGML_NATIVE=ON",
	}
	return append(flags, ggmlBackendFlags(in)...)
}

// WhisperDefaults are the values `aictl rebuild-whisper` falls back to: node.yaml's whisper.commit
// and whisper.backend (auto, cuda, vulkan or cpu), with auto meaning llama.backend, and the CUDA
// architecture of the llama block (the one GPU the node has).
func WhisperDefaults(commit, backend string, llama config.LlamaBlock) Defaults {
	d := Defaults{Commit: commit, Backend: backend, LlamaBackend: llama.Backend}
	if llama.CudaArch != nil {
		d.CudaArch = *llama.CudaArch
	}
	return d
}

// SharedWhisperLibs returns the shared libraries the ELF program at path asks for that belong to
// whisper.cpp or ggml (libwhisper*, libggml*): a static build has none, and one that has them would
// stop working when moved out of its build directory. A file that is not an ELF is an error. The
// dynamic section is read with debug/elf: nothing is run (ldd would load the program).
func SharedWhisperLibs(fsys FS, path string) ([]string, error) {
	f, err := fsys.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	ef, err := elf.NewFile(f)
	if err != nil {
		return nil, err
	}
	defer ef.Close()
	libs, err := ef.ImportedLibraries()
	if err != nil {
		return nil, err
	}
	var bad []string
	for _, l := range libs {
		if strings.HasPrefix(l, "libwhisper") || strings.HasPrefix(l, "libggml") {
			bad = append(bad, l)
		}
	}
	return bad, nil
}

// smokeWhisper is the smoke test of a whisper.cpp build (companion-services.md section 4.2.1): no
// whisper or ggml shared library is referenced, both programs answer --help with status 0, and, when
// a verified model and the sample recording are available, `whisper-cli -m <model> -f <sample> -l en
// -nt` exits 0 and prints text. A step that cannot run for lack of a model or a sample is skipped
// with a warning; a failure is the recipe's UnusableCode (LAI-KIT-021) and nothing is switched.
func smokeWhisper(r *run, ctx context.Context) (Smoke, *Error) {
	fsys := r.m.opt.FS
	fail := func(msg, cause string, kv ...any) (Smoke, *Error) {
		return Smoke{}, newErr(CodeBuildUnusable, StageSmoke, msg, nil, append([]any{"cause", cause}, kv...)...)
	}
	var steps []string

	for _, name := range r.m.opt.Recipe.Programs {
		libs, err := SharedWhisperLibs(fsys, filepath.Join(r.plan.OutDir, "bin", name))
		if err != nil {
			return Smoke{}, newErr(CodeBuildUnusable, StageSmoke, "The new whisper.cpp build could not be inspected: "+name+" is not a usable program.", err,
				"cause", "not_elf", "binary", name)
		}
		if len(libs) > 0 {
			return fail("The new whisper.cpp build is not self-contained: "+name+" needs "+strings.Join(libs, ", ")+", which are not installed anywhere it can find them.",
				"smoke_shared_libs", "binary", name, "libs", libs)
		}
	}
	steps = append(steps, "static")

	for _, name := range r.m.opt.Recipe.Programs {
		prog := filepath.Join(r.plan.OutDir, "bin", name)
		args := []string{"--help"}
		command := name + " --help"
		r.logf("$ %s %s\n", prog, strings.Join(args, " "))
		res, err := r.m.opt.Exec.Run(ctx, execx.Cmd{Path: prog, Args: args, Timeout: r.m.opt.Timing.Smoke, MaxOut: 1 << 20, Env: toolEnv()})
		r.logf("%s%s", res.Stdout, res.Stderr)
		switch {
		case err != nil && ctx.Err() != nil:
			return Smoke{}, &Error{Code: CodeBuildUnusable, Stage: StageSmoke, Message: "cancelled", Err: ctx.Err(), Details: map[string]any{}}
		case err != nil:
			r.logf("[could not run: %v]\n", err)
			return Smoke{}, newErr(CodeBuildUnusable, StageSmoke, "The new whisper.cpp build did not start: "+command+" failed.", err,
				"cause", "smoke_exec", "command", command)
		case res.ExitCode != 0:
			r.logf("[exit status %d]\n", res.ExitCode)
			return Smoke{}, newErr(CodeBuildUnusable, StageSmoke,
				fmt.Sprintf("The new whisper.cpp build did not start: %s exited with status %d.", command, res.ExitCode), nil,
				"cause", "smoke_exit", "command", command, "exit_code", res.ExitCode)
		}
	}
	steps = append(steps, "help")

	transcribed, e := r.transcribe(ctx)
	if e != nil {
		return Smoke{}, e
	}
	if transcribed {
		steps = append(steps, "transcribe")
	}
	return Smoke{Devices: []string{}, Checks: steps}, nil
}

// transcribe is the last step of the whisper smoke test: the new whisper-cli transcribes the sample
// recording with the speech model of this computer and must print text. It reports whether it ran;
// with no model or no sample it is skipped with a warning.
func (r *run) transcribe(ctx context.Context) (bool, *Error) {
	skip := func(why string) (bool, *Error) {
		r.warnings = append(r.warnings, "The new build was not asked to transcribe a sample, so only its --help was tested: "+why)
		return false, nil
	}
	opt := r.m.opt
	if opt.SmokeModel == nil {
		return skip("no speech model is available to test it with.")
	}
	model, ok := opt.SmokeModel()
	if !ok || model == "" {
		return skip("no speech model is available to test it with.")
	}
	if opt.SmokeAudio == nil {
		return skip("no sample recording is available.")
	}
	audio, err := opt.SmokeAudio()
	if err != nil || len(audio) == 0 {
		return skip("the sample recording cannot be read.")
	}
	fsys := opt.FS
	if err := fsys.MkdirAll(opt.ScratchDir, 0o755); err != nil {
		return false, newErr(CodeBuildFailed, StageSmoke, "The scratch folder for the sample recording could not be created.", err, "cause", "filesystem")
	}
	sample := filepath.Join(opt.ScratchDir, "smoke-"+r.plan.BuildID+".wav")
	if err := fsys.WriteFile(sample, audio, 0o600); err != nil {
		return false, newErr(CodeBuildFailed, StageSmoke, "The sample recording could not be written.", err, "cause", "filesystem")
	}
	defer func() { _ = fsys.RemoveAll(sample) }()

	cli := filepath.Join(r.plan.OutDir, "bin", "whisper-cli")
	args := []string{"-m", model, "-f", sample, "-l", whisperSampleLanguage, "-nt"}
	command := "whisper-cli -m <model> -f <sample> -l " + whisperSampleLanguage + " -nt"
	r.logf("$ %s %s\n", cli, strings.Join(args, " "))
	res, err := opt.Exec.Run(ctx, execx.Cmd{Path: cli, Args: args, Timeout: opt.Timing.Generate, MaxOut: 1 << 20, Env: toolEnv()})
	r.logf("%s%s", res.Stdout, res.Stderr)
	switch {
	case err != nil && ctx.Err() != nil:
		return false, &Error{Code: CodeBuildUnusable, Stage: StageSmoke, Message: "cancelled", Err: ctx.Err(), Details: map[string]any{}}
	case err != nil:
		r.logf("[could not run: %v]\n", err)
		return false, newErr(CodeBuildUnusable, StageSmoke, "The new whisper.cpp build did not finish transcribing the sample: "+err.Error(), err,
			"cause", "smoke_transcribe_exec", "command", command)
	case res.ExitCode != 0:
		r.logf("[exit status %d]\n", res.ExitCode)
		return false, newErr(CodeBuildUnusable, StageSmoke, fmt.Sprintf("The new whisper.cpp build failed to transcribe the sample: whisper-cli exited with status %d.", res.ExitCode), nil,
			"cause", "smoke_transcribe_exit", "command", command, "exit_code", res.ExitCode)
	case strings.TrimSpace(string(res.Stdout)) == "":
		return false, newErr(CodeBuildUnusable, StageSmoke, "The new whisper.cpp build ran but transcribed nothing.", nil,
			"cause", "smoke_transcribe_empty", "command", command)
	}
	return true, nil
}

// serviceCheck is the post-switch check of a recipe whose service is not llama's: Options.ServiceCheck
// is asked until the restarted service answers, for at most Timing.HealthWait from the restart
// (a poll every Timing.Poll). A service with no configuration yet (ErrServiceNotConfigured) is a
// warning, not a failure: there is nothing to start. Any other answer at the deadline fails the
// check, so the switch is undone.
func (m *Manager) serviceCheck(ctx context.Context, start time.Time) (string, []string, *Error) {
	check := m.opt.ServiceCheck
	if check == nil {
		return "", []string{"The new build was not checked: nothing was available to ask the service."}, nil
	}
	t := m.opt.Timing
	by := start.Add(t.HealthWait)
	for {
		err := check(ctx)
		switch {
		case err == nil:
			return "", nil, nil
		case errors.Is(err, ErrServiceNotConfigured):
			return "", []string{"The service has no configuration yet, so the new build was not checked after the restart."}, nil
		case ctx.Err() != nil:
			return "", nil, newErr(CodeBuildUnusable, StageCheck, "The new llama.cpp build did not work.", ctx.Err(), "cause", "cancelled")
		case !m.opt.Clock().Before(by):
			return "", nil, newErr(CodeBuildUnusable, StageCheck, "The new llama.cpp build did not work.", err, "cause", "health_timeout")
		}
		m.opt.Sleep(ctx, t.Poll)
	}
}
