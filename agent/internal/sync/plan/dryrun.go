package plan

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"

	"github.com/marinoscar/local-ai/agent/internal/api"
	"github.com/marinoscar/local-ai/agent/internal/ark"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
)

// DryRun is the adapter between POST /ark/sync with dry_run: true and the planner: it implements
// api.ArkSyncPlanner (internal/api cannot import the planner, so the route holds the interface and this
// package holds the implementation). It gathers each requested root, plans them together (PlanAll, so
// a download shared by two roots is sequenced) and returns one plan per root. It writes nothing, takes
// no run slot, and is allowed in every mode: offline and in survival mode it plans without the network
// and says so, instead of refusing.
type DryRun struct {
	g *Gatherer
}

var _ api.ArkSyncPlanner = (*DryRun)(nil)

// NewDryRun returns the dry-run planner over a gatherer.
func NewDryRun(g *Gatherer) *DryRun { return &DryRun{g: g} }

// DryRun implements api.ArkSyncPlanner. The body is a []RootPlan: the requested roots in the order of
// the request (every writable root, internal first, when none is named). The items filter shows only
// those rows; the budget, planned and peak figures still describe the whole plan, because every rule
// is applied to the whole profile.
func (d *DryRun) DryRun(ctx context.Context, req api.ArkSyncRequest) (any, error) {
	rootIDs, err := d.selectRoots(req.Roots)
	if err != nil {
		return nil, err
	}
	// The same rule the engine's preflight follows, so a dry run plans what the run would: the internet
	// is reachable and the mode allows it (the offline mode only with allow_offline_mode).
	online := d.g.d.Network().MayUse(req.AllowOfflineMode)
	inputs, err := d.g.GatherAll(ctx, rootIDs, GatherOptions{Resolve: true, Online: &online})
	if err != nil {
		return nil, gatherError(err)
	}
	plans := PlanAll(inputs)
	if len(req.Items) > 0 {
		for _, want := range req.Items {
			if !anyPlanHas(plans, want) {
				return nil, api.NewError(api.CodeInvalidRequest, http.StatusBadRequest, map[string]any{
					"field": "items", "reason": "unknown_item", "value": want,
				})
			}
		}
		for i := range plans {
			plans[i] = plans[i].OnlyItems(req.Items)
		}
	}
	return plans, nil
}

// selectRoots turns the request's roots (ids or paths) into root ids. Empty means every writable
// mounted root; none writable is LAI-ARK-001. A name that is not a mounted root is a 400. A root
// named twice is planned once.
func (d *DryRun) selectRoots(names []string) ([]string, error) {
	roots := d.g.Roots()
	var out []string
	if len(names) == 0 {
		for _, r := range roots {
			if r.ReadOnly || (r.State != ark.StateReady && r.State != "") {
				continue
			}
			out = append(out, r.ID)
		}
		if len(out) == 0 {
			return nil, api.NewError(doctor.ARK001, http.StatusConflict, nil)
		}
		return out, nil
	}
	seen := map[string]bool{}
	for _, name := range names {
		id := ""
		for _, r := range roots {
			if r.ID == name || (r.Path != "" && filepath.Clean(r.Path) == filepath.Clean(name)) || (r.Mount != "" && filepath.Clean(r.Mount) == filepath.Clean(name)) {
				id = r.ID
				break
			}
		}
		if id == "" {
			return nil, api.NewError(api.CodeInvalidRequest, http.StatusBadRequest, map[string]any{
				"field": "roots", "reason": "unknown_root", "value": name,
			})
		}
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out, nil
}

func anyPlanHas(plans []RootPlan, id string) bool {
	for _, p := range plans {
		if _, ok := p.Item(id); ok {
			return true
		}
	}
	return false
}

// gatherError maps the gatherer's typed errors to the route's. Anything else is a bug or an I/O
// failure and stays a plain error (500).
func gatherError(err error) error {
	var unknownRoot *ErrUnknownRoot
	var unknownProfile *ErrUnknownProfile
	switch {
	case errors.As(err, &unknownRoot):
		return api.NewError(api.CodeInvalidRequest, http.StatusBadRequest, map[string]any{
			"field": "roots", "reason": "unknown_root", "value": unknownRoot.Root,
		})
	case errors.As(err, &unknownProfile):
		return api.NewError(CodeUnknownProfile, http.StatusUnprocessableEntity, map[string]any{
			"profile": unknownProfile.Profile, "root": unknownProfile.Root,
		})
	}
	return err
}
