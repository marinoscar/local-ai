package kit_test

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/ark"
	"github.com/marinoscar/local-ai/agent/internal/catalog"
	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/sync/manifest"
)

// epoch is the clock every test shares.
var epoch = time.Date(2026, 11, 2, 18, 4, 11, 0, time.UTC)

// The repository's redaction check rejects any 64-hex string outside its own fixtures, so the
// fixture catalog writes a sha256 as @sha:<hex digit>@ and this expands it to 64 characters (the
// same convention as internal/catalog's testdata).
var shaToken = regexp.MustCompile(`@sha:([0-9a-f])@`)

func expand(s string) string {
	return shaToken.ReplaceAllStringFunc(s, func(m string) string {
		return strings.Repeat(shaToken.FindStringSubmatch(m)[1], 64)
	})
}

// fixtureDir copies testdata/catalog into a fresh temp dir, expanding the sha tokens, so a test can
// lint, load or edit it.
func fixtureDir(t testing.TB) string {
	t.Helper()
	src := filepath.Join("testdata", "catalog")
	dst := t.TempDir()
	err := filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, []byte(expand(string(data))), 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	return dst
}

// fixtureCatalog loads the fixture catalog (it must pass lint).
func fixtureCatalog(t testing.TB) *catalog.Catalog {
	t.Helper()
	c, err := catalog.Load(fixtureDir(t))
	if err != nil {
		t.Fatalf("the fixture catalog does not load: %v", err)
	}
	return c
}

func item(t testing.TB, c *catalog.Catalog, id string) catalog.ContentItem {
	t.Helper()
	it, ok := c.ContentItem(id)
	if !ok {
		t.Fatalf("fixture catalog has no item %s", id)
	}
	return *it
}

// shaOf is a stand-in sha256 (the hash of a name): no hash literal is ever written in the repo.
func shaOf(name string) string {
	sum := sha256.Sum256([]byte(name))
	return hex.EncodeToString(sum[:])
}

// writeFile writes a file with mode 0644, creating parents.
func writeFile(t testing.TB, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// kitSpec describes a verified kit item to put on a root.
type kitSpec struct {
	id      string
	dir     string // below kit/
	version string
	files   map[string]string
	dirItem bool
	// state overrides: break the manifest on purpose.
	mutate func(*manifest.Manifest)
}

func (s kitSpec) manifest() manifest.Manifest {
	m := manifest.Manifest{
		SchemaVersion: 1, ItemID: s.id, ItemKind: manifest.KindKit, CatalogVersion: "2026.11.02-1",
		Version: s.version, SortKey: s.version, State: manifest.StateVerified,
		Source:       manifest.Source{Type: "url"},
		DownloadedAt: epoch, VerifiedAt: epoch, LastReverifiedAt: epoch, Trust: manifest.TrustPinned,
		SyncedBy: manifest.SyncedBy{NodeID: "node-a", AgentVersion: "0.4.0", RunID: "run-20261102T180000Z-0001"},
	}
	var sums []manifest.Sum
	names := make([]string, 0, len(s.files))
	for p := range s.files {
		names = append(names, p)
	}
	sortStrings(names)
	for _, p := range names {
		c := s.files[p]
		m.TotalBytes += int64(len(c))
		if s.dirItem {
			sums = append(sums, manifest.Sum{SHA256: shaOf(p), Path: p})
			continue
		}
		m.Files = append(m.Files, manifest.File{Kind: "archive", Path: p, SizeBytes: int64(len(c)), SHA256: shaOf(p), SHA256Source: "catalog"})
	}
	if s.dirItem {
		m.TreeSHA256, _ = manifest.TreeSHA256(sums)
		m.FileCount = len(sums)
	}
	if s.mutate != nil {
		s.mutate(&m)
	}
	return m
}

func sortStrings(s []string) {
	for i := range s {
		for j := i + 1; j < len(s); j++ {
			if s[j] < s[i] {
				s[i], s[j] = s[j], s[i]
			}
		}
	}
}

// putKit writes the item's files and its MANIFEST under <root>/kit/<dir> and returns the directory.
func putKit(t testing.TB, root ark.Root, s kitSpec) string {
	t.Helper()
	dir := filepath.Join(root.Path, "kit", filepath.FromSlash(s.dir))
	for p, c := range s.files {
		writeFile(t, filepath.Join(dir, filepath.FromSlash(p)), c)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if s.dirItem {
		var sums []manifest.Sum
		for p := range s.files {
			sums = append(sums, manifest.Sum{SHA256: shaOf(p), Path: p})
		}
		if err := manifest.WriteSums(config.OSFS{}, dir, sums); err != nil {
			t.Fatal(err)
		}
	}
	if err := manifest.Write(config.OSFS{}, manifest.Path(dir), s.manifest()); err != nil {
		// A deliberately invalid manifest cannot go through Write: store its raw bytes instead.
		raw, merr := s.manifest().MarshalJSON()
		if merr != nil {
			t.Fatal(err)
		}
		writeFile(t, manifest.Path(dir), string(raw))
	}
	return dir
}

func internalRoot(t testing.TB) ark.Root {
	t.Helper()
	root := ark.NewInternalRoot(filepath.Join(t.TempDir(), "srv"), "small-8gb")
	if err := os.MkdirAll(filepath.Join(root.Path, "kit"), 0o755); err != nil {
		t.Fatal(err)
	}
	return root
}

func readFile(t testing.TB, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
