package adapt

import (
	"context"
	"errors"
	"net/http"

	"github.com/marinoscar/local-ai/agent/internal/api"
	"github.com/marinoscar/local-ai/agent/internal/ark"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/sync/retention"
	"github.com/marinoscar/local-ai/agent/internal/sync/run"
)

var _ api.ArkPruner = Pruner{}

// Pruner is api.ArkPruner over the engine (which knows what the profile wants, so what is an orphan)
// and retention (which decides what may go): POST /ark/prune. It takes no run slot: while a sync run is
// active it refuses with 409 LAI-SYN-005, and retention judges every deletion against the live state
// anyway, so a run that starts in the middle cannot make it delete a last verified copy.
type Pruner struct {
	Engine    *run.Engine
	Retention *retention.Retention
}

// Prune implements api.ArkPruner.
func (p Pruner) Prune(ctx context.Context, req api.ArkPruneRequest) (api.ArkPruneResult, error) {
	if p.Engine.RunActive() {
		d := map[string]any{}
		if st, ok := p.Engine.Status(); ok && st.RunID != "" {
			d["run_id"] = st.RunID
		}
		return api.ArkPruneResult{}, api.NewError(doctor.SYN005, http.StatusConflict, d)
	}
	var ids []string
	seen := map[string]bool{}
	for _, name := range req.Roots {
		r, ok := p.Engine.ResolveRoot(name)
		if !ok {
			return api.ArkPruneResult{}, api.NewError(api.CodeInvalidRequest, http.StatusBadRequest, map[string]any{"field": "roots", "reason": "unknown_root", "value": name})
		}
		if !seen[r.ID] {
			seen[r.ID] = true
			ids = append(ids, r.ID)
		}
	}
	res, err := p.Retention.Prune(ctx, retention.PruneRequest{
		Roots: ids, Orphans: req.Orphans, Trash: req.Trash, DryRun: req.DryRun,
		Orphan: func(ctx context.Context, root ark.Root) ([]string, error) { return p.Engine.OrphanItems(ctx, root) },
	})
	if err != nil {
		if errors.Is(err, run.ErrNoCatalog) {
			// Without a catalog every item would look like an orphan: say so and delete nothing.
			return api.ArkPruneResult{}, api.NewError(doctor.CAT001, http.StatusConflict, map[string]any{"reason": "no_catalog"})
		}
		return api.ArkPruneResult{}, err
	}
	out := api.ArkPruneResult{
		Deleted:    make([]api.ArkPruned, 0, len(res.Deleted)),
		Refused:    make([]api.ArkPruneRefused, 0, len(res.Refused)),
		FreedBytes: res.FreedBytes,
	}
	for _, d := range res.Deleted {
		out.Deleted = append(out.Deleted, api.ArkPruned{Root: d.Root, Item: d.Item, Version: d.Version, Bytes: d.Bytes, Kind: d.Kind})
	}
	for _, d := range res.Refused {
		out.Refused = append(out.Refused, api.ArkPruneRefused{Root: d.Root, Item: d.Item, Version: d.Version, Reason: d.Reason, Kind: d.Kind})
	}
	return out, nil
}
