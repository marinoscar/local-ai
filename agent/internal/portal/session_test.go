package portal_test

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/api"
	"github.com/marinoscar/local-ai/agent/internal/portal"
)

// mode is a settable mode name.
type mode struct {
	mu sync.Mutex
	m  string
}

func (m *mode) get() string  { m.mu.Lock(); defer m.mu.Unlock(); return m.m }
func (m *mode) set(v string) { m.mu.Lock(); m.m = v; m.mu.Unlock() }

// zeroRand is a deterministic random source.
type zeroRand struct{ n byte }

func (z *zeroRand) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = z.n
		z.n++
	}
	return len(p), nil
}

func newSessions(t testing.TB, m *mode, clk *clock) (*portal.Sessions, *bytes.Buffer, *portal.Metrics) {
	t.Helper()
	var logs bytes.Buffer
	met := portal.NewMetrics()
	s := portal.NewSessions(portal.SessionOptions{
		Mode: m.get, Now: clk.Now, Rand: &zeroRand{}, Metrics: met,
		Logger: slog.New(slog.NewJSONHandler(&logs, nil)),
	})
	return s, &logs, met
}

var cookieRe = regexp.MustCompile(`^lai_portal=([A-Za-z0-9_-]{43}); Path=/; HttpOnly; SameSite=Strict; Max-Age=43200$`)

func hookCookie(t testing.TB, s *portal.Sessions, req *http.Request) (header, value string) {
	t.Helper()
	rec := httptest.NewRecorder()
	s.Hook(rec, req)
	got := rec.Header().Values("Set-Cookie")
	if len(got) == 0 {
		return "", ""
	}
	if len(got) != 1 {
		t.Fatalf("Set-Cookie = %v", got)
	}
	m := cookieRe.FindStringSubmatch(got[0])
	if m == nil {
		t.Fatalf("Set-Cookie = %q, not the exact cookie of survival-portal.md section 3.3", got[0])
	}
	return got[0], m[1]
}

func TestSessionIssuance(t *testing.T) {
	cases := []struct {
		name   string
		mode   string
		cookie func(*portal.Sessions, *clock) string // an existing cookie to send
		issued bool
	}{
		{"survival, no cookie", "survival", nil, true},
		{"offline: nothing", "offline", nil, false},
		{"online: nothing", "online", nil, false},
		{"survival, a valid cookie is kept", "survival", func(s *portal.Sessions, _ *clock) string { c, _ := s.Issue(); return c }, false},
		{"survival, an unknown cookie gets a new one", "survival", func(*portal.Sessions, *clock) string { return "not-a-session" }, true},
		{"survival, an expired cookie gets a new one", "survival", func(s *portal.Sessions, c *clock) string {
			v, _ := s.Issue()
			c.Advance(portal.SessionTTL + time.Second)
			return v
		}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m, clk := &mode{m: "survival"}, &clock{t: t0}
			s, logs, _ := newSessions(t, m, clk)
			req := httptest.NewRequest(http.MethodGet, "/portal", nil)
			if c.cookie != nil {
				req.AddCookie(&http.Cookie{Name: portal.Cookie, Value: c.cookie(s, clk)})
			}
			m.set(c.mode)
			header, value := hookCookie(t, s, req)
			if (header != "") != c.issued {
				t.Fatalf("issued = %v, want %v", header != "", c.issued)
			}
			if c.issued {
				if _, ok := s.Lookup(value); !ok {
					t.Error("the new cookie does not resolve")
				}
				if strings.Contains(logs.String(), value) {
					t.Errorf("the log holds the cookie value: %s", logs.String())
				}
				if !strings.Contains(logs.String(), "portal.session_issued") {
					t.Errorf("portal.session_issued not logged: %s", logs.String())
				}
			}
		})
	}
}

func TestSessionSlidingExpiry(t *testing.T) {
	m, clk := &mode{m: "survival"}, &clock{t: t0}
	s, _, _ := newSessions(t, m, clk)
	c, err := s.Issue()
	if err != nil {
		t.Fatal(err)
	}
	// Used every 11 hours: it lives on, however old it is.
	for i := 0; i < 5; i++ {
		clk.Advance(11 * time.Hour)
		if _, ok := s.Lookup(c); !ok {
			t.Fatalf("session gone after %d uses", i+1)
		}
	}
	// Idle for 11 h 59 min: still there; one minute more and it is not.
	clk.Advance(12*time.Hour - time.Minute)
	if _, ok := s.Lookup(c); !ok {
		t.Fatal("expired one minute early")
	}
	clk.Advance(12 * time.Hour)
	if _, ok := s.Lookup(c); ok {
		t.Fatal("still valid after 12 idle hours")
	}
	if n := s.Count(); n != 0 {
		t.Errorf("Count = %d after expiry", n)
	}
}

