// Package modelsapi serves the model and key routes of the agent: GET /models, GET /models/{id},
// POST /models/{id}/download, DELETE /models/{id}, GET /key and POST /key/rotate (agent.md sections
// 3.8.7, 3.8.11, 3.8.12, 3.8.13 and 3.8.14).
//
// It is a package of its own, like internal/statusapi, because the routes compose the catalog, the
// router and the sync engine, and internal/router imports internal/api (an import of router from api
// would be a cycle). The load, unload and default routes stay in internal/api (models_ops.go, over
// router.HTTPOps).
//
// Everything the routes do is a method of Service (List, Get, Download, Delete, ShowKey, RotateKey):
// the HTTP handlers only decode and encode, so the cloud command executors (AGENT-18) call the same
// methods with the cloud actor and get the same checks, audit lines and typed *api.Error values. The
// daemon wiring (AGENT-20) builds one Service with New and adds its routes to the server's registry
// with Register; nothing registers at init time.
//
// Rules this package keeps:
//
//   - The llama key is read by ShowKey and written by RotateKey and by nothing else. It is never
//     logged, never put in an error, an event, an audit line or a state file; every one of those
//     shows the fingerprint ("ab12…9f3c"). ShowKey is the only place that returns it.
//   - A model delete and a key rotation run under modelOpsMu (router.OpLock), so they never overlap a
//     load, an unload, a reload or a llama restart. A busy lock is 409 LAI-AGT-022 with the holder's
//     op id, and nothing is touched.
//   - Deleting a copy that would leave the model with no verified copy is refused unless the caller
//     says keep_last=false; the check itself is the sync engine's (ark-sync.md section 4.10).
package modelsapi

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/api"
	"github.com/marinoscar/local-ai/agent/internal/audit"
	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/router"
	"github.com/marinoscar/local-ai/agent/internal/state"
	"github.com/marinoscar/local-ai/agent/internal/statusapi"
)

// OpLocker is modelOpsMu without waiting: *router.OpLock implements it.
type OpLocker interface {
	TryAcquire(kind string) (router.Hold, error)
}

// Restarter restarts llama and restores the models under modelOpsMu: *router.Orchestrator implements
// it (llama-router.md section 4.11).
type Restarter interface {
	RestartWith(ctx context.Context, req router.RestartRequest) (router.RestartResult, error)
}

// StateStore is the part of *state.Store that records the key's bookkeeping.
type StateStore interface {
	MutateSync(fn func(*state.State) error) error
}

// TailnetView gives the node's tailnet base URL, "" when there is none: *modes.TailscaleManager
// implements it.
type TailnetView interface {
	TailnetURL(ctx context.Context) string
}

// Deps are what the routes and the Service need. Config, Catalog, Models, Sync, Lock, Keys and
// Restarter are required (New and Register refuse a Deps without them); everything else is optional
// and its absence has the documented effect.
type Deps struct {
	Config  statusapi.ConfigView
	Catalog *statusapi.CatalogStore
	// Models is the merge of GET /models (*statusapi.ModelMerger); it reads cached values only.
	Models statusapi.ModelView
	// Mode says whether a download may start and whether the cloud can be told about a rotation; nil
	// means online.
	Mode statusapi.ModeView
	// Sync is the sync engine (ark-sync.md): downloads and the keep-last delete.
	Sync SyncEngine
	// Lock is modelOpsMu, shared with the orchestrator and the generator.
	Lock OpLocker
	// Regenerator regenerates models.ini after a delete (llama-router.md section 4.2); nil skips it.
	Regenerator statusapi.Regenerator

	// Keys reads the llama key (router.NewFileKey). When it also has Invalidate (router.FileKey), it
	// is told to re-read after a rotation.
	Keys router.KeySource
	// KeyFS and KeyPath say where RotateKey writes the key: the real filesystem and
	// router.DefaultKeyPath when empty.
	KeyFS   config.FS
	KeyPath string
	// Rand is the entropy of a new key; nil means crypto/rand.
	Rand io.Reader
	// SetLiveKey tells the log scrubber the live key after a rotation; nil means logx.SetCurrentKey.
	SetLiveKey func(key string)
	// Restarter restarts llama and restores the models for a rotation with restart true.
	Restarter Restarter
	// State records state.key; nil skips it.
	State StateStore
	// Pusher reports the new key to the cloud (AGENT-17); nil means the node is not linked, so
	// cloud_synced is false.
	Pusher KeyPusher
	// Tailnet gives the tailnet URL of GET /key; nil means none.
	Tailnet TailnetView

	// Publisher receives key.rotated; Alerts gets LAI-AGT-016 after a rotation without a restart;
	// Audit receives key.show, key.rotate and model.delete. Each may be nil.
	Publisher router.Publisher
	Alerts    router.AlertSink
	Audit     api.AuditWriter

	// Now defaults to time.Now; Logger to slog.Default.
	Now    func() time.Time
	Logger *slog.Logger
}

