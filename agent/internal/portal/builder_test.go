package portal_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/catalog"
	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/hardware"
	hwfake "github.com/marinoscar/local-ai/agent/internal/hardware/fake"
	"github.com/marinoscar/local-ai/agent/internal/modes"
	"github.com/marinoscar/local-ai/agent/internal/portal"
	"github.com/marinoscar/local-ai/agent/internal/router"
	"github.com/marinoscar/local-ai/agent/internal/services"
	"github.com/marinoscar/local-ai/agent/internal/statusapi"
	"github.com/marinoscar/local-ai/agent/internal/svc/health"
)

const (
	gammaVL = "model-gamma-vl-q4"
	beta    = "model-beta-27b-q4"
)

type fakeModels struct {
	rows  []statusapi.ModelRow
	calls int
}

func (f *fakeModels) Models(string) statusapi.ModelsView {
	f.calls++
	return statusapi.ModelsView{Rows: f.rows, ModelsMax: 2}
}

type fakeHealth struct{ recs []health.Record }

func (f *fakeHealth) Records(context.Context) []health.Record { return f.recs }

type fakeLlama struct{ state string }

func (f *fakeLlama) Snapshot(context.Context) []services.Status {
	return []services.Status{{Name: "llama", Health: f.state}}
}

type fakeArk struct{ s portal.ArkSummary }

func (f *fakeArk) ArkSummary() portal.ArkSummary { return f.s }

type fakeDocs struct{ d portal.DocsInfo }

func (f *fakeDocs) Docs() portal.DocsInfo { return f.d }

func row(id, engine string, roles []string, inProfile, loadable bool) statusapi.ModelRow {
	return statusapi.ModelRow{ID: id, DisplayName: "Name " + id, Summary: "Summary " + id, Engine: engine, Roles: roles, InProfile: inProfile, Loadable: loadable}
}

// fullRig is a builder over a complete fake node (profile small-8gb: alpha and gamma-vl are survival models).
type fullRig struct {
	b         *portal.Builder
	models    *fakeModels
	rt        *fakeRouter
	hw        *hwfake.Sampler
	health    *fakeHealth
	llama     *fakeLlama
	ark       *fakeArk
	docs      *fakeDocs
	alerts    *fakeAlerts
	cfg       *config.Store
	cat       *catalog.Catalog
	buildTime time.Time
}

func newFullRig(t *testing.T) *fullRig {
	t.Helper()
	r := &fullRig{cat: loadCatalog(t), buildTime: t0.Add(-24 * time.Hour)}
	r.cfg = openConfig(t, nodeYAML)
	r.hw = hwfake.New(hardware.Snapshot{Power: batteryPower(72, hardware.SourceBattery, false)})
	r.models = &fakeModels{rows: []statusapi.ModelRow{
		row(beta, "llama", []string{"reasoning"}, true, false),
		row(alpha, "llama", []string{"chat"}, true, true),
		row(gammaVL, "llama", []string{"vision", "chat"}, true, true),
		row("model-delta-embed", "llama", []string{"embedding"}, false, true),
		row("whisper-small", "whisper", []string{"stt"}, true, true),
	}}
	r.rt = &fakeRouter{view: router.View{At: t0, Models: []router.ModelStatus{{ID: alpha, Status: router.StatusLoaded}}}}
	r.health = &fakeHealth{recs: []health.Record{
		{Name: "kiwix", State: health.StateOK, Books: []health.Book{{ContentID: "wiki-alpha-en", Name: "wiki_alpha_en", Root: "/srv/localai"}}},
		{Name: "whisper", State: health.StateOK},
		{Name: "piper", State: health.StateOK, Voices: []string{"en_US-name-medium", "es_MX-name-medium"}},
		{Name: "maps", State: health.StateOK, Maps: []string{"region"}},
	}}
	r.llama = &fakeLlama{state: "ok"}
	r.ark = &fakeArk{s: portal.ArkSummary{Overall: "yellow", SurvivalCapable: true, DriveConnected: true}}
	r.docs = &fakeDocs{d: portal.DocsInfo{Docs: []portal.Doc{
		{Slug: "01-survival-mode", Title: "Survival mode", TitleES: "Modo supervivencia", HTML: "/docs/01-survival-mode.html"},
		{Slug: "bad-link", Title: "Bad", HTML: "/etc/passwd"},
		{Slug: "traversal", Title: "Bad", HTML: "/docs/../secret.html"},
	}}}
	r.alerts = &fakeAlerts{}
	store := statusapi.NewCatalogStore(catalog.Loaded{Catalog: r.cat, Source: catalog.SourceOffline}, t0)
	r.b = portal.NewBuilder(portal.Sources{
		Config: r.cfg, Mode: &fakeMode{info: modes.Info{Mode: modes.Survival, Effective: modes.Survival}}, Hardware: r.hw,
		Router: r.rt, Catalog: store, Alerts: r.alerts, Models: r.models, Health: r.health, Llama: r.llama, Ark: r.ark, Docs: r.docs,
		Settings:  func() portal.Settings { return portal.SettingsFrom(r.cfg.Get(), r.cat) },
		BuildTime: r.buildTime,
	})
	return r
}

