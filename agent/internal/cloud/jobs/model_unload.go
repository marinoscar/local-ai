package jobs

import (
	"context"

	"github.com/marinoscar/local-ai/agent/internal/cloud"
	"github.com/marinoscar/local-ai/agent/internal/router"
)

// ModelUnloadResult is the result of model.unload.
type ModelUnloadResult struct {
	ModelID   string `json:"model_id"`
	Status    string `json:"status"`
	WasLoaded bool   `json:"was_loaded"`
}

// newModelUnload is POST /models/{id}/unload (never forced: survival mode keeps its last model) with
// the 60 s wait for modelOpsMu.
func newModelUnload(d Deps) cloud.Handler {
	return typed[ModelIDParams]{
		name: cloud.TypeModelUnload, decode: decodeModelID,
		run: func(ctx context.Context, job cloud.Job, p ModelIDParams) (cloud.Outcome, error) {
			res, err := d.Router.UnloadWith(ctx, router.UnloadRequest{
				ID: p.ModelID, Actor: job.Actor, LockWait: router.DefaultLockWait,
			})
			if err != nil {
				return cloud.Outcome{}, err
			}
			return cloud.Outcome{Result: ModelUnloadResult{ModelID: p.ModelID, Status: "unloaded", WasLoaded: !res.AlreadyUnloaded}}, nil
		},
	}
}
