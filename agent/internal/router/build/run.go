package build

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/audit"
	"github.com/marinoscar/local-ai/agent/internal/execx"
	"github.com/marinoscar/local-ai/agent/internal/kit"
)

const (
	// maxToolOut caps what a cmake run keeps of each stream: the log of a whole llama.cpp build is
	// a few MiB.
	maxToolOut = 64 << 20
	// logTailLines is how much of a failed build's log goes into the error details.
	logTailLines = 200
	// logTailBytes bounds the part of the log file read to find those lines.
	logTailBytes = 256 << 10
)

// run is one Build in progress.
type run struct {
	m       *Manager
	req     Request
	in      Inputs
	plan    Plan
	tools   Tools
	logW    io.WriteCloser
	logOpen bool
	// redistRoot is the unpacked CUDA redistributable tree of option B ("" when the system's nvcc
	// is used).
	redistRoot string
	// drop is set when the compile runs as another user (Options.CompileUser): uid and gid are
	// that user's, setpriv the program that drops to it.
	drop        bool
	uid, gid    int
	setpriv     string
	compileUser string
}

// Build runs the whole procedure of llama-router.md section 4.10: resolve the inputs, check the
// prerequisites and the disk, find the source, configure and build, install, check that the result
// is relocatable, smoke test it, write BUILD.json, put it in place and (unless Request.NoActivate)
// switch current to it, restart llama, check that it serves and switch back when it does not, then
// apply the retention.
//
// Errors are *Error with a doctor code, except a cancelled context, whose ctx.Err() is returned
// after the half-built directory is removed and current is left alone. A failure before the switch
// changes nothing but the build directory (kept as <id>.tmp.failed with its build.log); a failure
// after it has switched current back (LAI-LLM-019) unless there was nothing to switch back to.
func (m *Manager) Build(ctx context.Context, req Request) (Result, error) {
	progress := m.progressFn(req)
	// cancelled turns a stage error into the context's error when the caller gave up.
	cancelled := func(e *Error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return e
	}

	if req.Prebuilt != "" {
		return m.restorePrebuilt(ctx, req, progress)
	}

	var (
		in         Inputs
		tools      Tools
		redist     *kit.CudaRedist
		redistRoot string
		report     *Report
	)
	if req.DryRun || req.FromKit {
		// The offline preflight (rebuild-kit.md section 4.3.1 step 1): every check, reported at once.
		var rerr error
		in, rerr = m.Resolve(ctx, req)
		rep := m.preflightFor(ctx, req, in, rerr)
		if ctx.Err() != nil {
			return Result{}, ctx.Err()
		}
		report = &rep
		if e := rep.Err(); e != nil {
			return Result{Preflight: report, DryRun: req.DryRun}, e
		}
		if req.DryRun {
			return Result{Preflight: report, DryRun: true}, nil
		}
		tools, redist = rep.Tools, rep.Redist
	} else {
		var e *Error
		if in, e = m.resolveErr(ctx, req); e != nil {
			return Result{}, cancelled(e)
		}
		if tools, e = m.Preflight(in.Backend); e != nil {
			return Result{}, e
		}
	}
	if err := m.opt.FS.MkdirAll(filepath.Join(m.root, buildsDir), 0o755); err != nil {
		return Result{}, newErr(CodeBuildFailed, StagePreflight, "The builds folder could not be created.", err, "cause", "filesystem")
	}
	release, err := lockFile(filepath.Join(m.root, lockName))
	if err != nil {
		if errors.Is(err, errLockHeld) {
			return Result{}, newErr(CodeBuildFailed, StagePreflight, "Another llama.cpp build is already running on this computer.", err, "cause", "locked")
		}
		return Result{}, newErr(CodeBuildFailed, StagePreflight, "The build lock could not be taken.", err, "cause", "lock")
	}
	defer release()
	if e := m.checkDisk(); e != nil {
		return Result{}, e
	}

	progress(StageSource, "Looking for the llama.cpp source "+in.Commit)
	sha, e := m.ensureSource(ctx, req, in)
	if e != nil {
		return Result{}, cancelled(e)
	}
	plan := NewPlan(m.root, in, sha)
	if redist != nil {
		progress(StageSource, "Unpacking the CUDA redistributable "+redist.Version+" of the kit")
		root, e := m.prepareRedist(ctx, *redist)
		if e != nil {
			return Result{}, cancelled(e)
		}
		plan = plan.WithCudaRoot(root)
		redistRoot = root
		tools.NVCC = filepath.Join(root, "bin", "nvcc")
	}

	curTarget, err := m.currentTarget()
	if err != nil {
		return Result{}, newErr(CodeBuildFailed, StagePreflight, "The current link of the llama.cpp builds cannot be read.", err, "cause", "current_link")
	}
	if !in.Activate && curTarget != "" && buildName(curTarget) == plan.BuildID {
		return Result{}, newErr(CodeBuildFailed, StagePreflight,
			"Build "+plan.BuildID+" is the one in use now; rebuilding it without switching would replace it silently. Build it with activation, or choose another commit or backend.",
			nil, "cause", "active_build", "build_id", plan.BuildID)
	}

	r := &run{m: m, req: req, in: in, plan: plan, tools: tools, redistRoot: redistRoot}
	if e := r.prepare(); e != nil {
		return Result{}, e
	}

	progress(StageConfigure, fmt.Sprintf("Building llama.cpp %s for %s%s, %d parallel jobs",
		in.ShortCommit, in.Backend, archSuffix(in.CudaArch), in.Jobs))
	if _, e := r.compile(ctx, StageConfigure, tools.CMake, plan.ConfigureArgs, m.opt.Timing.Configure); e != nil {
		return Result{}, r.abort(ctx, e)
	}
	progress(StageBuild, "Compiling (this takes a while)")
	if _, e := r.compile(ctx, StageBuild, tools.CMake, plan.BuildArgs, m.opt.Timing.Build); e != nil {
		return Result{}, r.abort(ctx, e)
	}

	progress(StageInstall, "Installing the programs")
	sums, e := r.install()
	if e != nil {
		return Result{}, r.abort(ctx, e)
	}
	if e := r.checkRunpath(); e != nil {
		return Result{}, r.abort(ctx, e)
	}
	progress(StageSmoke, "Testing the new build")
	smoke, e := r.smoke(ctx)
	if e != nil {
		return Result{}, r.abort(ctx, e)
	}
	info, e := r.writeBuildJSON(ctx, sums, smoke)
	if e != nil {
		return Result{}, r.abort(ctx, e)
	}
	res, err := m.completeBuild(ctx, r, info, curTarget, progress)
	res.Preflight = report
	return res, err
}

