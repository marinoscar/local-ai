package catalog

import (
	"errors"
	"fmt"
	"sort"
)

// The effective plan (catalog.md section 4, "Planning rule", and section 5): what a node keeps, the
// profile's include list changed by the node's overrides and limited by the storage budget. Pure:
// it reads the catalog and returns a value.

// Overrides is node.yaml profile_overrides (catalog.md section 4, "Node overrides"). The fields
// mirror config.ProfileOverrides, so a caller converts with Overrides(cfg.ProfileOverrides); this
// package does not import config.
type Overrides struct {
	AddModels     []string `yaml:"add_models" json:"add_models"`
	RemoveModels  []string `yaml:"remove_models" json:"remove_models"`
	AddContent    []string `yaml:"add_content" json:"add_content"`
	RemoveContent []string `yaml:"remove_content" json:"remove_content"`
	// BudgetBytes replaces the profile's storage.budget_bytes when set (it must be positive).
	BudgetBytes *int64 `yaml:"budget_bytes" json:"budget_bytes"`
}

// Item kinds of a Skipped entry.
const (
	KindModel   = "model"
	KindContent = "content"
)

// SkipBudget is the reason of an item dropped to fit the budget (READY.json: "skipped: budget").
const SkipBudget = "budget"

// Skipped is an item the plan dropped. It is reported, never an error.
type Skipped struct {
	Kind      string `json:"kind"`
	ID        string `json:"id"`
	Reason    string `json:"reason"`
	SizeBytes int64  `json:"size_bytes"`
}

// Plan is the effective plan of a profile with overrides: the body of "effective" in GET /profile.
// Models and Content are what is kept, in list order (the profile's include order, then the added
// ids); they are never filtered by whether a model fits this node's hardware (a model that cannot
// load may still be stored; only "loadable" reflects fit). Skipped holds what the budget dropped,
// in the order it was dropped.
type Plan struct {
	Profile      string    `json:"-"`
	Models       []string  `json:"models"`
	Content      []string  `json:"content"`
	PlannedBytes int64     `json:"planned_bytes"`
	BudgetBytes  int64     `json:"budget_bytes"`
	Skipped      []Skipped `json:"skipped"`
}

// UnknownRef is one id that is not in the catalog.
type UnknownRef struct {
	// Field is where the id was written: include.models, include.content, add_models,
	// remove_models, add_content or remove_content.
	Field string
	ID    string
}

// ProfileError is a plan request the catalog cannot satisfy: an unknown profile (LAI-CAT-003) or
// ids that are not in the catalog (LAI-CAT-004). PUT /profile answers it with 422.
type ProfileError struct {
	code string
	// Profile is the requested profile id.
	Profile string
	// Refs lists every unknown id, for LAI-CAT-004; empty for LAI-CAT-003.
	Refs []UnknownRef
}

// Code is the doctor code of the error.
func (e *ProfileError) Code() string { return e.code }

func (e *ProfileError) Error() string {
	if e.code == CodeUnknownProfile {
		return fmt.Sprintf("%s: profile %q is not in the catalog", e.code, truncate(e.Profile, 70))
	}
	r := e.Refs[0]
	msg := fmt.Sprintf("%s: profile %q: %s references unknown id %q", e.code, truncate(e.Profile, 70), r.Field, truncate(r.ID, 70))
	if n := len(e.Refs) - 1; n > 0 {
		msg += fmt.Sprintf(" (and %d more)", n)
	}
	return msg
}

// ErrBudgetOverride is returned when profile_overrides.budget_bytes is set to a value that is not
// positive. It carries no doctor code: the shape of node.yaml belongs to config validation.
var ErrBudgetOverride = errors.New("profile_overrides.budget_bytes must be greater than 0")

// ModelSizeBytes is the size of every file of a model (weights, mmproj, draft, template).
func ModelSizeBytes(m *Model) int64 {
	var n int64
	for _, f := range m.Files {
		n += f.SizeBytes
	}
	return n
}

// modelTier is the survival tier of a model; an unset tier is 3, the first to be dropped, like a
// content item without one (ContentItem.Tier).
func modelTier(m *Model) int {
	if m.SurvivalTier != 0 {
		return m.SurvivalTier
	}
	return 3
}

