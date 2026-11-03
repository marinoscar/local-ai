package migrate

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/audit"
	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/sync/manifest"
)

// adoptedSource is the manifest source type of a model the migration took over (ark-sync.md section
// 4.11): the files were already on the node, and the catalog's sha256 matched them.
const adoptedSource = "adopted"

// ---- M1 build -------------------------------------------------------------------------------------

// m1Build builds the legacy binary's commit when current is missing or at another commit, and switches
// current to it. The old service keeps running; the new one is not started.
func (e *Engine) m1Build(ctx context.Context, p *Plan) error {
	if !p.BuildNeeded {
		return nil
	}
	if c := e.currentCommit(); c != "" && sameCommit(c, p.LegacyCommit) {
		return nil // an earlier run already switched
	}
	id, ok := e.findBuild(p.LegacyCommit, p.Backend)
	if !ok {
		res, err := e.builder.Build(ctx, BuildRequest{
			Commit: p.LegacyCommit, Backend: p.Backend, SourceRepo: p.WorkingDirectory,
			Progress: func(s string) { e.say("     %s", s) },
		})
		if err != nil {
			code := CodeOf(err)
			if code == "" {
				code = CodeBuild
			}
			return wrap(code, M1, err, "the llama.cpp build failed; nothing was changed")
		}
		id = res.BuildID
	}
	if err := e.switchCurrent(id); err != nil {
		return err
	}
	if err := e.state.RecordBuild(id); err != nil {
		e.warnf("cannot record the build %s in state.json: %v", id, err)
	}
	return e.verifyCurrent(ctx, p)
}

// findBuild looks for a finished build of the commit and backend (an earlier run built it and was
// killed before switching), so a long compile is never repeated.
func (e *Engine) findBuild(commit, backend string) (string, bool) {
	entries, err := e.fs.ReadDir(e.paths.Builds())
	if err != nil {
		return "", false
	}
	for _, ent := range entries {
		name := ent.Name()
		if !ent.IsDir() || !strings.HasSuffix(name, "-"+backend) {
			continue
		}
		data, err := e.fs.ReadFile(filepath.Join(e.paths.Builds(), name, "BUILD.json"))
		if err != nil {
			continue
		}
		var b struct {
			Commit string `json:"commit"`
		}
		if json.Unmarshal(data, &b) == nil && sameCommit(b.Commit, commit) {
			return name, true
		}
	}
	return "", false
}

// switchCurrent points current at builds/<id> with a new link renamed over the old one (atomic).
func (e *Engine) switchCurrent(id string) error {
	target := filepath.Join("builds", id)
	if t, err := e.fs.Readlink(e.paths.Current()); err == nil && t == target {
		return nil
	}
	tmp := e.paths.Current() + ".tmp"
	if err := e.removeFile(tmp); err != nil {
		return wrap(CodeInterrupted, M1, err, "cannot clear %s", tmp)
	}
	if err := e.fs.Symlink(target, tmp); err != nil {
		return wrap(CodeInterrupted, M1, err, "cannot create the link to the new build")
	}
	if err := e.fs.Rename(tmp, e.paths.Current()); err != nil {
		_ = e.fs.Remove(tmp)
		return wrap(CodeInterrupted, M1, err, "cannot switch current to %s", id)
	}
	_ = syncDir(e.fs, e.paths.LlamaRoot)
	return nil
}

var deviceLine = regexp.MustCompile(`(?m)^\s*([A-Za-z]+)(\d+):\s*(.*)$`)

// verifyCurrent is the check of M1: `current/bin/llama-server --version` shows the commit, and
// `--list-devices` shows the GPU (CUDA0, or a Vulkan device that is not llvmpipe).
func (e *Engine) verifyCurrent(ctx context.Context, p *Plan) error {
	bin := path.Join(e.paths.Current(), "bin", "llama-server")
	res, err := e.run(ctx, e.timing.VersionCmd, bin, "--version")
	if err != nil || res.ExitCode != 0 {
		return fail(CodeBuild, M1, "%s --version failed", bin)
	}
	if out := string(res.Stdout) + string(res.Stderr); !strings.Contains(out, short(p.LegacyCommit, 7)) {
		return fail(CodeBuild, M1, "%s --version does not show commit %s", bin, short(p.LegacyCommit, 9))
	}
	if p.Backend == "cpu" || p.Backend == "" {
		return nil
	}
	dev, err := e.run(ctx, e.timing.VersionCmd, bin, "--list-devices")
	if err != nil || dev.ExitCode != 0 {
		return fail(CodeBuild, M1, "%s --list-devices failed", bin)
	}
	good := false
	for _, m := range deviceLine.FindAllStringSubmatch(string(dev.Stdout)+"\n"+string(dev.Stderr), -1) {
		kind, rest := strings.ToLower(m[1]), strings.ToLower(m[3])
		switch p.Backend {
		case "cuda":
			good = good || kind == "cuda"
		case "vulkan":
			good = good || (kind == "vulkan" && !strings.Contains(rest, "llvmpipe"))
		}
	}
	if !good {
		return fail(CodeBuild, M1, "%s --list-devices shows no %s GPU (a CPU-only Vulkan device does not count)", bin, p.Backend)
	}
	return nil
}

