// Package build is the llama.cpp build manager (llama-router.md section 4.10, ROUTER-08): it turns a
// pinned commit into a relocatable, smoke-tested build under /opt/localai/llama.cpp/builds, switches
// the `current` symlink to it, restarts llama, checks that the new build really serves, switches
// back when it does not, and keeps only the builds that are worth keeping.
//
// The engine runs in the `aictl rebuild-llama` process (as root) or in `aiagent migrate`, never in
// the daemon, so a rebuild works when the agent does not. Everything it touches sits behind a seam:
//
//   - execx.Runner for cmake, git, nvidia-smi, the compilers, llama-server and systemctl;
//   - FS for the files (OSFS in production, a wrapper that injects faults in tests);
//   - Agent, Probe and StateRecorder for the agent and the router (AICTL-09 implements them over the
//     agent's HTTP API; the package imports neither internal/api nor internal/cli);
//   - Source for where the llama.cpp tree comes from (source.go; KIT-04 adds the kit readers);
//   - a clock and a sleep, so the waits of the post-switch check need no real time.
//
// The package owns no command surface: flags, the confirmation, progress and human output, `--json`
// and exit codes belong to AICTL-09 (aictl.md section 4.23). The one entry point is Manager.Build,
// with Manager.Resolve and Manager.Preflight for a caller that needs the inputs or the prerequisites
// before it starts (the confirmation line, `--dry-run`).
package build

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/audit"
	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/execx"
)

// Paths and limits of llama-router.md sections 3.1, 4.10.1 and 4.10.2.
const (
	// DefaultRoot is /opt/localai/llama.cpp: builds/, src/ and the current symlink live under it.
	DefaultRoot = "/opt/localai/llama.cpp"

	// BuildTimeout bounds `cmake --build` (offline laptop builds can be slow).
	BuildTimeout = 3 * time.Hour
	// ConfigureTimeout bounds `cmake -S ... -B ...`.
	ConfigureTimeout = 30 * time.Minute
	// SmokeTimeout bounds each smoke command.
	SmokeTimeout = time.Minute
	// GenerateTimeout bounds the llama-cli completion of the smoke test (rebuild-kit.md section 4.3.3).
	GenerateTimeout = 2 * time.Minute

	// MinFreeBytes is the free space on the root's filesystem a build needs before it starts.
	MinFreeBytes uint64 = 2 << 30
	// KitMinFreeBytes is the free space the offline preflight wants in /opt/localai (the source
	// tree under llama.cpp/src/ and the .tmp build tree under llama.cpp/builds/) and
	// KitMinScratchBytes the space in the scratch directory (rebuild-kit.md section 4.3.1).
	KitMinFreeBytes    uint64 = 5 << 30
	KitMinScratchBytes uint64 = 1 << 30
	// DefaultScratchDir is where option B unpacks the CUDA redistributables and where the whisper and
	// agent builds work (rebuild-kit.md section 7).
	DefaultScratchDir = "/var/lib/localai/build"
	// DefaultCompileUser is the user the compile runs as (rebuild-kit.md section 7).
	DefaultCompileUser = "localai"
	// CompileAsCaller, as Options.CompileUser, runs the compile as the user of the build manager
	// itself: for tests and for an unprivileged caller. Production leaves the default.
	CompileAsCaller = "-"
	// MinCMake is the oldest cmake the offline preflight accepts.
	MinCMake = "3.21"

	// DefaultKeep is the number of builds kept besides current (the previous one and the newest other).
	DefaultKeep = 2
	// KeepSources is how many source trees (commits) are kept.
	KeepSources = 2

	// SmokeTokens is the size of the completion of the post-switch check.
	SmokeTokens = 16
)

// Names inside the root.
const (
	buildsDir   = "builds"
	srcDir      = "src"
	currentLink = "current"
	currentTmp  = "current.tmp"
	lockName    = ".build.lock"

	suffixTmp    = ".tmp"
	suffixFailed = ".tmp.failed"
	infixOld     = ".old-"
	buildJSON    = "BUILD.json"
	buildLog     = "build.log"
)

// Backends of llama.backend.
const (
	BackendCUDA   = "cuda"
	BackendVulkan = "vulkan"
	BackendCPU    = "cpu"
)