func TestSessionLookup(t *testing.T) {
	m, clk := &mode{m: "survival"}, &clock{t: t0}
	s, _, met := newSessions(t, m, clk)
	a, _ := s.Issue()
	b, _ := s.Issue()
	if a == b || len(a) != 43 || len(b) != 43 {
		t.Fatalf("cookies %q %q", a, b)
	}
	ida, ok := s.Lookup(a)
	idb, _ := s.Lookup(b)
	if !ok || ida == idb || strings.Contains(a, ida) || ida == "" {
		t.Errorf("ids %q %q for cookies %q %q", ida, idb, a, b)
	}
	for _, bad := range []string{"", "x", strings.Repeat("a", 43), strings.Repeat("a", 500)} {
		if _, ok := s.Lookup(bad); ok {
			t.Errorf("Lookup(%q) succeeded", bad)
		}
	}
	if !s.Allowed(nil) {
		t.Error("Allowed false in survival")
	}
	if got := met.Counters()["localai_portal_sessions_active"]; got != 2 {
		t.Errorf("sessions_active = %v, want 2", got)
	}
	// Outside survival a cookie resolves to nothing, and Allowed says no.
	m.set("offline")
	if _, ok := s.Lookup(a); ok || s.Allowed(nil) {
		t.Error("a session works outside survival")
	}
}

func TestSessionInvalidateAll(t *testing.T) {
	m, clk := &mode{m: "survival"}, &clock{t: t0}
	s, _, _ := newSessions(t, m, clk)
	a, _ := s.Issue()
	b, _ := s.Issue()
	s.InvalidateAll()
	m.set("survival") // even back in survival the old cookies are gone
	for _, c := range []string{a, b} {
		if _, ok := s.Lookup(c); ok {
			t.Error("a session survived InvalidateAll")
		}
	}
	if s.Count() != 0 {
		t.Error("Count != 0")
	}
}

// TestHookBehindTheServer: through the real SPA route and server, a bad Host never reaches the hook.
func TestHookBehindTheServer(t *testing.T) {
	m, clk := &mode{m: "survival"}, &clock{t: t0}
	s, _, _ := newSessions(t, m, clk)
	reg := api.NewRegistry()
	files := fstest.MapFS{"index.html": {Data: []byte("<html>Dashboard</html>")}, "portal.html": {Data: []byte("<html>Portal</html>")}}
	if err := reg.Add(api.SPARoutes(api.SPAOptions{FS: files, Mode: m.get, PortalHook: s.Hook})...); err != nil {
		t.Fatal(err)
	}
	srv, err := api.New(api.Options{Registry: reg, Version: "test", Mode: m.get})
	if err != nil {
		t.Fatal(err)
	}
	do := func(host, target string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, target, nil)
		req.Host = host
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		return rec
	}
	if rec := do("127.0.0.1:7070", "/portal"); rec.Code != 200 || len(rec.Header().Values("Set-Cookie")) != 1 {
		t.Errorf("loopback: %d %v", rec.Code, rec.Header())
	}
	if rec := do("evil.example", "/portal"); rec.Code == 200 || len(rec.Header().Values("Set-Cookie")) != 0 {
		t.Errorf("foreign Host: %d %v (no cookie may be set)", rec.Code, rec.Header())
	}
	if rec := do("127.0.0.1:7070", "/portal/assets/x.js"); len(rec.Header().Values("Set-Cookie")) != 0 {
		t.Error("an asset request issued a cookie")
	}
	m.set("offline")
	if rec := do("127.0.0.1:7070", "/portal"); rec.Code != 200 || len(rec.Header().Values("Set-Cookie")) != 0 {
		t.Errorf("offline: %d %v", rec.Code, rec.Header())
	}
}

var _ io.Reader = (*zeroRand)(nil)
