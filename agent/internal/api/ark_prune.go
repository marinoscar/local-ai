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

// Handler of POST /ark/prune (agent.md section 3.8.20a, ark-sync.md sections 3.5 and 4.10): delete the
// orphans and the trash of the content roots, on request. The route is admin: an operator over TCP gets
// 403 with details.pin_required before the handler runs, root and an operator with a PIN grant do not.
// The route decodes and validates the request and asks an ArkPruner; this package imports nothing of
// retention or the planner (internal/sync/run/adapt implements the interface over both), so the rules
// of rule R1 and R6 live in one place only and cannot be reworded here. There is no field and no flag
// that deletes the last verified copy of an item.

const (
	// arkPruneTimeout is the deadline of POST /ark/prune (agent.md section 3.8, 60 s): a prune plans
	// (metadata only) and deletes, and a big model directory is a unlink per file.
	arkPruneTimeout = 60 * time.Second
	// maxArkPruneRoots bounds the roots list of a request.
	maxArkPruneRoots = 32
)

// ArkPruneRequest is POST /ark/prune as the route decoded and validated it.
type ArkPruneRequest struct {
	// Roots are root ids or paths; empty means every writable mounted root.
	Roots []string
	// Orphans and Trash say what to delete; at least one is set.
	Orphans, Trash bool
	DryRun         bool
	OpID           string
	Actor          audit.Actor
}

// ArkPruned is one entry that was deleted (or, in a dry run, would be).
type ArkPruned struct {
	Root    string `json:"root"`
	Item    string `json:"item"`
	Version string `json:"version,omitempty"`
	Bytes   int64  `json:"bytes"`
	// Kind is "orphan" or "trash".
	Kind string `json:"kind"`
}

// ArkPruneRefused is one entry that was kept, and why: last_verified_copy (rule R1 and R6),
// read_only (R8) or replacement_unverified (R3).
type ArkPruneRefused struct {
	Root    string `json:"root"`
	Item    string `json:"item"`
	Version string `json:"version,omitempty"`
	Reason  string `json:"reason"`
	// Kind is "orphan" or "trash".
	Kind string `json:"kind,omitempty"`
}

// ArkPruneResult is the 200 body.
type ArkPruneResult struct {
	Deleted    []ArkPruned       `json:"deleted"`
	Refused    []ArkPruneRefused `json:"refused"`
	FreedBytes int64             `json:"freed_bytes"`
}

// ArkPruner does the prune. Its errors are *Error values with the documented status and code (409
// LAI-SYN-005 while a sync run is active, 400 LAI-AGT-020 for a root that is not mounted, 409
// LAI-ARK-001 when no root is writable); a plain error is a bug and answers 500.
type ArkPruner interface {
	Prune(ctx context.Context, req ArkPruneRequest) (ArkPruneResult, error)
}

// ArkPruneDeps are what the prune route needs. Pruner is required.
type ArkPruneDeps struct {
	Pruner ArkPruner
	// Audit records ark.prune for every request that deletes (not for a dry run); optional.
	Audit AuditWriter
	// NewOpID makes the id of an operation (op_ plus 12 base32 characters); nil uses the default.
	NewOpID func() string
	// Logger defaults to slog.Default.
	Logger *slog.Logger
}

// ArkPruneRoutes returns POST /ark/prune (admin, 60 s).
func ArkPruneRoutes(d ArkPruneDeps) []Route {
	return []Route{
		{Method: http.MethodPost, Pattern: "/ark/prune", Auth: []Level{AuthAdmin}, Timeout: arkPruneTimeout, Handler: HandlerFunc(d.post)},
	}
}

// RegisterArkPrune adds the prune route to reg (DefaultRegistry when nil). The wiring in internal/app
// calls it; the package does not register anything at init time.
func RegisterArkPrune(reg *Registry, d ArkPruneDeps) error {
	if d.Pruner == nil {
		return errors.New("api: the ark prune route needs a pruner")
	}
	if reg == nil {
		reg = DefaultRegistry
	}
	return reg.Add(ArkPruneRoutes(d)...)
}

func (d ArkPruneDeps) opID() string {
	if d.NewOpID != nil {
		return d.NewOpID()
	}
	return servicesOpID()
}

func (d ArkPruneDeps) log() *slog.Logger {
	if d.Logger != nil {
		return d.Logger.With("component", "api")
	}
	return slog.Default().With("component", "api")
}

// arkPruneBody is the body of POST /ark/prune. There is deliberately no field for the last copy.
type arkPruneBody struct {
	Roots   []string `json:"roots"`
	Orphans bool     `json:"orphans"`
	Trash   bool     `json:"trash"`
	DryRun  bool     `json:"dry_run"`
}

func (d ArkPruneDeps) post(w http.ResponseWriter, r *http.Request) error {
	var body arkPruneBody
	if err := DecodeJSON(w, r, &body); err != nil {
		return err
	}
	if len(body.Roots) > maxArkPruneRoots {
		return NewError(CodeInvalidRequest, http.StatusBadRequest, map[string]any{"field": "roots", "reason": "too_many", "max": maxArkPruneRoots})
	}
	for _, v := range body.Roots {
		if strings.TrimSpace(v) == "" {
			return NewError(CodeInvalidRequest, http.StatusBadRequest, map[string]any{"field": "roots", "reason": "empty_value"})
		}
	}
	if !body.Orphans && !body.Trash {
		// A prune that names nothing is a mistake, not "everything".
		return NewError(CodeInvalidRequest, http.StatusBadRequest, map[string]any{"field": "orphans", "reason": "nothing_selected"})
	}

	info := InfoFrom(r.Context())
	opID := d.opID()
	SetOpID(w, opID)
	req := ArkPruneRequest{
		Roots: body.Roots, Orphans: body.Orphans, Trash: body.Trash, DryRun: body.DryRun,
		OpID: opID, Actor: AuditActor(info),
	}
	res, err := d.Pruner.Prune(r.Context(), req)
	if err != nil {
		if !body.DryRun {
			d.audit(info, audit.ResultFail, errorCode(err), req, nil)
		}
		return err
	}
	if res.Deleted == nil {
		res.Deleted = []ArkPruned{}
	}
	if res.Refused == nil {
		res.Refused = []ArkPruneRefused{}
	}
	if !body.DryRun {
		d.audit(info, audit.ResultOK, "", req, &res)
	}
	WriteJSON(w, r, http.StatusOK, res)
	return nil
}

func (d ArkPruneDeps) audit(info *RequestInfo, result audit.Result, code string, req ArkPruneRequest, res *ArkPruneResult) {
	if d.Audit == nil {
		return
	}
	target := map[string]string{
		"roots":   strings.Join(req.Roots, ","),
		"orphans": strconv.FormatBool(req.Orphans),
		"trash":   strconv.FormatBool(req.Trash),
		"op_id":   req.OpID,
	}
	if res != nil {
		target["deleted"] = strconv.Itoa(len(res.Deleted))
		target["refused"] = strconv.Itoa(len(res.Refused))
		target["freed_bytes"] = strconv.FormatInt(res.FreedBytes, 10)
	}
	ev := audit.Event{Action: audit.ActionArkPrune, Result: result, Code: code, Actor: AuditActor(info), Target: target, ReqID: info.ID}
	if err := d.Audit.Write(ev); err != nil {
		d.log().Warn("audit write failed", "action", audit.ActionArkPrune, "error", err.Error())
	}
}
