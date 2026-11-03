// Package adapt connects the sync run engine (internal/sync/run) to the rest of the agent through the
// small interfaces each neighbour already defines. The engine knows none of them; this package knows
// all of them, so the seams stay one-directional (internal/api cannot import the engine, the engine
// does not import the HTTP layer's vocabulary, and the daemon wiring, AGENT-20, only composes):
//
//	Starter      api.ArkSyncStarter       POST /ark/sync (a real run)
//	Controller   api.ArkSyncController    GET and DELETE /ark/sync
//	Models       modelsapi.SyncEngine     model download and delete (rules R1 and R7)
//	Jobs         jobs.Ark                 the cloud's ark.sync and ark.verify
//	CloudSync    cloud.SyncSource         sync.last_run of the state snapshot
//	*run.Engine  modes.SyncController     CancelGraceful and ScheduleCatchup
//	*run.Engine  drill.ArkActivity        RunActive
//	GathererPlanner                       run.Planner over the planner's Gatherer
//
// Nothing here registers itself: the wiring builds each adapter and hands it over.
package adapt

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/api"
	"github.com/marinoscar/local-ai/agent/internal/ark"
	"github.com/marinoscar/local-ai/agent/internal/ark/ready"
	"github.com/marinoscar/local-ai/agent/internal/audit"
	"github.com/marinoscar/local-ai/agent/internal/cloud"
	"github.com/marinoscar/local-ai/agent/internal/cloud/jobs"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/drill"
	"github.com/marinoscar/local-ai/agent/internal/modelsapi"
	"github.com/marinoscar/local-ai/agent/internal/modes"
	"github.com/marinoscar/local-ai/agent/internal/sync/plan"
	"github.com/marinoscar/local-ai/agent/internal/sync/retention"
	"github.com/marinoscar/local-ai/agent/internal/sync/run"
)

// Compile-time proof that the engine is what its neighbours expect.
var (
	_ modes.SyncController  = (*run.Engine)(nil)
	_ drill.ArkActivity     = (*run.Engine)(nil)
	_ api.ArkSyncStarter    = Starter{}
	_ api.ArkSyncController = Controller{}
	_ modelsapi.SyncEngine  = Models{}
	_ jobs.Ark              = Jobs{}
	_ cloud.SyncSource      = CloudSync{}
	_ run.Planner           = GathererPlanner{}
)

// triggerOf is the trigger of a run an actor asked for: a cloud actor's is cloud, others keep what the
// route decoded.
func triggerOf(trigger string, actor audit.Actor) string {
	if actor.Kind == audit.KindCloud && (trigger == "" || trigger == run.TriggerManual) {
		return run.TriggerCloud
	}
	return trigger
}

// mapStartError turns the engine's typed errors into the *api.Error of a route.
func mapStartError(err error) error {
	var active *run.RunActiveError
	var mode *run.ModeError
	var unknown *run.UnknownRootError
	var none run.NoRootsError
	switch {
	case errors.As(err, &active):
		d := map[string]any{}
		if active.RunID != "" {
			d["run_id"] = active.RunID
		}
		return api.NewError(doctor.SYN005, http.StatusConflict, d)
	case errors.As(err, &mode):
		return api.NewError(mode.Code, http.StatusConflict, map[string]any{"mode": mode.Mode})
	case errors.As(err, &none):
		return api.NewError(doctor.ARK001, http.StatusConflict, nil)
	case errors.As(err, &unknown):
		return api.NewError(api.CodeInvalidRequest, http.StatusBadRequest, map[string]any{"field": "roots", "reason": "unknown_root", "value": unknown.Root})
	}
	return err
}

// Starter is api.ArkSyncStarter over the engine (POST /ark/sync without dry_run).
type Starter struct{ Engine *run.Engine }

