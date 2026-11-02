package cli

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/config"
)

// Helpers shared by the model and service commands of AICTL-05 (load, unload, default, download,
// delete, restart, bench; aictl.md sections 4.3 to 4.7, 4.14 and 4.24). Nothing here knows a route
// beyond GET /models; each command file owns its own wire types.

// lookupTimeout bounds the best-effort reads that only make a message friendlier (a display name,
// a size). A slow or failing read never fails the command: the id is shown instead.
const lookupTimeout = 3 * time.Second

// modelIDArg is the usage check of the commands that take one model id (aictl.md section 4.3): one
// argument, in the catalog's id format (`^[a-z0-9][a-z0-9.\-]{2,63}$`). A malformed id is exit 2
// before the agent is asked anything.
func (c *Call) modelIDArg() (string, error) {
	if err := c.exactArgs(1, "<model-id>"); err != nil {
		return "", err
	}
	return c.checkModelID(c.Args[0])
}

// checkModelID accepts a model id and refuses anything else with a usage error.
func (c *Call) checkModelID(id string) (string, error) {
	if itemIDRe.MatchString(id) {
		return id, nil
	}
	return "", c.usageErr(
		pick(c.Lang, fmt.Sprintf("%q is not a model id.", id), fmt.Sprintf("%q no es un identificador de modelo.", id)),
		pick(c.Lang, "Model ids are lower-case words like `qwen3.5-9b-q4km`; `aictl models` lists them.",
			"Los identificadores son palabras en minúsculas como `qwen3.5-9b-q4km`; `aictl models` los muestra."))
}

// modelPath is GET/POST path of one model, with the id escaped (it is validated, but never trusted
// to be a path segment).
func modelPath(id string, tail string) string {
	return "/models/" + url.PathEscape(id) + tail
}

// modelDetail reads GET /models/{id} for a friendlier message. It returns nil when the agent does not
// answer in lookupTimeout or says anything but 200: callers fall back to the id.
func (c *Call) modelDetail(cl *Client, id string) *wireModel {
	ctx, cancel := context.WithTimeout(c.Ctx, lookupTimeout)
	defer cancel()
	var m wireModel
	if _, _, err := c.callWith(cl, ctx, http.MethodGet, modelPath(id, ""), nil, &m); err != nil {
		return nil
	}
	return &m
}

// modelNames reads GET /models once and returns the display name of every model by id, for the
// progress lines that name a model by the id an event carries. Empty on any failure.
func (c *Call) modelNames(cl *Client) modelNameMap {
	names := modelNameMap{}
	ctx, cancel := context.WithTimeout(c.Ctx, lookupTimeout)
	defer cancel()
	var ms wireModels
	if _, _, err := c.callWith(cl, ctx, http.MethodGet, "/models", nil, &ms); err != nil {
		return names
	}
	for _, m := range ms.Models {
		if m.DisplayName != "" {
			names[m.ID] = m.DisplayName
		}
	}
	return names
}

// modelNameMap maps a model id to its display name.
type modelNameMap map[string]string

// of is the display name of id, or the id.
func (n modelNameMap) of(id string) string {
	if v := n[id]; v != "" {
		return v
	}
	return id
}

// list names several models, "A", "A and B", "A, B and C".
func (n modelNameMap) list(lang Lang, ids []string) string {
	names := make([]string, 0, len(ids))
	for _, id := range ids {
		names = append(names, n.of(id))
	}
	return joinWords(lang, names)
}

// joinWords is "A", "A and B" or "A, B and C" (es: "A y B").
func joinWords(lang Lang, words []string) string {
	and := pick(lang, " and ", " y ")
	switch len(words) {
	case 0:
		return ""
	case 1:
		return words[0]
	}
	return strings.Join(words[:len(words)-1], ", ") + and + words[len(words)-1]
}

// modelLabel is the display name of a model read from GET /models/{id}, or the id.
func modelLabel(m *wireModel, id string) string {
	if m != nil && m.DisplayName != "" {
		return m.DisplayName
	}
	return id
}

// llamaLocalBase is the address programs on this computer use for the model service (overview.md
// section 3, the llama port unless node.yaml says otherwise).
var llamaLocalBase = fmt.Sprintf("http://127.0.0.1:%d/v1", config.DefaultLlamaPort)
