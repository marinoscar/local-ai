package run

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/ark"
	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/events"
	"github.com/marinoscar/local-ai/agent/internal/kit"
	"github.com/marinoscar/local-ai/agent/internal/sync/item"
	"github.com/marinoscar/local-ai/agent/internal/sync/journal"
	"github.com/marinoscar/local-ai/agent/internal/sync/plan"
)

// LastRunName is the file in the journal directory that remembers the last finished run, so GET
// /ark/sync answers after a restart too.
const LastRunName = "last-run.json"

// Deps are the engine's collaborators. Journal, Machine, Planner, Copies, FS and Settings are
// required; everything else is optional and has a safe default.
type Deps struct {
	Journal *journal.Journal
	Machine ItemMachine
	Planner Planner
	// Copies finds the verified copies of an item (the best-copy index), for local copies.
	Copies CopyFinder
	// FS reads manifests of source copies and writes last-run.json.
	FS config.FS
	// JournalDir is where last-run.json lives (journal.DefaultDir when empty).
	JournalDir string

	Settings func() Settings
	// Net is the node's mode and connectivity; nil means online with the internet reachable.
	Net func() NetState
	// Power says whether the node is on battery; nil means never.
	Power Power
	// Catalog refreshes the catalog before the plan; nil skips it.
	Catalog CatalogRefresher
	// Resolvers are the source resolvers; a type with none falls back to what the catalog pins.
	Resolvers *ItemResolvers
	// ItemRef places an item (ark.ItemDir); the default handles every kind, kit items through
	// internal/kit's layout.
	ItemRef func(plan.CatalogItem) (ark.ItemRef, error)
	// Retention runs at the end of a run; Unknown lists a root's unexplained staging for it.
	Retention Retainer
	Unknown   func(root ark.Root) ([]string, error)

	Events    Publisher
	Ready     ReadyRefresher
	Hooks     Hooks
	RootFault RootFaultFunc

	// Lock is the cross-process run slot; nil means the in-memory guard only.
	Lock Locker
	// Clock is the time source; the wall clock when nil.
	Clock Clock
	// Priority sets the priority of a worker; LowPriority when nil.
	Priority func(scheduled bool) error
	Logger   *slog.Logger
}

// Engine is the run engine of a node. One per agent.
type Engine struct {
	d Deps

	mu       sync.Mutex
	active   *runState
	last     *RunStatus
	catchup  func() bool
	rootRuns map[string]RootRun
}

// Causes of a cancelled context, to tell a cancel, a mode switch and the time limit apart.
var (
	errCancelUser = errors.New("run: cancelled by a request")
	errCancelMode = errors.New("run: cancelled by a mode switch")
	errTimeLimit  = errors.New("run: max_run_hours reached")
	errRootGone   = errors.New("run: the root was unplugged or is being ejected")
)

// SkipRootGone is the reason of an item that did not finish because its root went away (a drive
// unplugged or ejected during the run, LAI-ARK-007).
const SkipRootGone = "root_gone"

// New checks the dependencies and returns an engine. It reads last-run.json when there is one.
func New(d Deps) (*Engine, error) {
	var missing []string
	if d.Journal == nil {
		missing = append(missing, "Journal")
	}
	if d.Machine == nil {
		missing = append(missing, "Machine")
	}
	if d.Planner == nil {
		missing = append(missing, "Planner")
	}
	if d.Copies == nil {
		missing = append(missing, "Copies")
	}
	if d.FS == nil {
		missing = append(missing, "FS")
	}
	if d.Settings == nil {
		missing = append(missing, "Settings")
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("run: the engine needs %v", missing)
	}
	if d.Clock == nil {
		d.Clock = RealClock()
	}
	if d.Priority == nil {
		d.Priority = LowPriority
	}
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	if d.Net == nil {
		d.Net = func() NetState { return NetState{Mode: plan.ModeOnline, Online: true} }
	}
	if d.JournalDir == "" {
		d.JournalDir = journal.DefaultDir
	}
	if d.ItemRef == nil {
		d.ItemRef = DefaultItemRef
	}
	e := &Engine{d: d}
	e.loadLast()
	e.loadRootRuns()
	return e, nil
}

// DefaultItemRef places a catalog item for ark.ItemDir: a model by its engine, a content item by its
// kind, and a kit item by the layout of rebuild-kit.md (internal/kit, which names its subdirectory).
func DefaultItemRef(it plan.CatalogItem) (ark.ItemRef, error) {
	if it.Kind == "kit" {
		if it.Content == nil {
			return ark.ItemRef{}, fmt.Errorf("run: kit item %s has no catalog entry", it.ID)
		}
		return kit.ItemRef(*it.Content)
	}
	return ark.ItemRef{Kind: it.Kind, Engine: it.Engine, ID: it.ID}, nil
}

func (e *Engine) lastPath() string { return filepath.Join(e.d.JournalDir, LastRunName) }

func (e *Engine) loadLast() {
	data, err := e.d.FS.ReadFile(e.lastPath())
	if err != nil {
		return
	}
	var st RunStatus
	if json.Unmarshal(data, &st) != nil || !journal.ValidRunID(st.RunID) {
		return
	}
	// A run that was running when the agent died did not finish: say so.
	if st.State == StateRunning || st.State == StateCancelling {
		st.State, st.Result = StateFailed, ResultFailed
	}
	st.Active = nil
	e.last = &st
}

// ---- state of a run ---------------------------------------------------------------------------------

type activeEntry struct {
	ActiveItem
	lastEmit  time.Time
	lastState string
	lastBytes int64
	lastAt    time.Time
	planned   int64
}

