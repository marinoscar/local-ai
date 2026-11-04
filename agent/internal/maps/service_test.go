package maps

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/ark"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/svc/health"
)

const mapID = "osm-central-america"

type env struct {
	svc  *Maps
	srv  *httptest.Server
	data []byte
	dir  string
	cp   fakeCopies
}

func newEnv(t *testing.T, mutate func(e *env)) *env {
	t.Helper()
	e := &env{dir: t.TempDir(), cp: fakeCopies{}}
	e.data = costaRica().build(t)
	e.cp[mapID] = mapCopy(t, e.dir, mapID, "internal", "20261015", testSHA, e.data)
	e.cp[ark.MapAssetsID] = assetsCopy(t, e.dir, true)
	if mutate != nil {
		mutate(e)
	}
	e.svc = New(Deps{Copies: e.cp, Metrics: health.NewMetrics(), Items: func() []MapItem {
		return []MapItem{
			{ID: mapID, DisplayName: "Offline map: Costa Rica & Central America", DisplayNameES: "Mapa sin internet", Tier: 1, InProfile: true},
			{ID: "osm-north-america", DisplayName: "Offline map: North America", Tier: 3},
		}
	}})
	mux := http.NewServeMux()
	for _, rt := range e.svc.Routes() {
		mux.Handle(rt.Method+" "+rt.Path(), rt.Handler)
	}
	e.srv = httptest.NewServer(mux)
	t.Cleanup(e.srv.Close)
	return e
}

func (e *env) do(t *testing.T, method, path string, hdr map[string]string) (*http.Response, []byte) {
	t.Helper()
	req, _ := http.NewRequest(method, e.srv.URL+path, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := e.srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, b
}

func code(b []byte) string {
	var env struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(b, &env)
	return env.Error.Code
}

const tilePath = "/maps/tiles/" + mapID + ".pmtiles"

func TestTileRange(t *testing.T) {
	e := newEnv(t, nil)
	etag := `"0123456789abcdef"`

	t.Run("single range gives 206 and the right bytes", func(t *testing.T) {
		resp, body := e.do(t, "GET", tilePath, map[string]string{"Range": "bytes=0-126"})
		if resp.StatusCode != 206 || !bytes.Equal(body, e.data[:127]) {
			t.Fatalf("%d, %d bytes", resp.StatusCode, len(body))
		}
		if got := resp.Header.Get("Content-Range"); got != "bytes 0-126/"+itoa(len(e.data)) {
			t.Errorf("Content-Range %q", got)
		}
		if string(body[:7]) != "PMTiles" {
			t.Errorf("magic %q", body[:7])
		}
	})
	t.Run("headers", func(t *testing.T) {
		resp, _ := e.do(t, "GET", tilePath, nil)
		h := resp.Header
		if resp.StatusCode != 200 || h.Get("ETag") != etag || h.Get("Cache-Control") != "no-cache" ||
			h.Get("Content-Type") != "application/vnd.pmtiles" || h.Get("Accept-Ranges") != "bytes" {
			t.Fatalf("%d %v", resp.StatusCode, h)
		}
	})
	t.Run("suffix range", func(t *testing.T) {
		resp, body := e.do(t, "GET", tilePath, map[string]string{"Range": "bytes=-10"})
		if resp.StatusCode != 206 || !bytes.Equal(body, e.data[len(e.data)-10:]) {
			t.Fatalf("%d", resp.StatusCode)
		}
	})
	t.Run("multi-range is 416", func(t *testing.T) {
		resp, _ := e.do(t, "GET", tilePath, map[string]string{"Range": "bytes=0-10,20-30"})
		if resp.StatusCode != 416 || resp.Header.Get("Content-Range") != "bytes */"+itoa(len(e.data)) {
			t.Fatalf("%d %v", resp.StatusCode, resp.Header)
		}
	})
	t.Run("range past the end is 416", func(t *testing.T) {
		resp, _ := e.do(t, "GET", tilePath, map[string]string{"Range": "bytes=999999999-"})
		if resp.StatusCode != 416 {
			t.Fatalf("%d", resp.StatusCode)
		}
	})
	t.Run("If-Range with the matching ETag gives 206", func(t *testing.T) {
		resp, body := e.do(t, "GET", tilePath, map[string]string{"Range": "bytes=0-6", "If-Range": etag})
		if resp.StatusCode != 206 || string(body) != "PMTiles" {
			t.Fatalf("%d %q", resp.StatusCode, body)
		}
	})
	t.Run("If-Range with a stale ETag gives the whole file", func(t *testing.T) {
		resp, body := e.do(t, "GET", tilePath, map[string]string{"Range": "bytes=0-6", "If-Range": `"ffffffffffffffff"`})
		if resp.StatusCode != 200 || !bytes.Equal(body, e.data) {
			t.Fatalf("%d, %d bytes", resp.StatusCode, len(body))
		}
	})
	t.Run("If-None-Match revalidates", func(t *testing.T) {
		resp, _ := e.do(t, "GET", tilePath, map[string]string{"If-None-Match": etag})
		if resp.StatusCode != 304 {
			t.Fatalf("%d", resp.StatusCode)
		}
	})
	t.Run("HEAD sends headers only", func(t *testing.T) {
		resp, body := e.do(t, "HEAD", tilePath, nil)
		if resp.StatusCode != 200 || len(body) != 0 || resp.Header.Get("Content-Length") != itoa(len(e.data)) {
			t.Fatalf("%d, %d bytes, %v", resp.StatusCode, len(body), resp.Header)
		}
	})
	t.Run("POST is not allowed", func(t *testing.T) {
		resp, _ := e.do(t, "POST", tilePath, nil)
		if resp.StatusCode != 405 {
			t.Fatalf("%d", resp.StatusCode)
		}
	})
}

func itoa(n int) string { return strconv.Itoa(n) }

func TestTileNotFound(t *testing.T) {
	e := newEnv(t, func(e *env) {
		delete(e.cp, mapID)
		e.cp["osm-north-america"] = mapCopy(t, e.dir, "osm-north-america", "internal", "1", testSHA, func() []byte {
			f := costaRica()
			f.magic = "NotTile"
			return f.build(t)
		}())
		e.cp["wikimed-en"] = mapCopy(t, e.dir, "wikimed-en", "internal", "1", testSHA, costaRica().build(t))
	})
	tests := []struct {
		name, path, wantCode string
	}{
		{"no verified copy", tilePath, doctor.SVC030},
		{"unknown id", "/maps/tiles/nope-nope.pmtiles", doctor.SVC030},
		{"an item that is not a catalog map", "/maps/tiles/wikimed-en.pmtiles", doctor.SVC030},
		{"bad header", "/maps/tiles/osm-north-america.pmtiles", doctor.SVC034},
		{"not a pmtiles name", "/maps/tiles/" + mapID, doctor.SVC030},
		{"traversal in the name", "/maps/tiles/..%2f..%2fetc%2fpasswd.pmtiles", doctor.SVC030},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resp, body := e.do(t, "GET", tc.path, nil)
			if resp.StatusCode != 404 || code(body) != tc.wantCode {
				t.Fatalf("%d %s", resp.StatusCode, body)
			}
		})
	}
}

