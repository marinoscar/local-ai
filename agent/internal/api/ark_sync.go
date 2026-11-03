package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/audit"
)

// Handler of POST /ark/sync (agent.md section 3.8.18, ark-sync.md section 3.5). This file holds the
// dry-run branch (SYNC-05): `dry_run: true` answers 200 with the plan of every requested root and
// writes nothing. The route decodes and validates the request and calls the sync code through two
// small interfaces, so this package imports nothing of the planner or the engine: internal/sync/plan
// implements ArkSyncPlanner, and the run engine (SYNC-07) will implement ArkSyncStarter, the start
// of a real run (202), along with GET and DELETE /ark/sync, which are added to this file with it.

const (
	// arkSyncTimeout is the deadline of POST /ark/sync (agent.md section 3.8): a dry run reads
	// metadata only, and a real start answers 202 at once.
	arkSyncTimeout = 10 * time.Second
	// maxArkSyncList bounds the roots and items lists of a request.
	maxArkSyncList = 256
	// arkSyncEventsURL is the events URL a started run reports on (ark-sync.md section 3.5).
	arkSyncEventsURL = "/api/v1/events?types=sync.*"
)

// Triggers a request may carry (ark-sync.md section 3.5).
const (
	ArkTriggerManual     = "manual"
	ArkTriggerScheduled  = "scheduled"
	ArkTriggerCloud      = "cloud"
	ArkTriggerModeOnline = "mode_online"
	ArkTriggerInstall    = "install"
	ArkTriggerVerify     = "verify"
	ArkTriggerImport     = "import"
)

// ArkSyncStateRunning is the state of a started run in the 202 body.
const ArkSyncStateRunning = "running"

// ArkSyncRequest is POST /ark/sync as the route decoded and validated it.
type ArkSyncRequest struct {
	// Roots are root ids or paths; empty means every writable mounted root.
	Roots []string
	// Items are catalog ids; empty means the whole plan.
	Items            []string
	DryRun           bool
	AllowOfflineMode bool
	// Trigger is one of the Ark trigger constants; manual when the body had none.
	Trigger string
	// OpID is the operation id of the response header.
	OpID  string
	Actor audit.Actor
}

// ArkSyncPlanner answers a dry run. DryRun returns the body of the 200: a JSON-encodable list with
// one plan per root (ark-sync.md section 4.4.4). It reads and computes and must write nothing. Its
// errors are *Error values with the documented status and code (400 LAI-AGT-020 for an unknown root
// or item, 409 LAI-ARK-001 when no root is writable, 422 LAI-CAT-003 for a profile the catalog
// lacks); a plain error is a bug and answers 500.
type ArkSyncPlanner interface {
	DryRun(ctx context.Context, req ArkSyncRequest) (plans any, err error)
}

// ArkSyncStarter starts a real run (SYNC-07). Its errors are *Error values too (409 LAI-SYN-005
// with details.run_id, 409 LAI-SYN-010 and LAI-SYN-011 for the mode rules, 409 LAI-ARK-001).
type ArkSyncStarter interface {
	Start(ctx context.Context, req ArkSyncRequest) (ArkSyncStarted, error)
}

// ArkSyncStarted is a run that began.
type ArkSyncStarted struct {
	// RunID is run-YYYYMMDDTHHMMSSZ-<4 hex>.
	RunID string
}

// ArkSyncDeps are what the sync routes need. Planner is required; Starter is nil until the run
// engine exists, and a real (non dry-run) start then answers 503 LAI-AGT-024.
type ArkSyncDeps struct {
	Planner ArkSyncPlanner
	Starter ArkSyncStarter
	// NewOpID makes the id of an operation (op_ plus 12 base32 characters); nil uses the default.
	NewOpID func() string
}

// ArkSyncRoutes returns POST /ark/sync (op, 10 s).
func ArkSyncRoutes(d ArkSyncDeps) []Route {
	return []Route{
		{Method: http.MethodPost, Pattern: "/ark/sync", Auth: []Level{AuthOp}, Timeout: arkSyncTimeout, Handler: HandlerFunc(d.post)},
	}
}

// RegisterArkSync adds the sync routes to reg (DefaultRegistry when nil). The wiring in internal/app
// calls it; the package does not register anything at init time.
func RegisterArkSync(reg *Registry, d ArkSyncDeps) error {
	if d.Planner == nil {
		return errors.New("api: the ark sync routes need a planner")
	}
	if reg == nil {
		reg = DefaultRegistry
	}
	return reg.Add(ArkSyncRoutes(d)...)
}

func (d ArkSyncDeps) opID() string {
	if d.NewOpID != nil {
		return d.NewOpID()
	}
	return servicesOpID()
}

// arkSyncBody is the body of POST /ark/sync.
type arkSyncBody struct {
	Roots            []string `json:"roots"`
	Items            []string `json:"items"`
	DryRun           bool     `json:"dry_run"`
	AllowOfflineMode bool     `json:"allow_offline_mode"`
	Trigger          string   `json:"trigger"`
}

type arkSyncAccepted struct {
	RunID  string `json:"run_id"`
	State  string `json:"state"`
	Events string `json:"events"`
}

func (d ArkSyncDeps) post(w http.ResponseWriter, r *http.Request) error {
	var body arkSyncBody
	if err := DecodeJSON(w, r, &body); err != nil {
		return err
	}
	switch body.Trigger {
	case "":
		body.Trigger = ArkTriggerManual
	case ArkTriggerManual, ArkTriggerScheduled, ArkTriggerCloud, ArkTriggerModeOnline, ArkTriggerInstall, ArkTriggerVerify, ArkTriggerImport:
	default:
		return NewError(CodeInvalidRequest, http.StatusBadRequest, map[string]any{
			"field": "trigger", "reason": "unknown_trigger",
			"allowed": []string{ArkTriggerManual, ArkTriggerScheduled, ArkTriggerCloud, ArkTriggerModeOnline, ArkTriggerInstall, ArkTriggerVerify, ArkTriggerImport},
		})
	}
	for _, l := range []struct {
		field string
		list  []string
	}{{"roots", body.Roots}, {"items", body.Items}} {
		if len(l.list) > maxArkSyncList {
			return NewError(CodeInvalidRequest, http.StatusBadRequest, map[string]any{"field": l.field, "reason": "too_many", "max": maxArkSyncList})
		}
		for _, v := range l.list {
			if strings.TrimSpace(v) == "" {
				return NewError(CodeInvalidRequest, http.StatusBadRequest, map[string]any{"field": l.field, "reason": "empty_value"})
			}
		}
	}

	info := InfoFrom(r.Context())
	opID := d.opID()
	SetOpID(w, opID)
	req := ArkSyncRequest{
		Roots: body.Roots, Items: body.Items, DryRun: body.DryRun, AllowOfflineMode: body.AllowOfflineMode,
		Trigger: body.Trigger, OpID: opID, Actor: AuditActor(info),
	}

	if body.DryRun {
		plans, err := d.Planner.DryRun(r.Context(), req)
		if err != nil {
			return err
		}
		WriteJSON(w, r, http.StatusOK, plans)
		return nil
	}
	if d.Starter == nil {
		// The run engine is not wired yet: the agent can plan but not sync.
		return NewError(CodeStarting, http.StatusServiceUnavailable, map[string]any{"reason": "sync_engine_not_ready"})
	}
	started, err := d.Starter.Start(r.Context(), req)
	if err != nil {
		return err
	}
	WriteJSON(w, r, http.StatusAccepted, arkSyncAccepted{RunID: started.RunID, State: ArkSyncStateRunning, Events: arkSyncEventsURL})
	return nil
}
