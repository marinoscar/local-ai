package maps

import (
	"errors"
	"math"
	"os"
	"path/filepath"
	"testing"
)

func TestParseHeader(t *testing.T) {
	good := costaRica()
	neg := costaRica()
	neg.minLon, neg.maxLon = -179.9999999, -0.0000001
	v2 := costaRica()
	v2.magic, v2.version = "PM", 2
	v4 := costaRica()
	v4.version = 4
	badMagic := costaRica()
	badMagic.magic = "NotTile"
	zoom := costaRica()
	zoom.minZ, zoom.maxZ = 9, 3
	world := costaRica()
	world.maxLat = 91
	empty := costaRica()
	empty.minLon, empty.maxLon = -80, -80

	tests := []struct {
		name    string
		fx      fixture
		cut     int // truncate the file to this many bytes when > 0
		wantErr bool
	}{
		{"valid v3", good, 0, false},
		{"negative longitude", neg, 0, false},
		{"version 2 is rejected", v2, 0, true},
		{"version 4 is rejected", v4, 0, true},
		{"bad magic", badMagic, 0, true},
		{"zoom range inverted", zoom, 0, true},
		{"bounds outside the world", world, 0, true},
		{"empty bounds", empty, 0, true},
		{"truncated", good, 100, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b := tc.fx.build(t)
			if tc.cut > 0 {
				b = b[:tc.cut]
			}
			h, err := ParseHeader(b)
			if tc.wantErr {
				if !errors.Is(err, ErrBadHeader) {
					t.Fatalf("want ErrBadHeader, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			near := func(a, b float64) bool { return math.Abs(a-b) < 1e-6 }
			if !near(h.MinLon, tc.fx.minLon) || !near(h.MinLat, tc.fx.minLat) || !near(h.MaxLon, tc.fx.maxLon) || !near(h.MaxLat, tc.fx.maxLat) {
				t.Errorf("bounds %v %v %v %v", h.MinLon, h.MinLat, h.MaxLon, h.MaxLat)
			}
			if h.MinZoom != tc.fx.minZ || h.MaxZoom != tc.fx.maxZ || h.CenterZoom != tc.fx.centerZ {
				t.Errorf("zooms %d %d %d", h.MinZoom, h.MaxZoom, h.CenterZoom)
			}
			if !near(h.CenterLon, tc.fx.centerLon) || !near(h.CenterLat, tc.fx.centerLat) {
				t.Errorf("center %v %v", h.CenterLon, h.CenterLat)
			}
		})
	}
}

func TestReadHeaderFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "m.pmtiles")
	if err := os.WriteFile(p, costaRica().build(t), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadHeader(p); err != nil {
		t.Fatal(err)
	}
	short := filepath.Join(dir, "short.pmtiles")
	if err := os.WriteFile(short, []byte("PMTiles\x03"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadHeader(short); !errors.Is(err, ErrBadHeader) {
		t.Fatalf("short file: %v", err)
	}
	if _, err := ReadHeader(filepath.Join(dir, "missing")); err == nil {
		t.Fatal("a missing file must fail")
	}
}

func TestZxyToTileID(t *testing.T) {
	tests := []struct {
		z    uint8
		x, y uint32
		want uint64
	}{
		{0, 0, 0, 0},
		{1, 0, 0, 1}, {1, 0, 1, 2}, {1, 1, 1, 3}, {1, 1, 0, 4},
		{2, 0, 0, 5},
	}
	for _, tc := range tests {
		got, err := ZxyToTileID(tc.z, tc.x, tc.y)
		if err != nil || got != tc.want {
			t.Errorf("z%d/%d/%d = %d, %v; want %d", tc.z, tc.x, tc.y, got, err, tc.want)
		}
	}
	if _, err := ZxyToTileID(1, 2, 0); err == nil {
		t.Error("a tile outside the zoom level must fail")
	}
}

func TestFetchTile(t *testing.T) {
	fx := costaRica()
	b := fx.build(t)
	h, err := ParseHeader(b)
	if err != nil {
		t.Fatal(err)
	}
	rd := func(off, n uint64) ([]byte, error) { return b[off : off+n], nil }
	x, y := LonLatToTile(fx.centerLon, fx.centerLat, fx.tileZ)
	tile, err := FetchTile(h, rd, fx.tileZ, x, y)
	if err != nil || len(tile) < 2 || tile[0] != 0x1f || tile[1] != 0x8b {
		t.Fatalf("tile %x, %v", tile, err)
	}
	if _, err := FetchTile(h, rd, fx.tileZ, x+1, y); !errors.Is(err, ErrNoTile) {
		t.Fatalf("a tile the file lacks: %v", err)
	}
}
