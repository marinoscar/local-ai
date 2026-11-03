package app

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/execx"
	"github.com/marinoscar/local-ai/agent/internal/sync/download"
	"github.com/marinoscar/local-ai/agent/internal/sync/manifest"
	"github.com/marinoscar/local-ai/agent/internal/sync/resolve"
	"github.com/marinoscar/local-ai/agent/internal/sync/run"
)

// The library and maps source types (SYNC-10): kiwix and pmtiles_extract go through the real engine,
// machine, journal, validators and swap. The outside world is two invented hosts served by one local TLS
// server (the sync client is told to dial it whatever the address), and a fake pmtiles tool.

const (
	libKiwixHost  = "mirror.example.test"
	libPlanetHost = "tiles.example.test"
	libBook       = "wiki_alpha_en_nopic"
	libNewest     = "2026-09"
	libZIM        = libBook + "_" + libNewest + ".zim"
)

func libFixture(t *testing.T, rel string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "sync", "resolve", "testdata", filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// libCert is a self-signed certificate for the two invented hosts.
func libCert(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "library test"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true, IsCA: true, DNSNames: []string{libKiwixHost, libPlanetHost},
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, pool
}

// libWorld is the two hosts. zim is what the kiwix host serves as the newest book.
type libWorld struct {
	srv *httptest.Server
	zim []byte
	sum string

	mu   sync.Mutex
	reqs []string
}

func newLibWorld(t *testing.T, zim []byte) (*libWorld, *download.HTTPClient) {
	t.Helper()
	cert, pool := libCert(t)
	h := sha256.Sum256(zim)
	w := &libWorld{zim: zim, sum: hex.EncodeToString(h[:])}
	w.srv = httptest.NewUnstartedServer(http.HandlerFunc(w.serve))
	w.srv.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	w.srv.StartTLS()
	t.Cleanup(w.srv.Close)
	addr := w.srv.Listener.Addr().String()
	return w, download.NewHTTPClient(download.ClientConfig{
		RootCAs: pool,
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, network, addr)
		},
	})
}

func (w *libWorld) serve(rw http.ResponseWriter, r *http.Request) {
	host := strings.Split(r.Host, ":")[0]
	w.mu.Lock()
	w.reqs = append(w.reqs, r.Method+" "+host+r.URL.Path)
	w.mu.Unlock()
	switch {
	case host == libKiwixHost && r.URL.Path == "/zim/alpha/":
		_, _ = rw.Write([]byte(`<html><a href="` + libBook + `_2026-08.zim">old</a><a href="` + libZIM + `">new</a><a href="` + libZIM + `.torrent">t</a></html>`))
	case host == libKiwixHost && r.URL.Path == "/zim/alpha/"+libZIM+".sha256":
		_, _ = rw.Write([]byte(w.sum + "  " + libZIM + "\n"))
	case host == libKiwixHost && r.URL.Path == "/zim/alpha/"+libZIM:
		http.ServeContent(rw, r, libZIM, time.Time{}, strings.NewReader(string(w.zim)))
	case host == libPlanetHost && strings.HasSuffix(r.URL.Path, ".pmtiles"):
		rw.WriteHeader(http.StatusOK) // every build date exists
	default:
		http.NotFound(rw, r)
	}
}

// libToolRunner runs the fake pmtiles for real and leaves every other tool to the rig's runner.
type libToolRunner struct {
	tool  string
	inner execx.Runner
}

func (l libToolRunner) Run(ctx context.Context, c execx.Cmd) (execx.Result, error) {
	if c.Path == l.tool {
		return execx.Exec{}.Run(ctx, c)
	}
	return l.inner.Run(ctx, c)
}

// newLibRig is a node whose catalog has wiki-alpha-en and map-region-alpha in its profile, an
// installed fake pmtiles in mode (empty: not installed) and the two invented hosts as its network.
func newLibRig(t *testing.T, zim []byte, toolMode string) (*rig, *libWorld) {
	t.Helper()
	w, httpc := newLibWorld(t, zim)
	var toolPath string
	r := newRig(t, func(r *rig) {
		editFile(t, r.path("etc/localai/catalog/profiles/small-8gb.yaml"), func(s string) string {
			s = mustReplace(t, s, "content: [wiki-alpha-en, medical-alpha-en, map-region-alpha, map-assets, places-region-alpha, kit-llama-cpp, kit-debs-resolute-amd64, docs-bundle]",
				"content: [wiki-alpha-en, map-region-alpha]")
			return mustReplace(t, s, "min_free_bytes: 25000000000", "min_free_bytes: 1000000")
		})
		if toolMode != "" {
			dir := r.path("opt/localai/pmtiles/current")
			r.mkdir("opt/localai/pmtiles/current")
			toolPath = filepath.Join(dir, "pmtiles")
			for _, f := range []struct {
				name string
				data []byte
				perm os.FileMode
			}{
				{"pmtiles", libFixture(t, "pmtiles/fake-pmtiles.sh"), 0o755},
				{"tiny.pmtiles", libFixture(t, "pmtiles/tiny.pmtiles"), 0o644},
				{"mode", []byte(toolMode + "\n"), 0o644},
			} {
				if err := os.WriteFile(filepath.Join(dir, f.name), f.data, f.perm); err != nil {
					t.Fatal(err)
				}
			}
			r.env.Runner = libToolRunner{tool: toolPath, inner: r.env.Runner}
		}
		r.env.NetState = func() run.NetState { return run.NetState{Mode: "online", Online: true} }
		r.env.SyncHTTP = httpc
	})
	return r, w
}

