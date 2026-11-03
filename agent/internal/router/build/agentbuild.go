package build

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"debug/elf"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/audit"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/execx"
	"github.com/marinoscar/local-ai/agent/internal/kit"
	upd "github.com/marinoscar/local-ai/agent/internal/update"
)

// The agent rebuild (rebuild-kit.md section 4.3.4, aictl.md section 4.23b, KIT-05): `aictl
// rebuild-agent` builds aiagent from the kit's source tarball with the kit's Go toolchain, with no
// network, tests the result and swaps it in with the self-update rules (aiagent.prev kept, restart,
// health check within 60 s, rollback). It shares the building blocks of the program rebuilds: the
// hardened extraction (ExtractArchive), the unprivileged compile (setpriv to the compile user, the
// tree handed back to root before anything of it is read or run), the build lock and the reports of
// the preflight (Check).
//
// What is not shared is the Manager: an agent has no builds/ folder, no `current` link and no model
// service to restart, so AgentBuilder is its own small type with the same seams (execx.Runner, FS,
// clock, the agent's health) and the same error type (*Error).

// Doctor codes of the agent rebuild. They alias internal/doctor/codes.go.
const (
	// CodeAgentBuildFailed: the compile failed (LAI-KIT-020).
	CodeAgentBuildFailed = doctor.KIT020
	// CodeAgentUnusable: the new agent did not pass its test, or did not become healthy and the
	// previous one was put back (LAI-KIT-021).
	CodeAgentUnusable = doctor.KIT021
	// CodeAgentSourceIncomplete: the source in the kit is not complete, so it cannot be built offline
	// (LAI-KIT-030).
	CodeAgentSourceIncomplete = doctor.KIT030
	// CodeKitMissing: the kit has no agent source or no Go toolchain for this computer (LAI-KIT-040).
	CodeKitMissing = doctor.KIT040
)

// Names and limits of the agent rebuild.
const (
	// DefaultOptRoot is /opt/localai: the agent is <root>/bin/aiagent.
	DefaultOptRoot = "/opt/localai"
	// AgentUnit is the systemd unit the new agent is restarted through.
	AgentUnit = "localai-agent.service"
	// AgentBuildTimeout bounds `go build`.
	AgentBuildTimeout = time.Hour
	// AgentHealthWait is how long the restarted agent has to answer with the new version (rebuild-kit.md
	// section 4.3.4 step 4).
	AgentHealthWait = 60 * time.Second

	agentBuildLog  = "agent-build.log"
	agentLockName  = ".agent-build.lock"
	agentSmokeWait = 10 * time.Second
	agentRestart   = 2 * time.Minute
	agentModuleMax = 1 << 20
	agentModsMax   = 8 << 20
)

// AgentHealth is the part of GET /health the check reads.
type AgentHealth struct {
	Version  string
	Starting bool
}

// AgentTiming holds the waits of the agent rebuild; zero fields take the defaults.
type AgentTiming struct {
	Build, Smoke, Restart, HealthWait, Poll time.Duration
}

func (t AgentTiming) withDefaults() AgentTiming {
	def := func(p *time.Duration, d time.Duration) {
		if *p <= 0 {
			*p = d
		}
	}
	def(&t.Build, AgentBuildTimeout)
	def(&t.Smoke, agentSmokeWait)
	def(&t.Restart, agentRestart)
	def(&t.HealthWait, AgentHealthWait)
	def(&t.Poll, 2*time.Second)
	return t
}

// AgentOptions configures NewAgentBuilder. Every field is optional; the zero value is the real machine
// without a health check.
type AgentOptions struct {
	// KitRoots returns the content roots that may hold the kit, the internal root first.
	KitRoots func() []string
	// Exec runs go, setpriv, the new agent and systemctl; Lookup resolves their absolute paths.
	Exec   execx.Runner
	Lookup func(name string) (string, error)
	// FS is the filesystem (OSFS{}).
	FS FS
	// ScratchDir is /var/lib/localai/build: the toolchain and the source tree are unpacked here and
	// removed again. OptRoot is /opt/localai.
	ScratchDir, OptRoot string
	// Arch is amd64 or arm64 (the architecture of the kit's toolchain and of the agent).
	Arch string
	// CompileUser and LookupUser are as in Options: the compile runs as this user (default localai);
	// CompileAsCaller disables the drop.
	CompileUser string
	LookupUser  func(name string) (uid, gid int, err error)
	// Health reads GET /health of the running agent; nil skips the health check with a warning. A
	// failing read is retried until HealthWait is over.
	Health func(ctx context.Context) (AgentHealth, error)
	// Audit receives agent.build.switch.
	Audit AuditWriter
	// Timing, Clock and Sleep are the waits; tests inject the clock.
	Timing AgentTiming
	Clock  func() time.Time
	Sleep  func(ctx context.Context, d time.Duration)
	// FreeBytes reads the free space of a path.
	FreeBytes func(path string) (uint64, error)
	Logger    *slog.Logger
}

// AgentBuilder rebuilds the agent from the kit.
type AgentBuilder struct {
	opt AgentOptions
	log *slog.Logger
}

