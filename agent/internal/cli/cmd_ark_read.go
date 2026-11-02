package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/marinoscar/local-ai/agent/internal/cli/out"
)

// `aictl ark` / `aictl ark status` and `aictl ark drives` (aictl.md section 4.10, ark-sync.md section
// 3.4): how complete the offline content is and which drives are connected. Read-only; sync, verify,
// import and drive management are other issues (AICTL-06, SYNC-03).

func init() {
	Register(&Command{
		Name:    "ark",
		Summary: "Show how complete the offline content is and which ark drives are connected.",
		Description: `"aictl ark" is the same as "aictl ark status".
--json: "ark status" prints {"ark": <GET /ark>, "sync": <GET /ark/sync> or null}; "ark drives" prints the agent body.`,
		Timeout: readOnlyTimeout,
		Run:     runArkStatus,
		Sub: []*Command{
			{
				Name:    "status",
				Summary: "Show how complete the content is on this computer and on the ark drive.",
				Timeout: readOnlyTimeout,
				Run:     runArkStatus,
			},
			{
				Name:    "drives",
				Summary: "List the drives that look like an ark drive, with the problems of unusable ones.",
				Timeout: readOnlyTimeout,
				Run:     runArkDrives,
			},
		},
	})
}

func runArkStatus(c *Call) error {
	if err := roNoArgs(c); err != nil {
		return err
	}
	var ark wireArk
	rawArk, err := roGet(c, "/ark", &ark)
	if err != nil {
		return err
	}
	// The last run is extra detail: if the agent has none to show, the status still prints.
	var run wireSyncRun
	rawSync, err := roGet(c, "/ark/sync", &run)
	if err != nil {
		var ae *APIError
		if !errors.As(err, &ae) {
			return err
		}
		rawSync, run = nil, wireSyncRun{}
	}
	if c.Global.JSON {
		doc := map[string]json.RawMessage{"ark": rawArk, "sync": rawSync}
		if rawSync == nil {
			doc["sync"] = json.RawMessage("null")
		}
		return c.Out.JSON().Document(doc)
	}
	renderArkStatus(c.Out, &ark, &run)
	return nil
}

// readyPct is the share of wanted items that are verified, from the READY.json counts (orphans are
// not wanted). ok is false when nothing is wanted or the agent sent no counts. An explicit
// ready_pct from the agent wins.
func readyPct(r *wireReady) (pct float64, ok bool) {
	if r.ReadyPct != nil {
		return *r.ReadyPct, true
	}
	total := 0
	for k, n := range r.Counts {
		if k != "orphan" {
			total += n
		}
	}
	if total == 0 {
		return 0, false
	}
	return float64(r.Counts["verified"]) * 100 / float64(total), true
}

// readySymbol follows the root's overall colour (green, yellow, red); without one, 100% is ok.
func readySymbol(p *out.Printer, r *wireReady, pct float64, hasPct bool) string {
	sym := p.Symbols()
	switch r.Overall {
	case "green":
		return sym.OK
	case "yellow":
		return sym.Warn
	case "red":
		return sym.Error
	}
	if hasPct && pct >= 100 {
		return sym.OK
	}
	return sym.Warn
}

func renderArkStatus(p *out.Printer, ark *wireArk, run *wireSyncRun) {
	tr := func(en, es string) string { return pick(p.Lang(), en, es) }
	sym := p.Symbols()
	labelOf := func(r *wireReady) string {
		if r.Root.Kind == "internal" {
			return tr("Internal disk", "Disco interno")
		}
		return tr("Ark drive", "Unidad ark")
	}
	notConnected := tr("not connected", "no conectada")
	lw := out.DisplayWidth(tr("Ark drive", "Unidad ark"))
	hasDrive := false
	for i := range ark.Roots {
		if w := out.DisplayWidth(labelOf(&ark.Roots[i])); w > lw {
			lw = w
		}
		if ark.Roots[i].Root.Kind != "internal" {
			hasDrive = true
		}
	}

	if len(ark.Roots) == 0 {
		p.Result(roPad(tr("Internal disk", "Disco interno"), lw) + "  " + sym.Warn + " " + tr("no content folder reported", "sin carpeta de contenido"))
	}
	for i := range ark.Roots {
		r := &ark.Roots[i]
		pct, hasPct := readyPct(r)
		line := roPad(roPad(labelOf(r), lw)+"  "+r.Root.ID, lw+2+12) + "  " + readySymbol(p, r, pct, hasPct) + " "
		if hasPct {
			line += fmt.Sprintf("%.0f%% %s", pct, tr("ready", "listo"))
		} else {
			line += tr("not checked", "sin comprobar")
		}
		var detail []string
		if r.Root.BudgetBytes > 0 {
			detail = append(detail, out.Size(r.Root.UsedBytes)+tr(" of ", " de ")+out.Size(r.Root.BudgetBytes)+tr(" budget", " de presupuesto"))
		}
		if r.Root.FSFreeBytes > 0 {
			detail = append(detail, out.Size(r.Root.FSFreeBytes)+tr(" free", " libres"))
		}
		if r.Root.ReadOnly {
			line += "  " + sym.Warn + " " + tr("read-only", "solo lectura")
		}
		if len(detail) > 0 {
			d := "(" + strings.Join(detail, ", ") + ")"
			if out.DisplayWidth(line)+3+out.DisplayWidth(d) <= out.DefaultWidth {
				line += "   " + d
				d = ""
			}
			p.Result(line)
			if d != "" {
				p.Result("  " + d) // too wide for the line: the sizes go on their own line
			}
		} else {
			p.Result(line)
		}

		var missing []string
		for _, it := range r.Items {
			if it.Status != "missing" {
				continue
			}
			s := it.ID
			if it.Bytes > 0 {
				s += " (" + out.Size(it.Bytes) + ")"
			}
			missing = append(missing, s)
		}
		if len(missing) > 0 {
			first := "  " + tr("missing: ", "falta: ")
			for _, l := range out.Wrap(strings.Join(missing, ", "), out.DefaultWidth, first, strings.Repeat(" ", out.DisplayWidth(first))) {
				p.Result(l)
			}
		}
		if ls := r.LastSync; ls != nil && ls.FinishedAt != "" {
			p.Result("  " + tr("last sync ", "última sincronización ") + roAgo(p, ls.FinishedAt) + ": " + syncStateWord(p, ls.State))
		} else {
			p.Result("  " + tr("never synced", "nunca sincronizado"))
		}
	}
	if !hasDrive {
		p.Result(roPad(tr("Ark drive", "Unidad ark"), lw) + "  " + notConnected)
	}

	if run.State == "running" || run.State == "cancelling" {
		line := tr("Sync running", "Sincronización en curso")
		if run.Totals.Items > 0 {
			line += fmt.Sprintf(tr(": %d of %d items", ": %d de %d elementos"), run.Totals.Done, run.Totals.Items)
		}
		p.Blank()
		p.Info(line + ".")
	}

	// Problems the readiness reports carry, in the problem layout.
	first := true
	for i := range ark.Roots {
		for _, pr := range ark.Roots[i].Problems {
			if first {
				p.Blank()
				first = false
			}
			p.Finding(out.Problem{Severity: roSeverity(pr.Severity), Code: pr.Code, Message: sentence(pr.Title)})
		}
	}
}

