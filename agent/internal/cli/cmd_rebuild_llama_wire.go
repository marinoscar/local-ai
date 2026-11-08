package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/catalog"
	"github.com/marinoscar/local-ai/agent/internal/cli/i18n"
	"github.com/marinoscar/local-ai/agent/internal/cli/local"
	"github.com/marinoscar/local-ai/agent/internal/cli/out"
	"github.com/marinoscar/local-ai/agent/internal/cli/progress"
	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/kit"
	"github.com/marinoscar/local-ai/agent/internal/router"
	"github.com/marinoscar/local-ai/agent/internal/router/build"
	"github.com/marinoscar/local-ai/agent/internal/state"
	"github.com/marinoscar/local-ai/agent/internal/sync/manifest"
	"gopkg.in/yaml.v3"
)

// The seams of `aictl rebuild-llama` to the machine (AICTL-09): the build manager's Options, filled in
// from node.yaml, the kit and a real agent client. Everything here is replaceable by a test through the
// package variables below; the engine itself is the fake of llamaNewEngine.

// llamaChatTimeout bounds the 16-token completion of the post-switch check; the engine's own deadline
// (180 s from the restart) is what really limits it, so a CPU-only laptop is not cut off earlier.
const llamaChatTimeout = 3 * time.Minute

// llamaHeartbeatEvery is how often a long build says that it is still working.
const llamaHeartbeatEvery = 30 * time.Second

var (
	// llamaInterrupts delivers one value per Ctrl-C (Interrupts); tests send their own.
	llamaInterrupts = Interrupts
	// llamaHeartbeat is the ticker of the "still working" line; llamaClock reads the time it counts.
	llamaHeartbeat = func() (<-chan time.Time, func()) {
		t := time.NewTicker(llamaHeartbeatEvery)
		return t.C, t.Stop
	}
	llamaClock = time.Now
	// llamaRouterFn makes the client of the model router (llama-server) on this computer.
	llamaRouterFn = func(env local.Env, port int) (llamaRouter, error) {
		return router.NewClient(router.Options{
			Port:        port,
			Keys:        router.NewFileKey(env.FS, filepath.Join(env.Paths.EtcDir, "api-key")),
			ChatTimeout: llamaChatTimeout,
		})
	}
)

// llamaRouter is the part of the router client the post-switch check uses (*router.Client).
type llamaRouter interface {
	Health(ctx context.Context) error
	Models(ctx context.Context) ([]router.ModelStatus, error)
	Chat(ctx context.Context, model string, maxTokens int) error
}

// ---- node.yaml and state.json ---------------------------------------------------------------------

// llamaSettings are the values of node.yaml the rebuild reads.
type llamaSettings struct {
	// Llama is the llama block as written (commit, backend, cuda_arch).
	Llama config.LlamaBlock
	// Port is ports.llama.
	Port int
}

// readLlamaSettings reads node.yaml. A missing or unreadable file is not an error: a node being rebuilt
// from the kit may have none, and --commit and --backend then have to say everything.
func readLlamaSettings(env local.Env) llamaSettings {
	s := llamaSettings{Port: config.DefaultLlamaPort}
	data, err := os.ReadFile(env.Paths.NodeYAML())
	if err != nil || len(data) > 1<<20 {
		return s
	}
	cfg, _ := config.Parse(data, nil)
	s.Llama = cfg.Llama
	if cfg.Ports.Llama > 0 {
		s.Port = cfg.Ports.Llama
	}
	return s
}

// llamaNodeRecorder is build.NodeRecorder over node.yaml: llama.commit after a switch to another commit
// (the other keys and the comments stay as they are).
type llamaNodeRecorder struct{ env local.Env }

// RecordLlamaCommit implements build.NodeRecorder.
func (r llamaNodeRecorder) RecordLlamaCommit(commit string) error {
	store := config.Open(config.Options{Path: r.env.Paths.NodeYAML()})
	return store.Update(func(root *yaml.Node) error {
		return config.SetKey(root, commit, "llama", "commit")
	})
}

