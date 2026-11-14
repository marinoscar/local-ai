package api

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"
)

// pageServer builds a server with the given routes (and no others) and the test authenticator.
func pageServer(t *testing.T, routes ...Route) *Server {
	t.Helper()
	reg := NewRegistry()
	if err := reg.Add(routes...); err != nil {
		t.Fatal(err)
	}
	s, err := New(Options{Registry: reg, Auth: credAuth, Logger: nil})
	if err != nil {
		t.Fatal(err)
	}
	s.SetMutationsOpen(true)
	return s
}

func distFS() fstest.MapFS {
	return fstest.MapFS{
		"portal.html":                 {Data: []byte("<!doctype html><title>Portal</title>")},
		"maps.html":                   {Data: []byte("<!doctype html><title>Maps</title>")},
		"viewer/maps-7a8b9c.js":       {Data: []byte("console.log('maps')")},
		"favicon.svg":                 {Data: []byte("<svg xmlns='http://www.w3.org/2000/svg'/>")},
		".secret":                     {Data: []byte("dotfile")},
		"assets/app-1a2b3c.js":        {Data: []byte("console.log('app')")},
		"assets/app-1a2b3c.css":       {Data: []byte("body{}")},
		"assets/sub/deep.woff2":       {Data: []byte("font")},
		"assets/.hidden":              {Data: []byte("dotfile")},
		"assets/portal-4d5e6f.js":     {Data: []byte("console.log('portal')")},
		"portal/ignored-subdir.txt":   {Data: []byte("not served")},
		"node_modules/x/index.js":     {Data: []byte("not an asset")},
		"assets/subdir-as-asset/file": {Data: []byte("x")},
	}
}

func spaServer(t *testing.T, opts SPAOptions) *Server {
	t.Helper()
	if opts.FS == nil {
		opts.FS = distFS()
	}
	return pageServer(t, SPARoutes(opts)...)
}

func hdr(accept string) map[string]string {
	if accept == "" {
		return nil
	}
	return map[string]string{"Accept": accept}
}

// Issue #497: the Portal's Home is the Dashboard in every mode, so "/" and every page path no other
// route claims go to /portal (the old index.html placeholder is gone).
func TestSPAOnlineAndFallback(t *testing.T) {
	s := spaServer(t, SPAOptions{Mode: func() string { return "online" }})
	h := s.Handler()

	for _, p := range []string{"/", "/settings", "/models/qwen", "/some/deep/client/route", "/index.html", "/portal.html", "/maps.html", "/chat", "/library", "/docs", "/favicon.ico", "/.secret", "/node_modules/x/index.js", "/missing.js"} {
		t.Run("to the Portal "+p, func(t *testing.T) {
			rec := do(h, req{target: p})
			if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/portal" {
				t.Fatalf("%s: %d Location=%q", p, rec.Code, rec.Header().Get("Location"))
			}
		})
	}
	t.Run("HEAD", func(t *testing.T) {
		rec := do(h, req{method: "HEAD", target: "/"})
		if rec.Code != http.StatusFound {
			t.Fatalf("%d %q", rec.Code, rec.Body.String())
		}
	})
	t.Run("a file at the top of the dist is served as itself", func(t *testing.T) {
		rec := do(h, req{target: "/favicon.svg"})
		if rec.Code != 200 || !strings.HasPrefix(rec.Body.String(), "<svg") || rec.Header().Get("Content-Type") != "image/svg+xml" {
			t.Fatalf("%d %q %v", rec.Code, rec.Body.String(), rec.Header())
		}
		if rec.Header().Get("Content-Security-Policy") != "" {
			t.Fatalf("an svg must not carry the page CSP: %v", rec.Header())
		}
	})
	t.Run("unknown API path is the JSON 404, not the SPA", func(t *testing.T) {
		for _, p := range []string{"/api/v1/nothing", "/api/nothing", "/api", "/api/"} {
			rec := do(h, req{target: p, header: hdr("text/html")})
			e := expectError(t, rec, 404, CodeNotFound)
			if e.Error.Details["reason"] != "unknown_route" {
				t.Fatalf("%s: %+v", p, e)
			}
		}
	})
	t.Run("POST to an unknown path is 404, not 405", func(t *testing.T) {
		rec := do(h, req{method: "POST", target: "/whatever", header: jsonHeaders(nil)})
		expectError(t, rec, 404, CodeNotFound)
		rec = do(h, req{method: "POST", target: "/", header: jsonHeaders(nil)})
		expectError(t, rec, 404, CodeNotFound)
	})
	t.Run("OPTIONS stays 405", func(t *testing.T) {
		rec := do(h, req{method: "OPTIONS", target: "/"})
		expectError(t, rec, 405, CodeInvalidRequest)
	})
	t.Run("traversal under the SPA never leaves the dist", func(t *testing.T) {
		for _, p := range []string{"/../etc/passwd", "/%2e%2e/etc/passwd", "/assets/../index.html", "/assets/%2e%2e/%2e%2e/etc/passwd"} {
			rec := do(h, req{target: p})
			if rec.Code != 404 && rec.Code != http.StatusFound {
				t.Fatalf("%s: %d %s", p, rec.Code, rec.Body.String())
			}
			if strings.Contains(rec.Body.String(), "root:") {
				t.Fatalf("%s leaked a system file", p)
			}
		}
	})
}