func TestTileNoOpenHandleAfterResponse(t *testing.T) {
	if _, err := os.Stat("/proc/self/fd"); err != nil {
		t.Skip("no /proc")
	}
	e := newEnv(t, nil)
	p := e.cp[mapID].Files[0]
	for i := 0; i < 3; i++ {
		e.do(t, "GET", tilePath, map[string]string{"Range": "bytes=0-126"})
		e.do(t, "GET", "/maps/catalog.json", nil)
	}
	ents, _ := os.ReadDir("/proc/self/fd")
	for _, ent := range ents {
		if l, err := os.Readlink(filepath.Join("/proc/self/fd", ent.Name())); err == nil && l == p {
			t.Fatalf("fd %s still has %s open", ent.Name(), p)
		}
	}
}

func TestTileBytesMetric(t *testing.T) {
	e := newEnv(t, nil)
	e.do(t, "GET", tilePath, map[string]string{"Range": "bytes=0-99"})
	e.do(t, "HEAD", tilePath, nil)
	e.do(t, "GET", tilePath, map[string]string{"Range": "bytes=0-1,5-6"})
	if got := e.svc.metrics.Counters()[health.MetricMapsTileBytes]; got != int64(100) {
		t.Fatalf("tile bytes = %v, want 100", got)
	}
}

