package cli

import (
	"github.com/marinoscar/local-ai/agent/internal/cli/out"
)

// `aictl default` (aictl.md section 4.5), show path only: the model that starts with this computer,
// from GET /models. Setting it (`aictl default <model-id>`) belongs to AICTL-05.

func init() {
	Register(&Command{
		Name:    "default",
		Summary: "Show the default model (the one that starts with this computer).",
		Description: `Prints the default model. Changing it is not available yet.
--json prints {"default": "<model id>"}.`,
		Timeout: readOnlyTimeout,
		Run:     runDefault,
	})
}

func runDefault(c *Call) error {
	if len(c.Args) > 0 {
		return notImplemented("default <model-id>", "AICTL-05")(c)
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
