package plan_test

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/ark"
	"github.com/marinoscar/local-ai/agent/internal/catalog"
	"github.com/marinoscar/local-ai/agent/internal/sync/manifest"
	"github.com/marinoscar/local-ai/agent/internal/sync/plan"
)

var update = flag.Bool("update", false, "rewrite the golden files of testdata/golden")

const gb = int64(1_000_000_000)

// goldenScenario returns the plans of one named scenario. The numbers are real gigabytes so the
// rendered text looks like a plan a person reads. Versions are short invented strings (no hash-like
// literals anywhere in the fixtures).
func goldenScenario(name string) []plan.RootPlan {
	switch name {
	case "internal-online":
		return []plan.RootPlan{plan.Plan(internalOnline())}
	case "offline-survival":
		in := internalOnline()
		in.Mode, in.Offline = plan.ModeSurvival, true
		in.Resolved = map[string]plan.Resolved{}
		for id, r := range internalOnline().Resolved {
			if id != "wiki-alpha-en" {
				in.Resolved[id] = r
			}
		}
		return []plan.RootPlan{plan.Plan(in)}
	case "two-roots":
		a, b := internalOnline(), driveInput()
		return plan.PlanAll([]plan.PlanInput{a, b})
	case "read-only-drive":
		in := driveInput()
		in.Root.ReadOnly = true
		return []plan.RootPlan{plan.Plan(in)}
	}
	panic("unknown golden scenario " + name)
}

func goldenCatalog() (models []catalog.Model, content []catalog.ContentItem) {
	models = []catalog.Model{model("model-alpha-9b", 1, 6*gb), model("model-beta-vision", 1, 5*gb), model("model-gamma-tiny", 2, 1*gb)}
	models[0].Source.Revision, models[1].Source.Revision, models[2].Source.Revision = "rev-alpha-0001", "rev-beta-0002", "rev-gamma-0003"
	content = []catalog.ContentItem{
		kit("kit-llama-cpp", gb/2),
		debs("resolute", "amd64", 3*gb),
		docs(gb / 10),
		zim("wiki-alpha-en", 49*gb, tier(1), refresh(90)),
		zim("stackexchange-alpha", 4*gb, tier(2), refresh(30)),
		zim("books-alpha-en", 30*gb, tier(3), refresh(180)),
		zim("maps-alpha", 12*gb, tier(3), refresh(90)),
	}
	return models, content
}

func internalOnline() plan.PlanInput {
	models, content := goldenCatalog()
	s := newScenario(models, content)
	s.survive = []string{"model-alpha-9b"}
	s.in.Profile.ID = "small-8gb"
	s.in.Root = plan.RootInfo{ID: ark.InternalID, Kind: ark.KindInternal, Path: "/srv/localai", Profile: "small-8gb", TotalBytes: 500 * gb, FreeBytes: 290 * gb}
	s.in.Profile.Storage = catalog.Storage{Target: "internal", BudgetBytes: 80 * gb, MinFreeBytes: 25 * gb}
	s.in.Node = plan.NodeInfo{Backend: "cuda", Arch: "amd64", Codename: "resolute"}
	s.in.Mode = plan.ModeOnline

	// What the sources offer. books-alpha-en has no answer: its resolver failed.
	s.resolve("model-alpha-9b", "rev-alpha-0001", 6*gb)
	s.resolve("model-beta-vision", "rev-beta-0002", 5*gb)
	s.resolve("model-gamma-tiny", "rev-gamma-0003", 1*gb)
	s.resolve("kit-llama-cpp", "tag-kit-llama-cpp", gb/2)
	s.resolve("kit-debs-resolute-amd64", "closure-b", 3*gb+100*1_000_000)
	s.resolve("docs-bundle", "catalog-"+catVersion, gb/10)
	s.resolve("wiki-alpha-en", "2026-10", 49*gb)
	s.resolve("stackexchange-alpha", "2026-09", 4*gb)
	s.resolve("maps-alpha", "20261001", 12*gb)

	// What is on disk.
	s.have("model-alpha-9b", manifest.KindModel, "rev-alpha-0001", 6*gb, 40*days)
	s.have("kit-llama-cpp", manifest.KindKit, "tag-kit-llama-cpp", gb/2, 40*days)
	s.have("kit-debs-resolute-amd64", manifest.KindKit, "closure-a", 3*gb, 31*days)
	s.have("wiki-alpha-en", manifest.KindKiwix, "2026-07", 48*gb, 100*days)
	s.have("osm-north-america", manifest.KindMaps, "20260501", 11*gb+200*1_000_000, 160*days)
	s.in.Partial["model-beta-vision"] = plan.PartState{Version: "rev-beta-0002", PartBytes: 2 * gb}
	s.in.Trash = []plan.TrashEntry{{ItemID: "wiki-alpha-en", Version: "2026-04", Bytes: 47 * gb, Eligible: true, TrashedAt: now.Add(-20 * days)}}
	s.in.OtherRoots = map[string][]plan.RootCopy{
		"model-gamma-tiny": {{RootID: "ark-3f9c0a12", Manifest: manifest.Manifest{ItemID: "model-gamma-tiny", Version: "rev-gamma-0003", TotalBytes: gb}}},
	}
	return s.build()
}