// Registering the catch-all must not change how the API answers: a known path with another method
// is still 405 with Allow, an unknown one still 404.
func TestSPADoesNotShadowTheAPI(t *testing.T) {
	routes := append(SPARoutes(SPAOptions{FS: distFS()}), testRoutes()...)
	h := pageServer(t, routes...).Handler()

	rec := do(h, req{method: "GET", target: "/api/v1/echo", header: jsonHeaders(nil)})
	e := expectError(t, rec, 405, CodeInvalidRequest)
	if rec.Header().Get("Allow") != "POST" || e.Error.Details["reason"] != "method_not_allowed" {
		t.Fatalf("Allow = %q, %+v", rec.Header().Get("Allow"), e)
	}
	expectError(t, do(h, req{target: "/api/v1/unknown", header: jsonHeaders(nil)}), 404, CodeNotFound)
	// And the real route still works next to the catch-all.
	if rec := do(h, req{target: "/api/v1/ping", header: jsonHeaders(nil)}); rec.Code != 200 {
		t.Fatalf("ping: %d", rec.Code)
	}
}

// "/" opens the Portal in every mode, and without a mode function too.
func TestSPASurvivalRedirect(t *testing.T) {
	var mode atomic.Value
	s := spaServer(t, SPAOptions{Mode: func() string { return mode.Load().(string) }})
	h := s.Handler()
	for _, m := range []string{"survival", "online", "offline"} {
		mode.Store(m)
		if rec := do(h, req{target: "/"}); rec.Code != http.StatusFound || rec.Header().Get("Location") != "/portal" {
			t.Fatalf("%s: %d Location=%q", m, rec.Code, rec.Header().Get("Location"))
		}
	}
	s2 := spaServer(t, SPAOptions{})
	if rec := do(s2.Handler(), req{target: "/"}); rec.Code != http.StatusFound {
		t.Fatalf("no mode: %d", rec.Code)
	}
}

