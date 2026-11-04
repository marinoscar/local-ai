package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/api"
	"github.com/marinoscar/local-ai/agent/internal/audit"
	"github.com/marinoscar/local-ai/agent/internal/auth"
	"github.com/marinoscar/local-ai/agent/internal/catalog"
	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/events"
	"github.com/marinoscar/local-ai/agent/internal/hardware"
	"github.com/marinoscar/local-ai/agent/internal/logx"
	"github.com/marinoscar/local-ai/agent/internal/portal"
	"github.com/marinoscar/local-ai/agent/internal/router"
	"github.com/marinoscar/local-ai/agent/internal/state"
	"github.com/marinoscar/local-ai/agent/internal/statusapi"
	"github.com/marinoscar/local-ai/agent/internal/svc/piper"
	"github.com/marinoscar/local-ai/agent/internal/sync/run"
)

// Start runs the startup sequence of agent.md section 4.1 up to step 11 and returns. Steps 12 to 17 run
// in the background (12, 13, 15 and 16 concurrently, 14 after 12), because the listeners are already
// up: the API answers, survival is reachable and the mutating routes answer 503 LAI-AGT-024 until
// step 12 ends. Wait for Started to know step 17 has run.
//
// Every step degrades instead of aborting, except the two fatal ones: not root (step 1, LAI-AGT-011)
// and both listeners failing (step 10). A fatal error is a *FatalError; the caller exits 1.
func (d *Daemon) Start(ctx context.Context) error {
	d.began = d.env.Clock.Now()
	d.rootCtx, d.cancel = context.WithCancel(context.WithoutCancel(ctx))
	d.bootCtx, d.bootCancel = context.WithCancel(d.rootCtx)

	// 1
	d.ensureLogger()
	if d.env.OnStep != nil {
		d.env.OnStep(StepEvent{N: 1, Name: "root"})
	}
	if uid := d.env.Geteuid(); uid != 0 {
		err := fmt.Errorf("aiagent serve must run as root (effective uid is %d)", uid)
		d.log.Error("not running as root", "code", doctor.AGT011, "euid", uid)
		d.record(StepResult{N: 1, Name: "root", Err: err})
		if d.env.OnStep != nil {
			d.env.OnStep(StepEvent{N: 1, Name: "root", End: true, Err: err})
		}
		return &FatalError{Code: doctor.AGT011, Err: err}
	}
	d.record(StepResult{N: 1, Name: "root"})
	if d.env.OnStep != nil {
		d.env.OnStep(StepEvent{N: 1, Name: "root", End: true})
	}

	// 2
	_ = d.runStep(ctx, 2, "logging", 0, func(context.Context) error {
		d.log.Info("aiagent starting", "version", d.env.Version, "root", d.env.Root)
		return nil
	})
	d.resolveOwners()

	// 3 .. 8: the foundations. Each failure is an alert and startup goes on.
	_ = d.runStep(ctx, 3, "config", stepTimeoutConfig, d.stepConfig)
	_ = d.runStep(ctx, 4, "state", stepTimeoutState, d.stepState)
	_ = d.runStep(ctx, 5, "directories", stepTimeoutDirs, d.stepDirs)
	_ = d.runStep(ctx, 6, "secrets", stepTimeoutSecrets, d.stepSecrets)
	_ = d.runStep(ctx, 7, "audit", 0, d.stepAudit)
	_ = d.runStep(ctx, 8, "catalog", stepTimeoutCatalog, d.stepCatalog)

	// 9
	if err := d.runStep(ctx, 9, "subsystems", stepTimeoutSubsystem, d.stepSubsystems); err != nil && d.c.API == nil {
		// The server could not even be built: nothing can be served.
		return &FatalError{Code: doctor.AGT099, Err: err}
	}

	// 10
	var fatal error
	_ = d.runStep(ctx, 10, "listeners", stepTimeoutListeners, func(sctx context.Context) error {
		fatal = d.stepListeners(sctx)
		return fatal
	})
	if fatal != nil {
		return fatal
	}

	// 11
	_ = d.runStep(ctx, 11, "ready", 0, d.stepReady)

	d.bg.Add(1)
	go func() {
		defer d.bg.Done()
		d.background()
	}()
	return nil
}

