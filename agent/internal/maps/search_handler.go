package maps

import (
	"encoding/json"
	"math"
	"net/http"
	"strconv"
	"strings"

	"github.com/marinoscar/local-ai/agent/internal/api"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/maps/places"
)

// Paths of the place routes.
const (
	SearchPath  = "/maps/search"
	NearestPath = "/maps/nearest"
)

// Attribution is the credit GeoNames' licence (CC BY 4.0) asks for; it is in every answer.
const Attribution = "GeoNames (CC BY 4.0)"

// SetPlaces registers the place index manager (SVC-11). The catalog's search block, the maps
// readiness check and /maps/search and /maps/nearest read from it.
func (s *Maps) SetPlaces(m *places.Manager) { s.places.Store(m) }

// placeResult is one result on the wire.
type placeResult struct {
	Name       string   `json:"name"`
	Kind       string   `json:"kind"`
	Lat        float64  `json:"lat"`
	Lon        float64  `json:"lon"`
	Admin1     string   `json:"admin1"`
	Country    string   `json:"country"`
	DistanceKM *float64 `json:"distance_km,omitempty"`
}

type placeResponse struct {
	Results     []placeResult `json:"results"`
	Attribution string        `json:"attribution"`
}

func roundTo(v float64, decimals int) float64 {
	p := math.Pow10(decimals)
	return math.Round(v*p) / p
}

func respond(w http.ResponseWriter, r *http.Request, rs []places.Result) error {
	out := placeResponse{Results: make([]placeResult, 0, len(rs)), Attribution: Attribution}
	for _, x := range rs {
		pr := placeResult{Name: x.Name, Kind: x.Kind, Lat: roundTo(x.Lat, 5), Lon: roundTo(x.Lon, 5), Admin1: x.Admin1, Country: x.Country}
		if x.DistanceKM != nil {
			d := roundTo(*x.DistanceKM, 1)
			pr.DistanceKM = &d
		}
		out.Results = append(out.Results, pr)
	}
	body, err := json.Marshal(out)
	if err != nil {
		return err
	}
	h := w.Header()
	h.Set("Content-Type", "application/json; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(body)
	}
	return nil
}

func badParam(field, reason string) *api.Error {
	return api.NewError(api.CodeInvalidRequest, http.StatusBadRequest, map[string]any{"field": field, "reason": reason})
}

// ready returns the serving index, or the error to answer: 404 with LAI-SVC-032 when the item is not
// installed, 503 {"state":"building"} while the first build runs (written here, so the second result
// is true and the caller returns).
func (s *Maps) readyIndex(w http.ResponseWriter) (*places.Index, bool, error) {
	m := s.places.Load()
	if m == nil {
		return nil, false, notFound(doctor.SVC032, "not_installed")
	}
	if ix := m.Index(); ix != nil {
		return ix, false, nil
	}
	switch m.State() {
	case places.StateAbsent:
		return nil, false, notFound(doctor.SVC032, "not_installed")
	case places.StateFailed:
		writeState(w, "failed")
	default:
		writeState(w, "building")
	}
	return nil, true, nil
}

func writeState(w http.ResponseWriter, state string) {
	h := w.Header()
	h.Set("Content-Type", "application/json; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("Retry-After", "5")
	w.WriteHeader(http.StatusServiceUnavailable)
	_ = json.NewEncoder(w).Encode(map[string]string{"state": state})
}

// parsePoint reads "lat,lon".
func parsePoint(v string) (places.Point, bool) {
	a, b, ok := strings.Cut(v, ",")
	if !ok {
		return places.Point{}, false
	}
	lat, err1 := strconv.ParseFloat(strings.TrimSpace(a), 64)
	lon, err2 := strconv.ParseFloat(strings.TrimSpace(b), 64)
	if err1 != nil || err2 != nil || !validCoord(lat, lon) {
		return places.Point{}, false
	}
	return places.Point{Lat: lat, Lon: lon}, true
}

func validCoord(lat, lon float64) bool {
	return !math.IsNaN(lat) && !math.IsNaN(lon) && lat >= -90 && lat <= 90 && lon >= -180 && lon <= 180
}

func limitParam(q map[string][]string, def int) (int, *api.Error) {
	v := ""
	if l := q["limit"]; len(l) > 0 {
		v = l[0]
	}
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 {
		return 0, badParam("limit", "not_a_positive_integer")
	}
	return places.ClampLimit(n, def), nil
}

// serveSearch is GET|HEAD /maps/search?q=&near=<lat>,<lon>&limit=20&lang=en (section 4.4.4).
func (s *Maps) serveSearch(w http.ResponseWriter, r *http.Request) error {
	q := r.URL.Query()
	if len(places.Fold(q.Get("q"))) < places.MinQueryLen {
		return badParam("q", "too_short")
	}
	var near *places.Point
	if v := q.Get("near"); v != "" {
		p, ok := parsePoint(v)
		if !ok {
			return badParam("near", "not_lat_lon")
		}
		near = &p
	}
	if l := q.Get("lang"); l != "" && l != "en" && l != "es" {
		return badParam("lang", "not_en_or_es")
	}
	limit, perr := limitParam(q, places.DefaultLimit)
	if perr != nil {
		return perr
	}
	ix, written, err := s.readyIndex(w)
	if written || err != nil {
		return err
	}
	return respond(w, r, ix.Search(q.Get("q"), near, limit))
}

// serveNearest is GET|HEAD /maps/nearest?lat=&lon=&kind=<kind>&limit=5 (section 4.4.4).
func (s *Maps) serveNearest(w http.ResponseWriter, r *http.Request) error {
	q := r.URL.Query()
	lat, err1 := strconv.ParseFloat(q.Get("lat"), 64)
	lon, err2 := strconv.ParseFloat(q.Get("lon"), 64)
	switch {
	case err1 != nil || !validCoord(lat, 0):
		return badParam("lat", "not_a_latitude")
	case err2 != nil || !validCoord(0, lon):
		return badParam("lon", "not_a_longitude")
	case !places.ValidKind(q.Get("kind")):
		return badParam("kind", "unknown_kind")
	}
	limit, perr := limitParam(q, places.NearestLimit)
	if perr != nil {
		return perr
	}
	ix, written, err := s.readyIndex(w)
	if written || err != nil {
		return err
	}
	return respond(w, r, ix.Nearest(places.Point{Lat: lat, Lon: lon}, q.Get("kind"), limit))
}

// placeSearchInfo is the catalog's search block, from the registered manager.
func (s *Maps) placeSearchInfo() (SearchInfo, bool) {
	m := s.places.Load()
	if m == nil {
		return SearchInfo{}, false
	}
	ix := m.Index()
	if ix == nil {
		return SearchInfo{Source: m.Source()}, true
	}
	return SearchInfo{Available: true, Places: ix.Len(), Source: m.Source()}, true
}
