package modelsapi

import (
	"context"
	"net/http"
	"strings"

	"github.com/marinoscar/local-ai/agent/internal/api"
	"github.com/marinoscar/local-ai/agent/internal/audit"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/modes"
)

// Limits of the roots a download names; far above any node (one internal root and a few drives).
const (
	maxRoots   = 16
	maxRootLen = 4096
)

// DownloadRequest is a download as the route decoded it.
type DownloadRequest struct {
	ID string
	// Roots are content roots as the caller named them, a path or a root id; empty is the default.
	Roots []string
	// AllowOfflineMode lets the download start in offline mode.
	AllowOfflineMode bool
	Actor            audit.Actor
}

// Download is POST /models/{id}/download: it queues a sync run for the one model and returns the
// run's id at once; the transfer, its verification and its progress belong to the sync engine and
// arrive on /events (sync.*). Errors: 404 LAI-AGT-021 (unknown model), 400 LAI-AGT-020 (bad roots),
// 409 LAI-SYN-010 (survival mode), 409 LAI-AGT-023 (offline mode without AllowOfflineMode), and the
// engine's: 409 LAI-SYN-005 (a run is active), 422 LAI-ARK-001 (no writable root).
func (s *Service) Download(ctx context.Context, req DownloadRequest) (DownloadAccepted, error) {
	cm, err := s.findModel(req.ID)
	if err != nil {
		return DownloadAccepted{}, err
	}
	if err := checkRoots(req.Roots, "roots"); err != nil {
		return DownloadAccepted{}, err
	}
	if err := s.downloadAllowed(req.AllowOfflineMode); err != nil {
		return DownloadAccepted{}, err
	}
	run, err := s.d.Sync.StartModelDownload(ctx, SyncDownload{
		Model: cm, Roots: append([]string(nil), req.Roots...), AllowOfflineMode: req.AllowOfflineMode, Actor: req.Actor,
	})
	if err != nil {
		return DownloadAccepted{}, syncErr(err, "roots")
	}
	return DownloadAccepted{ID: cm.ID, SyncRunID: run.ID, Queued: true}, nil
}

// downloadAllowed is the mode rule of a download (agent.md section 3.8.11): never in survival mode,
// in offline mode only when the caller allows it. A node heading to a mode counts as being in it.
func (s *Service) downloadAllowed(allowOffline bool) error {
	if s.d.Mode == nil {
		return nil
	}
	switch mode := s.d.Mode.Info().Effective; mode {
	case modes.Survival:
		return api.NewError(doctor.SYN010, http.StatusConflict, map[string]any{"mode": string(mode)})
	case modes.Offline:
		if !allowOffline {
			return api.NewError(api.CodeNotInMode, http.StatusConflict, map[string]any{
				"mode": string(mode), "requires": "allow_offline_mode",
			})
		}
	}
	return nil
}

// checkRoots validates the roots a request names before the engine resolves them.
func checkRoots(roots []string, field string) error {
	bad := func(reason string) error {
		return api.NewError(api.CodeInvalidRequest, http.StatusBadRequest, map[string]any{"field": field, "reason": reason})
	}
	if len(roots) > maxRoots {
		return bad("too_many")
	}
	for _, r := range roots {
		if r == "" || len(r) > maxRootLen || strings.ContainsRune(r, 0) {
			return bad("invalid_value")
		}
	}
	return nil
}

// downloadBody is the optional body of POST /models/{id}/download.
type downloadBody struct {
	Roots            []string `json:"roots"`
	AllowOfflineMode bool     `json:"allow_offline_mode"`
}

func (s *Service) downloadModel(w http.ResponseWriter, r *http.Request) error {
	var body downloadBody
	if err := api.DecodeJSONOrEmpty(w, r, &body); err != nil {
		return err
	}
	res, err := s.Download(r.Context(), DownloadRequest{
		ID: r.PathValue("id"), Roots: body.Roots, AllowOfflineMode: body.AllowOfflineMode,
		Actor: api.AuditActor(api.InfoFrom(r.Context())),
	})
	if err != nil {
		return err
	}
	api.WriteJSON(w, r, http.StatusAccepted, res)
	return nil
}