// progressFn is the progress callback of a request: the log, then the caller's Progress.
func (m *Manager) progressFn(req Request) func(Stage, string) {
	return func(s Stage, msg string) {
		m.log.Info(msg, "stage", string(s))
		if req.Progress != nil {
			req.Progress(Progress{Stage: s, Message: msg})
		}
	}
}

// abort ends a failed build: a cancelled context removes the half-built directory and returns
// ctx.Err(); any other failure keeps the directory as <id>.tmp.failed with its log.
func (r *run) abort(ctx context.Context, e *Error) error {
	if ctx.Err() != nil {
		r.discard()
		return ctx.Err()
	}
	r.keepFailed(e)
	return e
}

// resolveErr is Resolve with a typed error, so Build never wraps a nil *Error in an interface.
func (m *Manager) resolveErr(ctx context.Context, req Request) (Inputs, *Error) {
	in, err := m.Resolve(ctx, req)
	if err == nil {
		return in, nil
	}
	var e *Error
	if errors.As(err, &e) {
		return Inputs{}, e
	}
	return Inputs{}, newErr(CodeBuildFailed, StageResolve, "The build inputs could not be resolved.", err)
}

func archSuffix(arch string) string {
	if arch == "" {
		return ""
	}
	return " (sm " + arch + ")"
}