func driveInput() plan.PlanInput {
	models, content := goldenCatalog()
	s := newScenario(models, content)
	s.survive = []string{"model-alpha-9b"}
	s.in.Profile.ID = "ark-drive-full"
	s.in.Root = plan.RootInfo{ID: "ark-3f9c0a12", Kind: ark.KindArkDrive, Path: "/media/operator/AI-ARK/localai", Profile: "ark-drive-full", TotalBytes: 100 * gb, FreeBytes: 60 * gb}
	s.in.Node = plan.NodeInfo{Backend: "cuda", Arch: "amd64", Codename: "resolute"}
	s.in.Mode = plan.ModeOnline
	s.resolve("model-alpha-9b", "rev-alpha-0001", 6*gb)
	s.resolve("model-beta-vision", "rev-beta-0002", 5*gb)
	s.resolve("model-gamma-tiny", "rev-gamma-0003", 1*gb)
	s.resolve("kit-llama-cpp", "tag-kit-llama-cpp", gb/2)
	s.resolve("kit-debs-resolute-amd64", "closure-b", 3*gb+100*1_000_000)
	s.resolve("docs-bundle", "catalog-"+catVersion, gb/10)
	s.resolve("wiki-alpha-en", "2026-10", 49*gb)
	s.resolve("stackexchange-alpha", "2026-09", 4*gb)
	s.resolve("books-alpha-en", "2026-08", 30*gb)
	s.resolve("maps-alpha", "20261001", 12*gb)
	s.have("model-alpha-9b", manifest.KindModel, "rev-alpha-0001", 6*gb, 10*days)
	return s.build()
}

func readGolden(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "golden", name))
	if err != nil {
		t.Fatalf("%v (run: go test ./internal/sync/plan -update)", err)
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

func TestGolden(t *testing.T) {
	for _, name := range []string{"internal-online", "offline-survival", "two-roots", "read-only-drive"} {
		t.Run(name, func(t *testing.T) {
			plans := goldenScenario(name)
			files := map[string][]byte{
				name + ".en.txt": []byte(plan.RenderAll(plans, "en")),
				name + ".es.txt": []byte(plan.RenderAll(plans, "es")),
			}
			js, err := json.MarshalIndent(plans, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			files[name+".json"] = append(js, '\n')
			for file, got := range files {
				if *update {
					writeGolden(t, file, got)
					continue
				}
				if want := readGolden(t, file); string(want) != string(got) {
					t.Errorf("%s differs from the golden file.\n--- want ---\n%s\n--- got ---\n%s", file, want, got)
				}
			}
		})
	}
}

// TestRenderFromJSON: the human text is a function of the plan's JSON alone, which is what the CLI
// relies on when it formats the server's answer.
func TestRenderFromJSON(t *testing.T) {
	for _, name := range []string{"internal-online", "offline-survival", "two-roots", "read-only-drive"} {
		t.Run(name, func(t *testing.T) {
			var plans []plan.RootPlan
			if err := json.Unmarshal(readGolden(t, name+".json"), &plans); err != nil {
				t.Fatal(err)
			}
			for _, lang := range []string{"en", "es"} {
				if got, want := plan.RenderAll(plans, lang), string(readGolden(t, name+"."+lang+".txt")); got != want {
					t.Errorf("%s: text rendered from the JSON differs from the golden file:\n%s", lang, got)
				}
			}
			// And it re-encodes to the same bytes: nothing is lost on the way.
			again, _ := json.MarshalIndent(plans, "", "  ")
			if string(again)+"\n" != string(readGolden(t, name+".json")) {
				t.Error("the JSON does not round-trip")
			}
		})
	}
}

func TestTierJSON(t *testing.T) {
	for _, tc := range []struct {
		in   plan.Tier
		json string
	}{{plan.Tier{Pinned: true}, `"k"`}, {plan.Tier{N: 2}, `2`}} {
		b, _ := json.Marshal(tc.in)
		if string(b) != tc.json {
			t.Errorf("marshal %+v = %s", tc.in, b)
		}
		var back plan.Tier
		if err := json.Unmarshal(b, &back); err != nil || back != tc.in {
			t.Errorf("round trip %s = %+v, %v", b, back, err)
		}
	}
	var bad plan.Tier
	if json.Unmarshal([]byte(`"x"`), &bad) == nil || json.Unmarshal([]byte(`true`), &bad) == nil {
		t.Error("a tier that is neither \"k\" nor a number must be refused")
	}
}

func TestFormatGB(t *testing.T) {
	for _, tc := range []struct {
		in   int64
		want string
	}{{0, "0.0 GB"}, {-5, "0.0 GB"}, {49_999_999, "0.0 GB"}, {50_000_000, "0.1 GB"}, {400_000_000, "0.4 GB"}, {48_700_000_000, "48.7 GB"},
		{48_749_999_999, "48.7 GB"}, {48_750_000_000, "48.8 GB"}, {220_000_000_000, "220.0 GB"}, {2_000_398_934_016, "2000.4 GB"}} {
		if got := plan.FormatGB(tc.in); got != tc.want {
			t.Errorf("FormatGB(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