func (r *fullRig) build(t *testing.T, lang string) portal.Status {
	t.Helper()
	st, err := r.b.Build(context.Background(), portal.Request{Lang: lang, Session: portal.SessionPortal, Now: t0})
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func (r *fullRig) record(name string) *health.Record {
	for i := range r.health.recs {
		if r.health.recs[i].Name == name {
			return &r.health.recs[i]
		}
	}
	return nil
}

func TestBuilderHealthyNode(t *testing.T) {
	r := newFullRig(t)
	r.rt.view.Models[0].Status = router.StatusLoaded
	st := r.build(t, "es")
	if st.SchemaVersion != 1 || st.NodeID != "ai-n3" || st.Mode != "survival" || st.Language != "es" || st.EmergencyNumber != "112" || st.Session != "portal" {
		t.Errorf("header = %+v", st)
	}
	c := st.Capabilities
	if c.Chat.State != portal.StateReady || c.Talk.State != portal.StateReady || c.Library.State != portal.StateReady ||
		c.Maps.State != portal.StateReady || c.Manuals.State != portal.StateReady {
		t.Errorf("capabilities = %+v", c)
	}
	// alpha has no vision: a vision model is loadable, so photo needs a switch.
	if c.Photo.State != portal.StateDegraded || c.Photo.Reason != "needs_switch" || c.Photo.VisionModel != gammaVL || c.Photo.Code != "" {
		t.Errorf("photo = %+v", c.Photo)
	}
	if !reflectEqual(c.Talk.TTSLanguage, []string{"en", "es"}) || !c.Talk.STT {
		t.Errorf("talk = %+v", c.Talk)
	}
	if len(c.Library.Books) != 1 || c.Library.Books[0] != (portal.Book{ContentID: "wiki-alpha-en", Title: c.Library.Books[0].Title, Book: "wiki_alpha_en", Lang: c.Library.Books[0].Lang}) {
		t.Errorf("books = %+v", c.Library.Books)
	}
	if len(c.Manuals.Items) != 1 || c.Manuals.Items[0] != (portal.Manual{ID: "01-survival-mode", Title: "Modo supervivencia", URL: "/docs/01-survival-mode.html"}) {
		t.Errorf("manuals = %+v (links outside /docs/ and traversal must be dropped; the title is localized)", c.Manuals.Items)
	}
	if st.Ark != (portal.Ark{Overall: "yellow", SurvivalCapable: true, DriveConnected: true}) {
		t.Errorf("ark = %+v", st.Ark)
	}
	if !st.ClockOK || st.FirstAid == nil || len(st.FirstAid) != 0 {
		t.Errorf("clock_ok %v first_aid %v", st.ClockOK, st.FirstAid)
	}
}

func reflectEqual(a, b []string) bool {
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

func TestBuilderVisionLoaded(t *testing.T) {
	r := newFullRig(t)
	r.rt.view.Models = []router.ModelStatus{{ID: gammaVL, Status: router.StatusLoaded}}
	st := r.build(t, "en")
	if st.Capabilities.Photo.State != portal.StateReady || st.Capabilities.Photo.VisionModel != "" {
		t.Errorf("photo = %+v", st.Capabilities.Photo)
	}
	if len(st.Brain.Loaded) != 1 || !st.Brain.Loaded[0].Vision {
		t.Errorf("loaded = %+v", st.Brain.Loaded)
	}
}

func TestBuilderNoVisionModelLoadable(t *testing.T) {
	r := newFullRig(t)
	r.models.rows[2].Loadable = false
	st := r.build(t, "en")
	if p := st.Capabilities.Photo; p.State != portal.StateUnavailable || p.Code != doctor.SVC040 {
		t.Errorf("photo = %+v", p)
	}
}

func TestBuilderChoices(t *testing.T) {
	r := newFullRig(t)
	st := r.build(t, "es")
	var ids []string
	for _, c := range st.Brain.Choices {
		ids = append(ids, c.ID)
	}
	// Only llama models of the profile; survival ones (alpha, gamma-vl) first, then the rest in catalog order.
	if !reflectEqual(ids, []string{alpha, gammaVL, beta}) {
		t.Fatalf("choices = %v", ids)
	}
	if c := st.Brain.Choices[1]; !c.Vision || !c.Loadable || !c.Survival || c.Summary != "Summary "+gammaVL {
		t.Errorf("gamma = %+v", c)
	}
	if c := st.Brain.Choices[2]; c.Survival || c.Loadable {
		t.Errorf("beta = %+v", c)
	}
}

func TestBuilderServiceStates(t *testing.T) {
	cases := []struct {
		name string
		mod  func(*fullRig)
		get  func(portal.Capabilities) (string, string)
		want want
	}{
		{"kiwix down", func(r *fullRig) { r.record("kiwix").State = health.StateDown },
			func(c portal.Capabilities) (string, string) { return c.Library.State, c.Library.Code }, want{portal.StateUnavailable, doctor.SVC002, ""}},
		{"no books", func(r *fullRig) { r.record("kiwix").State, r.record("kiwix").Books = health.StateMissingContent, nil },
			func(c portal.Capabilities) (string, string) { return c.Library.State, c.Library.Code }, want{portal.StateUnavailable, doctor.SVC001, ""}},
		{"whisper missing", func(r *fullRig) { r.record("whisper").State = health.StateMissingContent },
			func(c portal.Capabilities) (string, string) { return c.Talk.State, c.Talk.Code }, want{portal.StateUnavailable, doctor.SVC010, ""}},
		{"no voice", func(r *fullRig) { r.record("piper").Voices = []string{} },
			func(c portal.Capabilities) (string, string) { return c.Talk.State, c.Talk.Code }, want{portal.StateDegraded, doctor.SVC022, ""}},
		{"map assets missing", func(r *fullRig) {
			*r.record("maps") = health.Record{Name: "maps", State: health.StateDegraded, Maps: []string{"region"}, Problems: []health.Problem{{Code: doctor.SVC031}}}
		}, func(c portal.Capabilities) (string, string) { return c.Maps.State, c.Maps.Code }, want{portal.StateDegraded, doctor.SVC031, ""}},
		{"no map", func(r *fullRig) { r.record("maps").Maps = nil },
			func(c portal.Capabilities) (string, string) { return c.Maps.State, c.Maps.Code }, want{portal.StateUnavailable, doctor.SVC030, ""}},
		{"llama down", func(r *fullRig) { r.llama.state = "down" },
			func(c portal.Capabilities) (string, string) { return c.Chat.State, c.Chat.Code }, want{portal.StateUnavailable, doctor.SVC102, ""}},
		{"docs without inventory", func(r *fullRig) { r.docs.d.NoInventory = true },
			func(c portal.Capabilities) (string, string) { return c.Manuals.State, c.Manuals.Code }, want{portal.StateDegraded, doctor.SYN062, ""}},
		{"no docs", func(r *fullRig) { r.docs.d.Docs = nil },
			func(c portal.Capabilities) (string, string) { return c.Manuals.State, c.Manuals.Code }, want{portal.StateUnavailable, "", ""}},
		{"nothing loaded", func(r *fullRig) { r.rt.view.Models = nil },
			func(c portal.Capabilities) (string, string) { return c.Chat.State, c.Chat.Code }, want{portal.StateUnavailable, doctor.SVC106, ""}},
		{"loading", func(r *fullRig) { r.rt.view.Models[0].Status = router.StatusLoading },
			func(c portal.Capabilities) (string, string) { return c.Chat.State, c.Chat.Code }, want{portal.StateDegraded, "", ""}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newFullRig(t)
			c.mod(r)
			state, code := c.get(r.build(t, "en").Capabilities)
			if state != c.want.state || code != c.want.code {
				t.Errorf("got %s %s, want %+v", state, code, c.want)
			}
		})
	}
}

func TestBuilderClockOK(t *testing.T) {
	cases := []struct {
		name  string
		now   time.Time
		build time.Time
		ark   portal.ArkSummary
		want  bool
	}{
		{"fine", t0, t0.Add(-time.Hour), portal.ArkSummary{NewestVerifiedAt: t0.Add(-time.Minute)}, true},
		{"before the build", t0, t0.Add(time.Hour), portal.ArkSummary{}, false},
		{"before the newest verified_at", t0, t0.Add(-time.Hour), portal.ArkSummary{NewestVerifiedAt: t0.Add(time.Second)}, false},
		{"READY.json says the clock is wrong", t0, t0.Add(-time.Hour), portal.ArkSummary{ClockWrong: true}, false},
		{"no build time, no reports", t0, time.Time{}, portal.ArkSummary{}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newFullRig(t)
			r.ark.s = c.ark
			b := portal.NewBuilder(portal.Sources{Ark: r.ark, BuildTime: c.build})
			st, err := b.Build(context.Background(), portal.Request{Lang: "en", Now: c.now})
			if err != nil || st.ClockOK != c.want {
				t.Errorf("clock_ok = %v, want %v (%v)", st.ClockOK, c.want, err)
			}
		})
	}
}