func TestSPAAssets(t *testing.T) {
	h := spaServer(t, SPAOptions{}).Handler()
	t.Run("hashed asset is immutable", func(t *testing.T) {
		for _, p := range []string{"/assets/app-1a2b3c.js", "/portal/assets/app-1a2b3c.js"} {
			rec := do(h, req{target: p})
			if rec.Code != 200 || rec.Body.String() != "console.log('app')" {
				t.Fatalf("%s: %d %q", p, rec.Code, rec.Body.String())
			}
			if rec.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" ||
				rec.Header().Get("Content-Type") != "text/javascript; charset=utf-8" ||
				rec.Header().Get("X-Content-Type-Options") != "nosniff" {
				t.Fatalf("%s headers: %v", p, rec.Header())
			}
		}
	})
	t.Run("nested and font", func(t *testing.T) {
		rec := do(h, req{target: "/assets/sub/deep.woff2"})
		if rec.Code != 200 || rec.Header().Get("Content-Type") != "font/woff2" {
			t.Fatalf("%d %v", rec.Code, rec.Header())
		}
	})
	t.Run("Range works on assets", func(t *testing.T) {
		rec := do(h, req{target: "/assets/app-1a2b3c.js", header: map[string]string{"Range": "bytes=0-6"}})
		if rec.Code != 206 || rec.Body.String() != "console" {
			t.Fatalf("%d %q", rec.Code, rec.Body.String())
		}
	})
	t.Run("missing asset is a 404, never the index", func(t *testing.T) {
		for _, p := range []string{"/assets/gone-123.js", "/portal/assets/gone-123.js", "/assets/", "/assets/sub", "/assets/subdir-as-asset", "/assets/.hidden"} {
			rec := do(h, req{target: p})
			expectError(t, rec, 404, CodeNotFound)
			if strings.Contains(rec.Body.String(), "Dashboard") {
				t.Fatalf("%s fell back to index.html", p)
			}
		}
	})
	t.Run("traversal in assets", func(t *testing.T) {
		for _, p := range []string{"/assets/../index.html", "/assets/%2e%2e/index.html", "/assets/..%2findex.html"} {
			rec := do(h, req{target: p})
			if rec.Code == 200 && strings.Contains(rec.Body.String(), "Dashboard") && rec.Header().Get("Cache-Control") == "public, max-age=31536000, immutable" {
				t.Fatalf("%s served the index as an asset", p)
			}
		}
	})
	t.Run("the SPA does not accept POST", func(t *testing.T) {
		rec := do(h, req{method: "POST", target: "/assets/app-1a2b3c.js", header: jsonHeaders(nil)})
		if rec.Code != 405 && rec.Code != 404 {
			t.Fatalf("POST asset: %d", rec.Code)
		}
	})
}

func TestSPAPortal(t *testing.T) {
	var hooked []string
	s := spaServer(t, SPAOptions{PortalHook: func(w http.ResponseWriter, r *http.Request) {
		hooked = append(hooked, r.URL.Path)
		http.SetCookie(w, &http.Cookie{Name: "lai_portal", Value: "x", Path: "/"})
	}})
	h := s.Handler()

	for _, p := range []string{"/portal", "/portal/", "/portal/chat", "/portal/library", "/portal/first-aid", "/portal/some/deep/route"} {
		t.Run(p, func(t *testing.T) {
			rec := do(h, req{target: p})
			if rec.Code != 200 || !strings.Contains(rec.Body.String(), "Portal") {
				t.Fatalf("%d %s", rec.Code, rec.Body.String())
			}
			// survival-portal.md section 4.11: the strict CSP replaces the global one.
			if got := rec.Header().Get("Content-Security-Policy"); got != PortalContentSecurityPolicy {
				t.Fatalf("CSP = %q", got)
			}
			if strings.Contains(rec.Header().Get("Content-Security-Policy"), "unsafe-inline") {
				t.Fatal("the Portal CSP must not allow inline")
			}
			if rec.Header().Get("X-Content-Type-Options") != "nosniff" || rec.Header().Get("Referrer-Policy") != "no-referrer" {
				t.Fatalf("headers: %v", rec.Header())
			}
		})
	}
	t.Run("hook runs on the entry only", func(t *testing.T) {
		hooked = nil
		do(h, req{target: "/portal"})
		do(h, req{target: "/portal/chat"})
		do(h, req{target: "/portal/assets/app-1a2b3c.js"})
		if len(hooked) != 1 || hooked[0] != "/portal" {
			t.Fatalf("hook calls: %v", hooked)
		}
		rec := do(h, req{target: "/portal"})
		if !strings.Contains(rec.Header().Get("Set-Cookie"), "lai_portal=x") {
			t.Fatalf("Set-Cookie: %v", rec.Header())
		}
	})
	t.Run("portal assets", func(t *testing.T) {
		rec := do(h, req{target: "/portal/assets/portal-4d5e6f.js"})
		if rec.Code != 200 || rec.Body.String() != "console.log('portal')" {
			t.Fatalf("%d %q", rec.Code, rec.Body.String())
		}
	})
	t.Run("no hook is a no-op", func(t *testing.T) {
		h2 := spaServer(t, SPAOptions{}).Handler()
		rec := do(h2, req{target: "/portal"})
		if rec.Code != 200 || rec.Header().Get("Set-Cookie") != "" {
			t.Fatalf("%d %v", rec.Code, rec.Header())
		}
	})
}

