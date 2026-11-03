package run

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/ark"
	"github.com/marinoscar/local-ai/agent/internal/catalog"
	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/sync/item"
	"github.com/marinoscar/local-ai/agent/internal/sync/manifest"
	"github.com/marinoscar/local-ai/agent/internal/sync/plan"
)

func TestPinnedModel(t *testing.T) {
	model := &catalog.Model{
		ID: "qwen-x", Engine: "llama",
		Source: catalog.ModelSource{Type: "huggingface", Repo: "org-alpha/qwen-x-gguf", Revision: "0123456789abcdef0123456789abcdef01234567"},
		Files: []catalog.ModelFile{
			{Kind: "weights", Name: "qwen-x-q4.gguf", SizeBytes: 5000, SHA256: strings.ToUpper(sha([]byte("w")))},
			{Kind: "mmproj", Name: "mmproj.gguf", URL: "https://mirror.example.test/mmproj.gguf", SizeBytes: 90, SHA256: sha([]byte("m"))},
		},
	}
	res, err := Pinned(plan.ModelItem(model), plan.ItemPlan{ID: "qwen-x", ToVersion: "0123456789ab"}, "2026.11.02-1")
	if err != nil {
		t.Fatal(err)
	}
	if res.Version != "0123456789ab" || res.Trust != manifest.TrustPinned || res.Source.Type != "huggingface" || res.Source.String("repo") != "org-alpha/qwen-x-gguf" || res.Resolver != "catalog_pin" {
		t.Fatalf("resolution = %+v", res)
	}
	if len(res.Files) != 2 {
		t.Fatalf("files = %+v", res.Files)
	}
	w, m := res.Files[0], res.Files[1]
	if w.URL != "https://huggingface.co/org-alpha/qwen-x-gguf/resolve/0123456789abcdef0123456789abcdef01234567/qwen-x-q4.gguf" || w.SHA256 != sha([]byte("w")) || w.Size != 5000 || !w.Auth || w.SHA256Source != "catalog" {
		t.Fatalf("weights = %+v", w)
	}
	if m.URL != "https://mirror.example.test/mmproj.gguf" || m.Kind != "mmproj" {
		t.Fatalf("mmproj = %+v", m)
	}
	// The planner's own version is used when the row has none.
	if res, err = Pinned(plan.ModelItem(model), plan.ItemPlan{ID: "qwen-x"}, ""); err != nil || res.Version != "0123456789ab" {
		t.Fatalf("version from the catalog: %+v, %v", res, err)
	}
	// An unpinned file is not guessed at.
	bad := *model
	bad.Files = []catalog.ModelFile{{Kind: "weights", Name: "x.gguf"}}
	_, err = Pinned(plan.ModelItem(&bad), plan.ItemPlan{ID: "qwen-x", ToVersion: "v"}, "")
	var re *item.ResolveError
	if !errors.As(err, &re) || re.Code != doctor.SYN033 || !re.Permanent {
		t.Fatalf("unpinned file: %v", err)
	}
}