func alertCodes(st portal.Status) []string {
	var out []string
	for _, a := range st.Alerts {
		out = append(out, a.Code)
	}
	return out
}

func TestBuilderAlerts(t *testing.T) {
	cases := []struct {
		name    string
		pct     int
		backend string
		listed  []doctor.Alert
		want    []string
	}{
		{"healthy", 72, "cuda", nil, []string{}},
		{"low battery", 20, "cuda", nil, []string{doctor.SVC111}},
		{"very low battery", 10, "cuda", nil, []string{doctor.SVC112}},
		{"no gpu", 72, "cpu", nil, []string{doctor.SVC113}},
		{"listed alert is not repeated", 15, "cpu", []doctor.Alert{{Code: doctor.SVC111, Severity: doctor.Warning}}, []string{doctor.SVC111, doctor.SVC113}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newFullRig(t)
			r.hw.Set(hardware.Snapshot{Power: batteryPower(c.pct, hardware.SourceBattery, false)})
			r.alerts.list = c.listed
			yaml := strings.Replace(nodeYAML, "profile: small-8gb\n", "profile: small-8gb\nllama:\n  backend: "+c.backend+"\n", 1)
			r.cfg = openConfig(t, yaml)
			store := statusapi.NewCatalogStore(catalog.Loaded{Catalog: r.cat, Source: catalog.SourceOffline}, t0)
			b := portal.NewBuilder(portal.Sources{Config: r.cfg, Hardware: r.hw, Alerts: r.alerts, Catalog: store})
			st, err := b.Build(context.Background(), portal.Request{Lang: "en", Now: t0})
			if err != nil {
				t.Fatal(err)
			}
			if got := alertCodes(st); !reflectEqual(got, c.want) {
				t.Errorf("alerts = %v, want %v", got, c.want)
			}
		})
	}
}

