package app

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/api"
	"github.com/marinoscar/local-ai/agent/internal/ark"
	"github.com/marinoscar/local-ai/agent/internal/auth"
	"github.com/marinoscar/local-ai/agent/internal/catalog"
	"github.com/marinoscar/local-ai/agent/internal/cloud"
	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/drill"
	"github.com/marinoscar/local-ai/agent/internal/events"
	"github.com/marinoscar/local-ai/agent/internal/hardware"
	"github.com/marinoscar/local-ai/agent/internal/modelsapi"
	"github.com/marinoscar/local-ai/agent/internal/modes"
	"github.com/marinoscar/local-ai/agent/internal/portal"
	"github.com/marinoscar/local-ai/agent/internal/router"
	"github.com/marinoscar/local-ai/agent/internal/services"
	"github.com/marinoscar/local-ai/agent/internal/state"
	"github.com/marinoscar/local-ai/agent/internal/statusapi"
	"github.com/marinoscar/local-ai/agent/internal/svc/health"
	"github.com/marinoscar/local-ai/agent/internal/svc/kiwix"
)

// modeView is the statusapi.ModeView, drill.ModeView and router mode source that exists before the
// mode engine does: the engine is built after the link and the status builder, which both need a
// mode view. Until the engine is set it answers from the state store.
type modeView struct {
	st *state.Store
	e  atomic.Pointer[modes.Engine]
}

// Info implements statusapi.ModeView and drill.ModeView.
func (v *modeView) Info() modes.Info {
	if e := v.e.Load(); e != nil {
		return e.Info()
	}
	m := v.st.Snapshot().Mode
	return modes.Info{Mode: m, Effective: m, Connectivity: state.ConnNotChecked}
}

func (v *modeView) online() bool {
	if e := v.e.Load(); e != nil {
		return e.Online()
	}
	return false
}

