package ready_test

import (
	"reflect"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/ark/ready"
	"github.com/marinoscar/local-ai/agent/internal/catalog"
)

// survivalWorld is a node with everything survival needs in its profile: two survival models, a
// whisper model, an English and a Spanish voice, a tier-1 library, a tier-1 map and the map assets.
// Every item is present and verified unless a case removes it.
func survivalWorld() *world {
	w := newWorld().withModels(
		model("brain", 1, 40), model("eyes", 1, 40), model("extra", 3, 40),
		whisper("whisper-small", 10), piper("piper-en", "en", 5), piper("piper-es", "es-MX", 5),
	).withContent(
		kiwix("wiki", 1, 20), kiwix("wiki-es", 2, 20), mapOf("map-a", 1, 20), mapAssets(5), mapOf("map-b", 3, 20),
	)
	w.survive = []string{"brain", "eyes"}
	for _, id := range []string{"brain", "eyes", "extra", "whisper-small", "piper-en", "piper-es", "wiki", "wiki-es", "map-a", "map-assets", "map-b"} {
		w.have(id, "v1", day)
	}
	w.synced(day)
	return w
}

// without removes a verified copy.
func (w *world) without(ids ...string) *world {
	for _, id := range ids {
		delete(w.present, id)
	}
	return w
}

func TestSurvivalCapableRoot(t *testing.T) {
	tests := []struct {
		name  string
		setup func(w *world)
		want  bool
		// missing is what the explanation lists.
		missing []string
	}{
		{"everything is verified", func(w *world) {}, true, nil},
		{"a survival model is missing", func(w *world) { w.without("eyes") }, false, []string{"model:eyes"}},
		{"a survival model is stale", func(w *world) { w.have("eyes", "v0", day).target("eyes", "v1", 40) }, false, []string{"model:eyes"}},
		{"a survival model failed", func(w *world) { w.without("brain").failedItem("brain", "failed", "v1", "LAI-SYN-031") }, false, []string{"model:brain"}},
		{"a model that is not a survival model does not matter", func(w *world) { w.without("extra") }, true, nil},
		{"no whisper model", func(w *world) { w.without("whisper-small") }, false, []string{"whisper"}},
		{"no English voice", func(w *world) { w.without("piper-en") }, false, []string{"piper:en"}},
		{"no Spanish voice", func(w *world) { w.without("piper-es") }, false, []string{"piper:es"}},
		{"no tier-1 library (a tier-2 library does not count)", func(w *world) { w.without("wiki") }, false, []string{"kiwix"}},
		{"no tier-1 map (a tier-3 map does not count)", func(w *world) { w.without("map-a") }, false, []string{"map"}},
		{"no map assets", func(w *world) { w.without("map-assets") }, false, []string{"map-assets"}},
		{"everything but the models is missing", func(w *world) {
			w.without("whisper-small", "piper-en", "piper-es", "wiki", "map-a", "map-assets")
		}, false, []string{"whisper", "piper:en", "piper:es", "kiwix", "map", "map-assets"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := survivalWorld()
			tt.setup(w)
			in := w.input()
			r := ready.Build(in)
			if r.SurvivalCapable != tt.want {
				t.Errorf("survival_capable = %v, want %v", r.SurvivalCapable, tt.want)
			}
			needs := ready.NeedsFor(in.Catalog, in.Profile, nil)
			if got := needs.Missing(ready.Verified(r)); !reflect.DeepEqual(got, tt.missing) && !(len(got) == 0 && len(tt.missing) == 0) {
				t.Errorf("missing = %v, want %v", got, tt.missing)
			}
		})
	}
}

// TestSurvivalLoadable: a survival model that cannot load on this node is not required, but only
// when the caller says so; with no answer every survival model is required.
func TestSurvivalLoadable(t *testing.T) {
	w := survivalWorld().without("eyes")
	in := w.input()
	if ready.Build(in).SurvivalCapable {
		t.Fatal("with no loadable function, the missing model is required")
	}
	in.Loadable = func(id string) bool { return id != "eyes" }
	if !ready.Build(in).SurvivalCapable {
		t.Error("eyes cannot load on this node, so it is not required")
	}
	in.Loadable = func(id string) bool { return true }
	if ready.Build(in).SurvivalCapable {
		t.Error("eyes can load, so it is required")
	}
}

