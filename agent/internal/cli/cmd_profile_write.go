package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
)

// `aictl profile set <id>` (aictl.md section 4.12). Changing the profile changes what `aictl ark sync`
// will want on disk, so it asks first and shows what the new profile keeps. There is no "plan" route
// (nothing is computed here): the lists are the profile's models and content with their sizes, read
// from GET /catalog, next to the profile in use. Nothing is deleted by the change itself.

// profileInfo is GET and PUT /profile (agent.md section 3.8.30).
type profileInfo struct {
	Profile     string         `json:"profile"`
	DisplayName string         `json:"display_name"`
	Overrides   map[string]any `json:"overrides"`
	Effective   struct {
		Models       []string `json:"models"`
		Content      []string `json:"content"`
		PlannedBytes int64    `json:"planned_bytes"`
		BudgetBytes  int64    `json:"budget_bytes"`
	} `json:"effective"`
}

// catalogDoc is the part of GET /catalog the preview reads: the size of every model and content item,
// and the include lists of every profile.
type catalogDoc struct {
	Version string `json:"version"`
	Models  []struct {
		ID          string `json:"id"`
		DisplayName string `json:"display_name"`
		SizeBytes   int64  `json:"size_bytes"`
	} `json:"models"`
	Content []struct {
		ID          string `json:"id"`
		DisplayName string `json:"display_name"`
		SizeBytes   int64  `json:"size_bytes"`
	} `json:"content"`
	Profiles []struct {
		ID          string `json:"id"`
		DisplayName string `json:"display_name"`
		Include     struct {
			Models  []string `json:"models"`
			Content []string `json:"content"`
		} `json:"include"`
		BudgetBytes int64 `json:"budget_bytes"`
	} `json:"profiles"`
}

func init() {
	attachAll(at(&Command{
		Name: "set", Summary: "Choose the profile of this node.", Marks: MarkAdmin,
		Usage: "<id> [--yes]", Timeout: 30 * time.Second,
		Description: "Shows what the new profile keeps and asks first. It changes the plan; it deletes nothing and starts no sync.\n" +
			"Run `aictl ark sync` afterwards to fetch what is missing.",
		Run: profileSet,
	}, "profile"))
}

func profileSet(c *Call) error {
	if err := c.exactArgs(1, "<id>"); err != nil {
		return err
	}
	id := c.Args[0]
	if !profileIDRe.MatchString(id) {
		return c.usageErr(
			pick(c.Lang, fmt.Sprintf("%q is not a profile id.", id), fmt.Sprintf("%q no es un identificador de perfil.", id)),
			pick(c.Lang, "Profile ids are lower-case words like `cuda-12gb`; `aictl profile suggest` names the one for this computer.",
				"Los perfiles son palabras en minúsculas como `cuda-12gb`; `aictl profile suggest` indica el de este equipo."))
	}
	if !c.Global.Yes {
		// Nobody to ask (a pipe, --json): say so before any request.
		if err := c.requireTerminalForConfirm(false); err != nil {
			return err
		}
		var cur profileInfo
		if _, _, err := c.call(http.MethodGet, "/profile", nil, &cur); err != nil {
			return err
		}
		if cur.Profile == id {
			c.Out.Info(pick(c.Lang, "The profile is already "+id+".", "El perfil ya es "+id+"."))
			return nil
		}
		var cat catalogDoc
		if _, _, err := c.call(http.MethodGet, "/catalog", nil, &cat); err != nil {
			return err
		}
		if err := c.profilePreview(cur, cat, id); err != nil {
			return err
		}
		ok, err := c.Confirm(pick(c.Lang, "Switch to the profile "+id+"?", "¿Cambiar al perfil "+id+"?"))
		if err != nil || !ok {
			return err
		}
	}
	var raw json.RawMessage
	err := c.Admin(func() (e error) {
		raw, _, e = c.call(http.MethodPut, "/profile", map[string]string{"profile": id}, nil)
		return e
	})
	if err != nil {
		return err
	}
	if c.Global.JSON {
		return c.emitRaw(raw)
	}
	c.Out.OK(pick(c.Lang,
		"Profile set to "+id+". Run `aictl ark sync` to fetch the new content.",
		"Perfil cambiado a "+id+". Ejecute `aictl ark sync` para traer el contenido nuevo."))
	return nil
}

