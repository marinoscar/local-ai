package ensure

import (
	"encoding/json"
	"time"
)

// Auto is the setting ensure.auto of node.yaml.
type Auto string

// The values of ensure.auto. On installs by itself; ask only reports and waits for the button or the
// command; off is the same as ask but says the automatic install is switched off.
const (
	AutoOn  Auto = "on"
	AutoAsk Auto = "ask"
	AutoOff Auto = "off"
)

// Valid reports whether a is one of the three values.
func (a Auto) Valid() bool { return a == AutoOn || a == AutoAsk || a == AutoOff }

// Step ids. They are part of the REST contract (agent.md section 3.8.33) and are never renamed.
const (
	IDKiwix          = "tool.kiwix-tools"
	IDPmtiles        = "tool.pmtiles"
	IDPiper          = "tool.piper"
	IDPackages       = "apt.packages"
	IDWhisperRestore = "whisper.restore"
	IDWhisperBuild   = "whisper.build"
	IDRestartKiwix   = "restart.kiwix"
)

// StepIDs lists every step id, in the order a pass runs them.
var StepIDs = []string{IDPackages, IDKiwix, IDPmtiles, IDPiper, IDWhisperRestore, IDWhisperBuild, IDRestartKiwix}

// Kind is what a step does. The set is closed: the executor refuses a step of any other kind (the
// allowlist, allowlist.go).
type Kind string

// The kinds.
const (
	KindTool           Kind = "install_tool"
	KindPackages       Kind = "install_packages"
	KindWhisperRestore Kind = "restore_whisper"
	KindWhisperBuild   Kind = "build_whisper"
	KindRestart        Kind = "restart_service"
)

// Status is where a step stands.
type Status string

// The statuses of a step.
const (
	// StatusOK: nothing to do, the node already has it.
	StatusOK Status = "ok"
	// StatusPending: will be done by the next pass (automatic, ensure.auto is on).
	StatusPending Status = "pending"
	// StatusWaiting: work to do, but ensure.auto is ask or off, so it waits for the button or the command.
	StatusWaiting Status = "waiting"
	// StatusNeedsConfirm: never automatic; runs only when the caller names the step in confirm.
	StatusNeedsConfirm Status = "needs_confirm"
	// StatusBlocked: cannot be done now: the kit has no verified copy (Reason says why).
	StatusBlocked Status = "blocked"
	// StatusRunning: running now.
	StatusRunning Status = "running"
	// StatusDone: done by the latest pass.
	StatusDone Status = "done"
	// StatusFailed: tried by the latest pass and failed (Code says why); the next pass tries again.
	StatusFailed Status = "failed"
	// StatusCancelled: not done because the pass was cancelled.
	StatusCancelled Status = "cancelled"
)

// Reasons of a blocked step (Step.Reason).
const (
	ReasonKitMissing = "kit_missing"
	ReasonKitDamaged = "kit_damaged"
	ReasonNotInKit   = "not_in_kit"
)

// Trigger says what started a pass.
type Trigger string

// The triggers.
const (
	TriggerSync   Trigger = "sync_done"
	TriggerStart  Trigger = "start"
	TriggerOnline Trigger = "mode_online"
	TriggerManual Trigger = "manual"
)

// State is the one word the node card shows.
type State string

// The states of GET /ensure.
const (
	// StateComplete: nothing left to install.
	StateComplete State = "complete"
	// StateInstalling: a pass is running.
	StateInstalling State = "installing"
	// StateNeedsYou: something remains that only a person can start or decide (a confirmation, a failed step,
	// a kit item that is missing, or ensure.auto is ask).
	StateNeedsYou State = "needs_you"
	// StateOff: ensure.auto is off and something is missing.
	StateOff State = "off"
)

// Result of a pass.
type Result string

// The results of a pass.
const (
	ResultNothing   Result = "nothing_to_do"
	ResultOK        Result = "ok"
	ResultPartial   Result = "partial"
	ResultFailed    Result = "failed"
	ResultCancelled Result = "cancelled"
)

