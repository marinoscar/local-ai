package cli

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/marinoscar/local-ai/agent/internal/cli/local"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/kit"
	"github.com/marinoscar/local-ai/agent/internal/router/build"
)

// `aictl rebuild-llama` with no backend flags picks its own fallbacks (issue #433, aictl.md section
// 4.23, rebuild-kit.md section 4.3.4): a person rebuilding a node offline should not have to choose
// between --prebuilt, --backend vulkan, --cuda-arch and --jobs by reading codes. The options are tried
// in this order, each only when the one before it failed in a way the next one can get around:
//
//  1. the ready-made build in the kit that fits this computer exactly (rebuild-kit.md section 4.2);
//  2. building on the node's backend (llama.backend of node.yaml);
//  3. building for Vulkan (when that backend was cuda);
//  4. building for the processor (cpu), which always works.
//
// Before moving to the next option a person at a terminal is asked once "That did not work (<code>).
// Try <next>? [Y/n]"; with --yes, --json or no terminal the command goes on by itself. A compile killed
// for lack of memory is tried once more with half the parallel jobs, and the command says so. Any flag
// that chooses (--backend, --cuda-arch, --jobs, --source, --prebuilt) keeps the single build of before.

// llamaAutoMode reports whether the command chooses its own fallbacks: no flag chose the backend, the
// CUDA architecture, the jobs or the source (--prebuilt and --dry-run never get here).
func llamaAutoMode(req build.Request) bool {
	return req.Backend == "" && req.CudaArch == "" && req.Jobs == 0 && req.SourceDir == ""
}

// llamaStep is one option of the chain: a ready-made build (Prebuilt set) or a build for Backend.
type llamaStep struct {
	Prebuilt string
	Backend  string
}

// llamaChain is the list of options for a node whose backend is primary.
func llamaChain(primary, prebuilt string) []llamaStep {
	var steps []llamaStep
	if prebuilt != "" {
		steps = append(steps, llamaStep{Prebuilt: prebuilt})
	}
	steps = append(steps, llamaStep{Backend: primary})
	if primary == build.BackendCUDA {
		steps = append(steps, llamaStep{Backend: build.BackendVulkan})
	}
	if primary != build.BackendCPU {
		steps = append(steps, llamaStep{Backend: build.BackendCPU})
	}
	return steps
}

// stepText names an option in a sentence: "building for Vulkan (works with most graphics chips)".
func stepText(l Lang, s llamaStep) string {
	if s.Prebuilt != "" {
		return fmt.Sprintf(pick(l, "the ready-made build from the kit (%s)", "la compilación lista del kit (%s)"), filepath.Base(s.Prebuilt))
	}
	switch s.Backend {
	case build.BackendCUDA:
		return pick(l, "building for the NVIDIA graphics chip (cuda)", "compilar para el chip gráfico NVIDIA (cuda)")
	case build.BackendVulkan:
		return pick(l, "building for Vulkan (works with most graphics chips)", "compilar para Vulkan (funciona con la mayoría de los chips gráficos)")
	}
	return pick(l, "building for the processor only (cpu: slower answers, but it always works)",
		"compilar solo para el procesador (cpu: respuestas más lentas, pero siempre funciona)")
}

// stepShort names an option in the list of the chain: "Vulkan".
func stepShort(l Lang, s llamaStep) string {
	if s.Prebuilt != "" {
		return pick(l, "the ready-made build", "la compilación lista")
	}
	switch s.Backend {
	case build.BackendCUDA:
		return "cuda"
	case build.BackendVulkan:
		return "Vulkan"
	}
	return pick(l, "the processor only (cpu)", "solo el procesador (cpu)")
}

// genericTools are the build tools every backend needs: when one of them is missing another backend
// cannot help.
var genericTools = map[string]bool{"gcc": true, "g++": true, "cc": true, "c++": true, "cmake": true, "ninja": true, "pkg-config": true, "git": true}