type runState struct {
	e       *Engine
	id      string
	trigger string
	// scheduled is the trigger class that gets a time limit and the idle I/O class.
	scheduled bool
	settings  Settings
	opID      string
	items     map[string]bool

	ctx    context.Context
	cancel context.CancelCauseFunc
	run    *journal.Run
	lease  Lease
	done   chan struct{}

	mu         sync.Mutex
	state      string
	reason     string
	code       string
	started    time.Time
	roots      []string
	totals     Totals
	active     map[string]*activeEntry
	results    []ItemResult
	warnings   []string
	faulted    map[string]bool
	touched    map[string]bool
	rootCtx    map[string]context.Context
	rootCancel map[string]context.CancelCauseFunc
	rate       float64
	rateAt     time.Time
	rateBytes  int64
	finishedAt *time.Time
	ready      map[string]string
	outcome    map[string]string
}

// snapshot is the RunStatus now. Caller holds rs.mu.
func (rs *runState) snapshotLocked(now time.Time) RunStatus {
	st := RunStatus{
		RunID: rs.id, Trigger: rs.trigger, State: rs.state, Reason: rs.reason, Code: rs.code, StartedAt: rs.started,
		FinishedAt: rs.finishedAt, Roots: append([]string{}, rs.roots...), Totals: rs.totals, Active: []ActiveItem{},
		Warnings: append([]string(nil), rs.warnings...),
	}
	var activeBytes int64
	keys := make([]string, 0, len(rs.active))
	for k := range rs.active {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		a := rs.active[k]
		st.Active = append(st.Active, a.ActiveItem)
		activeBytes += min(a.BytesDone, max(a.planned, a.BytesTotal))
	}
	st.Totals.BytesDone += activeBytes
	if rs.finishedAt == nil {
		rate := int64(rs.rate)
		st.Totals.RateBPS = rate
		if remaining := st.Totals.BytesTotal - st.Totals.BytesDone; rate > 0 && remaining > 0 {
			st.Totals.ETAS = remaining / rate
		}
	} else {
		st.Totals.RateBPS, st.Totals.ETAS = 0, 0
		st.Items = append([]ItemResult(nil), rs.results...)
		st.Ready = rs.ready
		st.Result = rs.resultLocked()
	}
	return st
}

// resultLocked is ok, partial, failed, cancelled or skipped (cloud-app.md section 3.6).
func (rs *runState) resultLocked() string {
	switch rs.state {
	case StateSkipped:
		return ResultSkipped
	case StateCancelled:
		return ResultCancelled
	case StateFailed:
		return ResultFailed
	}
	left := 0
	for _, it := range rs.results {
		if it.Result == "skipped" && (it.Reason == string(plan.ReasonTimeLimit) || it.Reason == string(plan.ReasonBudget) || it.Reason == string(plan.ReasonDisk)) {
			left++
		}
	}
	if rs.totals.Failed == 0 && left == 0 {
		return ResultOK
	}
	return ResultPartial
}

// sampleLocked folds the bytes done so far into the moving rate (at most one sample a second).
func (rs *runState) sampleLocked(now time.Time) {
	var done int64 = rs.totals.BytesDone
	for _, a := range rs.active {
		done += min(a.BytesDone, max(a.planned, a.BytesTotal))
	}
	if rs.rateAt.IsZero() {
		rs.rateAt, rs.rateBytes = now, done
		return
	}
	dt := now.Sub(rs.rateAt)
	if dt < time.Second {
		return
	}
	inst := float64(done-rs.rateBytes) / dt.Seconds()
	if inst < 0 {
		inst = 0
	}
	if rs.rate == 0 {
		rs.rate = inst
	} else {
		rs.rate = 0.7*rs.rate + 0.3*inst
	}
	rs.rateAt, rs.rateBytes = now, done
}

// ---- starting a run --------------------------------------------------------------------------------

// Start begins a run and returns at once; the run goes on in the background and reports on the event
// bus. It is POST /ark/sync (real run), the cloud's ark.sync, the model download, the scheduled timer
// and the mode catch-up. Errors: *RunActiveError (LAI-SYN-005), *ModeError (LAI-SYN-010 and -011, for a
// manual run), NoRootsError (LAI-ARK-001), *UnknownRootError. A scheduled run that preflight skips is
// not an error: it is a run that ended at once in state skipped, and Started says so.
func (e *Engine) Start(ctx context.Context, req StartRequest) (Started, error) {
	if req.Trigger == "" {
		req.Trigger = TriggerManual
	}
	if !validTrigger(req.Trigger) {
		return Started{}, fmt.Errorf("run: %q is not a trigger", req.Trigger)
	}
	if err := ctx.Err(); err != nil {
		return Started{}, err
	}
	s := e.d.Settings()
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.active != nil {
		return Started{}, &RunActiveError{RunID: e.active.id}
	}
	onBattery := e.d.Power != nil && e.d.Power.OnBattery()
	v := preflight(req, s, e.d.Net(), onBattery, e.d.Planner.Roots())
	if v.Err != nil {
		return Started{}, v.Err
	}
	now := e.d.Clock.Now()
	if v.Skip {
		return e.skippedLocked(req, v, now)
	}

	// The cross-process slot: another agent syncing (a chroot, a second install) is the same answer.
	var lease Lease
	if e.d.Lock != nil {
		l, err := e.d.Lock.Acquire("starting")
		if err != nil {
			var busy *LockBusyError
			if errors.As(err, &busy) {
				return Started{}, &RunActiveError{RunID: busy.RunID}
			}
			return Started{}, err
		}
		lease = l
	}
	run, err := e.d.Journal.Runs.Start()
	if err != nil {
		if lease != nil {
			lease.Release()
		}
		return Started{}, fmt.Errorf("run: cannot start the run log: %w", err)
	}
	if lease != nil {
		lease.SetHolder(run.ID)
	}
	rctx, cancel := context.WithCancelCause(context.Background())
	rs := &runState{
		e: e, id: run.ID, trigger: req.Trigger, scheduled: isScheduled(req.Trigger), settings: s, opID: req.OpID,
		ctx: rctx, cancel: cancel, run: run, lease: lease, done: make(chan struct{}),
		state: StateRunning, started: now, roots: v.Roots, warnings: v.Warnings,
		active: map[string]*activeEntry{}, faulted: map[string]bool{}, touched: map[string]bool{}, outcome: map[string]string{},
		items: map[string]bool{},
	}
	for _, id := range req.Items {
		rs.items[id] = true
	}
	e.active = rs
	go e.execute(rs, v.Offline)
	e.d.Logger.Info("sync run started", "component", "sync", "run_id", rs.id, "trigger", rs.trigger, "roots", rs.roots)
	return Started{RunID: rs.id, State: StateRunning}, nil
}

