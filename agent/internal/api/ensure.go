package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/audit"
)

// Handlers of GET, POST and DELETE /ensure (agent.md section 3.8.33, rebuild-kit.md section 4.5, ADR-0010):
// the agent keeps the node complete. GET is the picture (state complete, installing, needs_you or off, the
// steps with their status, the progress and the one next thing to do, in plain words in the language of the
// request). POST starts a pass now (the button "Install what's missing", `aictl ensure`), or with dry_run
// only shows what it would do. DELETE cancels the running pass. The work is internal/ensure's; this package
// decodes, checks who may ask for what, and maps the engine's refusals to documented errors, so it imports
// nothing of the engine (the seam rule of the ark routes).

const (
	ensureReadTimeout  = 5 * time.Second
	ensureWriteTimeout = 10 * time.Second
	maxEnsureConfirm   = 8
	maxEnsureIDLen     = 64
)

// EnsureStartRequest is POST /ensure as the route decoded, checked and (for a Portal session) replaced by
// the safe default.
type EnsureStartRequest struct {
	DryRun bool
	// Confirm names the steps the caller confirms; only an admin* caller may send any.
	Confirm []string
	Actor   audit.Actor
	Lang    string
	OpID    string
}

// EnsureEngine is what the routes ask. The View values are marshalled as they are (ensure.View). Its errors
// are *Error values with the documented status and code (400 LAI-AGT-020 for a confirmation that names no
// step waiting for one, 409 LAI-SYN-005 while a sync run is active, 409 LAI-AGT-022 during a mode change); a
// plain error is a bug and answers 500.
type EnsureEngine interface {
	// EnsureView is the picture now.
	EnsureView(ctx context.Context, lang string) (any, error)
	// EnsureStart starts a pass (or plans one, for a dry run). running is true when a pass is running after
	// the call (started by it, or already running).
	EnsureStart(ctx context.Context, req EnsureStartRequest) (view any, running bool, err error)
	// EnsureCancel stops the running pass. running is true when one was running.
	EnsureCancel(ctx context.Context, lang string) (view any, running bool, err error)
}

// EnsureDeps are what the routes need. Engine is required.
type EnsureDeps struct {
	Engine EnsureEngine
	// NewOpID makes the id of an operation (op_ plus 12 base32 characters); nil uses the default.
	NewOpID func() string
}

// EnsureRoutes returns GET, POST and DELETE /ensure (op, or portal; confirm needs admin*).
func EnsureRoutes(d EnsureDeps) []Route {
	both := []Level{AuthOp, AuthPortal}
	return []Route{
		{Method: http.MethodGet, Pattern: "/ensure", Auth: both, Timeout: ensureReadTimeout, Handler: HandlerFunc(d.get)},
		{Method: http.MethodPost, Pattern: "/ensure", Auth: both, Timeout: ensureWriteTimeout, Handler: HandlerFunc(d.post)},
		{Method: http.MethodDelete, Pattern: "/ensure", Auth: both, Timeout: ensureWriteTimeout, Handler: HandlerFunc(d.delete)},
	}
}

// RegisterEnsure adds the routes to reg (DefaultRegistry when nil). The wiring in internal/app calls it.
func RegisterEnsure(reg *Registry, d EnsureDeps) error {
	if d.Engine == nil {
		return errors.New("api: the ensure routes need an engine")
	}
	if reg == nil {
		reg = DefaultRegistry
	}
	return reg.Add(EnsureRoutes(d)...)
}

func (d EnsureDeps) opID() string {
	if d.NewOpID != nil {
		return d.NewOpID()
	}
	return servicesOpID()
}

// ensureBody is the body of POST /ensure.
type ensureBody struct {
	DryRun  bool     `json:"dry_run"`
	Confirm []string `json:"confirm"`
}

func (d EnsureDeps) get(w http.ResponseWriter, r *http.Request) error {
	info := InfoFrom(r.Context())
	v, err := d.Engine.EnsureView(r.Context(), info.Lang)
	if err != nil {
		return err
	}
	WriteJSON(w, r, http.StatusOK, v)
	return nil
}

func (d EnsureDeps) post(w http.ResponseWriter, r *http.Request) error {
	info := InfoFrom(r.Context())
	opID := d.opID()
	SetOpID(w, opID)
	req := EnsureStartRequest{Actor: AuditActor(info), Lang: info.Lang, OpID: opID}
	if info.Principal.Kind != KindPortal {
		var body ensureBody
		if err := DecodeJSONOrEmpty(w, r, &body); err != nil {
			return err
		}
		if len(body.Confirm) > maxEnsureConfirm {
			return NewError(CodeInvalidRequest, http.StatusBadRequest, map[string]any{"field": "confirm", "reason": "too_many", "max": maxEnsureConfirm})
		}
		for _, id := range body.Confirm {
			if strings.TrimSpace(id) == "" || len(id) > maxEnsureIDLen {
				return NewError(CodeInvalidRequest, http.StatusBadRequest, map[string]any{"field": "confirm", "reason": "bad_value"})
			}
		}
		// Compiling and restarting a service that runs are decisions of a person at the machine, or with the
		// PIN: admin*. A dry run changes nothing and needs no more than op.
		if len(body.Confirm) > 0 && !body.DryRun && !info.Principal.Satisfies([]Level{AuthAdminStar}) {
			return NewError(CodeAdminRequired, http.StatusForbidden, map[string]any{"pin_required": true, "reason": "confirm"})
		}
		req.DryRun, req.Confirm = body.DryRun, body.Confirm
	}
	// A Portal session's body is ignored: it starts the automatic steps and nothing that needs a
	// confirmation (survival-portal.md section 3.3).
	v, running, err := d.Engine.EnsureStart(r.Context(), req)
	if err != nil {
		return err
	}
	status := http.StatusOK
	if running {
		status = http.StatusAccepted
	}
	WriteJSON(w, r, status, v)
	return nil
}

func (d EnsureDeps) delete(w http.ResponseWriter, r *http.Request) error {
	info := InfoFrom(r.Context())
	v, running, err := d.Engine.EnsureCancel(r.Context(), info.Lang)
	if err != nil {
		return err
	}
	status := http.StatusOK
	if running {
		status = http.StatusAccepted
	}
	WriteJSON(w, r, status, v)
	return nil
}
