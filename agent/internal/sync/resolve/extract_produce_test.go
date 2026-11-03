package resolve

import (
	"archive/tar"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/catalog"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/sync/download"
	"github.com/marinoscar/local-ai/agent/internal/sync/item"
	"github.com/marinoscar/local-ai/agent/internal/sync/manifest"
	"github.com/marinoscar/local-ai/agent/internal/sync/plan"
)

// The extract path end to end, below the machine: the resolver says "a directory item made by Produce",
// Produce downloads the archive through the real downloader (against a TLS fake), re-reads it, unpacks
// it into a fresh staging directory and seals the tree; the machine would then swap that tree in.

type produceRig struct {
	r      *webRig
	origin *fakeOrigin
	root   string
}

func newProduceRig(t *testing.T) *produceRig {
	r := newWebRig(t)
	o := newFakeOrigin(r)
	r.w.Downloader = r.downloader(o.srv)
	return &produceRig{r: r, origin: o, root: t.TempDir()}
}

// produce resolves nothing: it runs the Produce of res like the machine does.
func (p *produceRig) produce(res item.Resolution, id string) (item.Produced, string, error) {
	staging := filepath.Join(p.root, ".staging", id, res.Version)
	if err := os.MkdirAll(staging, 0o755); err != nil {
		panic(err)
	}
	prod, err := res.Produce(context.Background(), item.ProduceEnv{StagingDir: staging, RootID: "internal", Root: p.root})
	return prod, staging, err
}

func goodTarGz(t *testing.T) []byte { return archGz(t, archTar(t, goodEntries...)) }

func checkSealed(t *testing.T, staging string, prod item.Produced) {
	t.Helper()
	if prod.TreeFrom != "extract.1" && !strings.HasPrefix(prod.TreeFrom, "extract.") {
		t.Fatalf("TreeFrom = %q", prod.TreeFrom)
	}
	tree := filepath.Join(staging, prod.TreeFrom)
	wantGoodTree(t, tree)
	sums, err := manifest.ReadSums(osFS, tree)
	if err != nil {
		t.Fatalf("the tree has no usable SHA256SUMS: %v", err)
	}
	if len(sums) != 4 {
		t.Errorf("sums = %v", sums)
	}
	want, err := manifest.TreeSHA256(sums)
	if err != nil || prod.TreeSHA256 != want {
		t.Errorf("tree hash = %s, want %s (%v)", prod.TreeSHA256, want, err)
	}
	for _, s := range sums {
		b, _ := os.ReadFile(filepath.Join(tree, s.Path))
		if sum256(b) != s.SHA256 {
			t.Errorf("%s: SHA256SUMS does not match the file", s.Path)
		}
	}
}

// A github_release item with extract: true becomes a directory item whose tree is the unpacked archive;
// the archive is verified against the pin on the way, and a retry reuses it.
func TestGitHubExtractProducesADirectoryItem(t *testing.T) {
	p := newProduceRig(t)
	g := newFakeGitHub(p.r)
	p.r.w.GitHubAPI = g.srv.URL
	data := goodTarGz(t)
	p.origin.put("/dl/assets.tar.gz", data, "", "")
	asset := fakeAsset("assets.tar.gz", "sha256:"+sum256(data), int64(len(data)))
	asset["browser_download_url"] = p.origin.srv.URL + "/dl/assets.tar.gz"
	g.releases["org-alpha/map-assets@v1.2.3"] = []map[string]any{asset}

	gh := &GitHubRelease{W: p.r.w}
	res, err := gh.Engine().Resolve(context.Background(), ghItem("assets.tar.gz", sum256(data), truePtr()), plan.ItemPlan{})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Dir || res.Produce == nil || len(res.Files) != 0 || res.Build != nil || res.Version != "v1.2.3" || res.Trust != manifest.TrustPinned {
		t.Fatalf("resolution = %+v", res)
	}
	prod, staging, err := p.produce(res, "map-assets")
	if err != nil {
		t.Fatal(err)
	}
	checkSealed(t, staging, prod)
	if prod.Trust != manifest.TrustPinned || prod.Source["archive"] != "assets.tar.gz" || prod.Source["archive_sha256"] != sum256(data) {
		t.Errorf("produced = %+v", prod)
	}
	// The verified archive sits in staging, outside the tree.
	if sum, _, err := hashFile(filepath.Join(staging, "archive", "assets.tar.gz")); err != nil || sum != sum256(data) {
		t.Errorf("the archive in staging: %s, %v", sum, err)
	}
	gets := p.origin.gets
	if gets() != 1 {
		t.Errorf("GETs = %d", gets())
	}
	// A retry finds the verified archive and does not download it again; it unpacks into a new directory.
	prod2, _, err := p.produce(res, "map-assets")
	if err != nil {
		t.Fatal(err)
	}
	if gets() != 1 || prod2.TreeFrom != "extract.2" {
		t.Errorf("retry: GETs = %d, tree = %s", gets(), prod2.TreeFrom)
	}
}

