package migrate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/audit"
	"github.com/marinoscar/local-ai/agent/internal/catalog"
	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/execx"
	"github.com/marinoscar/local-ai/agent/internal/services"
	"github.com/marinoscar/local-ai/agent/internal/state"
)

// ---- clock ------------------------------------------------------------------------------------------

// fakeClock is a manual clock: Sleep advances it and never waits.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
	// slept counts the sleeps, for tests that need to know nothing really waited.
	slept int
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.slept++
	c.mu.Unlock()
	return nil
}

// ---- systemd ----------------------------------------------------------------------------------------

type unitRec struct {
	active  string
	mainPID int
	// enabled is the enable flag; masked is derived from the file in /etc/systemd/system.
	enabled bool
}

// fakeSD is systemd over the memory filesystem: a unit file in /etc/systemd/system makes the unit
// known, a link to /dev/null masks it, and mask fails with "File exists" when a unit file is there.
type fakeSD struct {
	w     *world
	mu    sync.Mutex
	units map[string]*unitRec
	pid   int
	calls []string
	// fail makes "verb unit" fail with the error once (then it is removed).
	fail map[string]error
	// failAlways makes "verb unit" fail every time.
	failAlways map[string]error
}

func newFakeSD(w *world) *fakeSD {
	return &fakeSD{w: w, units: map[string]*unitRec{}, pid: 1000, fail: map[string]error{}, failAlways: map[string]error{}}
}

func (s *fakeSD) unit(name string) *unitRec {
	u := s.units[name]
	if u == nil {
		u = &unitRec{active: "inactive"}
		s.units[name] = u
	}
	return u
}

func (s *fakeSD) unitFile(name string) string { return path.Join("/etc/systemd/system", name) }

func (s *fakeSD) fileState(name string) string {
	fi, err := s.w.fs.Lstat(s.unitFile(name))
	if err != nil {
		return ""
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return "masked"
	}
	if s.unit(name).enabled {
		return "enabled"
	}
	return "disabled"
}

func (s *fakeSD) State(_ context.Context, unit string) (services.UnitState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u := s.unit(unit)
	return services.UnitState{Unit: unit, ActiveState: u.active, MainPID: u.mainPID, UnitFileState: s.fileState(unit)}, nil
}

// do runs the common part of a verb: record, fail on request, tick the killer.
func (s *fakeSD) do(verb, unit string) error {
	key := verb + " " + unit
	s.mu.Lock()
	s.calls = append(s.calls, key)
	err := s.fail[key]
	delete(s.fail, key)
	if err == nil {
		err = s.failAlways[key]
	}
	s.mu.Unlock()
	if err != nil {
		return err
	}
	return s.w.k.tick()
}

func (s *fakeSD) Start(_ context.Context, unit string) error {
	if err := s.do("start", unit); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.fileState(unit)
	if st == "" {
		return fmt.Errorf("Unit %s not found.", unit)
	}
	if st == "masked" {
		return fmt.Errorf("Unit %s is masked.", unit)
	}
	u := s.unit(unit)
	if u.active != "active" {
		s.pid++
		u.mainPID = s.pid
	}
	u.active = "active"
	return s.w.k.done()
}

func (s *fakeSD) Stop(_ context.Context, unit string) error {
	if err := s.do("stop", unit); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	u := s.unit(unit)
	u.active, u.mainPID = "inactive", 0
	return s.w.k.done()
}

func (s *fakeSD) Enable(_ context.Context, unit string) error {
	if err := s.do("enable", unit); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	switch s.fileState(unit) {
	case "":
		return fmt.Errorf("Failed to enable unit: Unit file %s does not exist.", unit)
	case "masked":
		return fmt.Errorf("Failed to enable unit: Unit file %s is masked.", unit)
	}
	s.unit(unit).enabled = true
	return s.w.k.done()
}

func (s *fakeSD) Disable(_ context.Context, unit string) error {
	if err := s.do("disable", unit); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fileState(unit) == "" {
		return fmt.Errorf("Failed to disable unit: Unit file %s does not exist.", unit)
	}
	s.unit(unit).enabled = false
	return s.w.k.done()
}

