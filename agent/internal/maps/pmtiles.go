package maps

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
)

// HeaderSize is the size of a PMTiles v3 header.
const HeaderSize = 127

// Compression and tile type codes of the PMTiles v3 header.
const (
	CompressionUnknown = 0
	CompressionNone    = 1
	CompressionGzip    = 2
	TileTypeMVT        = 1
)

// ErrBadHeader: the bytes are not a usable PMTiles v3 header (bad magic, version 2, truncated or
// out-of-range values). The map is not listed and the doctor code is LAI-SVC-034.
var ErrBadHeader = errors.New("maps: not a PMTiles v3 header")

// Header is the part of a PMTiles v3 header the agent uses. Layout:
// https://github.com/protomaps/PMTiles/blob/main/spec/v3/spec.md#header (little-endian; the bounds
// and the center are int32 * 1e-7 degrees).
type Header struct {
	RootDirOffset, RootDirLength uint64
	LeafDirOffset, LeafDirLength uint64
	TileDataOffset, TileDataLen  uint64
	InternalCompression          uint8
	TileCompression              uint8
	TileType                     uint8
	MinZoom, MaxZoom             uint8
	MinLon, MinLat               float64
	MaxLon, MaxLat               float64
	CenterZoom                   uint8
	CenterLon, CenterLat         float64
}

func coord(b []byte) float64 { return float64(int32(binary.LittleEndian.Uint32(b))) / 1e7 }

// ParseHeader decodes the first 127 bytes of a PMTiles file. Version 2 files (magic "PM") and
// anything that is not version 3 are refused.
func ParseHeader(b []byte) (Header, error) {
	bad := func(format string, a ...any) (Header, error) {
		return Header{}, fmt.Errorf("%w: %s", ErrBadHeader, fmt.Sprintf(format, a...))
	}
	if len(b) < HeaderSize {
		return bad("%d bytes, need %d", len(b), HeaderSize)
	}
	if string(b[:7]) != "PMTiles" {
		return bad("bad magic")
	}
	if b[7] != 3 {
		return bad("version %d, only 3 is supported", b[7])
	}
	u64 := func(o int) uint64 { return binary.LittleEndian.Uint64(b[o:]) }
	h := Header{
		RootDirOffset: u64(8), RootDirLength: u64(16),
		LeafDirOffset: u64(40), LeafDirLength: u64(48),
		TileDataOffset: u64(56), TileDataLen: u64(64),
		InternalCompression: b[97], TileCompression: b[98], TileType: b[99],
		MinZoom: b[100], MaxZoom: b[101],
		MinLon: coord(b[102:]), MinLat: coord(b[106:]), MaxLon: coord(b[110:]), MaxLat: coord(b[114:]),
		CenterZoom: b[118], CenterLon: coord(b[119:]), CenterLat: coord(b[123:]),
	}
	switch {
	case h.MinZoom > h.MaxZoom || h.MaxZoom > 30:
		return bad("zoom range %d..%d", h.MinZoom, h.MaxZoom)
	case h.MinLon < -180 || h.MaxLon > 180 || h.MinLat < -90 || h.MaxLat > 90:
		return bad("bounds outside the world")
	case h.MinLon >= h.MaxLon || h.MinLat >= h.MaxLat:
		return bad("empty bounds")
	case h.CenterLon < -180 || h.CenterLon > 180 || h.CenterLat < -90 || h.CenterLat > 90:
		return bad("center outside the world")
	case h.RootDirLength == 0:
		return bad("no root directory")
	}
	return h, nil
}

// ReadHeader reads and parses the header of the file at path; the file is closed before it returns.
func ReadHeader(path string) (Header, error) {
	f, err := os.Open(path)
	if err != nil {
		return Header{}, err
	}
	defer f.Close()
	buf := make([]byte, HeaderSize)
	if _, err := io.ReadFull(f, buf); err != nil {
		return Header{}, fmt.Errorf("%w: %v", ErrBadHeader, err)
	}
	return ParseHeader(buf)
}

// RangeReader reads length bytes at offset from a pmtiles file. The deep health check supplies one
// that goes through the HTTP tile route, so it proves the route and not only the file.
type RangeReader func(offset, length uint64) ([]byte, error)

func inflate(data []byte, compression uint8) ([]byte, error) {
	switch compression {
	case CompressionNone, CompressionUnknown:
		return data, nil
	case CompressionGzip:
		zr, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		defer zr.Close()
		return io.ReadAll(io.LimitReader(zr, 64<<20))
	}
	return nil, fmt.Errorf("maps: unsupported compression %d", compression)
}

// dirEntry is one entry of a PMTiles directory. RunLength 0 points to a leaf directory.
type dirEntry struct {
	TileID, Offset uint64
	Length         uint32
	RunLength      uint32
}

