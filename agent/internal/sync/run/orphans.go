package run

import (
	"context"
	"errors"
	"sort"

	"github.com/marinoscar/local-ai/agent/internal/ark"
	"github.com/marinoscar/local-ai/agent/internal/sync/plan"
)

// ErrNoCatalog: the planner has no catalog or profile to judge by, so it cannot say what is an
// orphan. With an empty catalog every item would look like one; no caller may prune on that.
var ErrNoCatalog = errors.New("run: no catalog is loaded, so nothing can be called an orphan")

// OrphanItems returns the ids of the verified copies on a root that the profile no longer wants
// (the rows of the plan with action orphan, ark-sync.md section 4.4.3), by id. It plans offline: what
// is wanted comes from the catalog, the profile and the node, never from the network. It takes no run
// slot and writes nothing. It is the list `aictl ark prune --orphans` deletes from, through
// retention.Prune, which still refuses the last verified copy of each.
func (e *Engine) OrphanItems(ctx context.Context, root ark.Root) ([]string, error) {
	plans, err := e.d.Planner.Plan(ctx, PlanRequest{RootIDs: []string{root.ID}, Offline: true})
	if err != nil {
		return nil, err
	}
	var out []string
	found := false
	for _, rp := range plans.Plans {
		if rp.Root != root.ID {
			continue
		}
		found = true
		if rp.CatalogVersion == "" {
			return nil, ErrNoCatalog
		}
		for _, row := range rp.Items {
			if row.Action == plan.ActionOrphan {
				out = append(out, row.ID)
			}
		}
	}
	if !found {
		return nil, ErrNoCatalog
	}
	sort.Strings(out)
	return out, nil
}
