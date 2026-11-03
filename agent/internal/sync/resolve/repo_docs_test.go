package resolve

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/catalog"
	"github.com/marinoscar/local-ai/agent/internal/docsrender"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/execx"
	"github.com/marinoscar/local-ai/agent/internal/sync/download"
	"github.com/marinoscar/local-ai/agent/internal/sync/manifest"
	"github.com/marinoscar/local-ai/agent/internal/sync/plan"
)

// The fixture documents are Go strings, not committed .md files: scripts/check-links.sh checks every
// *.md of the repository, and these are made up. Every name and value is invented.
const fixtureCard = `<!-- Card template for the tests. -->

| CARD — computer <NODE_ID> | TARJETA — computadora <NODE_ID> |
|---|---|
| **Start** Log in as **<DESKTOP_USER>**. | **Inicio** Entre como **<DESKTOP_USER>**. |

Computer / Computadora **<NODE_ID>** · Catalog / Catálogo <CATALOG_VERSION> · Printed / Impreso <RENDER_DATE>
`

func docsFixture() map[string]string {
	return map[string]string{
		"docs/README.md":                    "# Docs map\n\nRunbooks:\n\n- [Survival card](runbooks/00-survival-card.md)\n- [First](runbooks/01-first.md)\n- [Rebuild](runbooks/06-rebuild.md)\n",
		"docs/runbooks/00-survival-card.md": fixtureCard,
		"docs/runbooks/01-first.md":         "# 01. First\n\nLog in as `<DESKTOP_USER>` and reach node one at <TAILNET_IP_AI_N1>.\n",
		"docs/runbooks/06-rebuild.md":       "# 06. Rebuild a node offline\n\nNo internet needed.\n",
	}
}

var shaToken = regexp.MustCompile(`@sha:([0-9a-f])@`)

