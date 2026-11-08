package migrate

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/audit"
	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/services"
	"github.com/marinoscar/local-ai/agent/internal/state"
)

// The names the migration is about.
const (
	// LegacyUnitName is the hand-written unit that is replaced when no other known one serves (the
	// migration itself keeps the name llama-qwen for its state, its directory and its command).
	LegacyUnitName = services.UnitLlamaQwen
	// NewUnitName is the agent's unit that takes over.
	NewUnitName = services.UnitLlama
	// StateKey is the key of state.migration.
	StateKey = "llama_qwen"
	// LegacyPort is the port the legacy unit listens on and the new one keeps.
	LegacyPort = 8080
	// LoopbackHost is the only address the legacy unit may bind.
	LoopbackHost = "127.0.0.1"
	// ServiceUser is the user and group the new unit runs as (overview.md section 3.1).
	ServiceUser = "localai"
)

// LegacyUnitNames are the hand-written llama units the migration knows, in order of preference: ai-n1
// serves Qwen3-Coder-Next from llama-coder-next.service, and keeps llama-qwen.service beside it
// (llama-router.md section 4.9.1). The migration replaces the one that serves port 8080 and masks the
// others, so none of them takes the port back after a reboot.
var LegacyUnitNames = []string{services.UnitLlamaQwen, services.UnitLlamaCoderNext}

// Paths are the places the migration reads and writes (llama-router.md sections 3.1 and 4.9). Tests run
// on an in-memory filesystem with the real absolute paths, or on a temporary directory through Under.
type Paths struct {
	// UnitDir is /etc/systemd/system.
	UnitDir string
	// LegacyUnit is the legacy unit this migration replaces (one of LegacyUnitNames); "" means
	// LegacyUnitName. Preflight chooses it and plan.json keeps it.
	LegacyUnit string
	// LegacyKey is /etc/llama-qwen/api-key; NewKey is /etc/localai/api-key.
	LegacyKey, NewKey string
	// NodeYAML is /etc/localai/node.yaml; INI is /etc/localai/models.ini.
	NodeYAML, INI string
	// MigDir is /var/lib/localai/migration/llama-qwen.
	MigDir string
	// LlamaRoot is /opt/localai/llama.cpp (current and builds/ live in it).
	LlamaRoot string
	// InternalRoot is /srv/localai; models live in <InternalRoot>/models/<id>/.
	InternalRoot string
}

// DefaultPaths returns the canonical paths under root ("" means "/").
func DefaultPaths(root string) Paths {
	if root == "" {
		root = "/"
	}
	j := func(p string) string { return filepath.Join(root, p) }
	return Paths{
		UnitDir:      j("etc/systemd/system"),
		LegacyKey:    j("etc/llama-qwen/api-key"),
		NewKey:       j("etc/localai/api-key"),
		NodeYAML:     j("etc/localai/node.yaml"),
		INI:          j("etc/localai/models.ini"),
		MigDir:       j("var/lib/localai/migration/llama-qwen"),
		LlamaRoot:    j("opt/localai/llama.cpp"),
		InternalRoot: j("srv/localai"),
	}
}

// Legacy is the name of the legacy unit this migration replaces.
func (p Paths) Legacy() string { return orStr(p.LegacyUnit, LegacyUnitName) }

// UnitFile is /etc/systemd/system/<unit>; DropInDir is its .d directory.
func (p Paths) UnitFile(unit string) string  { return filepath.Join(p.UnitDir, unit) }
func (p Paths) DropInDir(unit string) string { return p.UnitFile(unit) + ".d" }

// LegacyUnitFile is /etc/systemd/system/llama-qwen.service (or the chosen legacy unit).
func (p Paths) LegacyUnitFile() string { return p.UnitFile(p.Legacy()) }

// LegacyDropInDir is /etc/systemd/system/llama-qwen.service.d (or the chosen legacy unit's).
func (p Paths) LegacyDropInDir() string { return p.DropInDir(p.Legacy()) }