// Binaries a build installs, in the order of BUILD.json's sha256.
var binaries = []string{"llama-server", "llama-cli", "llama-bench"}

// Doctor codes of the build manager. They alias internal/doctor/codes.go, the one file allowed to
// spell a "LAI-..." literal (scripts/check-doctor-codes.sh).
const (
	// CodeBuildFailed: building llama.cpp failed (LAI-LLM-018). Only that: a missing source is
	// CodeNoSource.
	CodeBuildFailed = doctor.LLM018
	// CodeBuildUnusable: the new build did not work and the previous one is still in use (LAI-LLM-019).
	CodeBuildUnusable = doctor.LLM019
	// CodeNoCudaArch: the CUDA architecture could not be detected (LAI-HW-019).
	CodeNoCudaArch = doctor.HW019
	// CodeNoSource: offline, and neither /opt/localai/llama.cpp/src nor the kit has the commit
	// (LAI-KIT-002).
	CodeNoSource = doctor.KIT002
	// CodeBusy: the agent is running another model operation (LAI-AGT-022; exit 6 at the CLI).
	CodeBusy = doctor.AGT022
	// CodeSourceDamaged: a source (or prebuilt) package of the kit does not match its recorded
	// sha256 (LAI-KIT-001).
	CodeSourceDamaged = doctor.KIT001
	// Codes of the offline preflight (rebuild-kit.md section 4.3.1).
	CodeKitTools   = doctor.KIT010 // build tools missing
	CodeKitCUDA    = doctor.KIT011 // nvcc missing or newer than the driver
	CodeKitVulkan  = doctor.KIT012 // no real Vulkan device, or its tools are missing
	CodeKitDriver  = doctor.KIT013 // the NVIDIA driver is not loaded
	CodeKitDiskLow = doctor.KIT014 // not enough disk space
)

// Stage names the part of the procedure an Error happened in.
type Stage string

// Stages of llama-router.md sections 4.10.1 to 4.10.3.
const (
	StageResolve   Stage = "resolve"   // inputs: commit, backend, CUDA arch, jobs
	StagePreflight Stage = "preflight" // prerequisite programs, free disk, the build lock
	StageSource    Stage = "source"    // finding and extracting the llama.cpp tree
	StageConfigure Stage = "configure" // cmake -S -B
	StageBuild     Stage = "build"     // cmake --build
	StageInstall   Stage = "install"   // copying the programs into place
	StageRunpath   Stage = "runpath"   // the relocatable check
	StageSmoke     Stage = "smoke"     // --version and --list-devices
	StageSwitch    Stage = "switch"    // the current symlink and the restart
	StageCheck     Stage = "check"     // the post-switch check
	StageRetention Stage = "retention" // deleting what is no longer worth keeping
)

// Error is a failed build, with the doctor code it is reported under. Details is never nil; typical
// keys are build_id, commit, backend, cause, exit_code, log_path, log_tail (the last 200 lines of
// the build log), missing, packages, previous_build, rolled_back.
type Error struct {
	Code    string
	Stage   Stage
	Message string
	Details map[string]any
	Err     error
}

// Error implements error.
func (e *Error) Error() string {
	s := e.Code + ": " + e.Message
	if e.Err != nil {
		s += ": " + e.Err.Error()
	}
	return s
}

// Unwrap lets errors.Is and errors.As reach the cause.
func (e *Error) Unwrap() error { return e.Err }

func newErr(code string, stage Stage, msg string, cause error, kv ...any) *Error {
	e := &Error{Code: code, Stage: stage, Message: msg, Err: cause, Details: map[string]any{"stage": string(stage)}}
	for i := 0; i+1 < len(kv); i += 2 {
		if k, ok := kv[i].(string); ok {
			e.Details[k] = kv[i+1]
		}
	}
	return e
}