// ---- M2 key ---------------------------------------------------------------------------------------

// m2Key copies the legacy key byte for byte to /etc/localai/api-key (root 0600). An existing target must
// be identical.
func (e *Engine) m2Key(_ context.Context, p *Plan) error {
	key, err := e.readKey(e.paths.LegacyKey)
	if err != nil {
		if err == errBadKeyShape {
			return fail(CodeBadKey, M2, "%s is not 64 lowercase hex digits", e.paths.LegacyKey)
		}
		return wrap(CodeStopped, M2, err, "cannot read the legacy key")
	}
	if cur, err := e.fs.ReadFile(e.paths.NewKey); err == nil {
		if !bytes.Equal(cur, key) {
			return fail(CodeStopped, M2, "%s already exists and holds a different key", e.paths.NewKey)
		}
		return e.fs.Chmod(e.paths.NewKey, 0o600)
	}
	if err := e.fs.MkdirAll(path.Dir(e.paths.NewKey), 0o750); err != nil {
		return wrap(CodeInterrupted, M2, err, "cannot create %s", path.Dir(e.paths.NewKey))
	}
	if err := config.WriteFileAtomicFS(e.fs, e.paths.NewKey, key, 0o600, e.rootUID, e.rootGID); err != nil {
		return wrap(CodeInterrupted, M2, err, "cannot write %s", e.paths.NewKey)
	}
	got, err := e.fs.ReadFile(e.paths.NewKey)
	if err != nil || !bytes.Equal(got, key) {
		return fail(CodeInterrupted, M2, "the copy of the key at %s does not match", e.paths.NewKey)
	}
	return nil
}

// ---- M3 models ------------------------------------------------------------------------------------

// m3Models moves every model file under /srv/localai/models/<id>/ (a rename on one filesystem, a verified
// copy across two), leaves a symbolic link behind, and writes MANIFEST.json last.
func (e *Engine) m3Models(ctx context.Context, p *Plan) error {
	uid, gid, err := e.users.LookupUser(ServiceUser)
	if err != nil {
		return wrap(CodeStopped, M3, err, "the %q user does not exist", ServiceUser)
	}
	if g, err := e.users.LookupGroup(ServiceUser); err == nil {
		gid = g
	}
	dir := e.paths.ModelDir(p.ModelID)
	if err := e.fs.MkdirAll(dir, 0o755); err != nil {
		return wrap(CodeInterrupted, M3, err, "cannot create %s", dir)
	}
	if err := e.fs.Chown(dir, uid, gid); err != nil {
		return wrap(CodeInterrupted, M3, err, "cannot give %s to %s", dir, ServiceUser)
	}
	if err := e.fs.Chmod(dir, 0o755); err != nil {
		return wrap(CodeInterrupted, M3, err, "cannot set the mode of %s", dir)
	}
	for _, f := range p.Files {
		if err := e.moveFile(ctx, f, dir, uid, gid); err != nil {
			return err
		}
	}
	return e.writeManifest(p, uid, gid)
}

