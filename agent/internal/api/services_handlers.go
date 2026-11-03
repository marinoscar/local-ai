package api

import (
	"context"
	"crypto/rand"
	"errors"
	"log/slog"
	"net/http"
	"sort"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/audit"
	"github.com/marinoscar/local-ai/agent/internal/services"
	"github.com/marinoscar/local-ai/agent/internal/svc/health"
)

// Handlers of GET /services and POST /services/{name}/{action} (agent.md section 3.8.22). Nothing here
// talks to systemd: the handlers see a status source, an action runner, the model-operations hook and
// the audit log through the small interfaces below, so every path is tested with fakes.

// servicesSurvivalMode is the mode in which stopping llama needs "force": true.
const servicesSurvivalMode = "survival"

// ServiceStatusSource is where GET /services reads the services from; *services.Watcher implements it.
type ServiceStatusSource interface {
	Snapshot(ctx context.Context) []services.Status
}

// ServiceHealthSource is where GET /services reads the companion services' health from;
// *health.Supervisor implements it (companion-services.md section 4.6). Its records replace the rows of
// the same name from the status source and add the services the status source does not list (piper,
// maps), so the daemon may wire the supervisor next to a watcher that still lists kiwix and whisper.
type ServiceHealthSource interface {
	Records(ctx context.Context) []health.Record
}

// ServiceActions runs start, stop and restart and waits for the unit's final state;
// *services.Controller implements it.
type ServiceActions interface {
	Do(ctx context.Context, svc services.Service, action services.Action) (services.UnitState, error)
}

// LlamaRestarter is the model-operations hook of the llama actions (agent.md sections 3.8.22 and 4.9),
// implemented by ROUTER-06. Run takes modelOpsMu without blocking (a busy lock is a *BusyError), calls
// op, which performs the unit action, and after a start or restart waits for the router's /health and
// restores models.desired_loaded, returning the ids restored. While no implementation is wired the
// handler runs the action alone and answers with restored_models: [].
type LlamaRestarter interface {
	Run(ctx context.Context, opID string, action services.Action, op func(context.Context) error) (restored []string, err error)
}

// BusyError is returned by a LlamaRestarter when another model operation holds modelOpsMu. The handler
// answers 409 LAI-AGT-022 with the holder's op id.
type BusyError struct {
	// OpID is the operation that holds the lock.
	OpID string
}

func (e *BusyError) Error() string { return "another model operation is running: " + e.OpID }

// AuditWriter is the audit log; *audit.Log implements it.
type AuditWriter interface {
	Write(ev audit.Event) error
}

// ServicesDeps are what the /services routes need. Status and Actions are required.
type ServicesDeps struct {
	Status  ServiceStatusSource
	Actions ServiceActions
	// Health is optional: without it GET /services lists what Status reports.
	Health ServiceHealthSource
	// Llama is optional (see LlamaRestarter).
	Llama LlamaRestarter
	// Mode returns the current mode name; nil means never survival.
	Mode func() string
	// Audit is optional.
	Audit AuditWriter
	// NewOpID makes the id of an operation (op_ plus 12 base32 characters); nil uses the default.
	NewOpID func() string
	// Logger defaults to slog.Default.
	Logger *slog.Logger
}

func (d ServicesDeps) validate() error {
	if d.Status == nil || d.Actions == nil {
		return errors.New("api: services routes need a status source and an action runner")
	}
	return nil
}

// ServicesRoutes returns GET /services (op, 5 s) and POST /services/{name}/{action} (op, 120 s: a
// 60 s wait for the unit plus, for llama, the wait for the router and the model restore).
func ServicesRoutes(d ServicesDeps) []Route {
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	return []Route{
		{Method: http.MethodGet, Pattern: "/services", Auth: []Level{AuthOp}, Timeout: 5 * time.Second, Handler: HandlerFunc(d.list)},
		{Method: http.MethodPost, Pattern: "/services/{name}/{action}", Auth: []Level{AuthOp}, Timeout: 120 * time.Second, Handler: HandlerFunc(d.act)},
	}
}

// RegisterServices adds the /services routes to reg (DefaultRegistry when nil). The wiring in
// internal/app calls it (AGENT-20); the package does not register anything at init time.
func RegisterServices(reg *Registry, d ServicesDeps) error {
	if err := d.validate(); err != nil {
		return err
	}
	if reg == nil {
		reg = DefaultRegistry
	}
	return reg.Add(ServicesRoutes(d)...)
}

