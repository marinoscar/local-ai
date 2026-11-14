package kit_test

import (
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/catalog"
	"github.com/marinoscar/local-ai/agent/internal/sync/plan"
)

// KIT-01 against the catalog that ships (catalog/ark.yaml and catalog/profiles), not the fixture: every
// profile names the kit items of the "Profiles" column of rebuild-kit.md section 3.2, and a dry-run plan
// of an empty root puts each of them in its section 3.1 folder. An edit of the catalog that drops a kit
// item from a profile, or moves one to another folder, fails here.

func realCatalog(t *testing.T) *catalog.Catalog {
	t.Helper()
	c, err := catalog.Load(filepath.Join("..", "..", "..", "catalog"))
	if err != nil {
		t.Fatalf("the real catalog does not load: %v", err)
	}
	return c
}

// realKitDirs is section 3.1 for the kit items the real catalog holds today. kit-desktop (no desktop
// release exists to pin) and kit-cuda-redist-* (off by default) are not in the catalog yet, so they are not here.
var realKitDirs = map[string]string{
	"kit-catalog":                        "kit/catalog",
	"kit-agent-binaries":                 "kit/bin",
	"kit-agent-source":                   "kit/src",
	"kit-desktop":                        "kit/desktop",
	"kit-llama-cpp":                      "kit/git/llama.cpp.git",
	"kit-whisper-cpp":                    "kit/git/whisper.cpp.git",
	"kit-local-ai-repo":                  "kit/git/local-ai.git",
	"kit-go-toolchain":                   "kit/toolchains",
	"kit-kiwix-tools":                    "kit/tools/kiwix-tools",
	"kit-pmtiles":                        "kit/tools/pmtiles",
	"kit-piper":                          "kit/tools/piper",
	"kit-debs-resolute-amd64":            "kit/debs/resolute-amd64",
	"kit-nvidia-resolute-amd64-595-open": "kit/nvidia/resolute-amd64-595-open",
	"kit-ubuntu-iso-26.04":               "kit/iso",
}

func TestRealCatalogKitItemsPerProfile(t *testing.T) {
	c := realCatalog(t)
	all := []string{"kit-agent-binaries", "kit-agent-source", "kit-catalog", "kit-desktop", "kit-go-toolchain", "kit-kiwix-tools",
		"kit-llama-cpp", "kit-piper", "kit-pmtiles", "kit-whisper-cpp", "kit-debs-resolute-amd64"}
	with := func(extra ...string) []string {
		out := append(append([]string(nil), all...), extra...)
		sort.Strings(out)
		return out
	}
	cases := []struct {
		profile string
		root    plan.RootInfo
		node    plan.NodeInfo
		want    []string
	}{
		{"small-8gb", internalInfo("small-8gb"), plan.NodeInfo{Backend: "cuda", Arch: "amd64", Codename: "resolute"}, with("kit-nvidia-resolute-amd64-595-open")},
		{"cuda-12gb", internalInfo("cuda-12gb"), plan.NodeInfo{Backend: "cuda", Arch: "amd64", Codename: "resolute"}, with("kit-nvidia-resolute-amd64-595-open")},
		{"max-128gb", internalInfo("max-128gb"), plan.NodeInfo{Backend: "vulkan", Arch: "amd64", Codename: "resolute"}, with()},
		{"cpu-only", internalInfo("cpu-only"), plan.NodeInfo{Backend: "cpu", Arch: "amd64", Codename: "resolute"}, with()},
		{"ark-drive-full", driveInfo(), plan.NodeInfo{Backend: "cuda", Arch: "amd64", Codename: "resolute"},
			with("kit-local-ai-repo", "kit-nvidia-resolute-amd64-595-open", "kit-ubuntu-iso-26.04")},
	}
	for _, tc := range cases {
		t.Run(tc.profile, func(t *testing.T) {
			prof, ok := c.Profile(tc.profile)
			if !ok {
				t.Fatalf("no profile %s", tc.profile)
			}
			var named []string
			for _, id := range prof.Include.Content {
				if strings.HasPrefix(id, "kit-") {
					named = append(named, id)
				}
			}
			sort.Strings(named)
			if strings.Join(named, " ") != strings.Join(tc.want, " ") {
				t.Errorf("kit items of %s:\n got %v\nwant %v", tc.profile, named, tc.want)
			}

			// The dry-run plan of an empty root: every kit item is downloaded into its folder.
			p := planFor(t, c, tc.profile, tc.root, tc.node)
			kp := kitRowsOf(p)
			if len(kp.Kit) != len(tc.want) || len(kp.NotForNode) != 0 {
				t.Errorf("planned %d kit rows (filtered %v), want %d", len(kp.Kit), kp.NotForNode, len(tc.want))
			}
			for _, r := range kp.Kit {
				ci := item(t, c, r.ID)
				if want := realKitDirs[r.ID]; r.Dir != want {
					t.Errorf("%s: dir = %q, want %q (rebuild-kit.md section 3.1)", r.ID, r.Dir, want)
				}
				if r.Action != plan.ActionDownload || r.Bytes != ci.ExpectedSizeBytes || ci.ExpectedSizeBytes <= 0 {
					t.Errorf("%s: action %s, %d bytes, catalog says %d", r.ID, r.Action, r.Bytes, ci.ExpectedSizeBytes)
				}
				if ci.Kind != "kit" || ci.Tier() != 1 {
					t.Errorf("%s: kind %s tier %d, want a tier 1 kit item", r.ID, ci.Kind, ci.Tier())
				}
			}
			if len(kp.Findings) != 0 {
				t.Errorf("findings = %v", kp.Findings)
			}
			// Node profiles never carry the drive-only items or the optional CUDA redistributables.
			if tc.profile != "ark-drive-full" {
				for _, id := range named {
					if id == "kit-ubuntu-iso-26.04" || id == "kit-local-ai-repo" {
						t.Errorf("%s includes %s, which is for the ark drive only", tc.profile, id)
					}
				}
			}
		})
	}
}

// Every kit item the real catalog holds is placed by the layout table, and the pinned ones (url items)
// carry a sha256 for every file: nothing in the kit is fetched unverified.
func TestRealCatalogKitItemsArePlacedAndPinned(t *testing.T) {
	c := realCatalog(t)
	seen := 0
	for _, it := range c.Content {
		if it.Kind != "kit" {
			continue
		}
		seen++
		want, known := realKitDirs[it.ID]
		if !known {
			t.Errorf("%s is a kit item this test does not know: add it to realKitDirs with its section 3.1 folder", it.ID)
			continue
		}
		if got, err := kitItemDir(it); err != nil || got != want {
			t.Errorf("%s: layout %q, %v, want %q", it.ID, got, err, want)
		}
		if it.Source.Type == "url" && it.Source.Checksum != "sidecar" {
			files := it.Source.Files
			if len(files) == 0 {
				t.Errorf("%s: a url kit item with no files", it.ID)
			}
			for _, f := range files {
				if len(f.SHA256) != 64 {
					t.Errorf("%s: %s is not pinned", it.ID, f.URL)
				}
			}
		}
	}
	if seen != len(realKitDirs) {
		t.Errorf("the catalog holds %d kit items, the table %d", seen, len(realKitDirs))
	}
}
