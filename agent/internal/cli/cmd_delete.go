package cli

import (
	"encoding/json"
	"flag"
	"net/http"
	"net/url"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/cli/out"
)

// `aictl delete <model-id> [--root <path>] [--allow-last-copy] [--yes]` (aictl.md section 4.7):
// DELETE /models/{id}?root=...&keep_last=... after a confirmation, as an administrator.
//
//   - The question names the model and its size, from GET /models/{id}. It defaults to No.
//   - --allow-last-copy sets keep_last=false and asks a second question that only a person can answer:
//     they must type the model id. --yes does not answer that one, so it needs a terminal even with
//     --yes (and so it cannot be combined with --json).
//   - The admin PIN is asked only when the agent asks for it (an operator over the socket); root is
//     never asked.
//   - The agent refuses a loaded model (409 LAI-AGT-022) and, unless --allow-last-copy, the last
//     verified copy anywhere (409): both are exit 6. Nothing is deleted then.

// modelDeleted is the 200 body of DELETE /models/{id} (agent.md section 3.8.12).
type modelDeleted struct {
	ID      string `json:"id"`
	Deleted []struct {
		Root  string `json:"root"`
		Bytes int64  `json:"bytes"`
	} `json:"deleted"`
	OpID string `json:"op_id"`
}

func init() {
	Register(&Command{
		Name:    "delete",
		Summary: "Delete a model from this computer.",
		Marks:   MarkAdmin,
		Usage:   "<model-id> [--root <path>] [--allow-last-copy] [--yes]",
		Description: `Asks first (the default answer is No). Without --root the model is deleted from every content
folder of this computer; --root limits it to one (a path or a root id from aictl ark drives).
A loaded model is refused: unload it first. The last verified copy is kept unless you add --allow-last-copy,
which also asks you to type the model id. --json needs --yes (and cannot be combined with --allow-last-copy).`,
		Timeout: 60 * time.Second,
		Flags: func(fs *flag.FlagSet) {
			fs.String("root", "", "delete only from this content root: a path or a root id")
			fs.Bool("allow-last-copy", false, "also delete the last verified copy (asks you to type the model id)")
			fs.Bool("yes", false, "do not ask the first question")
			fs.Bool("y", false, "same as --yes")
		},
		Run: runDelete,
	})
}

func runDelete(c *Call) error {
	applyYes(c)
	id, err := c.modelIDArg()
	if err != nil {
		return err
	}
	root := c.String("root")
	if root != "" {
		if err := c.checkRoot("--root", root, true); err != nil {
			return err
		}
	}
	lastCopy := c.Bool("allow-last-copy")

	// Nobody to ask (a pipe, --json): say so before any request. The typed question is never
	// answered by --yes, so it needs a person whatever the flags say.
	if lastCopy {
		if err := c.requireTerminalForConfirm(true); err != nil {
			return err
		}
	}
	if !c.Global.Yes {
		if err := c.requireTerminalForConfirm(false); err != nil {
			return err
		}
	}

	cl, err := c.Client()
	if err != nil {
		return err
	}
	var detail *wireModel
	if !c.Global.Yes {
		// The question needs the model: its name and size. A model that does not exist ends here.
		var m wireModel
		if _, _, err := c.callWith(cl, c.Ctx, http.MethodGet, modelPath(id, ""), nil, &m); err != nil {
			return err
		}
		detail = &m
		where := pick(c.Lang, "this computer", "este equipo")
		if root != "" {
			where = root
		}
		size := ""
		if m.SizeBytes > 0 {
			size = " (" + sizeText(m.SizeBytes) + ")"
		}
		ok, err := c.Confirm(pick(c.Lang,
			"Delete "+modelLabel(detail, id)+" from "+where+size+"?",
			"¿Borrar "+modelLabel(detail, id)+" de "+where+size+"?"))
		if err != nil || !ok {
			return c.deleteDeclined(err)
		}
	}
	if lastCopy {
		ok, err := c.ConfirmTyped(pick(c.Lang,
			"This is the last verified copy anywhere. Type the model id to confirm:",
			"Esta es la última copia verificada que existe. Escriba el identificador del modelo para confirmar:"), id)
		if err != nil || !ok {
			return c.deleteDeclined(err)
		}
	}
	if detail == nil && !c.Global.JSON {
		detail = c.modelDetail(cl, id)
	}

	q := url.Values{}
	if root != "" {
		q.Set("root", root)
	}
	q.Set("keep_last", boolWord(!lastCopy))
	path := modelPath(id, "") + "?" + q.Encode()

	var res modelDeleted
	var raw json.RawMessage
	err = c.Admin(func() (e error) {
		raw, _, e = c.callWith(cl, c.Ctx, http.MethodDelete, path, nil, &res)
		return e
	})
	if err != nil {
		return err
	}
	if c.Global.JSON {
		return c.emitRaw(raw)
	}
	c.deleteDone(modelLabel(detail, id), res)
	return nil
}

// deleteDeclined ends the command after a No (or the end of the input): nothing was deleted. A
// non-nil err (Ctrl-C at the prompt, a closed terminal) is returned as it is.
func (c *Call) deleteDeclined(err error) error {
	if err != nil {
		return err
	}
	c.Out.Info(pick(c.Lang, "Nothing was deleted.", "No se borró nada."))
	return nil
}

// deleteDone prints what was deleted, one line per content folder.
func (c *Call) deleteDone(name string, res modelDeleted) {
	if len(res.Deleted) == 0 {
		c.Out.Info(pick(c.Lang, name+" was not stored there. Nothing was deleted.", name+" no estaba guardado allí. No se borró nada."))
		return
	}
	for _, d := range res.Deleted {
		c.Out.OK(pick(c.Lang,
			"Deleted "+name+" from "+d.Root+" ("+out.Size(d.Bytes)+" freed).",
			"Se borró "+name+" de "+d.Root+" ("+out.Size(d.Bytes)+" liberados)."))
	}
}

func boolWord(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
