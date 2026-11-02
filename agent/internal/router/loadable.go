package router

import "github.com/marinoscar/local-ai/agent/internal/catalog"

// Loadable lives here, not in package catalog: it wraps StaticFit, and this package imports
// catalog (a catalog -> router import would be a cycle).

// Loadability is the "loadable" part of one model in GET /models (agent.md section 3.8.7).
type Loadability struct {
	ID       string  `json:"id"`
	Loadable bool    `json:"loadable"`
	Reason   *Reason `json:"loadable_reason"`
}

// Loadable answers "can this catalog model load on this node?" with the static fit rules
// (llama-router.md section 4.3.2) and, when it cannot, the reason with its code and plain-language
// message. present says whether a complete, verified copy is on disk.
//
// Every engine is judged. A whisper or piper model has no graphics-memory requirement in the
// rules (the catalog says 0 for them), so only the backend and the memory rules apply: whatever
// min_vram_mib such an entry declares is ignored.
func (e Env) Loadable(m *catalog.Model, present bool) (bool, *Reason) {
	req := m.Requirements
	if m.Engine != "llama" {
		req.MinVRAMMiB = 0
	}
	return e.StaticFit(Candidate{ID: m.ID, Requirements: req, Present: present})
}

// LoadableAll judges every model, in catalog order. present maps model id to "a complete verified
// copy is on a mounted root"; a nil map means nothing is present. A model that does not fit may
// still be stored (catalog.md section 5): this result is for display and for the router preset,
// never a filter on the effective plan.
func (e Env) LoadableAll(models []catalog.Model, present map[string]bool) []Loadability {
	out := make([]Loadability, len(models))
	for i := range models {
		ok, why := e.Loadable(&models[i], present[models[i].ID])
		out[i] = Loadability{ID: models[i].ID, Loadable: ok, Reason: why}
	}
	return out
}
