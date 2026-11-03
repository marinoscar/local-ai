package kit_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/ark"
	"github.com/marinoscar/local-ai/agent/internal/catalog"
	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/config/fake"
	"github.com/marinoscar/local-ai/agent/internal/kit"
	"github.com/marinoscar/local-ai/agent/internal/sync/manifest"
)

// UPDATE_GOLDEN=1 go test ./internal/kit/ rewrites testdata/golden (review the diff).
// longHash matches a sha256: golden files store its first eight digits instead, because the
// repository's redaction check rejects any 64-hex string outside its own fixtures.
var longHash = regexp.MustCompile(`\b[0-9a-f]{40,}\b`)

func golden(t *testing.T, name, got string) {
	t.Helper()
	got = longHash.ReplaceAllStringFunc(got, func(h string) string { return "sha256-" + h[:8] })
	path := filepath.Join("testdata", "golden", name)
	if os.Getenv("UPDATE_GOLDEN") != "" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run UPDATE_GOLDEN=1 go test ./internal/kit/ to create it)", err)
	}
	if got != string(want) {
		t.Errorf("%s differs from the golden file:\n--- got\n%s\n--- want\n%s", name, got, want)
	}
}

// nodeKit puts the kit of an internal node on a root: six items, the config backups and a prebuilt
// directory that have no manifest, and a stale index that must be ignored by the scan.
func nodeKit(t *testing.T) ark.Root {
	t.Helper()
	root := internalRoot(t)
	putKit(t, root, kitSpec{id: "kit-catalog", dir: "catalog", version: "catalog-2026.11.02-1",
		files: map[string]string{"models.yaml": "models", "ark.yaml": "ark", "CATALOG_VERSION": "2026.11.02-1\n"}})
	putKit(t, root, kitSpec{id: "kit-agent-binaries", dir: "bin", version: "node-v0.4.0",
		files: map[string]string{"aiagent-linux-amd64": "amd64-binary", "aiagent-linux-arm64": "arm64-binary", "install-node.sh": "installer"}})
	putKit(t, root, kitSpec{id: "kit-agent-source", dir: "src", version: "node-v0.4.0",
		files: map[string]string{"local-ai-node-v0.4.0-src.tar.gz": "source-tarball"}})
	putKit(t, root, kitSpec{id: "kit-llama-cpp", dir: "git/llama.cpp.git", version: "0123456789ab", dirItem: true,
		files: map[string]string{"HEAD": "ref: refs/heads/master\n", "objects/pack/p1.pack": "pack"}})
	putKit(t, root, kitSpec{id: "kit-go-toolchain", dir: "toolchains", version: "e3b0c44298fc",
		files: map[string]string{"go1.24.13.linux-amd64.tar.gz": "go-amd64"}})
	putKit(t, root, kitSpec{id: "kit-debs-resolute-amd64", dir: "debs/resolute-amd64", version: "a1b2c3d4e5f6", dirItem: true,
		files: map[string]string{"pool/build-essential.deb": "deb", "Packages": "pkgs", "PACKAGES.lock": "lock"}})
	// not items: config backups, prebuilt exports (KIT-05 writes their own manifest later), a stale index
	writeFile(t, filepath.Join(root.Path, "kit", "config", "ai-n3", "localai-config-ai-n3-20261101T000000Z.tar.gz"), "secret-backup")
	writeFile(t, filepath.Join(root.Path, "kit", "prebuilt", "llama.cpp-012345678-cuda-resolute-amd64-sm75.tar.gz"), "prebuilt")
	writeFile(t, filepath.Join(root.Path, "kit", "MANIFEST.json"), `{"schema": "localai.kit-index", "index_version": 0}`)
	writeFile(t, filepath.Join(root.Path, "kit", "README.txt"), "old guide")
	return root
}

func kitItemsOf(t *testing.T, c *catalog.Catalog, profile string) []catalog.ContentItem {
	t.Helper()
	p, ok := c.Profile(profile)
	if !ok {
		t.Fatalf("no profile %s", profile)
	}
	var out []catalog.ContentItem
	for _, id := range p.Include.Content {
		if strings.HasPrefix(id, "kit-") {
			out = append(out, item(t, c, id))
		}
	}
	return out
}

