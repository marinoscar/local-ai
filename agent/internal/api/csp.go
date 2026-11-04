package api

import (
	"net/http"
	"strings"
)

// IsPortalCSPPath reports whether a request path is covered by the Portal CSP: /portal, /portal/*,
// /maps and /maps/* (survival-portal.md section 4.11, agent.md section 3.11.3). Everything else
// keeps the global HTML policy.
func IsPortalCSPPath(p string) bool {
	return p == "/portal" || strings.HasPrefix(p, "/portal/") || p == "/maps" || strings.HasPrefix(p, "/maps/")
}

// portalCSP makes sure every response under /portal and /maps/ carries exactly one
// Content-Security-Policy, PortalContentSecurityPolicy. The value is set (not added) when the
// response starts, so it replaces whatever a handler or a proxied upstream wrote, including the
// global policy with 'unsafe-inline' (two CSP headers would intersect, and the looser one would
// still be listed). It covers assets and error answers too, not only the HTML pages.
func (s *Server) portalCSP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !IsPortalCSPPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		next.ServeHTTP(&cspWriter{ResponseWriter: w}, r)
	})
}

// cspWriter sets the Portal CSP as the response starts. It keeps http.Flusher and exposes the wrapped
// writer to http.ResponseController (WebSocket upgrades, streams).
type cspWriter struct {
	http.ResponseWriter
	set bool
}

func (w *cspWriter) enforce() {
	if !w.set {
		w.set = true
		w.Header()["Content-Security-Policy"] = []string{PortalContentSecurityPolicy}
	}
}

func (w *cspWriter) WriteHeader(code int) {
	// 1xx informational responses (103 Early Hints) are not the final answer.
	if code >= 200 || code == http.StatusSwitchingProtocols {
		w.enforce()
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *cspWriter) Write(b []byte) (int, error) {
	w.enforce()
	return w.ResponseWriter.Write(b)
}

func (w *cspWriter) Flush() {
	w.enforce()
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *cspWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
