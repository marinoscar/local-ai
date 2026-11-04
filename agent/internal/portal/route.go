package portal

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/api"
	"github.com/marinoscar/local-ai/agent/internal/catalog"
	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
)

// CacheTTL is how long a built body is served again (agent.md section 3.8.5: cached for 2 s).
const CacheTTL = 2 * time.Second

// Settings are the Portal settings the route and the builder read: the portal block of node.yaml
// (survival-portal.md section 3.4) and the profile's portal language.
type Settings struct {
	// Language is portal.language: auto, en or es; "" counts as auto.
	Language string
	// EmergencyNumber is portal.emergency_number; "" means DefaultEmergencyNumber.
	EmergencyNumber string
	// ProfileLanguage is profile.survival.portal_language (en, es or empty), the first step of "auto".
	ProfileLanguage string
}

// portalBlock is the part of the portal block of node.yaml that the status reads.
type portalBlock struct {
	Language        string `yaml:"language"`
	EmergencyNumber string `yaml:"emergency_number"`
}

// SettingsFrom reads the settings from node.yaml and the catalog. An unreadable or absent block
// gives the defaults: the status must work with a broken config.
func SettingsFrom(cfg config.Config, cat *catalog.Catalog) Settings {
	var s Settings
	if raw, ok := cfg.Blocks["portal"]; ok {
		var b portalBlock
		if err := raw.Decode(&b); err == nil {
			s.Language, s.EmergencyNumber = b.Language, b.EmergencyNumber
		}
	}
	if cat != nil {
		if p, ok := cat.Profile(cfg.Profile); ok {
			s.ProfileLanguage = p.Survival.PortalLanguage
		}
	}
	return s
}

// resolveLang is the language of the body. In order: the ?lang= parameter, portal.language when it is
// en or es (auto skips this step), the profile's portal_language, then fallback, which is the
// request's own language (Accept-Language or ui.language, agent.md section 3.7).
func resolveLang(query string, s Settings, fallback string) string {
	for _, l := range []string{query, s.Language, s.ProfileLanguage} {
		if validLang(l) {
			return l
		}
	}
	if validLang(fallback) {
		return fallback
	}
	return doctor.LangEN
}

func validLang(l string) bool { return l == doctor.LangEN || l == doctor.LangES }

// RouteOptions configures Route.
type RouteOptions struct {
	// Builder builds the body; required.
	Builder StatusBuilder
	// Settings supplies the language settings; nil means the defaults (auto).
	Settings func() Settings
	// TTL overrides CacheTTL; Now is the clock (default time.Now).
	TTL time.Duration
	Now func() time.Time
	// Metrics counts the requests (localai_portal_status_requests_total); nil ignores them.
	Metrics *Metrics
}

// Route returns GET /portal/status: no auth, 5 s, a 2 s cache keyed by language and session.
func Route(o RouteOptions) api.Route {
	h := &statusHandler{opt: o, cache: map[string]cached{}}
	if h.opt.TTL <= 0 {
		h.opt.TTL = CacheTTL
	}
	if h.opt.Now == nil {
		h.opt.Now = time.Now
	}
	return api.Route{
		Method: http.MethodGet, Pattern: "/portal/status", Auth: []api.Level{api.AuthNone},
		Timeout: 5 * time.Second, Handler: api.HandlerFunc(h.serve),
	}
}

// Register adds the route to reg (api.DefaultRegistry when nil). The wiring in internal/app calls it
// (AGENT-20); the package does not register anything at init time.
func Register(reg *api.Registry, o RouteOptions) error {
	if o.Builder == nil {
		return errors.New("portal: the status route needs a builder")
	}
	if reg == nil {
		reg = api.DefaultRegistry
	}
	return reg.Add(Route(o))
}

type cached struct {
	at   time.Time
	body []byte
}

type statusHandler struct {
	opt   RouteOptions
	mu    sync.Mutex
	cache map[string]cached
}

func (h *statusHandler) serve(w http.ResponseWriter, r *http.Request) error {
	var q string
	if v, ok := r.URL.Query()["lang"]; ok {
		q = v[0]
		if !validLang(q) {
			return api.NewError(api.CodeInvalidRequest, http.StatusBadRequest, map[string]any{
				"field": "lang", "reason": "invalid_value", "allowed": []string{doctor.LangEN, doctor.LangES},
			})
		}
	}
	info := api.InfoFrom(r.Context())
	var settings Settings
	if h.opt.Settings != nil {
		settings = h.opt.Settings()
	}
	h.opt.Metrics.StatusRequest(sessionOf(info.Principal))
	req := Request{Lang: resolveLang(q, settings, info.Lang), Session: sessionOf(info.Principal), Now: h.opt.Now()}

	body, err := h.body(r, req)
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
	return nil
}

// body returns the marshalled body of req from the cache, or builds and stores it.
func (h *statusHandler) body(r *http.Request, req Request) ([]byte, error) {
	key := req.Lang + "|" + req.Session
	h.mu.Lock()
	defer h.mu.Unlock()
	if c, ok := h.cache[key]; ok && req.Now.Sub(c.at) < h.opt.TTL && !req.Now.Before(c.at) {
		return c.body, nil
	}
	st, err := h.opt.Builder.Build(r.Context(), req)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(st); err != nil {
		return nil, err
	}
	h.cache[key] = cached{at: req.Now, body: buf.Bytes()}
	return buf.Bytes(), nil
}

// sessionOf is what the SPA may call with the credential of the request.
func sessionOf(p api.Principal) string {
	switch p.Kind {
	case api.KindPortal:
		return SessionPortal
	case api.KindOperator, api.KindAdmin:
		return SessionFull
	}
	return SessionNone
}
