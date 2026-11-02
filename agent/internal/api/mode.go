package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/audit"
	"github.com/marinoscar/local-ai/agent/internal/modes"
	"github.com/marinoscar/local-ai/agent/internal/state"
)

// Handlers of GET /mode, POST /mode and POST /mode/survival (agent.md sections 3.8.15 and 3.8.16).
// They see the mode engine through the small interface below, so every path is tested with a fake.

// modeWaitTimeout is the route deadline of POST /mode: 20 minutes with "wait": true, and a request
// without it answers at once (agent.md section 3.8).
const modeWaitTimeout = 20 * time.Minute

// ModeEngine is what the /mode routes need from *modes.Engine.
type ModeEngine interface {
	// Request asks for a transition (see modes.Engine.Request).
	Request(ctx context.Context, target state.Mode, o modes.RequestOptions) (modes.Result, error)
	// Info is the picture GET /mode shows.
	Info() modes.Info
}

// ModeDeps are what the /mode routes need. Engine is required.
type ModeDeps struct {
	Engine ModeEngine
	// Audit is optional; it records a refused attempt to leave survival (the engine audits the
	// transitions themselves).
	Audit AuditWriter
	// Logger defaults to slog.Default.
	Logger *slog.Logger
}

// ModeRoutes returns GET /mode (op, 5 s), POST /mode (op; admin* to leave survival, 20 min) and
// POST /mode/survival (none, 5 s, exempt from the startup and shutdown gate: it is queued instead).
func ModeRoutes(d ModeDeps) []Route {
	return []Route{
		{Method: http.MethodGet, Pattern: "/mode", Auth: []Level{AuthOp}, Timeout: 5 * time.Second, Handler: HandlerFunc(d.get)},
		{Method: http.MethodPost, Pattern: "/mode", Auth: []Level{AuthOp}, Timeout: modeWaitTimeout, Handler: HandlerFunc(d.change)},
		{Method: http.MethodPost, Pattern: "/mode/survival", Auth: []Level{AuthNone}, Timeout: 5 * time.Second, GateExempt: true, Handler: HandlerFunc(d.survival)},
	}
}

// RegisterMode adds the /mode routes to reg (DefaultRegistry when nil). The wiring in internal/app
// calls it (AGENT-20); the package does not register anything at init time.
func RegisterMode(reg *Registry, d ModeDeps) error {
	if d.Engine == nil {
		return errors.New("api: mode routes need an engine")
	}
	if reg == nil {
		reg = DefaultRegistry
	}
	return reg.Add(ModeRoutes(d)...)
}

func (d ModeDeps) log() *slog.Logger {
	if d.Logger != nil {
		return d.Logger.With("component", "api")
	}
	return slog.Default().With("component", "api")
}

// modeTargets are the values of "target".
var modeTargets = []string{string(modes.Online), string(modes.Offline), string(modes.Survival)}

type modeTransitionJSON struct {
	OpID      string          `json:"op_id"`
	From      string          `json:"from"`
	Target    string          `json:"target"`
	Step      string          `json:"step"`
	StartedAt string          `json:"started_at"`
	Warnings  []modes.Warning `json:"warnings"`
}

type modeInfoJSON struct {
	Mode               string              `json:"mode"`
	Since              *string             `json:"since"`
	Transition         *modeTransitionJSON `json:"transition"`
	AllowedTargets     []string            `json:"allowed_targets"`
	LeaveRequiresAdmin bool                `json:"leave_requires_admin"`
	Connectivity       string              `json:"connectivity"`
	SuggestSurvival    bool                `json:"suggest_survival"`
}

func (d ModeDeps) get(w http.ResponseWriter, r *http.Request) error {
	info := d.Engine.Info()
	out := modeInfoJSON{
		Mode:               string(info.Mode),
		AllowedTargets:     []string{},
		LeaveRequiresAdmin: info.Mode == modes.Survival,
		Connectivity:       info.Connectivity,
		SuggestSurvival:    info.SuggestSurvival,
	}
	// Every transition is allowed in every direction (modes.md section 2): the other two modes.
	for _, m := range modeTargets {
		if m != string(info.Mode) {
			out.AllowedTargets = append(out.AllowedTargets, m)
		}
	}
	if info.Since != nil {
		s := rfc3339(*info.Since)
		out.Since = &s
	}
	if t := info.Transition; t != nil {
		ws := t.Warnings
		if ws == nil {
			ws = []modes.Warning{}
		}
		out.Transition = &modeTransitionJSON{
			OpID: t.OpID, From: string(t.From), Target: string(t.Target), Step: t.Step,
			StartedAt: rfc3339(t.StartedAt), Warnings: ws,
		}
	}
	WriteJSON(w, r, http.StatusOK, out)
	return nil
}

// modeRequest is the body of POST /mode.
type modeRequest struct {
	Target  string `json:"target"`
	Wait    bool   `json:"wait"`
	Reapply bool   `json:"reapply"`
}

// survivalRequest is the body of POST /mode/survival.
type survivalRequest struct {
	Reapply bool `json:"reapply"`
}