func TestSPABadBuild(t *testing.T) {
	empty := fstest.MapFS{"assets/x.js": {Data: []byte("x")}}
	h := spaServer(t, SPAOptions{FS: empty}).Handler()

	t.Run("browser sees the built-in page with the links that still work", func(t *testing.T) {
		rec := do(h, req{target: "/portal", header: hdr("text/html")})
		if rec.Code != 503 {
			t.Fatalf("status = %d", rec.Code)
		}
		body := rec.Body.String()
		for _, want := range []string{"LAI-SVC-100", `href="/chat/"`, `href="/library/"`, `href="/maps/"`, `href="/docs/"`, "install-node.sh --repair"} {
			if !strings.Contains(body, want) {
				t.Errorf("page lacks %q", want)
			}
		}
	})
	t.Run("Spanish", func(t *testing.T) {
		rec := do(h, req{target: "/portal", header: map[string]string{"Accept": "text/html", "Accept-Language": "es"}})
		if rec.Code != 503 || !strings.Contains(rec.Body.String(), "Biblioteca") || !strings.Contains(rec.Body.String(), "LAI-SVC-100") {
			t.Fatalf("%d %s", rec.Code, rec.Body.String())
		}
	})
	t.Run("API client sees the envelope", func(t *testing.T) {
		rec := do(h, req{target: "/portal"})
		expectError(t, rec, 503, CodePortalMissing)
	})
	t.Run("nil FS is a bad build too", func(t *testing.T) {
		s := pageServer(t, SPARoutes(SPAOptions{})...)
		expectError(t, do(s.Handler(), req{target: "/portal"}), 503, CodePortalMissing)
	})
}

func portOf(t *testing.T, rawURL string) int {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	p, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// Real TCP: the routes work through the full server and its Host check, with a browser-like client.
func TestSPAMapsViewer(t *testing.T) {
	h := spaServer(t, SPAOptions{}).Handler()

	t.Run("/maps/ is the viewer with the Portal CSP", func(t *testing.T) {
		rec := do(h, req{target: "/maps/"})
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), "Maps") {
			t.Fatalf("%d %s", rec.Code, rec.Body.String())
		}
		if got := rec.Header().Get("Content-Security-Policy"); got != PortalContentSecurityPolicy {
			t.Fatalf("CSP = %q", got)
		}
		if rec.Header().Get("Cache-Control") != "no-cache" {
			t.Fatalf("Cache-Control = %q", rec.Header().Get("Cache-Control"))
		}
	})
	t.Run("the bundle is served from /maps/viewer/ and cached", func(t *testing.T) {
		rec := do(h, req{target: "/maps/viewer/maps-7a8b9c.js"})
		if rec.Code != 200 || rec.Body.String() != "console.log('maps')" {
			t.Fatalf("%d %q", rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Header().Get("Cache-Control"), "immutable") {
			t.Fatalf("Cache-Control = %q", rec.Header().Get("Cache-Control"))
		}
	})
	t.Run("anything else is a 404", func(t *testing.T) {
		for _, p := range []string{"/maps/viewer/", "/maps/viewer/missing.js", "/maps/viewer/../index.html", "/maps/other", "/maps/index.html"} {
			rec := do(h, req{target: p})
			if rec.Code != 404 {
				t.Fatalf("%s: %d %s", p, rec.Code, rec.Body.String())
			}
		}
	})
	t.Run("/maps without the slash goes to /maps/", func(t *testing.T) {
		rec := do(h, req{target: "/maps"})
		if rec.Code != 307 && rec.Code != 301 || rec.Header().Get("Location") != "/maps/" {
			t.Fatalf("%d %v", rec.Code, rec.Header())
		}
	})
}

func TestSPAOverTCP(t *testing.T) {
	s := spaServer(t, SPAOptions{Mode: func() string { return "survival" }})
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	s.port.Store(int32(portOf(t, srv.URL)))

	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != 302 || resp.Header.Get("Location") != "/portal" {
		t.Fatalf("%d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
}