// An url item with extract: true works exactly the same, for a zip too.
func TestURLExtractProducesADirectoryItem(t *testing.T) {
	for name, data := range map[string][]byte{
		"tar.gz": goodTarGz(t),
		"zip": archZip(t,
			archZipEntry{name: "fonts/Alpha Regular/0-255.pbf", body: "glyphs"},
			archZipEntry{name: "sprites/sprite.json", body: `{"a":1}`},
			archZipEntry{name: "sprites/sprite.png", body: "png", mode: 0o600},
			archZipEntry{name: "bin/run.sh", body: "#!/bin/sh\n", mode: 0o755},
			archZipEntry{name: "empty/"}),
	} {
		t.Run(name, func(t *testing.T) {
			p := newProduceRig(t)
			p.origin.put("/dl/bundle."+name, data, `"e1"`, "")
			u := &URLSource{W: p.r.w}
			it := urlContent("kit-tool-bundle", "kit", catalog.ContentSource{
				Type: "url", URL: p.origin.srv.URL + "/dl/bundle." + name, SHA256: sum256(data), Extract: truePtr(),
			})
			res, err := u.Engine().Resolve(context.Background(), it, plan.ItemPlan{})
			if err != nil {
				t.Fatal(err)
			}
			if !res.Dir || res.Produce == nil || len(res.Files) != 0 || res.Version != sum256(data)[:12] {
				t.Fatalf("resolution = %+v", res)
			}
			prod, staging, err := p.produce(res, "kit-tool-bundle")
			if err != nil {
				t.Fatal(err)
			}
			checkSealed(t, staging, prod)
		})
	}
}

// An extract item whose checksum is computed (trust on first use) records the archive's digest as the
// downloader measured it, and re-downloads on a retry.
func TestURLExtractTOFURecordsTheDigest(t *testing.T) {
	p := newProduceRig(t)
	data := goodTarGz(t)
	p.origin.put("/export/places.tar.gz", data, `"e1"`, "lm")
	it := urlContent("places", "maps", catalog.ContentSource{
		Type: "url", URL: p.origin.srv.URL + "/export/places.tar.gz", VersionPolicy: "latest", Checksum: "computed", Trust: "tofu", Extract: truePtr(),
	})
	res, err := (&URLSource{W: p.r.w}).Engine().Resolve(context.Background(), it, plan.ItemPlan{})
	if err != nil {
		t.Fatal(err)
	}
	prod, staging, err := p.produce(res, "places")
	if err != nil {
		t.Fatal(err)
	}
	checkSealed(t, staging, prod)
	if prod.Trust != manifest.TrustTOFU || prod.Source["archive_sha256"] != sum256(data) {
		t.Errorf("produced = %+v", prod)
	}
}