// resolveOwners looks up the localai user once. An unprivileged run, and a machine without the user,
// leave every file with the agent's own owner.
func (d *Daemon) resolveOwners() {
	if d.env.Unprivileged {
		return
	}
	uid, gid, ok := d.env.LookupLocalAI()
	if !ok {
		d.log.Warn("the localai user does not exist; files keep the agent's owner")
		return
	}
	d.haveLocalAI, d.localaiUID, d.localaiGID = true, uid, gid
	d.owner = &state.Owner{UID: 0, GID: gid} // state.json is root:localai
}

// ---- step 3: node.yaml --------------------------------------------------------------------------

func (d *Daemon) stepConfig(context.Context) error {
	env := d.env
	hooks := config.Hooks{
		ProfileExists: func(id string) bool {
			if d.c.Catalog == nil {
				return true
			}
			cat := d.c.Catalog.Get().Catalog
			if len(cat.Profiles) == 0 {
				return true // an empty catalog must not invalidate node.yaml
			}
			_, ok := cat.Profile(id)
			return ok
		},
		CatalogModelIDs: func() []string {
			if d.c.Catalog == nil {
				return nil
			}
			var ids []string
			for _, m := range d.c.Catalog.Get().Catalog.Models {
				ids = append(ids, m.ID)
			}
			return ids
		},
		DetectBackend: hardware.DetectBackend,
		// The owners of a node.yaml block validate it (the store refuses a file one of them rejects).
		Blocks: map[string]config.BlockValidator{"sync": run.SettingsValidator{}, "piper": piper.BlockValidator{},
			"portal": portal.BlockValidator{}},
	}
	store := config.Open(config.Options{Root: env.Root, Clock: env.Clock, Hooks: hooks})
	d.c.Config = store
	cfg := store.Get()
	if !cfg.SafeDefaults && cfg.Logging.Level != "" {
		if err := logx.SetLevel(d.level, cfg.Logging.Level); err != nil {
			d.log.Warn("logging.level is not valid", "level", cfg.Logging.Level)
		}
	}
	if st := store.Status(); st.Code != "" {
		d.log.Warn("node.yaml is not healthy; starting on "+safeWord(cfg.SafeDefaults), "code", st.Code, "safe_defaults", cfg.SafeDefaults)
	}
	return nil
}

func safeWord(safe bool) string {
	if safe {
		return "the safe defaults"
	}
	return "the last valid configuration"
}

// ---- step 4: state.json -------------------------------------------------------------------------

func (d *Daemon) stepState(context.Context) error {
	opts := state.Options{FS: d.env.StateFS, AgentVersion: d.env.Version, Now: d.env.Clock.Now, Logger: d.log, Owner: d.owner}
	st, err := state.Open(d.c.Paths.State(), opts)
	if err != nil {
		return err
	}
	d.c.State = st
	d.stoppers.state = st
	for _, f := range st.Findings() {
		d.log.Warn("the state store degraded", "kind", string(f.Kind), "severity", string(f.Severity), "message", f.Message)
	}

	// Restart-loop detection (LAI-AGT-014): the start time goes into state.json right after the open.
	now := d.env.Clock.Now().UTC()
	var starts []time.Time
	if err := st.Mutate(func(s *state.State) error {
		s.Agent.RecentStarts = append(s.Agent.RecentStarts, now)
		if n := len(s.Agent.RecentStarts); n > recentStartsKept {
			s.Agent.RecentStarts = append([]time.Time(nil), s.Agent.RecentStarts[n-recentStartsKept:]...)
		}
		starts = append([]time.Time(nil), s.Agent.RecentStarts...)
		return nil
	}); err != nil {
		return err
	}
	if n := startsWithin(starts, now, restartWindow); n > restartLimit {
		d.log.Warn("the agent is restarting repeatedly", "code", doctor.AGT014, "starts_last_hour", n)
		d.raise(doctor.AGT014, map[string]any{"starts_last_hour": n})
	} else {
		d.clearAlert(doctor.AGT014)
	}
	return nil
}

