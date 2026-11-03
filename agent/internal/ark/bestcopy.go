package ark

import (
	"context"
	"errors"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"

	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/sync/manifest"
)

// ErrNotPresent: no eligible verified copy of the item exists. The caller maps it to its own doctor
// code (LAI-LLM-008 for a model).
var ErrNotPresent = errors.New("ark: item not present")

// Copy is one verified copy of an item on one root, as BestCopy hands it to the router, Kiwix,
// whisper, piper and the maps. It is a snapshot: later swaps do not change a Copy already returned.
type Copy struct {
	// RootID is the root the copy is on ("internal" or "ark-<8 hex>").
	RootID string
	// Dir is the absolute item directory.
	Dir string
	// Files are the absolute paths of the manifest's files, in manifest order. A directory item
	// (docs, map-assets, kit trees) has none: its content is the tree under Dir.
	Files []string
	// Manifest is the item's validated MANIFEST. Treat it as read-only.
	Manifest manifest.Manifest
}

// CopyFinder is what a consumer of content takes (and tests fake): the one question "which file do
// I use for this item". *Index implements it. Nothing else in the agent walks content directories.
type CopyFinder interface {
	BestCopy(itemID string) (Copy, error)
}

// scannable reports whether a root in this state may be read and served from: mounted and not being
// ejected. Ejecting, not mounted, unusable and uninitialized roots are neither scanned nor offered.
func scannable(s RootState) bool {
	return s == StateReady || s == StateReadOnly || s == StateUnclean
}

type entry struct {
	rootID, relDir string
	version        string
	sortKey        string
	copy           Copy
}

// snapshot is immutable once published; writers build a new one and swap the pointer.
type snapshot struct {
	roots map[string]Root
	items map[string][]entry // by item id
}

type markKey struct{ rootID, itemID, version string }

// Index is the in-memory best-copy index (ark-sync.md section 4.2): which verified copies of which
// items exist on which mounted roots. Readers (BestCopy, Copies, ItemIDs) take no lock and never
// touch the disk, so they cannot block on a slow or unplugged drive. Writers (Rebuild, Refresh,
// SetRootState) are serialised and publish a new snapshot atomically, so a reader sees the whole
// old index or the whole new one. Safe for concurrent use.
type Index struct {
	fsys config.FS

	wmu  sync.Mutex // serialises writers (and holds them across the disk scan)
	snap atomic.Pointer[snapshot]

	vmu      sync.RWMutex
	verified map[markKey]struct{} // serve-time verification passed (unclean roots)
}

// NewIndex returns an empty index that reads manifests through fsys. Call Rebuild at start.
func NewIndex(fsys config.FS) *Index {
	ix := &Index{fsys: fsys, verified: map[markKey]struct{}{}}
	ix.snap.Store(&snapshot{roots: map[string]Root{}, items: map[string][]entry{}})
	return ix
}

func (ix *Index) load() *snapshot { return ix.snap.Load() }

// Rebuild rescans the given roots and replaces the whole index, which is what a mount or an unmount
// does. roots is the complete current list; a root missing from it is gone. Roots are read
// concurrently, so a drive that hangs delays nothing but itself; ctx bounds the wait: when it ends
// Rebuild returns its error and the old index stays. A root that is not scannable (see RootState:
// ejecting, not mounted, unusable, uninitialized) is recorded but not read, and offers no copy.
//
// The returned problems are one LAI-SYN-052 per item that was left out (see ScanRoot). Serve-time
// verification marks (MarkServeVerified) survive for copies that are still there and are dropped for
// roots that were unmounted, so a drive that comes back is verified again.
func (ix *Index) Rebuild(ctx context.Context, roots []Root) ([]Problem, error) {
	ix.wmu.Lock()
	defer ix.wmu.Unlock()

	type result struct {
		items    []ScannedItem
		problems []Problem
		err      error
	}
	results := make([]chan result, len(roots))
	for i, r := range roots {
		results[i] = make(chan result, 1)
		if !scannable(r.State) {
			results[i] <- result{}
			continue
		}
		go func(r Root, out chan<- result) {
			items, problems, err := ScanRoot(ctx, ix.fsys, r)
			out <- result{items, problems, err}
		}(r, results[i])
	}

	next := &snapshot{roots: make(map[string]Root, len(roots)), items: map[string][]entry{}}
	var problems []Problem
	for i, r := range roots {
		next.roots[r.ID] = r
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case res := <-results[i]:
			if res.err != nil {
				return nil, res.err
			}
			problems = append(problems, res.problems...)
			for _, it := range res.items {
				next.items[it.Manifest.ItemID] = append(next.items[it.Manifest.ItemID], newEntry(it))
			}
		}
	}
	ix.snap.Store(next)
	ix.pruneMarks(next)
	return problems, nil
}

