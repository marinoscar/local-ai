package api

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/api/static"
)

// This is the scenario harness of agent.md section 9.2 (7): a /maps/tiles/<content-id>.pmtiles mount
// built on the static helpers, with a test .pmtiles. The real mount is SVC-09's; what this proves is
// that SafeJoin and ServeFile carry its contract (Range, 206, Accept-Ranges, path safety).

var tileID = regexp.MustCompile(`^[a-z0-9][a-z0-9.\-]{2,63}$`)

func tilesRoute(root string) Route {
	return Route{Method: "GET", Pattern: "/maps/tiles/", Absolute: true, Auth: []Level{AuthNone}, Handler: HandlerFunc(func(w http.ResponseWriter, r *http.Request) error {
		name := strings.TrimPrefix(r.URL.Path, "/maps/tiles/")
		id, ok := strings.CutSuffix(name, ".pmtiles")
		if !ok || !tileID.MatchString(id) {
			return notFoundError()
		}
		p, err := static.SafeJoin(root, "maps/"+id+".pmtiles")
		if err != nil {
			return StaticError(err)
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		if err := static.ServeFile(w, r, p); err != nil {
			return StaticError(err)
		}
		return nil
	})}
}

func TestMapsTilesRangeHarness(t *testing.T) {
	root := t.TempDir()
	data := make([]byte, 8192)
	copy(data, "PMTiles\x03")
	for i := 8; i < len(data); i++ {
		data[i] = byte(i * 7 % 253)
	}
	writeFile(t, filepath.Join(root, "maps", "test-region-2026.pmtiles"), string(data))
	outside := t.TempDir()
	writeFile(t, filepath.Join(outside, "secret.pmtiles"), "TOP-SECRET")
	if err := os.Symlink(filepath.Join(outside, "secret.pmtiles"), filepath.Join(root, "maps", "leak-region-2026.pmtiles")); err != nil {
		t.Skip(err)
	}

	s := pageServer(t, tilesRoute(root))
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	s.port.Store(int32(portOf(t, srv.URL)))
	get := func(path string, hdr map[string]string) (*http.Response, []byte) {
		t.Helper()
		req, _ := http.NewRequest("GET", srv.URL+path, nil)
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp, b
	}
	const tile = "/maps/tiles/test-region-2026.pmtiles"

	t.Run("header read, the way a pmtiles client starts", func(t *testing.T) {
		resp, body := get(tile, map[string]string{"Range": "bytes=0-126"})
		if resp.StatusCode != 206 || !bytes.Equal(body, data[:127]) {
			t.Fatalf("%d, %d bytes", resp.StatusCode, len(body))
		}
		if resp.Header.Get("Content-Range") != "bytes 0-126/"+strconv.Itoa(len(data)) || resp.Header.Get("Accept-Ranges") != "bytes" {
			t.Fatalf("headers: %v", resp.Header)
		}
	})
	t.Run("tile read in the middle", func(t *testing.T) {
		resp, body := get(tile, map[string]string{"Range": "bytes=4096-5119"})
		if resp.StatusCode != 206 || !bytes.Equal(body, data[4096:5120]) {
			t.Fatalf("%d", resp.StatusCode)
		}
	})
	t.Run("whole file", func(t *testing.T) {
		resp, body := get(tile, nil)
		if resp.StatusCode != 200 || !bytes.Equal(body, data) || resp.Header.Get("Accept-Ranges") != "bytes" {
			t.Fatalf("%d", resp.StatusCode)
		}
	})
	t.Run("multi-range is 416", func(t *testing.T) {
		resp, _ := get(tile, map[string]string{"Range": "bytes=0-9,100-109"})
		if resp.StatusCode != 416 || strings.HasPrefix(resp.Header.Get("Content-Type"), "multipart/") {
			t.Fatalf("%d %v", resp.StatusCode, resp.Header)
		}
	})
	t.Run("refused ids", func(t *testing.T) {
		for _, p := range []string{
			"/maps/tiles/missing-region-2026.pmtiles", "/maps/tiles/leak-region-2026.pmtiles", "/maps/tiles/..%2f..%2fetc%2fpasswd.pmtiles",
			"/maps/tiles/test-region-2026.pmtiles%00.png", "/maps/tiles/TEST.pmtiles", "/maps/tiles/a.pmtiles", "/maps/tiles/test-region-2026",
			"/maps/tiles/", "/maps/tiles/.pmtiles",
		} {
			resp, body := get(p, nil)
			if resp.StatusCode != 404 || bytes.Contains(body, []byte("TOP-SECRET")) {
				t.Errorf("%s: %d %q", p, resp.StatusCode, body)
			}
		}
	})
}