// checkDisk refuses to start with less than MinFreeBytes free on the root's filesystem. A filesystem
// that cannot be read does not block the build.
func (m *Manager) checkDisk() *Error {
	free, err := m.opt.FreeBytes(m.root)
	if err != nil {
		m.log.Warn("could not read the free disk space; building anyway", "error", err)
		return nil
	}
	if free < MinFreeBytes {
		return newErr(CodeBuildFailed, StagePreflight,
			fmt.Sprintf("Not enough free disk space for the build: %.1f GB needed, %.1f GB free. Free some space and try again.",
				float64(MinFreeBytes)/1e9, float64(free)/1e9),
			nil, "cause", "disk_space", "free_bytes", free, "needed_bytes", MinFreeBytes)
	}
	return nil
}

// prepare removes what an interrupted build left (<id>.tmp) and creates O/bin and build.log.
func (r *run) prepare() *Error {
	fsys := r.m.opt.FS
	fail := func(err error) *Error {
		return newErr(CodeBuildFailed, StagePreflight, "The build folder could not be created.", err,
			"cause", "filesystem", "build_id", r.plan.BuildID)
	}
	if err := fsys.RemoveAll(r.plan.OutDir); err != nil {
		return fail(err)
	}
	if err := fsys.MkdirAll(filepath.Join(r.plan.OutDir, "bin"), 0o755); err != nil {
		return fail(err)
	}
	w, err := fsys.Create(filepath.Join(r.plan.OutDir, buildLog), 0o644)
	if err != nil {
		return fail(err)
	}
	r.logW, r.logOpen = w, true
	return r.handOver()
}

// handOver makes builds/<id>.tmp (and its bin/) owned by the compile user, so that an unprivileged
// compile can write its build tree there (rebuild-kit.md section 7). The log stays root's: only the
// build manager writes it. With CompileAsCaller nothing changes hands.
func (r *run) handOver() *Error {
	user := r.m.opt.CompileUser
	if user == CompileAsCaller {
		return nil
	}
	fail := func(cause string, err error) *Error {
		return newErr(CodeBuildFailed, StagePreflight,
			"The compile must run as the user "+user+", and that could not be arranged: "+err.Error(), err,
			"cause", cause, "user", user, "build_id", r.plan.BuildID)
	}
	uid, gid, err := r.m.opt.LookupUser(user)
	if err != nil {
		return fail("no_compile_user", err)
	}
	path, err := r.m.opt.Lookup("setpriv")
	if err != nil {
		return fail("no_setpriv", err)
	}
	for _, dir := range []string{r.plan.OutDir, filepath.Join(r.plan.OutDir, "bin")} {
		if err := r.m.opt.FS.Chown(dir, uid, gid); err != nil {
			return fail("chown", err)
		}
	}
	r.drop, r.uid, r.gid, r.setpriv, r.compileUser = true, uid, gid, path, user
	return nil
}

// takeBack gives builds/<id>.tmp and everything in it to root:root again, before anything of it is
// run or renamed into place (rebuild-kit.md section 7).
func (r *run) takeBack() *Error {
	if !r.drop {
		return nil
	}
	if err := chownTree(r.m.opt.FS, r.plan.OutDir, 0, 0); err != nil {
		return newErr(CodeBuildFailed, StageInstall, "The new build could not be handed back to root.", err, "cause", "chown", "build_id", r.plan.BuildID)
	}
	return nil
}

// compile runs the configure or the build step. With a compile user it is
// `setpriv --reuid <user> --regid <user> --clear-groups <program> <args>`: a build script that is
// not trustworthy cannot write system files. Errors and the log name the program (cmake), not
// setpriv.
func (r *run) compile(ctx context.Context, stage Stage, path string, args []string, timeout time.Duration) (execx.Result, *Error) {
	if !r.drop {
		return r.cmd(ctx, stage, path, args, timeout)
	}
	wrapped := append([]string{"--reuid", r.compileUser, "--regid", r.compileUser, "--clear-groups", path}, args...)
	return r.cmdNamed(ctx, stage, filepath.Base(path), r.setpriv, wrapped, timeout)
}

