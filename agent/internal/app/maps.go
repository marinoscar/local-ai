package app

import (
	"github.com/marinoscar/local-ai/agent/internal/maps"
	"github.com/marinoscar/local-ai/agent/internal/svc/health"
)

// mapsDeps are the dependencies of the maps routes and the maps health probe: the best-copy index
// (which says a map is installed), the catalog's maps and the node.yaml maps block as it is now.
func (d *Daemon) mapsDeps(m *health.Metrics) maps.Deps {
	return maps.Deps{
		Copies:  d.c.ArkIndex,
		Items:   d.mapItems,
		Config:  func() maps.Config { return maps.FromConfig(d.c.Config.Get()) },
		Metrics: m,
		Logger:  d.log,
	}
}

// mapItems lists the maps of the catalog in use (kind maps, built by pmtiles_extract) and marks the
// ones the node's profile includes.
func (d *Daemon) mapItems() []maps.MapItem {
	cat := d.c.Catalog.Get().Catalog
	if cat == nil {
		return nil
	}
	inProfile := map[string]bool{}
	if p := d.profile(); p != nil {
		for _, id := range p.Include.Content {
			inProfile[id] = true
		}
	}
	var out []maps.MapItem
	for i := range cat.Content {
		it := &cat.Content[i]
		if it.Kind != "maps" || it.Source.Type != "pmtiles_extract" {
			continue
		}
		out = append(out, maps.MapItem{ID: it.ID, DisplayName: it.DisplayName, Tier: it.Tier(), InProfile: inProfile[it.ID]})
	}
	return out
}

// lazyGPS gives /maps/catalog.json the position source of "Where am I". That service is built by a
// route hook after this one, so it is looked up on use.
type lazyGPS struct{ d *Daemon }

// CatalogGPS implements maps.GPSProvider.
func (l lazyGPS) CatalogGPS() (bool, string) {
	if v, ok := whereAmI.Load(l.d.c); ok {
		if p, ok := v.(*whereAmIParts); ok && p.svc != nil {
			return p.svc.CatalogGPS()
		}
	}
	return false, ""
}
