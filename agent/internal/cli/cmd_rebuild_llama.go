package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/cli/local"
	"github.com/marinoscar/local-ai/agent/internal/cli/out"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/kit"
	"github.com/marinoscar/local-ai/agent/internal/router/build"
)

// `aictl rebuild-llama` (aictl.md section 4.23, llama-router.md section 4.10, rebuild-kit.md section
// 4.3, AICTL-09). It runs the build manager of internal/router/build in this process, as root, so a
// rebuild works when the agent does not: the pinned llama.cpp commit becomes
// /opt/localai/llama.cpp/builds/<commit9>-<backend>, is tested, `current` is switched to it and llama
// is restarted through the agent (or systemctl when the agent is down). If the new build does not
// serve (/health within 60 s, then the default model loaded and 16 tokens within 180 s) the engine
// switches back and the command ends with LAI-LLM-019.
//
// This file is the command surface: flags, the confirmation, the offer of a ready-made build, the
// words and the exit codes. cmd_rebuild_llama_wire.go holds the seams to the machine (the agent, the
// router, node.yaml, state.json, the smoke model, the Ctrl-C question). The work is the engine's.

const (
	// rebuildLlamaTimeout is the default deadline (aictl.md section 4.23 says 3 h, the engine's own
	// build timeout).
	rebuildLlamaTimeout = 3 * time.Hour
	// llamaMaxKeep refuses a nonsensical --keep (the engine's default is 2).
	llamaMaxKeep = 20
)

var llamaBackends = []string{build.BackendCUDA, build.BackendVulkan, build.BackendCPU}

var (
	llamaCommitRe   = regexp.MustCompile(`^[0-9a-f]{7,40}$`)
	llamaCudaArchRe = regexp.MustCompile(`^[0-9]{2,3}(;[0-9]{2,3})*$`)
)

var llamaPrebuiltCmd = prebuiltCmd{Program: kit.ProgramLlama, Label: "llama.cpp", Command: "rebuild-llama"}

func init() {
	Register(&Command{
		Name:    "rebuild-llama",
		Summary: "Rebuild the AI engine (llama.cpp) from the pinned source, even with no internet.",
		Usage: "[--commit <sha>] [--backend cuda|vulkan|cpu] [--cuda-arch <list>] [--source <dir> | --from-kit | --prebuilt <file>] " +
			"[--jobs <n>] [--activate | --no-activate] [--keep <n>] [--dry-run] [--yes]",
		Description: `Builds llama.cpp from the commit pinned in node.yaml (llama.commit), tests the new build with
the smallest model on this computer, and only then switches /opt/localai/llama.cpp/current to it and
restarts the AI model service. If the new build does not start or does not answer, the previous
build is put back and the command ends with LAI-LLM-019. It is the way back after a driver change or
a broken build, and it works when the agent is down.
  --commit <sha>      Build this commit (7 to 40 hex characters) instead of llama.commit.
  --backend <name>    cuda, vulkan or cpu (default: llama.backend of node.yaml).
  --cuda-arch <list>  Compute capabilities without the dot, separated by semicolons: "75" or "75;86"
                      (quote it in the shell). Default: llama.cuda_arch, else detected with nvidia-smi.
  --source <dir>      Build from this folder instead of the usual sources.
  --from-kit          Use only the rebuild kit (its source tarball, else its git mirror): no internet.
  --prebuilt <file>   Restore a ready-made build from kit/prebuilt/ (made by "aictl kit export-build"
                      on this computer) instead of compiling: it takes a minute. It is refused unless
                      it was made for this Ubuntu version, processor, backend and graphics chip.
  --jobs <n>          At most this many compile jobs (never more than the processors, or than half the
                      free memory in GiB).
  --activate          Switch to the new build when it passes its test (the default).
  --no-activate       Build and test it, but do not switch to it.
  --keep <n>          Builds to keep besides the one in use (default 2: the previous one and the
                      newest other). The previous build is never deleted.
  --dry-run           Only check that everything needed is there; change nothing. Exit 0 only if every
                      check passes, and a missing tool comes with the exact "aictl kit install-debs"
                      command that installs it from the kit.
  --yes               Do not ask for confirmation.
The source is, in this order, the tree already in /opt/localai/llama.cpp/src/<commit>/, the kit's
tarball of the commit, the kit's git mirror and, only when the node is online, GitHub. With no internet
(offline or survival mode, or --from-kit) and no local source the command stops with LAI-KIT-002. When
the kit holds a ready-made build that fits this computer exactly, you are asked whether to use it
instead of compiling. The new build is exported to the kit when it is switched on. Ctrl-C asks whether
to stop the build; stopping leaves the build in use untouched. Run it with sudo.
Specification: docs/specs/aictl.md section 4.23, docs/specs/llama-router.md section 4.10, docs/specs/rebuild-kit.md section 4.3`,
		Marks:   MarkLocal | MarkRoot,
		Timeout: rebuildLlamaTimeout,
		Flags: func(fs *flag.FlagSet) {
			fs.String("commit", "", "build this commit (7 to 40 hex characters)")
			fs.String("backend", "", "cuda, vulkan or cpu")
			fs.String("cuda-arch", "", `compute capabilities without the dot, e.g. "75;86"`)
			fs.String("source", "", "build from this folder")
			fs.Bool("from-kit", false, "use only the rebuild kit as the source")
			fs.String("prebuilt", "", "restore this ready-made build from the kit instead of compiling")
			fs.Int("jobs", 0, "at most this many compile jobs")
			fs.Bool("activate", false, "switch to the new build when it passes its test (the default)")
			fs.Bool("no-activate", false, "build and test it without switching to it")
			fs.Int("keep", 0, "builds to keep besides the one in use (default 2)")
			fs.Bool("dry-run", false, "check what is needed and change nothing")
			declareYes(fs)
		},
		Run: rebuildLlama,
	})
}

