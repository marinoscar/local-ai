package cli

import (
	"flag"
	"net/http"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
)

// `aictl unload <model-id> [--force]` (aictl.md section 4.4): POST /models/{id}/unload. In survival
// mode the agent refuses to unload the last model unless the body says "force": true (409
// LAI-AGT-023, exit 6).

// modelUnloadAnswer is the 200 body of POST /models/{id}/unload (agent.md section 3.8.9).
type modelUnloadAnswer struct {
	ID              string   `json:"id"`
	Status          string   `json:"status"`
	AlreadyUnloaded bool     `json:"already_unloaded"`
	OpID            string   `json:"op_id"`
	Restarted       bool     `json:"restarted"`
	Restored        []string `json:"restored"`
}

func init() {
	Register(&Command{
		Name:    "unload",
		Summary: "Unload a model to free graphics memory.",
		Usage:   "<model-id> [--force]",
		Description: `In survival mode the last loaded model is kept unless you add --force.
--json prints the agent body unchanged.`,
		Timeout: 120 * time.Second,
		Flags: func(fs *flag.FlagSet) {
			fs.Bool("force", false, "unload the last model even in survival mode")
		},
		Run: runUnload,
	})
}

func runUnload(c *Call) error {
	id, err := c.modelIDArg()
	if err != nil {
		return err
	}
	cl, err := c.Client()
	if err != nil {
		return err
	}
	body := map[string]any{}
	if c.Bool("force") {
		body["force"] = true
	}
	var a modelUnloadAnswer
	raw, _, err := c.callWith(cl, c.Ctx, http.MethodPost, modelPath(id, "/unload"), body, &a)
	if err != nil {
		return err
	}
	if c.Global.JSON {
		return c.emitRaw(raw)
	}
	name := modelLabel(c.modelDetail(cl, id), id)
	if a.AlreadyUnloaded {
		c.Out.Info(pick(c.Lang, name+" was already unloaded.", name+" ya estaba descargado."))
		return nil
	}
	c.Out.OK(pick(c.Lang, name+" was unloaded.", name+" se descargó."))
	if a.Restarted {
		// The model would not stop, so the agent restarted llama and loaded the others again.
		p := c.codeProblem(doctor.LLM031, "")
		if len(a.Restored) > 0 {
			all := c.modelNames(cl)
			p.Message = sentence(p.Message) + pick(c.Lang,
				" Loaded again: "+all.list(c.Lang, a.Restored)+".",
				" Se cargaron de nuevo: "+all.list(c.Lang, a.Restored)+".")
		}
		c.Out.Problem(p)
	}
	return nil
}