// moveFile brings one file to its new place, whatever state an earlier run left it in.
func (e *Engine) moveFile(ctx context.Context, f PlanFile, newDir string, uid, gid int) error {
	if err := e.fs.MkdirAll(path.Dir(f.New), 0o755); err != nil {
		return wrap(CodeInterrupted, M3, err, "cannot create %s", path.Dir(f.New))
	}
	oldFi, oldErr := e.fs.Lstat(f.Old)
	newFi, newErr := e.fs.Lstat(f.New)
	oldLinked := false
	if oldErr == nil && isLink(oldFi) {
		t, _ := e.fs.Readlink(f.Old)
		if t != f.New {
			return fail(CodeInterrupted, M3, "%s is a link to %s, not to %s", f.Old, t, f.New)
		}
		oldLinked = true
	}
	switch {
	case oldLinked && newErr == nil:
		// Done by an earlier run.
	case oldLinked:
		return fail(CodeInterrupted, M3, "%s points at %s, which is missing", f.Old, f.New)
	case oldErr != nil:
		// The rename happened and the run died before the link was made.
		if newErr != nil {
			return fail(CodeInterrupted, M3, "neither %s nor %s exists", f.Old, f.New)
		}
		if err := e.fs.Symlink(f.New, f.Old); err != nil {
			return wrap(CodeInterrupted, M3, err, "cannot link %s to %s", f.Old, f.New)
		}
	default:
		sameFS := e.sameFS(f.Old, newDir)
		switch {
		case newErr == nil && newFi.Size() != f.Size:
			return fail(CodeInterrupted, M3, "%s exists with another size than the catalog says", f.New)
		case newErr == nil && sameFS:
			// Both exist on one filesystem (the model was fetched separately): prove the new one before
			// the old one is replaced by a link.
			sum, err := e.sha256Of(ctx, f.New)
			if err != nil || sum != f.SHA256 {
				return fail(CodeInterrupted, M3, "%s exists but its sha256 is not the catalog's", f.New)
			}
			if err := e.replaceWithLink(f.Old, f.New); err != nil {
				return err
			}
		case newErr == nil:
			// Copied across filesystems by an earlier run (renamed into place only after its sha256
			// matched): the original stays until M9.
		case sameFS:
			// The running server keeps its open descriptor and its mapping through a rename.
			if err := e.fs.Rename(f.Old, f.New); err != nil {
				return wrap(CodeInterrupted, M3, err, "cannot move %s to %s", f.Old, f.New)
			}
			if err := e.fs.Symlink(f.New, f.Old); err != nil {
				return wrap(CodeInterrupted, M3, err, "cannot link %s to %s", f.Old, f.New)
			}
		default:
			if err := e.copyVerified(ctx, f); err != nil {
				return err
			}
		}
	}
	if err := e.fs.Chown(f.New, uid, gid); err != nil {
		return wrap(CodeInterrupted, M3, err, "cannot give %s to %s", f.New, ServiceUser)
	}
	if err := e.fs.Chmod(f.New, 0o644); err != nil {
		return wrap(CodeInterrupted, M3, err, "cannot set the mode of %s", f.New)
	}
	return nil
}

// copyVerified copies across filesystems: to <new>.part, fsync, read it back and check the sha256, then
// rename. A .part left by a killed run is thrown away and the copy starts again.
func (e *Engine) copyVerified(ctx context.Context, f PlanFile) error {
	part := f.New + ".part"
	if err := e.removeFile(part); err != nil {
		return wrap(CodeInterrupted, M3, err, "cannot clear %s", part)
	}
	e.say("     copying %s (%s) ...", f.Name, humanBytes(f.Size))
	if err := e.copyFile(ctx, f.Old, part, 0o644); err != nil {
		return wrap(CodeInterrupted, M3, err, "cannot copy %s to %s", f.Old, part)
	}
	sum, err := e.sha256Of(ctx, part)
	if err != nil {
		_ = e.fs.Remove(part)
		return wrap(CodeInterrupted, M3, err, "cannot read %s back", part)
	}
	if sum != f.SHA256 {
		_ = e.fs.Remove(part)
		return fail(CodeInterrupted, M3, "the copy of %s does not match the catalog's sha256: the original is untouched", f.Name)
	}
	if err := e.fs.Rename(part, f.New); err != nil {
		return wrap(CodeInterrupted, M3, err, "cannot move %s into place", part)
	}
	_ = syncDir(e.fs, path.Dir(f.New))
	return nil
}

// replaceWithLink makes old a symbolic link to target in one atomic step.
func (e *Engine) replaceWithLink(old, target string) error {
	tmp := old + ".lnk-tmp"
	if err := e.removeFile(tmp); err != nil {
		return wrap(CodeInterrupted, M3, err, "cannot clear %s", tmp)
	}
	if err := e.fs.Symlink(target, tmp); err != nil {
		return wrap(CodeInterrupted, M3, err, "cannot create a link to %s", target)
	}
	if err := e.fs.Rename(tmp, old); err != nil {
		_ = e.fs.Remove(tmp)
		return wrap(CodeInterrupted, M3, err, "cannot replace %s by a link", old)
	}
	return nil
}

