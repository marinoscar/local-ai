package catalog

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// planCatalog is a small synthetic catalog with round sizes so the budget arithmetic is visible.
//
// Models (size, tier): m1 10 t1, m2 20 t2, m3 30 t3, m4 40 t3, mx 5 t1 (not in the profile).
// Content (size, tier): c1 100 t1, c2 200 t2, c3 300 t3, c4 400 t3, k1 50 kit (t1), c5 unknown
// size t3, cx 7 t3 (not in the profile).
//
// Drop order (tier 3 first, later position first; models come before content):
// c5 (size 0, skipped over), c4, c3, m4, m3, then tier 2: c2, m2, then tier 1: k1, c1, m1.
func planCatalog() *Catalog {
	model := func(id string, tier int, size int64) Model {
		return Model{ID: id, Engine: "llama", SurvivalTier: tier, Files: []ModelFile{{Kind: "weights", Name: id, SizeBytes: size}}}
	}
	item := func(id, kind string, tier int, size int64) ContentItem {
		return ContentItem{ID: id, Kind: kind, SurvivalTier: tier, ExpectedSizeBytes: size}
	}
	models := []Model{model("m1", 1, 10), model("m2", 2, 20), model("m3", 3, 30), model("m4", 3, 40), model("mx", 1, 5)}
	content := []ContentItem{
		item("c1", "kiwix", 1, 100), item("c2", "kiwix", 2, 200), item("c3", "kiwix", 0, 300),
		item("c4", "maps", 3, 400), item("k1", "kit", 0, 50), item("c5", "kiwix", 0, 0), item("cx", "kiwix", 3, 7),
	}
	profiles := []Profile{{
		ID: "p", DisplayName: "Profile P",
		Storage: Storage{BudgetBytes: 10000},
		Include: Include{Models: []string{"m1", "m2", "m3", "m4"}, Content: []string{"c1", "c2", "c3", "c4", "k1", "c5"}},
	}}
	return newCatalog("2026.11.02-1", models, content, profiles)
}

func TestEffectivePlanSelection(t *testing.T) {
	tests := []struct {
		name        string
		ov          Overrides
		wantModels  []string
		wantContent []string
		wantBytes   int64
	}{
		{"no overrides", Overrides{},
			[]string{"m1", "m2", "m3", "m4"}, []string{"c1", "c2", "c3", "c4", "k1", "c5"}, 100 + 1050},
		{"add appends after the include list", Overrides{AddModels: []string{"mx"}, AddContent: []string{"cx"}},
			[]string{"m1", "m2", "m3", "m4", "mx"}, []string{"c1", "c2", "c3", "c4", "k1", "c5", "cx"}, 105 + 1057},
		{"remove", Overrides{RemoveModels: []string{"m2"}, RemoveContent: []string{"c1"}},
			[]string{"m1", "m3", "m4"}, []string{"c2", "c3", "c4", "k1", "c5"}, 80 + 950},
		{"adding an included id adds nothing", Overrides{AddModels: []string{"m1", "m1"}, AddContent: []string{"c1"}},
			[]string{"m1", "m2", "m3", "m4"}, []string{"c1", "c2", "c3", "c4", "k1", "c5"}, 1150},
		{"remove wins over add", Overrides{AddModels: []string{"mx"}, RemoveModels: []string{"mx"}, AddContent: []string{"cx"}, RemoveContent: []string{"cx"}},
			[]string{"m1", "m2", "m3", "m4"}, []string{"c1", "c2", "c3", "c4", "k1", "c5"}, 1150},
		{"removing an id that is not included is a no-op", Overrides{RemoveModels: []string{"mx"}, RemoveContent: []string{"cx"}},
			[]string{"m1", "m2", "m3", "m4"}, []string{"c1", "c2", "c3", "c4", "k1", "c5"}, 1150},
		{"remove everything", Overrides{RemoveModels: []string{"m1", "m2", "m3", "m4"}, RemoveContent: []string{"c1", "c2", "c3", "c4", "k1", "c5"}},
			[]string{}, []string{}, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p, err := planCatalog().EffectivePlan("p", tc.ov)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(p.Models, tc.wantModels) || !reflect.DeepEqual(p.Content, tc.wantContent) {
				t.Fatalf("models %q content %q, want %q %q", p.Models, p.Content, tc.wantModels, tc.wantContent)
			}
			if p.PlannedBytes != tc.wantBytes || p.BudgetBytes != 10000 || len(p.Skipped) != 0 {
				t.Fatalf("planned %d budget %d skipped %v", p.PlannedBytes, p.BudgetBytes, p.Skipped)
			}
		})
	}
}

