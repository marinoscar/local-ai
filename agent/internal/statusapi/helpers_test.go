package statusapi_test

import (
	"context"
	"io"
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
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/hardware"
	hwfake "github.com/marinoscar/local-ai/agent/internal/hardware/fake"
	"github.com/marinoscar/local-ai/agent/internal/modes"
	"github.com/marinoscar/local-ai/agent/internal/router"
	"github.com/marinoscar/local-ai/agent/internal/services"
	"github.com/marinoscar/local-ai/agent/internal/state"
	statefake "github.com/marinoscar/local-ai/agent/internal/state/fake"
	"github.com/marinoscar/local-ai/agent/internal/statusapi"
	"gopkg.in/yaml.v3"
)

var t0 = time.Date(2026, 11, 2, 15, 4, 5, 0, time.UTC)

// The catalog fixture is the valid catalog of internal/catalog; its sha256 values are written as
// @sha:<digit>@ so that the redaction check never sees 64 hex digits.
var shaToken = regexp.MustCompile(`@sha:([0-9a-f])@`)

// loadCatalog loads a copy of ../catalog/testdata/valid, with the sha tokens expanded.
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

// nodeYAML is a small node.yaml with comments that an update must keep.
const nodeYAML = `# The node file of this test.
schema_version: 1
node:
  id: ai-n3
  display_name: "Node Three"
profile: small-8gb # keep this comment
profile_overrides:
  add_models: []
  remove_models: []
  add_content: []
  remove_content: []
  budget_bytes: null
tailscale:
  manage: true
  serve_pattern: B
`

// snapAiN3 is a CUDA laptop: an 8 GB card and 14 GiB of RAM.
func snapAiN3() hardware.Snapshot {
	temp, util := 61.0, 3
	cpu := 72.0
	bat := 64
	rem := int64(9000)
	return hardware.Snapshot{
		SampledAt: t0,
		Backend:   hardware.BackendCUDA,
		GPUs: []hardware.GPU{{
			Index: 0, Vendor: hardware.VendorNVIDIA, Name: "GPU Example", Backend: hardware.BackendCUDA,
			VRAMTotalMiB: 8192, VRAMUsedMiB: 6791, TempC: &temp, UtilPct: &util, Throttling: []string{},
			Processes: []hardware.GPUProcess{{PID: 4242, Name: "llama-server", VRAMMiB: 6581}},
		}},
		RAM:     hardware.RAM{TotalMiB: 14336, AvailableMiB: 5120},
		Power:   hardware.Power{Source: hardware.SourceBattery, BatteryPercent: &bat, TimeRemainingS: &rem},
		Thermal: hardware.Thermal{CPUTempC: &cpu, Zones: []hardware.ThermalZone{}},
		Disks:   []hardware.Disk{},
	}
}

// ---------------------------------------------------------------------------------------------------
// Fakes of the views

type fakeMode struct{ info modes.Info }

func (f *fakeMode) Info() modes.Info { return f.info }

type fakeServices struct{ list []services.Status }

func (f *fakeServices) Snapshot(context.Context) []services.Status { return f.list }

type fakeAlerts struct{ list []doctor.Alert }

func (f *fakeAlerts) Active() []doctor.Alert { return f.list }

type fakeRouter struct {
	mu   sync.Mutex
	view router.View
}

func (f *fakeRouter) View() router.View {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.view
}

func (f *fakeRouter) set(v router.View) {
	f.mu.Lock()
	f.view = v
	f.mu.Unlock()
}

// fakePresence is a set of "<root>|<model id>" that hold a complete copy.
type fakePresence map[string]bool

func (p fakePresence) Present(root string, m *catalog.Model) bool { return p[root+"|"+m.ID] }

type fakeArk struct{ info statusapi.ArkInfo }

func (f fakeArk) Ark() statusapi.ArkInfo { return f.info }

type fakeCloud struct{ info statusapi.CloudInfo }

func (f fakeCloud) Cloud() statusapi.CloudInfo { return f.info }

type fakeTailscale struct{ st modes.TailscaleStatus }

func (f fakeTailscale) Status() modes.TailscaleStatus { return f.st }

type fakeCounters struct{}

func (fakeCounters) Counters() map[string]any {
	return map[string]any{"model_loads_ok": 3, "model_loads_failed": 1}
}

type fakeContent map[string]statusapi.ContentState

func (f fakeContent) ContentState(id string) statusapi.ContentState { return f[id] }

// fakeRegen counts regenerations.
type fakeRegen struct {
	mu    sync.Mutex
	calls int
	res   statusapi.RegenResult
	err   error
}

func (f *fakeRegen) Regenerate(context.Context) (statusapi.RegenResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.res, f.err
}

