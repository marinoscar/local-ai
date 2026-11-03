package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"
	"unicode/utf8"

	"github.com/marinoscar/local-ai/agent/internal/audit"
)

// Handlers of POST /drill, GET /drill and DELETE /drill (agent.md section 3.8.28, drill.md section
// 3.1, DRILL-01). They decode, authorize and audit, and call the drill engine through the DrillService
// interface: internal/drill implements it (drill.NewService over *drill.Engine), so this package
// imports nothing of the drill. POST /drill/manual (DRILL-06) records the manual "pull the plug" drill
// through the same interface; it is the only admin route of the four.

const (
	// drillTimeout is the deadline of the three routes (agent.md section 3.8): the start answers
	// 202 and the run goes on in the background.
	drillTimeout = 10 * time.Second
	// drillManualTimeout is the deadline of POST /drill/manual: one small file is written.
	drillManualTimeout = 5 * time.Second

	// MaxDrillNotes is the longest notes text of a manual record, in characters.
	MaxDrillNotes = 2000
	// Results of a manual record.
	DrillManualPass = "pass"
	DrillManualFail = "fail"

	// Drill trigger values a request may carry; cloud and manual_plug are not requested over this route.
	drillTriggerManual    = "manual"
	drillTriggerScheduled = "scheduled"

	// DrillStateSkipped is the state of a 202 for a scheduled run a precondition held back.
	DrillStateSkipped = "skipped"
)

// DrillService is what the three routes need from the drill engine. Every error it returns is an
// *Error with the documented status and code (400 LAI-AGT-020 for a bad request or an unknown check,
// 404 LAI-AGT-021, 409 LAI-DRL-002/003/004/005, 503 LAI-AGT-024); a plain error is a bug and
// answers 500.
type DrillService interface {
	// Start validates and starts a run, or records a scheduled one as skipped.
	Start(ctx context.Context, req DrillStartRequest) (DrillStartResult, error)
	// Results returns the results document of a run; an empty runID means the last finished one. A run
	// that is not kept is a 404 LAI-AGT-021.
	Results(ctx context.Context, runID string) ([]byte, error)
	// Abort stops the running drill and returns its run id; with none running it is a 404
	// LAI-AGT-021.
	Abort(ctx context.Context) (runID string, err error)
	// Manual stores a manual_plug record (drill.md section 4.6). It re-checks what the route cannot
	// (an at in the future, notes with control characters), as 400 LAI-AGT-020.
	Manual(ctx context.Context, req DrillManualRequest) (DrillManualResult, error)
}

// DrillManualRequest is a manual record as the route decoded it. At is the zero time when the body
// had none (the engine then uses its own clock).
type DrillManualRequest struct {
	Result string
	Notes  string
	At     time.Time
	// OpID stamps the audit record.
	OpID  string
	Actor audit.Actor
}

// DrillManualResult is the outcome of a manual record: the id of the document it stored.
type DrillManualResult struct {
	RunID string
}

// DrillStartRequest is a drill start as the route decoded it.
type DrillStartRequest struct {
	Quick   bool
	Only    []string
	Trigger string
	// OpID stamps the run's events.
	OpID  string
	Actor audit.Actor
}

// DrillStartResult is the outcome of a start. State is "" for a run that started and
// DrillStateSkipped for a scheduled run a precondition held back (Code and Reason say why; RetryAt is
// nil when the agent will not try again).
type DrillStartResult struct {
	RunID   string
	State   string
	Code    string
	Reason  string
	RetryAt *time.Time
}

// DrillDeps are what the drill routes need. Service is required.
type DrillDeps struct {
	Service DrillService
	// Audit is optional.
	Audit AuditWriter
	// NewOpID makes the id of an operation (op_ plus 12 base32 characters); nil uses the default.
	NewOpID func() string
	// Logger defaults to slog.Default.
	Logger *slog.Logger
}