func TestCatalog(t *testing.T) {
	e := newEnv(t, nil)
	resp, body := e.do(t, "GET", "/maps/catalog.json", nil)
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") {
		t.Fatalf("%d %v", resp.StatusCode, resp.Header)
	}
	var c Catalog
	if err := json.Unmarshal(body, &c); err != nil {
		t.Fatal(err)
	}
	if len(c.Maps) != 1 {
		t.Fatalf("maps: %+v", c.Maps)
	}
	m := c.Maps[0]
	h, _ := ParseHeader(e.data)
	if m.ID != mapID || m.URL != tilePath || m.Version != "20261015" || m.Root != "internal" || !m.Default {
		t.Errorf("%+v", m)
	}
	if m.Bounds != [4]float64{h.MinLon, h.MinLat, h.MaxLon, h.MaxLat} || m.Bounds[0] != -92.5 || m.Bounds[3] != 18.6 {
		t.Errorf("bounds %v", m.Bounds)
	}
	if m.MinZoom != 0 || m.MaxZoom != 15 || m.Center != [3]float64{-84.08, 9.93, 8} {
		t.Errorf("zooms/center %d %d %v", m.MinZoom, m.MaxZoom, m.Center)
	}
	if m.DisplayNameES != "Mapa sin internet" {
		t.Errorf("es name %q", m.DisplayNameES)
	}
	if !c.Assets.Available || c.Assets.Glyphs != GlyphsTemplate || c.Assets.Sprite != SpritePath || len(c.Assets.Fonts) != 3 {
		t.Errorf("assets %+v", c.Assets)
	}
	if c.Search.Available || c.GPS.Available {
		t.Errorf("search %+v gps %+v", c.Search, c.GPS)
	}
	// "source" is a JSON null, and "maps" never null.
	if !bytes.Contains(body, []byte(`"gps":{"available":false,"source":null}`)) {
		t.Errorf("gps block: %s", body)
	}
	if resp, hb := e.do(t, "HEAD", "/maps/catalog.json", nil); resp.StatusCode != 200 || len(hb) != 0 {
		t.Errorf("HEAD %d %d", resp.StatusCode, len(hb))
	}
}

type fakeSearch struct{ info SearchInfo }

func (f fakeSearch) SearchInfo() SearchInfo { return f.info }

func TestCatalogCases(t *testing.T) {
	t.Run("no map", func(t *testing.T) {
		e := newEnv(t, func(e *env) { delete(e.cp, mapID) })
		var c Catalog
		_, body := e.do(t, "GET", "/maps/catalog.json", nil)
		if err := json.Unmarshal(body, &c); err != nil || c.Maps == nil || len(c.Maps) != 0 {
			t.Fatalf("%s %v", body, err)
		}
		if !bytes.Contains(body, []byte(`"maps":[]`)) {
			t.Errorf("maps must be []: %s", body)
		}
	})
	t.Run("bad header is omitted", func(t *testing.T) {
		for name, mk := range map[string]func(*fixture){
			"bad magic": func(f *fixture) { f.magic = "XXXXXXX" },
			"version 2": func(f *fixture) { f.magic, f.version = "PM", 2 },
		} {
			t.Run(name, func(t *testing.T) {
				e := newEnv(t, func(e *env) {
					f := costaRica()
					mk(&f)
					e.cp[mapID] = mapCopy(t, e.dir, mapID, "internal", "1", testSHA, f.build(t))
				})
				var c Catalog
				_, body := e.do(t, "GET", "/maps/catalog.json", nil)
				_ = json.Unmarshal(body, &c)
				if len(c.Maps) != 0 {
					t.Fatalf("a damaged map was listed: %s", body)
				}
				if rd := e.svc.readiness(context.Background()); !hasCode(rd.Problems, doctor.SVC034) {
					t.Errorf("readiness %+v", rd)
				}
			})
		}
	})
	t.Run("no assets", func(t *testing.T) {
		e := newEnv(t, func(e *env) { delete(e.cp, ark.MapAssetsID) })
		var c Catalog
		_, body := e.do(t, "GET", "/maps/catalog.json", nil)
		_ = json.Unmarshal(body, &c)
		if c.Assets.Available {
			t.Fatal("assets must be unavailable")
		}
		if rd := e.svc.readiness(context.Background()); !hasCode(rd.Problems, doctor.SVC031) {
			t.Errorf("readiness %+v", rd)
		}
	})
	t.Run("assets without the fonts", func(t *testing.T) {
		e := newEnv(t, func(e *env) { e.cp[ark.MapAssetsID] = assetsCopy(t, t.TempDir(), false) })
		if e.svc.Catalog().Assets.Available {
			t.Fatal("missing fonts must make the assets unavailable")
		}
	})
	t.Run("search provider", func(t *testing.T) {
		e := newEnv(t, nil)
		e.svc.SetSearch(fakeSearch{SearchInfo{Available: true, Places: 287341, Source: "places-central-america"}})
		s := e.svc.Catalog().Search
		if !s.Available || s.Places != 287341 || s.Source == nil || *s.Source != "places-central-america" {
			t.Fatalf("%+v", s)
		}
	})
	t.Run("default map follows node.yaml", func(t *testing.T) {
		e := newEnv(t, func(e *env) {
			e.cp["osm-north-america"] = mapCopy(t, e.dir, "osm-north-america", "ark-1234abcd", "2", testSHA, costaRica().build(t))
		})
		cfg := Config{Enabled: true, DefaultMap: "auto"}
		e.svc.cfg = func() Config { return cfg }
		def := func() string {
			for _, m := range e.svc.Catalog().Maps {
				if m.Default {
					return m.ID
				}
			}
			return ""
		}
		if got := def(); got != mapID {
			t.Errorf("auto: %q", got)
		}
		cfg.DefaultMap = "osm-north-america"
		if got := def(); got != "osm-north-america" {
			t.Errorf("explicit: %q", got)
		}
		cfg.DefaultMap = "not-installed"
		if got := def(); got != mapID {
			t.Errorf("unknown id falls back to auto: %q", got)
		}
		if n := len(e.svc.Catalog().Maps); n != 2 {
			t.Errorf("both maps listed, got %d", n)
		}
	})
}