// EffectivePlan computes the plan of the profile with the overrides.
//
// Selection: models = include.models ∪ add_models − remove_models, and content likewise, each in
// first-seen order without duplicates; a removed id wins over an added one, and removing an id
// that is not included is a no-op. An unknown profile id is LAI-CAT-003; any id in the profile's
// include list or in the overrides that the catalog does not have is LAI-CAT-004 (all of them are
// listed). The budget is overrides.budget_bytes when set, else the profile's storage.budget_bytes.
//
// Budget: the items are the kept models and then the kept content, a model weighing the sum of its
// files' size_bytes and a content item its expected_size_bytes (0 when unknown: it adds nothing and
// is never dropped, since dropping it would free nothing). While the total exceeds the budget the
// plan drops the next item in reverse priority: tier 3 before tier 2 before tier 1 (a model's tier
// is survival_tier, a content item's is ContentItem.Tier) and, within a tier, the later list
// position first (content comes after models). PlannedBytes is the sum of what is kept; it can
// still exceed the budget only when nothing with a size is left to drop.
func (c *Catalog) EffectivePlan(profileID string, ov Overrides) (*Plan, error) {
	p, ok := c.Profile(profileID)
	if !ok {
		return nil, &ProfileError{code: CodeUnknownProfile, Profile: profileID}
	}
	var refs []UnknownRef
	check := func(field string, ids []string, known func(string) bool) {
		for _, id := range ids {
			if !known(id) {
				refs = append(refs, UnknownRef{Field: field, ID: id})
			}
		}
	}
	isModel := func(id string) bool { _, ok := c.Model(id); return ok }
	isContent := func(id string) bool { _, ok := c.ContentItem(id); return ok }
	check("include.models", p.Include.Models, isModel)
	check("include.content", p.Include.Content, isContent)
	check("add_models", ov.AddModels, isModel)
	check("remove_models", ov.RemoveModels, isModel)
	check("add_content", ov.AddContent, isContent)
	check("remove_content", ov.RemoveContent, isContent)
	if len(refs) > 0 {
		return nil, &ProfileError{code: CodeUnknownRef, Profile: profileID, Refs: refs}
	}
	budget := p.Storage.BudgetBytes
	if ov.BudgetBytes != nil {
		if *ov.BudgetBytes <= 0 {
			return nil, ErrBudgetOverride
		}
		budget = *ov.BudgetBytes
	}

	type item struct {
		kind, id string
		tier     int
		size     int64
		pos      int
	}
	var items []item
	for _, id := range merge(p.Include.Models, ov.AddModels, ov.RemoveModels) {
		m, _ := c.Model(id)
		items = append(items, item{KindModel, id, modelTier(m), ModelSizeBytes(m), len(items)})
	}
	for _, id := range merge(p.Include.Content, ov.AddContent, ov.RemoveContent) {
		ci, _ := c.ContentItem(id)
		items = append(items, item{KindContent, id, ci.Tier(), ci.ExpectedSizeBytes, len(items)})
	}
	var total int64
	for _, it := range items {
		total += it.size
	}

	// Drop order: highest tier first, later position first.
	order := make([]int, len(items))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool {
		x, y := items[order[a]], items[order[b]]
		if x.tier != y.tier {
			return x.tier > y.tier
		}
		return x.pos > y.pos
	})
	dropped := make([]bool, len(items))
	plan := &Plan{Profile: p.ID, Models: []string{}, Content: []string{}, BudgetBytes: budget, Skipped: []Skipped{}}
	for _, i := range order {
		if total <= budget {
			break
		}
		if items[i].size == 0 {
			continue
		}
		dropped[i] = true
		total -= items[i].size
		plan.Skipped = append(plan.Skipped, Skipped{Kind: items[i].kind, ID: items[i].id, Reason: SkipBudget, SizeBytes: items[i].size})
	}
	for i, it := range items {
		if dropped[i] {
			continue
		}
		if it.kind == KindModel {
			plan.Models = append(plan.Models, it.id)
		} else {
			plan.Content = append(plan.Content, it.id)
		}
	}
	plan.PlannedBytes = total
	return plan, nil
}

// merge is include ∪ add − remove, in first-seen order without duplicates.
func merge(include, add, remove []string) []string {
	removed := make(map[string]bool, len(remove))
	for _, id := range remove {
		removed[id] = true
	}
	seen := map[string]bool{}
	out := []string{}
	for _, list := range [][]string{include, add} {
		for _, id := range list {
			if removed[id] || seen[id] {
				continue
			}
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}