// NewAgentBuilder returns an AgentBuilder.
func NewAgentBuilder(opt AgentOptions) *AgentBuilder {
	if opt.Exec == nil {
		opt.Exec = execx.Exec{}
	}
	if opt.Lookup == nil {
		opt.Lookup = execx.Lookup
	}
	if opt.FS == nil {
		opt.FS = OSFS{}
	}
	if opt.ScratchDir == "" {
		opt.ScratchDir = DefaultScratchDir
	}
	if opt.OptRoot == "" {
		opt.OptRoot = DefaultOptRoot
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
	if opt.Clock == nil {
		opt.Clock = time.Now
	}
	if opt.Sleep == nil {
		opt.Sleep = sleepCtx
	}
	if opt.FreeBytes == nil {
		opt.FreeBytes = osFreeBytes
	}
	opt.Timing = opt.Timing.withDefaults()
	lg := opt.Logger
	if lg == nil {
		lg = slog.Default()
	}
	return &AgentBuilder{opt: opt, log: lg.With("component", "router.build.agent")}
}

// AgentRequest is one rebuild.
type AgentRequest struct {
	// Tag is the release tag to build ("node-v0.4.0"); empty is the newest source in the kit.
	Tag string
	// DryRun only checks that the source, the vendored modules and the toolchain are there and complete;
	// it changes nothing and needs no root.
	DryRun bool
	// Actor is who asked, for the audit line; the zero value is audit.CLIRoot().
	Actor audit.Actor
	// Progress is told about each step as it starts.
	Progress func(Progress)
}

// AgentReport is the result of the checks of a rebuild (the whole of --dry-run).
type AgentReport struct {
	// Tag and Version are the source that would be built ("" when none was found); GoVersion the
	// toolchain's.
	Tag       string  `json:"tag,omitempty"`
	Version   string  `json:"version,omitempty"`
	GoVersion string  `json:"go_version,omitempty"`
	Checks    []Check `json:"checks"`
}

// OK reports whether every check passed.
func (r AgentReport) OK() bool {
	for _, c := range r.Checks {
		if !c.OK {
			return false
		}
	}
	return true
}

// Err is the first failed check as the *Error a rebuild ends with; nil when all passed.
func (r AgentReport) Err() *Error {
	var failed []string
	var first *Check
	for i, c := range r.Checks {
		if !c.OK {
			failed = append(failed, c.ID)
			if first == nil {
				first = &r.Checks[i]
			}
		}
	}
	if first == nil {
		return nil
	}
	return newErr(first.Code, StagePreflight, first.Title+": "+first.Detail, nil, "cause", "check_"+first.ID, "failed_checks", failed)
}

// AgentResult is what Build returns.
type AgentResult struct {
	Tag     string `json:"tag"`
	Version string `json:"version"`
	// Switched is true when the new agent is installed (and, when it could be restarted, healthy).
	Switched bool `json:"switched"`
	// Previous is the version the replaced agent reported ("" when unknown or there was none).
	Previous string `json:"previous"`
	// Restarted is true when the agent was restarted and answered with the new version; false when
	// there was nothing to restart (no running agent, or no unit): RestartNote says why.
	Restarted   bool   `json:"restarted"`
	RestartNote string `json:"restart_note,omitempty"`
	DryRun      bool   `json:"dry_run,omitempty"`

	Report   *AgentReport `json:"report,omitempty"`
	Warnings []string     `json:"-"`
	// LogPath is the build log.
	LogPath string `json:"-"`
}

func (b *AgentBuilder) progress(req AgentRequest, msg string) {
	if req.Progress != nil {
		req.Progress(Progress{Stage: StageBuild, Message: msg})
	}
}

func (b *AgentBuilder) roots() []string {
	if b.opt.KitRoots == nil {
		return nil
	}
	return b.opt.KitRoots()
}

func (b *AgentBuilder) finder() kit.SourceFinder {
	return kit.SourceFinder{FS: kitFS{b.opt.FS}, Roots: b.roots()}
}

func (b *AgentBuilder) binDir() string { return filepath.Join(b.opt.OptRoot, "bin") }

// ---- the checks ------------------------------------------------------------------------------

// found is what the checks learned, for the build that follows.
type agentPlan struct {
	src   kit.AgentSource
	tool  kit.GoToolchain
	insp  sourceInspection
	mod   string // the module path of agent/go.mod
	check AgentReport
}

// Check runs the checks of a rebuild (rebuild-kit.md section 4.3.4, --dry-run): the kit has the agent
// source and a Go toolchain for this architecture and their sha256 match what the kit recorded; the
// source has agent/go.mod, the command and every package of vendor/modules.txt (so `go build
// -mod=vendor` needs no network); the toolchain is new enough for go.mod; and the compile can run as
// the compile user. Nothing is written.
func (b *AgentBuilder) Check(ctx context.Context, req AgentRequest) (AgentReport, error) {
	p, err := b.check(ctx, req)
	return p.check, err
}

func (b *AgentBuilder) check(ctx context.Context, req AgentRequest) (agentPlan, error) {
	var p agentPlan
	add := func(id, title string, ok bool, code, detail string) {
		p.check.Checks = append(p.check.Checks, Check{ID: id, Title: title, OK: ok, Code: codeIf(!ok, code), Detail: detail})
	}
	finder := b.finder()

	src, serr := finder.FindAgentSource(req.Tag)
	switch {
	case serr == nil:
		p.src = src
		p.check.Tag, p.check.Version = src.Tag, src.Version
		add("source", "The agent source in the kit", true, "", fmt.Sprintf("%s (%s)", src.Name, humanBytes(src.Size)))
	case errors.Is(serr, kit.ErrSourceDamaged):
		add("source", "The agent source in the kit", false, doctor.KIT001, "a file of the kit does not match the checksum recorded for it: "+firstProblem(serr))
	default:
		what := "no source tarball local-ai-<tag>-src.tar.gz on any root"
		if req.Tag != "" {
			what = "no source tarball for " + req.Tag + " on any root"
		}
		add("source", "The agent source in the kit", false, CodeKitMissing, what)
	}
	tool, terr := finder.FindGoToolchain(b.opt.Arch)
	switch {
	case terr == nil:
		p.tool = tool
		p.check.GoVersion = tool.Version
		add("toolchain", "The Go toolchain in the kit", true, "", fmt.Sprintf("%s (%s)", tool.Name, humanBytes(tool.Size)))
	case errors.Is(terr, kit.ErrSourceDamaged):
		add("toolchain", "The Go toolchain in the kit", false, doctor.KIT001, "a file of the kit does not match the checksum recorded for it: "+firstProblem(terr))
	default:
		add("toolchain", "The Go toolchain in the kit", false, CodeKitMissing, "no go<version>.linux-"+b.opt.Arch+".tar.gz on any root")
	}

	if serr == nil {
		insp, ierr := b.inspect(ctx, src)
		switch {
		case ctx.Err() != nil:
			return p, ctx.Err()
		case ierr != nil:
			add("vendor", "The source is complete and vendored", false, CodeAgentSourceIncomplete, "the tarball cannot be read: "+ierr.Error())
		default:
			p.insp = insp
			p.mod = insp.module()
			if problems := insp.problems(); len(problems) > 0 {
				add("vendor", "The source is complete and vendored", false, CodeAgentSourceIncomplete, "missing: "+listMax(problems, 6))
			} else {
				add("vendor", "The source is complete and vendored", true, "", fmt.Sprintf("agent/go.mod, cmd/aiagent and %d vendored packages", insp.packages))
			}
			if terr == nil {
				if need := insp.goNeeds(); need != "" && !kit.GoVersionAtLeast(tool.Version, need) {
					add("go_version", "The Go toolchain is new enough", false, CodeAgentSourceIncomplete,
						fmt.Sprintf("the source needs Go %s, the kit has %s", need, tool.Version))
				} else {
					add("go_version", "The Go toolchain is new enough", true, "", "Go "+tool.Version)
				}
			}
		}
	}

	if b.opt.CompileUser != CompileAsCaller {
		_, _, uerr := b.opt.LookupUser(b.opt.CompileUser)
		_, perr := b.opt.Lookup("setpriv")
		switch {
		case uerr != nil:
			add("compile", "The compile can run as an unprivileged user", false, CodeAgentBuildFailed, "the user "+b.opt.CompileUser+" does not exist")
		case perr != nil:
			add("compile", "The compile can run as an unprivileged user", false, CodeAgentBuildFailed, "setpriv is not installed")
		default:
			add("compile", "The compile can run as an unprivileged user", true, "", "as "+b.opt.CompileUser)
		}
	}
	if free, err := b.opt.FreeBytes(nearestExisting(b.opt.FS, b.opt.ScratchDir)); err == nil {
		if free < KitMinScratchBytes {
			add("disk", "Free disk space for the build", false, CodeKitDiskLow, fmt.Sprintf("%s free in %s, 1 GB needed", humanBytes(int64(free)), b.opt.ScratchDir))
		} else {
			add("disk", "Free disk space for the build", true, "", humanBytes(int64(free))+" free in "+b.opt.ScratchDir)
		}
	}
	return p, nil
}

func codeIf(cond bool, code string) string {
	if cond {
		return code
	}
	return ""
}

func firstProblem(err error) string {
	s := err.Error()
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return s
}

func listMax(items []string, n int) string {
	if len(items) <= n {
		return strings.Join(items, ", ")
	}
	return fmt.Sprintf("%s and %d more", strings.Join(items[:n], ", "), len(items)-n)
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.0f MB", float64(n)/(1<<20))
	}
	return fmt.Sprintf("%d KB", (n+1023)/1024)
}

