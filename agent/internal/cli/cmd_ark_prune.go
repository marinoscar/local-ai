package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/cli/out"
)

// `aictl ark prune [--root <id>]... [--orphans] [--trash] [--dry-run] [--yes]` (aictl.md section 4.10,
// ark-sync.md sections 3.4 and 4.10): POST /ark/prune, as an administrator.
//
//   - At least one of --orphans and --trash is required: a prune that names nothing does nothing.
//   - The command always asks the agent for a dry run first and prints what would go and what is kept.
//     With --dry-run that is all it does. Otherwise it asks the person to type DELETE (--yes answers
//     it) and only then sends the real request.
//   - The agent never deletes the last verified copy of anything and the command has no flag that
//     could ask it to: an entry the agent keeps for that reason is printed with the sentence "This is
//     the only copy of <item> on this computer; it was kept."
//   - The admin PIN is asked only when the agent asks for it (an operator over the socket); root is
//     never asked.

// pruneAnswer is the 200 body of POST /ark/prune (agent.md section 3.8.20a).
type pruneAnswer struct {
	Deleted []struct {
		Root    string `json:"root"`
		Item    string `json:"item"`
		Version string `json:"version"`
		Bytes   int64  `json:"bytes"`
		Kind    string `json:"kind"`
	} `json:"deleted"`
	Refused []struct {
		Root    string `json:"root"`
		Item    string `json:"item"`
		Version string `json:"version"`
		Reason  string `json:"reason"`
		Kind    string `json:"kind"`
	} `json:"refused"`
	FreedBytes int64 `json:"freed_bytes"`
}

func init() {
	attachAll(at(&Command{
		Name: "prune", Summary: "Delete content the profile no longer wants, and old versions.", Marks: MarkAdmin,
		Usage: "[--root <id>]... [--orphans] [--trash] [--dry-run] [--yes]",
		Description: `Frees disk space. Choose what to delete:
  --orphans   content that is stored but no longer in the profile
  --trash     old versions that were kept after an update
Without --root it works on every content folder that can be written. It shows the list first, then
asks you to type DELETE (--yes answers for you; --json needs --yes). With --dry-run it only shows the list.
The last verified copy of anything is never deleted, and nothing can be added to the command to do it:
copy the content to another drive first (aictl ark sync --root <drive>), then prune.`,
		Timeout: 3 * time.Minute,
		Flags: func(fs *flag.FlagSet) {
			fs.Var(&multiFlag{}, "root", "a root id (internal, ark-<8 hex>) or a path; may be repeated")
			fs.Bool("orphans", false, "delete content that is no longer in the profile")
			fs.Bool("trash", false, "delete old versions kept after an update")
			fs.Bool("dry-run", false, "show what would be deleted and change nothing")
			fs.Bool("yes", false, "do not ask to type DELETE")
			fs.Bool("y", false, "same as --yes")
		},
		Run: arkPrune,
	}, "ark"))
}

func arkPrune(c *Call) error {
	applyYes(c)
	if err := c.noArgs(); err != nil {
		return err
	}
	roots := c.multi("root")
	for _, r := range roots {
		if err := c.checkRoot("--root", r, true); err != nil {
			return err
		}
	}
	orphans, trash, dry := c.Bool("orphans"), c.Bool("trash"), c.Bool("dry-run")
	if !orphans && !trash {
		return c.usageErr(
			pick(c.Lang, "Say what to prune: --orphans, --trash or both.", "Indique qué limpiar: --orphans, --trash o ambos."),
			pick(c.Lang, "`aictl ark prune --orphans --dry-run` shows what would go first.", "`aictl ark prune --orphans --dry-run` muestra primero lo que se borraría."))
	}
	// Nobody to ask (a pipe, --json) and no --yes: say so before any request.
	if !dry && !c.Global.Yes {
		if err := c.requireTerminalForConfirm(false); err != nil {
			e, ok := err.(*CLIError)
			if ok {
				e.Fix = pick(c.Lang, "Run it in a terminal, or add `--yes`.", "Ejecútelo en una terminal, o agregue `--yes`.")
			}
			return err
		}
	}
	cl, err := c.Client()
	if err != nil {
		return err
	}
	send := func(dryRun bool) (pruneAnswer, json.RawMessage, error) {
		var res pruneAnswer
		var raw json.RawMessage
		body := map[string]any{"roots": roots, "orphans": orphans, "trash": trash, "dry_run": dryRun}
		err := c.Admin(func() (e error) {
			raw, _, e = c.callWith(cl, c.Ctx, http.MethodPost, "/ark/prune", body, &res)
			return e
		})
		return res, raw, err
	}

	// The list first, always.
	plan, raw, err := send(true)
	if err != nil {
		return err
	}
	if dry {
		if c.Global.JSON {
			return c.emitRaw(raw)
		}
		c.prunePrint(plan, true)
		c.Out.Result(pick(c.Lang, "Nothing was changed (dry run).", "No se cambió nada (simulacro)."))
		return nil
	}
	if len(plan.Deleted) == 0 {
		if c.Global.JSON {
			return c.emitRaw(raw)
		}
		c.prunePrint(plan, true)
		c.Out.Info(pick(c.Lang, "Nothing to delete.", "No hay nada que borrar."))
		return nil
	}
	if !c.Global.Yes {
		c.prunePrint(plan, true)
		ok, err := c.ConfirmTyped(pick(c.Lang,
			fmt.Sprintf("Delete %s from this computer? This cannot be undone. Type DELETE to confirm:", pruneCount(c, len(plan.Deleted), plan.FreedBytes)),
			fmt.Sprintf("¿Borrar %s de este equipo? No se puede deshacer. Escriba DELETE para confirmar:", pruneCount(c, len(plan.Deleted), plan.FreedBytes))), "DELETE")
		if err != nil || !ok {
			if err != nil {
				return err
			}
			c.Out.Info(pick(c.Lang, "Nothing was deleted.", "No se borró nada."))
			return nil
		}
	}
	res, raw, err := send(false)
	if err != nil {
		return err
	}
	if c.Global.JSON {
		return c.emitRaw(raw)
	}
	c.prunePrint(res, false)
	return nil
}