// Request is one build: the flags of aictl rebuild-llama that belong to the engine (aictl.md section
// 4.23). Empty Commit, Backend and CudaArch take the node's values (Options.Node), then detection.
// The confirmation (--yes), the output format (--json) and the exit code are the caller's.
type Request struct {
	// Commit is a full sha or a short one (7 to 40 hex characters, lower case).
	Commit string
	// Backend is cuda, vulkan or cpu.
	Backend string
	// CudaArch is a semicolon list of compute capabilities without the dot ("75" or "75;86"); only
	// the cuda backend uses it.
	CudaArch string
	// SourceDir builds from this directory (--source) instead of the usual source chain: a git
	// checkout is read with `git archive <commit>`, a plain tree is copied as it is. Either way the
	// tree ends up in src/<commit>/, replacing one that is already there.
	SourceDir string
	// Sources, when not nil, replaces Options.Sources for this build.
	Sources []Source
	// FromKit is --from-kit: the chain is the kit alone (the verified tarball of the commit on any
	// root, then the git mirror of every root: KitSources over Options.KitRoots) and the offline
	// preflight (rebuild-kit.md section 4.3.1) runs first, so a missing tool is LAI-KIT-010 with
	// the `aictl kit install-debs` command rather than LAI-LLM-018 with `apt install`. A tree
	// already extracted in src/<commit>/ is still used first.
	FromKit bool
	// DryRun is --dry-run: Build runs the preflight only and returns Result.Preflight; it changes
	// nothing and needs no root. The error is the first failed check (nil when every check passed).
	DryRun bool
	// Prebuilt is --prebuilt: the path of an exported build (a tar.gz with BUILD.json and bin/,
	// rebuild-kit.md section 4.2). Nothing is compiled: the archive is unpacked into a scratch
	// directory, BUILD.json and the sha256 of every program are verified, and only then is the build
	// put in place, smoke tested and activated like a fresh one. A tampered file is LAI-KIT-001.
	// Commit and Backend, when given, must match the build.
	Prebuilt string
	// PrebuiltSHA256, when set, is the digest the kit's prebuilt manifest records for the archive;
	// the archive is refused if its bytes differ.
	PrebuiltSHA256 string
	// Offline skips every source that needs the network (the GitHub clone). With no other source
	// having the commit the build stops with LAI-KIT-002.
	Offline bool
	// Jobs is the -j value; 0 computes it (Jobs).
	Jobs int
	// NoActivate builds and smoke-tests without switching current (--no-activate).
	NoActivate bool
	// Keep is the number of builds kept besides current: the previous build and the newest others;
	// 0 means DefaultKeep. current and the previous build are never deleted.
	Keep int
	// Actor is who asked, for the audit line; the zero value is audit.CLIRoot().
	Actor audit.Actor
	// Progress, when set, is told about each stage as it starts (the caller prints it on stderr).
	Progress func(Progress)
	// Output, when set, also receives the output of the compile step (`cmake --build`, so ninja's
	// "[n/N] Building …" lines) as it is written, besides build.log; the caller turns it into a
	// percentage and an estimate of the time left. Its errors are ignored.
	Output io.Writer
}

// Progress is one progress line.
type Progress struct {
	Stage   Stage
	Message string
}

// Inputs are the resolved inputs of a build (llama-router.md section 4.10.1).
type Inputs struct {
	// Commit is the commit as given, lower case: a full sha, or a short one that source resolution
	// turns into the full sha.
	Commit string
	// ShortCommit is the first 9 characters of Commit, the part of the build id.
	ShortCommit string
	Backend     string
	// CudaArch is the semicolon list for cuda, "" for the other backends.
	CudaArch string
	Jobs     int
	// Activate is false with --no-activate.
	Activate bool
	// Keep is the effective number of builds kept besides current.
	Keep int
}

// BuildInfo is BUILD.json (llama-router.md section 4.10.3 step 4).
type BuildInfo struct {
	BuildID    string            `json:"build_id"`
	Commit     string            `json:"commit"`
	Backend    string            `json:"backend"`
	CudaArch   string            `json:"cuda_arch,omitempty"`
	CMakeFlags []string          `json:"cmake_flags"`
	Compiler   string            `json:"compiler"`
	NVCC       string            `json:"nvcc,omitempty"`
	BuiltAt    string            `json:"built_at"`
	BuiltBy    string            `json:"built_by"`
	SHA256     map[string]string `json:"sha256"`
	Smoke      Smoke             `json:"smoke"`
}

