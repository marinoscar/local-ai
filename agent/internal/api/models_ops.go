package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/audit"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
)

// Handlers of POST /models/{id}/load, POST /models/{id}/unload and PUT /models/default (agent.md
// sections 3.8.8 to 3.8.10, ROUTER-05). They decode and authorize, call the load orchestrator
// (internal/router, llama-router.md section 4.4) through the ModelOps interface and write its typed
// results. The orchestrator does the work: it takes modelOpsMu, writes the audit line and publishes
// the events; nothing here touches the router or the state. GET /models*, download, DELETE and the
// key routes are AGENT-15's.

const (
	// defaultLoadTimeout is llama.load_timeout_seconds when the deployment does not say.
	defaultLoadTimeout = 300 * time.Second
	// unloadTimeout is the route deadline of POST /models/{id}/unload (agent.md section 3.8): the
	// 60 s wait for a hung child, then the llama restart.
	unloadTimeout = 90 * time.Second
	// defaultSetTimeout is the route deadline of PUT /models/default.
	defaultSetTimeout = 5 * time.Second
)

// ModelOps is what the three routes need from the load orchestrator. internal/router implements it
// (router.NewHTTPOps over *router.Orchestrator): it cannot be the orchestrator itself, because
// internal/router imports this package, so the request and result types are defined here. Every
// error the implementation returns is an *Error carrying the documented code, status and details (a
// plain error is a bug and answers 500, the context's own error is the route deadline).
type ModelOps interface {
	Load(ctx context.Context, req ModelLoadRequest) (ModelLoadResult, error)
	Unload(ctx context.Context, req ModelUnloadRequest) (ModelUnloadResult, error)
	SetDefault(ctx context.Context, id string, actor audit.Actor) (ModelDefaultResult, error)
}

// ModelLoadRequest is a load as the route decoded it.
type ModelLoadRequest struct {
	ID           string
	Wait         bool
	UnloadOthers bool
	Actor        audit.Actor
}

// ModelLoadResult is the outcome of a load (200 or, with Accepted, 202).
type ModelLoadResult struct {
	ID            string
	AlreadyLoaded bool
	Accepted      bool
	LoadMS        int64
	Unloaded      []string
	OpID          string
	// Warning is a LAI-HW-005 or LAI-HW-006 notice, already in the language of the request.
	Warning *ModelWarning
}

// ModelWarning is a doctor code with a plain-language message.
type ModelWarning struct {
	Code    string
	Message string
}

// ModelUnloadRequest is an unload as the route decoded it.
type ModelUnloadRequest struct {
	ID    string
	Force bool
	Actor audit.Actor
}

// ModelUnloadResult is the outcome of an unload.
type ModelUnloadResult struct {
	ID              string
	AlreadyUnloaded bool
	OpID            string
	// Restarted is true when the model would not stop and llama was restarted; Restored are the
	// models loaded again afterwards.
	Restarted bool
	Restored  []string
}

// ModelDefaultResult is the outcome of PUT /models/default.
type ModelDefaultResult struct {
	Default        string
	Previous       string
	IniRegenerated bool
}

// ModelOpsDeps are what the three routes need. Orchestrator is required.
type ModelOpsDeps struct {
	Orchestrator ModelOps
	// LoadTimeout and ModelsMax size the deadline of the load route, load_timeout x (n+2) + 30 s with
	// n = models_max (agent.md section 3.8): the wiring passes llama.load_timeout_seconds and the
	// profile's models_max. Zero values mean 300 s and 1.
	LoadTimeout time.Duration
	ModelsMax   int
}

// loadRouteTimeout is the deadline of POST /models/{id}/load.
func (d ModelOpsDeps) loadRouteTimeout() time.Duration {
	t, n := d.LoadTimeout, d.ModelsMax
	if t <= 0 {
		t = defaultLoadTimeout
	}
	if n < 1 {
		n = 1
	}
	return t*time.Duration(n+2) + 30*time.Second
}

// ModelOpsRoutes returns POST /models/{id}/load (op or portal, load_timeout x (n+2) + 30 s),
// POST /models/{id}/unload (op, 90 s) and PUT /models/default (op, 5 s).
func ModelOpsRoutes(d ModelOpsDeps) []Route {
	return []Route{
		{Method: http.MethodPost, Pattern: "/models/{id}/load", Auth: []Level{AuthOp, AuthPortal}, Timeout: d.loadRouteTimeout(), Handler: HandlerFunc(d.load)},
		{Method: http.MethodPost, Pattern: "/models/{id}/unload", Auth: []Level{AuthOp}, Timeout: unloadTimeout, Handler: HandlerFunc(d.unload)},
		{Method: http.MethodPut, Pattern: "/models/default", Auth: []Level{AuthOp}, Timeout: defaultSetTimeout, Handler: HandlerFunc(d.setDefault)},
	}
}

// RegisterModelOps adds the three routes to reg (DefaultRegistry when nil). The wiring in
// internal/app calls it; the package does not register anything at init time.
func RegisterModelOps(reg *Registry, d ModelOpsDeps) error {
	if d.Orchestrator == nil {
		return errors.New("api: model operation routes need an orchestrator")
	}
	if reg == nil {
		reg = DefaultRegistry
	}
	return reg.Add(ModelOpsRoutes(d)...)
}

// loadBody is the body of POST /models/{id}/load; both fields default to true.
type loadBody struct {
	Wait         *bool `json:"wait"`
	UnloadOthers *bool `json:"unload_others"`
}