func sk(kind, id string, size int64) Skipped {
	return Skipped{Kind: kind, ID: id, Reason: SkipBudget, SizeBytes: size}
}

func TestEffectivePlanBudgetDrops(t *testing.T) {
	// The full plan is 100 (models) + 1050 (content) = 1150 bytes.
	tests := []struct {
		name        string
		budget      int64
		wantSkipped []Skipped
		wantPlanned int64
		wantModels  []string
		wantContent []string
	}{
		{"exactly the budget drops nothing", 1150, nil, 1150,
			[]string{"m1", "m2", "m3", "m4"}, []string{"c1", "c2", "c3", "c4", "k1", "c5"}},
		{"one byte over drops the largest-position tier 3 item with a size", 1149,
			[]Skipped{sk("content", "c4", 400)}, 750,
			[]string{"m1", "m2", "m3", "m4"}, []string{"c1", "c2", "c3", "k1", "c5"}},
		{"stops as soon as it fits", 750, []Skipped{sk("content", "c4", 400)}, 750,
			[]string{"m1", "m2", "m3", "m4"}, []string{"c1", "c2", "c3", "k1", "c5"}},
		{"within tier 3 later positions go first, content before models", 700,
			[]Skipped{sk("content", "c4", 400), sk("content", "c3", 300)}, 450,
			[]string{"m1", "m2", "m3", "m4"}, []string{"c1", "c2", "k1", "c5"}},
		{"tier 3 before tier 2 before tier 1", 300,
			[]Skipped{sk("content", "c4", 400), sk("content", "c3", 300), sk("model", "m4", 40), sk("model", "m3", 30), sk("content", "c2", 200)}, 180,
			[]string{"m1", "m2"}, []string{"c1", "k1", "c5"}},
		{"tier 1 goes last, later position first", 120,
			[]Skipped{sk("content", "c4", 400), sk("content", "c3", 300), sk("model", "m4", 40), sk("model", "m3", 30), sk("content", "c2", 200), sk("model", "m2", 20), sk("content", "k1", 50)}, 110,
			[]string{"m1"}, []string{"c1", "c5"}},
		{"nothing fits", 5,
			[]Skipped{sk("content", "c4", 400), sk("content", "c3", 300), sk("model", "m4", 40), sk("model", "m3", 30), sk("content", "c2", 200), sk("model", "m2", 20),
				sk("content", "k1", 50), sk("content", "c1", 100), sk("model", "m1", 10)}, 0,
			[]string{}, []string{"c5"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b := tc.budget
			p, err := planCatalog().EffectivePlan("p", Overrides{BudgetBytes: &b})
			if err != nil {
				t.Fatal(err)
			}
			if p.BudgetBytes != tc.budget {
				t.Fatalf("budget %d", p.BudgetBytes)
			}
			want := tc.wantSkipped
			if want == nil {
				want = []Skipped{}
			}
			if !reflect.DeepEqual(p.Skipped, want) {
				t.Fatalf("skipped\n got %v\nwant %v", p.Skipped, want)
			}
			if p.PlannedBytes != tc.wantPlanned {
				t.Fatalf("planned %d, want %d", p.PlannedBytes, tc.wantPlanned)
			}
			if !reflect.DeepEqual(p.Models, tc.wantModels) || !reflect.DeepEqual(p.Content, tc.wantContent) {
				t.Fatalf("kept models %q content %q, want %q %q", p.Models, p.Content, tc.wantModels, tc.wantContent)
			}
		})
	}
}

func TestEffectivePlanBudgetSource(t *testing.T) {
	c := planCatalog()
	// The profile's own budget applies without an override.
	c.Profiles[0].Storage.BudgetBytes = 1000
	p, err := c.EffectivePlan("p", Overrides{})
	if err != nil || p.BudgetBytes != 1000 || p.PlannedBytes > 1000 {
		t.Fatalf("profile budget: %+v %v", p, err)
	}
	// The override wins, larger or smaller.
	for _, b := range []int64{1, 1 << 40} {
		b := b
		p, err := c.EffectivePlan("p", Overrides{BudgetBytes: &b})
		if err != nil || p.BudgetBytes != b {
			t.Fatalf("override %d: %+v %v", b, p, err)
		}
	}
	// A budget that is not positive is refused.
	for _, b := range []int64{0, -1} {
		b := b
		if _, err := c.EffectivePlan("p", Overrides{BudgetBytes: &b}); !errors.Is(err, ErrBudgetOverride) {
			t.Fatalf("budget %d: err %v", b, err)
		}
	}
}

