package api

import (
	"context"
	"crypto/rand"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/audit"
	"github.com/marinoscar/local-ai/agent/internal/services"
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

func (d ServicesDeps) list(w http.ResponseWriter, r *http.Request) error {
	rows := []serviceJSON{}
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
		rows = append(rows, row)
	}
	WriteJSON(w, r, http.StatusOK, map[string]any{"services": rows})
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
	rec := serviceAudit{deps: d, svc: svc, action: action, force: body.Force, opID: opID, info: info}

	// Stopping llama in survival mode would leave the Portal without a brain: it needs force.
	if svc.Name == "llama" && action == services.ActionStop && !body.Force && d.Mode != nil && d.Mode() == servicesSurvivalMode {
		err := NewError(CodeNotInMode, http.StatusConflict, map[string]any{"mode": servicesSurvivalMode, "requires": "force", "op_id": opID})
		rec.write(audit.ResultDenied, CodeNotInMode)
		return err
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
		restored, err = d.Llama.Run(r.Context(), opID, action, op)
	} else {
		err = op(r.Context())
	}
	if err != nil {
		apiErr := d.mapActionError(err, svc, action, opID)
		code := ""
		var ae *Error
		if errors.As(apiErr, &ae) {
			code = ae.Code
		}
		rec.write(audit.ResultFail, code)
		return apiErr
	}
	rec.write(audit.ResultOK, "")
	if restored == nil {
		restored = []string{}
	}
	WriteJSON(w, r, http.StatusOK, serviceActionResponse{Name: svc.Name, Active: final.ActiveState, RestoredModels: restored, OpID: opID})
	return nil
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
	info   *RequestInfo
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
		Actor:  serviceActor(a.info),
		Target: target,
		OpID:   a.opID,
		ReqID:  a.info.ID,
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

func init() {
	// The messages of the codes a failed start or restart answers with, in both languages
	// (companion-services.md section 8 and doctor-codes.md). AGENT-11 replaces the table.
	messages[services.CodeLlamaNotRunning] = map[string]text{
		langEN: {"The AI model service (localai-llama) is not running.", "Run `aictl restart llama`. If it fails again, run `aictl logs --unit llama`."},
		langES: {"El servicio del modelo de IA (localai-llama) no está en marcha.", "Ejecute `aictl restart llama`. Si falla otra vez, ejecute `aictl logs --unit llama`."},
	}
	messages[services.CodeLibraryNotRunning] = map[string]text{
		langEN: {"The library service isn't running.", "Press Restart library, or run `aictl services restart kiwix`."},
		langES: {"El servicio de la biblioteca no está en marcha.", "Presione Reiniciar biblioteca, o ejecute `aictl services restart kiwix`."},
	}
	messages[services.CodeSpeechNotRunning] = map[string]text{
		langEN: {"The speech-recognition service isn't running.", "Run `aictl services restart whisper`."},
		langES: {"El servicio de reconocimiento de voz no está en marcha.", "Ejecute `aictl services restart whisper`."},
	}
}