// TestBuilderAlertLanguage: titles and fixes follow the language, codes never change.
func TestBuilderAlertLanguage(t *testing.T) {
	r := newFullRig(t)
	r.hw.Set(hardware.Snapshot{Power: batteryPower(5, hardware.SourceBattery, false)})
	en, es := r.build(t, "en"), r.build(t, "es")
	if len(en.Alerts) == 0 || len(es.Alerts) != len(en.Alerts) {
		t.Fatalf("alerts en %v es %v", en.Alerts, es.Alerts)
	}
	if en.Alerts[0].Code != es.Alerts[0].Code || en.Alerts[0].Title == es.Alerts[0].Title || en.Alerts[0].Fix == es.Alerts[0].Fix {
		t.Errorf("en %+v es %+v", en.Alerts[0], es.Alerts[0])
	}
	if en.Alerts[0].Title != "Battery very low" || en.Alerts[0].Severity != "critical" {
		t.Errorf("en alert = %+v", en.Alerts[0])
	}
}

func TestBuilderModeFallback(t *testing.T) {
	st, err := portal.NewBuilder(portal.Sources{}).Build(context.Background(), portal.Request{Lang: "en", Now: t0})
	if err != nil || st.Mode != "offline" || st.Ark.Overall != "unknown" {
		t.Errorf("status = %+v err %v", st, err)
	}
}

