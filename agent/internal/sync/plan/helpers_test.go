package plan_test

import (
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/ark"
	"github.com/marinoscar/local-ai/agent/internal/catalog"
	"github.com/marinoscar/local-ai/agent/internal/sync/manifest"
	"github.com/marinoscar/local-ai/agent/internal/sync/plan"
)

// now is the one clock of the tests.
var now = time.Date(2026, 11, 2, 15, 0, 0, 0, time.UTC)

const catVersion = "2026.11.02-1"

// The tests use tiny sizes (a budget of 100, an item of 40) so a case reads as arithmetic. Only the
// golden tests use gigabytes.

type itemOpt func(*catalog.ContentItem)

func tier(n int) itemOpt    { return func(c *catalog.ContentItem) { c.SurvivalTier = n } }
func refresh(d int) itemOpt { return func(c *catalog.ContentItem) { c.RefreshDays = d } }
func source(s catalog.ContentSource) itemOpt {
	return func(c *catalog.ContentItem) { c.Source = s }
}

// latestZIM is a kiwix source that follows "latest": its version needs a resolver.
var latestZIM = catalog.ContentSource{Type: "kiwix", VersionPolicy: "latest"}

// model is a llama model of a given tier and size.
func model(id string, tierN int, size int64) catalog.Model {
	return catalog.Model{
		ID: id, Engine: "llama", SurvivalTier: tierN,
		Source: catalog.ModelSource{Type: "huggingface", Revision: "rev-" + id + "-0000"},
		Files:  []catalog.ModelFile{{Kind: "weights", Name: id + ".gguf", SizeBytes: size}},
	}
}

// zim is a kiwix item that follows "latest" (a refresh_days item, not pinned).
func zim(id string, size int64, opts ...itemOpt) catalog.ContentItem {
	c := catalog.ContentItem{ID: id, Kind: "kiwix", ExpectedSizeBytes: size, Source: latestZIM, SurvivalTier: 2}
	for _, o := range opts {
		o(&c)
	}
	return c
}

func kit(id string, size int64, opts ...itemOpt) catalog.ContentItem {
	c := catalog.ContentItem{ID: id, Kind: "kit", ExpectedSizeBytes: size, Source: catalog.ContentSource{Type: "github_release", Tag: "tag-" + id}}
	for _, o := range opts {
		o(&c)
	}
	return c
}

func debs(codename, arch string, size int64) catalog.ContentItem {
	return catalog.ContentItem{ID: "kit-debs-" + codename + "-" + arch, Kind: "kit", ExpectedSizeBytes: size, RefreshDays: 30,
		Source: catalog.ContentSource{Type: "apt_closure", Codename: codename, Arch: arch}}
}

func docs(size int64) catalog.ContentItem {
	return catalog.ContentItem{ID: "docs-bundle", Kind: "docs", ExpectedSizeBytes: size, Source: catalog.ContentSource{Type: "repo_docs"}}
}

// scenario builds a PlanInput for the internal root: a profile that lists models then content in the
// given order, a budget of 100 and a min_free of 5, with 1000 free.
type scenario struct {
	models  []catalog.Model
	content []catalog.ContentItem
	survive []string
	in      plan.PlanInput
}

func newScenario(models []catalog.Model, content []catalog.ContentItem) *scenario {
	s := &scenario{models: models, content: content}
	prof := catalog.Profile{ID: "test-profile", Storage: catalog.Storage{Target: "internal", BudgetBytes: 100, MinFreeBytes: 5}}
	for _, m := range models {
		prof.Include.Models = append(prof.Include.Models, m.ID)
	}
	for _, c := range content {
		prof.Include.Content = append(prof.Include.Content, c.ID)
	}
	s.in = plan.PlanInput{
		Root:     plan.RootInfo{ID: ark.InternalID, Kind: ark.KindInternal, Path: "/srv/localai", Profile: "test-profile", TotalBytes: 2000, FreeBytes: 1000},
		Profile:  &prof,
		Present:  map[string]manifest.Manifest{},
		Partial:  map[string]plan.PartState{},
		Resolved: map[string]plan.Resolved{},
		Now:      now,
	}
	return s
}

// build finishes the scenario: it creates the catalog and, for every desired item that has no
// Resolved entry yet, resolves it from the catalog pins (what the gatherer does offline), so a case
// only states what is special about it.
func (s *scenario) build() plan.PlanInput {
	s.in.Profile.Survival.Models = s.survive
	s.in.Catalog = catalog.New(catVersion, s.models, s.content, []catalog.Profile{*s.in.Profile})
	return s.in
}

// resolve sets the resolver answer of an item.
func (s *scenario) resolve(id, version string, size int64) {
	s.in.Resolved[id] = plan.Resolved{Version: version, SizeBytes: size}
}

// resolveAll resolves every item as "v1" with the catalog size.
func (s *scenario) resolveAll() {
	for _, m := range s.models {
		s.resolve(m.ID, "v1", catalog.ModelSizeBytes(&m))
	}
	for _, c := range s.content {
		s.resolve(c.ID, "v1", c.ExpectedSizeBytes)
	}
}

// have marks a verified copy of an item on the root.
func (s *scenario) have(id, kind, version string, size int64, age time.Duration) {
	s.in.Present[id] = manifest.Manifest{ItemID: id, ItemKind: kind, Version: version, TotalBytes: size, DownloadedAt: now.Add(-age)}
}

const days = 24 * time.Hour

// row returns the row of an item or fails the test.
func row(t *testing.T, p plan.RootPlan, id string) plan.ItemPlan {
	t.Helper()
	it, ok := p.Item(id)
	if !ok {
		t.Fatalf("no row for %s in %+v", id, p.Items)
	}
	return it
}

// ids is the item ids of the plan in order.
func ids(p plan.RootPlan) []string {
	out := make([]string, len(p.Items))
	for i, it := range p.Items {
		out[i] = it.ID
	}
	return out
}
