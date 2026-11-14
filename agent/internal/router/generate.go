package router

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/catalog"
	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/events"
	"github.com/marinoscar/local-ai/agent/internal/state"
)

// File modes of the generated files (llama-router.md section 3.1).
const (
	iniMode    os.FileMode = 0o640 // root:localai
	dropInMode os.FileMode = 0o644 // root
)

// DefaultOpsWait is how long Generate waits for the model-operations lock before giving up on
// applying a reload (the file is already written; the reload is retried on the next Generate).
const DefaultOpsWait = 60 * time.Second

// DefaultSettle is how long Generate waits, after a reload, for the router to stop the running
// models whose preset changed, before it loads them again.
const DefaultSettle = 20 * time.Second

// aliasRe is the rule of node.yaml llama.legacy_aliases (agent.md section 3.4).
var aliasRe = regexp.MustCompile(`^[a-z0-9][a-z0-9.\-_:]{1,63}$`)

// Paths are the files Generate owns.
type Paths struct {
	// INI is /etc/localai/models.ini; INI+".prev" and INI+".hand-edited-<ts>" sit beside it.
	INI string
	// DropIn is /etc/systemd/system/localai-llama.service.d/50-env.conf.
	DropIn string
}

// DefaultPaths returns the paths of llama-router.md section 3.1 under root ("" means "/").
func DefaultPaths(root string) Paths {
	if root == "" {
		root = "/"
	}
	return Paths{
		INI:    filepath.Join(root, "etc", "localai", "models.ini"),
		DropIn: filepath.Join(root, "etc", "systemd", "system", "localai-llama.service.d", "50-env.conf"),
	}
}

// Ownership is a numeric owner for a written file; -1 leaves that id to the process.
type Ownership struct{ UID, GID int }

// IniState is the slice of state.json Generate keeps: llama.ini_sha256, the hash of the file it
// last wrote, which is how a hand edit is noticed (llama-router.md section 4.2 step 4).
type IniState interface {
	// IniSHA256 returns the recorded hash, "" when none.
	IniSHA256() string
	// SetIniSHA256 records the hash durably.
	SetIniSHA256(sha string) error
}

// StateIni is the IniState over the agent's state store.
type StateIni struct{ Store *state.Store }

// IniSHA256 implements IniState.
func (s StateIni) IniSHA256() string { return s.Store.Snapshot().Llama.IniSHA256 }

// SetIniSHA256 implements IniState.
func (s StateIni) SetIniSHA256(sha string) error {
	return s.Store.MutateSync(func(st *state.State) error {
		st.Llama.IniSHA256 = sha
		return nil
	})
}

// DaemonReloader runs systemctl daemon-reload; internal/services.Manager implements it.
type DaemonReloader interface {
	DaemonReload(ctx context.Context) error
}

// OpsLocker is the model-operations lock (modelOpsMu, llama-router.md section 4.4 step 2). Acquire
// waits for it until ctx ends. The lock must be released exactly once.
type OpsLocker interface {
	Acquire(ctx context.Context) (release func(), err error)
}

// ModelLoader loads a model again after a reload dropped it. Generate holds the model-operations
// lock while it calls LoadHeld, so an implementation backed by the load orchestrator (ROUTER-05)
// must not take the lock a second time; it waits for the model to be loaded.
type ModelLoader interface {
	LoadHeld(ctx context.Context, id string) error
}

// DirectLoader is the ModelLoader used until the orchestrator exists: it asks the router to load
// the model and returns once the router accepted the request, without planning or waiting. It is
// correct here because the set of loaded models is the one the router had before the reload.
type DirectLoader struct{ Router Router }

// LoadHeld implements ModelLoader.
func (d DirectLoader) LoadHeld(ctx context.Context, id string) error { return d.Router.Load(ctx, id) }

