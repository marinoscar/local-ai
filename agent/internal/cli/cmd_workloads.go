package cli

import (
	"errors"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/cli/out"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
)

// `aictl workloads` and `aictl workloads activate <name> [--force] [--no-wait]` (aictl.md section
// 4.12a): GET /workloads and POST /workloads/{name}/activate (agent.md section 3.8.34).

// workloadNameRe is the form of a workload name; anything else is a usage error before the agent is
// asked.
var workloadNameRe = regexp.MustCompile(`^[a-z][a-z0-9-]{1,31}$`)

// activateTimeout bounds `workloads activate`: the agent's own deadline for a wait is
// load_timeout × (n+2) + 60 s; this covers models_max 3 with the default 300 s.
const activateTimeout = 30 * time.Minute

type workloadLabel struct {
	EN string `json:"en"`
	ES string `json:"es"`
}

type workloadEntry struct {
	Name    string        `json:"name"`
	Label   workloadLabel `json:"label"`
	Models  []string      `json:"models"`
	Active  bool          `json:"active"`
	Fits    bool          `json:"fits"`
	Missing []string      `json:"missing"`
}

type workloadsAnswer struct {
	Workloads []workloadEntry `json:"workloads"`
	Active    *string         `json:"active"`
}

type workloadActivated struct {
	OpID     string   `json:"op_id"`
	Active   string   `json:"active"`
	Loaded   []string `json:"loaded"`
	Unloaded []string `json:"unloaded"`
}

func init() {
	Register(&Command{
		Name:    "workloads",
		Summary: "Show what this computer can be set to do (coding, chat, music), or switch it.",
		Description: `Without a subcommand it lists the workloads, the active one first.
--json prints the agent body unchanged.`,
		Timeout: 10 * time.Second,
		Run:     runWorkloads,
		Sub: []*Command{{
			Name:    "activate",
			Summary: "Switch this computer to a workload: the models that are not in it stop, the missing ones start.",
			Usage:   "<name> [--force] [--no-wait]",
			Description: `In survival mode the agent refuses unless you confirm (or add --force); the survival models stay.
  --no-wait   Return as soon as the switch has started.`,
			Timeout: activateTimeout,
			Flags: func(fs *flag.FlagSet) {
				fs.Bool("force", false, "switch even in survival mode (the survival models stay loaded)")
				fs.Bool("no-wait", false, "return as soon as the switch has started")
			},
			Run: runWorkloadActivate,
		}},
	})
}

func (w workloadEntry) label(l Lang) string {
	if l == LangES && w.Label.ES != "" {
		return w.Label.ES
	}
	return w.Label.EN
}

func runWorkloads(c *Call) error {
	if len(c.Args) > 0 {
		return c.usageErr(pick(c.Lang, fmt.Sprintf("Unknown subcommand %q.", c.Args[0]), fmt.Sprintf("Subcomando desconocido %q.", c.Args[0])),
			pick(c.Lang, "Use `aictl workloads` or `aictl workloads activate <name>`.", "Use `aictl workloads` o `aictl workloads activate <name>`."))
	}
	cl, err := c.Client()
	if err != nil {
		return err
	}
	var a workloadsAnswer
	raw, _, err := c.callWith(cl, c.Ctx, http.MethodGet, "/workloads", nil, &a)
	if err != nil {
		return err
	}
	if c.Global.JSON {
		return c.emitRaw(raw)
	}
	p := c.Out
	if len(a.Workloads) == 0 {
		p.Info(pick(c.Lang, "This computer has no workloads. The profile or node.yaml `workloads:` defines them.",
			"Este equipo no tiene usos. Los define el perfil o `workloads:` en node.yaml."))
		return nil
	}
	ordered := append([]workloadEntry(nil), a.Workloads...)
	for i, w := range ordered {
		if a.Active != nil && w.Name == *a.Active && i > 0 {
			ordered = append([]workloadEntry{w}, append(ordered[:i:i], ordered[i+1:]...)...)
			break
		}
	}
	var rows [][]string
	for _, w := range ordered {
		mark := " "
		if a.Active != nil && w.Name == *a.Active {
			mark = "●"
		}
		models := strings.Join(w.Models, ", ")
		switch {
		case len(w.Missing) > 0:
			models = "✗ " + pick(c.Lang, "missing: ", "falta: ") + strings.Join(w.Missing, ", ")
		case !w.Fits:
			models = "✗ " + pick(c.Lang, "does not fit: ", "no cabe: ") + models
		}
		rows = append(rows, []string{mark + " " + w.label(c.Lang), w.Name, models})
	}
	p.Table([]out.Col{{Title: pick(c.Lang, "  WORKLOAD", "  USO")}, {Title: pick(c.Lang, "NAME", "NOMBRE")}, {Title: pick(c.Lang, "MODELS", "MODELOS")}}, rows)
	return nil
}

func runWorkloadActivate(c *Call) error {
	if err := c.exactArgs(1, "<name>"); err != nil {
		return err
	}
	name := c.Args[0]
	if !workloadNameRe.MatchString(name) {
		return c.usageErr(pick(c.Lang, fmt.Sprintf("%q is not a workload name.", name), fmt.Sprintf("%q no es un nombre de uso.", name)),
			pick(c.Lang, "`aictl workloads` lists the names.", "`aictl workloads` muestra los nombres."))
	}
	cl, err := c.Client()
	if err != nil {
		return err
	}
	noWait := c.Bool("no-wait")
	force := c.Bool("force")
	path := "/workloads/" + url.PathEscape(name) + "/activate"
	start := time.Now()
	call := func(force bool) (json []byte, a workloadActivated, err error) {
		raw, _, err := c.callWith(cl, c.Ctx, http.MethodPost, path, map[string]any{"wait": !noWait, "force": force}, &a)
		return raw, a, err
	}
	if !noWait && !c.Global.JSON {
		c.Out.Progress(pick(c.Lang, "Switching to "+name+"; this can take a few minutes.", "Cambiando a "+name+"; puede tardar unos minutos."))
	}
	raw, a, err := call(force)
	var ae *APIError
	if err != nil && errors.As(err, &ae) && ae.Code == doctor.LLM053 && !force && c.canAsk() {
		ok, cerr := c.Confirm(pick(c.Lang, "This computer is in survival mode. Change it anyway? The survival models stay loaded.",
			"Este equipo está en modo supervivencia. ¿Cambiarlo de todos modos? Los modelos de supervivencia siguen cargados."))
		if cerr != nil {
			return cerr
		}
		if !ok {
			c.Out.Info(pick(c.Lang, "Nothing was changed.", "No se cambió nada."))
			return nil
		}
		raw, a, err = call(true)
	}
	if err != nil {
		return err
	}
	if c.Global.JSON {
		return c.emitRaw(raw)
	}
	if noWait {
		c.Out.OK(pick(c.Lang, "The switch to "+name+" has started (operation "+a.OpID+").",
			"Comenzó el cambio a "+name+" (operación "+a.OpID+")."))
		return nil
	}
	for _, id := range a.Unloaded {
		c.Out.Info(pick(c.Lang, "Stopped "+id, "Se detuvo "+id))
	}
	for _, id := range a.Loaded {
		c.Out.Info(pick(c.Lang, "Started "+id, "Se inició "+id))
	}
	secs := int(time.Since(start).Round(time.Second) / time.Second)
	c.Out.OK(pick(c.Lang, fmt.Sprintf("This computer is now set for %s. (%d s)", a.Active, secs),
		fmt.Sprintf("Este equipo ahora está configurado para %s. (%d s)", a.Active, secs)))
	return nil
}
