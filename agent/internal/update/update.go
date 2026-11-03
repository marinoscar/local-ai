package update

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/audit"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/events"
	"github.com/marinoscar/local-ai/agent/internal/execx"
	"github.com/marinoscar/local-ai/agent/internal/logx"
	"github.com/marinoscar/local-ai/agent/internal/state"
)

// Status of an Update or Check.
const (
	// StatusAvailable (Check only): a newer release exists.
	StatusAvailable = "available"
	// StatusAlreadyCurrent: nothing to install (the version is the running one, or older and a downgrade is
	// not allowed). Reason says which.
	StatusAlreadyCurrent = "already_current"
	// StatusRestarting (Update only): the new binary is installed, the rollback is armed and the restart was
	// asked for. The new process settles the rest (Startup).
	StatusRestarting = "restarting"
)

// Reasons of StatusAlreadyCurrent.
const (
	ReasonSameVersion      = "same_version"
	ReasonDowngradeRefused = "downgrade_refused"
)

// States of the update.state event.
const (
	StateChecking    = "checking"
	StateDownloading = "downloading"
	StateVerifying   = "verifying"
	StateInstalling  = "installing"
	StateRestarting  = "restarting"
	StateOK          = "ok"
	StateRolledBack  = "rolled_back"
	StateFailed      = "failed"
)

// Audit actions (agent.update.*, agent.md section 4.11).
const (
	ActionInstall    = "agent.update.install"
	ActionOK         = "agent.update.ok"
	ActionRolledBack = "agent.update.rolled_back"
	ActionFail       = "agent.update.fail"
)

// Timing of the rollback guard (agent.md section 4.10).
const (
	// RollbackAfter is the delay of the transient timer and the deadline written in the marker.
	RollbackAfter = 120 * time.Second
	// HealthWindow is how long the new process has to pass its health check.
	HealthWindow = 60 * time.Second
	// MaxBootAttempts is the number of starts of the new binary after which it is rolled back without
	// another health check.
	MaxBootAttempts = 3
	// SmokeTimeout bounds `<staged> version --json`.
	SmokeTimeout = 5 * time.Second
	// staleMarkerAfter is how long past its deadline a marker that nobody acted on is still respected.
	staleMarkerAfter = 10 * time.Minute

	timerUnit   = "localai-agent-rollback.timer"
	serviceUnit = "localai-agent-rollback.service"
	agentUnit   = "localai-agent.service"
)

// Paths are the directories of an update: /var/lib/localai/update and /opt/localai/bin on a node, a temp
// directory in tests.
type Paths struct {
	UpdateDir string
	BinDir    string
}

// DefaultPaths are the canonical paths (overview.md section 3.2).
func DefaultPaths() Paths {
	return Paths{UpdateDir: "/var/lib/localai/update", BinDir: "/opt/localai/bin"}
}

// Agent is the installed binary.
func (p Paths) Agent() string { return filepath.Join(p.BinDir, "aiagent") }

// Prev is the previous binary, always kept (ADR-0001): `cp aiagent.prev aiagent` recovers by hand.
func (p Paths) Prev() string { return filepath.Join(p.BinDir, "aiagent.prev") }

// New is the name a binary has while it is written next to the installed one.
func (p Paths) New() string { return filepath.Join(p.BinDir, "aiagent.new") }

// Pending is the marker of an update in progress.
func (p Paths) Pending() string { return filepath.Join(p.UpdateDir, "pending.json") }

// Clock is the time seam: Now for stamps and deadlines, Sleep for every wait, so tests drive time and never
// sleep.
type Clock interface {
	Now() time.Time
	// Sleep waits d, or returns ctx.Err() when ctx ends first.
	Sleep(ctx context.Context, d time.Duration) error
}

// RealClock is the wall clock.
type RealClock struct{}

// Now implements Clock.
func (RealClock) Now() time.Time { return time.Now() }

// Sleep implements Clock.
func (RealClock) Sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// AuditSink is the audit log: *audit.Log implements it.
type AuditSink interface{ Write(ev audit.Event) error }

// EventSink is the event bus: *events.Bus implements it.
type EventSink interface {
	Publish(typ, opID string, data any) error
}

// StateStore is the part of the state store the update uses: *state.Store implements it. Only the daemon
// has one; `aictl update` runs in its own process and never writes state.json (agent.md section 3.5).
type StateStore interface {
	Snapshot() state.State
	MutateSync(fn func(*state.State) error) error
}

