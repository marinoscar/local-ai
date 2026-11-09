package checks

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/drill"
	"github.com/marinoscar/local-ai/agent/internal/maps"
)

// pmtilesSpec describes the tiny PMTiles v3 file the tests serve.
type pmtilesSpec struct {
	minZoom, maxZoom uint8
	lon, lat         float64 // the center
	tile             []byte  // the stored (compressed as the header says) tile at the center
	compression      uint8
	leaf             bool // the tile is behind a leaf directory, not in the root
	noTile           bool // the directory has no entry for the center tile
	badMagic         bool
}

func uvarints(vs ...uint64) []byte {
	var out []byte
	for _, v := range vs {
		out = binary.AppendUvarint(out, v)
	}
	return out
}

// directory encodes entries (tile id, run length, length, offset) the way the spec lays them out: count,
// then four columns, the offset column holding offset+1.
func directory(ids, runs, lens, offs []uint64) []byte {
	out := uvarints(uint64(len(ids)))
	var last uint64
	for _, id := range ids {
		out = append(out, uvarints(id-last)...)
		last = id
	}
	for _, c := range [][]uint64{runs, lens} {
		for _, v := range c {
			out = append(out, uvarints(v)...)
		}
	}
	for _, o := range offs {
		out = append(out, uvarints(o+1)...)
	}
	return out
}

func buildPMTiles(t testing.TB, s pmtilesSpec) []byte {
	t.Helper()
	if s.maxZoom == 0 {
		s.maxZoom = 14
	}
	z := uint8(10)
	if z > s.maxZoom {
		z = s.maxZoom
	}
	if z < s.minZoom {
		z = s.minZoom
	}
	x, y := maps.LonLatToTile(s.lon, s.lat, z)
	id, err := maps.ZxyToTileID(z, x, y)
	must(t, err)
	if s.tile == nil {
		s.tile = []byte("tile-bytes")
	}
	var root, leaf []byte
	tileOff := uint64(0)
	switch {
	case s.noTile:
		// An entry for a different tile only.
		root = directory([]uint64{id + 5}, []uint64{1}, []uint64{uint64(len(s.tile))}, []uint64{0})
	case s.leaf:
		leaf = directory([]uint64{id}, []uint64{1}, []uint64{uint64(len(s.tile))}, []uint64{tileOff})
		root = directory([]uint64{id}, []uint64{0}, []uint64{uint64(len(leaf))}, []uint64{0})
	default:
		root = directory([]uint64{id}, []uint64{1}, []uint64{uint64(len(s.tile))}, []uint64{tileOff})
	}
	const hdr = maps.HeaderSize
	h := make([]byte, hdr)
	copy(h, "PMTiles")
	h[7] = 3
	if s.badMagic {
		copy(h, "PMxxxxx")
	}
	rootOff := uint64(hdr)
	leafOff := rootOff + uint64(len(root))
	dataOff := leafOff + uint64(len(leaf))
	binary.LittleEndian.PutUint64(h[8:], rootOff)
	binary.LittleEndian.PutUint64(h[16:], uint64(len(root)))
	binary.LittleEndian.PutUint64(h[40:], leafOff)
	binary.LittleEndian.PutUint64(h[48:], uint64(len(leaf)))
	binary.LittleEndian.PutUint64(h[56:], dataOff)
	binary.LittleEndian.PutUint64(h[64:], uint64(len(s.tile)))
	h[97] = maps.CompressionNone
	h[98] = s.compression
	h[99] = maps.TileTypeMVT
	h[100], h[101] = s.minZoom, s.maxZoom
	e7 := func(v float64) uint32 { return uint32(int32(v * 1e7)) }
	binary.LittleEndian.PutUint32(h[102:], e7(s.lon-1))
	binary.LittleEndian.PutUint32(h[106:], e7(s.lat-1))
	binary.LittleEndian.PutUint32(h[110:], e7(s.lon+1))
	binary.LittleEndian.PutUint32(h[114:], e7(s.lat+1))
	h[118] = 8
	binary.LittleEndian.PutUint32(h[119:], e7(s.lon))
	binary.LittleEndian.PutUint32(h[123:], e7(s.lat))
	var out bytes.Buffer
	out.Write(h)
	out.Write(root)
	out.Write(leaf)
	out.Write(s.tile)
	return out.Bytes()
}

