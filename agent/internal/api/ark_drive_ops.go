package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/audit"
)

// Handlers of POST /ark/drives/{root_id}/eject, /init and /mount and PUT /ark/drives/{root_id}/profile
// (agent.md section 3.8.20a, ark-sync.md sections 3.5 and 4.1.5, SYNC-03). The routes decode, validate,
// call an ArkDriveOps and write the answer; the drive layer (internal/ark) does the work. internal/api
// imports nothing of it: ark implements the interface, the same seam as GET /ark/drives.
//
// Eject, init and mount are op routes; the profile route is admin (an operator over TCP gets 403 with
// details.pin_required before the handler runs). Every one is audited, whatever its result: they change
// what is mounted or what a drive is planned with.

// arkDriveTimeout is the deadline of the four routes (agent.md section 3.8, 60 s): an eject waits up to 30 s
// for the agent's own transfers and for the drive users before it answers.
const arkDriveTimeout = 60 * time.Second

// maxProfileIDLen bounds a profile id of the set-profile body.
const maxProfileIDLen = 128

// ArkDriveRequest is the common part of the four requests: which drive, the language of the answer, and the
// operation id (also in X-LocalAI-Op-Id).
type ArkDriveRequest struct {
	RootID string
	Lang   string
	OpID   string
}

// ArkEjectRequest is POST /ark/drives/{root_id}/eject as the route decoded it. Force is "eject anyway":
// unload the models whose only copy is on the drive instead of refusing.
type ArkEjectRequest struct {
	ArkDriveRequest
	Force bool
}

// ArkProfileRequest is PUT /ark/drives/{root_id}/profile as the route decoded it.
type ArkProfileRequest struct {
	ArkDriveRequest
	Profile string
}

// ArkProfileResult is the 200 body of the profile route.
type ArkProfileResult struct {
	RootID  string `json:"root_id"`
	Profile string `json:"profile"`
}

// ArkDriveOps does the four drive actions. Its errors are *Error values with the documented status and
// code (404 LAI-AGT-021 for an unknown drive, 409 LAI-AGT-020 with details.state, 409 LAI-ARK-009 with
// details.blockers, 422 LAI-CAT-003); a plain error is a bug and answers 500.
type ArkDriveOps interface {
	// Eject checks that nothing uses the drive and starts the unmount; nil means accepted (202).
	Eject(ctx context.Context, req ArkEjectRequest) error
	// Init and Mount return the drive object of GET /ark/drives as it is afterwards.
	Init(ctx context.Context, req ArkDriveRequest) (any, error)
	Mount(ctx context.Context, req ArkDriveRequest) (any, error)
	// SetProfile writes the profile into the drive's ROOT.json.
	SetProfile(ctx context.Context, req ArkProfileRequest) (ArkProfileResult, error)
}

// ArkDriveDeps are what the four routes need. Ops is required.
type ArkDriveDeps struct {
	Ops ArkDriveOps
	// Audit records ark.eject, ark.init, ark.mount and ark.profile.set; optional.
	Audit AuditWriter
	// NewOpID makes the id of an operation (op_ plus 12 base32 characters); nil uses the default.
	NewOpID func() string
	// Logger defaults to slog.Default.
	Logger *slog.Logger
}

// ArkDriveRoutes returns the four routes: POST eject, init and mount (op, 60 s) and PUT profile (admin,
// 60 s).
func ArkDriveRoutes(d ArkDriveDeps) []Route {
	return []Route{
		{Method: http.MethodPost, Pattern: "/ark/drives/{root_id}/eject", Auth: []Level{AuthOp}, Timeout: arkDriveTimeout, Handler: HandlerFunc(d.eject)},
		{Method: http.MethodPost, Pattern: "/ark/drives/{root_id}/init", Auth: []Level{AuthOp}, Timeout: arkDriveTimeout, Handler: HandlerFunc(d.init)},
		{Method: http.MethodPost, Pattern: "/ark/drives/{root_id}/mount", Auth: []Level{AuthOp}, Timeout: arkDriveTimeout, Handler: HandlerFunc(d.mount)},
		{Method: http.MethodPut, Pattern: "/ark/drives/{root_id}/profile", Auth: []Level{AuthAdmin}, Timeout: arkDriveTimeout, Handler: HandlerFunc(d.profile)},
	}
}

// RegisterArkDrive adds the four routes to reg (DefaultRegistry when nil). The wiring in internal/app calls
// it; the package does not register anything at init time.
func RegisterArkDrive(reg *Registry, d ArkDriveDeps) error {
	if d.Ops == nil {
		return errors.New("api: the ark drive routes need an operations implementation")
	}
	if reg == nil {
		reg = DefaultRegistry
	}
	return reg.Add(ArkDriveRoutes(d)...)
}

func (d ArkDriveDeps) opID() string {
	if d.NewOpID != nil {
		return d.NewOpID()
	}
	return servicesOpID()
}

func (d ArkDriveDeps) log() *slog.Logger {
	if d.Logger != nil {
		return d.Logger.With("component", "api")
	}
	return slog.Default().With("component", "api")
}

