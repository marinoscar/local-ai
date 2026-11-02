package modes

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/audit"
	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/events"
	"github.com/marinoscar/local-ai/agent/internal/services"
	"github.com/marinoscar/local-ai/agent/internal/state"
)

// Mode is the node's operating mode (modes.md).
type Mode = state.Mode

// The three modes.
const (
	Online   = state.ModeOnline
	Offline  = state.ModeOffline
	Survival = state.ModeSurvival
)

// Warning is one entry of mode_transition.warnings and of mode.changed.warnings.
type Warning = events.Warning

// The step names (agent.md section 4.2.1). They are recorded in state.json, so they never change.
const (
	StepLinkStop        = "link_stop"
	StepSyncCancel      = "sync_cancel"
	StepProberStop      = "prober_stop"
	StepTailscaleOff    = "tailscale_off"
	StepSurvivalModels  = "survival_models"
	StepSurvivalService = "survival_services"
	StepTailscaleOn     = "tailscale_on"
	StepLinkStart       = "link_start"
	StepProberStart     = "prober_start"
	StepPersist         = "persist"
	StepEmit            = "emit"
	StepSyncCatchup     = "sync_catchup"
)

// Steps returns the ordered step list of a transition to target (agent.md section 4.2.1), or nil for
// an invalid mode. The caller owns the slice.
func Steps(target Mode) []string {
	var names []string
	for _, s := range stepsFor(target) {
		names = append(names, s.name)
	}
	return names
}

// Defaults of Options.
const (
	// DefaultMaxAttempts is how many times a step may start before a resume skips it (agent.md 4.2.4).
	DefaultMaxAttempts = 3
	// DefaultTailscaleRetry is the background retry period of a failed tailscale_off.
	DefaultTailscaleRetry = 60 * time.Second
	// DefaultCatchupAfter is how old sync.last_success_at must be for sync_catchup to schedule a sync.
	DefaultCatchupAfter = 24 * time.Hour
	// DefaultCatchupIn is the delay of that sync.
	DefaultCatchupIn = 5 * time.Minute
	// DefaultSuggestSurvivalAfter is how long connectivity must be "none" before GET /mode suggests
	// survival (modes.md section 4).
	DefaultSuggestSurvivalAfter = 30 * time.Minute
)

// LinkController starts and stops the cloud link loop (AGENT-17, AGENT-18). Stop includes the final
// deregister heartbeat (3 s, errors ignored by the implementation). Start starts the loop only when
// the cloud credential exists and is a no-op otherwise.
type LinkController interface {
	Stop(ctx context.Context) error
	Start(ctx context.Context) error
}

// SyncController is the ark sync engine's seam. CancelGraceful cancels a running sync and keeps the
// .part files. ScheduleCatchup schedules one sync in the given time; the sync engine checks the mode
// when it fires (a scheduled run exits "skipped: mode" outside online), which is what makes it
// cancellable.
type SyncController interface {
	CancelGraceful(ctx context.Context) error
	ScheduleCatchup(in time.Duration)
}

// Prober is the connectivity prober (AGENT-13). Both calls are idempotent and do not block.
type Prober interface {
	Start()
	Stop()
}

// TailscaleController applies and removes our Tailscale Serve listeners (agent.md section 4.7,
// AGENT-13). On applies the configured pattern, Off removes both of ours and never anyone else's.
// Both are idempotent. The engine serializes them (never two calls at once) and gives each a
// context with the step timeout, which the implementation must honor. A returned warning is
// recorded as is; the engine retries Off in the background while the node stays offline when it
// returns an error or a warning coded LAI-NET-004 or LAI-NET-006.
type TailscaleController interface {
	On(ctx context.Context) ([]Warning, error)
	Off(ctx context.Context) ([]Warning, error)
}

// ModelLoader loads the profile's survival model set (ROUTER-05, ROUTER-06). It reads
// survival.models and models_max itself, takes modelOpsMu like any load, tries each model for up to
// 300 s, skips the ids in skip, and returns LAI-LLM-020 (a model failed, the next was used) and
// LAI-LLM-021 (none loaded) as warnings. A non-nil error means it could not even try.
type ModelLoader interface {
	LoadSurvivalSet(ctx context.Context, skip []string) ([]Warning, error)
}

