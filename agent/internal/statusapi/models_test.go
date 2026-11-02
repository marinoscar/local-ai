package statusapi_test

import (
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/modes"
	"github.com/marinoscar/local-ai/agent/internal/router"
	"github.com/marinoscar/local-ai/agent/internal/state"
	"github.com/marinoscar/local-ai/agent/internal/statusapi"
)

const alpha = "model-alpha-9b-q4"

// TestModelStatuses: the status of a row for each situation of agent.md section 3.8.7.
func TestModelStatuses(t *testing.T) {
	cases := []struct {
		name    string
		present bool
		ms      *router.ModelStatus
		want    string
	}{
		{"no copy", false, nil, "absent"},
		{"no copy, router lists it", false, &router.ModelStatus{ID: alpha, Status: router.StatusUnloaded}, "absent"},
		{"copy, not in the router", true, nil, "stored"},
		{"unloaded", true, &router.ModelStatus{ID: alpha, Status: router.StatusUnloaded}, "unloaded"},
		{"loading", true, &router.ModelStatus{ID: alpha, Status: router.StatusLoading}, "loading"},
		{"loaded", true, &router.ModelStatus{ID: alpha, Status: router.StatusLoaded}, "loaded"},
		{"sleeping", true, &router.ModelStatus{ID: alpha, Status: router.StatusSleeping}, "sleeping"},
		{"failed", true, &router.ModelStatus{ID: alpha, Status: router.StatusUnloaded, Failed: true, ExitCode: 1}, "failed"},
		{"router reports downloading", true, &router.ModelStatus{ID: alpha, Status: router.StatusDownloading}, "unloaded"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t, nil)
			if c.present {
				r.pres["/srv/localai|"+alpha] = true
			}
			v := router.View{At: t0}
			if c.ms != nil {
				v.Models = []router.ModelStatus{*c.ms}
			}
			r.rt.set(v)
			row := rowOf(t, r.deps.Models.Models("en").Rows, alpha)
			if row.Status != c.want || row.Loaded != (c.want == "loaded") {
				t.Errorf("status %q loaded %v, want %q", row.Status, row.Loaded, c.want)
			}
		})
	}
}

func TestModelsRouterDown(t *testing.T) {
	r := newRig(t, nil)
	r.pres["/srv/localai|"+alpha] = true
	r.pres["/srv/localai|whisper-small"] = true
	r.rt.set(router.View{Stale: true, Err: "router: connection refused"})
	rows := r.deps.Models.Models("en").Rows
	a := rowOf(t, rows, alpha)
	if a.Loadable || a.LoadableReason == nil || a.LoadableReason.Code != doctor.LLM004 || a.Status != "stored" {
		t.Errorf("alpha = %+v", a)
	}
	// A model the router does not run is not affected by the router being down.
	if w := rowOf(t, rows, "whisper-small"); !w.Loadable || w.LoadableReason != nil {
		t.Errorf("whisper = %+v", w)
	}
	// The default is still chosen: it follows the fit rules, not the router's health.
	if got := r.deps.Models.Models("en").Default; got != alpha {
		t.Errorf("default = %q", got)
	}

	// Before the first poll the watcher is stale without an error text: nothing is claimed.
	r.rt.set(router.View{Stale: true})
	if a := rowOf(t, r.deps.Models.Models("en").Rows, alpha); !a.Loadable {
		t.Errorf("alpha before the first poll = %+v", a)
	}
}