// Start implements api.ArkSyncStarter.
func (s Starter) Start(ctx context.Context, req api.ArkSyncRequest) (api.ArkSyncStarted, error) {
	started, err := s.Engine.Start(ctx, run.StartRequest{
		Roots: req.Roots, Items: req.Items, AllowOfflineMode: req.AllowOfflineMode, Trigger: triggerOf(req.Trigger, req.Actor),
		OpID: req.OpID, Actor: req.Actor,
	})
	if err != nil {
		return api.ArkSyncStarted{}, mapStartError(err)
	}
	return api.ArkSyncStarted{RunID: started.RunID, State: started.State, Reason: started.Reason, Code: started.Code}, nil
}

// Controller is api.ArkSyncController over the engine (GET and DELETE /ark/sync).
type Controller struct{ Engine *run.Engine }

// Current implements api.ArkSyncController.
func (c Controller) Current(context.Context) (any, bool) {
	st, ok := c.Engine.Status()
	if !ok {
		return nil, false
	}
	// An ended run carries what became of each item (ark-sync.md section 3.5): that is where a skipped
	// item's reason and code are read. A running run has none yet.
	return st, true
}

// Cancel implements api.ArkSyncController.
func (c Controller) Cancel(_ context.Context, _ api.ArkSyncCancelRequest) (api.ArkSyncCancelled, error) {
	id, active := c.Engine.Cancel()
	return api.ArkSyncCancelled{RunID: id, Active: active}, nil
}

// Models is modelsapi.SyncEngine over the engine and retention (agent.md section 3.8.32).
type Models struct {
	Engine    *run.Engine
	Retention *retention.Retention
}

// StartModelDownload implements modelsapi.SyncEngine: a run of the one model, trigger manual (cloud for
// a cloud actor).
func (m Models) StartModelDownload(ctx context.Context, req modelsapi.SyncDownload) (modelsapi.SyncRun, error) {
	started, err := m.Engine.Start(ctx, run.StartRequest{
		Roots: req.Roots, Items: []string{req.Model.ID}, AllowOfflineMode: req.AllowOfflineMode,
		Trigger: triggerOf(run.TriggerManual, req.Actor), Actor: req.Actor,
	})
	if err != nil {
		var active *run.RunActiveError
		var unknown *run.UnknownRootError
		var none run.NoRootsError
		var mode *run.ModeError
		switch {
		case errors.As(err, &active):
			return modelsapi.SyncRun{}, &modelsapi.RunActiveError{RunID: active.RunID}
		case errors.As(err, &unknown):
			return modelsapi.SyncRun{}, &modelsapi.UnknownRootError{Root: unknown.Root}
		case errors.As(err, &none):
			return modelsapi.SyncRun{}, modelsapi.ErrNoWritableRoot
		case errors.As(err, &mode):
			return modelsapi.SyncRun{}, mapStartError(err)
		}
		return modelsapi.SyncRun{}, err
	}
	return modelsapi.SyncRun{ID: started.RunID}, nil
}

// DeleteModel implements modelsapi.SyncEngine: the model's copy on each named root (every mounted root
// when none is named), one root at a time, each through retention's CanDelete when KeepLast is set. A
// copy R1 refuses is kept and reported (last_verified_copy); a root with no copy is reported
// (not_present).
func (m Models) DeleteModel(ctx context.Context, req modelsapi.SyncDelete) (modelsapi.SyncDeleteResult, error) {
	var roots []ark.Root
	if len(req.Roots) == 0 {
		roots = m.Engine.Roots()
	} else {
		seen := map[string]bool{}
		for _, name := range req.Roots {
			r, ok := m.Engine.ResolveRoot(name)
			if !ok {
				return modelsapi.SyncDeleteResult{}, &modelsapi.UnknownRootError{Root: name}
			}
			if !seen[r.ID] {
				seen[r.ID] = true
				roots = append(roots, r)
			}
		}
	}
	var out modelsapi.SyncDeleteResult
	for _, r := range roots {
		res, err := m.Retention.DeleteCopy(ctx, retention.DeleteRequest{RootID: r.ID, ItemID: req.Model.ID, KeepLast: req.KeepLast})
		if err != nil {
			return out, err
		}
		if res.Deleted {
			out.Deleted = append(out.Deleted, modelsapi.DeletedCopy{Root: r.ID, Bytes: res.Bytes})
			continue
		}
		reason := res.Reason
		switch reason {
		case retention.ReasonNotPresent:
			reason = modelsapi.KeptNotPresent
		default:
			reason = modelsapi.KeptLastVerifiedCopy
		}
		out.Kept = append(out.Kept, modelsapi.KeptCopy{Root: r.ID, Reason: reason})
	}
	return out, nil
}