// ---- the seams of the command ---------------------------------------------------------------------

// llamaEngine is the part of *build.Manager the command uses; tests replace it.
type llamaEngine interface {
	Resolve(ctx context.Context, req build.Request) (build.Inputs, error)
	Build(ctx context.Context, req build.Request) (build.Result, error)
}

// llamaNewEngine makes the engine from the options the command assembled.
var llamaNewEngine = func(opt build.Options) llamaEngine { return build.New(opt) }

// llamaCompileUser is Options.CompileUser; tests set build.CompileAsCaller.
var llamaCompileUser = ""

// ---- the command ----------------------------------------------------------------------------------

func rebuildLlama(c *Call) error {
	if err := noExtraArgs(c); err != nil {
		return err
	}
	applyYes(c)
	req, err := llamaRequest(c)
	if err != nil {
		return err
	}
	if err := RequireRoot(c); err != nil {
		return err
	}
	env, host := localEnv(), kitHostFn()
	set := readLlamaSettings(env)

	// No kit anywhere is not an error here: the tree may already be extracted under
	// /opt/localai/llama.cpp/src, and when nothing has the commit the engine says LAI-KIT-002.
	var kitPaths []string
	roots, _ := kitRoots(c, env, "")
	for _, r := range roots {
		kitPaths = append(kitPaths, r.Path)
	}
	release, err := kitLock(c, env)
	if err != nil {
		return err
	}
	defer release()

	// The node's mode decides whether GitHub may be used: offline and survival never touch the
	// network, so a commit that is nowhere on this computer is LAI-KIT-002 and not a failed clone.
	mode := ""
	if !req.FromKit && req.SourceDir == "" {
		if mode = nodeMode(c, env); mode != "" && mode != "online" {
			req.Offline = true
		}
	}

	pr := newLlamaPrinter(c)
	opt := llamaOptions(c, env, host, set, roots, kitPaths, req, pr)
	eng := llamaNewEngine(opt)
	req.Progress = pr.progress
	req.Output = pr.compileOutput()

	if req.Prebuilt != "" {
		return llamaRestore(c, eng, host, env, req, pr)
	}
	if req.DryRun {
		return llamaDryRun(c, eng, req)
	}

	in, err := eng.Resolve(c.Ctx, req)
	if err != nil {
		return llamaErr(c, err)
	}
	if !c.Global.JSON {
		pr.line(llamaPlanLine(c.Lang, in))
		if req.Offline && !req.FromKit {
			c.Out.Info(fmt.Sprintf(pick(c.Lang,
				"This computer is in %s mode: only the source on this computer and in the rebuild kit is used, never the internet.",
				"Este equipo está en modo %s: solo se usa el código fuente de este equipo y del kit de reconstrucción, nunca internet."), modeWord(c.Out, mode)))
		}
	}
	// A ready-made build that fits this computer is offered before anything is compiled.
	if req.SourceDir == "" {
		if file, ok := offerPrebuilt(c, host, contentRootPaths(c.Ctx, env), kit.ProgramLlama, "rebuild-llama", in); ok {
			req.Prebuilt = file
			return llamaRestore(c, eng, host, env, req, pr)
		}
	}
	ok, cerr := c.Confirm(llamaConfirmPrompt(c.Lang, in, req))
	if cerr != nil {
		return cerr
	}
	if !ok {
		c.Out.Info(pick(c.Lang, "Nothing was changed.", "No se cambió nada."))
		return ExitWith(ExitRefused)
	}
	res, err := llamaBuild(c, eng, req, pr, env)
	if err != nil {
		return err
	}
	return llamaDone(c, res, req)
}

