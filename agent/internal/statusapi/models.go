package statusapi

import (
	"github.com/marinoscar/local-ai/agent/internal/catalog"
	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/hardware"
	"github.com/marinoscar/local-ai/agent/internal/modes"
	"github.com/marinoscar/local-ai/agent/internal/router"
	"github.com/marinoscar/local-ai/agent/internal/state"
)

// Model status values of a ModelRow (agent.md section 3.8.7).
const (
	ModelAbsent   = "absent"
	ModelStored   = "stored"
	ModelUnloaded = "unloaded"
	ModelLoading  = "loading"
	ModelLoaded   = "loaded"
	ModelSleeping = "sleeping"
	ModelFailed   = "failed"
	ModelNA       = "n/a"
)

// RouterView is the router watcher's cached picture; *router.Watcher implements it.
type RouterView interface {
	View() router.View
}

// ModelMerger is the ModelView of the daemon: it merges every catalog model with where its files
// are (the roots' manifests), the static fit rules for this hardware and the router's state, the way
// GET /models does (agent.md section 3.8.7). It reads cached values only: the watcher's last poll,
// the hardware sample and the manifests of the mounted roots. Every field except Catalog and Config
// is optional; without a router every present model is "stored".
type ModelMerger struct {
	Config  ConfigView
	Catalog *CatalogStore
	// State gives the default model, the last load time and (without Mode) the mode.
	State StateView
	// Mode, when set, decides "survival" for the default model; else State's mode is used.
	Mode     ModeView
	Hardware HardwareView
	Router   RouterView
	// Roots returns the mounted content roots; nil means none, so nothing is present.
	Roots func() router.Roots
	// Presence decides "a complete, verified copy of this model is on this root"; nil means none.
	Presence router.Presence
	// WhisperReserveMiB is W_res of the fit rules (llama-router.md section 4.3.1); nil means 0.
	WhisperReserveMiB func() int64
}

var _ ModelView = (*ModelMerger)(nil)

// Models implements ModelView.
func (m *ModelMerger) Models(lang string) ModelsView {
	cs := m.Catalog.Get()
	cfg := m.Config.Get()
	prof, _ := cs.Catalog.Profile(cfg.Profile)

	var snap hardware.Snapshot
	if m.Hardware != nil {
		snap = m.Hardware.Snapshot()
	}
	var routerCfg catalog.Router
	if prof != nil {
		routerCfg = prof.Router
	}
	var w int64
	if m.WhisperReserveMiB != nil {
		w = m.WhisperReserveMiB()
	}
	env := router.NewEnv(snap, cfg.Llama.Backend, routerCfg, w)

	var st state.State
	haveState := m.State != nil
	if haveState {
		st = m.State.Snapshot()
	}
	survival := st.Mode == state.ModeSurvival
	if m.Mode != nil {
		survival = m.Mode.Info().Effective == modes.Survival
	}

	var roots []string
	if m.Roots != nil {
		roots = m.Roots().Ordered()
	}

	var rv router.View
	haveRouter := m.Router != nil
	byID := map[string]router.ModelStatus{}
	if haveRouter {
		rv = m.Router.View()
		for _, ms := range rv.Models {
			byID[ms.ID] = ms
		}
	}
	routerDown := haveRouter && rv.Stale && rv.Err != ""

	inProfile := InProfileModels(cs.Catalog, prof, cfg.Profile, cfg.ProfileOverrides)

	rows := make([]ModelRow, 0, len(cs.Catalog.Models))
	var withPreset []string
	for i := range cs.Catalog.Models {
		cm := &cs.Catalog.Models[i]
		row := ModelRow{
			ID: cm.ID, DisplayName: cm.DisplayName, Summary: summaryIn(cm, lang), Engine: cm.Engine,
			Roles: nonNil(cm.Roles), SurvivalTier: cm.SurvivalTier, InProfile: inProfile[cm.ID],
			SizeBytes: catalog.ModelSizeBytes(cm), Locations: []ModelLocation{}, Aliases: []string{},
			Requirements: ModelRequirement{
				Backends: nonNil(cm.Requirements.Backends), MinVRAMMiB: cm.Requirements.MinVRAMMiB, MinRAMMiB: cm.Requirements.MinRAMMiB,
			},
		}
		if m.Presence != nil {
			for _, r := range roots {
				if m.Presence.Present(r, cm) {
					row.Locations = append(row.Locations, ModelLocation{Root: r, Complete: true})
				}
			}
		}
		row.Present = len(row.Locations) > 0
		row.Verified = row.Present // a location is a complete copy whose manifest is verified

		ok, why := env.Loadable(cm, row.Present)
		if ok && cm.Engine == "llama" {
			withPreset = append(withPreset, cm.ID)
		}
		if ok && routerDown && cm.Engine == "llama" {
			ok = false
			why = routerDownReason()
		}
		row.Loadable = ok
		if !ok && why != nil {
			row.LoadableReason = &ReasonBody{Code: string(why.Code), Message: why.Text(lang)}
		}

		ms, listed := byID[cm.ID]
		if listed {
			row.Aliases = nonNil(ms.Aliases)
		}
		row.Status = modelStatus(cm, row.Present, ms, listed)
		row.Loaded = row.Status == ModelLoaded
		if ll := st.Models.LastLoad; ll != nil && ll.ID == cm.ID && ll.Result == state.LoadOK && ll.LoadMS > 0 {
			ms := ll.LoadMS
			row.LastLoadMS = &ms
		}
		rows = append(rows, row)
	}

	stateDefault := ""
	if st.Models.Default != nil {
		stateDefault = *st.Models.Default
	}
	def := router.EffectiveDefault(withPreset, stateDefault, prof, survival)
	for i := range rows {
		rows[i].Default = def != "" && rows[i].ID == def
	}
	max := routerCfg.ModelsMax
	if max < 1 {
		max = 1
	}
	return ModelsView{Rows: rows, Default: def, ModelsMax: max}
}

