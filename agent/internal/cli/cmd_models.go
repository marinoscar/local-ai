package cli

import (
	"flag"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/cli/out"
)

// `aictl models [--loadable] [--all] [--engine ...]` and `aictl models show <id>` (aictl.md section
// 4.2): the model catalog merged with what is on this computer, from GET /models and GET
// /models/{id}. They work in every mode.

// modelEngines are the engines the agent reports (agent.md section 3.8.7).
var modelEngines = []string{"llama", "whisper", "piper"}

func init() {
	Register(&Command{
		Name:    "models",
		Summary: "List the models: stored, loaded and what fits this computer.",
		Usage:   "[--loadable] [--all] [--engine llama|whisper|piper]",
		Description: `By default lists the models of this computer's profile plus any that are stored here.
--all lists the whole catalog. --loadable asks the agent for only the models that can be loaded.
"aictl models show <id>" prints one model in detail. --json prints the agent body unchanged.`,
		Timeout: readOnlyTimeout,
		Flags: func(fs *flag.FlagSet) {
			fs.Bool("loadable", false, "only models that can be loaded on this computer")
			fs.Bool("all", false, "the whole catalog, not only this profile and what is stored")
			fs.String("engine", "", "only one engine: llama, whisper or piper")
		},
		Run: runModels,
		Sub: []*Command{{
			Name:    "show",
			Summary: "Show one model in detail.",
			Usage:   "<model-id>",
			Timeout: readOnlyTimeout,
			Run:     runModelsShow,
		}},
	})
}

func runModels(c *Call) error {
	if err := roNoArgs(c); err != nil {
		return err
	}
	q := url.Values{}
	if c.Bool("loadable") {
		q.Set("loadable", "1")
	}
	if e := c.String("engine"); e != "" {
		if !contains(modelEngines, e) {
			return c.Err(CodeUsage, pick(c.Lang,
				"Unknown engine "+quote(e)+". Use "+strings.Join(modelEngines, ", ")+".",
				"Motor desconocido "+quote(e)+". Use "+strings.Join(modelEngines, ", ")+"."))
		}
		q.Set("engine", e)
	}
	path := "/models"
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	var ms wireModels
	raw, err := roGet(c, path, &ms)
	if err != nil {
		return err
	}
	if c.Global.JSON {
		return roEmit(c, raw)
	}
	renderModels(c.Out, &ms, c.Bool("all") || c.Bool("loadable"))
	return nil
}

// renderModels prints the table. Without all it keeps the models in the profile and the ones stored
// here (the filter is client side, aictl.md 4.2); with --all or --loadable the agent's list is
// shown as it came.
func renderModels(p *out.Printer, ms *wireModels, all bool) {
	tr := func(en, es string) string { return pick(p.Lang(), en, es) }
	var rows [][]string
	for _, m := range ms.Models {
		if !all && !m.InProfile && !m.Present {
			continue
		}
		size := roDash(p)
		if m.SizeBytes > 0 {
			size = out.Size(m.SizeBytes)
		}
		rows = append(rows, []string{m.ID, size, modelStatusWord(p, m.Status), modelNotes(p, m, ms.Default, false)})
	}
	if len(rows) == 0 {
		p.Info(tr("No models match.", "Ningún modelo coincide."))
		return
	}
	p.Table([]out.Col{
		{Title: tr("MODEL", "MODELO")}, {Title: tr("SIZE", "TAMAÑO"), Right: true},
		{Title: tr("STATUS", "ESTADO")}, {Title: tr("NOTES", "NOTAS")},
	}, rows)
}

func modelStatusWord(p *out.Printer, s string) string {
	switch s {
	case "absent":
		return pick(p.Lang(), s, "ausente")
	case "stored":
		return pick(p.Lang(), s, "guardado")
	case "unloaded":
		return pick(p.Lang(), s, "descargado")
	case "loading":
		return pick(p.Lang(), s, "cargando")
	case "loaded":
		return pick(p.Lang(), s, "cargado")
	case "sleeping":
		return pick(p.Lang(), s, "dormido")
	case "failed":
		return pick(p.Lang(), s, "falló")
	case "n/a":
		return pick(p.Lang(), s, "n/d")
	case "":
		return roDash(p)
	}
	return s
}

