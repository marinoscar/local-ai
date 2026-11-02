package portal_test

import (
	"context"
	"encoding/json"
	"errors"
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
	"github.com/marinoscar/local-ai/agent/internal/catalog"
	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/hardware"
	hwfake "github.com/marinoscar/local-ai/agent/internal/hardware/fake"
	"github.com/marinoscar/local-ai/agent/internal/modes"
	"github.com/marinoscar/local-ai/agent/internal/portal"
	"github.com/marinoscar/local-ai/agent/internal/router"
	"github.com/marinoscar/local-ai/agent/internal/statusapi"
)

var t0 = time.Date(2026, 11, 2, 14, 5, 11, 0, time.UTC)

var shaToken = regexp.MustCompile(`@sha:([0-9a-f])@`)

// loadCatalog loads a copy of the valid catalog of internal/catalog (its sha256 values are written
// as @sha:<digit>@ so that the redaction check never sees 64 hex digits).
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

type fakeMode struct{ info modes.Info }

func (f *fakeMode) Info() modes.Info { return f.info }

type fakeAlerts struct{ list []doctor.Alert }

func (f *fakeAlerts) Active() []doctor.Alert { return f.list }

type fakeRouter struct{ view router.View }

func (f *fakeRouter) View() router.View { return f.view }

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

const nodeYAML = `schema_version: 1
node:
  id: ai-n3
  display_name: "Node Three"
profile: small-8gb
portal:
  language: auto
  emergency_number: "112"
`

func openConfig(t testing.TB, content string) *config.Store {
	t.Helper()
	p := filepath.Join(t.TempDir(), "node.yaml")
	if err := os.WriteFile(p, []byte(content), 0o640); err != nil {
		t.Fatal(err)
	}
	return config.Open(config.Options{Path: p})
}

func batteryPower(pct int, source string, charging bool) hardware.Power {
	return hardware.Power{Source: source, BatteryPercent: &pct, Charging: charging}
}

// rig is the minimal builder behind the real route and middleware chain.
type rig struct {
	t      *testing.T
	cfg    *config.Store
	cat    *catalog.Catalog
	hw     *hwfake.Sampler
	mode   *fakeMode
	rt     *fakeRouter
	alerts *fakeAlerts
	clk    *clock
	builds *countingBuilder
	h      http.Handler
}

// countingBuilder wraps a builder and counts the calls and the requests.
type countingBuilder struct {
	inner portal.StatusBuilder
	mu    sync.Mutex
	reqs  []portal.Request
	err   error
}

func (c *countingBuilder) Build(ctx context.Context, req portal.Request) (portal.Status, error) {
	c.mu.Lock()
	c.reqs = append(c.reqs, req)
	err := c.err
	c.mu.Unlock()
	if err != nil {
		return portal.Status{}, err
	}
	return c.inner.Build(ctx, req)
}

func (c *countingBuilder) calls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.reqs)
}

func (c *countingBuilder) last() portal.Request {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.reqs[len(c.reqs)-1]
}

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

