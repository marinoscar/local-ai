package router

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/marinoscar/local-ai/agent/internal/audit"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/events"
)

// Doctor codes of the survival set (llama-router.md section 8), returned as warnings to the mode
// engine. They alias internal/doctor/codes.go like the other codes of this package.
const (
	// CodeSurvivalFallback: the preferred survival model did not start; the next one is used.
	CodeSurvivalFallback Code = doctor.LLM020
	// CodeSurvivalNone: in survival mode no model could be started.
	CodeSurvivalNone Code = doctor.LLM021
)

var _ ModelLoader = (*Orchestrator)(nil)

// LoadHeld implements ModelLoader for the Generator: load a model again after a reload dropped it,
// while the caller already holds modelOpsMu. It starts the model and waits for it, up to the load
// timeout; it takes no lock, plans no eviction (the set loaded before the reload fits) and does not
// roll back.
func (o *Orchestrator) LoadHeld(ctx context.Context, id string) error {
	in := o.opt.Inputs()
	if f := o.loadOne(ctx, id, o.loadTimeout(in, id), nil); f != nil {
		return f
	}
	return nil
}

// LoadSurvivalSet implements modes.ModelLoader (agent.md section 4.2.1, step survival_models): it
// loads the profile's survival.models, in order, until models_max of them are loaded, taking
// modelOpsMu like any load (it waits up to DefaultLockWait) and trying each model for the load
// timeout. Models in skip are not tried (the one whose load was in progress when the box died).
// Models that are loaded already count and are left alone; the others are loaded with eviction, least
// recently used first, so a survival model never evicts another survival model just loaded.
//
// A model that fails is rolled back by Load, and the next one is tried; each failure is the warning
// LAI-LLM-020. When no model is loaded at the end the warning LAI-LLM-021 is added. An error means
// it could not even try (no profile, the router does not answer, the agent is shutting down).
func (o *Orchestrator) LoadSurvivalSet(ctx context.Context, skip []string) ([]events.Warning, error) {
	in := o.opt.Inputs()
	if in.Profile == nil || len(in.Profile.Survival.Models) == 0 {
		return nil, errors.New("router: the node has no survival model list")
	}
	statuses, err := o.opt.Router.Models(ctx)
	if err != nil {
		return nil, err
	}
	want := in.modelsMax()
	have := statusByID(statuses)
	skipped := map[string]bool{}
	for _, s := range skip {
		skipped[s] = true
	}
	var list []string
	seen := map[string]bool{}
	for _, id := range in.Profile.Survival.Models {
		if !seen[id] {
			seen[id] = true
			list = append(list, id)
		}
	}
	count := 0
	for _, id := range list {
		if s, ok := have[id]; ok && s.Status.Resident() {
			count++
		}
	}

	var warns []events.Warning
	for _, id := range list {
		if count >= want {
			break
		}
		if skipped[id] {
			continue
		}
		if s, ok := have[id]; ok && s.Status.Resident() {
			continue
		}
		_, lerr := o.LoadWith(ctx, LoadRequest{ID: id, Wait: true, UnloadOthers: true, Actor: audit.System(), LockWait: DefaultLockWait})
		switch {
		case lerr == nil:
			count++
		case errors.Is(lerr, ErrInterrupted), ctx.Err() != nil:
			return warns, lerr
		default:
			var oe *OpError
			if errors.As(lerr, &oe) {
				o.log.Warn("a survival model did not start", "model", id, "code", string(oe.Code))
			}
			warns = append(warns, events.Warning{Code: string(CodeSurvivalFallback),
				Message: fmt.Sprintf("The survival model %s did not start; the next one is used.", id)})
		}
	}
	if o.noneLoaded(ctx) {
		msg := "In survival mode no model could be started."
		if len(skip) > 0 {
			msg += " Skipped: " + strings.Join(skip, ", ") + "."
		}
		warns = append(warns, events.Warning{Code: string(CodeSurvivalNone), Message: msg})
	}
	return warns, nil
}
