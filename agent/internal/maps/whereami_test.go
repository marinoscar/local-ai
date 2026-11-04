package maps

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/maps/gps"
	"github.com/marinoscar/local-ai/agent/internal/state"
)

func TestParseCoordinates(t *testing.T) {
	tests := []struct {
		in       string
		lat, lon float64
		err      bool
	}{
		{"9.93, -84.08", 9.93, -84.08, false},
		{"9.93,-84.08", 9.93, -84.08, false},
		{"  9.93 -84.08 ", 9.93, -84.08, false},
		{"-33.5; 151.25", -33.5, 151.25, false},
		{`9°55'48"N 84°4'48"W`, 9.93, -84.08, false},
		{`9°55'48"N, 84°4'48"W`, 9.93, -84.08, false},
		{`84°4'48"W 9°55'48"N`, 9.93, -84.08, false}, // either order
		{`9°55′48″ n 84°4′48″ w`, 9.93, -84.08, false},
		{`9°N 84°W`, 9, -84, false},
		{`33°30'S 151°15'E`, -33.5, 151.25, false},
		{"91, 10", 0, 0, true},
		{"10, 181", 0, 0, true},
		{"abc", 0, 0, true},
		{"9.93", 0, 0, true},
		{"", 0, 0, true},
		{`9°55'48"N 84°4'48"N`, 0, 0, true}, // two latitudes
		{`9°60'00"N 84°4'48"W`, 0, 0, true}, // 60 minutes
		{`9°55'48"N 84°4'48"W extra`, 0, 0, true},
		{`91°00'00"N 84°4'48"W`, 0, 0, true},
	}
	for _, tc := range tests {
		lat, lon, err := ParseCoordinates(tc.in)
		if (err != nil) != tc.err {
			t.Errorf("%q: err=%v", tc.in, err)
			continue
		}
		if !tc.err && (abs(lat-tc.lat) > 1e-6 || abs(lon-tc.lon) > 1e-6) {
			t.Errorf("%q: %v, %v", tc.in, lat, lon)
		}
	}
}

func abs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

type memStore struct{ loc *ManualLocation }

func (m *memStore) Get() *ManualLocation { return m.loc }
func (m *memStore) Set(l ManualLocation) error {
	m.loc = &l
	return nil
}
func (m *memStore) Clear() error { m.loc = nil; return nil }

type clk struct{ now time.Time }

func (c *clk) Now() time.Time { return c.now }

var t0 = time.Date(2026, 11, 2, 14, 5, 11, 0, time.UTC)

func newService(c *clk, cfg gps.Config) (*Service, *gps.Tracker, *memStore) {
	tr := gps.NewTracker(c.Now)
	ms := &memStore{}
	return &Service{Tracker: tr, Config: func() gps.Config { return cfg }, Manual: ms, Clock: c.Now}, tr, ms
}

func TestPositionPrecedence(t *testing.T) {
	c := &clk{now: t0}
	svc, tr, ms := newService(c, gps.Config{Mode: gps.ModeAuto})

	if p := svc.Position(); p.Source != SourceNone || p.Lat != nil || p.Code != "" {
		t.Fatalf("nothing: %+v", p)
	}
	ms.loc = &ManualLocation{Lat: 9.5, Lon: -84.5, Label: "Home", SetAt: t0}
	if p := svc.Position(); p.Source != SourceManual || *p.Lat != 9.5 || *p.Label != "Home" {
		t.Fatalf("manual: %+v", p)
	}
	tr.Arm()
	tr.Update(gps.Fix{Lat: 9.93241, Lon: -84.07912, AccuracyM: 8, Mode: 3, At: t0})
	p := svc.Position()
	if p.Source != SourceGPS || *p.Fix != "3d" || *p.AccuracyM != 8 || *p.Lat != 9.93241 || *p.At != "2026-11-02T14:05:11Z" || p.Code != "" {
		t.Fatalf("gps: %+v", p)
	}
	if ok, src := svc.CatalogGPS(); !ok || src != SourceGPS {
		t.Fatal("catalog gps")
	}

	// A stale fix is still the gps source; the UI offers the manual pin.
	c.now = t0.Add(90 * time.Second)
	p = svc.Position()
	if p.Source != SourceGPS || *p.Fix != "stale" || p.Code != "" {
		t.Fatalf("stale: %+v", p)
	}
	if ok, _ := svc.CatalogGPS(); ok {
		t.Fatal("stale gps reported available")
	}
	// Silent for 2 minutes: the code appears.
	c.now = t0.Add(2*time.Minute + time.Second)
	if p = svc.Position(); p.Code != doctor.SVC033 {
		t.Fatalf("silent: %+v", p)
	}
	if r := svc.Check().Run(context.Background()); len(r) != 1 || r[0].Code != doctor.SVC033 || r[0].Severity != doctor.Info {
		t.Fatalf("check: %+v", r)
	}
}