func TestPinnedContent(t *testing.T) {
	t.Run("a url item", func(t *testing.T) {
		c := &catalog.ContentItem{ID: "docs-x", Kind: "docs", ExpectedSizeBytes: 77, Source: catalog.ContentSource{Type: "url", URL: "https://files.example.test/pub/guide.pdf?token=abc", SHA256: sha([]byte("g"))}}
		res, err := Pinned(plan.ContentItem(c), plan.ItemPlan{ID: "docs-x", ToVersion: "v9"}, "")
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Files) != 1 || res.Files[0].Path != "guide.pdf" || res.Files[0].Size != 77 || res.Files[0].SHA256 != sha([]byte("g")) || res.Source.Type != "url" {
			t.Fatalf("resolution = %+v", res)
		}
	})
	t.Run("a url item with a file list", func(t *testing.T) {
		c := &catalog.ContentItem{ID: "two", Kind: "docs", Source: catalog.ContentSource{Type: "url", Files: []catalog.URLFile{
			{URL: "https://files.example.test/a.bin", SHA256: sha([]byte("a"))}, {URL: "https://files.example.test/b.bin", SHA256: sha([]byte("b"))},
		}}}
		res, err := Pinned(plan.ContentItem(c), plan.ItemPlan{ID: "two", ToVersion: "v"}, "")
		if err != nil || len(res.Files) != 2 || res.Files[1].Path != "b.bin" || res.Files[0].Size != 0 {
			t.Fatalf("%+v, %v", res, err)
		}
	})
	t.Run("a pinned kiwix file", func(t *testing.T) {
		c := &catalog.ContentItem{ID: "wiki", Kind: "kiwix", ExpectedSizeBytes: 1234, Source: catalog.ContentSource{
			Type: "kiwix", Book: "wikipedia_en", BaseURL: "https://kiwix.example.test/zim/", VersionPolicy: "pinned",
			Pinned: &catalog.KiwixPinned{File: "wikipedia_en_2026-10.zim", SHA256: sha([]byte("z"))},
		}}
		res, err := Pinned(plan.ContentItem(c), plan.ItemPlan{ID: "wiki", ToVersion: "2026-10"}, "")
		if err != nil || res.Files[0].URL != "https://kiwix.example.test/zim/wikipedia_en_2026-10.zim" || res.Files[0].Kind != "zim" || res.Source.String("book") != "wikipedia_en" {
			t.Fatalf("%+v, %v", res, err)
		}
	})
	t.Run("what needs a resolver is unresolved", func(t *testing.T) {
		for name, c := range map[string]*catalog.ContentItem{
			"kiwix latest":   {ID: "w", Kind: "kiwix", Source: catalog.ContentSource{Type: "kiwix", VersionPolicy: "latest", BaseURL: "https://k.example.test/"}},
			"a git mirror":   {ID: "g", Kind: "kit", Source: catalog.ContentSource{Type: "git_mirror", URL: "https://git.example.test/r.git"}},
			"a url, no sha":  {ID: "u", Kind: "docs", Source: catalog.ContentSource{Type: "url", URL: "https://files.example.test/x.bin"}},
			"an apt closure": {ID: "a", Kind: "kit", Source: catalog.ContentSource{Type: "apt_closure"}},
		} {
			_, err := Pinned(plan.ContentItem(c), plan.ItemPlan{ID: c.ID, ToVersion: "v"}, "")
			var re *item.ResolveError
			if !errors.As(err, &re) || re.Code != doctor.SYN033 {
				t.Errorf("%s: %v", name, err)
			}
		}
	})
}

func TestItemResolversRegistry(t *testing.T) {
	r := NewItemResolvers()
	called := ""
	f := ItemResolverFunc(func(ctx context.Context, it plan.CatalogItem, target plan.ItemPlan) (item.Resolution, error) {
		called = it.ID
		return item.Resolution{Version: "v"}, nil
	})
	if r.Register("", f) == nil || r.Register("x", nil) == nil {
		t.Fatal("an incomplete registration was accepted")
	}
	if err := r.Register("kiwix", f); err != nil {
		t.Fatal(err)
	}
	if r.Register("kiwix", f) == nil {
		t.Fatal("a second resolver for one source type was accepted")
	}
	got, ok := r.For("kiwix")
	if !ok {
		t.Fatal("not registered")
	}
	if _, err := got.Resolve(bg, plan.CatalogItem{ID: "wiki"}, plan.ItemPlan{}); err != nil || called != "wiki" {
		t.Fatalf("called = %q, %v", called, err)
	}
	if _, ok := r.For("url"); ok {
		t.Fatal("an unregistered type resolved")
	}
	var nilReg *ItemResolvers
	if _, ok := nilReg.For("kiwix"); ok {
		t.Fatal("a nil registry resolved")
	}
}

type copies map[string][]ark.Copy

func (c copies) Copies(id string) []ark.Copy { return c[id] }