func newRig(t *testing.T, nodeConf string) *rig {
	t.Helper()
	r := &rig{t: t, cat: loadCatalog(t), clk: &clock{t: t0}}
	r.cfg = openConfig(t, nodeConf)
	r.hw = hwfake.New(hardware.Snapshot{Power: batteryPower(72, hardware.SourceBattery, false)})
	r.mode = &fakeMode{info: modes.Info{Mode: modes.Survival, Effective: modes.Survival}}
	r.rt = &fakeRouter{view: router.View{At: t0}}
	r.alerts = &fakeAlerts{}
	store := statusapi.NewCatalogStore(catalog.Loaded{Catalog: r.cat, Source: catalog.SourceOffline}, t0)
	min := portal.NewMinimal(portal.MinimalSources{
		Config: r.cfg, Mode: r.mode, Hardware: r.hw, Router: r.rt, Catalog: store, Alerts: r.alerts,
		Settings:  func() portal.Settings { return portal.SettingsFrom(r.cfg.Get(), r.cat) },
		BuildTime: t0.Add(-24 * time.Hour),
	})
	r.builds = &countingBuilder{inner: min}
	reg := api.NewRegistry()
	err := portal.Register(reg, portal.RouteOptions{
		Builder:  r.builds,
		Settings: func() portal.Settings { return portal.SettingsFrom(r.cfg.Get(), r.cat) },
		Now:      r.clk.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	s, err := api.New(api.Options{Registry: reg, Auth: principals, Version: "test", Mode: func() string { return "survival" }})
	if err != nil {
		t.Fatal(err)
	}
	s.SetMutationsOpen(true)
	r.h = s.Handler()
	return r
}

func (r *rig) get(target, principal string, header map[string]string) *httptest.ResponseRecorder {
	r.t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1"+target, nil)
	req.Host = "127.0.0.1:7070"
	if principal != "" {
		req.Header.Set("X-Test-Principal", principal)
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	r.h.ServeHTTP(rec, req)
	apitest.RecordResult(r.t, req, rec)
	return rec
}

func status(t testing.TB, rec *httptest.ResponseRecorder) portal.Status {
	t.Helper()
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var st portal.Status
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	return st
}

const alpha = "model-alpha-9b-q4"

func TestPortalStatus(t *testing.T) {
	r := newRig(t, nodeYAML)
	r.rt.view = router.View{At: t0, Models: []router.ModelStatus{
		{ID: alpha, Status: router.StatusLoaded},
		{ID: "model-gamma-vl-q4", Status: router.StatusLoading},
		{ID: "unlisted-cache-model", Status: router.StatusLoaded},
	}}
	r.alerts.list = []doctor.Alert{{Code: doctor.SVC013, Severity: doctor.Info, Title: "x", Fix: "y", Since: t0}}
	rec := r.get("/portal/status", "", map[string]string{"Accept-Language": "es"})
	st := status(t, rec)

	if st.SchemaVersion != 1 || st.GeneratedAt != "2026-11-02T14:05:11Z" || st.NodeID != "ai-n3" || st.Mode != "survival" {
		t.Errorf("header = %+v", st)
	}
	if st.Language != "es" || st.EmergencyNumber != "112" || st.Session != "none" || !st.ClockOK {
		t.Errorf("language/emergency/session/clock = %q %q %q %v", st.Language, st.EmergencyNumber, st.Session, st.ClockOK)
	}
	if st.Power.State != "discharging" || st.Power.BatteryPct == nil || *st.Power.BatteryPct != 72 || st.Power.OnAC || st.Power.Low || st.Power.Critical {
		t.Errorf("power = %+v", st.Power)
	}
	if len(st.Brain.Loaded) != 1 || st.Brain.Loaded[0].ID != alpha || st.Brain.Loaded[0].DisplayName == "" || len(st.Brain.Loaded[0].Roles) == 0 || st.Brain.Loaded[0].Vision {
		t.Errorf("loaded = %+v", st.Brain.Loaded)
	}
	if st.Brain.Loading == nil || st.Brain.Loading.ID != "model-gamma-vl-q4" || !st.Brain.Loading.Vision {
		t.Errorf("loading = %+v", st.Brain.Loading)
	}
	if st.Capabilities["chat"].State != portal.StateReady {
		t.Errorf("chat = %+v", st.Capabilities["chat"])
	}
	if len(st.Alerts) != 1 || st.Alerts[0].Code != doctor.SVC013 {
		t.Fatalf("alerts = %+v", st.Alerts)
	}
	title, fix, _ := doctor.Text(doctor.SVC013, doctor.LangES)
	if st.Alerts[0].Title != title || st.Alerts[0].Fix != fix {
		t.Errorf("alert is not in Spanish: %+v", st.Alerts[0])
	}
	if st.FirstAid == nil || st.Brain.Choices == nil {
		t.Error("first_aid and brain.choices must be empty lists, not null")
	}
}

func TestPortalPower(t *testing.T) {
	ptr := func(v int64) *int64 { return &v }
	cases := []struct {
		name         string
		p            hardware.Power
		state        string
		onAC         bool
		low, crit    bool
		pct, minutes *int
	}{
		{"no battery on AC", hardware.Power{Source: hardware.SourceAC}, "no_battery", true, false, false, nil, nil},
		{"unknown power", hardware.Power{Source: hardware.SourceUnknown}, "no_battery", false, false, false, nil, nil},
		{"discharging", batteryPower(72, hardware.SourceBattery, false), "discharging", false, false, false, intp(72), nil},
		{"low", batteryPower(19, hardware.SourceBattery, false), "discharging", false, true, false, intp(19), nil},
		{"exactly 20 is not low", batteryPower(20, hardware.SourceBattery, false), "discharging", false, false, false, intp(20), nil},
		{"critical", batteryPower(9, hardware.SourceBattery, false), "discharging", false, true, true, intp(9), nil},
		{"charging", batteryPower(50, hardware.SourceAC, true), "charging", true, false, false, intp(50), nil},
		{"full", batteryPower(100, hardware.SourceAC, false), "full", true, false, false, intp(100), nil},
		{"low but plugged in is no alarm", batteryPower(5, hardware.SourceAC, true), "charging", true, false, false, intp(5), nil},
		{"time left", hardware.Power{Source: hardware.SourceBattery, BatteryPercent: intp(72), TimeRemainingS: ptr(8700)}, "discharging", false, false, false, intp(72), intp(145)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t, nodeYAML)
			r.hw.Set(hardware.Snapshot{Power: c.p})
			st := status(t, r.get("/portal/status", "", nil))
			p := st.Power
			if p.State != c.state || p.OnAC != c.onAC || p.Low != c.low || p.Critical != c.crit {
				t.Errorf("power = %+v", p)
			}
			if !sameInt(p.BatteryPct, c.pct) || !sameInt(p.MinutesLeft, c.minutes) {
				t.Errorf("pct %v minutes %v", p.BatteryPct, p.MinutesLeft)
			}
		})
	}
}

func intp(v int) *int { return &v }

func sameInt(a, b *int) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func TestPortalChatCapability(t *testing.T) {
	cases := []struct {
		name   string
		models []router.ModelStatus
		state  string
		code   string
	}{
		{"loaded", []router.ModelStatus{{ID: alpha, Status: router.StatusLoaded}}, "ready", ""},
		{"loading", []router.ModelStatus{{ID: alpha, Status: router.StatusLoading}}, "degraded", ""},
		{"nothing running", []router.ModelStatus{{ID: alpha, Status: router.StatusUnloaded}}, "unavailable", doctor.SVC106},
		{"failed", []router.ModelStatus{{ID: alpha, Status: router.StatusLoaded, Failed: true}}, "unavailable", doctor.SVC106},
		{"router has no model yet", nil, "unavailable", doctor.SVC106},
		{"only a cache model", []router.ModelStatus{{ID: "unlisted-cache-model", Status: router.StatusLoaded}}, "unavailable", doctor.SVC106},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t, nodeYAML)
			r.rt.view = router.View{At: t0, Models: c.models}
			chat := status(t, r.get("/portal/status", "", nil)).Capabilities["chat"]
			if chat.State != c.state || chat.Code != c.code {
				t.Errorf("chat = %+v", chat)
			}
		})
	}
}

