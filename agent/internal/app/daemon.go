package app

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/api"
	"github.com/marinoscar/local-ai/agent/internal/audit"
	"github.com/marinoscar/local-ai/agent/internal/cloud/jobs"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/drill"
	"github.com/marinoscar/local-ai/agent/internal/logx"
	"github.com/marinoscar/local-ai/agent/internal/maps"
	"github.com/marinoscar/local-ai/agent/internal/maps/places"
	"github.com/marinoscar/local-ai/agent/internal/modelsapi"
	"github.com/marinoscar/local-ai/agent/internal/modes"
	"github.com/marinoscar/local-ai/agent/internal/portal"
	"github.com/marinoscar/local-ai/agent/internal/state"
	"github.com/marinoscar/local-ai/agent/internal/statusapi"
	"github.com/marinoscar/local-ai/agent/internal/svc/kiwix"
	"github.com/marinoscar/local-ai/agent/internal/svc/piper"
	"github.com/marinoscar/local-ai/agent/internal/svc/whisper"
)

// FatalError is a startup failure that ends the process: exit 1, and systemd restarts the agent
// (agent.md section 4.1: not root, or both listeners failed).
type FatalError struct {
	// Code is the doctor code logged with the failure.
	Code string
	Err  error
}

func (e *FatalError) Error() string { return e.Code + ": " + e.Err.Error() }
func (e *FatalError) Unwrap() error { return e.Err }

// Step budgets of agent.md section 4.1.
const (
	stepTimeoutConfig    = time.Second
	stepTimeoutState     = time.Second
	stepTimeoutDirs      = time.Second
	stepTimeoutSecrets   = time.Second
	stepTimeoutCatalog   = 5 * time.Second
	stepTimeoutSubsystem = 5 * time.Second
	stepTimeoutListeners = 2 * time.Second
	stepTimeoutReconcile = 30 * time.Second
	stepTimeoutUpdate    = 60 * time.Second
)

// StepResult is what the startup recorded of one step.
type StepResult struct {
	N        int
	Name     string
	Duration time.Duration
	// Err is the step's failure (it degraded, or was fatal); TimedOut says the step budget ran out.
	Err      error
	TimedOut bool
}

// Daemon is `aiagent serve`: the startup of agent.md section 4.1 and the shutdown of section 4.12
// over the subsystems of the other packages. Create it with NewDaemon, call Start, wait for Started, and
// call Shutdown (or use Serve).
type Daemon struct {
	// firstAid is the first-aid availability worker (portal_wire.go); started in step 16.
	firstAid *portal.FirstAidResolver
	env      Env
	hooks    hookSet
	c        *Components
	log      *slog.Logger
	level    *slog.LevelVar

	began time.Time

	// rootCtx is the long-lived context of the workers (cancelled once the shutdown has nothing left
	// that uses it); bootCtx is the context of the background startup steps (cancelled when the
	// shutdown begins, so a restore in progress stops starting new work).
	rootCtx    context.Context
	cancel     context.CancelFunc
	bootCtx    context.Context
	bootCancel context.CancelFunc
	bg         sync.WaitGroup

	closers    []io.Closer
	mode       *modeView
	gauges     *gauges
	sdeps      statusapi.Deps
	merger     *statusapi.ModelMerger
	portalOnce sync.Once
	pSessions  *portal.Sessions
	pMetrics   *portal.Metrics
	syncEngine modelsapi.SyncEngine
	ark        *arkStack    // nil when the sync stack could not be built
	ensure     *ensureStack // nil when the sync stack or the install engine could not be built (ensure.go)
	piper      *piper.Service
	whisperSTT *whisper.STTService
	maps       *maps.Maps
	places     *places.Manager
	hooksSync  syncCanceller
	reporter   *drill.Reporter
	updater    *jobs.AgentUpdate
	modelsDeps modelsapi.Deps
	models     *modelsapi.Service
	svcDeps    api.ServicesDeps

	kiwixStatus *kiwix.Status // what the last library run found (health probe, GET /services)
	kiwixKick   kiwixKick     // asks the library worker for a regeneration
	doctorKick  chan struct{} // coalesced requests for a doctor pass (the drill reporter's OnChange)

	startedCh chan struct{} // closed at step 17
	gateCh    chan struct{} // closed when step 12 ends
	started   atomic.Bool
	stopping  atomic.Bool

	mu       sync.Mutex
	steps    []StepResult
	pending  []pendingAlert
	periodic []Worker // started workers, stopped in reverse order at shutdown step 2
	stoppers shutdownSet

	audit *auditSlot
	claim *claimSlot
	views *lazyViews

	owner       *state.Owner
	localaiUID  int
	localaiGID  int
	haveLocalAI bool

	whisper whisperState

	shutdown1 sync.Once
}

type pendingAlert struct {
	code    string
	details map[string]any
	sev     doctor.Severity
}

// NewDaemon returns a Daemon over env. Nothing runs until Start.
func NewDaemon(env Env) *Daemon {
	env.defaults()
	d := &Daemon{env: env, hooks: env.Hooks.freeze(), startedCh: make(chan struct{}), gateCh: make(chan struct{}),
		doctorKick: make(chan struct{}, 1)}
	d.c = &Components{Env: env, Paths: env.paths(), Notify: env.Notifier}
	d.audit = &auditSlot{}
	d.claim = &claimSlot{}
	d.views = &lazyViews{}
	return d
}

// Components returns what the daemon has built (for the hooks and the tests).
func (d *Daemon) Components() *Components { return d.c }