func TestLocalCopyResolution(t *testing.T) {
	dir := t.TempDir()
	t.Run("a file item copies the files of the other root's manifest", func(t *testing.T) {
		m := manifest.Manifest{
			ItemID: "qwen-x", ItemKind: "model", Engine: "llama", Version: "rev1", SortKey: "rev1", State: "verified",
			Source: manifest.Source{Type: "huggingface"}, Trust: manifest.TrustPinned,
			Files: []manifest.File{
				{Kind: "weights", Path: "b.gguf", SizeBytes: 20, SHA256: sha([]byte("b")), SHA256Source: "catalog"},
				{Kind: "mmproj", Path: "a.gguf", SizeBytes: 10, SHA256: sha([]byte("a")), SHA256Source: "catalog"},
			},
		}
		c := copies{"qwen-x": {
			{RootID: "internal", Dir: filepath.Join(dir, "internal", "models", "qwen-x"), Manifest: m},
			{RootID: driveID, Dir: filepath.Join(dir, "drive", "models", "qwen-x"), Manifest: manifest.Manifest{Version: "other"}},
		}}
		res, err := LocalCopy(config.OSFS{}, c, plan.ItemPlan{ID: "qwen-x", FromRoot: "internal", ToVersion: "rev1"})
		if err != nil {
			t.Fatal(err)
		}
		if res.Version != "rev1" || !res.Local || res.Dir || res.Resolver != "local_copy" || res.Trust != manifest.TrustPinned || len(res.Files) != 2 {
			t.Fatalf("resolution = %+v", res)
		}
		if res.Files[0].Path != "a.gguf" || res.Files[0].SHA256 != sha([]byte("a")) || res.Files[0].Size != 10 || res.Files[0].Source == nil || res.Files[0].URL != "" {
			t.Fatalf("files = %+v", res.Files)
		}
	})
	t.Run("a directory item copies the files of SHA256SUMS and expects the tree hash", func(t *testing.T) {
		src := filepath.Join(dir, "internal", "docs")
		if err := os.MkdirAll(src, 0o755); err != nil {
			t.Fatal(err)
		}
		sums := []manifest.Sum{{SHA256: sha([]byte("1")), Path: "index.html"}, {SHA256: sha([]byte("2")), Path: "guide/a.txt"}}
		if err := manifest.WriteSums(config.OSFS{}, src, sums); err != nil {
			t.Fatal(err)
		}
		tree, _ := manifest.TreeSHA256(sums)
		m := manifest.Manifest{ItemID: "docs", ItemKind: "docs", Version: "v1", SortKey: "v1", State: "verified", Source: manifest.Source{Type: "repo_docs"}, Trust: manifest.TrustComputed, TreeSHA256: tree, FileCount: 2}
		c := copies{"docs": {{RootID: "internal", Dir: src, Manifest: m}}}
		res, err := LocalCopy(config.OSFS{}, c, plan.ItemPlan{ID: "docs", FromRoot: "internal"})
		if err != nil {
			t.Fatal(err)
		}
		if !res.Dir || res.ExpectedTree != tree || len(res.Files) != 2 || res.Files[0].Path != "guide/a.txt" || res.Files[1].SHA256 != sha([]byte("1")) {
			t.Fatalf("resolution = %+v", res)
		}
		var paths []string
		for _, f := range res.Files {
			paths = append(paths, f.Path)
		}
		if !reflect.DeepEqual(paths, []string{"guide/a.txt", "index.html"}) {
			t.Fatalf("paths = %v", paths)
		}
	})
	t.Run("a source with no verified copy is LAI-SYN-070", func(t *testing.T) {
		_, err := LocalCopy(config.OSFS{}, copies{}, plan.ItemPlan{ID: "gone", FromRoot: "internal"})
		var re *item.ResolveError
		if !errors.As(err, &re) || re.Code != doctor.SYN070 {
			t.Fatalf("err = %v", err)
		}
		// A copy of another version is not the one the plan expected.
		c := copies{"x": {{RootID: "internal", Manifest: manifest.Manifest{Version: "other"}}}}
		if _, err := LocalCopy(config.OSFS{}, c, plan.ItemPlan{ID: "x", FromRoot: "internal", ToVersion: "wanted"}); err == nil {
			t.Fatal("a copy of another version was used")
		}
	})
}
