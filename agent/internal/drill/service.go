package drill

import (
	"context"
	"errors"
	"net/http"

	"github.com/marinoscar/local-ai/agent/internal/api"
)

// Service adapts an Engine to api.DrillService, the interface of the /drill routes. It lives here,
// not in internal/api, so the API package imports nothing of the drill. Every error it returns is an
// *api.Error ready for the envelope.
type Service struct {
	E *Engine
}

var _ api.DrillService = (*Service)(nil)

// NewService returns the api.DrillService over e.
func NewService(e *Engine) *Service { return &Service{E: e} }

// Start implements api.DrillService.
func (s *Service) Start(ctx context.Context, req api.DrillStartRequest) (api.DrillStartResult, error) {
	res, err := s.E.Start(ctx, StartRequest{
		Quick: req.Quick, Only: req.Only, Trigger: Trigger(req.Trigger), OpID: req.OpID,
	})
	if err != nil {
		return api.DrillStartResult{}, mapError(err)
	}
	out := api.DrillStartResult{RunID: res.RunID, Code: res.Code, Reason: res.Reason, RetryAt: res.RetryAt}
	if res.State == StartSkipped {
		out.State = api.DrillStateSkipped
	}
	return out, nil
}

// Results implements api.DrillService.
func (s *Service) Results(_ context.Context, runID string) ([]byte, error) {
	var (
		doc []byte
		err error
	)
	if runID == "" {
		doc, err = s.E.Latest()
	} else {
		doc, err = s.E.Get(runID)
	}
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			details := map[string]any{"kind": "drill_results"}
			if runID != "" {
				details["run_id"] = runID
			}
			return nil, api.NewError(api.CodeNotFound, http.StatusNotFound, details)
		}
		return nil, err
	}
	return doc, nil
}

// Abort implements api.DrillService.
func (s *Service) Abort(context.Context) (string, error) {
	id, err := s.E.Abort("")
	if err != nil {
		return "", mapError(err)
	}
	return id, nil
}

// mapError turns an engine error into the API error of the route.
func mapError(err error) error {
	var refused *RefusedError
	if errors.As(err, &refused) {
		details := map[string]any{}
		for k, v := range refused.Details {
			details[k] = v
		}
		return api.NewError(refused.Code, http.StatusConflict, details)
	}
	var invalid *InvalidRequestError
	if errors.As(err, &invalid) {
		details := map[string]any{"field": invalid.Field, "reason": invalid.Reason}
		if len(invalid.Unknown) > 0 {
			details["unknown"] = invalid.Unknown
		}
		return api.NewError(api.CodeInvalidRequest, http.StatusBadRequest, details)
	}
	switch {
	case errors.Is(err, ErrNoRun):
		return api.NewError(api.CodeNotFound, http.StatusNotFound, map[string]any{"kind": "drill"})
	case errors.Is(err, ErrClosing):
		return api.NewError(api.CodeStarting, http.StatusServiceUnavailable, nil)
	}
	return err
}
