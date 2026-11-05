package kit_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/catalog"
	"github.com/marinoscar/local-ai/agent/internal/kit"
	"github.com/marinoscar/local-ai/agent/internal/sync/manifest"
)

// The real catalog/ark.yaml puts the release's signed checksum list where the kit readers look for it
// (issue #360, rebuild-kit.md section 3.2.1).
func TestRealCatalogAgentBinariesCarryTheSignedList(t *testing.T) {
	cat, err := catalog.Load(filepath.Join("..", "..", "..", "catalog"))
	if err != nil {
		t.Fatal(err)
	}
	it, ok := cat.ContentItem("kit-agent-binaries")
	if !ok {
		t.Fatal("kit-agent-binaries is not in the catalog")
	}
	l, err := kit.LayoutOf(*it)
	if err != nil || l.Dir != kit.PartBin {
		t.Fatalf("layout = %+v, %v", l, err)
	}
	names := map[string]catalog.URLFile{}
	var order []string
	for _, f := range it.Source.Files {
		n := f.ItemPath()
		if !manifest.SafePath(n) {
			t.Errorf("%s: %q is not a usable path inside the item (the sync engine would refuse the file)", f.URL, n)
		}
		names[n] = f
		order = append(order, n)
	}
	sums, sig := kit.ReleaseDirName+"/SHA256SUMS", kit.ReleaseDirName+"/SHA256SUMS.minisig"
	for _, n := range []string{sums, sig, "aiagent-linux-amd64", "aiagent-linux-arm64", "install-node.sh"} {
		if _, ok := names[n]; !ok {
			t.Errorf("kit-agent-binaries has no file %q (has %v)", n, order)
		}
	}
	// The signed list is what the readers verify against, so it comes from the same release as the
	// binaries and is pinned like them.
	tag := func(url string) string {
		i := strings.Index(url, "/releases/download/")
		if i < 0 {
			return ""
		}
		return strings.SplitN(url[i+len("/releases/download/"):], "/", 2)[0]
	}
	want := tag(names["aiagent-linux-amd64"].URL)
	if want == "" {
		t.Fatalf("the agent URL is not a release download: %s", names["aiagent-linux-amd64"].URL)
	}
	for n, f := range names {
		if tag(f.URL) != want {
			t.Errorf("%s comes from release %q, the binaries from %q", n, tag(f.URL), want)
		}
		if !manifest.IsSHA256(strings.ToLower(f.SHA256)) {
			t.Errorf("%s is not pinned", n)
		}
	}
	// An item's version is the front of its first file's sha256: the signed list first means the version
	// names the release the list signs, and a node synced before the list existed fetches the item again.
	if order[0] != sums {
		t.Errorf("the first file is %q, want the signed list %q", order[0], sums)
	}
}
