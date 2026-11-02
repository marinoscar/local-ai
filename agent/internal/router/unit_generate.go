package router

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/state"
)

// Modes of the generated unit and its cache directory (llama-router.md section 3.1).
const (
	unitFileMode  os.FileMode = 0o644 // root
	cacheDirMode  os.FileMode = 0o750 // localai
	cacheListSize             = 5     // names listed in the details of LAI-LLM-024
)

// Doctor codes of the unit generator. Like the codes of codes.go they alias internal/doctor/codes.go,
// the one file that spells a "LAI-..." literal.
const (
	// CodeUnitFailed: the unit (or a part of it) could not be generated or written. It is the code
	// the preset generator uses for the file it owns (the text of LAI-LLM-007 names the model list;
	// the details name the file).
	CodeUnitFailed = CodePresetFailed
	// CodeUnitHandEdited: the unit was edited by hand; a copy was saved and the unit regenerated.
	CodeUnitHandEdited = CodePresetHandEdited
	// CodeUnitRestart: the unit changed in a way a running llama only picks up when restarted.
	CodeUnitRestart = CodePresetRestart
	// CodeBinaryMissing: the llama-server program the unit starts is missing or not executable.
	CodeBinaryMissing Code = doctor.LLM002
	// CodeSingleMode: the node runs in single-model mode (info).
	CodeSingleMode Code = doctor.LLM023
)

// UnitPaths are the files the unit generator owns.
type UnitPaths struct {
	// Unit is /etc/systemd/system/localai-llama.service; Unit+".prev" and
	// Unit+".hand-edited-<ts>" sit beside it.
	Unit string
	// CacheDir is /var/lib/localai/llama-cache.
	CacheDir string
}

// DefaultUnitPaths returns the paths of llama-router.md section 3.1 under root ("" means "/").
func DefaultUnitPaths(root string) UnitPaths {
	if root == "" {
		root = "/"
	}
	return UnitPaths{
		Unit:     filepath.Join(root, "etc", "systemd", "system", "localai-llama.service"),
		CacheDir: filepath.Join(root, "var", "lib", "localai", "llama-cache"),
	}
}

// UnitHashState is the slice of state.json the unit generator keeps: llama.unit_sha256, the hash of
// the file it last wrote, which is how a hand edit is noticed.
type UnitHashState interface {
	// UnitSHA256 returns the recorded hash, "" when none.
	UnitSHA256() string
	// SetUnitSHA256 records the hash durably.
	SetUnitSHA256(sha string) error
}

// StateUnit is the UnitHashState over the agent's state store.
type StateUnit struct{ Store *state.Store }

// UnitSHA256 implements UnitHashState.
func (s StateUnit) UnitSHA256() string { return s.Store.Snapshot().Llama.UnitSHA256 }

// SetUnitSHA256 implements UnitHashState.
func (s StateUnit) SetUnitSHA256(sha string) error {
	return s.Store.MutateSync(func(st *state.State) error {
		st.Llama.UnitSHA256 = sha
		return nil
	})
}

// UnitGeneratorOptions configures NewUnitGenerator. Every field except State is optional.
type UnitGeneratorOptions struct {
	Paths UnitPaths
	// FS is the filesystem seam; default config.OSFS. MkdirAll creates the cache directory's
	// parents; default os.MkdirAll.
	FS       config.FS
	MkdirAll func(path string, perm os.FileMode) error
	// LocalAI is the unit's user, for the readability check of a single-mode model; nil means
	// unknown: only the o+rx bits count.
	LocalAI *Identity
	// UnitOwner is the owner of the unit (root:root in production); CacheOwner of the cache
	// directory (localai:localai). Default -1, -1.
	UnitOwner, CacheOwner Ownership
	// State records unit_sha256. Required.
	State UnitHashState
	// Systemd runs daemon-reload after the unit changed (internal/services.Manager); nil skips it.
	Systemd DaemonReloader
	// Alerts receives the doctor codes.
	Alerts AlertSink
	Clock  config.Clock
	Logger *slog.Logger
}

