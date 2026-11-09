package cli

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"strings"

	"github.com/marinoscar/local-ai/agent/internal/cli/out"
)

// `aictl services` (aictl.md section 4.13): the companion services and whether they are running, from
// GET /services (AICTL-04), and `services start|stop|restart <name> [--force]` (AICTL-10), which
// call POST /services/{name}/{action}. `restart` and `services restart` share serviceAction
// (cmd_restart.go), so the two forms behave identically.

func init() {
	Register(&Command{
		Name:    "services",
		Summary: "List the services (llama, kiwix, whisper), or start, stop or restart one.",
		Description: `Without a subcommand it lists the services and whether they run. --json prints the agent body unchanged.
  aictl services start|stop|restart <llama|kiwix|whisper> [--force]
Starting llama also waits until the router answers and loads the models that were loaded before. Stopping
llama in survival mode needs --force. "aictl restart <name>" is the same as "aictl services restart <name>".`,
		Timeout: readOnlyTimeout,
		Run:     runServices,
	})
	for _, action := range []string{"start", "stop", "restart"} {
		attachAll(at(serviceActionCommand(action), "services"))
	}
}

// serviceActionCommand is `services <action> <name> [--force]`.
func serviceActionCommand(action string) *Command {
	summary := map[string]string{
		"start":   "Start a service (llama, kiwix or whisper).",
		"stop":    "Stop a service (llama, kiwix or whisper).",
		"restart": "Restart a service (llama, kiwix or whisper).",
	}[action]
	return &Command{
		Name:    action,
		Summary: summary,
		Usage:   "<llama|kiwix|whisper> [--force]",
		Description: `Waits until the service is ` + map[string]string{"stop": "stopped", "start": "running", "restart": "running again"}[action] + ` (at most 60 seconds). For llama it also waits for the router and loads
the models that were loaded before. --force is sent to the agent as given: it is needed only to stop
llama in survival mode. --json prints the agent body unchanged.`,
		Timeout: restartTimeout,
		Flags: func(fs *flag.FlagSet) {
			fs.Bool("force", false, "send force to the agent (to stop llama in survival mode)")
		},
		Run: func(c *Call) error {
			if err := c.exactArgs(1, "<llama|kiwix|whisper>"); err != nil {
				return err
			}
			return c.serviceAction(c.Args[0], action)
		},
	}
}

// decodeServices accepts the list either as a bare array or as {"services": [...]}: agent.md
// section 3.8.22 says "the services array of /status plus ..." without fixing which.
func decodeServices(raw []byte) ([]wireService, error) {
	t := bytes.TrimSpace(raw)
	var list []wireService
	if bytes.HasPrefix(t, []byte("[")) {
		return list, json.Unmarshal(t, &list)
	}
	var obj struct {
		Services []wireService `json:"services"`
	}
	err := json.Unmarshal(t, &obj)
	return obj.Services, err
}

func runServices(c *Call) error {
	if len(c.Args) > 0 {
		return roNoArgs(c)
	}
	var list []wireService
	raw, err := roGet(c, "/services", func(b []byte) (err error) {
		list, err = decodeServices(b)
		return err
	})
	if err != nil {
		return err
	}
	if c.Global.JSON {
		return roEmit(c, raw)
	}
	renderServices(c.Out, list)
	return nil
}

func renderServices(p *out.Printer, list []wireService) {
	tr := func(en, es string) string { return pick(p.Lang(), en, es) }
	if len(list) == 0 {
		p.Info(tr("No services reported.", "No se informó ningún servicio."))
		return
	}
	sym := p.Symbols()
	var rows [][]string
	notes := false
	for _, s := range list {
		health := sym.Error
		switch {
		case s.Healthy:
			health = sym.OK
		case s.State == "starting":
			health = sym.Progress
		case s.State == "degraded":
			health = sym.Warn
		}
		since := roDash(p)
		if ago := roAgo(p, s.Since); ago != "" {
			since = ago
		}
		var note []string
		if s.LastExit != nil && s.LastExit.Code != nil && *s.LastExit.Code != 0 {
			note = append(note, fmt.Sprintf(tr("last exit code %d", "último código de salida %d"), *s.LastExit.Code))
		}
		if s.Enabled != nil && !*s.Enabled {
			note = append(note, tr("disabled", "desactivado"))
		}
		switch s.Device {
		case "cpu":
			note = append(note, tr("runs on the processor (cpu)", "usa el procesador (cpu)"))
		case "gpu":
			note = append(note, tr("runs on the graphics card (gpu)", "usa la tarjeta gráfica (gpu)"))
		}
		if s.Restarts > 0 {
			note = append(note, fmt.Sprintf(tr("restarted %d times", "reiniciado %d veces"), s.Restarts))
		}
		n := strings.Join(note, "; ")
		if n != "" {
			notes = true
		}
		rows = append(rows, []string{s.Name, serviceState(p, s), health, since, n})
	}
	cols := []out.Col{{Title: tr("SERVICE", "SERVICIO")}, {Title: tr("STATE", "ESTADO")}, {Title: tr("HEALTH", "SALUD")}, {Title: tr("SINCE", "DESDE")}}
	if notes {
		cols = append(cols, out.Col{})
	}
	p.Table(cols, rows)
}

// serviceState is the plain-word state of a unit: running, stopped, failed, starting.
func serviceState(p *out.Printer, s wireService) string {
	switch {
	case s.Active == "" && s.State != "": // a service without a unit (voices, maps): the supervisor's state
		switch s.State {
		case "ok", "degraded":
			return pick(p.Lang(), "available", "disponible")
		case "starting":
			return pick(p.Lang(), "starting", "iniciando")
		case "disabled":
			return pick(p.Lang(), "disabled", "desactivado")
		}
		return pick(p.Lang(), "stopped", "detenido")
	case s.Active == "active" && (s.Sub == "running" || s.Sub == ""):
		return pick(p.Lang(), "running", "en ejecución")
	case s.Active == "active":
		return s.Sub
	case s.Active == "failed":
		return pick(p.Lang(), "failed", "falló")
	case s.Active == "activating":
		return pick(p.Lang(), "starting", "iniciando")
	case s.Active == "inactive" || s.Active == "deactivating":
		return pick(p.Lang(), "stopped", "detenido")
	}
	return s.Active
}