// build constructs every subsystem and the HTTP server, in dependency order. It starts nothing: a
// constructor does no I/O beyond reading what it needs, and the workers start in steps 9 to 16. The
// constructors cannot fail for any reason an operator can cause (they return an error only for a
// missing required dependency), so a build error is a programming error and is fatal to the step.
func (d *Daemon) build(ctx context.Context) error {
	c, env, clock, log := d.c, d.env, d.env.Clock, d.log
	cfg := c.Config
	fs := config.OSFS{}
	node := cfg.Get().Node.ID

	// The event bus and the alert manager come first: everything else publishes and alerts.
	c.Bus = events.New(events.Options{Node: node, Now: clock.Now})
	c.Checks = doctor.NewRegistry()
	c.Doctor = doctor.NewManager(doctor.ManagerOptions{Registry: c.Checks, Bus: c.Bus, Now: clock.Now, Logger: log})
	d.flushAlerts()
	alerts := router.ManagerAlerts(c.Doctor)
	d.stoppers.bus = c.Bus

	// Systemd: the D-Bus Manager with the systemctl fallback, behind the unit allowlist.
	base := env.Systemd
	if base == nil {
		dial := env.DialBus
		if dial == nil {
			dial = services.DialSystemBus
		}
		auto := services.NewAuto(ctx, dial, env.Runner, services.WithLogger(log), services.WithClock(clock))
		base = auto
		d.closers = append(d.closers, auto)
	}
	c.Systemd = services.NewGuard(base)
	c.Controller = &services.Controller{Manager: c.Systemd}

	// Hardware.
	if env.Hardware != nil {
		c.Hardware = env.Hardware
	} else {
		svc := hardware.New(hardware.Options{
			Exec:   env.Runner,
			Now:    clock.Now,
			Logger: log,
			DriveRoots: func() []string {
				return d.roots().Drives
			},
			ProfileStorage: func() (int64, bool) {
				if p := d.profile(); p != nil {
					return p.Storage.MinFreeBytes, true
				}
				return 0, false
			},
			ConfiguredBackend: func() string { return cfg.Get().Llama.Backend },
			OnPowerChange: func(ev hardware.PowerEvent) {
				_ = c.Bus.Publish(events.TypeHardwarePower, "", events.HardwarePower{Source: ev.Source, BatteryPercent: ev.BatteryPercent, Charging: ev.Charging})
			},
		})
		c.Hardware, c.HardwareService = svc, svc
	}

	// Doctor checks of the agent core, the network, the hardware and the companion services.
	if err := doctor.RegisterAgentChecks(c.Checks, doctor.AgentSources{Config: c.Config, State: c.State, Audit: c.Audit}); err != nil {
		return err
	}
	if c.HardwareService != nil {
		for _, ch := range hardwareChecks(c.HardwareService) {
			if err := c.Checks.Add(ch); err != nil {
				return err
			}
		}
	}

	// Service watcher (llama) and the health supervisor (kiwix, whisper).
	ports := func() services.Ports {
		p := cfg.Get().Ports
		return services.Ports{Llama: p.Llama, Kiwix: p.Kiwix, Whisper: p.Whisper}
	}
	prober := env.Prober
	if prober == nil {
		prober = services.NewHTTPProber()
	}
	c.Watcher = services.NewWatcher(services.WatcherOptions{
		Manager: c.Systemd, Prober: prober, Ports: ports(), Publisher: c.Bus, Alerts: alerts,
		Clock: clock, Logger: log, Only: []string{"llama"},
	})
	metrics := health.NewMetrics()
	sup, err := health.New(health.Options{
		Manager: c.Systemd, Publisher: c.Bus, Mode: d.modeName, Metrics: metrics, Clock: clock, Logger: log,
		Specs: []health.Spec{
			health.KiwixSpec(prober, ports(), func() bool { return blockEnabled(cfg.Get(), "kiwix") }),
			health.WhisperSpec(prober, ports(), func() bool { return blockEnabled(cfg.Get(), "whisper") }),
		},
		RequireServices: func() []string {
			if p := d.profile(); p != nil {
				return p.Survival.RequireServices
			}
			return nil
		},
	})
	if err != nil {
		return fmt.Errorf("health supervisor: %w", err)
	}
	c.Health = sup
	if err := sup.RegisterDoctor(c.Checks); err != nil {
		return err
	}
	c.Kiwix = kiwix.NewInstaller(kiwix.Options{Runner: env.Runner, Logger: log, Root: c.Paths.Join(optDir + "/kiwix-tools")})
	if err := c.Checks.Add(c.Kiwix.DoctorCheck(func() bool { return blockEnabled(cfg.Get(), "kiwix") })); err != nil {
		return err
	}

	// The llama router: client, watcher, the one shared lock, generators, orchestrator, reconciler.
	c.Router, err = router.NewClient(router.Options{Port: cfg.Get().Ports.Llama, Keys: router.NewFileKey(fs, c.Paths.APIKey()), Logger: log})
	if err != nil {
		return fmt.Errorf("router client: %w", err)
	}
	c.RouterWatcher = router.NewWatcher(router.WatcherOptions{
		Client: c.Router, Publisher: c.Bus, Alerts: alerts, Clock: clock, Logger: log,
		ModelsMax: func() int {
			if p := d.profile(); p != nil {
				return p.Router.ModelsMax
			}
			return 0
		},
	})
	c.OpLock = router.NewOpLock(router.NewOpID, clock.Now)
	loader := &lateLoader{}
	iniOwner, dropOwner, unitOwner, cacheOwner, ident := d.fileOwners()
	c.Generator, err = router.NewGenerator(router.GeneratorOptions{
		Paths: router.DefaultPaths(env.Root), FS: fs, IniState: router.StateIni{Store: c.State}, Router: c.Router,
		Systemd: c.Systemd, Alerts: alerts, Publisher: c.Bus, Suppress: c.RouterWatcher.Suppress, Ops: c.OpLock,
		Loader: loader, Clock: clock, Version: env.Version, Logger: log, LocalAI: ident,
		INIOwner: iniOwner, DropInOwner: dropOwner,
	})
	if err != nil {
		return fmt.Errorf("models.ini generator: %w", err)
	}
	c.UnitGenerator, err = router.NewUnitGenerator(router.UnitGeneratorOptions{
		Paths: router.DefaultUnitPaths(env.Root), FS: fs, State: router.StateUnit{Store: c.State}, Systemd: c.Systemd,
		Alerts: alerts, Clock: clock, Logger: log, LocalAI: ident, UnitOwner: unitOwner, CacheOwner: cacheOwner,
	})
	if err != nil {
		return fmt.Errorf("unit generator: %w", err)
	}
	c.Orchestrator, err = router.NewOrchestrator(router.OrchestratorOptions{
		Router: c.Router, State: c.State, Inputs: d.routerInputs, Presence: router.ManifestPresence{FS: fs},
		Lock: c.OpLock, Generator: c.Generator, Publisher: c.Bus, Audit: d.audit, Alerts: alerts,
		Suppress: c.RouterWatcher.Suppress, Progress: c.RouterWatcher, Restarter: llamaUnit{c.Systemd},
		Builds: router.FileBuild{FS: fs, Path: c.Paths.BuildJSON()}, Timing: env.RouterTiming, Clock: clock, Logger: log,
	})
	if err != nil {
		return fmt.Errorf("orchestrator: %w", err)
	}
	loader.o.Store(c.Orchestrator)
	c.Reconciler, err = router.NewReconciler(router.ReconcileOptions{
		Units: c.UnitGenerator, Models: c.Generator, Inputs: d.reconcileInputs, Router: c.Router,
		Unit: llamaUnit{c.Systemd}, Logger: log,
	})
	if err != nil {
		return fmt.Errorf("reconciler: %w", err)
	}

	// Tailscale and the connectivity prober, over the engine's Online (late bound).
	mv := &modeView{st: c.State}
	d.mode = mv
	c.Tailscale, err = modes.NewTailscaleManager(modes.TailscaleOptions{
		Run: env.Runner, Settings: modes.SettingsFromConfig(cfg.Get), State: c.State, Online: mv.online,
		Clock: clock, HTTP: env.ModesHTTP, Logger: log,
	})
	if err != nil {
		return fmt.Errorf("tailscale manager: %w", err)
	}
	c.Prober, err = modes.NewProber(modes.ProberOptions{
		State: c.State, Settings: modes.ProberSettingsFromConfig(cfg.Get), Online: mv.online, HTTP: env.ModesHTTP,
		Tailnet: c.Tailscale, Clock: clock, Logger: log,
	})
	if err != nil {
		return fmt.Errorf("connectivity prober: %w", err)
	}
	if err := doctor.RegisterNetChecks(c.Checks, doctor.NetSources{Serve: c.Tailscale, Reachable: c.Tailscale, Connectivity: c.Prober}); err != nil {
		return err
	}

	// The model views, the status builder, the counters.
	merger := &statusapi.ModelMerger{
		Config: cfg, Catalog: c.Catalog, State: c.State, Mode: mv, Hardware: c.Hardware, Router: c.RouterWatcher,
		Roots: d.roots, Presence: router.ManifestPresence{FS: fs},
	}
	svcView := health.Combine(c.Watcher, sup)
	gauge := &gauges{}
	d.gauges = gauge
	sdeps := statusapi.Deps{
		Config: cfg, Catalog: c.Catalog, State: c.State, Hardware: c.Hardware, Mode: mv, Services: svcView, Models: merger,
		Cloud: d.views, Tailscale: c.Tailscale, Alerts: c.Doctor, Counters: counterView{d: d, health: metrics, gauges: gauge},
		CheckedAt: c.Prober.LastCheckedAt, Version: env.Version, StartedAt: d.began, Refresher: refresher{d},
		Regenerator: statusapi.RouterRegenerator{Generator: c.Generator, Inputs: func(context.Context) (router.Inputs, error) { return d.routerInputs(), nil }},
		Audit:       d.audit, Now: clock.Now, Logger: log,
	}
	d.sdeps = sdeps
	d.merger = merger

	// The cloud link. The claim plane (#36) and the snapshot sources arrive through the hooks.
	var snap cloud.SnapshotOptions
	if f := d.hooks.snapshot; f != nil {
		s := f(c)
		snap.Ark, snap.Sync, snap.Drill = s.Ark, s.Sync, s.Drill
	}
	snap.Models, snap.Catalog = merger, c.Catalog
	c.Cloud, err = cloud.New(cloud.Deps{
		Config: cfg, State: c.State, Credential: cloud.CredentialStore{Path: c.Paths.CloudCredential(), FS: fs},
		LinkFile: cloud.LinkStore{Path: c.Paths.CloudLink(), FS: fs}, Status: sdeps.Builder(), Snapshot: snap,
		Hardware: c.Hardware, Keys: cloud.FileKeySource{Path: c.Paths.APIKey(), FS: fs, State: c.State},
		Claim: d.claim, Events: c.Bus, Bus: c.Bus, Alerts: c.Doctor, Inflight: d.claim.Inflight, Online: mv.online,
		Clock: env.CloudClock, HTTP: env.CloudHTTP, Rand: env.CloudRand, Version: env.Version, StartedAt: d.began, Logger: log,
	})
	if err != nil {
		return fmt.Errorf("cloud link: %w", err)
	}
	d.views.cloud.Store(c.Cloud)
	if f := d.hooks.claim; f != nil {
		d.claim.set(f(c))
	}

	// The sync engine behind the model routes: the extension point, or the documented stub.
	var syncEngine modelsapi.SyncEngine = noSyncEngine{}
	if f := d.hooks.sync; f != nil {
		if e := f(c); e != nil {
			syncEngine = e
		}
	}
	d.syncEngine = syncEngine

	// The mode engine.
	var link modes.LinkController
	if c.Cloud != nil {
		link = c.Cloud
	}
	mopts := env.ModeOptions
	if mopts.Clock == nil {
		mopts.Clock = clock
	}
	if mopts.Logger == nil {
		mopts.Logger = log
	}
	c.Modes, err = modes.New(modes.Deps{
		State: c.State, Events: c.Bus, Audit: d.audit, Link: link, Prober: c.Prober, Tailscale: c.Tailscale,
		Models: c.Orchestrator, Services: c.Controller,
		SurvivalServices: func() []string {
			if p := d.profile(); p != nil {
				return p.Survival.RequireServices
			}
			return nil
		},
	}, mopts)
	if err != nil {
		return fmt.Errorf("mode engine: %w", err)
	}
	mv.e.Store(c.Modes)

	// The drill framework and the internal-root monitor.
	c.Drill, err = drill.New(drill.Deps{
		Registry: drill.NewRegistry(), Results: drill.NewResults(drill.ResultsOptions{Dir: c.Paths.DrillDir(), Owner: d.owner}),
		State: c.State, Publisher: c.Bus, Mode: mv, Llama: llamaActivity{lock: c.OpLock, view: c.RouterWatcher},
		Power: powerSource{h: c.Hardware}, Metrics: gauge, NodeID: node, AgentVersion: env.Version,
		CatalogVersion: func() string { return c.Catalog.Get().Catalog.Version }, Clock: clock, Logger: log,
	})
	if err != nil {
		return fmt.Errorf("drill engine: %w", err)
	}
	var arkOwner *ark.Owner
	if d.haveLocalAI && !env.Unprivileged {
		arkOwner = &ark.Owner{UID: d.localaiUID, GID: d.localaiGID}
	}
	c.ArkMonitor = ark.NewMonitor(ark.OSFS{}, ark.InternalOptions{
		Path: c.Paths.InternalRoot(), Profile: cfg.Get().Profile, NodeID: node, AgentVersion: env.Version,
		Owner: arkOwner, Now: clock.Now, Logger: log,
	}, clock, d.arkNotify)

	// The routes and the server.
	if err := d.buildServer(); err != nil {
		return err
	}

	// What shutdown stops.
	d.stoppers.link = c.Cloud
	d.stoppers.engine = c.Modes
	d.stoppers.drill = c.Drill
	d.stoppers.orch = c.Orchestrator
	d.stoppers.prober = c.Prober
	if d.hooksSync != nil {
		d.stoppers.syncer = d.hooksSync
	}
	return nil
}

