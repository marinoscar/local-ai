package router

import (
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/catalog"
	"github.com/marinoscar/local-ai/agent/internal/config"
)

// maxManifestBytes bounds what the presence check reads from a MANIFEST.json; a model's manifest
// is a few KiB.
const maxManifestBytes = 4 << 20

// Roots are the content roots a node has mounted (llama-router.md section 4.1): the internal root
// and any ark drives. A root is a directory that holds models/<id>/.
type Roots struct {
	// Internal is the internal root (/srv/localai); empty when the node has none.
	Internal string
	// Drives are the mount paths of the mounted ark drives, in any order.
	Drives []string
}

// Ordered returns the roots in preference order: internal first, then the drives sorted by mount
// path. The returned slice is fresh.
func (r Roots) Ordered() []string {
	var out []string
	if r.Internal != "" {
		out = append(out, r.Internal)
	}
	drives := append([]string(nil), r.Drives...)
	sort.Strings(drives)
	for _, d := range drives {
		if d != "" && d != r.Internal {
			out = append(out, d)
		}
	}
	return out
}

// Presence answers "is a complete, verified copy of this model on this root?".
type Presence interface {
	Present(root string, m *catalog.Model) bool
}

// ManifestPresence is the Presence of ark-sync.md section 4.11: <root>/models/<id>/MANIFEST.json
// lists every files[] entry of the catalog model with the catalog's sha256, has state verified and
// a verified_at timestamp. It reads only the manifest; whether the files themselves are still
// there and readable is checked by Generate (llama-router.md section 4.2 step 3).
type ManifestPresence struct {
	FS config.FS
}

// manifest is the part of MANIFEST.json the presence rule reads. Unknown fields are ignored.
type manifest struct {
	ItemID     string `json:"item_id"`
	State      string `json:"state"`
	VerifiedAt string `json:"verified_at"`
	Files      []struct {
		Kind   string `json:"kind"`
		Path   string `json:"path"`
		SHA256 string `json:"sha256"`
	} `json:"files"`
}

// Present implements Presence.
func (p ManifestPresence) Present(root string, m *catalog.Model) bool {
	if p.FS == nil || m == nil || root == "" {
		return false
	}
	mf := modelDir(root, m.ID) + "/MANIFEST.json"
	info, err := p.FS.Stat(mf)
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxManifestBytes {
		return false
	}
	raw, err := p.FS.ReadFile(mf)
	if err != nil {
		return false
	}
	var man manifest
	if json.Unmarshal(raw, &man) != nil {
		return false
	}
	if man.ItemID != m.ID || man.State != "verified" {
		return false
	}
	if _, err := time.Parse(time.RFC3339, man.VerifiedAt); err != nil {
		return false
	}
	if len(m.Files) == 0 {
		return false
	}
	for _, want := range m.Files {
		if want.SHA256 == "" {
			return false // an unpinned catalog entry cannot be verified
		}
		found := false
		for _, got := range man.Files {
			if got.Path == want.Name && strings.EqualFold(got.SHA256, want.SHA256) &&
				(got.Kind == "" || got.Kind == want.Kind) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// modelDir is <root>/models/<id>.
func modelDir(root, id string) string {
	return strings.TrimRight(root, "/") + "/models/" + id
}

// DesiredModels is the profile's include.models plus node.yaml profile_overrides.add_models, minus
// remove_models (llama-router.md section 4.1 rule 1). The remove list wins over both. profile may
// be nil (a profile that is not in the catalog).
func DesiredModels(profile *catalog.Profile, ov config.ProfileOverrides) map[string]bool {
	want := map[string]bool{}
	if profile != nil {
		for _, id := range profile.Include.Models {
			want[id] = true
		}
	}
	for _, id := range ov.AddModels {
		want[id] = true
	}
	for _, id := range ov.RemoveModels {
		delete(want, id)
	}
	return want
}

// EffectiveInput is what EffectiveSet decides from.
type EffectiveInput struct {
	// Models is the catalog's model list; only engine: llama entries are considered.
	Models  []catalog.Model
	Profile *catalog.Profile
	// Overrides is node.yaml profile_overrides.
	Overrides config.ProfileOverrides
	Roots     Roots
	Presence  Presence
	// Fit is the machine side of the static fit rules (section 4.3.2).
	Fit Env
}

// Entry is a model that gets a preset.
type Entry struct {
	Model *catalog.Model
	// Root is the chosen content root: internal if the model is present there, else the first
	// mounted drive (sorted by path) that has it.
	Root string
	// Desired is true when the model is in the desired set (profile include plus add, minus remove).
	// A model that is not desired is present on a root only: it stays loadable (ADR-0005) but sync
	// does not fetch it and boot does not load it.
	Desired bool
}

// Excluded is an in-scope model that gets no preset, and why: the first failing rule of the static
// fit (LAI-CAT-005, or LAI-LLM-008 when the files are missing or unverified).
type Excluded struct {
	ID     string
	Reason Reason
}

// Set is the effective model set of llama-router.md section 4.1.
type Set struct {
	// Entries are the models that get a preset, sorted by id.
	Entries []Entry
	// Excluded are the in-scope models that do not, sorted by id.
	Excluded []Excluded
}

// IDs returns the ids of the entries, sorted.
func (s Set) IDs() []string {
	ids := make([]string, len(s.Entries))
	for i, e := range s.Entries {
		ids[i] = e.Model.ID
	}
	return ids
}

// EffectiveSet computes llama-router.md section 4.1. A model is in scope when it is desired or
// present on a mounted root (a removed model that is still on disk stays in scope until it is
// deleted). It gets a preset iff it is in scope, present and statically fits.
func EffectiveSet(in EffectiveInput) Set {
	desired := DesiredModels(in.Profile, in.Overrides)
	roots := in.Roots.Ordered()
	var set Set
	for i := range in.Models {
		m := &in.Models[i]
		if m.Engine != "llama" {
			continue
		}
		chosen := ""
		if in.Presence != nil {
			for _, r := range roots {
				if in.Presence.Present(r, m) {
					chosen = r
					break
				}
			}
		}
		present := chosen != ""
		if !desired[m.ID] && !present {
			continue // not in scope
		}
		ok, why := in.Fit.StaticFit(Candidate{ID: m.ID, Requirements: m.Requirements, Present: present})
		if !ok {
			set.Excluded = append(set.Excluded, Excluded{ID: m.ID, Reason: *why})
			continue
		}
		set.Entries = append(set.Entries, Entry{Model: m, Root: chosen, Desired: desired[m.ID]})
	}
	sort.Slice(set.Entries, func(a, b int) bool { return set.Entries[a].Model.ID < set.Entries[b].Model.ID })
	sort.Slice(set.Excluded, func(a, b int) bool { return set.Excluded[a].ID < set.Excluded[b].ID })
	return set
}

// EffectiveDefault is llama-router.md section 4.6, first bullet: state.models.default if it has a
// preset; else the profile's default_model if it has one; else, in survival mode, the profile's
// survival.default_model; else "" (no default, LAI-LLM-014). ids are the models that have a preset.
func EffectiveDefault(ids []string, stateDefault string, profile *catalog.Profile, survival bool) string {
	has := make(map[string]bool, len(ids))
	for _, id := range ids {
		has[id] = true
	}
	if stateDefault != "" && has[stateDefault] {
		return stateDefault
	}
	if profile != nil {
		if profile.DefaultModel != "" && has[profile.DefaultModel] {
			return profile.DefaultModel
		}
		if survival && profile.Survival.DefaultModel != "" && has[profile.Survival.DefaultModel] {
			return profile.Survival.DefaultModel
		}
	}
	return ""
}