func TestEffectivePlanKeepsModelsThatDoNotFit(t *testing.T) {
	// The plan never looks at hardware: a model that cannot load on this node is still stored.
	c := planCatalog()
	c.Models[3].Requirements = Requirements{Backends: []string{"cuda"}, MinVRAMMiB: 1 << 30, MinRAMMiB: 1 << 30}
	p, err := c.EffectivePlan("p", Overrides{})
	if err != nil || !reflect.DeepEqual(p.Models, []string{"m1", "m2", "m3", "m4"}) {
		t.Fatalf("%+v %v", p, err)
	}
}

func TestEffectivePlanErrors(t *testing.T) {
	c := planCatalog()
	t.Run("unknown profile", func(t *testing.T) {
		for _, id := range []string{"nope", ""} {
			_, err := c.EffectivePlan(id, Overrides{})
			var pe *ProfileError
			if !errors.As(err, &pe) || pe.Code() != CodeUnknownProfile {
				t.Fatalf("%q: %v", id, err)
			}
			if !strings.Contains(err.Error(), CodeUnknownProfile) {
				t.Fatalf("message lacks the code: %v", err)
			}
		}
	})
	t.Run("unknown ids in every list", func(t *testing.T) {
		ov := Overrides{AddModels: []string{"bad-a"}, RemoveModels: []string{"bad-b"}, AddContent: []string{"bad-c"}, RemoveContent: []string{"bad-d", "m1"}}
		_, err := c.EffectivePlan("p", ov)
		var pe *ProfileError
		if !errors.As(err, &pe) || pe.Code() != CodeUnknownRef {
			t.Fatalf("%v", err)
		}
		want := []UnknownRef{{"add_models", "bad-a"}, {"remove_models", "bad-b"}, {"add_content", "bad-c"}, {"remove_content", "bad-d"}, {"remove_content", "m1"}}
		if !reflect.DeepEqual(pe.Refs, want) {
			t.Fatalf("refs %v, want %v", pe.Refs, want)
		}
		if !strings.Contains(err.Error(), "(and 4 more)") {
			t.Fatalf("message: %v", err)
		}
	})
	t.Run("a model id in a content list is unknown", func(t *testing.T) {
		_, err := c.EffectivePlan("p", Overrides{AddContent: []string{"m1"}})
		var pe *ProfileError
		if !errors.As(err, &pe) || pe.Code() != CodeUnknownRef || len(pe.Refs) != 1 {
			t.Fatalf("%v", err)
		}
	})
	t.Run("unknown id in the profile include list", func(t *testing.T) {
		bad := planCatalog()
		bad.Profiles[0].Include.Models = append(bad.Profiles[0].Include.Models, "ghost")
		bad.Profiles[0].Include.Content = append(bad.Profiles[0].Include.Content, "ghost-content")
		_, err := bad.EffectivePlan("p", Overrides{})
		var pe *ProfileError
		if !errors.As(err, &pe) || pe.Code() != CodeUnknownRef {
			t.Fatalf("%v", err)
		}
		want := []UnknownRef{{"include.models", "ghost"}, {"include.content", "ghost-content"}}
		if !reflect.DeepEqual(pe.Refs, want) {
			t.Fatalf("refs %v", pe.Refs)
		}
	})
}

func TestEffectivePlanDoesNotMutateInputs(t *testing.T) {
	c := planCatalog()
	ov := Overrides{AddModels: []string{"mx"}, RemoveModels: []string{"m2"}}
	if _, err := c.EffectivePlan("p", ov); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(c.Profiles[0].Include.Models, []string{"m1", "m2", "m3", "m4"}) || !reflect.DeepEqual(ov.AddModels, []string{"mx"}) {
		t.Fatal("EffectivePlan changed its inputs")
	}
	// Same inputs, same answer.
	a, _ := c.EffectivePlan("p", ov)
	b, _ := c.EffectivePlan("p", ov)
	if !reflect.DeepEqual(a, b) {
		t.Fatal("not deterministic")
	}
}