func libSync(t *testing.T, r *rig, items ...string) run.RunStatus {
	t.Helper()
	got := r.do("POST", "/ark/sync", map[string]any{"items": items})
	if got.status != http.StatusAccepted {
		t.Fatalf("POST /ark/sync = %d %s", got.status, got.body)
	}
	id, _ := got.json()["run_id"].(string)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := r.d.Components().SyncEngine.Wait(ctx, id); err != nil {
		t.Fatalf("the run %s did not end: %v\n%s", id, err, r.logText())
	}
	st, ok := r.d.Components().SyncEngine.Status()
	if !ok || st.RunID != id {
		t.Fatalf("no status for %s: %+v", id, st)
	}
	return st
}

func libManifest(t *testing.T, r *rig, rel string) (manifest.Manifest, error) {
	t.Helper()
	return manifest.Read(config.OSFS{}, manifest.Path(filepath.Join(r.path("srv/localai"), filepath.FromSlash(rel))))
}

func libExists(r *rig, rel string) bool {
	_, err := os.Stat(filepath.Join(r.path("srv/localai"), filepath.FromSlash(rel)))
	return err == nil
}

func TestLibrarySourcesAreWiredToTheNodeAndSyncOnline(t *testing.T) {
	r, w := newLibRig(t, libFixture(t, "zim/tiny.zim"), "ok")
	r.start()
	r.waitStarted()
	st := libSync(t, r, "wiki-alpha-en", "map-region-alpha")
	if st.State != run.StateDone || st.Result != run.ResultOK {
		t.Fatalf("the run ended %+v\n%s", st, r.logText())
	}
	for _, id := range []string{"wiki-alpha-en", "map-region-alpha"} {
		if res, code := resultOf(st, id); res != "verified" {
			t.Fatalf("%s ended %q %s (%v)\n%s", id, res, code, st.Items, r.logText())
		}
	}

	m, err := libManifest(t, r, "kiwix/wiki-alpha-en")
	if err != nil {
		t.Fatal(err)
	}
	if m.Version != libNewest || m.SortKey != libNewest || m.Trust != manifest.TrustSidecar || m.Source.Type != "kiwix" || m.Source.String("book") != libBook || len(m.Files) != 1 {
		t.Fatalf("kiwix manifest %+v", m)
	}
	f := m.Files[0]
	if f.Path != libZIM || f.Kind != "zim" || f.SHA256 != w.sum || f.SHA256Source != "kiwix_sidecar" || f.ZimMD5OK == nil || !*f.ZimMD5OK {
		t.Fatalf("kiwix file %+v", f)
	}
	got, err := os.ReadFile(filepath.Join(r.path("srv/localai"), "kiwix", "wiki-alpha-en", libZIM))
	if err != nil || string(got) != string(w.zim) {
		t.Fatalf("the ZIM is not installed: %v", err)
	}

	mm, err := libManifest(t, r, "maps/map-region-alpha")
	if err != nil {
		t.Fatal(err)
	}
	if mm.Trust != manifest.TrustComputed || mm.Source.Type != "pmtiles_extract" || mm.Source.String("bbox") != "-92.5,7,-77,18.6" || mm.TreeSHA256 == "" || mm.FileCount != 1 {
		t.Fatalf("maps manifest %+v", mm)
	}
	got, err = os.ReadFile(filepath.Join(r.path("srv/localai"), "maps", "map-region-alpha", "map-region-alpha.pmtiles"))
	if err != nil || string(got) != string(libFixture(t, "pmtiles/tiny.pmtiles")) {
		t.Fatalf("the map is not installed: %v", err)
	}

	// The sidecar came from the origin host and nothing else was asked of the planet but HEAD probes.
	for _, q := range w.requests() {
		if strings.HasSuffix(q, ".sha256") && !strings.HasPrefix(q, "GET "+libKiwixHost) {
			t.Errorf("sidecar request %q", q)
		}
	}
}

