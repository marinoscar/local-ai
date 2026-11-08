package cli

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// `aictl ark budget [<size>|default]` (aictl.md section 4.10, issue #435): shows or changes how much of
// the internal disk the offline content may use (ark-sync.md section 4.4.3). The fix of LAI-SYN-041
// names this command instead of asking a person to edit `budget_bytes` in node.yaml.
//
// The agent owns node.yaml: the change goes through PUT /profile (agent.md section 3.8.30, admin, so
// the PIN flow applies), which writes `profile_overrides.budget_bytes` with comments kept and a .bak.
// That route replaces the whole overrides block, so the other four keys are sent back as GET /profile
// returned them. The budget of an ark drive is its size and is not changed here.

const (
	minBudgetBytes = 1_000_000_000         // 1 GB: anything smaller is a typo
	maxBudgetBytes = 1_000_000_000_000_000 // 1 PB
)

// budgetRe is a size with a decimal unit: 500G, 500 GB, 1.5T, 1.5tb.
var budgetRe = regexp.MustCompile(`^(?i)\s*([0-9]+(?:\.[0-9]+)?)\s*(g|gb|t|tb)\s*$`)

// overrideListKeys are the four list keys of profile_overrides; the fifth is budget_bytes.
var overrideListKeys = []string{"add_models", "add_content", "remove_models", "remove_content"}

// budgetView is the part of GET and PUT /profile the command reads.
type budgetView struct {
	Profile   string         `json:"profile"`
	Overrides map[string]any `json:"overrides"`
	Effective struct {
		PlannedBytes int64 `json:"planned_bytes"`
		BudgetBytes  int64 `json:"budget_bytes"`
		Skipped      []struct {
			ID string `json:"id"`
		} `json:"skipped"`
	} `json:"effective"`
}

// overridden reports whether node.yaml sets its own budget.
func (v budgetView) overridden() bool {
	b, ok := v.Overrides["budget_bytes"]
	return ok && b != nil
}

func init() {
	attachAll(at(&Command{
		Name: "budget", Summary: "Show or change how much disk the offline content may use.",
		Usage: "[<size> | default]", Timeout: 30 * time.Second, Marks: MarkAdmin,
		Description: `Without an argument it shows the budget of the internal disk and what the plan uses.
With a size (for example 500G or 1.5T; decimal units, at least 1G) it sets this computer's own
budget; "default" goes back to the profile's. A change needs the admin PIN (or sudo). It deletes
nothing and starts no sync: run "aictl ark sync" to fetch what now fits.
Specification: docs/specs/aictl.md section 4.10`,
		Run: arkBudget,
	}, "ark"))
}

// parseBudget reads a size such as 500G or 1.5T (decimal units); false when it is not one or is
// outside 1 GB to 1 PB.
func parseBudget(s string) (int64, bool) {
	m := budgetRe.FindStringSubmatch(s)
	if m == nil {
		return 0, false
	}
	n, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0, false
	}
	unit := 1e9
	if strings.HasPrefix(strings.ToLower(m[2]), "t") {
		unit = 1e12
	}
	b := math.Round(n * unit)
	if b < minBudgetBytes || b > maxBudgetBytes {
		return 0, false
	}
	return int64(b), true
}