// Started is closed when startup step 17 has run (the daemon is fully up).
func (d *Daemon) Started() <-chan struct{} { return d.startedCh }

// GateOpened is closed when step 12 has ended and the mutating routes are open.
func (d *Daemon) GateOpened() <-chan struct{} { return d.gateCh }

// Steps returns what the startup recorded so far.
func (d *Daemon) Steps() []StepResult {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]StepResult(nil), d.steps...)
}

// ensureLogger builds the logger once (step 2; step 1 needs it to report a fatal error).
func (d *Daemon) ensureLogger() {
	if d.log != nil {
		return
	}
	if d.env.Logger != nil {
		d.log = d.env.Logger
		d.level = new(slog.LevelVar)
	} else {
		d.level = new(slog.LevelVar)
		d.log = logx.New(d.env.Stdout, d.level)
	}
	d.c.Log = d.log
}

// raise raises an alert on behalf of an event (a startup finding, not a periodic check). Before the
// alert manager exists the alert is kept and raised when it does.
func (d *Daemon) raise(code string, details map[string]any) { d.raiseSeverity(code, "", details) }

// raiseSeverity is raise with an explicit severity ("" is the code's default).
func (d *Daemon) raiseSeverity(code string, sev doctor.Severity, details map[string]any) {
	d.mu.Lock()
	m := d.c.Doctor
	if m == nil {
		d.pending = append(d.pending, pendingAlert{code: code, details: details, sev: sev})
		d.mu.Unlock()
		return
	}
	d.mu.Unlock()
	var opts []doctor.RaiseOption
	if sev != "" {
		opts = append(opts, doctor.WithSeverity(sev))
	}
	if err := m.Raise(code, details, opts...); err != nil {
		d.log.Warn("could not raise an alert", "code", code, "error", err.Error())
	}
}

func (d *Daemon) clearAlert(code string) {
	d.mu.Lock()
	m := d.c.Doctor
	if m == nil {
		kept := d.pending[:0]
		for _, p := range d.pending {
			if p.code != code {
				kept = append(kept, p)
			}
		}
		d.pending = kept
		d.mu.Unlock()
		return
	}
	d.mu.Unlock()
	m.Clear(code)
}

// flushAlerts raises what was found before the alert manager existed.
func (d *Daemon) flushAlerts() {
	d.mu.Lock()
	pend := d.pending
	d.pending = nil
	m := d.c.Doctor
	d.mu.Unlock()
	for _, p := range pend {
		var opts []doctor.RaiseOption
		if p.sev != "" {
			opts = append(opts, doctor.WithSeverity(p.sev))
		}
		if err := m.Raise(p.code, p.details, opts...); err != nil {
			d.log.Warn("could not raise an alert", "code", p.code, "error", err.Error())
		}
	}
}

// record stores a step result.
func (d *Daemon) record(r StepResult) {
	d.mu.Lock()
	d.steps = append(d.steps, r)
	d.mu.Unlock()
}

// runStep runs one startup step under its budget, records it, and tells Env.OnStep. fn gets a
// context that ends at the budget (on the daemon's clock); a step that overruns is recorded as timed
// out and startup goes on. An error is logged and returned; whether it is fatal is the caller's
// decision.
func (d *Daemon) runStep(ctx context.Context, n int, name string, budget time.Duration, fn func(ctx context.Context) error) error {
	if d.env.OnStep != nil {
		d.env.OnStep(StepEvent{N: n, Name: name})
	}
	began := d.env.Clock.Now()
	sctx, cancel := withDeadline(ctx, d.env.Clock, budget)
	err := fn(sctx)
	over := timedOut(sctx)
	cancel()
	res := StepResult{N: n, Name: name, Duration: d.env.Clock.Now().Sub(began), Err: err, TimedOut: over}
	d.record(res)
	switch {
	case err != nil:
		d.log.Warn("startup step degraded", "step", n, "name", name, "error", err.Error(), "timed_out", over)
	case over:
		d.log.Warn("startup step ran out of time", "step", n, "name", name, "budget_ms", budget.Milliseconds())
	default:
		d.log.Debug("startup step done", "step", n, "name", name, "duration_ms", res.Duration.Milliseconds())
	}
	if d.env.OnStep != nil {
		d.env.OnStep(StepEvent{N: n, Name: name, End: true, Err: err})
	}
	return err
}

// kickDoctor asks for a doctor pass soon: the drill reporter calls it when a document changed what it
// reports, so an alert shows up at once and not at the next 60 s pass. It never blocks; requests that
// arrive while one is pending share it.
func (d *Daemon) kickDoctor() {
	select {
	case d.doctorKick <- struct{}{}:
	default:
	}
}

// auditSlot is the audit log as the subsystems that are built before it was opened see it: it
// forwards to the log once step 7 has opened it and drops the record before (nothing audits that
// early). The log is always usable once open, so a write never panics.
type auditSlot struct {
	mu  sync.Mutex
	log *audit.Log
}

func (a *auditSlot) set(l *audit.Log) {
	a.mu.Lock()
	a.log = l
	a.mu.Unlock()
}

// Write implements the audit writer interfaces of every package.
func (a *auditSlot) Write(ev audit.Event) error {
	a.mu.Lock()
	l := a.log
	a.mu.Unlock()
	if l == nil {
		return nil
	}
	return l.Write(ev)
}

// modeName is the current mode for /health, from memory.
func (d *Daemon) modeName() string {
	if m := d.c.Modes; m != nil {
		return string(m.Current())
	}
	if st := d.c.State; st != nil {
		return string(st.Snapshot().Mode)
	}
	return string(modes.Offline)
}
