package jobs

import (
	"context"
	"errors"

	"github.com/marinoscar/local-ai/agent/internal/cloud"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/modelsapi"
)

// dryRunID is the sync_run_id of a dry run: the result schema wants a run id (1 to 64 characters)
// and a dry run has no run. started is false for it, like for a run that was already going.
const dryRunID = "dry-run"

// ArkSyncPlan is the plan member of the ark.sync result.
type ArkSyncPlan struct {
	ItemsToDownload    int64 `json:"items_to_download"`
	BytesToDownload    int64 `json:"bytes_to_download"`
	ItemsSkippedBudget int64 `json:"items_skipped_budget"`
}

// ArkSyncResult is the result of ark.sync.
type ArkSyncResult struct {
	SyncRunID string      `json:"sync_run_id"`
	Started   bool        `json:"started"`
	Plan      ArkSyncPlan `json:"plan"`
}

// ArkVerifyResult is the result of ark.verify.
type ArkVerifyResult struct {
	VerifyRunID  string `json:"verify_run_id"`
	Started      bool   `json:"started"`
	ItemsPlanned int64  `json:"items_planned"`
}

// newArkSync is POST /ark/sync with the trigger "cloud": it returns when the run has started, the
// progress arrives in the snapshots. A run that is already going is not an error: the answer is
// started: false with that run's id. A dry run writes nothing and answers the plan.
func newArkSync(d Deps) cloud.Handler {
	return typed[ArkSyncParams]{
		name: cloud.TypeArkSync, cancellable: true, decode: decodeArkSync,
		run: func(ctx context.Context, job cloud.Job, p ArkSyncParams) (cloud.Outcome, error) {
			res, err := d.Ark.StartSync(ctx, ArkRequest{Roots: p.Roots, Items: p.Items, DryRun: p.DryRun, Actor: job.Actor})
			var active *modelsapi.RunActiveError
			if errors.As(err, &active) && active.RunID != "" {
				return cloud.Outcome{Result: ArkSyncResult{SyncRunID: active.RunID}}, nil
			}
			if err != nil {
				return cloud.Outcome{}, err
			}
			out := ArkSyncResult{
				SyncRunID: res.RunID, Started: res.RunID != "" && !p.DryRun,
				Plan: ArkSyncPlan{
					ItemsToDownload: max(res.ItemsToDownload, 0), BytesToDownload: max(res.BytesToDownload, 0),
					ItemsSkippedBudget: max(res.ItemsSkippedBudget, 0),
				},
			}
			if out.SyncRunID == "" {
				out.SyncRunID = dryRunID
			}
			return cloud.Outcome{Result: out}, nil
		},
	}
}

// newArkVerify is POST /ark/verify (a run of trigger verify, sharing the sync run slot).
func newArkVerify(d Deps) cloud.Handler {
	return typed[ArkVerifyParams]{
		name: cloud.TypeArkVerify, cancellable: true, decode: decodeArkVerify,
		run: func(ctx context.Context, job cloud.Job, p ArkVerifyParams) (cloud.Outcome, error) {
			res, err := d.Ark.StartVerify(ctx, ArkRequest{Roots: p.Roots, Items: p.Items, Actor: job.Actor})
			var active *modelsapi.RunActiveError
			if errors.As(err, &active) && active.RunID != "" {
				return cloud.Outcome{Result: ArkVerifyResult{VerifyRunID: active.RunID}}, nil
			}
			if err != nil {
				return cloud.Outcome{}, err
			}
			if res.RunID == "" {
				return cloud.Outcome{}, jobErr(doctor.AGT099, "The verify run did not report an id.")
			}
			return cloud.Outcome{Result: ArkVerifyResult{VerifyRunID: res.RunID, Started: true, ItemsPlanned: max(res.ItemsPlanned, 0)}}, nil
		},
	}
}