// NewUnitFile is /etc/systemd/system/localai-llama.service.
func (p Paths) NewUnitFile() string { return filepath.Join(p.UnitDir, NewUnitName) }

// LegacyKeyDir is /etc/llama-qwen.
func (p Paths) LegacyKeyDir() string { return filepath.Dir(p.LegacyKey) }

// Plan is plan.json; Progress is progress.json; Result is result.json; ServeBefore is serve-before.json.
func (p Paths) Plan() string        { return filepath.Join(p.MigDir, "plan.json") }
func (p Paths) Progress() string    { return filepath.Join(p.MigDir, "progress.json") }
func (p Paths) Result() string      { return filepath.Join(p.MigDir, "result.json") }
func (p Paths) ServeBefore() string { return filepath.Join(p.MigDir, "serve-before.json") }

// UnitBackup is the saved legacy unit; DropInBackup the saved drop-in directory.
func (p Paths) UnitBackup() string   { return p.UnitBackupOf(p.Legacy()) }
func (p Paths) DropInBackup() string { return p.DropInBackupOf(p.Legacy()) }

// UnitBackupOf and DropInBackupOf are the same for any known legacy unit (the other units are saved too).
func (p Paths) UnitBackupOf(unit string) string   { return filepath.Join(p.MigDir, unit) }
func (p Paths) DropInBackupOf(unit string) string { return filepath.Join(p.MigDir, unit+".d") }

// Current is /opt/localai/llama.cpp/current; Builds is the directory of the builds.
func (p Paths) Current() string { return filepath.Join(p.LlamaRoot, "current") }
func (p Paths) Builds() string  { return filepath.Join(p.LlamaRoot, "builds") }

// ModelDir is <InternalRoot>/models/<id>.
func (p Paths) ModelDir(id string) string { return filepath.Join(p.InternalRoot, "models", id) }

// FS is the filesystem the migration works on: config.FS (the atomic writer takes it) and the few things
// it needs beyond that. internal/router/migrate/host has the real one; the tests have a memory one with
// several device numbers.
type FS interface {
	config.FS
	// MkdirAll creates a directory and its parents.
	MkdirAll(path string, perm os.FileMode) error
	// Symlink creates link pointing at target; it fails when link exists.
	Symlink(target, link string) error
	// Readlink returns the target of a symbolic link.
	Readlink(path string) (string, error)
	// OpenRead opens a file for a streamed read (it follows symbolic links).
	OpenRead(name string) (io.ReadCloser, error)
	// Dev returns the device number (st_dev) of the file or directory itself, without following a
	// final symbolic link.
	Dev(name string) (uint64, error)
	// FreeBytes returns the free space of the filesystem that holds path.
	FreeBytes(path string) (uint64, error)
}

// Systemd is the part of services.Manager the migration uses. The real one is the Manager behind the
// allowlist (services.NewGuard).
type Systemd interface {
	State(ctx context.Context, unit string) (services.UnitState, error)
	Start(ctx context.Context, unit string) error
	Stop(ctx context.Context, unit string) error
	Enable(ctx context.Context, unit string) error
	Disable(ctx context.Context, unit string) error
	Mask(ctx context.Context, unit string) error
	Unmask(ctx context.Context, unit string) error
	DaemonReload(ctx context.Context) error
}

// AgentUnit is localai-agent.service, which the single-writer rule stops while the migration runs. It is
// not part of Systemd: the daemon's allowlist forbids anything but a restart of the agent's own unit, and
// this runs in the aiagent migrate process, not in the daemon.
type AgentUnit interface {
	Active(ctx context.Context) (bool, error)
	Stop(ctx context.Context) error
	Start(ctx context.Context) error
}

// Users resolves the accounts of the node.
type Users interface {
	LookupUser(name string) (uid, gid int, err error)
	LookupGroup(name string) (gid int, err error)
}

// Clock is the time seam: the engine never sleeps by itself.
type Clock interface {
	Now() time.Time
	// Sleep waits for d or until ctx ends (then it returns ctx.Err()).
	Sleep(ctx context.Context, d time.Duration) error
}

