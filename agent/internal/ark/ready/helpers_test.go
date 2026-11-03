package ready_test

import (
	"flag"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/ark"
	"github.com/marinoscar/local-ai/agent/internal/ark/ready"
	"github.com/marinoscar/local-ai/agent/internal/catalog"
	"github.com/marinoscar/local-ai/agent/internal/sync/journal"
	"github.com/marinoscar/local-ai/agent/internal/sync/manifest"
	"github.com/marinoscar/local-ai/agent/internal/sync/plan"
)

var update = flag.Bool("update", false, "rewrite the golden files")

// now is the one clock of the tests: every fixture time is an offset from it, never a second reading
// of the wall clock.
var now = time.Date(2026, 11, 2, 15, 0, 0, 0, time.UTC)

const (
	catVersion = "2026.11.02-1"
	day        = 24 * time.Hour
	gb         = int64(1_000_000_000)
)

// world builds a ready.Input the way the gatherer does: a tiny catalog, a profile, a planner pass
// over it, and the facts of the root. The numbers are small (a budget of 100, an item of 40) so a case
// reads as arithmetic; the golden tests use gigabytes.
type world struct {
	models  []catalog.Model
	content []catalog.ContentItem
	survive []string

	present  map[string]manifest.Manifest
	resolved map[string]plan.Resolved
	journal  map[string]ready.JournalFact
	// in is the Input under construction; its Plan, Present, Journal, Catalog and Profile are filled by
	// input().
	in ready.Input

	kind    ark.RootKind
	budget  int64
	minFree int64
	total   int64
	free    int64
	// offline plans without the network.
	offline  bool
	readOnly bool
	// profile is the profile id; "test-profile" (internal) or "ark-drive-full" (drive) when empty.
	profile string
}

func newWorld() *world {
	return &world{
		present: map[string]manifest.Manifest{}, resolved: map[string]plan.Resolved{}, journal: map[string]ready.JournalFact{},
		kind: ark.KindInternal, budget: 1000, minFree: 5, total: 2000, free: 1000,
		in: ready.Input{Now: now, NodeID: "ai-n3", AgentVersion: "0.4.0", CatalogVersion: catVersion},
	}
}

// drive makes the root an ark drive of 100 GB with 60 GB free: the planner's drive floor for free space
// is 5 GB, so a drive with tiny numbers would always be short of room.
func (w *world) drive() *world {
	w.kind = ark.KindArkDrive
	w.total, w.free = 100*gb, 60*gb
	return w
}

// with adds items to the profile (models first, as the planner lists them).
func (w *world) withModels(ms ...catalog.Model) *world { w.models = append(w.models, ms...); return w }
func (w *world) withContent(cs ...catalog.ContentItem) *world {
	w.content = append(w.content, cs...)
	return w
}

// model is a llama model of a tier and size; engine and languages are set by the caller for speech.
func model(id string, tier int, size int64) catalog.Model {
	return catalog.Model{
		ID: id, Engine: "llama", SurvivalTier: tier, Languages: []string{"en"},
		Source: catalog.ModelSource{Type: "huggingface", Revision: "rev-" + id + "-0000"},
		Files:  []catalog.ModelFile{{Kind: "weights", Name: id + ".gguf", SizeBytes: size}},
	}
}

func whisper(id string, size int64) catalog.Model {
	m := model(id, 1, size)
	m.Engine, m.Languages = "whisper", []string{"multi"}
	return m
}

func piper(id, lang string, size int64) catalog.Model {
	m := model(id, 1, size)
	m.Engine, m.Languages = "piper", []string{lang}
	return m
}

func kiwix(id string, tier int, size int64) catalog.ContentItem {
	return catalog.ContentItem{ID: id, Kind: "kiwix", SurvivalTier: tier, ExpectedSizeBytes: size, RefreshDays: 90,
		Source: catalog.ContentSource{Type: "kiwix", VersionPolicy: "latest"}}
}

