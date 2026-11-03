package plan_test

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/ark"
	"github.com/marinoscar/local-ai/agent/internal/catalog"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/sync/manifest"
	"github.com/marinoscar/local-ai/agent/internal/sync/plan"
)

// Tests of the real catalog/ directory (CAT-03, docs/specs/catalog.md section 3.1 and 4): the ark
// content lints, every profile plans inside its budget with nothing skipped, ark-drive-full holds every
// item, and the first-aid links are complete. They run the real loader (catalog.Load) and the real
// planner (plan.Plan) on the files that ship, so an edit that overflows a profile fails here before a
// node finds out.
//
// models.yaml (CAT-02) may not exist yet. Then these tests stage a synthetic one next to the real
// ark.yaml and profiles: one lint-valid model per id the profiles name, with an upper-bound size from
// modelReserve and a digest computed at run time (the repository never stores a 64-hex string, see
// scripts/check-redaction.sh). Once catalog/models.yaml exists it is used as it is.

// catalogDir is the real catalog directory, relative to this package.
const catalogDir = "../../../../catalog"

// modelReserve is the upper bound of the bytes a model takes, used only while catalog/models.yaml does
// not exist. The figures are catalog.md section 2.1 and the lab inventory rounded up (the coder model
// is about 48.4 GB on ai-n1). A model that is not listed weighs the default.
var modelReserve = map[string]int64{
	"qwen3.5-9b-q4km":        8_000_000_000,
	"qwen3.6-35b-a3b-q4km":   26_000_000_000,
	"qwen3-coder-next-q4km":  52_000_000_000,
	"qwen3.8-27b-q4km":       22_000_000_000,
	"whisper-small":          600_000_000,
	"whisper-medium":         1_600_000_000,
	"whisper-large-v3-turbo": 1_800_000_000,
	"piper-en-medium":        100_000_000,
	"piper-es-medium":        100_000_000,
}

const defaultModelReserve = 10_000_000_000

