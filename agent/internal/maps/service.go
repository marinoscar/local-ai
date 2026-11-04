package maps

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/marinoscar/local-ai/agent/internal/ark"
	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/svc/health"
)

// ServiceName is the name of the maps service in the health supervisor and in GET /services.
const ServiceName = "maps"

// Paths of the survival-set routes (companion-services.md section 3.5).
const (
	TilesPrefix  = "/maps/tiles/"
	AssetsPrefix = "/maps/assets/"
	CatalogPath  = "/maps/catalog.json"
)

// Config is the part of the maps block of node.yaml (companion-services.md section 3.1) this package
// reads. The other keys (gps, default_center) belong to the location code.
type Config struct {
	Enabled bool `yaml:"enabled"`
	// DefaultMap is "auto" (the tier-1 map of the profile) or a content id.
	DefaultMap string `yaml:"default_map"`
}

// DefaultConfig is the block as written when node.yaml has none.
func DefaultConfig() Config { return Config{Enabled: true, DefaultMap: "auto"} }

// FromConfig reads the maps block of a loaded node.yaml; a missing or unreadable block is the
// defaults (the maps are on unless node.yaml says no).
func FromConfig(cfg config.Config) Config {
	c := DefaultConfig()
	if raw, ok := cfg.Blocks["maps"]; ok && len(raw) > 0 {
		if err := raw.Decode(&c); err != nil {
			return DefaultConfig()
		}
	}
	if c.DefaultMap == "" {
		c.DefaultMap = "auto"
	}
	return c
}

// MapItem is a map the catalog knows (kind maps, source pmtiles_extract). It says nothing about the
// disk: whether the map is installed is the ark index's answer.
type MapItem struct {
	ID            string
	DisplayName   string
	DisplayNameES string
	Tier          int
	// InProfile is true when the node's profile lists the item in include.content.
	InProfile bool
}

// SearchInfo is what the place index tells /maps/catalog.json (SVC-11 registers the provider).
type SearchInfo struct {
	Available bool
	Places    int
	Source    string
}

// SearchProvider is the place index as the catalog and the health probe see it.
type SearchProvider interface {
	SearchInfo() SearchInfo
}

// Deps are what the maps service needs. Copies and Items are required.
type Deps struct {
	// Copies finds the best verified copy of a map item and of map-assets (the ark index).
	Copies ark.CopyFinder
	// Items lists the catalog's maps, asked on every request (the catalog may be refreshed).
	Items func() []MapItem
	// Config returns the maps block as it is now; nil means DefaultConfig.
	Config func() Config
	// Metrics receives localai_maps_tile_bytes_total; nil ignores it.
	Metrics *health.Metrics
	Logger  *slog.Logger
}

// Maps is the maps service: the routes and the health probe share it.
type Maps struct {
	copies  ark.CopyFinder
	items   func() []MapItem
	cfg     func() Config
	metrics *health.Metrics
	log     *slog.Logger

	search atomic.Pointer[searchHolder]
	gps    atomic.Pointer[gpsHolder]

	mu      sync.Mutex
	headers map[headerKey]Header
}

type searchHolder struct{ p SearchProvider }

type headerKey struct{ path, sha string }

// maxCachedHeaders bounds the header cache; it is cleared when it grows past this.
const maxCachedHeaders = 64

// New returns the Maps service.
func New(d Deps) *Maps {
	s := &Maps{copies: d.Copies, items: d.Items, cfg: d.Config, metrics: d.Metrics, log: d.Logger,
		headers: map[headerKey]Header{}}
	if s.cfg == nil {
		s.cfg = DefaultConfig
	}
	if s.items == nil {
		s.items = func() []MapItem { return nil }
	}
	if s.log == nil {
		s.log = slog.Default()
	}
	s.log = s.log.With("component", "maps")
	return s
}

// SetSearch registers the place index (SVC-11). Until then the catalog says search is not available.
func (s *Maps) SetSearch(p SearchProvider) { s.search.Store(&searchHolder{p: p}) }

func (s *Maps) searchInfo() SearchInfo {
	if h := s.search.Load(); h != nil && h.p != nil {
		return h.p.SearchInfo()
	}
	return SearchInfo{}
}

// Enabled reports whether node.yaml turns the maps on.
func (s *Maps) Enabled() bool { return s.cfg().Enabled }

// Errors of resolving a map.
var (
	// ErrNoMap: the id is not a map of the catalog, or it has no verified copy on a mounted root.
	ErrNoMap = errors.New("maps: no verified copy of that map")
	// ErrNoFile: the copy has no usable .pmtiles file.
	ErrNoFile = errors.New("maps: the map copy has no usable .pmtiles file")
)

// resolved is a map with a verified copy and a parsed header.
type resolved struct {
	Item    MapItem
	Copy    ark.Copy
	Path    string
	Header  Header
	ETag    string
	Version string
}

