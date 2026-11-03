package kit_test

import (
	"errors"
	"sort"
	"strings"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/catalog"
	"github.com/marinoscar/local-ai/agent/internal/kit"
)

// The fixture catalog is the shape of the real kit items: it must pass the real linter, or every
// other test here would be judging a catalog the agent never loads.
func TestFixtureCatalogPassesLint(t *testing.T) {
	rep := catalog.Lint(fixtureDir(t), catalog.LintOptions{})
	if !rep.Valid() {
		t.Fatalf("the fixture catalog fails lint: %+v", rep.Problems)
	}
}

func TestLayoutOfFixtureItems(t *testing.T) {
	c := fixtureCatalog(t)
	cases := []struct {
		id   string
		dir  string
		part string
		also []string
	}{
		{"kit-catalog", "catalog", kit.PartCatalog, nil},
		{"kit-agent-binaries", "bin", kit.PartBin, []string{kit.PartInstallers}},
		{"kit-agent-source", "src", kit.PartSrc, nil},
		{"kit-llama-cpp", "git/llama.cpp.git", kit.PartGit, []string{kit.PartSrc}},
		{"kit-whisper-cpp", "git/whisper.cpp.git", kit.PartGit, []string{kit.PartSrc}},
		{"kit-local-ai-repo", "git/local-ai.git", kit.PartGit, nil},
		{"kit-go-toolchain", "toolchains", kit.PartToolchains, nil},
		{"kit-kiwix-tools", "tools/kiwix-tools", kit.PartTools, nil},
		{"kit-pmtiles", "tools/pmtiles", kit.PartTools, nil},
		{"kit-piper", "tools/piper", kit.PartTools, nil},
		{"kit-desktop", "desktop", kit.PartDesktop, nil},
		{"kit-debs-resolute-amd64", "debs/resolute-amd64", kit.PartDebs, nil},
		{"kit-debs-resolute-arm64", "debs/resolute-arm64", kit.PartDebs, nil},
		{"kit-nvidia-resolute-amd64-595-open", "nvidia/resolute-amd64-595-open", kit.PartNvidia, nil},
		{"kit-cuda-redist-12.4.1-amd64", "cuda/12.4.1-amd64", kit.PartCuda, nil},
		{"kit-ubuntu-iso-26.04", "iso", kit.PartISO, nil},
	}
	seen := map[string]bool{}
	for _, tc := range cases {
		seen[tc.id] = true
		t.Run(tc.id, func(t *testing.T) {
			it := item(t, c, tc.id)
			l, err := kit.LayoutOf(it)
			if err != nil {
				t.Fatal(err)
			}
			if l.Dir != tc.dir || l.Part != tc.part || strings.Join(l.Also, ",") != strings.Join(tc.also, ",") {
				t.Errorf("layout = %+v, want dir %q part %q also %v", l, tc.dir, tc.part, tc.also)
			}
			if !kit.ValidPart(l.Part) {
				t.Errorf("part %q is not in kit.Parts", l.Part)
			}
			for _, a := range l.Also {
				if !kit.ValidPart(a) {
					t.Errorf("also %q is not in kit.Parts", a)
				}
			}
			dir, err := kit.ItemDir(it)
			if err != nil || dir != "kit/"+tc.dir {
				t.Errorf("ItemDir = %q, %v; want kit/%s", dir, err, tc.dir)
			}
			ref, err := kit.ItemRef(it)
			if err != nil || ref.Kind != "kit" || ref.ID != tc.id || ref.Subdir != tc.dir {
				t.Errorf("ItemRef = %+v, %v", ref, err)
			}
		})
	}
	// Every kit item of the fixture is in the table above: a new item cannot slip in unchecked.
	var all []string
	for _, ci := range contentOf(c) {
		if ci.Kind == "kit" && !seen[ci.ID] {
			all = append(all, ci.ID)
		}
	}
	if len(all) > 0 {
		t.Errorf("kit items with no expected layout in this test: %v", all)
	}
}

func contentOf(c *catalog.Catalog) []catalog.ContentItem {
	return append([]catalog.ContentItem(nil), c.Content...)
}

