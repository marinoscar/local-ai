package ark_test

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/ark"
	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/config/fake"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/sync/manifest"
)

func TestItemDir(t *testing.T) {
	tests := []struct {
		name string
		ref  ark.ItemRef
		want string // "" = error
	}{
		{"llama model", model("qwen-x", "llama"), "models/qwen-x"},
		{"whisper model", model("whisper-small", "whisper"), "speech/whisper/whisper-small"},
		{"piper model", model("piper-es", "piper"), "speech/piper/piper-es"},
		{"kiwix", ark.ItemRef{Kind: "kiwix", ID: "wiki-x"}, "kiwix/wiki-x"},
		{"maps", ark.ItemRef{Kind: "maps", ID: "osm-central-america"}, "maps/osm-central-america"},
		{"map assets", ark.ItemRef{Kind: "maps", ID: "map-assets"}, "maps/assets"},
		{"kit src", ark.ItemRef{Kind: "kit", ID: "kit-llama-cpp", Subdir: "src"}, "kit/src"},
		{"kit nested", ark.ItemRef{Kind: "kit", ID: "kit-debs", Subdir: "debs/resolute-amd64"}, "kit/debs/resolute-amd64"},
		{"docs", ark.ItemRef{Kind: "docs", ID: "docs-bundle"}, "docs"},
		{"model without engine", model("m", ""), ""},
		{"model with unknown engine", model("m", "vllm"), ""},
		{"unknown kind", ark.ItemRef{Kind: "game", ID: "x"}, ""},
		{"empty kind", ark.ItemRef{ID: "x"}, ""},
		{"empty id", ark.ItemRef{Kind: "kiwix"}, ""},
		{"id climbs", ark.ItemRef{Kind: "kiwix", ID: ".."}, ""},
		{"id with slash", ark.ItemRef{Kind: "kiwix", ID: "a/b"}, ""},
		{"kit without subdir", ark.ItemRef{Kind: "kit", ID: "k"}, ""},
		{"kit subdir climbs", ark.ItemRef{Kind: "kit", ID: "k", Subdir: "../x"}, ""},
		{"kit subdir absolute", ark.ItemRef{Kind: "kit", ID: "k", Subdir: "/x"}, ""},
		{"kit subdir unclean", ark.ItemRef{Kind: "kit", ID: "k", Subdir: "a//b"}, ""},
		{"kit subdir backslash", ark.ItemRef{Kind: "kit", ID: "k", Subdir: `a\b`}, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ark.ItemDir(tc.ref)
			if tc.want == "" {
				if err == nil {
					t.Fatalf("want an error, got %q", got)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("got %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

func TestRootItemPath(t *testing.T) {
	r := ark.NewInternalRoot("/srv/localai", "p")
	p, err := r.ItemPath(model("qwen-x", "llama"))
	if err != nil || p != "/srv/localai/models/qwen-x" {
		t.Fatalf("%q %v", p, err)
	}
	if _, err := r.ItemPath(model("..", "llama")); err == nil {
		t.Fatal("a bad id must fail")
	}
}

func TestLoadItem(t *testing.T) {
	root := testRoot(t, ark.InternalID, 0)
	dir := putItem(t, root, llama("qwen-x", "v1", "2026-11-01"))

	it, err := ark.LoadItem(config.OSFS{}, root, "models/qwen-x")
	if err != nil {
		t.Fatal(err)
	}
	if it.Dir != dir || it.RootID != "internal" || it.Manifest.Version != "v1" {
		t.Fatalf("%+v", it)
	}

	t.Run("no manifest is not here, not a problem", func(t *testing.T) {
		_, err := ark.LoadItem(config.OSFS{}, root, "models/other")
		if !errors.Is(err, fs.ErrNotExist) || ark.IsInvalidItem(err) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("a file is missing", func(t *testing.T) {
		if err := os.Remove(filepath.Join(dir, "qwen-x.gguf")); err != nil {
			t.Fatal(err)
		}
		_, err := ark.LoadItem(config.OSFS{}, root, "models/qwen-x")
		if !errors.Is(err, manifest.ErrIncomplete) || !ark.IsInvalidItem(err) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("a manifest copied into another item's directory is misplaced", func(t *testing.T) {
		other := putItem(t, root, llama("elsewhere", "v1", "2026-11-01"))
		b := readFile(t, filepath.Join(other, manifest.FileName))
		writeFile(t, filepath.Join(root.Path, "models", "copy", manifest.FileName), b)
		writeFile(t, filepath.Join(root.Path, "models", "copy", "elsewhere.gguf"), readFile(t, filepath.Join(other, "elsewhere.gguf")))
		_, err := ark.LoadItem(config.OSFS{}, root, "models/copy")
		if !errors.Is(err, ark.ErrMisplaced) || !ark.IsInvalidItem(err) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("a path that climbs out of the root", func(t *testing.T) {
		if _, err := ark.LoadItem(config.OSFS{}, root, "../etc"); err == nil || errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("got %v", err)
		}
	})
}

// layoutRoot builds a root holding one item of every kind, plus the things a scan must skip.
func layoutRoot(t *testing.T) (ark.Root, map[string]string) {
	root := testRoot(t, ark.InternalID, 0)
	want := map[string]string{} // rel dir -> item id
	add := func(s itemSpec) {
		dir := putItem(t, root, s)
		rel, _ := filepath.Rel(root.Path, dir)
		want[filepath.ToSlash(rel)] = s.ref.ID
	}
	add(llama("qwen-x", "v1", "2026-11-01"))
	add(itemSpec{ref: model("whisper-small", "whisper"), version: "w1", sortKey: "2026-11-01", files: map[string]string{"ggml.bin": "w"}})
	add(itemSpec{ref: model("piper-es", "piper"), version: "p1", sortKey: "2026-11-01", files: map[string]string{"es.onnx": "p", "es.onnx.json": "{}"}})
	add(itemSpec{ref: ark.ItemRef{Kind: "kiwix", ID: "wiki-x"}, version: "2026-10", sortKey: "2026-10", files: map[string]string{"wiki.zim": "z"}})
	add(itemSpec{ref: ark.ItemRef{Kind: "maps", ID: "osm-ca"}, version: "20261101", sortKey: "20261101", files: map[string]string{"ca.pmtiles": "m"}})
	add(itemSpec{ref: ark.ItemRef{Kind: "maps", ID: "map-assets"}, version: "a1", sortKey: "2026-11-01", files: map[string]string{"fonts/a.pbf": "f", "style.json": "{}"}, dirItem: true})
	add(itemSpec{ref: ark.ItemRef{Kind: "kit", ID: "kit-src", Subdir: "src"}, version: "k1", sortKey: "2026-11-01", files: map[string]string{"llama.tar.gz": "t"}})
	add(itemSpec{ref: ark.ItemRef{Kind: "kit", ID: "kit-debs", Subdir: "debs/resolute-amd64"}, version: "k1", sortKey: "2026-11-01", files: map[string]string{"pool/a.deb": "d"}, dirItem: true})
	add(itemSpec{ref: ark.ItemRef{Kind: "kit", ID: "kit-deep", Subdir: "a/b/c"}, version: "k1", sortKey: "2026-11-01", files: map[string]string{"x": "x"}})
	add(itemSpec{ref: ark.ItemRef{Kind: "docs", ID: "docs-bundle"}, version: "d1", sortKey: "2026-11-01", files: map[string]string{"index.html": "<html>"}, dirItem: true})

	// Skipped without a word: no manifest (kit/config, a half-swapped directory), the kit index, the
	// staging and trash trees, files where directories are expected.
	writeFile(t, filepath.Join(root.Path, "kit", "config", "backup.tar"), "x")
	writeFile(t, filepath.Join(root.Path, "models", "halfswapped", "m.gguf"), "x")
	writeFile(t, filepath.Join(root.Path, "kit", manifest.FileName), `{"index":"of kit items"}`)
	writeFile(t, filepath.Join(root.Path, ".staging", "qwen-x", "v2", manifest.FileName), "garbage")
	writeFile(t, filepath.Join(root.Path, ".trash", "qwen-x", "v0", manifest.FileName), "garbage")
	writeFile(t, filepath.Join(root.Path, "models", "stray.txt"), "x")
	return root, want
}

func TestScanRoot(t *testing.T) {
	root, want := layoutRoot(t)
	items, problems, err := ark.ScanRoot(context.Background(), config.OSFS{}, root)
	if err != nil {
		t.Fatal(err)
	}
	if len(problems) != 0 {
		t.Fatalf("problems: %+v", problems)
	}
	got := map[string]string{}
	for i, it := range items {
		got[it.RelDir] = it.Manifest.ItemID
		if i > 0 && items[i-1].RelDir >= it.RelDir {
			t.Errorf("not sorted: %s before %s", items[i-1].RelDir, it.RelDir)
		}
		if it.RootID != "internal" || it.Dir != filepath.Join(root.Path, filepath.FromSlash(it.RelDir)) {
			t.Errorf("item %+v", it)
		}
	}
	// kit/a/b/c is three levels deep: found. Nothing from .staging or .trash.
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got  %v\nwant %v", got, want)
	}
}

func TestScanRootReportsBadItemsAndKeepsGoing(t *testing.T) {
	root, want := layoutRoot(t)
	// Damaged manifest, a missing file, a manifest of another item, an unverified one.
	writeFile(t, filepath.Join(root.Path, "models", "damaged", manifest.FileName), `{"schema_version":1,`)
	dir := putItem(t, root, llama("incomplete", "v1", "2026-11-01"))
	if err := os.Remove(filepath.Join(dir, "incomplete.gguf")); err != nil {
		t.Fatal(err)
	}
	other := putItem(t, root, llama("origin", "v1", "2026-11-01"))
	writeFile(t, filepath.Join(root.Path, "models", "misplaced", manifest.FileName), readFile(t, filepath.Join(other, manifest.FileName)))
	want["models/origin"] = "origin"

	ud := putItem(t, root, llama("unverified", "v1", "2026-11-01"))
	b := readFile(t, filepath.Join(ud, manifest.FileName))
	writeFile(t, filepath.Join(ud, manifest.FileName), string(strings.Replace(b, `"verified"`, `"staged"`, 1)))

	items, problems, err := ark.ScanRoot(context.Background(), config.OSFS{}, root)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, it := range items {
		got[it.RelDir] = it.Manifest.ItemID
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("good items\ngot  %v\nwant %v", got, want)
	}
	bad := map[string]bool{}
	for _, p := range problems {
		if p.Code != doctor.SYN052 || ark.CodeManifestInvalid != doctor.SYN052 {
			t.Errorf("code %s", p.Code)
		}
		if p.Details["root_id"] != "internal" || p.Details["reason"] == "" || p.Details["invalid"] != true {
			t.Errorf("details %v", p.Details)
		}
		bad[p.Details["dir"].(string)] = true
	}
	for _, dir := range []string{"models/damaged", "models/incomplete", "models/misplaced", "models/unverified"} {
		if !bad[dir] {
			t.Errorf("no problem for %s (got %v)", dir, bad)
		}
	}
	if len(problems) != 4 {
		t.Errorf("%d problems: %+v", len(problems), problems)
	}
}

func TestScanRootDriveErrors(t *testing.T) {
	root, _ := layoutRoot(t)
	t.Run("a listing that fails is a problem and the scan goes on", func(t *testing.T) {
		ffs := fake.New(config.OSFS{})
		bad := &listFailFS{FS: ffs, fail: filepath.Join(root.Path, "kiwix")}
		items, problems, err := ark.ScanRoot(context.Background(), bad, root)
		if err != nil {
			t.Fatal(err)
		}
		if len(problems) != 1 || problems[0].Details["dir"] != "kiwix" || problems[0].Details["invalid"] != false {
			t.Fatalf("problems: %+v", problems)
		}
		for _, it := range items {
			if it.Manifest.ItemKind == "kiwix" {
				t.Fatal("kiwix must be missing")
			}
		}
		if len(items) != 9 {
			t.Fatalf("%d items", len(items))
		}
	})
	t.Run("a manifest that cannot be read is a problem with invalid=false", func(t *testing.T) {
		ffs := fake.New(config.OSFS{}).FailNth("readfile", 2)
		_, problems, err := ark.ScanRoot(context.Background(), ffs, root)
		if err != nil || len(problems) != 1 || problems[0].Details["invalid"] != false {
			t.Fatalf("%v %+v", err, problems)
		}
	})
	t.Run("a missing root is empty", func(t *testing.T) {
		items, problems, err := ark.ScanRoot(context.Background(), config.OSFS{}, ark.NewInternalRoot(filepath.Join(t.TempDir(), "nope"), ""))
		if err != nil || len(items) != 0 || len(problems) != 0 {
			t.Fatalf("%v %v %v", items, problems, err)
		}
	})
	t.Run("a cancelled context stops the scan", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, _, err := ark.ScanRoot(ctx, config.OSFS{}, root); !errors.Is(err, context.Canceled) {
			t.Fatalf("got %v", err)
		}
	})
}

// listFailFS fails ReadDir of one directory with EIO.
type listFailFS struct {
	config.FS
	fail string
}

func (f *listFailFS) ReadDir(name string) ([]os.DirEntry, error) {
	if name == f.fail {
		return nil, &fs.PathError{Op: "readdir", Path: name, Err: syscall.EIO}
	}
	return f.FS.ReadDir(name)
}