// GeneratorOptions configures NewGenerator. Every field except IniState is optional.
type GeneratorOptions struct {
	Paths Paths
	// FS is the filesystem seam; default config.OSFS. MkdirAll creates the drop-in directory;
	// default os.MkdirAll (tests that fake the FS point both at a temp directory).
	FS       config.FS
	MkdirAll func(path string, perm os.FileMode) error
	// Presence decides "present on a root"; default ManifestPresence over FS.
	Presence Presence
	// LocalAI is the unit's user, for the readability check; nil means unknown: only the o+rx bits
	// count.
	LocalAI *Identity
	// INIOwner and DropInOwner are the owners the files are given (root:localai and root:root in
	// production); default -1, -1.
	INIOwner, DropInOwner Ownership
	// IniState records ini_sha256. Required.
	IniState IniState
	// Router is the client; nil or a router that is down means "not running": the file is written
	// and the next router start reads it (step 9).
	Router Router
	// Systemd runs daemon-reload after the drop-in changes; nil skips it.
	Systemd DaemonReloader
	// Alerts receives the doctor codes; Publisher the model.unloaded events.
	Alerts    AlertSink
	Publisher Publisher
	// Suppress is Watcher.Suppress: it keeps the watcher from reporting the unloads of a reload a
	// second time. nil skips it.
	Suppress func(opID string) (release func())
	// Ops is the model-operations lock; nil means no lock (tests, single-threaded callers).
	Ops OpsLocker
	// OpsWait bounds the wait for Ops; default DefaultOpsWait.
	OpsWait time.Duration
	// Loader loads a model again after a reload; default DirectLoader{Router}.
	Loader ModelLoader
	// Settle bounds the wait for the router to stop changed models; default DefaultSettle.
	// Sleep waits between polls (default: a context-aware time.After).
	Settle time.Duration
	Sleep  func(ctx context.Context, d time.Duration)
	Clock  config.Clock
	// Version is the agent version for the header.
	Version string
	Logger  *slog.Logger
}

// Inputs are what one Generate decides from. The caller assembles them from the config store,
// the catalog, the hardware snapshot, the state store and the mounted roots.
type Inputs struct {
	// NodeID is node.yaml node.id (header only).
	NodeID string
	// Catalog is the loaded catalog; only its exported fields (Version, Models) are read.
	Catalog *catalog.Catalog
	// Profile is the node's profile, nil when it is not in the catalog.
	Profile   *catalog.Profile
	Overrides config.ProfileOverrides
	// Llama is node.yaml llama{}: legacy_aliases and extra_env are read.
	Llama config.LlamaBlock
	Roots Roots
	// Fit is the machine side of the static fit (router.NewEnv).
	Fit Env
	// StateDefault is state.models.default ("" when unset); Survival is true in survival mode.
	StateDefault string
	Survival     bool
	// Workloads are the node's resolved workloads (ResolveWorkloads); only the workload routes read
	// them.
	Workloads []Workload
}

// PresetFinding is a doctor code Generate raised, with the model or setting it concerns.
type PresetFinding struct {
	Code    Code
	Subject string
	Details map[string]any
}

// Dropped is a model that was in the effective set but got no preset because of a problem with its
// catalog entry (LAI-LLM-007) or its files (LAI-LLM-008).
type Dropped struct {
	ID     string
	Code   Code
	Reason string
}

// Result is what a Generate did.
type Result struct {
	INIPath string
	// Written is true when models.ini was replaced; SHA256 is the hash of the file now on disk.
	Written bool
	SHA256  string
	// InputsSHA256 is the hash in the header.
	InputsSHA256 string
	// Models are the ids in the file; Default the model with load-on-startup ("" for none).
	Models  []string
	Default string
	// Excluded are in-scope models left out by the fit rules or missing files; Dropped those left
	// out for a problem.
	Excluded []Excluded
	Dropped  []Dropped
	// HandEditedCopy is the path the previous file was saved to when it had been edited by hand.
	HandEditedCopy string
	// DropInChanged is true when the drop-in was written or removed; RestartNeeded then too (the
	// router must be restarted to apply it; never done automatically).
	DropInChanged bool
	RestartNeeded bool
	DropInErr     error
	// Env is the merged environment of the drop-in.
	Env      map[string]string
	Findings []PresetFinding
	// Reload is the reload-safety decision, Reloaded true when the router was asked to re-read the
	// file. Restored are the models loaded again; ApplyErr is a failure to apply the file to a
	// running router (retried by the next Generate).
	Reload   ReloadDecision
	Reloaded bool
	Restored []string
	ApplyErr error
}

// Generator renders models.ini and the environment drop-in and applies them to the router
// (llama-router.md section 4.2). It is safe for concurrent use; calls are serialised.
type Generator struct {
	opt   GeneratorOptions
	log   *slog.Logger
	clock config.Clock

	mu     sync.Mutex
	raised map[findingKey]bool
	// applied is what the running router last read; appliedOK says it is known. When it is not
	// (agent start, router down) the file on disk before the write stands in for it.
	applied   Sections
	appliedOK bool
	// synced is true once the router's preset list has been compared with the file since the
	// router was last seen down; pending is true after a reload that could not be applied.
	synced  bool
	pending bool
}