// buildServer registers every route and constructs the HTTP server (step 10 starts it).
func (d *Daemon) buildServer() error {
	c, env := d.c, d.env
	reg := api.NewRegistry()
	c.Routes = reg
	authSvc := c.Auth

	regs := []struct {
		name string
		add  func() error
	}{
		{"auth", func() error { return auth.Register(reg, authSvc) }},
		{"events", func() error {
			return api.RegisterEvents(reg, api.EventsOptions{Bus: c.Bus, Stopping: d.eventsStopping, Clock: env.Clock, Logger: d.log})
		}},
		{"services", func() error {
			return api.RegisterServices(reg, api.ServicesDeps{
				Status: health.Combine(c.Watcher, c.Health), Actions: c.Controller, Health: c.Health,
				Llama: router.NewLlamaHook(c.Orchestrator), Mode: d.modeName, Audit: d.audit, Logger: d.log,
			})
		}},
		{"logs", func() error {
			return api.RegisterLogs(reg, api.LogsDeps{Journal: services.NewJournal(env.Runner, "")})
		}},
		{"doctor", func() error {
			return api.RegisterDoctor(reg, api.DoctorOptions{Registry: c.Checks, Now: env.Clock.Now})
		}},
		{"hardware", func() error { return hardware.Register(reg, c.Hardware, hardware.HandlerOptions{Now: env.Clock.Now}) }},
		{"status", func() error { return statusapi.Register(reg, d.sdeps) }},
		{"portal", func() error {
			return portal.Register(reg, portal.RouteOptions{
				Builder: portal.NewMinimal(portal.MinimalSources{
					Config: c.Config, State: c.State, Mode: d.mode, Hardware: c.Hardware, Router: c.RouterWatcher,
					Catalog: c.Catalog, Alerts: c.Doctor,
					Settings: func() portal.Settings { return portal.SettingsFrom(c.Config.Get(), c.Catalog.Get().Catalog) },
				}),
				Settings: func() portal.Settings { return portal.SettingsFrom(c.Config.Get(), c.Catalog.Get().Catalog) },
				Now:      env.Clock.Now,
			})
		}},
		{"models", func() error {
			return modelsapi.Register(reg, modelsapi.Deps{
				Config: c.Config, Catalog: c.Catalog, Models: d.merger, Mode: d.mode, Sync: d.syncEngine, Lock: c.OpLock,
				Regenerator: d.sdeps.Regenerator, Keys: router.NewFileKey(config.OSFS{}, c.Paths.APIKey()), KeyFS: config.OSFS{},
				KeyPath: c.Paths.APIKey(), Restarter: c.Orchestrator, State: c.State, Pusher: c.Cloud, Tailnet: c.Tailscale,
				Publisher: c.Bus, Alerts: router.ManagerAlerts(c.Doctor), Audit: d.audit, Now: env.Clock.Now, Logger: d.log,
			})
		}},
		{"model operations", func() error {
			cfg := c.Config.Get()
			max := 1
			if p := d.profile(); p != nil {
				max = p.Router.ModelsMax
			}
			return api.RegisterModelOps(reg, api.ModelOpsDeps{
				Orchestrator: router.NewHTTPOps(c.Orchestrator), LoadTimeout: loadTimeout(cfg), ModelsMax: max,
			})
		}},
		{"mode", func() error {
			return api.RegisterMode(reg, api.ModeDeps{Engine: c.Modes, Audit: d.audit, Logger: d.log})
		}},
		{"cloud", func() error {
			return api.RegisterCloud(reg, api.CloudDeps{Link: c.Cloud, Audit: d.audit, Logger: d.log})
		}},
		{"drill", func() error {
			return api.RegisterDrill(reg, api.DrillDeps{Service: drill.NewService(c.Drill), Audit: d.audit, Logger: d.log})
		}},
		{"docs", func() error {
			return reg.Add(api.DocsRoutes(api.DocsOptions{Roots: func() []string { return d.roots().Ordered() }, Logger: d.log})...)
		}},
	}
	for _, r := range regs {
		if err := r.add(); err != nil {
			return fmt.Errorf("routes of %s: %w", r.name, err)
		}
	}
	for _, h := range d.hooks.routes {
		if err := h.add(reg, c); err != nil {
			// A hook that cannot add its routes degrades; the agent still serves everything else.
			d.log.Error("a route hook failed", "hook", h.name, "error", err.Error())
		}
	}

	sock := env.SocketPath
	if sock == "" {
		sock = c.Paths.Socket()
	}
	srv, err := api.New(api.Options{
		Config: c.Config, TCPAddr: env.TCPAddr, SocketPath: sock, SocketGroup: env.SocketGroup, Version: env.Version,
		Mode: d.modeName, Auth: c.Auth, Registry: reg, Logger: d.log, Hooks: env.SocketHooks,
	})
	if err != nil {
		return &FatalError{Code: doctor.AGT099, Err: fmt.Errorf("api server: %w", err)}
	}
	c.API = srv
	d.stoppers.server = srv
	return nil
}

