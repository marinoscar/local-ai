package modelsapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/marinoscar/local-ai/agent/internal/api"
	"github.com/marinoscar/local-ai/agent/internal/audit"
	"github.com/marinoscar/local-ai/agent/internal/catalog"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
)

// SyncEngine is what the model routes need from the ark sync engine (ark-sync.md, SYNC-05 to
// SYNC-08). The engine owns the rules that make a download safe (resumable, sha256 verified before
// the atomic swap, disk-full handling, the run slot, events with progress on /events) and the rule
// that keeps the last verified copy (retention.CanDelete, section 4.10); these routes only ask and
// map its typed errors. Until the engine exists the daemon passes a fake or leaves the routes out.
type SyncEngine interface {
	// StartModelDownload queues a sync run of trigger manual (or cloud, for an Actor of kind cloud)
	// with items: [Model.ID] and returns at once with the run's id (run-<ts>-<hex>); progress is the
	// sync.* events. Roots are content roots as the caller named them, a path or a root id; empty
	// means the default (the internal root, plus the drive when its profile includes the model).
	//
	// Errors: *RunActiveError (a run is already active), *UnknownRootError (a root is not mounted),
	// ErrNoWritableRoot (there is nowhere to write). The mode rules are the caller's.
	StartModelDownload(ctx context.Context, req SyncDownload) (SyncRun, error)

	// DeleteModel deletes the model's verified copies on the named roots (empty Roots: every mounted
	// root), one root at a time, and reports each. With KeepLast it calls the retention rule before
	// each root, so a copy whose removal would leave no verified copy on any mounted root is left in
	// place and reported in Kept with the reason last_verified_copy; the copies it may delete are
	// deleted (a model on two roots loses one copy and keeps the other). A root that holds no copy
	// is reported in Kept with the reason not_present. Without KeepLast the retention rule is not
	// consulted. Every deletion goes through retention.CanDelete's code path, never a plain remove.
	//
	// Errors: *UnknownRootError. The caller holds modelOpsMu and has checked that the model is not
	// loaded.
	DeleteModel(ctx context.Context, req SyncDelete) (SyncDeleteResult, error)
}

// SyncDownload is a queued download as the route decoded it.
type SyncDownload struct {
	Model *catalog.Model
	Roots []string
	// AllowOfflineMode is true when the caller said so in offline mode (the route has checked the
	// mode already; the engine records it in the run's preflight).
	AllowOfflineMode bool
	Actor            audit.Actor
}

// SyncRun is a started run.
type SyncRun struct {
	// ID is the run id, run-<ts>-<hex> (ark-sync.md section 3.5).
	ID string
}

// SyncDelete is a delete as the route decoded it.
type SyncDelete struct {
	Model *catalog.Model
	// Roots is the one root the caller named (a path or a root id), or nil for every mounted root.
	Roots    []string
	KeepLast bool
	// OpID is the modelOpsMu operation the delete runs under; the engine puts it in its log lines.
	OpID  string
	Actor audit.Actor
}

// DeletedCopy is a copy that was removed.
type DeletedCopy struct {
	Root  string `json:"root"`
	Bytes int64  `json:"bytes"`
}

// Reasons of a KeptCopy.
const (
	// KeptLastVerifiedCopy: removing the copy would have left no verified copy (keep_last).
	KeptLastVerifiedCopy = "last_verified_copy"
	// KeptNotPresent: the root holds no copy of the model.
	KeptNotPresent = "not_present"
)

// KeptCopy is a root whose copy was left alone, and why.
type KeptCopy struct {
	Root   string `json:"root"`
	Reason string `json:"reason"`
}

// SyncDeleteResult is what SyncEngine.DeleteModel did.
type SyncDeleteResult struct {
	Deleted []DeletedCopy
	Kept    []KeptCopy
}

// RunActiveError is returned by the engine when a sync run is already active (LAI-SYN-005).
type RunActiveError struct{ RunID string }

func (e *RunActiveError) Error() string { return "modelsapi: a sync run is already active: " + e.RunID }

// UnknownRootError is returned by the engine for a root that is not a mounted content root, neither
// as a path nor as a root id.
type UnknownRootError struct{ Root string }

func (e *UnknownRootError) Error() string { return "modelsapi: not a mounted content root: " + e.Root }

// ErrNoWritableRoot is returned by the engine when no mounted root can be written (LAI-ARK-001).
var ErrNoWritableRoot = errors.New("modelsapi: there is no writable content root")

// syncErr maps what the engine returned to the *api.Error of the route. field names the request
// field a root came from ("roots" for a download, "root" for a delete).
func syncErr(err error, field string) error {
	var active *RunActiveError
	var unknown *UnknownRootError
	switch {
	case errors.As(err, &active):
		d := map[string]any{}
		if active.RunID != "" {
			d["run_id"] = active.RunID
		}
		return api.NewError(doctor.SYN005, http.StatusConflict, d)
	case errors.As(err, &unknown):
		return api.NewError(api.CodeInvalidRequest, http.StatusBadRequest, map[string]any{
			"field": field, "reason": "unknown_root", "value": unknown.Root,
		})
	case errors.Is(err, ErrNoWritableRoot):
		return api.NewError(doctor.ARK001, http.StatusUnprocessableEntity, nil)
	}
	return err
}