// UnitResult is what one Generate did.
type UnitResult struct {
	Path string
	Mode string
	// Written is true when the unit file was replaced; SHA256 is the hash of the file now on disk.
	Written bool
	SHA256  string
	// Reloaded is true when daemon-reload ran and succeeded; ReloadErr is its failure (the file is
	// already written; the reload is retried by the next Generate).
	Reloaded  bool
	ReloadErr error
	// RestartRequired is true when a running llama must be restarted to use the new unit
	// (RestartRequired); Changed names the [Service] directives that differ. The agent never
	// restarts llama by itself: LAI-AGT-016 is raised.
	RestartRequired bool
	Changed         []string
	// HandEditedCopy is the path the previous file was saved to when it had been edited by hand.
	HandEditedCopy string
	// Cache is the state of the cache directory after the run.
	Cache CacheStatus
	// DroppedAliases are the single-mode aliases that were left out.
	DroppedAliases []string
	Findings       []PresetFinding
}

// CacheStatus is what Generate found in LLAMA_CACHE.
type CacheStatus struct {
	Path string
	// Created is true when the directory was created by this run; Entries counts what it holds
	// (it must be 0: LAI-LLM-024 otherwise). Nothing in it is ever deleted.
	Created bool
	Entries int
}

// UnitGenerator renders localai-llama.service, writes it atomically, runs daemon-reload when the
// file changed and keeps the cache directory in order (llama-router.md sections 3.1, 3.2 and 3.4;
// agent.md section 4.3.4). It is safe for concurrent use; calls are serialised.
type UnitGenerator struct {
	opt   UnitGeneratorOptions
	log   *slog.Logger
	clock config.Clock

	mu     sync.Mutex
	raised map[findingKey]bool
	// reloadPending is true after a daemon-reload that failed: the next run reloads again even
	// though the file is unchanged.
	reloadPending bool
}

// NewUnitGenerator returns a UnitGenerator.
func NewUnitGenerator(opt UnitGeneratorOptions) (*UnitGenerator, error) {
	if opt.State == nil {
		return nil, errors.New("router: the unit generator needs a UnitHashState")
	}
	if opt.Paths.Unit == "" {
		return nil, errors.New("router: the unit generator needs the path of the unit")
	}
	if opt.FS == nil {
		opt.FS = config.OSFS{}
	}
	if opt.MkdirAll == nil {
		opt.MkdirAll = os.MkdirAll
	}
	if opt.LocalAI == nil {
		opt.LocalAI = &Identity{UID: -1, GID: -1}
	}
	g := &UnitGenerator{opt: opt, clock: opt.Clock, raised: map[findingKey]bool{}}
	if g.clock == nil {
		g.clock = config.RealClock{}
	}
	l := opt.Logger
	if l == nil {
		l = slog.Default()
	}
	g.log = l.With("component", "router")
	return g, nil
}