// ServiceStarter starts a unit and waits for it to be active. *services.Controller implements it.
type ServiceStarter interface {
	Do(ctx context.Context, svc services.Service, action services.Action) (services.UnitState, error)
}

// PortalSessions is the part of the Portal session store the engine needs: every session is
// dropped when the node leaves survival (PORTAL-02 implements it; auth.PortalSessions satisfies it).
type PortalSessions interface {
	DropAll()
}

// StateStore is the part of *state.Store the engine uses.
type StateStore interface {
	Snapshot() state.State
	Mutate(fn func(*state.State) error) error
	MutateSync(fn func(*state.State) error) error
	Writable() bool
}

// Publisher is the event bus; *events.Bus implements it.
type Publisher interface {
	Publish(typ, opID string, data any) error
}

// AuditWriter is the audit log; *audit.Log implements it.
type AuditWriter interface {
	Write(ev audit.Event) error
}

// Deps are what the engine drives. State is required; every other dependency may be nil, and the
// step that needs it then does nothing (so the engine can be wired as the other subsystems land).
type Deps struct {
	State     StateStore
	Events    Publisher
	Audit     AuditWriter
	Link      LinkController
	Sync      SyncController
	Prober    Prober
	Tailscale TailscaleController
	Models    ModelLoader
	Services  ServiceStarter
	Portal    PortalSessions
	// SurvivalServices returns the short names of the services survival mode starts besides kiwix
	// and whisper: the profile's survival.require_services. Nil means none.
	SurvivalServices func() []string
}

// Options tune the engine. Every field is optional.
type Options struct {
	// Clock is the time source for timestamps, step timeouts and the Tailscale retry; default real.
	Clock config.Clock
	// Timeouts overrides entries of DefaultTimeouts, by step name. A zero value means no timeout.
	Timeouts map[string]time.Duration
	// MaxAttempts overrides DefaultMaxAttempts.
	MaxAttempts int
	// TailscaleRetry overrides DefaultTailscaleRetry.
	TailscaleRetry time.Duration
	// CatchupAfter and CatchupIn override the sync_catchup defaults.
	CatchupAfter, CatchupIn time.Duration
	// SuggestSurvivalAfter overrides DefaultSuggestSurvivalAfter.
	SuggestSurvivalAfter time.Duration
	// NewOpID makes operation ids ("op_" plus 12 base32 characters); default random.
	NewOpID func() string
	// Logger defaults to a discarding logger.
	Logger *slog.Logger
}

// DefaultTimeouts are the per-step timeouts of agent.md section 4.2.1. survival_models is a
// backstop (300 s for each of up to three models): the loader enforces the 300 s of each load
// itself. A step missing from the table (persist, emit, sync_catchup) has no timeout.
var DefaultTimeouts = map[string]time.Duration{
	StepLinkStop:        15 * time.Second,
	StepSyncCancel:      30 * time.Second,
	StepProberStop:      time.Second,
	StepTailscaleOff:    20 * time.Second,
	StepSurvivalModels:  3 * 300 * time.Second,
	StepSurvivalService: 60 * time.Second,
	StepTailscaleOn:     30 * time.Second,
	StepLinkStart:       time.Second,
	StepProberStart:     time.Second,
}

// RequestOptions are the options of Engine.Request.
type RequestOptions struct {
	// Wait blocks Request until the transition ends (or ctx ends; the transition goes on).
	Wait bool
	// Reapply re-runs every step even when the node is already in the target mode.
	Reapply bool
	// Actor says who asked; it is recorded in mode_transition and the audit log.
	Actor state.Actor
}