// The restart-loop rule of agent.md section 4.1: the newest 10 starts are kept, and more than 4 within
// the last 60 minutes (more than 3 restarts in the last hour) raises LAI-AGT-014.
const (
	recentStartsKept = 10
	restartLimit     = 4
	restartWindow    = time.Hour
)

// startsWithin counts the starts in (now-window, now].
func startsWithin(starts []time.Time, now time.Time, window time.Duration) int {
	n := 0
	for _, s := range starts {
		if age := now.Sub(s); age >= 0 && age < window {
			n++
		}
	}
	return n
}

// ---- step 5: directories ------------------------------------------------------------------------

func (d *Daemon) stepDirs(context.Context) error {
	var errs []error
	for _, dir := range d.c.Paths.DataDirs() {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", dir, err))
			continue
		}
		if d.haveLocalAI && !d.env.Unprivileged {
			_ = os.Chown(dir, 0, d.localaiGID)
		}
		_ = os.Chmod(dir, 0o750)
	}
	// /run/localai is the systemd RuntimeDirectory; outside systemd it is created here.
	if err := os.MkdirAll(d.c.Paths.Run(), 0o755); err != nil {
		errs = append(errs, fmt.Errorf("%s: %w", d.c.Paths.Run(), err))
	}
	if err := errors.Join(errs...); err != nil {
		d.log.Error("a directory could not be created", "code", doctor.AGT004, "error", err.Error())
		d.raise(doctor.AGT004, map[string]any{"error": err.Error()})
		return err
	}
	return nil
}

// ---- step 6: the token and the secret files -----------------------------------------------------

func (d *Daemon) stepSecrets(context.Context) error {
	p := d.c.Paths
	acfg := auth.Config{
		TokenPath: p.AgentToken(), PINPath: p.AdminPIN(), Groups: d.env.Groups, Audit: d.audit, Alert: authAlert(d), Logger: d.log,
	}
	if d.env.Unprivileged {
		tp, pp := config.SecretPolicy{Mode: 0o640, UID: -1, GID: -1}, config.SecretPolicy{Mode: 0o600, UID: -1, GID: -1}
		acfg.TokenPolicy, acfg.PINPolicy = &tp, &pp
	}
	d.c.Auth = auth.New(acfg)

	var fixed []string
	check := func(path string, pol config.SecretPolicy) {
		if d.env.Unprivileged {
			pol.UID, pol.GID = -1, -1
		}
		issues, err := config.EnforceSecret(config.OSFS{}, path, pol)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			d.log.Warn("a secret file could not be checked", "path", path, "error", err.Error())
		}
		for _, is := range issues {
			fixed = append(fixed, is.Path+": "+is.Message)
		}
	}
	check(p.APIKey(), config.PolicyAPIKey)
	check(p.CloudCredential(), config.PolicyCloudCredential)
	check(p.AdminPIN(), config.PolicyAdminPIN)

	// The agent token: its permissions are checked before Ensure fixes them, so the alert is raised.
	tokenPolicy := config.PolicyAgentToken
	if d.env.Unprivileged {
		tokenPolicy = config.SecretPolicy{Mode: 0o640, UID: -1, GID: -1}
	}
	if issues, err := config.CheckSecret(config.OSFS{}, p.AgentToken(), tokenPolicy); err == nil {
		for _, is := range issues {
			fixed = append(fixed, is.Path+": "+is.Message)
		}
	}
	created, err := d.c.Auth.EnsureToken()
	if err != nil {
		d.log.Error("the agent token is not usable", "error", err.Error())
		return err
	}
	if created {
		d.raise(auth.CodeTokenCreated, nil)
	}
	if len(fixed) > 0 {
		d.log.Warn("secret files had unsafe permissions and were fixed", "code", auth.CodeSecretPermissions, "issues", fixed)
		d.raise(auth.CodeSecretPermissions, map[string]any{"issues": fixed})
	}
	return nil
}

// ---- step 7: the audit log ----------------------------------------------------------------------

