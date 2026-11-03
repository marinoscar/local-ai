package modelsapi_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/api"
	"github.com/marinoscar/local-ai/agent/internal/api/apitest"
	"github.com/marinoscar/local-ai/agent/internal/audit"
	"github.com/marinoscar/local-ai/agent/internal/catalog"
	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/hardware"
	hwfake "github.com/marinoscar/local-ai/agent/internal/hardware/fake"
	"github.com/marinoscar/local-ai/agent/internal/modelsapi"
	"github.com/marinoscar/local-ai/agent/internal/modes"
	"github.com/marinoscar/local-ai/agent/internal/router"
	"github.com/marinoscar/local-ai/agent/internal/state"
	statefake "github.com/marinoscar/local-ai/agent/internal/state/fake"
	"github.com/marinoscar/local-ai/agent/internal/statusapi"
)

// One clock reading for the whole test binary: served and expected fixtures share it.
var t0 = time.Date(2026, 11, 2, 15, 4, 5, 0, time.UTC)

const (
	idAlpha   = "model-alpha-9b-q4" // llama, fits the 8 GB card
	idBeta    = "model-beta-27b-q4" // llama, needs 20 GB: never loadable here
	idWhisper = "whisper-small"     // not a llama model
	rootMain  = "/srv/localai"      // the internal root of the rig
	rootDrive = "/media/ops/AI-ARK/localai"
)

// The catalog fixture is the valid catalog of internal/catalog; its sha256 values are written as
// @sha:<digit>@ so that the redaction check never sees 64 hex digits.
var shaToken = regexp.MustCompile(`@sha:([0-9a-f])@`)

func loadCatalog(t testing.TB) *catalog.Catalog {
	t.Helper()
	src := filepath.Join("..", "catalog", "testdata", "valid")
	dst := t.TempDir()
	err := filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		out := shaToken.ReplaceAllStringFunc(string(data), func(m string) string {
			return strings.Repeat(shaToken.FindStringSubmatch(m)[1], 64)
		})
		return os.WriteFile(target, []byte(out), 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	cat, err := catalog.Load(dst)
	if err != nil {
		t.Fatal(err)
	}
	return cat
}

// snapAiN3 is a CUDA laptop: an 8 GB card and 14 GiB of RAM.
func snapAiN3() hardware.Snapshot {
	return hardware.Snapshot{
		SampledAt: t0,
		Backend:   hardware.BackendCUDA,
		GPUs: []hardware.GPU{{
			Index: 0, Vendor: hardware.VendorNVIDIA, Name: "GPU Example", Backend: hardware.BackendCUDA,
			VRAMTotalMiB: 8192, VRAMUsedMiB: 300, Throttling: []string{},
		}},
		RAM: hardware.RAM{TotalMiB: 14336, AvailableMiB: 11000},
	}
}

// fakeKey is a key made at run time from a repeated byte, so that no 64-hex literal is in the tree.
func fakeKey(t testing.TB, b byte) string {
	t.Helper()
	k, err := router.GenerateKey(bytes.NewReader(bytes.Repeat([]byte{b}, router.KeyBytes)))
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// ---------------------------------------------------------------------------------------------------
// Fakes

type fakeConfig struct{ cfg config.Config }

func (f fakeConfig) Get() config.Config { return f.cfg }

type fakeMode struct {
	mu   sync.Mutex
	mode state.Mode
}

func (f *fakeMode) Info() modes.Info {
	f.mu.Lock()
	defer f.mu.Unlock()
	return modes.Info{Mode: f.mode, Effective: f.mode}
}

func (f *fakeMode) set(m state.Mode) {
	f.mu.Lock()
	f.mode = m
	f.mu.Unlock()
}

type fakeRouterView struct {
	mu   sync.Mutex
	view router.View
}

func (f *fakeRouterView) View() router.View {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.view
}

func (f *fakeRouterView) set(v router.View) {
	f.mu.Lock()
	f.view = v
	f.mu.Unlock()
}

// fakePresence is a set of "<root>|<model id>" that hold a complete copy.
type fakePresence map[string]bool

func (p fakePresence) Present(root string, m *catalog.Model) bool { return p[root+"|"+m.ID] }

type fakeTailnet struct{ url string }

func (f fakeTailnet) TailnetURL(context.Context) string { return f.url }

// fakeSync is the sync engine: it records what it was asked and answers what a test scripted.
type fakeSync struct {
	mu        sync.Mutex
	downloads []modelsapi.SyncDownload
	deletes   []modelsapi.SyncDelete
	runID     string
	dlErr     error
	delRes    modelsapi.SyncDeleteResult
	delErr    error
	// onDelete runs inside DeleteModel (to look at the lock, say).
	onDelete func()
}

func (f *fakeSync) StartModelDownload(_ context.Context, req modelsapi.SyncDownload) (modelsapi.SyncRun, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.downloads = append(f.downloads, req)
	if f.dlErr != nil {
		return modelsapi.SyncRun{}, f.dlErr
	}
	return modelsapi.SyncRun{ID: f.runID}, nil
}

func (f *fakeSync) DeleteModel(_ context.Context, req modelsapi.SyncDelete) (modelsapi.SyncDeleteResult, error) {
	f.mu.Lock()
	f.deletes = append(f.deletes, req)
	hook := f.onDelete
	f.mu.Unlock()
	if hook != nil {
		hook()
	}
	return f.delRes, f.delErr
}

func (f *fakeSync) downloadCalls() []modelsapi.SyncDownload {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]modelsapi.SyncDownload(nil), f.downloads...)
}

