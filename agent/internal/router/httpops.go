package router

import (
	"context"
	"errors"
	"net/http"

	"github.com/marinoscar/local-ai/agent/internal/api"
	"github.com/marinoscar/local-ai/agent/internal/audit"
)

// HTTPOps is the api.ModelOps over an Orchestrator: the three model-operation routes
// (api.ModelOpsRoutes, internal/api/models_ops.go) call it. It lives here because this package
// imports internal/api (through the catalog and hardware packages) and not the other way round. It
// carries the request id into the audit line and turns every *OpError into the *api.Error the route
// answers with: the doctor code, the HTTP status and the details of the orchestrator, with a message
// built from the situation (why a model does not fit) in the language of the request.
type HTTPOps struct{ o *Orchestrator }

var _ api.ModelOps = (*HTTPOps)(nil)

// NewHTTPOps returns the HTTP face of o.
func NewHTTPOps(o *Orchestrator) *HTTPOps { return &HTTPOps{o: o} }

// Load implements api.ModelOps.
func (h *HTTPOps) Load(ctx context.Context, req api.ModelLoadRequest) (api.ModelLoadResult, error) {
	info := api.InfoFrom(ctx)
	res, err := h.o.LoadWith(WithReqID(ctx, info.ID), LoadRequest{
		ID: req.ID, Wait: req.Wait, UnloadOthers: req.UnloadOthers, Actor: req.Actor,
	})
	if err != nil {
		return api.ModelLoadResult{}, apiError(info.Lang, err)
	}
	out := api.ModelLoadResult{
		ID: res.ID, AlreadyLoaded: res.AlreadyLoaded, Accepted: res.Accepted, LoadMS: res.LoadMS,
		Unloaded: res.Unloaded, OpID: res.OpID,
	}
	if res.Warning != nil {
		out.Warning = &api.ModelWarning{Code: string(res.Warning.Code), Message: res.Warning.Text(info.Lang)}
	}
	return out, nil
}

// Unload implements api.ModelOps.
func (h *HTTPOps) Unload(ctx context.Context, req api.ModelUnloadRequest) (api.ModelUnloadResult, error) {
	info := api.InfoFrom(ctx)
	res, err := h.o.UnloadWith(WithReqID(ctx, info.ID), UnloadRequest{ID: req.ID, Force: req.Force, Actor: req.Actor})
	if err != nil {
		return api.ModelUnloadResult{}, apiError(info.Lang, err)
	}
	return api.ModelUnloadResult{
		ID: res.ID, AlreadyUnloaded: res.AlreadyUnloaded, OpID: res.OpID, Restarted: res.Restarted, Restored: res.Restored,
	}, nil
}

// SetDefault implements api.ModelOps.
func (h *HTTPOps) SetDefault(ctx context.Context, id string, actor audit.Actor) (api.ModelDefaultResult, error) {
	info := api.InfoFrom(ctx)
	res, err := h.o.SetDefault(WithReqID(ctx, info.ID), id, actor)
	if err != nil {
		return api.ModelDefaultResult{}, apiError(info.Lang, err)
	}
	return api.ModelDefaultResult{Default: res.Default, Previous: res.Previous, IniRegenerated: res.IniRegenerated}, nil
}

// apiError maps an orchestrator error to an API error. An *OpError keeps its code, status and
// details; a message built from the situation is kept in the request's language, every other message
// and fix comes from the doctor table. The agent shutting down is the 503 of the gate. Anything else
// (the caller's deadline, a bug) is returned as it is for the server to classify.
func apiError(lang string, err error) error {
	var oe *OpError
	switch {
	case errors.As(err, &oe):
		details := make(map[string]any, len(oe.Details))
		for k, v := range oe.Details {
			details[k] = v
		}
		e := api.NewError(string(oe.Code), oe.HTTPStatus(), details)
		if oe.MessageES != "" {
			e = e.WithMessage(oe.Text(lang), "")
		}
		return e
	case errors.Is(err, ErrInterrupted):
		return api.NewError(api.CodeStarting, http.StatusServiceUnavailable, nil)
	}
	return err
}
