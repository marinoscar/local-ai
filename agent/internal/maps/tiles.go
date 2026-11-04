package maps

import (
	"errors"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/api"
	"github.com/marinoscar/local-ai/agent/internal/api/static"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
)

// ContentTypePMTiles is the media type of a pmtiles file.
const ContentTypePMTiles = "application/vnd.pmtiles"

// Routes returns the survival-set routes of the maps (companion-services.md section 3.5):
// /maps/tiles/, /maps/assets/, /maps/catalog.json, /maps/search and /maps/nearest. All are GET (and
// so HEAD), level none.
func (s *Maps) Routes() []api.Route {
	get := func(pattern string, h func(http.ResponseWriter, *http.Request) error) api.Route {
		return api.Route{Method: http.MethodGet, Pattern: pattern, Auth: []api.Level{api.AuthNone}, Absolute: true,
			Handler: api.HandlerFunc(h)}
	}
	return []api.Route{
		get(CatalogPath, s.serveCatalog),
		get(TilesPrefix, s.serveTile),
		get(AssetsPrefix, s.serveAsset),
		get(SearchPath, s.serveSearch),
		get(NearestPath, s.serveNearest),
	}
}

// Register adds the routes to reg (nil means api.DefaultRegistry).
func Register(reg *api.Registry, d Deps) error {
	if reg == nil {
		reg = api.DefaultRegistry
	}
	return reg.Add(New(d).Routes()...)
}

func notFound(code, reason string) *api.Error {
	return api.NewError(code, http.StatusNotFound, map[string]any{"reason": reason})
}

// tileID reports the content id in "<id>.pmtiles"; ids are plain names.
func tileID(name string) (string, bool) {
	id, ok := strings.CutSuffix(name, ".pmtiles")
	if !ok || id == "" || len(id) > 64 {
		return "", false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || (i > 0 && (c == '-' || c == '.' || c == '_'))) {
			return "", false
		}
	}
	return id, true
}

// serveTile is GET|HEAD /maps/tiles/<content-id>.pmtiles (companion-services.md section 4.4.1): the
// best verified copy, served by http.ServeContent (Range, If-Range, 206, 416). A request for several
// ranges is refused with 416. The file is opened here and closed before the handler returns, so an
// eject never finds a handle left behind.
func (s *Maps) serveTile(w http.ResponseWriter, r *http.Request) error {
	id, ok := tileID(strings.TrimPrefix(r.URL.Path, TilesPrefix))
	if !ok {
		return notFound(doctor.SVC030, "not_found")
	}
	it, ok := s.item(id)
	if !ok {
		return notFound(doctor.SVC030, "not_found")
	}
	m, err := s.resolve(it)
	switch {
	case errors.Is(err, ErrNoMap):
		return notFound(doctor.SVC030, "not_installed")
	case err != nil:
		s.log.Warn("a map file is damaged", "map", id, "code", doctor.SVC034, "error", err.Error())
		return notFound(doctor.SVC034, "damaged")
	}
	f, err := os.Open(m.Path)
	if err != nil {
		return notFound(doctor.SVC030, "not_installed")
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		return notFound(doctor.SVC030, "not_installed")
	}
	if rng := r.Header.Get("Range"); multipleRanges(rng) {
		return api.StaticError(&static.Error{Status: http.StatusRequestedRangeNotSatisfiable, Reason: "multiple_ranges",
			Header: http.Header{"Content-Range": []string{"bytes */" + strconv.FormatInt(fi.Size(), 10)}}})
	}
	h := w.Header()
	h.Set("Content-Type", ContentTypePMTiles)
	h.Set("ETag", m.ETag)
	h.Set("Cache-Control", "no-cache")
	h.Set("Accept-Ranges", "bytes")
	h.Set("X-Content-Type-Options", "nosniff")
	cw := &countingWriter{ResponseWriter: w}
	// A zero modtime keeps ServeContent from sending Last-Modified: the strong ETag is the validator.
	http.ServeContent(cw, r, "", time.Time{}, f)
	if r.Method == http.MethodGet && (cw.status == 0 || cw.status == http.StatusOK || cw.status == http.StatusPartialContent) {
		s.metrics.MapsTileBytes(cw.n)
	}
	return nil
}

// countingWriter counts the body bytes written and remembers the status.
type countingWriter struct {
	http.ResponseWriter
	n      int64
	status int
}

func (c *countingWriter) WriteHeader(code int) {
	c.status = code
	c.ResponseWriter.WriteHeader(code)
}

func (c *countingWriter) Write(b []byte) (int, error) {
	n, err := c.ResponseWriter.Write(b)
	c.n += int64(n)
	return n, err
}

// Unwrap lets http.ResponseController reach the real writer.
func (c *countingWriter) Unwrap() http.ResponseWriter { return c.ResponseWriter }

// multipleRanges reports whether a Range header asks for more than one byte range.
func multipleRanges(h string) bool {
	rest, ok := strings.CutPrefix(strings.TrimSpace(h), "bytes=")
	if !ok {
		return false
	}
	n := 0
	for _, part := range strings.Split(rest, ",") {
		if strings.TrimSpace(part) != "" {
			n++
		}
	}
	return n > 1
}