func TestPositionGPSOffIgnoresFix(t *testing.T) {
	c := &clk{now: t0}
	svc, tr, ms := newService(c, gps.Config{Mode: gps.ModeOff})
	tr.Update(gps.Fix{Lat: 1, Lon: 1, Mode: 3, At: t0})
	ms.loc = &ManualLocation{Lat: 2, Lon: 2, SetAt: t0}
	if p := svc.Position(); p.Source != SourceManual {
		t.Fatalf("%+v", p)
	}
	if r := svc.Check().Run(context.Background()); len(r) != 0 {
		t.Fatalf("%+v", r)
	}
}

const origin = "http://127.0.0.1:7070"

func serve(t *testing.T, svc *Service) http.Handler {
	t.Helper()
	h := &Handler{Service: svc, Origins: func() []string { return SameOrigins(7070) }}
	mux := http.NewServeMux()
	for _, rt := range h.Routes() {
		mux.Handle(rt.Method+" "+rt.Pattern, rt.Handler)
	}
	return mux
}

func do(h http.Handler, method, body, org string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "/maps/whereami", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if org != "" {
		req.Header.Set("Origin", org)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestHandler(t *testing.T) {
	c := &clk{now: t0}
	svc, _, ms := newService(c, gps.Config{Mode: gps.ModeAuto})
	h := serve(t, svc)

	tests := []struct {
		name, method, body, origin string
		status                     int
	}{
		{"get needs no origin", "GET", "", "", 200},
		{"put without origin", "PUT", `{"lat":9.93,"lon":-84.08}`, "", 403},
		{"put foreign origin", "PUT", `{"lat":9.93,"lon":-84.08}`, "https://evil.example", 403},
		{"put other port", "PUT", `{"lat":9.93,"lon":-84.08}`, "http://127.0.0.1:8080", 403},
		{"put null origin", "PUT", `{"lat":9.93,"lon":-84.08}`, "null", 403},
		{"delete without origin", "DELETE", "", "", 403},
		{"delete foreign origin", "DELETE", "", "http://localhost:9999", 403},
		{"put ok", "PUT", `{"lat":9.93,"lon":-84.08,"label":"Home"}`, origin, 200},
		{"put localhost", "PUT", `{"lat":9.93,"lon":-84.08}`, "http://localhost:7070", 200},
		{"put desktop", "PUT", `{"lat":9.93,"lon":-84.08}`, "localai://app", 200},
		{"put text decimal", "PUT", `{"text":"9.93, -84.08"}`, origin, 200},
		{"put text dms", "PUT", `{"text":"9°55'48\"N 84°4'48\"W"}`, origin, 200},
		{"put bad text", "PUT", `{"text":"nowhere"}`, origin, 400},
		{"put text and lat", "PUT", `{"text":"9.93, -84.08","lat":1}`, origin, 400},
		{"lat out of range", "PUT", `{"lat":91,"lon":0}`, origin, 400},
		{"lon out of range", "PUT", `{"lat":0,"lon":-181}`, origin, 400},
		{"missing lon", "PUT", `{"lat":0}`, origin, 400},
		{"missing lat", "PUT", `{"lon":0}`, origin, 400},
		{"label too long", "PUT", `{"lat":1,"lon":1,"label":"` + strings.Repeat("x", 81) + `"}`, origin, 400},
		{"label at limit", "PUT", `{"lat":1,"lon":1,"label":"` + strings.Repeat("é", 80) + `"}`, origin, 200},
		{"unknown field", "PUT", `{"lat":1,"lon":1,"altitude":3}`, origin, 400},
		{"not json", "PUT", `nope`, origin, 400},
		{"delete ok", "DELETE", "", origin, 204},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if rec := do(h, tc.method, tc.body, tc.origin); rec.Code != tc.status {
				t.Fatalf("status %d, want %d: %s", rec.Code, tc.status, rec.Body)
			}
		})
	}

	// A rejected request changes nothing.
	ms.loc = nil
	do(h, "PUT", `{"lat":9.93,"lon":-84.08}`, "https://evil.example")
	if ms.loc != nil {
		t.Fatal("a 403 stored a location")
	}
}