func (w *libWorld) requests() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.reqs...)
}

func TestLibraryKiwixZIMWithABadInternalChecksumIsQuarantined(t *testing.T) {
	bad := libFixture(t, "zim/tiny.zim")
	bad[200] ^= 0x01 // the sha256 of these bytes is what the sidecar says, so only the ZIM's own MD5 can tell
	r, _ := newLibRig(t, bad, "ok")
	r.start()
	r.waitStarted()
	st := libSync(t, r, "wiki-alpha-en", "map-region-alpha")
	if res, code := resultOf(st, "wiki-alpha-en"); res != "quarantined" || code != doctor.SYN028 {
		t.Fatalf("wiki-alpha-en ended %q %s, want quarantined LAI-SYN-028 (%v)\n%s", res, code, st.Items, r.logText())
	}
	if libExists(r, "kiwix/wiki-alpha-en/"+libZIM) || libExists(r, "kiwix/wiki-alpha-en/MANIFEST.json") {
		t.Error("a ZIM that failed its checksum was installed")
	}
	if res, code := resultOf(st, "map-region-alpha"); res != "verified" {
		t.Errorf("the other item must go on: map-region-alpha ended %q %s", res, code)
	}
}

func TestLibraryPMTilesVerifyFailureIsQuarantined(t *testing.T) {
	for _, mode := range []string{"verify_fail", "wrong_type", "small_bbox"} {
		t.Run(mode, func(t *testing.T) {
			r, _ := newLibRig(t, libFixture(t, "zim/tiny.zim"), mode)
			r.start()
			r.waitStarted()
			st := libSync(t, r, "wiki-alpha-en", "map-region-alpha")
			if res, code := resultOf(st, "map-region-alpha"); res != "quarantined" || code != doctor.SYN023 {
				t.Fatalf("map-region-alpha ended %q %s, want quarantined LAI-SYN-023 (%v)\n%s", res, code, st.Items, r.logText())
			}
			if libExists(r, "maps/map-region-alpha/map-region-alpha.pmtiles") || libExists(r, "maps/map-region-alpha/MANIFEST.json") {
				t.Error("an extract that failed its checks was installed")
			}
			if res, code := resultOf(st, "wiki-alpha-en"); res != "verified" {
				t.Errorf("the other item must go on: wiki-alpha-en ended %q %s", res, code)
			}
		})
	}
}

func TestLibraryMissingPMTilesBinaryFailsOnlyTheMap(t *testing.T) {
	r, _ := newLibRig(t, libFixture(t, "zim/tiny.zim"), "") // no tool installed
	r.start()
	r.waitStarted()
	st := libSync(t, r, "wiki-alpha-en", "map-region-alpha")
	res, code := resultOf(st, "map-region-alpha")
	if res != "failed" || code != doctor.SYN061 {
		t.Fatalf("map-region-alpha ended %q %s, want failed LAI-SYN-061 (%v)\n%s", res, code, st.Items, r.logText())
	}
	if res, code := resultOf(st, "wiki-alpha-en"); res != "verified" {
		t.Errorf("the other item must go on: wiki-alpha-en ended %q %s\n%s", res, code, r.logText())
	}
	if libExists(r, "maps/map-region-alpha/MANIFEST.json") {
		t.Error("a map was installed with no tool")
	}
}

// The resolvers are bound to the node: the tool path under the node's root, the metadata client of
// the sync downloader and the service user's credential when there is one.
func TestLibraryResolversAreBoundToTheNode(t *testing.T) {
	r, _ := newLibRig(t, libFixture(t, "zim/tiny.zim"), "ok")
	d := r.start()
	r.waitStarted()
	c := d.Components()
	pmr, _ := c.SyncResolvers.For("pmtiles_extract")
	pm, ok := pmr.(*resolve.PMTiles)
	if !ok {
		t.Fatalf("the pmtiles_extract resolver is a %T", pmr)
	}
	if want := r.path("opt/localai/pmtiles/current/pmtiles"); pm.Tool != want {
		t.Errorf("tool %s, want %s", pm.Tool, want)
	}
	if pm.Credential != nil {
		t.Errorf("an unprivileged run must not drop to the service user: %+v", pm.Credential)
	}
	if pm.Env == nil || pm.Env.SyncHome != d.syncDir() {
		t.Errorf("the pmtiles environment is not the sources' one: %+v", pm.Env)
	}
}
