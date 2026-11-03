package migrate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/catalog"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/sync/manifest"
)

// planVersion is the schema of plan.json.
const planVersion = 1

// PlanFile is one model file the migration moves.
type PlanFile struct {
	// Kind is the catalog file kind (weights, draft, mmproj, ...); Name the catalog file name.
	Kind string `json:"kind"`
	Name string `json:"name"`
	// Old is where the legacy unit has it; New is /srv/localai/models/<id>/<name>.
	Old    string `json:"old"`
	New    string `json:"new"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size_bytes"`
}

// Plan is what the preflight (step M0) found and what the migration will do. It is written to plan.json
// (llama-router.md section 4.9.2): it holds no secret, only the key's fingerprint. ServeBefore and the
// problems are kept in memory (serve-before.json is its own file).
type Plan struct {
	Version   int       `json:"version"`
	CreatedAt time.Time `json:"created_at"`

	// What the legacy unit says.
	User             string `json:"user"`
	Group            string `json:"group"`
	WorkingDirectory string `json:"working_directory"`
	Binary           string `json:"binary"`
	Host             string `json:"host"`
	Port             int    `json:"port"`
	Ctx              int    `json:"ctx"`
	Backend          string `json:"backend"`
	LegacyCommit     string `json:"legacy_commit,omitempty"`

	// The model.
	ModelID        string     `json:"model_id"`
	CatalogVersion string     `json:"catalog_version"`
	Files          []PlanFile `json:"files"`
	// Aliases are the names the old unit served the model under that the new one must keep (the ones
	// that are not the model id itself).
	Aliases  []string          `json:"aliases"`
	ExtraEnv map[string]string `json:"extra_env,omitempty"`

	// Flag differences between the unit and the catalog; AcceptedFlags is true when
	// --accept-catalog-flags was given, so the catalog's values win.
	Diffs         []FlagDiff `json:"diffs,omitempty"`
	AcceptedFlags bool       `json:"accepted_flags,omitempty"`
	// ExpectedCtx is the n_ctx M8 expects: the old -c, or the catalog's ctx when its flags were accepted.
	ExpectedCtx int `json:"expected_ctx"`

	KeyFingerprint string `json:"key_fingerprint"`

	// The build of M1.
	BuildNeeded   bool   `json:"build_needed"`
	CurrentCommit string `json:"current_commit,omitempty"`

	// PublicHost and PublicBefore: the public URL's status before the migration (0: not asked).
	PublicHost   string `json:"public_host,omitempty"`
	PublicBefore int    `json:"public_before,omitempty"`

	Warnings []string `json:"warnings,omitempty"`

	// ServeBefore is the canonical `tailscale serve status --json` before the migration.
	ServeBefore []byte `json:"-"`
	// Problems are the reasons not to go on; empty when the migration can run.
	Problems []Problem `json:"-"`
	// Mode is what a run does with it (ModeFresh, ModeResume, ModeDone, ModeRollback).
	Mode string `json:"-"`
	// Resume is true when this plan was read back from an interrupted migration; Progress is its state.
	Resume   bool     `json:"-"`
	Progress Progress `json:"-"`
}

// OK is true when nothing stops the migration.
func (p *Plan) OK() bool { return len(p.Problems) == 0 }

func (p *Plan) problem(code, cause, format string, a ...any) *Problem {
	p.Problems = append(p.Problems, Problem{Code: code, Cause: cause, Message: fmt.Sprintf(format, a...)})
	return &p.Problems[len(p.Problems)-1]
}

func (p *Plan) with(pr *Problem, k string, v any) {
	if pr.Details == nil {
		pr.Details = map[string]any{}
	}
	pr.Details[k] = v
}

// legacyAlias is the name M8 asks for: the first legacy alias, else the model id.
func (p *Plan) legacyAlias() string {
	if len(p.Aliases) > 0 {
		return p.Aliases[0]
	}
	return p.ModelID
}