// Generate renders the unit for the inputs and brings the disk to it (llama-router.md section
// 3.2, agent.md section 4.3.4):
//
//  1. make sure the cache directory exists (localai 0750) and report what it holds;
//  2. check that the binary the unit starts is there;
//  3. render, refusing inputs that cannot be a unit (a single-mode model whose files the unit's user
//     cannot read is refused too: the unit that works is never replaced by one that cannot serve);
//  4. read the current file; if it is not what was last written, save it as
//     localai-llama.service.hand-edited-<ts> and go on;
//  5. replace the file atomically, keeping the previous one as localai-llama.service.prev, only when
//     the bytes differ, and record unit_sha256;
//  6. run daemon-reload when the file changed;
//  7. if a running llama would need a restart for the change, raise LAI-AGT-016.
//
// It returns an error only when the unit is not in the state the inputs call for (it could not be
// rendered, read, saved aside or written). The cache directory, the binary and daemon-reload are
// findings in the Result and retried by the next call. The Result is valid with an error too.
func (g *UnitGenerator) Generate(ctx context.Context, in UnitInputs) (res UnitResult, err error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	res.Path = g.opt.Paths.Unit
	var fnd collector
	defer func() {
		res.Findings = fnd.list
		g.settle(fnd.list)
	}()
	fail := func(code Code, subject string, cause error, details map[string]any) (UnitResult, error) {
		if details == nil {
			details = map[string]any{}
		}
		details["error"] = clip(cause.Error(), 300)
		fnd.add(code, subject, details)
		g.log.Error("could not generate the llama unit", "subject", subject, "error", cause)
		return res, fmt.Errorf("router: %s: %w", subject, cause)
	}
	now := g.clock.Now().UTC()

	mode := in.Mode
	if mode == "" {
		mode = LlamaModeRouter
	}
	res.Mode = mode
	if mode == LlamaModeSingle {
		fnd.add(CodeSingleMode, "", map[string]any{"mode": mode})
	}

	// Step 1 and 2: the things around the unit that never stop it from being written.
	g.ensureCache(&res, &fnd)
	binary := in.Binary
	if binary == "" {
		binary = DefaultLlamaBinary
	}
	g.checkBinary(binary, &fnd)

	// Step 3: render.
	rendered, rerr := RenderUnit(in)
	if rerr != nil {
		details := map[string]any{"path": g.opt.Paths.Unit}
		var ue *UnitError
		if errors.As(rerr, &ue) {
			details["subject"], details["reason"] = ue.Subject, ue.Reason
		}
		return fail(CodeUnitFailed, "unit", rerr, details)
	}
	for _, p := range rendered.DroppedAliases {
		res.DroppedAliases = append(res.DroppedAliases, p.alias)
		fnd.add(CodeUnitFailed, "alias:"+p.alias, map[string]any{"model": p.model, "alias": p.alias, "reason": p.reason})
	}
	for _, p := range rendered.Paths {
		if cerr := CheckReadable(g.opt.FS, *g.opt.LocalAI, p); cerr != nil {
			prob := &AccessProblem{Path: p, Reason: AccessError}
			errors.As(cerr, &prob)
			model := ""
			if in.Single != nil && in.Single.Model != nil {
				model = in.Single.Model.ID
			}
			fnd.add(CodeFilesMissing, model, map[string]any{"model": model, "path": prob.Path, "reason": prob.Reason})
			return res, fmt.Errorf("router: single mode: the model's files are not readable (%s): %s", prob.Reason, prob.Path)
		}
	}
	data := rendered.Data

	// Step 4: the current file, and whether someone edited it.
	cur, cerr := g.opt.FS.ReadFile(g.opt.Paths.Unit)
	haveCur := cerr == nil
	if cerr != nil && !errors.Is(cerr, os.ErrNotExist) {
		return fail(CodeUnitFailed, "read unit", cerr, map[string]any{"path": g.opt.Paths.Unit})
	}
	if haveCur {
		recorded := g.opt.State.UnitSHA256()
		edited := false
		if recorded != "" {
			edited = SHA256Hex(cur) != recorded
		} else {
			// No record (a fresh state file): a file that carries our header is ours.
			edited = !bytes.HasPrefix(cur, []byte(unitHeaderPrefix))
		}
		// A file that already holds what we would write loses nothing.
		if edited && bytes.Equal(cur, data) {
			edited = false
		}
		if edited {
			saved, serr := g.saveAside(cur, now)
			if serr != nil {
				// Never overwrite what could not be kept.
				return fail(CodeUnitFailed, "save the hand-edited unit", serr, map[string]any{"path": g.opt.Paths.Unit})
			}
			res.HandEditedCopy = saved
			details := map[string]any{"path": g.opt.Paths.Unit, "saved_as": saved, "file": "unit"}
			fnd.add(CodeUnitHandEdited, "unit", details)
			g.raise(CodeUnitHandEdited, "unit", details)
			g.log.Warn("the llama unit was edited by hand; a copy was saved", "saved_as", saved)
		}
	}

	// Steps 5 and 6: write unless the file already says this; reload when it changed.
	switch {
	case haveCur && bytes.Equal(cur, data):
		res.SHA256 = SHA256Hex(cur)
		if g.opt.State.UnitSHA256() != res.SHA256 {
			if serr := g.opt.State.SetUnitSHA256(res.SHA256); serr != nil {
				g.log.Error("could not record the hash of the llama unit", "error", serr)
			}
		}
		if g.reloadPending {
			g.reload(ctx, &res, &fnd)
		}
		return res, nil
	default:
		if haveCur {
			if perr := config.WriteFileAtomicFS(g.opt.FS, g.opt.Paths.Unit+".prev", cur, unitFileMode, g.opt.UnitOwner.UID, g.opt.UnitOwner.GID); perr != nil && !errors.Is(perr, config.ErrNotDurable) {
				g.log.Warn("could not keep the previous llama unit", "error", perr)
			}
		}
		if werr := config.WriteFileAtomicFS(g.opt.FS, g.opt.Paths.Unit, data, unitFileMode, g.opt.UnitOwner.UID, g.opt.UnitOwner.GID); werr != nil {
			if !errors.Is(werr, config.ErrNotDurable) {
				return fail(CodeUnitFailed, "write unit", werr, map[string]any{"path": g.opt.Paths.Unit})
			}
			g.log.Warn("the llama unit was replaced but the directory sync failed", "error", werr)
		}
	}
	res.Written = true
	res.SHA256 = SHA256Hex(data)
	if serr := g.opt.State.SetUnitSHA256(res.SHA256); serr != nil {
		// The next run sees a file that already holds what it would write and records the hash.
		g.log.Error("could not record the hash of the llama unit", "error", serr)
	}
	g.reload(ctx, &res, &fnd)

	// Step 7: a running llama keeps its old command line until it is restarted.
	if haveCur && RestartRequired(cur, data) {
		res.RestartRequired = true
		res.Changed = ChangedSettings(cur, data)
		details := map[string]any{"target": "llama", "path": g.opt.Paths.Unit, "reason": "unit", "changed": res.Changed}
		fnd.add(CodeUnitRestart, "llama-unit", details)
		g.raise(CodeUnitRestart, "llama-unit", details)
		g.log.Info("the llama unit changed; restart llama to apply it", "changed", res.Changed)
	}
	return res, nil
}