func TestPortalLanguage(t *testing.T) {
	withBlock := func(lang string) string {
		return strings.Replace(nodeYAML, "language: auto", "language: "+lang, 1)
	}
	cases := []struct {
		name   string
		node   string
		target string
		accept string
		want   string
	}{
		{"nothing set, English", nodeYAML, "/portal/status", "", "en"},
		{"Accept-Language es", nodeYAML, "/portal/status", "es", "es"},
		{"Accept-Language es-CR", nodeYAML, "/portal/status", "es-CR,es;q=0.9,en;q=0.5", "es"},
		{"portal.language es beats Accept-Language en", withBlock("es"), "/portal/status", "en", "es"},
		{"portal.language en beats Accept-Language es", withBlock("en"), "/portal/status", "es", "en"},
		{"query beats portal.language", withBlock("es"), "/portal/status?lang=en", "es", "en"},
		{"query es", nodeYAML, "/portal/status?lang=es", "en", "es"},
		{"auto falls through to the request", withBlock("auto"), "/portal/status", "es", "es"},
		{"an unknown portal.language is ignored", withBlock("fr"), "/portal/status", "es", "es"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t, c.node)
			var h map[string]string
			if c.accept != "" {
				h = map[string]string{"Accept-Language": c.accept}
			}
			if got := status(t, r.get(c.target, "", h)).Language; got != c.want {
				t.Errorf("language = %q, want %q", got, c.want)
			}
		})
	}
}

func TestPortalLanguageFromProfile(t *testing.T) {
	r := newRig(t, nodeYAML)
	cfg := r.cfg.Get()
	// The profile's portal_language is the step after portal.language and before the request.
	cat := loadCatalog(t)
	for i := range cat.Profiles {
		if cat.Profiles[i].ID == "small-8gb" {
			cat.Profiles[i].Survival.PortalLanguage = "es"
		}
	}
	s := portal.SettingsFrom(cfg, cat)
	if s.ProfileLanguage != "es" || s.Language != "auto" || s.EmergencyNumber != "112" {
		t.Errorf("settings = %+v", s)
	}
	none := portal.SettingsFrom(cfg, nil)
	if none.ProfileLanguage != "" {
		t.Errorf("settings without a catalog = %+v", none)
	}
}