func mapOf(id string, tier int, size int64) catalog.ContentItem {
	return catalog.ContentItem{ID: id, Kind: "maps", SurvivalTier: tier, ExpectedSizeBytes: size, RefreshDays: 180,
		Source: catalog.ContentSource{Type: "pmtiles_extract"}}
}

func mapAssets(size int64) catalog.ContentItem {
	return catalog.ContentItem{ID: "map-assets", Kind: "maps", SurvivalTier: 1, ExpectedSizeBytes: size,
		Source: catalog.ContentSource{Type: "github_release", Tag: "v1"}}
}

func kit(id string, size int64) catalog.ContentItem {
	return catalog.ContentItem{ID: id, Kind: "kit", ExpectedSizeBytes: size, Source: catalog.ContentSource{Type: "github_release", Tag: "tag-" + id}}
}

func closure(codename, arch string, size int64) catalog.ContentItem {
	return catalog.ContentItem{ID: "kit-debs-" + codename + "-" + arch, Kind: "kit", ExpectedSizeBytes: size, RefreshDays: 30,
		Source: catalog.ContentSource{Type: "apt_closure", Codename: codename, Arch: arch}}
}

func docs(size int64) catalog.ContentItem {
	return catalog.ContentItem{ID: "docs-bundle", Kind: "docs", ExpectedSizeBytes: size, Source: catalog.ContentSource{Type: "repo_docs"}}
}

// kindOf returns the manifest item kind of a catalog id.
func (w *world) kindOf(id string) string {
	for _, m := range w.models {
		if m.ID == id {
			return manifest.KindModel
		}
	}
	for _, c := range w.content {
		if c.ID == id {
			return c.Kind
		}
	}
	return "other"
}

// have puts a verified copy on the root: version v, verified and re-verified `age` ago, 10 bytes
// unless the catalog says otherwise.
func (w *world) have(id, version string, age time.Duration) *world {
	size := int64(10)
	for _, m := range w.models {
		if m.ID == id {
			size = catalog.ModelSizeBytes(&m)
		}
	}
	for _, c := range w.content {
		if c.ID == id && c.ExpectedSizeBytes > 0 {
			size = c.ExpectedSizeBytes
		}
	}
	w.present[id] = manifest.Manifest{
		SchemaVersion: 1, ItemID: id, ItemKind: w.kindOf(id), CatalogVersion: catVersion, Version: version, SortKey: version,
		State: manifest.StateVerified, TotalBytes: size, Trust: manifest.TrustPinned,
		DownloadedAt: now.Add(-age), VerifiedAt: now.Add(-age), LastReverifiedAt: now.Add(-age),
	}
	return w
}

// haveOrphan puts a verified copy of an item that is not in the profile.
func (w *world) haveOrphan(id, kind string, age time.Duration) *world {
	w.present[id] = manifest.Manifest{
		SchemaVersion: 1, ItemID: id, ItemKind: kind, Version: "v1", SortKey: "v1", State: manifest.StateVerified,
		TotalBytes: 10, Trust: manifest.TrustPinned, DownloadedAt: now.Add(-age), VerifiedAt: now.Add(-age), LastReverifiedAt: now.Add(-age),
	}
	return w
}

// reverified overrides last_reverified_at of a copy.
func (w *world) reverified(id string, age time.Duration) *world {
	m := w.present[id]
	m.LastReverifiedAt = now.Add(-age)
	w.present[id] = m
	return w
}

// target sets what the resolver says the version of an item should be.
func (w *world) target(id, version string, size int64) *world {
	w.resolved[id] = plan.Resolved{Version: version, SizeBytes: size}
	return w
}

// checked sets checked_current_at of an item.
func (w *world) checked(id string, age time.Duration) *world {
	j := w.journal[id]
	j.CheckedCurrentAt = now.Add(-age)
	w.journal[id] = j
	return w
}

// failedItem records a failed or quarantined attempt for version v.
func (w *world) failedItem(id string, state journal.ItemState, version, code string) *world {
	j := w.journal[id]
	j.State, j.Version, j.Code = state, version, code
	w.journal[id] = j
	return w
}