// modelStatus is the status of a row: n/a for a model the router does not run, absent without a
// complete copy, the router's state when it lists the model, else stored.
func modelStatus(cm *catalog.Model, present bool, ms router.ModelStatus, listed bool) string {
	switch {
	case cm.Engine != "llama":
		return ModelNA
	case !present:
		return ModelAbsent
	case !listed:
		return ModelStored
	case ms.Failed:
		return ModelFailed
	}
	switch ms.Status {
	case router.StatusLoaded:
		return ModelLoaded
	case router.StatusLoading:
		return ModelLoading
	case router.StatusSleeping:
		return ModelSleeping
	}
	return ModelUnloaded
}

// routerDownReason is the loadable_reason of a model that would load, while the router does not
// answer (agent.md section 3.8.7).
func routerDownReason() *router.Reason {
	en, _, _ := doctor.Text(string(router.CodeRouterDown), doctor.LangEN)
	es, _, _ := doctor.Text(string(router.CodeRouterDown), doctor.LangES)
	return &router.Reason{Code: router.CodeRouterDown, Message: en, MessageES: es}
}

// InProfileModels is the set of model ids the node's profile keeps: the models of the effective plan,
// or, when the plan cannot be computed (an unknown profile or id), the profile's include list changed
// by the overrides, so a node with a broken node.yaml still shows what it asked for.
func InProfileModels(cat *catalog.Catalog, prof *catalog.Profile, profileID string, ov config.ProfileOverrides) map[string]bool {
	if plan, err := cat.EffectivePlan(profileID, catalog.Overrides(ov)); err == nil {
		set := make(map[string]bool, len(plan.Models))
		for _, id := range plan.Models {
			set[id] = true
		}
		return set
	}
	return router.DesiredModels(prof, ov)
}

func summaryIn(cm *catalog.Model, lang string) string {
	if lang == doctor.LangES {
		if s := cm.Summaries["summary_es"]; s != "" {
			return s
		}
	}
	return cm.Summary
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return append([]string{}, s...)
}
