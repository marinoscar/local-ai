package api

import (
	"bytes"
	"io"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/api/static"
)

// PortalContentSecurityPolicy is the CSP of /portal and /portal/* (survival-portal.md section 4.11,
// authoritative). It replaces HTMLContentSecurityPolicy on these paths: no inline style, no
// scripts but the embedded ones, nothing from another origin.
const PortalContentSecurityPolicy = "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data: blob:; " +
	"media-src 'self' blob:; font-src 'self'; connect-src 'self'; worker-src 'self' blob:; frame-src 'self'; " +
	"frame-ancestors 'self' localai://app; base-uri 'none'; form-action 'self'"

// SPAOptions configures the embedded local UI and Portal (SPARoutes).
type SPAOptions struct {
	// FS is the embedded ui/dist (agent/ui/embed.go, created by PORTAL-04), rooted at dist: the
	// Dashboard entry, the Portal entry and assets/. Tests pass an fstest.MapFS.
	FS fs.FS
	// Mode returns the current mode name; "survival" makes "/" answer 302 /portal. Nil means never.
	Mode func() string
	// IndexFile is the Dashboard entry, default "index.html". Unknown non-API paths fall back to it.
	IndexFile string
	// PortalFile is the Portal entry, default "portal.html". Every /portal/* path that is not an
	// asset serves it, so the SPA's client-side routes survive a reload.
	PortalFile string
	// MapsFile is the map viewer entry, default "maps.html".
	MapsFile string
	// PortalHook runs on GET /portal before the page is written. PORTAL-02 sets it to issue the
	// lai_portal cookie in survival mode (survival-portal.md section 3.3); nil is a no-op.
	PortalHook func(w http.ResponseWriter, r *http.Request)
}

type spa struct {
	opts SPAOptions
}

// SPARoutes returns the routes of agent.md section 3.11 that serve the embedded UI: GET /, GET
// /assets/*, GET /portal, GET /portal/* and GET /portal/assets/*, all level none (the data the pages
// load needs its own credentials). The routes are plain: add them with Register or Registry.Add; the
// server never sees the SPA catch-all under /api/, where an unknown path stays 404 LAI-AGT-021.
//
// Layout of FS: IndexFile and PortalFile at the top, hashed bundles in assets/. Both pages and
// /portal/assets/* read assets/ (Vite writes one bundle directory for both entries).
func SPARoutes(opts SPAOptions) []Route {
	if opts.IndexFile == "" {
		opts.IndexFile = "index.html"
	}
	if opts.PortalFile == "" {
		opts.PortalFile = "portal.html"
	}
	if opts.MapsFile == "" {
		opts.MapsFile = "maps.html"
	}
	s := &spa{opts: opts}
	get := func(pattern string, fn HandlerFunc) Route {
		return Route{Method: http.MethodGet, Pattern: pattern, Auth: []Level{AuthNone}, Absolute: true, Handler: fn}
	}
	return []Route{
		get("/", s.root),
		get("/assets/", s.assets),
		get("/portal", s.portal),
		get("/portal/", s.portal),
		get("/portal/assets/", s.assets),
		get("/maps/", s.maps),
		get("/maps/viewer/", s.viewerAssets),
		get("/music/", s.music),
	}
}

// music serves the music page (E11, issue #512) at exactly /music/: the Portal entry, whose router
// opens its Music screen for that path, with the Portal CSP (the page uses the Portal's assets).
// Any other path under /music/ is a plain 404; the music API lives under /api/v1/music/.
func (s *spa) music(w http.ResponseWriter, r *http.Request) error {
	if r.URL.Path != "/music/" {
		return notFoundError()
	}
	return s.page(w, r, s.opts.PortalFile, PortalContentSecurityPolicy)
}

func (s *spa) survival() bool {
	return s.opts.Mode != nil && s.opts.Mode() == "survival"
}

// root serves "/" and every GET path no other route claims: the Dashboard SPA with its client-side
// routes. A file at the top of the dist (favicon, manifest) is served as itself; anything else is
// index.html. In survival mode "/" itself redirects to the Portal.
func (s *spa) root(w http.ResponseWriter, r *http.Request) error {
	p := r.URL.Path
	if p == "/" && s.survival() {
		http.Redirect(w, r, "/portal", http.StatusFound)
		return nil
	}
	name := strings.TrimPrefix(p, "/")
	if name != "" && !strings.Contains(name, "/") && name != s.opts.IndexFile && name != s.opts.PortalFile && s.isFile(name) {
		return s.serve(w, r, name, "no-cache")
	}
	return s.page(w, r, s.opts.IndexFile, HTMLContentSecurityPolicy)
}

