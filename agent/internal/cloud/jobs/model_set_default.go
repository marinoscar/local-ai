package jobs

import (
	"context"

	"github.com/marinoscar/local-ai/agent/internal/cloud"
)

// ModelSetDefaultResult is the result of model.set_default; Previous is null when there was none.
type ModelSetDefaultResult struct {
	ModelID  string  `json:"model_id"`
	Previous *string `json:"previous"`
}

// newModelSetDefault is PUT /models/default.
func newModelSetDefault(d Deps) cloud.Handler {
	return typed[ModelIDParams]{
		name: cloud.TypeModelSetDefault, decode: decodeModelID,
		run: func(ctx context.Context, job cloud.Job, p ModelIDParams) (cloud.Outcome, error) {
			res, err := d.Router.SetDefault(ctx, p.ModelID, job.Actor)
			if err != nil {
				return cloud.Outcome{}, err
			}
			out := ModelSetDefaultResult{ModelID: p.ModelID}
			if res.Previous != "" {
				prev := res.Previous
				out.Previous = &prev
			}
			return cloud.Outcome{Result: out}, nil
		},
	}
}