// Refresh re-reads one item directory of one root after a swap (relDir as ItemDir returns it) and
// updates the index; it is the cheap alternative to Rebuild. root also updates what the index
// knows about that root (state, mount id). A directory without a manifest removes the item's copy on
// that root; an invalid one removes it and reports LAI-SYN-052. A root that is not scannable is
// recorded and not read. The error is the context's.
func (ix *Index) Refresh(ctx context.Context, root Root, relDir string) ([]Problem, error) {
	ix.wmu.Lock()
	defer ix.wmu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cur := ix.load()
	next := &snapshot{roots: make(map[string]Root, len(cur.roots)+1), items: make(map[string][]entry, len(cur.items)+1)}
	for id, r := range cur.roots {
		next.roots[id] = r
	}
	next.roots[root.ID] = root
	for id, es := range cur.items {
		next.items[id] = es
	}
	if !scannable(root.State) {
		ix.snap.Store(next)
		return nil, nil
	}

	// Drop what this root had in that directory, then add what is there now.
	for id, es := range next.items {
		var kept []entry
		for _, e := range es {
			if e.rootID != root.ID || e.relDir != relDir {
				kept = append(kept, e)
			}
		}
		if len(kept) != len(es) {
			if len(kept) == 0 {
				delete(next.items, id)
			} else {
				next.items[id] = kept
			}
		}
	}
	var problems []Problem
	it, err := LoadItem(ix.fsys, root, relDir)
	switch {
	case err == nil:
		id := it.Manifest.ItemID
		next.items[id] = append(append([]entry(nil), next.items[id]...), newEntry(it))
	case isNotExist(err):
	default:
		problems = append(problems, newProblem(CodeManifestInvalid, map[string]any{
			"root_id": root.ID, "dir": relDir, "reason": err.Error(), "invalid": IsInvalidItem(err),
		}))
	}
	ix.snap.Store(next)
	return problems, nil
}

// SetRootState records a root's new state without reading the disk: the instant exclusion of a root
// that starts ejecting, or the return of one that stays after a refused eject. It reports whether
// the root is known. A root that becomes scannable again needs a Rebuild to be read.
func (ix *Index) SetRootState(rootID string, state RootState) bool {
	ix.wmu.Lock()
	defer ix.wmu.Unlock()
	cur := ix.load()
	r, ok := cur.roots[rootID]
	if !ok {
		return false
	}
	r.State = state
	next := &snapshot{roots: make(map[string]Root, len(cur.roots)), items: cur.items}
	for id, old := range cur.roots {
		next.roots[id] = old
	}
	next.roots[rootID] = r
	ix.snap.Store(next)
	return true
}

// MarkServeVerified records that the serve-time verification of one copy passed. On a root in state
// unclean a copy is offered only after this (ark-sync.md section 4.1.3, rule 4 of 4.2); on any other
// root it changes nothing. The mark belongs to that exact version: a swap that installs another
// version needs a new verification. The hashing itself is not this package's job.
func (ix *Index) MarkServeVerified(rootID, itemID, version string) {
	ix.vmu.Lock()
	ix.verified[markKey{rootID, itemID, version}] = struct{}{}
	ix.vmu.Unlock()
}

func (ix *Index) isMarked(e entry) bool {
	ix.vmu.RLock()
	defer ix.vmu.RUnlock()
	_, ok := ix.verified[markKey{e.rootID, e.copy.Manifest.ItemID, e.version}]
	return ok
}

// pruneMarks keeps only the marks of copies the new snapshot still has on a scannable root.
func (ix *Index) pruneMarks(s *snapshot) {
	ix.vmu.Lock()
	defer ix.vmu.Unlock()
	for k := range ix.verified {
		keep := false
		if r, ok := s.roots[k.rootID]; ok && scannable(r.State) {
			for _, e := range s.items[k.itemID] {
				if e.rootID == k.rootID && e.version == k.version {
					keep = true
					break
				}
			}
		}
		if !keep {
			delete(ix.verified, k)
		}
	}
}

