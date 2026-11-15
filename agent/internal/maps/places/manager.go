package places

import (
	"context"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/marinoscar/local-ai/agent/internal/ark"
)

// DefaultItem is the catalog item the index is built from when the node's profile names none.
const DefaultItem = "places-us"

// State is the state of the place index as the routes report it.
type State string

const (
	// StateAbsent: the item has no verified copy on a mounted root (LAI-SVC-032).
	StateAbsent State = "absent"
	// StateBuilding: the first build is running; searches answer 503 {"state":"building"}.
	StateBuilding State = "building"
	// StateReady: an index is serving.
	StateReady State = "ready"
	// StateFailed: the copy is there but could not be read.
	StateFailed State = "failed"
)

// Manager owns the index of one places item: it builds it from the item's best copy and swaps the
// finished index in with one atomic pointer store, so a search never sees a half-built index.
type Manager struct {
	// Item is the catalog item id when Items is nil; default DefaultItem.
	Item string
	// Items lists the places items the node wants, best first (the profile's places-* items, issue
	// #586). The index is built from the first one that has a copy. Nil or empty means Item.
	Items func() []string
	// Copies finds the best copy of the item (the ark index).
	Copies ark.CopyFinder
	Log    *slog.Logger
	// BuildFn builds an index from zip files; default Build. Tests replace it.
	BuildFn func(ctx context.Context, zips []string) (*Index, error)

	cur   atomic.Pointer[Index]
	state atomic.Value // State
	built atomic.Pointer[builtFrom]
	src   atomic.Value // string: the item the serving index was built from
	mu    sync.Mutex   // serialises Rebuild
}

type builtFrom struct{ item, dir, version string }

func (m *Manager) item() string {
	if m.Item == "" {
		return DefaultItem
	}
	return m.Item
}

// candidates are the items to try, best first.
func (m *Manager) candidates() []string {
	if m.Items != nil {
		if ids := m.Items(); len(ids) > 0 {
			return ids
		}
	}
	return []string{m.item()}
}

func (m *Manager) log() *slog.Logger {
	if m.Log != nil {
		return m.Log
	}
	return slog.Default()
}

// State returns the current state. Until the first Rebuild has decided whether a copy exists it is
// StateBuilding.
func (m *Manager) State() State {
	if s, ok := m.state.Load().(State); ok {
		return s
	}
	return StateBuilding
}

// Index returns the serving index, nil when there is none.
func (m *Manager) Index() *Index { return m.cur.Load() }

// Source is the catalog item id the serving index comes from; before any index is built it is the
// first candidate.
func (m *Manager) Source() string {
	if s, ok := m.src.Load().(string); ok && s != "" {
		return s
	}
	return m.candidates()[0]
}

// RowsOf is the row count behind the serving index when it was built from itemID, else 0: the
// plausibility check of a places item compares only with that same item's previous version.
func (m *Manager) RowsOf(itemID string) int {
	if s, ok := m.src.Load().(string); ok && s == itemID {
		return m.Rows()
	}
	return 0
}

// Rows is the total row count of the dumps behind the serving index (0 with none); the plausibility
// check of the next version compares with it.
func (m *Manager) Rows() int { return m.cur.Load().TotalRows() }

// Rebuild looks at the best copy of the item and rebuilds the index when it changed since the last
// build. With no copy the index is dropped (the drive was ejected) and the state is absent. While the
// first build runs the state is building; a rebuild keeps serving the old index until the new one is
// swapped in. A failed build keeps the old index.
func (m *Manager) Rebuild(ctx context.Context) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var cp ark.Copy
	itemID := ""
	err := ark.ErrNotPresent
	if m.Copies != nil {
		for _, id := range m.candidates() {
			if cp, err = m.Copies.BestCopy(id); err == nil {
				itemID = id
				break
			}
		}
	}
	if err != nil {
		m.cur.Store(nil)
		m.built.Store(nil)
		m.src.Store("")
		m.state.Store(StateAbsent)
		return
	}
	from := &builtFrom{item: itemID, dir: cp.Dir, version: cp.Manifest.Version}
	if b := m.built.Load(); b != nil && *b == *from && m.cur.Load() != nil {
		return
	}
	var zips []string
	for _, p := range cp.Files {
		if strings.HasSuffix(strings.ToLower(p), ".zip") && filepath.IsAbs(p) {
			zips = append(zips, p)
		}
	}
	if m.cur.Load() == nil {
		m.state.Store(StateBuilding)
	}
	build := m.BuildFn
	if build == nil {
		build = Build
	}
	ix, err := build(ctx, zips)
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		m.log().Warn("the place index could not be built", "item", itemID, "error", err.Error())
		if m.cur.Load() == nil {
			m.state.Store(StateFailed)
		}
		return
	}
	m.cur.Store(ix)
	m.built.Store(from)
	m.src.Store(itemID)
	m.state.Store(StateReady)
	m.log().Info("the place index is ready", "item", itemID, "places", ix.Len())
}