func (d ModeDeps) change(w http.ResponseWriter, r *http.Request) error {
	var body modeRequest
	if err := DecodeJSON(w, r, &body); err != nil {
		return err
	}
	target := state.Mode(body.Target)
	if !target.Valid() {
		return NewError(CodeInvalidRequest, http.StatusBadRequest, map[string]any{
			"field": "target", "reason": "invalid_value", "allowed": modeTargets,
		})
	}
	info := InfoFrom(r.Context())

	// Leaving survival needs admin*: an admin, or a socket peer in localai-operators; anyone else
	// needs the PIN (modes.md section 2.4). Entering survival never needs more than op.
	cur := d.Engine.Info()
	if cur.Effective == modes.Survival && target != modes.Survival && !info.Principal.Satisfies([]Level{AuthAdminStar}) {
		d.auditDenied(info, cur.Effective, target)
		return NewError(CodeAdminRequired, http.StatusForbidden, map[string]any{
			"pin_required": true, "reason": "leaving_survival",
		})
	}

	res, err := d.Engine.Request(r.Context(), target, modes.RequestOptions{
		Wait: body.Wait, Reapply: body.Reapply, Actor: modeActor(info, ""),
	})
	if err != nil {
		return d.mapError(err)
	}
	if res.OpID != "" {
		SetOpID(w, res.OpID)
	}
	switch {
	case res.Already:
		WriteJSON(w, r, http.StatusOK, map[string]any{"mode": string(res.Target), "already": true})
	case res.Queued:
		// The engine is starting or stopping while the gate is open: POST /mode is not queued (only
		// POST /mode/survival is), so this is a 503. The survival request itself is on disk as
		// pending_mode and runs at the next opportunity, as the CLI's survival route would have done.
		return NewError(CodeStarting, http.StatusServiceUnavailable, map[string]any{"queued": true, "op_id": res.OpID})
	case res.Preempted:
		// Survival took over this transition: there is no result of its own to report.
		return NewError(CodeBusy, http.StatusConflict, map[string]any{"op_id": res.PreemptedBy, "preempted": true})
	case res.Interrupted:
		return NewError(CodeStarting, http.StatusServiceUnavailable, map[string]any{"op_id": res.OpID, "reason": "interrupted"})
	case res.Done:
		warnings := res.Warnings
		if warnings == nil {
			warnings = []modes.Warning{}
		}
		WriteJSON(w, r, http.StatusOK, map[string]any{
			"op_id": res.OpID, "from": string(res.From), "mode": string(res.Target), "warnings": warnings,
		})
	default:
		WriteJSON(w, r, http.StatusAccepted, map[string]any{
			"op_id": res.OpID, "from": string(res.From), "target": string(res.Target),
			"transition": map[string]any{"step": res.Step, "started_at": rfc3339(res.StartedAt)},
		})
	}
	return nil
}

func (d ModeDeps) survival(w http.ResponseWriter, r *http.Request) error {
	var body survivalRequest
	if err := DecodeJSONOrEmpty(w, r, &body); err != nil {
		return err
	}
	info := InfoFrom(r.Context())
	// Anyone at the keyboard can do this, so the actor is usually anonymous; the spec records it as
	// "portal" (agent.md section 3.5).
	res, err := d.Engine.Request(r.Context(), modes.Survival, modes.RequestOptions{
		Reapply: body.Reapply, Actor: modeActor(info, audit.ViaPortal),
	})
	if err != nil {
		return d.mapError(err)
	}
	if res.OpID != "" {
		SetOpID(w, res.OpID)
	}
	switch {
	case res.Already:
		WriteJSON(w, r, http.StatusOK, map[string]any{"mode": string(modes.Survival), "already": true})
	case res.Queued:
		WriteJSON(w, r, http.StatusAccepted, map[string]any{"op_id": res.OpID, "target": string(modes.Survival), "queued": true})
	default:
		WriteJSON(w, r, http.StatusAccepted, map[string]any{"op_id": res.OpID, "target": string(modes.Survival)})
	}
	return nil
}

// mapError turns an engine error into an API error.
func (d ModeDeps) mapError(err error) error {
	var busy *modes.BusyError
	switch {
	case errors.As(err, &busy):
		return NewError(CodeBusy, http.StatusConflict, map[string]any{"op_id": busy.OpID, "target": string(busy.Target)})
	case errors.Is(err, modes.ErrNotAccepting), errors.Is(err, modes.ErrClosed):
		return NewError(CodeStarting, http.StatusServiceUnavailable, nil)
	case errors.Is(err, modes.ErrInvalidTarget):
		return NewError(CodeInvalidRequest, http.StatusBadRequest, map[string]any{"field": "target", "reason": "invalid_value", "allowed": modeTargets})
	}
	return err
}

func (d ModeDeps) auditDenied(info *RequestInfo, from, to modes.Mode) {
	if d.Audit == nil {
		return
	}
	ev := audit.Event{
		Action: audit.ActionModeChange,
		Result: audit.ResultDenied,
		Code:   CodeAdminRequired,
		Actor:  serviceActor(info),
		Target: map[string]string{"from": string(from), "to": string(to)},
		ReqID:  info.ID,
	}
	if err := d.Audit.Write(ev); err != nil {
		d.log().Warn("audit write failed", "action", ev.Action, "error", err.Error())
	}
}

// modeActor maps the request's principal to the actor recorded in state.json and the audit log. An
// anonymous caller gets anonVia as its "via" (the survival route passes "portal").
func modeActor(info *RequestInfo, anonVia string) state.Actor {
	a := serviceActor(info)
	via := a.Via
	if a.Kind == audit.KindAnonymous && via == "" {
		via = anonVia
	}
	return state.Actor{Kind: string(a.Kind), Via: via}
}