// Step is one thing the node may need.
type Step struct {
	ID     string `json:"id"`
	Kind   Kind   `json:"kind"`
	Status Status `json:"status"`
	// Target is what the step acts on: the tool, "packages", "whisper", the service.
	Target string `json:"target,omitempty"`
	// Item is the catalog id of the kit item the step installs from (kit-kiwix-tools).
	Item string `json:"item,omitempty"`
	// Version is the version the step installs; From the version it replaces ("" when none).
	Version string `json:"version,omitempty"`
	From    string `json:"from,omitempty"`
	// Packages lists the packages an apt.packages step installs.
	Packages []string `json:"packages,omitempty"`
	// NeedsConfirmation is true for a step that never runs without the caller's confirmation.
	NeedsConfirmation bool `json:"needs_confirmation"`
	// Reason and Code explain a blocked or failed step (the doctor code's own fix is the way out).
	Reason string `json:"reason,omitempty"`
	Code   string `json:"code,omitempty"`
	// Error is the plain sentence of a failure, in English; the UI shows the code's fix text.
	Error      string     `json:"error,omitempty"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`

	// Source is the kit root the step installs from (a path); never sent to a client.
	Source string `json:"-"`
	// File is the ready-made build a whisper.restore step restores; never sent to a client.
	File string `json:"-"`
}

// Plan is what the planner found.
type Plan struct {
	Steps []Step `json:"steps"`
	// At is when it was made.
	At time.Time `json:"at"`
}

// Step returns the step with the id.
func (p Plan) Step(id string) (Step, bool) {
	for _, s := range p.Steps {
		if s.ID == id {
			return s, true
		}
	}
	return Step{}, false
}

// Work counts the steps that are not ok.
func (p Plan) Work() int {
	n := 0
	for _, s := range p.Steps {
		if s.Status != StatusOK && s.Status != StatusDone {
			n++
		}
	}
	return n
}

// Run describes a pass.
type Run struct {
	ID         string     `json:"id"`
	Trigger    Trigger    `json:"trigger"`
	DryRun     bool       `json:"dry_run"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	Result     Result     `json:"result,omitempty"`
}

// Progress is how far the running pass is.
type Progress struct {
	Done  int `json:"done"`
	Total int `json:"total"`
	// Current is the id of the step running now ("" when none).
	Current string `json:"current,omitempty"`
}

// NextAction is the single next thing a person should do, in plain words.
type NextAction struct {
	// Kind is none, wait, press_button, confirm, sync, fix.
	Kind string `json:"kind"`
	Text string `json:"text"`
	// Step is the step the action is about ("" when none).
	Step string `json:"step,omitempty"`
}

// Next-action kinds.
const (
	NextNone        = "none"
	NextWait        = "wait"
	NextPressButton = "press_button"
	NextConfirm     = "confirm"
	NextSync        = "sync"
	NextFix         = "fix"
)

// View is the body of GET /ensure and the 202 of POST /ensure (agent.md section 3.8.33): the state, the
// steps with their status, the progress and the next action, in the language asked for.
type View struct {
	State      State      `json:"state"`
	Auto       Auto       `json:"auto"`
	Message    string     `json:"message"`
	NextAction NextAction `json:"next_action"`
	Progress   Progress   `json:"progress"`
	Steps      []ViewStep `json:"steps"`
	Run        *Run       `json:"run"`
	DryRun     bool       `json:"dry_run"`
	CheckedAt  time.Time  `json:"checked_at"`
}

// ViewStep is a step as a client sees it: the Step plus its title and sentence in the language asked for.
type ViewStep struct {
	Step
	Title  string `json:"title"`
	Detail string `json:"detail"`
}

// JSON renders v with the stable key order of the struct tags.
func (v View) JSON() []byte {
	b, _ := json.Marshal(v)
	return b
}