func buildNodeIndex(t *testing.T, root ark.Root, opts kit.IndexOptions) kit.Index {
	t.Helper()
	ix, err := kit.BuildIndex(context.Background(), config.OSFS{}, root, opts)
	if err != nil {
		t.Fatal(err)
	}
	return ix
}

func TestIndexOfANodeKit(t *testing.T) {
	c := fixtureCatalog(t)
	root := nodeKit(t)
	ix := buildNodeIndex(t, root, kit.IndexOptions{Now: epoch, CatalogVersion: c.Version, Expected: kitItemsOf(t, c, "small-8gb")})

	var ids []string
	for _, it := range ix.Items {
		ids = append(ids, it.ID+"@"+it.Dir)
	}
	// sorted by directory, not by id or by the order they were written
	wantSorted := "kit-agent-binaries@bin kit-catalog@catalog kit-debs-resolute-amd64@debs/resolute-amd64 kit-llama-cpp@git/llama.cpp.git kit-agent-source@src kit-go-toolchain@toolchains"
	if got := strings.Join(ids, " "); got != wantSorted {
		t.Errorf("items = %s\nwant    %s", got, wantSorted)
	}
	if len(ix.Invalid) != 0 {
		t.Errorf("invalid = %+v", ix.Invalid)
	}
	if got := strings.Join(ix.Missing, " "); got != "kit-desktop kit-kiwix-tools kit-nvidia-resolute-amd64-595-open kit-piper kit-pmtiles kit-whisper-cpp" {
		t.Errorf("missing = %s", got)
	}
	var parts []string
	for _, p := range ix.Parts {
		parts = append(parts, p.Part)
	}
	if got := strings.Join(parts, " "); got != "catalog bin src git toolchains debs" {
		t.Errorf("parts = %s (the order of the layout of rebuild-kit.md section 3.1)", got)
	}
	// directory items report the file count of their manifest
	for _, it := range ix.Items {
		switch it.ID {
		case "kit-llama-cpp":
			if it.Files != 2 || it.TreeSHA256 == "" {
				t.Errorf("mirror: %+v", it)
			}
		case "kit-agent-binaries":
			if it.Files != 3 || it.TreeSHA256 != "" {
				t.Errorf("binaries: %+v", it)
			}
		}
	}
	var total int64
	for _, it := range ix.Items {
		total += it.TotalBytes
	}
	if ix.TotalBytes != total || total == 0 {
		t.Errorf("TotalBytes = %d, items sum to %d", ix.TotalBytes, total)
	}

	data, err := ix.Encode()
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "index.json", string(data))
	golden(t, "README.txt", string(ix.Readme()))
}

