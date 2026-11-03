package jobs

import (
	"context"

	"github.com/marinoscar/local-ai/agent/internal/cloud"
	"github.com/marinoscar/local-ai/agent/internal/modelsapi"
)

// KeyRotateResult is the result of key.rotate. The key itself is never in it, only its fingerprint.
type KeyRotateResult struct {
	Fingerprint string `json:"fingerprint"`
	RotatedAt   string `json:"rotated_at"`
	CloudSynced bool   `json:"cloud_synced"`
}

// newKeyRotate is POST /key/rotate with the route's defaults (llama restarts and the models come
// back). modelsapi.Service.RotateKey pushes the new key to the cloud before it returns, so the result
// is settled after the push; cloud_synced false says the push did not go through and the link retries
// it (LAI-CLD-060). A model operation that holds modelOpsMu is waited for up to 60 s; once the key is
// written nothing is repeated.
func newKeyRotate(d Deps) cloud.Handler {
	return typed[noParams]{
		name: cloud.TypeKeyRotate, decode: decodeNone,
		run: func(ctx context.Context, job cloud.Job, _ noParams) (cloud.Outcome, error) {
			res, err := retryBusy(ctx, d, func() (modelsapi.KeyRotated, error) {
				return d.Models.RotateKey(ctx, modelsapi.KeyRotateRequest{Restart: true, Actor: job.Actor})
			})
			if err != nil {
				return cloud.Outcome{}, err
			}
			return cloud.Outcome{Result: KeyRotateResult{Fingerprint: res.Fingerprint, RotatedAt: res.RotatedAt, CloudSynced: res.CloudSynced}}, nil
		},
	}
}