// serviceJSON is a row of GET /services: the services array of /status plus enabled, last_exit and the
// health state.
type serviceJSON struct {
	Name     string        `json:"name"`
	Unit     string        `json:"unit"`
	Active   string        `json:"active"`
	Sub      string        `json:"sub"`
	Since    *string       `json:"since"`
	Restarts int           `json:"restarts"`
	Healthy  bool          `json:"healthy"`
	State    string        `json:"state"`
	Enabled  bool          `json:"enabled"`
	LastExit *lastExitJSON `json:"last_exit"`
}

type lastExitJSON struct {
	Code int    `json:"code"`
	At   string `json:"at"`
}

func rfc3339(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// companionRow is the row of a service the health supervisor reports, as the map the JSON is built
// from: kiwix and whisper carry the unit fields of agent.md section 3.8.22 as every row does, piper and
// maps (no unit) only name, unit (null), state and what the supervisor knows (companion-services.md
// section 4.6). problems is always an array; books, voices and maps appear when they apply to the
// service, even when empty.
func companionRow(rec health.Record) map[string]any {
	row := map[string]any{
		"name": rec.Name, "unit": nil, "state": string(rec.State), "healthy": rec.Healthy,
		"problems": append([]health.Problem{}, rec.Problems...),
	}
	if rec.HasUnit() {
		row["unit"] = rec.Unit
		row["active"], row["sub"], row["restarts"], row["enabled"] = rec.Active, rec.Sub, rec.Restarts, rec.Enabled
		row["since"], row["last_exit"] = nil, nil
		if !rec.Since.IsZero() {
			row["since"] = rfc3339(rec.Since)
		}
		if rec.LastExit != nil {
			row["last_exit"] = lastExitJSON{Code: rec.LastExit.Code, At: rfc3339(rec.LastExit.At)}
		}
	}
	if !rec.LastCheck.IsZero() {
		row["last_check"] = rfc3339(rec.LastCheck)
	}
	if rec.Detail != "" {
		row["detail"] = rec.Detail
	}
	if rec.Model != "" {
		row["model"] = rec.Model
	}
	if rec.Device != "" {
		row["device"] = rec.Device
	}
	if rec.Books != nil {
		row["books"] = rec.Books
	}
	if rec.Voices != nil {
		row["voices"] = rec.Voices
	}
	if rec.Maps != nil {
		row["maps"] = rec.Maps
	}
	return row
}

func (d ServicesDeps) list(w http.ResponseWriter, r *http.Request) error {
	type namedRow struct {
		name string
		row  any
	}
	var rows []namedRow
	put := func(name string, row any) {
		for i := range rows {
			if rows[i].name == name {
				rows[i].row = row
				return
			}
		}
		rows = append(rows, namedRow{name, row})
	}
	for _, s := range d.Status.Snapshot(r.Context()) {
		row := serviceJSON{
			Name: s.Name, Unit: s.Unit, Active: s.Active, Sub: s.Sub, Restarts: s.Restarts,
			Healthy: s.Healthy, State: s.Health, Enabled: s.Enabled,
		}
		if !s.Since.IsZero() {
			since := rfc3339(s.Since)
			row.Since = &since
		}
		if s.LastExit != nil {
			row.LastExit = &lastExitJSON{Code: s.LastExit.Code, At: rfc3339(s.LastExit.At)}
		}
		put(s.Name, row)
	}
	if d.Health != nil {
		for _, rec := range d.Health.Records(r.Context()) {
			put(rec.Name, companionRow(rec))
		}
	}
	// llama, kiwix, whisper, piper, maps whichever source a row came from.
	sort.SliceStable(rows, func(i, j int) bool { return health.Order(rows[i].name) < health.Order(rows[j].name) })
	out := make([]any, 0, len(rows))
	for _, nr := range rows {
		out = append(out, nr.row)
	}
	WriteJSON(w, r, http.StatusOK, map[string]any{"services": out})
	return nil
}

// serviceActionBody is the optional body of POST /services/{name}/{action}.
type serviceActionBody struct {
	Force bool `json:"force"`
}

// serviceActionResponse is the 200 body of POST /services/{name}/{action}.
type serviceActionResponse struct {
	Name           string   `json:"name"`
	Active         string   `json:"active"`
	RestoredModels []string `json:"restored_models"`
	OpID           string   `json:"op_id"`
}

func (d ServicesDeps) act(w http.ResponseWriter, r *http.Request) error {
	name, actionName := r.PathValue("name"), r.PathValue("action")
	svc, ok := services.ByName(name)
	if !ok {
		return NewError(CodeNotFound, http.StatusNotFound, map[string]any{"name": name, "kind": "service"})
	}
	action := services.Action(actionName)
	if !action.Valid() {
		return NewError(CodeInvalidRequest, http.StatusBadRequest, map[string]any{
			"field": "action", "reason": "unknown_action", "allowed": []string{"start", "stop", "restart"},
		})
	}
	var body serviceActionBody
	if err := DecodeJSONOrEmpty(w, r, &body); err != nil {
		return err
	}

	info := InfoFrom(r.Context())
	opID := d.opID()
	SetOpID(w, opID)
	res, err := d.run(r.Context(), svc, action, body.Force, opID, serviceActor(info), info.ID)
	if err != nil {
		return err
	}
	WriteJSON(w, r, http.StatusOK, serviceActionResponse{Name: res.Name, Active: res.Active, RestoredModels: res.RestoredModels, OpID: res.OpID})
	return nil
}

// ServiceActionRequest is one start, stop or restart as the route decoded it. The cloud executors
// (AGENT-18) build the same request with the cloud actor.
type ServiceActionRequest struct {
	// Name is the service name: llama, kiwix or whisper.
	Name   string
	Action services.Action
	// Force lets a stop of llama through in survival mode.
	Force bool
	Actor audit.Actor
	// ReqID goes to the audit line; empty is fine.
	ReqID string
}

// ServiceActionResult is the outcome of RunAction: the body of the 200 answer of the route.
type ServiceActionResult struct {
	Name string
	// Active is the unit's active state when the action ended ("active" after a start or restart).
	Active         string
	RestoredModels []string
	OpID           string
}

// RunAction is POST /services/{name}/{action} without the HTTP: the same survival rule, the same
// llama hook, the same typed errors (*Error) and the same service.<action> audit line. Errors:
// 404 LAI-AGT-021 for an unknown service, 400 LAI-AGT-020 for an unknown action, 409 LAI-AGT-023
// (stopping llama in survival without Force), 409 LAI-AGT-022 (another model operation holds
// modelOpsMu), 502 with the service's "not running" code.
func (d ServicesDeps) RunAction(ctx context.Context, req ServiceActionRequest) (ServiceActionResult, error) {
	svc, ok := services.ByName(req.Name)
	if !ok {
		return ServiceActionResult{}, NewError(CodeNotFound, http.StatusNotFound, map[string]any{"name": req.Name, "kind": "service"})
	}
	if !req.Action.Valid() {
		return ServiceActionResult{}, NewError(CodeInvalidRequest, http.StatusBadRequest, map[string]any{
			"field": "action", "reason": "unknown_action", "allowed": []string{"start", "stop", "restart"},
		})
	}
	return d.run(ctx, svc, req.Action, req.Force, d.opID(), req.Actor, req.ReqID)
}

// run is the part of the route that does not depend on the request: the survival rule, the unit
// action (through the llama hook for llama), the audit line and the error mapping.
func (d ServicesDeps) run(ctx context.Context, svc services.Service, action services.Action, force bool, opID string, actor audit.Actor, reqID string) (ServiceActionResult, error) {
	rec := serviceAudit{deps: d, svc: svc, action: action, force: force, opID: opID, actor: actor, reqID: reqID}

	// Stopping llama in survival mode would leave the Portal without a brain: it needs force.
	if svc.Name == "llama" && action == services.ActionStop && !force && d.Mode != nil && d.Mode() == servicesSurvivalMode {
		err := NewError(CodeNotInMode, http.StatusConflict, map[string]any{"mode": servicesSurvivalMode, "requires": "force", "op_id": opID})
		rec.write(audit.ResultDenied, CodeNotInMode)
		return ServiceActionResult{}, err
	}

	var final services.UnitState
	op := func(ctx context.Context) error {
		st, err := d.Actions.Do(ctx, svc, action)
		final = st
		return err
	}
	var restored []string
	var err error
	if svc.Name == "llama" && d.Llama != nil {
		restored, err = d.Llama.Run(ctx, opID, action, op)
	} else {
		err = op(ctx)
	}
	if err != nil {
		apiErr := d.mapActionError(err, svc, action, opID)
		code := ""
		var ae *Error
		if errors.As(apiErr, &ae) {
			code = ae.Code
		}
		rec.write(audit.ResultFail, code)
		return ServiceActionResult{}, apiErr
	}
	rec.write(audit.ResultOK, "")
	if restored == nil {
		restored = []string{}
	}
	return ServiceActionResult{Name: svc.Name, Active: final.ActiveState, RestoredModels: restored, OpID: opID}, nil
}

// mapActionError turns what the action runner or the llama hook returned into an API error.
func (d ServicesDeps) mapActionError(err error, svc services.Service, action services.Action, opID string) error {
	var busy *BusyError
	if errors.As(err, &busy) {
		return NewError(CodeBusy, http.StatusConflict, map[string]any{"op_id": busy.OpID})
	}
	var apiErr *Error
	if errors.As(err, &apiErr) {
		return err
	}
	var ae *services.ActionError
	if errors.As(err, &ae) {
		details := map[string]any{
			"service": svc.Name, "unit": svc.Unit, "action": string(action), "reason": ae.Reason,
			"active": ae.State.ActiveState, "sub": ae.State.SubState, "op_id": opID,
		}
		d.log().Warn("service action failed", "unit", svc.Unit, "action", string(action), "reason", ae.Reason, "error", ae.Error())
		if action == services.ActionStop {
			// A stop that did not stop is not "the service is not running": it is a bug or a stuck unit.
			return NewError(CodeInternal, http.StatusInternalServerError, details)
		}
		return NewError(services.NotRunningCode(svc.Name), http.StatusBadGateway, details)
	}
	return err
}

func (d ServicesDeps) log() *slog.Logger {
	if d.Logger != nil {
		return d.Logger.With("component", "api")
	}
	return slog.Default().With("component", "api")
}

// servicesOpID returns a fresh operation id: op_ plus 12 base32 characters (a-z, 2-7), the form of
// agent.md section 2.
func servicesOpID() string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	id := make([]byte, 0, 15)
	id = append(id, "op_"...)
	for _, c := range b {
		id = append(id, requestIDAlphabet[c&31])
	}
	return string(id)
}

