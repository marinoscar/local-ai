package cli

import (
	"flag"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// `aictl restart <llama|kiwix|whisper> [--force]` (aictl.md section 4.14), the alias of
// `aictl services restart <name>` (section 4.13, AICTL-10): POST /services/{name}/restart. The agent
// waits up to 60 s for the unit and, for llama, for the router and the models it restores, so the
// answer (200) says which models are loaded again.
//
// restartService is the one implementation: AICTL-10 binds `services restart` to it so the two forms
// behave identically.

// restartTimeout is the route deadline of the agent: 60 s for the unit plus the router wait and the
// model restore (agent.md section 3.8.22).
const restartTimeout = 120 * time.Second

// serviceNames are the services `restart` accepts (agent.md section 3.8.22).
var serviceNames = []string{"llama", "kiwix", "whisper"}

// serviceActionAnswer is the 200 body of POST /services/{name}/{action}.
type serviceActionAnswer struct {
	Name           string   `json:"name"`
	Active         string   `json:"active"`
	RestoredModels []string `json:"restored_models"`
	OpID           string   `json:"op_id"`
}

func init() {
	Register(&Command{
		Name:    "restart",
		Summary: "Restart a service (llama, kiwix or whisper).",
		Usage:   "<llama|kiwix|whisper> [--force]",
		Description: `Alias of "aictl services restart". Restarting llama waits until the models that were loaded
are loaded again. --force is sent to the agent as it is (the agent needs it only to stop llama in survival
mode). --json prints the agent body unchanged.`,
		Timeout: restartTimeout,
		Flags: func(fs *flag.FlagSet) {
			fs.Bool("force", false, "send force to the agent (for llama in survival mode)")
		},
		Run: func(c *Call) error {
			if err := c.exactArgs(1, "<llama|kiwix|whisper>"); err != nil {
				return err
			}
			return c.restartService(c.Args[0])
		},
	})
}

// serviceWords is how a service is called in a sentence, with its article ("the AI model service").
func serviceWords(lang Lang, name string) string {
	switch name {
	case "llama":
		return pick(lang, "the AI model service", "el servicio de modelos de IA")
	case "kiwix":
		return pick(lang, "the library service", "el servicio de biblioteca")
	case "whisper":
		return pick(lang, "the speech service", "el servicio de voz")
	}
	return name
}

// restartService restarts one service and tells what came back.
func (c *Call) restartService(name string) error {
	if !contains(serviceNames, name) {
		return c.usageErr(
			pick(c.Lang, "Unknown service "+quote(name)+".", "Servicio desconocido "+quote(name)+"."),
			pick(c.Lang, "Use "+strings.Join(serviceNames, ", ")+".", "Use "+strings.Join(serviceNames, ", ")+"."))
	}
	cl, err := c.Client()
	if err != nil {
		return err
	}
	body := map[string]any{}
	if c.Bool("force") {
		body["force"] = true
	}
	words := serviceWords(c.Lang, name)
	if !c.Global.JSON {
		c.Out.Progress(pick(c.Lang, "Restarting "+words, "Reiniciando "+words))
	}
	var a serviceActionAnswer
	raw, _, err := c.callWith(cl, c.Ctx, http.MethodPost, "/services/"+url.PathEscape(name)+"/restart", body, &a)
	if err != nil {
		return err
	}
	if c.Global.JSON {
		return c.emitRaw(raw)
	}
	line := pick(c.Lang, capitalize(words)+" is running again.", capitalize(words)+" está en marcha otra vez.")
	if name == "llama" {
		if len(a.RestoredModels) > 0 {
			names := c.modelNames(cl)
			verb := pick(c.Lang, " is loaded.", " está cargado.")
			if len(a.RestoredModels) > 1 {
				verb = pick(c.Lang, " are loaded.", " están cargados.")
			}
			line += " " + names.list(c.Lang, a.RestoredModels) + verb
		} else {
			line += pick(c.Lang, " No model is loaded yet: load one with `aictl load <model-id>`.",
				" Todavía no hay ningún modelo cargado: cargue uno con `aictl load <id-del-modelo>`.")
		}
	}
	c.Out.OK(line)
	return nil
}

// capitalize upper-cases the first letter of s.
func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
