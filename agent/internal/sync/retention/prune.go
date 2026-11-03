package retention

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"

	"github.com/marinoscar/local-ai/agent/internal/ark"
)

// Kinds of a pruned entry (ark-sync.md section 3.5).
const (
	// PruneKindOrphan: a verified copy of an item the profile no longer wants (R6).
	PruneKindOrphan = "orphan"
	// PruneKindTrash: a superseded version in .trash (R3).
	PruneKindTrash = "trash"
)

// ErrNothingSelected: a prune request that asks for neither orphans nor trash.
var ErrNothingSelected = errors.New("retention: prune needs orphans, trash or both")

// OrphanLister names the items a root holds that the profile does not want: the verified copies the
// planner marks as orphans. The planner owns that question (it knows the catalog, the profile and the
// node), retention only deletes.
type OrphanLister func(ctx context.Context, root ark.Root) ([]string, error)

// PruneRequest is what `aictl ark prune` and POST /ark/prune ask for.
type PruneRequest struct {
	// Roots are root ids of mounted roots. Empty means every writable mounted root (R8); a read-only
	// root that is named has each of its candidates refused with ReasonReadOnly.
	Roots []string
	// Orphans deletes the orphans (R6); Trash deletes every eligible superseded version at once (R3),
	// whatever its age.
	Orphans, Trash bool
	// DryRun deletes nothing: Deleted lists what a real run would delete, judged as that run would
	// judge it (two copies of one item on two roots: the first can go, the second is the last).
	DryRun bool
	// Orphan lists the orphans of a root; required when Orphans is set.
	Orphan OrphanLister
}

// Pruned is one entry that went (or, with DryRun, would go).
type Pruned struct {
	Root, Item, Version string
	Bytes               int64
	// Kind is PruneKindOrphan or PruneKindTrash.
	Kind string
}

// PruneRefusal is one entry that was kept, and why (ReasonLastVerifiedCopy, ReasonReadOnly,
// ReasonReplacementUnverified).
type PruneRefusal struct {
	Root, Item, Version string
	Reason              string
	// Kind is PruneKindOrphan or PruneKindTrash.
	Kind string
}

// PruneResult is the outcome of Prune. Both lists are never nil.
type PruneResult struct {
	Deleted    []Pruned
	Refused    []PruneRefusal
	FreedBytes int64
}

// Prune deletes orphans and superseded versions on request, always through the same rules as every
// other deletion. There is no override: the last verified copy of an item is refused (R1, R6) however
// the request is worded, and a read-only root is never modified (R8). Orphans go first, then trash, so
// an orphan whose only other copy is an old version in .trash can go and the trash entry then stays
// (R3 needs a live copy), instead of both disappearing.
func (r *Retention) Prune(ctx context.Context, req PruneRequest) (PruneResult, error) {
	res := PruneResult{Deleted: []Pruned{}, Refused: []PruneRefusal{}}
	if !req.Orphans && !req.Trash {
		return res, ErrNothingSelected
	}
	if req.Orphans && req.Orphan == nil {
		return res, errors.New("retention: prune of orphans needs the planner's list")
	}
	roots, err := r.pruneRoots(req.Roots)
	if err != nil {
		return res, err
	}
	gone := goneSet{}
	if req.DryRun {
		r.mu.Lock() // one consistent view for the whole simulation
		defer r.mu.Unlock()
	}
	if req.Orphans {
		for _, root := range roots {
			if err := r.pruneOrphans(ctx, root, req, gone, &res); err != nil {
				return res, err
			}
		}
	}
	if req.Trash {
		for _, root := range roots {
			if err := r.pruneTrash(ctx, root, req, gone, &res); err != nil {
				return res, err
			}
		}
	}
	return res, nil
}