// DrillRoutes returns POST /drill, GET /drill and DELETE /drill (op, 10 s each) and POST /drill/manual
// (admin, 5 s).
func DrillRoutes(d DrillDeps) []Route {
	return []Route{
		{Method: http.MethodPost, Pattern: "/drill", Auth: []Level{AuthOp}, Timeout: drillTimeout, Handler: HandlerFunc(d.start)},
		{Method: http.MethodGet, Pattern: "/drill", Auth: []Level{AuthOp}, Timeout: drillTimeout, Handler: HandlerFunc(d.get)},
		{Method: http.MethodDelete, Pattern: "/drill", Auth: []Level{AuthOp}, Timeout: drillTimeout, Handler: HandlerFunc(d.abort)},
		{Method: http.MethodPost, Pattern: "/drill/manual", Auth: []Level{AuthAdmin}, Timeout: drillManualTimeout, Handler: HandlerFunc(d.manual)},
	}
}

// RegisterDrill adds the drill routes to reg (DefaultRegistry when nil). The wiring in internal/app
// calls it; the package does not register anything at init time.
func RegisterDrill(reg *Registry, d DrillDeps) error {
	if d.Service == nil {
		return errors.New("api: drill routes need a drill service")
	}
	if reg == nil {
		reg = DefaultRegistry
	}
	return reg.Add(DrillRoutes(d)...)
}

func (d DrillDeps) log() *slog.Logger {
	if d.Logger != nil {
		return d.Logger.With("component", "api")
	}
	return slog.Default().With("component", "api")
}

func (d DrillDeps) opID() string {
	if d.NewOpID != nil {
		return d.NewOpID()
	}
	return servicesOpID()
}

// drillBody is the body of POST /drill. The body is optional; every field defaults.
type drillBody struct {
	Quick   bool     `json:"quick"`
	Only    []string `json:"only"`
	Trigger string   `json:"trigger"`
}

// drillAccepted is the 202 body: the run id, plus, for a skipped scheduled run, why.
type drillAccepted struct {
	RunID   string  `json:"run_id"`
	State   string  `json:"state,omitempty"`
	Code    string  `json:"code,omitempty"`
	Reason  string  `json:"reason,omitempty"`
	RetryAt *string `json:"retry_at,omitempty"`
}

func (d DrillDeps) start(w http.ResponseWriter, r *http.Request) error {
	var body drillBody
	if err := DecodeJSONOrEmpty(w, r, &body); err != nil {
		return err
	}
	switch body.Trigger {
	case "":
		body.Trigger = drillTriggerManual
	case drillTriggerManual, drillTriggerScheduled:
	default:
		return NewError(CodeInvalidRequest, http.StatusBadRequest, map[string]any{
			"field": "trigger", "reason": "unknown_trigger", "allowed": []string{drillTriggerManual, drillTriggerScheduled},
		})
	}
	info := InfoFrom(r.Context())
	opID := d.opID()
	SetOpID(w, opID)
	rec := drillAudit{deps: d, action: "drill.start", opID: opID, info: info,
		target: map[string]string{"trigger": body.Trigger}}
	if body.Quick {
		rec.target["quick"] = "true"
	}

	res, err := d.Service.Start(r.Context(), DrillStartRequest{
		Quick: body.Quick, Only: body.Only, Trigger: body.Trigger, OpID: opID, Actor: AuditActor(info),
	})
	if err != nil {
		rec.writeErr(err)
		return err
	}
	rec.target["run_id"] = res.RunID
	if res.State == DrillStateSkipped {
		rec.target["skipped"] = res.Code
	}
	rec.write(audit.ResultOK, "")
	out := drillAccepted{RunID: res.RunID, State: res.State, Code: res.Code, Reason: res.Reason}
	if res.RetryAt != nil {
		at := rfc3339(*res.RetryAt)
		out.RetryAt = &at
	}
	WriteJSON(w, r, http.StatusAccepted, out)
	return nil
}

func (d DrillDeps) get(w http.ResponseWriter, r *http.Request) error {
	runID := r.URL.Query().Get("run_id")
	doc, err := d.Service.Results(r.Context(), runID)
	if err != nil {
		return err
	}
	WriteJSON(w, r, http.StatusOK, json.RawMessage(doc))
	return nil
}