func (s *fakeSD) Mask(_ context.Context, unit string) error {
	if err := s.do("mask", unit); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	fi, err := s.w.fs.Lstat(s.unitFile(unit))
	if err == nil && fi.Mode()&os.ModeSymlink == 0 {
		return fmt.Errorf("Failed to mask unit: File %s already exists.", s.unitFile(unit))
	}
	if err != nil {
		s.w.fs.mu.Lock()
		s.w.fs.nodes[s.unitFile(unit)] = &mnode{kind: kLink, target: "/dev/null", dev: 1}
		s.w.fs.mu.Unlock()
	}
	s.unit(unit).enabled = false
	return s.w.k.done()
}

func (s *fakeSD) Unmask(_ context.Context, unit string) error {
	if err := s.do("unmask", unit); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fileState(unit) == "masked" {
		s.w.fs.mu.Lock()
		delete(s.w.fs.nodes, s.unitFile(unit))
		s.w.fs.mu.Unlock()
	}
	return s.w.k.done()
}

func (s *fakeSD) DaemonReload(context.Context) error {
	if err := s.do("daemon-reload", ""); err != nil {
		return err
	}
	return s.w.k.done()
}

// called returns how many times a call "verb unit" happened.
func (s *fakeSD) called(verb, unit string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, c := range s.calls {
		if c == verb+" "+unit {
			n++
		}
	}
	return n
}

// ---- the router and the network ----------------------------------------------------------------------

// portOwner is who holds port 8080 now: the old service while it is active, else the new one when it is
// active and healthy.
func (w *world) portOwner() string {
	w.sd.mu.Lock()
	defer w.sd.mu.Unlock()
	if w.sd.unit(w.legacy()).active == "active" {
		return "legacy"
	}
	if w.sd.unit(NewUnitName).active == "active" && !w.newBroken {
		return "new"
	}
	return ""
}

type fakeProbe struct {
	w     *world
	polls int
}

func (p *fakeProbe) Health(context.Context) error {
	if p.w.portOwner() == "" {
		return errors.New("connection refused")
	}
	return nil
}

func (p *fakeProbe) Models(context.Context) ([]ModelState, error) {
	switch p.w.portOwner() {
	case "new":
		p.polls++
		st := "loading"
		if p.polls > p.w.loadPolls {
			st = "loaded"
		}
		if p.w.loadFails {
			return []ModelState{{ID: p.w.modelID, Status: "unloaded", Failed: true}}, nil
		}
		return []ModelState{{ID: p.w.modelID, Status: st}}, nil
	}
	return nil, errors.New("connection refused")
}

func (p *fakeProbe) V1Models(_ context.Context, withKey bool) (int, []V1Model, error) {
	owner := p.w.portOwner()
	if owner == "" {
		return 0, nil, errors.New("connection refused")
	}
	if !withKey {
		if (p.w.noAuth && owner == "new") || (p.w.legacyNoAuth && owner == "legacy") {
			return 200, nil, nil
		}
		return 401, nil, nil
	}
	if owner == "legacy" {
		return 200, []V1Model{{ID: p.w.alias}}, nil
	}
	if p.w.v1Status != 0 {
		return p.w.v1Status, nil, nil
	}
	aliases := []string{p.w.alias}
	if p.w.missingAlias {
		aliases = nil
	}
	return 200, []V1Model{{ID: p.w.modelID, Aliases: aliases}}, nil
}

func (p *fakeProbe) Chat(_ context.Context, model string, _ int) (int, error) {
	owner := p.w.portOwner()
	switch {
	case owner == "":
		return 0, errors.New("connection refused")
	case p.w.chatStatus != 0 && owner == "new":
		return p.w.chatStatus, nil
	case model == p.w.alias || model == p.w.modelID:
		return 200, nil
	}
	return 404, nil
}

func (p *fakeProbe) NCtx(context.Context, string) (int, error) {
	if p.w.portOwner() != "new" {
		return 0, errors.New("connection refused")
	}
	return p.w.nctx, nil
}

func (p *fakeProbe) Public(context.Context, string) (int, error) {
	if p.w.publicErr {
		return 0, errors.New("no route")
	}
	if p.w.portOwner() == "new" && p.w.publicAfter != 0 {
		return p.w.publicAfter, nil
	}
	return p.w.publicBefore, nil
}

