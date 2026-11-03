package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/api"
	"github.com/marinoscar/local-ai/agent/internal/audit"
	"github.com/marinoscar/local-ai/agent/internal/cli/local"
	"github.com/marinoscar/local-ai/agent/internal/cli/out"
	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/events"
	"github.com/marinoscar/local-ai/agent/internal/update"
)

// `aictl update` (aictl.md section 4.27, agent.md section 4.10, AICTL-09). It runs steps 1 to 8 of the
// agent's self-update in this process, as root, with the same internal/update code the cloud's
// agent.update job uses: resolve the release of update.repo, download and verify it (minisign, sha256),
// smoke test the staged binary, install it keeping aiagent.prev, arm the rollback timer and ask systemd
// to restart the agent. Then it waits up to 120 s for GET /health to report the new version, or the old
// one again (the rollback). The new process does step 9 and 10 (its health check and the rollback), so
// no HTTP route is involved; the agent only has to answer /health. This file is the flags, the mode
// check, the confirmation, the words and the exit codes; the work is the library's.

const (
	// updateTimeout is the default deadline (aictl.md section 4.27 says 15 min).
	updateTimeout = 15 * time.Minute
	// updateWait is how long the restarted agent has to report the new version.
	updateWait = 120 * time.Second
)

func init() {
	Register(&Command{
		Name:    "update",
		Summary: "Install a newer agent release (needs internet; the old one is put back if the new one fails).",
		Usage:   "[--version <x.y.z>] [--check] [--yes]",
		Description: `Looks for a newer agent release on GitHub, checks its signature and checksum, installs it and
restarts the agent. The models keep running. If the new agent does not start properly, the previous
one is put back automatically (about two minutes at most).
  --version <x.y.z>  Install this release instead of the newest one.
  --check            Only look and say what is available; change nothing.
  --yes              Do not ask for confirmation.
Needs internet, so it is refused in offline and survival mode (exit 6). With no internet, install the
agent from the rebuild kit instead: "sudo aictl kit install agent" (or "sudo aictl rebuild-agent").
Ctrl-C while it waits for the restarted agent only stops the waiting; the rollback timer still protects
the node. Run it with sudo.
Specification: docs/specs/aictl.md section 4.27, docs/specs/agent.md section 4.10`,
		Marks:   MarkLocal | MarkRoot,
		Timeout: updateTimeout,
		Flags: func(fs *flag.FlagSet) {
			fs.String("version", "", "install this release (x.y.z) instead of the newest")
			fs.Bool("check", false, "only look; change nothing")
			declareYes(fs)
		},
		Run: updateRun,
	})
}

// ---- the seams of the command ---------------------------------------------------------------------

var (
	// updateTweak, when set, adjusts the Runner after the command assembled it (a test points it at a
	// fake GitHub, a test key, fake systemctl and a manual clock).
	updateTweak func(*update.Runner)
	// updateHealthFn reads GET /health of the agent for the wait after the restart.
	updateHealthFn = func(c *Call) func(context.Context) (update.Health, error) {
		return func(ctx context.Context) (update.Health, error) {
			cl, err := c.Client()
			if err != nil {
				return update.Health{}, err
			}
			var h api.Health
			if _, err := cl.Do(ctx, http.MethodGet, "/health", nil, &h); err != nil {
				return update.Health{}, err
			}
			return update.Health{Version: h.Version, Starting: h.Starting}, nil
		}
	}
	// updateConnProbe says whether GitHub answers; it decides the mode when neither the agent nor
	// state.json can say.
	updateConnProbe = func(ctx context.Context) error {
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodHead, update.DefaultAPIBase, nil)
		if err != nil {
			return err
		}
		resp, err := (&http.Client{}).Do(req)
		if err != nil {
			return err
		}
		_ = resp.Body.Close()
		return nil
	}
)

// updateSettings are the values of node.yaml's update block.
type updateSettings struct {
	Repo, Channel  string
	AllowDowngrade bool
}

