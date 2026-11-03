package run

import (
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/ark"
	"github.com/marinoscar/local-ai/agent/internal/catalog"
	"github.com/marinoscar/local-ai/agent/internal/sync/plan"
)

func TestDefaultItemRefPlacesEveryKind(t *testing.T) {
	dir := func(it plan.CatalogItem) string {
		t.Helper()
		ref, err := DefaultItemRef(it)
		if err != nil {
			t.Fatalf("%s: %v", it.ID, err)
		}
		d, err := ark.ItemDir(ref)
		if err != nil {
			t.Fatalf("%s: %v", it.ID, err)
		}
		return d
	}
	for _, tc := range []struct {
		it   plan.CatalogItem
		want string
	}{
		{plan.ModelItem(&catalog.Model{ID: "qwen-x", Engine: "llama"}), "models/qwen-x"},
		{plan.ModelItem(&catalog.Model{ID: "tiny", Engine: "whisper"}), "speech/whisper/tiny"},
		{plan.ContentItem(&catalog.ContentItem{ID: "wiki", Kind: "kiwix"}), "kiwix/wiki"},
		{plan.ContentItem(&catalog.ContentItem{ID: "docs-bundle", Kind: "docs"}), "docs"},
		// A kit item is placed by the layout of rebuild-kit.md (internal/kit).
		{plan.ContentItem(&catalog.ContentItem{ID: "kit-pmtiles", Kind: "kit", Source: catalog.ContentSource{Type: "github_release"}}), "kit/tools/pmtiles"},
		{plan.ContentItem(&catalog.ContentItem{ID: "kit-debs-resolute-amd64", Kind: "kit", Source: catalog.ContentSource{Type: "apt_closure", Codename: "resolute", Arch: "amd64"}}), "kit/debs/resolute-amd64"},
	} {
		if got := dir(tc.it); got != tc.want {
			t.Errorf("%s is placed at %s, want %s", tc.it.ID, got, tc.want)
		}
	}
	// A kit item the layout has no place for is an error (the engine then skips it as unresolved).
	if _, err := DefaultItemRef(plan.ContentItem(&catalog.ContentItem{ID: "kit-unheard-of", Kind: "kit"})); err == nil {
		t.Fatal("an unknown kit item was placed")
	}
	if _, err := DefaultItemRef(plan.CatalogItem{ID: "kit-x", Kind: "kit"}); err == nil {
		t.Fatal("a kit item with no catalog entry was placed")
	}
}