// loadTimeout is llama.load_timeout_seconds as a duration (zero: the route's default).
func loadTimeout(cfg config.Config) time.Duration {
	return time.Duration(cfg.Llama.LoadTimeoutSeconds) * time.Second
}

// profile is the node's profile in the catalog the agent runs with, nil when it has none.
func (d *Daemon) profile() *catalog.Profile {
	cfg := d.c.Config.Get()
	p, _ := d.c.Catalog.Get().Catalog.Profile(cfg.Profile)
	return p
}

// routerInputs assembles router.Inputs from the config store, the catalog store, the hardware
// sample, the state store and the mounted roots (the caller owns that, as the router documents), so
// every trigger of a regeneration or a model operation sees the same value.
func (d *Daemon) routerInputs() router.Inputs {
	cfg := d.c.Config.Get()
	cs := d.c.Catalog.Get()
	prof, _ := cs.Catalog.Profile(cfg.Profile)
	var rcfg catalog.Router
	if prof != nil {
		rcfg = prof.Router
	}
	st := d.c.State.Snapshot()
	def := ""
	if st.Models.Default != nil {
		def = *st.Models.Default
	}
	return router.Inputs{
		NodeID: cfg.Node.ID, Catalog: cs.Catalog, Profile: prof, Overrides: cfg.ProfileOverrides, Llama: cfg.Llama,
		Roots: d.roots(), Fit: router.NewEnv(d.c.Hardware.Snapshot(), cfg.Llama.Backend, rcfg, 0),
		StateDefault: def, Survival: st.Mode == state.ModeSurvival,
	}
}

