package jobs

import (
	"context"

	"github.com/marinoscar/local-ai/agent/internal/cloud"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/logx"
	"github.com/marinoscar/local-ai/agent/internal/modes"
	"github.com/marinoscar/local-ai/agent/internal/state"
)

// ModeOfflineResult is the result of mode.offline.
type ModeOfflineResult struct {
	PreviousMode string `json:"previous_mode"`
	Mode         string `json:"mode"`
}

// newModeOffline is POST /mode {target: offline}, in the order of agent.md section 4.8.6: the result
// is settled first, then the transition runs (Outcome.After), because the transition stops the link.
// The cloud cannot leave a node in survival mode: this command refuses it (LAI-AGT-023), as it
// refuses every other way to change the mode. Only someone at the node can bring it back online.
func newModeOffline(d Deps) cloud.Handler {
	return typed[ModeOfflineParams]{
		name: cloud.TypeModeOffline, cancellable: true, decode: decodeModeOffline,
		run: func(ctx context.Context, job cloud.Job, p ModeOfflineParams) (cloud.Outcome, error) {
			info := d.Modes.Info()
			if info.Effective == modes.Survival {
				return cloud.Outcome{}, jobErr(doctor.AGT023, "The node is in survival mode; the cloud cannot change it.")
			}
			if p.Reason != "" {
				job.Logger.Info("the cloud asked the node to go offline", "reason", logx.ScrubString(printable(p.Reason)))
			}
			actor := state.Actor{Kind: "cloud", Via: "job"}
			return cloud.Outcome{
				Result: ModeOfflineResult{PreviousMode: string(info.Mode), Mode: string(modes.Offline)},
				After: func(ctx context.Context) {
					if _, err := d.Modes.Request(ctx, modes.Offline, modes.RequestOptions{Actor: actor}); err != nil {
						job.Logger.Warn("the offline transition did not start", "error", logx.ScrubString(err.Error()))
					}
				},
			}, nil
		},
	}
}

// printable keeps the printable ASCII of free text from the cloud (at most 200 characters) before
// it reaches the log.
func printable(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		if len(out) >= maxReason {
			break
		}
		if r >= 0x20 && r < 0x7f {
			out = append(out, r)
		} else {
			out = append(out, '?')
		}
	}
	return string(out)
}