func newEntry(it ScannedItem) entry {
	files := make([]string, len(it.Manifest.Files))
	for i, f := range it.Manifest.Files {
		files[i] = filepath.Join(it.Dir, filepath.FromSlash(f.Path))
	}
	return entry{
		rootID: it.RootID, relDir: it.RelDir, version: it.Manifest.Version, sortKey: it.Manifest.SortKey,
		copy: Copy{RootID: it.RootID, Dir: it.Dir, Files: files, Manifest: it.Manifest},
	}
}

// eligible returns the copies of itemID that may be served: on a mounted root that is not ejecting,
// and, on an unclean root, only those whose serve-time verification passed (rules 1 and 4).
func (ix *Index) eligible(s *snapshot, itemID string) []entry {
	var out []entry
	for _, e := range s.items[itemID] {
		r, ok := s.roots[e.rootID]
		if !ok || !scannable(r.State) {
			continue
		}
		if r.State == StateUnclean && !ix.isMarked(e) {
			continue
		}
		out = append(out, e)
	}
	return out
}

// rootLess is the order of roots among copies of the same version: internal first, then drives in
// mount order (mountinfo mount id), the root id breaking a tie so the order is total.
func rootLess(a, b Root) bool {
	if (a.Kind == KindInternal) != (b.Kind == KindInternal) {
		return a.Kind == KindInternal
	}
	if a.MountID != b.MountID {
		return a.MountID < b.MountID
	}
	return a.ID < b.ID
}

// Copies returns every servable copy of the item, best first (the order BestCopy picks from). It is
// for callers that need to see all of them (retention counting other verified copies, a
// local-copy source); consumers of content use BestCopy.
func (ix *Index) Copies(itemID string) []Copy {
	s := ix.load()
	es := ix.eligible(s, itemID)
	if len(es) == 0 {
		return nil
	}
	// Rule 2: the version the internal root records comes first, then the newest sort key.
	internalVersion, hasInternal := "", false
	for _, e := range es {
		if e.rootID == InternalID {
			internalVersion, hasInternal = e.version, true
		}
	}
	sort.SliceStable(es, func(i, j int) bool {
		a, b := es[i], es[j]
		if hasInternal {
			if am, bm := a.version == internalVersion, b.version == internalVersion; am != bm {
				return am
			}
		}
		if a.sortKey != b.sortKey {
			return a.sortKey > b.sortKey
		}
		if a.version != b.version {
			return a.version > b.version
		}
		return rootLess(s.roots[a.rootID], s.roots[b.rootID]) // rule 3
	})
	out := make([]Copy, len(es))
	for i, e := range es {
		out[i] = cloneCopy(e.copy)
	}
	return out
}

// BestCopy returns the copy of the item to use (ark-sync.md section 4.2):
//
//  1. only verified copies on mounted roots that are not ejecting;
//  2. the version recorded in the internal root's MANIFEST first, then the newest sort_key (plain
//     string comparison);
//  3. among copies of one version, the internal root, then drives in mount order;
//  4. on an unclean root, a copy counts only after MarkServeVerified;
//  5. none: ErrNotPresent.
//
// It reads memory only and is safe for concurrent use.
func (ix *Index) BestCopy(itemID string) (Copy, error) {
	if cs := ix.Copies(itemID); len(cs) > 0 {
		return cs[0], nil
	}
	return Copy{}, ErrNotPresent
}

// ItemIDs lists the ids of the items that have at least one servable copy, sorted.
func (ix *Index) ItemIDs() []string {
	s := ix.load()
	var ids []string
	for id := range s.items {
		if len(ix.eligible(s, id)) > 0 {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}

// cloneCopy gives the caller its own slices so that nothing it does can reach the shared snapshot.
func cloneCopy(c Copy) Copy {
	c.Files = append([]string(nil), c.Files...)
	c.Manifest.Files = append([]manifest.File(nil), c.Manifest.Files...)
	c.Manifest.History = append([]manifest.HistoryEntry(nil), c.Manifest.History...)
	return c
}

var _ CopyFinder = (*Index)(nil)
