package checks

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/drill"
	"github.com/marinoscar/local-ai/agent/internal/maps"
)

// Doctor codes of the map checks.
const (
	// CodeMapsFailed: the maps do not work (LAI-DRL-040).
	CodeMapsFailed = doctor.DRL040
	// CodeMapAssetsMissing: the map labels (fonts, sprites) are missing (LAI-SVC-031).
	CodeMapAssetsMissing = doctor.SVC031
	// CodePlaceIndexMissing: place search is not installed (LAI-SVC-032).
	CodePlaceIndexMissing = doctor.SVC032
)

// Paths and sizes of the map check.
const (
	mapsCatalogPath = "/maps/catalog.json"
	// glyphPath and spritePath are the first glyph range of the basemap's first font stack and the
	// sprite sheet's index, the two files without which the viewer draws no labels.
	glyphPath  = "/maps/assets/fonts/Noto Sans Regular/0-255.pbf"
	spritePath = "/maps/assets/sprites/v4/light.json"
	// tileZoom is the zoom of the tile the check fetches (drill.md section 4.2 check 13), lowered to
	// the map's maxzoom and raised to its minzoom when the map does not reach it.
	tileZoom = 10
)

// MapsDeps are what the map checks need.
type MapsDeps struct {
	HTTP AgentHTTP
	// Query is the place search of check 14; empty means "San José".
	Query string
}

// httpRanges reads a pmtiles file through the agent's tile route with Range requests, the way the
// viewer's PMTiles protocol does. It is a maps.RangeReader, so the directory lookup is the maps
// package's own (internal/maps/pmtiles.go): the check proves the route and the file.
type httpRanges struct {
	ctx  context.Context
	http AgentHTTP
	path string
	n    int
}

func (r *httpRanges) read(offset, length uint64) ([]byte, error) {
	if length == 0 {
		return nil, errors.New("empty range")
	}
	r.n++
	hdr := http.Header{}
	hdr.Set("Range", fmt.Sprintf("bytes=%d-%d", offset, offset+length-1))
	rp, err := callAgent(r.ctx, r.http, http.MethodGet, r.path, hdr, nil, int64(length)+1)
	switch {
	case err != nil:
		return nil, err
	case rp.Status != http.StatusPartialContent:
		return nil, fmt.Errorf("the tile route answered %d to a range request", rp.Status)
	case uint64(len(rp.Body)) != length:
		return nil, fmt.Errorf("the tile route returned %d bytes of the %d asked for", len(rp.Body), length)
	}
	return rp.Body, nil
}

// NewMapTileCheck is check 13, drl.maps.tile (critical): the default map's header is read, the z=10
// tile at its center is fetched through /maps/tiles/ with Range requests (the directory lookup is
// done here, in Go), and the first glyph range and the sprite sheet are fetched from /maps/assets/.
//
//	pass: the tile is non-empty (gzip or MVT) and the glyphs and the sprite answer 200.
//	warn: the glyphs or the sprite are missing (LAI-SVC-031).
//	fail: there is no map, or the header or the tile cannot be fetched (LAI-DRL-040).
func NewMapTileCheck(d MapsDeps) drill.Check {
	return drill.Check{
		ID: "drl.maps.tile", Area: "maps", Critical: true, Order: orderMapsTile, FailCode: CodeMapsFailed,
		Run: func(ctx context.Context, _ *drill.Env) drill.Outcome { return runMapTile(ctx, d) },
	}
}