// Bytes that are not the pinned archive are the downloader's mismatch; nothing is unpacked.
func TestExtractArchiveThatIsNotThePin(t *testing.T) {
	p := newProduceRig(t)
	good := goodTarGz(t)
	p.origin.put("/dl/a.tar.gz", archGz(t, archTar(t, archEntry{name: "other.txt", body: "other"})), "", "")
	it := urlContent("x", "kit", catalog.ContentSource{Type: "url", URL: p.origin.srv.URL + "/dl/a.tar.gz", SHA256: sum256(good), Extract: truePtr()})
	res, err := (&URLSource{W: p.r.w}).Engine().Resolve(context.Background(), it, plan.ItemPlan{})
	if err != nil {
		t.Fatal(err)
	}
	_, staging, err := p.produce(res, "x")
	var de *download.Error
	if !errors.As(err, &de) || de.Kind != download.KindMismatch {
		t.Fatalf("err = %v", err)
	}
	if ents, _ := filepath.Glob(filepath.Join(staging, "extract.*")); len(ents) != 0 {
		t.Errorf("something was unpacked: %v", ents)
	}
}

// An archive that is exactly the pinned one but holds something forbidden is refused as a whole:
// LAI-SYN-021, permanent, and no tree is handed on.
func TestExtractPinnedButUnsafeArchive(t *testing.T) {
	for name, data := range map[string][]byte{
		"traversal": archGz(t, archTar(t, archEntry{name: "ok.txt", body: "fine"}, archEntry{name: "../escape", body: "x"})),
		"symlink":   archGz(t, archTar(t, archEntry{name: "link", typ: tar.TypeSymlink, link: "/"})),
		"hard link": archGz(t, archTar(t, archEntry{name: "a", body: "x"}, archEntry{name: "b", typ: tar.TypeLink, link: "a"})),
		"device":    archGz(t, archTar(t, archEntry{name: "null", typ: tar.TypeChar, major: 1})),
		"empty":     archGz(t, archTar(t)),
	} {
		t.Run(name, func(t *testing.T) {
			p := newProduceRig(t)
			p.origin.put("/dl/evil.tar.gz", data, "", "")
			it := urlContent("x", "kit", catalog.ContentSource{Type: "url", URL: p.origin.srv.URL + "/dl/evil.tar.gz", SHA256: sum256(data), Extract: truePtr()})
			res, err := (&URLSource{W: p.r.w}).Engine().Resolve(context.Background(), it, plan.ItemPlan{})
			if err != nil {
				t.Fatal(err)
			}
			prod, _, err := p.produce(res, "x")
			var re *item.ResolveError
			if !errors.As(err, &re) || re.Code != doctor.SYN021 || !re.Permanent {
				t.Fatalf("err = %#v", err)
			}
			if prod.TreeFrom != "" || prod.TreeSHA256 != "" {
				t.Errorf("a refused archive produced a tree: %+v", prod)
			}
		})
	}
}

// Produce writes only where it was told to: a staging directory that is not the item's is refused, and
// being offline stops it before the download.
func TestExtractProduceGuards(t *testing.T) {
	p := newProduceRig(t)
	data := goodTarGz(t)
	p.origin.put("/dl/a.tar.gz", data, "", "")
	it := urlContent("x", "kit", catalog.ContentSource{Type: "url", URL: p.origin.srv.URL + "/dl/a.tar.gz", SHA256: sum256(data), Extract: truePtr()})
	res, err := (&URLSource{W: p.r.w}).Engine().Resolve(context.Background(), it, plan.ItemPlan{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = res.Produce(context.Background(), item.ProduceEnv{StagingDir: filepath.Join(p.root, "elsewhere"), RootID: "internal", Root: p.root})
	var re *item.ResolveError
	if !errors.As(err, &re) || re.Code != item.CodeInternal {
		t.Errorf("wrong staging: %v", err)
	}
	before := p.origin.count()
	p.r.online = false
	staging := filepath.Join(p.root, ".staging", "x", res.Version)
	_ = os.MkdirAll(staging, 0o755)
	_, err = res.Produce(context.Background(), item.ProduceEnv{StagingDir: staging, RootID: "internal", Root: p.root})
	if CodeOf(err) != doctor.SYN002 {
		t.Errorf("offline: %v", err)
	}
	if p.origin.count() != before {
		t.Errorf("%d requests were made while offline", p.origin.count()-before)
	}
}