// stateReader reads the part of state.json the CLI looks at, without opening the store (which would
// quarantine a damaged file, or write): mode, models.default and llama.previous_build.
type stateView struct {
	Mode   string `json:"mode"`
	Models struct {
		Default *string `json:"default"`
	} `json:"models"`
	Llama struct {
		PreviousBuild *string `json:"previous_build"`
	} `json:"llama"`
}

func readStateView(env local.Env) (stateView, bool) {
	var v stateView
	data, err := os.ReadFile(env.Paths.StateFile())
	if err != nil || len(data) > 4<<20 || json.Unmarshal(data, &v) != nil {
		return v, false
	}
	return v, true
}

// llamaStateRecorder is build.StateRecorder over state.json, used only after a restart that did not go
// through the agent (the agent is down, so nothing else writes the file). It never creates state.json,
// and it leaves a file it cannot read alone: the agent repairs that on its next start.
type llamaStateRecorder struct{ env local.Env }

type syncMutator struct{ s *state.Store }

func (m syncMutator) Mutate(fn func(*state.State) error) error { return m.s.MutateSync(fn) }

// RecordBuild implements build.StateRecorder.
func (r llamaStateRecorder) RecordBuild(id string) error {
	path := r.env.Paths.StateFile()
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil // a node that never ran the agent has nothing to record into
		}
		return err
	}
	if !json.Valid(data) {
		return errors.New("state.json is damaged, so it was not changed")
	}
	opts := state.Options{AgentVersion: r.env.AgentVersion}
	if r.env.LookupGroup != nil {
		if gid, gerr := r.env.LookupGroup(local.ServiceGroup); gerr == nil {
			opts.Owner = &state.Owner{UID: r.env.RootUID, GID: gid} // state.json is root:localai
		}
	}
	st, err := state.Open(path, opts)
	if err != nil {
		return err
	}
	defer st.Close()
	return build.StoreRecorder{Store: syncMutator{st}}.RecordBuild(id)
}

// nodeMode is the mode of this node ("online", "offline" or "survival"): from the agent when it
// answers, else from state.json (read only). It is "" when neither says, and callers then decide for
// themselves (rebuild-llama tries the network, update probes it).
func nodeMode(c *Call, env local.Env) string {
	if cl, err := c.Client(); err == nil {
		ctx, cancel := context.WithTimeout(c.Ctx, lookupTimeout)
		defer cancel()
		var m modeInfo
		if _, _, err := c.callWith(cl, ctx, http.MethodGet, "/mode", nil, &m); err == nil && validModeName(m.Mode) {
			return m.Mode
		}
	}
	if v, ok := readStateView(env); ok && validModeName(v.Mode) {
		return v.Mode
	}
	return ""
}

func validModeName(m string) bool { return m == "online" || m == "offline" || m == "survival" }

// ---- the agent ------------------------------------------------------------------------------------

// llamaAgent is build.Agent over the agent's HTTP API: POST /services/llama/restart (the agent takes its
// model lock, restarts the unit, waits for /health and restores the models). Up and the busy and
// unreachable mappings are those of whisperServices.
type llamaAgent struct{ whisperServices }

// RestartLlama implements build.Agent.
func (a llamaAgent) RestartLlama(ctx context.Context) error { return a.RestartService(ctx, "llama") }

// llamaProbe is build.Probe, the post-switch check of llama-router.md section 4.10.3: the router answers
// /health, the default model is loaded (loaded through the agent when it is not) and a 16-token
// completion succeeds. The router is asked directly (as root, with the key file); the agent is asked for
// what only it knows (the effective default model) and to load the model.
type llamaProbe struct {
	c    *Call
	env  local.Env
	port int

	once sync.Once
	rt   llamaRouter
	err  error
}

func newLlamaProbe(c *Call, env local.Env, set llamaSettings) *llamaProbe {
	return &llamaProbe{c: c, env: env, port: set.Port}
}

func (p *llamaProbe) router() (llamaRouter, error) {
	p.once.Do(func() { p.rt, p.err = llamaRouterFn(p.env, p.port) })
	return p.rt, p.err
}

