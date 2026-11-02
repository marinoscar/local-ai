package api

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
)

// DoctorTimeout is the handler deadline of GET /doctor (agent.md section 3.8, route table).
const DoctorTimeout = 60 * time.Second

// DoctorResponse is the body of GET /doctor (agent.md section 3.8.27): the report of the run. It is
// the doctor package's Report, whose JSON tags mirror the OpenAPI schema DoctorResponse.
type DoctorResponse = doctor.Report

// DoctorOptions configures GET /doctor.
type DoctorOptions struct {
	// Registry holds the checks to run; nil means doctor.DefaultRegistry.
	Registry *doctor.Registry
	// Now is the clock of ran_at and duration_ms; default time.Now. Tests set it.
	Now func() time.Time
}

// DoctorRoute returns the GET /doctor route (agent.md section 3.8.27): auth op, 60 s deadline.
// `?area=LLM` (repeatable, or comma-separated) keeps the checks of those areas; `?fast=1` runs only
// the fast checks and gives the whole run doctor.FastBudget. Title and fix come in the language of
// the request (Accept-Language or ui.language), codes and field names never change.
func DoctorRoute(opts DoctorOptions) Route {
	reg := opts.Registry
	if reg == nil {
		reg = doctor.DefaultRegistry
	}
	return Route{
		Method:  http.MethodGet,
		Pattern: "/doctor",
		Auth:    []Level{AuthOp},
		Timeout: DoctorTimeout,
		Handler: HandlerFunc(func(w http.ResponseWriter, r *http.Request) error {
			q := r.URL.Query()
			var areas []string
			for _, v := range q["area"] {
				for _, a := range strings.Split(v, ",") {
					if a = strings.TrimSpace(a); a != "" {
						areas = append(areas, a)
					}
				}
			}
			fast, err := parseBoolParam(q.Get("fast"))
			if err != nil {
				return NewError(CodeInvalidRequest, http.StatusBadRequest,
					map[string]any{"reason": "invalid_fast", "param": "fast"})
			}
			ro := doctor.RunOptions{Areas: areas, FastOnly: fast, Lang: InfoFrom(r.Context()).Lang, Now: opts.Now}
			if fast {
				ro.Budget = doctor.FastBudget
			}
			rep, err := reg.Run(r.Context(), ro)
			if errors.Is(err, doctor.ErrInvalidArea) {
				return NewError(CodeInvalidRequest, http.StatusBadRequest,
					map[string]any{"reason": "invalid_area", "param": "area", "areas": doctor.Areas()})
			}
			if err != nil {
				return err
			}
			w.Header().Set("Cache-Control", "no-store")
			WriteJSON(w, r, http.StatusOK, rep)
			return nil
		}),
	}
}

// RegisterDoctor adds GET /doctor to reg (the seam of section 3.9). AGENT-20 calls it with the
// registry it gives api.New; a nil reg means DefaultRegistry.
func RegisterDoctor(reg *Registry, opts DoctorOptions) error {
	if reg == nil {
		reg = DefaultRegistry
	}
	return reg.Add(DoctorRoute(opts))
}

// parseBoolParam reads a boolean query value: "" and 0/false/no are false, 1/true/yes are true.
func parseBoolParam(v string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", "0", "false", "no":
		return false, nil
	case "1", "true", "yes":
		return true, nil
	}
	return false, errors.New("not a boolean")
}