// Result is the outcome of Request.
type Result struct {
	// OpID is the operation id; empty when Already.
	OpID string
	// From is the mode the transition started from.
	From Mode
	// Target is the requested mode. When Done, it is the mode the node is in.
	Target Mode
	// Already: the node is in the target mode and Reapply was not set; nothing was done.
	Already bool
	// Queued: the request (survival only) was recorded as pending_mode because the agent is
	// starting or stopping; it runs after startup step 12 or on the next start.
	Queued bool
	// Joined: a survival transition was already running and the request attached to it.
	Joined bool
	// Step and StartedAt describe the transition when it was accepted without Wait.
	Step      string
	StartedAt time.Time
	// Done is set when Wait was given and the transition ended. Warnings are then its warnings.
	Done     bool
	Warnings []Warning
	// Preempted: with Wait, the transition was stopped by a survival request; PreemptedBy is the
	// survival operation that replaced it.
	Preempted   bool
	PreemptedBy string
	// Interrupted: with Wait, the engine shut down mid-transition; it resumes at the next start.
	Interrupted bool
}

// Errors of Request.
var (
	// ErrInvalidTarget: the target is not online, offline or survival.
	ErrInvalidTarget = errors.New("modes: invalid target mode")
	// ErrNotAccepting: the engine is not accepting requests (before Open, or shutting down), and
	// the request is not for survival, which is queued instead.
	ErrNotAccepting = errors.New("modes: not accepting mode changes (starting or stopping)")
	// ErrClosed: the engine was shut down.
	ErrClosed = errors.New("modes: engine closed")
)

// BusyError: another transition is running (409 LAI-AGT-022). OpID is the running one.
type BusyError struct {
	OpID   string
	Target Mode
}

func (e *BusyError) Error() string {
	return fmt.Sprintf("modes: another mode change is running: %s (to %s)", e.OpID, e.Target)
}

// Transition is the engine's working copy of state.mode_transition, handed to a step as a copy.
type Transition struct {
	OpID      string
	From      Mode
	Target    Mode
	Step      string
	StartedAt time.Time
	Attempts  map[string]int
	Warnings  []Warning
	Actor     state.Actor
	// Reapply: the transition re-runs the steps of the mode the node is already in.
	Reapply bool
	// Resumed: the transition was found in state.json at startup.
	Resumed bool
	// SkipModels are the models survival_models must not try: the ones whose load was in progress
	// when the agent died (state.models.last_load), accumulated across restarts in
	// mode_transition.skip_models.
	SkipModels []string
}

func (t Transition) clone() Transition {
	c := t
	c.Attempts = make(map[string]int, len(t.Attempts))
	for k, v := range t.Attempts {
		c.Attempts[k] = v
	}
	c.Warnings = append([]Warning(nil), t.Warnings...)
	c.SkipModels = append([]string(nil), t.SkipModels...)
	return c
}

// Change is what Subscribe delivers when the committed mode changes.
type Change struct {
	From, To Mode
	OpID     string
	At       time.Time
}

// TransitionInfo is the in-flight transition as GET /mode shows it.
type TransitionInfo struct {
	OpID      string
	From      Mode
	Target    Mode
	Step      string
	StartedAt time.Time
	Warnings  []Warning
}

// Info is the engine's picture for GET /mode.
type Info struct {
	// Mode is the committed mode (state.mode).
	Mode Mode
	// Effective is the mode the node is heading to: the target of a running transition, else Mode.
	Effective Mode
	// Since is when Mode was committed; nil when unknown (a fresh node).
	Since *time.Time
	// Transition is the transition in flight, nil when idle.
	Transition *TransitionInfo
	// Connectivity is state.connectivity.value, "not_checked" when empty.
	Connectivity string
	// SuggestSurvival: online and no connectivity for more than 30 minutes (never automatic).
	SuggestSurvival bool
}

// HTTPPolicy are the mode-dependent HTTP flags AGENT-16 consumes (agent.md section 4.2.5).
type HTTPPolicy struct {
	// RedirectRootToPortal: GET / answers 302 /portal (survival only).
	RedirectRootToPortal bool
	// ChatAnonymous: /chat/ needs no session (survival only, agent.md section 3.11.1).
	ChatAnonymous bool
}