// llamaFallbackable reports whether err (an engine error) is a failure the next option can get
// around: the backend's own tools, driver or device, a compile that failed, a build that did not
// pass its test and was rolled back. A source the kit lacks or that is damaged, a full disk, missing
// generic tools, a busy agent, a lock, a stop and a rollback that failed end the command.
func llamaFallbackable(err error) bool {
	var be *build.Error
	if !errors.As(err, &be) {
		return false
	}
	switch be.Code {
	case doctor.HW019, doctor.KIT011, doctor.KIT012, doctor.KIT013:
		return true
	case doctor.LLM019:
		rb, ok := be.Details["rolled_back"].(bool)
		return !ok || rb
	case doctor.LLM018:
		switch be.Details["cause"] {
		case "exit", "timeout", "exec":
			return true
		case "missing_tools":
			missing, _ := be.Details["missing"].([]string)
			for _, m := range missing {
				if genericTools[m] {
					return false
				}
			}
			return len(missing) > 0
		}
	}
	return false
}

// oomRe finds, in the end of a build log, the compiler killed for lack of memory: gcc's "Killed signal
// terminated program cc1plus", nvcc's "'cicc' died due to signal 9", and the allocators' own words.
var oomRe = regexp.MustCompile(`(?i)killed signal terminated program|internal compiler error: killed|died due to signal 9|signal 9 \(kill|virtual memory exhausted|cannot allocate memory|out of memory allocating|std::bad_alloc`)

// llamaOutOfMemory reports whether a failed compile ran out of memory.
func llamaOutOfMemory(err error) bool {
	var be *build.Error
	if !errors.As(err, &be) || be.Code != doctor.LLM018 || be.Stage != build.StageBuild {
		return false
	}
	tail, _ := be.Details["log_tail"].(string)
	return oomRe.MatchString(tail)
}

// errCodeOf is the doctor code of an engine or command error ("" when it has none).
func errCodeOf(err error) string {
	var be *build.Error
	if errors.As(err, &be) {
		return be.Code
	}
	var ce *CLIError
	if errors.As(err, &ce) {
		return ce.Code
	}
	return ""
}

// askDefaultYes asks a question whose default is Yes ("[Y/n]"): Enter, a yes and the end of the input
// answer true; anything else is No. --yes, --json and no terminal answer true without asking (the
// command goes on by itself). Ctrl-C ends the command (exit 130).
func (c *Call) askDefaultYes(prompt string) (bool, error) {
	if c.Global.Yes || c.Global.JSON || !c.Term.Interactive() {
		return true, nil
	}
	fmt.Fprint(c.Stderr, prompt+pick(c.Lang, " [Y/n] ", " [S/n] "))
	line, err := c.Term.ReadLine(c.Ctx)
	if err != nil {
		if e := c.endPrompt(err); e != nil {
			return false, e
		}
		return true, nil
	}
	return strings.TrimSpace(line) == "" || isYes(c.Lang, line), nil
}

// llamaFindPrebuilt is the ready-made build in the kit that fits in exactly ("" when there is none).
func llamaFindPrebuilt(host kitHost, roots []string, in build.Inputs) string {
	want, err := prebuiltWant(host, kit.ProgramLlama, in.Commit, in.Backend, in.CudaArch)
	if err != nil {
		return ""
	}
	f, err := kit.SelectPrebuilt(host.FS, roots, want)
	if err != nil {
		return ""
	}
	return f.Path
}

// stepOutcome is how one option of the chain ended.
type stepOutcome int

const (
	// stepDone: the command is over, with the error given (nil on success).
	stepDone stepOutcome = iota
	// stepFailed: the option failed in a way the next option can get around.
	stepFailed
	// stepDeclined: the person did not want the ready-made build; compiling comes next, with its
	// own confirmation.
	stepDeclined
)