func (r *run) closeLog() {
	if r.logOpen {
		_ = r.logW.Close()
		r.logOpen = false
	}
}

// discard removes the half-built directory (a cancelled build).
func (r *run) discard() {
	r.closeLog()
	_ = r.m.opt.FS.RemoveAll(r.plan.OutDir)
}

// keepFailed turns O into <id>.tmp.failed for inspection, deletes the (large) build tree and puts
// the log's last lines and path in the error.
func (r *run) keepFailed(e *Error) {
	r.closeLog()
	fsys := r.m.opt.FS
	_ = fsys.RemoveAll(r.plan.BuildDir)
	_ = r.takeBack()
	_ = fsys.RemoveAll(r.plan.FailedDir)
	logPath := filepath.Join(r.plan.OutDir, buildLog)
	if err := fsys.Rename(r.plan.OutDir, r.plan.FailedDir); err == nil {
		logPath = filepath.Join(r.plan.FailedDir, buildLog)
	}
	e.Details["build_id"] = r.plan.BuildID
	e.Details["commit"] = r.plan.Commit
	e.Details["backend"] = r.plan.Backend
	e.Details["log_path"] = logPath
	tail := tailLines(fsys, logPath, logTailLines)
	if tail != "" {
		e.Details["log_tail"] = tail
		if pk := hintPackages(tail); len(pk) > 0 && e.Code == CodeBuildFailed {
			e.Details["hint_packages"] = pk
			e.Message += " Missing package? Try: sudo apt install " + strings.Join(pk, " ")
		}
	}
	r.m.log.Error("llama.cpp build failed", "build_id", r.plan.BuildID, "code", e.Code, "stage", string(e.Stage), "log", logPath)
}

