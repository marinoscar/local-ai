package run

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/ark"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/execx"
	"github.com/marinoscar/local-ai/agent/internal/sync/download"
	"github.com/marinoscar/local-ai/agent/internal/sync/item"
	"github.com/marinoscar/local-ai/agent/internal/sync/manifest"
	"github.com/marinoscar/local-ai/agent/internal/sync/plan"
	"github.com/marinoscar/local-ai/agent/internal/sync/resolve"
)

// The real repo_docs producer (resolve.RepoDocs, with the in-process renderer) through the real engine
// and machine: the docs tree and the kit catalog of one tarball become two verified items, and a drive
// is filled by local copies. The tarball is built at run time from the catalog and renderer fixtures
// and served by a local HTTP server, so nothing touches the network.

const realCard = `<!-- Card template for the tests. -->

| CARD — computer <NODE_ID> | TARJETA — computadora <NODE_ID> |
|---|---|
| **Start** Log in as **<DESKTOP_USER>**. | **Inicio** Entre como **<DESKTOP_USER>**. |

Computer / Computadora **<NODE_ID>** · Catalog / Catálogo <CATALOG_VERSION> · Printed / Impreso <RENDER_DATE>
`

var realShaToken = regexp.MustCompile(`@sha:([0-9a-f])@`)

// realRepoTree is the fixture repository: docs, the inventory example and a lint-clean catalog.
func realRepoTree(t *testing.T) map[string]string {
	t.Helper()
	files := map[string]string{
		"docs/README.md":                    "# Docs map\n\nRunbooks:\n\n- [Survival card](runbooks/00-survival-card.md)\n- [First](runbooks/01-first.md)\n",
		"docs/runbooks/00-survival-card.md": realCard,
		"docs/runbooks/01-first.md":         "# 01. First\n\nLog in as `<DESKTOP_USER>` and reach node one at <TAILNET_IP_AI_N1>.\n",
	}
	src := filepath.Join("..", "..", "catalog", "testdata", "valid")
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		files["catalog/"+filepath.ToSlash(rel)] = realShaToken.ReplaceAllStringFunc(string(b), func(m string) string {
			return strings.Repeat(realShaToken.FindStringSubmatch(m)[1], 64)
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	ex, err := os.ReadFile(filepath.Join("..", "..", "docsrender", "testdata", "inventory.example.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	files["inventory.example.yaml"] = string(ex)
	return files
}

func realTarball(t *testing.T, top string, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: top + "/" + n, Mode: 0o644, Size: int64(len(files[n]))}); err != nil {
			t.Fatal(err)
		}
		_, _ = tw.Write([]byte(files[n]))
	}
	_ = tw.Close()
	_ = zw.Close()
	return buf.Bytes()
}

func TestRealRepoDocsProducerThroughTheEngine(t *testing.T) {
	files := realRepoTree(t)
	version := strings.TrimSpace(files["catalog/CATALOG_VERSION"])
	tarball := realTarball(t, "local-ai-catalog-"+version, files)
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Path != "/"+resolve.DefaultRepo+"/tar.gz/refs/tags/catalog-"+version {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(tarball)
	}))
	defer srv.Close()

	env := &resolve.Env{
		Online:   func() bool { return true },
		Now:      func() time.Time { return epoch },
		SyncHome: t.TempDir(),
		Lookup: func(name string) (string, error) {
			switch name {
			case "git", "gzip":
				return execx.Lookup(name)
			}
			return "", execx.ErrNotFound // no Chromium: no PDF, the notice LAI-CLI-014
		},
		Downloader: download.New(download.Options{Backoff: download.Backoff{Steps: []time.Duration{time.Nanosecond}, Jitter: -1}}),
	}
	rd := &resolve.RepoDocs{Env: env, CatalogVersion: func() string { return version }, CodeloadBase: srv.URL}
	s := newPStack(t, true, map[string]resolve.Producer{"repo_docs": rd}, docsBundle(), kitCatalog())
	s.setRows(map[string][]plan.ItemPlan{
		ark.InternalID: {dlRow("docs-bundle", "docs", "catalog-"+version), dlRow("kit-catalog", "kit", "catalog-"+version)},
		driveID:        {copyRow("docs-bundle", "docs", "catalog-"+version), copyRow("kit-catalog", "kit", "catalog-"+version)},
	})
	st := s.sync()
	if st.State != StateDone || st.Result != ResultOK || st.Totals.Done != 4 {
		t.Fatalf("status = %+v (%+v)", st, st.Items)
	}
	// One tarball per item (two items, two producer calls), none for the drive's local copies.
	if hits.Load() != 2 {
		t.Fatalf("tarball requests = %d, want 2", hits.Load())
	}
	for _, r := range []ark.Root{s.internal, s.drive} {
		dm, err := s.load(r, "docs")
		if err != nil {
			t.Fatalf("%s docs: %v", r.ID, err)
		}
		cm, err := s.load(r, "kit/catalog")
		if err != nil {
			t.Fatalf("%s catalog: %v", r.ID, err)
		}
		if dm.Version != "catalog-"+version || cm.Version != "catalog-"+version || dm.Trust != manifest.TrustComputed || cm.Trust != manifest.TrustComputed {
			t.Fatalf("%s manifests: docs %+v, catalog %+v", r.ID, dm, cm)
		}
		if _, err := os.Stat(filepath.Join(r.Path, "docs", "index.html")); err != nil {
			t.Fatalf("%s: no rendered index: %v", r.ID, err)
		}
		for _, name := range []string{"models.yaml", "ark.yaml", "CATALOG_VERSION", "pins.json"} {
			if _, err := os.Stat(filepath.Join(r.Path, "kit", "catalog", name)); err != nil {
				t.Fatalf("%s: kit/catalog/%s: %v", r.ID, name, err)
			}
		}
		// The rendered docs are not inside the catalog and the catalog is not inside the docs.
		if _, err := os.Stat(filepath.Join(r.Path, "kit", "catalog", "index.html")); err == nil {
			t.Fatalf("%s: the manuals were installed into the catalog", r.ID)
		}
		var notices []string
		if err := json.Unmarshal(dm.Source.Fields[item.NoticesKey], &notices); err != nil || len(notices) != 2 || notices[0] != doctor.CLI014 || notices[1] != doctor.SYN062 {
			t.Fatalf("%s docs source.notices = %s, %v", r.ID, dm.Source.Fields[item.NoticesKey], err)
		}
		if cm.Source.String("tag") != "catalog-"+version || cm.Source.String("tarball_sha256") == "" {
			t.Fatalf("%s catalog source = %+v", r.ID, cm.Source)
		}
		s.assertStagingClean(r)
	}
}
