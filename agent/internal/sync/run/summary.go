package run

import (
	"context"
	"path/filepath"

	"github.com/marinoscar/local-ai/agent/internal/ark"
	"github.com/marinoscar/local-ai/agent/internal/sync/plan"
)

// PlanSummary is the plan of a request reduced to the three figures the cloud's ark.sync result
// carries.
type PlanSummary struct {
	ItemsToDownload    int64
	BytesToDownload    int64
	ItemsSkippedBudget int64
}

// PlanSummary plans the request without running it and returns the figures: how many items would be
// downloaded or updated, how many bytes, and how many were left out because the budget is full. It
// takes no run slot, writes nothing and is allowed in every mode (offline and survival plan without
// the network). items limits the rows counted; empty means the whole plan. An unknown root is an
// *UnknownRootError, no writable root is NoRootsError.
func (e *Engine) PlanSummary(ctx context.Context, rootNames, items []string) (PlanSummary, error) {
	s := e.d.Settings()
	mounted := e.d.Planner.Roots()
	ids, err := selectRoots(rootNames, s, mounted)
	if err != nil {
		return PlanSummary{}, err
	}
	if len(ids) == 0 {
		return PlanSummary{}, NoRootsError{}
	}
	net := e.d.Net()
	plans, err := e.d.Planner.Plan(ctx, PlanRequest{RootIDs: ids, Offline: !net.Online || net.Mode != plan.ModeOnline})
	if err != nil {
		return PlanSummary{}, err
	}
	want := map[string]bool{}
	for _, id := range items {
		want[id] = true
	}
	var sum PlanSummary
	for _, rp := range plans.Plans {
		for _, row := range rp.Items {
			if len(want) > 0 && !want[row.ID] {
				continue
			}
			switch {
			case row.Action == plan.ActionDownload || row.Action == plan.ActionUpdate:
				sum.ItemsToDownload++
				sum.BytesToDownload += row.NeedBytes
			case row.Action == plan.ActionSkip && row.Reason == plan.ReasonBudget:
				sum.ItemsSkippedBudget++
			}
		}
	}
	return sum, nil
}

// ResolveRoot returns the mounted root a name refers to: a root id, its path or its mount point.
func (e *Engine) ResolveRoot(name string) (ark.Root, bool) {
	for _, r := range e.d.Planner.Roots() {
		if r.ID == name || (r.Path != "" && filepath.Clean(r.Path) == filepath.Clean(name)) || (r.Mount != "" && filepath.Clean(r.Mount) == filepath.Clean(name)) {
			return r, true
		}
	}
	return ark.Root{}, false
}

// Roots returns the mounted content roots.
func (e *Engine) Roots() []ark.Root { return e.d.Planner.Roots() }
