package cli

import (
	"net/http"

	"github.com/marinoscar/local-ai/agent/internal/cli/out"
)

// `aictl default [<model-id>]` (aictl.md section 4.5). Without an argument it shows the model that
// starts with this computer (GET /models); with one it sets it (PUT /models/default). Setting it does
// not load the model.

// modelDefaultAnswer is the 200 body of PUT /models/default (agent.md section 3.8.10).
type modelDefaultAnswer struct {
	Default        string  `json:"default"`
	Previous       *string `json:"previous"`
	IniRegenerated bool    `json:"ini_regenerated"`
}

func init() {
	Register(&Command{
		Name:    "default",
		Summary: "Show or change the default model (the one that starts with this computer).",
		Usage:   "[<model-id>]",
		Description: `Without a model id, prints the default model. With one, makes it the default: it starts
automatically when the computer starts. It does not load the model now (use aictl load).
--json prints {"default": "<model id>"} when showing, and the agent body when changing.`,
		Timeout: readOnlyTimeout,
		Run:     runDefault,
	})
}

func runDefault(c *Call) error {
	if len(c.Args) > 0 {
		return runDefaultSet(c)
	}
	var ms struct {
		Default *string     `json:"default"`
		Models  []wireModel `json:"models"`
	}
	if _, err := roGet(c, "/models", &ms); err != nil {
		return err
	}
	id := ""
	if ms.Default != nil {
		id = *ms.Default
	}
	if c.Global.JSON {
		return c.Out.JSON().Document(map[string]string{"default": id})
	}
	renderDefault(c.Out, id, ms.Models)
	return nil
}

func runDefaultSet(c *Call) error {
	id, err := c.modelIDArg()
	if err != nil {
		return err
	}
	cl, err := c.Client()
	if err != nil {
		return err
	}
	var a modelDefaultAnswer
	raw, _, err := c.callWith(cl, c.Ctx, http.MethodPut, "/models/default", map[string]string{"id": id}, &a)
	if err != nil {
		return err
	}
	if c.Global.JSON {
		return c.emitRaw(raw)
	}
	if a.Default == "" {
		a.Default = id
	}
	m := c.modelDetail(cl, a.Default)
	name := modelLabel(m, a.Default)
	c.Out.OK(pick(c.Lang,
		"The default model is now "+name+". It starts automatically when the computer starts.",
		"El modelo predeterminado ahora es "+name+". Se inicia solo cuando arranca el equipo."))
	if m != nil {
		if m.Status == "loaded" {
			c.Out.Secondary(pick(c.Lang, "  It is loaded right now.", "  Está cargado ahora."))
		} else {
			c.Out.Secondary(pick(c.Lang,
				"  It is not loaded right now. Load it with `aictl load "+a.Default+"`.",
				"  No está cargado ahora. Cárguelo con `aictl load "+a.Default+"`."))
		}
	}
	return nil
}

// renderDefault says which model is the default, with its display name and whether it is loaded.
func renderDefault(p *out.Printer, id string, models []wireModel) {
	tr := func(en, es string) string { return pick(p.Lang(), en, es) }
	if id == "" {
		p.Info(tr("No default model is set. Choose one with `aictl models`.",
			"No hay un modelo predeterminado. Elija uno con `aictl models`."))
		return
	}
	name := id
	state := ""
	for _, m := range models {
		if m.ID != id {
			continue
		}
		if m.DisplayName != "" && m.DisplayName != id {
			name = m.DisplayName + " (" + id + ")"
		}
		switch m.Status {
		case "loaded":
			state = tr(" It is loaded.", " Está cargado.")
		case "loading":
			state = tr(" It is loading.", " Se está cargando.")
		case "absent":
			state = tr(" It is not stored on this computer.", " No está guardado en este equipo.")
		default:
			state = tr(" It is not loaded right now.", " No está cargado ahora.")
		}
	}
	p.Result(tr("The default model is ", "El modelo predeterminado es ") + name + "." + state)
}
