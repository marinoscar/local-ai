package router

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/catalog"
	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/state"
	statefake "github.com/marinoscar/local-ai/agent/internal/state/fake"
)

// ---- a fake llama-server: one model (single) or a router, over HTTP on loopback ----

type srvModel struct {
	ID      string
	Aliases []string
	Status  string // router kind only
}

// singleSrv answers /health, /v1/models and /models the way llama-server does: a one-model server
// lists its model with no status, a router lists every preset with one. It checks the bearer key on
// everything but /health and keeps a log of "METHOD path".
type singleSrv struct {
	srv *httptest.Server
	key string

	mu      sync.Mutex
	healthy bool
	kind    string // LlamaModeSingle or LlamaModeRouter
	models  []srvModel
	reqs    []string
}

func newSingleSrv(key string) *singleSrv {
	s := &singleSrv{key: key, kind: LlamaModeSingle}
	s.srv = httptest.NewServer(http.HandlerFunc(s.serve))
	return s
}

func (s *singleSrv) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.reqs = append(s.reqs, r.Method+" "+r.URL.Path)
	healthy, kind := s.healthy, s.kind
	models := append([]srvModel(nil), s.models...)
	s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if r.URL.Path == "/health" {
		if !healthy {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":{"code":503,"message":"Loading model","type":"unavailable_error"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"status":"ok"}`))
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+s.key {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"code":401,"message":"Invalid API Key","type":"authentication_error"}}`))
		return
	}
	if !healthy {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":{"code":503,"message":"Loading model","type":"unavailable_error"}}`))
		return
	}
	if r.Method == http.MethodGet && (r.URL.Path == "/v1/models" || r.URL.Path == "/models") {
		data := []any{}
		for _, m := range models {
			e := map[string]any{"id": m.ID, "object": "model", "owned_by": "llamacpp", "aliases": m.Aliases}
			if kind == LlamaModeRouter {
				e["status"] = map[string]any{"value": m.Status}
			}
			data = append(data, e)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": data})
		return
	}
	w.WriteHeader(http.StatusNotFound)
	_, _ = w.Write([]byte(`{"error":{"code":404,"message":"not found","type":"not_found_error"}}`))
}

func (s *singleSrv) set(healthy bool, kind string, models ...srvModel) {
	s.mu.Lock()
	s.healthy, s.kind, s.models = healthy, kind, models
	s.mu.Unlock()
}

func (s *singleSrv) setHealthy(h bool) {
	s.mu.Lock()
	s.healthy = h
	s.mu.Unlock()
}

func (s *singleSrv) requests() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.reqs...)
}

func (s *singleSrv) count(prefix string) int {
	n := 0
	for _, r := range s.requests() {
		if strings.HasPrefix(r, prefix) {
			n++
		}
	}
	return n
}

// ---- the fake systemd: restart reads the unit file the way systemd would start from it ----

var (
	unitModelRe = regexp.MustCompile(`(?m)^\s+-m (\S+)`)
	unitAliasRe = regexp.MustCompile(`(?m)^\s+--alias (\S+)`)
)

// sysd is the LlamaRestarter and LlamaStopper of the tests. A restart reads the unit file and brings
// the fake server to what that unit would run: the model of its -m path, or a router when the unit
// has --models-max. crash lists models whose process dies at start (the server never answers);
// slow lists how many polls a model takes to become ready.
type sysd struct {
	r *singleRig

	mu       sync.Mutex
	restarts int
	stops    int
	fail     map[int]error // restart number (0-based) -> error
	crash    map[string]bool
	slow     map[string]int
	// wrong makes restart number n bring up that model instead of what the unit says.
	wrong map[int]srvModel
	// onRestart runs after the unit was read and before the server changes (a test acts here).
	onRestart func(n int)
}

func (s *sysd) RestartLlama(ctx context.Context) error {
	s.mu.Lock()
	n := s.restarts
	s.restarts++
	err, hook := s.fail[n], s.onRestart
	s.mu.Unlock()
	if hook != nil {
		hook(n)
	}
	if err != nil {
		s.r.srv.setHealthy(false)
		return err
	}
	s.mu.Lock()
	w, ok := s.wrong[n]
	s.mu.Unlock()
	if ok {
		s.r.srv.set(true, LlamaModeSingle, w)
		return nil
	}
	s.apply()
	return nil
}

func (s *sysd) StopLlama(ctx context.Context) error {
	s.mu.Lock()
	s.stops++
	s.mu.Unlock()
	s.r.srv.setHealthy(false)
	return nil
}

func (s *sysd) counts() (restarts, stops int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.restarts, s.stops
}