func (d *Daemon) stepAudit(context.Context) error {
	l, err := audit.Open(d.c.Paths.Audit(), audit.WithLogger(d.log), audit.WithClock(d.env.Clock.Now))
	d.c.Audit = l // always usable: a failed open is a log that writes to the journal only
	d.audit.set(l)
	d.stoppers.audit = l
	if err != nil {
		d.log.Warn("the audit log is degraded; records go to the journal only", "code", doctor.AGT013, "error", err.Error())
	}
	return nil // the agt.audit_writable check reports LAI-AGT-013
}

// ---- step 8: the catalog ------------------------------------------------------------------------

func (d *Daemon) stepCatalog(context.Context) error {
	f := &catalog.Fetcher{Dir: d.c.Paths.Catalog(), KitDirs: d.kitDirs()}
	loaded, err := f.Startup()
	d.c.Catalog = statusapi.NewCatalogStore(loaded, statusapi.InstalledAt(loaded.Dir))
	if err != nil {
		d.log.Warn("the catalog in /etc/localai/catalog could not be used", "code", doctor.CAT001, "source", string(loaded.Source), "error", err.Error())
		d.raise(doctor.CAT001, map[string]any{"error": err.Error(), "source": string(loaded.Source)})
	}
	// node.yaml is checked against the catalog now that there is one.
	d.c.Config.ReloadNow()
	return nil
}

// ---- step 9: the subsystems ---------------------------------------------------------------------

func (d *Daemon) stepSubsystems(ctx context.Context) error {
	if err := d.build(ctx); err != nil {
		return err
	}
	c := d.c
	// The event bus has no goroutine. The hardware sampler takes its first sample synchronously, then
	// samples on its timer; the service watcher and the config reload poll start.
	if c.HardwareService != nil {
		c.HardwareService.Prime(ctx)
		d.startWorker(hardwareWorker{c.HardwareService})
	}
	d.startWorker(c.Config)
	d.startWorker(c.Watcher)
	// The kiwix unit is rewritten at every start (like the whisper unit), so an agent upgrade that changes
	// the unit text reaches the disk without a node.yaml edit. The writer never starts or stops the unit.
	if cfg := d.c.Config.Get(); blockEnabled(cfg, "kiwix") && d.c.Systemd != nil {
		d.writeKiwixUnit(ctx, kiwixThreads(cfg))
	}
	if d.ark != nil {
		d.startWorker(d.ark.driveWorker()) // a drive that goes away stops the transfers writing to it
		d.ark.startDrives()                // after it: the first pass publishes drive.mounted for a drive already plugged in
		if d.ark.index != nil && d.ark.roots != nil {
			d.startWorker(d.ark.kiwixLibraryWorker()) // the ark stack is up: build the Kiwix libraries now and on every change
		}
	}
	// The drill reporter reads the kept documents (and stamps drill.watch_since on a node that never
	// ran a drill), so the drill alerts are derived by the first pass below.
	if err := d.reporter.Load(); err != nil {
		d.log.Warn("the drill results could not be read", "error", err.Error())
	}
	// The first doctor pass runs now, so a degraded start (missing node.yaml, a damaged state file)
	// is an alert from the moment the API answers; the periodic loop starts at step 16.
	c.Doctor.Tick(ctx)
	return nil
}

// startWorker starts w under the long-lived context and remembers it for shutdown step 2.
func (d *Daemon) startWorker(w Worker) {
	if err := w.Start(d.rootCtx); err != nil {
		d.log.Error("worker failed to start", "worker", w.Name(), "error", err.Error())
		return
	}
	d.mu.Lock()
	d.periodic = append(d.periodic, w)
	d.mu.Unlock()
	d.log.Debug("worker started", "worker", w.Name())
}

// ---- step 10: the listeners ---------------------------------------------------------------------

func (d *Daemon) stepListeners(context.Context) error {
	err := d.c.API.Start(d.rootCtx)
	tcp, sock := d.c.API.BindErrors()
	if tcp != nil {
		d.log.Error("the TCP listener failed", "code", api.CodeTCPBind, "error", tcp.Err.Error())
		d.raise(api.CodeTCPBind, map[string]any{"error": tcp.Err.Error(), "addr": tcp.Addr})
	}
	if sock != nil {
		d.log.Error("the socket listener failed", "code", api.CodeSocketBind, "error", sock.Err.Error())
		d.raise(api.CodeSocketBind, map[string]any{"error": sock.Err.Error(), "addr": sock.Addr})
	}
	if err != nil {
		code := api.CodeTCPBind
		var le *api.ListenError
		if errors.As(err, &le) {
			code = api.CodeSocketBind
		}
		return &FatalError{Code: code, Err: err}
	}
	return nil
}

