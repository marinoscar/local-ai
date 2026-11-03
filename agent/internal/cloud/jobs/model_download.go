package jobs

import (
	"context"
	"slices"

	"github.com/marinoscar/local-ai/agent/internal/cloud"
	"github.com/marinoscar/local-ai/agent/internal/modelsapi"
	"github.com/marinoscar/local-ai/agent/internal/statusapi"
)

// ModelDownloadResult is the result of model.download; SyncRunID is null when the model is already
// there.
type ModelDownloadResult struct {
	ModelID        string  `json:"model_id"`
	AlreadyPresent bool    `json:"already_present"`
	SyncRunID      *string `json:"sync_run_id"`
	BytesPlanned   int64   `json:"bytes_planned"`
}

// newModelDownload is POST /models/{id}/download: it queues a sync run for the model and returns at
// once (the transfer is the sync engine's, its progress arrives in the snapshots). A model that is
// already verified on every named root (every mounted root when none is named) is answered
// already_present without a run; bytes_planned is the model's size when a run was queued (the
// engine's own plan is its business and arrives with the run).
func newModelDownload(d Deps) cloud.Handler {
	return typed[ModelDownloadParams]{
		name: cloud.TypeModelDownload, cancellable: true, decode: decodeModelDownload,
		run: func(ctx context.Context, job cloud.Job, p ModelDownloadParams) (cloud.Outcome, error) {
			row, err := d.Models.Get(ctx, p.ModelID)
			if err != nil {
				return cloud.Outcome{}, err
			}
			if row.Present && row.Verified && onEveryRoot(row.Locations, p.Roots) {
				return cloud.Outcome{Result: ModelDownloadResult{ModelID: p.ModelID, AlreadyPresent: true}}, nil
			}
			acc, err := d.Models.Download(ctx, modelsapi.DownloadRequest{ID: p.ModelID, Roots: p.Roots, Actor: job.Actor})
			if err != nil {
				return cloud.Outcome{}, err
			}
			run := acc.SyncRunID
			return cloud.Outcome{Result: ModelDownloadResult{
				ModelID: p.ModelID, SyncRunID: &run, BytesPlanned: max(row.SizeBytes, 0),
			}}, nil
		},
	}
}

// onEveryRoot reports whether every named root holds a complete copy; with no root named, the
// internal root (the one every node has) is the one that counts: copying to a drive is the sync
// engine's scheduled work.
func onEveryRoot(locs []statusapi.ModelLocation, roots []string) bool {
	if len(roots) == 0 {
		roots = []string{"internal"}
	}
	for _, r := range roots {
		if !slices.ContainsFunc(locs, func(l statusapi.ModelLocation) bool { return l.Root == r && l.Complete }) {
			return false
		}
	}
	return true
}