func hasCode(ps []health.Problem, c string) bool {
	for _, p := range ps {
		if p.Code == c {
			return true
		}
	}
	return false
}

func TestAssets(t *testing.T) {
	e := newEnv(t, nil)
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("TOP-SECRET"), 0o644); err != nil {
		t.Fatal(err)
	}
	root := e.cp[ark.MapAssetsID].Dir
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(root, "leak.txt")); err != nil {
		t.Skip(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".hidden"), []byte("dot"), 0o644); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name, path string
		want       int
		body       string
	}{
		{"a glyph range with spaces", "/maps/assets/fonts/Noto%20Sans%20Regular/0-255.pbf", 200, "pbf-Noto Sans Regular"},
		{"sprite json", "/maps/assets/sprites/v4/light.json", 200, "{}"},
		{"missing file", "/maps/assets/fonts/Nope/0-255.pbf", 404, ""},
		{"encoded dot dot", "/maps/assets/%2e%2e/%2e%2e/etc/passwd", 404, ""},
		{"double encoded dot dot", "/maps/assets/%252e%252e/etc/passwd", 404, ""},
		{"symlink out of the root", "/maps/assets/leak.txt", 404, ""},
		{"dotfile", "/maps/assets/.hidden", 404, ""},
		{"a directory", "/maps/assets/fonts/", 404, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resp, body := e.do(t, "GET", tc.path, nil)
			if resp.StatusCode != tc.want {
				t.Fatalf("%d %s", resp.StatusCode, body)
			}
			if tc.want == 200 && string(body) != tc.body {
				t.Errorf("body %q", body)
			}
			if strings.Contains(string(body), "TOP-SECRET") {
				t.Fatal("leaked")
			}
		})
	}
	if resp, _ := e.do(t, "GET", "/maps/assets/fonts/Noto%20Sans%20Regular/0-255.pbf", nil); resp.Header.Get("Content-Type") != "application/x-protobuf" {
		t.Errorf("content type %q", resp.Header.Get("Content-Type"))
	}
	if resp, body := e.do(t, "HEAD", "/maps/assets/sprites/v4/light.json", nil); resp.StatusCode != 200 || len(body) != 0 {
		t.Errorf("HEAD %d %d", resp.StatusCode, len(body))
	}
	if resp, _ := e.do(t, "POST", "/maps/assets/sprites/v4/light.json", nil); resp.StatusCode != 405 {
		t.Errorf("POST %d", resp.StatusCode)
	}
	t.Run("no assets installed", func(t *testing.T) {
		e := newEnv(t, func(e *env) { delete(e.cp, ark.MapAssetsID) })
		resp, body := e.do(t, "GET", "/maps/assets/sprites/v4/light.json", nil)
		if resp.StatusCode != 404 || code(body) != doctor.SVC031 {
			t.Fatalf("%d %s", resp.StatusCode, body)
		}
	})
}