func TestLayoutOfErrors(t *testing.T) {
	kitItem := func(id string, src catalog.ContentSource) catalog.ContentItem {
		return catalog.ContentItem{ID: id, Kind: "kit", Source: src}
	}
	cases := []struct {
		name string
		it   catalog.ContentItem
		want string
	}{
		{"not a kit item", catalog.ContentItem{ID: "docs-bundle", Kind: "docs", Source: catalog.ContentSource{Type: "repo_docs"}}, "is not kit"},
		{"unknown kit id", kitItem("kit-something", catalog.ContentSource{Type: "url"}), "no place"},
		{"id is not a plain name", kitItem("kit-../x", catalog.ContentSource{Type: "url"}), "plain name"},
		{"git mirror url without a path", kitItem("kit-x", catalog.ContentSource{Type: "git_mirror", URL: "https://h.test"}), "no path"},
		{"git mirror url ends in ..", kitItem("kit-x", catalog.ContentSource{Type: "git_mirror", URL: "https://h.test/a/../.."}), "plain <name>.git"},
		{"git mirror url ends in .git only", kitItem("kit-x", catalog.ContentSource{Type: "git_mirror", URL: "https://h.test/a/.git"}), "plain <name>.git"},
		{"debs codename with a slash", kitItem("kit-debs-a-amd64", catalog.ContentSource{Type: "apt_closure", Codename: "re/solute", Arch: "amd64"}), "codename"},
		{"debs codename with dots", kitItem("kit-debs-a-amd64", catalog.ContentSource{Type: "apt_closure", Codename: "..", Arch: "amd64"}), "codename"},
		{"debs arch unknown", kitItem("kit-debs-resolute-riscv", catalog.ContentSource{Type: "apt_closure", Codename: "resolute", Arch: "riscv"}), "arch"},
		{"nvidia id without a branch", kitItem("kit-nvidia-resolute-amd64-", catalog.ContentSource{Type: "apt_closure", Codename: "resolute", Arch: "amd64"}), "kit-nvidia-<codename>"},
		{"nvidia id disagrees with the source", kitItem("kit-nvidia-noble-amd64-595", catalog.ContentSource{Type: "apt_closure", Codename: "resolute", Arch: "amd64"}), "kit-nvidia-<codename>"},
		{"nvidia id with a slash is not a plain name", kitItem("kit-nvidia-resolute-amd64-5/95", catalog.ContentSource{Type: "apt_closure", Codename: "resolute", Arch: "amd64"}), "plain name"},
		{"nvidia branch with capitals", kitItem("kit-nvidia-resolute-amd64-595-OPEN", catalog.ContentSource{Type: "apt_closure", Codename: "resolute", Arch: "amd64"}), "branch"},
		{"apt closure with another id", kitItem("kit-other", catalog.ContentSource{Type: "apt_closure", Codename: "resolute", Arch: "amd64"}), "no place"},
		{"cuda redist id without a version", kitItem("kit-cuda-redist-", catalog.ContentSource{Type: "url"}), "kit-cuda-redist"},
		{"iso id without a version", kitItem("kit-ubuntu-iso-", catalog.ContentSource{Type: "url"}), "kit-ubuntu-iso"},
		{"fixed id on another source type is still fixed", kitItem("kit-catalog", catalog.ContentSource{Type: "url"}), ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			l, err := kit.LayoutOf(tc.it)
			if tc.want == "" {
				if err != nil || l.Dir != "catalog" {
					t.Fatalf("want catalog layout, got %+v, %v", l, err)
				}
				return
			}
			if !errors.Is(err, kit.ErrNoLayout) {
				t.Fatalf("err = %v (layout %+v), want ErrNoLayout", err, l)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %q, want it to mention %q", err, tc.want)
			}
			if l.Dir != "" {
				t.Errorf("a failed layout returned a dir %q", l.Dir)
			}
			if _, err := kit.ItemDir(tc.it); !errors.Is(err, kit.ErrNoLayout) {
				t.Errorf("ItemDir err = %v, want ErrNoLayout", err)
			}
		})
	}
}

func TestLayoutAlsoIsACopy(t *testing.T) {
	c := fixtureCatalog(t)
	l, _ := kit.LayoutOf(item(t, c, "kit-agent-binaries"))
	l.Also[0] = "mutated"
	l2, _ := kit.LayoutOf(item(t, c, "kit-agent-binaries"))
	if l2.Also[0] != kit.PartInstallers {
		t.Fatalf("the layout table was changed through a returned slice: %v", l2.Also)
	}
}

