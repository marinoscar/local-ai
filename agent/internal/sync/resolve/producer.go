package resolve

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"

	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/sync/download"
	"github.com/marinoscar/local-ai/agent/internal/sync/manifest"
	"github.com/marinoscar/local-ai/agent/internal/sync/plan"
)

// osFS is the real filesystem of the manifest helpers.
var osFS config.FS = config.OSFS{}

// Producer is the fetch half of a resolver: it materialises one catalog item into the staging
// directory of its root. The sync engine calls it while the item is "downloading" and swaps what it
// returns. Produce is safe to call again after a crash: it overwrites its own leftovers in staging.
type Producer interface {
	Produce(ctx context.Context, req Request) (Product, error)
}

// Request is what the engine hands a producer.
type Request struct {
	// Item is the catalog item.
	Item plan.CatalogItem
	// RootID and Root name the content root: its id ("internal", "ark-<8 hex>") and absolute path.
	RootID, Root string
	// Drive reports an ark drive root: it keeps the sources of every pinned commit (rebuild-kit.md
	// section 3.3), an internal root only its own node's.
	Drive bool
	// Version is the target version the planner resolved; the staging directory is named for it.
	Version string
	// CatalogVersion is the version of the catalog the plan was made with (CATALOG_VERSION).
	CatalogVersion string
	// PresentDir is the absolute directory of the present verified copy of the item on this root
	// ("" when there is none) and PresentVersion its version. apt_closure hard-links unchanged debs
	// from it and keeps the item when the version is unchanged.
	PresentDir, PresentVersion string
	// ItemDir is where the item lives below the root (ark.ItemDir, slash separated), for the kinds
	// whose directory depends on the catalog id (kit-debs and kit-nvidia). "" derives it from the id.
	ItemDir string
	// OtherRoots are the absolute paths of the other mounted content roots, where repo_docs looks for
	// a local-ai.git mirror to build the docs from offline.
	OtherRoots []string
	// Checkpoints persists the resume points of the downloads (the journal's); nil keeps them in
	// memory.
	Checkpoints download.Checkpointer
	// MinFreeBytes is the root's min_free_bytes, passed to the downloader.
	MinFreeBytes int64
	// Progress, when set, is told after each file of a multi-file item (debs): the file name, the
	// number done and the total.
	Progress func(file string, done, total int)
}

// StagingDir is <Root>/.staging/<item>/<version>, where the downloader and the producers write.
func (r Request) StagingDir() string {
	return filepath.Join(r.Root, ".staging", r.Item.ID, r.Version)
}

// PartKind says how the engine installs a Part.
type PartKind string

// The kinds of Part.
const (
	// PartTree is a directory: swap Staging/From into Root/To (RENAME_EXCHANGE when To exists). Its
	// SHA256SUMS is already written inside it; Sums and TreeSHA256 are what its manifest records.
	PartTree PartKind = "tree"
	// PartFile is one file or a set of files: rename each Staging/From/<file> into Root/To/<file>.
	// Files lists them with their sha256.
	PartFile PartKind = "files"
	// PartInPlace was already updated where it lives (a git mirror fetch); nothing is swapped, the
	// manifest is written after it (the commit point).
	PartInPlace PartKind = "in_place"
)

// Part is one installable piece of an item. Most items have one; git_mirror has the mirror and the
// tarballs, repo_docs the docs tree and the kit catalog.
type Part struct {
	// Name is a short label: "mirror", "tarballs", "docs", "catalog", "debs".
	Name string
	Kind PartKind
	// From is the slash path inside the staging directory ("" for PartInPlace) and To the slash path
	// inside the root.
	From, To string
	// Files are the files of a PartFile, paths relative to From (and To).
	Files []manifest.File
	// Sums and TreeSHA256 describe a PartTree (see manifest.TreeSHA256).
	Sums       []manifest.Sum
	TreeSHA256 string
	// Bytes is the total size of the part.
	Bytes int64
}

// Notice is a condition the item succeeded with: READY shows it (LAI-SYN-062, LAI-CLI-014).
type Notice struct {
	Code    string
	Message string
}

// Product is what a producer made.
type Product struct {
	// Version and SortKey are what the manifest records (the producer's own, after any refinement:
	// repo_docs adds "+inv" when real values were rendered).
	Version, SortKey string
	// Keep: the present version is already current, nothing was staged (apt_closure).
	Keep bool
	// Parts are the installable pieces, in install order.
	Parts []Part
	// Trust is how the sha256 values were established (manifest.TrustComputed for git and docs,
	// manifest.TrustSignedIndex for apt).
	Trust manifest.Trust
	// Source holds extra manifest.source fields (the url, the commit, the codename...), never a secret.
	Source map[string]any
	// Notices are yellow conditions that do not fail the item.
	Notices []Notice
}

// hashFile returns the lower-case hex sha256 and the size of a regular file.
func hashFile(p string) (sum string, size int64, err error) {
	f, err := os.Open(p)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// treeSums walks dir and returns the sha256 of every regular file (SHA256SUMS and MANIFEST.json
// excluded) with the total size. Anything but a regular file or a directory (a symlink, a device) is
// an error: a tree the engine swaps must contain nothing else.
func treeSums(dir string) ([]manifest.Sum, int64, error) {
	var sums []manifest.Sum
	var total int64
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("%s is not a regular file", p)
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if rel == manifest.SumsName || rel == manifest.FileName {
			return nil
		}
		sum, n, err := hashFile(p)
		if err != nil {
			return err
		}
		sums = append(sums, manifest.Sum{SHA256: sum, Path: rel})
		total += n
		return nil
	})
	if err != nil {
		return nil, 0, err
	}
	sort.Slice(sums, func(i, j int) bool { return sums[i].Path < sums[j].Path })
	return sums, total, nil
}

// sealTree writes the SHA256SUMS of a staged directory and returns the PartTree for it: the sums are
// written before the manifest, which the engine writes at the commit point.
func sealTree(name, dir, from, to string) (Part, error) {
	sums, total, err := treeSums(dir)
	if err != nil {
		return Part{}, err
	}
	if len(sums) == 0 {
		return Part{}, fmt.Errorf("%s is empty", name)
	}
	if err := manifest.WriteSums(osFS, dir, sums); err != nil {
		return Part{}, err
	}
	tree, err := manifest.TreeSHA256(sums)
	if err != nil {
		return Part{}, err
	}
	return Part{Name: name, Kind: PartTree, From: from, To: to, Sums: sums, TreeSHA256: tree, Bytes: total}, nil
}

// writeFileAtomic writes data to p (0644) through a temporary file in the same directory.
func writeFileAtomic(p string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), "."+filepath.Base(p)+".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(name, 0o644); err != nil {
		return err
	}
	return os.Rename(name, p)
}

// inside reports whether the slash path p stays inside its base (no "..", not absolute).
func inside(p string) bool {
	return p != "" && fs.ValidPath(p) && path.Clean(p) == p && p != "."
}

// sha256Hex is the lower-case hex sha256 of b.
func sha256Hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