func runMapTile(ctx context.Context, d MapsDeps) drill.Outcome {
	if d.HTTP == nil {
		return contentSkip("not_available", nil)
	}
	fail := func(reason string, err error) drill.Outcome {
		det := map[string]any{"reason": reason}
		if err != nil {
			det["error"] = reasonOf(err)
		}
		return contentFail(CodeMapsFailed, det)
	}
	rp, err := getAgent(ctx, d.HTTP, mapsCatalogPath, 1<<20)
	switch {
	case err != nil:
		return fail("catalog_unreachable", err)
	case rp.Status != http.StatusOK:
		return contentFail(CodeMapsFailed, map[string]any{"reason": "catalog_" + statusReason(rp.Status)})
	}
	var cat maps.Catalog
	if err := json.Unmarshal(rp.Body, &cat); err != nil {
		return fail("catalog_damaged", err)
	}
	if len(cat.Maps) == 0 {
		return fail("no_map", nil)
	}
	m := cat.Maps[0]
	for _, c := range cat.Maps {
		if c.Default {
			m = c
			break
		}
	}
	details := map[string]any{"map": m.ID}

	rd := &httpRanges{ctx: ctx, http: d.HTTP, path: m.URL}
	head, err := rd.read(0, maps.HeaderSize)
	if err != nil {
		details["reason"] = "header_unreadable"
		details["error"] = reasonOf(err)
		return contentFail(CodeMapsFailed, details)
	}
	h, err := maps.ParseHeader(head)
	if err != nil {
		details["reason"] = "header_damaged"
		details["error"] = reasonOf(err)
		return contentFail(CodeMapsFailed, details)
	}
	z := uint8(tileZoom)
	if z > h.MaxZoom {
		z = h.MaxZoom
	}
	if z < h.MinZoom {
		z = h.MinZoom
	}
	x, y := maps.LonLatToTile(h.CenterLon, h.CenterLat, z)
	details["tile"] = fmt.Sprintf("%d/%d/%d", z, x, y)
	tile, err := maps.FetchTile(h, rd.read, z, x, y)
	if err != nil {
		reason := "tile_unreadable"
		if errors.Is(err, maps.ErrNoTile) {
			reason = "tile_missing"
		}
		details["reason"] = reason
		details["error"] = reasonOf(err)
		return contentFail(CodeMapsFailed, details)
	}
	size, err := tileSize(tile, h.TileCompression)
	if err != nil || size == 0 {
		details["reason"] = "tile_empty"
		if err != nil {
			details["error"] = reasonOf(err)
		}
		return contentFail(CodeMapsFailed, details)
	}
	details["tile_bytes"] = len(tile)

	var missing []string
	if g, err := getAgent(ctx, d.HTTP, escapePath(glyphPath), maxReply); err != nil || g.Status != http.StatusOK || len(g.Body) == 0 {
		missing = append(missing, "glyphs")
	}
	if s, err := getAgent(ctx, d.HTTP, spritePath, maxReply); err != nil || s.Status != http.StatusOK || !json.Valid(s.Body) {
		missing = append(missing, "sprite")
	}
	if len(missing) > 0 {
		details["assets_missing"] = missing
		return contentWarn(CodeMapAssetsMissing, details)
	}
	return contentPass(details)
}

// tileSize is the size of a tile after decompression: an MVT is protobuf and has no magic number of its
// own, so a gzip tile is inflated and a plain one is taken as it is.
func tileSize(tile []byte, compression uint8) (int, error) {
	if compression != maps.CompressionGzip {
		return len(tile), nil
	}
	zr, err := gzip.NewReader(bytes.NewReader(tile))
	if err != nil {
		return 0, err
	}
	defer zr.Close()
	b, err := io.ReadAll(io.LimitReader(zr, 64<<20))
	return len(b), err
}

// NewMapSearchCheck is check 14, drl.maps.search: the place search finds a known place.
//
//	pass: /maps/search?q=San José has at least one result.
//	warn: the place index is not installed, still building, or finds nothing (LAI-SVC-032).
func NewMapSearchCheck(d MapsDeps) drill.Check {
	return drill.Check{
		ID: "drl.maps.search", Area: "maps", Order: orderMapsSearch,
		Run: func(ctx context.Context, _ *drill.Env) drill.Outcome {
			if d.HTTP == nil {
				return contentSkip("not_available", nil)
			}
			q := d.Query
			if q == "" {
				q = "San José"
			}
			rp, err := getAgent(ctx, d.HTTP, maps.SearchPath+"?q="+url.QueryEscape(q), 1<<20)
			switch {
			case err != nil:
				return contentWarn(CodePlaceIndexMissing, map[string]any{"reason": "request_failed", "error": reasonOf(err)})
			case rp.Status != http.StatusOK:
				return contentWarn(CodePlaceIndexMissing, map[string]any{"reason": statusReason(rp.Status)})
			}
			var body struct {
				Results []json.RawMessage `json:"results"`
			}
			if err := json.Unmarshal(rp.Body, &body); err != nil {
				return contentWarn(CodePlaceIndexMissing, map[string]any{"reason": "answer_damaged", "error": reasonOf(err)})
			}
			if len(body.Results) == 0 {
				return contentWarn(CodePlaceIndexMissing, map[string]any{"reason": "no_results"})
			}
			return contentPass(map[string]any{"results": len(body.Results)})
		},
	}
}
