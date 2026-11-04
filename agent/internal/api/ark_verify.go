package api

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/marinoscar/local-ai/agent/internal/audit"
)

// Handler of POST /ark/verify (agent.md section 3.8.19, ark-sync.md sections 3.5 and 4.13): start a run
// of trigger verify that re-hashes what is stored. It shares the run slot with sync (one run at a
// time: 409 LAI-SYN-005) and is allowed in every mode, because it reads local disks only. The route
// decodes and validates the request and asks an ArkVerifyStarter; internal/sync/run/adapt implements it
// over the run engine, so this package imports nothing of the engine.

// ArkVerifyRequest is POST /ark/verify as the route decoded and validated it.
type ArkVerifyRequest struct {
	// Roots are root ids or paths; empty means every mounted root that can be read.
	Roots []string
	// Items limits the check to these catalog ids; a named item is checked whatever its age.
	Items []string
	// Full ignores reverify_days and checks everything.
	Full  bool
	OpID  string
	Actor audit.Actor
}

// ArkVerifyStarter starts a verify run. Its errors are *Error values with the documented status and
// code (409 LAI-SYN-005 with details.run_id, 400 LAI-AGT-020 for a root that is not mounted, 409
// LAI-ARK-001 when no root can be read); a plain error is a bug and answers 500.
type ArkVerifyStarter interface {
	StartVerify(ctx context.Context, req ArkVerifyRequest) (ArkSyncStarted, error)
}

// ArkVerifyDeps are what the route needs. Starter is required.
type ArkVerifyDeps struct {
	Starter ArkVerifyStarter
	// NewOpID makes the id of an operation (op_ plus 12 base32 characters); nil uses the default.
	NewOpID func() string
}

// ArkVerifyRoutes returns POST /ark/verify (op, 10 s).
func ArkVerifyRoutes(d ArkVerifyDeps) []Route {
	return []Route{
		{Method: http.MethodPost, Pattern: "/ark/verify", Auth: []Level{AuthOp}, Timeout: arkSyncTimeout, Handler: HandlerFunc(d.post)},
	}
}

// RegisterArkVerify adds the route to reg (DefaultRegistry when nil). The wiring in internal/app calls
// it; the package does not register anything at init time.
func RegisterArkVerify(reg *Registry, d ArkVerifyDeps) error {
	if d.Starter == nil {
		return errors.New("api: the ark verify route needs a starter")
	}
	if reg == nil {
		reg = DefaultRegistry
	}
	return reg.Add(ArkVerifyRoutes(d)...)
}

func (d ArkVerifyDeps) opID() string {
	if d.NewOpID != nil {
		return d.NewOpID()
	}
	return servicesOpID()
}

// arkVerifyBody is the body of POST /ark/verify.
type arkVerifyBody struct {
	Roots []string `json:"roots"`
	Items []string `json:"items"`
	Full  bool     `json:"full"`
}

func (d ArkVerifyDeps) post(w http.ResponseWriter, r *http.Request) error {
	var body arkVerifyBody
	if err := DecodeJSON(w, r, &body); err != nil {
		return err
	}
	if err := checkArkLists(map[string][]string{"roots": body.Roots, "items": body.Items}); err != nil {
		return err
	}
	info := InfoFrom(r.Context())
	opID := d.opID()
	SetOpID(w, opID)
	started, err := d.Starter.StartVerify(r.Context(), ArkVerifyRequest{
		Roots: body.Roots, Items: body.Items, Full: body.Full, OpID: opID, Actor: AuditActor(info),
	})
	if err != nil {
		return err
	}
	WriteJSON(w, r, http.StatusAccepted, arkRunAccepted(started))
	return nil
}

// arkRunAccepted is the 202 body of a run that was started: the same shape as POST /ark/sync's.
func arkRunAccepted(s ArkSyncStarted) arkSyncAccepted {
	state := s.State
	if state == "" {
		state = ArkSyncStateRunning
	}
	return arkSyncAccepted{RunID: s.RunID, State: state, Reason: s.Reason, Code: s.Code, Events: arkSyncEventsURL}
}

// checkArkLists rejects a list that is too long or holds an empty value (400 LAI-AGT-020). The fields
// are checked in name order, so the first error is stable.
func checkArkLists(lists map[string][]string) error {
	for _, field := range []string{"roots", "items"} {
		list, ok := lists[field]
		if !ok {
			continue
		}
		if len(list) > maxArkSyncList {
			return NewError(CodeInvalidRequest, http.StatusBadRequest, map[string]any{"field": field, "reason": "too_many", "max": maxArkSyncList})
		}
		for _, v := range list {
			if strings.TrimSpace(v) == "" {
				return NewError(CodeInvalidRequest, http.StatusBadRequest, map[string]any{"field": field, "reason": "empty_value"})
			}
		}
	}
	return nil
}