// ---- programs ---------------------------------------------------------------------------------------

type fakeExec struct{ w *world }

func (x *fakeExec) Run(_ context.Context, c execx.Cmd) (execx.Result, error) {
	w := x.w
	base := path.Base(c.Path)
	args := strings.Join(c.Args, " ")
	ok := func(out string) (execx.Result, error) { return execx.Result{Stdout: []byte(out)}, nil }
	exit := func(code int) (execx.Result, error) { return execx.Result{ExitCode: code}, nil }
	w.execCalls = append(w.execCalls, base+" "+args)
	switch {
	case base == "systemctl" && strings.HasPrefix(args, "--no-pager cat "):
		if w.catErr != "" {
			return execx.Result{ExitCode: 1, Stderr: []byte(w.catErr)}, nil
		}
		return ok(w.legacyText)
	case base == "tailscale":
		if w.noTailscale {
			return exit(1)
		}
		if w.serveAfter != "" && w.portOwner() == "new" {
			return ok(w.serveAfter)
		}
		return ok(w.serveJSON)
	case base == "ss":
		switch w.portOwner() {
		case "legacy":
			pid := w.sd.unit(w.legacy()).mainPID
			if w.ssPID != 0 {
				pid = w.ssPID
			}
			line := fmt.Sprintf("LISTEN 0 4096 127.0.0.1:8080 0.0.0.0:* users:((\"llama-server\",pid=%d,fd=3))\n", pid)
			if w.ssDual {
				// The same process on the IPv6 loopback too: one pid, two lines.
				line += fmt.Sprintf("LISTEN 0 4096 [::1]:8080 [::]:* users:((\"llama-server\",pid=%d,fd=4))\n", pid)
			}
			return ok(line)
		case "new":
			return ok(fmt.Sprintf("LISTEN 0 4096 127.0.0.1:8080 0.0.0.0:* users:((\"llama-server\",pid=%d,fd=3))\n", w.sd.unit(NewUnitName).mainPID))
		}
		if w.strangerOnPort {
			return ok("LISTEN 0 4096 127.0.0.1:8080 0.0.0.0:* users:((\"other\",pid=4242,fd=3))\n")
		}
		return ok("")
	case base == "runuser" && len(c.Args) >= 6 && c.Args[3] == "test":
		fi, err := w.fs.Stat(c.Args[len(c.Args)-1])
		if err != nil || w.unreadable[c.Args[len(c.Args)-1]] || fi.Mode().Perm()&0o004 == 0 {
			return exit(1)
		}
		return exit(0)
	case base == "runuser":
		if w.newBinaryBroken {
			return exit(126)
		}
		if strings.HasSuffix(args, "--help") {
			if w.oldRouterFlags {
				return ok("--models-preset PATH\n--models-max N\n")
			}
			return ok("--models-preset PATH\n--models-max N\n--models-memory-margin MiB\n")
		}
		return ok("version: 6000 (commit " + w.commit + ")\n")
	case base == "llama-server" && strings.HasPrefix(c.Path, "/home/"):
		if w.legacyVersionOut == "" {
			return ok("version: 6000 (commit " + w.commit + ")\n")
		}
		return ok(w.legacyVersionOut)
	case base == "llama-server" && strings.HasSuffix(args, "--version"):
		cur := w.currentCommitOnDisk()
		if cur == "" || w.currentBroken {
			return exit(1)
		}
		return ok("version: 6000 (commit " + cur + ")\n")
	case base == "llama-server" && strings.HasSuffix(args, "--help"):
		if w.oldRouterFlags {
			return ok("--models-preset PATH\n--models-max N\n")
		}
		return ok("--models-preset PATH\n--models-max N\n--models-memory-margin MiB\n")
	case base == "llama-server" && strings.HasSuffix(args, "--list-devices"):
		if w.noGPU {
			return ok("Available devices:\n  Vulkan0: llvmpipe (LLVM 15.0.7, 256 bits) (64000 MiB)\n")
		}
		return ok("Available devices:\n  CUDA0: Test GPU (8192 MiB, 8000 MiB free)\n")
	}
	return execx.Result{}, fmt.Errorf("fakeExec: unexpected command %s %s", c.Path, args)
}

