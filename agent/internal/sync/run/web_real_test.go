package run

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/ark"
	"github.com/marinoscar/local-ai/agent/internal/catalog"
	"github.com/marinoscar/local-ai/agent/internal/sync/download"
	"github.com/marinoscar/local-ai/agent/internal/sync/manifest"
	"github.com/marinoscar/local-ai/agent/internal/sync/plan"
	"github.com/marinoscar/local-ai/agent/internal/sync/resolve"
)

// The real url resolver with extract: true through the real engine and machine: the archive is
// downloaded and unpacked by Produce, the tree is verified and swapped in as a directory item, and the
// drive gets it by a local copy. The origin is a TLS server on the loopback.

func webTarGz(t *testing.T, files map[string]string, extra ...tar.Header) []byte {
	t.Helper()
	var raw bytes.Buffer
	tw := tar.NewWriter(&raw)
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	for _, n := range names {
		if err := tw.WriteHeader(&tar.Header{Name: n, Mode: 0o644, Size: int64(len(files[n])), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(files[n])); err != nil {
			t.Fatal(err)
		}
	}
	for i := range extra {
		if err := tw.WriteHeader(&extra[i]); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	_, _ = zw.Write(raw.Bytes())
	_ = zw.Close()
	return gz.Bytes()
}

func webStack(t *testing.T, srv *httptest.Server, it plan.CatalogItem) (*pstack, *resolve.URLSource) {
	t.Helper()
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	web := &resolve.Web{
		Env:  &resolve.Env{Online: func() bool { return true }, Now: func() time.Time { return epoch }},
		HTTP: srv.Client(),
		Downloader: download.New(download.Options{
			HTTP: download.NewHTTPClient(download.ClientConfig{RootCAs: pool}), Logger: quiet,
			Backoff: download.Backoff{Steps: []time.Duration{time.Nanosecond}, Jitter: -1},
		}),
	}
	u := &resolve.URLSource{W: web}
	s := newPStackRes(t, true, nil, map[string]ItemResolver{"url": u.Engine()}, it)
	return s, u
}

func TestRealURLExtractThroughTheEngine(t *testing.T) {
	data := webTarGz(t, map[string]string{"fonts/Alpha/0-255.pbf": "glyphs", "sprites/sprite.json": `{"a":1}`})
	sum := sha256.Sum256(data)
	pin := hex.EncodeToString(sum[:])
	var gets atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/dl/places-alpha.tar.gz" {
			http.NotFound(w, r)
			return
		}
		if r.Method == http.MethodGet {
			gets.Add(1)
		}
		http.ServeContent(w, r, "places-alpha.tar.gz", epoch, bytes.NewReader(data))
	}))
	defer srv.Close()

	yes := true
	it := plan.ContentItem(&catalog.ContentItem{ID: "places-alpha", Kind: "maps", Source: catalog.ContentSource{
		Type: "url", URL: srv.URL + "/dl/places-alpha.tar.gz", SHA256: pin, Extract: &yes,
	}})
	s, _ := webStack(t, srv, it)
	version := pin[:12]
	s.setRows(map[string][]plan.ItemPlan{
		ark.InternalID: {dlRow("places-alpha", "maps", version)},
		driveID:        {copyRow("places-alpha", "maps", version)},
	})
	st := s.sync()
	if st.State != StateDone || st.Result != ResultOK || st.Totals.Done != 2 {
		t.Fatalf("status = %+v (%+v)", st, st.Items)
	}
	if gets.Load() != 1 {
		t.Errorf("archive downloads = %d, want 1 (the drive copies locally)", gets.Load())
	}
	for _, r := range []ark.Root{s.internal, s.drive} {
		m, err := s.load(r, "maps/places-alpha")
		if err != nil {
			t.Fatalf("%s: %v", r.ID, err)
		}
		if m.Version != version || m.Trust != manifest.TrustPinned || !m.IsDir() || m.State != manifest.StateVerified {
			t.Fatalf("%s manifest = %+v", r.ID, m)
		}
		for rel, want := range map[string]string{"fonts/Alpha/0-255.pbf": "glyphs", "sprites/sprite.json": `{"a":1}`} {
			b, err := os.ReadFile(filepath.Join(r.Path, "maps", "places-alpha", filepath.FromSlash(rel)))
			if err != nil || string(b) != want {
				t.Errorf("%s: %s = %q, %v", r.ID, rel, b, err)
			}
		}
		// The archive itself is not part of the item.
		if _, err := os.Stat(filepath.Join(r.Path, "maps", "places-alpha", "places-alpha.tar.gz")); err == nil {
			t.Errorf("%s: the archive was installed with the tree", r.ID)
		}
		if m.Source.Type != "url" || m.Source.String("archive") != "places-alpha.tar.gz" || m.Source.String("archive_sha256") != pin {
			t.Errorf("%s source = %+v", r.ID, m.Source)
		}
		s.assertStagingClean(r)
	}
}

// An archive with a forbidden entry never reaches the root: the item fails with LAI-SYN-021 and nothing
// is installed.
func TestRealURLExtractRefusesAnUnsafeArchive(t *testing.T) {
	data := webTarGz(t, map[string]string{"ok.txt": "fine"}, tar.Header{Name: "link", Typeflag: tar.TypeSymlink, Linkname: "/", Mode: 0o777})
	sum := sha256.Sum256(data)
	pin := hex.EncodeToString(sum[:])
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, "x.tar.gz", epoch, bytes.NewReader(data))
	}))
	defer srv.Close()
	yes := true
	it := plan.ContentItem(&catalog.ContentItem{ID: "places-alpha", Kind: "maps", Source: catalog.ContentSource{
		Type: "url", URL: srv.URL + "/dl/x.tar.gz", SHA256: pin, Extract: &yes,
	}})
	s, _ := webStack(t, srv, it)
	s.setRows(map[string][]plan.ItemPlan{ark.InternalID: {dlRow("places-alpha", "maps", pin[:12])}})
	st := s.sync()
	var code string
	for _, r := range st.Items {
		if r.Item == "places-alpha" {
			code = r.Code
		}
	}
	if st.Result == ResultOK || code != "LAI-SYN-021" {
		t.Fatalf("status = %+v, code %q (%+v)", st, code, st.Items)
	}
	if _, err := os.Stat(filepath.Join(s.internal.Path, "maps", "places-alpha")); err == nil {
		t.Error("a refused archive was installed")
	}
}