func TestCheckLayout(t *testing.T) {
	c := fixtureCatalog(t)
	all := contentOf(c)
	if p := kit.CheckLayout(all); len(p) != 0 {
		t.Fatalf("the fixture catalog has layout problems: %+v", p)
	}
	iso := item(t, c, "kit-ubuntu-iso-26.04")
	iso2 := iso
	iso2.ID = "kit-ubuntu-iso-28.04"
	unknown := catalog.ContentItem{ID: "kit-fork-extra", Kind: "kit", Source: catalog.ContentSource{Type: "url"}}
	notKit := catalog.ContentItem{ID: "wiki-x", Kind: "kiwix"}

	cases := []struct {
		name  string
		items []catalog.ContentItem
		want  []string // "<id>: <fragment>"
	}{
		{"two ISO series share iso/", []catalog.ContentItem{iso, iso2}, []string{"kit-ubuntu-iso-28.04: kit/iso is already the directory of kit-ubuntu-iso-26.04"}},
		{"an unknown kit item", []catalog.ContentItem{unknown}, []string{"kit-fork-extra: the layout of rebuild-kit.md"}},
		{"items that are not kit items are ignored", []catalog.ContentItem{notKit}, nil},
		{"problems come in item order", []catalog.ContentItem{unknown, iso, iso2}, []string{"kit-fork-extra: ", "kit-ubuntu-iso-28.04: "}},
		{"no items", nil, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := kit.CheckLayout(tc.items)
			if len(got) != len(tc.want) {
				t.Fatalf("problems = %+v, want %d", got, len(tc.want))
			}
			for i, w := range tc.want {
				line := got[i].ID + ": " + got[i].Reason
				if !strings.HasPrefix(line, strings.SplitN(w, ": ", 2)[0]+": ") || !strings.Contains(line, strings.SplitN(w, ": ", 2)[1]) {
					t.Errorf("problem %d = %q, want %q", i, line, w)
				}
			}
		})
	}
}

func TestPartsAreTheLayoutOfSection31(t *testing.T) {
	want := "catalog installers bin src git toolchains tools desktop debs nvidia cuda prebuilt iso"
	if got := strings.Join(kit.Parts, " "); got != want {
		t.Fatalf("Parts = %q, want %q", got, want)
	}
	for _, p := range kit.Parts {
		if !kit.ValidPart(p) {
			t.Errorf("ValidPart(%q) = false", p)
		}
	}
	for _, bad := range []string{"", "config", "BIN", "bin/", "debs/resolute-amd64", "rebuild", " bin"} {
		if kit.ValidPart(bad) {
			t.Errorf("ValidPart(%q) = true", bad)
		}
	}
}

// Which kit items each profile keeps (the "Profiles" column of rebuild-kit.md section 3.2 and the
// decisions of KIT-01): the catalog items a profile names must be exactly these.
func TestProfilesIncludeTheKitItems(t *testing.T) {
	c := fixtureCatalog(t)
	common := []string{
		"kit-agent-binaries", "kit-agent-source", "kit-catalog", "kit-desktop", "kit-go-toolchain",
		"kit-kiwix-tools", "kit-llama-cpp", "kit-piper", "kit-pmtiles", "kit-whisper-cpp",
	}
	with := func(extra ...string) []string {
		out := append(append([]string(nil), common...), extra...)
		sort.Strings(out)
		return out
	}
	cases := []struct {
		profile string
		want    []string
	}{
		{"small-8gb", with("kit-debs-resolute-amd64", "kit-nvidia-resolute-amd64-595-open")},
		{"cuda-12gb", with("kit-debs-resolute-amd64", "kit-nvidia-resolute-amd64-595-open")},
		{"max-128gb", with("kit-debs-resolute-amd64")},
		{"cpu-only", with("kit-debs-resolute-amd64")},
		{"ark-drive-full", with("kit-debs-resolute-amd64", "kit-debs-resolute-arm64", "kit-local-ai-repo",
			"kit-nvidia-resolute-amd64-595-open", "kit-ubuntu-iso-26.04")},
	}
	for _, tc := range cases {
		t.Run(tc.profile, func(t *testing.T) {
			p, ok := c.Profile(tc.profile)
			if !ok {
				t.Fatalf("no profile %s", tc.profile)
			}
			var got []string
			for _, id := range p.Include.Content {
				if strings.HasPrefix(id, "kit-") {
					got = append(got, id)
				}
			}
			sort.Strings(got)
			if strings.Join(got, " ") != strings.Join(tc.want, " ") {
				t.Errorf("kit items of %s:\n got %v\nwant %v", tc.profile, got, tc.want)
			}
			// Every one of them has a layout: a profile never names a kit item that cannot be placed.
			for _, id := range got {
				if _, err := kit.LayoutOf(item(t, c, id)); err != nil {
					t.Errorf("%s: %v", id, err)
				}
			}
		})
	}
	// The ISO, the lab repository mirror and the optional CUDA redistributables stay off the node
	// profiles: the first two are drive-only, the last is off by default everywhere.
	for _, name := range []string{"small-8gb", "cuda-12gb", "max-128gb", "cpu-only", "ark-drive-full"} {
		p, _ := c.Profile(name)
		for _, id := range p.Include.Content {
			if id == "kit-cuda-redist-12.4.1-amd64" {
				t.Errorf("%s includes the optional CUDA redistributables, which are off by default", name)
			}
			if name != "ark-drive-full" && (id == "kit-ubuntu-iso-26.04" || id == "kit-local-ai-repo") {
				t.Errorf("%s includes %s, which is for the ark drive only", name, id)
			}
		}
	}
}