// Health implements build.Probe.
func (p *llamaProbe) Health(ctx context.Context) error {
	rt, err := p.router()
	if err != nil {
		return err
	}
	return rt.Health(ctx)
}

// DefaultModel implements build.Probe: the agent's effective default (GET /models); with the agent
// down, the one recorded in state.json, else "" (only /health is then checked, with a warning).
func (p *llamaProbe) DefaultModel(ctx context.Context) (string, error) {
	if cl, err := p.c.Client(); err == nil {
		var ms struct {
			Default *string `json:"default"`
		}
		if _, _, err := p.c.callWith(cl, ctx, http.MethodGet, "/models", nil, &ms); err == nil {
			if ms.Default != nil {
				return *ms.Default, nil
			}
			return "", nil
		}
	}
	if v, ok := readStateView(p.env); ok && v.Models.Default != nil {
		return *v.Models.Default, nil
	}
	return "", nil
}

// ModelStatus implements build.Probe: the model's status in the router's own GET /models; "" when the
// router does not list it.
func (p *llamaProbe) ModelStatus(ctx context.Context, id string) (string, error) {
	rt, err := p.router()
	if err != nil {
		return "", err
	}
	ms, err := rt.Models(ctx)
	if err != nil {
		return "", err
	}
	for _, m := range ms {
		if m.ID == id {
			return string(m.Status), nil
		}
	}
	return "", nil
}

// LoadModel implements build.Probe: POST /models/{id}/load through the agent, without unloading the
// other models and without waiting (the check polls). No agent is build.ErrNoAgent. An agent that is
// busy (LAI-AGT-022) is already doing something with the models, so the check keeps polling.
func (p *llamaProbe) LoadModel(ctx context.Context, id string) error {
	cl, err := p.c.Client()
	if err != nil {
		return build.ErrNoAgent
	}
	_, _, err = p.c.callWith(cl, ctx, http.MethodPost, modelPath(id, "/load"), map[string]any{"wait": false, "unload_others": false}, nil)
	var ae *APIError
	var ce *CLIError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &ae) && ae.Status == http.StatusConflict && ae.Code == doctor.AGT022:
		return nil
	case errors.As(err, &ce) && ce.Code == CodeUnreachable:
		return build.ErrNoAgent
	}
	return err
}

// Complete implements build.Probe: one short chat completion of at most maxTokens tokens.
func (p *llamaProbe) Complete(ctx context.Context, id string, maxTokens int) error {
	rt, err := p.router()
	if err != nil {
		return err
	}
	return rt.Chat(ctx, id, maxTokens)
}

// ---- the smoke model ------------------------------------------------------------------------------

// llamaChatRoles are the roles of a model `llama-cli` can write a few words with: an embedding model
// cannot, and would fail the smoke test for the wrong reason.
var llamaChatRoles = []string{"chat", "reasoning", "coder", "vision"}

// llamaSmokeModel returns the weights file of the smallest verified llama model that can generate text
// (aictl.md section 4.23, rebuild-kit.md section 4.3.3): the catalog says which models are llama models
// and what they are for, the manifests on the content roots say which are complete and verified. ok is
// false when there is none, or when the catalog cannot be read: the smoke test then skips its completion
// step with a warning rather than failing on a model that was never meant to chat.
func llamaSmokeModel(env local.Env) (string, bool) {
	cat, err := catalog.Load(env.Paths.CatalogDir())
	if err != nil || cat == nil {
		return "", false
	}
	return smallestModelFile(env, cat, "llama", "weights", llamaChatRoles)
}