func (d ServicesDeps) opID() string {
	if d.NewOpID != nil {
		return d.NewOpID()
	}
	return servicesOpID()
}

// serviceAudit writes the service.<action> audit record of one request.
type serviceAudit struct {
	deps   ServicesDeps
	svc    services.Service
	action services.Action
	force  bool
	opID   string
	actor  audit.Actor
	reqID  string
}

func (a serviceAudit) write(result audit.Result, code string) {
	if a.deps.Audit == nil {
		return
	}
	target := map[string]string{"service": a.svc.Name, "unit": a.svc.Unit}
	if a.force {
		target["force"] = "true"
	}
	ev := audit.Event{
		Action: "service." + string(a.action),
		Result: result,
		Code:   code,
		Actor:  a.actor,
		Target: target,
		OpID:   a.opID,
		ReqID:  a.reqID,
	}
	if err := a.deps.Audit.Write(ev); err != nil {
		// The record is already in the journal (the audit log mirrors to slog); the action stands.
		a.deps.log().Warn("audit write failed", "action", ev.Action, "error", err.Error())
	}
}

// serviceActor maps the request's principal to the audit actor.
func serviceActor(info *RequestInfo) audit.Actor {
	p := info.Principal
	var a audit.Actor
	switch p.Kind {
	case KindAdmin:
		a.Kind = audit.KindAdmin
	case KindOperator:
		a.Kind = audit.KindOperator
	case KindPortal:
		a.Kind = audit.KindPortal
	default:
		a.Kind = audit.KindAnonymous
	}
	switch {
	case p.Via == "pin":
		a.Via = audit.ViaPIN
	case p.Credential == CredBearer:
		a.Via = audit.ViaBearer
	case p.Credential == CredSession:
		a.Via = audit.ViaSession
	case p.Credential == CredSocket:
		a.Via = audit.ViaSocket
	case p.Credential == CredPortal:
		a.Via = audit.ViaPortal
	}
	if info.Peer != nil {
		a = a.WithUID(info.Peer.UID)
	}
	return a
}
