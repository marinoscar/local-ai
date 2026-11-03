// Package run is the ark sync run engine (docs/specs/ark-sync.md sections 4.3, 4.7 and 3.5): the one
// run at a time of a node, from the trigger to the report.
//
//	trigger -> preflight -> catalog refresh -> plan (per root) -> execute -> retention -> READY -> report
//
// The engine owns the run slot (an in-memory guard and a flock on /var/lib/localai/sync/lock, so two
// agents cannot sync at once: LAI-SYN-005), the preflight table (mode, battery, connectivity, roots,
// scheduled against manual), the scheduler (sync.concurrency network slots, sync.per_root_writers per
// root, round-robin between roots, a local copy only after the first copy is verified, one exclusive
// resource such as `pmtiles extract` at a time), the low priority of its workers, the max_run_hours
// stop, cancellation that leaves .part files and checkpoints for the next run, the sync.* events, and
// the end-of-run passes (retention, READY.json, hooks for re-verification, drive extras and the cloud
// report). The per-item work is internal/sync/item's.
//
// Nothing here touches the network or the disk of a root directly: the planner (internal/sync/plan),
// the machine (internal/sync/item), retention, the downloader and the READY writer are injected, each
// behind a small interface, so the whole engine is tested with fakes and no sleeps. The engine is also
// what the rest of the agent talks to, through small adapters in adapters.go: the /ark/sync routes
// (api.ArkSyncStarter and api.ArkSyncController), the model routes (modelsapi.SyncEngine), the cloud
// job handlers (jobs.Ark), the mode engine (modes.SyncController), the drill (ArkActivity) and the
// cloud snapshot (cloud.SyncSource). Wiring them is the daemon's job (internal/app, AGENT-20).
package run

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/ark"
	"github.com/marinoscar/local-ai/agent/internal/audit"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/sync/download"
	"github.com/marinoscar/local-ai/agent/internal/sync/item"
	"github.com/marinoscar/local-ai/agent/internal/sync/journal"
	"github.com/marinoscar/local-ai/agent/internal/sync/plan"
	"github.com/marinoscar/local-ai/agent/internal/sync/retention"
)

// Run states of GET /ark/sync (ark-sync.md section 3.5).
const (
	StateRunning    = "running"
	StateCancelling = "cancelling"
	StateDone       = "done"
	StateCancelled  = "cancelled"
	StateFailed     = "failed"
	StateSkipped    = "skipped"
)

// Skip reasons of a run that preflight ended (the reason of state skipped).
const (
	SkipOffline  = "offline"
	SkipMode     = "mode"
	SkipBattery  = "battery"
	SkipDisabled = "disabled"
	SkipNoRoots  = "no_roots"
)

// Results of a finished run, for the report (cloud-app.md section 3.6): ok, partial (some item failed
// or was left), failed, cancelled or skipped.
const (
	ResultOK        = "ok"
	ResultPartial   = "partial"
	ResultFailed    = "failed"
	ResultCancelled = "cancelled"
	ResultSkipped   = "skipped"
)

// Triggers of a run (ark-sync.md section 3.5). They are the api package's constants, repeated here so
// the engine does not import the HTTP layer for its own vocabulary.
const (
	TriggerManual     = "manual"
	TriggerScheduled  = "scheduled"
	TriggerCloud      = "cloud"
	TriggerModeOnline = "mode_online"
	TriggerInstall    = "install"
	TriggerVerify     = "verify"
	TriggerImport     = "import"
)

// The doctor codes of the engine. They alias the doctor table.
const (
	CodeNoInternet   = doctor.SYN001
	CodeModeSkip     = doctor.SYN002
	CodeOnBattery    = doctor.SYN003
	CodeAlreadyRuns  = doctor.SYN005
	CodeSurvival     = doctor.SYN010
	CodeOfflineMode  = doctor.SYN011
	CodeRepeatFailed = doctor.SYN035
	CodeBudgetFull   = doctor.SYN041
	CodeNoRoot       = doctor.ARK001
	CodeReadOnly     = doctor.ARK003
	CodeCatalogBad   = doctor.CAT002
)

// RepeatFailureRuns is how many runs in a row an item may fail before LAI-SYN-035.
const RepeatFailureRuns = 3

// NetState is the node's network picture: the mode ("online", "offline", "survival") and whether the
// internet is reachable (connectivity is not "none", modes.md section 4).
type NetState struct {
	Mode   string
	Online bool
}

