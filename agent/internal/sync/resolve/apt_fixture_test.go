package resolve

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/md5" //nolint:gosec // the fixture's Packages index carries an MD5sum field like a real archive
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/catalog"
	"github.com/marinoscar/local-ai/agent/internal/execx"
	"github.com/marinoscar/local-ai/agent/internal/sync/download"
	"github.com/marinoscar/local-ai/agent/internal/sync/plan"
)

// fxPkg is one package of the fixture archive.
type fxPkg struct {
	Name, Version string
	// Depends is the Depends field ("" for none).
	Depends string
}

// aptFixture is a tiny signed Ubuntu-style archive served over HTTP: a real InRelease signed with a
// throwaway key (generated at run time, never committed), a Packages index and real .deb files
// built with dpkg-deb. Every hash is computed here at run time.
type aptFixture struct {
	t       *testing.T
	root    string
	gnupg   string
	suite   string
	arch    string
	srv     *httptest.Server
	tools   map[string]string
	Keyring []byte
	// Pkgs is what publish writes; change it and publish again to make a new archive state.
	Pkgs []fxPkg
	// MD5Only publishes a Packages index and a Release with MD5 hashes only.
	MD5Only bool
	// now is the one clock reading of the fixture: the Release date is an hour before it, so apt's own
	// "not valid yet" check (which reads the real clock) passes.
	now time.Time

	// gen counts the publishes; each one stamps its files a minute later than the last, so apt's
	// If-Modified-Since never takes a second publish within the same second for "not modified".
	gen int

	mu       sync.Mutex
	hits     map[string]int
	override map[string]http.HandlerFunc
}

// newAptFixture builds, signs and serves an archive of pkgs for one suite and architecture. It skips
// the test when a needed tool is missing.
func newAptFixture(t *testing.T, suite, arch string, pkgs ...fxPkg) *aptFixture {
	t.Helper()
	tools := needTool(t, "apt-get", "apt-cache", "dpkg-deb", "gpg", "gpgconf")
	dir := t.TempDir()
	// gpg-agent's socket lives in the home directory and a socket path is limited to about 100
	// characters, which a test name in t.TempDir() exceeds: the key home gets a short path.
	gnupg, err := os.MkdirTemp("", "gh")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(gnupg) })
	f := &aptFixture{
		t: t, root: filepath.Join(dir, "www"), gnupg: gnupg, suite: suite, arch: arch,
		tools: tools, Pkgs: pkgs, now: time.Now(), hits: map[string]int{}, override: map[string]http.HandlerFunc{},
	}
	for _, d := range []string{f.root, f.gnupg} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		_, _ = execx.Exec{}.Run(context.Background(), execx.Cmd{Path: tools["gpgconf"], Args: []string{"--homedir", f.gnupg, "--kill", "gpg-agent"},
			Env: f.gpgEnv(), Timeout: 10 * time.Second, MaxOut: 1 << 16})
	})
	f.makeKey()
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	f.publish()
	return f
}

func (f *aptFixture) gpgEnv() []string {
	return []string{"PATH=" + execx.FixedPath, "GNUPGHOME=" + f.gnupg, "LC_ALL=C"}
}

func (f *aptFixture) gpg(args ...string) string {
	f.t.Helper()
	return runTool(f.t, f.gpgEnv(), f.tools["gpg"], args...)
}

func (f *aptFixture) makeKey() {
	params := filepath.Join(f.gnupg, "params")
	spec := "%no-protection\nKey-Type: eddsa\nKey-Curve: ed25519\nKey-Usage: sign\nName-Real: Fixture Repo\nName-Email: repo@example.test\nExpire-Date: 0\n%commit\n"
	if err := os.WriteFile(params, []byte(spec), 0o600); err != nil {
		f.t.Fatal(err)
	}
	f.gpg("--batch", "--generate-key", params)
	res, err := execx.Exec{}.Run(context.Background(), execx.Cmd{Path: f.tools["gpg"], Args: []string{"--batch", "--export"},
		Env: f.gpgEnv(), Timeout: 30 * time.Second, MaxOut: 1 << 20})
	if err != nil || res.ExitCode != 0 || len(res.Stdout) == 0 {
		f.t.Fatalf("exporting the fixture key: %v %s", err, res.Stderr)
	}
	f.Keyring = res.Stdout
}