func pruneCount(c *Call, n int, bytes int64) string {
	return pick(c.Lang,
		fmt.Sprintf("%d item(s) (%s)", n, sizeText(bytes)),
		fmt.Sprintf("%d elemento(s) (%s)", n, sizeText(bytes)))
}

// prunePrint prints what a prune deleted (or, for a list, would delete) and what it kept.
func (c *Call) prunePrint(a pruneAnswer, plan bool) {
	l := c.Lang
	if len(a.Deleted) > 0 {
		if plan {
			c.Out.Info(pick(l, "Would delete "+pruneCount(c, len(a.Deleted), a.FreedBytes)+":", "Se borraría "+pruneCount(c, len(a.Deleted), a.FreedBytes)+":"))
			cols := []out.Col{
				{Title: pick(l, "Item", "Elemento")}, {Title: pick(l, "Version", "Versión")}, {Title: pick(l, "Where", "Dónde")},
				{Title: pick(l, "Size", "Tamaño"), Right: true}, {Title: pick(l, "Why", "Motivo")},
			}
			var rows [][]string
			for _, d := range a.Deleted {
				version := d.Version
				if len(version) > 10 {
					version = version[:10]
				}
				why := pick(l, "not in the profile", "ya no está en el perfil")
				if d.Kind == "trash" {
					why = pick(l, "old version", "versión anterior")
				}
				rows = append(rows, []string{d.Item, orText(version, "-"), d.Root, sizeText(d.Bytes), why})
			}
			c.Out.Table(cols, rows)
			c.Out.Blank()
		} else {
			for _, d := range a.Deleted {
				name := d.Item
				if d.Version != "" {
					name += " (" + d.Version + ")"
				}
				c.Out.OK(pick(l,
					"Deleted "+name+" from "+d.Root+" ("+sizeText(d.Bytes)+" freed).",
					"Se borró "+name+" de "+d.Root+" ("+sizeText(d.Bytes)+" liberados)."))
			}
			c.Out.Result(pick(l, "Freed "+sizeText(a.FreedBytes)+".", "Se liberaron "+sizeText(a.FreedBytes)+"."))
		}
	}
	for _, k := range a.Refused {
		c.Out.Info(c.pruneKept(k.Item, k.Root, k.Reason))
	}
}

// pruneKept words why an entry was kept.
func (c *Call) pruneKept(item, root, reason string) string {
	switch reason {
	case "last_verified_copy":
		return pick(c.Lang,
			"This is the only copy of "+item+" on this computer; it was kept. Copy it to another drive first (aictl ark sync --root <drive>), then prune.",
			"Esta es la única copia de "+item+" en este equipo; se conservó. Cópiela primero a otra unidad (aictl ark sync --root <unidad>), y luego limpie.")
	case "read_only":
		return pick(c.Lang,
			root+" is read-only, so "+item+" was kept.",
			root+" es de solo lectura, así que "+item+" se conservó.")
	case "replacement_unverified":
		return pick(c.Lang,
			"The newer version of "+item+" is not checked yet, so the old version was kept.",
			"La versión nueva de "+item+" aún no se ha comprobado, así que se conservó la anterior.")
	}
	return pick(c.Lang, item+" was kept.", item+" se conservó.")
}