func TestSettingsFromDefaults(t *testing.T) {
	cfg := openConfig(t, "schema_version: 1\nnode:\n  id: ai-n3\n")
	s := portal.SettingsFrom(cfg.Get(), loadCatalog(t))
	if s.Language != "" || s.EmergencyNumber != "" {
		t.Errorf("settings = %+v", s)
	}
	// The builder falls back to the default emergency number.
	r := newRig(t, "schema_version: 1\nnode:\n  id: ai-n3\nprofile: small-8gb\n")
	if got := status(t, r.get("/portal/status", "", nil)).EmergencyNumber; got != portal.DefaultEmergencyNumber {
		t.Errorf("emergency number = %q", got)
	}
}

func TestPortalInvalidLang(t *testing.T) {
	r := newRig(t, nodeYAML)
	for _, target := range []string{"/portal/status?lang=fr", "/portal/status?lang=", "/portal/status?lang=ES"} {
		rec := r.get(target, "", nil)
		if rec.Code != 400 {
			t.Fatalf("%s: status %d: %s", target, rec.Code, rec.Body.String())
		}
		var e struct{ Error struct{ Code string } }
		_ = json.Unmarshal(rec.Body.Bytes(), &e)
		if e.Error.Code != api.CodeInvalidRequest {
			t.Errorf("%s: code %s", target, e.Error.Code)
		}
	}
	if r.builds.calls() != 0 {
		t.Error("the builder ran for an invalid request")
	}
}

func TestPortalSessionKinds(t *testing.T) {
	r := newRig(t, nodeYAML)
	for principal, want := range map[string]string{"": "none", "portal": "portal", "op": "full", "admin": "full"} {
		if got := status(t, r.get("/portal/status", principal, nil)).Session; got != want {
			t.Errorf("principal %q: session %q, want %q", principal, got, want)
		}
	}
}

// TestPortalCache: a body is served again for 2 s per language and session kind, then rebuilt.
func TestPortalCache(t *testing.T) {
	r := newRig(t, nodeYAML)
	first := r.get("/portal/status", "", nil).Body.String()
	r.get("/portal/status", "", nil)
	r.clk.Advance(time.Second + 900*time.Millisecond)
	cached := r.get("/portal/status", "", nil).Body.String()
	if r.builds.calls() != 1 || cached != first {
		t.Fatalf("builds = %d, body changed = %v", r.builds.calls(), cached != first)
	}

	// A change shows up only once the entry is older than 2 s.
	r.hw.Set(hardware.Snapshot{Power: batteryPower(50, hardware.SourceBattery, false)})
	r.clk.Advance(150 * time.Millisecond)
	st := status(t, r.get("/portal/status", "", nil))
	if r.builds.calls() != 2 || st.Power.BatteryPct == nil || *st.Power.BatteryPct != 50 {
		t.Errorf("builds = %d power = %+v", r.builds.calls(), st.Power)
	}

	// Another language and another session kind have entries of their own.
	r.get("/portal/status?lang=es", "", nil)
	r.get("/portal/status", "portal", nil)
	r.get("/portal/status", "op", nil)
	if r.builds.calls() != 5 {
		t.Errorf("builds = %d, want 5", r.builds.calls())
	}
	r.get("/portal/status?lang=es", "", nil)
	r.get("/portal/status", "portal", nil)
	if r.builds.calls() != 5 {
		t.Errorf("builds = %d after repeats, want 5", r.builds.calls())
	}
	if req := r.builds.last(); req.Session != "full" || req.Lang != "en" {
		t.Errorf("last request = %+v", req)
	}
}

