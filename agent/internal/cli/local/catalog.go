package local

import (
	"context"
	"errors"
	"os"

	"github.com/marinoscar/local-ai/agent/internal/catalog"
)

// Where a restored catalog came from.
const (
	// FromKit: <root>/kit/catalog of the internal disk or an ark drive.
	FromKit = "kit"
	// FromPrevious: /etc/localai/catalog.prev, put back after `catalog use --test`.
	FromPrevious = "previous"
)

// CatalogResult says what a catalog swap installed.
type CatalogResult struct {
	// Version is the CATALOG_VERSION now live.
	Version string
	// Previous is the version that was replaced ("" when none, or it was unreadable).
	Previous string
	// From is FromKit or FromPrevious for a restore; "" for use --test.
	From string
	// Dir is the directory the files were copied from.
	Dir string
	// Root is the content root of a kit source; nil otherwise.
	Root *Root
}

// ownedFS is catalog.OSFS that gives every directory and file it creates the catalog's owner and
// modes (root:localai, 0750 and 0640), which catalog.Install does not know about.
type ownedFS struct {
	catalog.OSFS
	uid, gid int
}

func (o ownedFS) MkdirAll(path string, _ os.FileMode) error {
	if err := os.MkdirAll(path, ModeCatDir); err != nil {
		return err
	}
	return o.fix(path, ModeCatDir)
}

func (o ownedFS) WriteFile(name string, data []byte, _ os.FileMode) error {
	if err := o.OSFS.WriteFile(name, data, ModeCatalog); err != nil {
		return err
	}
	return o.fix(name, ModeCatalog)
}

func (o ownedFS) fix(path string, mode os.FileMode) error {
	if err := os.Chmod(path, mode); err != nil {
		return err
	}
	return os.Chown(path, o.uid, o.gid)
}

func (e Env) catalogFS() (ownedFS, error) {
	gid, err := e.LookupGroup(ServiceGroup)
	if err != nil {
		return ownedFS{}, &GroupError{Name: ServiceGroup, Err: err}
	}
	return ownedFS{uid: e.RootUID, gid: gid}, nil
}

func isDir(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}

// Source is one place a restore may copy from.
type Source struct {
	Dir  string
	From string // FromKit or FromPrevious
	Root *Root  // the content root of a kit source; nil for catalog.prev
}

// RestoreSources lists, in the order RestoreCatalog tries them, where a restore would copy from.
// When the live catalog is a draft (TEST marker) and /etc/localai/catalog.prev holds the real one,
// that comes first. Then every kit catalog that exists: the internal root, then the ark drives
// sorted by mount path.
func RestoreSources(ctx context.Context, e Env) []Source {
	var out []Source
	live, prev := e.Paths.CatalogDir(), e.Paths.CatalogPrev()
	if catalog.IsTestCatalog(live) && isDir(prev) && !catalog.IsTestCatalog(prev) {
		out = append(out, Source{Dir: prev, From: FromPrevious})
	}
	for _, r := range FindRoots(ctx, e) {
		r := r
		if isDir(r.KitCatalog()) {
			out = append(out, Source{Dir: r.KitCatalog(), From: FromKit, Root: &r})
		}
	}
	return out
}

// RestoreCatalog is `aictl catalog restore`: it puts a last good catalog back as
// /etc/localai/catalog, atomically (catalog.Install: copy to catalog.new, lint the copy, rename the
// old one to catalog.prev, rename catalog.new into place, fsync). Sources are tried in order
// (RestoreSources) and the first that passes lint is used, so one damaged copy does not stop a
// restore while another is intact. When none passes, the result is *catalog.RejectedError
// (LAI-CAT-002) and /etc/localai/catalog is untouched; when no source exists at all, ErrNoKit.
// Restoring from catalog.prev removes it afterwards, because it was only the draft's backup.
// The caller holds Lock.
func RestoreCatalog(ctx context.Context, e Env) (*CatalogResult, error) {
	fsys, err := e.catalogFS()
	if err != nil {
		return nil, err
	}
	sources := RestoreSources(ctx, e)
	if len(sources) == 0 {
		return nil, ErrNoKit
	}
	var rejected *catalog.RejectedError
	for _, s := range sources {
		if rep := catalog.Lint(s.Dir, catalog.LintOptions{}); !rep.Valid() {
			if rejected == nil {
				rejected = &catalog.RejectedError{Report: rep}
			}
			continue
		}
		res, err := catalog.Install(e.Paths.CatalogDir(), s.Dir, catalog.InstallOptions{FS: fsys})
		if err != nil {
			if res == nil {
				return nil, installErr(e, err)
			}
			// Installed, but the final directory sync failed: the catalog is in place.
		}
		if s.From == FromPrevious {
			// catalog.prev now holds the draft that was just replaced: it was only a backup.
			_ = os.RemoveAll(e.Paths.CatalogPrev())
		}
		return &CatalogResult{Version: res.Version, Previous: res.Previous, From: s.From, Dir: s.Dir, Root: s.Root}, nil
	}
	return nil, rejected
}

// UseTestCatalog is `aictl catalog use <dir> --test`: it installs the draft catalog in src as
// /etc/localai/catalog with a TEST marker, keeping the catalog it replaces as catalog.prev (and, when
// the live catalog already was a draft, keeping the real one that catalog.prev holds). A catalog
// that fails lint is *catalog.RejectedError and nothing is touched. The caller holds Lock and has
// already confirmed.
func UseTestCatalog(e Env, src string) (*CatalogResult, error) {
	fsys, err := e.catalogFS()
	if err != nil {
		return nil, err
	}
	live, prev := e.Paths.CatalogDir(), e.Paths.CatalogPrev()
	keep := catalog.IsTestCatalog(live) && isDir(prev) && !catalog.IsTestCatalog(prev)
	res, err := catalog.Install(live, src, catalog.InstallOptions{FS: fsys, Test: true, KeepPrevious: keep})
	if err != nil && res == nil {
		return nil, installErr(e, err)
	}
	return &CatalogResult{Version: res.Version, Previous: res.Previous, Dir: src}, nil
}

// installErr keeps a validation rejection as it is and turns everything else into a FileError.
func installErr(e Env, err error) error {
	var rej *catalog.RejectedError
	if errors.As(err, &rej) {
		return err
	}
	return fileErr("write", e.Paths.EtcDir, err)
}
