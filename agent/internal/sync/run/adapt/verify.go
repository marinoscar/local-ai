package adapt

import (
	"context"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/api"
	"github.com/marinoscar/local-ai/agent/internal/cloud/jobs"
	"github.com/marinoscar/local-ai/agent/internal/sync/run"
	"github.com/marinoscar/local-ai/agent/internal/sync/verify"
)

var (
	_ api.ArkVerifyStarter = VerifyStarter{}
	_ Verifier             = CloudVerifier{}
)

// VerifyStarter is api.ArkVerifyStarter over the engine: POST /ark/verify starts a run of trigger
// verify, which shares the run slot with sync. What the run does is the Verify hook the wiring set
// (Reverification.Run).
type VerifyStarter struct{ Engine *run.Engine }

// StartVerify implements api.ArkVerifyStarter.
func (v VerifyStarter) StartVerify(ctx context.Context, req api.ArkVerifyRequest) (api.ArkSyncStarted, error) {
	started, err := v.Engine.Start(ctx, run.StartRequest{
		Roots: req.Roots, Items: req.Items, Full: req.Full, Trigger: run.TriggerVerify, OpID: req.OpID, Actor: req.Actor,
	})
	if err != nil {
		return api.ArkSyncStarted{}, mapStartError(err)
	}
	return api.ArkSyncStarted{RunID: started.RunID, State: started.State, Reason: started.Reason, Code: started.Code}, nil
}

// CloudVerifier is the Verifier of Jobs: the cloud's ark.verify starts the same run for a cloud actor.
type CloudVerifier struct {
	Engine *run.Engine
	// Reverifier, when set, counts the copies the run will check (the command's items_planned).
	Reverifier *verify.Reverifier
	// Settings returns the sync: block now; reverify_days is read from it for the count.
	Settings func() run.Settings
}

// StartVerify implements Verifier.
func (v CloudVerifier) StartVerify(ctx context.Context, req jobs.ArkRequest) (jobs.ArkVerifyStarted, error) {
	var planned int64
	if v.Reverifier != nil {
		days := 0
		if v.Settings != nil {
			days = v.Settings().ReverifyDays
		}
		var ids []string
		for _, name := range req.Roots {
			if r, ok := v.Engine.ResolveRoot(name); ok {
				ids = append(ids, r.ID)
			}
		}
		if cs, err := v.Reverifier.Candidates(ctx, verify.Options{Roots: ids, Items: req.Items, DueDays: days}); err == nil {
			planned = int64(len(cs))
		}
	}
	started, err := v.Engine.Start(ctx, run.StartRequest{
		Roots: req.Roots, Items: req.Items, Trigger: run.TriggerVerify, Actor: req.Actor,
	})
	if err != nil {
		return jobs.ArkVerifyStarted{}, jobsError(err)
	}
	return jobs.ArkVerifyStarted{RunID: started.RunID, ItemsPlanned: planned}, nil
}

// Reverification adapts a verify.Reverifier to the two hooks of the engine: the slice at the end of a
// scheduled run (Hooks.Reverify) and the body of a verify run (Hooks.Verify).
type Reverification struct {
	Reverifier *verify.Reverifier
	// Settings returns the sync: block now (reverify_days, reverify_budget_minutes).
	Settings func() run.Settings
}

// Slice is run.Hooks.Reverify: up to budget re-hashing the copies that are due, oldest first. It reports
// to the run only the copies that turned out damaged.
func (r Reverification) Slice(ctx context.Context, rc run.RunContext, budget time.Duration) {
	_, _ = r.Reverifier.Run(ctx, verify.Options{
		DueDays: r.Settings().ReverifyDays, Budget: budget, Begin: rc.Begin, Record: recorder(rc),
	})
}

// Run is run.Hooks.Verify, the body of POST /ark/verify. A run that names items or asks for full has no
// time budget (a person asked for exactly that); a plain one is the scheduled slice with a person
// waiting: the copies that are due, within reverify_budget_minutes. The copies the budget left are
// recorded as skipped (time_limit), so the run says it did not finish.
func (r Reverification) Run(ctx context.Context, rc run.RunContext, req run.VerifyRequest) {
	s := req.Settings
	var budget time.Duration
	if !req.Full && len(req.Items) == 0 {
		budget = time.Duration(s.ReverifyBudgetMinutes) * time.Minute
	}
	rep, _ := r.Reverifier.Run(ctx, verify.Options{
		Roots: req.Roots, Items: req.Items, Full: req.Full, DueDays: s.ReverifyDays, Budget: budget,
		Begin: rc.Begin, Record: recorder(rc),
	})
	for _, left := range rep.Left {
		rc.Record(run.ItemResult{Root: left.Root, Item: left.Item, Result: verify.ResultSkipped, Version: left.Version, Reason: verify.ReasonTimeLimit}, 0)
	}
}

// recorder turns an Outcome into the run's ItemResult.
func recorder(rc run.RunContext) func(verify.Outcome) {
	return func(o verify.Outcome) {
		rc.Record(run.ItemResult{Root: o.Root, Item: o.Item, Result: o.Result, Version: o.Version, Reason: o.Reason, Code: o.Code}, o.Bytes)
	}
}
