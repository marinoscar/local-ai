package ready_test

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	apispec "github.com/marinoscar/local-ai/agent/api"
	"github.com/marinoscar/local-ai/agent/internal/ark"
	"github.com/marinoscar/local-ai/agent/internal/ark/ready"
	"github.com/marinoscar/local-ai/agent/internal/catalog"
	"github.com/marinoscar/local-ai/agent/internal/cloud"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/sync/journal"
)

// The golden reports are three whole READY.json files, in English (the file on disk and what the cloud
// receives) and in Spanish (what GET /ark answers to an Accept-Language of es). Every time in them is
// an offset from the one clock of the tests, so they never move.

// goldenCatalog is the profile of a small laptop: a survival model, speech in two languages, the
// libraries, a map with its assets, the kit and the manuals. Sizes are real-looking gigabytes.
func goldenWorld() *world {
	w := newWorld().withModels(
		sized(model("model-alpha-9b-q4", 1, 0), 5_870_000_000),
		sized(whisper("whisper-small", 0), 487_000_000),
		sized(piper("piper-en-medium", "en", 0), 63_000_000),
		sized(piper("piper-es-medium", "es", 0), 63_000_000),
	).withContent(
		kiwix("wikipedia-en-nopic", 1, 47_800_000_000),
		kiwix("medical-alpha-en", 1, 2_000_000_000),
		kiwix("stackexchange-electronics", 2, 6_000_000_000),
		mapOf("map-region-alpha", 1, 2_500_000_000),
		mapAssets(50_000_000),
		kit("kit-llama-cpp", 500_000_000),
		closure("resolute", "amd64", 3_100_000_000),
		docs(100_000_000),
	)
	w.survive = []string{"model-alpha-9b-q4"}
	w.profile = "small-8gb"
	w.budget, w.minFree, w.total, w.free = 220*gb, 25*gb, 502*gb, 105*gb
	return w
}

// sized sets the weights size of a model.
func sized(m catalog.Model, bytes int64) catalog.Model {
	m.Files[0].SizeBytes = bytes
	return m
}

// haveAll puts every desired item on the root, verified `age` ago.
func (w *world) haveAll(age time.Duration) *world {
	for _, m := range w.models {
		w.have(m.ID, "v1", age)
	}
	for _, c := range w.content {
		w.have(c.ID, "v1", age)
	}
	return w
}

func goldenScenario(name string) ready.Report {
	switch name {
	case "fresh-internal":
		// Everything verified recently, the run finished this morning, one orphan.
		w := goldenWorld().haveAll(3 * day)
		w.have("model-alpha-9b-q4", "v1", 11*time.Hour).checked("model-alpha-9b-q4", 11*time.Hour)
		w.have("wikipedia-en-nopic", "v1", 11*time.Hour).checked("wikipedia-en-nopic", 11*time.Hour)
		w.synced(11 * time.Hour)
		w.haveOrphan("stackexchange-diy", "kiwix", 40*day)
		return w.build()

	case "stale-drive":
		// A drive last synced 45 days ago: yellow. A library is a version behind after a failed update
		// (stale, so it does not count as verified), the other tier-1 library still carries survival,
		// and the tier-2 library never made it.
		w := goldenWorld().drive().haveAll(45 * day)
		w.profile = "ark-drive-full"
		delete(w.present, "stackexchange-electronics")
		w.have("wikipedia-en-nopic", "v0", 45*day).target("wikipedia-en-nopic", "v1", 47_800_000_000)
		for i := range w.content {
			if w.content[i].ID == "wikipedia-en-nopic" {
				w.content[i].Source.VersionPolicy = "pinned"
			}
		}
		w.failedItem("wikipedia-en-nopic", journal.StateFailed, "v1", doctor.SYN031)
		w.synced(45 * day)
		w.reverified("model-alpha-9b-q4", 20*day)
		w.total, w.free = 100*gb, 20*gb
		return w.build()

	case "unclean-drive":
		// A drive whose filesystem has errors: its copies are not offered until they are checked, so
		// most of the content is missing, and the drive is red whatever else is true.
		w := goldenWorld().drive().haveAll(2 * day)
		w.profile = "ark-drive-full"
		for _, id := range []string{"model-alpha-9b-q4", "wikipedia-en-nopic", "map-region-alpha", "map-assets", "docs-bundle", "stackexchange-electronics"} {
			delete(w.present, id)
		}
		w.synced(2 * day)
		w.total, w.free = 100*gb, 60*gb
		w.readOnly = true
		in := w.input()
		in.Root.State = ark.StateUnclean
		in.Docs = ready.DocsFacts{Known: true}
		return ready.Build(in)
	}
	panic("unknown scenario " + name)
}

var goldenNames = []string{"fresh-internal", "stale-drive", "unclean-drive"}