var aliasRe = regexp.MustCompile(`^[a-z0-9][a-z0-9.\-_:]{1,63}$`)

// ggmlEnv is the part of the old unit's environment that is carried over (spec M4: GGML_*).
var ggmlEnv = regexp.MustCompile(`^GGML_[A-Z0-9_]+$`)

// Preflight is step M0: it reads everything it needs, changes nothing (not even plan.json: Run writes
// that) and returns the plan with its problems. An error means a read failed in a way that says nothing
// about the node (the program could not run at all).
func (e *Engine) Preflight(ctx context.Context) (*Plan, error) {
	p := &Plan{Version: planVersion, CreatedAt: e.clock.Now().UTC().Truncate(time.Second), AcceptedFlags: false}
	cfg := e.config.Get()

	// Preconditions (4.9.1): the agent is installed and the catalog is loaded.
	if _, _, uerr := e.users.LookupUser(ServiceUser); uerr != nil {
		e.add(p, CodeStopped, "agent_not_installed", "the %q user does not exist: install the agent first (the installer does this before it migrates)", ServiceUser)
	}
	if fi, err := e.fs.Stat(e.paths.InternalRoot); err != nil || !fi.IsDir() {
		e.add(p, CodeStopped, "agent_not_installed", "%s does not exist: install the agent first", e.paths.InternalRoot)
	}
	if e.cat == nil || len(e.cat.Models) == 0 {
		e.add(p, CodeStopped, "no_catalog", "the catalog is not loaded or has no models: the node's current model must be in it, with its exact sha256")
	}
	if cfg.Ports.Llama != 0 && cfg.Ports.Llama != LegacyPort {
		e.add(p, CodeStopped, "port", "node.yaml ports.llama is %d; the migration keeps port %d", cfg.Ports.Llama, LegacyPort)
	}
	if st, err := e.sd.State(ctx, NewUnitName); err == nil && (st.ActiveState == "active" || st.ActiveState == "activating") {
		e.add(p, CodeStopped, "new_unit_running", "%s is already running: stop it first, it would take port %d", NewUnitName, LegacyPort)
	}

	// 1. The legacy unit.
	text, err := e.catLegacy(ctx)
	if err != nil {
		e.add(p, CodeStopped, "unit_unreadable", "cannot read %s: %v", LegacyUnitName, err)
		return p, nil
	}
	u, perr := ParseUnit(text)
	var flags *ExecFlags
	if perr == nil {
		flags, perr = ParseExec(u.ExecStart)
	}
	if perr != nil {
		e.add(p, CodeStopped, "unit_unreadable", "%v", perr)
		return p, nil
	}
	p.User, p.Group, p.WorkingDirectory = u.User, u.Group, u.WorkingDirectory
	p.Binary, p.Ctx = flags.Binary, flags.Ctx
	p.Host, p.Port = orStr(flags.Host, LoopbackHost), orInt(flags.Port, LegacyPort)

	// 6. Host and port.
	if p.Host != LoopbackHost || p.Port != LegacyPort {
		e.add(p, CodeStopped, "host_port", "the unit listens on %s:%d; the migration needs --host %s --port %d", p.Host, p.Port, LoopbackHost, LegacyPort)
	}
	e.checkKeyWiring(p, u, flags)

	// 2. The key.
	key, kerr := e.readKey(e.paths.LegacyKey)
	switch {
	case errors.Is(kerr, errBadKeyShape):
		e.add(p, CodeCredShape, "key_shape", "%s is not 64 lowercase hex digits; the migration will not copy a key it cannot vouch for", e.paths.LegacyKey)
	case kerr != nil:
		e.add(p, CodeStopped, "key_unreadable", "cannot read the legacy key %s: %v", e.paths.LegacyKey, kerr)
	default:
		p.KeyFingerprint = keyFingerprint(key)
		if cur, err := e.fs.ReadFile(e.paths.NewKey); err == nil && string(cur) != string(key) {
			e.add(p, CodeStopped, "key_differs", "%s already exists and holds a different key: the migration keeps the key clients use, so remove or reconcile one of them", e.paths.NewKey)
		}
	}

	// Aliases and environment.
	e.planNames(p, u, flags)

	// 3 and 4. The model files, and the flags against the catalog.
	if flags.Model == "" {
		e.add(p, CodeStopped, "no_model", "the unit has no -m: only a single-model unit can be migrated")
	} else if e.cat != nil {
		e.matchModel(ctx, p, flags)
	}

	// 5. Tailscale Serve, as it is now.
	p.ServeBefore = e.captureServe(ctx)

	// 7. The build.
	e.planBuild(ctx, p, u, flags)

	// The public URL as it is now (only when the internet may be asked).
	if host := cfg.Node.PublicHost; host != nil && *host != "" && e.online() {
		p.PublicHost = *host
		if st, err := e.probe.Public(ctx, *host); err == nil {
			p.PublicBefore = st
		}
	}
	return p, nil
}