// profilePreview prints, to stderr, the profile in use and what the target profile keeps. An unknown
// target is refused here (LAI-CAT-003, exit 6, as the agent would). A catalog that does not list
// its profiles (an agent without that field) gets the question without the lists.
func (c *Call) profilePreview(cur profileInfo, cat catalogDoc, id string) error {
	w := c.Stderr
	label := func(id, name string) string {
		if name == "" {
			return id
		}
		return id + " (" + name + ")"
	}
	nowLabel, toLabel := pick(c.Lang, "Profile now:", "Perfil actual:"), pick(c.Lang, "Switch to:", "Cambiar a:")
	width := len(nowLabel)
	if len(toLabel) > width {
		width = len(toLabel)
	}
	width += 2
	fmt.Fprintf(w, "%-*s%s\n", width, nowLabel, label(cur.Profile, cur.DisplayName))
	if len(cat.Profiles) == 0 {
		fmt.Fprintf(w, "%-*s%s\n", width, toLabel, id)
		return nil
	}
	found := -1
	for i, p := range cat.Profiles {
		if p.ID == id {
			found = i
		}
	}
	if found < 0 {
		return c.codeError(doctor.CAT003, ExitRefused, "")
	}
	p := cat.Profiles[found]
	fmt.Fprintf(w, "%-*s%s\n", width, toLabel, label(p.ID, p.DisplayName))

	models, content := applyOverrides(p.Include.Models, cur.Overrides, "models"), applyOverrides(p.Include.Content, cur.Overrides, "content")
	msize, csize := map[string]int64{}, map[string]int64{}
	for _, m := range cat.Models {
		msize[m.ID] = m.SizeBytes
	}
	for _, x := range cat.Content {
		csize[x.ID] = x.SizeBytes
	}
	total := int64(0)
	list := func(title string, ids []string, sizes map[string]int64) {
		var sum int64
		for _, id := range ids {
			sum += sizes[id]
		}
		total += sum
		fmt.Fprintf(w, "%s (%d, %s):\n", title, len(ids), sizeText(sum))
		width := 0
		for _, id := range ids {
			if len(id) > width {
				width = len(id)
			}
		}
		for _, id := range ids {
			size := "?"
			if s, ok := sizes[id]; ok {
				size = sizeText(s)
			}
			fmt.Fprintf(w, "  %-*s  %s\n", width, id, size)
		}
	}
	list(pick(c.Lang, "Models", "Modelos"), models, msize)
	list(pick(c.Lang, "Content", "Contenido"), content, csize)
	budget := p.BudgetBytes
	if v, ok := cur.Overrides["budget_bytes"].(float64); ok && v > 0 {
		budget = int64(v)
	}
	line := pick(c.Lang, "Total "+sizeText(total), "Total "+sizeText(total))
	if budget > 0 {
		line += pick(c.Lang, " of a "+sizeText(budget)+" budget.", " de un presupuesto de "+sizeText(budget)+".")
	} else {
		line += "."
	}
	fmt.Fprintln(w, line+pick(c.Lang, " Nothing is deleted now.", " Ahora no se borra nada."))
	return nil
}

// applyOverrides is the profile's list plus the overrides' add_<kind> minus remove_<kind>, sorted
// (ark-sync.md section 4.4.1).
func applyOverrides(base []string, overrides map[string]any, kind string) []string {
	strs := func(v any) []string {
		var out []string
		if list, ok := v.([]any); ok {
			for _, x := range list {
				if s, ok := x.(string); ok {
					out = append(out, s)
				}
			}
		}
		return out
	}
	remove := map[string]bool{}
	for _, id := range strs(overrides["remove_"+kind]) {
		remove[id] = true
	}
	seen := map[string]bool{}
	var out []string
	for _, id := range append(append([]string{}, base...), strs(overrides["add_"+kind])...) {
		if !seen[id] && !remove[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}