func TestIndexIsNotAnItemManifest(t *testing.T) {
	root := nodeKit(t)
	ix := buildNodeIndex(t, root, kit.IndexOptions{Now: epoch})
	data, err := ix.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if !kit.IsIndex(data) {
		t.Error("IsIndex(index) = false")
	}
	if _, err := manifest.Parse(data); !errors.Is(err, manifest.ErrSchema) && !errors.Is(err, manifest.ErrCorrupt) {
		t.Errorf("an item-manifest parser accepted the index: %v", err)
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(data, &probe); err != nil {
		t.Fatal(err)
	}
	if _, has := probe["schema_version"]; has {
		t.Error("the index carries schema_version, which is the item manifest's key")
	}
	item := kitSpec{id: "kit-go-toolchain", dir: "toolchains", version: "v", files: map[string]string{"f": "x"}}.manifest()
	raw, _ := item.Encode()
	for name, in := range map[string][]byte{"an item manifest": raw, "empty": nil, "not json": []byte("nope"), "an array": []byte("[]"),
		"another schema": []byte(`{"schema": "other"}`), "schema of the wrong type": []byte(`{"schema": 1}`)} {
		if kit.IsIndex(in) {
			t.Errorf("IsIndex(%s) = true", name)
		}
	}
	// the scan that finds items never mistakes kit/MANIFEST.json for one
	items, problems, err := ark.ScanRoot(context.Background(), config.OSFS{}, root)
	if err != nil || len(problems) != 0 || len(items) != 6 {
		t.Errorf("ScanRoot = %d items, %+v, %v", len(items), problems, err)
	}
}

func TestIndexMissingAndInvalid(t *testing.T) {
	c := fixtureCatalog(t)
	root := internalRoot(t)
	// good
	putKit(t, root, kitSpec{id: "kit-go-toolchain", dir: "toolchains", version: "v1", files: map[string]string{"go.tar.gz": "go"}})
	// damaged JSON
	writeFile(t, filepath.Join(root.Path, "kit", "bin", "MANIFEST.json"), "{")
	// state is not verified
	putKit(t, root, kitSpec{id: "kit-catalog", dir: "catalog", version: "v1", files: map[string]string{"ark.yaml": "a"},
		mutate: func(m *manifest.Manifest) { m.State = "downloading" }})
	// a listed file is missing
	dir := putKit(t, root, kitSpec{id: "kit-agent-source", dir: "src", version: "v1", files: map[string]string{"s.tar.gz": "src"}})
	if err := os.Remove(filepath.Join(dir, "s.tar.gz")); err != nil {
		t.Fatal(err)
	}
	// a listed file has another size
	dir = putKit(t, root, kitSpec{id: "kit-desktop", dir: "desktop", version: "v1", files: map[string]string{"d.deb": "deb"}})
	writeFile(t, filepath.Join(dir, "d.deb"), "a much longer content than the manifest recorded")
	// a newer schema is not rewritten or trusted
	m := kitSpec{id: "kit-piper", dir: "tools/piper", version: "v1", files: map[string]string{"p.tar.gz": "p"}}.manifest()
	raw, _ := m.MarshalJSON()
	writeFile(t, filepath.Join(root.Path, "kit", "tools", "piper", "p.tar.gz"), "p")
	writeFile(t, filepath.Join(root.Path, "kit", "tools", "piper", "MANIFEST.json"), strings.Replace(string(raw), `"schema_version":1`, `"schema_version":2`, 1))
	// a manifest in the wrong directory: a toolchain manifest sitting where a different item belongs
	// is fine for the index (the directory is derived), but a model manifest in kit/ is misplaced
	mm := kitSpec{id: "some-model", dir: "x", version: "v1", files: map[string]string{"m.gguf": "m"}}.manifest()
	mm.ItemKind = manifest.KindModel
	mm.Engine = "llama"
	mraw, _ := mm.MarshalJSON()
	writeFile(t, filepath.Join(root.Path, "kit", "x", "m.gguf"), "m")
	writeFile(t, filepath.Join(root.Path, "kit", "x", "MANIFEST.json"), string(mraw))

	ix := buildNodeIndex(t, root, kit.IndexOptions{Now: epoch, Expected: kitItemsOf(t, c, "small-8gb")})

	if len(ix.Items) != 1 || ix.Items[0].ID != "kit-go-toolchain" {
		t.Errorf("items = %+v, want only the good one", ix.Items)
	}
	var inv []string
	for _, p := range ix.Invalid {
		if p.Code != "LAI-SYN-052" {
			t.Errorf("problem %+v has code %s, want LAI-SYN-052", p, p.Code)
		}
		inv = append(inv, p.Dir)
	}
	if got := strings.Join(inv, " "); got != "bin catalog desktop src tools/piper x" {
		t.Errorf("invalid dirs = %s", got)
	}
	// an invalid directory counts as missing too: its item is not usable
	for _, id := range []string{"kit-catalog", "kit-agent-source", "kit-desktop", "kit-piper", "kit-agent-binaries"} {
		found := false
		for _, m := range ix.Missing {
			found = found || m == id
		}
		if !found {
			t.Errorf("%s is not listed as missing", id)
		}
	}
	for _, m := range ix.Missing {
		if m == "kit-go-toolchain" {
			t.Error("a present item is listed as missing")
		}
	}
	readme := string(ix.Readme())
	for _, want := range []string{"NEEDS ATTENTION", "Missing: kit-agent-binaries", "Damaged: bin (LAI-SYN-052)", "aictl kit verify --full"} {
		if !strings.Contains(readme, want) {
			t.Errorf("README lacks %q:\n%s", want, readme)
		}
	}
	data, err := ix.Encode()
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "index-invalid.json", string(data))
}