// nearestExisting walks up from p to the nearest directory that exists (the scratch folder may not
// have been created yet).
func nearestExisting(fsys FS, p string) string {
	for {
		if fi, err := fsys.Stat(p); err == nil && fi.IsDir() {
			return p
		}
		parent := filepath.Dir(p)
		if parent == p {
			return p
		}
		p = parent
	}
}

// ---- reading the source tarball without unpacking it ------------------------------------------

// sourceInspection is what one pass over the source tarball found. dirHasFile holds the folders that
// have a file directly in them (a package folder of vendor/ has one).
type sourceInspection struct {
	goMod, modules []byte
	files          map[string]bool
	dirHasFile     map[string]bool
	packages       int
}

// inspect reads the tarball once, with no disk, and returns what the completeness check needs. The
// stream is hashed on the way and must equal the digest of the verified file (a file replaced since
// the lookup is refused).
func (b *AgentBuilder) inspect(ctx context.Context, src kit.AgentSource) (sourceInspection, error) {
	f, err := b.opt.FS.Open(src.Path)
	if err != nil {
		return sourceInspection{}, err
	}
	defer f.Close()
	h := sha256.New()
	insp, err := inspectTarball(io.TeeReader(ctxReaderOf(ctx, f), h))
	if err != nil {
		return sourceInspection{}, err
	}
	if hex.EncodeToString(h.Sum(nil)) != src.SHA256 {
		return sourceInspection{}, errors.New("the file changed while it was being checked")
	}
	return insp, nil
}

type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}

func ctxReaderOf(ctx context.Context, r io.Reader) io.Reader { return ctxReader{ctx, r} }

// inspectTarball indexes a gzip-compressed tar stream: the paths of its files, which directories hold a
// file, and the text of agent/go.mod and agent/vendor/modules.txt. Names that are not plain relative
// paths are an error (the extraction would refuse them too). The whole stream is read, so a caller
// that hashes it sees every byte.
func inspectTarball(r io.Reader) (sourceInspection, error) {
	insp := sourceInspection{files: map[string]bool{}, dirHasFile: map[string]bool{}}
	br := bufio.NewReader(r)
	gz, err := gzip.NewReader(br)
	if err != nil {
		return insp, err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return insp, err
		}
		if hdr.Typeflag == tar.TypeXGlobalHeader || hdr.Typeflag == tar.TypeXHeader {
			continue
		}
		rel, ok := stripName(hdr.Name, 0)
		if !ok {
			return insp, fmt.Errorf("unsafe path %q in the archive", hdr.Name)
		}
		if rel == "" || hdr.Typeflag != tar.TypeReg {
			continue
		}
		insp.files[rel] = true
		insp.dirHasFile[path.Dir(rel)] = true // the folder that holds the file itself
		switch rel {
		case "agent/go.mod":
			insp.goMod, err = io.ReadAll(io.LimitReader(tr, agentModuleMax))
		case "agent/vendor/modules.txt":
			insp.modules, err = io.ReadAll(io.LimitReader(tr, agentModsMax))
		}
		if err != nil {
			return insp, err
		}
	}
	// Drain what the tar reader did not consume (the gzip trailer), so the caller's hash is complete.
	_, _ = io.Copy(io.Discard, br)
	return insp, nil
}