// portal serves the Portal entry for /portal and every /portal/<client route>.
func (s *spa) portal(w http.ResponseWriter, r *http.Request) error {
	if p := r.URL.Path; (p == "/portal" || p == "/portal/") && s.opts.PortalHook != nil {
		s.opts.PortalHook(w, r)
	}
	return s.page(w, r, s.opts.PortalFile, PortalContentSecurityPolicy)
}

// assets serves /assets/<x> and /portal/assets/<x> from assets/<x> of the dist. A missing bundle
// is a 404, never the index page, so a stale hashed name fails loudly instead of loading HTML as
// JavaScript. Hashed names never change, so they are cached for a year.
func (s *spa) assets(w http.ResponseWriter, r *http.Request) error {
	rest := strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, "/portal"), "/assets/")
	if rest == "" || strings.Contains(rest, "..") || !s.isFile(path.Join("assets", rest)) {
		return notFoundError()
	}
	return s.serve(w, r, path.Join("assets", rest), "public, max-age=31536000, immutable")
}

// maps serves the offline map viewer at exactly /maps/ (companion-services.md section 4.4.3) with the
// same CSP as the Portal. Any other path under /maps/ that no tile, asset or place route claims is a
// plain 404.
func (s *spa) maps(w http.ResponseWriter, r *http.Request) error {
	if r.URL.Path != "/maps/" {
		return notFoundError()
	}
	return s.page(w, r, s.opts.MapsFile, PortalContentSecurityPolicy)
}

// viewerAssets serves /maps/viewer/<x> from viewer/<x> of the dist: the viewer's hashed bundle. It is
// not under /maps/assets/, which belongs to the map fonts and sprites (SVC-09).
func (s *spa) viewerAssets(w http.ResponseWriter, r *http.Request) error {
	rest := strings.TrimPrefix(r.URL.Path, "/maps/viewer/")
	if rest == "" || strings.Contains(rest, "..") || !s.isFile(path.Join("viewer", rest)) {
		return notFoundError()
	}
	return s.serve(w, r, path.Join("viewer", rest), "public, max-age=31536000, immutable")
}

// page serves an HTML entry with the CSP of its area. A missing entry means a bad build: the agent
// answers a built-in page with links to what still works (survival-portal.md section 5, LAI-SVC-100).
func (s *spa) page(w http.ResponseWriter, r *http.Request, name, csp string) error {
	if !s.isFile(name) {
		WriteErrorPageLinks(w, r, NewError(CodePortalMissing, http.StatusServiceUnavailable, map[string]any{"reason": "ui_missing", "entry": name}),
			emergencyLinks(InfoFrom(r.Context()).Lang))
		return nil
	}
	SetHTMLHeaders(w.Header())
	w.Header().Set("Content-Security-Policy", csp)
	return s.serve(w, r, name, "no-cache")
}

// isFile reports whether name is a servable regular file of the dist: a valid path with no dot
// segment (no dotfiles, no "..") that is not a directory.
func (s *spa) isFile(name string) bool {
	if s.opts.FS == nil || !fs.ValidPath(name) || name == "." {
		return false
	}
	for _, seg := range strings.Split(name, "/") {
		if strings.HasPrefix(seg, ".") {
			return false
		}
	}
	fi, err := fs.Stat(s.opts.FS, name)
	return err == nil && fi.Mode().IsRegular()
}

// serve writes a dist file with http.ServeContent (embedded files have no modification time, so
// there are no validators; the cache policy is the caller's).
func (s *spa) serve(w http.ResponseWriter, r *http.Request, name, cacheControl string) error {
	f, err := s.opts.FS.Open(name)
	if err != nil {
		return notFoundError()
	}
	defer f.Close()
	rs, ok := f.(io.ReadSeeker)
	if !ok {
		b, err := io.ReadAll(f)
		if err != nil {
			return notFoundError()
		}
		rs = bytes.NewReader(b)
	}
	h := w.Header()
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cache-Control", cacheControl)
	if ct := static.ContentType(name); ct != "" {
		h.Set("Content-Type", ct)
	}
	http.ServeContent(w, r, name, time.Time{}, rs)
	return nil
}

func notFoundError() *Error {
	return NewError(CodeNotFound, http.StatusNotFound, map[string]any{"reason": "not_found"})
}

// emergencyLinks are the buttons of the built-in page shown when the embedded screens are missing:
// everything that does not need them.
func emergencyLinks(lang string) []PageLink {
	if lang == langES {
		return []PageLink{{"/chat/", "Chat con la IA"}, {"/library/", "Biblioteca"}, {"/maps/", "Mapas"}, {"/docs/", "Manuales"}}
	}
	return []PageLink{{"/chat/", "AI chat"}, {"/library/", "Library"}, {"/maps/", "Maps"}, {"/docs/", "Manuals"}}
}