func arkBudget(c *Call) error {
	if len(c.Args) > 1 {
		return c.usageErr(pick(c.Lang, "Give one size at most.", "Indique como mucho un tamaño."),
			pick(c.Lang, "For example `aictl ark budget 500G`.", "Por ejemplo `aictl ark budget 500G`."))
	}
	var want any // int64, or nil for "default"
	if len(c.Args) == 1 {
		arg := c.Args[0]
		if strings.EqualFold(arg, "default") {
			want = nil
		} else if b, ok := parseBudget(arg); ok {
			want = b
		} else {
			return c.usageErr(
				pick(c.Lang, fmt.Sprintf("%q is not a size.", arg), fmt.Sprintf("%q no es un tamaño.", arg)),
				pick(c.Lang, "Use a number and G or T, from 1G up, for example `aictl ark budget 500G` or `aictl ark budget 1.5T`.",
					"Use un número y G o T, desde 1G, por ejemplo `aictl ark budget 500G` o `aictl ark budget 1.5T`."))
		}
	}

	var cur budgetView
	_, _, err := c.call(http.MethodGet, "/profile", nil, &cur)
	if err != nil {
		return err
	}
	if len(c.Args) == 0 {
		if c.Global.JSON {
			return c.Out.JSON().Document(map[string]any{
				"budget_bytes": cur.Effective.BudgetBytes, "planned_bytes": cur.Effective.PlannedBytes,
				"overridden": cur.overridden(), "profile": cur.Profile, "skipped": len(cur.Effective.Skipped),
			})
		}
		c.showBudget(cur)
		return nil
	}
	overrides := map[string]any{"budget_bytes": want}
	for _, k := range overrideListKeys {
		if v, ok := cur.Overrides[k]; ok && v != nil {
			overrides[k] = v
		} else {
			overrides[k] = []string{}
		}
	}
	if b, ok := want.(int64); ok && cur.overridden() && cur.Effective.BudgetBytes == b {
		c.Out.Info(pick(c.Lang, "The budget is already "+sizeText(b)+".", "El presupuesto ya es "+sizeText(b)+"."))
		return nil
	}
	if want == nil && !cur.overridden() {
		c.Out.Info(pick(c.Lang, "The budget already is the profile's ("+sizeText(cur.Effective.BudgetBytes)+").",
			"El presupuesto ya es el del perfil ("+sizeText(cur.Effective.BudgetBytes)+")."))
		return nil
	}
	var res budgetView
	var out json.RawMessage
	err = c.Admin(func() (e error) {
		out, _, e = c.call(http.MethodPut, "/profile", map[string]any{"profile_overrides": overrides}, &res)
		return e
	})
	if err != nil {
		return err
	}
	if c.Global.JSON {
		return c.emitRaw(out)
	}
	if want == nil {
		c.Out.OK(pick(c.Lang, "The budget is back to the profile's: "+sizeText(res.Effective.BudgetBytes)+".",
			"El presupuesto volvió a ser el del perfil: "+sizeText(res.Effective.BudgetBytes)+"."))
	} else {
		c.Out.OK(pick(c.Lang, "The offline content may now use up to "+sizeText(res.Effective.BudgetBytes)+" of this computer's disk.",
			"El contenido sin conexión ahora puede usar hasta "+sizeText(res.Effective.BudgetBytes)+" del disco de este equipo."))
	}
	c.budgetTail(res)
	c.Out.Info(pick(c.Lang, "Nothing was deleted. Run `aictl ark sync` to fetch what now fits.",
		"No se borró nada. Ejecute `aictl ark sync` para traer lo que ahora cabe."))
	return nil
}

// showBudget prints the budget, where it comes from and the plan.
func (c *Call) showBudget(v budgetView) {
	if v.overridden() {
		c.Out.Info(pick(c.Lang,
			"The offline content may use up to "+sizeText(v.Effective.BudgetBytes)+" of this computer's disk (set on this computer; `aictl ark budget default` goes back to the profile's).",
			"El contenido sin conexión puede usar hasta "+sizeText(v.Effective.BudgetBytes)+" del disco de este equipo (fijado en este equipo; `aictl ark budget default` vuelve al del perfil)."))
	} else {
		c.Out.Info(pick(c.Lang,
			"The offline content may use up to "+sizeText(v.Effective.BudgetBytes)+" of this computer's disk (from the profile "+v.Profile+").",
			"El contenido sin conexión puede usar hasta "+sizeText(v.Effective.BudgetBytes)+" del disco de este equipo (del perfil "+v.Profile+")."))
	}
	c.budgetTail(v)
}

// budgetTail says what the plan uses and what does not fit.
func (c *Call) budgetTail(v budgetView) {
	c.Out.Info(pick(c.Lang, "The plan uses "+sizeText(v.Effective.PlannedBytes)+".", "El plan usa "+sizeText(v.Effective.PlannedBytes)+"."))
	if n := len(v.Effective.Skipped); n > 0 {
		c.Out.Warn(pick(c.Lang,
			fmt.Sprintf("%d item(s) do not fit. To make room, raise the budget, for example `aictl ark budget %s`.", n, suggestBudget(v)),
			fmt.Sprintf("%d elemento(s) no caben. Para hacer sitio, suba el presupuesto, por ejemplo `aictl ark budget %s`.", n, suggestBudget(v))))
	}
}

// suggestBudget is a round size above the current budget, as an example to type.
func suggestBudget(v budgetView) string {
	gb := v.Effective.BudgetBytes/1_000_000_000 + 100
	return strconv.FormatInt((gb+99)/100*100, 10) + "G"
}