// reconcileInputs is the pair of inputs of the startup reconcile: the preset inputs and the unit's.
func (d *Daemon) reconcileInputs() (router.Inputs, router.UnitInputs) {
	in := d.routerInputs()
	cfg := d.c.Config.Get()
	uin := router.UnitInputs{Mode: cfg.Llama.Mode, Port: cfg.Ports.Llama}
	if in.Profile != nil {
		uin.Router = in.Profile.Router
	}
	if cfg.Llama.Mode == router.LlamaModeSingle {
		uin.Single = d.singleModel(in)
	}
	return in, uin
}

// singleModel picks the model a unit in single mode serves: the state's default, else the profile's,
// on the first root that holds a verified copy. nil when there is none (the unit generator reports it).
func (d *Daemon) singleModel(in router.Inputs) *router.SingleModel {
	id := in.StateDefault
	if id == "" && in.Profile != nil {
		id = in.Profile.DefaultModel
	}
	if id == "" || in.Catalog == nil {
		return nil
	}
	m, ok := in.Catalog.Model(id)
	if !ok {
		return nil
	}
	presence := router.ManifestPresence{FS: config.OSFS{}}
	for _, root := range in.Roots.Ordered() {
		if presence.Present(root, m) {
			return &router.SingleModel{Model: m, Root: root, Aliases: in.Llama.LegacyAliases[id]}
		}
	}
	return nil
}

