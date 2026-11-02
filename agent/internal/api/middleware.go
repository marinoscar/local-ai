package api

import (
	"context"
	"crypto/rand"
	"fmt"
	"log/slog"
	"mime"
	"net/http"
	"path"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"time"
)

// The middleware chain, outermost first (agent.md section 3.9.1):
//
//	requestID        step 1  request id, language, transport
//	observe          access log and panic recovery around everything below
//	hostCheck        step 2  TCP only
//	originCheck      step 3  TCP only
//	matchGuard       step 4  OPTIONS, unclean paths; then the mux matches the route
//	serveRoute       steps 5 to 9, then the handler under the route deadline:
//	                   auth (5 to 7), mutating-route gate, CSRF (8), content type and body limit (9)
//
// Every refusal is the JSON error envelope; nothing redirects.

const requestIDAlphabet = "abcdefghijklmnopqrstuvwxyz234567"

// NewRequestID returns a fresh request id: req_ plus 12 base32 characters (a-z, 2-7).
func NewRequestID() string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	id := make([]byte, 0, 16)
	id = append(id, "req_"...)
	for _, c := range b {
		id = append(id, requestIDAlphabet[c&31]) // 256 is a multiple of 32: no modulo bias
	}
	return string(id)
}

// ValidRequestID reports whether s has the form ^req_[a-z2-7]{12}$.
func ValidRequestID(s string) bool {
	if len(s) != 16 || !strings.HasPrefix(s, "req_") {
		return false
	}
	for i := 4; i < len(s); i++ {
		if c := s[i]; !(c >= 'a' && c <= 'z') && !(c >= '2' && c <= '7') {
			return false
		}
	}
	return true
}

// requestID is step 1. It echoes a valid incoming X-Request-Id and replaces anything else, sets the
// header before any other work so even a panic or a refusal carries it, and stores the RequestInfo.
func (s *Server) requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-Id")
		if !ValidRequestID(id) {
			id = NewRequestID()
		}
		w.Header().Set("X-Request-Id", id)
		conn := connFrom(r.Context())
		info := &RequestInfo{
			ID:        id,
			Transport: conn.transport,
			Peer:      conn.peer,
			Lang:      resolveLang(r.Header.Get("Accept-Language"), s.uiLanguage()),
			MaxBody:   DefaultMaxBody,
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), infoKey{}, info)))
	})
}

// statusWriter records what the handler wrote, for the access log and for panic recovery. It keeps
// http.Flusher (SSE) and exposes the wrapped writer to http.ResponseController.
type statusWriter struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (w *statusWriter) WriteHeader(code int) {
	if !w.wrote {
		w.status, w.wrote = code, true
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if !w.wrote {
		w.status, w.wrote = http.StatusOK, true
	}
	return w.ResponseWriter.Write(b)
}

func (w *statusWriter) Flush() {
	if !w.wrote {
		w.status, w.wrote = http.StatusOK, true
	}
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// observe writes the access log line and turns a panic into 500 LAI-AGT-099 with details.request_id
// (agent.md section 3.7). If the response had already started, the connection is aborted instead:
// a half-written answer must not look complete.
func (s *Server) observe(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w}
		info := InfoFrom(r.Context())
		defer func() {
			rec := recover()
			abort := false
			if rec != nil {
				if rec == http.ErrAbortHandler {
					abort = true
				} else {
					s.log.Error("panic in request handler", "code", CodeInternal, "req_id", info.ID,
						"method", r.Method, "path", r.URL.Path, "panic", fmt.Sprint(rec), "stack", string(debug.Stack()))
					if sw.wrote {
						abort = true
					} else {
						WriteError(sw, r, NewError(CodeInternal, http.StatusInternalServerError, nil))
					}
				}
			}
			s.logAccess(r, info, sw, time.Since(start))
			if abort {
				panic(http.ErrAbortHandler)
			}
		}()
		next.ServeHTTP(sw, r)
	})
}

// logAccess writes one line per request: debug, or info for a mutating method (agent.md section 4.11).
func (s *Server) logAccess(r *http.Request, info *RequestInfo, sw *statusWriter, d time.Duration) {
	level := slog.LevelDebug
	if isMutating(r.Method) {
		level = slog.LevelInfo
	}
	if !s.log.Enabled(r.Context(), level) {
		return
	}
	status := sw.status
	if !sw.wrote {
		status = http.StatusOK // net/http answers 200 when a handler writes nothing
	}
	attrs := []slog.Attr{
		slog.String("req_id", info.ID),
		slog.String("method", r.Method),
		slog.String("path", r.URL.Path),
		slog.Int("http_status", status),
		slog.Int64("duration_ms", d.Milliseconds()),
		slog.String("principal", info.Principal.String()),
		slog.String("transport", string(info.Transport)),
	}
	if info.ErrCode != "" {
		attrs = append(attrs, slog.String("code", info.ErrCode))
	}
	if info.Err != nil {
		attrs = append(attrs, slog.String("error", info.Err.Error()))
	}
	s.log.LogAttrs(r.Context(), level, "api.request", attrs...)
}

