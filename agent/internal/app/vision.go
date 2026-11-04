package app

import (
	"context"

	"github.com/marinoscar/local-ai/agent/internal/catalog"
	"github.com/marinoscar/local-ai/agent/internal/router"
	"github.com/marinoscar/local-ai/agent/internal/svc/vision"
)

// visionSource is vision.Source over the catalog in use, the best-copy index and the static fit rules
// (companion-services.md section 4.5). Presence is read per file from the verified manifest of the
// best copy, so a model whose weights are there but whose mmproj is not shows up as LAI-SVC-041.
type visionSource struct{ d *Daemon }

// VisionModels implements vision.Source.
func (v visionSource) VisionModels(context.Context) []vision.Entry {
	d := v.d
	cat := d.c.Catalog.Get().Catalog
	var rcfg catalog.Router
	if p := d.profile(); p != nil {
		rcfg = p.Router
	}
	env := router.NewEnv(d.c.Hardware.Snapshot(), d.c.Config.Get().Llama.Backend, rcfg, 0)
	var out []vision.Entry
	for i := range cat.Models {
		m := &cat.Models[i]
		if !vision.IsVision(m) {
			continue
		}
		e := vision.Entry{ID: m.ID}
		if m.Llama != nil {
			e.Ctx = m.Llama.Ctx
		}
		weights, mmproj := v.presence(m)
		e.WeightsPresent, e.MmprojPresent = weights, mmproj
		e.Loadable, _ = env.Loadable(m, weights && mmproj)
		out = append(out, e)
	}
	return out
}

// presence reports whether the best verified copy of m has its weights and its mmproj file.
func (v visionSource) presence(m *catalog.Model) (weights, mmproj bool) {
	if v.d.c.ArkIndex == nil {
		return false, false
	}
	cp, err := v.d.c.ArkIndex.BestCopy(m.ID)
	if err != nil {
		return false, false
	}
	has := func(kind string) bool {
		want := false
		for _, f := range m.Files {
			if f.Kind != kind {
				continue
			}
			want = true
			found := false
			for _, g := range cp.Manifest.Files {
				if g.Path == f.Name {
					found = true
				}
			}
			if !found {
				return false
			}
		}
		return want
	}
	weights = has("weights")
	mmproj = m.Llama == nil || m.Llama.MmprojFileKind == "" || has(m.Llama.MmprojFileKind)
	return weights, mmproj
}