// NewGenerator returns a Generator.
func NewGenerator(opt GeneratorOptions) (*Generator, error) {
	if opt.IniState == nil {
		return nil, errors.New("router: the generator needs an IniState")
	}
	if opt.Paths.INI == "" {
		return nil, errors.New("router: the generator needs the path of models.ini")
	}
	if opt.FS == nil {
		opt.FS = config.OSFS{}
	}
	if opt.MkdirAll == nil {
		opt.MkdirAll = os.MkdirAll
	}
	if opt.Presence == nil {
		opt.Presence = ManifestPresence{FS: opt.FS}
	}
	if opt.LocalAI == nil {
		opt.LocalAI = &Identity{UID: -1, GID: -1}
	}
	if opt.Loader == nil && opt.Router != nil {
		opt.Loader = DirectLoader{Router: opt.Router}
	}
	if opt.OpsWait <= 0 {
		opt.OpsWait = DefaultOpsWait
	}
	if opt.Settle <= 0 {
		opt.Settle = DefaultSettle
	}
	if opt.Sleep == nil {
		opt.Sleep = func(ctx context.Context, d time.Duration) {
			t := time.NewTimer(d)
			defer t.Stop()
			select {
			case <-ctx.Done():
			case <-t.C:
			}
		}
	}
	g := &Generator{opt: opt, clock: opt.Clock, raised: map[findingKey]bool{}}
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

// collector gathers the findings of one run.
type collector struct{ list []PresetFinding }

func (c *collector) add(code Code, subject string, details map[string]any) {
	c.list = append(c.list, PresetFinding{Code: code, Subject: subject, Details: details})
}

// Generate runs llama-router.md section 4.2. It returns an error only when models.ini is not in
// the state the inputs call for (it could not be rendered, read, saved aside or written); a
// failure to apply the file to a running router, or to write the drop-in, is in the Result and
// retried by the next call. Findings are raised and cleared through GeneratorOptions.Alerts.
func (g *Generator) Generate(ctx context.Context, in Inputs) (res Result, err error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	res.INIPath = g.opt.Paths.INI
	var fnd collector
	defer func() {
		res.Findings = fnd.list
		g.settle(fnd.list)
	}()
	fail := func(subject string, cause error, details map[string]any) (Result, error) {
		if details == nil {
			details = map[string]any{}
		}
		details["error"] = clip(cause.Error(), 300)
		fnd.add(CodePresetFailed, subject, details)
		g.log.Error("could not generate the preset file", "subject", subject, "error", cause)
		return res, fmt.Errorf("router: %s: %w", subject, cause)
	}
	now := g.clock.Now().UTC()

	// Steps 1 and 3: the effective set, then the entries that can be rendered and read.
	var models []catalog.Model
	version := ""
	if in.Catalog != nil {
		models, version = in.Catalog.Models, in.Catalog.Version
	}
	set := EffectiveSet(EffectiveInput{
		Models: models, Profile: in.Profile, Overrides: in.Overrides,
		Roots: in.Roots, Presence: g.opt.Presence, Fit: in.Fit,
	})
	res.Excluded = set.Excluded
	aliases, aliasProblems := resolveAliases(set.Entries, in.Llama.LegacyAliases)
	for _, p := range aliasProblems {
		fnd.add(CodePresetFailed, "alias:"+p.alias, map[string]any{"model": p.model, "alias": p.alias, "reason": p.reason})
	}
	var kept []Entry
	var presets []Preset
	for _, e := range set.Entries {
		p, perr := BuildPreset(e.Model, e.Root, aliases[e.Model.ID])
		if perr != nil {
			reason := perr.Error()
			var pe *PresetError
			if errors.As(perr, &pe) {
				reason = pe.Reason
			}
			res.Dropped = append(res.Dropped, Dropped{ID: e.Model.ID, Code: CodePresetFailed, Reason: reason})
			fnd.add(CodePresetFailed, e.Model.ID, map[string]any{"model": e.Model.ID, "reason": reason})
			continue
		}
		if prob := g.unreadable(p.Paths); prob != nil {
			res.Dropped = append(res.Dropped, Dropped{ID: e.Model.ID, Code: CodeFilesMissing, Reason: prob.Reason})
			fnd.add(CodeFilesMissing, e.Model.ID, map[string]any{"model": e.Model.ID, "path": prob.Path, "reason": prob.Reason})
			continue
		}
		kept = append(kept, e)
		presets = append(presets, p)
	}
	ids := make([]string, len(kept))
	for i, e := range kept {
		ids[i] = e.Model.ID
	}
	res.Models = ids

	// The effective default (section 4.6).
	res.Default = EffectiveDefault(ids, in.StateDefault, in.Profile, in.Survival)
	if res.Default == "" {
		fnd.add(CodePresetNoDefault, "", map[string]any{"models": len(ids)})
	}

	// Step 2: render.
	hashIn := HashInput{
		Node: in.NodeID, CatalogVersion: version, Default: res.Default, Entries: kept, Aliases: aliases,
		AddModels: in.Overrides.AddModels, RemoveModels: in.Overrides.RemoveModels,
	}
	if in.Profile != nil {
		hashIn.Profile = in.Profile.ID
	}
	res.InputsSHA256 = InputsHash(hashIn)
	data, rerr := Render(Header{
		AgentVersion: g.opt.Version, At: now, Node: in.NodeID, Profile: hashIn.Profile,
		Catalog: version, InputsSHA256: res.InputsSHA256,
	}, presets, res.Default)
	if rerr != nil {
		return fail("render", rerr, nil)
	}
	newSecs := ParseINI(data)

	// The environment (step 6). It is reconciled on every run, even when the file is unchanged:
	// an edit of node.yaml llama.extra_env changes the drop-in and nothing in models.ini.
	keptModels := make([]*catalog.Model, len(kept))
	for i := range kept {
		keptModels[i] = kept[i].Model
	}
	merged := MergeEnv(keptModels, in.Llama.ExtraEnv)
	res.Env = merged.Env
	for _, c := range merged.Conflicts {
		fnd.add(CodePresetEnvConflict, c.Key, map[string]any{"key": c.Key, "models": c.Models})
	}
	for _, r := range merged.Rejected {
		fnd.add(CodePresetFailed, "env:"+r.Key, map[string]any{"key": r.Key, "source": r.Source, "model": r.Model, "reason": r.Reason})
	}

	// Step 4: the current file, and whether someone edited it.
	cur, rerr := g.opt.FS.ReadFile(g.opt.Paths.INI)
	haveCur := rerr == nil
	if rerr != nil && !errors.Is(rerr, os.ErrNotExist) {
		return fail("read models.ini", rerr, map[string]any{"path": g.opt.Paths.INI})
	}
	if haveCur {
		recorded := g.opt.IniState.IniSHA256()
		edited := false
		if recorded != "" {
			edited = SHA256Hex(cur) != recorded
		} else {
			// No record (a fresh state file): a file that carries our header is ours.
			edited = !bytes.HasPrefix(cur, []byte(headerPrefix))
		}
		// A file that already holds what we would write loses nothing (a crash between the rename
		// and the state update looks like an edit).
		if edited && SameBody(cur, data) {
			edited = false
		}
		if edited {
			saved, serr := g.saveAside(cur, now)
			if serr != nil {
				// Never overwrite what could not be kept.
				return fail("save the hand-edited models.ini", serr, map[string]any{"path": g.opt.Paths.INI})
			}
			res.HandEditedCopy = saved
			fnd.add(CodePresetHandEdited, "", map[string]any{"path": g.opt.Paths.INI, "saved_as": saved})
			g.raise(CodePresetHandEdited, "", map[string]any{"path": g.opt.Paths.INI, "saved_as": saved})
			g.log.Warn("models.ini was edited by hand; a copy was saved", "saved_as", saved)
		}
	}

	// Step 6: the drop-in.
	g.reconcileDropIn(ctx, &res, &fnd, merged.Env)

	// Steps 5 and 7: write unless the file already says this.
	if haveCur && SameBody(cur, data) {
		res.SHA256 = SHA256Hex(cur)
		if g.opt.IniState.IniSHA256() != res.SHA256 {
			if serr := g.opt.IniState.SetIniSHA256(res.SHA256); serr != nil {
				g.log.Error("could not record the hash of models.ini", "error", serr)
			}
		}
	} else {
		if haveCur {
			if perr := config.WriteFileAtomicFS(g.opt.FS, g.opt.Paths.INI+".prev", cur, iniMode, g.opt.INIOwner.UID, g.opt.INIOwner.GID); perr != nil && !errors.Is(perr, config.ErrNotDurable) {
				g.log.Warn("could not keep models.ini.prev", "error", perr)
			}
		}
		if werr := config.WriteFileAtomicFS(g.opt.FS, g.opt.Paths.INI, data, iniMode, g.opt.INIOwner.UID, g.opt.INIOwner.GID); werr != nil {
			if !errors.Is(werr, config.ErrNotDurable) {
				return fail("write models.ini", werr, map[string]any{"path": g.opt.Paths.INI})
			}
			g.log.Warn("models.ini replaced but the directory sync failed", "error", werr)
		}
		res.Written = true
		res.SHA256 = SHA256Hex(data)
		if serr := g.opt.IniState.SetIniSHA256(res.SHA256); serr != nil {
			// The next run sees a file that already holds what it would write and records the hash.
			g.log.Error("could not record the hash of models.ini", "error", serr)
		}
	}

	// Steps 8 and 9: apply to a running router.
	var old Sections
	if haveCur {
		old = ParseINI(cur)
	}
	g.apply(ctx, &res, old, newSecs, now)
	return res, nil
}

// saveAside writes the edited file as models.ini.hand-edited-<ts> (-2, -3 ... when taken).
func (g *Generator) saveAside(cur []byte, now time.Time) (string, error) {
	base := g.opt.Paths.INI + ".hand-edited-" + now.Format("20060102T150405Z")
	name := base
	for n := 2; n < 100; n++ {
		if _, err := g.opt.FS.Stat(name); errors.Is(err, os.ErrNotExist) {
			break
		}
		name = fmt.Sprintf("%s-%d", base, n)
	}
	if err := config.WriteFileAtomicFS(g.opt.FS, name, cur, iniMode, g.opt.INIOwner.UID, g.opt.INIOwner.GID); err != nil && !errors.Is(err, config.ErrNotDurable) {
		return "", err
	}
	return name, nil
}

// unreadable returns the first problem the unit's user would have with the paths, or nil.
func (g *Generator) unreadable(paths []string) *AccessProblem {
	for _, p := range paths {
		if err := CheckReadable(g.opt.FS, *g.opt.LocalAI, p); err != nil {
			var ap *AccessProblem
			if errors.As(err, &ap) {
				return ap
			}
			return &AccessProblem{Path: p, Reason: AccessError}
		}
	}
	return nil
}

// reconcileDropIn writes, replaces or removes the environment drop-in so that it matches env, runs
// daemon-reload when it changed and raises LAI-AGT-016: the new environment reaches llama only at
// its next restart, which the agent never does by itself.
func (g *Generator) reconcileDropIn(ctx context.Context, res *Result, fnd *collector, env map[string]string) {
	path := g.opt.Paths.DropIn
	if path == "" {
		return
	}
	want := RenderDropIn(env)
	cur, err := g.opt.FS.ReadFile(path)
	exists := err == nil
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		res.DropInErr = err
		fnd.add(CodePresetFailed, "drop-in", map[string]any{"path": path, "error": clip(err.Error(), 300)})
		return
	}
	switch {
	case want == nil && !exists:
		return
	case want != nil && exists && bytes.Equal(cur, want):
		return
	case want == nil:
		if err := g.opt.FS.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			res.DropInErr = err
			fnd.add(CodePresetFailed, "drop-in", map[string]any{"path": path, "error": clip(err.Error(), 300)})
			return
		}
	default:
		if err := g.opt.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			res.DropInErr = err
			fnd.add(CodePresetFailed, "drop-in", map[string]any{"path": path, "error": clip(err.Error(), 300)})
			return
		}
		if err := config.WriteFileAtomicFS(g.opt.FS, path, want, dropInMode, g.opt.DropInOwner.UID, g.opt.DropInOwner.GID); err != nil && !errors.Is(err, config.ErrNotDurable) {
			res.DropInErr = err
			fnd.add(CodePresetFailed, "drop-in", map[string]any{"path": path, "error": clip(err.Error(), 300)})
			return
		}
	}
	res.DropInChanged, res.RestartNeeded = true, true
	if g.opt.Systemd != nil {
		if err := g.opt.Systemd.DaemonReload(ctx); err != nil {
			res.DropInErr = err
			g.log.Error("daemon-reload failed after the drop-in changed", "error", err)
		}
	}
	details := map[string]any{"target": "llama", "path": path, "reason": "environment"}
	fnd.add(CodePresetRestart, "llama-env", details)
	g.raise(CodePresetRestart, "llama-env", details)
	g.log.Info("the llama environment changed; restart llama to apply it", "path", path)
}