func (d Deps) validate() error {
	switch {
	case d.Config == nil || d.Catalog == nil || d.Models == nil:
		return errors.New("modelsapi: the routes need a config view, a catalog store and a model view")
	case d.Sync == nil:
		return errors.New("modelsapi: the routes need a sync engine")
	case d.Lock == nil:
		return errors.New("modelsapi: the routes need modelOpsMu (an OpLocker)")
	case d.Keys == nil:
		return errors.New("modelsapi: the routes need a key source")
	case d.Restarter == nil:
		return errors.New("modelsapi: key rotation needs a Restarter")
	}
	return nil
}

// Service is what the routes and the cloud executors call. Create it with New.
type Service struct{ d Deps }

// New returns a Service over d, or an error when a required dependency is missing.
func New(d Deps) (*Service, error) {
	if err := d.validate(); err != nil {
		return nil, err
	}
	return &Service{d: d}, nil
}

func (s *Service) now() time.Time {
	if s.d.Now != nil {
		return s.d.Now()
	}
	return time.Now()
}

func (s *Service) log() *slog.Logger {
	if s.d.Logger != nil {
		return s.d.Logger.With("component", "modelsapi")
	}
	return slog.Default().With("component", "modelsapi")
}

// Route timeouts (agent.md section 3.8).
const (
	readTimeout     = 5 * time.Second
	downloadTimeout = 10 * time.Second
	deleteTimeout   = 60 * time.Second
	rotateTimeout   = 15 * time.Minute
)

// Routes returns GET /models (op, 5 s), GET /models/{id} (op, 5 s), POST /models/{id}/download (op,
// 10 s), DELETE /models/{id} (admin, 60 s), GET /key (op, 5 s) and POST /key/rotate (admin, 15 min).
func (s *Service) Routes() []api.Route {
	return []api.Route{
		{Method: http.MethodGet, Pattern: "/models", Auth: []api.Level{api.AuthOp}, Timeout: readTimeout, Handler: api.HandlerFunc(s.listModels)},
		{Method: http.MethodGet, Pattern: "/models/{id}", Auth: []api.Level{api.AuthOp}, Timeout: readTimeout, Handler: api.HandlerFunc(s.getModel)},
		{Method: http.MethodPost, Pattern: "/models/{id}/download", Auth: []api.Level{api.AuthOp}, Timeout: downloadTimeout, Handler: api.HandlerFunc(s.downloadModel)},
		{Method: http.MethodDelete, Pattern: "/models/{id}", Auth: []api.Level{api.AuthAdmin}, Timeout: deleteTimeout, Handler: api.HandlerFunc(s.deleteModel)},
		{Method: http.MethodGet, Pattern: "/key", Auth: []api.Level{api.AuthOp}, Timeout: readTimeout, Handler: api.HandlerFunc(s.getKey)},
		{Method: http.MethodPost, Pattern: "/key/rotate", Auth: []api.Level{api.AuthAdmin}, Timeout: rotateTimeout, Handler: api.HandlerFunc(s.rotateKey)},
	}
}

// Routes returns the routes of a Service over d without validating d: the handlers of a route whose
// dependency is missing fail with a 500. Tests and the OpenAPI conformance test use it for the
// route metadata; the daemon uses Register.
func Routes(d Deps) []api.Route { return (&Service{d: d}).Routes() }

// Register adds the routes of s to reg (api.DefaultRegistry when nil). The wiring in internal/app
// calls it (AGENT-20).
func (s *Service) Register(reg *api.Registry) error {
	if reg == nil {
		reg = api.DefaultRegistry
	}
	return reg.Add(s.Routes()...)
}

// Register builds a Service over d and adds its routes to reg. The wiring calls New and
// (*Service).Register instead when the cloud executors need the same Service.
func Register(reg *api.Registry, d Deps) error {
	s, err := New(d)
	if err != nil {
		return err
	}
	return s.Register(reg)
}

// lang is the language of the request (the cloud executors run with the default, English).
func lang(ctx context.Context) string { return api.InfoFrom(ctx).Lang }

// auditWrite records ev, logging a failure to write instead of failing the request: the audit log
// mirrors every line to the journal, so the record exists even when the file cannot be written.
func (s *Service) auditWrite(ctx context.Context, action string, result audit.Result, code, opID string, actor audit.Actor, target map[string]string) {
	if s.d.Audit == nil {
		return
	}
	ev := audit.Event{
		Action: action, Result: result, Code: code, Actor: actor, Target: target,
		OpID: opID, ReqID: api.InfoFrom(ctx).ID,
	}
	if err := s.d.Audit.Write(ev); err != nil {
		s.log().Warn("audit write failed", "action", action, "error", err.Error())
	}
}
