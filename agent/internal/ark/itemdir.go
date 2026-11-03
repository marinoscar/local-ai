package ark

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/sync/manifest"
)

// CodeManifestInvalid: an item's MANIFEST is missing a file, damaged, not verified or from a newer
// schema, so the item counts as missing on that root (warning). It is the manifest package's code.
const CodeManifestInvalid = doctor.SYN052

// Engines of models.yaml, as they appear in a model manifest's engine field.
const (
	EngineLlama   = "llama"
	EngineWhisper = "whisper"
	EnginePiper   = "piper"
)

// MapAssetsID is the one maps item that is a fixed directory (maps/assets/) instead of maps/<id>/.
const MapAssetsID = "map-assets"

// ItemRef names an item the way a catalog entry does, which is all ItemDir needs to place it.
type ItemRef struct {
	// Kind is the item kind: manifest.KindModel for models.yaml entries, the ark.yaml kind otherwise.
	Kind string
	// Engine is the models.yaml engine (llama, whisper, piper); only for Kind model.
	Engine string
	// ID is the catalog id.
	ID string
	// Subdir is the kit item's directory below kit/ (rebuild-kit.md section Layout, for example
	// "src" or "debs/resolute-amd64"); only for Kind kit.
	Subdir string
}

// ItemDir returns the item directory of ref relative to a root, slash separated, per the table of
// ark-sync.md section 3.1:
//
//	model, engine llama    models/<id>
//	model, engine whisper  speech/whisper/<id>
//	model, engine piper    speech/piper/<id>
//	kiwix                  kiwix/<id>
//	maps                   maps/<id>   (map-assets: maps/assets)
//	kit                    kit/<subdir>
//	docs                   docs
//
// An id or subdir that could leave the directory, an unknown kind or engine, or a kit item without
// a subdir is an error. Nothing else in the agent decides where an item lives.
func ItemDir(ref ItemRef) (string, error) {
	if !manifest.SafeName(ref.ID) {
		return "", fmt.Errorf("ark: item id %q is not a plain name", ref.ID)
	}
	switch ref.Kind {
	case manifest.KindModel:
		switch ref.Engine {
		case EngineLlama:
			return path.Join("models", ref.ID), nil
		case EngineWhisper:
			return path.Join("speech", "whisper", ref.ID), nil
		case EnginePiper:
			return path.Join("speech", "piper", ref.ID), nil
		}
		return "", fmt.Errorf("ark: model %s has unknown engine %q", ref.ID, ref.Engine)
	case manifest.KindKiwix:
		return path.Join("kiwix", ref.ID), nil
	case manifest.KindMaps:
		if ref.ID == MapAssetsID {
			return "maps/assets", nil
		}
		return path.Join("maps", ref.ID), nil
	case manifest.KindKit:
		if !validSubdir(ref.Subdir) {
			return "", fmt.Errorf("ark: kit item %s needs a subdir below kit/, got %q", ref.ID, ref.Subdir)
		}
		return path.Join("kit", ref.Subdir), nil
	case manifest.KindDocs:
		return "docs", nil
	}
	return "", fmt.Errorf("ark: item %s has unknown kind %q", ref.ID, ref.Kind)
}

// validSubdir: a clean relative slash path with no "." or ".." element.
func validSubdir(s string) bool {
	if s == "" || path.Clean(s) != s || strings.ContainsRune(s, '\\') {
		return false
	}
	for _, el := range strings.Split(s, "/") {
		if !manifest.SafeName(el) {
			return false
		}
	}
	return true
}

// ItemPath is the absolute path of an item's directory on this root.
func (r Root) ItemPath(ref ItemRef) (string, error) {
	rel, err := ItemDir(ref)
	if err != nil {
		return "", err
	}
	return r.Join(filepath.FromSlash(rel))
}

// ScannedItem is one verified item found on a root.
type ScannedItem struct {
	// RootID is the root the item is on.
	RootID string
	// RelDir is the item directory relative to the root, slash separated (see ItemDir).
	RelDir string
	// Dir is the absolute item directory.
	Dir string
	// Manifest is the item's validated MANIFEST.
	Manifest manifest.Manifest
}

// Ref is the manifest's own ItemRef: the item kind, engine and id, and for a kit item the subdir
// taken from RelDir. ItemDir(Ref()) equals RelDir for an item that sits where it belongs.
func (s ScannedItem) Ref() ItemRef {
	ref := ItemRef{Kind: s.Manifest.ItemKind, Engine: s.Manifest.Engine, ID: s.Manifest.ItemID}
	if ref.Kind == manifest.KindKit {
		ref.Subdir = strings.TrimPrefix(s.RelDir, "kit/")
	}
	return ref
}