// llamaAuto runs the chain. mode is the node's mode, for the line that says the internet is not used.
func llamaAuto(c *Call, eng llamaEngine, host kitHost, env local.Env, req build.Request, pr *llamaPrinter, mode string) error {
	in, rerr := eng.Resolve(c.Ctx, req)
	primary := in.Backend
	if rerr != nil {
		// A CUDA node whose graphics chip cannot be read can still build for Vulkan or the processor.
		if errCodeOf(rerr) != doctor.HW019 {
			return llamaErr(c, rerr)
		}
		primary = build.BackendCUDA
	}
	prebuilt := ""
	if rerr == nil {
		prebuilt = llamaFindPrebuilt(host, contentRootPaths(c.Ctx, env), in)
	}
	steps := llamaChain(primary, prebuilt)
	if !c.Global.JSON {
		if rerr == nil {
			pr.line(llamaPlanLine(c.Lang, in))
		}
		if req.Offline && !req.FromKit {
			c.Out.Info(fmt.Sprintf(pick(c.Lang,
				"This computer is in %s mode: only the source on this computer and in the rebuild kit is used, never the internet.",
				"Este equipo está en modo %s: solo se usa el código fuente de este equipo y del kit de reconstrucción, nunca internet."), modeWord(c.Out, mode)))
		}
		if len(steps) > 1 {
			var names []string
			for _, s := range steps {
				names = append(names, stepShort(c.Lang, s))
			}
			c.Out.Info(fmt.Sprintf(pick(c.Lang, "It tries, in order: %s. It stops at the first that works.",
				"Prueba, por orden: %s. Se detiene en la primera que funcione."), strings.Join(names, "; ")))
		}
	}

	var last error     // the command's error for the last option that failed
	confirm := true    // the first build asks for the confirmation; a "Try …?" yes stands for it
	askBefore := false // a failure comes before this option
	for i, s := range steps {
		if askBefore {
			ok, err := llamaNextStep(c, last, s)
			if err != nil {
				return err
			}
			if !ok {
				return last
			}
			confirm = false
		}
		var out stepOutcome
		if s.Prebuilt != "" {
			out, last = llamaAutoRestore(c, eng, host, env, req, pr, s.Prebuilt)
		} else {
			stepReq, pre := req, (*build.Inputs)(nil)
			if i > 0 && s.Backend != primary {
				stepReq.Backend = s.Backend
			}
			if rerr == nil && s.Backend == primary {
				pre = &in
			}
			out, last = llamaAutoBuild(c, eng, stepReq, pr, pre, confirm)
		}
		switch out {
		case stepDone:
			return last
		case stepDeclined:
			askBefore = false
		case stepFailed:
			askBefore = true
		}
	}
	return last
}

// llamaNextStep says that the last option did not work and asks before the next one (or says that
// it goes on by itself).
func llamaNextStep(c *Call, last error, next llamaStep) (bool, error) {
	code := errCodeOf(last)
	if !c.Global.JSON {
		var ce *CLIError
		if errors.As(last, &ce) && ce.Message != "" {
			c.Out.Warn(fmt.Sprintf("%s [%s]", strings.TrimSpace(ce.Message), ce.Code))
		}
	}
	if c.Global.Yes || c.Global.JSON || !c.Term.Interactive() {
		c.Out.Progress(fmt.Sprintf(pick(c.Lang, "That did not work (%s). Trying %s instead.", "Eso no funcionó (%s). Se prueba %s en su lugar."),
			code, stepText(c.Lang, next)))
		return true, nil
	}
	return c.askDefaultYes(fmt.Sprintf(pick(c.Lang, "That did not work (%s). Try %s instead?", "Eso no funcionó (%s). ¿Probar %s en su lugar?"),
		code, stepText(c.Lang, next)))
}