// catalogFixture reads the lint-clean catalog of the catalog package's testdata, expanding the
// @sha:x@ tokens (the redaction check rejects a 64-hex literal outside its own fixtures).
func catalogFixture(t *testing.T) (files map[string]string, version string) {
	t.Helper()
	src := filepath.Join("..", "..", "catalog", "testdata", "valid")
	files = map[string]string{}
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		files["catalog/"+filepath.ToSlash(rel)] = shaToken.ReplaceAllStringFunc(string(data), func(m string) string {
			return strings.Repeat(shaToken.FindStringSubmatch(m)[1], 64)
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	version = strings.TrimSpace(files["catalog/"+"CATALOG_VERSION"])
	if version == "" {
		t.Fatal("the catalog fixture has no CATALOG_VERSION")
	}
	return files, version
}

// repoTree is the whole fixture repository: docs, the inventory example and the catalog.
func repoTree(t *testing.T) (files map[string]string, version string) {
	t.Helper()
	files, version = catalogFixture(t)
	for k, v := range docsFixture() {
		files[k] = v
	}
	ex, err := os.ReadFile(filepath.Join("..", "..", "docsrender", "testdata", "inventory.example.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	files["inventory.example.yaml"] = string(ex)
	return files, version
}

func localInventory(t *testing.T) string {
	t.Helper()
	p, err := filepath.Abs(filepath.Join("..", "..", "docsrender", "testdata", "inventory.fixture.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

type tarEntry struct {
	hdr  tar.Header
	data string
}

// makeTar builds a gzipped tar of files below one top directory, like GitHub's archive, with extra
// raw entries appended (traversal names, links, devices).
func makeTar(t *testing.T, top string, files map[string]string, extra ...tarEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeXGlobalHeader, Name: "pax_global_header", PAXRecords: map[string]string{"comment": "fixture"}}); err != nil {
		t.Fatal(err)
	}
	if top != "" {
		_ = tw.WriteHeader(&tar.Header{Typeflag: tar.TypeDir, Name: top + "/", Mode: 0o755})
	}
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	for _, n := range names {
		h := &tar.Header{Typeflag: tar.TypeReg, Name: top + "/" + n, Mode: 0o644, Size: int64(len(files[n]))}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		_, _ = tw.Write([]byte(files[n]))
	}
	for _, e := range extra {
		h := e.hdr
		h.Size = int64(len(e.data))
		if h.Typeflag == 0 {
			h.Typeflag = tar.TypeReg
		}
		if err := tw.WriteHeader(&h); err != nil {
			t.Fatal(err)
		}
		_, _ = tw.Write([]byte(e.data))
	}
	_ = tw.Close()
	_ = zw.Close()
	return buf.Bytes()
}

// noPrinterLookup finds git and gzip but no Chromium, like a node that never installed it.
func noPrinterLookup(name string) (string, error) {
	switch name {
	case "git", "gzip":
		return execx.Lookup(name)
	}
	return "", execx.ErrNotFound
}

// newRepoDocs is a RepoDocs over a codeload server (nil for none), offline or online.
func newRepoDocs(t *testing.T, version string, online bool, srv *httptest.Server) *RepoDocs {
	t.Helper()
	env := testEnv(t)
	env.Online = func() bool { return online }
	env.Lookup = noPrinterLookup
	env.Downloader = download.New(download.Options{Backoff: download.Backoff{Steps: []time.Duration{time.Nanosecond}, Jitter: -1}})
	r := &RepoDocs{Env: env, CatalogVersion: func() string { return version }}
	if srv != nil {
		r.CodeloadBase = srv.URL
	}
	return r
}

func docsItem() plan.CatalogItem {
	return contentItem("docs-bundle", "docs", catalog.ContentSource{Type: "repo_docs"})
}

func docsRequest(root, version string, others ...string) Request {
	return Request{Item: docsItem(), RootID: "internal", Root: root, Version: docsVersion(version, false), CatalogVersion: version, OtherRoots: others}
}

// codeload serves tarball at the path codeload.github.com would, counting requests.
func codeload(t *testing.T, version string, tarball []byte) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Path != "/"+DefaultRepo+"/tar.gz/refs/tags/catalog-"+version {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(tarball)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func TestRepoDocsFromCodeload(t *testing.T) {
	files, version := repoTree(t)
	tarball := makeTar(t, "local-ai-catalog-"+version, files)
	srv, hits := codeload(t, version, tarball)
	r := newRepoDocs(t, version, true, srv)
	root := t.TempDir()
	req := docsRequest(root, version)
	prod, err := r.Produce(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if hits.Load() != 1 {
		t.Errorf("tarball requests = %d, want 1", hits.Load())
	}
	if prod.Version != "catalog-"+version || prod.SortKey != version || prod.Trust != manifest.TrustComputed {
		t.Errorf("product = %+v", prod)
	}
	if prod.Source["origin"] != "codeload" || prod.Source["tarball_sha256"] != sha256Hex(tarball) || prod.Source["tag"] != "catalog-"+version {
		t.Errorf("source = %+v", prod.Source)
	}
	if len(prod.Parts) != 2 {
		t.Fatalf("parts = %+v", prod.Parts)
	}
	docs, cat := prod.Parts[0], prod.Parts[1]
	if docs.Kind != PartTree || docs.From != "tree" || docs.To != "docs" || cat.Kind != PartTree || cat.From != "catalog" || cat.To != "kit/catalog" {
		t.Errorf("parts = %+v / %+v", docs, cat)
	}
	staging := req.StagingDir()
	for _, rel := range []string{"tree/index.html", "tree/01-first.html", "tree/06-rebuild.html", "tree/SHA256SUMS", "catalog/models.yaml", "catalog/ark.yaml", "catalog/CATALOG_VERSION", "catalog/pins.json", "catalog/profiles/cpu-only.yaml", "catalog/SHA256SUMS"} {
		if _, err := os.Stat(filepath.Join(staging, filepath.FromSlash(rel))); err != nil {
			t.Errorf("staging lacks %s: %v", rel, err)
		}
	}
	// The survival card HTML is named for the node ("unconfigured" here: no node state exists).
	cards, _ := filepath.Glob(filepath.Join(staging, "tree", "survival-card-*.html"))
	if len(cards) != 1 {
		t.Errorf("survival card HTML files: %v", cards)
	}
	// Without an inventory and without Chromium both yellow notices are raised, and the item is still good.
	codes := map[string]bool{}
	for _, n := range prod.Notices {
		codes[n.Code] = true
	}
	if !codes[doctor.SYN062] || !codes[doctor.CLI014] || len(prod.Notices) != 2 {
		t.Errorf("notices = %+v, want LAI-SYN-062 and LAI-CLI-014", prod.Notices)
	}
	// The placeholders stay when there is no inventory.
	html, _ := os.ReadFile(filepath.Join(staging, "tree", "01-first.html"))
	if !strings.Contains(string(html), "TAILNET_IP_AI_N1") {
		t.Error("the placeholder was replaced without an inventory")
	}
	// Both trees are sealed: SHA256SUMS agrees with the tree hash.
	for _, p := range []Part{docs, cat} {
		dir := filepath.Join(staging, p.From)
		sums, err := manifest.ReadSums(osFS, dir)
		if err != nil {
			t.Fatal(err)
		}
		if tsha, _ := manifest.TreeSHA256(sums); tsha != p.TreeSHA256 || len(sums) != len(p.Sums) {
			t.Errorf("%s: tree hash/sums disagree with the part", p.Name)
		}
	}
	// pins.json is a valid skeleton when the release carries none.
	var pins map[string]any
	data, _ := os.ReadFile(filepath.Join(staging, "catalog", "pins.json"))
	if err := json.Unmarshal(data, &pins); err != nil || pins["schema_version"] != float64(1) {
		t.Errorf("pins.json = %s (%v)", data, err)
	}
	// Nothing outside staging was written.
	if _, err := os.Stat(filepath.Join(root, "docs")); err == nil {
		t.Error("the producer wrote into the root's docs directory")
	}
	if _, err := os.Stat(filepath.Join(root, "kit")); err == nil {
		t.Error("the producer wrote into the root's kit directory")
	}
}

func TestRepoDocsRendersRealValuesWithAnInventory(t *testing.T) {
	files, version := repoTree(t)
	srv, _ := codeload(t, version, makeTar(t, "local-ai-catalog-"+version, files))
	r := newRepoDocs(t, version, true, srv)
	r.Inventory = func() string { return localInventory(t) }
	res, err := r.Resolve(context.Background(), docsItem())
	if err != nil || res.Version != "catalog-"+version+"+inv" {
		t.Fatalf("Resolve = (%+v, %v)", res, err)
	}
	root := t.TempDir()
	req := docsRequest(root, version)
	req.Version = res.Version
	prod, err := r.Produce(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if prod.Version != "catalog-"+version+"+inv" {
		t.Errorf("version = %q", prod.Version)
	}
	for _, n := range prod.Notices {
		if n.Code == doctor.SYN062 {
			t.Error("LAI-SYN-062 raised although the inventory was rendered")
		}
	}
	html, _ := os.ReadFile(filepath.Join(req.StagingDir(), "tree", "01-first.html"))
	if strings.Contains(string(html), "TAILNET_IP_AI_N1") {
		t.Error("the inventory value was not rendered")
	}
}

// TestRepoDocsWantMakesOnlyThePartOfTheItem: the engine produces repo_docs once per item and says which
// part it installs. The catalog item gets the catalog and nothing is rendered; the docs item gets the
// docs tree; with no Want both parts come back, as before.
func TestRepoDocsWantMakesOnlyThePartOfTheItem(t *testing.T) {
	files, version := repoTree(t)
	srv, _ := codeload(t, version, makeTar(t, "local-ai-catalog-"+version, files))
	for _, tc := range []struct {
		want      string
		parts     []string
		rendered  bool
		catalogOn bool
	}{
		{"", []string{"docs", "catalog"}, true, true},
		{PartNameDocs, []string{"docs"}, true, false},
		{PartNameCatalog, []string{"catalog"}, false, true},
	} {
		t.Run("want "+tc.want, func(t *testing.T) {
			r := newRepoDocs(t, version, true, srv)
			root := t.TempDir()
			req := docsRequest(root, version)
			req.Want = tc.want
			prod, err := r.Produce(context.Background(), req)
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, p := range prod.Parts {
				got = append(got, p.Name)
			}
			if strings.Join(got, ",") != strings.Join(tc.parts, ",") {
				t.Fatalf("parts = %v, want %v", got, tc.parts)
			}
			if _, err := os.Stat(filepath.Join(req.StagingDir(), "tree", "index.html")); (err == nil) != tc.rendered {
				t.Fatalf("manuals rendered = %v, want %v", err == nil, tc.rendered)
			}
			if _, err := os.Stat(filepath.Join(req.StagingDir(), "catalog", "SHA256SUMS")); (err == nil) != tc.catalogOn {
				t.Fatalf("catalog sealed = %v, want %v", err == nil, tc.catalogOn)
			}
			if tc.want == PartNameCatalog && (prod.Version != "catalog-"+version || len(prod.Notices) != 0) {
				t.Fatalf("catalog product = %+v", prod)
			}
		})
	}
}

// TestRepoDocsCatalogItemHasNoInventoryInItsVersion: the kit catalog is copied from the tarball as it is,
// so an inventory that appears does not make it a new version; the docs item does get "+inv".
func TestRepoDocsCatalogItemHasNoInventoryInItsVersion(t *testing.T) {
	r := newRepoDocs(t, "2026.11.02-1", false, nil)
	r.Inventory = func() string { return "/etc/localai/inventory.local.yaml" }
	cat := contentItem("kit-catalog", "kit", catalog.ContentSource{Type: "repo_docs"})
	got, err := r.Resolve(context.Background(), cat)
	if err != nil || got.Version != "catalog-2026.11.02-1" {
		t.Fatalf("catalog item: (%+v, %v)", got, err)
	}
	got, err = r.Resolve(context.Background(), docsItem())
	if err != nil || got.Version != "catalog-2026.11.02-1+inv" {
		t.Fatalf("docs item: (%+v, %v)", got, err)
	}
}

func TestRepoDocsResolveWithoutInventoryOrVersion(t *testing.T) {
	r := newRepoDocs(t, "2026.11.02-1", false, nil)
	got, err := r.Resolve(context.Background(), docsItem())
	if err != nil || got.Version != "catalog-2026.11.02-1" || got.SortKey != "2026.11.02-1" {
		t.Fatalf("(%+v, %v)", got, err)
	}
	r.CatalogVersion = func() string { return "" }
	if _, err := r.Resolve(context.Background(), docsItem()); err == nil {
		t.Error("an empty catalog version resolved")
	}
	r.CatalogVersion = func() string { return "../x" }
	if _, err := r.Resolve(context.Background(), docsItem()); err == nil {
		t.Error("a path-like catalog version resolved")
	}
	if _, err := r.Resolve(context.Background(), contentItem("x", "docs", catalog.ContentSource{Type: "url"})); err == nil {
		t.Error("a url item resolved as repo_docs")
	}
}

// mirrorWithTag makes a local-ai.git mirror holding the fixture repository at the tag.
func mirrorWithTag(t *testing.T, root string, files map[string]string, tag string) string {
	t.Helper()
	u := newUpstream(t)
	for name, content := range files {
		p := filepath.Join(u.work, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	runTool(t, u.env, u.git, "-C", u.work, "add", "-A")
	runTool(t, u.env, u.git, "-C", u.work, "commit", "-q", "-m", "docs and catalog")
	if tag != "" {
		runTool(t, u.env, u.git, "-C", u.work, "tag", tag)
	}
	mirror := filepath.Join(root, "kit", "git", "local-ai.git")
	if err := os.MkdirAll(filepath.Dir(mirror), 0o755); err != nil {
		t.Fatal(err)
	}
	runTool(t, u.env, u.git, "clone", "-q", "--mirror", u.work, mirror)
	return mirror
}

func TestRepoDocsFromALocalMirrorWorksOffline(t *testing.T) {
	needTool(t, "git")
	files, version := repoTree(t)
	// The mirror lives on another root (the ark drive); this node is offline and has no server at all.
	drive := t.TempDir()
	mirrorWithTag(t, drive, files, "catalog-"+version)
	r := newRepoDocs(t, version, false, nil)
	root := t.TempDir()
	req := docsRequest(root, version, drive)
	prod, err := r.Produce(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if prod.Source["origin"] != "git_archive" || prod.Version != "catalog-"+version {
		t.Errorf("product = %+v", prod)
	}
	if _, err := os.Stat(filepath.Join(req.StagingDir(), "tree", "index.html")); err != nil {
		t.Error(err)
	}
	// Staging is reusable: a second run over the same staging directory overwrites its leftovers.
	if _, err := r.Produce(context.Background(), req); err != nil {
		t.Fatalf("second run over the same staging: %v", err)
	}
}

func TestRepoDocsPrefersTheMirrorAndFallsBackToTheNetwork(t *testing.T) {
	needTool(t, "git")
	files, version := repoTree(t)
	srv, hits := codeload(t, version, makeTar(t, "local-ai-catalog-"+version, files))

	// Online, with the mirror holding the tag: the mirror is used, the server is never asked.
	root := t.TempDir()
	mirrorWithTag(t, root, files, "catalog-"+version)
	r := newRepoDocs(t, version, true, srv)
	prod, err := r.Produce(context.Background(), docsRequest(root, version))
	if err != nil || prod.Source["origin"] != "git_archive" {
		t.Fatalf("(%+v, %v)", prod.Source, err)
	}
	if hits.Load() != 0 {
		t.Errorf("the network was used although the mirror has the tag (%d requests)", hits.Load())
	}

	// The mirror lacks the tag: online falls back to the tarball, offline fails with LAI-SYN-024.
	root2 := t.TempDir()
	mirrorWithTag(t, root2, files, "")
	prod, err = r.Produce(context.Background(), docsRequest(root2, version))
	if err != nil || prod.Source["origin"] != "codeload" {
		t.Fatalf("fallback: (%+v, %v)", prod.Source, err)
	}
	r2 := newRepoDocs(t, version, false, srv)
	_, err = r2.Produce(context.Background(), docsRequest(root2, version))
	if CodeOf(err) != doctor.SYN024 {
		t.Fatalf("offline with a mirror lacking the tag: code %q (err %v)", CodeOf(err), err)
	}
}

func TestRepoDocsNetworkFailures(t *testing.T) {
	files, version := repoTree(t)
	good := makeTar(t, "local-ai-catalog-"+version, files)
	t.Run("offline without a mirror never touches the network", func(t *testing.T) {
		srv, hits := codeload(t, version, good)
		r := newRepoDocs(t, version, false, srv)
		_, err := r.Produce(context.Background(), docsRequest(t.TempDir(), version))
		if !errors.Is(err, ErrOffline) || hits.Load() != 0 {
			t.Fatalf("err = %v, requests = %d", err, hits.Load())
		}
	})
	t.Run("tag not found is a permanent refusal", func(t *testing.T) {
		srv, _ := codeload(t, "another-version", good)
		r := newRepoDocs(t, version, true, srv)
		_, err := r.Produce(context.Background(), docsRequest(t.TempDir(), version))
		var de *download.Error
		if !errors.As(err, &de) || de.Kind != download.KindPermanent || de.Code != doctor.SYN030 {
			t.Fatalf("err = %v, want a permanent LAI-SYN-030", err)
		}
	})
	t.Run("unreachable host", func(t *testing.T) {
		srv, _ := codeload(t, version, good)
		r := newRepoDocs(t, version, true, srv)
		srv.Close()
		_, err := r.Produce(context.Background(), docsRequest(t.TempDir(), version))
		var de *download.Error
		if !errors.As(err, &de) || de.Kind != download.KindExhausted {
			t.Fatalf("err = %v, want a download failure that resumes next run", err)
		}
	})
	t.Run("not a tarball", func(t *testing.T) {
		srv, _ := codeload(t, version, []byte("this is not an archive"))
		r := newRepoDocs(t, version, true, srv)
		_, err := r.Produce(context.Background(), docsRequest(t.TempDir(), version))
		if CodeOf(err) != CodeVerifyDocs {
			t.Fatalf("code = %q (err %v)", CodeOf(err), err)
		}
	})
}

func TestRepoDocsRefusesAnUnusableRequest(t *testing.T) {
	files, version := repoTree(t)
	srv, _ := codeload(t, version, makeTar(t, "x", files))
	r := newRepoDocs(t, version, true, srv)
	for _, v := range []string{"", "../x", "a b", "a;b", strings.Repeat("a", 80)} {
		req := docsRequest(t.TempDir(), version)
		req.CatalogVersion = v
		if _, err := r.Produce(context.Background(), req); err == nil {
			t.Errorf("catalog version %q was accepted", v)
		}
	}
	r.Repo = "../../etc/passwd"
	if _, err := r.Produce(context.Background(), docsRequest(t.TempDir(), version)); err == nil {
		t.Error("a path-like repository name was accepted")
	}
}

func TestRepoDocsVerificationFailsWhenAListedRunbookHasNoHTML(t *testing.T) {
	files, version := repoTree(t)
	files["docs/README.md"] += "- [Seven](runbooks/07-listed-but-absent.md)\n"
	srv, _ := codeload(t, version, makeTar(t, "local-ai-catalog-"+version, files))
	r := newRepoDocs(t, version, true, srv)
	root := t.TempDir()
	req := docsRequest(root, version)
	_, err := r.Produce(context.Background(), req)
	if CodeOf(err) != CodeVerifyDocs || !strings.Contains(err.Error(), "07-listed-but-absent.html") {
		t.Fatalf("err = %v, want a verification failure naming the missing HTML", err)
	}
	if _, err := os.Stat(filepath.Join(req.StagingDir(), "tree", manifest.SumsName)); err == nil {
		t.Error("a failed item was sealed")
	}
}

func TestRepoDocsVerifyTable(t *testing.T) {
	readme := []byte("[a](runbooks/01-first.md) and [b](./runbooks/02-second.md#x) and [c](runbooks/README.md) and [d](https://example.test/runbooks/03-remote.md)\n")
	build := func(files ...string) string {
		dir := t.TempDir()
		for _, f := range files {
			if err := os.WriteFile(filepath.Join(dir, f), []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		return dir
	}
	docsSrc := t.TempDir()
	if err := os.WriteFile(filepath.Join(docsSrc, "README.md"), readme, 0o644); err != nil {
		t.Fatal(err)
	}
	r := &RepoDocs{Env: &Env{}}
	tests := []struct {
		name    string
		files   []string
		rep     docsrender.Report
		wantErr string
		notices []string
	}{
		{"complete with pdf and inventory", []string{"index.html", "01-first.html", "02-second.html", "survival-card-n1.html", "survival-card-n1.pdf"},
			docsrender.Report{Card: "survival-card-n1", Inventory: true}, "", nil},
		{"only the card pdf missing is yellow", []string{"index.html", "01-first.html", "02-second.html", "survival-card-n1.html"},
			docsrender.Report{Card: "survival-card-n1", Inventory: true}, "", []string{doctor.CLI014}},
		{"no inventory is yellow", []string{"index.html", "01-first.html", "02-second.html", "survival-card-n1.html", "survival-card-n1.pdf"},
			docsrender.Report{Card: "survival-card-n1"}, "", []string{doctor.SYN062}},
		{"no index", []string{"01-first.html", "02-second.html", "survival-card-n1.html"}, docsrender.Report{Card: "survival-card-n1"}, "index.html", nil},
		{"a runbook html missing", []string{"index.html", "01-first.html", "survival-card-n1.html"}, docsrender.Report{Card: "survival-card-n1"}, "02-second.html", nil},
		{"card html missing", []string{"index.html", "01-first.html", "02-second.html"}, docsrender.Report{Card: "survival-card-n1"}, "survival card", nil},
		{"no card at all", []string{"index.html", "01-first.html", "02-second.html"}, docsrender.Report{}, "survival card", nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rep := tc.rep
			notices, err := r.verify(docsSrc, build(tc.files...), &rep)
			if tc.wantErr != "" {
				if CodeOf(err) != CodeVerifyDocs || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want %s mentioning %q", err, CodeVerifyDocs, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, n := range notices {
				got = append(got, n.Code)
			}
			if strings.Join(got, ",") != strings.Join(tc.notices, ",") {
				t.Errorf("notices = %v, want %v", got, tc.notices)
			}
		})
	}
}

func TestListedRunbooks(t *testing.T) {
	got := listedRunbooks([]byte("[a](runbooks/02-b.md) [b](runbooks/01-a.md#sec) [c](./runbooks/01-a.md) [d](runbooks/README.md) [e](other/03.md) [f](runbooks/sub/04.md)"))
	if strings.Join(got, ",") != "01-a,02-b" {
		t.Errorf("runbooks = %v", got)
	}
}

func TestRepoDocsBadCatalogKeepsTheOldKitCatalog(t *testing.T) {
	tests := []struct {
		name string
		edit func(files map[string]string, version string)
	}{
		{"lint failure", func(f map[string]string, _ string) {
			f["catalog/ark.yaml"] = "schema_version: 1\ncontent: not-a-list\n"
		}},
		{"missing models", func(f map[string]string, _ string) { delete(f, "catalog/models.yaml") }},
		{"version differs from the tag", func(f map[string]string, _ string) { f["catalog/CATALOG_VERSION"] = "2020.01.01-1\n" }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			files, version := repoTree(t)
			tc.edit(files, version)
			srv, _ := codeload(t, version, makeTar(t, "local-ai-catalog-"+version, files))
			r := newRepoDocs(t, version, true, srv)
			root := t.TempDir()
			// An installed kit/catalog from an earlier version.
			old := filepath.Join(root, "kit", "catalog")
			if err := os.MkdirAll(old, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(old, "CATALOG_VERSION"), []byte("2019.01.01-1\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			req := docsRequest(root, version)
			_, err := r.Produce(context.Background(), req)
			if CodeOf(err) != doctor.CAT002 {
				t.Fatalf("code = %q, want LAI-CAT-002 (err %v)", CodeOf(err), err)
			}
			if got, _ := os.ReadFile(filepath.Join(old, "CATALOG_VERSION")); string(got) != "2019.01.01-1\n" {
				t.Errorf("the installed kit/catalog changed: %q", got)
			}
			if _, err := os.Stat(filepath.Join(req.StagingDir(), "tree")); err == nil {
				t.Error("the manuals were rendered although the catalog failed lint (they travel together)")
			}
		})
	}
}

func TestRepoDocsKeepsPinsFromTheRelease(t *testing.T) {
	files, version := repoTree(t)
	files["catalog/pins.json"] = `{"schema_version":1,"llama_cpp":[{"commit":"` + absentCommit("pin") + `","nodes":["ai-n1"]}]}` + "\n"
	srv, _ := codeload(t, version, makeTar(t, "local-ai-catalog-"+version, files))
	r := newRepoDocs(t, version, true, srv)
	req := docsRequest(t.TempDir(), version)
	if _, err := r.Produce(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(req.StagingDir(), "catalog", "pins.json"))
	if string(got) != files["catalog/pins.json"] {
		t.Errorf("pins.json from the release was not kept: %s", got)
	}
}

func TestRepoDocsCardWithASecretIsRefused(t *testing.T) {
	files, version := repoTree(t)
	files["docs/runbooks/00-survival-card.md"] = strings.Replace(fixtureCard, "<DESKTOP_USER>", "<TAILNET_IP_AI_N1>", 1)
	srv, _ := codeload(t, version, makeTar(t, "local-ai-catalog-"+version, files))
	r := newRepoDocs(t, version, true, srv)
	r.Inventory = func() string { return localInventory(t) }
	_, err := r.Produce(context.Background(), docsRequest(t.TempDir(), version))
	if CodeOf(err) != doctor.CLI016 {
		t.Fatalf("code = %q, want LAI-CLI-016 (err %v)", CodeOf(err), err)
	}
}

func TestRepoDocsRefusesAnUnsafeTarball(t *testing.T) {
	files, version := repoTree(t)
	top := "local-ai-catalog-" + version
	tests := []struct {
		name  string
		extra []tarEntry
	}{
		{"path traversal", []tarEntry{{hdr: tar.Header{Name: top + "/docs/../../escape.txt", Mode: 0o644}, data: "x"}}},
		{"traversal outside the wanted paths too", []tarEntry{{hdr: tar.Header{Name: top + "/other/../../../escape.txt", Mode: 0o644}, data: "x"}}},
		{"absolute path", []tarEntry{{hdr: tar.Header{Name: "/etc/escape.txt", Mode: 0o644}, data: "x"}}},
		{"symlink in docs", []tarEntry{{hdr: tar.Header{Typeflag: tar.TypeSymlink, Name: top + "/docs/link", Linkname: "/etc/passwd"}}}},
		{"hard link in the catalog", []tarEntry{{hdr: tar.Header{Typeflag: tar.TypeLink, Name: top + "/catalog/models.yaml", Linkname: top + "/docs/README.md"}}}},
		{"device node in docs", []tarEntry{{hdr: tar.Header{Typeflag: tar.TypeChar, Name: top + "/docs/dev"}}}},
		{"a second top directory", []tarEntry{{hdr: tar.Header{Name: "evil/docs/README.md", Mode: 0o644}, data: "x"}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv, _ := codeload(t, version, makeTar(t, top, files, tc.extra...))
			r := newRepoDocs(t, version, true, srv)
			root := t.TempDir()
			_, err := r.Produce(context.Background(), docsRequest(root, version))
			if CodeOf(err) != CodeVerifyDocs || !errors.Is(err, ErrUnsafeArchive) {
				t.Fatalf("err = %v, want an unsafe-archive refusal with %s", err, CodeVerifyDocs)
			}
			if _, err := os.Stat(filepath.Join(filepath.Dir(root), "escape.txt")); err == nil {
				t.Error("a file escaped the staging directory")
			}
		})
	}
}

func TestExtractTarIgnoresWhatIsNotWantedAndBoundsWhatIs(t *testing.T) {
	rules := []tarRule{{Prefix: "docs/", Dest: "src/docs"}, {Prefix: "inventory.example.yaml", Dest: "src/inventory.example.yaml"}}
	top := "t"
	t.Run("links outside the wanted paths are ignored, wanted files are written 0644", func(t *testing.T) {
		root := t.TempDir()
		data := makeTar(t, top, map[string]string{"docs/a.md": "# a", "src/main.go": "package x", "inventory.example.yaml": "x: 1"},
			tarEntry{hdr: tar.Header{Typeflag: tar.TypeSymlink, Name: top + "/vendor/link", Linkname: "/etc"}},
			tarEntry{hdr: tar.Header{Name: top + "/docs/exec.sh", Mode: 0o755}, data: "#!/bin/sh"})
		written, err := extractTar(bytes.NewReader(data), root, rules)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Join(sortedCopy(written), ",") != "src/docs/a.md,src/docs/exec.sh,src/inventory.example.yaml" {
			t.Errorf("written = %v", written)
		}
		fi, err := os.Stat(filepath.Join(root, "src", "docs", "exec.sh"))
		if err != nil || fi.Mode().Perm() != 0o644 {
			t.Errorf("mode = %v (%v), want 0644", fi.Mode().Perm(), err)
		}
		if _, err := os.Stat(filepath.Join(root, "src", "main.go")); err == nil {
			t.Error("an unwanted file was extracted")
		}
	})
	t.Run("a file over the per-file bound", func(t *testing.T) {
		big := strings.Repeat("a", maxTarFile+1)
		data := makeTar(t, top, map[string]string{"docs/big.md": big})
		if _, err := extractTar(bytes.NewReader(data), t.TempDir(), rules); !errors.Is(err, ErrUnsafeArchive) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("the filter skips an entry without failing", func(t *testing.T) {
		only := []tarRule{{Prefix: "docs/", Dest: "d", Allow: func(rel string) bool { return strings.HasSuffix(rel, ".md") }}}
		root := t.TempDir()
		written, err := extractTar(bytes.NewReader(makeTar(t, top, map[string]string{"docs/a.md": "x", "docs/b.bin": "y"})), root, only)
		if err != nil || len(written) != 1 || written[0] != "d/a.md" {
			t.Fatalf("(%v, %v)", written, err)
		}
	})
	t.Run("plain tar and gzip both read", func(t *testing.T) {
		gz := makeTar(t, top, map[string]string{"docs/a.md": "x"})
		zr, _ := gzip.NewReader(bytes.NewReader(gz))
		plain, _ := io.ReadAll(zr)
		if w, err := extractTar(bytes.NewReader(plain), t.TempDir(), rules); err != nil || len(w) != 1 {
			t.Fatalf("plain tar: (%v, %v)", w, err)
		}
	})
	t.Run("a destination that is a symlink is refused", func(t *testing.T) {
		root := t.TempDir()
		outside := t.TempDir()
		if err := os.MkdirAll(filepath.Join(root, "src"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(root, "src", "docs")); err != nil {
			t.Fatal(err)
		}
		_, err := extractTar(bytes.NewReader(makeTar(t, top, map[string]string{"docs/a.md": "x"})), root, rules)
		if !errors.Is(err, ErrUnsafeArchive) {
			t.Fatalf("err = %v", err)
		}
		if ents, _ := os.ReadDir(outside); len(ents) != 0 {
			t.Error("a file was written through the symlink")
		}
	})
	t.Run("too many entries", func(t *testing.T) {
		files := map[string]string{}
		for i := 0; i <= maxTarEntries; i++ {
			files["x/"+strings.Repeat("a", 1)+itoa(i)] = ""
		}
		if _, err := extractTar(bytes.NewReader(makeTar(t, top, files)), t.TempDir(), rules); !errors.Is(err, ErrUnsafeArchive) {
			t.Fatalf("err = %v", err)
		}
	})
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for ; i > 0; i /= 10 {
		b = append([]byte{byte('0' + i%10)}, b...)
	}
	return string(b)
}

func sortedCopy(s []string) []string {
	out := append([]string(nil), s...)
	for i := range out {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

// failRunner fails the test on any tool run: the planner's local question must not run anything.
type failRunner struct{ t *testing.T }

func (f failRunner) Run(_ context.Context, c execx.Cmd) (execx.Result, error) {
	f.t.Errorf("a tool ran: %s %v", c.Path, c.Args)
	return execx.Result{}, errors.New("no tool may run")
}

// ResolveLocal is the planner's offline question: is there material on a root to build the docs from.
// It reads the mirror's ref files only (no tool, no network) and answers with Resolve's version.
func TestRepoDocsResolveLocalReadsOnlyTheMirrorsRefs(t *testing.T) {
	const version = "2026.11.02-1"
	ask := func(t *testing.T, item plan.CatalogItem, roots ...string) (plan.Resolved, bool) {
		t.Helper()
		r := newRepoDocs(t, version, false, nil)
		r.Env.Runner = failRunner{t}
		r.Inventory = func() string { return "/etc/localai/inventory.local.yaml" }
		return r.ResolveLocal(context.Background(), item, roots)
	}
	loose := func(t *testing.T, tag string) string {
		root := t.TempDir()
		ref := filepath.Join(root, "kit", "git", "local-ai.git", "refs", "tags", tag)
		if err := os.MkdirAll(filepath.Dir(ref), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(ref, []byte(strings.Repeat("1", 40)+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		return root
	}
	packed := func(t *testing.T, lines string) string {
		root := t.TempDir()
		dir := filepath.Join(root, "kit", "git", "local-ai.git")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "packed-refs"), []byte(lines), 0o644); err != nil {
			t.Fatal(err)
		}
		return root
	}
	sha := strings.Repeat("2", 40)
	tag := "catalog-" + version

	t.Run("a loose tag on any root: the docs keep their inventory suffix, the catalog does not", func(t *testing.T) {
		got, ok := ask(t, docsItem(), t.TempDir(), loose(t, tag))
		if !ok || got.Version != "catalog-"+version+"+inv" {
			t.Errorf("docs = (%+v, %v)", got, ok)
		}
		got, ok = ask(t, contentItem("kit-catalog", "kit", catalog.ContentSource{Type: "repo_docs"}), loose(t, tag))
		if !ok || got.Version != "catalog-"+version {
			t.Errorf("catalog = (%+v, %v)", got, ok)
		}
	})
	t.Run("a packed tag", func(t *testing.T) {
		if _, ok := ask(t, docsItem(), packed(t, "# pack-refs with: peeled fully-peeled sorted \n"+sha+" refs/heads/main\n"+sha+" refs/tags/"+tag+"\n^"+sha+"\n")); !ok {
			t.Error("a tag in packed-refs was not found")
		}
	})
	t.Run("a real mirror made by git", func(t *testing.T) {
		needTool(t, "git")
		files, v := repoTree(t)
		root := t.TempDir()
		mirrorWithTag(t, root, files, "catalog-"+v)
		r := newRepoDocs(t, v, false, nil)
		r.Env.Runner = failRunner{t}
		if got, ok := r.ResolveLocal(context.Background(), docsItem(), []string{root}); !ok || got.Version != "catalog-"+v {
			t.Errorf("(%+v, %v)", got, ok)
		}
	})
	t.Run("no answer when the material is not there", func(t *testing.T) {
		for name, roots := range map[string][]string{
			"no roots":                    {},
			"a root without a mirror":     {t.TempDir()},
			"a mirror without the tag":    {loose(t, "catalog-0.0.1")},
			"a longer tag is not the tag": {loose(t, tag+"-rc1"), packed(t, sha+" refs/tags/"+tag+"0\n")},
			"a branch of that name":       {packed(t, sha+" refs/heads/"+tag+"\n")},
		} {
			if _, ok := ask(t, docsItem(), roots...); ok {
				t.Errorf("%s: answered", name)
			}
		}
	})
	t.Run("no answer for another source type, or without a usable catalog version", func(t *testing.T) {
		if _, ok := ask(t, contentItem("x", "docs", catalog.ContentSource{Type: "url"}), loose(t, tag)); ok {
			t.Error("a url item answered")
		}
		r := newRepoDocs(t, "../x", false, nil)
		if _, ok := r.ResolveLocal(context.Background(), docsItem(), []string{loose(t, tag)}); ok {
			t.Error("a path-like version answered")
		}
		r.CatalogVersion = nil
		if _, ok := r.ResolveLocal(context.Background(), docsItem(), []string{loose(t, tag)}); ok {
			t.Error("no catalog version answered")
		}
	})
	t.Run("a cancelled context answers nothing", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		r := newRepoDocs(t, version, false, nil)
		if _, ok := r.ResolveLocal(ctx, docsItem(), []string{loose(t, tag)}); ok {
			t.Error("answered after cancel")
		}
	})
}