// smallestModelFile finds, among the catalog's models of engine whose roles include one of roles (any
// role when roles is empty) and whose manifest on a content root is verified, the one with the smallest
// files of kind; the answer is the first such file of that model (the first shard of a split GGUF).
func smallestModelFile(env local.Env, cat *catalog.Catalog, engine, kind string, roles []string) (string, bool) {
	type cand struct {
		id, path string
		size     int64
	}
	var found []cand
	roots := local.FindRoots(context.Background(), env)
	for _, m := range cat.Models {
		if m.Engine != engine || !hasAnyRole(m.Roles, roles) {
			continue
		}
		for _, r := range roots {
			dir := filepath.Join(r.Path, "models", m.ID)
			mf, err := manifest.Load(config.OSFS{}, dir)
			if err != nil || mf.Engine != engine || mf.State != manifest.StateVerified {
				continue
			}
			var paths []string
			var size int64
			for _, f := range mf.Files {
				if f.Kind == kind && manifest.ValidFilePath(f.Path) {
					paths = append(paths, f.Path)
					size += f.SizeBytes
				}
			}
			if len(paths) == 0 {
				continue
			}
			sort.Strings(paths)
			found = append(found, cand{m.ID, filepath.Join(dir, filepath.FromSlash(paths[0])), size})
			break // the first root that has it is the one the router would use
		}
	}
	if len(found) == 0 {
		return "", false
	}
	sort.Slice(found, func(i, j int) bool {
		if found[i].size != found[j].size {
			return found[i].size < found[j].size
		}
		return found[i].id < found[j].id
	})
	return found[0].path, true
}

func hasAnyRole(have, want []string) bool {
	if len(want) == 0 {
		return true
	}
	for _, w := range want {
		if contains(have, w) {
			return true
		}
	}
	return false
}

// ---- the options ----------------------------------------------------------------------------------

// llamaBuildsDir is the folder holding the builds (and the build.log of one being made).
func llamaBuildsDir(host kitHost) string {
	return filepath.Join(llamaRoot(host), "builds")
}

func llamaRoot(host kitHost) string {
	if host.OptRoot != "" {
		return filepath.Join(host.OptRoot, "llama.cpp")
	}
	return build.DefaultRoot
}

// llamaOptions assembles every seam of the build manager from the machine: node.yaml, the kit's roots,
// the agent client and the router. Nothing here touches the machine yet.
func llamaOptions(c *Call, env local.Env, host kitHost, set llamaSettings, roots []kitRoot, kitPaths []string,
	req build.Request, pr *llamaPrinter) build.Options {
	opt := build.Options{
		Root:    llamaRoot(host),
		Recipe:  build.LlamaRecipe,
		BuiltBy: "aictl " + c.env.Version,
		Node:    func() config.LlamaBlock { return set.Llama },
		Exec:    host.Runner, Lookup: host.LookPath,
		Sources:     build.ChainWithKit(kit.SourceStemLlama, kitPaths, build.DefaultCloneURL),
		KitRoots:    func() []string { return kitPaths },
		ScratchDir:  host.ScratchDir,
		Arch:        host.GOARCH,
		CompileUser: llamaCompileUser,
		Audit:       localAudit{env},
		Agent:       llamaAgent{whisperServices{c}},
		Probe:       newLlamaProbe(c, env, set),
		Recorder:    llamaStateRecorder{env},
		PreviousBuild: func() string {
			if v, ok := readStateView(env); ok && v.Llama.PreviousBuild != nil {
				return *v.Llama.PreviousBuild
			}
			return ""
		},
		SmokeModel: func() (string, bool) { return llamaSmokeModel(env) },
		NodeConfig: llamaNodeRecorder{env},
		Logger:     llamaSlogger(c, pr),
	}
	// The offline simulation of a missing package (`apt-get -s install` against the kit's repository)
	// is the kit's own, the one `aictl kit install-debs --dry-run` runs.
	if codename, arch, err := kit.MachineRelease(host.FS, host.OSRelease, host.GOARCH); err == nil && len(roots) > 0 {
		opt.Apt = &kitAptSim{apt: &kit.Apt{
			FS: host.FS, Runner: host.Runner, LookPath: host.LookPath, Codename: codename, Arch: arch,
			SourceList: host.SourceList, ScratchDir: host.ScratchDir,
		}, roots: roots}
	}
	// After a switch the build is exported to the kit (rebuild-kit.md section 4.2); a computer whose
	// Ubuntu release is unknown cannot name the file, and then nothing is exported.
	if !req.DryRun {
		if ex, err := newPrebuiltExporter(c, env, host, kit.ProgramLlama); err == nil {
			opt.Exporter = ex
		}
	}
	return opt
}