// modelNotes is the NOTES cell: "default", the roles, and for a model that cannot be loaded the
// agent's reason (already in the user's language through Accept-Language).
func modelNotes(p *out.Printer, m wireModel, defaultID string, full bool) string {
	var parts []string
	if m.Default || (defaultID != "" && m.ID == defaultID) {
		parts = append(parts, pick(p.Lang(), "default", "predeterminado"))
	}
	if len(m.Roles) > 0 {
		parts = append(parts, strings.Join(m.Roles, ", "))
	}
	if !m.Loadable && m.LoadableReason != nil && m.LoadableReason.Message != "" {
		parts = append(parts, p.Symbols().Error+" "+strings.TrimSuffix(m.LoadableReason.Message, "."))
	}
	return strings.Join(parts, roDot(p))
}

func runModelsShow(c *Call) error {
	switch len(c.Args) {
	case 0:
		return c.Err(CodeUsage, pick(c.Lang, "A model id is required.", "Se necesita el id de un modelo."))
	case 1:
	default:
		return c.Err(CodeUsage, pick(c.Lang,
			"Unexpected argument "+quote(c.Args[1])+" (one model only).",
			"Argumento inesperado "+quote(c.Args[1])+" (solo un modelo)."))
	}
	path := "/models/" + url.PathEscape(c.Args[0])
	var m wireModel
	raw, err := roGet(c, path, &m)
	if err != nil {
		return err
	}
	if c.Global.JSON {
		return roEmit(c, raw)
	}
	renderModel(c.Out, &m)
	return nil
}

// renderModel prints one model: a headline and a key/value block (the stored locations, what it
// needs, the files).
func renderModel(p *out.Printer, m *wireModel) {
	tr := func(en, es string) string { return pick(p.Lang(), en, es) }
	head := m.ID
	if m.DisplayName != "" && m.DisplayName != m.ID {
		head = m.DisplayName + " (" + m.ID + ")"
	}
	p.Result(head)
	if m.Summary != "" {
		p.Result(m.Summary)
	}
	p.Blank()
	var rows []out.Pair
	add := func(k, v string) {
		if v != "" {
			rows = append(rows, out.Pair{Label: k, Value: v})
		}
	}
	add(tr("Engine", "Motor"), m.Engine)
	add(tr("Status", "Estado"), modelStatusWord(p, m.Status))
	if m.SizeBytes > 0 {
		add(tr("Size", "Tamaño"), out.Size(m.SizeBytes))
	}
	add(tr("Roles", "Funciones"), strings.Join(m.Roles, ", "))
	if m.Default {
		add(tr("Default", "Predeterminado"), p.Symbols().OK+" "+tr("yes", "sí"))
	}
	switch {
	case m.Loadable:
		add(tr("Loadable", "Se puede cargar"), p.Symbols().OK+" "+tr("yes", "sí"))
	case m.LoadableReason != nil:
		add(tr("Loadable", "Se puede cargar"), p.Symbols().Error+" "+m.LoadableReason.Message+codeSuffix(m.LoadableReason.Code))
	}
	if len(m.Locations) > 0 {
		var locs []string
		for _, l := range m.Locations {
			s := l.Root
			if !l.Complete {
				s += tr(" (incomplete)", " (incompleto)")
			}
			locs = append(locs, s)
		}
		add(tr("Stored in", "Guardado en"), strings.Join(locs, ", "))
	}
	if r := m.Requirements; r != nil {
		var need []string
		if r.MinVRAM > 0 {
			need = append(need, out.GBFromMiB(r.MinVRAM)+tr(" graphics memory", " de memoria gráfica"))
		}
		if r.MinRAM > 0 {
			need = append(need, out.GBFromMiB(r.MinRAM)+tr(" memory", " de memoria"))
		}
		if len(r.Backends) > 0 {
			need = append(need, strings.Join(r.Backends, "/"))
		}
		add(tr("Needs", "Necesita"), strings.Join(need, roDot(p)))
	}
	add(tr("Aliases", "Alias"), strings.Join(m.Aliases, ", "))
	if m.LastLoadMS != nil {
		add(tr("Last load", "Última carga"), out.Duration(time.Duration(*m.LastLoadMS)*time.Millisecond))
	}
	if n := len(m.Files); n > 0 {
		add(tr("Files", "Archivos"), strconv.Itoa(n))
	}
	p.KV(rows)
}

// codeSuffix is "  [CODE]" for a doctor code the agent attached to a reason, or "".
func codeSuffix(code string) string {
	if code == "" {
		return ""
	}
	return "  [" + code + "]"
}