// writeManifest writes MANIFEST.json (source adopted) once every file is in place: it is the commit
// point of the model, and the thing the effective set looks for.
func (e *Engine) writeManifest(p *Plan, uid, gid int) error {
	dir := e.paths.ModelDir(p.ModelID)
	mf := manifest.Path(dir)
	if m, err := manifest.Read(e.fs, mf); err == nil && m.ItemID == p.ModelID && m.Source.Type == adoptedSource && len(m.Files) == len(p.Files) {
		return nil
	}
	model, ok := e.cat.Model(p.ModelID)
	if !ok {
		return fail(CodeStopped, M3, "the model %s is not in the catalog any more", p.ModelID)
	}
	now := e.clock.Now().UTC().Truncate(time.Second)
	src := manifest.Source{Type: adoptedSource}
	for k, v := range map[string]string{"repo": model.Source.Repo, "revision": model.Source.Revision} {
		if v == "" {
			continue
		}
		var err error
		if src, err = src.With(k, v); err != nil {
			return wrap(CodeInterrupted, M3, err, "cannot build the manifest")
		}
	}
	version := adoptedSource
	if model.Source.Revision != "" {
		version = short(model.Source.Revision, 12)
	}
	m := manifest.Manifest{
		SchemaVersion: manifest.SchemaVersion, ItemID: p.ModelID, ItemKind: manifest.KindModel, Engine: model.Engine,
		CatalogVersion: p.CatalogVersion, Version: version, SortKey: now.Format(time.RFC3339), State: manifest.StateVerified,
		Source: src, DownloadedAt: now, VerifiedAt: now, LastReverifiedAt: now, Trust: manifest.TrustPinned,
		SyncedBy: manifest.SyncedBy{AgentVersion: e.version},
	}
	for _, f := range p.Files {
		m.Files = append(m.Files, manifest.File{Kind: f.Kind, Path: f.Name, SizeBytes: f.Size, SHA256: f.SHA256, SHA256Source: adoptedSource})
		m.TotalBytes += f.Size
	}
	if err := manifest.Write(e.fs, mf, m); err != nil {
		return wrap(CodeInterrupted, M3, err, "cannot write %s", mf)
	}
	if err := e.fs.Chown(mf, uid, gid); err != nil {
		return wrap(CodeInterrupted, M3, err, "cannot give %s to %s", mf, ServiceUser)
	}
	return nil
}

// ---- M4 config ------------------------------------------------------------------------------------

// m4Config records the legacy aliases and environment in node.yaml, the model as the default, and
// renders models.ini and the unit (daemon-reload included). It does not enable or start anything.
func (e *Engine) m4Config(ctx context.Context, p *Plan) error {
	ch := ConfigChange{Commit: e.currentCommit()}
	if len(p.Aliases) > 0 {
		ch.LegacyAliases = map[string][]string{p.ModelID: p.Aliases}
	}
	if len(p.ExtraEnv) > 0 {
		ch.ExtraEnv = p.ExtraEnv
	}
	if ch.Commit != "" && sameCommit(ch.Commit, e.config.Get().Llama.Commit) {
		ch.Commit = ""
	}
	if err := e.config.Record(ctx, ch); err != nil {
		return wrap(CodeInterrupted, M4, err, "cannot update node.yaml")
	}
	if err := e.state.SetDefaultModel(p.ModelID); err != nil {
		return wrap(CodeInterrupted, M4, err, "cannot record the default model")
	}
	out, err := e.render.Render(ctx)
	if err != nil {
		return wrap(CodeInterrupted, M4, err, "cannot generate models.ini and the unit")
	}
	ini := orStr(out.INIPath, e.paths.INI)
	data, err := e.fs.ReadFile(ini)
	if err != nil {
		return wrap(CodeInterrupted, M4, err, "cannot read %s", ini)
	}
	sec := iniSection(string(data), p.ModelID)
	if sec == nil {
		return fail(CodeStopped, M4, "%s has no section for %s: the model is not usable on this node (see the doctor: LAI-LLM-008 or LAI-CAT-005)", ini, p.ModelID)
	}
	if sec["load-on-startup"] != "true" {
		return fail(CodeStopped, M4, "the section of %s in %s lacks load-on-startup = true", p.ModelID, ini)
	}
	for _, a := range p.Aliases {
		if !containsCSV(sec["alias"], a) {
			return fail(CodeStopped, M4, "the section of %s in %s lacks the alias %s", p.ModelID, ini, a)
		}
	}
	if !exists(e.fs, orStr(out.UnitPath, e.paths.NewUnitFile())) {
		return fail(CodeInterrupted, M4, "%s was not written", e.paths.NewUnitFile())
	}
	return nil
}

