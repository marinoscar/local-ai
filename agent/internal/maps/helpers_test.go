package maps

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/ark"
	"github.com/marinoscar/local-ai/agent/internal/sync/manifest"
)

// fakeCopies is an ark.CopyFinder over a map; an id that is not in it is not present.
type fakeCopies map[string]ark.Copy

func (f fakeCopies) BestCopy(id string) (ark.Copy, error) {
	if c, ok := f[id]; ok {
		return c, nil
	}
	return ark.Copy{}, ark.ErrNotPresent
}

// fixture describes a synthetic PMTiles v3 file.
type fixture struct {
	magic                          string
	version                        byte
	minLon, minLat, maxLon, maxLat float64
	minZ, maxZ, centerZ            uint8
	centerLon, centerLat           float64
	tileZ                          uint8 // zoom of the one tile the file holds
	tile                           []byte
}

func costaRica() fixture {
	return fixture{magic: "PMTiles", version: 3, minLon: -92.5, minLat: 7.0, maxLon: -77.0, maxLat: 18.6,
		minZ: 0, maxZ: 15, centerZ: 8, centerLon: -84.08, centerLat: 9.93, tileZ: 10}
}

func gz(b []byte) []byte {
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	_, _ = w.Write(b)
	_ = w.Close()
	return buf.Bytes()
}

func i32(f float64) uint32 { return uint32(int32(f * 1e7)) }

// build writes header, a gzip root directory with one tile (the one at the center at tileZ) and the
// gzip tile data.
func (f fixture) build(t testing.TB) []byte {
	t.Helper()
	x, y := LonLatToTile(f.centerLon, f.centerLat, f.tileZ)
	id, err := ZxyToTileID(f.tileZ, x, y)
	if err != nil {
		t.Fatal(err)
	}
	tile := f.tile
	if tile == nil {
		tile = gz([]byte("tile-bytes"))
	}
	var dir bytes.Buffer
	put := func(v uint64) { dir.Write(binary.AppendUvarint(nil, v)) }
	put(1)
	put(id)
	put(1)
	put(uint64(len(tile)))
	put(1) // offset 0, stored +1
	rootDir := gz(dir.Bytes())

	h := make([]byte, HeaderSize)
	copy(h, f.magic)
	h[7] = f.version
	le := binary.LittleEndian
	le.PutUint64(h[8:], HeaderSize)
	le.PutUint64(h[16:], uint64(len(rootDir)))
	le.PutUint64(h[56:], uint64(HeaderSize+len(rootDir)))
	le.PutUint64(h[64:], uint64(len(tile)))
	h[97], h[98], h[99] = CompressionGzip, CompressionGzip, TileTypeMVT
	h[100], h[101] = f.minZ, f.maxZ
	le.PutUint32(h[102:], i32(f.minLon))
	le.PutUint32(h[106:], i32(f.minLat))
	le.PutUint32(h[110:], i32(f.maxLon))
	le.PutUint32(h[114:], i32(f.maxLat))
	h[118] = f.centerZ
	le.PutUint32(h[119:], i32(f.centerLon))
	le.PutUint32(h[123:], i32(f.centerLat))
	out := append(h, rootDir...)
	return append(out, tile...)
}

// mapCopy writes <dir>/<id>/<id>.pmtiles and returns the copy the index would hand out.
func mapCopy(t testing.TB, dir, id, rootID, version, sum string, data []byte) ark.Copy {
	t.Helper()
	d := filepath.Join(dir, id)
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(d, id+".pmtiles")
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return ark.Copy{RootID: rootID, Dir: d, Files: []string{p}, Manifest: manifest.Manifest{
		ItemID: id, ItemKind: manifest.KindMaps, Version: version,
		Files: []manifest.File{{Kind: "pmtiles", Path: id + ".pmtiles", SHA256: sum, SizeBytes: int64(len(data))}},
	}}
}

// assetsCopy writes the minimal map-assets tree and returns its copy.
func assetsCopy(t testing.TB, dir string, withFonts bool) ark.Copy {
	t.Helper()
	d := filepath.Join(dir, "assets")
	files := map[string]string{"sprites/v4/light.json": "{}", "sprites/v4/light.png": "png"}
	if withFonts {
		for _, f := range FontStacks {
			files["fonts/"+f+"/0-255.pbf"] = "pbf-" + f
		}
	}
	for rel, body := range files {
		p := filepath.Join(d, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return ark.Copy{RootID: "internal", Dir: d, Manifest: manifest.Manifest{ItemID: ark.MapAssetsID, ItemKind: manifest.KindMaps, Version: "1"}}
}

var testSHA = strings.Repeat("0123456789abcdef", 4)