// llamaBuild runs the engine under the Ctrl-C question and the heartbeat, and maps its error. A build
// the person stopped is exit 130 with the build in use untouched.
func llamaBuild(c *Call, eng llamaEngine, req build.Request, pr *llamaPrinter, env local.Env) (build.Result, error) {
	hb := pr.startHeartbeat(llamaBuildsDir(kitHostFn()))
	var res build.Result
	stopped, err := c.guardInterrupt(pr, func(ctx context.Context) error {
		var berr error
		res, berr = eng.Build(ctx, req)
		return berr
	})
	hb()
	pr.endCompile()
	if stopped && err != nil {
		return res, llamaStopped(c)
	}
	if err != nil {
		return res, llamaErr(c, err)
	}
	return res, nil
}

// llamaStopped is the end of a build the person stopped with Ctrl-C: nothing in use changed (the engine
// puts `current` back when the stop came during the switch).
func llamaStopped(c *Call) error {
	msg := pick(c.Lang,
		"The build was stopped. Nothing was changed: the build in use is the same as before. Run the same command again to carry on where it stopped.",
		"Se detuvo la compilación. No se cambió nada: se sigue usando la misma versión de antes. Ejecute el mismo comando otra vez para continuar donde se detuvo.")
	if c.Global.JSON {
		return c.Err(CodeInterrupted, msg).WithCause(context.Canceled)
	}
	c.Out.Info(msg)
	return ExitWith(ExitInterrupted)
}

// ---- the flags ------------------------------------------------------------------------------------