var (
	goModuleRe  = regexp.MustCompile(`(?m)^module\s+("?)([A-Za-z0-9._~/-]+)("?)\s*(?://.*)?$`)
	goVersionRe = regexp.MustCompile(`(?m)^go\s+([0-9]+\.[0-9]+(?:\.[0-9]+)?)\s*$`)
	goToolRe    = regexp.MustCompile(`(?m)^toolchain\s+go([0-9]+\.[0-9]+(?:\.[0-9]+)?)\s*$`)
)

// module is the module path of agent/go.mod ("" when it cannot be read).
func (s sourceInspection) module() string {
	if m := goModuleRe.FindSubmatch(s.goMod); m != nil {
		return string(m[2])
	}
	return ""
}

// goNeeds is the Go version agent/go.mod asks for: its toolchain line, else its go line.
func (s sourceInspection) goNeeds() string {
	if m := goToolRe.FindSubmatch(s.goMod); m != nil {
		return string(m[1])
	}
	if m := goVersionRe.FindSubmatch(s.goMod); m != nil {
		return string(m[1])
	}
	return ""
}

// requires lists the modules agent/go.mod requires (a `require` line or a block of them).
func (s sourceInspection) requires() []string {
	var out []string
	inBlock := false
	for _, line := range strings.Split(string(s.goMod), "\n") {
		line = strings.TrimSpace(line)
		if i := strings.Index(line, "//"); i >= 0 {
			line = strings.TrimSpace(line[:i])
		}
		switch {
		case line == "":
		case inBlock && line == ")":
			inBlock = false
		case inBlock:
			if f := strings.Fields(line); len(f) >= 2 {
				out = append(out, f[0])
			}
		case line == "require (":
			inBlock = true
		case strings.HasPrefix(line, "require "):
			if f := strings.Fields(strings.TrimPrefix(line, "require ")); len(f) >= 2 {
				out = append(out, f[0])
			}
		}
	}
	return out
}

// problems lists what is missing from the source for an offline build: agent/go.mod and its module
// line, the command agent/cmd/aiagent, agent/vendor/modules.txt, every module go.mod requires as a
// vendored module, and a folder with files for every package vendor/modules.txt lists. It also sets
// the number of vendored packages.
func (s *sourceInspection) problems() []string {
	var out []string
	if !s.files["agent/go.mod"] {
		out = append(out, "agent/go.mod")
	} else if s.module() == "" {
		out = append(out, "the module line of agent/go.mod")
	}
	if !s.dirHasFile["agent/cmd/aiagent"] {
		out = append(out, "agent/cmd/aiagent")
	}
	if !s.files["agent/vendor/modules.txt"] {
		return append(out, "agent/vendor/modules.txt")
	}
	headers := map[string]bool{}
	for _, line := range strings.Split(string(s.modules), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "" || strings.HasPrefix(line, "##"):
		case strings.HasPrefix(line, "# "):
			if f := strings.Fields(line); len(f) >= 2 {
				headers[f[1]] = true
			}
		default:
			s.packages++
			if !s.dirHasFile["agent/vendor/"+line] {
				out = append(out, "vendor/"+line)
			}
		}
	}
	for _, mod := range s.requires() {
		if !headers[mod] {
			out = append(out, "vendor/modules.txt: "+mod)
		}
	}
	sort.Strings(out)
	return out
}

// ---- the build -------------------------------------------------------------------------------

