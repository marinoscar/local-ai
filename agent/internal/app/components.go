package app

import (
	"log/slog"

	"github.com/marinoscar/local-ai/agent/internal/api"
	"github.com/marinoscar/local-ai/agent/internal/ark"
	"github.com/marinoscar/local-ai/agent/internal/audit"
	"github.com/marinoscar/local-ai/agent/internal/auth"
	"github.com/marinoscar/local-ai/agent/internal/cloud"
	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/drill"
	"github.com/marinoscar/local-ai/agent/internal/events"
	"github.com/marinoscar/local-ai/agent/internal/hardware"
	"github.com/marinoscar/local-ai/agent/internal/modes"
	"github.com/marinoscar/local-ai/agent/internal/router"
	"github.com/marinoscar/local-ai/agent/internal/services"
	"github.com/marinoscar/local-ai/agent/internal/state"
	"github.com/marinoscar/local-ai/agent/internal/statusapi"
	"github.com/marinoscar/local-ai/agent/internal/svc/health"
	"github.com/marinoscar/local-ai/agent/internal/svc/kiwix"
)

// Components are the subsystems the daemon has built. Hooks (hooks.go) receive them, so a slice that
// plugs in later reaches the state store, the bus, the audit log, the orchestrator or the cloud link
// without a global. A field is nil until the step that builds it has run; every hook runs after the
// build, so the fields documented as "always set" are.
//
// Treat the fields as read-only: the daemon owns their lifecycle.
type Components struct {
	Env   Env
	Paths Paths
	Log   *slog.Logger

	// Config, State, Audit and Bus are set from startup step 3, 4, 7 and 9 on. The state store and the
	// audit log are always usable: a damaged disk degrades them to memory or to the journal.
	Config *config.Store
	State  *state.Store
	Audit  *audit.Log
	Bus    *events.Bus

	// Doctor and Checks are the alert manager and the registry of its checks.
	Doctor *doctor.Manager
	Checks *doctor.Registry

	// Auth, API and Routes are the authenticator, the HTTP server and the registry it serves.
	Auth   *auth.Service
	API    *api.Server
	Routes *api.Registry

	// Systemd is the guarded systemd Manager; Controller runs the start, stop and restart actions;
	// Watcher watches llama (and, until the health supervisor owns them, kiwix and whisper).
	Systemd    services.Manager
	Controller *services.Controller
	Watcher    *services.Watcher
	Health     *health.Supervisor

	// Hardware is the sampler; HardwareService is the real one (nil when the Env supplied a fake).
	Hardware        hardware.Sampler
	HardwareService *hardware.Service

	// Catalog holds the catalog the agent runs with; Fetcher builds the one a refresh installs.
	Catalog *statusapi.CatalogStore

	// The llama router: its client, watcher, generators, orchestrator, reconciler and the one shared
	// model-operations lock (agent.md section 4.9).
	Router        *router.Client
	RouterWatcher *router.Watcher
	Generator     *router.Generator
	UnitGenerator *router.UnitGenerator
	Orchestrator  *router.Orchestrator
	Reconciler    *router.Reconciler
	OpLock        *router.OpLock

	// Modes is the mode engine; Tailscale and Prober are the pieces it drives.
	Modes     *modes.Engine
	Tailscale *modes.TailscaleManager
	Prober    *modes.ConnectivityProber

	// Cloud is the cloud link; the claim plane and sync engine plug in through the hooks.
	Cloud *cloud.Link

	// Drill is the drill framework engine; ArkMonitor checks the internal content root.
	Drill      *drill.Engine
	ArkMonitor *ark.Monitor

	// Kiwix is the installer whose doctor check is registered.
	Kiwix *kiwix.Installer

	// Notify and Watchdog are the sd_notify side.
	Notify   *Notifier
	Watchdog *Watchdog
}
