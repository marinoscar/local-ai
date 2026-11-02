package catalog

import "github.com/marinoscar/local-ai/agent/internal/hardware"

// ProfileView is the body of GET /profile and of the PUT /profile answer (agent.md section 3.8.30).
type ProfileView struct {
	Profile     string `json:"profile"`
	DisplayName string `json:"display_name"`
	// Overrides is node.yaml profile_overrides, with empty lists instead of null.
	Overrides Overrides `json:"overrides"`
	// Suggested is the profile the hardware matches ("" only when the catalog has no cpu-only
	// profile and nothing else matches) and MatchReasons says why (never null).
	Suggested    string   `json:"suggested"`
	MatchReasons []string `json:"match_reasons"`
	// Effective is the plan of Profile with Overrides.
	Effective Plan `json:"effective"`
}

// BuildProfileView builds the GET /profile body for the node's profile and overrides on this
// hardware. A node whose profile does not match its hardware keeps it: Suggested only reports.
//
// When the plan cannot be computed (an unknown profile, LAI-CAT-003, or an unknown id,
// LAI-CAT-004) the error is returned together with a view that still carries Profile, Overrides,
// Suggested and MatchReasons (and the display name when the profile exists), so the caller can
// answer the question "what would fit" even for a broken node.yaml.
func (c *Catalog) BuildProfileView(profileID string, ov Overrides, snap hardware.Snapshot) (ProfileView, error) {
	suggested, reasons := c.Suggest(snap)
	v := ProfileView{
		Profile:      profileID,
		Overrides:    ov.normalized(),
		Suggested:    suggested,
		MatchReasons: append([]string{}, reasons...),
		Effective:    Plan{Profile: profileID, Models: []string{}, Content: []string{}, Skipped: []Skipped{}},
	}
	if p, ok := c.Profile(profileID); ok {
		v.DisplayName = p.DisplayName
	}
	plan, err := c.EffectivePlan(profileID, ov)
	if err != nil {
		return v, err
	}
	v.Effective = *plan
	return v, nil
}

// normalized copies the overrides with nil lists replaced by empty ones, for a stable wire shape.
func (o Overrides) normalized() Overrides {
	cp := func(s []string) []string { return append([]string{}, s...) }
	out := Overrides{AddModels: cp(o.AddModels), RemoveModels: cp(o.RemoveModels),
		AddContent: cp(o.AddContent), RemoveContent: cp(o.RemoveContent)}
	if o.BudgetBytes != nil {
		v := *o.BudgetBytes
		out.BudgetBytes = &v
	}
	return out
}
