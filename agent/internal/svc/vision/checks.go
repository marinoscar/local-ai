package vision

import (
	"context"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/catalog"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
)

// CheckID is the doctor check of this package.
const CheckID = "svc.vision"

// MinCtx is the smallest llama.ctx of a vision model: images take hundreds to thousands of tokens
// (companion-services.md section 4.5).
const MinCtx = 8192

// RoleVision is the catalog role of a model that understands images.
const RoleVision = "vision"

// IsVision reports whether the catalog model has the vision role.
func IsVision(m *catalog.Model) bool {
	for _, r := range m.Roles {
		if r == RoleVision {
			return true
		}
	}
	return false
}

// Entry is one catalog vision model as the check sees it. The source computes the presence flags from
// the ark index and Loadable from the router's static fit rules.
type Entry struct {
	ID string
	// Ctx is llama.ctx of the catalog entry (0 when there is no llama block).
	Ctx int
	// WeightsPresent: a verified copy of the model's weights is on a mounted root.
	WeightsPresent bool
	// MmprojPresent: the same copy also has a verified mmproj file.
	MmprojPresent bool
	// Loadable is the static fit verdict including presence of every file.
	Loadable bool
}

// Source lists the catalog's vision models.
type Source interface {
	VisionModels(ctx context.Context) []Entry
}

// Check returns the doctor check svc.vision:
//   - LAI-SVC-041 (error) per vision model whose weights are on disk but whose mmproj has no verified
//     copy (details.model);
//   - LAI-SVC-040 (warning) when no vision model is loadable at all;
//   - a ctx below MinCtx is reported as the lint rule LAI-CAT-013 relaxed to a warning (details.rule
//     "ctx_min"), since the lint itself runs at catalog load.
func Check(src Source) doctor.Check {
	return doctor.Check{ID: CheckID, Area: "SVC", Fast: true, Timeout: 5 * time.Second, Run: func(ctx context.Context) []doctor.Result {
		return Evaluate(src.VisionModels(ctx))
	}}
}

// Register adds the check to reg (doctor.DefaultRegistry when nil).
func Register(reg *doctor.Registry, src Source) error {
	if reg == nil {
		reg = doctor.DefaultRegistry
	}
	return reg.Add(Check(src))
}

// Evaluate is the pure part of the check.
func Evaluate(entries []Entry) []doctor.Result {
	var out []doctor.Result
	anyLoadable := false
	for _, e := range entries {
		if e.Loadable {
			anyLoadable = true
		}
		if e.WeightsPresent && !e.MmprojPresent {
			out = append(out, doctor.Result{Code: doctor.SVC041, Severity: doctor.Error,
				Details: map[string]any{"model": e.ID}})
		}
		if e.Ctx < MinCtx {
			out = append(out, doctor.Result{Code: doctor.CAT013, Severity: doctor.Warning,
				Details: map[string]any{"model": e.ID, "rule": "ctx_min", "ctx": e.Ctx, "min": MinCtx}})
		}
	}
	if !anyLoadable {
		out = append(out, doctor.Result{Code: doctor.SVC040, Severity: doctor.Warning,
			Details: map[string]any{"vision_models": len(entries)}})
	}
	return out
}

// FromCatalog resolves a profile's model lists into Choose's Profile. loadable is the static fit
// verdict of a catalog model (presence of every file included).
func FromCatalog(cat *catalog.Catalog, p *catalog.Profile, loadable func(*catalog.Model) bool) Profile {
	if p == nil {
		return Profile{}
	}
	conv := func(ids []string) []Model {
		var out []Model
		for _, id := range ids {
			if m, ok := cat.Model(id); ok {
				out = append(out, ModelOf(m, loadable(m)))
			}
		}
		return out
	}
	return Profile{Survival: conv(p.Survival.Models), Include: conv(p.Include.Models)}
}

// ModelOf converts one catalog model.
func ModelOf(m *catalog.Model, loadable bool) Model {
	return Model{ID: m.ID, Vision: IsVision(m), Loadable: loadable}
}
