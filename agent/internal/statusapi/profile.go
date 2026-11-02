package statusapi

import (
	"errors"
	"net/http"

	"github.com/marinoscar/local-ai/agent/internal/api"
	"github.com/marinoscar/local-ai/agent/internal/audit"
	"github.com/marinoscar/local-ai/agent/internal/catalog"
	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/hardware"
	"gopkg.in/yaml.v3"
)

// maxRefsInDetails bounds the unknown ids an error lists.
const maxRefsInDetails = 20

// profileRequest is the body of PUT /profile. Either field may be absent or null: unchanged.
type profileRequest struct {
	Profile          *string           `json:"profile"`
	ProfileOverrides *overridesRequest `json:"profile_overrides"`
}

// overridesRequest is profile_overrides of the body: it replaces the whole block.
type overridesRequest struct {
	AddModels     []string `json:"add_models"`
	RemoveModels  []string `json:"remove_models"`
	AddContent    []string `json:"add_content"`
	RemoveContent []string `json:"remove_content"`
	BudgetBytes   *int64   `json:"budget_bytes"`
}

func (h *handlers) snapshot() hardware.Snapshot {
	if h.Hardware == nil {
		return hardware.Snapshot{}
	}
	return h.Hardware.Snapshot()
}

// profileBody builds the GET /profile body for a profile and overrides. A plan that cannot be
// computed is returned as the 422 error of agent.md section 3.8.30, whose details still carry the
// suggestion.
func (h *handlers) profileBody(profile string, ov catalog.Overrides) (ProfileBody, error) {
	view, err := h.Catalog.Get().Catalog.BuildProfileView(profile, ov, h.snapshot())
	if err != nil {
		return ProfileBody{}, profileError(view, err)
	}
	return ProfileBody{
		Profile: view.Profile, DisplayName: view.DisplayName, Overrides: view.Overrides,
		Suggested: strPtr(view.Suggested), MatchReasons: view.MatchReasons, Effective: view.Effective,
	}, nil
}

// profileError maps what the catalog refuses (an unknown profile, LAI-CAT-003; unknown ids,
// LAI-CAT-004) to 422 with the unknown references and the suggestion in details.
func profileError(view catalog.ProfileView, err error) error {
	var pe *catalog.ProfileError
	if !errors.As(err, &pe) {
		return err
	}
	details := map[string]any{
		"profile": pe.Profile, "suggested": strPtr(view.Suggested), "match_reasons": view.MatchReasons,
	}
	if len(pe.Refs) > 0 {
		refs := make([]map[string]string, 0, len(pe.Refs))
		for _, r := range pe.Refs {
			if len(refs) == maxRefsInDetails {
				break
			}
			refs = append(refs, map[string]string{"field": r.Field, "id": r.ID})
		}
		details["unknown"] = refs
	}
	return api.NewError(pe.Code(), http.StatusUnprocessableEntity, details)
}

func (h *handlers) getProfile(w http.ResponseWriter, r *http.Request) error {
	cfg := h.configOf()
	body, err := h.profileBody(cfg.Profile, catalog.Overrides(cfg.ProfileOverrides))
	if err != nil {
		return err
	}
	api.WriteJSON(w, r, http.StatusOK, body)
	return nil
}

