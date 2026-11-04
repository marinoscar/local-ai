package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/cli/out"
)

// `--dry-run` of `ark sync` and `ark import`: the agent answers 200 with the plan of ark-sync.md
// section 4.4.4 and changes nothing. Human output is that section's table; --json is the body.

type arkPlanItem struct {
	Pos         int    `json:"pos"`
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	Tier        any    `json:"tier"`
	Action      string `json:"action"`
	FromVersion string `json:"from_version"`
	ToVersion   string `json:"to_version"`
	FinalBytes  int64  `json:"final_bytes"`
	NeedBytes   int64  `json:"need_bytes"`
	Source      string `json:"source"`
	Reason      string `json:"reason"`
}

type arkPlan struct {
	Root           string        `json:"root"`
	Profile        string        `json:"profile"`
	CatalogVersion string        `json:"catalog_version"`
	BudgetBytes    int64         `json:"budget_bytes"`
	PlannedBytes   int64         `json:"planned_bytes"`
	FSFreeBytes    int64         `json:"fs_free_bytes"`
	MinFreeBytes   int64         `json:"min_free_bytes"`
	PeakBytes      int64         `json:"peak_bytes"`
	Items          []arkPlanItem `json:"items"`
}

// arkPlans reads the answer: one plan object (one root), or {"plans": [...]} / an array when several
// roots were planned (the shape for several roots is not fixed by the spec yet; both are accepted).
func arkPlans(raw json.RawMessage) ([]arkPlan, bool) {
	var many struct {
		Plans []arkPlan `json:"plans"`
	}
	if json.Unmarshal(raw, &many) == nil && len(many.Plans) > 0 {
		return many.Plans, true
	}
	var list []arkPlan
	if json.Unmarshal(raw, &list) == nil && len(list) > 0 {
		return list, true
	}
	var one arkPlan
	if json.Unmarshal(raw, &one) == nil {
		return []arkPlan{one}, true
	}
	return nil, false
}

func (c *Call) arkPlan(path string, body map[string]any) error {
	ctx, cancel := c.withDefaultTimeout(5 * time.Minute) // a dry run resolves items over the network
	defer cancel()
	cl, err := c.Client()
	if err != nil {
		return err
	}
	raw, _, err := c.callWith(cl, ctx, http.MethodPost, path, body, nil)
	if err != nil {
		return err
	}
	if c.Global.JSON {
		return c.emitRaw(raw)
	}
	plans, ok := arkPlans(raw)
	if !ok {
		return c.Err(CodeBadResponse, "").WithDetails("reason", "not a plan")
	}
	for i, p := range plans {
		if i > 0 {
			c.Out.Blank()
		}
		c.printPlan(p)
	}
	c.Out.Result(pick(c.Lang, "Nothing was changed (dry run).", "No se cambió nada (simulacro)."))
	return nil
}

// planReason is a plan reason in a word or two: the table is 80 columns wide, the note is its last
// column, and a longer note would be cut.
func (c *Call) planReason(reason string) string {
	l := c.Lang
	switch reason {
	case "refresh_days", "refresh_due":
		return pick(l, "refresh due", "toca renovar")
	case "budget":
		return pick(l, "budget", "presupuesto")
	case "disk":
		return pick(l, "no disk space", "sin espacio")
	case "unresolved":
		return pick(l, "unresolved", "sin resolver")
	case "needs_network":
		return pick(l, "needs network", "necesita red")
	case "time_limit":
		return pick(l, "time limit", "sin tiempo")
	case "read_only":
		return pick(l, "read-only", "solo lectura")
	case "backend":
		return pick(l, "backend", "backend")
	case "catalog_pin":
		return pick(l, "catalog pin", "fijado")
	}
	return reason
}