// ---- builder ----------------------------------------------------------------------------------------

type fakeBuilder struct {
	w        *world
	canErr   error
	buildErr error
	built    []BuildRequest
}

func (b *fakeBuilder) CanBuild(context.Context, string) error { return b.canErr }

func (b *fakeBuilder) Build(_ context.Context, req BuildRequest) (BuildResult, error) {
	b.built = append(b.built, req)
	if b.buildErr != nil {
		return BuildResult{}, b.buildErr
	}
	id := req.Commit[:9] + "-" + req.Backend
	dir := path.Join("/opt/localai/llama.cpp/builds", id)
	if err := b.w.fs.MkdirAll(dir, 0o755); err != nil {
		return BuildResult{}, err
	}
	meta, _ := json.Marshal(map[string]string{"build_id": id, "commit": req.Commit, "backend": req.Backend})
	if err := config.WriteFileAtomicFS(b.w.fs, path.Join(dir, "BUILD.json"), meta, 0o644, -1, -1); err != nil {
		return BuildResult{}, err
	}
	return BuildResult{BuildID: id}, nil
}

// ---- node.yaml, state, renderer, agent, users -----------------------------------------------------

type fakeConfig struct {
	w       *world
	cfg     config.Config
	changes []ConfigChange
	err     error
}

func (c *fakeConfig) Get() config.Config { return c.cfg.Clone() }

func (c *fakeConfig) Record(_ context.Context, ch ConfigChange) error {
	if c.err != nil {
		return c.err
	}
	if err := c.w.k.tick(); err != nil {
		return err
	}
	c.changes = append(c.changes, ch)
	if c.cfg.Llama.LegacyAliases == nil {
		c.cfg.Llama.LegacyAliases = map[string][]string{}
	}
	for id, a := range ch.LegacyAliases {
		c.cfg.Llama.LegacyAliases[id] = a
	}
	if c.cfg.Llama.ExtraEnv == nil {
		c.cfg.Llama.ExtraEnv = map[string]string{}
	}
	for k, v := range ch.ExtraEnv {
		if _, set := c.cfg.Llama.ExtraEnv[k]; !set {
			c.cfg.Llama.ExtraEnv[k] = v
		}
	}
	if ch.Commit != "" {
		c.cfg.Llama.Commit = ch.Commit
	}
	return c.w.k.done()
}

type fakeState struct {
	w       *world
	entry   state.MigrationEntry
	has     bool
	history []state.MigrationEntry
	def     string
	build   string
	conn    string
	fail    error
}

func (s *fakeState) Migration() (state.MigrationEntry, bool) { return s.entry, s.has }

func (s *fakeState) SetMigration(e state.MigrationEntry) error {
	if s.fail != nil {
		return s.fail
	}
	if err := s.w.k.tick(); err != nil {
		return err
	}
	s.entry, s.has = e, true
	s.history = append(s.history, e)
	return s.w.k.done()
}

func (s *fakeState) SetDefaultModel(id string) error {
	if err := s.w.k.tick(); err != nil {
		return err
	}
	s.def = id
	return s.w.k.done()
}

func (s *fakeState) RecordBuild(id string) error {
	if err := s.w.k.tick(); err != nil {
		return err
	}
	s.build = id
	return s.w.k.done()
}

func (s *fakeState) Connectivity() string { return s.conn }

type fakeRenderer struct {
	w          *world
	omitModel  bool
	noLoadFlag bool
	err        error
	renders    int
}