// apply starts the server the unit on disk describes.
func (s *sysd) apply() {
	data, ok := s.r.fs.get(unitPath)
	if !ok {
		s.r.srv.setHealthy(false)
		return
	}
	text := string(data)
	if strings.Contains(text, "--models-max") {
		def := s.r.stateDefault()
		var ms []srvModel
		for _, m := range aiN3Models() {
			st := "unloaded"
			if m.ID == def {
				st = "loaded"
			}
			ms = append(ms, srvModel{ID: m.ID, Status: st})
		}
		s.r.srv.set(true, LlamaModeRouter, ms...)
		return
	}
	mm := unitModelRe.FindStringSubmatch(text)
	if mm == nil {
		s.r.srv.setHealthy(false)
		return
	}
	id := path.Base(path.Dir(mm[1]))
	names := []string{id}
	if am := unitAliasRe.FindStringSubmatch(text); am != nil {
		names = strings.Split(am[1], ",")
	}
	m := srvModel{ID: names[0], Aliases: names}
	s.mu.Lock()
	crash, slow := s.crash[id], s.slow[id]
	s.mu.Unlock()
	switch {
	case crash:
		s.r.srv.set(false, LlamaModeSingle, m)
	case slow > 0:
		s.r.srv.set(false, LlamaModeSingle, m)
		s.r.pending(slow, m)
	default:
		s.r.srv.set(true, LlamaModeSingle, m)
	}
}

// ---- other stubs ----

// flakyReloader is the unit generator's daemon-reload: the calls listed in fail answer an error.
type flakyReloader struct {
	mu   sync.Mutex
	n    int
	fail map[int]bool
}

func (f *flakyReloader) DaemonReload(ctx context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := f.n
	f.n++
	if f.fail[n] {
		return errors.New("daemon-reload failed")
	}
	return nil
}

func (f *flakyReloader) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.n
}

// iniStub is the preset generator: it records what it was asked to render.
type iniStub struct {
	mu    sync.Mutex
	calls []Inputs
}

func (g *iniStub) Generate(ctx context.Context, in Inputs) (Result, error) {
	g.mu.Lock()
	g.calls = append(g.calls, in)
	g.mu.Unlock()
	return Result{}, nil
}

func (g *iniStub) defaults() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	var out []string
	for _, c := range g.calls {
		out = append(out, c.StateDefault)
	}
	return out
}

// ---- the rig ----

type singleCfg struct {
	noUnits bool
	mode    string
}

// singleRig is an Orchestrator over a fake one-model llama-server on HTTP, a real unit generator on an
// in-memory disk, a real state store, a fake systemd that starts the server from the unit file on
// disk, and a virtual clock that only moves when the orchestrator waits. The node is ai-n3: two
// models (qwen3.5-9b-q4km, the default, and small-vlm-q4km), models_max 1.
type singleRig struct {
	t      *testing.T
	srv    *singleSrv
	client *Client
	clk    *vClock
	fs     *unitFS
	st     *state.Store
	gen    *UnitGenerator
	reload *flakyReloader
	sys    *sysd
	ini    *iniStub
	pub    *pubRec
	alerts *alertLog
	aud    *auditRec
	logs   *logBuf
	o      *Orchestrator

	mu      sync.Mutex
	modeV   string
	waiting int
	ready   srvModel
	hook    func(n int)
	sleeps  int
}

const (
	idQwen = "qwen3.5-9b-q4km"
	idVLM  = "small-vlm-q4km"
)

func newSingleRig(t *testing.T, mut ...func(*singleCfg)) *singleRig {
	t.Helper()
	cfg := &singleCfg{mode: LlamaModeSingle}
	for _, m := range mut {
		m(cfg)
	}
	r := &singleRig{
		t: t, clk: &vClock{t: fixedTime()}, fs: newUnitFS(), reload: &flakyReloader{fail: map[int]bool{}},
		ini: &iniStub{}, pub: &pubRec{}, alerts: &alertLog{}, aud: &auditRec{}, logs: &logBuf{}, modeV: cfg.mode,
	}
	bearer := secret(2)
	r.srv = newSingleSrv(bearer)
	t.Cleanup(r.srv.srv.Close)
	r.sys = &sysd{r: r, fail: map[int]error{}, crash: map[string]bool{}, slow: map[string]int{}, wrong: map[int]srvModel{}}

	c, err := NewClient(Options{BaseURL: r.srv.srv.URL, Keys: StaticKey(bearer), Timeout: 2 * time.Second, Logger: r.logs.logger(), Mode: r.mode})
	if err != nil {
		t.Fatal(err)
	}
	r.client = c
	st, err := state.Open(orStatePath, state.Options{FS: statefake.New(), AgentVersion: "test", Now: r.clk.Now})
	if err != nil {
		t.Fatal(err)
	}
	r.st = st
	t.Cleanup(func() { _ = st.Close() })

	r.fs.put(DefaultLlamaBinary, []byte("elf"), 0o755)
	for _, m := range aiN3Models() {
		installModel(r.fs.memFS, rootInternal, m)
	}
	gen, err := NewUnitGenerator(UnitGeneratorOptions{
		Paths: DefaultUnitPaths(""), FS: r.fs, MkdirAll: r.fs.MkdirAll, State: StateUnit{Store: st},
		Systemd: r.reload, Alerts: r.alerts, Clock: r.clk, Logger: r.logs.logger(),
	})
	if err != nil {
		t.Fatal(err)
	}
	r.gen = gen

	opt := OrchestratorOptions{
		Router: c, State: st, Inputs: r.inputs, Presence: presenceStub{},
		Lock: NewOpLock(nil, r.clk.Now), Generator: r.ini, Publisher: r.pub, Audit: r.aud, Alerts: r.alerts,
		Restarter: r.sys, Stopper: r.sys,
		UnitBase: func() UnitInputs { return UnitInputs{Port: 8080} },
		Timing:   Timing{RestartWait: 10 * time.Second}, Clock: r.clk, Sleep: r.sleep, Logger: r.logs.logger(),
	}
	if !cfg.noUnits {
		opt.Units = gen
	}
	o, err := NewOrchestrator(opt)
	if err != nil {
		t.Fatal(err)
	}
	r.o = o
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = o.Shutdown(ctx)
		if strings.Contains(r.logs.String(), bearer) {
			t.Errorf("the key is in the logs")
		}
	})
	return r
}