func gz(b []byte) []byte {
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	_, _ = w.Write(b)
	_ = w.Close()
	return buf.Bytes()
}

// serveRanges answers Range requests the way http.ServeContent does for a single range.
func serveRanges(file []byte, honourRange bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rg := r.Header.Get("Range")
		if rg == "" || !honourRange {
			w.WriteHeader(200)
			_, _ = w.Write(file)
			return
		}
		spec := strings.TrimPrefix(rg, "bytes=")
		a, b, _ := strings.Cut(spec, "-")
		lo, _ := strconv.ParseInt(a, 10, 64)
		hi, _ := strconv.ParseInt(b, 10, 64)
		if lo >= int64(len(file)) {
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
			return
		}
		if hi >= int64(len(file)) {
			hi = int64(len(file)) - 1
		}
		w.Header().Set("Content-Type", "application/vnd.pmtiles")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(file[lo : hi+1])
	}
}

type mapsFixture struct {
	catalog   maps.Catalog
	file      []byte
	honour    bool
	noGlyphs  bool
	noSprite  bool
	badSprite bool
	emptyGly  bool
	catStatus int
	catBody   string
}

func (f mapsFixture) agent() *fakeAgent {
	a := newFakeAgent()
	a.handle(mapsCatalogPath, func(w http.ResponseWriter, r *http.Request) {
		if f.catStatus != 0 {
			text(f.catStatus, "application/json", f.catBody)(w, r)
			return
		}
		jsonOf(200, f.catalog)(w, r)
	})
	a.handle("/maps/tiles/", serveRanges(f.file, f.honour))
	a.handle("/maps/assets/fonts/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != glyphPath {
			w.WriteHeader(404)
			return
		}
		switch {
		case f.noGlyphs:
			w.WriteHeader(404)
		case f.emptyGly:
			w.WriteHeader(200)
		default:
			text(200, "application/x-protobuf", "glyphs")(w, r)
		}
	})
	a.handle(spritePath, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case f.noSprite:
			w.WriteHeader(404)
		case f.badSprite:
			text(200, "application/json", "{")(w, r)
		default:
			text(200, "application/json", `{"marker":{"x":0}}`)(w, r)
		}
	})
	return a
}

func okMaps(t testing.TB, mod func(*pmtilesSpec)) mapsFixture {
	s := pmtilesSpec{minZoom: 0, maxZoom: 14, lon: -84.08, lat: 9.93, tile: gz([]byte("mvt-payload")), compression: maps.CompressionGzip}
	if mod != nil {
		mod(&s)
	}
	return mapsFixture{
		honour: true,
		file:   buildPMTiles(t, s),
		catalog: maps.Catalog{Maps: []maps.CatalogMap{
			{ID: "other", URL: "/maps/tiles/other.pmtiles"},
			{ID: "region", URL: "/maps/tiles/region.pmtiles", Default: true},
		}},
	}
}