// readUpdateSettings reads node.yaml; a missing or unreadable file gives the defaults.
func readUpdateSettings(env local.Env) updateSettings {
	s := updateSettings{Repo: update.DefaultRepo, Channel: update.ChannelStable}
	data, err := os.ReadFile(env.Paths.NodeYAML())
	if err != nil || len(data) > 1<<20 {
		return s
	}
	cfg, _ := config.Parse(data, nil)
	if cfg.Update.Repo != "" {
		s.Repo = cfg.Update.Repo
	}
	if cfg.Update.Channel != "" {
		s.Channel = cfg.Update.Channel
	}
	s.AllowDowngrade = cfg.Update.AllowDowngrade
	return s
}

// updateProgress turns the library's update.state events into the progress lines on stderr.
type updateProgress struct{ c *Call }

// Publish implements update.EventSink.
func (p updateProgress) Publish(_ string, _ string, data any) error {
	st, ok := data.(events.UpdateState)
	if !ok || p.c.Global.JSON {
		return nil
	}
	switch st.State {
	case update.StateDownloading:
		p.c.Out.Progress(pick(p.c.Lang, "Downloading and checking the signature", "Descargando y comprobando la firma"))
	case update.StateInstalling:
		p.c.Out.Progress(pick(p.c.Lang, "Installing the new version", "Instalando la nueva versión"))
	case update.StateRestarting:
		p.c.Out.Progress(pick(p.c.Lang, "Restarting the agent and waiting for it", "Reiniciando el agente y esperando a que responda"))
	}
	return nil
}

// newUpdateRunner builds the Runner of this process: the paths of the node, node.yaml's update block,
// the audit log of the local commands (actor cli-root) and a mode that is read once.
func newUpdateRunner(c *Call, env local.Env, host kitHost, set updateSettings) *update.Runner {
	var once sync.Once
	var mode string
	r := &update.Runner{
		Paths: update.Paths{
			UpdateDir: filepath.Join(env.Paths.StateDir, "update"),
			BinDir:    filepath.Join(optRootOf(host), "bin"),
		},
		Source:         update.Source{Repo: set.Repo, Channel: set.Channel},
		Current:        c.env.Version,
		AllowDowngrade: set.AllowDowngrade,
		Audit:          localAudit{env},
		Events:         updateProgress{c},
		Actor:          audit.CLIRoot(),
		Log:            slog.New(slog.NewTextHandler(io.Discard, nil)),
		// The CLI never writes state.json (the agent owns it): State stays nil.
		Mode: func() string {
			once.Do(func() {
				if mode = nodeMode(c, env); mode == "" {
					// Nothing says what the mode is: an update is allowed only if GitHub answers.
					mode = "offline"
					if updateConnProbe(c.Ctx) == nil {
						mode = "online"
					}
				}
			})
			return mode
		},
	}
	if host.Runner != nil {
		r.Exec = host.Runner
	}
	if updateTweak != nil {
		updateTweak(r)
	}
	return r
}

func optRootOf(host kitHost) string {
	if host.OptRoot != "" {
		return host.OptRoot
	}
	return "/opt/localai"
}

// ---- the command ----------------------------------------------------------------------------------

// updateDoc is the --json document of `aictl update` (and of --check): status is available,
// already_current or updated; reason qualifies already_current.
type updateDoc struct {
	Status string `json:"status"`
	From   string `json:"from"`
	To     string `json:"to"`
	Reason string `json:"reason,omitempty"`
}

