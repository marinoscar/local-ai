package jobs

import (
	"context"
	"errors"

	"github.com/marinoscar/local-ai/agent/internal/cloud"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
)

// StatusRefreshResult is the result of status.refresh: {seq, reported_at}.
type StatusRefreshResult struct {
	Seq        int64  `json:"seq"`
	ReportedAt string `json:"reported_at"`
}

// newStatusRefresh builds a snapshot and pushes it now; when the cloud does not hold the llama key
// yet it sends the key first, so the snapshot (and the cloud's view) show it synced.
func newStatusRefresh(d Deps) cloud.Handler {
	return typed[noParams]{
		name: cloud.TypeStatusRefresh, cancellable: true, decode: decodeNone,
		run: func(ctx context.Context, job cloud.Job, _ noParams) (cloud.Outcome, error) {
			if !d.State.Snapshot().Key.CloudSynced {
				if err := d.Link.Push(ctx); err != nil {
					job.Logger.Debug("the key was not sent with the status refresh", "error", err.Error())
				}
			}
			res, err := d.Link.PushNow(ctx)
			switch {
			case errors.Is(err, cloud.ErrNoStateEndpoint):
				return cloud.Outcome{}, jobErr(doctor.CLD056, "The cloud has no node-state endpoint yet, so a status cannot be sent on request.")
			case errors.Is(err, cloud.ErrNotLinked):
				return cloud.Outcome{}, jobErr(doctor.CLD050, "This node is not connected to the cloud.")
			case err != nil:
				return cloud.Outcome{}, jobErr(doctor.CLD053, "The status could not be sent to the cloud.")
			}
			at := res.ReportedAt
			if at == "" {
				at = d.clock().Now().UTC().Format("2006-01-02T15:04:05Z")
			}
			return cloud.Outcome{Result: StatusRefreshResult{Seq: res.Seq, ReportedAt: at}}, nil
		},
	}
}