// ErrMisplaced: a manifest sits in a directory that is not the one its item belongs in (a copied
// directory, a renamed item). The item counts as missing there; it is LAI-SYN-052.
var ErrMisplaced = errors.New("ark: manifest is in the wrong directory for its item")

// LoadItem loads the item directory relDir (slash separated, relative to the root): the manifest is
// read and validated, every listed file must exist with the recorded size (no hashing), and the
// directory must be where ItemDir puts the manifest's item. Errors:
//
//   - errors.Is(err, fs.ErrNotExist): there is no manifest, the item is simply not here (not a
//     problem; a half-swapped directory looks like this on purpose, ark-sync.md section 4.5.1);
//   - manifest.IsInvalid(err) or ErrMisplaced: LAI-SYN-052, the item counts as missing;
//   - anything else is an I/O error of the drive.
func LoadItem(fsys config.FS, root Root, relDir string) (ScannedItem, error) {
	dir, err := root.Join(filepath.FromSlash(relDir))
	if err != nil {
		return ScannedItem{}, err
	}
	m, err := manifest.Load(fsys, dir)
	if err != nil {
		return ScannedItem{}, err
	}
	it := ScannedItem{RootID: root.ID, RelDir: relDir, Dir: dir, Manifest: m}
	want, err := ItemDir(it.Ref())
	if err != nil || want != relDir {
		return ScannedItem{}, fmt.Errorf("%w: %s holds item %s, which belongs in %q", ErrMisplaced, relDir, m.ItemID, want)
	}
	return it, nil
}

// IsInvalidItem reports whether a LoadItem error is a LAI-SYN-052 one (as opposed to "not here" or
// a drive I/O error).
func IsInvalidItem(err error) bool {
	return manifest.IsInvalid(err) || errors.Is(err, ErrMisplaced)
}

// kitScanDepth is how deep below kit/ an item directory may be (kit/debs/<codename>-<arch>/ is 2;
// the third level allows a subdir with one more element). Only the directories above the deepest
// level are listed, so a bare git mirror or a debs pool is never walked.
const kitScanDepth = 3

// ScanRoot loads every item of a root: it looks for MANIFEST.json in exactly the directories that
// hold items (the table of ItemDir: docs, maps/*, models/*, speech/whisper/*, speech/piper/*,
// kiwix/*, and the directories of kit/ down to kitScanDepth) and returns the verified ones, sorted
// by RelDir. It reads manifests and stats files; it never hashes and never writes.
//
// An item with a damaged, unverified, incomplete or misplaced manifest is left out and reported as
// a LAI-SYN-052 Problem (details: root_id, dir, reason); a directory without a manifest is not an
// item and is silently skipped (kit/config/, a half-swapped directory, and kit/MANIFEST.json, the
// kit's own index, which is not an item manifest). A drive I/O error on one directory is also a
// Problem and the scan goes on. The only error returned is the context's: the scan checks it
// between directories, so an unplugged drive that hangs cannot hold a caller that gave up.
func ScanRoot(ctx context.Context, fsys config.FS, root Root) ([]ScannedItem, []Problem, error) {
	var items []ScannedItem
	var problems []Problem
	report := func(rel string, err error) {
		problems = append(problems, newProblem(CodeManifestInvalid, map[string]any{
			"root_id": root.ID, "dir": rel, "reason": err.Error(), "invalid": IsInvalidItem(err),
		}))
	}
	try := func(rel string) {
		it, err := LoadItem(fsys, root, rel)
		switch {
		case err == nil:
			items = append(items, it)
		case errors.Is(err, fs.ErrNotExist):
		default:
			report(rel, err)
		}
	}
	children := func(parent string) []string {
		entries, err := fsys.ReadDir(filepath.Join(root.Path, filepath.FromSlash(parent)))
		if err != nil {
			if !errors.Is(err, fs.ErrNotExist) {
				report(parent, err)
			}
			return nil
		}
		var out []string
		for _, e := range entries {
			if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
				out = append(out, path.Join(parent, e.Name()))
			}
		}
		return out
	}
	step := func() bool { return ctx.Err() == nil }

	try("docs")
	for _, parent := range []string{"models", "speech/whisper", "speech/piper", "kiwix", "maps"} {
		for _, rel := range children(parent) {
			if !step() {
				return nil, nil, ctx.Err()
			}
			try(rel)
		}
	}
	var walkKit func(parent string, depth int)
	walkKit = func(parent string, depth int) {
		for _, rel := range children(parent) {
			if !step() {
				return
			}
			try(rel)
			if depth < kitScanDepth-1 {
				walkKit(rel, depth+1)
			}
		}
	}
	walkKit("kit", 0)
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	sort.Slice(items, func(i, j int) bool { return items[i].RelDir < items[j].RelDir })
	return items, problems, nil
}