// Clock is the time seam of the engine (the run's rates, the max_run_hours stop, the catch-up timer).
type Clock interface {
	Now() time.Time
	// AfterFunc calls f once d has passed; stop cancels it and reports whether it was still pending.
	AfterFunc(d time.Duration, f func()) (stop func() bool)
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }
func (realClock) AfterFunc(d time.Duration, f func()) func() bool {
	t := time.AfterFunc(d, f)
	return t.Stop
}

// RealClock is the wall clock.
func RealClock() Clock { return realClock{} }

// Publisher is the event bus: *events.Bus implements it.
type Publisher interface {
	Publish(typ, opID string, data any) error
}

// Power says whether the node is on battery (a Mains supply that is offline and a battery that
// exists, ark-sync.md section 4.3); a desktop without a battery is never on battery.
type Power interface {
	OnBattery() bool
}

// PowerFunc adapts a function to Power.
type PowerFunc func() bool

// OnBattery implements Power.
func (f PowerFunc) OnBattery() bool { return f() }

// CatalogRefresher refreshes the catalog before the plan (ark-sync.md section 4.3). A failure is not
// fatal: the run goes on with the catalog it has and logs the code. internal/catalog's fetcher behind
// statusapi implements it.
type CatalogRefresher interface {
	Refresh(ctx context.Context) error
}

// ReadyRefresher regenerates READY.json of roots (SYNC-12, internal/ark/ready) and returns each root's
// overall colour. The engine calls it after every swap (for the root of the item) and at the end of a
// run (for every root the run touched); it must be cheap and safe to call concurrently.
type ReadyRefresher interface {
	Refresh(ctx context.Context, rootIDs []string) (overall map[string]string, err error)
}

// RunContext is what a run hook is told about the run.
type RunContext struct {
	RunID   string
	Trigger string
	Roots   []string
	// Run is the run's journal, for a hook that records events.
	Run *journal.Run
}

// Hooks are the run-lifecycle seams of later issues. All are optional, and none may panic the run: a
// panic in a hook is recovered and logged.
type Hooks struct {
	// Reverify spends up to budget re-hashing the items with the oldest last_reverified_at (SYNC-13).
	Reverify func(ctx context.Context, rc RunContext, budget time.Duration)
	// Extras writes the drive extras (START-HERE, the inventory copy; SYNC-14).
	Extras func(ctx context.Context, rc RunContext)
	// Report sends the run to the cloud (SYNC-14).
	Report func(ctx context.Context, rc RunContext, st RunStatus)
}

// ItemMachine is the per-item state machine: *item.Machine implements it.
type ItemMachine interface {
	Run(ctx context.Context, run *journal.Run, job item.Job) item.Result
	Recover(ctx context.Context, run *journal.Run, in journal.Intent) (item.Result, bool)
}

// Plans is what Planner.Plan returns: the plan of every requested root, made together so a download
// shared by two roots is sequenced (plan.PlanAll), and the catalog they were made from.
type Plans struct {
	Plans []plan.RootPlan
	// Item resolves a catalog item by id.
	Item func(id string) (plan.CatalogItem, bool)
	// CatalogVersion is the version of the catalog the plans were made from.
	CatalogVersion string
	// MinFree is min_free_bytes per root id (the profile's for the internal root, 2 % of the
	// filesystem, at least 5 GB, for a drive).
	MinFree map[string]int64
}

// PlanRequest asks for plans.
type PlanRequest struct {
	RootIDs []string
	// Offline: the network may not be used (the planner then plans from what the catalog pins).
	Offline bool
}

// Planner gathers and plans. *plan.Gatherer behind GathererPlanner implements it.
type Planner interface {
	// Roots returns the mounted content roots, internal first.
	Roots() []ark.Root
	Plan(ctx context.Context, req PlanRequest) (Plans, error)
}

// RootFaultFunc is told when a root turned out unwritable during a run (EIO, EROFS): the wiring re-runs
// the drive checks. The engine has already stopped planning writes to that root for the rest of the run.
type RootFaultFunc func(rootID string, err *download.Error)

// Retainer is the retention the engine runs at the end of a run (internal/sync/retention).
type Retainer interface {
	PurgeExpired(ctx context.Context, rootID string, trashDays, quarantineDays int) (retention.Expired, error)
	PurgeUnknownStaging(unknown func(root ark.Root) ([]string, error), rootID string) (int, error)
}