// llamaRequest reads and checks the flags (before anything is touched). Empty --commit, --backend and
// --cuda-arch take the node's values inside the engine.
func llamaRequest(c *Call) (build.Request, error) {
	var req build.Request
	req.Commit = strings.ToLower(strings.TrimSpace(c.String("commit")))
	if req.Commit != "" && !llamaCommitRe.MatchString(req.Commit) {
		return req, c.usageErr(
			pick(c.Lang, "The commit "+quote(req.Commit)+" must be 7 to 40 lowercase hex characters.",
				"El commit "+quote(req.Commit)+" debe tener de 7 a 40 caracteres hexadecimales en minúscula."),
			pick(c.Lang, "Copy it from `git log`, or leave --commit out to use llama.commit of node.yaml.",
				"Cópielo de `git log`, o quite --commit para usar llama.commit de node.yaml."))
	}
	req.Backend = strings.TrimSpace(c.String("backend"))
	if req.Backend != "" && !contains(llamaBackends, req.Backend) {
		return req, c.usageErr(
			pick(c.Lang, "The backend "+quote(req.Backend)+" is not one of cuda, vulkan or cpu.",
				"El backend "+quote(req.Backend)+" no es cuda, vulkan ni cpu."),
			pick(c.Lang, "Use --backend cuda, vulkan or cpu, or leave it out to use llama.backend of node.yaml.",
				"Use --backend cuda, vulkan o cpu, o quítelo para usar llama.backend de node.yaml."))
	}
	req.CudaArch = strings.TrimSpace(c.String("cuda-arch"))
	if req.CudaArch != "" {
		if !llamaCudaArchRe.MatchString(req.CudaArch) {
			return req, c.usageErr(
				pick(c.Lang, "The CUDA architecture "+quote(req.CudaArch)+" must look like 75 or 75;86: numbers without the dot, separated by semicolons.",
					"La arquitectura CUDA "+quote(req.CudaArch)+" debe ser como 75 o 75;86: números sin punto, separados por punto y coma."),
				pick(c.Lang, `Write 7.5 as 75, and quote the list in the shell: --cuda-arch "75;86".`,
					`Escriba 7.5 como 75 y ponga la lista entre comillas en la terminal: --cuda-arch "75;86".`))
		}
		if req.Backend != "" && req.Backend != build.BackendCUDA {
			return req, c.usageErr(
				pick(c.Lang, "--cuda-arch only applies to --backend cuda.", "--cuda-arch solo se aplica con --backend cuda."),
				pick(c.Lang, "Leave --cuda-arch out, or build for cuda.", "Quite --cuda-arch, o compile para cuda."))
		}
	}
	req.SourceDir = c.String("source")
	req.FromKit = c.Bool("from-kit")
	if p := c.String("prebuilt"); p != "" {
		if req.SourceDir != "" || req.FromKit {
			return req, c.usageErr(
				pick(c.Lang, "--prebuilt cannot be used with --source or --from-kit.", "--prebuilt no se puede usar con --source ni con --from-kit."),
				pick(c.Lang, "Restore a ready-made build (--prebuilt) or compile from a source (--source, --from-kit), not both.",
					"Restaure una compilación lista (--prebuilt) o compile desde una fuente (--source, --from-kit), no ambas cosas."))
		}
		abs, err := filepath.Abs(p)
		if err != nil {
			abs = p
		}
		req.Prebuilt = abs
	}
	if req.SourceDir != "" && req.FromKit {
		return req, c.usageErr(
			pick(c.Lang, "--source and --from-kit cannot be used together.", "--source y --from-kit no se pueden usar juntos."),
			pick(c.Lang, "Choose one source: a folder (--source) or the rebuild kit (--from-kit).",
				"Elija una sola fuente: una carpeta (--source) o el kit de reconstrucción (--from-kit)."))
	}
	if c.Bool("activate") && c.Bool("no-activate") {
		return req, c.usageErr(
			pick(c.Lang, "--activate and --no-activate cannot be used together.", "--activate y --no-activate no se pueden usar juntos."),
			pick(c.Lang, "Use --no-activate to only build and test, or leave both out.", "Use --no-activate para solo compilar y probar, o quite ambos."))
	}
	req.NoActivate = c.Bool("no-activate")
	req.DryRun = c.Bool("dry-run")
	if f := c.Flags.Lookup("jobs"); f != nil && f.Value.String() != "0" {
		n, err := strconv.Atoi(f.Value.String())
		if err != nil || n < 1 {
			return req, c.usageErr(
				pick(c.Lang, "--jobs must be a whole number of 1 or more.", "--jobs debe ser un número entero de 1 o más."),
				pick(c.Lang, "Leave --jobs out to use as many as the processors and the memory allow.",
					"Quite --jobs para usar tantos como permitan los procesadores y la memoria."))
		}
		req.Jobs = n
	}
	if f := c.Flags.Lookup("keep"); f != nil && f.Value.String() != "0" {
		n, err := strconv.Atoi(f.Value.String())
		if err != nil || n < 1 || n > llamaMaxKeep {
			return req, c.usageErr(
				pick(c.Lang, fmt.Sprintf("--keep must be a whole number from 1 to %d.", llamaMaxKeep),
					fmt.Sprintf("--keep debe ser un número entero de 1 a %d.", llamaMaxKeep)),
				pick(c.Lang, "Leave --keep out to keep the previous build and the newest other one.",
					"Quite --keep para conservar la versión anterior y la otra más reciente."))
		}
		req.Keep = n
	}
	// The kit alone is the offline mode: nothing may use the network, and a commit the kit lacks is
	// LAI-KIT-002. The node's mode adds the same rule later.
	req.Offline = req.FromKit
	return req, nil
}

// ---- the words ------------------------------------------------------------------------------------

// llamaPlanLine is "Building llama.cpp f3586e66b for cuda (sm 75), 5 parallel jobs". No duration is
// promised: the specs give none that holds for every backend and machine (the "Compiling (this takes
// a while)" line that follows says it is long).
func llamaPlanLine(l Lang, in build.Inputs) string {
	return fmt.Sprintf(pick(l, "Building llama.cpp %s for %s%s, %d parallel jobs",
		"Compilando llama.cpp %s para %s%s, %d trabajos en paralelo"),
		in.ShortCommit, in.Backend, sm(in), in.Jobs)
}

