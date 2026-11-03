package resolve

import (
	"bytes"
	"compress/gzip"
	"crypto/md5" //nolint:gosec // the ZIM format's own integrity field
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

// The binary fixtures of the kiwix and pmtiles tests are generated, not hand made, so a reader can see
// what is in them and regenerate them: LAI_UPDATE_FIXTURES=1 go test -run TestFixturesAreCurrent
// ./internal/sync/resolve rewrites testdata/zim/tiny.zim and testdata/pmtiles/tiny.pmtiles. Without the
// variable the test fails when a committed fixture differs from what the generator makes.

const (
	tinyZIMPath      = "testdata/zim/tiny.zim"
	tinyPMTilesPath  = "testdata/pmtiles/tiny.pmtiles"
	fakePMTilesPath  = "testdata/pmtiles/fake-pmtiles.sh"
	kiwixListingPath = "testdata/kiwix/listing.html"
)

// buildZIM makes a structurally valid ZIM: the 80-byte header, a deterministic payload, and at the end
// the MD5 of everything before it, which is where checksumPos points.
func buildZIM(payload []byte) []byte {
	h := make([]byte, zimHeaderSize)
	binary.LittleEndian.PutUint32(h[0:], zimMagic)
	binary.LittleEndian.PutUint16(h[4:], 6) // major
	binary.LittleEndian.PutUint16(h[6:], 1) // minor
	copy(h[8:24], []byte("tiny-zim-fixture"))
	binary.LittleEndian.PutUint32(h[24:], 1) // entryCount
	binary.LittleEndian.PutUint32(h[28:], 1) // clusterCount
	binary.LittleEndian.PutUint64(h[32:], zimHeaderSize)
	binary.LittleEndian.PutUint64(h[40:], zimHeaderSize)
	binary.LittleEndian.PutUint64(h[48:], zimHeaderSize)
	binary.LittleEndian.PutUint64(h[56:], zimHeaderSize)
	binary.LittleEndian.PutUint32(h[64:], 0xffffffff) // mainPage: none
	binary.LittleEndian.PutUint32(h[68:], 0xffffffff)
	binary.LittleEndian.PutUint64(h[zimChecksumAt:], uint64(zimHeaderSize+len(payload)))
	out := append(append([]byte{}, h...), payload...)
	sum := md5.Sum(out) //nolint:gosec
	return append(out, sum[:]...)
}

// tinyZIMPayload is the deterministic body of tiny.zim: 2 KiB of text.
func tinyZIMPayload() []byte {
	var b bytes.Buffer
	for i := 0; b.Len() < 2048; i++ {
		b.WriteString("This is a tiny invented ZIM body used only by the unit tests. Line ")
		b.WriteByte(byte('0' + i%10))
		b.WriteByte('\n')
	}
	return b.Bytes()[:2048]
}

// The PMTiles v3 header is 127 bytes (protomaps PMTiles spec): "PMTiles", version 3, six offset/length
// pairs, three counts, the clustered flag, the two compressions, the tile type, the zoom range, the
// bounds as E7 integers and the centre. The fixture holds the header, an empty root directory and an
// empty metadata object, both gzip-compressed, and no tile data: enough for a reader to recognise it,
// not a map. CI runs the fake pmtiles script over it; the real tool is not needed.
const pmtilesHeaderSize = 127

func gz(b []byte) []byte {
	var buf bytes.Buffer
	w, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	_, _ = w.Write(b)
	_ = w.Close()
	return buf.Bytes()
}

func buildPMTiles(minLon, minLat, maxLon, maxLat float64) []byte {
	root := gz([]byte{0}) // a directory with zero entries
	meta := gz([]byte("{}"))
	h := make([]byte, pmtilesHeaderSize)
	copy(h, "PMTiles")
	h[7] = 3
	le := binary.LittleEndian
	le.PutUint64(h[8:], pmtilesHeaderSize)                              // root offset
	le.PutUint64(h[16:], uint64(len(root)))                             // root length
	le.PutUint64(h[24:], uint64(pmtilesHeaderSize+len(root)))           // metadata offset
	le.PutUint64(h[32:], uint64(len(meta)))                             // metadata length
	le.PutUint64(h[40:], uint64(pmtilesHeaderSize+len(root)+len(meta))) // leaf offset
	le.PutUint64(h[56:], uint64(pmtilesHeaderSize+len(root)+len(meta))) // tile data offset
	h[96], h[97], h[98], h[99] = 1, 2, 2, 1                             // clustered, gzip, gzip, mvt
	h[100], h[101] = 0, 15                                              // zooms
	e7 := func(v float64) uint32 { return uint32(int32(v * 1e7)) }
	le.PutUint32(h[102:], e7(minLon))
	le.PutUint32(h[106:], e7(minLat))
	le.PutUint32(h[110:], e7(maxLon))
	le.PutUint32(h[114:], e7(maxLat))
	h[118] = 5
	le.PutUint32(h[119:], e7((minLon+maxLon)/2))
	le.PutUint32(h[123:], e7((minLat+maxLat)/2))
	return append(append(h, root...), meta...)
}

// tinyBBox is the bounding box of tiny.pmtiles and of the catalog entry the pmtiles tests use.
var tinyBBox = []float64{-92.5, 7.0, -77.0, 18.6}

func TestFixturesAreCurrent(t *testing.T) {
	want := map[string][]byte{
		tinyZIMPath:     buildZIM(tinyZIMPayload()),
		tinyPMTilesPath: buildPMTiles(tinyBBox[0], tinyBBox[1], tinyBBox[2], tinyBBox[3]),
	}
	for p, b := range want {
		if os.Getenv("LAI_UPDATE_FIXTURES") == "1" {
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, b, 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		got, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("%s: %v (regenerate with LAI_UPDATE_FIXTURES=1)", p, err)
		}
		if !bytes.Equal(got, b) {
			t.Errorf("%s differs from the generator (regenerate with LAI_UPDATE_FIXTURES=1)", p)
		}
	}
}

// readFixture reads a committed fixture.
func readFixture(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