func TestIndexEmptyAndEdgeRoots(t *testing.T) {
	t.Run("an empty kit directory", func(t *testing.T) {
		root := internalRoot(t)
		ix := buildNodeIndex(t, root, kit.IndexOptions{Now: epoch})
		if len(ix.Items) != 0 || len(ix.Invalid) != 0 || len(ix.Missing) != 0 || ix.TotalBytes != 0 {
			t.Errorf("index = %+v", ix)
		}
		data, err := ix.Encode()
		if err != nil {
			t.Fatal(err)
		}
		for _, key := range []string{`"items": []`, `"missing": []`, `"invalid": []`, `"parts": []`} {
			if !strings.Contains(string(data), key) {
				t.Errorf("an empty index lacks %s:\n%s", key, data)
			}
		}
		if !strings.Contains(string(ix.Readme()), "NOT HERE YET") {
			t.Errorf("README of an empty kit does not say what is missing")
		}
	})
	t.Run("no kit directory at all", func(t *testing.T) {
		root := ark.NewInternalRoot(filepath.Join(t.TempDir(), "srv"), "small-8gb")
		ix := buildNodeIndex(t, root, kit.IndexOptions{Now: epoch})
		if len(ix.Items) != 0 {
			t.Errorf("items = %+v", ix.Items)
		}
		if err := kit.WriteIndex(config.OSFS{}, root, ix); !errors.Is(err, kit.ErrIndex) {
			t.Errorf("WriteIndex err = %v, want ErrIndex", err)
		}
		if _, err := os.Stat(filepath.Join(root.Path, "kit")); !os.IsNotExist(err) {
			t.Errorf("WriteIndex created the kit directory")
		}
	})
	t.Run("a cancelled context stops the scan", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := kit.BuildIndex(ctx, config.OSFS{}, nodeKit(t), kit.IndexOptions{Now: epoch})
		if !errors.Is(err, kit.ErrIndex) || !errors.Is(err, context.Canceled) && !strings.Contains(err.Error(), "canceled") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("expected items that are not kit items are ignored, duplicates listed once", func(t *testing.T) {
		c := fixtureCatalog(t)
		root := internalRoot(t)
		exp := []catalog.ContentItem{item(t, c, "docs-bundle"), item(t, c, "kit-piper"), item(t, c, "kit-piper")}
		ix := buildNodeIndex(t, root, kit.IndexOptions{Now: epoch, Expected: exp})
		if strings.Join(ix.Missing, " ") != "kit-piper" {
			t.Errorf("missing = %v", ix.Missing)
		}
	})
	t.Run("the drive keeps the ISO: README says so", func(t *testing.T) {
		root := internalRoot(t)
		ix := buildNodeIndex(t, root, kit.IndexOptions{Now: epoch})
		if !strings.Contains(string(ix.Readme()), "only the AI-ARK drive keeps this") {
			t.Error("README does not explain the missing iso/ folder")
		}
		putKit(t, root, kitSpec{id: "kit-ubuntu-iso-26.04", dir: "iso", version: "26.04.1", files: map[string]string{"ubuntu.iso": "iso"}})
		ix = buildNodeIndex(t, root, kit.IndexOptions{Now: epoch})
		r := string(ix.Readme())
		if i := strings.Index(r, "iso/"); i < 0 || strings.Contains(r[i:], "only the AI-ARK drive keeps this") {
			t.Errorf("README still says the ISO is missing:\n%s", r)
		}
	})
}

func TestIndexIsDeterministic(t *testing.T) {
	c := fixtureCatalog(t)
	root := nodeKit(t)
	opts := kit.IndexOptions{Now: epoch.Add(900 * time.Millisecond), CatalogVersion: c.Version, Expected: kitItemsOf(t, c, "small-8gb")}
	first := buildNodeIndex(t, root, opts)
	b1, _ := first.Encode()
	for i := 0; i < 3; i++ {
		again := buildNodeIndex(t, root, opts)
		b2, _ := again.Encode()
		if string(b1) != string(b2) || string(first.Readme()) != string(again.Readme()) {
			t.Fatal("the same kit gave a different index")
		}
	}
	// the time is to the second, in UTC, whatever the zone of the caller's clock
	zone := time.FixedZone("UTC-6", -6*3600)
	local := buildNodeIndex(t, root, kit.IndexOptions{Now: epoch.In(zone), CatalogVersion: c.Version, Expected: opts.Expected})
	b3, _ := local.Encode()
	if string(b3) != string(b1) {
		t.Errorf("the index depends on the zone of the clock:\n%s\n%s", b1, b3)
	}
	if !strings.Contains(string(b1), `"generated_at": "2026-11-02T18:04:11Z"`) {
		t.Errorf("generated_at is not RFC 3339 UTC to the second:\n%s", b1)
	}
}

func TestReadmeShape(t *testing.T) {
	c := fixtureCatalog(t)
	root := nodeKit(t)
	// a root path that must never reach the README
	ix := buildNodeIndex(t, root, kit.IndexOptions{Now: epoch, CatalogVersion: c.Version, Expected: kitItemsOf(t, c, "small-8gb")})
	readme := string(ix.Readme())
	for i, line := range strings.Split(readme, "\n") {
		if len(line) > 78 {
			t.Errorf("line %d is %d characters: %q", i+1, len(line), line)
		}
		for _, r := range line {
			if r > 126 || r < 32 && r != '\t' {
				t.Errorf("line %d has a non-ASCII character %q", i+1, r)
				break
			}
		}
	}
	if !strings.HasSuffix(readme, "\n") || strings.Contains(readme, "\r") {
		t.Error("README must end with one LF and use LF only")
	}
	for _, want := range []string{
		"LOCAL AI REBUILD KIT",
		"../docs/06-rebuild-a-node-offline.html",
		"../docs/index.html",
		"installers/install-node.sh",
		"aictl kit verify",
		"Catalog:        " + c.Version,
		"Written (UTC):  2026-11-02 18:04",
	} {
		if !strings.Contains(readme, want) {
			t.Errorf("README lacks %q", want)
		}
	}
	if strings.Contains(readme, root.Path) || strings.Contains(readme, os.TempDir()) {
		t.Error("README leaks a host path")
	}
	if strings.Contains(readme, "secret-backup") || strings.Contains(readme, "localai-config-ai-n3") {
		t.Error("README names a config backup file")
	}
	// every top-level part is described, config/ last
	last := -1
	for _, name := range append(append([]string(nil), kit.Parts...), "config") {
		i := strings.Index(readme, "\n"+name+"/")
		if i < 0 {
			t.Errorf("README does not describe %s/", name)
		}
		if i < last {
			t.Errorf("%s/ is out of order", name)
		}
		last = i
	}
}

func TestHumanBytes(t *testing.T) {
	cases := []struct {
		n    int64
		want string
	}{
		{0, "0 B"}, {1, "1 B"}, {999, "999 B"}, {1000, "1.0 kB"}, {1500, "1.5 kB"}, {999_949, "999.9 kB"},
		{1_000_000, "1.0 MB"}, {612_400_000, "612.4 MB"}, {3_200_000_000, "3.2 GB"}, {153_665_522, "153.7 MB"},
		{2_000_000_000_000, "2.0 TB"}, {1_100_000_000_000_000, "1100.0 TB"}, {9_223_372_036_854_775_807, "9223372.0 TB"},
	}
	for _, tc := range cases {
		if got := kit.HumanBytes(tc.n); got != tc.want {
			t.Errorf("HumanBytes(%d) = %q, want %q", tc.n, got, tc.want)
		}
	}
}

// The acceptance test of KIT-01: the index and the guide are regenerated after a kit swap.
func TestRegenerateAfterAKitSwap(t *testing.T) {
	c := fixtureCatalog(t)
	root := nodeKit(t)
	now := epoch
	r := kit.Refresher{
		FS:             config.OSFS{},
		Now:            func() time.Time { return now },
		Expected:       func(ark.Root) []catalog.ContentItem { return kitItemsOf(t, c, "small-8gb") },
		CatalogVersion: func() string { return c.Version },
	}
	indexPath := filepath.Join(root.Path, "kit", "MANIFEST.json")
	readmePath := filepath.Join(root.Path, "kit", "README.txt")
	if readFile(t, readmePath) != "old guide" {
		t.Fatal("setup")
	}

	// 1. a swap of a kit item rewrites both files
	if err := r.AfterSwap(context.Background(), root, item(t, c, "kit-go-toolchain")); err != nil {
		t.Fatal(err)
	}
	idx1, rd1 := readFile(t, indexPath), readFile(t, readmePath)
	if !kit.IsIndex([]byte(idx1)) || !strings.Contains(idx1, `"id": "kit-go-toolchain"`) || !strings.Contains(rd1, "LOCAL AI REBUILD KIT") {
		t.Fatalf("not regenerated:\n%s\n%s", idx1, rd1)
	}
	for _, p := range []string{indexPath, readmePath} {
		if info, err := os.Stat(p); err != nil || info.Mode().Perm() != 0o644 {
			t.Errorf("%s: %v %v", p, info, err)
		}
	}

	// 2. a new item arrives (the swap of kit-piper): the next regeneration lists it and drops it from missing
	putKit(t, root, kitSpec{id: "kit-piper", dir: "tools/piper", version: "2023.11.14-2", files: map[string]string{"piper_linux_x86_64.tar.gz": "piper"}})
	now = epoch.Add(time.Minute)
	if err := r.AfterSwap(context.Background(), root, item(t, c, "kit-piper")); err != nil {
		t.Fatal(err)
	}
	idx2, rd2 := readFile(t, indexPath), readFile(t, readmePath)
	if !strings.Contains(idx2, `"id": "kit-piper"`) {
		t.Errorf("kit-piper not indexed:\n%s", idx2)
	}
	if strings.Contains(missingOf(t, idx2), "kit-piper") {
		t.Errorf("kit-piper is still missing: %s", missingOf(t, idx2))
	}
	if !strings.Contains(missingOf(t, idx1), "kit-piper") {
		t.Errorf("setup: kit-piper should have been missing before the swap")
	}
	if idx2 == idx1 || rd2 == rd1 || !strings.Contains(rd2, "tools/") {
		t.Error("the files did not change after a swap")
	}

	// 3. regenerating again without a change at the same time is byte-identical
	if err := r.Refresh(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	if readFile(t, indexPath) != idx2 || readFile(t, readmePath) != rd2 {
		t.Error("an unchanged kit gave different files")
	}

	// 4. an item removed (a drive prune) disappears from the index
	if err := os.RemoveAll(filepath.Join(root.Path, "kit", "tools")); err != nil {
		t.Fatal(err)
	}
	if err := r.Refresh(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(readFile(t, indexPath), `"id": "kit-piper"`) {
		t.Error("a removed item is still indexed")
	}

	// 5. the hook is a no-op for what is not a kit swap
	before := readFile(t, indexPath)
	if err := os.WriteFile(readmePath, []byte("hand edited"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := r.AfterSwap(context.Background(), root, item(t, c, "docs-bundle")); err != nil {
		t.Fatal(err)
	}
	if readFile(t, readmePath) != "hand edited" || readFile(t, indexPath) != before {
		t.Error("the hook wrote for a docs item")
	}
	ro := root
	ro.ReadOnly = true
	if err := r.AfterSwap(context.Background(), ro, item(t, c, "kit-go-toolchain")); err != nil {
		t.Fatal(err)
	}
	if readFile(t, readmePath) != "hand edited" {
		t.Error("the hook wrote to a read-only root")
	}

	// 6. a kit item swap does rewrite a hand-edited guide
	if err := r.AfterSwap(context.Background(), root, item(t, c, "kit-go-toolchain")); err != nil {
		t.Fatal(err)
	}
	if readFile(t, readmePath) == "hand edited" {
		t.Error("the guide was not regenerated")
	}
}

func missingOf(t *testing.T, indexJSON string) string {
	t.Helper()
	var v struct {
		Missing []string `json:"missing"`
	}
	if err := json.Unmarshal([]byte(indexJSON), &v); err != nil {
		t.Fatal(err)
	}
	return strings.Join(v.Missing, " ")
}

func TestRefresherDefaultsAndFailures(t *testing.T) {
	c := fixtureCatalog(t)
	t.Run("nil clock, expected and version hooks are fine", func(t *testing.T) {
		root := nodeKit(t)
		r := kit.Refresher{FS: config.OSFS{}}
		if err := r.AfterSwap(context.Background(), root, item(t, c, "kit-catalog")); err != nil {
			t.Fatal(err)
		}
		var v struct {
			GeneratedAt string   `json:"generated_at"`
			Missing     []string `json:"missing"`
		}
		if err := json.Unmarshal([]byte(readFile(t, filepath.Join(root.Path, "kit", "MANIFEST.json"))), &v); err != nil {
			t.Fatal(err)
		}
		if len(v.Missing) != 0 || v.GeneratedAt == "" {
			t.Errorf("index = %+v", v)
		}
	})
	t.Run("a root without a kit directory is an index error, nothing is created", func(t *testing.T) {
		root := ark.NewInternalRoot(filepath.Join(t.TempDir(), "srv"), "small-8gb")
		err := kit.Refresher{FS: config.OSFS{}}.AfterSwap(context.Background(), root, item(t, c, "kit-catalog"))
		if !errors.Is(err, kit.ErrIndex) {
			t.Fatalf("err = %v", err)
		}
		if _, err := os.Stat(root.Path); !os.IsNotExist(err) {
			t.Error("a directory was created")
		}
	})
	t.Run("the index goes first: a crash before the guide leaves a fresh index and the old guide", func(t *testing.T) {
		root := nodeKit(t)
		ffs := fake.New(config.OSFS{}).FailNth("rename", 2)
		err := kit.Refresher{FS: ffs, Now: func() time.Time { return epoch }}.Refresh(context.Background(), root)
		if !errors.Is(err, kit.ErrIndex) || !errors.Is(err, fake.ErrInjected) {
			t.Fatalf("err = %v", err)
		}
		if !strings.Contains(readFile(t, filepath.Join(root.Path, "kit", "MANIFEST.json")), `"id": "kit-catalog"`) {
			t.Error("the index was not written first")
		}
		if readFile(t, filepath.Join(root.Path, "kit", "README.txt")) != "old guide" {
			t.Error("the old guide was damaged")
		}
		entries, _ := os.ReadDir(filepath.Join(root.Path, "kit"))
		for _, e := range entries {
			if strings.Contains(e.Name(), ".tmp-") {
				t.Errorf("a temp file was left: %s", e.Name())
			}
		}
	})
	t.Run("every write step failing is an index error and leaves the old files", func(t *testing.T) {
		for _, op := range []string{"open", "write", "sync", "rename"} {
			t.Run(op, func(t *testing.T) {
				root := nodeKit(t)
				oldIdx := readFile(t, filepath.Join(root.Path, "kit", "MANIFEST.json"))
				ffs := fake.New(config.OSFS{}).FailOn(op, -1)
				err := kit.Refresher{FS: ffs, Now: func() time.Time { return epoch }}.Refresh(context.Background(), root)
				if !errors.Is(err, kit.ErrIndex) {
					t.Fatalf("err = %v", err)
				}
				if readFile(t, filepath.Join(root.Path, "kit", "MANIFEST.json")) != oldIdx || readFile(t, filepath.Join(root.Path, "kit", "README.txt")) != "old guide" {
					t.Error("a failed refresh changed the files")
				}
			})
		}
	})
}