func TestMapTile(t *testing.T) {
	cases := []struct {
		name    string
		fixture func(testing.TB) mapsFixture
		result  drill.Result
		code    string
		reason  string
	}{
		{"gzip tile in the root directory", func(t testing.TB) mapsFixture { return okMaps(t, nil) }, drill.ResultPass, "", ""},
		{"tile behind a leaf directory", func(t testing.TB) mapsFixture {
			return okMaps(t, func(s *pmtilesSpec) { s.leaf = true })
		}, drill.ResultPass, "", ""},
		{"uncompressed MVT tile", func(t testing.TB) mapsFixture {
			return okMaps(t, func(s *pmtilesSpec) { s.tile, s.compression = []byte("raw-mvt"), maps.CompressionNone })
		}, drill.ResultPass, "", ""},
		{"a map that stops at zoom 7 uses its own top zoom", func(t testing.TB) mapsFixture {
			return okMaps(t, func(s *pmtilesSpec) { s.maxZoom = 7 })
		}, drill.ResultPass, "", ""},
		{"a map that starts at zoom 12 uses its own bottom zoom", func(t testing.TB) mapsFixture {
			return okMaps(t, func(s *pmtilesSpec) { s.minZoom, s.maxZoom = 12, 15 })
		}, drill.ResultPass, "", ""},
		{"glyphs missing", func(t testing.TB) mapsFixture { f := okMaps(t, nil); f.noGlyphs = true; return f }, drill.ResultWarn, doctor.SVC031, ""},
		{"glyphs answer 200 with nothing", func(t testing.TB) mapsFixture { f := okMaps(t, nil); f.emptyGly = true; return f }, drill.ResultWarn, doctor.SVC031, ""},
		{"sprite missing", func(t testing.TB) mapsFixture { f := okMaps(t, nil); f.noSprite = true; return f }, drill.ResultWarn, doctor.SVC031, ""},
		{"sprite is not JSON", func(t testing.TB) mapsFixture { f := okMaps(t, nil); f.badSprite = true; return f }, drill.ResultWarn, doctor.SVC031, ""},
		{"both assets missing", func(t testing.TB) mapsFixture {
			f := okMaps(t, nil)
			f.noGlyphs, f.noSprite = true, true
			return f
		}, drill.ResultWarn, doctor.SVC031, ""},
		{"no map installed", func(t testing.TB) mapsFixture {
			f := okMaps(t, nil)
			f.catalog.Maps = []maps.CatalogMap{}
			return f
		}, drill.ResultFail, doctor.DRL040, "no_map"},
		{"catalog route fails", func(t testing.TB) mapsFixture {
			f := okMaps(t, nil)
			f.catStatus, f.catBody = 500, "{}"
			return f
		}, drill.ResultFail, doctor.DRL040, "catalog_http_500"},
		{"catalog is damaged", func(t testing.TB) mapsFixture {
			f := okMaps(t, nil)
			f.catStatus, f.catBody = 200, "{"
			return f
		}, drill.ResultFail, doctor.DRL040, "catalog_damaged"},
		{"header is damaged", func(t testing.TB) mapsFixture {
			return okMaps(t, func(s *pmtilesSpec) { s.badMagic = true })
		}, drill.ResultFail, doctor.DRL040, "header_damaged"},
		{"the tile route ignores Range", func(t testing.TB) mapsFixture { f := okMaps(t, nil); f.honour = false; return f }, drill.ResultFail, doctor.DRL040, "header_unreadable"},
		{"the directory has no entry for the center tile", func(t testing.TB) mapsFixture {
			return okMaps(t, func(s *pmtilesSpec) { s.noTile = true })
		}, drill.ResultFail, doctor.DRL040, "tile_missing"},
		{"the tile is not gzip though the header says so", func(t testing.TB) mapsFixture {
			return okMaps(t, func(s *pmtilesSpec) { s.tile = []byte("plain bytes") })
		}, drill.ResultFail, doctor.DRL040, "tile_empty"},
		{"the tile inflates to nothing", func(t testing.TB) mapsFixture {
			return okMaps(t, func(s *pmtilesSpec) { s.tile = gz(nil) })
		}, drill.ResultFail, doctor.DRL040, "tile_empty"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := tc.fixture(t).agent()
			out := run(t, NewMapTileCheck(MapsDeps{HTTP: a}))
			if out.Result != tc.result || out.Code != tc.code {
				t.Fatalf("outcome = %+v, want %s %q", out, tc.result, tc.code)
			}
			if tc.reason != "" && out.Details["reason"] != tc.reason {
				t.Errorf("reason = %v, want %s", out.Details["reason"], tc.reason)
			}
		})
	}
}

