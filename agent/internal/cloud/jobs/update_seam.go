package jobs

import (
	"context"

	"github.com/marinoscar/local-ai/agent/internal/cloud"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
)

// AgentUpdateResult is the result of agent.update (cloud-app.md section 3.7): the AGENT-19 updater
// builds it, status is updated, already_current or rolled_back.
type AgentUpdateResult struct {
	FromVersion string `json:"from_version"`
	ToVersion   string `json:"to_version"`
	Status      string `json:"status"`
}

// newAgentUpdate is the registration seam of agent.update. This package owns the strict decoder
// ({version: semver}); the executor is AGENT-19's: it passes an AgentUpdater in Deps.AgentUpdate.
// Until then the type is registered and fails with LAI-AGT-023, so a cloud that asks for an update
// gets a plain answer instead of "this node does not allow it". The type needs inflight.json: the new
// process settles the job from it after its health check, so a node that cannot write it refuses.
func newAgentUpdate(d Deps) cloud.Handler {
	return typed[AgentUpdateParams]{
		name: cloud.TypeAgentUpdate, inflight: true, decode: decodeAgentUpdate,
		run: func(ctx context.Context, job cloud.Job, p AgentUpdateParams) (cloud.Outcome, error) {
			if d.AgentUpdate == nil {
				return cloud.Outcome{}, jobErr(doctor.AGT023, "Updating the agent from the cloud is not available on this node.")
			}
			return d.AgentUpdate.Update(ctx, job, p.Version)
		},
	}
}
