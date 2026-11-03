package ready

import (
	"time"

	"github.com/marinoscar/local-ai/agent/internal/ark"
)

// StartHere reduces a readiness report to what START-HERE.html says (ark-sync.md section 4.15): the
// colour, survival_capable, the last sync that finished ok, the catalog version, the node that wrote the
// report and one row per item. name maps a catalog id to its display name (nil, or "", shows the id).
//
// Orphans are listed: they are on the drive. The report's order is kept.
func (r Report) StartHere(name func(id string) string) ark.StartHereInput {
	in := ark.StartHereInput{
		Overall: string(r.Overall), SurvivalCapable: r.SurvivalCapable, WrittenBy: r.GeneratedBy.NodeID,
	}
	if r.Root.CatalogVersion != nil {
		in.CatalogVersion = *r.Root.CatalogVersion
	}
	if t, err := time.Parse(time.RFC3339, r.GeneratedAt); err == nil {
		in.GeneratedAt = t
	}
	if r.LastSync != nil && r.LastSync.State == "done" {
		if t, err := time.Parse(time.RFC3339, r.LastSync.FinishedAt); err == nil {
			in.LastSync = t
		}
	}
	for _, it := range r.Items {
		row := ark.StartHereItem{ID: it.ID, Version: it.Version, Status: string(it.Status)}
		if name != nil {
			row.Name = name(it.ID)
		}
		if it.Bytes != nil {
			row.Bytes = *it.Bytes
		}
		in.Items = append(in.Items, row)
	}
	return in
}