func syncStateWord(p *out.Printer, s string) string {
	switch s {
	case "done":
		return "ok"
	case "failed":
		return pick(p.Lang(), "failed", "falló")
	case "cancelled":
		return pick(p.Lang(), "cancelled", "cancelada")
	case "skipped":
		return pick(p.Lang(), "skipped", "omitida")
	}
	return s
}

func runArkDrives(c *Call) error {
	if err := roNoArgs(c); err != nil {
		return err
	}
	var d wireDrives
	raw, err := roGet(c, "/ark/drives", &d)
	if err != nil {
		return err
	}
	if c.Global.JSON {
		return roEmit(c, raw)
	}
	renderArkDrives(c.Out, &d)
	return nil
}

func renderArkDrives(p *out.Printer, d *wireDrives) {
	tr := func(en, es string) string { return pick(p.Lang(), en, es) }
	sym := p.Symbols()
	if len(d.Drives) == 0 {
		p.Info(tr("No ark drive found. Plug one in, or see `aictl help ark`.", "No se encontró ninguna unidad ark. Conecte una."))
		return
	}
	var rows [][]string
	for _, dr := range d.Drives {
		id := roDash(p)
		if dr.RootID != nil && *dr.RootID != "" {
			id = *dr.RootID
		}
		space := ""
		if dr.SizeBytes > 0 {
			space = out.Size(dr.FreeBytes) + tr(" free of ", " libres de ") + out.Size(dr.SizeBytes)
		}
		label := dr.Label
		if label == "" {
			label = dr.Device
		}
		rows = append(rows, []string{label, id, dr.Mount, dr.FSType, space, driveState(p, dr.State, dr.ReadOnly)})
	}
	widths := make([]int, 5)
	for _, r := range rows {
		for i := 0; i < 5; i++ {
			if w := out.DisplayWidth(r[i]); w > widths[i] {
				widths[i] = w
			}
		}
	}
	for i, r := range rows {
		var b strings.Builder
		for j := 0; j < 5; j++ {
			b.WriteString(roPad(r[j], widths[j]) + "  ")
		}
		b.WriteString(r[5])
		p.Result(strings.TrimRight(b.String(), " "))
		for _, pr := range d.Drives[i].Problems {
			p.Result("  " + sym.Error + " " + sentence(pr.Title) + codeSuffix(pr.Code))
		}
	}
}

func driveState(p *out.Printer, state string, readOnly bool) string {
	tr := func(en, es string) string { return pick(p.Lang(), en, es) }
	sym := p.Symbols()
	switch state {
	case "ready":
		if readOnly {
			return sym.Warn + " " + tr("ready, read-only", "lista, solo lectura")
		}
		return sym.OK + " " + tr("ready", "lista")
	case "read_only":
		return sym.Warn + " " + tr("read-only", "solo lectura")
	case "unclean":
		return sym.Error + " " + tr("not shut down cleanly", "no se desmontó bien")
	case "not_mounted":
		return sym.Warn + " " + tr("not mounted", "sin montar")
	case "unusable":
		return sym.Error + " " + tr("cannot be used", "no se puede usar")
	case "ejecting":
		return sym.Progress + " " + tr("ejecting", "expulsando")
	case "uninitialized":
		return sym.Warn + " " + tr("not set up yet", "sin preparar")
	}
	return state
}
