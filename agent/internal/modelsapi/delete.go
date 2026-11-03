package modelsapi

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/marinoscar/local-ai/agent/internal/api"
	"github.com/marinoscar/local-ai/agent/internal/audit"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/router"
	"github.com/marinoscar/local-ai/agent/internal/statusapi"
)

// DeleteRequest is a delete as the route decoded it.
type DeleteRequest struct {
	ID string
	// Root is the one content root to delete from (a path or a root id); "" means every mounted root.
	Root string
	// KeepLast refuses to remove the last verified copy. The route's default is true; a caller that
	// sets it false (aictl delete --allow-last-copy, after a second confirmation) accepts the loss.
	// The cloud executor always passes true.
	KeepLast bool
	Actor    audit.Actor
}

// Delete is DELETE /models/{id}. It takes modelOpsMu without waiting, refuses a model the router
// holds in memory, asks the sync engine to delete the copies (the engine applies keep-last and does
// the removal), writes the model.delete audit line and regenerates models.ini. The sync engine's
// verdict decides what happens to each root:
//
//   - some copies deleted: 200 listing them (and, in kept, the roots left alone);
//   - nothing deleted because the only verified copy would go (KeepLast): 409 LAI-SYN-044;
//   - nothing to delete (no root holds a copy): 200 with an empty deleted list, as a repeated delete
//     is answered (agent.md section 3.7, idempotency).
//
// Errors: 404 LAI-AGT-021, 400 LAI-AGT-020 (an unknown root), 409 LAI-AGT-022 (another model operation
// holds the lock, details.op_id; or the model is loaded, details.reason "loaded"), 409 LAI-SYN-044.
func (s *Service) Delete(ctx context.Context, req DeleteRequest) (DeleteResult, error) {
	cm, err := s.findModel(req.ID)
	if err != nil {
		return DeleteResult{}, err
	}
	var roots []string
	if req.Root != "" {
		roots = []string{req.Root}
		if err := checkRoots(roots, "root"); err != nil {
			return DeleteResult{}, err
		}
	}
	target := map[string]string{"model": cm.ID, "keep_last": strconv.FormatBool(req.KeepLast)}
	if req.Root != "" {
		target["root"] = req.Root
	}

	hold, err := s.d.Lock.TryAcquire(router.HolderDelete)
	if err != nil {
		e := busyErr(err)
		s.auditWrite(ctx, audit.ActionModelDelete, audit.ResultFail, codeOf(e), "", req.Actor, target)
		return DeleteResult{}, e
	}
	held := true
	release := func() {
		if held {
			held = false
			hold.Release()
		}
	}
	defer release()

	// Under the lock no load can start, so the router view tells whether the files are in use.
	if st := s.statusOf(ctx, cm.ID); resident(st) {
		e := api.NewError(api.CodeBusy, http.StatusConflict, map[string]any{
			"model": cm.ID, "reason": "loaded", "status": st, "op_id": hold.OpID,
		}).WithMessage(loadedMessage(lang(ctx), cm.ID))
		s.auditWrite(ctx, audit.ActionModelDelete, audit.ResultFail, e.Code, hold.OpID, req.Actor, target)
		return DeleteResult{}, e
	}

	run, err := s.d.Sync.DeleteModel(ctx, SyncDelete{Model: cm, Roots: roots, KeepLast: req.KeepLast, OpID: hold.OpID, Actor: req.Actor})
	if err != nil {
		e := syncErr(err, "root")
		s.auditWrite(ctx, audit.ActionModelDelete, audit.ResultFail, codeOf(e), hold.OpID, req.Actor, target)
		return DeleteResult{}, e
	}

	out := DeleteResult{ID: cm.ID, Deleted: append([]DeletedCopy{}, run.Deleted...), OpID: hold.OpID}
	if len(run.Kept) > 0 {
		out.Kept = append([]KeptCopy{}, run.Kept...)
	}
	if req.KeepLast && len(out.Deleted) == 0 && slices.ContainsFunc(out.Kept, func(k KeptCopy) bool { return k.Reason == KeptLastVerifiedCopy }) {
		e := api.NewError(doctor.SYN044, http.StatusConflict, map[string]any{
			"model": cm.ID, "kept": out.Kept, "op_id": hold.OpID,
		})
		s.auditWrite(ctx, audit.ActionModelDelete, audit.ResultDenied, e.Code, hold.OpID, req.Actor, target)
		return DeleteResult{}, e
	}

	if len(out.Deleted) > 0 {
		deleted := make([]string, 0, len(out.Deleted))
		for _, d := range out.Deleted {
			deleted = append(deleted, d.Root)
		}
		target["roots"] = strings.Join(deleted, ",")
	}
	s.auditWrite(ctx, audit.ActionModelDelete, audit.ResultOK, "", hold.OpID, req.Actor, target)

	// The files are gone: models.ini loses the model's section and the router is told (llama-router.md
	// section 4.2). The generator takes modelOpsMu itself when it must unload something, so the lock
	// is given back first.
	release()
	if len(out.Deleted) > 0 && cm.Engine == "llama" {
		out.Warnings = statusapi.Regenerate(ctx, s.d.Regenerator)
	}
	return out, nil
}

// statusOf is the status of a model in the merged view ("" when the view does not list it).
func (s *Service) statusOf(ctx context.Context, id string) string {
	for _, row := range s.d.Models.Models(lang(ctx)).Rows {
		if row.ID == id {
			return row.Status
		}
	}
	return ""
}

// resident reports whether the router holds the model in memory or is bringing it there.
func resident(status string) bool {
	switch status {
	case statusapi.ModelLoaded, statusapi.ModelLoading, statusapi.ModelSleeping:
		return true
	}
	return false
}

// loadedMessage is the message and fix of the refusal to delete a loaded model.
func loadedMessage(lng, id string) (string, string) {
	if lng == doctor.LangES {
		return "El modelo " + id + " está cargado, así que no se puede borrar.", "Descárguelo primero."
	}
	return "The model " + id + " is loaded, so it cannot be deleted.", "Unload it first."
}

// busyErr maps a refused TryAcquire to 409 LAI-AGT-022 with the holder's op id.
func busyErr(err error) error {
	var busy *router.BusyError
	if errors.As(err, &busy) {
		d := map[string]any{}
		if busy.OpID != "" {
			d["op_id"] = busy.OpID
		}
		if busy.Kind != "" {
			d["running"] = busy.Kind
		}
		return api.NewError(api.CodeBusy, http.StatusConflict, d)
	}
	return err
}

// codeOf is the doctor code an error is answered with: the API error's own, and LAI-AGT-099 for
// anything else (a bug, or the caller's context error), as the server classifies it.
func codeOf(err error) string {
	var e *api.Error
	switch {
	case err == nil:
		return ""
	case errors.As(err, &e):
		return e.Code
	}
	return api.CodeInternal
}

func (s *Service) deleteModel(w http.ResponseWriter, r *http.Request) error {
	keepLast, err := flag(r, "keep_last", true)
	if err != nil {
		return err
	}
	res, err := s.Delete(r.Context(), DeleteRequest{
		ID: r.PathValue("id"), Root: r.URL.Query().Get("root"), KeepLast: keepLast,
		Actor: api.AuditActor(api.InfoFrom(r.Context())),
	})
	if err != nil {
		return err
	}
	api.SetOpID(w, res.OpID)
	api.WriteJSON(w, r, http.StatusOK, res)
	return nil
}
