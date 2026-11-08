package checks

import (
	"context"
	"fmt"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/drill"
	"github.com/marinoscar/local-ai/agent/internal/router"
)

// FitCheck is check 7, drl.llm.fit: compute the survival-entry plan (modes.md section 2.2 step 5)
// without switching to survival, and say whether the survival models fit this node by the rules of
// llama-router.md section 4.3.
//
//   - pass: every model of profile.survival.models fits;
//   - warn (LAI-DRL-024): some fit and some do not, so on entering survival a fallback model could
//     be used (details.fallback_used says whether the loop would reach a model that does not fit);
//   - fail (LAI-DRL-024): none fits, or the profile lists none.
//
// A model fits when the static fit rules say it can load here (backend, memory, files present and
// verified). The dynamic rules are evaluated as if nothing were loaded, and a shortage is only noted
// in details.tight: the real load in check 5 is the proof, and the router goes ahead when the
// shortage is other programs' memory (llama-router.md section 4.3.3).
func FitCheck(d ModelDeps) drill.Check {
	return drill.Check{
		ID: "drl.llm.fit", Area: "llm", Order: orderFit, FailCode: doctor.DRL024,
		Run: func(ctx context.Context, env *drill.Env) drill.Outcome { return d.fit(ctx) },
	}
}

func (d ModelDeps) fit(ctx context.Context) drill.Outcome {
	in := d.Inputs()
	list := survivalModels(in, d.Presence)
	if len(list) == 0 {
		return drill.Outcome{Result: drill.ResultFail, Code: doctor.DRL024, Details: map[string]any{"reason": "no_survival_models"}}
	}

	// The models loaded now would be unloaded to make room, so their RAM counts as free.
	var resident []router.Loaded
	if ids, err := d.Models.Resident(ctx); err == nil && in.Catalog != nil {
		for _, id := range ids {
			if m, ok := in.Catalog.Model(id); ok {
				resident = append(resident, router.Loaded{ID: id, Requirements: m.Requirements})
			}
		}
	}

	var fits, missing, tight []string
	for _, sm := range list {
		if !sm.Loadable {
			missing = append(missing, fmt.Sprintf("%s (%s)", sm.ID, reasonText(sm.Reason)))
			continue
		}
		fits = append(fits, sm.ID)
		short := in.Fit.DynamicFit(router.Candidate{ID: sm.ID, Requirements: sm.Model.Requirements, Present: true}, nil, resident)
		if short.GPU || short.RAM {
			tight = append(tight, sm.ID)
		}
	}

	details := map[string]any{"backend": in.Fit.Backend, "models": len(list), "fit": join(fits)}
	if len(missing) > 0 {
		details["not_loadable"] = join(missing)
	}
	if len(tight) > 0 {
		details["tight"] = join(tight)
	}
	switch {
	case len(fits) == 0:
		details["reason"] = "nothing_fits"
		return drill.Outcome{Result: drill.ResultFail, Code: doctor.DRL024, Details: details}
	case len(missing) > 0:
		details["fallback_used"] = fallbackUsed(in, list)
		return drill.Outcome{Result: drill.ResultWarn, Code: doctor.DRL024, Details: details}
	}
	return drill.Outcome{Result: drill.ResultPass, Details: details}
}

// fallbackUsed replays the survival-entry loop (router.Orchestrator.LoadSurvivalSet): models are tried
// in order until models_max of them are loaded; it reports whether a model that does not fit is
// reached, so a later one stands in for it.
func fallbackUsed(in router.Inputs, list []survivalModel) bool {
	want := in.Fit.ModelsMax
	if want < 1 {
		want = 1
	}
	loaded := 0
	for _, sm := range list {
		if loaded >= want {
			return false
		}
		if !sm.Loadable {
			return true
		}
		loaded++
	}
	return false
}

// reasonText is the code of the reason a model is not loadable, or a short phrase when it has none.
func reasonText(r *router.Reason) string {
	switch {
	case r == nil:
		return "does not fit"
	case r.Code != "":
		return string(r.Code)
	case r.Message != "":
		return r.Message
	}
	return "does not fit"
}