// parseDirectory decodes a decompressed directory (spec: entry count, then four columns of varints:
// delta tile ids, run lengths, lengths and offsets).
func parseDirectory(b []byte) ([]dirEntry, error) {
	pos := 0
	next := func() (uint64, error) {
		v, k := binary.Uvarint(b[pos:])
		if k <= 0 {
			return 0, errors.New("maps: bad varint in a directory")
		}
		pos += k
		return v, nil
	}
	n, err := next()
	if err != nil || n > 1<<24 {
		return nil, errors.New("maps: bad directory size")
	}
	es := make([]dirEntry, n)
	var last uint64
	for i := range es {
		d, err := next()
		if err != nil {
			return nil, err
		}
		last += d
		es[i].TileID = last
	}
	for i := range es {
		v, err := next()
		if err != nil {
			return nil, err
		}
		es[i].RunLength = uint32(v)
	}
	for i := range es {
		v, err := next()
		if err != nil {
			return nil, err
		}
		es[i].Length = uint32(v)
	}
	for i := range es {
		v, err := next()
		if err != nil {
			return nil, err
		}
		if v == 0 && i > 0 {
			es[i].Offset = es[i-1].Offset + uint64(es[i-1].Length)
		} else {
			es[i].Offset = v - 1
		}
	}
	return es, nil
}

// findEntry is the directory search of the spec: the entry that holds tileID, or the leaf pointer
// whose range covers it.
func findEntry(es []dirEntry, tileID uint64) (dirEntry, bool) {
	lo, hi := 0, len(es)-1
	for lo <= hi {
		m := (lo + hi) / 2
		switch {
		case tileID > es[m].TileID:
			lo = m + 1
		case tileID < es[m].TileID:
			hi = m - 1
		default:
			return es[m], true
		}
	}
	if hi >= 0 {
		if es[hi].RunLength == 0 || tileID-es[hi].TileID < uint64(es[hi].RunLength) {
			return es[hi], true
		}
	}
	return dirEntry{}, false
}

// ErrNoTile: the file has no tile at the asked z/x/y.
var ErrNoTile = errors.New("maps: the file has no such tile")

// FetchTile returns the stored bytes (compressed as the header says) of tile z/x/y, reading only the
// directories and the tile through rd.
func FetchTile(h Header, rd RangeReader, z uint8, x, y uint32) ([]byte, error) {
	id, err := ZxyToTileID(z, x, y)
	if err != nil {
		return nil, err
	}
	off, length := h.RootDirOffset, h.RootDirLength
	for depth := 0; depth < 4; depth++ {
		raw, err := rd(off, length)
		if err != nil {
			return nil, err
		}
		dir, err := inflate(raw, h.InternalCompression)
		if err != nil {
			return nil, err
		}
		es, err := parseDirectory(dir)
		if err != nil {
			return nil, err
		}
		e, ok := findEntry(es, id)
		if !ok {
			return nil, ErrNoTile
		}
		if e.RunLength > 0 {
			return rd(h.TileDataOffset+e.Offset, uint64(e.Length))
		}
		off, length = h.LeafDirOffset+e.Offset, uint64(e.Length)
	}
	return nil, errors.New("maps: directories nest too deep")
}

// ZxyToTileID is the Hilbert-curve tile id of the PMTiles spec.
func ZxyToTileID(z uint8, x, y uint32) (uint64, error) {
	if z > 31 {
		return 0, errors.New("maps: zoom too large")
	}
	n := uint64(1) << z
	if uint64(x) >= n || uint64(y) >= n {
		return 0, errors.New("maps: tile outside the zoom level")
	}
	var acc uint64
	for t := uint8(0); t < z; t++ {
		acc += (uint64(1) << t) * (uint64(1) << t)
	}
	tx, ty := uint64(x), uint64(y)
	var d uint64
	for s := n / 2; s > 0; s /= 2 {
		var rx, ry uint64
		if tx&s > 0 {
			rx = 1
		}
		if ty&s > 0 {
			ry = 1
		}
		d += s * s * ((3 * rx) ^ ry)
		if ry == 0 {
			if rx == 1 {
				tx = s - 1 - tx
				ty = s - 1 - ty
			}
			tx, ty = ty, tx
		}
	}
	return acc + d, nil
}

// LonLatToTile is the Web Mercator tile that holds a point at zoom z.
func LonLatToTile(lon, lat float64, z uint8) (x, y uint32) {
	n := math.Exp2(float64(z))
	lat = math.Max(-85.0511287798, math.Min(85.0511287798, lat))
	fx := (lon + 180) / 360 * n
	r := lat * math.Pi / 180
	fy := (1 - math.Log(math.Tan(r)+1/math.Cos(r))/math.Pi) / 2 * n
	clamp := func(v float64) uint32 {
		if v < 0 {
			return 0
		}
		if v > n-1 {
			return uint32(n - 1)
		}
		return uint32(v)
	}
	return clamp(fx), clamp(fy)
}
