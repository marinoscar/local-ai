package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/api"
	"github.com/marinoscar/local-ai/agent/internal/cli/out"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/router/build"
)

// `aictl rebuild-agent` (aictl.md section 4.23b, rebuild-kit.md section 4.3.4, KIT-05). It runs in this
// process as root, with no network: build.AgentBuilder unpacks the kit's Go toolchain and the agent
// source, compiles with the vendored modules as an unprivileged user, tests the program and swaps it
// in (aiagent.prev kept, restart, health check within 60 s, rollback). This file is the flags, the
// wiring of the kit and the agent's health, the words and the exit codes; the work is the engine's.

// rebuildAgentTimeout is the default deadline: a compile of the agent takes minutes, an offline laptop
// at most a few more (aictl.md section 4.23b says 1 h).
const rebuildAgentTimeout = time.Hour

func init() {
	Register(&Command{
		Name:    "rebuild-agent",
		Summary: "Rebuild the agent program from the kit's source, with no internet.",
		Usage:   "[--from-kit] [--dry-run] [--yes]",
		Description: `Builds the agent (aiagent) from the source and the Go toolchain in the rebuild kit
(<content root>/kit/src and kit/toolchains), with no internet. The new program is tested, the
program in use is kept as aiagent.prev, and the agent is restarted. If the new agent does not
answer as healthy within a minute, the previous one is put back automatically.
  --from-kit  Accepted for symmetry with the other rebuild commands: the agent is always
              built from the kit.
  --dry-run   Only check that the source (with all its vendored parts) and the toolchain are
              in the kit and complete; change nothing. Exit 0 only if every check passes.
  --yes       Do not ask for confirmation.
The fix for a damaged or missing agent when the release program in the kit cannot be used. Run it
with sudo.
Specification: docs/specs/aictl.md section 4.23b, docs/specs/rebuild-kit.md section 4.3.4`,
		Marks:   MarkLocal | MarkRoot,
		Timeout: rebuildAgentTimeout,
		Flags: func(fs *flag.FlagSet) {
			fs.Bool("from-kit", false, "accepted: the agent is always built from the kit")
			fs.Bool("dry-run", false, "check the kit and change nothing")
			declareYes(fs)
		},
		Run: rebuildAgent,
	})
}

// ---- the seams ----------------------------------------------------------------------------------

// agentEngine is the part of *build.AgentBuilder the command uses; tests replace it.
type agentEngine interface {
	Check(ctx context.Context, req build.AgentRequest) (build.AgentReport, error)
	Build(ctx context.Context, req build.AgentRequest) (build.AgentResult, error)
}

var agentNewEngine = func(opt build.AgentOptions) agentEngine { return build.NewAgentBuilder(opt) }

// agentCompileUser is AgentOptions.CompileUser; tests set build.CompileAsCaller.
var agentCompileUser = ""

// agentHealth reads GET /health of the running agent (not cached: it is polled while the agent
// restarts).
func agentHealth(c *Call) func(context.Context) (build.AgentHealth, error) {
	return func(ctx context.Context) (build.AgentHealth, error) {
		cl, err := c.Client()
		if err != nil {
			return build.AgentHealth{}, err
		}
		var h api.Health
		if _, err := cl.Do(ctx, http.MethodGet, "/health", nil, &h); err != nil {
			return build.AgentHealth{}, err
		}
		return build.AgentHealth{Version: h.Version, Starting: h.Starting}, nil
	}
}

// ---- the command --------------------------------------------------------------------------------

func rebuildAgent(c *Call) error {
	if err := noExtraArgs(c); err != nil {
		return err
	}
	applyYes(c)
	dry := c.Bool("dry-run")
	if err := RequireRoot(c); err != nil {
		return err
	}
	host, env := kitHostFn(), localEnv()
	roots, err := kitRoots(c, env, "")
	if err != nil {
		return err // no kit anywhere: LAI-KIT-040, exit 5
	}
	var kitPaths []string
	for _, r := range roots {
		kitPaths = append(kitPaths, r.Path)
	}
	release, err := kitLock(c, env)
	if err != nil {
		return err
	}
	defer release()

	eng := agentNewEngine(build.AgentOptions{
		KitRoots:    func() []string { return kitPaths },
		OptRoot:     host.OptRoot,
		Arch:        host.GOARCH,
		CompileUser: agentCompileUser,
		Health:      agentHealth(c),
		Audit:       localAudit{env},
	})
	req := build.AgentRequest{DryRun: dry}
	req.Progress = func(p build.Progress) {
		if !c.Global.JSON {
			c.Out.Progress(p.Message)
		}
	}
	if dry {
		return agentDryRun(c, eng, req)
	}

	// The checks come first, so that a missing part is told before anyone is asked anything.
	rep, err := eng.Check(c.Ctx, req)
	if err != nil {
		return agentErr(c, err)
	}
	if e := rep.Err(); e != nil {
		return agentErr(c, e)
	}
	if !c.Global.JSON {
		c.Out.Info(fmt.Sprintf(pick(c.Lang, "Rebuilding the agent %s from the kit with Go %s (no internet is used)",
			"Recompilando el agente %s desde el kit con Go %s (no se usa internet)"), rep.Version, rep.GoVersion))
	}
	ok, cerr := c.Confirm(fmt.Sprintf(pick(c.Lang,
		"Build the agent %s from the kit and switch to it? The agent restarts; this takes a few minutes.",
		"¿Compilar el agente %s desde el kit y cambiar a él? El agente se reinicia; tarda unos minutos."), rep.Version))
	if cerr != nil {
		return cerr
	}
	if !ok {
		c.Out.Info(pick(c.Lang, "Nothing was changed.", "No se cambió nada."))
		return ExitWith(ExitRefused)
	}
	res, err := eng.Build(c.Ctx, req)
	if err != nil {
		return agentErr(c, err)
	}
	return agentDone(c, res)
}

