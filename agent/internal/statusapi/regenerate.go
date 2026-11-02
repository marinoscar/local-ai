package statusapi

import (
	"context"
	"errors"

	"github.com/marinoscar/local-ai/agent/internal/api"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/router"
)

// Regenerator regenerates models.ini for the node's current profile, catalog and hardware and asks
// the router to apply it under the reload-safety rule (llama-router.md section 4.2). PUT /profile
// calls it after node.yaml changed, and POST /catalog/refresh after a new catalog was installed.
// The daemon wires RouterRegenerator; until then the routes write node.yaml and skip the reload.
type Regenerator interface {
	Regenerate(ctx context.Context) (RegenResult, error)
}

// RegenResult is what a Regenerator reports.
type RegenResult struct {
	// Written: models.ini was replaced. Reloaded: the router was asked to re-read it.
	Written, Reloaded bool
	// Warnings are problems that did not stop the regeneration (a model left out, a reload that
	// waits for the next run). They are returned to the caller and never fail the request.
	Warnings []WarningBlock
}

// RouterRegenerator is the Regenerator over router.Generator. Inputs assembles router.Inputs from
// the config store, the catalog store, the hardware sample, the state store and the mounted roots
// (the caller owns that, as the router.Inputs documentation says), so every trigger of a
// regeneration builds the same value.
type RouterRegenerator struct {
	Generator *router.Generator
	Inputs    func(ctx context.Context) (router.Inputs, error)
}

var _ Regenerator = RouterRegenerator{}

// Regenerate implements Regenerator.
func (r RouterRegenerator) Regenerate(ctx context.Context) (RegenResult, error) {
	in, err := r.Inputs(ctx)
	if err != nil {
		return RegenResult{}, err
	}
	res, err := r.Generator.Generate(ctx, in)
	if err != nil {
		return RegenResult{}, err
	}
	return resultOf(res), nil
}

// resultOf turns a router.Result into the warnings a route may return.
func resultOf(res router.Result) RegenResult {
	out := RegenResult{Written: res.Written, Reloaded: res.Reloaded}
	for _, f := range res.Findings {
		msg, _, _ := doctor.Text(string(f.Code), doctor.LangEN)
		if f.Subject != "" {
			msg += " (" + f.Subject + ")"
		}
		out.Warnings = append(out.Warnings, WarningBlock{Code: string(f.Code), Message: msg})
	}
	if res.ApplyErr != nil {
		// The file is written; the router could not take it now (model operations are busy, or a model
		// could not be loaded again) and the next regeneration retries.
		code := string(router.CodeOf(res.ApplyErr))
		if code == "" {
			code = api.CodeBusy
		}
		out.Warnings = append(out.Warnings, WarningBlock{
			Code:    code,
			Message: "The router could not apply the new model list yet; the next reload retries it.",
		})
	}
	return out
}

// regenerate runs the optional Regenerator and returns its warnings; an error becomes a warning too,
// because the change that triggered it is already written.
func regenerate(ctx context.Context, r Regenerator) []WarningBlock {
	if r == nil {
		return nil
	}
	res, err := r.Regenerate(ctx)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return []WarningBlock{{Code: api.CodeBusy, Message: "The router reload did not finish in time; the next reload retries it."}}
		}
		return []WarningBlock{{Code: api.CodeInternal, Message: "The model list could not be regenerated; the next reload retries it."}}
	}
	return res.Warnings
}