func updateRun(c *Call) error {
	if err := noExtraArgs(c); err != nil {
		return err
	}
	applyYes(c)
	want := strings.TrimSpace(c.String("version"))
	if want != "" {
		if _, err := update.ParseVersion(want); err != nil {
			return c.usageErr(
				pick(c.Lang, "The version "+quote(want)+" is not valid; use a version such as 1.2.3 (no leading v).",
					"La versión "+quote(want)+" no es válida; use una versión como 1.2.3 (sin v al principio)."),
				pick(c.Lang, "Leave --version out to install the newest release.", "Quite --version para instalar la versión más reciente."))
		}
	}
	if err := RequireRoot(c); err != nil {
		return err
	}
	env, host := localEnv(), kitHostFn()
	r := newUpdateRunner(c, env, host, readUpdateSettings(env))

	found, err := r.Check(c.Ctx, want)
	if err != nil {
		return updateErr(c, err)
	}
	if found.Status == update.StatusAlreadyCurrent {
		return updateUpToDate(c, found, want)
	}
	if c.Bool("check") {
		return updateAvailable(c, found, true)
	}
	if err := updateAvailable(c, found, false); err != nil {
		return err
	}
	ok, cerr := c.Confirm(pick(c.Lang,
		"Update now? The agent restarts; models keep running.",
		"¿Actualizar ahora? El agente se reinicia; los modelos siguen funcionando."))
	if cerr != nil {
		return cerr
	}
	if !ok {
		c.Out.Info(pick(c.Lang, "Nothing was changed.", "No se cambió nada."))
		return ExitWith(ExitRefused)
	}

	// The version that was shown is the one installed, even if a newer release appears meanwhile.
	res, err := r.Update(c.Ctx, update.Request{Version: found.To, Actor: audit.CLIRoot()})
	if err != nil {
		if errors.Is(c.Ctx.Err(), context.Canceled) {
			return updateStopped(c, false)
		}
		return updateErr(c, err)
	}
	if res.Status != update.StatusRestarting {
		return updateUpToDate(c, res, want) // another process updated the agent in the meantime
	}
	outcome, err := r.WaitForUpdate(c.Ctx, updateHealthFn(c), res.From, res.To, updateWait)
	switch {
	case err != nil && errors.Is(err, context.Canceled):
		return updateStopped(c, true)
	case err != nil:
		return updateErr(c, err)
	case outcome == update.WaitRolledBack:
		return updateRolledBack(c, res)
	}
	if c.Global.JSON {
		return c.Out.JSON().Document(updateDoc{Status: "updated", From: res.From, To: res.To})
	}
	c.Out.OK(fmt.Sprintf(pick(c.Lang, "Updated to %s. Everything is running.", "Actualizado a %s. Todo está en marcha."), res.To))
	return nil
}

// updateAvailable says that a newer release exists. With --check it is the whole answer, with the
// code and its fix; otherwise it is the line before the question.
func updateAvailable(c *Call, found update.Result, check bool) error {
	if c.Global.JSON {
		if !check {
			return nil // the document is written once, by the outcome
		}
		return c.Out.JSON().Document(updateDoc{Status: "available", From: found.From, To: found.To})
	}
	line := fmt.Sprintf(pick(c.Lang, "Version %s is available (you have %s).", "La versión %s está disponible (usted tiene la %s)."), found.To, found.From)
	if !check {
		c.Out.Info(line)
		return nil
	}
	p := c.codeProblem(doctor.AGT015, line)
	p.Severity, p.Message = sevInfo, line
	c.Out.Finding(p)
	return nil
}

// updateUpToDate is the answer when there is nothing to install: the newest release is the running
// one, or older and a downgrade is not allowed (update.allow_downgrade). Exit 0 either way.
func updateUpToDate(c *Call, found update.Result, want string) error {
	if c.Global.JSON {
		return c.Out.JSON().Document(updateDoc{Status: update.StatusAlreadyCurrent, From: found.From, To: found.To, Reason: found.Reason})
	}
	msg := fmt.Sprintf(pick(c.Lang, "You are up to date: version %s is the newest.", "Está al día: la versión %s es la más reciente."), found.From)
	fix := ""
	if found.Reason == update.ReasonDowngradeRefused {
		older := pick(c.Lang, "the release found", "la versión encontrada")
		if want != "" {
			older = pick(c.Lang, "version ", "la versión ") + want
		}
		msg = fmt.Sprintf(pick(c.Lang, "%s is older than the version you have (%s), so nothing was installed.",
			"%s es más antigua que la que usted tiene (%s), así que no se instaló nada."), capitalize(older), found.From)
		fix = pick(c.Lang, "To go back on purpose, set update.allow_downgrade to true in node.yaml.",
			"Para volver atrás a propósito, ponga update.allow_downgrade en true en node.yaml.")
	}
	c.Out.Finding(out.Problem{Severity: sevInfo, Code: doctor.AGT015, Message: msg, Fix: fix})
	return nil
}