// serve counts requests and lets a test replace the answer for a path.
func (f *aptFixture) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.hits[r.URL.Path]++
	h := f.override[r.URL.Path]
	f.mu.Unlock()
	if h != nil {
		h(w, r)
		return
	}
	http.FileServer(http.Dir(f.root)).ServeHTTP(w, r)
}

// Override answers requests for path with h until the end of the test.
func (f *aptFixture) Override(path string, h http.HandlerFunc) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.override[path] = h
}

// Hits is the number of requests whose path contains substr.
func (f *aptFixture) Hits(substr string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for p, c := range f.hits {
		if strings.Contains(p, substr) {
			n += c
		}
	}
	return n
}

// URL is the base URI of the archive (the "Ubuntu mirror" of the item).
func (f *aptFixture) URL() string { return f.srv.URL }

// Host is host:port of the archive.
func (f *aptFixture) Host() string { return strings.TrimPrefix(f.srv.URL, "http://") }

// FileName is the pool file name of a package (Debian drops the epoch from file names).
func (f *aptFixture) FileName(p fxPkg) string {
	v := p.Version
	if i := strings.Index(v, ":"); i >= 0 {
		v = v[i+1:]
	}
	return fmt.Sprintf("%s_%s_%s.deb", p.Name, v, f.arch)
}

// publish rebuilds the archive from f.Pkgs and signs it.
func (f *aptFixture) publish() {
	f.t.Helper()
	f.gen++
	pool := filepath.Join(f.root, "pool")
	if err := os.MkdirAll(pool, 0o755); err != nil {
		f.t.Fatal(err)
	}
	var index strings.Builder
	pk := append([]fxPkg(nil), f.Pkgs...)
	sort.Slice(pk, func(i, j int) bool { return pk[i].Name < pk[j].Name })
	for _, p := range pk {
		file := f.FileName(p)
		out := filepath.Join(pool, file)
		if _, err := os.Stat(out); err != nil {
			f.buildDeb(p, out)
		}
		data, err := os.ReadFile(out)
		if err != nil {
			f.t.Fatal(err)
		}
		md := md5.Sum(data) //nolint:gosec
		fmt.Fprintf(&index, "Package: %s\nVersion: %s\nArchitecture: %s\nMaintainer: Person A <person.a@example.test>\n", p.Name, p.Version, f.arch)
		if p.Depends != "" {
			fmt.Fprintf(&index, "Depends: %s\n", p.Depends)
		}
		fmt.Fprintf(&index, "Filename: pool/%s\nSize: %d\nMD5sum: %s\n", file, len(data), hex.EncodeToString(md[:]))
		if !f.MD5Only {
			fmt.Fprintf(&index, "SHA256: %s\n", sha256Hex(data))
		}
		fmt.Fprintf(&index, "Description: Fixture package %s\n Second line of the description.\n\n", p.Name)
	}
	rel := filepath.Join("main", "binary-"+f.arch)
	plain := []byte(index.String())
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	_, _ = zw.Write(plain)
	_ = zw.Close()
	files := map[string][]byte{filepath.Join(rel, "Packages"): plain, filepath.Join(rel, "Packages.gz"): gz.Bytes()}
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	// A real archive has <codename>, <codename>-updates and <codename>-security; the fixture serves
	// the same packages in all three.
	for _, suite := range []string{f.suite, f.suite + "-updates", f.suite + "-security"} {
		distDir := filepath.Join(f.root, "dists", suite)
		if err := os.MkdirAll(filepath.Join(distDir, rel), 0o755); err != nil {
			f.t.Fatal(err)
		}
		var release strings.Builder
		fmt.Fprintf(&release, "Origin: Fixture\nSuite: %s\nCodename: %s\nDate: %s\nArchitectures: %s\nComponents: main\n",
			suite, suite, f.now.Add(-time.Hour).UTC().Format("Mon, 02 Jan 2006 15:04:05 UTC"), f.arch)
		if f.MD5Only {
			release.WriteString("MD5Sum:\n")
		} else {
			release.WriteString("SHA256:\n")
		}
		for _, n := range names {
			if err := os.WriteFile(filepath.Join(distDir, n), files[n], 0o644); err != nil {
				f.t.Fatal(err)
			}
			if f.MD5Only {
				md := md5.Sum(files[n]) //nolint:gosec
				fmt.Fprintf(&release, " %s %d %s\n", hex.EncodeToString(md[:]), len(files[n]), filepath.ToSlash(n))
			} else {
				fmt.Fprintf(&release, " %s %d %s\n", sha256Hex(files[n]), len(files[n]), filepath.ToSlash(n))
			}
		}
		relPath := filepath.Join(distDir, "Release")
		if err := os.WriteFile(relPath, []byte(release.String()), 0o644); err != nil {
			f.t.Fatal(err)
		}
		f.gpg("--batch", "--yes", "--clearsign", "--output", filepath.Join(distDir, "InRelease"), relPath)
		stamp := f.now.Add(-10*time.Hour + time.Duration(f.gen)*time.Minute)
		for _, n := range append([]string{"Release", "InRelease"}, names...) {
			if err := os.Chtimes(filepath.Join(distDir, n), stamp, stamp); err != nil {
				f.t.Fatal(err)
			}
		}
	}
}