// ErrVerifyUnavailable: the re-verification (SYNC-13) is not wired yet.
var ErrVerifyUnavailable = errors.New("adapt: ark.verify is not available until the re-verification is wired")

// Verifier starts a verify run (SYNC-13): it shares the run slot with sync through the engine.
type Verifier interface {
	StartVerify(ctx context.Context, req jobs.ArkRequest) (jobs.ArkVerifyStarted, error)
}

// Jobs is jobs.Ark over the engine: the cloud's ark.sync and ark.verify.
type Jobs struct {
	Engine *run.Engine
	// Verifier serves ark.verify; nil answers ErrVerifyUnavailable.
	Verifier Verifier
}

// StartSync implements jobs.Ark. A dry run plans and answers the figures; a real run starts with the
// trigger cloud and answers its id.
func (j Jobs) StartSync(ctx context.Context, req jobs.ArkRequest) (jobs.ArkSyncStarted, error) {
	if req.DryRun {
		sum, err := j.Engine.PlanSummary(ctx, req.Roots, req.Items)
		if err != nil {
			return jobs.ArkSyncStarted{}, jobsError(err)
		}
		return jobs.ArkSyncStarted{ItemsToDownload: sum.ItemsToDownload, BytesToDownload: sum.BytesToDownload, ItemsSkippedBudget: sum.ItemsSkippedBudget}, nil
	}
	started, err := j.Engine.Start(ctx, run.StartRequest{Roots: req.Roots, Items: req.Items, Trigger: run.TriggerCloud, Actor: req.Actor})
	if err != nil {
		return jobs.ArkSyncStarted{}, jobsError(err)
	}
	out := jobs.ArkSyncStarted{RunID: started.RunID}
	if sum, err := j.Engine.PlanSummary(ctx, req.Roots, req.Items); err == nil {
		out.ItemsToDownload, out.BytesToDownload, out.ItemsSkippedBudget = sum.ItemsToDownload, sum.BytesToDownload, sum.ItemsSkippedBudget
	}
	return out, nil
}

// StartVerify implements jobs.Ark.
func (j Jobs) StartVerify(ctx context.Context, req jobs.ArkRequest) (jobs.ArkVerifyStarted, error) {
	if j.Verifier == nil {
		return jobs.ArkVerifyStarted{}, ErrVerifyUnavailable
	}
	return j.Verifier.StartVerify(ctx, req)
}

// jobsError maps the engine's errors to the ones the cloud executors understand: a run that is already
// active is *modelsapi.RunActiveError (the executor answers started: false with its id).
func jobsError(err error) error {
	var active *run.RunActiveError
	if errors.As(err, &active) {
		return &modelsapi.RunActiveError{RunID: active.RunID}
	}
	return mapStartError(err)
}

// CloudSync is cloud.SyncSource over the engine: the progress of the active run and the last finished
// one.
type CloudSync struct{ Engine *run.Engine }

// Sync implements cloud.SyncSource.
func (c CloudSync) Sync() cloud.SyncReport {
	st, ok := c.Engine.Status()
	if !ok {
		return cloud.SyncReport{}
	}
	if st.FinishedAt == nil {
		rep := cloud.SyncReport{Running: true, RunID: st.RunID}
		if st.Totals.BytesTotal > 0 {
			pct := 100 * float64(st.Totals.BytesDone) / float64(st.Totals.BytesTotal)
			rep.ProgressPct = &pct
		}
		return rep
	}
	return cloud.SyncReport{Last: lastRun(st)}
}