// ---- step 11: READY=1 and the watchdog ----------------------------------------------------------

func (d *Daemon) stepReady(context.Context) error {
	if err := d.env.Notifier.Ready(); err != nil {
		d.log.Warn("sd_notify READY failed", "error", err.Error())
	}
	_ = d.env.Notifier.Status("starting")
	wd := NewWatchdog(d.env.Notifier, d.env.Clock, WatchdogInterval(d.env.Getenv(EnvWatchdogUsec)), d.log,
		WatchProbe{Name: "http", Run: d.healthOverTransport},
		WatchProbe{Name: "state", Run: d.stateDrains},
	)
	d.c.Watchdog = wd
	if d.env.Notifier.Enabled() {
		d.startWorker(wd)
	}
	return nil
}

// stateDrains is the watchdog's state-writer probe: Flush returns once the writer has written what
// is pending. A write that fails (a full disk) returns an error and is not a hung loop (the state
// store degrades to memory and raises LAI-AGT-004), so only a Flush that does not return fails the
// probe.
func (d *Daemon) stateDrains(ctx context.Context) error {
	done := make(chan struct{})
	go func() {
		_ = d.c.State.Flush()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// ---- steps 12 to 17 -----------------------------------------------------------------------------

// background runs steps 12 to 17: 12, 13, 15 and 16 concurrently, 14 once 12 has ended (agent.md
// section 4.1), then step 17. It returns when every step goroutine has ended, so Shutdown (which waits
// for it before it stops the orchestrator) never races a restore that is still loading a model.
func (d *Daemon) background() {
	ctx := d.bootCtx
	c := d.c
	var wg sync.WaitGroup
	defer wg.Wait()
	spawn := func(f func()) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			f()
		}()
	}
	s15, s16 := make(chan struct{}), make(chan struct{})

	// 12: reconcile llama; when it ends, success or failure, the gate opens and the mode engine
	// starts accepting (a queued survival request starts now).
	spawn(func() {
		_ = d.runStep(ctx, 12, "reconcile_llama", stepTimeoutReconcile, func(sctx context.Context) error {
			_, err := c.Reconciler.Reconcile(sctx)
			return err
		})
		c.API.SetMutationsOpen(true)
		if _, err := c.Modes.Open(); err != nil {
			d.log.Warn("the mode engine did not open", "error", err.Error())
		}
		close(d.gateCh)
		d.startWorker(c.RouterWatcher)
	})

	// 13: resume the mode transition, or apply the policy of the committed mode.
	spawn(func() {
		_ = d.runStep(ctx, 13, "resume_mode", 0, func(sctx context.Context) error {
			_, err := c.Modes.Resume(sctx)
			return err
		})
	})

	// 14: restore the models once the reconcile has ended; then, once, the drill's recovery.
	spawn(func() {
		select {
		case <-d.gateCh:
		case <-ctx.Done():
			return
		}
		_ = d.runStep(ctx, 14, "restore_models", 0, func(sctx context.Context) error {
			// A node.yaml edited while the agent was down may have changed llama.mode: bring the running
			// llama to it first (a no-op when it already runs in that mode).
			if _, err := c.Orchestrator.ApplyMode(sctx, router.ModeRequest{LockWait: router.DefaultLockWait, Boot: true}); err != nil {
				d.log.Warn("the AI service was not switched to llama.mode at start", "error", err.Error())
			}
			_, err := c.Orchestrator.RestoreModels(sctx)
			return err
		})
		if err := c.Drill.Recover(ctx); err != nil {
			d.log.Warn("the drill recovery failed", "error", err.Error())
		}
	})

	// 15 and 16 do not wait for the others; 17 waits for 12, 15 and 16.
	spawn(func() {
		defer close(s15)
		_ = d.runStep(ctx, 15, "pending_update", stepTimeoutUpdate, d.stepUpdate)
	})
	spawn(func() {
		defer close(s16)
		_ = d.runStep(ctx, 16, "periodic_checks", 0, d.stepPeriodic)
	})

	<-s15
	<-s16
	select {
	case <-d.gateCh:
	case <-ctx.Done():
		return
	}
	d.step17()
}

