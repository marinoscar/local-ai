package statusapi_test

import (
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/catalog"
	"github.com/marinoscar/local-ai/agent/internal/router"
	"github.com/marinoscar/local-ai/agent/internal/statusapi"
)

// TestEngineModelStatuses: an acestep (music) model in GET /models takes its status from the engine's
// cached picture (E11), not from the router.
func TestEngineModelStatuses(t *testing.T) {
	const music = "music-alpha"
	cases := []struct {
		name    string
		present bool
		held    []router.EngineModel
		want    string
	}{
		{"no copy", false, nil, "absent"},
		{"copy, engine stopped", true, nil, "unloaded"},
		{"starting", true, []router.EngineModel{{ID: music}}, "loading"},
		{"ready", true, []router.EngineModel{{ID: music, Ready: true}}, "loaded"},
		{"another model held", true, []router.EngineModel{{ID: "music-beta", Ready: true}}, "unloaded"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t, nil)
			r.cat.Models = append(r.cat.Models, catalog.Model{ID: music, DisplayName: "Music", Engine: catalog.EngineAcestep,
				Roles: []string{"music"}, Requirements: catalog.Requirements{Backends: []string{"cuda"}, MinVRAMMiB: 3000}})
			if c.present {
				r.pres["/srv/localai|"+music] = true
			}
			m := r.deps.Models.(*statusapi.ModelMerger)
			m.EngineModels = func() []router.EngineModel { return c.held }
			row := rowOf(t, m.Models("en").Rows, music)
			if row.Status != c.want || row.Loaded != (c.want == "loaded") {
				t.Errorf("status %q loaded %v, want %q", row.Status, row.Loaded, c.want)
			}
		})
	}
}