func (d DrillDeps) abort(w http.ResponseWriter, r *http.Request) error {
	info := InfoFrom(r.Context())
	opID := d.opID()
	SetOpID(w, opID)
	rec := drillAudit{deps: d, action: "drill.abort", opID: opID, info: info, target: map[string]string{}}
	runID, err := d.Service.Abort(r.Context())
	if err != nil {
		rec.writeErr(err)
		return err
	}
	rec.target["run_id"] = runID
	rec.write(audit.ResultOK, "")
	WriteJSON(w, r, http.StatusAccepted, drillAccepted{RunID: runID})
	return nil
}

// drillManualBody is the body of POST /drill/manual. Notes is optional; at is an RFC 3339 time.
type drillManualBody struct {
	Result string  `json:"result"`
	Notes  string  `json:"notes"`
	At     *string `json:"at"`
}

// manual records the quarterly "pull the plug" drill (drill.md section 4.6) as a manual_plug document
// and answers 201 with its run id. The body is validated here (the result enum, the notes length, the
// shape of at) before the engine sees it; the engine rejects what needs its clock.
func (d DrillDeps) manual(w http.ResponseWriter, r *http.Request) error {
	var body drillManualBody
	if err := DecodeJSON(w, r, &body); err != nil {
		return err
	}
	switch body.Result {
	case DrillManualPass, DrillManualFail:
	default:
		return NewError(CodeInvalidRequest, http.StatusBadRequest, map[string]any{
			"field": "result", "reason": "invalid_result", "allowed": []string{DrillManualPass, DrillManualFail},
		})
	}
	if utf8.RuneCountInString(body.Notes) > MaxDrillNotes {
		return NewError(CodeInvalidRequest, http.StatusBadRequest, map[string]any{
			"field": "notes", "reason": "notes_too_long", "max": MaxDrillNotes,
		})
	}
	var at time.Time
	if body.At != nil {
		t, err := time.Parse(time.RFC3339, *body.At)
		if err != nil {
			return NewError(CodeInvalidRequest, http.StatusBadRequest, map[string]any{
				"field": "at", "reason": "invalid_at", "expected": "RFC 3339, for example 2026-11-02T15:00:00Z",
			})
		}
		at = t
	}
	info := InfoFrom(r.Context())
	opID := d.opID()
	SetOpID(w, opID)
	rec := drillAudit{deps: d, action: "drill.record_manual", opID: opID, info: info,
		target: map[string]string{"result": body.Result}}
	res, err := d.Service.Manual(r.Context(), DrillManualRequest{
		Result: body.Result, Notes: body.Notes, At: at, OpID: opID, Actor: AuditActor(info),
	})
	if err != nil {
		rec.writeErr(err)
		return err
	}
	rec.target["run_id"] = res.RunID
	rec.write(audit.ResultOK, "")
	WriteJSON(w, r, http.StatusCreated, drillAccepted{RunID: res.RunID})
	return nil
}

// drillAudit writes the drill.<action> audit record of one request.
type drillAudit struct {
	deps   DrillDeps
	action string
	opID   string
	info   *RequestInfo
	target map[string]string
}

// writeErr records a refusal as denied (a 409 or 404 changed nothing) and anything else as a failure.
func (a drillAudit) writeErr(err error) {
	var ae *Error
	if errors.As(err, &ae) {
		switch ae.Status {
		case http.StatusConflict, http.StatusNotFound:
			a.write(audit.ResultDenied, ae.Code)
		default:
			a.write(audit.ResultFail, ae.Code)
		}
		return
	}
	a.write(audit.ResultFail, "")
}

func (a drillAudit) write(result audit.Result, code string) {
	if a.deps.Audit == nil {
		return
	}
	ev := audit.Event{
		Action: a.action, Result: result, Code: code, Actor: AuditActor(a.info),
		Target: a.target, OpID: a.opID, ReqID: a.info.ID,
	}
	if err := a.deps.Audit.Write(ev); err != nil {
		// The record is already in the journal (the audit log mirrors to slog); the action stands.
		a.deps.log().Warn("audit write failed", "action", ev.Action, "error", err.Error())
	}
}