// stepUpdate is step 15: the post-update health check (internal/update) and the registered update
// hooks. With no update in progress and no hook it does nothing.
func (d *Daemon) stepUpdate(ctx context.Context) error {
	var errs []error
	// The post-update health check of the new process, then the settle of the cloud command that
	// asked for the update. With no update in progress it returns at once.
	if d.updater != nil && d.hooks.agentUpd == nil {
		if err := d.updater.PostStart(ctx); err != nil {
			d.log.Error("the post-update check failed", "error", err.Error())
			errs = append(errs, fmt.Errorf("agent.update: %w", err))
		}
	}
	for _, h := range d.hooks.update {
		if err := h.fn(ctx, d.c); err != nil {
			d.log.Error("an update hook failed", "hook", h.name, "error", err.Error())
			errs = append(errs, fmt.Errorf("%s: %w", h.name, err))
		}
	}
	return errors.Join(errs...)
}

// stepPeriodic is step 16: the doctor's periodic checks, the connectivity prober and Tailscale
// reconciler, the kiwix unit watcher, the health supervisor, the internal-root monitor, the drill's
// retry timer, the registered workers and the post-start hooks. The prober itself is started by the
// mode engine's policy (step 13), which knows whether the node is online.
func (d *Daemon) stepPeriodic(ctx context.Context) error {
	c := d.c
	d.startWorker(&doctorLoop{d: d}) // not doctor.Manager.Start: every pass is serialized (doctorloop.go)
	if d.ark != nil {
		d.startWorker(d.ark.ready) // READY.json now and every 6 hours
	}
	d.startWorker(c.Tailscale)
	d.startWorker(c.Health)
	d.bg.Add(1)
	go func() {
		defer d.bg.Done()
		c.ArkMonitor.Run(d.rootCtx)
	}()
	d.bg.Add(1)
	go func() { // StartRetries is the retry loop itself: it returns when its context ends
		defer d.bg.Done()
		c.Drill.StartRetries(d.rootCtx, time.Minute)
	}()
	d.startWhisper(ctx)
	d.watchConfig(d.rootCtx)
	for _, w := range d.hooks.workers {
		wk, err := w.build(c)
		if err != nil {
			d.log.Error("a worker could not be built", "worker", w.name, "error", err.Error())
			continue
		}
		if wk != nil {
			d.startWorker(wk)
		}
	}
	var errs []error
	for _, h := range d.hooks.postStart {
		if err := h.fn(ctx, c); err != nil {
			d.log.Error("a post-start hook failed", "hook", h.name, "error", err.Error())
			errs = append(errs, fmt.Errorf("%s: %w", h.name, err))
		}
	}
	return errors.Join(errs...)
}

// step17 clears the starting flag, tells the SSE clients to re-read, and logs agent.started.
func (d *Daemon) step17() {
	if d.env.OnStep != nil {
		d.env.OnStep(StepEvent{N: 17, Name: "started"})
	}
	c := d.c
	c.API.SetStarting(false)
	if err := c.Bus.Resync(events.ResyncRestart); err != nil {
		d.log.Warn("resync event refused", "error", err.Error())
	}
	_ = d.env.Notifier.Status("serving")
	dur := d.env.Clock.Now().Sub(d.began)
	steps := map[string]any{}
	for _, s := range d.Steps() {
		steps[fmt.Sprintf("%02d_%s", s.N, s.Name)] = s.Duration.Milliseconds()
	}
	d.log.Info("agent.started", "version", d.env.Version, "mode", d.modeName(), "node", c.Config.Get().Node.ID,
		"duration_ms", dur.Milliseconds(), "steps_ms", steps)
	d.started.Store(true)
	close(d.startedCh)
	if d.env.OnStep != nil {
		d.env.OnStep(StepEvent{N: 17, Name: "started", End: true})
	}
}