func TestMapTileRequestsTheDefaultMapWithRangesAndEscapesTheFontStack(t *testing.T) {
	a := okMaps(t, nil).agent()
	run(t, NewMapTileCheck(MapsDeps{HTTP: a}))
	reqs := strings.Join(a.requests(), "\n")
	if !strings.Contains(reqs, "GET /maps/tiles/region.pmtiles") || strings.Contains(reqs, "other.pmtiles") {
		t.Errorf("requests:\n%s", reqs)
	}
	if !strings.Contains(reqs, "/maps/assets/fonts/Noto%20Sans%20Regular/0-255.pbf") {
		t.Errorf("the font stack is not escaped:\n%s", reqs)
	}
	if !strings.Contains(reqs, "/maps/assets/sprites/v4/light.json") {
		t.Errorf("no sprite request:\n%s", reqs)
	}
}

func TestMapTileFallsBackToTheFirstMapWhenNoneIsDefault(t *testing.T) {
	f := okMaps(t, nil)
	f.catalog.Maps = []maps.CatalogMap{{ID: "region", URL: "/maps/tiles/region.pmtiles"}}
	if out := run(t, NewMapTileCheck(MapsDeps{HTTP: f.agent()})); out.Result != drill.ResultPass {
		t.Fatalf("outcome = %+v", out)
	}
}

func TestMapTileWiring(t *testing.T) {
	c := NewMapTileCheck(MapsDeps{})
	if c.ID != "drl.maps.tile" || !c.Critical || c.Order != 13 || c.FailCode != doctor.DRL040 {
		t.Errorf("check = %+v", c)
	}
	if out := run(t, c); out.Result != drill.ResultSkip {
		t.Errorf("nothing wired: %+v", out)
	}
	a := newFakeAgent()
	a.take("/maps", true)
	if out := run(t, NewMapTileCheck(MapsDeps{HTTP: a})); out.Result != drill.ResultFail || out.Code != doctor.DRL040 {
		t.Errorf("unreachable: %+v", out)
	}
}

func TestMapSearch(t *testing.T) {
	found := map[string]any{"results": []map[string]any{{"name": "San José", "kind": "city"}}, "attribution": "x"}
	cases := []struct {
		name   string
		h      http.HandlerFunc
		result drill.Result
		code   string
	}{
		{"one result", jsonOf(200, found), drill.ResultPass, ""},
		{"index not installed", jsonOf(404, map[string]any{"error": map[string]any{"code": doctor.SVC032}}), drill.ResultWarn, doctor.SVC032},
		{"index still building", jsonOf(503, map[string]string{"state": "building"}), drill.ResultWarn, doctor.SVC032},
		{"index build failed", jsonOf(503, map[string]string{"state": "failed"}), drill.ResultWarn, doctor.SVC032},
		{"no result", jsonOf(200, map[string]any{"results": []any{}}), drill.ResultWarn, doctor.SVC032},
		{"damaged answer", text(200, "application/json", "{"), drill.ResultWarn, doctor.SVC032},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := newFakeAgent()
			a.handle(maps.SearchPath, tc.h)
			out := run(t, NewMapSearchCheck(MapsDeps{HTTP: a}))
			if out.Result != tc.result || out.Code != tc.code {
				t.Fatalf("outcome = %+v, want %s %q", out, tc.result, tc.code)
			}
		})
	}
	a := newFakeAgent()
	var q string
	a.handle(maps.SearchPath, func(w http.ResponseWriter, r *http.Request) { q = r.URL.Query().Get("q"); jsonOf(200, found)(w, r) })
	run(t, NewMapSearchCheck(MapsDeps{HTTP: a}))
	if q != "San José" {
		t.Errorf("q = %q", q)
	}
	c := NewMapSearchCheck(MapsDeps{})
	if c.ID != "drl.maps.search" || c.Critical || c.Order != 14 {
		t.Errorf("check = %+v", c)
	}
	if out := run(t, c); out.Result != drill.ResultSkip {
		t.Errorf("nothing wired: %+v", out)
	}
	un := newFakeAgent()
	un.take("/maps", true)
	if out := run(t, NewMapSearchCheck(MapsDeps{HTTP: un})); out.Result != drill.ResultWarn {
		t.Errorf("unreachable must only warn: %+v", out)
	}
}