func TestHealthProbe(t *testing.T) {
	ctx := context.Background()
	t.Run("everything present", func(t *testing.T) {
		e := newEnv(t, nil)
		p := e.svc.Probe()
		if err := p.Liveness(ctx); err != nil {
			t.Fatal(err)
		}
		if c := p.Content(ctx); c.Missing {
			t.Fatalf("%+v", c)
		}
		rd := p.Readiness(ctx)
		if len(rd.Problems) != 0 || len(rd.Maps) != 1 || rd.Maps[0] != mapID {
			t.Fatalf("%+v", rd)
		}
		if ps := p.Deep(ctx); len(ps) != 0 {
			t.Fatalf("deep: %+v", ps)
		}
	})
	t.Run("no map", func(t *testing.T) {
		e := newEnv(t, func(e *env) { delete(e.cp, mapID) })
		p := e.svc.Probe()
		if c := p.Content(ctx); !c.Missing || c.Code != doctor.SVC030 {
			t.Fatalf("%+v", c)
		}
		if err := p.Liveness(ctx); err == nil || !strings.Contains(err.Error(), doctor.SVC030) {
			t.Fatalf("liveness %v", err)
		}
		if ps := p.Deep(ctx); !hasCode(ps, doctor.SVC030) {
			t.Fatalf("deep %+v", ps)
		}
	})
	t.Run("damaged header", func(t *testing.T) {
		e := newEnv(t, func(e *env) {
			f := costaRica()
			f.magic = "XXXXXXX"
			e.cp[mapID] = mapCopy(t, e.dir, mapID, "internal", "1", testSHA, f.build(t))
		})
		p := e.svc.Probe()
		if c := p.Content(ctx); c.Missing {
			t.Fatalf("a damaged map is not missing content: %+v", c)
		}
		if err := p.Liveness(ctx); err == nil || !strings.Contains(err.Error(), doctor.SVC034) {
			t.Fatalf("liveness %v", err)
		}
	})
	t.Run("assets missing is a warning", func(t *testing.T) {
		e := newEnv(t, func(e *env) { delete(e.cp, ark.MapAssetsID) })
		rd := e.svc.Probe().Readiness(ctx)
		if !hasCode(rd.Problems, doctor.SVC031) {
			t.Fatalf("%+v", rd)
		}
	})
	t.Run("deep fails when the center tile is not in the file", func(t *testing.T) {
		e := newEnv(t, func(e *env) {
			f := costaRica()
			f.tileZ = 9 // the file has a z=9 tile, the check asks for z=10
			e.cp[mapID] = mapCopy(t, e.dir, mapID, "internal", "1", testSHA, f.build(t))
		})
		if ps := e.svc.Probe().Deep(ctx); !hasCode(ps, doctor.SVC034) {
			t.Fatalf("%+v", ps)
		}
	})
	t.Run("deep fails when the tile is not gzip", func(t *testing.T) {
		e := newEnv(t, func(e *env) {
			f := costaRica()
			f.tile = []byte("plain text, not a tile")
			e.cp[mapID] = mapCopy(t, e.dir, mapID, "internal", "1", testSHA, f.build(t))
		})
		if ps := e.svc.Probe().Deep(ctx); !hasCode(ps, doctor.SVC034) {
			t.Fatalf("%+v", ps)
		}
	})
	t.Run("spec", func(t *testing.T) {
		e := newEnv(t, nil)
		sp := e.svc.Spec()
		if sp.Name != "maps" || sp.Unit != "" || sp.DownCode != doctor.SVC034 || !sp.Enabled() {
			t.Fatalf("%+v", sp)
		}
		e.svc.cfg = func() Config { return Config{Enabled: false} }
		if sp.Enabled() {
			t.Fatal("maps.enabled false must disable the service")
		}
	})
}

func TestConfigDefaults(t *testing.T) {
	if c := DefaultConfig(); !c.Enabled || c.DefaultMap != "auto" {
		t.Fatalf("%+v", c)
	}
}

func TestAssetsRawTraversal(t *testing.T) {
	e := newEnv(t, nil)
	h := e.svc.Routes()[2].Handler
	for _, p := range []string{"/maps/assets/../catalog.json", "/maps/assets/fonts/../../../etc/passwd", "/maps/assets//etc/passwd", "/maps/assets/fonts/\x00/0-255.pbf"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/", nil)
		req.URL.Path = p
		h.ServeHTTP(rec, req)
		if rec.Code != 404 {
			t.Errorf("%q gave %d", p, rec.Code)
		}
	}
}

type fakeGPS struct {
	avail bool
	src   string
}

func (f fakeGPS) CatalogGPS() (bool, string) { return f.avail, f.src }

func TestCatalogGPSBlock(t *testing.T) {
	e := newEnv(t, nil)
	if g := e.svc.Catalog().GPS; g.Available || g.Source != nil {
		t.Fatalf("no provider: %+v", g)
	}
	e.svc.SetGPS(fakeGPS{true, "gps"})
	if g := e.svc.Catalog().GPS; !g.Available || g.Source == nil || *g.Source != "gps" {
		t.Fatalf("gps: %+v", g)
	}
	e.svc.SetGPS(fakeGPS{false, "none"})
	if g := e.svc.Catalog().GPS; g.Available || g.Source != nil {
		t.Fatalf("none: %+v", g)
	}
}
