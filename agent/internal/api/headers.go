package api

import "net/http"

// HTMLContentSecurityPolicy is the CSP of every HTML page the agent serves (agent.md section 3.11):
// the local UI, the Portal, the maps viewer and the docs. Everything the pages load is on the
// agent's own origin, so the page works with no network.
const HTMLContentSecurityPolicy = "default-src 'self'; connect-src 'self'; img-src 'self' data: blob:; worker-src 'self' blob:; style-src 'self' 'unsafe-inline'"

// SetHTMLHeaders sets the global headers of agent-served HTML on h.
func SetHTMLHeaders(h http.Header) {
	h.Set("Content-Security-Policy", HTMLContentSecurityPolicy)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Frame-Options", "SAMEORIGIN")
}

// HTMLHeaders wraps next so every response carries the global HTML headers. The static and proxy
// handlers of AGENT-16 and later issues use it for the pages they serve; API responses (JSON) do
// not need it.
func HTMLHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		SetHTMLHeaders(w.Header())
		next.ServeHTTP(w, r)
	})
}
