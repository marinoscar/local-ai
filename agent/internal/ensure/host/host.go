// Package host is the real machine behind the ports of internal/ensure: the kit verifier, dpkg, the
// /opt/localai tool folders, the install functions of internal/kit and `aiagent ctl rebuild-whisper`.
// internal/app builds one Host and hands its parts to ensure.New; the tests of internal/ensure use fakes
// and the tests here use temporary folders and a fake execx.Runner.
package host

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/ensure"
	"github.com/marinoscar/local-ai/agent/internal/execx"
	"github.com/marinoscar/local-ai/agent/internal/kit"
	"github.com/marinoscar/local-ai/agent/internal/svc/whisper"
	"github.com/marinoscar/local-ai/agent/internal/update"
)

// AgentBinary is the multi-call program; `aiagent ctl ...` is aictl.
const AgentBinary = kit.DefaultOptRoot + "/bin/aiagent"

// Timeouts of the programs the installer runs.
const (
	restoreTimeout = 30 * time.Minute
	buildTimeout   = 3 * time.Hour
	maxOut         = 1 << 20
	quickTTL       = 15 * time.Second
)

// Config wires a Host.
type Config struct {
	FS      kit.VerifyFS
	Runner  execx.Runner
	OptRoot string
	// Roots lists the mounted content roots, internal first.
	Roots func() []kit.StatusRoot
	// Node returns the facts about this computer (the kit completeness rules need the Ubuntu codename, the
	// architecture, the backend and the whisper commit); nil answers are tolerated.
	Node func() *kit.NodeFacts
	// Kiwix and Piper are the tarball installers of their packages.
	Kiwix, Piper kit.TarballInstaller
	Keys         []update.PublicKey
	Now          func() time.Time
	// Restart restarts a service by name ("kiwix").
	Restart func(ctx context.Context, service string) error
	// ServiceActive says whether a unit runs.
	ServiceActive func(ctx context.Context, service string) bool
	// Aiagent is the program that runs `ctl`; default AgentBinary.
	Aiagent string
	// LookPath finds a program in the fixed PATH (execx.Lookup).
	LookPath func(name string) (string, error)
	// AptSourceList is the temporary apt source of an install ("" is kit.AptSourceList); tests point it at
	// a temporary folder.
	AptSourceList string
}

// Host implements ensure.Machine, ensure.Kit and ensure.Installer.
type Host struct {
	c     Config
	tools *kit.ToolInstaller
	verif kit.Verifier

	mu    sync.Mutex
	quick map[string]quickEntry
}

type quickEntry struct {
	at  time.Time
	rep kit.VerifyReport
	err error
}