func (r *fakeRenderer) Render(context.Context) (RenderResult, error) {
	if r.err != nil {
		return RenderResult{}, r.err
	}
	r.renders++
	w := r.w
	var b strings.Builder
	b.WriteString("; GENERATED by aiagent test\nversion = 1\n\n[*]\nstop-timeout = 10\nload-on-startup = false\n")
	if !r.omitModel {
		fmt.Fprintf(&b, "\n[%s]\nmodel = %s\nc = %d\n", w.modelID, path.Join("/srv/localai/models", w.modelID, w.fileName), w.ctx)
		if a := w.cfgNode.cfg.Llama.LegacyAliases[w.modelID]; len(a) > 0 {
			fmt.Fprintf(&b, "alias = %s\n", strings.Join(a, ","))
		}
		if !r.noLoadFlag && w.st.def == w.modelID {
			b.WriteString("load-on-startup = true\n")
		}
	}
	if err := config.WriteFileAtomicFS(w.fs, "/etc/localai/models.ini", []byte(b.String()), 0o640, -1, -1); err != nil {
		return RenderResult{}, err
	}
	if err := config.WriteFileAtomicFS(w.fs, "/etc/systemd/system/localai-llama.service", []byte("[Service]\nExecStart=/opt/localai/llama.cpp/current/bin/llama-server --models-preset /etc/localai/models.ini --models-max 1 --models-memory-margin 768\n"), 0o644, -1, -1); err != nil {
		return RenderResult{}, err
	}
	return RenderResult{INIPath: "/etc/localai/models.ini", UnitPath: "/etc/systemd/system/localai-llama.service"}, nil
}

type fakeAgent struct {
	w      *world
	active bool
	stops  int
	starts int
	err    error
}

func (a *fakeAgent) Active(context.Context) (bool, error) { return a.active, nil }

func (a *fakeAgent) Stop(context.Context) error {
	if err := a.w.k.tick(); err != nil {
		return err
	}
	a.active = false
	a.stops++
	return a.w.k.done()
}

func (a *fakeAgent) Start(context.Context) error {
	if a.err != nil {
		return a.err
	}
	if err := a.w.k.tick(); err != nil {
		return err
	}
	a.active = true
	a.starts++
	return a.w.k.done()
}

type fakeUsers struct{}

func (fakeUsers) LookupUser(name string) (int, int, error) {
	switch name {
	case "localai":
		return 990, 990, nil
	case "labuser":
		return 1000, 1000, nil
	}
	return 0, 0, errors.New("unknown user " + name)
}

func (fakeUsers) LookupGroup(name string) (int, error) {
	switch name {
	case "localai":
		return 990, nil
	case "labuser":
		return 1000, nil
	}
	return 0, errors.New("unknown group " + name)
}

// ---- the world --------------------------------------------------------------------------------------

// world is a node with everything the migration touches, in memory.
type world struct {
	t   *testing.T
	k   *killer
	fs  *memFS
	sd  *fakeSD
	clk *fakeClock

	probe   *fakeProbe
	builder *fakeBuilder
	cfgNode *fakeConfig
	st      *fakeState
	rend    *fakeRenderer
	agent   *fakeAgent
	cat     *catalog.Catalog
	audits  []string
	out     *lockedBuf

	// What the node is. legacyUnit is the old unit that serves ("" is LegacyUnitName).
	legacyUnit       string
	modelID          string
	alias            string
	fileName         string
	commit           string
	ctx              int
	key              string
	legacyText       string
	legacyVersionOut string
	serveJSON        string
	serveAfter       string
	model            []byte
	draft            []byte

	// Behaviour switches of the tests.
	newBroken       bool
	loadPolls       int
	loadFails       bool
	noAuth          bool
	missingAlias    bool
	chatStatus      int
	v1Status        int
	nctx            int
	publicBefore    int
	publicAfter     int
	publicErr       bool
	noTailscale     bool
	strangerOnPort  bool
	unreadable      map[string]bool
	newBinaryBroken bool
	// oldRouterFlags makes the new llama-server lack --models-memory-margin (llama.cpp before #21231).
	oldRouterFlags bool
	currentBroken  bool
	noGPU          bool
	catErr         string
	execCalls      []string
	ssPID          int
	legacyNoAuth   bool
	ssDual         bool
}

type lockedBuf struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// sha256Hex hashes at run time: no digest-shaped literal ever sits in the source.
func sha256Hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// worldOpts shapes a node.
type worldOpts struct {
	// crossFS puts /home on its own device, so the models must be copied.
	crossFS bool
	// withDraft adds a draft file (-md) to the model.
	withDraft bool
	// dropIn adds a drop-in with a GGML variable.
	dropIn bool
	// ctxMismatch makes the unit's -c differ from the catalog.
	ctxMismatch bool
	// hasCurrent installs a current build at the legacy commit.
	hasCurrent bool
}