// ---- progress, the heartbeat and Ctrl-C ----------------------------------------------------------

// llamaPrinter serialises everything the command writes to stderr while the engine runs: the stage
// lines, the compile progress (cmd_rebuild_llama_progress.go), the "still working" line of the
// heartbeat, the Ctrl-C question and the engine's -v log.
type llamaPrinter struct {
	c  *Call
	mu sync.Mutex

	// tty: stderr is a terminal, so the compile progress is one updating line (rend).
	tty  bool
	rend *progress.TextRenderer
	// meter reads the ninja lines; tenth is the last tenth of the build a plain line was printed
	// for; lastCompile is when the last ninja line came; paused is set while the Ctrl-C question
	// waits for its answer.
	meter       ninjaMeter
	tenth       int
	lastCompile time.Time
	paused      bool
}

func newLlamaPrinter(c *Call) *llamaPrinter {
	return &llamaPrinter{c: c, tty: !c.Global.JSON && !c.Global.Quiet && llamaStderrTTY(c)}
}

// line prints one progress line ("… message") to stderr, under the live compile line.
func (p *llamaPrinter) line(msg string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.clearLive()
	p.c.Out.Progress(msg)
}

// progress is Request.Progress: one line per stage. The engine repeats the plan line the command
// already printed before the confirmation, so that one is left out.
func (p *llamaPrinter) progress(pg build.Progress) {
	if p.c.Global.JSON {
		return
	}
	if pg.Stage == build.StageConfigure && strings.HasPrefix(pg.Message, "Building llama.cpp") {
		return
	}
	p.line(llamaProgressText(p.c.Lang, pg.Message))
}

// llamaProgressES are the engine's stage messages (internal/router/build, English only) in Spanish. A
// message the table does not know is shown as the engine wrote it.
var llamaProgressES = []struct{ en, es string }{
	{"Looking for the llama.cpp source ", "Buscando el código fuente de llama.cpp "},
	{"Unpacking the CUDA redistributable ", "Descomprimiendo el redistribuible de CUDA "},
	{"Compiling (this takes a while)", "Compilando (tarda un rato)"},
	{"Carrying on with the build that was stopped before", "Se continúa la compilación que se detuvo antes"},
	{"Installing the programs", "Instalando los programas"},
	{"Testing the new build", "Probando la nueva versión"},
	{"Switching to the new build", "Cambiando a la nueva versión"},
	{"Checking that the new build serves", "Comprobando que la nueva versión responde"},
	{"Removing builds and sources that are no longer needed", "Quitando versiones y fuentes que ya no hacen falta"},
	{"Checking the prebuilt build ", "Comprobando la compilación lista "},
	{"Testing the prebuilt build", "Probando la compilación lista"},
}

func llamaProgressText(l Lang, msg string) string {
	if l != LangES {
		return msg
	}
	for _, t := range llamaProgressES {
		if strings.HasPrefix(msg, t.en) {
			rest := strings.TrimPrefix(msg, t.en)
			if strings.HasSuffix(rest, " of the kit") {
				rest = strings.TrimSuffix(rest, " of the kit") + " del kit"
			}
			return t.es + rest
		}
	}
	return msg
}

// Write lets the engine's -v log share the lock.
func (p *llamaPrinter) Write(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.clearLive()
	return p.c.Stderr.Write(b)
}

// startHeartbeat prints "Still working (12 min so far)" every llamaHeartbeatEvery until the returned
// function is called, and with -v the last line of the build log. A build is a long silence
// otherwise.
func (p *llamaPrinter) startHeartbeat(buildsDir string) (stop func()) {
	if p.c.Global.JSON || p.c.Global.Quiet {
		return func() {}
	}
	tick, stopTick := llamaHeartbeat()
	start := llamaClock()
	done, finished := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(finished)
		for {
			select {
			case <-tick:
				if p.compiledRecently(llamaHeartbeatEvery) {
					continue // the compile progress already says that it is working
				}
				el := llamaClock().Sub(start)
				p.line(fmt.Sprintf(pick(p.c.Lang, "Still working on the build (%s so far)", "Sigue la compilación (%s hasta ahora)"), out.Duration(el.Round(time.Second))))
				if p.c.Global.Verbose {
					if l := lastBuildLogLine(buildsDir); l != "" {
						p.line(pick(p.c.Lang, "Build log: ", "Registro de compilación: ") + l)
					}
				}
			case <-done:
				return
			}
		}
	}()
	return func() {
		stopTick()
		close(done)
		<-finished
	}
}

