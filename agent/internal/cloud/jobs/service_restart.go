package jobs

import (
	"context"

	"github.com/marinoscar/local-ai/agent/internal/api"
	"github.com/marinoscar/local-ai/agent/internal/cloud"
	"github.com/marinoscar/local-ai/agent/internal/services"
)

// ServiceRestartResult is the result of service.restart.
type ServiceRestartResult struct {
	Service     string `json:"service"`
	ActiveState string `json:"active_state"`
	RestartedAt string `json:"restarted_at"`
}

// newServiceRestart is POST /services/{name}/restart: the same survival rule, the same llama hook
// (the models come back after a llama restart) and the same audit line as the route. The unit is
// named by the closed set llama, kiwix, whisper; nothing else reaches systemd. A model operation
// that holds modelOpsMu is waited for up to 60 s.
func newServiceRestart(d Deps) cloud.Handler {
	return typed[ServiceRestartParams]{
		name: cloud.TypeServiceRestart, decode: decodeServiceRestart,
		run: func(ctx context.Context, job cloud.Job, p ServiceRestartParams) (cloud.Outcome, error) {
			res, err := retryBusy(ctx, d, func() (api.ServiceActionResult, error) {
				return d.Services.RunAction(ctx, api.ServiceActionRequest{Name: p.Service, Action: services.ActionRestart, Actor: job.Actor})
			})
			if err != nil {
				return cloud.Outcome{}, err
			}
			return cloud.Outcome{Result: ServiceRestartResult{
				Service: p.Service, ActiveState: activeState(res.Active), RestartedAt: d.clock().Now().UTC().Format("2006-01-02T15:04:05Z"),
			}}, nil
		},
	}
}

// activeState maps a unit's active state to the schema's enum; after a restart that returned without
// an error anything but active or activating is failed.
func activeState(s string) string {
	switch s {
	case "active", "activating":
		return s
	}
	return "failed"
}