const legacyUnitTemplate = `# /etc/systemd/system/llama-qwen.service
[Unit]
Description=Test legacy server
After=network.target

[Service]
Type=simple
User=labuser
Group=labuser
WorkingDirectory=/home/labuser/llama.cpp
LoadCredential=llama-api-key:/etc/llama-qwen/api-key

ExecStart=/home/labuser/llama.cpp/build-cuda/bin/llama-server \
  -m /home/labuser/models/m1/%s \
%s  -ngl 99 \
  -c %d \
  --parallel 1 \
  --flash-attn on \
  --alias %s \
  --host 127.0.0.1 \
  --port 8080 \
  --api-key-file %%d/llama-api-key

Restart=on-failure

[Install]
WantedBy=multi-user.target
`

// newWorld builds a legacy node: the old unit running on port 8080 with its model under /home, a catalog
// that knows the model, an agent running, and no localai-llama yet.
func newWorld(t *testing.T, o worldOpts) *world {
	t.Helper()
	w := &world{t: t, k: &killer{}, fs: newMemFS(), clk: &fakeClock{now: time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)},
		modelID: "model-one", alias: "legacy-name", fileName: "Model-One-Q4.gguf", commit: "f3586e66b", ctx: 4096, nctx: 4096,
		out: &lockedBuf{}, unreadable: map[string]bool{}, publicBefore: 200, loadPolls: 2}
	w.fs.k = w.k
	w.model = []byte("model one weights, pretend this is 5 GB")
	w.draft = []byte("draft head weights")
	sum := sha256.Sum256([]byte("test key material"))
	w.key = hex.EncodeToString(sum[:])
	if o.crossFS {
		w.fs.mount("/home", 2, 1<<40)
	}
	w.fs.free[1] = 1 << 40

	// The legacy node.
	w.fs.put("/home/labuser/models/m1/"+w.fileName, w.model, 0o644)
	extra := ""
	if o.withDraft {
		w.fs.put("/home/labuser/models/m1/draft.gguf", w.draft, 0o644)
		extra = "  -md /home/labuser/models/m1/draft.gguf \\\n"
	}
	ctxFlag := w.ctx
	if o.ctxMismatch {
		ctxFlag = 8192
	}
	w.legacyText = fmt.Sprintf(legacyUnitTemplate, w.fileName, extra, ctxFlag, w.alias)
	if o.dropIn {
		w.legacyText += "\n# /etc/systemd/system/llama-qwen.service.d/vk.conf\n[Service]\nEnvironment=GGML_VK_SUBALLOCATION_BLOCK_SIZE=4294967296\n"
		w.fs.put("/etc/systemd/system/llama-qwen.service.d/vk.conf", []byte("[Service]\nEnvironment=GGML_VK_SUBALLOCATION_BLOCK_SIZE=4294967296\n"), 0o644)
	}
	w.fs.put("/etc/systemd/system/llama-qwen.service", []byte(w.legacyText), 0o644)
	w.fs.put("/etc/llama-qwen/api-key", []byte(w.key), 0o600)
	w.fs.put("/etc/localai/node.yaml", []byte("node:\n  id: ai-test\n"), 0o640)
	w.fs.put("/etc/localai/.keep", nil, 0o640)
	w.fs.put("/srv/localai/models/.keep", nil, 0o644)
	w.fs.put("/opt/localai/llama.cpp/builds/.keep", nil, 0o644)
	w.fs.put("/home/labuser/llama.cpp/build-cuda/bin/llama-server", []byte("elf"), 0o755)
	w.serveJSON = `{"TCP":{"443":{"HTTPS":true}},"Web":{"node.example.test:443":{"Handlers":{"/":{"Proxy":"http://127.0.0.1:8080"}}}}}`
	if o.hasCurrent {
		w.fs.put("/opt/localai/llama.cpp/builds/"+w.commit+"-cuda/BUILD.json", []byte(`{"commit":"`+w.commit+`7777777777777777777777777777"}`), 0o644)
		w.fs.nodes["/opt/localai/llama.cpp/current"] = &mnode{kind: kLink, target: "builds/" + w.commit + "-cuda", dev: 1}
	}

	// The catalog.
	files := []catalog.ModelFile{{Kind: "weights", Name: w.fileName, SizeBytes: int64(len(w.model)), SHA256: sha256Hex(w.model)}}
	llama := &catalog.Llama{Ctx: 4096, NGPULayers: catalog.IntOrAll{Set: true, N: 99}, FlashAttn: "on", Parallel: 1}
	if o.withDraft {
		files = append(files, catalog.ModelFile{Kind: "draft", Name: "draft.gguf", SizeBytes: int64(len(w.draft)), SHA256: sha256Hex(w.draft)})
		llama.Speculative = &catalog.Speculative{DraftFileKind: "draft"}
	}
	w.cat = catalog.New("2026.11.02-1", []catalog.Model{{
		ID: w.modelID, Engine: "llama", Roles: []string{"chat"}, Files: files, Llama: llama,
		Source: catalog.ModelSource{Type: "huggingface", Repo: "org-alpha/model-one", Revision: "0123456789abcdef0123456789abcdef01234567"},
	}}, nil, nil)

	// The fakes.
	w.sd = newFakeSD(w)
	w.sd.units[LegacyUnitName] = &unitRec{active: "active", mainPID: 500, enabled: true}
	w.probe = &fakeProbe{w: w}
	w.builder = &fakeBuilder{w: w}
	cfg := config.Defaults()
	cfg.Node.ID = "ai-test"
	cfg.Llama.Backend = "cuda"
	w.cfgNode = &fakeConfig{w: w, cfg: cfg}
	w.st = &fakeState{w: w, conn: "none"}
	w.rend = &fakeRenderer{w: w}
	w.agent = &fakeAgent{w: w, active: true}
	return w
}