// buildDeb makes a real .deb with dpkg-deb.
func (f *aptFixture) buildDeb(p fxPkg, out string) {
	f.t.Helper()
	src := filepath.Join(f.t.TempDir(), p.Name)
	if err := os.MkdirAll(filepath.Join(src, "DEBIAN"), 0o755); err != nil {
		f.t.Fatal(err)
	}
	ctrl := fmt.Sprintf("Package: %s\nVersion: %s\nArchitecture: %s\nMaintainer: Person A <person.a@example.test>\n", p.Name, p.Version, f.arch)
	if p.Depends != "" {
		ctrl += "Depends: " + p.Depends + "\n"
	}
	ctrl += "Description: Fixture package\n"
	if err := os.WriteFile(filepath.Join(src, "DEBIAN", "control"), []byte(ctrl), 0o644); err != nil {
		f.t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(src, "usr", "share", p.Name), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "usr", "share", p.Name, "payload"), []byte(p.Name+" "+p.Version+"\n"), 0o644); err != nil {
		f.t.Fatal(err)
	}
	runTool(f.t, []string{"PATH=" + execx.FixedPath}, f.tools["dpkg-deb"], "--root-owner-group", "-Zgzip", "--build", src, out)
}

// newAptClosure is an AptClosure over the fixture, with a downloader that does not wait between tries
// and, when the tests run as root, the unprivileged user the real agent would use.
func newAptClosure(t *testing.T, f *aptFixture) *AptClosure {
	t.Helper()
	env := testEnv(t)
	env.Downloader = download.New(download.Options{Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Backoff: download.Backoff{Steps: []time.Duration{time.Nanosecond}, Jitter: -1}})
	a := &AptClosure{Env: env, Mirror: f.URL(), Keyrings: func(string) ([]byte, error) { return f.Keyring, nil }}
	if os.Geteuid() == 0 {
		a.Credential = &execx.Credential{UID: 65534, GID: 65534}
		a.User = "nobody"
		// t.TempDir's parent is private to root; the apt user must be able to walk to its tree.
		for d := env.SyncHome; d != os.TempDir() && d != "/" && d != "."; d = filepath.Dir(d) {
			if err := os.Chmod(d, 0o755); err != nil {
				t.Fatal(err)
			}
		}
	}
	return a
}

// aptItem is an apt_closure catalog item.
func aptItem(id, codename, arch string, pkgs ...string) plan.CatalogItem {
	return contentItem(id, "kit", catalog.ContentSource{Type: "apt_closure", Codename: codename, Arch: arch, Packages: pkgs})
}