// AlertSink raises a doctor alert: *doctor.Manager implements it.
type AlertSink interface {
	Raise(code string, details map[string]any, opts ...doctor.RaiseOption) error
}

// Runner runs a self-update: steps 1 to 8 of agent.md section 4.10 (Update), the health check of the new
// process (Startup), and the rollback paths (RollbackCheck). The agent job and `aictl update` call the same
// Runner; every outside dependency is a field with a fake in the tests.
//
// A zero Runner is not usable: set at least Paths, Current and Mode (everything else has a default).
// Do not copy a Runner after its first use.
type Runner struct {
	Paths Paths
	// Source is the GitHub client; Source.Repo, Channel come from node.yaml update{}.
	Source Source
	// Current is the running version (app.Version).
	Current string
	// Arch is the architecture to install; default runtime.GOARCH.
	Arch string
	// AllowDowngrade is update.allow_downgrade.
	AllowDowngrade bool
	// Keys are the trusted public keys; nil means EmbeddedKeys().
	Keys []PublicKey
	FS   FS
	// Exec runs the staged binary, systemctl and systemd-run (always a fixed argv).
	Exec  execx.Runner
	Clock Clock
	// Mode returns the current mode name ("online", "offline", "survival"); an update only runs online.
	Mode func() string

	// Optional sinks. Nil ones are skipped.
	Audit  AuditSink
	Events EventSink
	State  StateStore
	Alerts AlertSink
	// Actor is the principal written to the audit log; default audit.System().
	Actor audit.Actor
	Log   *slog.Logger

	// Probe is one pass of the post-update health check of the new process (agent.md section 4.10 step 9:
	// both listeners up, state store writable, /health from itself, router reachable or the llama unit
	// unchanged, cloud registration ok when it was linked). nil means "alive is enough".
	Probe func(ctx context.Context) error

	// Overrides for tests; zero values take the defaults above.
	Systemctl       string
	SystemdRun      string
	DownloadTimeout time.Duration
	HealthWindow    time.Duration
	HealthInterval  time.Duration
	RollbackAfter   time.Duration

	once sync.Once
	mu   sync.Mutex // one update at a time in this process

	startMu     sync.Mutex
	startDone   chan struct{}
	startResult StartupResult
}

func (r *Runner) init() {
	r.once.Do(func() {
		if r.FS == nil {
			r.FS = OSFS{}
		}
		if r.Exec == nil {
			r.Exec = execx.Exec{}
		}
		if r.Clock == nil {
			r.Clock = RealClock{}
		}
		if r.Arch == "" {
			r.Arch = runtime.GOARCH
		}
		if r.Keys == nil {
			r.Keys = EmbeddedKeys()
		}
		if r.Log == nil {
			r.Log = slog.Default()
		}
		r.Log = r.Log.With("component", "update")
		if r.Actor.Kind == "" {
			r.Actor = audit.System()
		}
		if r.Paths == (Paths{}) {
			r.Paths = DefaultPaths()
		}
		if r.HealthWindow <= 0 {
			r.HealthWindow = HealthWindow
		}
		if r.HealthInterval <= 0 {
			r.HealthInterval = 2 * time.Second
		}
		if r.RollbackAfter <= 0 {
			r.RollbackAfter = RollbackAfter
		}
		r.startDone = make(chan struct{})
	})
}

// Request is one update.
type Request struct {
	// Version is the version to install; "" is the newest release of the channel.
	Version string
	// Job is the cloud command that asked for it; its claim token is kept in the marker so the new process
	// can settle it (agent.md section 4.10 step 9). nil for `aictl update`.
	Job *JobRef
	// Actor overrides Runner.Actor for the audit lines (audit.Cloud(jobID), audit.CLIRoot()).
	Actor audit.Actor
	// OpID correlates the events.
	OpID string
}

// JobRef is the cloud command that is waiting for an update: pending.json "job".
type JobRef struct {
	JobID      string `json:"job_id"`
	ClaimToken string `json:"claim_token"`
}

// Result is the outcome of Check or Update.
type Result struct {
	Status string
	// From is the running version; To the version resolved (installed, or available).
	From, To string
	// Reason qualifies StatusAlreadyCurrent.
	Reason string
}

