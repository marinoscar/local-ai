package build

import (
	"context"
	"debug/elf"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/marinoscar/local-ai/agent/internal/execx"
)

// ---- the relocatable check ---------------------------------------------------------------

// checkRunpath reads the dynamic section of every installed program with debug/elf (no exec) and
// refuses a RUNPATH or RPATH into a /home directory or into a builds/*.tmp directory: the unit has
// ProtectHome=yes, and a .tmp directory is renamed (llama-router.md section 4.10.3 step 2). A
// static program has no dynamic section and passes.
func (r *run) checkRunpath() *Error {
	for _, name := range r.m.opt.Recipe.Programs {
		path := filepath.Join(r.plan.OutDir, "bin", name)
		bad, err := RunpathProblems(r.m.opt.FS, path, filepath.Join(r.m.root, buildsDir))
		if err != nil {
			return newErr(CodeBuildUnusable, StageRunpath, "The new llama.cpp build could not be inspected: "+name+" is not a usable program.", err,
				"cause", "not_elf", "binary", name)
		}
		if len(bad) > 0 {
			return newErr(CodeBuildUnusable, StageRunpath,
				"The new llama.cpp build is not relocatable: "+name+" looks for libraries in "+strings.Join(bad, ", ")+".", nil,
				"cause", "runpath", "binary", name, "runpath", bad)
		}
	}
	return nil
}

// RunpathProblems returns the RUNPATH and RPATH entries of an ELF file that point into /home or into
// a ".tmp" directory under buildsRoot. $ORIGIN and system directories are fine. A file that is not
// an ELF is an error.
func RunpathProblems(fsys FS, path, buildsRoot string) ([]string, error) {
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
	var bad []string
	for _, tag := range []elf.DynTag{elf.DT_RUNPATH, elf.DT_RPATH} {
		vals, err := ef.DynString(tag)
		if err != nil {
			return nil, err
		}
		for _, v := range vals {
			for _, entry := range strings.Split(v, ":") {
				if entry != "" && badRunpathEntry(entry, buildsRoot) {
					bad = append(bad, entry)
				}
			}
		}
	}
	return bad, nil
}

func badRunpathEntry(entry, buildsRoot string) bool {
	e := filepath.Clean(entry)
	if e == "/home" || strings.HasPrefix(e, "/home/") {
		return true
	}
	prefix := filepath.Clean(buildsRoot) + "/"
	if strings.HasPrefix(e, prefix) {
		first := strings.SplitN(strings.TrimPrefix(e, prefix), "/", 2)[0]
		return strings.HasSuffix(first, suffixTmp) || strings.Contains(first, suffixTmp+".")
	}
	return false
}

// ---- the smoke test ----------------------------------------------------------------------

// smoke is the smoke test of the recipe (llama's unless the recipe brings its own, WhisperRecipe).
// A failure is the recipe's UnusableCode: the build is kept as <id>.tmp.failed and nothing is
// switched.
func (r *run) smoke(ctx context.Context) (Smoke, *Error) {
	if fn := r.m.opt.Recipe.smoke; fn != nil {
		return fn(r, ctx)
	}
	return r.smokeLlama(ctx)
}

// smokeLlama runs `llama-server --version` and `--list-devices` of the new build (llama-router.md
// section 4.10.3 step 3), then the completion (LAI-LLM-019).
func (r *run) smokeLlama(ctx context.Context) (Smoke, *Error) {
	server := filepath.Join(r.plan.OutDir, "bin", "llama-server")
	fail := func(msg, cause string, kv ...any) (Smoke, *Error) {
		return Smoke{}, newErr(CodeBuildUnusable, StageSmoke, msg, nil, append([]any{"cause", cause}, kv...)...)
	}
	invoke := func(args ...string) (string, *Error) {
		r.logf("$ %s %s\n", server, strings.Join(args, " "))
		res, err := r.m.opt.Exec.Run(ctx, execx.Cmd{Path: server, Args: args, Timeout: r.m.opt.Timing.Smoke, MaxOut: 1 << 20, Env: toolEnv()})
		out := string(res.Stdout) + string(res.Stderr)
		r.logf("%s", out)
		switch {
		case err != nil && ctx.Err() != nil:
			return "", &Error{Code: CodeBuildUnusable, Stage: StageSmoke, Message: "cancelled", Err: ctx.Err(), Details: map[string]any{}}
		case err != nil:
			r.logf("[could not run: %v]\n", err)
			return "", newErr(CodeBuildUnusable, StageSmoke, "The new llama.cpp build did not start: llama-server "+strings.Join(args, " ")+" failed.", err,
				"cause", "smoke_exec", "command", "llama-server "+strings.Join(args, " "))
		case res.ExitCode != 0:
			r.logf("[exit status %d]\n", res.ExitCode)
			return "", newErr(CodeBuildUnusable, StageSmoke,
				fmt.Sprintf("The new llama.cpp build did not start: llama-server %s exited with status %d.", strings.Join(args, " "), res.ExitCode), nil,
				"cause", "smoke_exit", "command", "llama-server "+strings.Join(args, " "), "exit_code", res.ExitCode)
		}
		return out, nil
	}

	out, e := invoke("--version")
	if e != nil {
		return Smoke{}, e
	}
	version, ok := VersionLine(out, r.plan.Commit)
	if !ok {
		return fail("The new llama.cpp build reports a different version than the commit "+r.plan.Commit[:7]+".", "smoke_version",
			"want_prefix", r.plan.Commit[:7], "output", firstLines(out, 5))
	}

	out, e = invoke("--list-devices")
	if e != nil {
		return Smoke{}, e
	}
	devices := ParseDevices(out)
	switch r.plan.Backend {
	case BackendCUDA:
		if !hasDevicePrefix(devices, "CUDA0") {
			return fail("The new llama.cpp build does not see the GPU (no CUDA0 device).", "smoke_devices", "devices", devices)
		}
	case BackendVulkan:
		real := false
		for _, d := range devices {
			if !strings.Contains(strings.ToLower(d), "llvmpipe") {
				real = true
			}
		}
		if !real {
			return fail("The new llama.cpp build only sees a software Vulkan device (llvmpipe), not the GPU.", "smoke_devices", "devices", devices)
		}
	}
	if devices == nil {
		devices = []string{}
	}
	if e := r.generate(ctx); e != nil {
		return Smoke{}, e
	}
	return Smoke{Version: version, Devices: devices}, nil
}