func (c *Call) printPlan(p arkPlan) {
	l := c.Lang
	c.Out.Result(pick(l,
		fmt.Sprintf("Plan for root %q · profile %s · catalog %s", p.Root, orText(p.Profile, "-"), orText(p.CatalogVersion, "-")),
		fmt.Sprintf("Plan para la raíz %q · perfil %s · catálogo %s", p.Root, orText(p.Profile, "-"), orText(p.CatalogVersion, "-"))))
	c.Out.Result(pick(l,
		fmt.Sprintf("Budget %s · planned %s · free now %s · keep free ≥ %s",
			sizeText(p.BudgetBytes), sizeText(p.PlannedBytes), sizeText(p.FSFreeBytes), sizeText(p.MinFreeBytes)),
		fmt.Sprintf("Presupuesto %s · planeado %s · libre ahora %s · mantener libre ≥ %s",
			sizeText(p.BudgetBytes), sizeText(p.PlannedBytes), sizeText(p.FSFreeBytes), sizeText(p.MinFreeBytes))))
	c.Out.Blank()

	cols := []out.Col{
		{Title: "#", Right: true}, {Title: pick(l, "Item", "Elemento")}, {Title: pick(l, "Tier", "Nivel")},
		{Title: pick(l, "Action", "Acción")}, {Title: pick(l, "Version", "Versión")},
		{Title: pick(l, "Size", "Tamaño"), Right: true}, {Title: pick(l, "Note", "Nota")},
	}
	var rows [][]string
	orphans := false
	var netN, localN int
	var netBytes, localBytes int64
	for _, it := range p.Items {
		version := orText(it.ToVersion, it.FromVersion)
		if len(version) > 10 {
			version = version[:10]
		}
		size := "-"
		if it.FinalBytes > 0 {
			size = sizeText(it.FinalBytes)
		}
		note := c.planReason(it.Reason)
		switch {
		case it.Action == "orphan":
			note = pick(l, "orphan", "huérfano")
			orphans = true
		case it.Source == "local_copy" && (it.Action == "download" || it.Action == "update"):
			note = pick(l, "local copy", "copia local")
		case it.Source == "local_build" && (it.Action == "download" || it.Action == "update"):
			note = pick(l, "built from local files", "se construye con archivos locales")
		}
		rows = append(rows, []string{fmt.Sprint(it.Pos), it.ID, fmt.Sprint(orAny(it.Tier, "-")), it.Action, orText(version, "-"), size, note})
		if it.Action == "download" || it.Action == "update" {
			n := it.NeedBytes
			if n == 0 {
				n = it.FinalBytes
			}
			if it.Source == "local_copy" || it.Source == "local_build" {
				localN++
				localBytes += n
			} else {
				netN++
				netBytes += n
			}
		}
	}
	c.Out.Table(cols, rows)
	c.Out.Blank()
	if orphans {
		c.Out.Result(pick(l,
			"An orphan is in the ark but not in the profile; `aictl ark prune --orphans` removes it.",
			"Un huérfano está en el arca pero no en el perfil; `aictl ark prune --orphans` lo quita."))
	}
	c.Out.Result(pick(l,
		fmt.Sprintf("Downloads: %d items, %s from the internet, %s local copy.", netN+localN, sizeText(netBytes), sizeText(localBytes)),
		fmt.Sprintf("Descargas: %d elementos, %s de internet, %s de copia local.", netN+localN, sizeText(netBytes), sizeText(localBytes))))
	if p.PeakBytes > 0 {
		low := p.FSFreeBytes - p.PeakBytes
		if low < 0 {
			low = 0
		}
		c.Out.Result(pick(l,
			fmt.Sprintf("Peak disk use during sync: %s (free will not drop below %s).", sizeText(p.PeakBytes), sizeText(low)),
			fmt.Sprintf("Uso máximo de disco durante la sincronización: %s (lo libre no bajará de %s).", sizeText(p.PeakBytes), sizeText(low))))
	}
}

func orAny(v any, fallback string) any {
	if v == nil || fmt.Sprint(v) == "" {
		return fallback
	}
	if f, ok := v.(float64); ok && f == float64(int(f)) {
		return int(f)
	}
	return v
}
