package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/cli/out"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/router/bench"
)

// `aictl bench <model-id> [--yes]` (aictl.md section 4.24, llama-router.md section 4.13): measure the
// graphics memory and the speed of one model for the catalog. The measurement engine is
// internal/router/bench; this file is the command around it: arguments, the confirmation to unload
// what is loaded, the seams over the agent (cmd_bench_seams.go), and the human and --json output with
// the exit codes.
//
// It runs as root, in the aictl process: the engine samples the GPU in-process, runs llama-bench
// and writes the result under /var/lib/localai/bench, none of which an operator may do. The check
// comes before anything is asked or changed, not after twenty minutes of measuring.

// benchTimeout is the command deadline (aictl.md section 4.24).
const benchTimeout = 30 * time.Minute

func init() {
	Register(&Command{
		Name:    "bench",
		Summary: "Measure the memory and speed of a model, for the catalog.",
		Marks:   MarkRoot,
		Usage:   "<model-id> [--yes]",
		Description: `Unloads the loaded models, loads this one alone at its full context, measures the graphics
memory it needs and its speed with llama-bench, saves the result in /var/lib/localai/bench and prints the value
for the catalog. Then it loads the models that were loaded before. Asks before unloading (--yes skips it).
Do not run other GPU programs meanwhile. To measure a model that is only in a draft catalog, install the
draft first with "sudo aictl catalog use <dir> --test". --json prints the result file's JSON.`,
		Timeout: benchTimeout,
		Flags: func(fs *flag.FlagSet) {
			fs.Bool("yes", false, "do not ask before unloading the loaded models")
			fs.Bool("y", false, "same as --yes")
		},
		Run: runBench,
	})
}

func runBench(c *Call) error {
	applyYes(c)
	id, err := c.modelIDArg()
	if err != nil {
		return err
	}
	if err := RequireRoot(c); err != nil {
		return err
	}
	cl, err := c.Client()
	if err != nil {
		return err
	}

	// What is loaded now decides whether to ask (and what the question says). A model that is loading
	// is somebody else's operation: refuse before asking anything.
	var ms wireModels
	if _, _, err := c.callWith(cl, c.Ctx, http.MethodGet, "/models", nil, &ms); err != nil {
		return err
	}
	names := modelNameMap{}
	var loaded []string
	for _, m := range ms.Models {
		if m.DisplayName != "" {
			names[m.ID] = m.DisplayName
		}
		switch m.Status {
		case "loading":
			return c.benchBusy("")
		case "loaded", "sleeping":
			loaded = append(loaded, m.ID)
		}
	}
	// The key is read before anything is asked or changed: a node whose router address is not on
	// this computer must fail now, not after the models were unloaded.
	chat, err := c.newBenchChat(cl)
	if err != nil {
		return err
	}

	if len(loaded) > 0 && !c.Global.Yes {
		if err := c.requireTerminalForConfirm(false); err != nil {
			return err
		}
		ok, err := c.Confirm(pick(c.Lang,
			"The test needs the graphics memory to itself: "+names.list(c.Lang, loaded)+" will be unloaded and loaded again afterwards. Continue?",
			"La prueba necesita la memoria gráfica para ella sola: se descargará "+names.list(c.Lang, loaded)+" y se volverá a cargar al terminar. ¿Continuar?"))
		if err != nil || !ok {
			if err == nil {
				c.Out.Info(pick(c.Lang, "Nothing was changed.", "No se cambió nada."))
			}
			return err
		}
	}

	host := benchHostFn()
	node := readBenchNode(host)
	cpu := node.Backend == "cpu"
	agent := &benchAgent{c: c, cl: cl, target: id, names: names, cpu: cpu, say: c.Out.Progress, onEvent: host.OnEvent}
	engine, err := bench.New(bench.Deps{Agent: agent, Chat: chat, GPU: host.GPU, Procs: host.Procs, Exec: host.Exec}, bench.Options{
		NodeID: node.ID, Backend: node.Backend, Env: node.Env,
		Dir: host.Dir, BenchBinary: host.BenchBinary, BuildLink: host.BuildLink,
		Now: host.Now, Sleep: host.Sleep,
	})
	if err != nil {
		return c.codeError(doctor.LLM025, ExitFailed, err.Error()+".")
	}
	res, err := engine.Run(c.Ctx, id)
	if err != nil {
		return c.benchError(err, id)
	}
	if c.Global.JSON {
		if err := c.Out.JSON().Document(res); err != nil {
			return err
		}
	} else {
		c.benchReport(res, id, names)
	}
	c.benchRestoreWarning(res, names)
	return nil
}

// benchBusy is the refusal while another model operation runs: exit 6, nothing changed.
func (c *Call) benchBusy(opID string) error {
	e := c.codeError(doctor.AGT022, ExitRefused, "")
	if opID != "" {
		e.WithDetails("op_id", opID)
	}
	return e
}

