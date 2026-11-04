package kiwix

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/api"
)

var quietLog = slog.New(slog.NewTextHandler(io.Discard, nil))

// upstream is a fake kiwix-serve that records the requests it gets.
type upstream struct {
	srv  *httptest.Server
	mu   sync.Mutex
	reqs []*http.Request
}

func newUpstream(t *testing.T, h http.HandlerFunc) *upstream {
	t.Helper()
	u := &upstream{}
	u.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u.mu.Lock()
		u.reqs = append(u.reqs, r)
		u.mu.Unlock()
		h(w, r)
	}))
	t.Cleanup(u.srv.Close)
	return u
}

func (u *upstream) port() int {
	pu, _ := url.Parse(u.srv.URL)
	n, _ := strconv.Atoi(pu.Port())
	return n
}

func (u *upstream) last(t *testing.T) *http.Request {
	t.Helper()
	u.mu.Lock()
	defer u.mu.Unlock()
	if len(u.reqs) == 0 {
		t.Fatal("the upstream got no request")
	}
	return u.reqs[len(u.reqs)-1]
}

func (u *upstream) count() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return len(u.reqs)
}

// serve runs h on a request carrying a RequestInfo like the server's middleware does.
func serve(h http.Handler, r *http.Request) *httptest.ResponseRecorder {
	r = r.WithContext(api.WithRequestInfo(r.Context(), &api.RequestInfo{ID: "req_abcdefghijkl", Lang: "en", MaxBody: api.DefaultMaxBody}))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func get(path string, hdr map[string]string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, path, nil)
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	return r
}

func TestProxyPreservesPathAndQuery(t *testing.T) {
	up := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, "ok")
	})
	h := NewProxy(ProxyOptions{Port: up.port(), Logger: quietLog})
	for _, tc := range []struct{ in, path, query string }{
		{"/library/search?books.name=mdwiki_en_all&pattern=water&pageLength=1", "/library/search", "books.name=mdwiki_en_all&pattern=water&pageLength=1"},
		{"/library/content/wikipedia_en_all_nopic/A/Water", "/library/content/wikipedia_en_all_nopic/A/Water", ""},
		{"/library/", "/library/", ""},
		{"/library/catalog/v2/entries?count=1", "/library/catalog/v2/entries", "count=1"},
	} {
		w := serve(h, get(tc.in, nil))
		if w.Code != 200 {
			t.Fatalf("%s: %d", tc.in, w.Code)
		}
		if got := up.last(t); got.URL.Path != tc.path || got.URL.RawQuery != tc.query {
			t.Fatalf("%s: upstream saw %s?%s", tc.in, got.URL.Path, got.URL.RawQuery)
		}
	}
}

func TestProxyHeaders(t *testing.T) {
	up := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {})
	h := NewProxy(ProxyOptions{Port: up.port(), Logger: quietLog})
	r := get("/library/x", map[string]string{
		"X-Forwarded-For": "203.0.113.9", "X-Forwarded-Host": "evil.example", "X-Forwarded-Proto": "https",
		"Connection": "keep-alive, X-Hop", "X-Hop": "1", "Keep-Alive": "timeout=5", "Cookie": "lai_session=abc", "Authorization": "Bearer x",
	})
	if w := serve(h, r); w.Code != 200 {
		t.Fatalf("status %d", w.Code)
	}
	got := up.last(t)
	for _, name := range []string{"X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto", "X-Forwarded-Prefix", "X-Hop", "Keep-Alive", "Cookie", "Authorization"} {
		if v := got.Header.Get(name); v != "" {
			t.Errorf("the upstream saw %s: %q", name, v)
		}
	}
	for name := range got.Header {
		if strings.HasPrefix(name, "X-Forwarded") {
			t.Errorf("the proxy added %s", name)
		}
	}
	if got.Host != "127.0.0.1:"+strconv.Itoa(up.port()) {
		t.Errorf("Host %q", got.Host)
	}
}

func TestProxyCSPOnHTMLOnly(t *testing.T) {
	tests := []struct {
		name, ctype string
		wantCSP     bool
	}{
		{"html", "text/html", true},
		{"html with charset", "text/html; charset=utf-8", true},
		{"html upper case", "Text/HTML", true},
		{"xhtml is not html", "application/xhtml+xml", false},
		{"image", "image/png", false},
		{"javascript", "application/javascript", false},
		{"css", "text/css", false},
		{"json", "application/json", false},
		{"none", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			up := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
				if tc.ctype != "" {
					w.Header().Set("Content-Type", tc.ctype)
				} else {
					w.Header()["Content-Type"] = nil
				}
				_, _ = io.WriteString(w, `<html><img src="https://example.invalid/x.png"><script src="https://example.invalid/x.js"></script></html>`)
			})
			w := serve(NewProxy(ProxyOptions{Port: up.port(), Logger: quietLog}), get("/library/content/b/A/page", nil))
			csp := w.Header().Get("Content-Security-Policy")
			if tc.wantCSP && csp != "default-src 'self' 'unsafe-inline' data:; connect-src 'self'" {
				t.Fatalf("CSP %q", csp)
			}
			if !tc.wantCSP && csp != "" {
				t.Fatalf("a %q response got a CSP: %q", tc.ctype, csp)
			}
			if w.Header().Get("X-Content-Type-Options") != "nosniff" {
				t.Fatal("nosniff missing")
			}
			if tc.wantCSP && (strings.Contains(csp, "http") || strings.Contains(csp, "*")) {
				t.Fatalf("the policy allows a remote origin: %q", csp)
			}
		})
	}
}