// request builds the common part from the path, and starts the operation (its id is on the response).
func (d ArkDriveDeps) request(w http.ResponseWriter, r *http.Request) (ArkDriveRequest, error) {
	root := strings.TrimSpace(r.PathValue("root_id"))
	if root == "" {
		return ArkDriveRequest{}, NewError(CodeInvalidRequest, http.StatusBadRequest, map[string]any{"field": "root_id", "reason": "invalid_value"})
	}
	opID := d.opID()
	SetOpID(w, opID)
	return ArkDriveRequest{RootID: root, Lang: InfoFrom(r.Context()).Lang, OpID: opID}, nil
}

// ejectBody is the optional body of the eject route.
type ejectBody struct {
	Force bool `json:"force"`
}

// arkEjecting is the 202 body of the eject route.
type arkEjecting struct {
	State string `json:"state"`
}

func (d ArkDriveDeps) eject(w http.ResponseWriter, r *http.Request) error {
	var body ejectBody
	if err := DecodeJSONOrEmpty(w, r, &body); err != nil {
		return err
	}
	req, err := d.request(w, r)
	if err != nil {
		return err
	}
	info := InfoFrom(r.Context())
	target := map[string]string{"root_id": req.RootID, "op_id": req.OpID, "force": strconv.FormatBool(body.Force)}
	if err := d.Ops.Eject(r.Context(), ArkEjectRequest{ArkDriveRequest: req, Force: body.Force}); err != nil {
		d.audit(info, audit.ActionArkEject, audit.ResultFail, errorCode(err), target)
		return err
	}
	d.audit(info, audit.ActionArkEject, audit.ResultOK, "", target)
	WriteJSON(w, r, http.StatusAccepted, arkEjecting{State: "ejecting"})
	return nil
}

func (d ArkDriveDeps) init(w http.ResponseWriter, r *http.Request) error {
	return d.driveAction(w, r, audit.ActionArkInit, d.Ops.Init)
}

func (d ArkDriveDeps) mount(w http.ResponseWriter, r *http.Request) error {
	return d.driveAction(w, r, audit.ActionArkMount, d.Ops.Mount)
}

// driveAction is the common body of init and mount: an empty JSON object in, the drive object out.
func (d ArkDriveDeps) driveAction(w http.ResponseWriter, r *http.Request, action string, do func(context.Context, ArkDriveRequest) (any, error)) error {
	var body struct{}
	if err := DecodeJSON(w, r, &body); err != nil {
		return err
	}
	req, err := d.request(w, r)
	if err != nil {
		return err
	}
	info := InfoFrom(r.Context())
	target := map[string]string{"root_id": req.RootID, "op_id": req.OpID}
	drive, err := do(r.Context(), req)
	if err != nil {
		d.audit(info, action, audit.ResultFail, errorCode(err), target)
		return err
	}
	d.audit(info, action, audit.ResultOK, "", target)
	WriteJSON(w, r, http.StatusOK, drive)
	return nil
}

// profileBody is the body of the profile route.
type profileBody struct {
	Profile string `json:"profile"`
}

func (d ArkDriveDeps) profile(w http.ResponseWriter, r *http.Request) error {
	var body profileBody
	if err := DecodeJSON(w, r, &body); err != nil {
		return err
	}
	if !validProfileID(body.Profile) {
		return NewError(CodeInvalidRequest, http.StatusBadRequest, map[string]any{"field": "profile", "reason": "invalid_value"})
	}
	req, err := d.request(w, r)
	if err != nil {
		return err
	}
	info := InfoFrom(r.Context())
	target := map[string]string{"root_id": req.RootID, "op_id": req.OpID, "profile": body.Profile}
	res, err := d.Ops.SetProfile(r.Context(), ArkProfileRequest{ArkDriveRequest: req, Profile: body.Profile})
	if err != nil {
		d.audit(info, audit.ActionArkProfileSet, audit.ResultFail, errorCode(err), target)
		return err
	}
	d.audit(info, audit.ActionArkProfileSet, audit.ResultOK, "", target)
	WriteJSON(w, r, http.StatusOK, res)
	return nil
}

// validProfileID accepts a non-empty id of printable ASCII without spaces, at most maxProfileIDLen long.
// The catalog decides whether it exists.
func validProfileID(id string) bool {
	if id == "" || len(id) > maxProfileIDLen {
		return false
	}
	for i := 0; i < len(id); i++ {
		if c := id[i]; c <= ' ' || c >= 0x7f {
			return false
		}
	}
	return true
}

func (d ArkDriveDeps) audit(info *RequestInfo, action string, result audit.Result, code string, target map[string]string) {
	if d.Audit == nil {
		return
	}
	ev := audit.Event{Action: action, Result: result, Code: code, Actor: AuditActor(info), Target: target, ReqID: info.ID}
	if err := d.Audit.Write(ev); err != nil {
		d.log().Warn("audit write failed", "action", action, "error", err.Error())
	}
}