// benchError maps what the engine returned to the error the user sees and its exit code (aictl.md
// section 4.24): a busy agent is 6, an unknown model 5, missing files 6, Ctrl-C 130, the deadline 7,
// every other failure LAI-LLM-025 and 1. The engine has put the models back either way.
func (c *Call) benchError(err error, id string) error {
	switch {
	case errors.Is(err, context.Canceled):
		c.Out.Info(pick(c.Lang,
			"Stopped. The models that were loaded before were started again.",
			"Detenido. Los modelos que estaban cargados se volvieron a iniciar."))
		return ExitWith(ExitInterrupted)
	case errors.Is(err, context.DeadlineExceeded):
		return c.Err(CodeTimeout, "").WithCause(err)
	}
	var busy *bench.BusyError
	var be *bench.Error
	if errors.As(err, &be) {
		cause := ""
		if be.Err != nil {
			cause = be.Err.Error()
		}
		switch be.Code {
		case bench.CodeBusy:
			op := ""
			if errors.As(err, &busy) {
				op = busy.OpID
			}
			return c.benchBusy(op)
		case bench.CodeUnknownModel:
			return c.codeError(doctor.AGT021, ExitNotFound, "")
		case bench.CodeFilesMissing:
			return c.codeError(doctor.LLM008, ExitRefused, "")
		}
		e := c.codeError(bench.CodeBenchFailed, ExitFailed, fmt.Sprintf("(%s: %s)", be.Step, trimPeriod(cause)))
		return e.WithDetails("model", id, "step", string(be.Step))
	}
	if errors.As(err, &busy) {
		return c.benchBusy(busy.OpID)
	}
	return err
}

// trimPeriod is s without a trailing period (it is placed inside parentheses).
func trimPeriod(s string) string { return strings.TrimSuffix(strings.TrimSpace(s), ".") }

// benchReport prints the human result (aictl.md section 4.24).
func (c *Call) benchReport(res *bench.Result, id string, names modelNameMap) {
	p := c.Out
	tr := func(en, es string) string { return pick(c.Lang, en, es) }
	where := tr("this computer (CPU only)", "este equipo (solo CPU)")
	if res.GPU != nil && *res.GPU != "" {
		where = *res.GPU
	}
	p.OK(id + tr(" on ", " en ") + where + ":")

	what, measured := tr("graphics memory", "memoria gráfica"), res.MeasuredVRAMMiB
	if measured == nil {
		what, measured = tr("memory", "memoria"), res.PeakRSSMiB
	}
	if measured != nil {
		line := fmt.Sprintf("  %s %s %s %d", what, out.GBFromMiB(*measured), tr("at ctx", "con ctx"), res.Ctx)
		if key, value, ok := res.SuggestedMiB(); ok {
			line += fmt.Sprintf(" %s %s%s: %d", p.Symbols().Arrow, tr("suggested ", "sugerido "), key, value)
		}
		p.Result(line)
	}
	speed := fmt.Sprintf("  %s %.0f %s%s%s %.1f %s", tr("prompt", "entrada"), res.PP512TPS, tr("tokens/s", "tokens/s"),
		roDot(p), tr("generation", "generación"), res.TG128TPS, tr("tokens/s", "tokens/s"))
	if res.ServerTGTPS != nil {
		if res.MTPAcceptance != nil {
			speed += fmt.Sprintf(tr(" (%.1f with MTP, %.0f%% accepted)", " (%.1f con MTP, %.0f%% aceptados)"), *res.ServerTGTPS, *res.MTPAcceptance*100)
		} else {
			speed += fmt.Sprintf(tr(" (%.1f through the server)", " (%.1f a través del servidor)"), *res.ServerTGTPS)
		}
	}
	p.Result(speed)
	p.Result(tr("  Saved ", "  Guardado en ") + res.Path)

	if snippet := res.Snippet(); snippet != "" {
		p.Result(tr("  For the catalog PR:", "  Para el PR del catálogo:"))
		for _, l := range strings.Split(strings.TrimRight(snippet, "\n"), "\n") {
			p.Result("    " + l)
		}
	}
	if res.Disturbed {
		p.Problem(out.Problem{Severity: out.SevWarning, Message: tr(
			"Another model operation ran during the test, so the numbers are not reliable and no catalog value is suggested.",
			"Otra operación de modelos ocurrió durante la prueba, así que los números no son confiables y no se sugiere ningún valor para el catálogo."),
			Fix: tr("Run `aictl bench "+id+"` again when nothing else is loading models.",
				"Ejecute `aictl bench "+id+"` otra vez cuando nada más esté cargando modelos.")})
	}
}

// benchRestoreWarning tells which models could not be loaded again (the node is not as it was) and how
// to fix it. It is a warning, not a failure: the measurement succeeded.
func (c *Call) benchRestoreWarning(res *bench.Result, names modelNameMap) {
	if len(res.RestoreFailed) == 0 {
		return
	}
	var cmds []string
	for _, id := range res.RestoreFailed {
		cmds = append(cmds, "`aictl load "+id+"`")
	}
	c.Out.Problem(out.Problem{Severity: out.SevWarning,
		Message: pick(c.Lang,
			names.list(c.Lang, res.RestoreFailed)+" could not be loaded again, so this computer is not as it was before the test.",
			"No se pudo cargar de nuevo "+names.list(c.Lang, res.RestoreFailed)+", así que este equipo no quedó como antes de la prueba."),
		Fix: pick(c.Lang, "Load it with "+strings.Join(cmds, ", ")+".", "Cárguelo con "+strings.Join(cmds, ", ")+".")})
}