func TestProxyOnlyGetAndHead(t *testing.T) {
	up := newUpstream(t, func(w http.ResponseWriter, r *http.Request) { w.Header().Set("Content-Type", "text/html") })
	h := NewProxy(ProxyOptions{Port: up.port(), Logger: quietLog})
	for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		before := up.count()
		w := serve(h, httptest.NewRequest(m, "/library/x", strings.NewReader("{}")))
		if w.Code != http.StatusMethodNotAllowed || up.count() != before {
			t.Errorf("%s: %d, upstream calls %d", m, w.Code, up.count()-before)
		}
		if a := w.Header().Get("Allow"); !strings.Contains(a, "GET") || !strings.Contains(a, "HEAD") {
			t.Errorf("%s: Allow %q", m, a)
		}
	}
	if w := serve(h, httptest.NewRequest(http.MethodHead, "/library/x", nil)); w.Code != 200 {
		t.Errorf("HEAD: %d", w.Code)
	}
}

// closedPort returns a loopback port nothing listens on.
func closedPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return p
}

func TestProxyDownPageLanguage(t *testing.T) {
	h := NewProxy(ProxyOptions{Port: closedPort(t), Logger: quietLog})
	tests := []struct {
		name    string
		hdr     map[string]string
		wantES  bool
		wantSub string
	}{
		{"no hint: english", nil, false, "library service"},
		{"ui header es", map[string]string{LanguageHeader: "es"}, true, "El servicio de la biblioteca"},
		{"ui header en beats Accept-Language es", map[string]string{LanguageHeader: "en", "Accept-Language": "es-CR,es;q=0.9"}, false, "library service"},
		{"Accept-Language es", map[string]string{"Accept-Language": "es-CR,es;q=0.9,en;q=0.5"}, true, "El servicio de la biblioteca"},
		{"Accept-Language en preferred", map[string]string{"Accept-Language": "en-US,es;q=0.4"}, false, "library service"},
		{"unknown header value falls to Accept-Language", map[string]string{LanguageHeader: "fr", "Accept-Language": "es"}, true, "El servicio de la biblioteca"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			hdr := map[string]string{"Accept": "text/html"}
			for k, v := range tc.hdr {
				hdr[k] = v
			}
			w := serve(h, get("/library/", hdr))
			body := w.Body.String()
			if w.Code != http.StatusServiceUnavailable || !strings.Contains(body, "LAI-SVC-002") {
				t.Fatalf("%d %s", w.Code, body)
			}
			if !strings.Contains(body, tc.wantSub) {
				t.Fatalf("page lacks %q:\n%s", tc.wantSub, body)
			}
			if es := strings.Contains(body, "Reiniciar biblioteca") || strings.Contains(body, "no se está ejecutando"); es != tc.wantES {
				t.Fatalf("spanish=%v, want %v:\n%s", es, tc.wantES, body)
			}
		})
	}
}

func TestRoutesAreGetOnlyLevelNone(t *testing.T) {
	rs := Routes(http.NotFoundHandler())
	if len(rs) != 1 || rs[0].Method != http.MethodGet || rs[0].Pattern != "/library/" || !rs[0].Absolute || rs[0].Timeout != 0 || rs[0].Auth[0] != api.AuthNone {
		t.Fatalf("%+v", rs)
	}
	if err := api.NewRegistry().Add(rs...); err != nil {
		t.Fatal(err)
	}
}

func TestUndatedName(t *testing.T) {
	tests := []struct{ in, want string }{
		{"wikipedia_en_all_nopic_2026-10", "wikipedia_en_all_nopic"},
		{"mdwiki_en_all_maxi_2025-01", "mdwiki_en_all_maxi"},
		{"mdwiki_en_all", "mdwiki_en_all"},
		{"water_en_2024-8", "water_en_2024-8"},
		{"book_2026-10_extra", "book_2026-10_extra"},
		{"_2026-10", ""},
		{"", ""},
	}
	for _, tc := range tests {
		if got := UndatedName(tc.in); got != tc.want {
			t.Errorf("UndatedName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestBookMap(t *testing.T) {
	got := BookMap([]BookRef{
		{ContentID: "wikimed-en", Name: "mdwiki_en_all_2026-09", Root: "internal"},
		{ContentID: "water", Name: "water_en", Root: "ark-0000aaaa"},
	})
	if len(got) != 2 || got[0].Name != "mdwiki_en_all" || got[0].ContentID != "wikimed-en" || got[0].Root != "internal" || got[1].Name != "water_en" || got[1].Root != "ark-0000aaaa" {
		t.Fatalf("%+v", got)
	}
	if b := BookMap(nil); b == nil || len(b) != 0 {
		t.Fatal("an empty library gives an empty, non-nil list")
	}
	_ = context.Background()
}