// fileOwners resolves the owners of the files the router generators write (root:localai for
// models.ini, root:root for the unit and the drop-in, localai:localai for the cache directory); -1
// leaves the owner to the process, which is what an unprivileged run and a machine without the
// localai user need.
func (d *Daemon) fileOwners() (ini, dropIn, unit, cache router.Ownership, ident *router.Identity) {
	none := router.Ownership{UID: -1, GID: -1}
	if !d.haveLocalAI || d.env.Unprivileged {
		return none, none, none, none, nil
	}
	return router.Ownership{UID: 0, GID: d.localaiGID}, router.Ownership{UID: 0, GID: 0}, router.Ownership{UID: 0, GID: 0},
		router.Ownership{UID: d.localaiUID, GID: d.localaiGID}, &router.Identity{UID: d.localaiUID, GID: d.localaiGID}
}

// arkNotify raises or clears the doctor alerts of the internal root monitor.
func (d *Daemon) arkNotify(_ ark.Root, problems []ark.Problem, err error) {
	seen := map[string]bool{}
	for _, p := range problems {
		seen[p.Code] = true
		d.raiseSeverity(p.Code, p.Severity, p.Details)
	}
	for _, code := range []string{ark.CodeInternalRecreated, ark.CodeNetworkFS} {
		if !seen[code] {
			d.clearAlert(code)
		}
	}
	if err != nil {
		d.log.Warn("the internal content root has a problem", "error", err.Error())
	}
}