// skippedLocked records a scheduled run that preflight ended. Caller holds e.mu.
func (e *Engine) skippedLocked(req StartRequest, v verdict, now time.Time) (Started, error) {
	id, err := journal.NewRunID(now, nil)
	if err != nil {
		return Started{}, err
	}
	fin := now
	st := RunStatus{
		RunID: id, Trigger: req.Trigger, State: StateSkipped, Reason: v.Reason, Code: v.Code, StartedAt: now, FinishedAt: &fin,
		Roots: []string{}, Active: []ActiveItem{}, Result: ResultSkipped,
	}
	e.last = &st
	e.persist(st)
	var mounted []string
	for _, r := range e.d.Planner.Roots() {
		mounted = append(mounted, r.ID)
	}
	e.noteRootRunsLocked(st, mounted)
	e.d.Logger.Info("sync run skipped", "component", "sync", "run_id", id, "trigger", req.Trigger, "reason", v.Reason, "code", v.Code)
	e.publish(events.TypeSyncDone, id, events.SyncDone{RunID: id, State: StateSkipped, Totals: st.MarshalTotals(), Ready: map[string]string{}})
	return Started{RunID: id, State: StateSkipped, Reason: v.Reason, Code: v.Code}, nil
}

func (e *Engine) publish(typ, opID string, data any) {
	if e.d.Events == nil {
		return
	}
	if err := e.d.Events.Publish(typ, opID, data); err != nil {
		e.d.Logger.Warn("event not published", "component", "sync", "type", typ, "err", err.Error())
	}
}

func (e *Engine) persist(st RunStatus) {
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return
	}
	if err := config.WriteFileAtomicFS(e.d.FS, e.lastPath(), append(data, '\n'), 0o644, -1, -1); err != nil && !errors.Is(err, config.ErrNotDurable) {
		e.d.Logger.Warn("last-run.json not written", "component", "sync", "err", err.Error())
	}
}

// ---- observing and cancelling -------------------------------------------------------------------

// Status returns the active run or the last one; ok is false when no run has happened yet.
func (e *Engine) Status() (RunStatus, bool) {
	e.mu.Lock()
	rs, last := e.active, e.last
	e.mu.Unlock()
	if rs != nil {
		rs.mu.Lock()
		defer rs.mu.Unlock()
		now := e.d.Clock.Now()
		rs.sampleLocked(now)
		return rs.snapshotLocked(now), true
	}
	if last != nil {
		return *last, true
	}
	return RunStatus{}, false
}

// RunActive reports whether a run is active (what the drill reads before it starts).
func (e *Engine) RunActive() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.active != nil
}

// Running is RunActive.
func (e *Engine) Running() bool { return e.RunActive() }

// Cancel stops the active run gracefully (DELETE /ark/sync): every transfer stops at the next 1 MiB
// boundary, checkpoints and exits, leaving its .part. It is idempotent. It returns the run it
// cancelled, or active false (and the last run's id) when none was running.
func (e *Engine) Cancel() (runID string, active bool) {
	return e.cancelWith(errCancelUser)
}

func (e *Engine) cancelWith(cause error) (string, bool) {
	e.mu.Lock()
	rs, last := e.active, e.last
	e.mu.Unlock()
	if rs == nil {
		if last != nil {
			return last.RunID, false
		}
		return "", false
	}
	rs.mu.Lock()
	if rs.state == StateRunning {
		rs.state = StateCancelling
	}
	rs.mu.Unlock()
	rs.cancel(cause)
	return rs.id, true
}

// RootGone tells the engine that a root went away during a run (a drive unplugged, or being ejected:
// drive.unmounted, SYNC-02): the transfers of that root are cancelled at the next block and checkpoint,
// the rest of its items are skipped (root_gone, LAI-ARK-007), and the other roots go on. It reports
// whether a run was active and working on that root. The next run resumes the interrupted items when
// the drive is back.
func (e *Engine) RootGone(rootID string) bool {
	e.mu.Lock()
	rs := e.active
	e.mu.Unlock()
	if rs == nil {
		return false
	}
	rs.mu.Lock()
	cancel := rs.rootCancel[rootID]
	rs.mu.Unlock()
	if cancel == nil {
		return false
	}
	cancel(errRootGone)
	return true
}

