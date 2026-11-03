package kit

import (
	"errors"
	"fmt"
	"net/url"
	"path"
	"strings"

	"github.com/marinoscar/local-ai/agent/internal/ark"
	"github.com/marinoscar/local-ai/agent/internal/catalog"
	"github.com/marinoscar/local-ai/agent/internal/sync/manifest"
)

// The top-level parts of <root>/kit/ (rebuild-kit.md section 3.1). They are also the values of
// `aictl kit verify --part`.
const (
	PartCatalog    = "catalog"
	PartInstallers = "installers"
	PartBin        = "bin"
	PartSrc        = "src"
	PartGit        = "git"
	PartToolchains = "toolchains"
	PartTools      = "tools"
	PartDesktop    = "desktop"
	PartDebs       = "debs"
	PartNvidia     = "nvidia"
	PartCuda       = "cuda"
	PartPrebuilt   = "prebuilt"
	PartISO        = "iso"
)

// Parts lists every part in the order of the layout of rebuild-kit.md section 3.1.
var Parts = []string{
	PartCatalog, PartInstallers, PartBin, PartSrc, PartGit, PartToolchains, PartTools,
	PartDesktop, PartDebs, PartNvidia, PartCuda, PartPrebuilt, PartISO,
}

// ValidPart reports whether name is one of Parts.
func ValidPart(name string) bool {
	for _, p := range Parts {
		if p == name {
			return true
		}
	}
	return false
}

// Directory names of the kit itself.
const (
	// DirName is the kit directory of a content root.
	DirName = "kit"
	// ConfigDirName is kit/config/: config backups, not a sync item, ignored by the index.
	ConfigDirName = "config"
	// PinsDir is the directory of pins.json below kit/.
	PinsDir = "catalog"
)

// ErrNoLayout: a kit item the layout table does not know and cannot derive (a kit id of a fork that
// the table has no row for, a source of a type the table does not map, an id or source value that
// could leave its directory). The planner then plans the item without a directory and the downloader
// refuses it; nothing is written outside kit/.
var ErrNoLayout = errors.New("kit: no layout for this kit item")

// Layout says where one kit item lives under <root>/kit/ (rebuild-kit.md section 3.1).
type Layout struct {
	// Dir is the item directory below kit/, slash separated, with no leading slash: the directory
	// that holds the item's MANIFEST.json (ark-sync.md section 3.1, ark.ItemRef.Subdir).
	Dir string
	// Part is the top-level part of Dir.
	Part string
	// Also lists the other parts the item writes files into, outside Dir. They are recorded in the
	// item's own MANIFEST and ignored by the index: kit-agent-binaries puts install-node.sh in
	// installers/, and the git mirrors put the source tarball of each pinned commit in src/.
	Also []string
}

// IsKitItem reports whether the catalog entry is a rebuild-kit item.
func IsKitItem(it catalog.ContentItem) bool { return it.Kind == manifest.KindKit }

// fixed maps the kit items that have one fixed place. The git mirrors, the apt closures, the CUDA
// redistributables and the ISO are derived from their source (LayoutOf), so a fork's second mirror
// or a second codename needs no row here.
var fixed = map[string]Layout{
	"kit-catalog":        {Dir: "catalog", Part: PartCatalog},
	"kit-agent-binaries": {Dir: "bin", Part: PartBin, Also: []string{PartInstallers}},
	"kit-agent-source":   {Dir: "src", Part: PartSrc},
	"kit-go-toolchain":   {Dir: "toolchains", Part: PartToolchains},
	"kit-kiwix-tools":    {Dir: "tools/kiwix-tools", Part: PartTools},
	"kit-pmtiles":        {Dir: "tools/pmtiles", Part: PartTools},
	"kit-piper":          {Dir: "tools/piper", Part: PartTools},
	"kit-desktop":        {Dir: "desktop", Part: PartDesktop},
}