func llamaConfirmPrompt(l Lang, in build.Inputs, req build.Request) string {
	if req.NoActivate {
		return fmt.Sprintf(pick(l,
			"Build llama.cpp %s for %s? This takes a while; the model service is not restarted.",
			"¿Compilar llama.cpp %s para %s? Tarda un rato; el servicio de modelos no se reinicia."), in.ShortCommit, in.Backend)
	}
	return fmt.Sprintf(pick(l,
		"Build llama.cpp %s for %s and switch the model service to it? The service restarts and the models load again; this takes a while.",
		"¿Compilar llama.cpp %s para %s y cambiar el servicio de modelos a esa versión? El servicio se reinicia y los modelos se cargan otra vez; tarda un rato."),
		in.ShortCommit, in.Backend)
}

var deviceMemRe = regexp.MustCompile(`\s*\([^()]*MiB[^()]*\)\s*$`)

// llamaDevices is what the smoke test saw, for the line "passed its test (CUDA0: NVIDIA ...)": the
// device lines without the memory figures.
func llamaDevices(devices []string) string {
	var names []string
	for _, d := range devices {
		if d = strings.TrimSpace(deviceMemRe.ReplaceAllString(d, "")); d != "" {
			names = append(names, d)
		}
	}
	return strings.Join(names, "; ")
}

func llamaDone(c *Call, res build.Result, req build.Request) error {
	if c.Global.JSON {
		return c.Out.JSON().Document(res)
	}
	for _, w := range res.Warnings {
		c.Out.Warn(w)
	}
	tested := fmt.Sprintf(pick(c.Lang, "Build %s passed its test", "La versión %s superó su prueba"), res.BuildID)
	if d := llamaDevices(res.BuildJSON.Smoke.Devices); d != "" {
		tested += " (" + d + ")"
	}
	tested += "."
	switch {
	case res.Switched:
		c.Out.OK(tested)
		line := pick(c.Lang, "Switched to the new build.", "Se cambió a la nueva versión.")
		if res.DefaultModel != "" {
			name := res.DefaultModel
			if cl, err := c.Client(); err == nil {
				name = c.modelNames(cl).of(res.DefaultModel)
			}
			line += " " + fmt.Sprintf(pick(c.Lang, "%s is loaded again.", "%s está cargado otra vez."), name)
		}
		c.Out.OK(line)
	default:
		c.Out.OK(tested + " " + pick(c.Lang,
			"It was not switched on (--no-activate): the build in use did not change.",
			"No se activó (--no-activate): se sigue usando la versión de antes."))
	}
	return nil
}

// ---- --dry-run ------------------------------------------------------------------------------------

// llamaDryRun prints every check of the preflight and exits 0 only when all pass.
func llamaDryRun(c *Call, eng llamaEngine, req build.Request) error {
	res, err := eng.Build(c.Ctx, req)
	rep := res.Preflight
	if rep == nil {
		return llamaErr(c, err) // the request could not even be checked
	}
	if c.Global.JSON {
		if err != nil {
			return llamaErr(c, err)
		}
		return c.Out.JSON().Document(rep)
	}
	for _, ch := range rep.Checks {
		line := ch.Title + ": " + ch.Detail
		if ch.OK {
			c.Out.OK(line)
			continue
		}
		fix := ""
		if _, f, ok := doctor.Text(ch.Code, string(c.Lang)); ok {
			fix = sentence(f)
		}
		c.Out.Finding(out.Problem{Severity: sevError, Code: ch.Code, Message: sentence(line), Fix: fix})
	}
	if rep.InstallCommand != "" {
		c.Out.Info(pick(c.Lang, "To install what is missing from the kit, run: ", "Para instalar lo que falta desde el kit, ejecute: ") + "`" + rep.InstallCommand + "`")
	}
	if err != nil {
		c.Out.Blank()
		c.Out.Info(pick(c.Lang,
			"Not ready: fix the checks marked above and run `sudo aictl rebuild-llama --dry-run` again.",
			"No está listo: corrija las comprobaciones marcadas arriba y ejecute `sudo aictl rebuild-llama --dry-run` otra vez."))
		return ExitWith(ExitFailed)
	}
	c.Out.OK(pick(c.Lang, "Everything needed to rebuild llama.cpp is in place.", "Todo lo necesario para recompilar llama.cpp está listo."))
	return nil
}

// ---- --prebuilt -----------------------------------------------------------------------------------

