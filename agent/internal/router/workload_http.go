package router

import (
	"errors"
	"net/http"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/api"
)

// The workload routes (agent.md section 3.8.34): GET /workloads and POST /workloads/{name}/activate,
// both auth op. They live here, next to the orchestrator, because internal/api cannot import this
// package.

// WorkloadRouteOptions configures RegisterWorkloads.
type WorkloadRouteOptions struct {
	Orchestrator *Orchestrator
	// LoadTimeout and ModelsMax size the deadline of an activation with wait: load_timeout × (n+2) +
	// 60 s. Zero values mean 300 s and 1.
	LoadTimeout time.Duration
	ModelsMax   int
}

func (o WorkloadRouteOptions) activateTimeout() time.Duration {
	t, n := o.LoadTimeout, o.ModelsMax
	if t <= 0 {
		t = DefaultLoadTimeout
	}
	if n < 1 {
		n = 1
	}
	return t*time.Duration(n+2) + 60*time.Second
}

// WorkloadRoutes returns the two routes.
func WorkloadRoutes(o WorkloadRouteOptions) []api.Route {
	return []api.Route{
		{Method: http.MethodGet, Pattern: "/workloads", Auth: []api.Level{api.AuthOp}, Timeout: 5 * time.Second, Handler: api.HandlerFunc(o.list)},
		{Method: http.MethodPost, Pattern: "/workloads/{name}/activate", Auth: []api.Level{api.AuthOp}, Timeout: o.activateTimeout(), Handler: api.HandlerFunc(o.activate)},
	}
}

// RegisterWorkloads adds the routes to reg (api.DefaultRegistry when nil).
func RegisterWorkloads(reg *api.Registry, o WorkloadRouteOptions) error {
	if o.Orchestrator == nil {
		return errors.New("router: the workload routes need an orchestrator")
	}
	if reg == nil {
		reg = api.DefaultRegistry
	}
	return reg.Add(WorkloadRoutes(o)...)
}

type workloadLabelJSON struct {
	EN string `json:"en"`
	ES string `json:"es"`
}

type workloadJSON struct {
	Name    string            `json:"name"`
	Label   workloadLabelJSON `json:"label"`
	Models  []string          `json:"models"`
	Active  bool              `json:"active"`
	Fits    bool              `json:"fits"`
	Missing []string          `json:"missing"`
}

type workloadsJSON struct {
	Workloads []workloadJSON `json:"workloads"`
	Active    *string        `json:"active"`
}

// WorkloadsBody is the JSON body of GET /workloads for a view; the cloud snapshot reuses its shape.
func WorkloadsBody(v WorkloadsView) any {
	out := workloadsJSON{Workloads: []workloadJSON{}}
	for _, w := range v.Workloads {
		out.Workloads = append(out.Workloads, workloadJSON{
			Name: w.Name, Label: workloadLabelJSON{EN: w.Label.EN, ES: w.Label.ES}, Models: nz(w.Models),
			Active: w.Active, Fits: w.Fits, Missing: nz(w.Missing),
		})
	}
	if v.Active != "" {
		a := v.Active
		out.Active = &a
	}
	return out
}

func (o WorkloadRouteOptions) list(w http.ResponseWriter, r *http.Request) error {
	api.WriteJSON(w, r, http.StatusOK, WorkloadsBody(o.Orchestrator.Workloads(r.Context())))
	return nil
}

// activateBody is the body of POST /workloads/{name}/activate; both fields default to false.
type activateBody struct {
	Wait  bool `json:"wait"`
	Force bool `json:"force"`
}

type activatedJSON struct {
	OpID     string   `json:"op_id"`
	Active   string   `json:"active"`
	Loaded   []string `json:"loaded"`
	Unloaded []string `json:"unloaded"`
}

type acceptedJSON struct {
	OpID string `json:"op_id"`
}

func (o WorkloadRouteOptions) activate(w http.ResponseWriter, r *http.Request) error {
	var body activateBody
	if err := api.DecodeJSONOrEmpty(w, r, &body); err != nil {
		return err
	}
	info := api.InfoFrom(r.Context())
	res, err := o.Orchestrator.ActivateWorkload(WithReqID(r.Context(), info.ID), WorkloadRequest{
		Name: r.PathValue("name"), Force: body.Force, Wait: body.Wait, Actor: api.AuditActor(info),
	})
	if res.OpID != "" {
		api.SetOpID(w, res.OpID)
	}
	if err != nil {
		return apiError(info.Lang, err)
	}
	if res.Accepted {
		api.WriteJSON(w, r, http.StatusAccepted, acceptedJSON{OpID: res.OpID})
		return nil
	}
	api.WriteJSON(w, r, http.StatusOK, activatedJSON{OpID: res.OpID, Active: res.Active, Loaded: nz(res.Loaded), Unloaded: nz(res.Unloaded)})
	return nil
}
