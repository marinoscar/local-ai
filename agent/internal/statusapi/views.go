package statusapi

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/catalog"
	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/hardware"
	"github.com/marinoscar/local-ai/agent/internal/modes"
	"github.com/marinoscar/local-ai/agent/internal/services"
	"github.com/marinoscar/local-ai/agent/internal/state"
)

// The views are the small read interfaces the builders compose GET /status, GET /profile and
// GET /catalog from. Every one is optional: a nil or unavailable view renders its documented empty
// state, so the routes answer on a node whose router, Tailscale or cloud link is down, and during
// startup before every subsystem exists. No view may call an upstream service: they return values
// the owning subsystem already holds (agent.md section 3.8.4, "views return cached values only").

// ConfigView is node.yaml as the config store holds it; *config.Store implements it.
type ConfigView interface {
	Get() config.Config
}

// StateView is state.json as the state store holds it; *state.Store implements it.
type StateView interface {
	Snapshot() state.State
}

// HardwareView is the last hardware sample; hardware.Sampler (and so *hardware.Service) implements it.
type HardwareView interface {
	Snapshot() hardware.Snapshot
}

// ModeView is the mode engine's picture; *modes.Engine implements it.
type ModeView interface {
	Info() modes.Info
}

// ServiceView is the supervised services; *services.Watcher implements it.
type ServiceView interface {
	Snapshot(ctx context.Context) []services.Status
}

// TailscaleView is the Tailscale manager's cached picture; *modes.TailscaleManager implements it.
type TailscaleView interface {
	Status() modes.TailscaleStatus
}

// AlertView is the active alerts; *doctor.Manager implements it.
type AlertView interface {
	Active() []doctor.Alert
}

// ModelView is the catalog models merged with the router's state (see ModelMerger).
type ModelView interface {
	// Models returns every catalog model with its local state, descriptions in lang ("en" or "es").
	Models(lang string) ModelsView
}

// ModelsView is what a ModelView answers.
type ModelsView struct {
	// Rows are the models in catalog order.
	Rows []ModelRow
	// Default is the effective default model id, "" for none.
	Default string
	// ModelsMax is router.models_max of the node's profile (at least 1).
	ModelsMax int
}

// ArkView is the content roots and the sync engine (ark-sync.md); the ark slice implements it.
type ArkView interface {
	Ark() ArkInfo
}

// ArkInfo is what an ArkView answers. A zero time is JSON null.
type ArkInfo struct {
	Roots []ArkRoot
	// DriveMounted is true when an ark drive is mounted.
	DriveMounted bool
	// SyncState is idle, running, cancelling or the like; "" means idle.
	SyncState     string
	LastSuccessAt time.Time
}

// ArkRoot is one content root.
type ArkRoot struct {
	Path string
	// Kind is internal or drive.
	Kind string
	// Status is the READY.json overall colour or "unknown".
	Status    string
	ReadyPct  *int
	UpdatedAt time.Time
}

// CloudView is the cloud link client's cached state (agent.md section 4.8); *cloud.Link implements it.
type CloudView interface {
	Cloud() CloudInfo
}

// CloudInfo is what a CloudView answers.
type CloudInfo struct {
	Enrolled bool
	// State is unenrolled, registering, linked, backoff, disabled, revoked or stopped (section 4.8.1).
	State           string
	URL             string
	LastHeartbeatAt time.Time
}

// ContentView says what the ark roots hold of each content item; the ark slice implements it. A nil
// view reports every item as absent.
type ContentView interface {
	ContentState(id string) ContentState
}

// ContentState is one content item as the READY.json files of the mounted roots list it.
type ContentState struct {
	// Present is true when a verified copy (current or stale) is on a mounted root.
	Present  bool
	Verified bool
	// Status is the item status of ark-sync.md section 4.12.1: verified, stale, missing, skipped,
	// failed, quarantined or orphan; "" means absent.
	Status string
}

// CounterView supplies node.counters of ?counters=1 (agent.md section 7): requests by status class,
// model loads, transitions, cloud claims and failures, sync bytes. The keys belong to the producers;
// the builder passes the map through.
type CounterView interface {
	Counters() map[string]any
}

// CatalogRefresher fetches and installs a newer catalog; *catalog.Fetcher implements it.
type CatalogRefresher interface {
	Refresh(ctx context.Context) (*catalog.RefreshResult, error)
}

// CatalogStore holds the catalog the agent runs with and where it came from. The startup code fills
// it from catalog.Fetcher.Startup, a successful POST /catalog/refresh replaces it, and the router,
// the sync planner and every route read it, so they all see one catalog.
type CatalogStore struct {
	mu      sync.RWMutex
	cur     catalog.Loaded
	fetched time.Time
}

// CatalogState is a consistent read of a CatalogStore.
type CatalogState struct {
	// Catalog is never nil.
	Catalog *catalog.Catalog
	Source  catalog.Source
	// FetchedAt is when the catalog was installed; zero when unknown.
	FetchedAt time.Time
}

// NewCatalogStore returns a store holding l. A nil l.Catalog becomes the empty catalog.
func NewCatalogStore(l catalog.Loaded, fetchedAt time.Time) *CatalogStore {
	s := &CatalogStore{}
	s.Set(l, fetchedAt)
	return s
}

// Get returns the current catalog.
func (s *CatalogStore) Get() CatalogState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return CatalogState{Catalog: s.cur.Catalog, Source: s.cur.Source, FetchedAt: s.fetched}
}

// Set replaces the catalog.
func (s *CatalogStore) Set(l catalog.Loaded, fetchedAt time.Time) {
	if l.Catalog == nil {
		l.Catalog = catalog.Empty()
	}
	s.mu.Lock()
	s.cur, s.fetched = l, fetchedAt
	s.mu.Unlock()
}

// InstalledAt is when the catalog directory dir was installed: the modification time of its
// CATALOG_VERSION file, which Install writes last. It returns the zero time when dir has none (an
// empty catalog), so the caller can pass the result to NewCatalogStore at startup.
func InstalledAt(dir string) time.Time {
	if dir == "" {
		return time.Time{}
	}
	fi, err := os.Stat(filepath.Join(dir, "CATALOG_VERSION"))
	if err != nil {
		return time.Time{}
	}
	return fi.ModTime()
}
