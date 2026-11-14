package jobs

import (
	"context"
	"encoding/json"
	"regexp"

	"github.com/marinoscar/local-ai/agent/internal/audit"
	"github.com/marinoscar/local-ai/agent/internal/cloud"
)

// Workloads is the workloads API's activation (agent.md section 3.8.34, llama-router.md section
// 4.16): POST /workloads/{name}/activate with wait: true. The workloads engine of #509 implements it,
// or an adapter over it does; without it workload.activate is left out of Handlers and refused with
// LAI-CLD-057. Its errors are *api.Error (LAI-LLM-050, LAI-AGT-022, LAI-LLM-053, LAI-LLM-051,
// LAI-LLM-052) and settle the command with their code.
type Workloads interface {
	Activate(ctx context.Context, name string, force bool, actor audit.Actor) (active string, err error)
}

// WorkloadActivateParams is {name, force?} (cloud-app.md section 3.7).
type WorkloadActivateParams struct {
	Name  string
	Force bool
}

// WorkloadActivateResult is the result of workload.activate.
type WorkloadActivateResult struct {
	Active string `json:"active"`
}

var workloadNameRE = regexp.MustCompile(`^[a-z][a-z0-9-]{1,31}$`)

func decodeWorkloadActivate(raw json.RawMessage) (WorkloadActivateParams, error) {
	var w struct {
		Name  *string `json:"name"`
		Force *bool   `json:"force"`
	}
	if err := cloud.DecodeParams(raw, &w); err != nil {
		return WorkloadActivateParams{}, err
	}
	if err := required("name", w.Name != nil); err != nil {
		return WorkloadActivateParams{}, err
	}
	if !workloadNameRE.MatchString(*w.Name) {
		return WorkloadActivateParams{}, bad("name", "must be a workload name (a lower case letter, then letters, digits and -; 2 to 32 characters)")
	}
	p := WorkloadActivateParams{Name: *w.Name}
	if w.Force != nil {
		p.Force = *w.Force
	}
	return p, nil
}

// newWorkloadActivate switches the node to a workload and waits for it. Like model.load it is not
// cancellable: an activation carries on locally when the node goes offline (agent.md section 4.8.8).
func newWorkloadActivate(d Deps) cloud.Handler {
	return typed[WorkloadActivateParams]{
		name: cloud.TypeWorkloadActivate, decode: decodeWorkloadActivate,
		run: func(ctx context.Context, job cloud.Job, p WorkloadActivateParams) (cloud.Outcome, error) {
			active, err := d.Workloads.Activate(ctx, p.Name, p.Force, job.Actor)
			if err != nil {
				return cloud.Outcome{}, err
			}
			return cloud.Outcome{Result: WorkloadActivateResult{Active: active}}, nil
		},
	}
}