// StartRequest is a request to start a run, as the adapters build it from the routes.
type StartRequest struct {
	// Roots are root ids or paths; empty means every writable mounted root.
	Roots []string
	// Items are catalog ids; empty means the whole plan.
	Items            []string
	AllowOfflineMode bool
	// Trigger is one of the Trigger constants; manual when empty.
	Trigger string
	OpID    string
	Actor   audit.Actor
}

// Started is the answer of Start.
type Started struct {
	RunID string
	// State is running, or skipped for a scheduled run that preflight ended; Reason says why then.
	State  string
	Reason string
	Code   string
}

// Errors of Start.
type (
	// RunActiveError: a run is already active (LAI-SYN-005); RunID is the active run when known.
	RunActiveError struct{ RunID string }
	// ModeError: a manual run refused by the mode (LAI-SYN-010 in survival, LAI-SYN-011 in offline mode).
	ModeError struct {
		Code string
		Mode string
	}
	// NoRootsError: no root can be written (LAI-ARK-001).
	NoRootsError struct{}
	// UnknownRootError: a named root is not a mounted content root.
	UnknownRootError struct{ Root string }
)

func (e *RunActiveError) Error() string { return "run: a sync run is already active: " + e.RunID }
func (e *ModeError) Error() string {
	return fmt.Sprintf("%s: sync is refused in %s mode", e.Code, e.Mode)
}
func (NoRootsError) Error() string { return "run: there is no writable content root" }
func (e *UnknownRootError) Error() string {
	return "run: not a mounted content root: " + e.Root
}

// Totals are the counters of GET /ark/sync (ark-sync.md section 3.5).
type Totals struct {
	Items      int   `json:"items"`
	Done       int   `json:"done"`
	Skipped    int   `json:"skipped"`
	Failed     int   `json:"failed"`
	BytesTotal int64 `json:"bytes_total"`
	BytesDone  int64 `json:"bytes_done"`
	RateBPS    int64 `json:"rate_bps"`
	ETAS       int64 `json:"eta_s"`
}

// ActiveItem is one item in flight, as GET /ark/sync lists it.
type ActiveItem struct {
	Root       string `json:"root"`
	Item       string `json:"item"`
	State      string `json:"state"`
	Version    string `json:"version,omitempty"`
	BytesDone  int64  `json:"bytes_done"`
	BytesTotal int64  `json:"bytes_total"`
	RateBPS    int64  `json:"rate_bps"`
	Attempt    int    `json:"attempt"`
}

// ItemResult is what became of one item in a run.
type ItemResult struct {
	Root    string `json:"root"`
	Item    string `json:"item"`
	Result  string `json:"result"`
	Version string `json:"version,omitempty"`
	Reason  string `json:"reason,omitempty"`
	Code    string `json:"code,omitempty"`
}

// RunStatus is the body of GET /ark/sync: the active run or the last one.
type RunStatus struct {
	RunID      string       `json:"run_id"`
	Trigger    string       `json:"trigger"`
	State      string       `json:"state"`
	Reason     string       `json:"reason,omitempty"`
	Code       string       `json:"code,omitempty"`
	StartedAt  time.Time    `json:"started_at"`
	FinishedAt *time.Time   `json:"finished_at"`
	Roots      []string     `json:"roots"`
	Totals     Totals       `json:"totals"`
	Active     []ActiveItem `json:"active"`
	// Result is ok, partial, failed, cancelled or skipped once the run has ended.
	Result string `json:"result,omitempty"`
	// Items lists what became of each item (the ended run only).
	Items []ItemResult `json:"items,omitempty"`
	// Warnings are plain notes (a manual run on battery, a catalog refresh that failed).
	Warnings []string `json:"warnings,omitempty"`
	// Ready maps a root id to its overall READY colour at the end of the run.
	Ready map[string]string `json:"ready,omitempty"`
}

// Left is how many items the run did not finish for time or budget reasons (cloud result "partial").
func (s RunStatus) Left() int {
	n := 0
	for _, it := range s.Items {
		if it.Result == "skipped" && (it.Reason == string(plan.ReasonTimeLimit) || it.Reason == string(plan.ReasonBudget) || it.Reason == string(plan.ReasonDisk)) {
			n++
		}
	}
	return n
}

// MarshalTotals is the totals as the raw JSON the sync.done event carries.
func (s RunStatus) MarshalTotals() json.RawMessage {
	b, _ := json.Marshal(s.Totals)
	return b
}
