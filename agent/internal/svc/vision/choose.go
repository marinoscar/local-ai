// Package vision decides which model answers a photo and checks that a vision model is usable
// (companion-services.md section 4.5, issue SVC-13). Vision is a normal LLM served by the router with
// an mmproj file, so there is nothing to run here: Choose is a pure function over what is loaded and
// what the profile lists, and the doctor check reports LAI-SVC-040 and LAI-SVC-041. The Portal photo
// flow and /portal/status (survival-portal.md sections 3.5 and 4.6) consume the Decision through the
// Advisor interface.
package vision

import "github.com/marinoscar/local-ai/agent/internal/doctor"

// The actions of a Decision (companion-services.md section 4.5).
const (
	// ActionUseLoaded: a loaded model already has the vision role.
	ActionUseLoaded = "use_loaded"
	// ActionLoadAlongside: models_max allows one more model and the vision model fits beside the others.
	ActionLoadAlongside = "load_alongside"
	// ActionNeedsSwitch: a vision model can load on this node, but only after an explicit unload/load
	// of the current brain (about 20 to 60 s on ai-n3). It carries no doctor code: it is not a problem.
	ActionNeedsSwitch = "needs_switch"
	// ActionNone: no vision model can load at all (LAI-SVC-040).
	ActionNone = "none"
)

// Model is what Choose needs to know about one catalog model.
type Model struct {
	ID string
	// Vision is true when the catalog roles contain "vision".
	Vision bool
	// Loadable is the static fit verdict (router Env.Loadable), which already includes "a verified
	// copy of the weights and the mmproj is on disk".
	Loadable bool
}

// Profile is the part of the node profile Choose reads: the survival list and the include list, in
// catalog order, each model resolved to a Model.
type Profile struct {
	Survival []Model
	Include  []Model
}

// FitFunc answers the router's dynamic fit (llama-router.md section 4.3.3): can this model load now,
// beside the models that stay loaded? Tests pass a fake, so Choose never needs the router.
type FitFunc func(modelID string) bool

// Decision is the answer to "which model answers a photo".
type Decision struct {
	// Action is one of the Action constants.
	Action string `json:"action"`
	// ModelID is the vision model to use or load; "" for ActionNone.
	ModelID string `json:"model_id"`
	// Code is "" or doctor.SVC040 (ActionNone only).
	Code string `json:"code"`
}

// Advisor is the internal interface the Portal consumers (capabilities.photo of /portal/status and
// the photo flow) call. Implementations gather the loaded models, the profile and the fit function
// and call Choose.
type Advisor interface {
	Decide() Decision
}

// Choose applies the rule of companion-services.md section 4.5, in order:
//
//  1. A loaded model has the vision role: use it.
//  2. models_max allows another model and the first vision model of profile.survival.models fits:
//     load it alongside. When the survival list has no vision model, the first vision model of
//     profile.include.models is the candidate instead (still subject to fit).
//  3. A vision model (survival list first, then include) is statically loadable: the user must switch
//     brain.
//  4. Otherwise none, with LAI-SVC-040.
//
// It never returns LAI-SVC-106: mapping "no brain running" is the Portal status builder's job.
func Choose(loaded []Model, profile Profile, fit FitFunc, modelsMax int) Decision {
	for _, m := range loaded {
		if m.Vision {
			return Decision{Action: ActionUseLoaded, ModelID: m.ID}
		}
	}
	cand, ok := firstVision(profile.Survival)
	if !ok {
		cand, ok = firstVision(profile.Include)
	}
	if ok && modelsMax > len(loaded) && cand.Loadable && fit != nil && fit(cand.ID) {
		return Decision{Action: ActionLoadAlongside, ModelID: cand.ID}
	}
	for _, list := range [][]Model{profile.Survival, profile.Include} {
		for _, m := range list {
			if m.Vision && m.Loadable {
				return Decision{Action: ActionNeedsSwitch, ModelID: m.ID}
			}
		}
	}
	return Decision{Action: ActionNone, Code: doctor.SVC040}
}

func firstVision(list []Model) (Model, bool) {
	for _, m := range list {
		if m.Vision {
			return m, true
		}
	}
	return Model{}, false
}