// Smoke is what the smoke test saw.
type Smoke struct {
	Version string   `json:"version"`
	Devices []string `json:"devices"`
	// Checks lists the steps a whisper.cpp smoke test passed ("static", "help", "transcribe"); llama's
	// smoke test leaves it empty.
	Checks []string `json:"checks,omitempty"`
}

// Result is what Build returns on success (aictl.md section 4.23, --json).
type Result struct {
	BuildID string `json:"build_id"`
	// Switched is true when current now points at the new build and the check passed.
	Switched bool `json:"switched"`
	// PreviousBuild is the directory name of the build current pointed at before the switch; ""
	// when nothing was switched or there was none.
	PreviousBuild string    `json:"previous_build"`
	BuildJSON     BuildInfo `json:"build_json"`

	// DefaultModel is the default model the post-switch check loaded and asked 16 tokens of; ""
	// when there is none (or nothing was switched).
	DefaultModel string `json:"-"`
	// Warnings are things the caller should show: no default model to check, a cleanup that failed.
	Warnings []string `json:"-"`
	// Preflight is the offline preflight of a --dry-run or --from-kit build (nil otherwise).
	Preflight *Report `json:"-"`
	// DryRun is true when Build only ran the preflight.
	DryRun bool `json:"-"`
	// Restored is true for a build restored with Request.Prebuilt.
	Restored bool `json:"-"`
	// Path is the directory of the build; LogPath its build.log.
	Path    string `json:"-"`
	LogPath string `json:"-"`
	// Removed lists what the retention deleted (build directories and source trees).
	Removed []string `json:"-"`
}

// Timing holds every wait of the engine; zero fields take the defaults. Tests shorten nothing: they
// inject Clock and Sleep.
type Timing struct {
	Configure, Build, Smoke time.Duration
	// Generate bounds the llama-cli completion of the smoke test (120 s).
	Generate time.Duration
	// Systemctl bounds `systemctl restart` on the path without the agent.
	Systemctl time.Duration
	// HealthWait is how long /health may take after the restart (60 s).
	HealthWait time.Duration
	// CheckWait is how long the whole post-switch check may take, counted from the restart (180 s).
	CheckWait time.Duration
	// Poll is the pause between polls of the post-switch check.
	Poll time.Duration
	// BusyEvery and BusyWait are how often and for how long a busy agent (409 LAI-AGT-022) is
	// retried before the switch is undone.
	BusyEvery, BusyWait time.Duration
}

func (t Timing) withDefaults() Timing {
	def := func(p *time.Duration, d time.Duration) {
		if *p <= 0 {
			*p = d
		}
	}
	def(&t.Configure, ConfigureTimeout)
	def(&t.Build, BuildTimeout)
	def(&t.Smoke, SmokeTimeout)
	def(&t.Generate, GenerateTimeout)
	def(&t.Systemctl, 2*time.Minute)
	def(&t.HealthWait, time.Minute)
	def(&t.CheckWait, 3*time.Minute)
	def(&t.Poll, 2*time.Second)
	def(&t.BusyEvery, 5*time.Second)
	def(&t.BusyWait, time.Minute)
	return t
}

// AuditWriter is the audit log; *audit.Log implements it.
type AuditWriter interface {
	Write(ev audit.Event) error
}

