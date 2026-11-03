package drill

import (
	"context"
	"log/slog"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/audit"
)

// SchemaVersion is the schema_version of the results document (drill.md section 3.4).
const SchemaVersion = 1

// Result is the outcome of one check.
type Result string

// The check results.
const (
	ResultPass Result = "pass"
	ResultWarn Result = "warn"
	ResultFail Result = "fail"
	ResultSkip Result = "skip"
)

func (r Result) valid() bool {
	switch r {
	case ResultPass, ResultWarn, ResultFail, ResultSkip:
		return true
	}
	return false
}

// The values of the document's overall field. pass, warn and fail come from the checks; the rest say
// the run did not complete (see State).
const (
	OverallPass    = "pass"
	OverallWarn    = "warn"
	OverallFail    = "fail"
	OverallAborted = "aborted"
	OverallSkipped = "skipped"
	OverallRunning = "running"
)

// Trigger says who started a run (drill.md section 2).
type Trigger string

// The triggers. TriggerCloud is reserved (no cloud job type starts a drill today) and
// TriggerManualPlug marks a manual "pull the plug" record (DRILL-06), which has no checks.
const (
	TriggerScheduled  Trigger = "scheduled"
	TriggerManual     Trigger = "manual"
	TriggerCloud      Trigger = "cloud"
	TriggerManualPlug Trigger = "manual_plug"
)

// State is the document's optional state field. A run that completed has none, so its document is
// exactly the drill.md section 3.4 shape; the other states are the documents of a run that did not
// complete.
type State string

// The document states.
const (
	// StateRunning is the in-flight snapshot GET /drill?run_id= serves while the run is active. It is
	// never written to disk as a final document.
	StateRunning State = "running"
	// StateSkipped is a scheduled run a precondition refused (drill.md section 4.1).
	StateSkipped State = "skipped"
	// StateAborted is a run that was aborted, interrupted by an agent stop or restart, or ended by a
	// bug.
	StateAborted State = "aborted"
)

// Counts are the number of checks per result.
type Counts struct {
	Pass int `json:"pass"`
	Warn int `json:"warn"`
	Fail int `json:"fail"`
	Skip int `json:"skip"`
}

// CheckResult is one entry of the document's checks array.
type CheckResult struct {
	ID      string         `json:"id"`
	Area    string         `json:"area"`
	Result  Result         `json:"result"`
	Code    string         `json:"code,omitempty"`
	MS      int64          `json:"ms"`
	Details map[string]any `json:"details,omitempty"`
}

// Restored is what the state restore did (drill.md section 4.3): the models loaded again, the
// default model ("" when there was none) and whether the node is back as it was.
type Restored struct {
	LoadedModels []string `json:"loaded_models"`
	DefaultModel string   `json:"default_model"`
	OK           bool     `json:"ok"`
}

// NetworkFence is the fence block of the document (drill.md section 4.4).
type NetworkFence struct {
	Enabled      bool `json:"enabled"`
	BlockedDials int  `json:"blocked_dials"`
	// Code is LAI-DRL-091 when a dial was refused, and Callers name who tried.
	Code    string   `json:"code,omitempty"`
	Callers []string `json:"callers,omitempty"`
}

// Document is the results document of one run (drill.md section 3.4). A completed run has exactly
// the keys of that section; State, Code, Reason and RetryAt appear only on a run that did not
// complete.
type Document struct {
	SchemaVersion  int           `json:"schema_version"`
	RunID          string        `json:"run_id"`
	Trigger        Trigger       `json:"trigger"`
	State          State         `json:"state,omitempty"`
	NodeID         string        `json:"node_id"`
	AgentVersion   string        `json:"agent_version"`
	CatalogVersion string        `json:"catalog_version"`
	Mode           string        `json:"mode"`
	Quick          bool          `json:"quick"`
	StartedAt      time.Time     `json:"started_at"`
	FinishedAt     *time.Time    `json:"finished_at"`
	Overall        string        `json:"overall"`
	SurvivalReady  bool          `json:"survival_ready"`
	Counts         Counts        `json:"counts"`
	Checks         []CheckResult `json:"checks"`
	Restored       *Restored     `json:"restored,omitempty"`
	NetworkFence   NetworkFence  `json:"network_fence"`

	// Code and Reason say why a skipped or aborted run did not complete; Reason is a plain English
	// phrase and Code the doctor code that localizes it. RetryAt is when the agent tries a skipped
	// scheduled run again (drill.md section 4.1).
	Code    string     `json:"code,omitempty"`
	Reason  string     `json:"reason,omitempty"`
	RetryAt *time.Time `json:"retry_at,omitempty"`

	// Notes is the free text of a manual_plug entry (aictl drill record-manual --notes). It stays on
	// the node: the cloud mapping drops it, and a manual_plug entry is never sent as results.
	Notes string `json:"notes,omitempty"`
}

// Phase says when in the run a check executes.
type Phase int

const (
	// PhaseMain checks run first, in Order, while the node is in the state the drill left it in.
	PhaseMain Phase = iota
	// PhasePostRestore checks run after the state restore. drl.restore (check 22) is one: it verifies
	// the restore, so it reads Env.Restored.
	PhasePostRestore
)

// Outcome is what a check returns. The framework adds the id, the area and the duration.
type Outcome struct {
	Result Result
	// Code is the doctor code of a result other than pass (cleared on a pass).
	Code    string
	Details map[string]any
	// StopRun ends the main phase after this check: the checks after it are recorded as skipped, the
	// state is restored and the post-restore checks still run. drl.fence sets it when the fence is
	// broken ("the run aborts").
	StopRun bool
}

// Env is the read-only environment a check runs in. A check never mutates content: it gets no
// writer, and the actor is the agent itself (the internal admin context, no token).
type Env struct {
	RunID   string
	Trigger Trigger
	Quick   bool
	// Mode is the committed mode when the run started ("online" or "offline"; never survival).
	Mode string
	// Actor is audit.System(): what model operations and internal service calls record.
	Actor audit.Actor
	// Now is the run's clock.
	Now func() time.Time
	// Log is the drill logger with the run id.
	Log *slog.Logger
	// Restored is the outcome of the state restore. It is nil in the main phase and set in the
	// post-restore phase (nil there too when no restorer is wired).
	Restored *Restored
}

// Check is one test of the drill. Its Run must honor ctx: the framework cancels it at the timeout
// and when the run is aborted, but it cannot stop a goroutine that ignores it (it stops waiting for
// it and goes on).
type Check struct {
	// ID is drl.<area>.<name>, unique in a registry.
	ID string
	// Area is the area of the document: llm, vision, speech, kit, ...
	Area string
	// Critical marks the checks of the survival-ready set (the stars of drill.md section 4.2).
	Critical bool
	// Order is the number of the check in drill.md section 4.2; checks run in ascending Order, and in
	// registration order among equals.
	Order int
	// Phase is PhaseMain unless the check must see the restored node.
	Phase Phase
	// Timeout bounds Run: 300 s for a model load, 120 s for a completion, 30 s for the rest. Zero
	// means DefaultTimeout.
	Timeout time.Duration
	// FailCode is the doctor code of the failure the framework records when Run times out or panics.
	FailCode string
	// SkipInQuick records the check as skipped, without running it, in a --quick run (check 4).
	SkipInQuick bool
	// Run executes the check.
	Run func(ctx context.Context, env *Env) Outcome
}

// DefaultTimeout is the timeout of a check that declares none (drill.md section 4.2: others 30 s).
const DefaultTimeout = 30 * time.Second