func (r *singleRig) mode() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.modeV
}

func (r *singleRig) setMode(m string) {
	r.mu.Lock()
	r.modeV = m
	r.mu.Unlock()
}

func (r *singleRig) stateDefault() string {
	if d := r.st.Snapshot().Models.Default; d != nil {
		return *d
	}
	return ""
}

func (r *singleRig) inputs() Inputs {
	def := r.stateDefault()
	return Inputs{
		NodeID: "ai-test", Catalog: &catalog.Catalog{Version: "test", Models: aiN3Models()}, Profile: aiN3Profile(),
		Llama: config.LlamaBlock{Mode: r.mode(), LoadTimeoutSeconds: 300, LegacyAliases: map[string][]string{idQwen: {"qwen3.5-9b"}}},
		Roots: Roots{Internal: rootInternal}, Fit: NewEnv(snapAiN3(), "cuda", routerBlock(1, 512), 0),
		StateDefault: def,
	}
}

// pending makes the server answer after n more polls (called by the fake systemd).
func (r *singleRig) pending(n int, m srvModel) {
	r.mu.Lock()
	r.waiting, r.ready = n, m
	r.mu.Unlock()
}

// sleep is the orchestrator's Sleep: it moves the virtual clock, lets a slow start progress, then
// lets a test act. It runs on the orchestrator's goroutine, so what it changes is in place before
// the next poll.
func (r *singleRig) sleep(_ context.Context, d time.Duration) {
	r.clk.advance(d)
	r.mu.Lock()
	r.sleeps++
	n := r.sleeps
	var up *srvModel
	if r.waiting > 0 {
		if r.waiting--; r.waiting == 0 {
			m := r.ready
			up = &m
		}
	}
	h := r.hook
	r.mu.Unlock()
	if up != nil {
		r.srv.set(true, LlamaModeSingle, *up)
	}
	if h != nil {
		h(n)
	}
}

// boot brings the node to "single mode, serving id": the unit is rendered and the server started
// from it, then every recorder is cleared so a test sees only what it does.
func (r *singleRig) boot(id string) {
	r.t.Helper()
	r.setMode(LlamaModeSingle)
	in := r.inputs()
	m, ok := findModel(in.Catalog, id)
	if !ok {
		r.t.Fatalf("no model %s", id)
	}
	if _, err := r.gen.Generate(bg, r.o.singleUnitInputs(in, m, rootInternal)); err != nil {
		r.t.Fatal(err)
	}
	r.sys.apply()
	r.clearRecorders()
}

func (r *singleRig) clearRecorders() {
	r.sys.mu.Lock()
	r.sys.restarts, r.sys.stops = 0, 0
	r.sys.mu.Unlock()
	r.reload.mu.Lock()
	r.reload.n = 0
	r.reload.mu.Unlock()
	r.alerts.reset()
	r.pub.take()
	r.srv.mu.Lock()
	r.srv.reqs = nil
	r.srv.mu.Unlock()
}

func (r *singleRig) unitText() string {
	r.t.Helper()
	b, ok := r.fs.get(unitPath)
	if !ok {
		r.t.Fatal("there is no unit on disk")
	}
	return string(b)
}

func (r *singleRig) prevText() string {
	r.t.Helper()
	b, ok := r.fs.get(unitPath + ".prev")
	if !ok {
		r.t.Fatal("there is no .prev next to the unit")
	}
	return string(b)
}

func (r *singleRig) lastLoad() state.LastLoad {
	r.t.Helper()
	ll := r.st.Snapshot().Models.LastLoad
	if ll == nil {
		r.t.Fatal("state.models.last_load is not set")
	}
	return *ll
}

// evs summarises the events published since the last call.
func (r *singleRig) evs() []string {
	var out []string
	for _, e := range r.pub.take() {
		b, _ := json.Marshal(e.data)
		out = append(out, fmt.Sprintf("%s %s", e.typ, b))
	}
	return out
}

func (r *singleRig) load(id string) (LoadResult, error) {
	return r.o.Load(bg, id, true, true, sysActor)
}

func hasSub(list []string, sub string) bool {
	for _, s := range list {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
