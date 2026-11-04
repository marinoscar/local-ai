package api

import (
	"net/http"
	"os"
	"strings"
	"testing"
)

// mapsRoute stands in for the /maps/ mount: like the real one it sets the global HTML headers (the
// looser CSP with 'unsafe-inline') before it writes, and the Portal CSP must still be the only one.
func mapsRoute(pattern string) Route {
	return Route{Method: "GET", Pattern: pattern, Absolute: true, Auth: []Level{AuthNone}, Handler: HandlerFunc(func(w http.ResponseWriter, r *http.Request) error {
		SetHTMLHeaders(w.Header())
		w.Header().Add("Content-Security-Policy", "default-src *")
		_, _ = w.Write([]byte("<!doctype html><title>maps</title>"))
		return nil
	})}
}

func TestPortalCSPHeader(t *testing.T) {
	routes := append(SPARoutes(SPAOptions{FS: distFS(), Mode: func() string { return "survival" }}), testRoutes()...)
	routes = append(routes, mapsRoute("/maps/tiles/")) // /maps/ itself is the viewer in SPARoutes
	s := pageServer(t, routes...)
	h := s.Handler()

	covered := []string{"/portal", "/portal/", "/portal/talk", "/portal/chat", "/portal/assets/portal-4d5e6f.js", "/portal/assets/missing.js", "/maps/", "/maps/tiles/x.pmtiles"}
	for _, p := range covered {
		t.Run("covered "+p, func(t *testing.T) {
			rec := do(h, req{target: p})
			got := rec.Header().Values("Content-Security-Policy")
			if len(got) != 1 || got[0] != PortalContentSecurityPolicy {
				t.Fatalf("%s (%d): Content-Security-Policy = %q", p, rec.Code, got)
			}
			if strings.Contains(got[0], "unsafe-inline") {
				t.Fatal("the Portal CSP must not allow inline")
			}
		})
	}

	// The same header on HEAD, and on the pages' other global headers.
	t.Run("global headers are kept next to it", func(t *testing.T) {
		rec := do(h, req{target: "/portal/talk"})
		if rec.Header().Get("X-Content-Type-Options") != "nosniff" || rec.Header().Get("Referrer-Policy") != "no-referrer" || rec.Header().Get("X-Frame-Options") != "SAMEORIGIN" {
			t.Fatalf("headers: %v", rec.Header())
		}
		rec = do(h, req{target: "/maps/"})
		if rec.Header().Get("X-Content-Type-Options") != "nosniff" || rec.Header().Get("Referrer-Policy") != "no-referrer" || rec.Header().Get("X-Frame-Options") != "SAMEORIGIN" {
			t.Fatalf("maps headers: %v", rec.Header())
		}
	})

	t.Run("not on the API or other areas", func(t *testing.T) {
		rec := do(h, req{target: "/api/v1/open"})
		if got := rec.Header().Values("Content-Security-Policy"); len(got) != 0 {
			t.Fatalf("/api/v1/open: %q", got)
		}
		rec = do(h, req{target: "/portalx"}) // the dashboard fallback, with the global policy
		if got := rec.Header().Values("Content-Security-Policy"); len(got) != 1 || got[0] != HTMLContentSecurityPolicy {
			t.Fatalf("/portalx: %q", got)
		}
	})
}

func TestIsPortalCSPPath(t *testing.T) {
	for p, want := range map[string]bool{
		"/portal": true, "/portal/": true, "/portal/talk": true, "/portal/assets/x.js": true,
		"/maps": true, "/maps/": true, "/maps/tiles/a.pmtiles": true,
		"/": false, "/portalx": false, "/mapsx": false, "/chat/": false, "/library/": false, "/api/v1/portal/status": false, "/assets/x.js": false,
	} {
		if got := IsPortalCSPPath(p); got != want {
			t.Errorf("IsPortalCSPPath(%q) = %v, want %v", p, got, want)
		}
	}
}

// The header in the code is the header of the spec (survival-portal.md section 4.11), word for word.
func TestPortalCSPMatchesSpec(t *testing.T) {
	b, err := os.ReadFile("../../../docs/specs/survival-portal.md")
	if err != nil {
		t.Fatal(err)
	}
	const prefix = "`Content-Security-Policy: "
	i := strings.Index(string(b), prefix)
	if i < 0 {
		t.Fatal("the spec has no Content-Security-Policy line")
	}
	rest := string(b)[i+len(prefix):]
	got := rest[:strings.Index(rest, "`")]
	if got != PortalContentSecurityPolicy {
		t.Fatalf("spec:\n%s\ncode:\n%s", got, PortalContentSecurityPolicy)
	}
}