// synced records the last run (done) that finished `ago` ago.
func (w *world) synced(ago time.Duration) *world {
	w.in.LastSync = &ready.RunInfo{RunID: "run-20261102T031500Z-a1f3", Trigger: "scheduled", FinishedAt: now.Add(-ago), State: "done"}
	w.in.LastDoneAt = now.Add(-ago)
	return w
}

// planInput builds the planner's input: it resolves every desired item that has no answer yet to "v1"
// at the catalog size (so a case states only what is special about it).
func (w *world) planInput() plan.PlanInput {
	prof := catalog.Profile{
		ID: "test-profile", Storage: catalog.Storage{Target: "internal", BudgetBytes: w.budget, MinFreeBytes: w.minFree},
	}
	if w.kind == ark.KindArkDrive {
		prof.ID = "ark-drive-full"
	}
	if w.profile != "" {
		prof.ID = w.profile
	}
	for _, m := range w.models {
		prof.Include.Models = append(prof.Include.Models, m.ID)
	}
	for _, c := range w.content {
		prof.Include.Content = append(prof.Include.Content, c.ID)
	}
	prof.Survival.Models = w.survive
	cat := catalog.New(catVersion, w.models, w.content, []catalog.Profile{prof})
	for i := range w.models {
		if _, ok := w.resolved[w.models[i].ID]; !ok {
			w.target(w.models[i].ID, "v1", catalog.ModelSizeBytes(&w.models[i]))
		}
	}
	for _, c := range w.content {
		if _, ok := w.resolved[c.ID]; !ok {
			w.target(c.ID, "v1", c.ExpectedSizeBytes)
		}
	}
	root := plan.RootInfo{
		ID: ark.InternalID, Kind: w.kind, Path: "/srv/localai", Profile: prof.ID, TotalBytes: w.total, FreeBytes: w.free, ReadOnly: w.readOnly,
	}
	if w.kind == ark.KindArkDrive {
		root.ID, root.Path = "ark-3f9c0a12", "/media/operator/AI-ARK/localai"
	}
	pin := plan.PlanInput{
		Root: root, Catalog: cat, Profile: &prof, Present: w.present, Resolved: w.resolved, Now: now,
		Node: plan.NodeInfo{Arch: "amd64", Codename: "resolute"}, Mode: plan.ModeOnline, Offline: w.offline,
	}
	if w.kind == ark.KindInternal {
		pin.Overrides = &plan.ProfileOverrides{}
	}
	return pin
}

// input builds the ready.Input: the planner's input, planned, and the facts of the root.
func (w *world) input() ready.Input {
	pin := w.planInput()
	root, prof := pin.Root, pin.Profile
	in := w.in
	in.Root = ready.RootFacts{ID: root.ID, Kind: w.kind, Path: root.Path, Profile: prof.ID, TotalBytes: w.total, FreeBytes: w.free, ReadOnly: w.readOnly}
	in.Plan = plan.Plan(pin)
	in.Catalog, in.Profile = pin.Catalog, prof
	in.Present, in.Journal = w.present, w.journal
	return in
}

// build is input() then ready.Build.
func (w *world) build() ready.Report { return ready.Build(w.input()) }

// item returns the report's entry of an id or fails the test.
func item(t *testing.T, r ready.Report, id string) ready.Item {
	t.Helper()
	it, ok := r.Item(id)
	if !ok {
		t.Fatalf("no item %s in %+v", id, r.Items)
	}
	return it
}

// problemCodes lists the codes of a report's problems, in order.
func problemCodes(r ready.Report) []string {
	out := make([]string, len(r.Problems))
	for i, p := range r.Problems {
		out[i] = p.Code
	}
	return out
}

func readGolden(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "golden", name))
	if err != nil {
		t.Fatalf("%v (run: go test ./internal/ark/ready -update)", err)
	}
	return b
}

func writeGolden(t *testing.T, name string, data []byte) {
	t.Helper()
	dir := filepath.Join("testdata", "golden")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
		t.Fatal(err)
	}
}