// TestBuilderRedaction: a fully populated fake node, full of paths, addresses and tokens in every
// source, never leaks them, in either language.
func TestBuilderRedaction(t *testing.T) {
	r := newFullRig(t)
	r.hw.Set(hardware.Snapshot{
		Power: batteryPower(5, hardware.SourceBattery, false),
		GPUs:  []hardware.GPU{{Name: "GPU /home/example-user", Processes: []hardware.GPUProcess{{PID: 1, Name: "/var/lib/localai/llama"}}}},
	})
	r.record("kiwix").Books[0].Root = "/media/example-drive/localai"
	r.record("kiwix").Detail = "198.51.100.7 /srv/localai"
	r.record("whisper").Model, r.record("whisper").Device = "/srv/localai/whisper.bin", "/dev/dri"
	r.record("maps").Maps = []string{"/srv/localai/maps/region.pmtiles"}
	r.record("piper").Voices = []string{"en_US-name-medium", "/etc/localai/voice.onnx"}
	r.alerts.list = []doctor.Alert{{Code: doctor.SVC013, Severity: doctor.Info, Title: "x", Fix: "y", Details: map[string]any{
		"path": "/home/example-user/x", "ip": "100.64" + ".0.9", "token": "nod" + "_0123456789abcdef", "key": "lai" + "_0123456789abcdef",
	}}}
	r.rt.view.Models[0].Args = []string{"--api-key", "marker-api-" + "key-value", "-m", "/srv/localai/models/x"}
	patterns := []*regexp.Regexp{
		regexp.MustCompile(`\b[0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,3}\b`),
		regexp.MustCompile(`\b100\.[0-9]+\.[0-9]+\.[0-9]+\b`),
		regexp.MustCompile(`lat_`), regexp.MustCompile(`nod_`), regexp.MustCompile(`pat_`), regexp.MustCompile(`lai_[0-9a-f]`),
		regexp.MustCompile(`\b[0-9a-fA-F]{64}\b`),
		regexp.MustCompile(`"/(srv|etc|var|media|home|dev)`), regexp.MustCompile(`[ "](/srv|/etc|/var|/media|/home)/`),
		regexp.MustCompile(`marker-api-key-value|example-user|example-drive`),
	}
	for _, lang := range []string{"en", "es"} {
		b, err := json.Marshal(r.build(t, lang))
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range patterns {
			if loc := p.FindIndex(b); loc != nil {
				t.Errorf("lang %s: the body matches %s near %q", lang, p, b[max(0, loc[0]-20):min(len(b), loc[1]+20)])
			}
		}
	}
}

