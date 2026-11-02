package statusapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/marinoscar/local-ai/agent/internal/api"
	"github.com/marinoscar/local-ai/agent/internal/audit"
	"github.com/marinoscar/local-ai/agent/internal/catalog"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/modes"
	"github.com/marinoscar/local-ai/agent/internal/state"
)

// maxProblemsInDetails bounds the lint problems a rejected refresh lists.
const maxProblemsInDetails = 10

// sourceOf is the wire value of a catalog source. A catalog that did not load (LAI-CAT-001, the
// empty catalog) has none; it reports offline, the copy the node already has.
func sourceOf(s catalog.Source) string {
	if s == "" {
		return string(catalog.SourceOffline)
	}
	return string(s)
}

func (h *handlers) getCatalog(w http.ResponseWriter, r *http.Request) error {
	cs := h.Catalog.Get()
	cfg := h.configOf()
	lang := api.InfoFrom(r.Context()).Lang

	body := CatalogBody{
		Version: cs.Catalog.Version, Source: sourceOf(cs.Source), FetchedAt: timePtr(cs.FetchedAt),
		Models: []ModelRow{}, Content: []ContentRow{}, Profiles: []ProfileRow{},
	}
	if h.Models != nil {
		if rows := h.Models.Models(lang).Rows; rows != nil {
			body.Models = rows
		}
	}

	// in_profile follows the effective plan, so an item the budget dropped is not in the profile.
	inProfile := map[string]bool{}
	if plan, err := cs.Catalog.EffectivePlan(cfg.Profile, catalog.Overrides(cfg.ProfileOverrides)); err == nil {
		for _, id := range plan.Content {
			inProfile[id] = true
		}
	}
	for _, c := range cs.Catalog.Content {
		var cst ContentState
		if h.Content != nil {
			cst = h.Content.ContentState(c.ID)
		}
		status := cst.Status
		if status == "" {
			status = "absent"
		}
		name := c.DisplayName
		if name == "" {
			name = c.ID
		}
		body.Content = append(body.Content, ContentRow{
			ID: c.ID, Kind: c.Kind, DisplayName: name, Present: cst.Present, Verified: cst.Verified,
			InProfile: inProfile[c.ID], SizeBytes: c.ExpectedSizeBytes, Status: status,
		})
	}
	for _, p := range cs.Catalog.Profiles {
		body.Profiles = append(body.Profiles, ProfileRow{
			ID: p.ID, DisplayName: p.DisplayName, BudgetBytes: p.Storage.BudgetBytes,
			Include: ProfileInclude{Models: nonNil(p.Include.Models), Content: nonNil(p.Include.Content)},
		})
	}
	api.WriteJSON(w, r, http.StatusOK, body)
	return nil
}

// online reports whether a refresh may run: the node is heading to online mode (or its mode is not
// known, which the engine's absence means in a test or a minimal daemon).
func (h *handlers) online() (mode string, ok bool) {
	switch {
	case h.Mode != nil:
		m := h.Mode.Info().Effective
		return string(m), m == modes.Online
	case h.State != nil:
		m := h.State.Snapshot().Mode
		return string(m), m == state.ModeOnline
	}
	return "", true
}

type refreshRequest struct{}

func (h *handlers) refreshCatalog(w http.ResponseWriter, r *http.Request) error {
	var body refreshRequest
	if err := api.DecodeJSON(w, r, &body); err != nil {
		return err
	}
	info := api.InfoFrom(r.Context())
	if mode, ok := h.online(); !ok {
		return api.NewError(api.CodeNotInMode, http.StatusConflict, map[string]any{"mode": mode, "action": "catalog.refresh"})
	}
	if h.Refresher == nil {
		return api.NewError(api.CodeStarting, http.StatusServiceUnavailable, nil)
	}

	res, err := h.Refresher.Refresh(r.Context())
	if err != nil {
		return h.refreshError(r.Context(), info, err)
	}
	h.Catalog.Set(res.Loaded, h.now())

	out := CatalogRefreshBody{Version: res.Catalog.Version, Previous: strPtr(res.Previous), Changed: res.Changed, Source: sourceOf(res.Source)}
	if res.Changed {
		// The router's presets follow the catalog; a problem there never undoes the install.
		out.Warnings = regenerate(r.Context(), h.Regenerator)
	}
	h.auditWrite(info, audit.ActionCatalogRefresh, audit.ResultOK, "", map[string]string{
		"version": out.Version, "previous": res.Previous, "source": out.Source,
	})
	api.WriteJSON(w, r, http.StatusOK, out)
	return nil
}

// refreshError maps a failed refresh: a rejected catalog is 422 LAI-CAT-002 (the previous one is
// kept), anything else that stopped the fetch is 502 LAI-CLD-013 (the node keeps its catalog).
func (h *handlers) refreshError(ctx context.Context, info *api.RequestInfo, err error) error {
	var rej *catalog.RejectedError
	switch {
	case errors.As(err, &rej):
		problems := make([]map[string]string, 0, maxProblemsInDetails)
		for _, p := range rej.Report.Problems {
			if len(problems) == maxProblemsInDetails {
				break
			}
			problems = append(problems, map[string]string{"code": p.Code, "file": p.File, "path": p.Path, "message": p.Message})
		}
		h.auditWrite(info, audit.ActionCatalogRefresh, audit.ResultFail, catalog.CodeRejected, nil)
		return api.NewError(catalog.CodeRejected, http.StatusUnprocessableEntity, map[string]any{
			"version": rej.Report.Version, "problem_count": len(rej.Report.Problems), "problems": problems,
		})
	case ctx.Err() != nil:
		return ctx.Err()
	}
	reason := "fetch_failed"
	if errors.Is(err, catalog.ErrNoSource) {
		reason = "no_source"
	}
	h.log().Warn("catalog refresh failed", "reason", reason, "error", err.Error())
	h.auditWrite(info, audit.ActionCatalogRefresh, audit.ResultFail, doctor.CLD013, nil)
	return api.NewError(doctor.CLD013, http.StatusBadGateway, map[string]any{"reason": reason})
}