// New builds a Host.
func New(c Config) *Host {
	if c.FS == nil {
		c.FS = kit.OSFS{}
	}
	if c.Runner == nil {
		c.Runner = execx.Exec{}
	}
	if c.OptRoot == "" {
		c.OptRoot = kit.DefaultOptRoot
	}
	if c.Aiagent == "" {
		c.Aiagent = AgentBinary
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	arch := "amd64"
	if n := nodeOf(c); n != nil && n.Arch != "" {
		arch = n.Arch
	}
	h := &Host{c: c, verif: kit.Verifier{FS: c.FS}, quick: map[string]quickEntry{}}
	h.tools = &kit.ToolInstaller{FS: c.FS, Runner: c.Runner, LookPath: h.lookPath, Arch: arch, Keys: c.Keys, OptRoot: c.OptRoot, Kiwix: c.Kiwix, Piper: c.Piper}
	return h
}

func nodeOf(c Config) *kit.NodeFacts {
	if c.Node == nil {
		return nil
	}
	return c.Node()
}

func (h *Host) lookPath(name string) (string, error) {
	if h.c.LookPath != nil {
		return h.c.LookPath(name)
	}
	return execx.Lookup(name)
}

func (h *Host) apt() *kit.Apt {
	n := nodeOf(h.c)
	a := &kit.Apt{FS: h.c.FS, Runner: h.c.Runner, LookPath: h.lookPath, SourceList: h.c.AptSourceList}
	if n != nil {
		a.Codename, a.Arch = n.Codename, n.Arch
	}
	return a
}

// ---- ensure.Machine -----------------------------------------------------------------------------

// ToolVersion implements ensure.Machine.
func (h *Host) ToolVersion(tool string) string { return h.tools.CurrentVersion(tool) }

// MissingPackages implements ensure.Machine.
func (h *Host) MissingPackages(ctx context.Context, names []string) ([]string, error) {
	have, err := h.apt().Installed(ctx, names)
	if err != nil {
		return nil, err
	}
	var missing []string
	for _, n := range names {
		if _, ok := have[n]; !ok {
			missing = append(missing, n)
		}
	}
	return missing, nil
}

// WhisperInstalled implements ensure.Machine: the program the unit runs exists and can be run.
func (h *Host) WhisperInstalled() bool {
	p := filepath.Join(h.c.OptRoot, "whisper.cpp", "current", "bin", "whisper-server")
	if h.c.OptRoot == kit.DefaultOptRoot {
		p = whisper.ServerPath
	}
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular() && fi.Mode().Perm()&0o111 != 0
}

// ServiceActive implements ensure.Machine.
func (h *Host) ServiceActive(ctx context.Context, service string) bool {
	return h.c.ServiceActive != nil && h.c.ServiceActive(ctx, service)
}

// ---- ensure.Kit ---------------------------------------------------------------------------------

// quickVerify is the quick verification of one part of one root, kept for a few seconds so that a plan that
// asks about five items verifies each part once.
func (h *Host) quickVerify(ctx context.Context, r kit.StatusRoot, part string) (kit.VerifyReport, error) {
	key := r.ID + "\x00" + r.Path + "\x00" + part
	h.mu.Lock()
	if e, ok := h.quick[key]; ok && h.c.Now().Sub(e.at) < quickTTL {
		h.mu.Unlock()
		return e.rep, e.err
	}
	h.mu.Unlock()
	opts := kit.Options{Mode: kit.ModeQuick, Part: part, Now: h.c.Now(), RootID: r.ID, Internal: r.Internal, Keys: h.c.Keys, Node: nodeOf(h.c)}
	rep, err := h.verif.Verify(ctx, r.Path, opts)
	h.mu.Lock()
	h.quick[key] = quickEntry{at: h.c.Now(), rep: rep, err: err}
	h.mu.Unlock()
	return rep, err
}

// itemState says whether the kit item verified on the root: ok, damaged, or absent (no manifest).
func itemState(rep kit.VerifyReport, id string) string {
	for _, it := range rep.Items {
		if it.ID == id {
			if it.Status == kit.StatusOK {
				return "ok"
			}
			return "damaged"
		}
	}
	return "absent"
}

func (h *Host) roots() []kit.StatusRoot {
	if h.c.Roots == nil {
		return nil
	}
	return h.c.Roots()
}

// firstVerified walks the roots in order and returns the first where the item verified; damaged says that
// some root had it but failed its check.
func (h *Host) firstVerified(ctx context.Context, part, item string) (root kit.StatusRoot, damaged bool, err error) {
	for _, r := range h.roots() {
		rep, verr := h.quickVerify(ctx, r, part)
		if verr != nil {
			if errors.Is(verr, kit.ErrNoKit) {
				continue
			}
			if ctx.Err() != nil {
				return root, damaged, ctx.Err()
			}
			return root, damaged, verr
		}
		switch itemState(rep, item) {
		case "ok":
			return r, damaged, nil
		case "damaged":
			damaged = true
		}
	}
	return kit.StatusRoot{}, damaged, ensure.ErrKitMissing
}

func kitErr(damaged bool, err error) error {
	if errors.Is(err, ensure.ErrKitMissing) && damaged {
		return ensure.ErrKitDamaged
	}
	return err
}

// Tool implements ensure.Kit.
func (h *Host) Tool(ctx context.Context, tool string) (ensure.Source, string, error) {
	item := "kit-" + tool
	r, damaged, err := h.firstVerified(ctx, kit.PartTools, item)
	if err != nil {
		return ensure.Source{}, "", kitErr(damaged, err)
	}
	kitDir := filepath.Join(r.Path, kit.DirName)
	v, err := h.tools.KitVersion(kitDir, tool)
	switch {
	case err == nil:
		return ensure.Source{Root: r.Path, KitDir: kitDir}, v, nil
	case errors.Is(err, kit.ErrToolNotInKit):
		return ensure.Source{}, "", ensure.ErrKitMissing
	case errors.Is(err, kit.ErrToolDamaged):
		return ensure.Source{}, "", ensure.ErrKitDamaged
	}
	return ensure.Source{}, "", err
}

// Packages implements ensure.Kit.
func (h *Host) Packages(ctx context.Context) (ensure.Source, map[string]bool, error) {
	n := nodeOf(h.c)
	if n == nil || n.Codename == "" || n.Arch == "" {
		return ensure.Source{}, nil, ensure.ErrKitMissing
	}
	r, damaged, err := h.firstVerified(ctx, kit.PartDebs, "kit-debs-"+n.Codename+"-"+n.Arch)
	if err != nil {
		return ensure.Source{}, nil, kitErr(damaged, err)
	}
	kitDir := filepath.Join(r.Path, kit.DirName)
	names, err := h.apt().KitPackages(kitDir)
	if err != nil {
		var ke *kit.Error
		if errors.As(err, &ke) && (errors.Is(ke.Kind, kit.ErrNoRepo) || errors.Is(ke.Kind, kit.ErrWrongRelease)) {
			return ensure.Source{}, nil, ensure.ErrKitMissing
		}
		if errors.As(err, &ke) && errors.Is(ke.Kind, kit.ErrRepoDamaged) {
			return ensure.Source{}, nil, ensure.ErrKitDamaged
		}
		return ensure.Source{}, nil, err
	}
	return ensure.Source{Root: r.Path, KitDir: kitDir}, names, nil
}

// WhisperPrebuilt implements ensure.Kit.
func (h *Host) WhisperPrebuilt(_ context.Context, w ensure.WhisperWant) (string, error) {
	n := nodeOf(h.c)
	if n == nil || n.Codename == "" || n.Arch == "" {
		return "", ensure.ErrKitMissing
	}
	var paths []string
	for _, r := range h.roots() {
		paths = append(paths, r.Path)
	}
	commit := w.Commit
	if commit == "" {
		commit = n.WhisperCommit
	}
	f, err := kit.SelectPrebuilt(h.c.FS, paths, kit.PrebuiltWant{
		Program: kit.ProgramWhisper, Commit: commit, Backend: w.Backend, Codename: n.Codename, Arch: n.Arch, CudaArch: n.CudaArch,
	})
	if err != nil {
		if errors.Is(err, kit.ErrNoPrebuilt) {
			return "", ensure.ErrKitMissing
		}
		return "", err
	}
	return f.Path, nil
}

// WhisperSource implements ensure.Kit: the kit's completeness rules say whether the whisper.cpp source of
// the pinned commit is there and sound.
func (h *Host) WhisperSource(ctx context.Context, w ensure.WhisperWant) (bool, error) {
	n := nodeOf(h.c)
	if n == nil {
		return false, ensure.ErrKitMissing
	}
	facts := *n
	if w.Commit != "" {
		facts.WhisperCommit = w.Commit
	}
	if facts.WhisperCommit == "" {
		return false, ensure.ErrKitMissing
	}
	damaged := false
	for _, r := range h.roots() {
		opts := kit.Options{Mode: kit.ModeQuick, Part: kit.PartSrc, Now: h.c.Now(), RootID: r.ID, Internal: r.Internal, Keys: h.c.Keys, Node: &facts}
		rep, err := h.verif.Verify(ctx, r.Path, opts)
		if err != nil {
			if errors.Is(err, kit.ErrNoKit) {
				continue
			}
			return false, err
		}
		if rep.Node == nil {
			continue
		}
		for _, need := range rep.Node.Needs {
			if need.Need != kit.NeedWhisperSource {
				continue
			}
			switch need.Status {
			case kit.StatusOK:
				return true, nil
			case kit.StatusDamaged:
				damaged = true
			}
		}
	}
	if damaged {
		return false, ensure.ErrKitDamaged
	}
	return false, ensure.ErrKitMissing
}

// ---- ensure.Installer ---------------------------------------------------------------------------

// InstallTool implements ensure.Installer. The agent is not a tool here: it is refused.
func (h *Host) InstallTool(ctx context.Context, kitDir, tool string) (ensure.ToolOutcome, error) {
	if tool != kit.ToolKiwix && tool != kit.ToolPmtiles && tool != kit.ToolPiper {
		return ensure.ToolOutcome{}, fmt.Errorf("%w: tool %q", ensure.ErrNotAllowed, tool)
	}
	res, err := h.tools.Install(ctx, kitDir, tool)
	if err != nil {
		return ensure.ToolOutcome{}, err
	}
	return ensure.ToolOutcome{Version: res.Version, Previous: res.Previous, Unchanged: res.Unchanged}, nil
}

// InstallPackages implements ensure.Installer: the same function as `aictl kit install-debs --packages`.
func (h *Host) InstallPackages(ctx context.Context, kitDir string, packages []string) (ensure.PackagesOutcome, error) {
	res, err := h.apt().Run(ctx, kitDir, kit.Request{Packages: packages})
	if err != nil {
		return ensure.PackagesOutcome{}, err
	}
	var names []string
	for _, p := range res.Installed {
		names = append(names, p.Name)
	}
	return ensure.PackagesOutcome{Installed: names}, nil
}

// codeRe finds a doctor code in the output of a command.
var codeRe = regexp.MustCompile(`LAI-[A-Z]{2,3}-[0-9]{3}`)

// coded is a failure of `aiagent ctl` with the doctor code its output named.
type coded struct {
	code string
	msg  string
}

func (c *coded) Error() string { return c.msg }
func (c *coded) Code() string  { return c.code }

func (h *Host) ctl(ctx context.Context, timeout time.Duration, args ...string) error {
	res, err := h.c.Runner.Run(ctx, execx.Cmd{Path: h.c.Aiagent, Args: append([]string{"ctl"}, args...), Timeout: timeout, MaxOut: maxOut})
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("the %s program could not run: %w", args[0], err)
	}
	if res.ExitCode == 0 {
		return nil
	}
	out := strings.TrimSpace(string(res.Stderr))
	if out == "" {
		out = strings.TrimSpace(string(res.Stdout))
	}
	last := out
	if i := strings.LastIndexByte(strings.TrimSpace(out), '\n'); i >= 0 {
		last = strings.TrimSpace(out[i+1:])
	}
	msg := fmt.Sprintf("%s failed (exit %d)", args[0], res.ExitCode)
	if last != "" {
		msg += ": " + last
	}
	if m := codeRe.FindString(out); m != "" {
		return &coded{code: m, msg: msg}
	}
	return errors.New(msg)
}

