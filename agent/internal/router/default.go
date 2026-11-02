package router

import (
	"context"
	"net/http"

	"github.com/marinoscar/local-ai/agent/internal/audit"
	"github.com/marinoscar/local-ai/agent/internal/catalog"
	"github.com/marinoscar/local-ai/agent/internal/events"
	"github.com/marinoscar/local-ai/agent/internal/state"
)

// DefaultResult is the body of the 200 of PUT /models/default (agent.md section 3.8.10).
type DefaultResult struct {
	Default string
	// Previous is the default before ("" when none was set).
	Previous string
	// IniRegenerated is true when models.ini was rewritten (its load-on-startup flag moved).
	IniRegenerated bool
}

// presetIDs are the models that get a preset right now: the effective set of section 4.1.
func (o *Orchestrator) presetIDs(in Inputs) []string {
	var models []catalog.Model
	if in.Catalog != nil {
		models = in.Catalog.Models
	}
	return EffectiveSet(EffectiveInput{
		Models: models, Profile: in.Profile, Overrides: in.Overrides,
		Roots: in.Roots, Presence: o.opt.Presence, Fit: in.Fit,
	}).IDs()
}

// SetDefault makes a model the default (agent.md section 3.8.10, llama-router.md section 4.6): the
// model must exist, be a llama model, be present and fit the node (404 LAI-AGT-021, 422
// LAI-CAT-005, 422 LAI-LLM-008). It records state.models.default and has the generator rewrite
// models.ini, whose load-on-startup flag follows, and apply it to the router under the reload-safety
// rule, so a running model whose preset did not change stays loaded. It does not load the model and
// emits model.default_changed when the default moved.
//
// SetDefault does not hold modelOpsMu itself: the generator takes it when a reload has to unload
// something, and a default change alone never does. A change that cannot be written (the generator
// fails) is undone in state and answered 500 LAI-LLM-007.
func (o *Orchestrator) SetDefault(ctx context.Context, id string, actor audit.Actor) (res DefaultResult, err error) {
	defer func() { o.auditWrite(ctx, audit.ActionModelSetDefault, actor, id, "", err) }()
	o.defaultMu.Lock()
	defer o.defaultMu.Unlock()

	in := o.opt.Inputs()
	m, ok := findModel(in.Catalog, id)
	if !ok {
		return res, errNotFound(id)
	}
	if m.Engine != "llama" {
		return res, errNotLlama(id, m.Engine)
	}
	_, present := o.locate(in, m)
	if fits, why := in.Fit.StaticFit(Candidate{ID: m.ID, Requirements: m.Requirements, Present: present}); !fits {
		return res, errNotLoadable(id, why)
	}

	// prev is the default in force now: the recorded one, else the profile's (section 4.6).
	recorded := ""
	if d := o.opt.State.Snapshot().Models.Default; d != nil {
		recorded = *d
	}
	prev := EffectiveDefault(o.presetIDs(in), recorded, in.Profile, in.Survival)
	res = DefaultResult{Default: id, Previous: prev}
	set := func(v string) error {
		return o.opt.State.MutateSync(func(st *state.State) error {
			if v == "" {
				st.Models.Default = nil
			} else {
				st.Models.Default = &v
			}
			return nil
		})
	}
	if serr := set(id); serr != nil {
		return res, opErr(CodePresetFailed, http.StatusInternalServerError, "The default model could not be saved.", map[string]any{"model": id}, serr)
	}
	if o.opt.Generator != nil {
		in.StateDefault = id
		gr, gerr := o.opt.Generator.Generate(ctx, in)
		if gerr != nil {
			if rerr := set(recorded); rerr != nil {
				o.log.Error("could not undo the default after a failed regeneration", "error", rerr)
			}
			return DefaultResult{Default: prev, Previous: prev}, opErr(CodePresetFailed, http.StatusInternalServerError,
				"The model list could not be written, so the default was not changed.", map[string]any{"model": id}, gerr)
		}
		res.IniRegenerated = gr.Written
		if gr.ApplyErr != nil {
			// The file is right; the router reads it at its next reload or start.
			o.log.Warn("the new default is saved but the router was not told yet", "model", id, "error", gr.ApplyErr)
		}
	}
	if prev != id {
		o.publish(events.TypeModelDefaultChanged, "", events.ModelDefaultChanged{Model: id, Previous: prev})
	}
	o.log.Info("default model set", "model", id, "previous", prev, "ini_regenerated", res.IniRegenerated)
	return res, nil
}