// updateStopped is the end of a Ctrl-C. After the restart it only stops the waiting: the rollback timer
// still protects the node. Before it, nothing was installed.
func updateStopped(c *Call, restarted bool) error {
	msg := pick(c.Lang, "The update was stopped before the agent was restarted. Nothing was changed.",
		"Se detuvo la actualización antes de reiniciar el agente. No se cambió nada.")
	if restarted {
		msg = pick(c.Lang,
			"Stopped waiting. The agent keeps updating: if the new version does not start properly, the previous one is put back automatically within about two minutes.",
			"Se dejó de esperar. El agente sigue actualizándose: si la nueva versión no arranca bien, se vuelve a la anterior automáticamente en unos dos minutos.")
	}
	if c.Global.JSON {
		return c.Err(CodeInterrupted, msg).WithCause(context.Canceled)
	}
	c.Out.Info(msg)
	return ExitWith(ExitInterrupted)
}

// updateRolledBack is the end of an update whose new agent did not start properly: the previous one
// is running again (LAI-AGT-009, exit 1).
func updateRolledBack(c *Call, res update.Result) error {
	e := c.codeError(doctor.AGT009, ExitFailed, "")
	e.Message = fmt.Sprintf(pick(c.Lang,
		"The new version %s did not start properly, so the previous version %s was put back automatically. Nothing is lost.",
		"La nueva versión %s no arrancó bien, así que se volvió automáticamente a la versión anterior %s. No se perdió nada."), res.To, res.From)
	return e.WithDetails("from", res.From, "to", res.To, "status", "rolled_back")
}

// updateErr turns what the library returns into the command's error: its doctor code, its plain
// sentence (the code's own title in Spanish), the code's fix. Exit codes: 6 for "not allowed now" (not
// online, no internet, an update already pending), 2 for a version that is not valid, 5 for one that was
// not released, 1 for everything else (a bad signature or checksum, a staged binary that does not run, a
// restart that was refused, the new version that did not report in time). Errors that are not the
// library's (a cancelled context) are returned as they are.
func updateErr(c *Call, err error) error {
	var ue *update.Error
	if !errors.As(err, &ue) {
		return err
	}
	exit := ExitFailed
	switch ue.Code {
	case doctor.AGT023, doctor.AGT022:
		exit = ExitRefused
	case doctor.AGT020:
		exit = ExitUsage
	case doctor.AGT021:
		exit = ExitNotFound
	}
	e := c.codeError(ue.Code, exit, "")
	if c.Lang != LangES || e.Message == "" {
		e.Message = ue.Message
	}
	switch ue.Code {
	case doctor.AGT010:
		if c.Lang != LangES {
			e.Message = strings.TrimSpace(e.Message) + " Nothing was replaced."
		} else {
			e.Message = strings.TrimSpace(e.Message) + " No se reemplazó nada."
		}
	case doctor.AGT023:
		if c.Lang == LangES {
			e.Message = strings.TrimSpace(e.Message) + " Actualizar necesita internet y este equipo no tiene salida a GitHub o no está en modo en línea."
		}
		e.Fix = pick(c.Lang,
			"Switch to online mode (`aictl mode online`) and run it again. With no internet, use `sudo aictl kit install agent` or `sudo aictl rebuild-agent` instead.",
			"Cambie al modo en línea (`aictl mode online`) y ejecútelo otra vez. Sin internet, use `sudo aictl kit install agent` o `sudo aictl rebuild-agent`.")
	case doctor.AGT022:
		e.Fix = pick(c.Lang, "Wait a few minutes for the running update to finish (`aictl version` shows the version), then try again.",
			"Espere unos minutos a que termine la actualización en curso (`aictl version` muestra la versión) e inténtelo otra vez.")
	}
	return e.WithDetails("step", ue.Step).WithCause(ue)
}
