package app

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/api"
	"github.com/marinoscar/local-ai/agent/internal/ark"
	"github.com/marinoscar/local-ai/agent/internal/auth"
	"github.com/marinoscar/local-ai/agent/internal/catalog"
	"github.com/marinoscar/local-ai/agent/internal/cloud"
	"github.com/marinoscar/local-ai/agent/internal/cloud/jobs"
	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/drill"
	"github.com/marinoscar/local-ai/agent/internal/events"
	"github.com/marinoscar/local-ai/agent/internal/hardware"
	"github.com/marinoscar/local-ai/agent/internal/maps"
	"github.com/marinoscar/local-ai/agent/internal/maps/places"
	"github.com/marinoscar/local-ai/agent/internal/modelsapi"
	"github.com/marinoscar/local-ai/agent/internal/modes"
	"github.com/marinoscar/local-ai/agent/internal/portal"
	"github.com/marinoscar/local-ai/agent/internal/router"
	"github.com/marinoscar/local-ai/agent/internal/router/migrate"
	"github.com/marinoscar/local-ai/agent/internal/services"
	"github.com/marinoscar/local-ai/agent/internal/setup/harden"
	"github.com/marinoscar/local-ai/agent/internal/state"
	"github.com/marinoscar/local-ai/agent/internal/statusapi"
	"github.com/marinoscar/local-ai/agent/internal/svc/health"
	"github.com/marinoscar/local-ai/agent/internal/svc/kiwix"
	"github.com/marinoscar/local-ai/agent/internal/svc/piper"
	"github.com/marinoscar/local-ai/agent/internal/svc/vision"
	"github.com/marinoscar/local-ai/agent/internal/svc/whisper"
	"github.com/marinoscar/local-ai/agent/internal/sync/plan"
	"github.com/marinoscar/local-ai/agent/internal/sync/run/adapt"
	"github.com/marinoscar/local-ai/agent/internal/update"
	"github.com/marinoscar/local-ai/agent/ui"
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
	base, _ := d.newSystemd(ctx)
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

	// The ark side: the monitor of the internal root, the best-copy index and the sync stack (journal,
	// downloader, machine, planner, run engine, readiness service). The model views, the piper routes
	// and the sync routes all read the one index.
	var arkOwner *ark.Owner
	if d.haveLocalAI && !env.Unprivileged {
		arkOwner = &ark.Owner{UID: d.localaiUID, GID: d.localaiGID}
	}
	c.ArkMonitor = ark.NewMonitor(ark.OSFS{}, ark.InternalOptions{
		Path: c.Paths.InternalRoot(), Profile: cfg.Get().Profile, NodeID: node, AgentVersion: env.Version,
		Owner: arkOwner, Now: clock.Now, Logger: log,
	}, clock, d.arkNotify)
	c.ArkIndex = d.arkIndex()
	if stack, err := d.buildArk(ctx, c.ArkIndex); err != nil {
		d.log.Error("the ark sync stack could not be built; sync, READY.json and GET /ark are off", "code", doctor.AGT099, "error", err.Error())
		d.raise(doctor.AGT099, map[string]any{"error": err.Error(), "subsystem": "ark sync"})
	} else {
		d.ark = stack
		c.ArkRoots, c.SyncEngine, c.Ready = stack.roots, stack.engine, stack.ready
		c.SyncResolvers, c.SyncItemResolvers = stack.resolvers, stack.itemResolvers
	}

	// Service watcher (llama) and the health supervisor (kiwix, whisper, piper).
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
	d.piper = piper.New(d.piperDeps(metrics))
	c.Piper = d.piper
	kiwixSpec := health.KiwixSpec(prober, ports(), func() bool { return blockEnabled(cfg.Get(), "kiwix") })
	d.kiwixStatus = kiwix.NewStatus()
	d.kiwixStatus.Attach(kiwixSpec.Probe.(*health.FuncProbe), kiwix.HTTPFetcher{}, func() int { return cfg.Get().Ports.Kiwix })
	d.whisperSTT = whisper.NewSTT(whisper.STTDeps{
		Config:  func() whisper.STTConfig { return whisper.STTConfigFrom(cfg.Get()) },
		Port:    func() int { return cfg.Get().Ports.Whisper },
		ModelID: func() string { return whisper.EnvModelID(d.c.Paths.Join(whisper.EnvPath), nil) },
		Now:     clock.Now, Metrics: metrics, Logger: log,
	})
	d.maps = maps.New(d.mapsDeps(metrics))
	d.maps.SetGPS(lazyGPS{d})
	d.places = &places.Manager{Copies: d.c.ArkIndex, Log: d.log}
	d.maps.SetPlaces(d.places)
	sup, err := health.New(health.Options{
		Manager: c.Systemd, Publisher: c.Bus, Mode: d.modeName, Metrics: metrics, Clock: clock, Logger: log,
		Specs: []health.Spec{
			kiwixSpec,
			health.WhisperSpec(prober, ports(), func() bool { return blockEnabled(cfg.Get(), "whisper") }),
			d.piper.Spec(),
			d.maps.Spec(),
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
	if err := c.Checks.Add(whisper.DoctorCheck(whisper.CheckOptions{
		Root:    c.Paths.Join(whisper.Root),
		Enabled: func() bool { return blockEnabled(cfg.Get(), "whisper") },
		Backend: func() string { return whisperBackend(cfg.Get()) },
	})); err != nil {
		return err
	}

	if err := vision.Register(c.Checks, visionSource{d}); err != nil {
		return err
	}

	// The llama router: client, watcher, the one shared lock, generators, orchestrator, reconciler.
	c.Router, err = router.NewClient(router.Options{Port: cfg.Get().Ports.Llama, Keys: router.NewFileKey(fs, c.Paths.APIKey()), Logger: log,
		// In single mode a one-model llama-server has no router status: the client reads /v1/models.
		Mode: func() string { return cfg.Get().Llama.Mode }})
	if err != nil {
		return fmt.Errorf("router client: %w", err)
	}
	c.RouterWatcher = router.NewWatcher(router.WatcherOptions{
		Client: c.Router, Publisher: c.Bus, Alerts: alerts, Clock: clock, Logger: log,
		ModelsMax: func() int {
			if cfg.Get().Llama.Mode == router.LlamaModeSingle {
				return 1 // a one-model server (llama-router.md section 4.8)
			}
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
		// Single mode (llama-router.md section 4.8): the unit is rewritten per model, a forced unload stops it.
		Units: c.UnitGenerator, Stopper: llamaUnit{c.Systemd},
		UnitBase: func() router.UnitInputs { return router.UnitInputs{Port: cfg.Get().Ports.Llama} },
		Builds:   router.FileBuild{FS: fs, Path: c.Paths.BuildJSON()}, Timing: env.RouterTiming, Clock: clock, Logger: log,
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
		Run: env.Runner, Lookup: env.Lookup, Settings: modes.SettingsFromConfig(cfg.Get), State: c.State, Online: mv.online,
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
	if err := harden.RegisterChecks(c.Checks, harden.New(harden.Paths{Root: env.Root}, harden.Options{}, nil)); err != nil {
		return err
	}
	if err := migrate.RegisterChecks(c.Checks, migrate.CheckSources{Systemd: c.Systemd, State: c.State, Paths: migrate.DefaultPaths(env.Root)}); err != nil {
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
		Ark: d.arkView(), Content: d.contentView(),
		CheckedAt: c.Prober.LastCheckedAt, Version: env.Version, StartedAt: d.began, Refresher: refresher{d},
		Regenerator: statusapi.RouterRegenerator{Generator: c.Generator, Inputs: func(context.Context) (router.Inputs, error) { return d.routerInputs(), nil }},
		Audit:       d.audit, Now: clock.Now, Logger: log,
	}
	d.sdeps = sdeps
	d.merger = merger

	// The drill's reporter: the last completed run in memory, the feed of the two drill alerts and the
	// cloud snapshot's drill block, so they can never disagree about which run is the last one.
	results := drill.NewResults(drill.ResultsOptions{Dir: c.Paths.DrillDir(), Owner: d.owner})
	reporter, err := drill.NewReporter(drill.ReporterOptions{
		Results: results, State: c.State, Clock: clock, OnChange: d.kickDoctor, Logger: log,
	})
	if err != nil {
		return fmt.Errorf("drill reporter: %w", err)
	}
	d.reporter = reporter
	if err := drill.RegisterAlertChecks(c.Checks, reporter, clock); err != nil {
		return err
	}

	// The cloud link. The snapshot sources besides the drill's (ark, sync) and any replacement of the
	// drill's arrive through the hook.
	var snap cloud.SnapshotOptions
	snap.Drill = reporter
	if d.ark != nil {
		snap.Ark, snap.Sync = d.ark.ready.CloudSource(), adapt.CloudSync{Engine: d.ark.engine}
	}
	if f := d.hooks.snapshot; f != nil { // a hook replaces what it supplies, and only that
		s := f(c)
		if s.Ark != nil {
			snap.Ark = s.Ark
		}
		if s.Sync != nil {
			snap.Sync = s.Sync
		}
		if s.Drill != nil {
			snap.Drill = s.Drill
		}
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

	// The sync engine behind the model routes: the extension point, or the documented stub.
	var syncEngine modelsapi.SyncEngine = noSyncEngine{}
	if d.ark != nil {
		syncEngine = adapt.Models{Engine: d.ark.engine, Retention: d.ark.ret}
	}
	if f := d.hooks.sync; f != nil {
		if e := f(c); e != nil {
			syncEngine = e
		}
	}
	d.syncEngine = syncEngine
	d.modelsDeps = modelsapi.Deps{
		Config: c.Config, Catalog: c.Catalog, Models: merger, Mode: mv, Sync: syncEngine, Lock: c.OpLock,
		Regenerator: sdeps.Regenerator, Keys: router.NewFileKey(fs, c.Paths.APIKey()), KeyFS: fs,
		KeyPath: c.Paths.APIKey(), Restarter: c.Orchestrator, State: c.State, Pusher: c.Cloud, Tailnet: c.Tailscale,
		Publisher: c.Bus, Alerts: alerts, Audit: d.audit, Now: clock.Now, Logger: log,
	}
	d.models, err = modelsapi.New(d.modelsDeps)
	if err != nil {
		return fmt.Errorf("models service: %w", err)
	}
	d.svcDeps = api.ServicesDeps{
		Status: svcView, Actions: &kiwixKickActions{ServiceActions: c.Controller, kick: &d.kiwixKick}, Health: sup, Llama: router.NewLlamaHook(c.Orchestrator),
		Mode: d.modeName, Audit: d.audit, Logger: log,
	}

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
	var syncCtl modes.SyncController
	if d.ark != nil {
		syncCtl = d.ark.engine
	}
	c.Modes, err = modes.New(modes.Deps{
		State: c.State, Events: c.Bus, Audit: d.audit, Link: link, Prober: c.Prober, Tailscale: c.Tailscale,
		Models: c.Orchestrator, Services: c.Controller, Sync: syncCtl, Portal: d.portalStore(),
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

	// The cloud command plane: the claim loop over the executors of internal/cloud/jobs, each calling
	// the same method as the REST route of its operation. A claim plane registered with UseClaimPlane
	// replaces it.
	if err := d.buildClaimPlane(sdeps); err != nil {
		return err
	}

	// The drill framework and the internal-root monitor.
	var arkActivity drill.ArkActivity
	if d.ark != nil {
		arkActivity = d.ark.engine
	}
	drillChecks := drill.NewRegistry()
	if err := registerFenceChecks(drillChecks, c.Paths); err != nil {
		return err
	}
	if err := d.registerContentDrillChecks(drillChecks); err != nil {
		return fmt.Errorf("drill content checks: %w", err)
	}
	drillRestorer, err := d.drillRestorer()
	if err != nil {
		return err
	}
	c.Drill, err = drill.New(drill.Deps{
		Registry: drillChecks, Restorer: drillRestorer, Results: results, Observer: reporter, Ark: arkActivity,
		State: c.State, Publisher: c.Bus, Mode: mv, Llama: llamaActivity{lock: c.OpLock, view: c.RouterWatcher},
		Power: powerSource{h: c.Hardware}, Metrics: gauge, NodeID: node, AgentVersion: env.Version,
		CatalogVersion: func() string { return c.Catalog.Get().Catalog.Version }, Clock: clock, Logger: log,
		// The fence pauses the cloud link for the length of a run and refuses every non-loopback dial of
		// the agent's outbound transport (drill.md section 4.4).
		Fence: newDrillFence(c.Cloud),
	})
	if err != nil {
		return fmt.Errorf("drill engine: %w", err)
	}
	if err := d.registerDrillModelChecks(drillChecks); err != nil {
		return err
	}
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
	if d.ark != nil {
		d.stoppers.syncer = d.ark.engine
	}
	if d.hooksSync != nil {
		d.stoppers.syncer = d.hooksSync
	}
	return nil
}

// newSystemd returns the systemd Manager and the backend that serves it: the injected one, or the
// D-Bus Manager with the systemctl fallback of services.NewAuto. A system bus that cannot be dialed
// is not an error: the agent runs `systemctl` through the Runner instead (agent.md section 4.3.2).
func (d *Daemon) newSystemd(ctx context.Context) (services.Manager, string) {
	if d.env.Systemd != nil {
		return d.env.Systemd, "injected"
	}
	dial := d.env.DialBus
	if dial == nil {
		dial = services.DialSystemBus
	}
	auto := services.NewAuto(ctx, dial, d.env.Runner, services.WithLogger(d.log), services.WithClock(d.env.Clock))
	d.closers = append(d.closers, auto)
	return auto, auto.Backend()
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
		{"services", func() error { return api.RegisterServices(reg, d.svcDeps) }},
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
				Builder:  d.portalBuilder(d.svcDeps.Health),
				Settings: func() portal.Settings { return portal.SettingsFrom(c.Config.Get(), c.Catalog.Get().Catalog) },
				Now:      env.Clock.Now,
				Metrics:  d.portalMetrics(),
			})
		}},
		{"portal ui", func() error {
			return reg.Add(api.SPARoutes(api.SPAOptions{FS: ui.Dist(), Mode: d.modeName, PortalHook: d.portalStore().Hook})...)
		}},
		{"models", func() error { return d.models.Register(reg) }},
		{"model operations", func() error {
			cfg := c.Config.Get()
			max := 1
			if p := d.profile(); p != nil && cfg.Llama.Mode != router.LlamaModeSingle {
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
		{"ark", func() error {
			if d.ark == nil { // the stack did not come up: the routes answer 503, the alert says why
				return api.RegisterArk(reg, api.ArkDeps{Ready: arkUnavailable{}})
			}
			return api.RegisterArk(reg, api.ArkDeps{Ready: d.ark.ready})
		}},
		{"ark drives", func() error {
			if d.ark == nil || d.ark.drives == nil { // no stack, no detector: 503, not an empty list
				return api.RegisterArkDrives(reg, api.ArkDrivesDeps{Drives: noDrives{}})
			}
			return api.RegisterArkDrives(reg, api.ArkDrivesDeps{Drives: d.ark.drivesView()})
		}},
		{"ark drive actions", func() error {
			deps := api.ArkDriveDeps{Audit: d.audit, Logger: d.log}
			if d.ark == nil || d.ark.driveActions == nil { // no detector, nothing to act on: 503
				deps.Ops = noDriveOps{}
			} else {
				deps.Ops = d.ark.driveOps()
			}
			return api.RegisterArkDrive(reg, deps)
		}},
		{"ark sync", func() error {
			if d.ark == nil { // no Starter and no Control: a real start, GET and DELETE answer 503
				return api.RegisterArkSync(reg, api.ArkSyncDeps{Planner: arkUnavailable{}})
			}
			return api.RegisterArkSync(reg, api.ArkSyncDeps{
				Planner: plan.NewDryRun(d.ark.plans), Starter: adapt.Starter{Engine: d.ark.engine}, Control: adapt.Controller{Engine: d.ark.engine},
			})
		}},
		{"ark prune", func() error {
			if d.ark == nil { // the stack did not come up: the route answers 503
				return api.RegisterArkPrune(reg, api.ArkPruneDeps{Pruner: arkUnavailable{}, Audit: d.audit, Logger: d.log})
			}
			return api.RegisterArkPrune(reg, api.ArkPruneDeps{Pruner: adapt.Pruner{Engine: d.ark.engine, Retention: d.ark.ret}, Audit: d.audit, Logger: d.log})
		}},
		{"ark verify", func() error {
			if d.ark == nil { // the stack did not come up: the route answers 503
				return api.RegisterArkVerify(reg, api.ArkVerifyDeps{Starter: arkUnavailable{}})
			}
			return api.RegisterArkVerify(reg, api.ArkVerifyDeps{Starter: adapt.VerifyStarter{Engine: d.ark.engine}})
		}},
		{"ark import", func() error {
			if d.ark == nil {
				return api.RegisterArkImport(reg, api.ArkImportDeps{Importer: arkUnavailable{}, Audit: d.audit, Logger: d.log})
			}
			return api.RegisterArkImport(reg, api.ArkImportDeps{Importer: adapt.Importer{Engine: d.ark.engine, Planner: d.ark.importer}, Audit: d.audit, Logger: d.log})
		}},
		{"speech", func() error { return reg.Add(d.piper.Routes()...) }},
		{"library", func() error {
			return reg.Add(kiwix.Routes(kiwix.NewProxy(kiwix.ProxyOptions{Port: c.Config.Get().Ports.Kiwix, Logger: d.log}))...)
		}},
		{"speech stt", func() error { return reg.Add(d.whisperSTT.Routes()...) }},
		{"maps", func() error { return reg.Add(d.maps.Routes()...) }},
		{"chat", func() error { return reg.Add(d.chatRoutes()...) }},
		{"docs", func() error { return reg.Add(d.docsRoutes()...) }},
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
		Mode: d.modeName, Auth: c.Auth, Registry: reg, Logger: d.log, Hooks: env.SocketHooks, Guard: d.portalStore().Guard,
	})
	if err != nil {
		return &FatalError{Code: doctor.AGT099, Err: fmt.Errorf("api server: %w", err)}
	}
	c.API = srv
	d.stoppers.server = srv
	return nil
}

// buildClaimPlane builds the cloud command plane (agent.md section 4.8.6): the executors of the
// job types the node can run, the claim loop over them and its inflight.json, bound to the link. A
// registration of UseClaimPlane replaces all of it. A job type whose dependency is missing is left out
// of the executors and refused with LAI-CLD-057, like a type missing from cloud.allow: ark.sync and
// ark.verify wait for the sync engine, agent.update for the self-update (UseAgentUpdate).
func (d *Daemon) buildClaimPlane(sdeps statusapi.Deps) error {
	c := d.c
	d.buildUpdater()
	if f := d.hooks.claim; f != nil {
		d.claim.set(f(c))
		return nil
	}
	jd := jobs.Deps{
		Router: c.Orchestrator, Models: d.models, Modes: c.Modes, Services: d.svcDeps,
		Logs: services.NewJournal(d.env.Runner, ""), Catalog: sdeps, Link: c.Cloud, Status: sdeps.Builder(),
		State: c.State, Hardware: c.Hardware, Clock: d.env.CloudClock, AgentUpdate: d.updater,
	}
	if d.ark != nil {
		jd.Ark = d.ark.jobsArk()
	}
	var recoverFn func(context.Context, cloud.InflightEntry) bool
	if d.updater != nil {
		recoverFn = d.updater.Recover
	}
	if f := d.hooks.agentUpd; f != nil { // a replacement of the self-update (a test double)
		u := f(c)
		jd.AgentUpdate, recoverFn = u.Updater, u.Recover
	}
	reg, err := cloud.NewRegistry(jobs.Handlers(jd)...)
	if err != nil {
		return fmt.Errorf("cloud job executors: %w", err)
	}
	claimer, err := cloud.NewClaimer(cloud.ClaimerDeps{
		Handlers: reg, Inflight: cloud.InflightStore{FS: config.OSFS{}, Path: c.Paths.Inflight()}, Audit: d.audit,
		Online: d.mode.online, Clock: d.env.CloudClock, Rand: d.env.CloudRand, Recover: recoverFn, Logger: d.log,
	})
	if err != nil {
		return fmt.Errorf("cloud claim loop: %w", err)
	}
	claimer.Bind(c.Cloud)
	if d.updater != nil {
		d.updater.Bind(claimer) // the settle of an agent.update command after the restart it causes
	}
	d.claim.set(ClaimPlane{Loop: claimer, Inflight: claimer.Inflight})
	return nil
}

// buildUpdater builds the self-update (agent.md section 4.10): the update.Runner every outside
// dependency of which is a seam, and the executor of the agent.update job over it. The new process
// runs its post-update health check at startup step 15 (stepUpdate); the cloud command is settled
// from there, or by the claim loop's Recover for an entry an earlier process left.
func (d *Daemon) buildUpdater() {
	c, env := d.c, d.env
	cfg := c.Config.Get()
	src := update.Source{Repo: cfg.Update.Repo, Channel: cfg.Update.Channel}
	if env.UpdateSource != nil {
		src = *env.UpdateSource
	}
	var uclock update.Clock = updateClock{env.Clock}
	if env.UpdateClock != nil {
		uclock = env.UpdateClock
	}
	c.Update = &update.Runner{
		Paths:  update.Paths{UpdateDir: c.Paths.Join(varDir + "/update"), BinDir: c.Paths.Join(optDir + "/bin")},
		Source: src, Current: env.Version, AllowDowngrade: cfg.Update.AllowDowngrade, Keys: env.UpdateKeys,
		Exec: env.Runner, Clock: uclock, Mode: d.modeName, Audit: d.audit, Events: c.Bus,
		State: c.State, Alerts: c.Doctor, Log: d.log, Probe: d.postUpdateProbe,
	}
	d.updater = &jobs.AgentUpdate{
		Runner: c.Update, Inflight: cloud.InflightStore{FS: config.OSFS{}, Path: c.Paths.Inflight()},
		Clock: uclock, Logger: d.log,
	}
}

// updateClock is update.Clock over the daemon's clock: Sleep waits on a ticker of it, so a test with a
// manual clock decides when the update's waits end.
type updateClock struct{ c config.Clock }

func (u updateClock) Now() time.Time { return u.c.Now() }

func (u updateClock) Sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := u.c.NewTicker(d)
	defer t.Stop()
	select {
	case <-t.C():
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// postUpdateProbe is one pass of the post-update health check of the new process (agent.md section
// 4.10 step 9): both listeners are up, the state store is writable, /health answers from the process
// itself, the router answers or at least the llama unit is as it should be, and the cloud registration
// is ok when the node was linked before. It returns the first problem; the update's Startup repeats it
// every 2 s for at most 60 s before it rolls back.
func (d *Daemon) postUpdateProbe(ctx context.Context) error {
	c := d.c
	if tcp, sock := c.API.BindErrors(); tcp != nil || sock != nil {
		return errors.New("a listener is not up")
	}
	if !c.State.Writable() {
		return errors.New("the state store is not writable")
	}
	if err := d.healthOverTransport(ctx); err != nil {
		return fmt.Errorf("/health: %w", err)
	}
	// The router answers; failing that, the llama unit is running (it is starting) or it is not meant to
	// be (disabled): nothing the update could have broken.
	if err := c.Router.Health(ctx); err != nil {
		us, uerr := llamaUnit{c.Systemd}.State(ctx)
		if uerr != nil {
			return fmt.Errorf("the router does not answer and the llama unit cannot be read: %w", uerr)
		}
		if !us.Running && us.Enabled {
			return errors.New("the router does not answer and the llama unit is not running")
		}
	}
	// A node that was linked before registers again, and must be linked once the registration ran.
	if _, ok := (cloud.LinkStore{Path: c.Paths.CloudLink(), FS: config.OSFS{}}).Load(); ok && d.mode.online() {
		if st := c.Cloud.State(); st != cloud.StateLinked {
			return fmt.Errorf("the cloud link is %s, not linked", st)
		}
	}
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
	if cfg.Llama.Mode == router.LlamaModeSingle {
		rcfg.ModelsMax = 1 // models_max is effectively 1 in single mode (llama-router.md section 4.8)
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
