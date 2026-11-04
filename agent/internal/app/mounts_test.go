package app

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

// mountsRig starts the daemon with llama and kiwix pointed at recording httptest upstreams.
type mountsRig struct {
	*rig
	mu            sync.Mutex
	llama, kiwix  []http.Header
	llamaPaths    []string
	kiwixPaths    []string
	kiwixSrv      *httptest.Server
	llamaSrv      *httptest.Server
	secretOutside string
}

func newMountsRig(t *testing.T, kiwixUp bool) *mountsRig {
	t.Helper()
	m := &mountsRig{}
	m.llamaSrv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		m.llama = append(m.llama, r.Header.Clone())
		m.llamaPaths = append(m.llamaPaths, r.URL.Path)
		m.mu.Unlock()
		if r.URL.Path == "/health" || r.URL.Path == "/v1/models" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"status":"ok","data":[]}`)
			return
		}
		w.Header().Set("Set-Cookie", "up=1")
		_, _ = io.WriteString(w, "llama-ui")
	}))
	t.Cleanup(m.llamaSrv.Close)
	m.kiwixSrv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		m.kiwix = append(m.kiwix, r.Header.Clone())
		m.kiwixPaths = append(m.kiwixPaths, r.URL.Path)
		m.mu.Unlock()
		_, _ = io.WriteString(w, "kiwix:"+r.URL.Path)
	}))
	t.Cleanup(m.kiwixSrv.Close)
	kiwixPort := upPort(t, m.kiwixSrv.URL)
	if !kiwixUp {
		l, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		kiwixPort = upPort(t, "http://"+l.Addr().String())
		_ = l.Close() // nothing listens there now
	}
	m.rig = newRig(t, func(r *rig) {
		text := "schema_version: 1\nnode:\n  id: " + rigNode + "\n  display_name: Node Three\nprofile: small-8gb\n" +
			"tailscale:\n  manage: false\n  serve_pattern: none\nports:\n  agent: 7070\n  llama: " + upPort(t, m.llamaSrv.URL) +
			"\n  kiwix: " + kiwixPort + "\n  whisper: 8081\n"
		r.writeNodeYAMLText(text)
	})
	return m
}

func upPort(t *testing.T, raw string) string {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u.Port()
}

// raw requests a non-API path of the running daemon.
func (m *mountsRig) raw(method, path string, hdr map[string]string) reply {
	m.t.Helper()
	a := m.d.Components().API.TCPAddr()
	req, err := http.NewRequest(method, "http://"+a.String()+path, nil)
	if err != nil {
		m.t.Fatal(err)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	cl := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := cl.Do(req)
	if err != nil {
		m.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return reply{status: resp.StatusCode, header: resp.Header, body: b}
}

// llamaSaw reports whether llama received a request for path (the daemon's own health probes use
// other paths).
func (m *mountsRig) llamaSaw(path string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, p := range m.llamaPaths {
		if p == path {
			return true
		}
	}
	return false
}

func TestChatMountOnTheDaemon(t *testing.T) {
	m := newMountsRig(t, true)
	m.start()
	m.waitStarted()
	bearer := map[string]string{"Authorization": "Bearer " + m.token()}

	t.Run("anonymous outside survival is locked", func(t *testing.T) {
		rp := m.raw("GET", "/chat/", map[string]string{"Accept": "text/html"})
		if rp.status != 403 || !strings.Contains(string(rp.body), "LAI-SVC-101") {
			t.Fatalf("%d %s", rp.status, rp.body)
		}
		if m.llamaSaw("/") {
			t.Error("a locked request reached llama")
		}
	})
	t.Run("operator gets the WebUI with the key injected and nothing leaking", func(t *testing.T) {
		hdr := map[string]string{"Authorization": "Bearer " + m.token(), "Cookie": "localai_session=abc", "X-Api-Key": "client"}
		rp := m.raw("GET", "/chat/props", hdr)
		if rp.status != 200 || string(rp.body) != "llama-ui" {
			t.Fatalf("%d %s", rp.status, rp.body)
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		got := m.llama[len(m.llama)-1]
		if got.Get("Authorization") != "Bearer "+m.key {
			t.Errorf("llama saw Authorization %q", got.Get("Authorization"))
		}
		if got.Get("Cookie") != "" || got.Get("X-Api-Key") != "" {
			t.Errorf("client credentials reached llama: %v", got)
		}
		if m.llamaPaths[len(m.llamaPaths)-1] != "/props" {
			t.Errorf("upstream path %q", m.llamaPaths[len(m.llamaPaths)-1])
		}
		if rp.header.Get("Set-Cookie") != "" || strings.Contains(string(rp.body)+rp.header.Get("Authorization"), m.key) {
			t.Error("upstream cookie or key reached the browser")
		}
	})
	t.Run("a model download from the page is refused", func(t *testing.T) {
		hdr := map[string]string{"Authorization": bearer["Authorization"], "Origin": "http://127.0.0.1:7070", "Content-Type": "application/json"}
		a := m.d.Components().API.TCPAddr()
		req, _ := http.NewRequest("POST", "http://"+a.String()+"/chat/models/download", strings.NewReader("{}"))
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != 403 {
			t.Fatalf("%d %s", resp.StatusCode, b)
		}
	})
	t.Run("redirect", func(t *testing.T) {
		rp := m.raw("GET", "/chat", nil)
		if rp.status != 301 || rp.header.Get("Location") != "/chat/" {
			t.Fatalf("%d %q", rp.status, rp.header.Get("Location"))
		}
	})
}

func TestLibraryMountOnTheDaemon(t *testing.T) {
	cases := []struct {
		name       string
		up         bool
		method     string
		path       string
		wantStatus int
		wantBody   string
		wantPath   string
	}{
		{name: "path preserved", up: true, method: "GET", path: "/library/viewer", wantStatus: 200, wantBody: "kiwix:/library/viewer", wantPath: "/library/viewer"},
		{name: "head", up: true, method: "HEAD", path: "/library/", wantStatus: 200, wantPath: "/library/"},
		{name: "post refused", up: true, method: "POST", path: "/library/", wantStatus: 405},
		{name: "traversal never forwarded", up: true, method: "GET", path: "/library/../etc", wantStatus: 404},
		{name: "kiwix down is the 503 page", up: false, method: "GET", path: "/library/", wantStatus: 503, wantBody: "LAI-SVC-002"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newMountsRig(t, tc.up)
			m.start()
			m.waitStarted()
			// No credentials at all: the mount is a survival route.
			rp := m.raw(tc.method, tc.path, map[string]string{"Accept": "text/html"})
			if rp.status != tc.wantStatus {
				t.Fatalf("status %d, want %d: %s", rp.status, tc.wantStatus, rp.body)
			}
			if tc.wantBody != "" && !strings.Contains(string(rp.body), tc.wantBody) {
				t.Errorf("body %q lacks %q", rp.body, tc.wantBody)
			}
			if tc.wantPath != "" {
				if len(m.kiwixPaths) == 0 || m.kiwixPaths[len(m.kiwixPaths)-1] != tc.wantPath {
					t.Errorf("kiwix saw %v, want %q", m.kiwixPaths, tc.wantPath)
				}
			} else if len(m.kiwixPaths) != 0 {
				t.Errorf("kiwix was reached: %v", m.kiwixPaths)
			}
			if tc.wantStatus == 503 && !strings.Contains(string(rp.body), "<html") {
				t.Error("a browser gets a page, not JSON")
			}
		})
	}
}

func TestDocsMountOnTheDaemon(t *testing.T) {
	m := newMountsRig(t, true)
	m.write("srv/localai/docs/index.json", `{"rendered_at":"2026-11-04T00:00:00Z","docs":[{"slug":"survival","title":"Survival","title_es":"Supervivencia","html":"/docs/survival.html"}]}`, 0o644)
	m.write("srv/localai/docs/index.html", "<html>home</html>", 0o644)
	m.write("srv/localai/docs/survival.html", "<html>manual</html>", 0o644)
	m.write("srv/localai/docs/sub/index.html", "<html>sub</html>", 0o644)
	m.write("srv/localai/docs/.secret", "hidden", 0o644)
	m.write("srv/localai/outside.txt", "outside the docs folder", 0o644)
	m.start()
	m.waitStarted()

	cases := []struct {
		name   string
		path   string
		status int
		body   string
		csp    bool
	}{
		{name: "index page", path: "/docs/", status: 200, body: "home", csp: true},
		{name: "a manual", path: "/docs/survival.html", status: 200, body: "manual", csp: true},
		{name: "a folder", path: "/docs/sub/", status: 200, body: "sub", csp: true},
		{name: "index.json is served", path: "/docs/index.json", status: 200, body: "title_es"},
		{name: "dotfile refused", path: "/docs/.secret", status: 404},
		{name: "traversal refused", path: "/docs/../outside.txt", status: 404},
		{name: "encoded traversal refused", path: "/docs/%2e%2e/outside.txt", status: 404},
		{name: "encoded slash traversal refused", path: "/docs/..%2foutside.txt", status: 404},
		{name: "missing", path: "/docs/nothing.html", status: 404},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rp := m.raw("GET", tc.path, nil) // no credentials
			if rp.status != tc.status {
				t.Fatalf("status %d, want %d: %s", rp.status, tc.status, rp.body)
			}
			if tc.body != "" && !strings.Contains(string(rp.body), tc.body) {
				t.Errorf("body %q lacks %q", rp.body, tc.body)
			}
			if strings.Contains(string(rp.body), "outside the docs folder") || strings.Contains(string(rp.body), "hidden") {
				t.Error("a file outside the served set leaked")
			}
			if tc.csp && rp.header.Get("Content-Security-Policy") != "default-src 'self'; img-src 'self' data:" {
				t.Errorf("CSP = %q", rp.header.Get("Content-Security-Policy"))
			}
			if rp.status == 200 && rp.header.Get("X-Content-Type-Options") != "nosniff" {
				t.Error("nosniff missing")
			}
		})
	}
	t.Run("post refused", func(t *testing.T) {
		if rp := m.raw("POST", "/docs/", nil); rp.status != 405 && rp.status != 403 && rp.status != 415 {
			t.Fatalf("status %d", rp.status)
		}
	})
	t.Run("docs index needs an operator", func(t *testing.T) {
		if rp := m.raw("GET", "/api/v1/docs/index", nil); rp.status != 401 {
			t.Fatalf("anonymous: %d", rp.status)
		}
		rp := m.do("GET", "/docs/index", nil)
		if rp.status != 200 || !strings.Contains(string(rp.body), "Supervivencia") {
			t.Fatalf("%d %s", rp.status, rp.body)
		}
	})
}

func TestDocsNotRenderedIs404WithReason(t *testing.T) {
	m := newMountsRig(t, true)
	m.start()
	m.waitStarted()
	rp := m.raw("GET", "/docs/", map[string]string{"Accept": "application/json"})
	if rp.status != 404 || !strings.Contains(string(rp.body), "docs_not_rendered") {
		t.Fatalf("%d %s", rp.status, rp.body)
	}
}
