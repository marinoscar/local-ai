package ready

import (
	"sort"
	"strings"

	"github.com/marinoscar/local-ai/agent/internal/ark"
	"github.com/marinoscar/local-ai/agent/internal/catalog"
	"github.com/marinoscar/local-ai/agent/internal/sync/manifest"
)

// survival_capable (ark-sync.md section 4.12.2): true when, with the network cable pulled, the node
// could still chat, listen, speak, read and find its way. The requirements are derived from the
// catalog and the profile; whether they are met is judged against the set of items that are verified
// (status verified: a stale copy does not count, because it is not the version the node was told to
// have, and the report never claims what it cannot prove).
//
// The same requirements are checked against one root (the root's own survival_capable) and against
// the union of every mounted root (the node-level answer of /status and /portal/status), which is why
// the check works on a set of item ids and not on a report.

// PortalLanguages are the languages of the Survival Portal (survival-portal.md section 4.9). The list
// is fixed: a piper voice is required for each.
var PortalLanguages = []string{"en", "es"}

// Needs are the requirements of survival_capable as sets of catalog ids.
type Needs struct {
	// Models are the profile's survival models that can load on this node: every one is required.
	Models []string
	// Whisper are the speech-to-text models of the catalog: one is required.
	Whisper []string
	// Piper maps a Portal language to the voices of the catalog for it: one per language is required.
	Piper map[string][]string
	// Kiwix are the tier-1 library items: one is required.
	Kiwix []string
	// Maps are the tier-1 maps (pmtiles extracts): one is required.
	Maps []string
	// MapAssets is the id of the map styles and fonts, required with the map. Empty when the catalog
	// has no such item, which makes the requirement impossible to meet.
	MapAssets string
}

// NeedsFor derives the requirements from the catalog and the profile. loadable says whether a
// catalog model can load on this node (the router's static fit rules); a nil loadable counts every
// model as loadable, which is the stricter reading: a survival model that cannot load on this node
// is then still required. A nil catalog or profile gives requirements that cannot be met.
func NeedsFor(cat *catalog.Catalog, prof *catalog.Profile, loadable func(modelID string) bool) Needs {
	n := Needs{Piper: map[string][]string{}}
	for _, l := range PortalLanguages {
		n.Piper[l] = nil
	}
	if cat == nil || prof == nil {
		return n
	}
	for _, id := range prof.Survival.Models {
		if loadable != nil && !loadable(id) {
			continue
		}
		n.Models = append(n.Models, id)
	}
	for i := range cat.Models {
		m := &cat.Models[i]
		switch m.Engine {
		case ark.EngineWhisper:
			n.Whisper = append(n.Whisper, m.ID)
		case ark.EnginePiper:
			for _, l := range PortalLanguages {
				if hasLanguage(m.Languages, l) {
					n.Piper[l] = append(n.Piper[l], m.ID)
				}
			}
		}
	}
	for i := range cat.Content {
		c := &cat.Content[i]
		if tierOfContent(c) != 1 {
			continue
		}
		switch {
		case c.Kind == manifest.KindKiwix:
			n.Kiwix = append(n.Kiwix, c.ID)
		case c.Kind == manifest.KindMaps && c.ID == ark.MapAssetsID:
			n.MapAssets = c.ID
		case c.Kind == manifest.KindMaps && c.Source.Type == "pmtiles_extract":
			n.Maps = append(n.Maps, c.ID)
		}
	}
	return n
}

// hasLanguage reports whether a model's language list names lang, ignoring case and a region suffix
// ("en-US" and "en_GB" are "en"). "multi" names no single language: a voice is one speaker of one
// language.
func hasLanguage(langs []string, lang string) bool {
	for _, l := range langs {
		l = strings.ToLower(strings.TrimSpace(l))
		if i := strings.IndexAny(l, "-_"); i >= 0 {
			l = l[:i]
		}
		if l == lang {
			return true
		}
	}
	return false
}

// Met reports whether the verified set satisfies every requirement.
func (n Needs) Met(verified map[string]bool) bool { return len(n.Missing(verified)) == 0 }

// Missing lists what the verified set lacks, in a fixed order, as short keys: "model:<id>",
// "whisper", "piper:<lang>", "kiwix", "map", "map-assets". It is what an explanation would show.
func (n Needs) Missing(verified map[string]bool) []string {
	var out []string
	for _, id := range n.Models {
		if !verified[id] {
			out = append(out, "model:"+id)
		}
	}
	if !anyVerified(n.Whisper, verified) {
		out = append(out, "whisper")
	}
	langs := make([]string, 0, len(n.Piper))
	for l := range n.Piper {
		langs = append(langs, l)
	}
	sort.Strings(langs)
	for _, l := range langs {
		if !anyVerified(n.Piper[l], verified) {
			out = append(out, "piper:"+l)
		}
	}
	if !anyVerified(n.Kiwix, verified) {
		out = append(out, "kiwix")
	}
	if !anyVerified(n.Maps, verified) {
		out = append(out, "map")
	}
	if n.MapAssets == "" || !verified[n.MapAssets] {
		out = append(out, "map-assets")
	}
	return out
}

func anyVerified(ids []string, verified map[string]bool) bool {
	for _, id := range ids {
		if verified[id] {
			return true
		}
	}
	return false
}

// Verified is the set of item ids a report lists with status verified.
func Verified(r Report) map[string]bool {
	out := map[string]bool{}
	for _, it := range r.Items {
		if it.Status == StatusVerified {
			out[it.ID] = true
		}
	}
	return out
}

// NodeSurvivalCapable is the node-level survival_capable: the requirements are met by the verified
// items of every report together, so a node with its models on the internal disk and its maps on a
// plugged-in drive is capable although neither root alone is. It is what /status and /portal/status
// show. With no report it is false.
func NodeSurvivalCapable(n Needs, reports []Report) bool {
	if len(reports) == 0 {
		return false
	}
	union := map[string]bool{}
	for _, r := range reports {
		for id := range Verified(r) {
			union[id] = true
		}
	}
	return n.Met(union)
}

// tierOfContent is the survival tier of a content item as the planner reads it (ark-sync.md section
// 4.4.2): the declared tier, else 1 for the kit and the manuals, else 2.
func tierOfContent(c *catalog.ContentItem) int {
	if c.SurvivalTier != 0 {
		return c.SurvivalTier
	}
	if c.Kind == manifest.KindKit || c.Kind == manifest.KindDocs {
		return 1
	}
	return 2
}

// tierOfModel is the survival tier of a model: the declared tier, else 2.
func tierOfModel(m *catalog.Model) int {
	if m.SurvivalTier != 0 {
		return m.SurvivalTier
	}
	return 2
}