// stagedCatalog copies the real catalog into a temp directory and adds a synthetic models.yaml when the
// real one is missing. It returns the directory and whether models.yaml is synthetic.
func stagedCatalog(t *testing.T) (dir string, synthetic bool) {
	t.Helper()
	src, err := filepath.Abs(catalogDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(src, "ark.yaml")); err != nil {
		t.Fatalf("the real catalog is missing: %v", err)
	}
	dir = t.TempDir()
	copyFile := func(rel string) {
		data, err := os.ReadFile(filepath.Join(src, rel))
		if err != nil {
			t.Fatal(err)
		}
		dst := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dst, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	copyFile("CATALOG_VERSION")
	copyFile("ark.yaml")
	profiles, err := filepath.Glob(filepath.Join(src, "profiles", "*.yaml"))
	if err != nil || len(profiles) == 0 {
		t.Fatalf("no profiles in %s: %v", src, err)
	}
	var ids []string
	seen := map[string]bool{}
	for _, p := range profiles {
		copyFile(filepath.Join("profiles", filepath.Base(p)))
	}
	if _, err := os.Stat(filepath.Join(src, "models.yaml")); err == nil {
		copyFile("models.yaml")
		return dir, false
	}
	// Collect the model ids the profiles name by loading them one by one through the YAML text: the
	// profiles are lint-checked below, so a simple scan of the include.models line is enough here.
	for _, p := range profiles {
		data, _ := os.ReadFile(p)
		for _, id := range modelIDsOf(string(data)) {
			if !seen[id] {
				seen[id] = true
				ids = append(ids, id)
			}
		}
	}
	sort.Strings(ids)
	if err := os.WriteFile(filepath.Join(dir, "models.yaml"), []byte(syntheticModels(ids)), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir, true
}

var includeModelsRe = regexp.MustCompile(`(?m)^  models: \[([^\]]*)\]`)

// modelIDsOf returns the ids of the first `models: [...]` flow list under include: in a profile.
func modelIDsOf(profile string) []string {
	m := includeModelsRe.FindStringSubmatch(profile)
	if m == nil {
		return nil
	}
	var out []string
	for _, id := range strings.Split(m[1], ",") {
		if id = strings.TrimSpace(id); id != "" {
			out = append(out, id)
		}
	}
	return out
}

func fakeDigest(s string) string {
	sum := sha256.Sum256([]byte("synthetic:" + s))
	return hex.EncodeToString(sum[:])
}

// syntheticModels writes a lint-valid models.yaml for the given ids.
func syntheticModels(ids []string) string {
	var b strings.Builder
	b.WriteString("schema_version: 1\nmodels:\n")
	for _, id := range ids {
		size, ok := modelReserve[id]
		if !ok {
			size = defaultModelReserve
		}
		fmt.Fprintf(&b, "  - id: %s\n    display_name: %q\n    summary: \"Synthetic model for the budget tests.\"\n", id, id)
		fmt.Fprintf(&b, "    license: apache-2.0\n    survival_tier: 1\n")
		switch {
		case strings.HasPrefix(id, "whisper-"):
			fmt.Fprintf(&b, "    engine: whisper\n    roles: [stt]\n    languages: [multi]\n")
			fmt.Fprintf(&b, "    source: {type: url}\n    files:\n      - {kind: whisper, name: %s.bin, url: \"https://example.test/%s.bin\", size_bytes: %d, sha256: \"%s\"}\n",
				id, id, size, fakeDigest(id))
			fmt.Fprintf(&b, "    requirements: {backends: [cuda, vulkan, cpu], min_vram_mib: 0, min_ram_mib: 1024}\n")
		case strings.HasPrefix(id, "piper-"):
			fmt.Fprintf(&b, "    engine: piper\n    roles: [tts]\n    languages: [en]\n")
			fmt.Fprintf(&b, "    source: {type: url}\n    files:\n")
			fmt.Fprintf(&b, "      - {kind: piper_onnx, name: %s.onnx, url: \"https://example.test/%s.onnx\", size_bytes: %d, sha256: \"%s\"}\n", id, id, size-1000, fakeDigest(id+"a"))
			fmt.Fprintf(&b, "      - {kind: piper_config, name: %s.onnx.json, url: \"https://example.test/%s.onnx.json\", size_bytes: 1000, sha256: \"%s\"}\n", id, id, fakeDigest(id+"b"))
			fmt.Fprintf(&b, "    requirements: {backends: [cuda, vulkan, cpu], min_vram_mib: 0, min_ram_mib: 512}\n")
		default:
			fmt.Fprintf(&b, "    engine: llama\n    roles: [chat]\n    languages: [en]\n")
			fmt.Fprintf(&b, "    source: {type: huggingface, repo: org-alpha/model-gguf, revision: 0123456789abcdef0123456789abcdef01234567}\n")
			fmt.Fprintf(&b, "    files:\n      - {kind: weights, name: %s.gguf, size_bytes: %d, sha256: \"%s\"}\n", id, size, fakeDigest(id))
			fmt.Fprintf(&b, "    requirements: {backends: [cuda, vulkan, cpu], min_vram_mib: 4000, min_ram_mib: 4096}\n")
			fmt.Fprintf(&b, "    llama: {ctx: 8192, n_gpu_layers: 99, flash_attn: \"on\", parallel: 1}\n")
		}
	}
	return b.String()
}

// loadReal stages and loads the real catalog; it fails the test with every lint problem.
func loadReal(t *testing.T) *catalog.Catalog {
	t.Helper()
	dir, _ := stagedCatalog(t)
	rep := catalog.Lint(dir, catalog.LintOptions{})
	if !rep.Valid() {
		t.Fatalf("the catalog does not lint:\n%s", strings.Join(rep.FormatLines("catalog"), "\n"))
	}
	cat, err := catalog.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	return cat
}

// resolveFromCatalog gives every desired item the answer a resolver would: one version, the catalog size.
func resolveFromCatalog(cat *catalog.Catalog, models, content []string) map[string]plan.Resolved {
	res := map[string]plan.Resolved{}
	for _, id := range models {
		m, _ := cat.Model(id)
		res[id] = plan.Resolved{Version: "v1", SizeBytes: catalog.ModelSizeBytes(m)}
	}
	for _, id := range content {
		c, _ := cat.ContentItem(id)
		res[id] = plan.Resolved{Version: "v1", SizeBytes: c.ExpectedSizeBytes}
	}
	return res
}

func internalPlan(cat *catalog.Catalog, p *catalog.Profile) plan.RootPlan {
	total := p.Storage.BudgetBytes + p.Storage.MinFreeBytes + 100_000_000_000
	return plan.Plan(plan.PlanInput{
		Root: plan.RootInfo{ID: ark.InternalID, Kind: ark.KindInternal, Path: "/srv/localai", Profile: p.ID,
			TotalBytes: total, FreeBytes: total},
		Catalog:  cat,
		Profile:  p,
		Present:  map[string]manifest.Manifest{},
		Partial:  map[string]plan.PartState{},
		Resolved: resolveFromCatalog(cat, p.Include.Models, p.Include.Content),
		Mode:     plan.ModeOnline,
		Now:      now,
	})
}

func drivePlan(cat *catalog.Catalog, p *catalog.Profile, driveBytes int64) plan.RootPlan {
	return plan.Plan(plan.PlanInput{
		Root: plan.RootInfo{ID: "ark-3f9c0a12", Kind: ark.KindArkDrive, Path: "/media/AI-ARK", Profile: p.ID,
			TotalBytes: driveBytes, FreeBytes: driveBytes},
		Catalog:  cat,
		Profile:  p,
		Present:  map[string]manifest.Manifest{},
		Partial:  map[string]plan.PartState{},
		Resolved: resolveFromCatalog(cat, p.Include.Models, p.Include.Content),
		Mode:     plan.ModeOnline,
		Now:      now,
	})
}

var profileIDs = []string{"small-8gb", "cuda-12gb", "max-128gb", "cpu-only", "ark-drive-full"}

func TestRealCatalogLints(t *testing.T) {
	cat := loadReal(t)
	if got := len(cat.Profiles); got != len(profileIDs) {
		t.Errorf("profiles = %d, want %d", got, len(profileIDs))
	}
	for _, id := range profileIDs {
		if _, ok := cat.Profile(id); !ok {
			t.Errorf("profile %s is missing", id)
		}
	}
	if !catalog.ValidVersion(cat.Version) {
		t.Errorf("CATALOG_VERSION %q is not YYYY.MM.DD-N", cat.Version)
	}
}

// Every profile plans inside its budget: nothing is skipped, nothing is over budget, and the plan
// keeps 10 % of headroom so a Kiwix book that grows between releases does not push a survival item out.
func TestRealCatalogEveryProfilePlansWithinBudget(t *testing.T) {
	cat := loadReal(t)
	for _, id := range profileIDs {
		t.Run(id, func(t *testing.T) {
			p, _ := cat.Profile(id)

			// The catalog's own estimate (catalog.md section 4): nothing dropped.
			eff, err := cat.EffectivePlan(id, catalog.Overrides{})
			if err != nil {
				t.Fatal(err)
			}
			if len(eff.Skipped) != 0 {
				t.Errorf("EffectivePlan skips %+v", eff.Skipped)
			}
			if eff.PlannedBytes > eff.BudgetBytes*9/10 {
				t.Errorf("EffectivePlan plans %d bytes of a %d budget: less than 10%% headroom", eff.PlannedBytes, eff.BudgetBytes)
			}
			if len(eff.Content) != len(p.Include.Content) || len(eff.Models) != len(p.Include.Models) {
				t.Errorf("EffectivePlan keeps %d models and %d content items, profile lists %d and %d",
					len(eff.Models), len(eff.Content), len(p.Include.Models), len(p.Include.Content))
			}

			// The sync planner (ark ark-sync.md section 4.4) on an empty root.
			var rp plan.RootPlan
			if id == catalog.ArkDriveProfileID {
				rp = drivePlan(cat, p, p.Storage.BudgetBytes+p.Storage.MinFreeBytes)
			} else {
				rp = internalPlan(cat, p)
			}
			want := len(p.Include.Models) + len(p.Include.Content)
			if len(rp.Items) != want {
				t.Errorf("planner rows = %d, want %d (the profile's include lists)", len(rp.Items), want)
			}
			for _, it := range rp.Items {
				if it.Action != plan.ActionDownload {
					t.Errorf("%s: action %s (reason %q), want download", it.ID, it.Action, it.Reason)
				}
			}
			for _, code := range []string{doctor.SYN040, doctor.SYN041, doctor.SYN042} {
				if rp.HasFinding(code) {
					t.Errorf("planner raises %s", code)
				}
			}
			if rp.PlannedBytes > rp.BudgetBytes {
				t.Errorf("planned %d bytes over the %d budget", rp.PlannedBytes, rp.BudgetBytes)
			}
			t.Logf("%s: %d items, planned %.1f GB of %.1f GB", id, len(rp.Items), float64(rp.PlannedBytes)/1e9, float64(rp.BudgetBytes)/1e9)
		})
	}
}

// A 1 TB drive is the smallest the runbook recommends. The drive profile wants everything, so the planner
// drops from the end; tier 1 and every model must survive even a drive far too small for the rest.
func TestRealCatalogSmallDriveKeepsTierOne(t *testing.T) {
	cat := loadReal(t)
	p, _ := cat.Profile(catalog.ArkDriveProfileID)
	for _, size := range []int64{1_000_000_000_000, 400_000_000_000} {
		t.Run(fmt.Sprintf("%dGB", size/1_000_000_000), func(t *testing.T) {
			rp := drivePlan(cat, p, size)
			if rp.PlannedBytes > rp.BudgetBytes {
				t.Errorf("planned %d over budget %d", rp.PlannedBytes, rp.BudgetBytes)
			}
			var skipped []string
			for _, it := range rp.Items {
				if it.Action != plan.ActionSkip {
					continue
				}
				skipped = append(skipped, it.ID)
				if it.Reason != plan.ReasonBudget {
					t.Errorf("%s skipped for %q, want budget", it.ID, it.Reason)
				}
				if c, ok := cat.ContentItem(it.ID); !ok || c.Tier() < 2 || c.Kind == "kit" || c.Kind == "docs" {
					t.Errorf("%s (kind %s) was dropped for budget, but only tier 2 and 3 content may be", it.ID, it.Kind)
				}
			}
			t.Logf("%d GB drive skips %d items: %s", size/1_000_000_000, len(skipped), strings.Join(skipped, " "))
		})
	}
}

// ark-drive-full is the union of every profile and holds every tier 1 to 3 item (catalog.md section 4).
func TestRealCatalogDriveProfileHoldsEverything(t *testing.T) {
	cat := loadReal(t)
	drive, _ := cat.Profile(catalog.ArkDriveProfileID)
	inDriveContent := toSet(drive.Include.Content)
	inDriveModels := toSet(drive.Include.Models)
	for _, c := range cat.Content {
		if !inDriveContent[c.ID] {
			t.Errorf("ark-drive-full does not include content %s", c.ID)
		}
	}
	for _, m := range cat.Models {
		if !inDriveModels[m.ID] {
			t.Errorf("ark-drive-full does not include model %s", m.ID)
		}
	}
	for _, p := range cat.Profiles {
		for _, id := range p.Include.Content {
			if !inDriveContent[id] {
				t.Errorf("%s includes %s, ark-drive-full does not", p.ID, id)
			}
		}
		for _, id := range p.Include.Models {
			if !inDriveModels[id] {
				t.Errorf("%s includes model %s, ark-drive-full does not", p.ID, id)
			}
		}
	}
}

// Within a profile the content list is ordered by tier (the planner drops later positions first), and
// each Wikipedia edition lands where catalog.md section 3.1 puts it.
func TestRealCatalogProfileOrderAndEditions(t *testing.T) {
	cat := loadReal(t)
	for _, p := range cat.Profiles {
		last := 0
		for _, id := range p.Include.Content {
			c, _ := cat.ContentItem(id)
			if c.Tier() < last {
				t.Errorf("%s lists %s (tier %d) after a tier %d item: order the list by tier", p.ID, id, c.Tier(), last)
			}
			if c.Tier() > last {
				last = c.Tier()
			}
		}
	}
	has := func(profile, id string) bool {
		p, _ := cat.Profile(profile)
		return toSet(p.Include.Content)[id]
	}
	for profile, want := range map[string]struct{ nopic, maxi bool }{
		"small-8gb": {true, false}, "cpu-only": {true, false},
		"cuda-12gb": {false, true}, "max-128gb": {false, true}, "ark-drive-full": {true, true},
	} {
		if got := has(profile, "wikipedia-en-nopic"); got != want.nopic {
			t.Errorf("%s: wikipedia-en-nopic included = %v, want %v", profile, got, want.nopic)
		}
		if got := has(profile, "wikipedia-en-maxi"); got != want.maxi {
			t.Errorf("%s: wikipedia-en-maxi included = %v, want %v", profile, got, want.maxi)
		}
	}
	// The tier 1 items every profile carries (catalog.md section 3.1).
	for _, p := range cat.Profiles {
		for _, id := range []string{"osm-central-america", "places-central-america", "docs-bundle", "wikimed-en",
			"survival-first-aid", "survival-water", "survival-food", "survival-post-disaster", "ifixit-en", "wikipedia-es-nopic"} {
			if !toSet(p.Include.Content)[id] {
				t.Errorf("%s does not include the tier 1 item %s", p.ID, id)
			}
		}
	}
	if has("max-128gb", "osm-north-america") || has("cuda-12gb", "osm-north-america") {
		t.Error("osm-north-america is for the ark drive only")
	}
	if !has("ark-drive-full", "osm-north-america") {
		t.Error("ark-drive-full must include osm-north-america")
	}
}

// The first-aid links of the Survival Portal (survival-portal.md section 3.4): all sixteen keys, each
// with an English article, a Spanish item and article, a unique key and a unique order.
func TestRealCatalogFirstAidLinks(t *testing.T) {
	cat := loadReal(t)
	wantKeys := []string{"cpr", "choking", "bleeding", "shock", "burns", "fracture", "head_injury", "allergic_reaction",
		"snake_bite", "dehydration", "heat_stroke", "hypothermia", "wound_infection", "childbirth", "water_purification", "food_safety"}
	got := map[string]catalog.PortalLink{}
	orders := map[int]string{}
	for _, c := range cat.Content {
		for _, l := range c.PortalLinks {
			if _, dup := got[l.Key]; dup {
				t.Errorf("duplicate portal key %s", l.Key)
			}
			got[l.Key] = l
			if prev, dup := orders[l.Order]; dup {
				t.Errorf("portal keys %s and %s share order %d", prev, l.Key, l.Order)
			}
			orders[l.Order] = l.Key
			if l.ESItem != "wikimed-es" {
				t.Errorf("%s: es_item = %q, want wikimed-es", l.Key, l.ESItem)
			}
			if l.Title.EN == "" || l.Title.ES == "" || l.Article == "" || l.ESArticle == "" {
				t.Errorf("%s: incomplete link %+v", l.Key, l)
			}
		}
	}
	for _, k := range wantKeys {
		if _, ok := got[k]; !ok {
			t.Errorf("first-aid key %s has no portal link", k)
		}
	}
	if len(got) != len(wantKeys) {
		t.Errorf("portal links = %d, want %d", len(got), len(wantKeys))
	}
	if wm, _ := cat.ContentItem("wikimed-en"); wm == nil || len(wm.PortalLinks) != 15 {
		t.Errorf("wikimed-en carries the first-aid links of the medical book")
	}
	if sw, _ := cat.ContentItem("survival-water"); sw == nil || len(sw.PortalLinks) != 1 || sw.PortalLinks[0].Key != "water_purification" {
		t.Errorf("survival-water carries water_purification")
	}
}

var kiwixOrigin = regexp.MustCompile(`^https://lb\.download\.kiwix\.org/zim/[a-z_]+/$`)
var bookName = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// Every Kiwix source follows the newest file with the origin's sidecar checksum and stores no digest.
func TestRealCatalogKiwixSources(t *testing.T) {
	cat := loadReal(t)
	books := map[string]string{}
	n := 0
	for _, c := range cat.Content {
		if c.Kind != "kiwix" {
			continue
		}
		n++
		s := c.Source
		if s.VersionPolicy != "latest" || s.Checksum != "sidecar" || s.Pinned != nil {
			t.Errorf("%s: want version_policy latest, checksum sidecar and no pinned block, got %+v", c.ID, s)
		}
		if !kiwixOrigin.MatchString(s.BaseURL) {
			t.Errorf("%s: base_url %q is not a directory of the Kiwix download origin", c.ID, s.BaseURL)
		}
		if !bookName.MatchString(s.Book) || strings.HasSuffix(s.Book, ".zim") || regexp.MustCompile(`_\d{4}-\d{2}$`).MatchString(s.Book) {
			t.Errorf("%s: book %q must be the name without a date", c.ID, s.Book)
		}
		if prev, dup := books[s.BaseURL+s.Book]; dup {
			t.Errorf("%s and %s are the same book", c.ID, prev)
		}
		books[s.BaseURL+s.Book] = c.ID
		if c.ExpectedSizeBytes <= 0 || c.RefreshDays < catalog.KiwixMinRefreshDays {
			t.Errorf("%s: expected_size_bytes %d, refresh_days %d", c.ID, c.ExpectedSizeBytes, c.RefreshDays)
		}
		if c.Summaries["summary_es"] == "" {
			t.Errorf("%s has no Spanish summary", c.ID)
		}
	}
	if n < 40 {
		t.Errorf("only %d Kiwix items", n)
	}
}

// Maps, places and docs: the sources that cannot carry a pinned digest use the checksum modes the spec
// allows (computed with trust on first use), so no 64-hex string is committed.
func TestRealCatalogMapsPlacesDocs(t *testing.T) {
	cat := loadReal(t)
	ca, _ := cat.ContentItem("osm-central-america")
	if ca == nil || ca.Tier() != 1 || ca.Source.Type != "pmtiles_extract" || ca.Source.Checksum != "computed" {
		t.Errorf("osm-central-america = %+v", ca)
	}
	na, _ := cat.ContentItem("osm-north-america")
	if na == nil || na.Tier() != 3 || na.Source.Type != "pmtiles_extract" {
		t.Errorf("osm-north-america = %+v", na)
	}
	pl, _ := cat.ContentItem("places-central-america")
	if pl == nil || pl.Tier() != 1 || pl.Source.Trust != "tofu" || pl.Source.Checksum != "computed" || len(pl.Source.Files) != 7 {
		t.Fatalf("places-central-america = %+v", pl)
	}
	for _, f := range pl.Source.Files {
		if f.SHA256 != "" {
			t.Errorf("places file %s carries a digest %q: computed+tofu items store none", f.URL, f.SHA256)
		}
	}
	d, _ := cat.ContentItem("docs-bundle")
	if d == nil || d.Kind != "docs" || d.Source.Type != "repo_docs" || d.Tier() != 1 {
		t.Errorf("docs-bundle = %+v", d)
	}
}

func toSet(ids []string) map[string]bool {
	m := make(map[string]bool, len(ids))
	for _, id := range ids {
		m[id] = true
	}
	return m
}