// blockEnabled reads `enabled` of an owned node.yaml block (kiwix, whisper): false when the block is
// absent, true when it is present without the key.
func blockEnabled(cfg config.Config, name string) bool {
	raw, ok := cfg.Blocks[name]
	if !ok {
		return false
	}
	var b struct {
		Enabled *bool `yaml:"enabled"`
	}
	if err := raw.Decode(&b); err != nil {
		return false
	}
	return b.Enabled == nil || *b.Enabled
}

// kiwixThreads reads `threads` of the kiwix block (0: absent).
func kiwixThreads(cfg config.Config) int {
	raw, ok := cfg.Blocks["kiwix"]
	if !ok {
		return 0
	}
	var b struct {
		Threads int `yaml:"threads"`
	}
	if err := raw.Decode(&b); err != nil {
		return 0
	}
	return b.Threads
}

// counterView builds node.counters of GET /status?counters=1 from the producers: the event bus, the
// orchestrator, the companion services, the cloud link and the drill gauges.
type counterView struct {
	d      *Daemon
	health *health.Metrics
	gauges *gauges
}

func (v counterView) Counters() map[string]any {
	c := v.d.c
	out := map[string]any{}
	for k, val := range v.health.Counters() {
		out[k] = val
	}
	for k, val := range v.gauges.Counters() {
		out[k] = val
	}
	if c.Bus != nil {
		out["events"] = c.Bus.Counters()
	}
	if c.Orchestrator != nil {
		oc := c.Orchestrator.Counters()
		out["model_loads_ok"], out["model_loads_failed"] = oc.LoadsOK, oc.LoadsFailed
	}
	if c.Cloud != nil {
		cc := c.Cloud.Counters().Map()
		out["cloud_claims"], out["cloud_failures"] = cc["claims"], cc["failed"]
	}
	return out
}