func TestModelTierDefaults(t *testing.T) {
	// A model without a tier is dropped before one with tier 2.
	c := planCatalog()
	c.Models[0].SurvivalTier = 0                     // m1 now counts as tier 3, position 0
	budget := int64(1150 - 400 - 300 - 40 - 30 - 10) // everything tier 3 must go
	p, err := c.EffectivePlan("p", Overrides{BudgetBytes: &budget})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, s := range p.Skipped {
		got[s.ID] = true
	}
	for _, id := range []string{"c4", "c3", "m4", "m3", "m1"} {
		if !got[id] {
			t.Errorf("%s not dropped: %v", id, p.Skipped)
		}
	}
	if got["m2"] || got["c2"] {
		t.Errorf("tier 2 dropped early: %v", p.Skipped)
	}
}

func TestBuildProfileView(t *testing.T) {
	c := planCatalog()
	c.Profiles[0].Storage.BudgetBytes = 1200
	c = newCatalog(c.Version, c.Models, c.Content, append(c.Profiles, labProfiles()...))
	snap := loadHW(t, "ai-n3")

	v, err := c.BuildProfileView("p", Overrides{}, snap)
	if err != nil {
		t.Fatal(err)
	}
	if v.Profile != "p" || v.DisplayName != "Profile P" || v.Suggested != "small-8gb" {
		t.Fatalf("%+v", v)
	}
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"profile", "display_name", "overrides", "suggested", "match_reasons", "effective"} {
		if _, ok := m[k]; !ok {
			t.Errorf("missing key %q in %s", k, raw)
		}
	}
	eff := m["effective"].(map[string]any)
	for _, k := range []string{"models", "content", "planned_bytes", "budget_bytes", "skipped"} {
		if _, ok := eff[k]; !ok {
			t.Errorf("missing effective.%s in %s", k, raw)
		}
	}
	ovm := m["overrides"].(map[string]any)
	if ovm["add_models"] == nil || ovm["budget_bytes"] != nil {
		t.Errorf("overrides must have empty lists and a null budget: %v", ovm)
	}
	if !reflect.DeepEqual(v.MatchReasons, []string{"backend cuda", "vram 8192 MiB in 7000..11999", "ram 14336 MiB in 12000.."}) {
		t.Errorf("reasons %q", v.MatchReasons)
	}
	if v.Effective.PlannedBytes != 1150 || len(v.Effective.Skipped) != 0 {
		t.Errorf("effective %+v", v.Effective)
	}

	// A profile that does not match the hardware is kept; Suggested only reports.
	v, err = c.BuildProfileView("max-128gb", Overrides{}, snap)
	if err != nil || v.Profile != "max-128gb" || v.Suggested != "small-8gb" {
		t.Fatalf("a mismatched profile must be kept: %+v %v", v, err)
	}
}

func TestBuildProfileViewUnknownProfileStillSuggests(t *testing.T) {
	c0 := planCatalog()
	c := newCatalog(c0.Version, c0.Models, c0.Content, append(c0.Profiles, labProfiles()...))
	v, err := c.BuildProfileView("gone", Overrides{AddModels: []string{"mx"}}, loadHW(t, "ai-n2"))
	var pe *ProfileError
	if !errors.As(err, &pe) || pe.Code() != CodeUnknownProfile {
		t.Fatalf("err %v", err)
	}
	if v.Profile != "gone" || v.Suggested != "cuda-12gb" || len(v.MatchReasons) == 0 || v.DisplayName != "" {
		t.Fatalf("%+v", v)
	}
	if v.Effective.Models == nil || v.Effective.Content == nil || v.Effective.Skipped == nil {
		t.Fatal("effective lists must be empty, not null")
	}
	if !reflect.DeepEqual(v.Overrides.AddModels, []string{"mx"}) {
		t.Fatalf("overrides lost: %+v", v.Overrides)
	}
}

func TestBuildProfileViewOnValidCatalog(t *testing.T) {
	c, err := Load(validDir(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range c.Profiles {
		plan, err := c.EffectivePlan(p.ID, Overrides{})
		if err != nil {
			t.Fatalf("%s: %v", p.ID, err)
		}
		if plan.PlannedBytes > plan.BudgetBytes && len(plan.Models)+len(plan.Content) > 0 {
			t.Errorf("%s: planned %d over budget %d", p.ID, plan.PlannedBytes, plan.BudgetBytes)
		}
	}
	if _, err := c.BuildProfileView("small-8gb", Overrides{}, loadHW(t, "ai-n3")); err != nil {
		t.Fatal(err)
	}
}