func marshal(t *testing.T, r ready.Report) []byte {
	t.Helper()
	b, err := r.MarshalIndented()
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestGolden(t *testing.T) {
	for _, name := range goldenNames {
		t.Run(name, func(t *testing.T) {
			r := goldenScenario(name)
			files := map[string][]byte{
				name + ".json":    marshal(t, r),
				name + ".es.json": marshal(t, r.In("es")),
			}
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

// TestGoldenColours pins what each scenario is about, so a rewrite of the golden files cannot hide a
// change of verdict.
func TestGoldenColours(t *testing.T) {
	tests := []struct {
		name     string
		overall  ready.Colour
		survival bool
		codes    []string
	}{
		{"fresh-internal", ready.Green, true, nil},
		{"stale-drive", ready.Yellow, true, []string{doctor.SYN031, doctor.ARK008}},
		{"unclean-drive", ready.Red, false, []string{doctor.ARK004}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := goldenScenario(tt.name)
			if r.Overall != tt.overall || r.SurvivalCapable != tt.survival {
				t.Errorf("overall %s survival %v, want %s %v", r.Overall, r.SurvivalCapable, tt.overall, tt.survival)
			}
			for _, c := range tt.codes {
				if !r.HasProblem(c) {
					t.Errorf("missing problem %s in %v", c, problemCodes(r))
				}
			}
		})
	}
}

// TestGoldenIsTheCloudSchema: stripped of root.path and generated_by.node_id, a report decodes with
// no unknown field into the cloud's mirror of ReadyReportV1 and survives the round trip.
func TestGoldenIsTheCloudSchema(t *testing.T) {
	for _, name := range goldenNames {
		t.Run(name, func(t *testing.T) {
			var doc map[string]any
			if err := json.Unmarshal(readGolden(t, name+".json"), &doc); err != nil {
				t.Fatal(err)
			}
			delete(doc["root"].(map[string]any), "path")
			delete(doc["generated_by"].(map[string]any), "node_id")
			stripped, _ := json.Marshal(doc)

			var rep cloud.ReadyReport
			dec := json.NewDecoder(bytes.NewReader(stripped))
			dec.DisallowUnknownFields()
			if err := dec.Decode(&rep); err != nil {
				t.Fatalf("the cloud mirror refuses the report: %v", err)
			}
			if rep.Overall == "" || rep.Root.ID == "" || rep.GeneratedBy.AgentVersion == "" || rep.Counts == nil {
				t.Errorf("decoded = %+v", rep)
			}
		})
	}
}

// TestGoldenIsTheOpenAPISchema: the reports validate against the ReadyReport schema of openapi.yaml.
func TestGoldenIsTheOpenAPISchema(t *testing.T) {
	spec, err := apispec.Default()
	if err != nil {
		t.Fatal(err)
	}
	sch, err := spec.Schema("ReadyReport")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range goldenNames {
		for _, suffix := range []string{".json", ".es.json"} {
			var doc any
			if err := json.Unmarshal(readGolden(t, name+suffix), &doc); err != nil {
				t.Fatal(err)
			}
			if err := spec.V.Validate(sch, doc); err != nil {
				t.Errorf("%s%s: %v", name, suffix, err)
			}
		}
	}
}

// TestGoldenSpanishTitles: Spanish changes only the titles of the problems.
func TestGoldenSpanishTitles(t *testing.T) {
	r := goldenScenario("stale-drive")
	es := r.In("es")
	if len(es.Problems) != len(r.Problems) || len(r.Problems) == 0 {
		t.Fatalf("problems %d vs %d", len(es.Problems), len(r.Problems))
	}
	for i := range r.Problems {
		if es.Problems[i].Code != r.Problems[i].Code || es.Problems[i].Item != r.Problems[i].Item || es.Problems[i].Severity != r.Problems[i].Severity {
			t.Errorf("problem %d changed more than its title: %+v vs %+v", i, es.Problems[i], r.Problems[i])
		}
		if es.Problems[i].Title == r.Problems[i].Title {
			t.Errorf("problem %d title not translated: %q", i, es.Problems[i].Title)
		}
	}
	es.Problems = r.Problems
	if !bytes.Equal(marshal(t, es), marshal(t, r)) {
		t.Error("everything but the titles must be identical")
	}
}

// TestMarshalIsStable: the same report gives the same bytes, with an empty list for no items or
// problems, and the golden files end in a newline.
func TestMarshalIsStable(t *testing.T) {
	r := goldenScenario("fresh-internal")
	if !bytes.Equal(marshal(t, r), marshal(t, r)) {
		t.Error("two marshals differ")
	}
	empty := ready.Report{SchemaVersion: 1}
	b := marshal(t, empty)
	if !bytes.Contains(b, []byte(`"items": []`)) || !bytes.Contains(b, []byte(`"problems": []`)) || !bytes.Contains(b, []byte(`"last_sync": null`)) {
		t.Errorf("empty report = %s", b)
	}
	if b[len(b)-1] != '\n' {
		t.Error("no trailing newline")
	}
}