// RestoreWhisper implements ensure.Installer: `aictl rebuild-whisper --prebuilt <file> --yes`, the command
// that verifies the archive, tests the build, switches to it and rolls back when the speech service does not
// answer. Nothing is compiled.
func (h *Host) RestoreWhisper(ctx context.Context, file string) error {
	if !filepath.IsAbs(file) || filepath.Clean(file) != file {
		return fmt.Errorf("%w: %q is not a clean absolute path", ensure.ErrNotAllowed, file)
	}
	return h.ctl(ctx, restoreTimeout, "rebuild-whisper", "--prebuilt", file, "--yes", "--json")
}

// BuildWhisper implements ensure.Installer: `aictl rebuild-whisper --from-kit --yes`, from the kit's source,
// with no internet. Only ever called with a confirmation.
func (h *Host) BuildWhisper(ctx context.Context) error {
	return h.ctl(ctx, buildTimeout, "rebuild-whisper", "--from-kit", "--yes", "--json")
}

// RestartService implements ensure.Installer; only kiwix is allowed.
func (h *Host) RestartService(ctx context.Context, service string) error {
	if service != "kiwix" {
		return fmt.Errorf("%w: service %q", ensure.ErrNotAllowed, service)
	}
	if h.c.Restart == nil {
		return errors.New("no way to restart a service is configured")
	}
	return h.c.Restart(ctx, service)
}

var (
	_ ensure.Machine   = (*Host)(nil)
	_ ensure.Kit       = (*Host)(nil)
	_ ensure.Installer = (*Host)(nil)
)
