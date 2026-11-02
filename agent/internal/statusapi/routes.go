// Package statusapi serves GET /status, GET and PUT /profile, GET /catalog and POST /catalog/refresh
// (agent.md sections 3.8.4, 3.8.21 and 3.8.30).
//
// It is a package of its own rather than files of internal/api because the routes compose views of
// the hardware, router and catalog packages, and internal/hardware imports internal/api (an import
// of catalog or hardware from api would be a cycle). The routes are built by constructors and added
// to the server's registry by the daemon wiring (AGENT-20); nothing registers at init time.
//
// The builders see every subsystem through the small interfaces of views.go, all optional, so the
// routes answer on a node whose router, Tailscale or cloud link is down. They never call an upstream
// service from a handler: views return cached values.
package statusapi

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/api"
	"github.com/marinoscar/local-ai/agent/internal/audit"
	"github.com/marinoscar/local-ai/agent/internal/config"
	"gopkg.in/yaml.v3"
)

// ConfigStore is node.yaml: reads for every route, the comment-preserving atomic update for PUT
// /profile. *config.Store implements it.
type ConfigStore interface {
	ConfigView
	Update(fn func(root *yaml.Node) error) error
}

// Deps are what the routes need. Config and Catalog are required; everything else is optional.
type Deps struct {
	Config  ConfigStore
	Catalog *CatalogStore

	// Views of GET /status; see StatusSources. Config and Catalog above are used for them too.
	State     StateView
	Hardware  HardwareView
	Mode      ModeView
	Services  ServiceView
	Models    ModelView
	Ark       ArkView
	Cloud     CloudView
	Tailscale TailscaleView
	Alerts    AlertView
	Counters  CounterView
	CheckedAt func() time.Time
	Version   string
	Hostname  func() (string, error)
	StartedAt time.Time

	// Content says what the roots hold of each catalog content item (GET /catalog).
	Content ContentView
	// Refresher fetches and installs a catalog (POST /catalog/refresh); *catalog.Fetcher.
	Refresher CatalogRefresher
	// Regenerator regenerates models.ini and reloads the router after PUT /profile and after a
	// changed catalog refresh; nil skips it.
	Regenerator Regenerator
	// Audit records profile.set and catalog.refresh; optional.
	Audit api.AuditWriter
	// Now defaults to time.Now.
	Now func() time.Time
	// Logger defaults to slog.Default.
	Logger *slog.Logger
}

func (d Deps) validate() error {
	if d.Config == nil || d.Catalog == nil {
		return errors.New("statusapi: the routes need a config store and a catalog store")
	}
	if d.Refresher == nil {
		return errors.New("statusapi: POST /catalog/refresh needs a catalog refresher")
	}
	return nil
}

func (d Deps) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

func (d Deps) log() *slog.Logger {
	if d.Logger != nil {
		return d.Logger.With("component", "statusapi")
	}
	return slog.Default().With("component", "statusapi")
}

// Builder returns the StatusBuilder of these dependencies: the handler's, and the source of the
// state snapshot the cloud link pushes (Build with forCloud true, AGENT-17).
func (d Deps) Builder() *StatusBuilder {
	var cfg ConfigView
	if d.Config != nil {
		cfg = d.Config
	}
	return NewStatusBuilder(StatusSources{
		Config: cfg, State: d.State, Hardware: d.Hardware, Mode: d.Mode, Services: d.Services,
		Models: d.Models, Ark: d.Ark, Cloud: d.Cloud, Tailscale: d.Tailscale, Alerts: d.Alerts,
		Counters: d.Counters, Catalog: d.Catalog, CheckedAt: d.CheckedAt, Version: d.Version,
		Hostname: d.Hostname, StartedAt: d.StartedAt, Now: d.Now,
	})
}

// Routes returns GET /status (op, 5 s), GET /profile (op, 5 s), PUT /profile (admin, 30 s),
// GET /catalog (op, 5 s) and POST /catalog/refresh (op, 120 s). It does not validate d: the
// handlers of a route whose dependency is missing answer 500, and Register refuses such a d.
func Routes(d Deps) []api.Route {
	h := &handlers{Deps: d, status: d.Builder()}
	return []api.Route{
		{Method: http.MethodGet, Pattern: "/status", Auth: []api.Level{api.AuthOp}, Timeout: 5 * time.Second, Handler: api.HandlerFunc(h.getStatus)},
		{Method: http.MethodGet, Pattern: "/profile", Auth: []api.Level{api.AuthOp}, Timeout: 5 * time.Second, Handler: api.HandlerFunc(h.getProfile)},
		{Method: http.MethodPut, Pattern: "/profile", Auth: []api.Level{api.AuthAdmin}, Timeout: 30 * time.Second, Handler: api.HandlerFunc(h.putProfile)},
		{Method: http.MethodGet, Pattern: "/catalog", Auth: []api.Level{api.AuthOp}, Timeout: 5 * time.Second, Handler: api.HandlerFunc(h.getCatalog)},
		{Method: http.MethodPost, Pattern: "/catalog/refresh", Auth: []api.Level{api.AuthOp}, Timeout: 120 * time.Second, Handler: api.HandlerFunc(h.refreshCatalog)},
	}
}

// Register adds the routes to reg (api.DefaultRegistry when nil). The wiring in internal/app calls
// it (AGENT-20).
func Register(reg *api.Registry, d Deps) error {
	if err := d.validate(); err != nil {
		return err
	}
	if reg == nil {
		reg = api.DefaultRegistry
	}
	return reg.Add(Routes(d)...)
}

type handlers struct {
	Deps
	status *StatusBuilder
}

func (h *handlers) getStatus(w http.ResponseWriter, r *http.Request) error {
	counters, err := boolQuery(r, "counters")
	if err != nil {
		return err
	}
	body := h.status.Build(r.Context(), false, BuildOptions{Lang: api.InfoFrom(r.Context()).Lang, Counters: counters})
	api.WriteJSON(w, r, http.StatusOK, body)
	return nil
}

// boolQuery reads a flag query parameter: 1 or true, 0 or false, or absent (false).
func boolQuery(r *http.Request, name string) (bool, error) {
	v := r.URL.Query().Get(name)
	if v == "" {
		return false, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return false, api.NewError(api.CodeInvalidRequest, http.StatusBadRequest, map[string]any{
			"field": name, "reason": "invalid_value", "allowed": []string{"0", "1"},
		})
	}
	return b, nil
}

// auditWrite records ev, logging a failure to write instead of failing the request.
func (h *handlers) auditWrite(info *api.RequestInfo, action string, result audit.Result, code string, target map[string]string) {
	if h.Audit == nil {
		return
	}
	ev := audit.Event{Action: action, Result: result, Code: code, Actor: api.AuditActor(info), Target: target, ReqID: info.ID}
	if err := h.Audit.Write(ev); err != nil {
		h.log().Warn("audit write failed", "action", action, "error", err.Error())
	}
}

// configOf is the node.yaml the request sees; the defaults when the store is missing.
func (h *handlers) configOf() config.Config {
	if h.Config == nil {
		return config.Defaults()
	}
	return h.Config.Get()
}