func TestFileDocs(t *testing.T) {
	root := t.TempDir()
	empty := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	idx := `{"root":"` + root + `","docs":[{"slug":"a","title":"A","title_es":"Aes","html":"/docs/a.html"},{"slug":"b","title":"B","title_es":null,"html":"/docs/b.html"}]}`
	if err := os.WriteFile(filepath.Join(root, "docs", "index.json"), []byte(idx), 0o644); err != nil {
		t.Fatal(err)
	}
	f := portal.FileDocs{Roots: func() []string { return []string{empty, root} }, NoInventory: func() bool { return true }}
	got := f.Docs()
	if len(got.Docs) != 2 || got.Docs[0].TitleES != "Aes" || got.Docs[1].TitleES != "" || !got.NoInventory {
		t.Errorf("docs = %+v", got)
	}
	if got := (portal.FileDocs{}).Docs(); len(got.Docs) != 0 {
		t.Errorf("nil roots = %+v", got)
	}
	if err := os.WriteFile(filepath.Join(root, "docs", "index.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := f.Docs(); len(got.Docs) != 0 {
		t.Errorf("corrupt index = %+v", got)
	}
}

func TestParseConfig(t *testing.T) {
	cases := []struct {
		name    string
		yaml    string
		want    portal.Config
		badPath string
	}{
		{"empty is the defaults", "", portal.Config{Language: "auto", EmergencyNumber: "911"}, ""},
		{"all fields", "language: es\nemergency_number: \"112\"\nkiosk: true\n", portal.Config{Language: "es", EmergencyNumber: "112", Kiosk: true}, ""},
		{"partial keeps defaults", "kiosk: true\n", portal.Config{Language: "auto", EmergencyNumber: "911", Kiosk: true}, ""},
		{"bad language", "language: fr\n", portal.Config{}, "language"},
		{"bad number", "emergency_number: \"call <b>me</b>\"\n", portal.Config{}, "emergency_number"},
		{"empty number", "emergency_number: \"\"\n", portal.Config{}, "emergency_number"},
		{"unknown key", "volume: 11\n", portal.Config{}, ""},
		{"not a mapping", "- a\n- b\n", portal.Config{}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, probs := portal.ParseConfig(config.RawBlock(c.yaml))
			bad := c.badPath != "" || strings.HasPrefix(c.name, "unknown") || strings.HasPrefix(c.name, "not a")
			if bad != probs.HasErrors() {
				t.Fatalf("problems = %+v", probs)
			}
			if !bad {
				if got != c.want {
					t.Errorf("config = %+v, want %+v", got, c.want)
				}
				return
			}
			for _, p := range probs {
				if p.Code != doctor.AGT002 || p.Severity != config.SeverityError {
					t.Errorf("problem = %+v, want LAI-AGT-002 error", p)
				}
				if c.badPath != "" && p.Path != c.badPath {
					t.Errorf("path = %q, want %q", p.Path, c.badPath)
				}
			}
		})
	}
	if probs := (portal.BlockValidator{}).ValidateBlock(config.RawBlock("language: xx\n")); !probs.HasErrors() {
		t.Error("BlockValidator accepted language xx")
	}
}

func TestMetrics(t *testing.T) {
	var m *portal.Metrics
	m.StatusRequest("none") // a nil Metrics ignores every call
	m.RateLimited("stt")
	m = portal.NewMetrics()
	m.StatusRequest("portal")
	m.StatusRequest("portal")
	m.StatusRequest("none")
	m.RateLimited("load")
	m.SetSessionsGauge(func() int { return 3 })
	got := m.Counters()
	want := map[string]any{
		`localai_portal_status_requests_total{session="portal"}`: int64(2),
		`localai_portal_status_requests_total{session="none"}`:   int64(1),
		`localai_portal_rate_limited_total{bucket="load"}`:       int64(1),
		"localai_portal_sessions_active":                         3,
	}
	if len(got) != len(want) {
		t.Fatalf("counters = %v", got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %v, want %v", k, got[k], v)
		}
	}
}
