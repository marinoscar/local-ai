package maps

import (
	"encoding/json"
	"net/http"
)

// CatalogMap is one entry of "maps" in /maps/catalog.json (companion-services.md section 4.4.2).
type CatalogMap struct {
	ID            string     `json:"id"`
	DisplayName   string     `json:"display_name"`
	DisplayNameES string     `json:"display_name_es"`
	URL           string     `json:"url"`
	Version       string     `json:"version"`
	Bounds        [4]float64 `json:"bounds"`
	MinZoom       int        `json:"minzoom"`
	MaxZoom       int        `json:"maxzoom"`
	Center        [3]float64 `json:"center"`
	Default       bool       `json:"default"`
	Root          string     `json:"root"`
}

// CatalogAssets is the "assets" block.
type CatalogAssets struct {
	Available bool     `json:"available"`
	Glyphs    string   `json:"glyphs"`
	Sprite    string   `json:"sprite"`
	Fonts     []string `json:"fonts"`
}

// CatalogSearch is the "search" block.
type CatalogSearch struct {
	Available bool    `json:"available"`
	Places    int     `json:"places"`
	Source    *string `json:"source"`
}

// CatalogGPS is the "gps" block (SVC-12 fills it).
type CatalogGPS struct {
	Available bool    `json:"available"`
	Source    *string `json:"source"`
}

// Catalog is the body of GET /maps/catalog.json.
type Catalog struct {
	Maps   []CatalogMap  `json:"maps"`
	Assets CatalogAssets `json:"assets"`
	Search CatalogSearch `json:"search"`
	GPS    CatalogGPS    `json:"gps"`
}

// Catalog builds the catalog from the best copies and their headers. A map with a bad header is not
// listed.
func (s *Maps) Catalog() Catalog {
	l := s.list()
	c := Catalog{
		Maps: []CatalogMap{},
		Assets: CatalogAssets{Available: s.assetsPresent(), Glyphs: GlyphsTemplate, Sprite: SpritePath,
			Fonts: append([]string{}, FontStacks...)},
	}
	for _, m := range l.Maps {
		h := m.Header
		es := m.Item.DisplayNameES
		if es == "" {
			es = m.Item.DisplayName
		}
		c.Maps = append(c.Maps, CatalogMap{
			ID: m.Item.ID, DisplayName: m.Item.DisplayName, DisplayNameES: es,
			URL: TilesPrefix + m.Item.ID + ".pmtiles", Version: m.Version,
			Bounds:  [4]float64{h.MinLon, h.MinLat, h.MaxLon, h.MaxLat},
			MinZoom: int(h.MinZoom), MaxZoom: int(h.MaxZoom),
			Center:  [3]float64{h.CenterLon, h.CenterLat, float64(h.CenterZoom)},
			Default: m.Item.ID == l.Default, Root: m.Copy.RootID,
		})
	}
	if g := s.gps.Load(); g != nil && g.p != nil {
		avail, src := g.p.CatalogGPS()
		c.GPS.Available = avail
		if src != "" && src != SourceNone {
			c.GPS.Source = &src
		}
	}
	if si := s.searchInfo(); si.Available {
		src := si.Source
		c.Search = CatalogSearch{Available: true, Places: si.Places, Source: &src}
	}
	return c
}

// serveCatalog is GET|HEAD /maps/catalog.json.
func (s *Maps) serveCatalog(w http.ResponseWriter, r *http.Request) error {
	body, err := json.Marshal(s.Catalog())
	if err != nil {
		return err
	}
	h := w.Header()
	h.Set("Content-Type", "application/json; charset=utf-8")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(body)
	}
	return nil
}

// GPSProvider is "where am I" as the catalog sees it (*Service of whereami.go implements it).
type GPSProvider interface {
	CatalogGPS() (available bool, source string)
}

type gpsHolder struct{ p GPSProvider }

// SetGPS registers the position source for the catalog's gps block.
func (s *Maps) SetGPS(p GPSProvider) { s.gps.Store(&gpsHolder{p: p}) }