// resolve finds the best copy of one map item and reads its header (from the cache when the file
// and its checksum are the ones seen before). The error is ErrNoMap, ErrNoFile or wraps
// ErrBadHeader.
func (s *Maps) resolve(it MapItem) (resolved, error) {
	cp, err := s.copies.BestCopy(it.ID)
	if err != nil {
		return resolved{}, fmt.Errorf("%w: %v", ErrNoMap, err)
	}
	path, sum := "", ""
	if len(cp.Files) == len(cp.Manifest.Files) {
		for i, f := range cp.Manifest.Files {
			if strings.HasSuffix(strings.ToLower(f.Path), ".pmtiles") && inside(cp.Dir, cp.Files[i]) {
				path, sum = cp.Files[i], f.SHA256
				break
			}
		}
	}
	if path == "" {
		return resolved{}, ErrNoFile
	}
	h, err := s.header(path, sum)
	if err != nil {
		return resolved{}, err
	}
	return resolved{Item: it, Copy: cp, Path: path, Header: h, ETag: etagOf(sum, cp.Manifest.Version, path), Version: cp.Manifest.Version}, nil
}

func (s *Maps) header(path, sum string) (Header, error) {
	k := headerKey{path, sum}
	s.mu.Lock()
	h, ok := s.headers[k]
	s.mu.Unlock()
	if ok {
		return h, nil
	}
	h, err := ReadHeader(path)
	if err != nil {
		if !errors.Is(err, ErrBadHeader) {
			err = fmt.Errorf("%w: %v", ErrBadHeader, err) // an unreadable file is a damaged one
		}
		return Header{}, err
	}
	s.mu.Lock()
	if len(s.headers) >= maxCachedHeaders {
		s.headers = map[headerKey]Header{}
	}
	s.headers[k] = h
	s.mu.Unlock()
	return h, nil
}

// etagOf is the strong validator of a map file: the first 16 hex digits of its sha256 from the
// MANIFEST (a new version has a new sum). A manifest without a usable sum falls back to a hash of
// the version and the path, which still changes with the version.
func etagOf(sum, version, path string) string {
	if len(sum) >= 16 {
		if _, err := hex.DecodeString(sum[:16]); err == nil {
			return `"` + strings.ToLower(sum[:16]) + `"`
		}
	}
	d := sha256.Sum256([]byte(version + "\x00" + path))
	return `"` + hex.EncodeToString(d[:8]) + `"`
}

// inside reports whether p is an absolute path below dir.
func inside(dir, p string) bool {
	if !filepath.IsAbs(p) || filepath.Clean(p) != p {
		return false
	}
	rel, err := filepath.Rel(dir, p)
	return err == nil && rel != "." && !strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel)
}

// mapItems returns the catalog's maps in the order the viewer lists them: the profile's first, then
// by survival tier, then by id.
func (s *Maps) mapItems() []MapItem {
	its := append([]MapItem(nil), s.items()...)
	sort.SliceStable(its, func(i, j int) bool {
		a, b := its[i], its[j]
		if a.InProfile != b.InProfile {
			return a.InProfile
		}
		if a.Tier != b.Tier {
			return a.Tier < b.Tier
		}
		return a.ID < b.ID
	})
	return its
}

func (s *Maps) item(id string) (MapItem, bool) {
	for _, it := range s.mapItems() {
		if it.ID == id {
			return it, true
		}
	}
	return MapItem{}, false
}

// listing is the state of every map of the catalog right now.
type listing struct {
	// Maps are the maps with a verified copy and a good header, in viewer order.
	Maps []resolved
	// Damaged are the ids whose copy has a bad header or no pmtiles file (LAI-SVC-034).
	Damaged []string
	// Installed counts the maps that have a verified copy at all (good or damaged).
	Installed int
	// Default is the id of the default map, "" when there is none.
	Default string
}

func (s *Maps) list() listing {
	var l listing
	for _, it := range s.mapItems() {
		r, err := s.resolve(it)
		switch {
		case err == nil:
			l.Installed++
			l.Maps = append(l.Maps, r)
		case errors.Is(err, ErrNoMap):
		default:
			l.Installed++
			l.Damaged = append(l.Damaged, it.ID)
			s.log.Warn("a map file is damaged", "map", it.ID, "code", doctor.SVC034, "error", err.Error())
		}
	}
	want := s.cfg().DefaultMap
	for _, r := range l.Maps {
		if want != "" && want != "auto" && r.Item.ID == want {
			l.Default = r.Item.ID
		}
	}
	if l.Default == "" && len(l.Maps) > 0 {
		// auto, or a configured map that is not installed: the first map in viewer order, which puts
		// the profile's tier-1 map first.
		l.Default = l.Maps[0].Item.ID
	}
	return l
}