// Build runs the whole rebuild: the checks, then extract the toolchain and the source into the scratch
// directory, compile as the compile user, check that the result is a static program of this
// architecture, copy it next to the installed agent, start it once (`version --json` must report the
// tag's version), and swap it in: aiagent.prev keeps the agent it replaces, the agent restarts, and
// within 60 s it must answer /health with the new version, or aiagent.prev is put back and the agent
// is restarted again. A DryRun returns after the checks.
func (b *AgentBuilder) Build(ctx context.Context, req AgentRequest) (AgentResult, error) {
	plan, err := b.check(ctx, req)
	rep := plan.check
	res := AgentResult{Tag: rep.Tag, Version: rep.Version, Report: &rep, DryRun: req.DryRun}
	if err != nil {
		return res, err
	}
	if e := rep.Err(); e != nil {
		return res, e
	}
	if req.DryRun {
		return res, nil
	}
	res.DryRun = false

	fsys := b.opt.FS
	scratch := b.opt.ScratchDir
	if err := fsys.MkdirAll(scratch, 0o755); err != nil {
		return res, newErr(CodeAgentBuildFailed, StagePreflight, "The build folder "+scratch+" could not be created.", err, "cause", "filesystem")
	}
	release, err := lockFile(filepath.Join(scratch, agentLockName))
	if err != nil {
		if errors.Is(err, errLockHeld) {
			return res, newErr(CodeAgentBuildFailed, StagePreflight, "Another agent build is already running on this computer.", err, "cause", "locked")
		}
		return res, newErr(CodeAgentBuildFailed, StagePreflight, "The build lock could not be taken.", err, "cause", "lock")
	}
	defer release()

	goDir := filepath.Join(scratch, "go")
	treeDir := filepath.Join(scratch, "local-ai-"+plan.src.Tag)
	logPath := filepath.Join(scratch, agentBuildLog)
	res.LogPath = logPath
	defer func() {
		_ = fsys.RemoveAll(goDir)
		_ = fsys.RemoveAll(treeDir)
		_ = fsys.RemoveAll(goDir + suffixTmp)
		_ = fsys.RemoveAll(treeDir + suffixTmp)
	}()
	var log strings.Builder
	logf := func(format string, a ...any) { fmt.Fprintf(&log, format, a...) }
	writeLog := func() { _ = fsys.WriteFile(logPath, []byte(log.String()), 0o644) }
	defer writeLog()

	cancelled := func(e *Error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return e
	}

	b.progress(req, "Unpacking the Go toolchain from the kit")
	if e := b.extract(ctx, plan.tool.Path, plan.tool.SHA256, goDir, 1, "the Go toolchain"); e != nil {
		return res, cancelled(e)
	}
	goBin := filepath.Join(goDir, "bin", "go")
	if fi, err := fsys.Lstat(goBin); err != nil || !fi.Mode().IsRegular() {
		return res, newErr(CodeAgentSourceIncomplete, StageSource, "The Go toolchain in the kit has no bin/go.", err, "cause", "no_go")
	}
	b.progress(req, "Unpacking the agent source "+plan.src.Tag+" from the kit")
	if e := b.extract(ctx, plan.src.Path, plan.src.SHA256, treeDir, 0, "the agent source"); e != nil {
		return res, cancelled(e)
	}

	// The compile runs as the compile user over a tree it owns; the tree is root's again before the
	// result is looked at.
	drop, uid, gid, setpriv, e := b.compileUser()
	if e != nil {
		return res, e
	}
	cache := filepath.Join(treeDir, ".cache")
	for _, d := range []string{"go-build", "gopath", "home"} {
		if err := fsys.MkdirAll(filepath.Join(cache, d), 0o755); err != nil {
			return res, newErr(CodeAgentBuildFailed, StagePreflight, "The build folders could not be created.", err, "cause", "filesystem")
		}
	}
	if drop {
		if err := chownTree(fsys, treeDir, uid, gid); err != nil {
			return res, newErr(CodeAgentBuildFailed, StagePreflight, "The build tree could not be handed to "+b.opt.CompileUser+": "+err.Error(), err, "cause", "chown")
		}
	}
	newBin := filepath.Join(treeDir, "aiagent.new")
	ldflags := fmt.Sprintf("-s -w -X %s/internal/app.Version=%s", plan.mod, plan.src.Version)
	goArgs := []string{"build", "-C", filepath.Join(treeDir, "agent"), "-trimpath", "-ldflags", ldflags, "-o", newBin, "./cmd/aiagent"}
	env := []string{
		"PATH=" + filepath.Join(goDir, "bin") + ":" + execx.FixedPath, "LC_ALL=C",
		"GOTOOLCHAIN=local", "GOFLAGS=-mod=vendor", "CGO_ENABLED=0", "GOPROXY=off", "GOENV=off", "GOTELEMETRY=off",
		"GOCACHE=" + filepath.Join(cache, "go-build"), "GOPATH=" + filepath.Join(cache, "gopath"), "HOME=" + filepath.Join(cache, "home"),
	}
	prog, args := goBin, goArgs
	if drop {
		prog, args = setpriv, append([]string{"--reuid", b.opt.CompileUser, "--regid", b.opt.CompileUser, "--clear-groups", goBin}, goArgs...)
	}
	b.progress(req, "Compiling the agent (no internet is used)")
	logf("$ %s %s\n", goBin, strings.Join(goArgs, " "))
	out, runErr := b.opt.Exec.Run(ctx, execx.Cmd{Path: prog, Args: args, Timeout: b.opt.Timing.Build, MaxOut: maxToolOut, Env: env})
	logf("%s%s", out.Stdout, out.Stderr)
	if drop {
		if err := chownTree(fsys, treeDir, 0, 0); err != nil {
			return res, newErr(CodeAgentBuildFailed, StageInstall, "The build tree could not be handed back to root.", err, "cause", "chown")
		}
	}
	if ctx.Err() != nil {
		return res, ctx.Err()
	}
	if ce := b.compileErr(out, runErr, logPath, &log); ce != nil {
		return res, ce
	}

	// What the compile produced is checked before it is run or copied anywhere.
	fi, err := fsys.Lstat(newBin)
	if err != nil || !fi.Mode().IsRegular() || fi.Size() == 0 || fi.Size() > upd.MaxBinaryBytes {
		return res, newErr(CodeAgentBuildFailed, StageInstall, "The compile produced no usable aiagent program.", err, "cause", "no_output", "log_path", logPath)
	}
	if why := staticELF(fsys, newBin, b.opt.Arch); why != "" {
		logf("static check: %s\n", why)
		return res, newErr(CodeAgentUnusable, StageRunpath, "The new agent is not a self-contained program for this computer ("+why+"), so it was not installed.",
			nil, "cause", "not_static", "log_path", logPath)
	}

	binDir := b.binDir()
	if err := fsys.MkdirAll(binDir, 0o755); err != nil {
		return res, newErr(CodeAgentBuildFailed, StageInstall, "The folder "+binDir+" could not be created.", err, "cause", "filesystem")
	}
	staged := filepath.Join(binDir, "aiagent.new")
	if err := copyBinary(fsys, newBin, staged); err != nil {
		_ = fsys.RemoveAll(staged)
		return res, newErr(CodeAgentBuildFailed, StageInstall, "The new agent could not be copied next to the installed one.", err, "cause", "install", "log_path", logPath)
	}
	b.progress(req, "Testing the new agent")
	got, serr := b.smoke(ctx, staged, plan.src.Version)
	if serr != nil {
		_ = fsys.RemoveAll(staged)
		logf("smoke: %s\n", serr)
		return res, cancelled(newErr(CodeAgentUnusable, StageSmoke, "The new agent did not pass its start-up test ("+serr.Error()+"), so nothing was replaced.",
			serr, "cause", "smoke", "log_path", logPath))
	}
	logf("smoke: aiagent.new reports %s\n", got)

	b.progress(req, "Switching to the new agent")
	return b.swap(ctx, req, plan, res, logf)
}