type aliasProblem struct{ model, alias, reason string }

// resolveAliases takes node.yaml llama.legacy_aliases for the models in the file and returns the
// aliases each section gets. An alias that is not a valid name, or that equals a section name or
// an alias of an earlier model (sorted by id), is dropped and reported: the router checks aliases
// for conflicts, and a refused file would leave every model unloadable. An alias of a model that
// is not in the file is ignored.
func resolveAliases(entries []Entry, legacy map[string][]string) (map[string][]string, []aliasProblem) {
	out := map[string][]string{}
	if len(legacy) == 0 {
		return out, nil
	}
	taken := map[string]string{}
	for _, e := range entries {
		taken[e.Model.ID] = e.Model.ID
	}
	var problems []aliasProblem
	for _, e := range entries { // sorted by id
		id := e.Model.ID
		for _, a := range legacy[id] {
			switch {
			case !aliasRe.MatchString(a) || checkValue(a) != nil || strings.ContainsAny(a, ","):
				problems = append(problems, aliasProblem{id, clip(a, 40), "not a valid alias"})
			case taken[a] == id:
				// the same alias listed twice for one model, or equal to its own id
				if a == id {
					problems = append(problems, aliasProblem{id, a, "equals the model id"})
				}
			case taken[a] != "":
				problems = append(problems, aliasProblem{id, a, "already names " + taken[a]})
			default:
				taken[a] = id
				out[id] = append(out[id], a)
			}
		}
	}
	return out, problems
}