// reload runs daemon-reload after the unit changed (or after a reload that failed).
func (g *UnitGenerator) reload(ctx context.Context, res *UnitResult, fnd *collector) {
	if g.opt.Systemd == nil {
		g.reloadPending = false
		return
	}
	if err := g.opt.Systemd.DaemonReload(ctx); err != nil {
		g.reloadPending = true
		res.ReloadErr = err
		fnd.add(CodeUnitFailed, "daemon-reload", map[string]any{"path": g.opt.Paths.Unit, "error": clip(err.Error(), 300)})
		g.log.Error("daemon-reload failed after the llama unit changed", "error", err)
		return
	}
	g.reloadPending = false
	res.Reloaded = true
}

// saveAside writes the edited file as localai-llama.service.hand-edited-<ts> (-2, -3 ... when
// taken).
func (g *UnitGenerator) saveAside(cur []byte, now time.Time) (string, error) {
	base := g.opt.Paths.Unit + ".hand-edited-" + now.Format("20060102T150405Z")
	name := base
	for n := 2; n < 100; n++ {
		if _, err := g.opt.FS.Stat(name); errors.Is(err, os.ErrNotExist) {
			break
		}
		name = fmt.Sprintf("%s-%d", base, n)
	}
	if err := config.WriteFileAtomicFS(g.opt.FS, name, cur, unitFileMode, g.opt.UnitOwner.UID, g.opt.UnitOwner.GID); err != nil && !errors.Is(err, config.ErrNotDurable) {
		return "", err
	}
	return name, nil
}

// checkBinary raises LAI-LLM-002 when the program the unit starts is missing, a directory or not
// executable. The unit is written anyway: the build manager may be about to install the build, and
// a unit that points at the right path is correct either way.
func (g *UnitGenerator) checkBinary(binary string, fnd *collector) {
	if CheckBinary(binary) != nil {
		return // RenderUnit refuses it with a better message
	}
	info, err := g.opt.FS.Stat(binary)
	reason := ""
	switch {
	case errors.Is(err, os.ErrNotExist):
		reason = AccessMissing
	case err != nil:
		reason = AccessError
	case !info.Mode().IsRegular():
		reason = AccessNotFile
	case info.Mode().Perm()&0o111 == 0:
		reason = "not_executable"
	}
	if reason != "" {
		fnd.add(CodeBinaryMissing, "binary", map[string]any{"path": binary, "reason": reason})
	}
}