// legacy is the old unit that serves on this node.
func (w *world) legacy() string { return orStr(w.legacyUnit, LegacyUnitName) }

// engine builds an Engine over the world. The killer, if any, is already shared by every fake.
func (w *world) engine(mod ...func(*Options)) *Engine {
	o := Options{
		Paths: DefaultPaths(""), FS: w.fs, Exec: &fakeExec{w: w},
		Lookup:  func(name string) (string, error) { return "/usr/bin/" + name, nil },
		Systemd: w.sd, Agent: w.agent, Users: fakeUsers{}, Probe: w.probe, Builder: w.builder,
		Config: w.cfgNode, State: w.st, Renderer: w.rend, Catalog: w.cat, Clock: w.clk, Out: w.out,
		Audit: func(action string, res audit.Result, code string, target map[string]string) error {
			w.audits = append(w.audits, action+" "+string(res)+" "+code)
			return nil
		},
		Version: "test",
	}
	for _, f := range mod {
		f(&o)
	}
	return New(o)
}

// setUnit changes the text of the legacy unit (as systemctl cat shows it and as the file holds it).
func (w *world) setUnit(f func(string) string) {
	w.legacyText = f(w.legacyText)
	w.fs.put("/etc/systemd/system/llama-qwen.service", []byte(w.legacyText), 0o644)
}

// currentCommitOnDisk reads the commit of the build current points at, "" when there is none.
func (w *world) currentCommitOnDisk() string {
	data, err := w.fs.ReadFile("/opt/localai/llama.cpp/current/BUILD.json")
	if err != nil {
		return ""
	}
	var b struct{ Commit string }
	_ = json.Unmarshal(data, &b)
	return b.Commit
}

// run does a whole migration: Prepare then Run.
func (w *world) run(e *Engine) (*Result, error) {
	p, err := e.Prepare(context.Background())
	if err != nil {
		return nil, err
	}
	return e.Run(context.Background(), p)
}

func mustRead(t *testing.T, w *world, p string) string {
	t.Helper()
	b, ok := w.fs.content(p)
	if !ok {
		t.Fatalf("%s does not exist", p)
	}
	return string(b)
}

func newLogger(w io.Writer) *slog.Logger {
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: slog.LevelDebug}))
}