// LayoutOf returns the layout of a kit item:
//
//	kit-catalog                            catalog/
//	kit-agent-binaries                     bin/        (+ installers/install-node.sh)
//	kit-agent-source                       src/
//	git_mirror (kit-llama-cpp, ...)        git/<url basename without .git>.git/   (+ src/ tarballs for also_tarball_of)
//	kit-go-toolchain                       toolchains/
//	kit-kiwix-tools, kit-pmtiles, kit-piper  tools/<tool>/
//	kit-desktop                            desktop/
//	kit-debs-<codename>-<arch>             debs/<codename>-<arch>/
//	kit-nvidia-<codename>-<arch>-<branch>  nvidia/<codename>-<arch>-<branch>/
//	kit-cuda-redist-<ver>-<arch>           cuda/<ver>-<arch>/
//	kit-ubuntu-iso-<ver>                   iso/
//
// An item that is not a kit item, an unknown kit id and any id, codename, arch, branch or mirror
// name that would not be one plain path element is ErrNoLayout.
func LayoutOf(it catalog.ContentItem) (Layout, error) {
	fail := func(format string, args ...any) (Layout, error) {
		return Layout{}, fmt.Errorf("%w: %s: %s", ErrNoLayout, it.ID, fmt.Sprintf(format, args...))
	}
	if !IsKitItem(it) {
		return fail("kind %q is not kit", it.Kind)
	}
	if !manifest.SafeName(it.ID) {
		return fail("the id is not a plain name")
	}
	src := it.Source
	switch {
	case src.Type == "git_mirror":
		name, err := mirrorName(src.URL)
		if err != nil {
			return fail("%v", err)
		}
		l := Layout{Dir: path.Join(PartGit, name), Part: PartGit}
		if src.AlsoTarballOf != "" {
			l.Also = []string{PartSrc}
		}
		return l, nil
	case src.Type == "apt_closure" && strings.HasPrefix(it.ID, "kit-debs-"):
		d, err := codenameArchDir(src.Codename, src.Arch, "")
		if err != nil {
			return fail("%v", err)
		}
		return Layout{Dir: path.Join(PartDebs, d), Part: PartDebs}, nil
	case src.Type == "apt_closure" && strings.HasPrefix(it.ID, "kit-nvidia-"):
		prefix := "kit-nvidia-" + src.Codename + "-" + src.Arch + "-"
		if !strings.HasPrefix(it.ID, prefix) || len(it.ID) == len(prefix) {
			return fail("the id must be kit-nvidia-<codename>-<arch>-<branch>")
		}
		d, err := codenameArchDir(src.Codename, src.Arch, strings.TrimPrefix(it.ID, prefix))
		if err != nil {
			return fail("%v", err)
		}
		return Layout{Dir: path.Join(PartNvidia, d), Part: PartNvidia}, nil
	case strings.HasPrefix(it.ID, "kit-cuda-redist-") && src.Type == "url":
		rest := strings.TrimPrefix(it.ID, "kit-cuda-redist-")
		if rest == "" || !manifest.SafeName(rest) {
			return fail("the id must be kit-cuda-redist-<ver>-<arch>")
		}
		return Layout{Dir: path.Join(PartCuda, rest), Part: PartCuda}, nil
	case strings.HasPrefix(it.ID, "kit-ubuntu-iso-") && src.Type == "url":
		if it.ID == "kit-ubuntu-iso-" {
			return fail("the id must be kit-ubuntu-iso-<ver>")
		}
		return Layout{Dir: PartISO, Part: PartISO}, nil
	}
	if l, ok := fixed[it.ID]; ok {
		l.Also = append([]string(nil), l.Also...)
		return l, nil
	}
	return fail("the layout of rebuild-kit.md section 3.1 has no place for it")
}

// ItemRef is the ark.ItemRef of a kit item, so ark.ItemDir places it at kit/<Dir>.
func ItemRef(it catalog.ContentItem) (ark.ItemRef, error) {
	l, err := LayoutOf(it)
	if err != nil {
		return ark.ItemRef{}, err
	}
	return ark.ItemRef{Kind: manifest.KindKit, ID: it.ID, Subdir: l.Dir}, nil
}

// ItemDir is the item directory relative to the content root (kit/<Dir>), slash separated.
func ItemDir(it catalog.ContentItem) (string, error) {
	ref, err := ItemRef(it)
	if err != nil {
		return "", err
	}
	return ark.ItemDir(ref)
}

// mirrorName is the directory name of a git mirror: the last element of the URL path, which must
// end in .git (ark-sync.md section 4.9.6: kit/git/<name>.git where name is the basename without .git).
func mirrorName(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Path == "" {
		return "", fmt.Errorf("git mirror url %q has no path", trunc(raw))
	}
	base := path.Base(strings.TrimRight(u.Path, "/"))
	name := strings.TrimSuffix(base, ".git")
	if name == "" || name == "." || name == ".." || !manifest.SafeName(name) {
		return "", fmt.Errorf("git mirror url %q does not end in a plain <name>.git", trunc(raw))
	}
	return name + ".git", nil
}

func codenameArchDir(codename, arch, branch string) (string, error) {
	if !codenameRe.MatchString(codename) {
		return "", fmt.Errorf("codename %q is not an Ubuntu codename", trunc(codename))
	}
	ok := false
	for _, a := range arches {
		ok = ok || a == arch
	}
	if !ok {
		return "", fmt.Errorf("arch %q is not amd64 or arm64", trunc(arch))
	}
	d := codename + "-" + arch
	if branch != "" {
		if !branchRe.MatchString(branch) {
			return "", fmt.Errorf("branch %q is not a driver branch", trunc(branch))
		}
		d += "-" + branch
	}
	return d, nil
}

// LayoutProblem is one thing wrong with the kit items of a catalog as a set.
type LayoutProblem struct {
	// ID is the item the problem is about.
	ID     string
	Reason string
}

// CheckLayout checks the kit items of a catalog as a set: every one has a layout, and no two share
// an item directory (one MANIFEST per directory). Items that are not kit items are ignored. The
// problems come in item order. A catalog with no problem installs cleanly into the layout.
func CheckLayout(items []catalog.ContentItem) []LayoutProblem {
	var problems []LayoutProblem
	owner := map[string]string{}
	for _, it := range items {
		if !IsKitItem(it) {
			continue
		}
		l, err := LayoutOf(it)
		if err != nil {
			problems = append(problems, LayoutProblem{ID: it.ID, Reason: strings.TrimPrefix(err.Error(), ErrNoLayout.Error()+": "+it.ID+": ")})
			continue
		}
		if first, dup := owner[l.Dir]; dup {
			problems = append(problems, LayoutProblem{ID: it.ID, Reason: fmt.Sprintf("kit/%s is already the directory of %s", l.Dir, first)})
			continue
		}
		owner[l.Dir] = it.ID
	}
	return problems
}