// refuseUnlessOnline is the mode rule: an update needs the internet, so it never runs in offline or survival
// mode (LAI-AGT-023).
func (r *Runner) refuseUnlessOnline() error {
	if r.Mode == nil {
		return nil
	}
	if m := r.Mode(); m != "online" {
		return fail(doctor.AGT023, "mode", "Updating needs the internet and this node is not online. Switch to online mode first (`aictl mode online`).",
			fmt.Errorf("mode %q", m))
	}
	return nil
}

func (r *Runner) emit(opID, st, from, to string) {
	if r.Events == nil {
		return
	}
	if err := r.Events.Publish(events.TypeUpdateState, opID, events.UpdateState{State: st, From: from, To: to}); err != nil {
		r.Log.Debug("update.state could not be published", "error", logx.ScrubString(err.Error()))
	}
}

func (r *Runner) writeAudit(actor audit.Actor, action string, res audit.Result, code string, target map[string]string, opID string) {
	if r.Audit == nil {
		return
	}
	if actor.Kind == "" {
		actor = r.Actor
	}
	ev := audit.Event{Action: action, Result: res, Code: code, Actor: actor, Target: target, OpID: opID}
	if err := r.Audit.Write(ev); err != nil {
		r.Log.Warn("an audit line could not be written", "action", action)
	}
}

// resolve runs steps 0 and 1: the mode rule, then the release and the version comparison. proceed is true
// when there is something to install.
func (r *Runner) resolve(ctx context.Context, want string) (rel ReleaseInfo, res Result, proceed bool, err error) {
	if err = r.refuseUnlessOnline(); err != nil {
		return
	}
	cur, perr := ParseVersion(r.Current)
	if perr != nil {
		// A development build (or a stamp that is not a version) is older than any release.
		cur = Version{}
	}
	src := r.Source
	rel, err = src.Resolve(ctx, want)
	if err != nil {
		return
	}
	res = Result{From: r.Current, To: rel.Version.String()}
	switch c := rel.Version.Compare(cur); {
	case c == 0:
		res.Status, res.Reason = StatusAlreadyCurrent, ReasonSameVersion
		res.To = r.Current
	case c < 0 && !r.AllowDowngrade:
		res.Status, res.Reason = StatusAlreadyCurrent, ReasonDowngradeRefused
		res.To = r.Current
	default:
		res.Status = StatusAvailable
		proceed = true
	}
	return
}

// Check resolves the release without installing anything (`aictl update --check`): StatusAvailable with the
// version found, or StatusAlreadyCurrent.
func (r *Runner) Check(ctx context.Context, want string) (Result, error) {
	r.init()
	_, res, _, err := r.resolve(ctx, want)
	return res, err
}