// iniSection returns the key = value pairs of [id] in a models.ini text, nil when there is no section.
func iniSection(text, id string) map[string]string {
	var cur map[string]string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, ";") || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			if line[1:len(line)-1] == id {
				cur = map[string]string{}
			} else if cur != nil {
				return cur
			}
			continue
		}
		if cur == nil {
			continue
		}
		if k, v, ok := strings.Cut(line, "="); ok {
			cur[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	return cur
}

func containsCSV(list, want string) bool {
	for _, s := range strings.Split(list, ",") {
		if strings.TrimSpace(s) == want {
			return true
		}
	}
	return false
}

// ---- M5 pre-switch checks --------------------------------------------------------------------------

// m5Checks proves the new service can start before the old one is stopped: the localai user reads every
// model file and runs the new llama-server, and the old service owns the port.
func (e *Engine) m5Checks(ctx context.Context, p *Plan) error {
	var bad []string
	for _, f := range p.Files {
		res, err := e.runNamed(ctx, "runuser", "-u", ServiceUser, "--", "test", "-r", f.New)
		if err != nil || res.ExitCode != 0 {
			bad = append(bad, fmt.Sprintf("%s cannot read %s", ServiceUser, f.New))
		}
	}
	bin := path.Join(e.paths.Current(), "bin", "llama-server")
	if res, err := e.runNamed(ctx, "runuser", "-u", ServiceUser, "--", bin, "--version"); err != nil || res.ExitCode != 0 {
		bad = append(bad, fmt.Sprintf("%s cannot run %s", ServiceUser, bin))
	}
	if msg := e.checkPortOwner(ctx); msg != "" {
		bad = append(bad, msg)
	}
	if len(bad) > 0 {
		return &Error{Code: CodeStopped, Step: M5, Message: "the new service would not start: " + strings.Join(bad, "; "),
			Details: map[string]any{"checks": bad}}
	}
	return nil
}

var pidRe = regexp.MustCompile(`pid=(\d+)`)

// portPIDs returns the processes listening on the port.
func (e *Engine) portPIDs(ctx context.Context) ([]int, error) {
	res, err := e.runNamed(ctx, "ss", "-ltnpH", "sport = :"+strconv.Itoa(LegacyPort))
	if err != nil {
		return nil, err
	}
	if res.ExitCode != 0 {
		return nil, fmt.Errorf("ss exited %d", res.ExitCode)
	}
	var pids []int
	for _, m := range pidRe.FindAllStringSubmatch(string(res.Stdout), -1) {
		n, _ := strconv.Atoi(m[1])
		pids = append(pids, n)
	}
	return pids, nil
}

// checkPortOwner returns "" when the port is held by the old service's main process (or by nobody while
// the old service is stopped), else the reason it is not.
func (e *Engine) checkPortOwner(ctx context.Context) string {
	pids, err := e.portPIDs(ctx)
	if err != nil {
		return fmt.Sprintf("cannot see who listens on port %d (%v)", LegacyPort, err)
	}
	st, err := e.sd.State(ctx, LegacyUnitName)
	if err != nil {
		return fmt.Sprintf("cannot read the state of %s", LegacyUnitName)
	}
	active := st.ActiveState == "active" || st.ActiveState == "activating"
	switch {
	case active && (len(pids) != 1 || st.MainPID == 0 || pids[0] != st.MainPID):
		return fmt.Sprintf("port %d is not held by the main process of %s (pid %d); something else listens there", LegacyPort, LegacyUnitName, st.MainPID)
	case !active && len(pids) > 0:
		return fmt.Sprintf("%s is not running but another program (pid %d) holds port %d", LegacyUnitName, pids[0], LegacyPort)
	}
	return ""
}

// ---- M6 stop old ----------------------------------------------------------------------------------

// m6StopOld stops the old service. The time is recorded first, so a killed run still knows when the
// downtime began.
func (e *Engine) m6StopOld(ctx context.Context, p *Plan) error {
	if e.prog.T0 == nil {
		pr := e.prog
		t := e.clock.Now().UTC()
		pr.T0 = &t
		if err := e.saveProgress(pr); err != nil {
			return err
		}
	}
	if err := e.sd.Stop(ctx, LegacyUnitName); err != nil {
		return wrap(CodeInterrupted, M6, err, "cannot stop %s", LegacyUnitName)
	}
	timedOut, err := e.until(ctx, e.timing.StopWait, func(c context.Context) (bool, error) {
		st, err := e.sd.State(c, LegacyUnitName)
		if err != nil {
			return false, err
		}
		return st.ActiveState == "inactive" || st.ActiveState == "failed", nil
	})
	if err != nil && !timedOut {
		return wrap(CodeInterrupted, M6, err, "waiting for %s to stop was interrupted", LegacyUnitName)
	}
	if timedOut {
		return fail(CodeInterrupted, M6, "%s did not stop within %s", LegacyUnitName, e.timing.StopWait)
	}
	pids, err := e.portPIDs(ctx)
	if err != nil {
		return wrap(CodeInterrupted, M6, err, "cannot see who listens on port %d", LegacyPort)
	}
	if len(pids) > 0 {
		return fail(CodeInterrupted, M6, "port %d is still held by pid %d after %s stopped", LegacyPort, pids[0], LegacyUnitName)
	}
	return nil
}

// ---- M7 start new ---------------------------------------------------------------------------------

// m7StartNew starts the new unit and waits for /health and for the model to be loaded.
func (e *Engine) m7StartNew(ctx context.Context, p *Plan) error {
	if err := e.sd.Start(ctx, NewUnitName); err != nil {
		return wrap(CodeInterrupted, M7, err, "cannot start %s", NewUnitName)
	}
	timedOut, err := e.until(ctx, e.timing.HealthWait, func(c context.Context) (bool, error) {
		return e.probe.Health(c) == nil, nil
	})
	if err != nil && !timedOut {
		return wrap(CodeInterrupted, M7, err, "waiting for /health was interrupted")
	}
	if timedOut {
		return fail(CodeInterrupted, M7, "%s did not answer /health within %s", NewUnitName, e.timing.HealthWait)
	}
	loadWait := e.timing.LoadWait
	if loadWait <= 0 {
		loadWait = time.Duration(e.config.Get().Llama.LoadTimeoutSeconds) * time.Second
	}
	if loadWait <= 0 {
		loadWait = 300 * time.Second
	}
	var failedLoad bool
	timedOut, err = e.until(ctx, loadWait, func(c context.Context) (bool, error) {
		ms, err := e.probe.Models(c)
		if err != nil {
			return false, err
		}
		for _, m := range ms {
			if m.ID != p.ModelID {
				continue
			}
			if m.Failed {
				failedLoad = true
				return true, nil
			}
			return m.Status == "loaded", nil
		}
		return false, fmt.Errorf("the router does not list %s", p.ModelID)
	})
	switch {
	case err != nil && !timedOut:
		return wrap(CodeInterrupted, M7, err, "waiting for the model was interrupted")
	case failedLoad:
		return fail(CodeInterrupted, M7, "the model %s failed to load", p.ModelID)
	case timedOut:
		return wrap(CodeInterrupted, M7, err, "%s was not loaded within %s", p.ModelID, loadWait)
	}
	pr := e.prog
	t := e.clock.Now().UTC()
	pr.TLoaded = &t
	return e.saveProgress(pr)
}

// ---- M8 verify the contract ---------------------------------------------------------------------

// m8Verify checks what clients rely on; any failure rolls the migration back.
func (e *Engine) m8Verify(ctx context.Context, p *Plan) error {
	var bad []string
	chk := func(ok bool, format string, a ...any) {
		if !ok {
			bad = append(bad, fmt.Sprintf(format, a...))
		}
	}
	// a) without the key: 401.
	st, _, err := e.probe.V1Models(ctx, false)
	chk(err == nil && st == 401, "a) GET /v1/models without a key answered %d, not 401 (%v)", st, err)
	// b) with the key: 200, the model, and its legacy aliases.
	st, models, err := e.probe.V1Models(ctx, true)
	chk(err == nil && st == 200, "b) GET /v1/models with the key answered %d, not 200 (%v)", st, err)
	found := false
	for _, m := range models {
		if m.ID != p.ModelID {
			continue
		}
		found = true
		for _, a := range p.Aliases {
			chk(containsStr(m.Aliases, a), "b) the model %s does not list the alias %s", p.ModelID, a)
		}
	}
	chk(found || st != 200, "b) /v1/models does not list %s", p.ModelID)
	// c) a completion under the legacy name.
	st, err = e.probe.Chat(ctx, p.legacyAlias(), 8)
	chk(err == nil && st == 200, "c) a chat completion for %q answered %d, not 200 (%v)", p.legacyAlias(), st, err)
	// d) Tailscale Serve is exactly as before.
	serveUnchanged := bytes.Equal(e.captureServe(ctx), bytes.TrimSpace(p.ServeBefore))
	chk(serveUnchanged, "d) the Tailscale Serve configuration is not what it was before")
	// e) the public URL, when the internet may be asked.
	if p.PublicHost != "" && e.online() {
		pst, perr := e.probe.Public(ctx, p.PublicHost)
		switch {
		case perr == nil && pst == 200:
		case p.PublicBefore == 200:
			chk(false, "e) https://%s/v1/models answered %d (%v); it answered 200 before the migration", p.PublicHost, pst, perr)
		default:
			e.warnf("https://%s/v1/models answered %d; it did not answer 200 before the migration either", p.PublicHost, pst)
		}
	} else if p.PublicHost != "" {
		e.warnf("the node is offline: the public URL was not checked")
	}
	// f) the context size.
	if p.ExpectedCtx > 0 {
		n, err := e.probe.NCtx(ctx, p.ModelID)
		chk(err == nil && n == p.ExpectedCtx, "f) n_ctx is %d, the old unit had -c %d (%v)", n, p.ExpectedCtx, err)
	}
	if len(bad) > 0 {
		return &Error{Code: CodeInterrupted, Step: M8, Message: "the new service does not keep the contract: " + strings.Join(bad, "; "),
			Details: map[string]any{"failed": bad}}
	}
	return nil
}

func containsStr(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// ---- M9 commit ------------------------------------------------------------------------------------

// m9Commit makes the migration permanent: the old unit is saved, removed and masked, the new one is
// enabled, the old key path becomes a link, the files copied across filesystems are replaced by links.
func (e *Engine) m9Commit(ctx context.Context, p *Plan) error {
	if err := e.retireOldUnit(ctx); err != nil {
		return err
	}
	if err := e.sd.Enable(ctx, NewUnitName); err != nil {
		return wrap(CodeInterrupted, M9, err, "cannot enable %s", NewUnitName)
	}
	if _, err := e.ensureKeyLink(); err != nil {
		return err
	}
	for _, f := range p.Files {
		if fi, err := e.fs.Lstat(f.Old); err == nil && !isLink(fi) {
			// The copy made at M3 is verified and in place: the original goes, atomically.
			if err := e.replaceWithLink(f.Old, f.New); err != nil {
				return err
			}
		}
	}
	// Verify what the spec says to.
	st, err := e.sd.State(ctx, LegacyUnitName)
	if err != nil || !strings.HasPrefix(st.UnitFileState, "masked") {
		return fail(CodeInterrupted, M9, "%s is not masked", LegacyUnitName)
	}
	if ns, err := e.sd.State(ctx, NewUnitName); err != nil || !ns.Enabled() {
		return fail(CodeInterrupted, M9, "%s is not enabled", NewUnitName)
	}
	if t, err := e.fs.Readlink(e.paths.LegacyKey); err != nil || t != e.paths.NewKey {
		return fail(CodeInterrupted, M9, "%s is not a link to %s", e.paths.LegacyKey, e.paths.NewKey)
	}
	e.auditLine("migration.llama_qwen.done", audit.ResultOK, "", map[string]string{"model": p.ModelID, "unit": NewUnitName})
	return nil
}

// retireOldUnit copies the old unit and its drop-ins to the backup, removes them from
// /etc/systemd/system (a unit file there makes mask fail with "File exists"), disables, masks and
// reloads. Every part is idempotent: the backup is only refreshed while the unit is still there.
func (e *Engine) retireOldUnit(ctx context.Context) error {
	unit := e.paths.LegacyUnitFile()
	if fi, err := e.fs.Lstat(unit); err == nil && fi.Mode().IsRegular() {
		data, err := e.fs.ReadFile(unit)
		if err != nil {
			return wrap(CodeInterrupted, M9, err, "cannot read %s", unit)
		}
		if cur, err := e.fs.ReadFile(e.paths.UnitBackup()); err != nil || !bytes.Equal(cur, data) {
			if err := e.writeJournal(e.paths.UnitBackup(), data); err != nil {
				return wrap(CodeInterrupted, M9, err, "cannot save %s", unit)
			}
		}
		if err := e.saveDropIns(); err != nil {
			return err
		}
		if err := e.fs.Remove(unit); err != nil {
			return wrap(CodeInterrupted, M9, err, "cannot remove %s", unit)
		}
	} else if err == nil && !isLink(fi) {
		return fail(CodeInterrupted, M9, "%s is not a regular file", unit)
	}
	if err := e.removeDropIns(); err != nil {
		return err
	}
	if err := e.sd.Disable(ctx, LegacyUnitName); err != nil && !ignorable(err) {
		return wrap(CodeInterrupted, M9, err, "cannot disable %s", LegacyUnitName)
	}
	if st, err := e.sd.State(ctx, LegacyUnitName); err != nil || !strings.HasPrefix(st.UnitFileState, "masked") {
		if err := e.sd.Mask(ctx, LegacyUnitName); err != nil {
			return wrap(CodeInterrupted, M9, err, "cannot mask %s", LegacyUnitName)
		}
	}
	if err := e.sd.DaemonReload(ctx); err != nil {
		return wrap(CodeInterrupted, M9, err, "daemon-reload failed")
	}
	return nil
}

// saveDropIns copies the files of llama-qwen.service.d/ into the backup directory.
func (e *Engine) saveDropIns() error {
	entries, err := e.fs.ReadDir(e.paths.LegacyDropInDir())
	if err != nil {
		if isNotExist(err) {
			return nil
		}
		return wrap(CodeInterrupted, M9, err, "cannot read %s", e.paths.LegacyDropInDir())
	}
	for _, ent := range entries {
		if ent.IsDir() {
			continue
		}
		data, err := e.fs.ReadFile(filepath.Join(e.paths.LegacyDropInDir(), ent.Name()))
		if err != nil {
			return wrap(CodeInterrupted, M9, err, "cannot read the drop-in %s", ent.Name())
		}
		if err := e.writeJournal(filepath.Join(e.paths.DropInBackup(), ent.Name()), data); err != nil {
			return wrap(CodeInterrupted, M9, err, "cannot save the drop-in %s", ent.Name())
		}
	}
	return nil
}

// removeDropIns removes the drop-in directory once its files are saved.
func (e *Engine) removeDropIns() error {
	entries, err := e.fs.ReadDir(e.paths.LegacyDropInDir())
	if err != nil {
		if isNotExist(err) {
			return nil
		}
		return wrap(CodeInterrupted, M9, err, "cannot read %s", e.paths.LegacyDropInDir())
	}
	for _, ent := range entries {
		src := filepath.Join(e.paths.LegacyDropInDir(), ent.Name())
		if !ent.IsDir() {
			if _, err := e.fs.ReadFile(filepath.Join(e.paths.DropInBackup(), ent.Name())); err != nil {
				return fail(CodeInterrupted, M9, "the drop-in %s is not in the backup yet: it is not removed", ent.Name())
			}
		}
		if err := e.fs.Remove(src); err != nil {
			return wrap(CodeInterrupted, M9, err, "cannot remove %s", src)
		}
	}
	if err := e.fs.Remove(e.paths.LegacyDropInDir()); err != nil && !isNotExist(err) {
		return wrap(CodeInterrupted, M9, err, "cannot remove %s", e.paths.LegacyDropInDir())
	}
	return nil
}

// ---- M10 report -----------------------------------------------------------------------------------

// m10Report prints what happened and writes result.json.
func (e *Engine) m10Report(_ context.Context, p *Plan) error {
	r := e.result(p)
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	if err := e.writeJournal(e.paths.Result(), append(data, '\n')); err != nil {
		return wrap(CodeInterrupted, M10, err, "cannot write result.json")
	}
	e.say("")
	e.say("Migration done.")
	e.say("  downtime:        %.0f s", r.DowntimeSeconds)
	e.say("  model:           %s (still answers to %s)", p.ModelID, p.legacyAlias())
	e.say("  key fingerprint: %s", p.KeyFingerprint)
	e.say("  tailscale serve: unchanged")
	e.say("  clients to update: none")
	return nil
}

func (e *Engine) readLinkOr(p string) string {
	if t, err := e.fs.Readlink(p); err == nil {
		return t
	}
	return ""
}
