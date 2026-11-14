package ensure

import (
	"context"
	"errors"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/audit"
)

// The ports: everything the package needs from the machine. The real ones are in host.go and in
// internal/app/ensure.go; the tests use the fakes of fake_test.go.

// Errors a Kit answers. Anything else is an unexpected failure of the check itself.
var (
	// ErrKitMissing: no root has a verified copy of the item (or the kit does not hold it for this computer).
	ErrKitMissing = errors.New("the kit has no verified copy of this for this computer")
	// ErrKitDamaged: a copy exists but a checksum or manifest says it is damaged. Nothing is installed.
	ErrKitDamaged = errors.New("the kit's copy of this is damaged")
)

// Source is a place to install from: the content root whose kit verified the item.
type Source struct {
	// Root is the content root ("/srv/localai", "/media/<user>/AI-ARK/localai"), KitDir its kit folder.
	Root, KitDir string
	// File and SHA256 are the one file a step installs and its sha256 in the kit's record (an engine's
	// wheel bundle); empty for the other steps.
	File, SHA256 string
}

// Kit answers what the rebuild kit can supply. Every answer comes from the kit verifier of
// internal/kit: ErrKitMissing when no root's kit holds the item, ErrKitDamaged when one does but it failed
// its check, otherwise the first root whose item is ok.
type Kit interface {
	// Tool is the verified source of a tarball tool (kiwix-tools, pmtiles, piper) and the version the kit
	// holds.
	Tool(ctx context.Context, tool string) (Source, string, error)
	// Packages is the verified apt repository for this computer and the names it carries.
	Packages(ctx context.Context) (Source, map[string]bool, error)
	// WhisperPrebuilt is the ready-made whisper.cpp build that fits this computer exactly: the archive's path.
	WhisperPrebuilt(ctx context.Context, w WhisperWant) (string, error)
	// WhisperSource says whether the kit holds the whisper.cpp source of the commit (so a build is
	// possible); an empty commit means the pinned one.
	WhisperSource(ctx context.Context, w WhisperWant) (bool, error)
	// Engine is the newest verified wheel bundle of a Python engine ("acestep") for a backend: the source
	// (File and SHA256 set) and its version.
	Engine(ctx context.Context, engine, backend string) (Source, string, error)
}

// WhisperWant describes the whisper build this computer would use.
type WhisperWant struct {
	// Commit is whisper.commit of node.yaml ("" when not pinned).
	Commit string
	// Backend is the backend of the build ("auto" resolved by the caller to cuda, vulkan or cpu).
	Backend string
}

// Machine is what this computer has.
type Machine interface {
	// ToolVersion is the version /opt/localai/<tool>/current points to, "" when the tool is not installed.
	ToolVersion(tool string) string
	// MissingPackages returns the names of the list that dpkg does not have installed.
	MissingPackages(ctx context.Context, names []string) ([]string, error)
	// WhisperInstalled says whether the speech program has a build in use.
	WhisperInstalled() bool
	// ServiceActive says whether the unit of a service is running ("kiwix").
	ServiceActive(ctx context.Context, service string) bool
	// EngineVersion is the version of a Python engine's runtime in use for the backend ("" when there
	// is none, or it is built for another backend).
	EngineVersion(engine, backend string) string
}

// Installer performs the allowlisted actions. Each method verifies what it installs again right before it
// uses it (a hash of the file against the kit's own record), so a flipped byte installs nothing.
type Installer interface {
	// InstallTool installs kiwix-tools, pmtiles or piper from the kit folder. It refuses any other name.
	InstallTool(ctx context.Context, kitDir, tool string) (ToolOutcome, error)
	// InstallPackages installs exactly these packages (and what apt must add for them, all of it from the
	// verified kit repository) from the kit folder.
	InstallPackages(ctx context.Context, kitDir string, packages []string) (PackagesOutcome, error)
	// RestoreWhisper restores the ready-made build file as the speech program in use.
	RestoreWhisper(ctx context.Context, file string) error
	// BuildWhisper compiles whisper.cpp from the kit's source (only with a confirmation).
	BuildWhisper(ctx context.Context) error
	// RestartService restarts a service (only kiwix, only with a confirmation).
	RestartService(ctx context.Context, service string) error
	// InstallEngine builds a Python engine's runtime ("acestep") from its verified wheel bundle, offline.
	// It never touches a running engine: the new runtime is used at its next start.
	InstallEngine(ctx context.Context, engine string, src Source, version, backend string) (ToolOutcome, error)
}

// ToolOutcome is what InstallTool did.
type ToolOutcome struct {
	Version, Previous string
	Unchanged         bool
}

// PackagesOutcome is what InstallPackages did.
type PackagesOutcome struct {
	Installed []string
}

// Auditor is the audit log (internal/audit.Log).
type Auditor interface {
	Write(audit.Event) error
}

// Clock is the time source.
type Clock interface{ Now() time.Time }