func (h *handlers) putProfile(w http.ResponseWriter, r *http.Request) error {
	var req profileRequest
	if err := api.DecodeJSON(w, r, &req); err != nil {
		return err
	}
	info := api.InfoFrom(r.Context())
	cfg := h.configOf()
	profile, ov := cfg.Profile, catalog.Overrides(cfg.ProfileOverrides)
	if req.Profile != nil {
		if *req.Profile == "" {
			return api.NewError(api.CodeInvalidRequest, http.StatusBadRequest, map[string]any{"field": "profile", "reason": "invalid_value"})
		}
		profile = *req.Profile
	}
	if o := req.ProfileOverrides; o != nil {
		if o.BudgetBytes != nil && *o.BudgetBytes <= 0 {
			return api.NewError(api.CodeInvalidRequest, http.StatusBadRequest, map[string]any{
				"field": "profile_overrides.budget_bytes", "reason": "invalid_value",
			})
		}
		ov = catalog.Overrides{
			AddModels: o.AddModels, RemoveModels: o.RemoveModels, AddContent: o.AddContent,
			RemoveContent: o.RemoveContent, BudgetBytes: o.BudgetBytes,
		}
	}
	target := map[string]string{"profile": profile, "previous": cfg.Profile}

	// Validate against the installed catalog before anything is written.
	body, err := h.profileBody(profile, ov)
	if err != nil {
		var ae *api.Error
		code := ""
		if errors.As(err, &ae) {
			code = ae.Code
		}
		h.auditWrite(info, audit.ActionProfileSet, audit.ResultFail, code, target)
		return err
	}

	changed := profile != cfg.Profile || (req.ProfileOverrides != nil && !sameOverrides(ov, catalog.Overrides(cfg.ProfileOverrides)))
	var uerr error
	if changed {
		uerr = h.Config.Update(func(root *yaml.Node) error { return writeProfile(root, profile, ov, req.ProfileOverrides != nil) })
	}
	if err := uerr; err != nil {
		var ve *config.ValidationError
		if errors.As(err, &ve) {
			problems := make([]map[string]string, 0, len(ve.Problems))
			for _, p := range ve.Problems.Errors() {
				problems = append(problems, map[string]string{"path": p.Path, "message": p.Message})
			}
			h.auditWrite(info, audit.ActionProfileSet, audit.ResultFail, api.CodeInvalidRequest, target)
			return api.NewError(api.CodeInvalidRequest, http.StatusBadRequest, map[string]any{"reason": "node_yaml_invalid", "problems": problems})
		}
		if !errors.Is(err, config.ErrNotDurable) {
			h.auditWrite(info, audit.ActionProfileSet, audit.ResultFail, api.CodeInternal, target)
			return err
		}
		// The new node.yaml is in place and applied; only its fsync was not confirmed.
		h.log().Warn("node.yaml written but not confirmed durable", "error", err.Error())
	}

	body.Warnings = regenerate(r.Context(), h.Regenerator)
	h.auditWrite(info, audit.ActionProfileSet, audit.ResultOK, "", target)
	api.WriteJSON(w, r, http.StatusOK, body)
	return nil
}

// writeProfile sets profile and, when overridesGiven, the five profile_overrides keys, key by key so
// that the comments of node.yaml stay.
func writeProfile(root *yaml.Node, profile string, ov catalog.Overrides, overridesGiven bool) error {
	if err := config.SetKey(root, profile, "profile"); err != nil {
		return err
	}
	if !overridesGiven {
		return nil
	}
	lists := []struct {
		key string
		val []string
	}{
		{"add_models", ov.AddModels}, {"remove_models", ov.RemoveModels},
		{"add_content", ov.AddContent}, {"remove_content", ov.RemoveContent},
	}
	for _, l := range lists {
		v := l.val
		if v == nil {
			v = []string{}
		}
		if err := config.SetKey(root, v, "profile_overrides", l.key); err != nil {
			return err
		}
	}
	var budget any
	if ov.BudgetBytes != nil {
		budget = *ov.BudgetBytes
	}
	return config.SetKey(root, budget, "profile_overrides", "budget_bytes")
}

// sameOverrides compares two override sets, an absent list being an empty one.
func sameOverrides(a, b catalog.Overrides) bool {
	eq := func(x, y []string) bool {
		if len(x) != len(y) {
			return false
		}
		for i := range x {
			if x[i] != y[i] {
				return false
			}
		}
		return true
	}
	if (a.BudgetBytes == nil) != (b.BudgetBytes == nil) || (a.BudgetBytes != nil && *a.BudgetBytes != *b.BudgetBytes) {
		return false
	}
	return eq(a.AddModels, b.AddModels) && eq(a.RemoveModels, b.RemoveModels) && eq(a.AddContent, b.AddContent) && eq(a.RemoveContent, b.RemoveContent)
}
