package api

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// BasePath is the prefix of every API route (agent.md section 3.7).
const BasePath = "/api/v1"

// Body limits (agent.md section 3.7).
const (
	// DefaultMaxBody is the request body limit of a route that does not set its own: 1 MiB.
	DefaultMaxBody int64 = 1 << 20
	// SpeechMaxBody is the limit of the STT upload: 25 MiB.
	SpeechMaxBody int64 = 25 << 20
)

// JSONMediaType is the only content type a mutating request may carry unless its route says otherwise.
const JSONMediaType = "application/json"

// Route is one entry of the route registry: what AGENT-06 needs to authenticate it, what AGENT-07
// compares with openapi.yaml, and what the server needs to serve it.
type Route struct {
	// Method is the HTTP method: GET, POST, PUT, PATCH or DELETE (HEAD is served by GET).
	Method string
	// Pattern is a net/http ServeMux path pattern, relative to BasePath ("/models/{id}/load"), as
	// in the route table of agent.md section 3.8. With Absolute it is the full path instead.
	Pattern string
	// Auth lists the levels the route accepts; satisfying any one is enough ("op, or portal" is
	// {AuthOp, AuthPortal}). It must not be empty.
	Auth []Level
	// Timeout is the handler deadline (the Timeout column of section 3.8). It becomes a deadline
	// on the request context and on the connection's read and write. Zero means none: streams (SSE).
	Timeout time.Duration
	// Handler serves the route. A handler must honor r.Context(); it reads path values with
	// r.PathValue. It is nil only in the metadata the package keeps for the routes the server
	// serves itself (BuiltinRoutes).
	Handler http.Handler

	// GateExempt keeps a mutating route answering while the mutating-route gate is closed (before
	// startup step 12, and while stopping): the handler decides what to do. Only POST /mode/survival
	// sets it (agent.md section 4.1 step 10).
	GateExempt bool
	// MaxBody is the body limit in bytes; zero means DefaultMaxBody. STT sets SpeechMaxBody.
	MaxBody int64
	// MediaTypes are the content types a mutating request may send; nil means application/json.
	// STT lists multipart/form-data. A mutating request must send one of them even with no body.
	MediaTypes []string
	// Absolute marks Pattern as the full path, outside BasePath: the SPA, /portal, /chat/, /library/,
	// /maps/ and /docs/ of agent.md section 3.11 (AGENT-16 and later issues).
	Absolute bool
}

// Path returns the full path pattern the route is served at.
func (rt Route) Path() string {
	if rt.Absolute {
		return rt.Pattern
	}
	return BasePath + rt.Pattern
}

// Mutating reports whether the route changes state: every method except GET and HEAD. Mutating
// routes need a JSON content type, the CSRF rule, and are held back by the gate.
func (rt Route) Mutating() bool { return isMutating(rt.Method) }

func isMutating(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return false
	}
	return true
}

func (rt *Route) accepts(l Level) bool {
	for _, a := range rt.Auth {
		if a == l {
			return true
		}
	}
	return false
}

func (rt Route) key() string { return rt.Method + " " + rt.Path() }

func (rt Route) validate(needHandler bool) error {
	switch rt.Method {
	case http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
	default:
		return fmt.Errorf("route %q: method must be GET, POST, PUT, PATCH or DELETE", rt.key())
	}
	if !strings.HasPrefix(rt.Pattern, "/") {
		return fmt.Errorf("route %q: pattern must start with /", rt.key())
	}
	if rt.Absolute && strings.HasPrefix(rt.Pattern, BasePath+"/") {
		return fmt.Errorf("route %q: an absolute pattern must not be under %s", rt.key(), BasePath)
	}
	if len(rt.Auth) == 0 {
		return fmt.Errorf("route %q: no auth level", rt.key())
	}
	for _, l := range rt.Auth {
		if !l.Valid() {
			return fmt.Errorf("route %q: unknown auth level %q", rt.key(), l)
		}
	}
	if rt.Timeout < 0 {
		return fmt.Errorf("route %q: negative timeout", rt.key())
	}
	if rt.MaxBody < 0 {
		return fmt.Errorf("route %q: negative body limit", rt.key())
	}
	if needHandler && rt.Handler == nil {
		return fmt.Errorf("route %q: no handler", rt.key())
	}
	return nil
}

// BuiltinRoutes returns the routes the server serves itself, as metadata (Handler is nil): today
// GET /health. The OpenAPI conformance test (AGENT-07) needs them next to the registered ones.
func BuiltinRoutes() []Route {
	return []Route{{Method: http.MethodGet, Pattern: "/health", Auth: []Level{AuthNone}, Timeout: 2 * time.Second}}
}

// Registry is the route registry: the seam later issues (AGENT-06 to AGENT-16) plug their routes
// into. It is safe for concurrent use.
type Registry struct {
	mu     sync.Mutex
	routes []Route
	keys   map[string]bool
}

// NewRegistry returns an empty registry. Its keys already include the built-in routes, so adding
// one of them is an error.
func NewRegistry() *Registry {
	r := &Registry{keys: map[string]bool{}}
	for _, b := range BuiltinRoutes() {
		r.keys[b.key()] = true
	}
	return r
}

// Add registers routes. It validates every route first and registers all or none: a bad method,
// pattern, auth level, a missing handler or a duplicate method and path is an error.
func (r *Registry) Add(routes ...Route) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	seen := map[string]bool{}
	for _, rt := range routes {
		if err := rt.validate(true); err != nil {
			return err
		}
		k := rt.key()
		if r.keys[k] || seen[k] {
			return fmt.Errorf("route %q is registered twice", k)
		}
		seen[k] = true
	}
	for _, rt := range routes {
		rt.Auth = append([]Level(nil), rt.Auth...)
		rt.MediaTypes = append([]string(nil), rt.MediaTypes...)
		r.keys[rt.key()] = true
		r.routes = append(r.routes, rt)
	}
	return nil
}

// Routes returns the registered routes (not the built-in ones), sorted by path then method. The
// caller owns the slice.
func (r *Registry) Routes() []Route {
	r.mu.Lock()
	out := make([]Route, len(r.routes))
	copy(out, r.routes)
	r.mu.Unlock()
	sortRoutes(out)
	return out
}

func sortRoutes(rs []Route) {
	sort.Slice(rs, func(i, j int) bool {
		if pi, pj := rs[i].Path(), rs[j].Path(); pi != pj {
			return pi < pj
		}
		return rs[i].Method < rs[j].Method
	})
}

// DefaultRegistry is the registry packages add their routes to at init time, so that Routes lists
// them without a running server. A server uses it unless Options.Registry says otherwise.
var DefaultRegistry = NewRegistry()

// Register adds routes to DefaultRegistry and panics on error, like http.Handle: a bad route is a
// programming error found at start-up.
func Register(routes ...Route) {
	if err := DefaultRegistry.Add(routes...); err != nil {
		panic("api: " + err.Error())
	}
}

// Routes returns every route of the agent: the built-in ones and those in DefaultRegistry, sorted by
// path then method. AGENT-07 compares this list with agent/api/openapi.yaml.
func Routes() []Route { return allRoutes(DefaultRegistry) }

func allRoutes(reg *Registry) []Route {
	out := append(BuiltinRoutes(), reg.Routes()...)
	sortRoutes(out)
	return out
}