type warningJSON struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type modelLoadedJSON struct {
	ID            string       `json:"id"`
	Status        string       `json:"status"`
	AlreadyLoaded bool         `json:"already_loaded"`
	LoadMS        int64        `json:"load_ms"`
	Unloaded      []string     `json:"unloaded"`
	OpID          string       `json:"op_id"`
	Warning       *warningJSON `json:"warning,omitempty"`
}

type modelLoadingJSON struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	OpID   string `json:"op_id"`
	// Warning is the planner's LAI-HW-005 / LAI-HW-006 notice, known before the 202 is sent.
	Warning *warningJSON `json:"warning,omitempty"`
}

func (d ModelOpsDeps) load(w http.ResponseWriter, r *http.Request) error {
	id := r.PathValue("id")
	info := InfoFrom(r.Context())
	wait, unloadOthers := true, true
	if info.Principal.Kind == KindPortal {
		// A Portal session may only start a load and never waits for it: the client body is ignored
		// and replaced (survival-portal.md section 3.3). The unload is the planner's: with
		// unload_others true it evicts only as many models as the fit rules require, so other models
		// stay loaded exactly when the target fits next to them. The orchestrator itself refuses a
		// model that is not loadable (422).
		wait = false
	} else {
		var body loadBody
		if err := DecodeJSONOrEmpty(w, r, &body); err != nil {
			return err
		}
		if body.Wait != nil {
			wait = *body.Wait
		}
		if body.UnloadOthers != nil {
			unloadOthers = *body.UnloadOthers
		}
	}

	res, err := d.Orchestrator.Load(r.Context(), ModelLoadRequest{
		ID: id, Wait: wait, UnloadOthers: unloadOthers, Actor: serviceActor(info),
	})
	if err != nil {
		return failedOp(w, err)
	}
	if res.OpID != "" {
		SetOpID(w, res.OpID)
	}
	if res.Accepted {
		accepted := modelLoadingJSON{ID: res.ID, Status: "loading", OpID: res.OpID}
		if res.Warning != nil {
			accepted.Warning = &warningJSON{Code: res.Warning.Code, Message: res.Warning.Message}
		}
		WriteJSON(w, r, http.StatusAccepted, accepted)
		return nil
	}
	out := modelLoadedJSON{
		ID: res.ID, Status: "loaded", AlreadyLoaded: res.AlreadyLoaded, LoadMS: res.LoadMS,
		Unloaded: append([]string{}, res.Unloaded...), OpID: res.OpID,
	}
	if res.Warning != nil {
		out.Warning = &warningJSON{Code: res.Warning.Code, Message: res.Warning.Message}
	}
	WriteJSON(w, r, http.StatusOK, out)
	return nil
}

// unloadBody is the body of POST /models/{id}/unload.
type unloadBody struct {
	Force bool `json:"force"`
}

type modelUnloadedJSON struct {
	ID              string   `json:"id"`
	Status          string   `json:"status"`
	AlreadyUnloaded bool     `json:"already_unloaded"`
	OpID            string   `json:"op_id"`
	Restarted       bool     `json:"restarted,omitempty"`
	Restored        []string `json:"restored,omitempty"`
}

func (d ModelOpsDeps) unload(w http.ResponseWriter, r *http.Request) error {
	var body unloadBody
	if err := DecodeJSONOrEmpty(w, r, &body); err != nil {
		return err
	}
	res, err := d.Orchestrator.Unload(r.Context(), ModelUnloadRequest{
		ID: r.PathValue("id"), Force: body.Force, Actor: serviceActor(InfoFrom(r.Context())),
	})
	if err != nil {
		return failedOp(w, err)
	}
	if res.OpID != "" {
		SetOpID(w, res.OpID)
	}
	WriteJSON(w, r, http.StatusOK, modelUnloadedJSON{
		ID: res.ID, Status: "unloaded", AlreadyUnloaded: res.AlreadyUnloaded, OpID: res.OpID,
		Restarted: res.Restarted, Restored: res.Restored,
	})
	return nil
}

// defaultBody is the body of PUT /models/default.
type defaultBody struct {
	ID string `json:"id"`
}

type modelDefaultJSON struct {
	Default        string  `json:"default"`
	Previous       *string `json:"previous"`
	IniRegenerated bool    `json:"ini_regenerated"`
}

func (d ModelOpsDeps) setDefault(w http.ResponseWriter, r *http.Request) error {
	var body defaultBody
	if err := DecodeJSON(w, r, &body); err != nil {
		return err
	}
	if body.ID == "" {
		return NewError(CodeInvalidRequest, http.StatusBadRequest, map[string]any{"field": "id", "reason": "invalid_value"})
	}
	res, err := d.Orchestrator.SetDefault(r.Context(), body.ID, serviceActor(InfoFrom(r.Context())))
	if err != nil {
		return failedOp(w, err)
	}
	out := modelDefaultJSON{Default: res.Default, IniRegenerated: res.IniRegenerated}
	if res.Previous != "" {
		prev := res.Previous
		out.Previous = &prev
	}
	WriteJSON(w, r, http.StatusOK, out)
	return nil
}

// failedOp passes an orchestrator error on. A load or unload that failed after it started created
// an operation, so the response names it in X-LocalAI-Op-Id like a successful one (agent.md section
// 3.7): the codes of a failed or timed out load and of "nothing could be loaded".
func failedOp(w http.ResponseWriter, err error) error {
	var e *Error
	if errors.As(err, &e) {
		switch e.Code {
		case doctor.LLM009, doctor.LLM010, doctor.LLM012:
			if op, ok := e.Details["op_id"].(string); ok && op != "" {
				SetOpID(w, op)
			}
		}
	}
	return err
}