// compileUser resolves the unprivileged user of the compile ("-" runs it as the caller).
func (b *AgentBuilder) compileUser() (drop bool, uid, gid int, setpriv string, e *Error) {
	if b.opt.CompileUser == CompileAsCaller {
		return false, 0, 0, "", nil
	}
	fail := func(cause string, err error) *Error {
		return newErr(CodeAgentBuildFailed, StagePreflight, "The compile must run as the user "+b.opt.CompileUser+", and that could not be arranged: "+err.Error(),
			err, "cause", cause, "user", b.opt.CompileUser)
	}
	uid, gid, err := b.opt.LookupUser(b.opt.CompileUser)
	if err != nil {
		return false, 0, 0, "", fail("no_compile_user", err)
	}
	path, err := b.opt.Lookup("setpriv")
	if err != nil {
		return false, 0, 0, "", fail("no_setpriv", err)
	}
	return true, uid, gid, path, nil
}

// vendorFailureHints are what `go build` says when something is not in vendor/ and it would have to be
// downloaded (GOPROXY=off makes that fail at once).
var vendorFailureHints = []string{
	"inconsistent vendoring", "GOPROXY=off", "lookup disabled", "cannot find module providing package", "no required module provides package",
	"vendor/modules.txt", "missing go.sum entry", "updates to go.mod needed", "cannot find package", "is not in std",
}