// tailLines returns the last n lines of a file, reading only its end.
func tailLines(fsys FS, path string, n int) string {
	fi, err := fsys.Lstat(path)
	if err != nil {
		return ""
	}
	f, err := fsys.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	size := fi.Size()
	start := max(0, size-logTailBytes)
	buf := make([]byte, size-start)
	if _, err := f.ReadAt(buf, start); err != nil && !errors.Is(err, io.EOF) {
		return ""
	}
	lines := strings.Split(strings.TrimRight(string(buf), "\n"), "\n")
	if start > 0 && len(lines) > 1 {
		lines = lines[1:] // the first line is cut in the middle
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

func toolEnv() []string { return []string{"PATH=" + execx.FixedPath, "LC_ALL=C"} }

// cmd runs one program of the build, appending the command line, both output streams and the exit
// status to build.log. A non-zero exit, a timeout and a start failure are LAI-LLM-018.
func (r *run) cmd(ctx context.Context, stage Stage, path string, args []string, timeout time.Duration) (execx.Result, *Error) {
	return r.cmdNamed(ctx, stage, filepath.Base(path), path, args, timeout)
}

// cmdNamed is cmd for a program that is run through a wrapper: name is what the log and the errors
// call it.
func (r *run) cmdNamed(ctx context.Context, stage Stage, name, path string, args []string, timeout time.Duration) (execx.Result, *Error) {
	line := "$ " + path + " " + strings.Join(args, " ") + "\n"
	r.logf("%s", line)
	res, err := r.m.opt.Exec.Run(ctx, execx.Cmd{Path: path, Args: args, Timeout: timeout, MaxOut: maxToolOut, Env: toolEnv()})
	r.logf("%s", string(res.Stdout))
	if len(res.Stderr) > 0 {
		r.logf("%s", string(res.Stderr))
	}
	if res.Truncated {
		r.logf("[output truncated]\n")
	}
	switch {
	case err != nil && ctx.Err() != nil:
		return res, &Error{Code: CodeBuildFailed, Stage: stage, Message: "cancelled", Err: ctx.Err(), Details: map[string]any{}}
	case errors.Is(err, execx.ErrTimeout):
		r.logf("[timed out after %s]\n", timeout)
		return res, newErr(CodeBuildFailed, stage, fmt.Sprintf("Building llama.cpp failed: %s did not finish within %s.", name, timeout), err,
			"cause", "timeout", "command", name)
	case err != nil:
		r.logf("[could not run: %v]\n", err)
		return res, newErr(CodeBuildFailed, stage, "Building llama.cpp failed: "+name+" could not be started.", err, "cause", "exec", "command", name)
	case res.ExitCode != 0:
		r.logf("[exit status %d]\n", res.ExitCode)
		msg := fmt.Sprintf("Building llama.cpp failed: %s exited with status %d.", name, res.ExitCode)
		if res.ExitCode < 0 {
			msg = "Building llama.cpp failed: " + name + " was stopped before it finished."
		}
		return res, newErr(CodeBuildFailed, stage, msg, nil, "cause", "exit", "command", name, "exit_code", res.ExitCode)
	}
	return res, nil
}

func (r *run) logf(format string, a ...any) {
	if r.logOpen {
		_, _ = fmt.Fprintf(r.logW, format, a...)
	}
}

// install copies the three programs from the build tree into O/bin (mode 0755), recording their
// sha256, and deletes the build tree (llama-router.md section 4.10.3 step 1).
func (r *run) install() (map[string]string, *Error) {
	fsys := r.m.opt.FS
	sums := map[string]string{}
	// The compile is over. Everything it wrote is root's again before anything of it is read, run
	// or renamed into place (rebuild-kit.md section 7), and what the compile user could have
	// replaced with a link, bin/ of the output, is made again. Only plain files are copied.
	if e := r.takeBack(); e != nil {
		return nil, e
	}
	if fi, err := fsys.Lstat(filepath.Join(r.plan.BuildDir, "bin")); err == nil && !fi.IsDir() {
		return nil, newErr(CodeBuildFailed, StageInstall, "Building llama.cpp failed: the build produced no bin directory.", nil, "cause", "not_regular")
	}
	outBin := filepath.Join(r.plan.OutDir, "bin")
	if err := fsys.RemoveAll(outBin); err != nil {
		return nil, newErr(CodeBuildFailed, StageInstall, "The output folder of the build could not be prepared.", err, "cause", "install")
	}
	if err := fsys.MkdirAll(outBin, 0o755); err != nil {
		return nil, newErr(CodeBuildFailed, StageInstall, "The output folder of the build could not be prepared.", err, "cause", "install")
	}
	for _, name := range binaries {
		from := filepath.Join(r.plan.BuildDir, "bin", name)
		if fi, err := fsys.Lstat(from); err == nil && !fi.Mode().IsRegular() {
			return nil, newErr(CodeBuildFailed, StageInstall, "Building llama.cpp failed: "+name+" is not a regular file.", nil, "cause", "not_regular", "binary", name)
		}
		sum, err := copyFile(fsys, from, filepath.Join(r.plan.OutDir, "bin", name), 0o755, true)
		if err != nil {
			cause := "install"
			if errors.Is(err, os.ErrNotExist) {
				cause = "missing_binary"
			}
			return nil, newErr(CodeBuildFailed, StageInstall, "Building llama.cpp failed: "+name+" was not produced.", err, "cause", cause, "binary", name)
		}
		sums[name] = sum
	}
	if r.redistRoot != "" {
		_ = fsys.RemoveAll(filepath.Join(r.plan.OutDir, "lib"))
		if err := r.m.copyRedistLibs(r.redistRoot, filepath.Join(r.plan.OutDir, "lib")); err != nil {
			return nil, newErr(CodeBuildFailed, StageInstall, "The CUDA libraries could not be copied into the build.", err, "cause", "redist_libs")
		}
	}
	if err := fsys.RemoveAll(r.plan.BuildDir); err != nil {
		r.m.log.Warn("could not delete the build tree", "dir", r.plan.BuildDir, "error", err)
	}
	return sums, nil
}

// copyFile copies a file, giving the copy exactly perm, and returns its sha256 when asked.
func copyFile(fsys FS, from, to string, perm os.FileMode, sum bool) (string, error) {
	in, err := fsys.Open(from)
	if err != nil {
		return "", err
	}
	defer in.Close()
	out, err := fsys.Create(to, perm)
	if err != nil {
		return "", err
	}
	var w io.Writer = out
	h := sha256.New()
	if sum {
		w = io.MultiWriter(out, h)
	}
	_, cerr := io.Copy(w, in)
	if err := out.Close(); cerr == nil {
		cerr = err
	}
	if cerr != nil {
		return "", cerr
	}
	if !sum {
		return "", nil
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// writeBuildJSON writes O/BUILD.json (llama-router.md section 4.10.3 step 4).
func (r *run) writeBuildJSON(ctx context.Context, sums map[string]string, smoke Smoke) (BuildInfo, *Error) {
	info := BuildInfo{
		BuildID: r.plan.BuildID, Commit: r.plan.Commit, Backend: r.plan.Backend, CudaArch: r.plan.CudaArch,
		CMakeFlags: append([]string{}, r.plan.CMakeFlags...),
		Compiler:   r.compilerVersion(ctx),
		BuiltAt:    r.m.opt.Clock().UTC().Format(time.RFC3339),
		BuiltBy:    r.m.opt.BuiltBy,
		SHA256:     sums,
		Smoke:      smoke,
	}
	if r.plan.Backend == BackendCUDA {
		info.NVCC = r.nvccVersion(ctx)
	}
	raw, err := json.MarshalIndent(info, "", "  ")
	if err == nil {
		err = r.m.opt.FS.WriteFile(filepath.Join(r.plan.OutDir, buildJSON), append(raw, '\n'), 0o644)
	}
	if err != nil {
		return BuildInfo{}, newErr(CodeBuildFailed, StageInstall, "BUILD.json could not be written.", err, "cause", "filesystem")
	}
	return info, nil
}

// version runs `<tool> --version` and returns its output; "" on any failure (BUILD.json says
// "unknown" then: the versions are information, never a reason to fail a good build).
func (r *run) version(ctx context.Context, path string) string {
	res, err := r.m.opt.Exec.Run(ctx, execx.Cmd{Path: path, Args: []string{"--version"}, Timeout: 30 * time.Second, MaxOut: 64 << 10, Env: toolEnv()})
	if err != nil || res.ExitCode != 0 {
		return ""
	}
	return string(res.Stdout)
}

func (r *run) compilerVersion(ctx context.Context) string {
	if r.tools.CC == "" {
		return "unknown"
	}
	if s := ParseCompiler(r.version(ctx, r.tools.CC)); s != "" {
		return s
	}
	return "unknown"
}

func (r *run) nvccVersion(ctx context.Context) string {
	if r.tools.NVCC == "" {
		return "unknown"
	}
	if s := ParseNVCC(r.version(ctx, r.tools.NVCC)); s != "" {
		return s
	}
	return "unknown"
}

var (
	versionNumRe = regexp.MustCompile(`[0-9]+\.[0-9]+(\.[0-9]+)?`)
	nvccVerRe    = regexp.MustCompile(`V([0-9]+\.[0-9]+\.[0-9]+)`)
	nvccRelRe    = regexp.MustCompile(`release ([0-9]+\.[0-9]+)`)
)

// ParseCompiler turns the first line of `cc --version` into BUILD.json's compiler field: "GNU 15.2.0"
// or "Clang 17.0.6". A line it does not recognise comes back as it is; "" for no output.
func ParseCompiler(out string) string {
	line := strings.TrimSpace(strings.SplitN(out, "\n", 2)[0])
	if line == "" {
		return ""
	}
	lower := strings.ToLower(line)
	version := ""
	if i := strings.Index(lower, "version "); i >= 0 {
		version = versionNumRe.FindString(line[i+len("version "):])
	}
	if version == "" {
		if all := versionNumRe.FindAllString(line, -1); len(all) > 0 {
			version = all[len(all)-1]
		}
	}
	family := ""
	switch {
	case strings.Contains(lower, "clang"):
		family = "Clang"
	case strings.Contains(lower, "gcc") || strings.Contains(lower, "g++") || strings.HasPrefix(lower, "cc ") || strings.HasPrefix(lower, "c++ "):
		family = "GNU"
	}
	if family == "" || version == "" {
		return line
	}
	return family + " " + version
}

// ParseNVCC returns the CUDA compiler version from `nvcc --version` ("12.4.131"; "12.4" when only
// the release is printed), "" when it is not there.
func ParseNVCC(out string) string {
	if m := nvccVerRe.FindStringSubmatch(out); m != nil {
		return m[1]
	}
	if m := nvccRelRe.FindStringSubmatch(out); m != nil {
		return m[1]
	}
	return ""
}

// place renames O to builds/<id>; an existing directory of that id is first renamed to
// <id>.old-<ts>, the only move-aside name (llama-router.md section 4.10.3 step 5). It returns the
// move-aside name, "" when there was nothing to move.
func (r *run) place() (oldDir string, e *Error) {
	fsys := r.m.opt.FS
	fail := func(msg string, err error) *Error {
		return newErr(CodeBuildFailed, StageSwitch, msg, err, "cause", "filesystem", "build_id", r.plan.BuildID)
	}
	if _, err := fsys.Lstat(r.plan.FinalDir); err == nil {
		ts := r.m.opt.Clock().UTC().Format("20060102T150405Z")
		oldDir = r.plan.FinalDir + infixOld + ts
		for i := 1; i < 10; i++ {
			if _, err := fsys.Lstat(oldDir); err != nil {
				break
			}
			oldDir = r.plan.FinalDir + infixOld + ts + "-" + strconv.Itoa(i)
		}
		if err := fsys.Rename(r.plan.FinalDir, oldDir); err != nil {
			return "", fail("The build with the same id could not be moved aside.", err)
		}
	}
	if err := fsys.Rename(r.plan.OutDir, r.plan.FinalDir); err != nil {
		if oldDir != "" {
			_ = fsys.Rename(oldDir, r.plan.FinalDir) // put the old build back
		}
		return "", fail("The new build could not be moved into place.", err)
	}
	return oldDir, nil
}

// currentTarget returns what the current link points at ("builds/<id>"), "" when there is no link.
// A current that is not a symlink cannot be switched atomically and is an error.
func (m *Manager) currentTarget() (string, error) {
	target, err := m.opt.FS.Readlink(filepath.Join(m.root, currentLink))
	if err == nil {
		return target, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	return "", err
}

// buildName is the directory name of a link target ("builds/<id>" or an absolute path into builds),
// "" for anything else.
func buildName(target string) string {
	if target == "" {
		return ""
	}
	return filepath.Base(filepath.Clean(target))
}

// auditSwitch writes llama.build.switch.
func (m *Manager) auditSwitch(req Request, result audit.Result, code string, target map[string]string) {
	if m.opt.Audit == nil {
		return
	}
	actor := req.Actor
	if actor.Kind == "" {
		actor = audit.CLIRoot()
	}
	if err := m.opt.Audit.Write(audit.Event{Action: audit.ActionLlamaBuildSwitch, Result: result, Code: code, Actor: actor, Target: target}); err != nil {
		m.log.Warn("could not write the audit line", "action", audit.ActionLlamaBuildSwitch, "error", err)
	}
}