// Options configures New. Every field is optional except where a feature needs it: Agent and Probe
// are needed to activate (without them a node is treated as having no agent and no router to ask).
type Options struct {
	// LocalNVCC is the compiler of NVIDIA's own installer when it is not on the fixed PATH; empty means
	// /usr/local/cuda/bin/nvcc.
	LocalNVCC string
	// Root is /opt/localai/llama.cpp (DefaultRoot).
	Root string
	// BuiltBy is the "built_by" of BUILD.json, "aiagent <version>"; default "aiagent dev".
	BuiltBy string
	// Node returns node.yaml's llama block; its commit, backend and cuda_arch are the defaults of a
	// Request. nil means no defaults.
	Node func() config.LlamaBlock
	// Exec runs every program (execx.Exec{}); Lookup resolves their absolute paths from the fixed
	// PATH (execx.Lookup).
	Exec   execx.Runner
	Lookup func(name string) (string, error)
	// FS is the filesystem (OSFS{}).
	FS FS
	// Sources is the chain tried, in order, when src/<commit>/ does not exist yet (DefaultSources).
	Sources []Source
	// Agent restarts llama through the agent API when the agent is up; nil means the agent is down
	// and `systemctl restart localai-llama` is used. Probe runs the post-switch check; nil skips the
	// check with a warning. Recorder is told the installed build after a restart that did not go
	// through the agent (the agent records it itself on its own restart path); nil skips it.
	Agent    Agent
	Probe    Probe
	Recorder StateRecorder
	// PreviousBuild returns state.llama.previous_build, so that retention keeps it when current did
	// not change (--no-activate); nil means unknown.
	PreviousBuild func() string
	// Audit receives llama.build.switch.
	Audit AuditWriter
	// Timing holds the waits.
	Timing Timing
	// Clock and Sleep measure and spend the waits; defaults time.Now and a context-aware sleep.
	Clock func() time.Time
	Sleep func(ctx context.Context, d time.Duration)
	// FreeBytes reads the free space of a path (statfs); MemAvailableMiB reads MemAvailable;
	// NumCPU is the processor count.
	FreeBytes       func(path string) (uint64, error)
	MemAvailableMiB func() (int64, error)
	NumCPU          func() int
	// Logger receives component=router.build lines; default slog.Default().
	Logger *slog.Logger

	// Recipe is the program being built (LlamaRecipe).
	Recipe Recipe
	// KitRoots returns the content roots that may hold the kit, the internal root first (the
	// mounted ark drives after it); nil means none. --from-kit, the preflight and the CUDA
	// redistributables read them.
	KitRoots func() []string
	// ScratchDir is DefaultScratchDir.
	ScratchDir string
	// Arch is the CPU architecture in kit naming, amd64 or arm64 (from runtime.GOARCH).
	Arch string
	// CompileUser is the user the compile runs as through `setpriv --reuid <user> --regid <user>
	// --clear-groups`: the builds/<id>.tmp tree is created owned by it and handed back to root:root
	// before the rename (rebuild-kit.md section 7). Empty means DefaultCompileUser; CompileAsCaller
	// disables the drop (tests, unprivileged callers).
	CompileUser string
	// LookupUser resolves the compile user to its ids; default os/user.Lookup.
	LookupUser func(name string) (uid, gid int, err error)
	// Streamer reads `xz -dc` of the CUDA redistributables; default execx.Exec{}.
	Streamer execx.Streamer
	// Apt simulates installing the missing packages against the kit's apt repository in the
	// preflight; nil skips the simulation (the check says so). `aictl kit install-debs` (KIT-06)
	// provides the implementation.
	Apt AptSimulator
	// NodeConfig records node.yaml's llama.commit after a switch to a different commit; Exporter
	// exports the activated build into kit/prebuilt (KIT-05). Both are optional, and a failure of
	// either is a warning: the build is already in use.
	NodeConfig NodeRecorder
	Exporter   PrebuiltExporter
	// SmokeModel returns the path of the smallest verified model of the recipe's program on this
	// computer (a llama model for llama.cpp, a whisper model for whisper.cpp), for the completion of
	// the smoke test (`llama-cli -m <model> -n 8 -p "Hello" -ngl 99 --no-warmup`, rebuild-kit.md
	// section 4.3.3; `whisper-cli -m <model> -f <sample> -l en -nt`, companion-services.md section
	// 4.2.1); ok is false when there is none. nil, or no model, skips that step with a warning in
	// Result.Warnings.
	SmokeModel func() (path string, ok bool)

	// Defaults replaces Node as the source of the values a Request falls back to (commit, backend,
	// CUDA architecture). whisper.cpp uses it: its commit and backend are node.yaml's whisper
	// block, with "auto" meaning the llama backend (WhisperDefaults). nil: derived from Node.
	Defaults func() Defaults
	// Services asks the agent to restart the recipe's service (POST /services/<name>/restart). It is
	// used for any recipe and takes precedence over Agent, which is llama's (RestartLlama).
	Services ServiceRestarter
	// ServiceCheck is the post-switch check of a recipe whose service is not llama's: it returns nil
	// when the restarted service answers, ErrServiceNotConfigured when there is nothing to check
	// (the service has no configuration yet: a warning, not a failure) and any other error to have
	// the switch undone. nil skips the check with a warning.
	ServiceCheck func(ctx context.Context) error
	// SmokeAudio returns the bytes of the sample recording (a 16 kHz mono WAV of speech in English)
	// the whisper smoke test transcribes (the drill's en.wav, companion-services.md section 4.2.1).
	// nil, or an error, falls back to samples/jfk.wav of the source tree being built; with neither
	// the transcription is skipped with a warning.
	SmokeAudio func() ([]byte, error)
	// RecordCommit records the node.yaml commit of the recipe's program after a switch to another
	// commit; it replaces NodeConfig (llama's) when set.
	RecordCommit func(commit string) error
}