// TestSurvivalLanguages: voices are matched by language ignoring case and region; a "multi" voice
// counts for none; a catalog with no Spanish voice can never be capable.
func TestSurvivalLanguages(t *testing.T) {
	w := survivalWorld()
	// Replace the Spanish voice by one tagged "multi".
	for i := range w.models {
		if w.models[i].ID == "piper-es" {
			w.models[i].Languages = []string{"multi"}
		}
	}
	if w.build().SurvivalCapable {
		t.Error("a voice that is only 'multi' is not a Spanish voice")
	}
	for i := range w.models {
		if w.models[i].ID == "piper-es" {
			w.models[i].Languages = []string{"ES_mx"}
		}
	}
	if !w.build().SurvivalCapable {
		t.Error("'ES_mx' is Spanish")
	}
	// No Spanish voice in the catalog at all.
	w2 := survivalWorld()
	var kept []catalog.Model
	for _, m := range w2.models {
		if m.ID != "piper-es" {
			kept = append(kept, m)
		}
	}
	w2.models = kept
	delete(w2.present, "piper-es")
	if w2.build().SurvivalCapable {
		t.Error("with no Spanish voice in the catalog the requirement cannot be met")
	}
	// No map assets in the catalog.
	w3 := survivalWorld()
	var content []catalog.ContentItem
	for _, c := range w3.content {
		if c.ID != "map-assets" {
			content = append(content, c)
		}
	}
	w3.content = content
	if w3.build().SurvivalCapable {
		t.Error("with no map-assets in the catalog the requirement cannot be met")
	}
}

func TestNeedsForNil(t *testing.T) {
	n := ready.NeedsFor(nil, nil, nil)
	if n.Met(map[string]bool{}) {
		t.Error("requirements built from nothing must not be met")
	}
	if !reflect.DeepEqual(n.Missing(nil), []string{"whisper", "piper:en", "piper:es", "kiwix", "map", "map-assets"}) {
		t.Errorf("missing = %v", n.Missing(nil))
	}
}

// TestNodeSurvivalCapable: the node-level answer is over every mounted root together: models on the
// internal disk and the maps on a drive make the node capable although neither root alone is.
func TestNodeSurvivalCapable(t *testing.T) {
	internal := survivalWorld().without("wiki", "map-a", "map-assets")
	drive := survivalWorld().drive().without("brain", "eyes", "extra", "whisper-small", "piper-en", "piper-es")

	in := internal.input()
	ri := ready.Build(in)
	rd := ready.Build(drive.input())
	if ri.SurvivalCapable || rd.SurvivalCapable {
		t.Fatalf("neither root alone is capable: internal %v drive %v", ri.SurvivalCapable, rd.SurvivalCapable)
	}
	needs := ready.NeedsFor(in.Catalog, in.Profile, nil)
	if !ready.NodeSurvivalCapable(needs, []ready.Report{ri, rd}) {
		t.Error("the two roots together are capable")
	}
	if ready.NodeSurvivalCapable(needs, []ready.Report{ri}) || ready.NodeSurvivalCapable(needs, []ready.Report{rd}) {
		t.Error("one of them alone is not")
	}
	if ready.NodeSurvivalCapable(needs, nil) {
		t.Error("no report is not capable")
	}

	// A stale copy on the drive does not complete the node.
	drive2 := survivalWorld().drive().without("brain", "eyes", "extra", "whisper-small", "piper-en", "piper-es")
	for i := range drive2.content {
		if drive2.content[i].ID == "wiki" {
			// A pinned library: a change of the catalog is applied at once, so the old copy is stale.
			drive2.content[i].Source.VersionPolicy = "pinned"
		}
	}
	drive2.have("wiki", "v0", day).target("wiki", "v1", 20)
	if ready.NodeSurvivalCapable(needs, []ready.Report{ri, ready.Build(drive2.input())}) {
		t.Error("a stale copy is not verified")
	}
}

func TestVerifiedSet(t *testing.T) {
	r := ready.Report{Items: []ready.Item{
		{ID: "a", Status: ready.StatusVerified}, {ID: "b", Status: ready.StatusStale}, {ID: "c", Status: ready.StatusOrphan},
	}}
	if got := ready.Verified(r); !reflect.DeepEqual(got, map[string]bool{"a": true}) {
		t.Errorf("verified = %v", got)
	}
}
