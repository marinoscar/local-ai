package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/marinoscar/local-ai/agent/internal/cli/out"
)

// `aictl services` (aictl.md section 4.13), list only: the companion services and whether they
// are running, from GET /services. start, stop and restart belong to AICTL-10.

func init() {
	Register(&Command{
		Name:        "services",
		Summary:     "List the services (llama, kiwix, whisper) and whether they run.",
		Description: "Changing a service (start, stop, restart) is not available yet.\n--json prints the agent body unchanged.",
		Timeout:     readOnlyTimeout,
		Run:         runServices,
	})
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
		switch c.Args[0] {
		case "start", "stop", "restart":
			return notImplemented("services "+c.Args[0], "AICTL-10")(c)
		}
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