// CancelGraceful is the modes.SyncController call (modes.md section 2.1, step 3): cancel the active run
// because the node leaves online mode, and wait until it has stopped (or ctx ends). The run ends
// cancelled with LAI-SYN-002 and keeps its .part files.
func (e *Engine) CancelGraceful(ctx context.Context) error {
	e.mu.Lock()
	rs := e.active
	e.mu.Unlock()
	if rs == nil {
		return nil
	}
	e.cancelWith(errCancelMode)
	select {
	case <-rs.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Wait blocks until the run runID has ended (it returns at once when it is not the active run) or ctx
// ends. It is for `aictl ark sync --wait` and the tests.
func (e *Engine) Wait(ctx context.Context, runID string) error {
	e.mu.Lock()
	rs := e.active
	e.mu.Unlock()
	if rs == nil || rs.id != runID {
		return nil
	}
	select {
	case <-rs.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// ScheduleCatchup arranges one run (trigger mode_online) after in (modes.SyncController): the mode
// engine asks for it when the node comes back online and the last success is old. Preflight decides
// when it fires, so a node that went offline in between skips it. A second call replaces the first.
func (e *Engine) ScheduleCatchup(in time.Duration) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.catchup != nil {
		e.catchup()
	}
	e.catchup = e.d.Clock.AfterFunc(in, func() {
		if _, err := e.Start(context.Background(), StartRequest{Trigger: TriggerModeOnline}); err != nil {
			var active *RunActiveError
			if !errors.As(err, &active) {
				e.d.Logger.Info("catch-up sync not started", "component", "sync", "err", err.Error())
			}
		}
	})
}

// ---- recovery -------------------------------------------------------------------------------------

// Resume finishes the swaps a crashed run left in flight (call it once at start, after journal.Open and
// the best-copy index are built, before anything serves content): every intent of the replayed run that
// has a swap plan is handed to the machine's Recover. The other states of a crashed item
// (downloading, verifying, staged) need nothing now: the next run resumes them from their records.
// It returns the results of the swaps it finished.
func (e *Engine) Resume(ctx context.Context, rec journal.Recovery) []item.Result {
	var keys []string
	for k, in := range rec.Intents {
		if in.Swap != nil {
			keys = append(keys, k)
		}
	}
	if len(keys) == 0 {
		return nil
	}
	sort.Strings(keys)
	run, err := e.d.Journal.Runs.Start()
	if err != nil {
		e.d.Logger.Error("cannot start a log for the recovery", "component", "sync", "err", err.Error())
		return nil
	}
	defer run.Close()
	var out []item.Result
	touched := map[string]bool{}
	for _, k := range keys {
		res, ok := e.d.Machine.Recover(ctx, run, rec.Intents[k])
		if !ok {
			continue
		}
		out = append(out, res)
		touched[res.Root] = true
		e.d.Logger.Info("swap recovered", "component", "sync", "root", res.Root, "item", res.Item, "version", res.Version, "state", string(res.State), "code", res.Code)
		e.publish(events.TypeSyncItemDone, run.ID, events.SyncItemDone{
			RunID: run.ID, Root: res.Root, Item: res.Item, Result: resultName(res), Version: res.Version, Code: res.Code,
		})
	}
	if e.d.Ready != nil && len(touched) > 0 {
		ids := make([]string, 0, len(touched))
		for id := range touched {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		_, _ = e.d.Ready.Refresh(ctx, ids)
	}
	return out
}

func resultName(r item.Result) string {
	switch r.State {
	case journal.StateVerified:
		return "verified"
	case journal.StateQuarantined:
		return "quarantined"
	case journal.StateSkipped:
		return "skipped"
	}
	return "failed"
}

// ---- executing a run -------------------------------------------------------------------------------

type task struct {
	root           ark.Root
	row            plan.ItemPlan
	cat            plan.CatalogItem
	ref            ark.ItemRef
	rel            string
	minFree        int64
	catalogVersion string
}

func (t *task) key() string { return t.root.ID + "/" + t.row.ID }

func (e *Engine) safe(name string, fn func()) {
	defer func() {
		if r := recover(); r != nil {
			e.d.Logger.Error("a run hook panicked", "component", "sync", "hook", name, "panic", fmt.Sprint(r))
		}
	}()
	fn()
}

// execute is the body of a run: refresh the catalog, plan, run the scheduler, then the end-of-run
// passes. It always ends by finish.
func (e *Engine) execute(rs *runState, offline bool) {
	var stopLimit func() bool
	if rs.scheduled && rs.settings.MaxRun() > 0 {
		stopLimit = e.d.Clock.AfterFunc(rs.settings.MaxRun(), func() { rs.cancel(errTimeLimit) })
	}
	defer func() {
		if stopLimit != nil {
			stopLimit()
		}
		e.finish(rs)
	}()
	ctx := rs.ctx

	if !offline && e.d.Catalog != nil && usesNetwork(rs.trigger) {
		if err := e.d.Catalog.Refresh(ctx); err != nil && ctx.Err() == nil {
			// Not fatal: the run goes on with the catalog it has (ark-sync.md section 4.3).
			e.d.Logger.Warn("catalog refresh failed, going on with the current catalog", "component", "sync", "run_id", rs.id, "err", err.Error())
			rs.mu.Lock()
			rs.warnings = append(rs.warnings, "the catalog could not be refreshed; the current one is used")
			rs.mu.Unlock()
		}
	}
	if ctx.Err() != nil {
		return
	}

	plans, err := e.d.Planner.Plan(ctx, PlanRequest{RootIDs: rs.roots, Offline: offline})
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		code := doctor.AGT099
		var uerr interface{ Code() string }
		if errors.As(err, &uerr) {
			code = uerr.Code()
		}
		e.d.Logger.Error("the plan failed", "component", "sync", "run_id", rs.id, "err", err.Error(), "code", code)
		rs.mu.Lock()
		rs.state, rs.code = StateFailed, code
		rs.warnings = append(rs.warnings, "the plan could not be made")
		rs.mu.Unlock()
		return
	}
	tasks := e.tasks(rs, plans)
	e.schedule(rs, tasks)
}

// tasks turns the plans into the tasks of the run, interleaved round-robin between roots, and records
// the rows the planner skipped.
func (e *Engine) tasks(rs *runState, plans Plans) []*task {
	mounted := map[string]ark.Root{}
	for _, r := range e.d.Planner.Roots() {
		mounted[r.ID] = r
	}
	var perRoot [][]*task
	current := map[string][]string{}
	defer func() {
		now := e.d.Clock.Now()
		for rootID, ids := range current {
			e.markCurrent(rootID, ids, now)
		}
	}()
	for _, rp := range plans.Plans {
		root, ok := mounted[rp.Root]
		if !ok {
			continue
		}
		var list []*task
		for _, row := range rp.Items {
			if len(rs.items) > 0 && !rs.items[row.ID] {
				continue
			}
			switch row.Action {
			case plan.ActionDownload, plan.ActionUpdate:
				cat, ok := plans.Item(row.ID)
				if !ok {
					rs.recordSkip(root, row.ID, row.ToVersion, string(plan.ReasonUnresolved), "", true)
					continue
				}
				ref, err := e.d.ItemRef(cat)
				if err == nil {
					_, err = ark.ItemDir(ref)
				}
				if err != nil {
					e.d.Logger.Warn("item cannot be placed", "component", "sync", "run_id", rs.id, "item", row.ID, "err", err.Error())
					rs.recordSkip(root, row.ID, row.ToVersion, string(plan.ReasonUnresolved), "", true)
					continue
				}
				rel, _ := ark.ItemDir(ref)
				list = append(list, &task{root: root, row: row, cat: cat, ref: ref, rel: rel, minFree: plans.MinFree[rp.Root], catalogVersion: plans.CatalogVersion})
			case plan.ActionSkip:
				rs.recordSkip(root, row.ID, row.ToVersion, string(row.Reason), skipCode(row.Reason), true)
			case plan.ActionKeep:
				// The plan resolved the version and found it present: the item is as current as this run.
				if row.ToVersion != "" {
					current[root.ID] = append(current[root.ID], row.ID)
				}
			}
		}
		if len(list) > 0 {
			perRoot = append(perRoot, list)
		}
	}
	out := interleave(perRoot)
	rs.mu.Lock()
	for _, t := range out {
		rs.totals.Items++
		rs.totals.BytesTotal += t.row.NeedBytes
		rs.touched[t.root.ID] = true
	}
	rs.mu.Unlock()
	return out
}

// interleave merges the per-root task lists round-robin: the first task of every root, then the second
// of every root, and so on, so that a big tier-1 item of a drive does not starve the internal disk's
// (ark-sync.md section 4.7). Within a root the planner's priority order is kept.
func interleave(perRoot [][]*task) []*task {
	var out []*task
	for i := 0; ; i++ {
		more := false
		for _, l := range perRoot {
			if i < len(l) {
				out = append(out, l[i])
				more = true
			}
		}
		if !more {
			return out
		}
	}
}

// skipCode is the doctor code of a planner skip reason.
func skipCode(r plan.Reason) string {
	switch r {
	case plan.ReasonBudget:
		return doctor.SYN041
	case plan.ReasonDisk:
		return doctor.SYN040
	case plan.ReasonReadOnly:
		return doctor.ARK003
	}
	return ""
}

// recordSkip records an item the run did not do. countItem is true for a row of the plan (it is one of
// the run's items); a task that was already counted and is skipped later (its first copy failed, its
// root faulted, the time ran out) is not counted twice.
func (rs *runState) recordSkip(root ark.Root, itemID, version, reason, code string, countItem bool) {
	rs.mu.Lock()
	if countItem {
		rs.totals.Items++
	}
	rs.totals.Skipped++
	rs.results = append(rs.results, ItemResult{Root: root.ID, Item: itemID, Result: "skipped", Version: version, Reason: reason, Code: code})
	rs.mu.Unlock()
	rs.e.publish(events.TypeSyncItemDone, rs.id, events.SyncItemDone{RunID: rs.id, Root: root.ID, Item: itemID, Result: "skipped", Version: version, Reason: reason, Code: code})
}

// ---- the scheduler ----------------------------------------------------------------------------------

// gate holds the network slots and the one-at-a-time resources of a run (ark-sync.md section 4.7).
type gate struct {
	net chan struct{}
	mu  sync.Mutex
	ex  map[string]chan struct{}
}

func newGate(concurrency int) *gate {
	return &gate{net: make(chan struct{}, max(concurrency, 1)), ex: map[string]chan struct{}{}}
}

func acquire(ctx context.Context, ch chan struct{}) (func(), error) {
	select {
	case ch <- struct{}{}:
		var once sync.Once
		return func() { once.Do(func() { <-ch }) }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Network implements item.Gate.
func (g *gate) Network(ctx context.Context) (func(), error) { return acquire(ctx, g.net) }

// Exclusive implements item.Gate.
func (g *gate) Exclusive(ctx context.Context, key string) (func(), error) {
	g.mu.Lock()
	ch, ok := g.ex[key]
	if !ok {
		ch = make(chan struct{}, 1)
		g.ex[key] = ch
	}
	g.mu.Unlock()
	return acquire(ctx, ch)
}

type taskDone struct {
	t   *task
	res item.Result
}

// schedule runs the tasks: at most per_root_writers per root, a local copy only after the first copy
// of its item is verified, tasks started in priority order interleaved between roots. Network slots
// are the gate's. It returns when every task has ended or the run was cancelled and its workers
// stopped; tasks that never started are skipped with time_limit when the limit ended the run.
func (e *Engine) schedule(rs *runState, tasks []*task) {
	// One context per root, children of the run's: a drive that goes away cancels only its own work.
	rs.mu.Lock()
	rs.rootCtx, rs.rootCancel = map[string]context.Context{}, map[string]context.CancelCauseFunc{}
	for _, t := range tasks {
		if _, ok := rs.rootCtx[t.root.ID]; !ok {
			c, cancel := context.WithCancelCause(rs.ctx)
			rs.rootCtx[t.root.ID], rs.rootCancel[t.root.ID] = c, cancel
		}
	}
	rs.mu.Unlock()
	defer func() {
		rs.mu.Lock()
		for _, cancel := range rs.rootCancel {
			cancel(nil)
		}
		rs.mu.Unlock()
	}()
	g := newGate(rs.settings.Concurrency)
	perRoot := max(rs.settings.PerRootWriters, 1)
	started := make([]bool, len(tasks))
	finished := make([]bool, len(tasks))
	running := map[string]int{}
	results := make(chan taskDone, len(tasks))
	inflight := 0

	start := func(i int) {
		t := tasks[i]
		started[i] = true
		running[t.root.ID]++
		inflight++
		go func() {
			if err := e.d.Priority(rs.scheduled); err != nil {
				e.d.Logger.Debug("worker priority not set", "component", "sync", "err", err.Error())
			}
			results <- taskDone{t: t, res: e.runTask(rs, t, g)}
		}()
	}
	settle := func(i int, res item.Result) {
		finished[i] = true
		rs.record(tasks[i], res)
	}
	index := map[*task]int{}
	for i, t := range tasks {
		index[t] = i
	}

	for {
		progressed := false
		for i, t := range tasks {
			if started[i] || finished[i] || rs.ctx.Err() != nil {
				continue
			}
			if running[t.root.ID] >= perRoot {
				continue
			}
			rs.mu.Lock()
			faulted := rs.faulted[t.root.ID]
			gone := errors.Is(context.Cause(rs.rootCtx[t.root.ID]), errRootGone)
			dep, depOK := rs.outcome[t.row.After+"/"+t.row.ID]
			rs.mu.Unlock()
			if gone {
				finished[i] = true
				rs.recordSkip(t.root, t.row.ID, t.row.ToVersion, SkipRootGone, doctor.ARK007, false)
				rs.mu.Lock()
				rs.outcome[t.key()] = "skipped"
				rs.mu.Unlock()
				progressed = true
				continue
			}
			if faulted {
				finished[i] = true
				rs.recordSkip(t.root, t.row.ID, t.row.ToVersion, string(plan.ReasonReadOnly), doctor.ARK003, false)
				rs.mu.Lock()
				rs.outcome[t.key()] = "skipped"
				rs.mu.Unlock()
				progressed = true
				continue
			}
			if t.row.After != "" {
				if !depOK {
					continue
				}
				if dep != "verified" {
					// The first copy never arrived, so there is nothing to copy locally.
					finished[i] = true
					rs.recordSkip(t.root, t.row.ID, t.row.ToVersion, string(plan.ReasonUnresolved), "", false)
					rs.mu.Lock()
					rs.outcome[t.key()] = "skipped"
					rs.mu.Unlock()
					progressed = true
					continue
				}
			}
			start(i)
			progressed = true
		}
		if inflight == 0 {
			if !progressed {
				break
			}
			continue
		}
		d := <-results
		inflight--
		running[d.t.root.ID]--
		settle(index[d.t], d.res)
	}

	// Whatever never started: skipped for time when the limit ended the run; left as it is for a cancel.
	if errors.Is(context.Cause(rs.ctx), errTimeLimit) {
		for i, t := range tasks {
			if !started[i] && !finished[i] {
				rs.recordSkip(t.root, t.row.ID, t.row.ToVersion, string(plan.ReasonTimeLimit), "", false)
			}
		}
	}
}

// runTask builds the job of a task and runs it through the machine.
func (e *Engine) runTask(rs *runState, t *task, g *gate) item.Result {
	key := t.key()
	rec, _ := e.d.Journal.Items.Get(t.root.ID, t.row.ID)
	rs.mu.Lock()
	rs.active[key] = &activeEntry{
		ActiveItem: ActiveItem{Root: t.root.ID, Item: t.row.ID, State: string(journal.StatePlanned), Version: t.row.ToVersion, BytesTotal: t.row.NeedBytes, Attempt: rec.Attempts + 1},
		planned:    t.row.NeedBytes,
	}
	rs.mu.Unlock()

	job := item.Job{
		Root: t.root, Ref: t.ref, RelDir: t.rel, CatalogVersion: t.catalogVersion, MinFree: t.minFree, Gate: g,
		Progress: func(p item.Progress) { rs.progress(t, p) },
		Resolve:  func(ctx context.Context) (item.Resolution, error) { return e.resolve(ctx, t) },
	}
	if cur, ok := e.d.Journal.Items.Get(t.root.ID, t.row.ID); ok && cur.State == journal.StateSwapping {
		job.Swap = e.swapPlanOf(t.root.ID, t.row.ID, cur.Version)
	}
	rs.mu.Lock()
	ctx := rs.rootCtx[t.root.ID]
	rs.mu.Unlock()
	return e.d.Machine.Run(ctx, rs.run, job)
}

// swapPlanOf finds the plan of the swap of version an earlier run left unfinished, in the newest run log
// that has one: an item whose drive was not mounted when the agent restarted is finished when the drive
// is back, from the plan in the log of the run that crashed.
func (e *Engine) swapPlanOf(rootID, itemID, version string) *journal.SwapPlan {
	ids, err := e.d.Journal.Runs.List()
	if err != nil {
		return nil
	}
	for i := len(ids) - 1; i >= 0; i-- {
		log, err := e.d.Journal.Runs.Read(ids[i])
		if err != nil {
			continue
		}
		if in, ok := journal.Replay(log.Events)[journal.Key(rootID, itemID)]; ok && in.Swap != nil && in.Version == version {
			return in.Swap
		}
	}
	return nil
}

// resolve is the resolving step of a task: a local copy is built from the other root's manifest, any
// other source goes to its registered resolver, and a source type with none to what the catalog pins.
func (e *Engine) resolve(ctx context.Context, t *task) (item.Resolution, error) {
	if t.row.Source == plan.SourceLocalCopy && t.row.FromRoot != "" {
		return LocalCopy(e.d.FS, e.d.Copies, t.row)
	}
	if r, ok := e.d.Resolvers.For(t.cat.SourceType()); ok {
		return r.Resolve(ctx, t.cat, t.row)
	}
	return Pinned(t.cat, t.row, t.catalogVersion)
}

// progress folds a progress report of the machine into the run and publishes sync.progress, at most
// once a second per item (and at once when the item's state changes).
func (rs *runState) progress(t *task, p item.Progress) {
	now := rs.e.d.Clock.Now()
	rs.mu.Lock()
	a, ok := rs.active[t.key()]
	if !ok {
		rs.mu.Unlock()
		return
	}
	a.State = string(p.State)
	a.BytesDone = p.Done
	if p.Total > 0 {
		a.BytesTotal = p.Total
	}
	if a.lastAt.IsZero() {
		a.lastAt, a.lastBytes = now, p.Done
	} else if dt := now.Sub(a.lastAt); dt >= time.Second {
		a.RateBPS = int64(float64(p.Done-a.lastBytes) / dt.Seconds())
		a.lastAt, a.lastBytes = now, p.Done
	}
	rs.sampleLocked(now)
	emit := a.lastState != a.State || a.lastEmit.IsZero() || now.Sub(a.lastEmit) >= time.Second
	var ev events.SyncProgress
	if emit {
		a.lastEmit, a.lastState = now, a.State
		ev = events.SyncProgress{RunID: rs.id, Root: a.Root, Item: a.Item, State: a.State, BytesDone: a.BytesDone, BytesTotal: a.BytesTotal, RateBPS: a.RateBPS}
	}
	rs.mu.Unlock()
	if emit {
		rs.e.publish(events.TypeSyncProgress, rs.id, ev)
	}
}

// record settles a finished task: counters, the event, the journal's failure counter, and the fault of
// a root.
func (rs *runState) record(t *task, res item.Result) {
	e := rs.e
	rs.mu.Lock()
	delete(rs.active, t.key())
	if res.Cancelled {
		gone := errors.Is(context.Cause(rs.rootCtx[t.root.ID]), errRootGone)
		rs.mu.Unlock()
		if gone {
			// the drive went away under it: its .part and checkpoints wait for the drive to come back
			rs.recordSkip(t.root, t.row.ID, t.row.ToVersion, SkipRootGone, doctor.ARK007, false)
			rs.mu.Lock()
			rs.outcome[t.key()] = "skipped"
			rs.mu.Unlock()
			return
		}
		if errors.Is(context.Cause(rs.ctx), errTimeLimit) {
			// the time ran out under it: its .part and checkpoints wait for the next run
			rs.recordSkip(t.root, t.row.ID, t.row.ToVersion, string(plan.ReasonTimeLimit), "", false)
		}
		return
	}
	result := resultName(res)
	code, reason := res.Code, res.Reason
	switch res.State {
	case journal.StateVerified:
		rs.totals.Done++
		rs.totals.BytesDone += t.row.NeedBytes
		rs.outcome[t.key()] = "verified"
		defer e.markCurrent(t.root.ID, []string{t.row.ID}, e.d.Clock.Now())
	case journal.StateSkipped:
		rs.totals.Skipped++
		rs.outcome[t.key()] = "skipped"
	default:
		rs.totals.Failed++
		rs.outcome[t.key()] = "failed"
	}
	if res.RootFault != nil {
		rs.faulted[t.root.ID] = true
	}
	rs.results = append(rs.results, ItemResult{Root: t.root.ID, Item: t.row.ID, Result: result, Version: res.Version, Reason: reason, Code: code})
	rs.mu.Unlock()

	if res.State == journal.StateFailed || res.State == journal.StateQuarantined {
		if rec, err := e.d.Journal.Items.Update(t.root.ID, t.row.ID, func(i *journal.Item) { i.ConsecutiveFailedRuns++ }); err == nil && rec.ConsecutiveFailedRuns >= RepeatFailureRuns {
			code = CodeRepeatFailed
			e.d.Logger.Warn("the same item failed in several runs in a row", "component", "sync", "root", t.root.ID, "item", t.row.ID, "runs", rec.ConsecutiveFailedRuns, "code", code)
			rs.mu.Lock()
			rs.results[len(rs.results)-1].Code = code
			rs.mu.Unlock()
		}
	}
	if res.RootFault != nil && e.d.RootFault != nil {
		e.safe("root_fault", func() { e.d.RootFault(t.root.ID, res.RootFault) })
	}
	e.d.Logger.Info("item done", "component", "sync", "run_id", rs.id, "root", t.root.ID, "item", t.row.ID, "version", res.Version, "result", result, "code", code, "reason", reason)
	e.publish(events.TypeSyncItemDone, rs.id, events.SyncItemDone{RunID: rs.id, Root: t.root.ID, Item: t.row.ID, Result: result, Version: res.Version, Reason: reason, Code: code})
	if res.State == journal.StateVerified && e.d.Ready != nil {
		e.safe("ready", func() { _, _ = e.d.Ready.Refresh(rs.ctxForPasses(), []string{t.root.ID}) })
	}
}

// ctxForPasses is a context the end-of-run passes use: the run's own context is cancelled by a
// cancel, but READY.json must still describe what a cancelled run did.
func (rs *runState) ctxForPasses() context.Context { return context.WithoutCancel(rs.ctx) }

// ---- the end of a run --------------------------------------------------------------------------------

// finish runs the end-of-run passes and settles the run: retention, the re-verification hook, READY,
// the extras hook, the final state, the report hook, sync.done, and the release of the run slot.
func (e *Engine) finish(rs *runState) {
	pctx := rs.ctxForPasses()
	cause := context.Cause(rs.ctx)
	cancelled := cause != nil && !errors.Is(cause, errTimeLimit)

	rs.mu.Lock()
	touched := make([]string, 0, len(rs.touched))
	for _, id := range rs.roots { // the run's root order: internal first
		if rs.touched[id] {
			touched = append(touched, id)
		}
	}
	rs.mu.Unlock()
	rc := RunContext{RunID: rs.id, Trigger: rs.trigger, Roots: rs.roots, Run: rs.run}

	// Retention: expired trash and quarantine, and staging nothing explains (R3, R5, section 4.5.2).
	if e.d.Retention != nil && usesNetwork(rs.trigger) {
		for _, id := range rs.roots {
			e.safe("retention", func() {
				if _, err := e.d.Retention.PurgeExpired(pctx, id, rs.settings.TrashDays, rs.settings.QuarantineDays); err != nil {
					e.d.Logger.Warn("retention pass failed", "component", "sync", "root", id, "err", err.Error())
				}
				if e.d.Unknown != nil {
					if _, err := e.d.Retention.PurgeUnknownStaging(e.d.Unknown, id); err != nil {
						e.d.Logger.Warn("unknown staging not removed", "component", "sync", "root", id, "err", err.Error())
					}
				}
			})
		}
	}
	if !cancelled && e.d.Hooks.Reverify != nil && rs.settings.ReverifyBudgetMinutes > 0 && usesNetwork(rs.trigger) {
		e.safe("reverify", func() {
			e.d.Hooks.Reverify(pctx, rc, time.Duration(rs.settings.ReverifyBudgetMinutes)*time.Minute)
		})
	}
	ready := map[string]string{}
	if e.d.Ready != nil && len(touched) > 0 {
		e.safe("ready", func() {
			if overall, err := e.d.Ready.Refresh(pctx, touched); err == nil {
				ready = overall
			} else {
				e.d.Logger.Warn("READY.json not refreshed", "component", "sync", "err", err.Error())
			}
		})
	}
	if e.d.Hooks.Extras != nil && !cancelled {
		e.safe("extras", func() { e.d.Hooks.Extras(pctx, rc) })
	}

	now := e.d.Clock.Now()
	rs.mu.Lock()
	switch {
	case rs.state == StateFailed:
	case errors.Is(cause, errCancelMode):
		rs.state, rs.reason, rs.code = StateCancelled, SkipMode, CodeModeSkip
	case cancelled:
		rs.state, rs.reason = StateCancelled, "cancelled"
	default:
		rs.state = StateDone
		if errors.Is(cause, errTimeLimit) {
			rs.reason = string(plan.ReasonTimeLimit)
		}
	}
	rs.finishedAt = &now
	rs.ready = ready
	rs.active = map[string]*activeEntry{}
	rs.sampleLocked(now)
	st := rs.snapshotLocked(now)
	rs.mu.Unlock()

	e.persist(st)
	if e.d.Hooks.Report != nil {
		e.safe("report", func() { e.d.Hooks.Report(pctx, rc, st) })
	}
	e.publish(events.TypeSyncDone, rs.id, events.SyncDone{RunID: rs.id, State: st.State, Totals: st.MarshalTotals(), Ready: ready})
	e.d.Logger.Info("sync run ended", "component", "sync", "run_id", rs.id, "state", st.State, "result", st.Result,
		"items", st.Totals.Items, "done", st.Totals.Done, "skipped", st.Totals.Skipped, "failed", st.Totals.Failed)

	_ = rs.run.Close()
	rs.cancel(nil)
	// The slot is released last: a new run may start the instant it is free.
	e.mu.Lock()
	e.active = nil
	e.last = &st
	e.noteRootRunsLocked(st, rs.roots)
	e.mu.Unlock()
	if rs.lease != nil {
		rs.lease.Release()
	}
	close(rs.done)
}