func (f *fakeSync) deleteCalls() []modelsapi.SyncDelete {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]modelsapi.SyncDelete(nil), f.deletes...)
}

// fakeRegen records regenerations and what the lock looked like during each.
type fakeRegen struct {
	mu       sync.Mutex
	calls    int
	lockFree []bool
	lock     *router.OpLock
	res      statusapi.RegenResult
	err      error
}

func (f *fakeRegen) Regenerate(context.Context) (statusapi.RegenResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.lock != nil {
		hold, err := f.lock.TryAcquire(router.HolderReload)
		f.lockFree = append(f.lockFree, err == nil)
		if err == nil {
			hold.Release()
		}
	}
	return f.res, f.err
}

func (f *fakeRegen) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// fakePusher is the cloud link's KeyPusher.
type fakePusher struct {
	mu    sync.Mutex
	calls int
	err   error
	// onPush runs inside Push.
	onPush func()
}

func (f *fakePusher) Push(context.Context) error {
	f.mu.Lock()
	f.calls++
	hook := f.onPush
	err := f.err
	f.mu.Unlock()
	if hook != nil {
		hook()
	}
	return err
}

func (f *fakePusher) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// fakeRestarter stands in for the orchestrator's RestartWith the way the real one behaves: it takes
// modelOpsMu without waiting, runs Prepare under it, then "restarts" and releases the lock.
type fakeRestarter struct {
	lock *router.OpLock

	mu       sync.Mutex
	calls    int
	heldKind string // the holder's kind while Prepare and the restart ran
	order    []string
	restored []string
	// restartErr is returned after Prepare ran (the unit did not restart).
	restartErr error
	// busy makes it answer the 409 of a busy lock without calling Prepare.
	busy *router.OpError
	// onRestart runs between Prepare and the return.
	onRestart func()
}

func (f *fakeRestarter) RestartWith(ctx context.Context, req router.RestartRequest) (router.RestartResult, error) {
	f.mu.Lock()
	f.calls++
	busy, restored, rerr, hook := f.busy, f.restored, f.restartErr, f.onRestart
	f.mu.Unlock()
	if busy != nil {
		return router.RestartResult{}, busy
	}
	hold, err := f.lock.TryAcquire(router.HolderRestart)
	if err != nil {
		var b *router.BusyError
		if errors.As(err, &b) {
			return router.RestartResult{}, &router.OpError{
				Code: router.CodeBusy, Status: http.StatusConflict, Message: "busy",
				Details: map[string]any{"op_id": b.OpID, "running": b.Kind},
			}
		}
		return router.RestartResult{}, err
	}
	defer hold.Release()
	if h, ok := f.lock.Holder(); ok {
		f.mu.Lock()
		f.heldKind = h.Kind
		f.mu.Unlock()
	}
	if req.Prepare != nil {
		f.record("prepare")
		if err := req.Prepare(ctx, hold.OpID); err != nil {
			return router.RestartResult{OpID: hold.OpID}, err
		}
	}
	f.record("restart")
	if hook != nil {
		hook()
	}
	if rerr != nil {
		return router.RestartResult{OpID: hold.OpID}, rerr
	}
	return router.RestartResult{OpID: hold.OpID, Restored: restored}, nil
}