// ensureCache makes sure LLAMA_CACHE exists as a directory owned by localai with mode 0750 and
// counts what is in it. A directory that is not empty raises LAI-LLM-024; nothing in it is ever
// deleted. A failure is a finding and never stops the unit.
func (g *UnitGenerator) ensureCache(res *UnitResult, fnd *collector) {
	dir := g.opt.Paths.CacheDir
	if dir == "" {
		return
	}
	res.Cache.Path = dir
	problem := func(what string, err error) {
		fnd.add(CodeUnitFailed, "llama-cache", map[string]any{"path": dir, "step": what, "error": clip(err.Error(), 300)})
		g.log.Error("could not prepare the llama cache directory", "step", what, "error", err)
	}
	info, err := g.opt.FS.Stat(dir)
	switch {
	case errors.Is(err, os.ErrNotExist):
		if merr := g.opt.MkdirAll(dir, cacheDirMode); merr != nil {
			problem("create", merr)
			return
		}
		res.Cache.Created = true
		info, err = g.opt.FS.Stat(dir)
		if err != nil {
			problem("stat", err)
			return
		}
	case err != nil:
		problem("stat", err)
		return
	case !info.IsDir():
		problem("stat", fmt.Errorf("it exists and is not a directory"))
		return
	}
	// The mask of the creating process may have narrowed or widened the mode, and the directory may
	// be older than this rule: set what section 3.1 says.
	if info.Mode().Perm() != cacheDirMode {
		if cerr := g.opt.FS.Chmod(dir, cacheDirMode); cerr != nil {
			problem("chmod", cerr)
			return
		}
	}
	if g.opt.CacheOwner.UID >= 0 || g.opt.CacheOwner.GID >= 0 {
		uid, gid, ok := ownerOf(info)
		if !ok || (g.opt.CacheOwner.UID >= 0 && uid != g.opt.CacheOwner.UID) || (g.opt.CacheOwner.GID >= 0 && gid != g.opt.CacheOwner.GID) {
			if cerr := g.opt.FS.Chown(dir, g.opt.CacheOwner.UID, g.opt.CacheOwner.GID); cerr != nil {
				problem("chown", cerr)
				return
			}
		}
	}
	entries, rerr := g.opt.FS.ReadDir(dir)
	if rerr != nil {
		problem("read", rerr)
		return
	}
	res.Cache.Entries = len(entries)
	if len(entries) > 0 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, clip(e.Name(), 80))
		}
		sort.Strings(names)
		if len(names) > cacheListSize {
			names = names[:cacheListSize]
		}
		fnd.add(CodeCacheModels, "llama-cache", map[string]any{"path": dir, "entries": len(entries), "names": names})
	}
}

// raise sends an event-style finding (LAI-LLM-006, LAI-AGT-016) to the alert sink at once.
func (g *UnitGenerator) raise(code Code, subject string, details map[string]any) {
	if g.opt.Alerts != nil {
		g.opt.Alerts.Raise(string(code), subject, details)
	}
}

// settle raises the findings that are new since the last run and clears the ones that went away.
// LAI-LLM-006 and LAI-AGT-016 are events, raised where they happen and never cleared here.
func (g *UnitGenerator) settle(found []PresetFinding) {
	now := map[findingKey]bool{}
	for _, f := range found {
		if f.Code == CodeUnitHandEdited || f.Code == CodeUnitRestart {
			continue
		}
		k := findingKey{f.Code, f.Subject}
		now[k] = true
		if !g.raised[k] {
			g.raise(f.Code, f.Subject, f.Details)
		}
	}
	for k := range g.raised {
		if !now[k] && g.opt.Alerts != nil {
			g.opt.Alerts.Clear(string(k.code), k.subject)
		}
	}
	g.raised = now
}