// ModelState is one entry of the router's GET /models.
type ModelState struct {
	ID     string
	Status string // loaded, loading, unloaded, ...
	Failed bool
}

// V1Model is one entry of GET /v1/models.
type V1Model struct {
	ID      string
	Aliases []string
}

// Probe asks the router on the migrated port. The real one holds the key file and is the only part that
// speaks HTTP; its methods never log or return the key.
type Probe interface {
	// Health is GET /health (no key): nil on 200.
	Health(ctx context.Context) error
	// Models is the router's own GET /models with the key.
	Models(ctx context.Context) ([]ModelState, error)
	// V1Models is GET /v1/models, with the key or without it; status is the HTTP status.
	V1Models(ctx context.Context, withKey bool) (status int, models []V1Model, err error)
	// Chat is POST /v1/chat/completions with a short prompt for model (an id or an alias).
	Chat(ctx context.Context, model string, maxTokens int) (status int, err error)
	// NCtx is n_ctx of GET /props?model=<id>.
	NCtx(ctx context.Context, id string) (int, error)
	// Public is GET https://<host>/v1/models with the key: the public URL the clients use.
	Public(ctx context.Context, host string) (status int, err error)
}

// BuildRequest is what M1 asks of the build manager.
type BuildRequest struct {
	// Commit is the legacy binary's commit (7 to 40 hex characters).
	Commit string
	// Backend is cuda, vulkan or cpu.
	Backend string
	// SourceRepo is the legacy checkout (the unit's WorkingDirectory) the source is taken from with
	// git archive; the kit mirror is the fallback.
	SourceRepo string
	// Progress reports each stage of the build.
	Progress func(string)
}

// BuildResult is a finished build. The build manager has already smoke-tested it and has not switched
// current: the migration does that itself, so the new service is never restarted by the build.
type BuildResult struct {
	BuildID string
}

// Builder is the build manager (internal/router/build).
type Builder interface {
	// CanBuild checks the build prerequisites for the backend (nvcc or glslc, cmake, disk).
	CanBuild(ctx context.Context, backend string) error
	// Build builds the commit and leaves current alone.
	Build(ctx context.Context, req BuildRequest) (BuildResult, error)
}

// ConfigChange is what M4 writes into node.yaml.
type ConfigChange struct {
	// LegacyAliases is llama.legacy_aliases: model id to the names the old unit served it under.
	LegacyAliases map[string][]string
	// ExtraEnv is llama.extra_env: the GGML_* variables of the old unit.
	ExtraEnv map[string]string
	// Commit is the llama.cpp commit now in current ("" leaves llama.commit alone).
	Commit string
}

// NodeConfig is node.yaml.
type NodeConfig interface {
	// Get returns the effective configuration.
	Get() config.Config
	// Record merges the change into node.yaml (comments stay; a key that is already set is kept) and
	// reloads.
	Record(ctx context.Context, ch ConfigChange) error
}

// State is state.json. The agent is stopped while the migration runs, so the migration is its single
// writer.
type State interface {
	Migration() (state.MigrationEntry, bool)
	SetMigration(e state.MigrationEntry) error
	// SetDefaultModel records state.models.default.
	SetDefaultModel(id string) error
	// RecordBuild records state.llama.build (and previous_build) for the build now in current.
	RecordBuild(buildID string) error
	// Connectivity is state.connectivity.value ("" when unknown): whether the internet may be asked.
	Connectivity() string
}

// RenderResult is what the renderer wrote.
type RenderResult struct {
	INIPath  string
	UnitPath string
}

// Renderer writes models.ini, localai-llama.service and its drop-in for the node as configured (node.yaml
// and state.models.default already hold the migration's values) and runs daemon-reload. It enables and
// starts nothing. The real one runs the preset and unit generators of internal/router.
type Renderer interface {
	Render(ctx context.Context) (RenderResult, error)
}

// AuditFunc records one audit line (migration.* actions, actor cli-root).
type AuditFunc func(action string, result audit.Result, code string, target map[string]string) error