func (f *fakeRestarter) record(s string) {
	f.mu.Lock()
	f.order = append(f.order, s)
	f.mu.Unlock()
}

func (f *fakeRestarter) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func (f *fakeRestarter) steps() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.order...)
}

// recAudit records audit events.
type recAudit struct {
	mu  sync.Mutex
	evs []audit.Event
}

func (a *recAudit) Write(ev audit.Event) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.evs = append(a.evs, ev)
	return nil
}

func (a *recAudit) events() []audit.Event {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]audit.Event(nil), a.evs...)
}

// only returns the single event of an action, failing the test when there is not exactly one.
func (a *recAudit) only(t testing.TB, action string) audit.Event {
	t.Helper()
	var got []audit.Event
	for _, ev := range a.events() {
		if ev.Action == action {
			got = append(got, ev)
		}
	}
	if len(got) != 1 {
		t.Fatalf("%d audit events of %s, want 1: %+v", len(got), action, a.events())
	}
	return got[0]
}

type published struct {
	typ, opID string
	data      any
}

type recPublisher struct {
	mu  sync.Mutex
	evs []published
}

func (p *recPublisher) Publish(typ, opID string, data any) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.evs = append(p.evs, published{typ, opID, data})
	return nil
}

func (p *recPublisher) list() []published {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]published(nil), p.evs...)
}

type alertCall struct {
	op, code, subject string
	details           map[string]any
}

type recAlerts struct {
	mu    sync.Mutex
	calls []alertCall
}

func (a *recAlerts) Raise(code, subject string, details map[string]any) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls = append(a.calls, alertCall{"raise", code, subject, details})
}

func (a *recAlerts) Clear(code, subject string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls = append(a.calls, alertCall{"clear", code, subject, nil})
}

func (a *recAlerts) list() []alertCall {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]alertCall(nil), a.calls...)
}

// chownFS is the real filesystem with a Chown that only records the owner asked for, so the tests
// run as an ordinary user and still prove the key file is written root:root.
type chownFS struct {
	config.OSFS
	mu     sync.Mutex
	owners [][2]int
}

type chownFile struct {
	config.File
	fs *chownFS
}

func (f chownFile) Chown(uid, gid int) error {
	f.fs.mu.Lock()
	f.fs.owners = append(f.fs.owners, [2]int{uid, gid})
	f.fs.mu.Unlock()
	return nil
}

func (c *chownFS) OpenFile(name string, flag int, perm os.FileMode) (config.File, error) {
	f, err := c.OSFS.OpenFile(name, flag, perm)
	if err != nil {
		return nil, err
	}
	return chownFile{File: f, fs: c}, nil
}

func (c *chownFS) ownersSeen() [][2]int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([][2]int(nil), c.owners...)
}

// failRenameFS fails the rename of the atomic write, as a full or read-only disk does.
type failRenameFS struct{ *chownFS }

func (f failRenameFS) Rename(string, string) error {
	return errors.New("rename: no space left on device")
}

// ---------------------------------------------------------------------------------------------------
// The rig

// rig is the real API server and middleware chain with the modelsapi routes over fakes, a real key
// file in a temp directory and a real state store.
type rig struct {
	t       *testing.T
	cat     *catalog.Catalog
	store   *statusapi.CatalogStore
	cfg     *fakeConfig
	st      *state.Store
	mode    *fakeMode
	rv      *fakeRouterView
	pres    fakePresence
	sync    *fakeSync
	lock    *router.OpLock
	regen   *fakeRegen
	pusher  *fakePusher
	restart *fakeRestarter
	audit   *recAudit
	pub     *recPublisher
	alerts  *recAlerts
	fs      *chownFS
	logs    *bytes.Buffer

	keyPath  string
	oldKey   string
	liveKeys []string
	deps     modelsapi.Deps
	svc      *modelsapi.Service
	h        http.Handler
}