// Defaults are the node.yaml values a Request falls back to.
type Defaults struct {
	// Commit and Backend are the recipe's own keys (llama.commit, whisper.backend); Backend may be
	// "auto" for a recipe with AllowAuto.
	Commit, Backend string
	// LlamaBackend is what "auto" resolves to: llama.backend.
	LlamaBackend string
	// CudaArch is the semicolon list of llama.cuda_arch, "" when unset.
	CudaArch string
}

// defaults returns the values a Request falls back to.
func (m *Manager) defaults() Defaults {
	switch {
	case m.opt.Defaults != nil:
		return m.opt.Defaults()
	case m.opt.Node != nil:
		n := m.opt.Node()
		d := Defaults{Commit: n.Commit, Backend: n.Backend, LlamaBackend: n.Backend}
		if n.CudaArch != nil {
			d.CudaArch = *n.CudaArch
		}
		return d
	}
	return Defaults{}
}

// Manager is the build manager. It is safe to call Build from one goroutine at a time; the build
// lock (a flock on <root>/.build.lock) keeps two processes from building at once.
type Manager struct {
	opt  Options
	root string
	log  *slog.Logger
}

// New returns a Manager.
func New(opt Options) *Manager {
	if opt.Recipe.Name == "" {
		opt.Recipe = LlamaRecipe
	}
	if opt.Root == "" {
		opt.Root = opt.Recipe.Root
	}
	if opt.Root == "" {
		opt.Root = DefaultRoot
	}
	opt.Root = filepath.Clean(opt.Root)
	if opt.BuiltBy == "" {
		opt.BuiltBy = "aiagent dev"
	}
	if opt.Exec == nil {
		opt.Exec = execx.Exec{}
	}
	if opt.Lookup == nil {
		opt.Lookup = execx.Lookup
	}
	if opt.FS == nil {
		opt.FS = OSFS{}
	}
	if opt.Sources == nil {
		opt.Sources = []Source{GitCloneSource{URL: opt.Recipe.CloneURL}}
	}
	if opt.ScratchDir == "" {
		opt.ScratchDir = DefaultScratchDir
	}
	if opt.Arch == "" {
		opt.Arch = kitArch(runtime.GOARCH)
	}
	if opt.CompileUser == "" {
		opt.CompileUser = DefaultCompileUser
	}
	if opt.LookupUser == nil {
		opt.LookupUser = lookupUser
	}
	if opt.Streamer == nil {
		opt.Streamer = execx.Exec{}
	}
	if opt.Clock == nil {
		opt.Clock = time.Now
	}
	if opt.Sleep == nil {
		opt.Sleep = sleepCtx
	}
	if opt.FreeBytes == nil {
		opt.FreeBytes = osFreeBytes
	}
	if opt.MemAvailableMiB == nil {
		opt.MemAvailableMiB = readMemAvailableMiB
	}
	if opt.NumCPU == nil {
		opt.NumCPU = numCPU
	}
	opt.Timing = opt.Timing.withDefaults()
	lg := opt.Logger
	if lg == nil {
		lg = slog.Default()
	}
	return &Manager{opt: opt, root: opt.Root, log: lg.With("component", "router.build")}
}

func numCPU() int { return runtime.NumCPU() }

