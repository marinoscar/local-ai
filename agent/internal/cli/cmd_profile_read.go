package cli

import (
	"fmt"
	"sort"
	"strings"

	"github.com/marinoscar/local-ai/agent/internal/cli/out"
)

// `aictl profile show` and `aictl profile suggest` (aictl.md section 4.12): which hardware profile
// this computer uses and which one the agent would pick, from GET /profile. Changing the profile
// (`profile set`) belongs to AICTL-06. Both work in every mode.

func init() {
	Register(&Command{
		Name:    "profile",
		Summary: "Show the hardware profile of this computer.",
		Sub: []*Command{
			{
				Name:        "show",
				Summary:     "Show the profile in use, its overrides and the planned content size.",
				Description: "--json prints the agent body unchanged.",
				Timeout:     readOnlyTimeout,
				Run:         runProfileShow,
			},
			{
				Name:        "suggest",
				Summary:     "Say which profile fits this computer.",
				Description: "--json prints the agent body unchanged.",
				Timeout:     readOnlyTimeout,
				Run:         runProfileSuggest,
			},
		},
	})
}

func runProfileShow(c *Call) error {
	if err := roNoArgs(c); err != nil {
		return err
	}
	var pr wireProfile
	raw, err := roGet(c, "/profile", &pr)
	if err != nil {
		return err
	}
	if c.Global.JSON {
		return roEmit(c, raw)
	}
	renderProfileShow(c.Out, &pr)
	return nil
}

func runProfileSuggest(c *Call) error {
	if err := roNoArgs(c); err != nil {
		return err
	}
	var pr wireProfile
	raw, err := roGet(c, "/profile", &pr)
	if err != nil {
		return err
	}
	if c.Global.JSON {
		return roEmit(c, raw)
	}
	// The hardware behind the match comes from /status; it is only for the sentence, so a failure
	// leaves the agent's own match reasons in its place.
	var st wireStatus
	hw := false
	if pr.Suggested != "" {
		ctx, cancel := roRequestContext(c)
		_, serr := roGetCtx(c, ctx, "/status", &st)
		cancel()
		hw = serr == nil && (st.Node.Backend != "" || st.Hardware.RAMTotalMiB > 0)
	}
	renderProfileSuggest(c.Out, &pr, &st, hw)
	return nil
}

func renderProfileShow(p *out.Printer, pr *wireProfile) {
	tr := func(en, es string) string { return pick(p.Lang(), en, es) }
	sym := p.Symbols()
	var rows []out.Pair
	add := func(k, v string) {
		if v != "" {
			rows = append(rows, out.Pair{Label: k, Value: v})
		}
	}
	prof := pr.Profile
	if pr.DisplayName != "" {
		prof += " — " + pr.DisplayName
	}
	add(tr("Profile", "Perfil"), prof)
	switch {
	case pr.Suggested == "":
	case pr.Suggested == pr.Profile:
		add(tr("Suggested", "Sugerido"), sym.OK+" "+tr("the same", "el mismo"))
	default:
		add(tr("Suggested", "Sugerido"), sym.Warn+" "+pr.Suggested)
	}
	if m, ok := pr.Overrides.(map[string]any); ok && len(m) > 0 {
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		add(tr("Overrides", "Ajustes propios"), strings.Join(keys, ", "))
	} else {
		add(tr("Overrides", "Ajustes propios"), tr("none", "ninguno"))
	}
	add(tr("Models", "Modelos"), strings.Join(pr.Effective.Models, ", "))
	add(tr("Content", "Contenido"), strings.Join(pr.Effective.Content, ", "))
	if e := pr.Effective; e.BudgetBytes > 0 {
		v := out.Size(e.PlannedBytes) + tr(" planned of ", " previstos de ") + out.Size(e.BudgetBytes) + tr(" budget", " de presupuesto")
		if e.PlannedBytes > e.BudgetBytes {
			v = sym.Warn + " " + v + tr(" (over budget)", " (excede el presupuesto)")
		}
		add(tr("Disk plan", "Plan de disco"), v)
	}
	p.KV(rows)
}

func renderProfileSuggest(p *out.Printer, pr *wireProfile, st *wireStatus, hw bool) {
	tr := func(en, es string) string { return pick(p.Lang(), en, es) }
	if pr.Suggested == "" {
		p.Info(tr("No profile matches this computer. Keep the current one or ask for a new profile.",
			"Ningún perfil coincide con este equipo. Conserve el actual o pida un perfil nuevo."))
		return
	}
	var why string
	if hw {
		var parts []string
		if st.Node.Backend != "" {
			parts = append(parts, st.Node.Backend)
		}
		if st.Hardware.VRAMTotalMiB > 0 {
			parts = append(parts, out.GBFromMiB(st.Hardware.VRAMTotalMiB)+tr(" graphics memory", " de memoria gráfica"))
		}
		if st.Hardware.RAMTotalMiB > 0 {
			parts = append(parts, out.GBFromMiB(st.Hardware.RAMTotalMiB)+tr(" memory", " de memoria"))
		}
		why = strings.Join(parts, ", ")
	} else {
		why = strings.Join(pr.Reasons, "; ")
	}
	if why != "" {
		why = " (" + why + ")"
	}
	if pr.Suggested == pr.Profile || pr.Profile == "" {
		p.OK(fmt.Sprintf(tr("This computer matches %q", "Este equipo coincide con %q"), pr.Suggested) + why + ".")
		return
	}
	// The profile in use differs from the suggestion: a result, not a failure (exit 0).
	p.Result(p.Symbols().Warn + " " + fmt.Sprintf(tr("This computer looks like %q%s, but it uses %q. See `aictl profile show`.",
		"Este equipo se parece a %q%s, pero usa %q. Vea `aictl profile show`."), pr.Suggested, why, pr.Profile))
}