// raise sends an event-style finding (LAI-LLM-006, LAI-AGT-016) to the alert sink at once.
func (g *Generator) raise(code Code, subject string, details map[string]any) {
	if g.opt.Alerts != nil {
		g.opt.Alerts.Raise(string(code), subject, details)
	}
}

// settle raises the findings that are new since the last run and clears the ones that went away.
// LAI-LLM-006 and LAI-AGT-016 are events, raised where they happen and never cleared here.
func (g *Generator) settle(found []PresetFinding) {
	now := map[findingKey]bool{}
	for _, f := range found {
		if f.Code == CodePresetHandEdited || f.Code == CodePresetRestart {
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

// runningIDs are the models with a child process: loaded, loading or sleeping.
func runningIDs(models []ModelStatus) []string {
	var ids []string
	for _, m := range models {
		if m.Status.Running() {
			ids = append(ids, m.ID)
		}
	}
	sort.Strings(ids)
	return ids
}

// presetIDs are the ids of the router's preset models (cache models are not ours).
func presetIDs(models []ModelStatus) []string {
	var ids []string
	for _, m := range models {
		if m.Source == "" || m.Source == SourcePreset {
			ids = append(ids, m.ID)
		}
	}
	sort.Strings(ids)
	return ids
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// apply is steps 8 and 9: the reload-safety rule against a running router.
func (g *Generator) apply(ctx context.Context, res *Result, old, newSecs Sections, now time.Time) {
	r := g.opt.Router
	if r == nil {
		g.appliedOK, g.synced = false, false
		return
	}
	statuses, err := r.Models(ctx)
	if err != nil {
		g.routerErr(res, err)
		return
	}
	base := old
	if g.appliedOK {
		base = g.applied
	}
	decide := func(statuses []ModelStatus) ReloadDecision {
		d := DecideReload(base, newSecs, runningIDs(statuses))
		if !d.Reload && (!g.synced || g.pending) && !sameStrings(presetIDs(statuses), newSecs.IDs()) {
			d.Reload, d.Reason = true, ReasonResync
		}
		return d
	}
	dec := decide(statuses)
	res.Reload = dec
	if !dec.Reload {
		// Nothing to tell the router. Keep what it last read as the base: after a default change
		// the file differs from it in load-on-startup, which only a router start reads.
		g.applied, g.appliedOK, g.synced, g.pending = base, true, true, false
		return
	}

	// A reload can unload running models: take the model-operations lock, then look again.
	if g.opt.Ops != nil {
		lctx, cancel := context.WithTimeout(ctx, g.opt.OpsWait)
		release, lerr := g.opt.Ops.Acquire(lctx)
		cancel()
		if lerr != nil {
			res.ApplyErr = fmt.Errorf("router: model operations are busy, the reload waits: %w", lerr)
			g.pending = true
			g.log.Warn("could not take the model-operations lock; the reload is retried at the next run", "error", lerr)
			return
		}
		defer release()
		if statuses, err = r.Models(ctx); err != nil {
			g.routerErr(res, err)
			return
		}
		dec = decide(statuses)
		res.Reload = dec
		if !dec.Reload {
			g.applied, g.appliedOK, g.synced, g.pending = base, true, true, false
			return
		}
	}

	opID := "ini-" + now.Format("20060102T150405Z")
	if g.opt.Suppress != nil {
		defer g.opt.Suppress(opID)()
	}
	if err := r.Reload(ctx); err != nil {
		g.routerErr(res, err)
		return
	}
	res.Reloaded = true
	g.applied, g.appliedOK, g.synced, g.pending = newSecs, true, true, false
	g.log.Info("the router re-read models.ini", "reason", dec.Reason, "restore", dec.Restore, "dropped", dec.Dropped)

	// The reload unloads the running models whose preset changed or went away. Say so, wait for
	// the router to finish, then load again the ones that still have a preset.
	for _, id := range append(append([]string(nil), dec.Restore...), dec.Dropped...) {
		g.publishUnloaded(opID, id)
	}
	toLoad := append([]string(nil), dec.Restore...)
	if len(dec.Restore) > 0 || len(dec.Kept) > 0 {
		after := g.settleStopped(ctx, dec.Restore)
		running := map[string]bool{}
		for _, id := range runningIDs(after) {
			running[id] = true
		}
		// A model that was meant to survive but did not: the router judged its preset changed
		// (for instance by a load-on-startup difference it had never read). It comes back too.
		for _, id := range dec.Kept {
			if after != nil && !running[id] {
				g.publishUnloaded(opID, id)
				toLoad = append(toLoad, id)
			}
		}
		sort.Strings(toLoad)
		for _, id := range toLoad {
			if g.opt.Loader == nil {
				break
			}
			if after != nil && running[id] {
				// The router did not unload it (it kept the old preset running): loading a
				// running model is an error, and there is nothing to bring back.
				g.log.Warn("the router kept a changed model loaded", "model", id)
				continue
			}
			if lerr := g.opt.Loader.LoadHeld(ctx, id); lerr != nil {
				g.log.Error("could not load a model again after the reload", "model", id, "error", lerr)
				res.ApplyErr = errors.Join(res.ApplyErr, fmt.Errorf("load %s again: %w", id, lerr))
				continue
			}
			res.Restored = append(res.Restored, id)
		}
	}
}

// routerErr handles a failed call to the router: down means step 9 (the next start reads the
// file); anything else is a failure to apply, retried by the next run.
func (g *Generator) routerErr(res *Result, err error) {
	if errors.Is(err, ErrDown) {
		g.appliedOK, g.synced, g.pending = false, false, false
		return
	}
	res.ApplyErr = err
	g.pending = true
	g.log.Warn("could not apply models.ini to the running router", "code", string(CodeOf(err)), "error", err)
}

func (g *Generator) publishUnloaded(opID, id string) {
	if g.opt.Publisher == nil {
		return
	}
	if err := g.opt.Publisher.Publish(events.TypeModelUnloaded, opID, events.ModelUnloaded{Model: id, Reason: events.UnloadPresetChanged}); err != nil {
		g.log.Error("could not publish the event", "event", events.TypeModelUnloaded, "error", err)
	}
}

// settleStopped waits until none of ids is running any more (the router unloads a model whose
// preset changed during the reload, but possibly after answering), at most Settle, and returns
// the last list of statuses it read (nil when the router could not be read).
func (g *Generator) settleStopped(ctx context.Context, ids []string) []ModelStatus {
	const poll = 500 * time.Millisecond
	var last []ModelStatus
	for waited := time.Duration(0); ; waited += poll {
		statuses, err := g.opt.Router.Models(ctx)
		if err != nil {
			return last
		}
		last = statuses
		still := false
		running := map[string]bool{}
		for _, id := range runningIDs(statuses) {
			running[id] = true
		}
		for _, id := range ids {
			if running[id] {
				still = true
			}
		}
		if !still || waited >= g.opt.Settle || ctx.Err() != nil {
			return last
		}
		g.opt.Sleep(ctx, poll)
	}
}