// generate is the last step of the smoke test (rebuild-kit.md section 4.3.3): the new llama-cli
// writes a few words with the smallest verified model of this computer. With no model to ask it is
// skipped, with a warning; a program that fails or prints nothing is LAI-LLM-019 like any smoke
// failure, so `current` is never switched to a build that cannot generate.
func (r *run) generate(ctx context.Context) *Error {
	skip := func(why string) *Error {
		r.warnings = append(r.warnings, "The new build was not asked to write a few words: "+why)
		return nil
	}
	if r.m.opt.SmokeModel == nil {
		return skip("no model is available to test it with.")
	}
	model, ok := r.m.opt.SmokeModel()
	if !ok || model == "" {
		return skip("no model is available to test it with.")
	}
	cli := filepath.Join(r.plan.OutDir, "bin", "llama-cli")
	args := []string{"-m", model, "-n", "8", "-p", "Hello", "-ngl", "99", "--no-warmup"}
	r.logf("$ %s %s\n", cli, strings.Join(args, " "))
	res, err := r.m.opt.Exec.Run(ctx, execx.Cmd{Path: cli, Args: args, Timeout: r.m.opt.Timing.Generate, MaxOut: 1 << 20, Env: toolEnv()})
	r.logf("%s%s", res.Stdout, res.Stderr)
	command := "llama-cli -m <model> -n 8 -p Hello -ngl 99 --no-warmup"
	switch {
	case err != nil && ctx.Err() != nil:
		return &Error{Code: CodeBuildUnusable, Stage: StageSmoke, Message: "cancelled", Err: ctx.Err(), Details: map[string]any{}}
	case err != nil:
		r.logf("[could not run: %v]\n", err)
		return newErr(CodeBuildUnusable, StageSmoke, "The new llama.cpp build did not finish writing a few words: "+err.Error(), err,
			"cause", "smoke_generate_exec", "command", command)
	case res.ExitCode != 0:
		r.logf("[exit status %d]\n", res.ExitCode)
		return newErr(CodeBuildUnusable, StageSmoke, fmt.Sprintf("The new llama.cpp build failed to write a few words: llama-cli exited with status %d.", res.ExitCode), nil,
			"cause", "smoke_generate_exit", "command", command, "exit_code", res.ExitCode)
	case strings.TrimSpace(string(res.Stdout)) == "":
		return newErr(CodeBuildUnusable, StageSmoke, "The new llama.cpp build ran but wrote nothing.", nil,
			"cause", "smoke_generate_empty", "command", command)
	}
	return nil
}

// VersionLine finds the line of `llama-server --version` that carries the first 7 hex characters of
// the commit. It accepts both "version: 6000 (f3586e6)" and "... commit f3586e66b". ok is false when
// no line has the prefix.
func VersionLine(out, commit string) (line string, ok bool) {
	prefix := strings.ToLower(commit)
	if len(prefix) > 7 {
		prefix = prefix[:7]
	}
	for _, l := range strings.Split(out, "\n") {
		l = strings.TrimSpace(l)
		if l != "" && strings.Contains(strings.ToLower(l), prefix) {
			return l, true
		}
	}
	return "", false
}

var deviceRe = regexp.MustCompile(`^\s*([A-Za-z][A-Za-z0-9_]*[0-9]+):\s*(\S.*)$`)

// ParseDevices returns the device lines of `llama-server --list-devices`: "CUDA0: NVIDIA ... (8192
// MiB, ...)" and "Vulkan0: ...", as printed, without the leading blanks. Other log lines are ignored.
func ParseDevices(out string) []string {
	var devices []string
	for _, l := range strings.Split(out, "\n") {
		if m := deviceRe.FindStringSubmatch(strings.TrimRight(l, "\r")); m != nil {
			devices = append(devices, m[1]+": "+strings.TrimSpace(m[2]))
		}
	}
	return devices
}

func hasDevicePrefix(devices []string, id string) bool {
	for _, d := range devices {
		if strings.HasPrefix(d, id+":") {
			return true
		}
	}
	return false
}

func firstLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
}
