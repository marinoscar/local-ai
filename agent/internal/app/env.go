package app

import (
	"io"
	"log/slog"
	"os"
	"os/user"
	"strconv"

	"github.com/marinoscar/local-ai/agent/internal/api"
	"github.com/marinoscar/local-ai/agent/internal/auth"
	"github.com/marinoscar/local-ai/agent/internal/catalog"
	"github.com/marinoscar/local-ai/agent/internal/cloud"
	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/execx"
	"github.com/marinoscar/local-ai/agent/internal/hardware"
	"github.com/marinoscar/local-ai/agent/internal/modes"
	"github.com/marinoscar/local-ai/agent/internal/router"
	"github.com/marinoscar/local-ai/agent/internal/services"
)

// Env is everything outside the process that the daemon touches, as seams. The zero value is the
// real machine: `aiagent serve` passes Env{}. An integration test fills the fields it wants to fake
// (a temp Root, a fake systemd, a fake hardware sampler, a fake cloud transport) and the real wiring
// of this package runs over them (agent.md section 9.2).
type Env struct {
	// Root is the filesystem root of every path in Paths; "" is "/".
	Root string
	// Version is the agent version reported by /health, state.json and the cloud (default Version).
	Version string
	// Getenv reads the environment (default os.Getenv): LOCALAI_LOG_LEVEL, NOTIFY_SOCKET and
	// WATCHDOG_USEC.
	Getenv func(string) string
	// Geteuid is the effective uid, for startup step 1 (default os.Geteuid).
	Geteuid func() int
	// Stdout receives the JSON log (default os.Stdout, which journald captures).
	Stdout io.Writer
	// Logger replaces the logger built from Stdout (a test that reads the log).
	Logger *slog.Logger
	// Clock drives every step timeout, the shutdown budget and the watchdog (default real).
	Clock config.Clock
	// Notifier sends sd_notify; nil builds one from $NOTIFY_SOCKET (a no-op when unset).
	Notifier *Notifier
	// Hooks is the extension registry (default DefaultHooks).
	Hooks *Hooks
	// OnStep, when set, is called before and after every startup step, synchronously. The tests use
	// it to observe the order and to hold a step (the gate stays closed while step 12 is held).
	OnStep func(StepEvent)

	// TCPAddr, SocketPath, SocketGroup and SocketHooks override the listeners (defaults: the port of
	// node.yaml on 127.0.0.1, Paths.Socket(), localai-operators and the operating system).
	TCPAddr     string
	SocketPath  string
	SocketGroup string
	SocketHooks api.Hooks

	// Runner runs external programs (tailscale, journalctl, systemctl); default execx.Exec.
	Runner execx.Runner
	// Systemd replaces the whole systemd seam (a fake). Nil dials the system bus and falls back to
	// systemctl through Runner (services.NewAuto).
	Systemd services.Manager
	// DialBus replaces the D-Bus dialer of the default Systemd (a test of the fallback).
	DialBus services.Dialer
	// Hardware replaces the hardware sampler (a fake); nil builds the real one.
	Hardware hardware.Sampler
	// Prober probes llama, kiwix and whisper over loopback HTTP (default services.NewHTTPProber).
	Prober services.Prober
	// CloudHTTP is the cloud link's HTTP client (default cloud.NewHTTPClient).
	CloudHTTP cloud.Doer
	// CloudClock and CloudRand drive the link's loops (default real).
	CloudClock cloud.Clock
	CloudRand  cloud.RandDuration
	// CatalogGitHub is the GitHub catalog source; nil builds catalog.NewGitHub(catalog.base_url).
	CatalogGitHub catalog.GitHubSource
	// ModesHTTP is the HTTP client of the connectivity prober and the tailnet reachability check.
	ModesHTTP modes.HTTPDoer
	// Groups looks up group membership of socket peers (default os/user).
	Groups auth.GroupLookup
	// Unprivileged makes the daemon skip every chown and owner check: files keep the owner of the
	// process. It is for tests and for a development machine that runs the agent as an ordinary user;
	// startup step 1 still wants Geteuid() == 0, so such a run also sets Geteuid.
	Unprivileged bool
	// LookupLocalAI resolves the localai user and group (default os/user). ok false means the user
	// does not exist (a development machine): files then keep the agent's own owner.
	LookupLocalAI func() (uid, gid int, ok bool)
	// RouterTiming shortens the orchestrator's waits (tests).
	RouterTiming router.Timing
	// ModeOptions tunes the mode engine (tests: a fake clock, step timeouts).
	ModeOptions modes.Options
}

func (e *Env) defaults() {
	if e.Version == "" {
		e.Version = Version
	}
	if e.Getenv == nil {
		e.Getenv = os.Getenv
	}
	if e.Geteuid == nil {
		e.Geteuid = os.Geteuid
	}
	if e.Stdout == nil {
		e.Stdout = os.Stdout
	}
	if e.Clock == nil {
		e.Clock = config.RealClock{}
	}
	if e.Hooks == nil {
		e.Hooks = DefaultHooks
	}
	if e.Runner == nil {
		e.Runner = execx.Exec{}
	}
	if e.Notifier == nil {
		e.Notifier = NewNotifier(e.Getenv(EnvNotifySocket))
	}
	if e.Groups == nil {
		e.Groups = auth.OSGroups{}
	}
	if e.LookupLocalAI == nil {
		e.LookupLocalAI = lookupLocalAI
	}
}

func (e Env) paths() Paths { return Paths{Root: e.Root} }

// lookupLocalAI resolves the localai user and its group with os/user (the pure-Go /etc/passwd
// reader in a static build).
func lookupLocalAI() (uid, gid int, ok bool) {
	u, err := user.Lookup("localai")
	if err != nil {
		return 0, 0, false
	}
	uid, err1 := strconv.Atoi(u.Uid)
	gid, err2 := strconv.Atoi(u.Gid)
	if err1 != nil || err2 != nil {
		return 0, 0, false
	}
	return uid, gid, true
}

// StepEvent is what Env.OnStep receives.
type StepEvent struct {
	// N is the step number of agent.md section 4.1 (1 to 17); Name is the step's name.
	N    int
	Name string
	// End is false before the step and true after it.
	End bool
	// Err is the step's outcome (End only): nil for success.
	Err error
}