func (f *fakeRegen) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// fakeRefresher answers Refresh with a scripted result and counts the calls.
type fakeRefresher struct {
	mu    sync.Mutex
	calls int
	res   *catalog.RefreshResult
	err   error
}

func (f *fakeRefresher) Refresh(context.Context) (*catalog.RefreshResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.res, f.err
}

func (f *fakeRefresher) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
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

// ---------------------------------------------------------------------------------------------------
// The rig

// rig is the real API server and middleware chain with the statusapi routes over fakes and a real
// config store on a temp node.yaml.
type rig struct {
	t        *testing.T
	cfg      *config.Store
	nodePath string
	cat      *catalog.Catalog
	store    *statusapi.CatalogStore
	hw       *hwfake.Sampler
	st       *state.Store
	mode     *fakeMode
	svc      *fakeServices
	alerts   *fakeAlerts
	rt       *fakeRouter
	pres     fakePresence
	regen    *fakeRegen
	refresh  *fakeRefresher
	audit    *recAudit
	deps     statusapi.Deps
	h        http.Handler
}

// newRig wires everything. tweak edits the deps before the server is built.
func newRig(t *testing.T, tweak func(*rig, *statusapi.Deps)) *rig {
	t.Helper()
	r := &rig{t: t, cat: loadCatalog(t)}
	r.nodePath = filepath.Join(t.TempDir(), "node.yaml")
	writeFile(t, r.nodePath, nodeYAML)
	r.cfg = config.Open(config.Options{Path: r.nodePath})
	r.store = statusapi.NewCatalogStore(catalog.Loaded{Catalog: r.cat, Source: catalog.SourceOffline}, t0.Add(-time.Hour))
	r.hw = hwfake.New(snapAiN3())
	var err error
	r.st, err = state.Open("/var/lib/localai/state.json", state.Options{FS: statefake.New(), Now: func() time.Time { return t0 }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.st.Close() })
	since := t0.Add(-26 * time.Hour)
	r.mode = &fakeMode{info: modes.Info{Mode: modes.Online, Effective: modes.Online, Since: &since, Connectivity: state.ConnFull}}
	r.svc = &fakeServices{list: []services.Status{
		{Name: "llama", Unit: "localai-llama.service", Active: "active", Sub: "running", Since: t0.Add(-7 * time.Hour), Healthy: true},
		{Name: "whisper", Unit: "localai-whisper.service", Active: "inactive", Sub: "dead"},
	}}
	r.alerts = &fakeAlerts{}
	r.rt = &fakeRouter{view: router.View{At: t0}}
	r.pres = fakePresence{}
	r.regen = &fakeRegen{}
	r.refresh = &fakeRefresher{}
	r.audit = &recAudit{}

	merger := &statusapi.ModelMerger{
		Config: r.cfg, Catalog: r.store, State: r.st, Mode: r.mode, Hardware: r.hw, Router: r.rt,
		Roots:    func() router.Roots { return router.Roots{Internal: "/srv/localai"} },
		Presence: r.pres,
	}
	r.deps = statusapi.Deps{
		Config: r.cfg, Catalog: r.store, State: r.st, Hardware: r.hw, Mode: r.mode, Services: r.svc,
		Models: merger, Alerts: r.alerts, Counters: fakeCounters{}, Content: fakeContent{},
		Refresher: r.refresh, Regenerator: r.regen, Audit: r.audit,
		Version: "0.1.0", Hostname: func() (string, error) { return "host-example", nil },
		StartedAt: t0.Add(-24 * time.Hour), Now: func() time.Time { return t0 },
		CheckedAt: func() time.Time { return t0.Add(-5 * time.Second) },
	}
	if tweak != nil {
		tweak(r, &r.deps)
	}
	r.h = serve(t, r.deps)
	return r
}

func writeFile(t testing.TB, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o640); err != nil {
		t.Fatal(err)
	}
}

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

// serve builds the real server with the statusapi routes registered.
func serve(t testing.TB, d statusapi.Deps) http.Handler {
	t.Helper()
	reg := api.NewRegistry()
	if err := statusapi.Register(reg, d); err != nil {
		t.Fatal(err)
	}
	s, err := api.New(api.Options{Registry: reg, Auth: principals, Version: "test", Mode: func() string { return "online" }})
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

// emptyLoaded is what catalog.Fetcher.Startup returns when nothing loads.
func emptyLoaded() catalog.Loaded { return catalog.Loaded{} }

// failingConfig is a config store whose edits node.yaml's validation refuses.
type failingConfig struct{ *config.Store }

func (failingConfig) Update(func(*yaml.Node) error) error {
	return &config.ValidationError{Problems: config.Problems{{Path: "profile", Message: "refused for the test", Severity: config.SeverityError}}}
}