// compileErr turns the end of `go build` into the error of the step (nil when it succeeded): a failure
// that says something is not vendored is LAI-KIT-030, any other is LAI-KIT-020, with the log's tail.
func (b *AgentBuilder) compileErr(out execx.Result, runErr error, logPath string, log *strings.Builder) *Error {
	text := string(out.Stdout) + string(out.Stderr)
	tail := lastLines(text, logTailLines)
	details := func(cause string) []any { return []any{"cause", cause, "log_path", logPath, "log_tail", tail} }
	switch {
	case errors.Is(runErr, execx.ErrTimeout):
		fmt.Fprintf(log, "[timed out after %s]\n", b.opt.Timing.Build)
		return newErr(CodeAgentBuildFailed, StageBuild, fmt.Sprintf("Building the agent failed: go did not finish within %s.", b.opt.Timing.Build), runErr, details("timeout")...)
	case runErr != nil:
		fmt.Fprintf(log, "[could not run: %v]\n", runErr)
		return newErr(CodeAgentBuildFailed, StageBuild, "Building the agent failed: go could not be started.", runErr, details("exec")...)
	case out.ExitCode == 0:
		return nil
	}
	fmt.Fprintf(log, "[exit status %d]\n", out.ExitCode)
	for _, h := range vendorFailureHints {
		if strings.Contains(text, h) {
			return newErr(CodeAgentSourceIncomplete, StageBuild, "The agent source in the kit is not fully vendored, so it cannot be built without internet.",
				nil, append(details("not_vendored"), "exit_code", out.ExitCode)...)
		}
	}
	return newErr(CodeAgentBuildFailed, StageBuild, fmt.Sprintf("Building the agent failed: go exited with status %d.", out.ExitCode), nil,
		append(details("exit"), "exit_code", out.ExitCode)...)
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// extract unpacks a verified archive into dest (through dest+".tmp" and a rename), hashing the bytes
// that are extracted: they must equal the digest the kit recorded, so a file replaced after the lookup
// is refused (LAI-KIT-001) and nothing stays behind.
func (b *AgentBuilder) extract(ctx context.Context, archive, wantSHA256, dest string, strip int, what string) *Error {
	fsys := b.opt.FS
	tmp := dest + suffixTmp
	_ = fsys.RemoveAll(tmp)
	_ = fsys.RemoveAll(dest)
	fail := func(code, msg string, err error, cause string) *Error {
		_ = fsys.RemoveAll(tmp)
		return newErr(code, StageSource, msg, err, "cause", cause, "file", archive)
	}
	if err := fsys.MkdirAll(tmp, 0o755); err != nil {
		return fail(CodeAgentBuildFailed, "The build folder for "+what+" could not be created.", err, "filesystem")
	}
	f, err := fsys.Open(archive)
	if err != nil {
		return fail(CodeKitMissing, "The file of "+what+" cannot be opened: "+err.Error(), err, "unreadable")
	}
	defer f.Close()
	h := sha256.New()
	xerr := extractTar(fsys, io.TeeReader(ctxReader{ctx, f}, h), tmp, strip)
	if xerr == nil {
		_, xerr = io.Copy(io.Discard, io.TeeReader(ctxReader{ctx, f}, h))
	}
	if ctx.Err() != nil {
		_ = fsys.RemoveAll(tmp)
		return newErr(CodeAgentBuildFailed, StageSource, "cancelled", ctx.Err())
	}
	if xerr != nil {
		return fail(CodeSourceDamaged, "The kit's file for "+what+" is damaged or not safe to unpack: "+xerr.Error()+". Run `aictl kit verify --full`.", xerr, "unsafe_archive")
	}
	if hex.EncodeToString(h.Sum(nil)) != wantSHA256 {
		return fail(CodeSourceDamaged, "The kit's file for "+what+" changed while it was read. Run `aictl kit verify --full`.", nil, "digest")
	}
	if err := fsys.Rename(tmp, dest); err != nil {
		return fail(CodeAgentBuildFailed, "The unpacked "+what+" could not be moved into place.", err, "filesystem")
	}
	return nil
}

// staticELF checks that the program is a self-contained ELF executable for the architecture: no
// interpreter, no shared library. The dynamic section is read with debug/elf; nothing is run. It
// returns "" when the program passes, else the reason in a few words.
func staticELF(fsys FS, p, arch string) string {
	f, err := fsys.Open(p)
	if err != nil {
		return "it cannot be read"
	}
	defer f.Close()
	ef, err := elf.NewFile(f)
	if err != nil {
		return "it is not a program"
	}
	defer ef.Close()
	want := map[string]elf.Machine{"amd64": elf.EM_X86_64, "arm64": elf.EM_AARCH64}[arch]
	if want == 0 || ef.Machine != want {
		return "it is built for another processor"
	}
	for _, pr := range ef.Progs {
		if pr.Type == elf.PT_INTERP {
			return "it needs a dynamic loader"
		}
	}
	if libs, err := ef.ImportedLibraries(); err != nil || len(libs) > 0 {
		return "it needs shared libraries"
	}
	return ""
}

// copyBinary copies src to dst (0755), syncing the file before it is closed.
func copyBinary(fsys FS, src, dst string) error {
	in, err := fsys.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := fsys.Create(dst, 0o755)
	if err != nil {
		return err
	}
	_, cerr := io.Copy(out, io.LimitReader(in, upd.MaxBinaryBytes+1))
	if sy, ok := out.(interface{ Sync() error }); ok && cerr == nil {
		cerr = sy.Sync()
	}
	if err := out.Close(); cerr == nil {
		cerr = err
	}
	if cerr != nil {
		_ = fsys.RemoveAll(dst)
	}
	return cerr
}

// agentVersionInfo is `aiagent version --json`.
type agentVersionInfo struct {
	Version string `json:"version"`
	OS      string `json:"os"`
	Arch    string `json:"arch"`
}

// smoke runs `<binary> version --json` and requires the version of the tag, linux and this
// architecture. It returns the version the program reports.
func (b *AgentBuilder) smoke(ctx context.Context, binary, want string) (string, error) {
	got, err := b.versionOf(ctx, binary)
	if err != nil {
		return "", err
	}
	switch {
	case got.Version != want:
		return "", fmt.Errorf("it reports version %q, not %q", clipText(got.Version, 32), want)
	case got.Arch != b.opt.Arch:
		return "", fmt.Errorf("it reports architecture %q, not %q", clipText(got.Arch, 16), b.opt.Arch)
	case got.OS != "linux":
		return "", fmt.Errorf("it reports os %q", clipText(got.OS, 16))
	}
	return got.Version, nil
}

func (b *AgentBuilder) versionOf(ctx context.Context, binary string) (agentVersionInfo, error) {
	res, err := b.opt.Exec.Run(ctx, execx.Cmd{Path: binary, Args: []string{"version", "--json"}, Timeout: b.opt.Timing.Smoke, MaxOut: 64 << 10, Env: toolEnv()})
	if err != nil {
		return agentVersionInfo{}, err
	}
	if res.ExitCode != 0 {
		return agentVersionInfo{}, fmt.Errorf("version --json exited with status %d", res.ExitCode)
	}
	var info agentVersionInfo
	if err := json.Unmarshal(res.Stdout, &info); err != nil {
		return agentVersionInfo{}, fmt.Errorf("the output of version --json is not JSON: %w", err)
	}
	return info, nil
}

func clipText(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// sameVersion compares two version texts by semver, else by text.
func sameVersion(a, b string) bool {
	va, ea := upd.ParseVersion(strings.TrimPrefix(a, "v"))
	vb, eb := upd.ParseVersion(strings.TrimPrefix(b, "v"))
	if ea != nil || eb != nil {
		return a == b
	}
	return va.Compare(vb) == 0
}

// ---- the switch -------------------------------------------------------------------------------

// swap installs the tested aiagent.new, restarts the agent and checks that it answers with the new
// version; when it does not, the previous agent is put back and restarted.
func (b *AgentBuilder) swap(ctx context.Context, req AgentRequest, plan agentPlan, res AgentResult, logf func(string, ...any)) (AgentResult, error) {
	fsys := b.opt.FS
	binDir := b.binDir()
	agent, prev, staged := filepath.Join(binDir, "aiagent"), filepath.Join(binDir, "aiagent.prev"), filepath.Join(binDir, "aiagent.new")
	target := map[string]string{"tag": plan.src.Tag, "version": plan.src.Version}
	fail := func(e *Error) (AgentResult, error) {
		b.audit(req, audit.ResultFail, e.Code, target)
		return res, e
	}

	hadAgent := false
	if fi, err := fsys.Lstat(agent); err == nil && fi.Mode().IsRegular() {
		hadAgent = true
		if info, err := b.versionOf(ctx, agent); err == nil {
			res.Previous = info.Version
			target["previous"] = info.Version
		}
		if err := copyBinary(fsys, agent, prev+suffixTmp); err != nil {
			_ = fsys.RemoveAll(staged)
			return fail(newErr(CodeAgentBuildFailed, StageSwitch, "The installed agent could not be kept as aiagent.prev, so nothing was replaced.", err, "cause", "keep_prev", "log_path", res.LogPath))
		}
		if err := fsys.Rename(prev+suffixTmp, prev); err != nil {
			_ = fsys.RemoveAll(prev + suffixTmp)
			_ = fsys.RemoveAll(staged)
			return fail(newErr(CodeAgentBuildFailed, StageSwitch, "The installed agent could not be kept as aiagent.prev, so nothing was replaced.", err, "cause", "keep_prev", "log_path", res.LogPath))
		}
	}
	wasUp := false
	if b.opt.Health != nil {
		_, herr := b.opt.Health(ctx)
		wasUp = herr == nil
	}
	if err := fsys.Rename(staged, agent); err != nil {
		_ = fsys.RemoveAll(staged)
		return fail(newErr(CodeAgentBuildFailed, StageSwitch, "The new agent could not be put in place, so nothing was replaced.", err, "cause", "rename", "log_path", res.LogPath))
	}
	res.Switched = true
	logf("installed %s as %s\n", plan.src.Version, agent)

	// Restart. An agent that was not running, or a node with no unit yet (a node rebuilt from scratch),
	// has nothing to restart: the verified program is installed and that is told, not failed.
	rerr := b.restart(ctx)
	if rerr != nil && !wasUp {
		res.RestartNote = "the agent was not running or has no service on this computer yet: " + rerr.Error()
		b.audit(req, audit.ResultOK, "", target)
		return res, nil
	}
	if rerr == nil && b.opt.Health == nil {
		res.RestartNote = "the agent was restarted; its health was not checked"
		res.Warnings = append(res.Warnings, "The agent was restarted, but its health could not be checked from here.")
		b.audit(req, audit.ResultOK, "", target)
		return res, nil
	}
	var cause error = rerr
	if rerr == nil {
		b.progress(req, "Waiting for the new agent to answer")
		cause = b.waitHealthy(ctx, plan.src.Version)
	}
	if cause == nil {
		res.Restarted = true
		b.audit(req, audit.ResultOK, "", target)
		return res, nil
	}
	if ctx.Err() != nil {
		return res, ctx.Err()
	}

	// The new agent did not come up: put the previous one back.
	logf("health check failed: %v\n", cause)
	target["rolled_back"] = "false"
	if !hadAgent {
		return fail(newErr(CodeAgentUnusable, StageCheck,
			"The new agent did not become healthy, and there was no earlier agent to put back. Start the kit's release agent with `sudo aictl kit install agent`.",
			cause, "cause", "health", "rolled_back", false, "log_path", res.LogPath))
	}
	if err := b.restorePrev(); err != nil {
		return fail(newErr(CodeAgentUnusable, StageCheck,
			"The new agent did not become healthy and the previous one could not be put back: run `sudo cp "+prev+" "+agent+"` and restart the agent.",
			errors.Join(cause, err), "cause", "health", "rolled_back", false, "log_path", res.LogPath))
	}
	rbErr := b.restart(ctx)
	res.Switched = false
	target["rolled_back"] = "true"
	msg := "The new agent did not become healthy, so the previous agent was put back."
	if rbErr != nil {
		msg += " It could not be restarted: run `sudo systemctl restart " + AgentUnit + "`."
	}
	return fail(newErr(CodeAgentUnusable, StageCheck, msg, cause, "cause", "health", "rolled_back", true,
		"previous_version", res.Previous, "log_path", res.LogPath))
}

// restorePrev puts aiagent.prev back as aiagent (copy to aiagent.new, then rename).
func (b *AgentBuilder) restorePrev() error {
	fsys := b.opt.FS
	binDir := b.binDir()
	prev := filepath.Join(binDir, "aiagent.prev")
	if fi, err := fsys.Lstat(prev); err != nil || !fi.Mode().IsRegular() || fi.Size() == 0 {
		return fmt.Errorf("the previous agent %s is not available", prev)
	}
	tmp := filepath.Join(binDir, "aiagent.new")
	if err := copyBinary(fsys, prev, tmp); err != nil {
		return err
	}
	if err := fsys.Rename(tmp, filepath.Join(binDir, "aiagent")); err != nil {
		_ = fsys.RemoveAll(tmp)
		return err
	}
	return nil
}

// restart restarts the agent unit with systemctl.
func (b *AgentBuilder) restart(ctx context.Context) error {
	bin, err := b.opt.Lookup("systemctl")
	if err != nil {
		return err
	}
	res, err := b.opt.Exec.Run(ctx, execx.Cmd{Path: bin, Args: []string{"restart", AgentUnit}, Timeout: b.opt.Timing.Restart, MaxOut: 64 << 10})
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("systemctl restart %s exited with status %d", AgentUnit, res.ExitCode)
	}
	return nil
}

// waitHealthy polls the agent's health until it reports the version and is not starting, or HealthWait
// has passed on the injected clock.
func (b *AgentBuilder) waitHealthy(ctx context.Context, version string) error {
	start := b.opt.Clock()
	deadline := start.Add(b.opt.Timing.HealthWait)
	var last error
	for {
		h, err := b.opt.Health(ctx)
		switch {
		case err != nil:
			last = err
		case sameVersion(h.Version, version) && !h.Starting:
			return nil
		case h.Starting:
			last = errors.New("the agent is still starting")
		default:
			last = fmt.Errorf("the agent reports version %q, not %q", clipText(h.Version, 32), version)
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !b.opt.Clock().Add(b.opt.Timing.Poll).Before(deadline) {
			return fmt.Errorf("no healthy answer within %s: %w", b.opt.Timing.HealthWait, last)
		}
		b.opt.Sleep(ctx, b.opt.Timing.Poll)
	}
}

func (b *AgentBuilder) audit(req AgentRequest, result audit.Result, code string, target map[string]string) {
	if b.opt.Audit == nil {
		return
	}
	actor := req.Actor
	if actor.Kind == "" {
		actor = audit.CLIRoot()
	}
	if err := b.opt.Audit.Write(audit.Event{Action: audit.ActionAgentBuildSwitch, Result: result, Code: code, Actor: actor, Target: target}); err != nil {
		b.log.Warn("could not write the audit line", "error", err)
	}
}
