package portal

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/api"
	"github.com/marinoscar/local-ai/agent/internal/api/proxy"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
)

// The /chat/ mount (survival-portal.md section 4.4, agent.md section 3.11.1): a loopback reverse
// proxy to the llama-server router that injects the API key, so the WebUI works without anyone typing
// a key and the key never reaches the browser.

const (
	// ChatBodyLimit is the largest request body of /chat/ (photos): 32 MiB.
	ChatBodyLimit int64 = 32 << 20
	// ChatIdleTimeout is the longest a chat exchange may go with no byte moving.
	ChatIdleTimeout = 10 * time.Minute
	// desktopOrigin is the origin of ai-desktop's own shell pages (agent.md section 3.9.4).
	desktopOrigin = "localai://app"
	// maxInterceptBody bounds the {"model": "<id>"} body of an intercepted load or unload.
	maxInterceptBody = 64 << 10
)

// ChatOptions configures the /chat/ routes.
type ChatOptions struct {
	// Port is node.yaml ports.llama.
	Port int
	// Key returns the llama API key (router.FileKey.Key). It is called per request; the value is set
	// on the upstream request only and never logged. A failure leaves the request without a key, so
	// llama answers 401 rather than the agent guessing.
	Key func() (string, error)
	// Mode returns the current mode name; "survival" opens the mount to anonymous callers.
	Mode func() string
	// Ops executes the intercepted POST /models/load and /models/unload. Nil refuses them (503).
	Ops api.ModelOps
	// Logger receives warnings. Nil: slog.Default.
	Logger *slog.Logger
}

type chat struct {
	opts ChatOptions
	rp   http.Handler
	log  *slog.Logger
}

// ChatRoutes returns the routes of /chat/: every method of the policy at level none (the enabling
// rule runs inside the handler: survival mode, an operator session or a Portal session) with a 32
// MiB body and no route deadline (the proxy owns the timeouts, so a token stream is never cut), and
// GET /chat, which answers 301 /chat/.
func ChatRoutes(opts ChatOptions) []api.Route {
	c := &chat{opts: opts, log: opts.Logger}
	if c.log == nil {
		c.log = slog.Default()
	}
	c.log = c.log.With("component", "chat")
	c.rp = proxy.New(proxy.Options{
		Target:             &url.URL{Scheme: "http", Host: "127.0.0.1:" + strconv.Itoa(opts.Port)},
		StripPrefix:        "/chat",
		SetForwardedPrefix: true,
		InjectHeaders:      c.inject,
		BodyLimit:          ChatBodyLimit,
		ResponseTimeout:    -1,
		IdleTimeout:        ChatIdleTimeout,
		PathPolicy:         c.policy,
		DropSetCookie:      true,
		FrameHeaders:       true,
		DownStatus:         http.StatusBadGateway,
		DownCode:           api.CodeChatDown,
		Logger:             c.log,
	})
	var routes []api.Route
	for _, m := range []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		routes = append(routes, api.Route{
			Method: m, Pattern: "/chat/", Auth: []api.Level{api.AuthNone}, Absolute: true,
			MaxBody: ChatBodyLimit, Handler: http.HandlerFunc(c.serve),
		})
	}
	routes = append(routes, api.Route{
		Method: http.MethodGet, Pattern: "/chat", Auth: []api.Level{api.AuthNone}, Absolute: true,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			target := "/chat/"
			if r.URL.RawQuery != "" {
				target += "?" + r.URL.RawQuery
			}
			http.Redirect(w, r, target, http.StatusMovedPermanently)
		}),
	})
	return routes
}

// enabled is the enabling rule of section 4.4: survival mode, or an operator or admin, or a Portal
// session.
func (c *chat) enabled(r *http.Request) bool {
	if c.opts.Mode != nil && c.opts.Mode() == "survival" {
		return true
	}
	switch api.InfoFrom(r.Context()).Principal.Kind {
	case api.KindOperator, api.KindAdmin, api.KindPortal:
		return true
	}
	return false
}