func sleepCtx(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

var (
	commitRe   = regexp.MustCompile(`^[0-9a-f]{7,40}$`)
	fullSHARe  = regexp.MustCompile(`^[0-9a-f]{40}$`)
	cudaArchRe = regexp.MustCompile(`^[0-9]{2,3}(;[0-9]{2,3})*$`)
)

// Resolve turns a Request into the inputs of the build: the commit, the backend, the CUDA
// architecture (from the request, then node.yaml, then nvidia-smi), the number of parallel jobs and
// the activation and retention choices. It runs nvidia-smi for a cuda build with no architecture
// given, and changes nothing. A caller prints "Building llama.cpp <commit> for <backend> (sm <arch>),
// <jobs> parallel jobs" from it.
func (m *Manager) Resolve(ctx context.Context, req Request) (Inputs, error) {
	in, err := m.resolve(ctx, req)
	return in, m.voiceErr(err)
}

func (m *Manager) resolve(ctx context.Context, req Request) (Inputs, error) {
	node := m.defaults()
	commit := strings.ToLower(strings.TrimSpace(req.Commit))
	if commit == "" {
		commit = strings.ToLower(strings.TrimSpace(node.Commit))
	}
	if commit == "" {
		return Inputs{}, newErr(CodeBuildFailed, StageResolve,
			"No llama.cpp commit is set. Pass --commit or set llama.commit in node.yaml.", nil, "cause", "no_commit")
	}
	if !commitRe.MatchString(commit) {
		return Inputs{}, newErr(CodeBuildFailed, StageResolve,
			"The llama.cpp commit must be 7 to 40 lowercase hex characters.", nil, "cause", "bad_commit", "commit", commit)
	}
	backend := strings.TrimSpace(req.Backend)
	if backend == "" {
		backend = node.Backend
	}
	if (backend == "auto" || backend == "") && m.opt.Recipe.AllowAuto {
		backend = strings.TrimSpace(node.LlamaBackend)
	}
	switch backend {
	case BackendCUDA, BackendVulkan, BackendCPU:
	case "":
		return Inputs{}, newErr(CodeBuildFailed, StageResolve,
			"No llama.cpp backend is set. Pass --backend or set llama.backend in node.yaml.", nil, "cause", "no_backend")
	default:
		return Inputs{}, newErr(CodeBuildFailed, StageResolve,
			"The llama.cpp backend must be cuda, vulkan or cpu.", nil, "cause", "bad_backend", "backend", backend)
	}

	in := Inputs{Commit: commit, ShortCommit: commit[:min(9, len(commit))], Backend: backend, Activate: !req.NoActivate}
	in.Keep = req.Keep
	if in.Keep <= 0 {
		in.Keep = m.defaultKeep()
	}
	if backend == BackendCUDA {
		arch := strings.TrimSpace(req.CudaArch)
		if arch == "" {
			arch = strings.TrimSpace(node.CudaArch)
		}
		if arch == "" {
			var err *Error
			if arch, err = m.DetectCudaArch(ctx); err != nil {
				return Inputs{}, err
			}
		}
		if !cudaArchRe.MatchString(arch) {
			return Inputs{}, newErr(CodeNoCudaArch, StageResolve,
				"The CUDA architecture must look like 75 or 75;86. Set llama.cuda_arch in node.yaml.", nil,
				"cause", "bad_cuda_arch", "cuda_arch", arch)
		}
		in.CudaArch = arch
	}

	// jobs = min(--jobs, nproc, floor(MemAvailable_GiB / 2)), at least 1 (rebuild-kit.md section
	// 4.3.1): a request for more jobs than the memory feeds would only get the build killed.
	mem, err := m.opt.MemAvailableMiB()
	if err != nil {
		m.log.Warn("could not read MemAvailable", "error", err)
	}
	switch {
	case err != nil && req.Jobs > 0:
		in.Jobs = min(req.Jobs, max(1, m.opt.NumCPU()))
	case err != nil:
		in.Jobs = 1
	case req.Jobs > 0:
		in.Jobs = min(req.Jobs, Jobs(m.opt.NumCPU(), mem))
	default:
		in.Jobs = Jobs(m.opt.NumCPU(), mem)
	}
	return in, nil
}

// Jobs is J = max(1, min(nproc, floor(MemAvailable_GiB / 2))): CUDA compilation needs about 2 GiB
// per job (llama-router.md section 4.10.2).
func Jobs(nproc int, memAvailableMiB int64) int {
	j := int(memAvailableMiB / 2048)
	if nproc < j {
		j = nproc
	}
	return max(1, j)
}

// DetectCudaArch asks nvidia-smi for the compute capability of every GPU: each line "7.5" becomes
// "75", the distinct values are sorted and joined with ";". No answer is LAI-HW-019.
func (m *Manager) DetectCudaArch(ctx context.Context) (string, *Error) {
	fail := func(cause string, err error) (string, *Error) {
		return "", newErr(CodeNoCudaArch, StageResolve,
			"The CUDA architecture of the GPU could not be detected. Set llama.cuda_arch in node.yaml and run sudo aictl rebuild-llama.",
			err, "cause", cause)
	}
	path, err := m.opt.Lookup("nvidia-smi")
	if err != nil {
		return fail("no_nvidia_smi", err)
	}
	res, err := m.opt.Exec.Run(ctx, execx.Cmd{
		Path: path, Args: []string{"--query-gpu=compute_cap", "--format=csv,noheader"},
		Timeout: 30 * time.Second, MaxOut: 64 << 10,
	})
	if err != nil {
		if ctx.Err() != nil {
			return "", &Error{Code: CodeNoCudaArch, Stage: StageResolve, Message: "cancelled", Err: ctx.Err(), Details: map[string]any{}}
		}
		return fail("nvidia_smi_failed", err)
	}
	if res.ExitCode != 0 {
		return fail("nvidia_smi_failed", fmt.Errorf("nvidia-smi exited with status %d", res.ExitCode))
	}
	arch := ParseComputeCaps(string(res.Stdout))
	if arch == "" {
		return fail("no_compute_cap", nil)
	}
	return arch, nil
}

var computeCapRe = regexp.MustCompile(`^([0-9]+)\.([0-9]+)$`)

// ParseComputeCaps turns the output of `nvidia-smi --query-gpu=compute_cap --format=csv,noheader`
// into a CUDA architecture list: "7.5" is "75", two GPUs "7.5" and "8.9" are "75;89". Lines that are
// not a version ("N/A", blank) are ignored; no valid line gives "".
func ParseComputeCaps(out string) string {
	seen := map[int]string{}
	var nums []int
	for _, line := range strings.Split(out, "\n") {
		m := computeCapRe.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil {
			continue
		}
		s := m[1] + m[2]
		n, err := strconv.Atoi(s)
		if err != nil {
			continue
		}
		if _, dup := seen[n]; !dup {
			seen[n] = s
			nums = append(nums, n)
		}
	}
	sort.Ints(nums)
	parts := make([]string, len(nums))
	for i, n := range nums {
		parts[i] = seen[n]
	}
	return strings.Join(parts, ";")
}

// readMemAvailableMiB reads MemAvailable from /proc/meminfo.
func readMemAvailableMiB() (int64, error) {
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, err
	}
	return parseMemAvailableMiB(b)
}

func parseMemAvailableMiB(b []byte) (int64, error) {
	for _, line := range strings.Split(string(b), "\n") {
		if !strings.HasPrefix(line, "MemAvailable:") {
			continue
		}
		f := strings.Fields(strings.TrimPrefix(line, "MemAvailable:"))
		if len(f) == 0 {
			break
		}
		kb, err := strconv.ParseInt(f[0], 10, 64)
		if err != nil {
			return 0, fmt.Errorf("build: MemAvailable %q: %w", f[0], err)
		}
		return kb / 1024, nil
	}
	return 0, errors.New("build: MemAvailable not found in /proc/meminfo")
}

// kitArch maps runtime.GOARCH to the architecture names of the kit layout.
func kitArch(goarch string) string {
	if goarch == "arm64" {
		return "arm64"
	}
	return "amd64"
}

// lookupUser resolves a user name to its numeric user and group id.
func lookupUser(name string) (int, int, error) {
	u, err := user.Lookup(name)
	if err != nil {
		return 0, 0, err
	}
	uid, err := strconv.Atoi(u.Uid)
	if err != nil {
		return 0, 0, err
	}
	gid, err := strconv.Atoi(u.Gid)
	if err != nil {
		return 0, 0, err
	}
	return uid, gid, nil
}