// llamaRestore restores a ready-made build (--prebuilt, or the one the operator accepted when it was
// offered): the file's record must fit this computer (LAI-KIT-048 otherwise), and then the engine
// verifies BUILD.json and every program, smoke tests the build, switches to it and rolls back when
// llama does not serve. With --dry-run it only verifies.
func llamaRestore(c *Call, eng llamaEngine, host kitHost, env local.Env, req build.Request, pr *llamaPrinter) error {
	plan, err := prepareRestore(c, llamaPrebuiltCmd, eng, host, contentRootPaths(c.Ctx, env), req, llamaErr)
	if err != nil {
		return err
	}
	if plan.Req.DryRun {
		res, err := eng.Build(c.Ctx, plan.Req)
		if err != nil {
			return llamaErr(c, err)
		}
		if c.Global.JSON {
			return c.Out.JSON().Document(res)
		}
		c.Out.OK(fmt.Sprintf(pick(c.Lang,
			"The ready-made build %s is intact and fits this computer. Nothing was changed.",
			"La compilación lista %s está intacta y corresponde a este equipo. No se cambió nada."), filepath.Base(req.Prebuilt)))
		return nil
	}
	res, err := llamaBuild(c, eng, plan.Req, pr, env)
	if err != nil {
		return err
	}
	if !c.Global.JSON {
		c.Out.Info(fmt.Sprintf(pick(c.Lang, "Restored from %s; nothing was compiled.", "Restaurada desde %s; no se compiló nada."), filepath.Base(req.Prebuilt)))
	}
	return llamaDone(c, res, plan.Req)
}

// ---- errors ---------------------------------------------------------------------------------------

// llamaErr turns what the engine returns into the command's error: its doctor code, the engine's plain
// sentence (the code's own title in Spanish), the code's fix, and the log of a failed build. Errors that
// are not the engine's (a cancelled context) are returned as they are.
//
// Exit codes: 1 for every failure of the build and of the switch (LAI-LLM-018, LAI-LLM-019, LAI-KIT-001,
// 002, 010 to 014, LAI-HW-019), 6 for a busy agent (LAI-AGT-022) that stayed busy for a minute.
func llamaErr(c *Call, err error) error {
	var be *build.Error
	if !errors.As(err, &be) {
		return err
	}
	exit := ExitFailed
	if be.Code == doctor.AGT022 {
		exit = ExitRefused
	}
	e := c.codeError(be.Code, exit, "")
	if c.Lang != LangES || e.Message == "" {
		e.Message = be.Message
	}
	switch be.Code {
	case doctor.KIT002:
		e.Fix = pick(c.Lang, "Sync the kit with internet, or choose a commit that is in the kit.",
			"Sincronice el kit con internet, o elija una versión que esté en el kit.")
	case doctor.LLM018:
		e.Fix = pick(c.Lang, "Read the build log shown and install the missing package named there, then run `sudo aictl rebuild-llama --dry-run`.",
			"Lea el registro de compilación mostrado e instale el paquete que falta, y ejecute `sudo aictl rebuild-llama --dry-run`.")
	case doctor.LLM019:
		if rb, ok := be.Details["rolled_back"].(bool); ok && !rb {
			e.Fix = pick(c.Lang, "The previous build could not be put back. Run `sudo aictl rebuild-llama` again, or `sudo aictl restart llama`, and report the log.",
				"No se pudo volver a la versión anterior. Ejecute `sudo aictl rebuild-llama` otra vez, o `sudo aictl restart llama`, e informe del registro.")
		}
	}
	detail := []any{"stage", string(be.Stage)}
	for _, k := range []string{"cause", "build_id", "commit", "backend", "previous_build", "rolled_back", "log_path", "packages", "install_command",
		"failed_checks", "check", "op_id", "exit_code", "restart_error", "missing"} {
		if v, ok := be.Details[k]; ok {
			detail = append(detail, k, v)
		}
	}
	e = e.WithDetails(detail...).WithCause(be)
	if cmd, _ := be.Details["install_command"].(string); cmd != "" && c.Lang == LangES {
		e.Message = strings.TrimSpace(e.Message) + " " + fmt.Sprintf("Ejecute: `%s`.", cmd)
	}
	if p, ok := be.Details["log_path"].(string); ok && p != "" && !c.Global.JSON {
		e.Message = strings.TrimSpace(e.Message) + " " + fmt.Sprintf(pick(c.Lang, "The log is in %s.", "El registro está en %s."), p)
	}
	return e
}

// llamaSlogger is the engine's logger: nothing, unless -v, which shows its component=router.build lines
// on stderr. The engine's log is for the journal of the agent, and a rebuild has no journal.
func llamaSlogger(c *Call, w io.Writer) *slog.Logger {
	if !c.Global.Verbose {
		return slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: slog.LevelInfo}))
}