// Update runs steps 1 to 8: resolve, download, verify the signature and the checksum, smoke-test the staged
// binary, install it (keeping the previous one as aiagent.prev), arm the rollback and ask systemd to restart
// the agent. It returns StatusRestarting once the restart was asked for, and it never settles anything: the
// new process finishes the job (Startup).
//
// Fail closed: every error leaves the installed binary as it was, with one exception that is also safe (the
// install succeeded but the guard could not be armed: the previous binary is put back before returning).
// The error is an *Error with its doctor code.
func (r *Runner) Update(ctx context.Context, req Request) (res Result, err error) {
	r.init()
	r.mu.Lock()
	defer r.mu.Unlock()
	actor := req.Actor
	if actor.Kind == "" {
		actor = r.Actor
	}
	from, to := r.Current, req.Version
	defer func() {
		if err == nil {
			return
		}
		r.Log.Warn("the update failed", "code", CodeOf(err), "step", stepOf(err), "error", logx.ScrubString(err.Error()))
		r.writeAudit(actor, ActionFail, audit.ResultFail, CodeOf(err), map[string]string{"from": from, "to": to, "step": stepOf(err)}, req.OpID)
		r.emit(req.OpID, StateFailed, from, to)
		switch CodeOf(err) {
		case doctor.AGT020, doctor.AGT021, doctor.AGT022, doctor.AGT023:
			// A refusal (bad request, unknown version, busy, offline) is not a failed update.
		default:
			r.recordLast(state.UpdateFailed, from, to)
		}
	}()

	r.emit(req.OpID, StateChecking, from, to)
	if perr := r.clearStalePending(); perr != nil {
		return Result{}, perr
	}
	if p, perr := r.readPending(); perr == nil && p != nil {
		return Result{}, fail(doctor.AGT022, "resolve", "An update is already in progress.", nil)
	}
	rel, res, proceed, err := r.resolve(ctx, req.Version)
	if err != nil {
		return Result{}, err
	}
	to = res.To
	if !proceed {
		r.Log.Info("no update to install", "reason", res.Reason, "current", r.Current)
		r.emit(req.OpID, StateOK, from, res.To)
		return res, nil
	}
	if !SupportedArch(r.Arch) {
		return Result{}, fail(doctor.AGT009, "resolve", "This computer's architecture has no agent release.", fmt.Errorf("arch %q", r.Arch))
	}

	r.cleanStaging(rel.Version)
	r.emit(req.OpID, StateDownloading, from, to)
	st, err := r.download(ctx, rel, r.Arch)
	if err != nil {
		if CodeOf(err) == doctor.AGT010 {
			r.discard(st)
		}
		return Result{}, err
	}

	r.emit(req.OpID, StateVerifying, from, to)
	if err = r.verify(st); err != nil {
		r.discard(st)
		return Result{}, err
	}
	if err = r.smoke(ctx, st, rel.Version); err != nil {
		r.discard(st)
		return Result{}, err
	}

	r.emit(req.OpID, StateInstalling, from, to)
	if err = r.install(st); err != nil {
		r.discard(st)
		return Result{}, err
	}
	now := r.Clock.Now().UTC()
	p := Pending{From: from, To: to, StartedAt: now, Deadline: now.Add(r.RollbackAfter), Job: req.Job}
	if err = r.arm(ctx, p); err != nil {
		// Never leave a new binary without its guard: put the previous one back.
		if rerr := r.restorePrev(); rerr != nil {
			r.Log.Error("the previous agent could not be put back", "error", logx.ScrubString(rerr.Error()))
		}
		_ = r.removePending()
		r.discard(st)
		return Result{}, err
	}
	r.discard(st) // the staged copy is installed; its 200 MiB are not needed any more
	r.writeAudit(actor, ActionInstall, audit.ResultOK, "", map[string]string{"from": from, "to": to}, req.OpID)
	r.emit(req.OpID, StateRestarting, from, to)
	if err = r.restart(ctx); err != nil {
		// The restart was refused: nothing runs the new binary yet, so undo the install completely.
		r.undoInstall(ctx)
		return Result{}, err
	}
	r.Log.Info("the update is installed; the agent restarts", "from", from, "to", to)
	return Result{Status: StatusRestarting, From: from, To: to}, nil
}

// undoInstall puts the previous binary back, removes the marker and the timer (the restart never happened).
func (r *Runner) undoInstall(ctx context.Context) {
	if err := r.restorePrev(); err != nil {
		r.Log.Error("the previous agent could not be put back", "error", logx.ScrubString(err.Error()))
	}
	_ = r.removePending()
	r.stopTimer(ctx)
}

func stepOf(err error) string {
	if e, ok := err.(*Error); ok {
		return e.Step
	}
	return ""
}

// discard deletes a staging directory.
func (r *Runner) discard(st Staged) {
	if st.Dir == "" {
		return
	}
	if err := r.FS.RemoveAll(st.Dir); err != nil {
		r.Log.Warn("the staging folder could not be removed", "error", logx.ScrubString(err.Error()))
	}
}

// cleanStaging removes the staging folders of other versions (an earlier failed run's leftovers).
func (r *Runner) cleanStaging(keep Version) {
	entries, err := r.FS.ReadDir(r.Paths.UpdateDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if v, err := ParseVersion(e.Name()); err == nil && v.Compare(keep) != 0 {
			_ = r.FS.RemoveAll(filepath.Join(r.Paths.UpdateDir, e.Name()))
		}
	}
}

// recordLast writes state.update.last when the Runner has a state store (the daemon); the CLI never does.
func (r *Runner) recordLast(result, from, to string) {
	if r.State == nil {
		return
	}
	at := r.Clock.Now().UTC()
	err := r.State.MutateSync(func(s *state.State) error {
		s.Update.Last = &state.UpdateLast{Result: result, From: from, To: to, At: at}
		return nil
	})
	if err != nil {
		r.Log.Warn("state.update.last could not be recorded", "error", logx.ScrubString(err.Error()))
	}
}

// jsonMarshal is json.MarshalIndent with a trailing newline.
func jsonMarshal(v any) ([]byte, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}