func lastRun(st run.RunStatus) *cloud.SyncRun {
	return &cloud.SyncRun{
		ID: st.RunID, FinishedAt: *st.FinishedAt, State: st.State, FailedItems: st.Totals.Failed, LeftItems: st.Left(),
		SkipReason: skipReason(st), ErrorCode: st.Code,
	}
}

func skipReason(st run.RunStatus) string {
	if st.State == run.StateSkipped {
		return st.Reason
	}
	return ""
}

// GathererPlanner is run.Planner over the planner's Gatherer: it gathers the requested roots together,
// plans them with plan.PlanAll (a download two roots share is sequenced, the second a local copy), and
// hands the engine the plans with the catalog they were made from.
type GathererPlanner struct{ G *plan.Gatherer }

// Roots implements run.Planner.
func (p GathererPlanner) Roots() []ark.Root { return p.G.Roots() }

// Plan implements run.Planner. req.Offline is the run's own verdict on the network (the engine's
// preflight: connectivity, and the mode with allow_offline_mode), and it overrides what the gatherer
// would read from the node: a run a person allowed in offline mode is planned online and its resolvers
// run; a node without connectivity is planned offline, from what the catalog pins and from material
// already on a root.
func (p GathererPlanner) Plan(ctx context.Context, req run.PlanRequest) (run.Plans, error) {
	online := !req.Offline
	inputs, err := p.G.GatherAll(ctx, req.RootIDs, plan.GatherOptions{Resolve: true, Online: &online})
	if err != nil {
		return run.Plans{}, err
	}
	out := run.Plans{Plans: plan.PlanAll(inputs), MinFree: map[string]int64{}}
	for _, rp := range out.Plans {
		out.MinFree[rp.Root] = rp.MinFreeBytes
		if out.CatalogVersion == "" {
			out.CatalogVersion = rp.CatalogVersion
		}
	}
	if len(inputs) > 0 && inputs[0].Catalog != nil {
		cat := inputs[0].Catalog
		out.Item = func(id string) (plan.CatalogItem, bool) {
			if m, ok := cat.Model(id); ok {
				return plan.ModelItem(m), true
			}
			if c, ok := cat.ContentItem(id); ok {
				return plan.ContentItem(c), true
			}
			return plan.CatalogItem{}, false
		}
	} else {
		out.Item = func(string) (plan.CatalogItem, bool) { return plan.CatalogItem{}, false }
	}
	return out, nil
}

// Ready is run.ReadyRefresher over the readiness service (SYNC-12): after a swap and at the end of a run
// the engine asks it to regenerate READY.json of the roots, and reads back each root's overall colour
// for sync.done.
type Ready struct{ Service *ready.Service }

// Refresh implements run.ReadyRefresher.
func (r Ready) Refresh(ctx context.Context, rootIDs []string) (map[string]string, error) {
	if err := r.Service.RunFinished(ctx, rootIDs...); err != nil {
		return nil, err
	}
	all := r.Service.Colours()
	out := make(map[string]string, len(rootIDs))
	for _, id := range rootIDs {
		if c, ok := all[id]; ok {
			out[id] = c
		}
	}
	return out, nil
}

// ReadyRuns is ready.RunSource over the engine: the last run that touched a root, of any trigger and
// final state, and when the last run that ended done finished.
type ReadyRuns struct{ Engine *run.Engine }

// LastRuns implements ready.RunSource.
func (r ReadyRuns) LastRuns(rootID string) (*ready.RunInfo, time.Time) {
	rr, ok := r.Engine.RootRun(rootID)
	if !ok {
		return nil, time.Time{}
	}
	return &ready.RunInfo{RunID: rr.RunID, Trigger: rr.Trigger, FinishedAt: rr.FinishedAt, State: rr.State}, rr.LastDone
}

// LastDriveSync implements ready.DriveSyncSource: the last run that ended done on any ark drive, even
// one that is unplugged now.
func (r ReadyRuns) LastDriveSync() time.Time { return r.Engine.LastDriveSync() }