func (c *chat) serve(w http.ResponseWriter, r *http.Request) {
	if !c.enabled(r) {
		api.WriteErrorPage(w, r, api.NewError(doctor.SVC101, http.StatusForbidden, nil))
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead && !sameOrigin(r) {
		api.WriteError(w, r, api.NewError(api.CodeBadOrigin, http.StatusForbidden, map[string]any{"reason": "origin_required"}))
		return
	}
	c.rp.ServeHTTP(w, r)
}

// sameOrigin is rule 5: a mutating request carries an Origin that is the page's own or ai-desktop's.
func sameOrigin(r *http.Request) bool {
	origins := r.Header.Values("Origin")
	if len(origins) != 1 {
		return false
	}
	o := origins[0]
	return o == desktopOrigin || (r.Host != "" && (o == "http://"+r.Host || o == "https://"+r.Host))
}

// inject sets the llama key. The proxy runs it last, after it dropped the client's credentials.
func (c *chat) inject(r *http.Request) {
	if c.opts.Key == nil {
		return
	}
	key, err := c.opts.Key()
	if err != nil || key == "" {
		c.log.Warn("the llama key is not readable; /chat/ goes to llama without it", "code", doctor.SVC102)
		return
	}
	r.Header.Set("Authorization", "Bearer "+key)
}

func isGet(r *http.Request) bool { return r.Method == http.MethodGet || r.Method == http.MethodHead }

// policy is the path table of section 4.4 rule 6, in llama's own paths.
func (c *chat) policy(r *http.Request) (proxy.Decision, http.Handler, string) {
	p := r.URL.Path
	under := func(prefix string) bool { return p == prefix || strings.HasPrefix(p, prefix+"/") }
	denied := func() (proxy.Decision, http.Handler, string) { return proxy.Deny, nil, doctor.SVC115 }

	switch {
	case p == "/models/load" && r.Method == http.MethodPost:
		return proxy.Intercept, http.HandlerFunc(c.load), ""
	case p == "/models/unload" && r.Method == http.MethodPost:
		return proxy.Intercept, http.HandlerFunc(c.unload), ""
	case strings.HasPrefix(p, "/models/download"), under("/models") && r.Method == http.MethodDelete,
		strings.HasPrefix(p, "/cors-proxy"), strings.HasPrefix(p, "/tools"):
		return denied()
	case under("/slots"):
		if p == "/slots" && isGet(r) {
			return proxy.Allow, nil, ""
		}
		return denied()
	case r.Method == http.MethodPost:
		switch {
		case p == "/v1/chat/completions", p == "/v1/completions", p == "/v1/embeddings",
			p == "/tokenize", p == "/detokenize", p == "/apply-template", under("/v1/stream"):
			return proxy.Allow, nil, ""
		}
		return denied()
	case isGet(r):
		return proxy.Allow, nil, ""
	}
	return denied()
}

func decodeModel(r *http.Request) (string, error) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxInterceptBody))
	if err != nil {
		return "", err
	}
	var b struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(raw, &b); err != nil || b.Model == "" {
		return "", api.NewError(api.CodeInvalidRequest, http.StatusBadRequest, map[string]any{"field": "model", "reason": "invalid_value"})
	}
	return b.Model, nil
}

// load runs the WebUI's POST /models/load through the agent's own orchestrator, so the model picker
// obeys the fit rules and the model lock.
func (c *chat) load(w http.ResponseWriter, r *http.Request) {
	if c.opts.Ops == nil {
		api.WriteError(w, r, api.NewError(api.CodeStarting, http.StatusServiceUnavailable, nil))
		return
	}
	id, err := decodeModel(r)
	if err != nil {
		api.WriteError(w, r, err)
		return
	}
	info := api.InfoFrom(r.Context())
	_, err = c.opts.Ops.Load(r.Context(), api.ModelLoadRequest{
		ID: id, Wait: info.Principal.Kind != api.KindPortal, UnloadOthers: true, Actor: api.AuditActor(info),
	})
	if err != nil {
		api.WriteError(w, r, err)
		return
	}
	api.WriteJSON(w, r, http.StatusOK, map[string]any{"success": true})
}

// unload is load's counterpart.
func (c *chat) unload(w http.ResponseWriter, r *http.Request) {
	if c.opts.Ops == nil {
		api.WriteError(w, r, api.NewError(api.CodeStarting, http.StatusServiceUnavailable, nil))
		return
	}
	id, err := decodeModel(r)
	if err != nil {
		api.WriteError(w, r, err)
		return
	}
	_, err = c.opts.Ops.Unload(r.Context(), api.ModelUnloadRequest{ID: id, Actor: api.AuditActor(api.InfoFrom(r.Context()))})
	if err != nil {
		api.WriteError(w, r, err)
		return
	}
	api.WriteJSON(w, r, http.StatusOK, map[string]any{"success": true})
}
