package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/marinoscar/local-ai/agent/internal/audit"
)

// Handler of POST /ark/import (agent.md section 3.8.20, ark-sync.md sections 3.5 and 4.14): copy
// verified items from an AI-ARK drive to the internal root, with no network at all. It is allowed in
// every mode, including survival. The route decodes and validates the request and asks an ArkImporter;
// internal/sync/run/adapt implements it over the importer and the run engine, so this package imports
// neither.
//
// add_to_profile changes node.yaml, so it needs the same rights as the other node.yaml changes the UI
// makes: admin* (the socket's operators, or the PIN grant over TCP).

// ArkImportRequest is POST /ark/import as the route decoded and validated it.
type ArkImportRequest struct {
	// FromRoot is the root id of the source drive; empty means the only mounted drive.
	FromRoot string
	// Items limits the import to these catalog ids; empty means everything the profile wants that the
	// drive holds.
	Items []string
	// AddToProfile appends a named item that the profile does not list to node.yaml. It needs Items.
	AddToProfile bool
	// Catalog installs the drive's catalog first when it is newer and valid.
	Catalog bool
	DryRun  bool
	OpID    string
	Actor   audit.Actor
}

// ArkImporter plans and starts imports. Its errors are *Error values with the documented status and
// code (409 LAI-ARK-015 with details.roots when several drives are mounted and none was named, 400
// LAI-AGT-020 for a drive that is not mounted, 409 LAI-SYN-005 with details.run_id while another run is
// active, 409 LAI-ARK-001 when the internal root cannot be written, 422 LAI-CAT-003 for a profile the
// catalog lacks); a plain error is a bug and answers 500.
type ArkImporter interface {
	// DryRunImport answers the body of the 200: a JSON-encodable list with the plan of the internal
	// root (ark-sync.md section 4.4.4). It writes nothing and takes no run slot.
	DryRunImport(ctx context.Context, req ArkImportRequest) (plans any, err error)
	// StartImport starts the run (trigger import) and returns at once.
	StartImport(ctx context.Context, req ArkImportRequest) (ArkSyncStarted, error)
}

// ArkImportDeps are what the route needs. Importer is required.
type ArkImportDeps struct {
	Importer ArkImporter
	// Audit records ark.import for every import that starts (not for a dry run); optional.
	Audit AuditWriter
	// NewOpID makes the id of an operation (op_ plus 12 base32 characters); nil uses the default.
	NewOpID func() string
	// Logger defaults to slog.Default.
	Logger *slog.Logger
}

// ArkImportRoutes returns POST /ark/import (op, 10 s; add_to_profile needs admin*).
func ArkImportRoutes(d ArkImportDeps) []Route {
	return []Route{
		{Method: http.MethodPost, Pattern: "/ark/import", Auth: []Level{AuthOp}, Timeout: arkSyncTimeout, Handler: HandlerFunc(d.post)},
	}
}

// RegisterArkImport adds the route to reg (DefaultRegistry when nil). The wiring in internal/app calls
// it; the package does not register anything at init time.
func RegisterArkImport(reg *Registry, d ArkImportDeps) error {
	if d.Importer == nil {
		return errors.New("api: the ark import route needs an importer")
	}
	if reg == nil {
		reg = DefaultRegistry
	}
	return reg.Add(ArkImportRoutes(d)...)
}

func (d ArkImportDeps) opID() string {
	if d.NewOpID != nil {
		return d.NewOpID()
	}
	return servicesOpID()
}

func (d ArkImportDeps) log() *slog.Logger {
	if d.Logger != nil {
		return d.Logger.With("component", "api")
	}
	return slog.Default().With("component", "api")
}

// arkImportBody is the body of POST /ark/import.
type arkImportBody struct {
	FromRoot     string   `json:"from_root"`
	Items        []string `json:"items"`
	AddToProfile bool     `json:"add_to_profile"`
	Catalog      bool     `json:"catalog"`
	DryRun       bool     `json:"dry_run"`
}

func (d ArkImportDeps) post(w http.ResponseWriter, r *http.Request) error {
	var body arkImportBody
	if err := DecodeJSON(w, r, &body); err != nil {
		return err
	}
	body.FromRoot = strings.TrimSpace(body.FromRoot)
	if err := checkArkLists(map[string][]string{"items": body.Items}); err != nil {
		return err
	}
	if body.AddToProfile && len(body.Items) == 0 {
		// "Add everything on the drive to the profile" is never what a person means.
		return NewError(CodeInvalidRequest, http.StatusBadRequest, map[string]any{"field": "items", "reason": "required_with_add_to_profile"})
	}
	info := InfoFrom(r.Context())
	// Changing node.yaml needs admin*: an operator on the socket, or the PIN. A dry run changes nothing.
	if body.AddToProfile && !body.DryRun && !info.Principal.Satisfies([]Level{AuthAdminStar}) {
		return NewError(CodeAdminRequired, http.StatusForbidden, map[string]any{"pin_required": true, "reason": "add_to_profile"})
	}

	opID := d.opID()
	SetOpID(w, opID)
	req := ArkImportRequest{
		FromRoot: body.FromRoot, Items: body.Items, AddToProfile: body.AddToProfile, Catalog: body.Catalog, DryRun: body.DryRun,
		OpID: opID, Actor: AuditActor(info),
	}
	if body.DryRun {
		plans, err := d.Importer.DryRunImport(r.Context(), req)
		if err != nil {
			return err
		}
		WriteJSON(w, r, http.StatusOK, plans)
		return nil
	}
	started, err := d.Importer.StartImport(r.Context(), req)
	if err != nil {
		d.audit(info, audit.ResultFail, errorCode(err), req, "")
		return err
	}
	d.audit(info, audit.ResultOK, "", req, started.RunID)
	WriteJSON(w, r, http.StatusAccepted, arkRunAccepted(started))
	return nil
}

func (d ArkImportDeps) audit(info *RequestInfo, result audit.Result, code string, req ArkImportRequest, runID string) {
	if d.Audit == nil {
		return
	}
	target := map[string]string{
		"from_root":      req.FromRoot,
		"items":          strconv.Itoa(len(req.Items)),
		"add_to_profile": strconv.FormatBool(req.AddToProfile),
		"catalog":        strconv.FormatBool(req.Catalog),
		"op_id":          req.OpID,
	}
	if runID != "" {
		target["run_id"] = runID
	}
	ev := audit.Event{Action: audit.ActionArkImport, Result: result, Code: code, Actor: AuditActor(info), Target: target, ReqID: info.ID}
	if err := d.Audit.Write(ev); err != nil {
		d.log().Warn("audit write failed", "action", audit.ActionArkImport, "error", err.Error())
	}
}
