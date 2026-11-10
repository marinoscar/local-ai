package jobs

import (
	"context"

	"github.com/marinoscar/local-ai/agent/internal/audit"
	"github.com/marinoscar/local-ai/agent/internal/cloud"
)

// EnsureStarted is what starting an install pass reports (ADR-0010).
type EnsureStarted struct {
	// Running is true when a pass runs after the call (started now, or already running).
	Running bool
	// State is the picture after the call: complete, installing, needs_you or off.
	State string
	// Pending is how many parts are not in place yet.
	Pending int
}

// Ensure is the install engine's start call: `POST /ensure` with the safe default, the same as the Portal's
// button. It never confirms compiling or restarting a running service (those need a person at the node, or
// the PIN), so the cloud cannot ask for them; the engine's refusals (a sync run holds the slot, a mode change
// is in flight) come back as *api.Error and end the command with their doctor code.
type Ensure interface {
	StartEnsure(ctx context.Context, actor audit.Actor) (EnsureStarted, error)
}

// EnsureRunResult is the result of ensure.run.
type EnsureRunResult struct {
	Started      bool   `json:"started"`
	State        string `json:"state"`
	PendingSteps int    `json:"pending_steps"`
}

// newEnsureRun is the cloud's "Install what's missing": it starts one pass and returns at once, the
// progress arrives in the snapshots' ensure block. Asking while a pass runs, or with nothing to do, is not
// an error: started is false.
func newEnsureRun(d Deps) cloud.Handler {
	return typed[noParams]{
		name: cloud.TypeEnsureRun, cancellable: true, decode: decodeNone,
		run: func(ctx context.Context, job cloud.Job, _ noParams) (cloud.Outcome, error) {
			res, err := d.Ensure.StartEnsure(ctx, job.Actor)
			if err != nil {
				return cloud.Outcome{}, err
			}
			return cloud.Outcome{Result: EnsureRunResult{Started: res.Running, State: ensureState(res.State), PendingSteps: max(res.Pending, 0)}}, nil
		},
	}
}

// ensureState keeps the result inside the schema's four values.
func ensureState(s string) string {
	switch s {
	case "complete", "installing", "needs_you", "off":
		return s
	}
	return "needs_you"
}