// lastBuildLogLine is the last non-empty line of the newest builds/*.tmp/build.log, cut to 120
// characters ("" when there is none).
func lastBuildLogLine(buildsDir string) string {
	logs, _ := filepath.Glob(filepath.Join(buildsDir, "*.tmp", "build.log"))
	var newest string
	var when time.Time
	for _, l := range logs {
		if fi, err := os.Stat(l); err == nil && (newest == "" || fi.ModTime().After(when)) {
			newest, when = l, fi.ModTime()
		}
	}
	if newest == "" {
		return ""
	}
	f, err := os.Open(newest)
	if err != nil {
		return ""
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return ""
	}
	off := fi.Size() - 4096
	if off < 0 {
		off = 0
	}
	buf := make([]byte, fi.Size()-off)
	if _, err := f.ReadAt(buf, off); err != nil && len(buf) == 0 {
		return ""
	}
	lines := strings.Split(strings.TrimRight(string(buf), "\r\n"), "\n")
	line := strings.TrimSpace(lines[len(lines)-1])
	if r := []rune(line); len(r) > 120 {
		line = string(r[:120]) + "…"
	}
	return line
}

// guardInterrupt runs fn under a context that the first Ctrl-C does not cancel: aictl.md section 4.23,
// "Ctrl-C during a build asks `Stop the build? [y/N]`; yes cancels the cmake process group and leaves
// `current` untouched; no keeps waiting (the build cannot continue without this process)". Main cancels
// Call.Ctx on the first signal, so the build gets a context of its own that keeps the command's
// deadline. Without a terminal (--json, a pipe, a unit) nobody can be asked, and the signal stops the
// build at once. stopped reports that the person (or the signal) did stop it.
func (c *Call) guardInterrupt(pr *llamaPrinter, fn func(ctx context.Context) error) (stopped bool, err error) {
	base := context.WithoutCancel(c.Ctx)
	var cancel context.CancelFunc
	if dl, ok := c.Ctx.Deadline(); ok {
		base, cancel = context.WithDeadline(base, dl)
	} else {
		base, cancel = context.WithCancel(base)
	}
	defer cancel()
	sig, stopSig := llamaInterrupts()
	defer stopSig()

	askCtx, askCancel := context.WithCancel(base)
	defer askCancel()
	done := make(chan struct{})
	var wasStopped atomic.Bool
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-sig:
				if c.askToStop(askCtx, pr) {
					wasStopped.Store(true)
					cancel()
					return
				}
			case <-done:
				return
			}
		}
	}()
	err = fn(base)
	close(done)
	askCancel()
	wg.Wait()
	return wasStopped.Load(), err
}

// askToStop asks "Stop the build? [y/N]" and reports the answer; the default is No. Without a terminal
// the answer is yes.
func (c *Call) askToStop(ctx context.Context, pr *llamaPrinter) bool {
	if c.Global.JSON || !c.Term.Interactive() {
		return true
	}
	pr.mu.Lock()
	pr.clearLive()
	pr.paused = true // the compile progress must not draw over the question
	fmt.Fprint(c.Stderr, "\n"+pick(c.Lang, "Stop the build?", "¿Detener la compilación?")+i18n.T(c.Lang, i18n.KeyConfirmSuffix))
	pr.mu.Unlock()
	line, err := c.Term.ReadLine(ctx)
	pr.mu.Lock()
	pr.paused = false
	pr.mu.Unlock()
	if err != nil {
		return false
	}
	if isYes(c.Lang, line) {
		return true
	}
	pr.line(pick(c.Lang, "Carrying on with the build.", "Se continúa con la compilación."))
	return false
}
