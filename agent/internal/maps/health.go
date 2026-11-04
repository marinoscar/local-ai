package maps

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/svc/health"
)

// deepZoom is the zoom of the tile the deep check fetches (companion-services.md section 4.6).
const deepZoom = 10

// Spec returns the supervisor Spec of the maps: no unit, "a map file is damaged" as the code of a
// failed liveness, enabled by maps.enabled. Put it in health.Options.Specs.
func (s *Maps) Spec() health.Spec {
	return health.Spec{Name: ServiceName, Unit: "", Enabled: s.Enabled, DownCode: doctor.SVC034, Probe: s.Probe()}
}

// Probe returns the four checks of the maps (companion-services.md section 4.6):
//
//   - Liveness: the default map has a best copy and its header parses;
//   - Content: no map with a verified copy is missing_content (LAI-SVC-030);
//   - Readiness: the style assets are there (LAI-SVC-031) and no installed map is damaged
//     (LAI-SVC-034);
//   - Deep: one tile at z=10 at the center of the default map, read with HTTP Range through the tile
//     route, starts with the gzip magic or is an uncompressed vector tile.
func (s *Maps) Probe() *health.FuncProbe {
	return &health.FuncProbe{LivenessFn: s.liveness, ContentFn: s.content, ReadinessFn: s.readiness, DeepFn: s.deep}
}

func (s *Maps) liveness(ctx context.Context) error {
	l := s.list()
	if len(l.Maps) == 0 {
		if len(l.Damaged) > 0 {
			return health.Coded(doctor.SVC034, errors.New("every installed map file is damaged"))
		}
		return health.Coded(doctor.SVC030, errors.New("no map is installed"))
	}
	for _, m := range l.Maps {
		if m.Item.ID == l.Default {
			return ctx.Err()
		}
	}
	return health.Coded(doctor.SVC034, errors.New("the default map is not usable"))
}

func (s *Maps) content(context.Context) health.Content {
	if l := s.list(); l.Installed == 0 {
		return health.Content{Missing: true, Code: doctor.SVC030}
	}
	return health.Content{}
}

func (s *Maps) readiness(context.Context) health.Readiness {
	l := s.list()
	r := health.Readiness{Maps: make([]string, 0, len(l.Maps))}
	for _, m := range l.Maps {
		r.Maps = append(r.Maps, m.Item.ID)
	}
	r.Detail = fmt.Sprintf("%d maps", len(l.Maps))
	if len(l.Maps) == 1 {
		r.Detail = "1 map"
	}
	if !s.assetsPresent() {
		r.Problems = append(r.Problems, problem(doctor.SVC031))
	}
	if len(l.Damaged) > 0 {
		r.Problems = append(r.Problems, problem(doctor.SVC034))
	}
	return r
}

func (s *Maps) deep(ctx context.Context) []health.Problem {
	l := s.list()
	var m *resolved
	for i := range l.Maps {
		if l.Maps[i].Item.ID == l.Default {
			m = &l.Maps[i]
		}
	}
	if m == nil {
		if len(l.Damaged) > 0 {
			return []health.Problem{problem(doctor.SVC034)}
		}
		return []health.Problem{problem(doctor.SVC030)}
	}
	h := m.Header
	z := uint8(deepZoom)
	if z > h.MaxZoom {
		z = h.MaxZoom
	}
	if z < h.MinZoom {
		z = h.MinZoom
	}
	x, y := LonLatToTile(h.CenterLon, h.CenterLat, z)
	url := TilesPrefix + m.Item.ID + ".pmtiles"
	tile, err := FetchTile(h, s.viaRoute(ctx, url), z, x, y)
	if err != nil || !plausibleTile(tile, h.TileCompression) {
		return []health.Problem{problem(doctor.SVC034)}
	}
	return nil
}

// viaRoute reads ranges of a map through the tile handler itself (an in-process request), so the
// check covers the route, the Range handling and the file.
func (s *Maps) viaRoute(ctx context.Context, url string) RangeReader {
	return func(off, length uint64) ([]byte, error) {
		if length == 0 {
			return nil, errors.New("empty range")
		}
		req := httptest.NewRequest(http.MethodGet, url, nil).WithContext(ctx)
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", off, off+length-1))
		rec := httptest.NewRecorder()
		if err := s.serveTile(rec, req); err != nil {
			return nil, err
		}
		res := rec.Result()
		defer res.Body.Close()
		if res.StatusCode != http.StatusPartialContent {
			return nil, fmt.Errorf("tile route answered %d", res.StatusCode)
		}
		return io.ReadAll(res.Body)
	}
}

// plausibleTile: a gzip stream, or (uncompressed) a vector tile whose first field is a layer.
func plausibleTile(b []byte, compression uint8) bool {
	if len(b) < 2 {
		return false
	}
	if compression == CompressionGzip {
		return b[0] == 0x1f && b[1] == 0x8b
	}
	return (b[0] == 0x1f && b[1] == 0x8b) || b[0] == 0x1a
}

func problem(code string) health.Problem {
	sev := doctor.Error
	if c, ok := doctor.Lookup(code); ok && c.Severity.Valid() {
		sev = c.Severity
	}
	return health.Problem{Code: code, Severity: sev}
}