// pruneRoots resolves the root ids of a request in the order of the node's roots (internal first).
func (r *Retention) pruneRoots(ids []string) ([]ark.Root, error) {
	var out []ark.Root
	if len(ids) == 0 {
		for _, ro := range r.d.Roots() {
			if ro.Path != "" && !rootReadOnly(ro) && (ro.State == ark.StateReady || ro.State == "") {
				out = append(out, ro)
			}
		}
		return out, nil
	}
	seen := map[string]bool{}
	for _, id := range ids {
		ro, ok := r.root(id)
		if !ok || ro.Path == "" {
			return nil, fmt.Errorf("retention: %s is not a mounted root", id)
		}
		if !seen[ro.ID] {
			seen[ro.ID] = true
			out = append(out, ro)
		}
	}
	return out, nil
}

func (r *Retention) pruneOrphans(ctx context.Context, root ark.Root, req PruneRequest, gone goneSet, res *PruneResult) error {
	ids, err := req.Orphan(ctx, root)
	if err != nil {
		return err
	}
	sort.Strings(ids)
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return err
		}
		var cp ark.Copy
		found := false
		for _, c := range r.d.Copies.Copies(id) {
			if c.RootID == root.ID {
				cp, found = c, true
				break
			}
		}
		if !found {
			continue // not a verified copy on this root any more: nothing to prune
		}
		refuse := func(reason string) {
			res.Refused = append(res.Refused, PruneRefusal{Root: root.ID, Item: id, Version: cp.Manifest.Version, Reason: reason, Kind: PruneKindOrphan})
		}
		if req.DryRun {
			cls := Class{Kind: KindCopy, Root: root, ItemID: id, Version: cp.Manifest.Version, Copy: cp}
			if ok, reason := r.canDeleteWithout(cls, gone); !ok {
				refuse(reason)
				continue
			}
			gone.add(cp)
			bytes := cp.Manifest.TotalBytes
			if bytes == 0 {
				bytes = r.size(cp.Dir, 0)
			}
			res.Deleted = append(res.Deleted, Pruned{Root: root.ID, Item: id, Version: cp.Manifest.Version, Bytes: bytes, Kind: PruneKindOrphan})
			res.FreedBytes += bytes
			continue
		}
		out, err := r.DeleteCopy(ctx, DeleteRequest{RootID: root.ID, ItemID: id, KeepLast: true})
		if err != nil {
			return err
		}
		if !out.Deleted {
			if out.Reason != ReasonNotPresent {
				refuse(out.Reason)
			}
			continue
		}
		res.Deleted = append(res.Deleted, Pruned{Root: root.ID, Item: id, Version: cp.Manifest.Version, Bytes: out.Bytes, Kind: PruneKindOrphan})
		res.FreedBytes += out.Bytes
	}
	return nil
}

func (r *Retention) pruneTrash(ctx context.Context, root ark.Root, req PruneRequest, gone goneSet, res *PruneResult) error {
	entries, err := r.ListTrash(root.ID)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		refuse := func(reason string) {
			res.Refused = append(res.Refused, PruneRefusal{Root: root.ID, Item: e.ItemID, Version: e.Version, Reason: reason, Kind: PruneKindTrash})
		}
		if req.DryRun {
			cls := Class{Kind: KindTrash, Root: root, ItemID: e.ItemID, Version: e.Version}
			if ok, reason := r.canDeleteWithout(cls, gone); !ok {
				refuse(reason)
				continue
			}
		} else if err := r.Remove(e.Path); err != nil {
			var refused *RefusedError
			if errors.As(err, &refused) {
				refuse(refused.Reason)
				continue
			}
			return err
		} else {
			r.d.Logger.Info("retention: trash pruned", "component", "sync", "root", root.ID, "item", e.ItemID, "version", e.Version, "bytes", e.Bytes)
			r.tidyTrashItem(filepath.Dir(e.Path))
		}
		res.Deleted = append(res.Deleted, Pruned{Root: root.ID, Item: e.ItemID, Version: e.Version, Bytes: e.Bytes, Kind: PruneKindTrash})
		res.FreedBytes += e.Bytes
	}
	return nil
}