func TestHandlerRoundTrip(t *testing.T) {
	c := &clk{now: t0}
	svc, _, _ := newService(c, gps.Config{Mode: gps.ModeAuto})
	h := serve(t, svc)

	rec := do(h, "PUT", `{"lat":9.93,"lon":-84.08,"label":"Home"}`, origin)
	var p map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	if p["source"] != "manual" || p["lat"] != 9.93 || p["lon"] != -84.08 || p["label"] != "Home" || p["at"] != "2026-11-02T14:05:11Z" {
		t.Fatalf("%v", p)
	}
	rec = do(h, "GET", "", "")
	if !strings.Contains(rec.Body.String(), `"source":"manual"`) || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("%s %v", rec.Body, rec.Header())
	}
	do(h, "DELETE", "", origin)
	rec = do(h, "GET", "", "")
	var n map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &n)
	if n["source"] != "none" || n["lat"] != nil || n["label"] != nil {
		t.Fatalf("%v", n)
	}
}

func openStore(t *testing.T, path string) *state.Store {
	t.Helper()
	s, err := state.Open(path, state.Options{})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestStateStorePersistsAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s := openStore(t, path)
	m := StateStore{S: s}
	if m.Get() != nil {
		t.Fatal("fresh state has a location")
	}
	// A newer agent's key inside maps must survive.
	if err := s.MutateSync(func(st *state.State) error {
		st.Maps = json.RawMessage(`{"_owner":"companion-services.md","manual_location":null,"future":{"k":1}}`)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	loc := ManualLocation{Lat: 9.93, Lon: -84.08, Label: "Home", SetAt: t0}
	if err := m.Set(loc); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s2 := openStore(t, path)
	defer s2.Close()
	m2 := StateStore{S: s2}
	got := m2.Get()
	if got == nil || got.Lat != 9.93 || got.Lon != -84.08 || got.Label != "Home" || !got.SetAt.Equal(t0) {
		t.Fatalf("after restart: %+v", got)
	}
	if !strings.Contains(compact(s2.Snapshot().Maps), `"future":{"k":1}`) {
		t.Fatalf("unknown key lost: %s", s2.Snapshot().Maps)
	}
	if err := m2.Clear(); err != nil {
		t.Fatal(err)
	}
	if m2.Get() != nil {
		t.Fatal("clear did not clear")
	}
	if !strings.Contains(compact(s2.Snapshot().Maps), `"manual_location":null`) {
		t.Fatalf("%s", s2.Snapshot().Maps)
	}
}

func TestSameOrigins(t *testing.T) {
	got := strings.Join(SameOrigins(7070), " ")
	if got != "http://127.0.0.1:7070 http://localhost:7070 localai://app" {
		t.Fatal(got)
	}
}

func compact(raw json.RawMessage) string {
	var b bytes.Buffer
	if json.Compact(&b, raw) != nil {
		return string(raw)
	}
	return b.String()
}