func (e *Engine) add(p *Plan, code, cause, format string, a ...any) *Problem {
	return p.problem(code, cause, format, a...)
}

func orStr(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

func orInt(n, d int) int {
	if n == 0 {
		return d
	}
	return n
}

// catLegacy runs `systemctl cat llama-qwen.service`.
func (e *Engine) catLegacy(ctx context.Context) (string, error) {
	res, err := e.runNamed(ctx, "systemctl", "--no-pager", "cat", LegacyUnitName)
	if err != nil {
		return "", err
	}
	if res.ExitCode != 0 {
		msg := strings.TrimSpace(string(res.Stderr))
		if msg == "" {
			msg = strings.TrimSpace(string(res.Stdout))
		}
		return "", fmt.Errorf("systemctl cat exited %d: %s", res.ExitCode, clip(msg, 200))
	}
	return string(res.Stdout), nil
}

// checkKeyWiring checks that the unit takes its key from /etc/llama-qwen/api-key.
func (e *Engine) checkKeyWiring(p *Plan, u *LegacyUnit, f *ExecFlags) {
	if f.InlineKey {
		e.add(p, CodeStopped, "key_inline", "the unit passes the API key on the command line (--api-key); move it to a key file first")
		return
	}
	src := ""
	switch {
	case f.APIKeyFile == "":
		e.add(p, CodeStopped, "no_key", "the unit has no --api-key-file: there is no key for the migration to keep")
		return
	case strings.HasPrefix(f.APIKeyFile, "%d/"):
		id := strings.TrimPrefix(f.APIKeyFile, "%d/")
		for _, c := range u.Credentials {
			if c.ID == id {
				src = c.Path
			}
		}
	default:
		src = f.APIKeyFile
	}
	if src != e.paths.LegacyKey {
		e.add(p, CodeStopped, "key_path", "the unit reads its key from %q, not %s", src, e.paths.LegacyKey)
	}
}

// planNames works out the legacy aliases and the environment to carry over.
func (e *Engine) planNames(p *Plan, u *LegacyUnit, f *ExecFlags) {
	seen := map[string]bool{}
	for _, a := range f.Aliases {
		if seen[a] {
			continue
		}
		seen[a] = true
		if !aliasRe.MatchString(a) {
			e.add(p, CodeStopped, "alias_invalid", "the alias %q cannot be kept: an alias is 2 to 64 characters, lower case letters, digits and . - _ :", a)
			continue
		}
		p.Aliases = append(p.Aliases, a)
	}
	var ignored []string
	for _, v := range u.Env {
		if ggmlEnv.MatchString(v.Name) {
			if p.ExtraEnv == nil {
				p.ExtraEnv = map[string]string{}
			}
			p.ExtraEnv[v.Name] = v.Value
			continue
		}
		ignored = append(ignored, v.Name)
	}
	if len(ignored) > 0 {
		p.Warnings = append(p.Warnings, "not carried over (only GGML_* is): Environment="+strings.Join(ignored, ", "))
	}
	if len(u.EnvironmentFiles) > 0 {
		p.Warnings = append(p.Warnings, "not carried over: EnvironmentFile="+strings.Join(u.EnvironmentFiles, ", "))
	}
}

// matchModel is steps 3 and 4 of M0: hash the files, find the catalog model, diff the flags.
func (e *Engine) matchModel(ctx context.Context, p *Plan, f *ExecFlags) {
	bySHA := map[string]struct {
		m    *catalog.Model
		file catalog.ModelFile
	}{}
	for i := range e.cat.Models {
		m := &e.cat.Models[i]
		for _, cf := range m.Files {
			if cf.SHA256 != "" {
				bySHA[strings.ToLower(cf.SHA256)] = struct {
					m    *catalog.Model
					file catalog.ModelFile
				}{m, cf}
			}
		}
	}
	type named struct{ role, path string }
	var matched *catalog.Model
	kinds := map[string]string{} // role -> catalog kind
	done := map[string]bool{}    // catalog file name -> already in plan
	hashed := 0
	for _, n := range []named{{"-m", f.Model}, {"-md", f.Draft}, {"--mmproj", f.Mmproj}} {
		if n.path == "" {
			continue
		}
		sum, size, err := e.hashNamed(ctx, p, n.path)
		hashed++
		if err != nil {
			pr := e.add(p, CodeStopped, "file_unreadable", "cannot read the model file %s (%s): %v", path.Base(n.path), n.role, err)
			p.with(pr, "path", n.path)
			continue
		}
		hit, ok := bySHA[sum]
		if !ok {
			pr := e.add(p, CodeStopped, "unmatched_file", "model file not in the catalog: %s (%s, sha256 %s). Add the model to the catalog with this sha256, or download the catalog entry's file", path.Base(n.path), n.role, sum)
			p.with(pr, "path", n.path)
			p.with(pr, "sha256", sum)
			continue
		}
		if matched == nil {
			matched = hit.m
		} else if matched.ID != hit.m.ID {
			pr := e.add(p, CodeStopped, "files_of_two_models", "%s belongs to the catalog model %s, but -m is %s", path.Base(n.path), hit.m.ID, matched.ID)
			p.with(pr, "path", n.path)
			continue
		}
		kinds[n.role] = hit.file.Kind
		done[hit.file.Name] = true
		p.Files = append(p.Files, PlanFile{Kind: hit.file.Kind, Name: hit.file.Name, Old: n.path, SHA256: sum, Size: size})
	}
	if matched == nil {
		return
	}
	p.ModelID = matched.ID
	p.CatalogVersion = e.cat.Version

	// The other files of the catalog entry (the shards of a split model) sit next to the named ones.
	dirs := map[string]bool{}
	for _, pf := range p.Files {
		dirs[path.Dir(pf.Old)] = true
	}
	for _, cf := range matched.Files {
		if done[cf.Name] {
			continue
		}
		found := ""
		for _, d := range sortedKeys(dirs) {
			if exists(e.fs, path.Join(d, cf.Name)) {
				found = path.Join(d, cf.Name)
				break
			}
		}
		if found == "" {
			pr := e.add(p, CodeStopped, "file_missing", "the catalog's %s file %s is not next to the model files: fetch it first (aictl download %s, or aictl ark import)", cf.Kind, cf.Name, matched.ID)
			p.with(pr, "name", cf.Name)
			continue
		}
		sum, size, err := e.hashNamed(ctx, p, found)
		if err != nil || sum != strings.ToLower(cf.SHA256) {
			pr := e.add(p, CodeStopped, "file_mismatch", "%s does not match the catalog's sha256 for %s", found, cf.Name)
			p.with(pr, "path", found)
			continue
		}
		p.Files = append(p.Files, PlanFile{Kind: cf.Kind, Name: cf.Name, Old: found, SHA256: sum, Size: size})
	}
	for i := range p.Files {
		pf := &p.Files[i]
		if !manifest.ValidFilePath(pf.Name) {
			e.add(p, CodeStopped, "file_name", "the catalog file name %q is not a plain relative path", pf.Name)
			continue
		}
		pf.New = filepath.Join(e.paths.ModelDir(matched.ID), filepath.FromSlash(pf.Name))
	}
	sort.SliceStable(p.Files, func(i, j int) bool { return p.Files[i].Name < p.Files[j].Name })

	// 4. The flags against the catalog's llama block.
	p.Diffs = DiffCatalog(f, matched, kinds["-md"], kinds["--mmproj"])
	p.ExpectedCtx = f.Ctx
	if len(p.Diffs) > 0 {
		if !e.acceptFlags {
			var lines []string
			for _, d := range p.Diffs {
				lines = append(lines, d.String())
			}
			pr := e.add(p, CodeStopped, "flags_differ", "the unit's settings differ from the catalog's for %s: %s. Re-run with --accept-catalog-flags to use the catalog's values", matched.ID, strings.Join(lines, "; "))
			p.with(pr, "diffs", p.Diffs)
		} else {
			p.AcceptedFlags = true
			if matched.Llama != nil {
				p.ExpectedCtx = matched.Llama.Ctx
			}
		}
	}
	e.checkSpace(p)
}

// hashNamed hashes one file, telling the person which one (a 48 GB model takes a while).
func (e *Engine) hashNamed(ctx context.Context, p *Plan, file string) (sum string, size int64, err error) {
	fi, err := e.fs.Stat(file)
	if err != nil {
		return "", 0, err
	}
	if !fi.Mode().IsRegular() {
		return "", 0, errors.New("not a regular file")
	}
	e.say("  hashing %s (%s) ...", path.Base(file), humanBytes(fi.Size()))
	sum, err = e.sha256Of(ctx, file)
	return sum, fi.Size(), err
}

// checkSpace makes sure the files that must be copied (they are on another filesystem than /srv/localai)
// fit, with a margin.
func (e *Engine) checkSpace(p *Plan) {
	var need int64
	for _, pf := range p.Files {
		if !e.sameFS(pf.Old, e.paths.ModelDir(p.ModelID)) {
			need += pf.Size
		}
	}
	if need == 0 {
		return
	}
	free, err := e.fs.FreeBytes(nearestExisting(e.fs, e.paths.ModelDir(p.ModelID)))
	if err != nil {
		p.Warnings = append(p.Warnings, "cannot tell how much space is free on "+e.paths.InternalRoot)
		return
	}
	const margin = 1 << 30
	if free < uint64(need)+margin {
		pr := e.add(p, CodeStopped, "disk_full", "the model files are on another filesystem and must be copied: %s needed, %s free on %s", humanBytes(need), humanBytes(int64(free)), e.paths.InternalRoot)
		p.with(pr, "need_bytes", need)
		p.with(pr, "free_bytes", free)
	}
}

// sameFS reports whether old and the (possibly not yet created) directory dir are on one filesystem.
func (e *Engine) sameFS(old, dir string) bool {
	a, err1 := e.fs.Dev(old)
	b, err2 := e.fs.Dev(nearestExisting(e.fs, dir))
	return err1 == nil && err2 == nil && a == b
}

// captureServe is step 5: `tailscale serve status --json`, in a canonical form so that two reads of an
// unchanged configuration are byte-identical. A node without Tailscale (or with it stopped) gets a fixed
// marker: the check at M8 then compares "unavailable" with "unavailable".
func (e *Engine) captureServe(ctx context.Context) []byte {
	res, err := e.runNamed(ctx, "tailscale", "serve", "status", "--json")
	if err != nil || res.ExitCode != 0 {
		return []byte(`{"unavailable":true}`)
	}
	var v any
	if json.Unmarshal(res.Stdout, &v) != nil {
		return []byte(`{"unavailable":true}`)
	}
	b, err := json.Marshal(v)
	if err != nil {
		return []byte(`{"unavailable":true}`)
	}
	return b
}

// planBuild is step 7: current exists, or the build of M1 is possible.
func (e *Engine) planBuild(ctx context.Context, p *Plan, u *LegacyUnit, f *ExecFlags) {
	p.Backend = e.config.Get().Llama.Backend
	if p.Backend == "" {
		p.Backend = backendOf(f.Binary)
	}
	p.CurrentCommit = e.currentCommit()
	p.LegacyCommit = e.legacyCommit(ctx, f.Binary)
	switch {
	case p.LegacyCommit == "" && p.CurrentCommit == "":
		e.add(p, CodeStopped, "commit_unknown", "cannot tell which llama.cpp commit %s was built from (%s --version shows none), and there is no build in %s: run aictl rebuild-llama first", path.Base(path.Dir(path.Dir(f.Binary))), path.Base(f.Binary), e.paths.Current())
		return
	case p.LegacyCommit == "":
		p.Warnings = append(p.Warnings, "the legacy binary's commit is unknown; the build in current is kept")
		return
	case p.CurrentCommit != "" && sameCommit(p.CurrentCommit, p.LegacyCommit):
		return
	}
	p.BuildNeeded = true
	if p.Backend == "" {
		e.add(p, CodeStopped, "backend_unknown", "cannot tell the backend (cuda, vulkan or cpu): set llama.backend in node.yaml")
		return
	}
	if err := e.builder.CanBuild(ctx, p.Backend); err != nil {
		code := CodeOf(err)
		if code == "" {
			code = CodeBuild
		}
		pr := e.add(p, code, "cannot_build", "llama.cpp %s cannot be built for %s here: %v", short(p.LegacyCommit, 9), p.Backend, err)
		p.with(pr, "commit", p.LegacyCommit)
	}
}

// backendOf guesses the backend from the legacy checkout's build directory name (build-cuda, ...).
func backendOf(binary string) string {
	dir := path.Base(path.Dir(path.Dir(binary)))
	for _, b := range []string{"cuda", "vulkan", "cpu"} {
		if strings.HasSuffix(dir, b) {
			return b
		}
	}
	return ""
}

var commitRe = regexp.MustCompile(`(?:commit[ :]+|\()([0-9a-f]{7,40})\b`)

// legacyCommit asks the legacy binary for its commit (`--version`: "commit <sha>" or "(<sha>)").
func (e *Engine) legacyCommit(ctx context.Context, binary string) string {
	res, err := e.run(ctx, e.timing.VersionCmd, binary, "--version")
	if err != nil {
		return ""
	}
	out := string(res.Stdout) + "\n" + string(res.Stderr)
	m := commitRe.FindStringSubmatch(out)
	if m == nil {
		return ""
	}
	return m[1]
}

// currentCommit reads the commit of the build current points at ("" when there is none).
func (e *Engine) currentCommit() string {
	data, err := e.fs.ReadFile(path.Join(e.paths.Current(), "BUILD.json"))
	if err != nil {
		return ""
	}
	var b struct {
		Commit string `json:"commit"`
	}
	if json.Unmarshal(data, &b) != nil {
		return ""
	}
	return b.Commit
}

// sameCommit compares a short sha with a full one (or two short ones) by their common prefix.
func sameCommit(a, b string) bool {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	return n >= 7 && strings.EqualFold(a[:n], b[:n])
}

// online says whether the internet may be asked (state.connectivity, written by the agent's prober).
func (e *Engine) online() bool {
	c := e.state.Connectivity()
	return c == "full" || c == "internet"
}

// ---- display --------------------------------------------------------------------------------------

// Steps returns what the migration would do, one line per step, for the dry run and the confirmation.
func (e *Engine) Steps(p *Plan) []string {
	var out []string
	add := func(id, format string, a ...any) {
		out = append(out, fmt.Sprintf("%-4s %s", id, fmt.Sprintf(format, a...)))
	}
	status := "ok"
	if !p.OK() {
		status = fmt.Sprintf("%d problem(s)", len(p.Problems))
	}
	add(M0, "preflight: %s (model %s, key %s)", status, orStr(p.ModelID, "unknown"), orStr(p.KeyFingerprint, "unknown"))
	if p.BuildNeeded {
		add(M1, "build llama.cpp %s for %s from %s, then switch current", short(p.LegacyCommit, 9), p.Backend, orStr(p.WorkingDirectory, "the kit mirror"))
	} else {
		add(M1, "no build needed: current already has commit %s", short(orStr(p.CurrentCommit, p.LegacyCommit), 9))
	}
	add(M2, "copy the API key to %s (byte copy, root 0600)", e.paths.NewKey)
	for _, f := range p.Files {
		how := "rename (same filesystem), leave a symlink"
		if f.New != "" && !e.sameFS(f.Old, e.paths.ModelDir(p.ModelID)) {
			how = "copy, verify the sha256, keep the original until M9"
		}
		add(M3, "move %s to %s: %s", f.Old, f.New, how)
	}
	al := "no legacy alias"
	if len(p.Aliases) > 0 {
		al = "legacy alias " + strings.Join(p.Aliases, ", ")
	}
	add(M4, "write node.yaml (%s), state.models.default = %s, models.ini, localai-llama.service; daemon-reload; do not start", al, orStr(p.ModelID, "?"))
	add(M5, "check %s can read the files and run llama-server, and that the old service owns port %d", ServiceUser, LegacyPort)
	add(M6, "stop %s (the downtime starts)", LegacyUnitName)
	add(M7, "start %s, wait for /health and for %s to be loaded", NewUnitName, orStr(p.ModelID, "the model"))
	add(M8, "verify: 401 without key, 200 with key, %s answers, serve config unchanged, n_ctx %d; failure rolls back automatically", p.legacyAlias(), p.ExpectedCtx)
	add(M9, "mask %s (unit saved in %s), enable %s, link %s to %s", LegacyUnitName, e.paths.MigDir, NewUnitName, e.paths.LegacyKey, e.paths.NewKey)
	add(M10, "report; no client needs a change")
	return out
}

// RenderPlan prints the plan for a person: the steps, the warnings, the flag differences accepted and
// every problem. It is what --dry-run prints.
func (e *Engine) RenderPlan(w io.Writer, p *Plan) {
	if p.Resume {
		fmt.Fprintf(w, "Resuming an interrupted migration (last completed step %s).\n", orStr(p.Progress.Step, "none"))
	}
	for _, l := range e.Steps(p) {
		fmt.Fprintln(w, l)
	}
	if p.AcceptedFlags {
		fmt.Fprintln(w, "\nThe catalog's values replace these settings of the old unit:")
		for _, d := range p.Diffs {
			fmt.Fprintf(w, "  %s\n", d)
		}
	}
	if len(p.Warnings) > 0 {
		fmt.Fprintln(w)
		for _, x := range p.Warnings {
			fmt.Fprintf(w, "warning: %s\n", x)
		}
	}
	if len(p.Problems) > 0 {
		fmt.Fprintln(w, "\nThe migration cannot start:")
		for _, pr := range p.Problems {
			fmt.Fprintf(w, "  %s %s\n", pr.Code, pr.Message)
		}
		for _, code := range uniqueCodes(p.Problems) {
			if title, fix, ok := doctor.Text(code, "en"); ok {
				fmt.Fprintf(w, "\n%s: %s\nWhat to do: %s\n", code, title, fix)
			}
		}
	}
}

func uniqueCodes(ps []Problem) []string {
	seen := map[string]bool{}
	var out []string
	for _, p := range ps {
		if !seen[p.Code] {
			seen[p.Code] = true
			out = append(out, p.Code)
		}
	}
	return out
}

func humanBytes(n int64) string {
	const g = 1 << 30
	switch {
	case n >= g:
		return fmt.Sprintf("%.1f GiB", float64(n)/g)
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
	}
	return fmt.Sprintf("%d bytes", n)
}
