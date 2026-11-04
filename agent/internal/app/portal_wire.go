package app

import (
	"time"

	"github.com/marinoscar/local-ai/agent/internal/ark"
	"github.com/marinoscar/local-ai/agent/internal/ark/ready"
	"github.com/marinoscar/local-ai/agent/internal/portal"
)

// portalBuilder is the body builder of GET /portal/status over the daemon's cached views
// (survival-portal.md section 3.5). No view runs a check.
//
// BuildTime is left zero: the agent binary does not record when it was built, so clock_ok rests on
// READY.json's clock_wrong and verified_at only.
func (d *Daemon) portalBuilder(sup portal.HealthView) *portal.Builder {
	c := d.c
	src := portal.Sources{
		Config: c.Config, State: c.State, Mode: d.mode, Hardware: c.Hardware, Router: c.RouterWatcher,
		Catalog: c.Catalog, Alerts: c.Doctor, Models: d.merger, Health: sup, Llama: c.Watcher,
		Settings: func() portal.Settings { return portal.SettingsFrom(c.Config.Get(), c.Catalog.Get().Catalog) },
		Docs:     portal.FileDocs{Roots: func() []string { return d.roots().Ordered() }},
	}
	if d.ark != nil && d.ark.ready != nil {
		src.Ark = readyArk{d.ark.ready}
	}
	return portal.NewBuilder(src)
}

// readyArk adapts the readiness service to the node-level summary of the status.
type readyArk struct{ svc *ready.Service }

// ArkSummary implements portal.ArkView. overall is the worst colour of the mounted roots;
// survival_capable is true when any root alone holds everything survival needs.
func (r readyArk) ArkSummary() portal.ArkSummary {
	var out portal.ArkSummary
	rank := map[ready.Colour]int{ready.Green: 1, ready.Yellow: 2, ready.Red: 3}
	worst := ready.Colour("")
	for _, rep := range r.svc.Reports() {
		if rank[rep.Overall] > rank[worst] {
			worst = rep.Overall
		}
		out.SurvivalCapable = out.SurvivalCapable || rep.SurvivalCapable
		out.ClockWrong = out.ClockWrong || rep.ClockWrong
		out.DriveConnected = out.DriveConnected || rep.Root.Kind == string(ark.KindArkDrive)
		for _, it := range rep.Items {
			if t, err := time.Parse(time.RFC3339, it.VerifiedAt); err == nil && t.After(out.NewestVerifiedAt) {
				out.NewestVerifiedAt = t
			}
		}
	}
	out.Overall = string(worst)
	return out
}