// llamaAutoRestore restores the ready-made build of the chain.
func llamaAutoRestore(c *Call, eng llamaEngine, host kitHost, env local.Env, req build.Request, pr *llamaPrinter, file string) (stepOutcome, error) {
	interactive := !c.Global.JSON && !c.Global.Yes && c.Term.Interactive()
	if interactive {
		ok, err := c.askDefaultYes(fmt.Sprintf(pick(c.Lang,
			"The kit has a ready-made build for exactly this computer (%s). Use it instead of compiling? It takes a moment instead of a long wait.",
			"El kit tiene una compilación lista para este equipo exacto (%s). ¿Usarla en vez de compilar? Tarda un momento en vez de una larga espera."), filepath.Base(file)))
		if err != nil {
			return stepDone, err
		}
		if !ok {
			return stepDeclined, nil
		}
	} else if !c.Global.JSON {
		c.Out.Info(fmt.Sprintf(pick(c.Lang, "Using the ready-made build %s from the kit instead of compiling.",
			"Se usa la compilación lista %s del kit en vez de compilar."), filepath.Base(file)))
	}
	rq := req
	rq.Prebuilt = file
	// The person has just agreed to use this file: the restore does not ask a second time.
	yes := c.Global.Yes
	if interactive {
		c.Global.Yes = true
	}
	plan, err := prepareRestore(c, llamaPrebuiltCmd, eng, host, contentRootPaths(c.Ctx, env), rq, llamaErr)
	c.Global.Yes = yes
	if err != nil {
		switch errCodeOf(err) {
		case doctor.KIT048, doctor.KIT001:
			return stepFailed, err // it does not fit, or cannot be trusted: compile instead
		}
		return stepDone, err
	}
	res, stopped, berr := llamaBuildRaw(c, eng, plan.Req, pr)
	switch {
	case stopped && berr != nil:
		return stepDone, llamaStopped(c)
	case berr != nil && (llamaFallbackable(berr) || errCodeOf(berr) == doctor.KIT001):
		return stepFailed, llamaErr(c, berr)
	case berr != nil:
		return stepDone, llamaErr(c, berr)
	}
	if !c.Global.JSON {
		c.Out.Info(fmt.Sprintf(pick(c.Lang, "Restored from %s; nothing was compiled.", "Restaurada desde %s; no se compiló nada."), filepath.Base(file)))
	}
	return stepDone, llamaDone(c, res, plan.Req)
}

// llamaAutoBuild runs one build of the chain, with the one retry for lack of memory. pre are the
// inputs already resolved (and the plan line already printed) for the node's own backend; confirm asks
// the build's confirmation.
func llamaAutoBuild(c *Call, eng llamaEngine, req build.Request, pr *llamaPrinter, pre *build.Inputs, confirm bool) (stepOutcome, error) {
	var in build.Inputs
	if pre != nil {
		in = *pre
	} else {
		var err error
		if in, err = eng.Resolve(c.Ctx, req); err != nil {
			if llamaFallbackable(err) {
				return stepFailed, llamaErr(c, err)
			}
			return stepDone, llamaErr(c, err)
		}
		if !c.Global.JSON {
			pr.line(llamaPlanLine(c.Lang, in))
		}
	}
	if confirm {
		ok, cerr := c.Confirm(llamaConfirmPrompt(c.Lang, in, req))
		if cerr != nil {
			return stepDone, cerr
		}
		if !ok {
			c.Out.Info(pick(c.Lang, "Nothing was changed.", "No se cambió nada."))
			return stepDone, ExitWith(ExitRefused)
		}
	}
	retried := false
	for {
		res, stopped, berr := llamaBuildRaw(c, eng, req, pr)
		switch {
		case stopped && berr != nil:
			return stepDone, llamaStopped(c)
		case berr == nil:
			return stepDone, llamaDone(c, res, req)
		case !retried && in.Jobs > 1 && llamaOutOfMemory(berr):
			retried = true
			jobs := max(1, in.Jobs/2)
			if !c.Global.JSON {
				c.Out.Warn(fmt.Sprintf(pick(c.Lang,
					"The compiler ran out of memory with %d parallel jobs. Trying again with %d: slower, but it needs less memory.",
					"El compilador se quedó sin memoria con %d trabajos en paralelo. Se intenta otra vez con %d: más lento, pero necesita menos memoria."),
					in.Jobs, jobs))
			}
			req.Jobs, in.Jobs = jobs, jobs
			continue
		case llamaFallbackable(berr):
			return stepFailed, llamaErr(c, berr)
		}
		return stepDone, llamaErr(c, berr)
	}
}

// llamaBuildRaw runs the engine under the Ctrl-C question and the heartbeat and returns its error as
// it is; stopped reports that the person (or the signal) stopped it.
func llamaBuildRaw(c *Call, eng llamaEngine, req build.Request, pr *llamaPrinter) (build.Result, bool, error) {
	hb := pr.startHeartbeat(llamaBuildsDir(kitHostFn()))
	var res build.Result
	stopped, err := c.guardInterrupt(pr, func(ctx context.Context) error {
		var berr error
		res, berr = eng.Build(ctx, req)
		return berr
	})
	hb()
	pr.endCompile()
	return res, stopped, err
}