// agentDryRun prints every check and exits 0 only when all pass.
func agentDryRun(c *Call, eng agentEngine, req build.AgentRequest) error {
	rep, err := eng.Check(c.Ctx, req)
	if err == nil {
		if e := rep.Err(); e != nil {
			err = e
		}
	}
	if c.Global.JSON {
		if err != nil {
			// A dry run that fails exits 1 whatever the first failed check is (the human output does too).
			e := agentErr(c, err)
			if ce, ok := e.(*CLIError); ok && ce.Exit == ExitNotFound {
				ce.Exit = ExitFailed
			}
			return e
		}
		return c.Out.JSON().Document(rep)
	}
	if err != nil && len(rep.Checks) == 0 {
		return agentErr(c, err)
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
	if err != nil {
		c.Out.Blank()
		c.Out.Result(pick(c.Lang,
			"Not ready: fix the checks marked ✗ above, then run it again.",
			"No está listo: corrija las comprobaciones marcadas con ✗ y ejecútelo otra vez."))
		return ExitWith(ExitFailed)
	}
	c.Out.OK(fmt.Sprintf(pick(c.Lang, "Everything needed to rebuild the agent %s from the kit is in place.",
		"Todo lo necesario para recompilar el agente %s desde el kit está listo."), rep.Version))
	return nil
}

// agentDone prints the result of a rebuild that succeeded.
func agentDone(c *Call, res build.AgentResult) error {
	if c.Global.JSON {
		return c.Out.JSON().Document(res)
	}
	for _, w := range res.Warnings {
		c.Out.Warn(w)
	}
	if res.Restarted {
		c.Out.OK(fmt.Sprintf(pick(c.Lang,
			"Built aiagent v%s from the ark kit (no internet used). The agent restarted and is healthy.",
			"Se compiló aiagent v%s desde el kit del arca (sin usar internet). El agente se reinició y está sano."), res.Version))
	} else {
		c.Out.OK(fmt.Sprintf(pick(c.Lang,
			"Built aiagent v%s from the ark kit (no internet used) and installed it.",
			"Se compiló aiagent v%s desde el kit del arca (sin usar internet) y se instaló."), res.Version))
		c.Out.Warn(pick(c.Lang,
			"The agent was not running or could not be restarted from here. Start it with `sudo systemctl restart localai-agent`.",
			"El agente no estaba en marcha o no se pudo reiniciar desde aquí. Inícielo con `sudo systemctl restart localai-agent`."))
	}
	if res.Previous != "" {
		c.Out.Info(fmt.Sprintf(pick(c.Lang, "The previous agent %s is kept as /opt/localai/bin/aiagent.prev.",
			"Se conserva el agente anterior %s como /opt/localai/bin/aiagent.prev."), res.Previous))
	}
	return nil
}

// agentErr turns what the engine returns into the command's error: its doctor code, the engine's
// plain sentence (the code's own title in Spanish), the code's fix and the log of a failed build. Errors
// that are not the engine's (a cancelled context) are returned as they are.
func agentErr(c *Call, err error) error {
	var be *build.Error
	if !errors.As(err, &be) {
		return err
	}
	exit := ExitFailed
	if be.Code == doctor.KIT040 {
		exit = ExitNotFound
	}
	e := c.codeError(be.Code, exit, "")
	if c.Lang != LangES || e.Message == "" {
		e.Message = be.Message
	}
	switch be.Code {
	case doctor.KIT020:
		e.Fix = pick(c.Lang, "Read the build log shown, then run `sudo aictl rebuild-agent --dry-run`.",
			"Lea el registro de compilación mostrado y ejecute `sudo aictl rebuild-agent --dry-run`.")
	case doctor.KIT021:
		if rb, _ := be.Details["rolled_back"].(bool); rb {
			e.Fix = pick(c.Lang, "The previous agent is running again; nothing is lost. Report the log.",
				"El agente anterior está en marcha otra vez; no se perdió nada. Informe del registro.")
		}
	}
	detail := []any{"stage", string(be.Stage)}
	for _, k := range []string{"cause", "rolled_back", "previous_version", "log_path", "failed_checks", "file", "exit_code"} {
		if v, ok := be.Details[k]; ok {
			detail = append(detail, k, v)
		}
	}
	e = e.WithDetails(detail...).WithCause(be)
	if p, ok := be.Details["log_path"].(string); ok && p != "" && !c.Global.JSON {
		e.Message = strings.TrimSpace(e.Message) + " " + fmt.Sprintf(pick(c.Lang, "The log is in %s.", "El registro está en %s."), p)
	}
	return e
}