// newRig wires everything. tweak edits the deps before the service is built.
func newRig(t *testing.T, tweak func(*rig, *modelsapi.Deps)) *rig {
	t.Helper()
	r := &rig{t: t, cat: loadCatalog(t), logs: &bytes.Buffer{}}
	r.store = statusapi.NewCatalogStore(catalog.Loaded{Catalog: r.cat, Source: catalog.SourceOffline}, t0.Add(-time.Hour))

	cfg := config.Defaults()
	host := "ai-n3.example.test"
	cfg.Node.PublicHost = &host
	r.cfg = &fakeConfig{cfg: cfg}

	var err error
	r.st, err = state.Open("/var/lib/localai/state.json", state.Options{FS: statefake.New(), Now: func() time.Time { return t0 }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.st.Close() })
	def := idAlpha
	if err := r.st.MutateSync(func(s *state.State) error {
		s.Models.Default = &def
		s.Key = state.Key{Fingerprint: "old0…old1", CloudSynced: true}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	r.mode = &fakeMode{mode: state.ModeOnline}
	r.rv = &fakeRouterView{view: router.View{At: t0}}
	r.pres = fakePresence{}
	r.lock = router.NewOpLock(nil, func() time.Time { return t0 })
	r.sync = &fakeSync{runID: "run-20261102T150405Z-a1f3"}
	r.regen = &fakeRegen{lock: r.lock}
	r.pusher = &fakePusher{}
	r.restart = &fakeRestarter{lock: r.lock, restored: []string{idAlpha}}
	r.audit = &recAudit{}
	r.pub = &recPublisher{}
	r.alerts = &recAlerts{}
	r.fs = &chownFS{}

	dir := t.TempDir()
	r.keyPath = filepath.Join(dir, "api-key")
	r.oldKey = fakeKey(t, 0xAB)
	if err := os.WriteFile(r.keyPath, []byte(r.oldKey+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	keys := router.NewFileKey(nil, r.keyPath)

	hw := hwfake.New(snapAiN3())
	merger := &statusapi.ModelMerger{
		Config: r.cfg, Catalog: r.store, State: r.st, Mode: r.mode, Hardware: hw, Router: r.rv,
		Roots:    func() router.Roots { return router.Roots{Internal: rootMain, Drives: []string{rootDrive}} },
		Presence: r.pres,
	}
	r.deps = modelsapi.Deps{
		Config: r.cfg, Catalog: r.store, Models: merger, Mode: r.mode, Sync: r.sync, Lock: r.lock,
		Regenerator: r.regen, Keys: keys, KeyFS: r.fs, KeyPath: r.keyPath,
		Rand:       &countingRand{t: t, next: 0x11},
		SetLiveKey: func(k string) { r.liveKeys = append(r.liveKeys, k) },
		Restarter:  r.restart, State: r.st, Pusher: r.pusher, Tailnet: fakeTailnet{url: "https://hp-linux.example.test/v1"},
		Publisher: r.pub, Alerts: r.alerts, Audit: r.audit,
		Now:    func() time.Time { return t0.Add(10 * time.Minute) },
		Logger: slog.New(slog.NewTextHandler(r.logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
	}
	if tweak != nil {
		tweak(r, &r.deps)
	}
	r.svc, err = modelsapi.New(r.deps)
	if err != nil {
		t.Fatal(err)
	}
	r.h = serve(t, r.svc)
	return r
}

// countingRand hands out the bytes of a fresh key: every Read fills the buffer with the next byte
// value, so each rotation of a test gets a different, predictable key.
type countingRand struct {
	t    testing.TB
	next byte
}

func (c *countingRand) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = c.next
	}
	c.next++
	return len(p), nil
}

// expectedKey is the key the n-th rotation of a rig makes (n starts at 0).
func expectedKey(t testing.TB, n int) string { return fakeKey(t, byte(0x11+n)) }

// failingRand is an entropy source that is gone.
type failingRand struct{}

func (failingRand) Read([]byte) (int, error) { return 0, io.ErrNoProgress }

// principals authenticates from the X-Test-Principal header.
var principals = api.AuthFunc(func(r *http.Request, _ *api.Route) (api.Principal, error) {
	switch r.Header.Get("X-Test-Principal") {
	case "op":
		return api.Principal{Kind: api.KindOperator, Credential: api.CredBearer, Via: "bearer"}, nil
	case "admin":
		return api.Principal{Kind: api.KindAdmin, Credential: api.CredSocket, Via: "uid=0"}, nil
	case "portal":
		return api.Principal{Kind: api.KindPortal, Credential: api.CredPortal, Via: "portal"}, nil
	}
	return api.Anonymous(), nil
})

// serve builds the real server with the service's routes registered.
func serve(t testing.TB, svc *modelsapi.Service) http.Handler {
	t.Helper()
	reg := api.NewRegistry()
	if err := svc.Register(reg); err != nil {
		t.Fatal(err)
	}
	s, err := api.New(api.Options{Registry: reg, Auth: principals, Version: "test", Mode: func() string { return "online" }, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	s.SetMutationsOpen(true)
	return s.Handler()
}

// call is one request. principal is op, admin, portal or "" (anonymous).
type call struct {
	method, target, principal, body string
	header                          map[string]string
}

// do serves c and records the response against openapi.yaml (agent.md section 4.14): a body or a
// status the document does not describe fails the test.
func do(t testing.TB, h http.Handler, c call) *httptest.ResponseRecorder {
	t.Helper()
	method := c.method
	if method == "" {
		method = http.MethodGet
	}
	var body io.Reader
	if c.body != "" {
		body = strings.NewReader(c.body)
	}
	r := httptest.NewRequest(method, "/api/v1"+c.target, body)
	r.Host = "127.0.0.1:7070"
	if c.principal != "" {
		r.Header.Set("X-Test-Principal", c.principal)
	}
	if method != http.MethodGet {
		r.Header.Set("Content-Type", "application/json")
	}
	for k, v := range c.header {
		r.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	apitest.RecordResult(t, r, rec)
	return rec
}

func (r *rig) get(target, principal string) *httptest.ResponseRecorder {
	r.t.Helper()
	return do(r.t, r.h, call{target: target, principal: principal})
}

func (r *rig) send(method, target, principal, body string) *httptest.ResponseRecorder {
	r.t.Helper()
	return do(r.t, r.h, call{method: method, target: target, principal: principal, body: body})
}

// has puts a complete copy of a model on a root.
func (r *rig) has(root, id string) { r.pres[root+"|"+id] = true }

// router sets the router's cached view.
func (r *rig) routerHas(statuses ...router.ModelStatus) {
	r.rv.set(router.View{At: t0, Models: statuses})
}

// readKey is the key file's key.
func (r *rig) readKey() string {
	r.t.Helper()
	b, err := os.ReadFile(r.keyPath)
	if err != nil {
		r.t.Fatal(err)
	}
	return strings.TrimSpace(string(b))
}

// everything is every place a secret could leak to, as one text: the log, the audit lines, the
// published events and the state file.
func (r *rig) everything() string {
	var b strings.Builder
	b.WriteString(r.logs.String())
	for _, ev := range r.audit.events() {
		fmt.Fprintf(&b, "%+v\n", ev)
	}
	for _, ev := range r.pub.list() {
		fmt.Fprintf(&b, "%+v\n", ev)
	}
	fmt.Fprintf(&b, "%+v\n", r.st.Snapshot().Key)
	for _, c := range r.alerts.list() {
		fmt.Fprintf(&b, "%+v\n", c)
	}
	return b.String()
}

// errorEnvelope decodes an error response.
type errorEnvelope struct {
	Error struct {
		Code    string         `json:"code"`
		Message string         `json:"message"`
		Fix     string         `json:"fix"`
		Details map[string]any `json:"details"`
	} `json:"error"`
}