func TestPortalBuilderError(t *testing.T) {
	r := newRig(t, nodeYAML)
	r.builds.err = errors.New("the capability model failed")
	rec := r.get("/portal/status", "", nil)
	if rec.Code != 500 {
		t.Fatalf("status %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "capability model") {
		t.Errorf("the error text reached the client: %s", rec.Body.String())
	}
	// A failure is not cached.
	r.builds.err = nil
	if rec := r.get("/portal/status", "", nil); rec.Code != 200 {
		t.Errorf("status %d after the builder recovered", rec.Code)
	}
}

func TestPortalClockOK(t *testing.T) {
	r := newRig(t, nodeYAML)
	r.clk.t = t0.Add(-48 * time.Hour) // before the build time of the agent
	if st := status(t, r.get("/portal/status", "", nil)); st.ClockOK {
		t.Error("clock_ok is true although the clock is earlier than the build")
	}
}

func TestPortalWithoutAnyView(t *testing.T) {
	reg := api.NewRegistry()
	b := portal.NewMinimal(portal.MinimalSources{})
	if err := portal.Register(reg, portal.RouteOptions{Builder: b}); err != nil {
		t.Fatal(err)
	}
	s, err := api.New(api.Options{Registry: reg, Auth: principals, Version: "test", Mode: func() string { return "offline" }})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/portal/status", nil)
	req.Host = "127.0.0.1:7070"
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	apitest.RecordResult(t, req, rec)
	st := status(t, rec)
	if st.Mode != "offline" || st.Session != "none" || st.Capabilities["chat"].Code != doctor.SVC106 || st.Alerts == nil || len(st.Alerts) != 0 || st.Power.State != "no_battery" {
		t.Errorf("status = %+v", st)
	}
	if err := portal.Register(api.NewRegistry(), portal.RouteOptions{}); err == nil {
		t.Error("Register accepted a route without a builder")
	}
}

// TestPortalRedaction: a node full of things a stranger must never see. None of them reaches the
// body: the types have no field for them, and the alert details are dropped.
func TestPortalRedaction(t *testing.T) {
	leaks := []string{
		"/home/example-user", "/srv/localai", "/etc/localai", "/var/lib/localai", "/media/example-drive",
		"198.51.100.7", "example-host.internal", "marker-api-key-value", "marker-session-value",
		"Example Owner Name", "uuid-0000-marker", "https://example-cloud.invalid", "alice@example.test",
		"--api-key", "journal-line-marker",
	}
	yaml := strings.Replace(nodeYAML, `display_name: "Node Three"`, `display_name: "Example Owner Name /home/example-user"`, 1) +
		"cloud:\n  url: \"https://example-cloud.invalid\"\n"
	r := newRig(t, yaml)
	r.hw.Set(hardware.Snapshot{
		Power: batteryPower(72, hardware.SourceBattery, false),
		GPUs:  []hardware.GPU{{Name: "GPU at /home/example-user", Processes: []hardware.GPUProcess{{PID: 1, Name: "/home/example-user/llama-server"}}}},
		Disks: []hardware.Disk{{Path: "/media/example-drive"}},
	})
	r.rt.view = router.View{At: t0, Err: "dial 198.51.100.7: refused", Models: []router.ModelStatus{{
		ID: alpha, Status: router.StatusLoaded, Aliases: []string{"/srv/localai"},
		Args:   []string{"--api-key", "marker-api-key-value", "-m", "/srv/localai/models/" + alpha},
		Preset: "model = /home/example-user/model.gguf\n",
	}}}
	r.alerts.list = []doctor.Alert{{
		Code: doctor.SVC013, Severity: doctor.Info, Title: "x", Fix: "y", Since: t0,
		Details: map[string]any{
			"path": "/media/example-drive/localai", "host": "example-host.internal", "ip": "198.51.100.7",
			"user": "alice@example.test", "uuid": "uuid-0000-marker", "log": "journal-line-marker",
			"token": "marker-session-value",
		},
	}}
	for _, p := range []string{"", "portal", "op"} {
		body := r.get("/portal/status?lang=es", p, nil).Body.String()
		for _, leak := range leaks {
			if strings.Contains(body, leak) {
				t.Errorf("principal %q: the body contains %q:\n%s", p, leak, body)
			}
		}
		// Only the node id identifies the node.
		if !strings.Contains(body, `"node_id":"ai-n3"`) {
			t.Errorf("node id missing: %s", body)
		}
		var keys map[string]json.RawMessage
		if err := json.Unmarshal([]byte(body), &keys); err != nil {
			t.Fatal(err)
		}
		for _, banned := range []string{"hostname", "ip", "paths", "token", "key", "cloud", "logs", "drives"} {
			if _, ok := keys[banned]; ok {
				t.Errorf("top-level key %q", banned)
			}
		}
	}
}

func TestRouteMetadata(t *testing.T) {
	rt := portal.Route(portal.RouteOptions{})
	if rt.Method != http.MethodGet || rt.Pattern != "/portal/status" || len(rt.Auth) != 1 || rt.Auth[0] != api.AuthNone || rt.Timeout != 5*time.Second {
		t.Errorf("route = %+v", rt)
	}
}