func TestModelsDefault(t *testing.T) {
	const gamma = "model-gamma-vl-q4"
	t.Run("the state default wins when it has a preset", func(t *testing.T) {
		r := newRig(t, nil)
		r.pres["/srv/localai|"+alpha], r.pres["/srv/localai|"+gamma] = true, true
		if err := r.st.MutateSync(func(s *state.State) error { d := gamma; s.Models.Default = &d; return nil }); err != nil {
			t.Fatal(err)
		}
		v := r.deps.Models.Models("en")
		if v.Default != gamma || !rowOf(t, v.Rows, gamma).Default || rowOf(t, v.Rows, alpha).Default {
			t.Errorf("default = %q", v.Default)
		}
	})
	t.Run("a default without a preset falls back to the profile", func(t *testing.T) {
		r := newRig(t, nil)
		r.pres["/srv/localai|"+alpha] = true
		if err := r.st.MutateSync(func(s *state.State) error { d := gamma; s.Models.Default = &d; return nil }); err != nil {
			t.Fatal(err)
		}
		if got := r.deps.Models.Models("en").Default; got != alpha {
			t.Errorf("default = %q", got)
		}
	})
	t.Run("nothing present, no default", func(t *testing.T) {
		r := newRig(t, nil)
		v := r.deps.Models.Models("en")
		if v.Default != "" || v.ModelsMax != 1 {
			t.Errorf("default %q max %d", v.Default, v.ModelsMax)
		}
		for _, row := range v.Rows {
			if row.Default {
				t.Errorf("%s is marked default", row.ID)
			}
		}
	})
	t.Run("survival uses the survival default", func(t *testing.T) {
		r := newRig(t, func(r *rig, _ *statusapi.Deps) {
			r.mode.info.Mode, r.mode.info.Effective = modes.Survival, modes.Survival
		})
		r.pres["/srv/localai|"+alpha], r.pres["/srv/localai|"+gamma] = true, true
		if got := r.deps.Models.Models("en").Default; got != alpha {
			t.Errorf("default = %q", got)
		}
	})
}

func TestModelsLastLoadAndLanguage(t *testing.T) {
	r := newRig(t, nil)
	r.pres["/srv/localai|"+alpha] = true
	if err := r.st.MutateSync(func(s *state.State) error {
		s.Models.LastLoad = &state.LastLoad{ID: alpha, Result: state.LoadOK, LoadMS: 7100, At: t0}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	row := rowOf(t, r.deps.Models.Models("en").Rows, alpha)
	if row.LastLoadMS == nil || *row.LastLoadMS != 7100 {
		t.Errorf("last_load_ms = %v", row.LastLoadMS)
	}
	if other := rowOf(t, r.deps.Models.Models("en").Rows, "model-gamma-vl-q4"); other.LastLoadMS != nil {
		t.Errorf("another model has last_load_ms %v", *other.LastLoadMS)
	}

	// The summary and the loadable reason follow the language.
	en := rowOf(t, r.deps.Models.Models("en").Rows, "model-beta-27b-q4")
	es := rowOf(t, r.deps.Models.Models("es").Rows, "model-beta-27b-q4")
	if en.LoadableReason == nil || es.LoadableReason == nil || en.LoadableReason.Message == es.LoadableReason.Message {
		t.Errorf("reasons: %+v / %+v", en.LoadableReason, es.LoadableReason)
	}
}

// TestModelsLocations: the internal root first, then the drives; each root that holds a copy is a location.
func TestModelsLocations(t *testing.T) {
	r := newRig(t, func(r *rig, d *statusapi.Deps) {
		d.Models = &statusapi.ModelMerger{
			Config: r.cfg, Catalog: r.store, State: r.st, Hardware: r.hw, Router: r.rt,
			Roots: func() router.Roots {
				return router.Roots{Internal: "/srv/localai", Drives: []string{"/media/ark-b", "/media/ark-a"}}
			},
			Presence: r.pres,
		}
	})
	r.pres["/media/ark-b|"+alpha], r.pres["/srv/localai|"+alpha], r.pres["/media/ark-a|"+alpha] = true, true, true
	row := rowOf(t, r.deps.Models.Models("en").Rows, alpha)
	got := []string{}
	for _, l := range row.Locations {
		got = append(got, l.Root)
	}
	want := []string{"/srv/localai", "/media/ark-a", "/media/ark-b"}
	if len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Errorf("locations = %v, want %v", got, want)
	}
}

// TestModelsMinimal: a merger with only the required fields works (no hardware, router, state, roots).
func TestModelsMinimal(t *testing.T) {
	r := newRig(t, nil)
	m := &statusapi.ModelMerger{Config: r.cfg, Catalog: r.store}
	v := m.Models("en")
	if len(v.Rows) != len(r.cat.Models) || v.Default != "" {
		t.Fatalf("view = %d rows, default %q", len(v.Rows), v.Default)
	}
	for _, row := range v.Rows {
		if row.Present || (row.Engine == "llama" && row.Status != "absent") {
			t.Errorf("row = %+v", row)
		}
	}
}