// hostCheck is step 2. On TCP the raw Host header must be exactly 127.0.0.1:<port> or
// localhost:<port>; anything else, including 127.0.0.1 without a port, is 403 (DNS rebinding
// defence). The socket skips it: a unix socket has no host name to rebind.
func (s *Server) hostCheck(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if InfoFrom(r.Context()).Transport == TransportTCP && !s.hostAllowed(r.Host) {
			WriteError(w, r, NewError(CodeBadHost, http.StatusForbidden, map[string]any{"reason": "host_not_allowed"}))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) hostAllowed(host string) bool {
	p := strconv.Itoa(s.agentPort())
	return host == "127.0.0.1:"+p || host == "localhost:"+p
}

// desktopOrigin is the origin of ai-desktop's own shell pages (agent.md section 3.9.4).
const desktopOrigin = "localai://app"

func (s *Server) originAllowed(origin string) bool {
	p := strconv.Itoa(s.agentPort())
	return origin == "http://127.0.0.1:"+p || origin == "http://localhost:"+p || origin == desktopOrigin
}

// originCheck is step 3, TCP only. A present Origin must be one of the three allowed values
// (`null`, another port or another host is refused). With no Origin, a present Sec-Fetch-Site must
// be same-origin or none. A request with neither header (curl, aictl over TCP) passes.
func (s *Server) originCheck(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if InfoFrom(r.Context()).Transport == TransportTCP {
			if origins := r.Header.Values("Origin"); len(origins) > 0 {
				if len(origins) != 1 || !s.originAllowed(origins[0]) {
					WriteError(w, r, NewError(CodeBadOrigin, http.StatusForbidden, map[string]any{"reason": "origin_not_allowed"}))
					return
				}
			} else if sites := r.Header.Values("Sec-Fetch-Site"); len(sites) > 0 {
				if len(sites) != 1 || (sites[0] != "same-origin" && sites[0] != "none") {
					WriteError(w, r, NewError(CodeBadOrigin, http.StatusForbidden, map[string]any{"reason": "fetch_site_not_allowed"}))
					return
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

// matchGuard is the first half of step 4. OPTIONS is always 405, never a preflight answer, so a
// cross-origin browser call fails closed (agent.md section 3.9.4). A path that is not in clean form
// would make ServeMux redirect; the agent answers 404 instead, since nothing redirects.
func (s *Server) matchGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			s.methodNotAllowed(w, r)
			return
		}
		if r.URL.Path != cleanPath(r.URL.Path) {
			WriteError(w, r, NewError(CodeNotFound, http.StatusNotFound, map[string]any{"reason": "unknown_route"}))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// cleanPath is net/http's path normalization: it keeps a trailing slash, resolves . and .. and
// repeated slashes, and always starts with /.
func cleanPath(p string) string {
	if p == "" {
		return "/"
	}
	if p[0] != '/' {
		p = "/" + p
	}
	np := path.Clean(p)
	if p[len(p)-1] == '/' && np != "/" {
		if len(p) == len(np)+1 && strings.HasPrefix(p, np) {
			np = p
		} else {
			np += "/"
		}
	}
	return np
}

func (s *Server) methodNotAllowed(w http.ResponseWriter, r *http.Request) {
	allow := s.allowedMethods(r)
	e := NewError(CodeInvalidRequest, http.StatusMethodNotAllowed, map[string]any{"reason": "method_not_allowed", "allow": allow})
	if len(allow) > 0 {
		e.Header = http.Header{"Allow": []string{strings.Join(allow, ", ")}}
	}
	WriteError(w, r, e)
}

// allowedMethods lists the methods some route accepts for r's path, sorted, with HEAD wherever GET is.
func (s *Server) allowedMethods(r *http.Request) []string {
	out := []string{}
	probe := r.WithContext(r.Context())
	for method, mux := range s.methodMux {
		probe.Method = method
		// "/" is the SPA catch-all (a registered absolute "/" route): it matches every path, so it
		// says nothing about which methods a route accepts.
		if _, pattern := mux.Handler(probe); pattern != "" && pattern != "/" {
			out = append(out, method)
			if method == http.MethodGet {
				out = append(out, http.MethodHead)
			}
		}
	}
	sort.Strings(out)
	return out
}

// unmatched serves every request the route table does not match: 405 when the path exists under
// another method, otherwise 404 LAI-AGT-021.
func (s *Server) unmatched(w http.ResponseWriter, r *http.Request) {
	if len(s.allowedMethods(r)) > 0 {
		s.methodNotAllowed(w, r)
		return
	}
	WriteError(w, r, NewError(CodeNotFound, http.StatusNotFound, map[string]any{"reason": "unknown_route"}))
}

// serveRoute runs steps 5 to 9 for a matched route and then its handler.
func (s *Server) serveRoute(rt *Route, w http.ResponseWriter, r *http.Request) {
	info := InfoFrom(r.Context())
	info.Route = rt

	// Steps 5 to 7: principal, PIN grant, auth decision (the AGENT-06 seam).
	p, err := s.auth.Authenticate(r, rt)
	info.Principal = p // set before a refusal, so the access log and audit know the caller
	if err != nil {
		WriteError(w, r, err)
		return
	}
	if !p.Satisfies(rt.Auth) {
		// A correct Authenticator never gets here: it refuses with the right code first. Fail closed.
		switch p.Kind {
		case "", KindAnonymous:
			WriteError(w, r, NewError(CodeUnauthenticated, http.StatusUnauthorized, nil))
		case KindPortal: // a Portal session is valid only on the routes survival-portal.md section 3.3 lists
			WriteError(w, r, NewError(CodePortalSessionEnded, http.StatusUnauthorized, nil))
		default:
			WriteError(w, r, NewError(CodeAdminRequired, http.StatusForbidden, nil))
		}
		return
	}

	// The mutating-route gate (agent.md section 4.1 steps 10 and 12, section 4.12).
	if rt.Mutating() && !rt.GateExempt && !s.MutationsOpen() {
		WriteError(w, r, NewError(CodeStarting, http.StatusServiceUnavailable, nil))
		return
	}

	// Step 8: CSRF rule.
	if rt.Mutating() {
		if err := s.csrf(r, p); err != nil {
			WriteError(w, r, err)
			return
		}
	}

	// Step 9: content type and body limit.
	limit := rt.MaxBody
	if limit == 0 {
		limit = DefaultMaxBody
	}
	info.MaxBody = limit
	if rt.Mutating() {
		if err := checkMediaType(r.Header.Get("Content-Type"), rt.MediaTypes); err != nil {
			WriteError(w, r, err)
			return
		}
	}
	if r.ContentLength > limit {
		WriteError(w, r, NewError(CodeTooLarge, http.StatusRequestEntityTooLarge, map[string]any{"limit": limit}))
		return
	}
	if r.Body != nil && r.Body != http.NoBody {
		r.Body = http.MaxBytesReader(w, r.Body, limit)
	}

	// The route deadline: on the context, and on the connection so a handler that ignores the
	// context cannot hold it open (SSE routes have no timeout).
	if rt.Timeout > 0 {
		ctx, cancel := context.WithTimeout(r.Context(), rt.Timeout)
		defer cancel()
		r = r.WithContext(ctx)
		rc := http.NewResponseController(w)
		deadline := time.Now().Add(rt.Timeout + connectionSlack)
		_ = rc.SetReadDeadline(deadline) // ErrNotSupported on a test recorder: fine
		_ = rc.SetWriteDeadline(deadline)
		// net/http resets neither deadline for the next request on a kept-alive connection
		// (the server has no WriteTimeout), so a stale one would break it.
		defer func() {
			_ = rc.SetReadDeadline(time.Time{})
			_ = rc.SetWriteDeadline(time.Time{})
		}()
	}
	rt.Handler.ServeHTTP(w, r)
}

// connectionSlack lets a handler that hit its deadline still write its error answer.
const connectionSlack = 2 * time.Second

// csrf is step 8, for mutating requests. A request authenticated by a cookie must prove it came
// from the agent's own origin: an operator session needs an allowed Origin or Sec-Fetch-Site:
// same-origin; a Portal session is stricter and needs an allowed Origin (Sec-Fetch-Site alone is
// refused, survival-portal.md section 3.3). Bearer tokens and socket peers cannot be attached by a
// browser and are exempt.
func (s *Server) csrf(r *http.Request, p Principal) *Error {
	if p.Credential != CredSession && p.Credential != CredPortal {
		return nil
	}
	origins := r.Header.Values("Origin")
	originOK := len(origins) == 1 && s.originAllowed(origins[0])
	switch {
	case len(origins) > 0 && !originOK:
		return NewError(CodeBadOrigin, http.StatusForbidden, map[string]any{"reason": "origin_not_allowed"})
	case originOK:
		return nil
	case p.Credential == CredSession && r.Header.Get("Sec-Fetch-Site") == "same-origin":
		return nil
	}
	return NewError(CodeBadOrigin, http.StatusForbidden, map[string]any{"reason": "origin_required"})
}

// checkMediaType accepts a content type listed in allowed (default: application/json). JSON takes
// no parameter but charset=utf-8; other types (multipart/form-data) take any.
func checkMediaType(contentType string, allowed []string) *Error {
	if len(allowed) == 0 {
		allowed = []string{JSONMediaType}
	}
	mt, params, err := mime.ParseMediaType(contentType)
	if err == nil {
		for _, a := range allowed {
			if mt != a {
				continue
			}
			if a != JSONMediaType {
				return nil
			}
			if len(params) == 0 || (len(params) == 1 && strings.EqualFold(params["charset"], "utf-8")) {
				return nil
			}
		}
	}
	return NewError(CodeInvalidRequest, http.StatusUnsupportedMediaType,
		map[string]any{"reason": "unsupported_media_type", "expected": allowed})
}
