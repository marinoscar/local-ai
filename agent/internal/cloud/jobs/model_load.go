package jobs

import (
	"context"

	"github.com/marinoscar/local-ai/agent/internal/cloud"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/router"
)

// ModelLoadResult is the result of model.load.
type ModelLoadResult struct {
	ModelID   string   `json:"model_id"`
	Status    string   `json:"status"`
	LoadMS    int64    `json:"load_ms"`
	Unloaded  []string `json:"unloaded"`
	VRAMUsed  *int64   `json:"vram_used_mib"`
	IsDefault bool     `json:"is_default"`
}

// maxUnloaded is the schema's bound on the models a load evicted.
const maxUnloaded = 16

// newModelLoad is POST /models/{id}/load with wait: true, then PUT /models/default when set_default.
// The load waits up to 60 s for modelOpsMu. vram_used_mib is read from a fresh hardware sample after
// the load, is_default from the status view.
func newModelLoad(d Deps) cloud.Handler {
	return typed[ModelLoadParams]{
		name: cloud.TypeModelLoad, decode: decodeModelLoad,
		run: func(ctx context.Context, job cloud.Job, p ModelLoadParams) (cloud.Outcome, error) {
			res, err := d.Router.LoadWith(ctx, router.LoadRequest{
				ID: p.ModelID, Wait: true, UnloadOthers: p.UnloadOthers, Actor: job.Actor, LockWait: router.DefaultLockWait,
			})
			if err != nil {
				return cloud.Outcome{}, err
			}
			if res.Accepted {
				return cloud.Outcome{}, jobErr(doctor.AGT099, "The model load did not wait for the model.")
			}
			if p.SetDefault {
				if _, err := d.Router.SetDefault(ctx, p.ModelID, job.Actor); err != nil {
					return cloud.Outcome{}, err
				}
			}
			if d.Hardware != nil {
				d.Hardware.Refresh(ctx)
			}
			st := d.Status.Build(ctx, true, statusOptions())
			out := ModelLoadResult{
				ModelID: p.ModelID, Status: "loaded", LoadMS: max(res.LoadMS, 0), Unloaded: []string{},
				VRAMUsed:  nonNegative(st.Hardware.VRAMUsedMiB),
				IsDefault: st.Models.Default != nil && *st.Models.Default == p.ModelID,
			}
			for _, id := range res.Unloaded {
				if len(out.Unloaded) < maxUnloaded {
					out.Unloaded = append(out.Unloaded, id)
				}
			}
			return cloud.Outcome{Result: out}, nil
		},
	}
}

func nonNegative(v *int64) *int64 {
	if v == nil {
		return nil
	}
	n := max(*v, 0)
	return &n
}
