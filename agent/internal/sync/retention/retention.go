// Package retention is the one place in ark sync that deletes content (docs/specs/ark-sync.md
// section 4.10, AGENTS.md rule 3: sync never deletes the last verified copy of anything).
//
// Every deleting code path of internal/sync and internal/ark goes through Retention.CanDelete, and
// every deletion through Retention.Remove, which asks CanDelete first and checks the rules against
// the LIVE state at the moment of the deletion (the best-copy index and the journal as they are
// now), never against a plan made earlier. A static test (TestOnlyRetentionDeletes) fails the build
// when any other file of internal/sync or internal/ark calls os.Remove, os.RemoveAll or an unlink.
//
// The rules this package enforces (the numbering is the spec's):
//
//   - R1: a file of a verified copy may be deleted only when, afterwards, another verified copy of
//     the same item (any version, on a mounted root that is not ejecting, or an intact verified
//     entry of .trash) exists and has been verified or re-verified within the freshness window
//     (24 h);
//   - R3: a trash entry (a superseded version) may be purged when the item has a verified live copy
//     and the journal does not say the item is mid-swap (a swapped but not yet verified replacement
//     does not count);
//   - R4: Restore (restore.go) puts a verified older version of .trash back when the live copy rotted;
//   - R5: staging leftovers and quarantine are not copies and may always go, and go first when space
//     is needed;
//   - R6: Prune (prune.go) deletes orphans and trash on request, and the last verified copy never;
//   - R8: a read-only root is never modified;
//   - R9: a path that is not inside a verified copy, the trash, the staging or the quarantine is not
//     an item and is never deleted.
//
// Nothing here deletes without asking CanDelete first, and no code outside this package deletes at all.
package retention

import (
	"context"
	"errors"
	"fmt"
	"github.com/marinoscar/local-ai/agent/internal/fsperm"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/ark"
	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/sync/journal"
	"github.com/marinoscar/local-ai/agent/internal/sync/manifest"
)

// Defaults of the settings that belong to retention (node.yaml sync.*).
const (
	// DefaultFreshness is how recent the verification of the surviving copy must be for R1 (24 h).
	DefaultFreshness = 24 * time.Hour
	// DefaultTrashDays is how long a superseded version is kept (sync.trash_days).
	DefaultTrashDays = 14
	// DefaultQuarantineDays is how long quarantined files are kept (sync.quarantine_days).
	DefaultQuarantineDays = 7

	maxWalkDepth = 12
)

// Reasons of a refusal (the reason of CanDelete, and the Kept reason the models route reports).
const (
	// ReasonLastVerifiedCopy: deleting would leave no fresh, verified copy of the item (R1).
	ReasonLastVerifiedCopy = "last_verified_copy"
	// ReasonReplacementUnverified: the live copy that superseded a trash entry is not verified yet (R3).
	ReasonReplacementUnverified = "replacement_unverified"
	// ReasonNotAnItem: the path is not a verified copy, trash, staging or quarantine (R9).
	ReasonNotAnItem = "not_an_item"
	// ReasonReadOnly: the root is read-only; nothing on it is ever modified (R8).
	ReasonReadOnly = "read_only"
	// ReasonNotPresent: the root holds no copy of the item.
	ReasonNotPresent = "not_present"
	// ReasonOK: allowed.
	ReasonOK = "ok"
)

// CodePurged is the information code of a deletion made to free room (LAI-SYN-043).
const CodePurged = doctor.SYN043

// Kind is what a path is, as far as retention is concerned.
type Kind string

// The kinds.
const (
	KindCopy       Kind = "copy"
	KindTrash      Kind = "trash"
	KindStaging    Kind = "staging"
	KindQuarantine Kind = "quarantine"
	KindUnknown    Kind = "unknown"
)

// RefusedError is returned by Remove for a path CanDelete refused.
type RefusedError struct {
	Path   string
	Reason string
}

func (e *RefusedError) Error() string {
	return fmt.Sprintf("retention: refused to delete %s: %s", e.Path, e.Reason)
}

// FS is the filesystem of retention. It is the only seam in internal/sync with a RemoveAll, and the
// OSFS below is the only place that calls os.RemoveAll.
type FS interface {
	config.FS
	// RemoveAll deletes a path and everything below it; a missing path is not an error.
	RemoveAll(name string) error
	// MkdirAll creates a directory and its parents (Restore and the quarantine it writes).
	MkdirAll(path string, perm os.FileMode) error
	// OpenRead opens a file for a streamed read (Restore hashes what it puts back).
	OpenRead(name string) (io.ReadCloser, error)
}

// OSFS is the real filesystem.
type OSFS struct{ config.OSFS }

// RemoveAll implements FS.
func (OSFS) RemoveAll(name string) error { return os.RemoveAll(name) }

// MkdirAll implements FS.
func (OSFS) MkdirAll(path string, perm os.FileMode) error { return fsperm.MkdirAll(path, perm) }

// OpenRead implements FS.
func (OSFS) OpenRead(name string) (io.ReadCloser, error) { return os.Open(name) }

// CopyLister is the part of *ark.Index retention reads (always the live index).
type CopyLister interface {
	ItemIDs() []string
	Copies(itemID string) []ark.Copy
}

// JournalView is the part of *journal.Items retention reads.
type JournalView interface {
	Get(rootID, itemID string) (journal.Item, bool)
}

// Refresher re-reads one item directory in the best-copy index after content changed (*ark.Index).
type Refresher interface {
	Refresh(ctx context.Context, root ark.Root, relDir string) ([]ark.Problem, error)
}

// Deps are what a Retention needs. FS, Roots, Copies and Journal are required.
type Deps struct {
	FS      FS
	Roots   func() []ark.Root
	Copies  CopyLister
	Journal JournalView
	// Index is refreshed after a copy is deleted; nil skips the refresh.
	Index Refresher
	// Items drops the journal record of a deleted copy; nil keeps it.
	Items interface {
		Delete(rootID, itemID string) error
	}
	// Now is the clock; time.Now when nil.
	Now func() time.Time
	// Freshness is the R1 window; DefaultFreshness when zero.
	Freshness time.Duration
	Logger    *slog.Logger
}

// Retention enforces the retention rules. Safe for concurrent use.
type Retention struct {
	d  Deps
	mu sync.Mutex // one deletion at a time: R1 is evaluated and acted on atomically
}

// New checks the dependencies and returns a Retention.
func New(d Deps) (*Retention, error) {
	var missing []string
	if d.FS == nil {
		missing = append(missing, "FS")
	}
	if d.Roots == nil {
		missing = append(missing, "Roots")
	}
	if d.Copies == nil {
		missing = append(missing, "Copies")
	}
	if d.Journal == nil {
		missing = append(missing, "Journal")
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("retention: needs %s", strings.Join(missing, ", "))
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Freshness <= 0 {
		d.Freshness = DefaultFreshness
	}
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	return &Retention{d: d}, nil
}

// Class is the classification of a path.
type Class struct {
	Kind    Kind
	Root    ark.Root
	ItemID  string
	Version string
	// Copy is the verified copy a KindCopy path belongs to.
	Copy ark.Copy
}

// Classify says what path is. A path outside every root, or inside one but not in a copy, the trash,
// the staging or the quarantine, is KindUnknown (R9).
func (r *Retention) Classify(path string) Class {
	path = filepath.Clean(path)
	var root ark.Root
	var rel string
	found := false
	for _, ro := range r.d.Roots() {
		if ro.Path == "" {
			continue
		}
		base := filepath.Clean(ro.Path)
		if path == base {
			return Class{Kind: KindUnknown, Root: ro}
		}
		if strings.HasPrefix(path, base+string(filepath.Separator)) {
			root, rel, found = ro, strings.TrimPrefix(path, base+string(filepath.Separator)), true
			break
		}
	}
	if !found {
		return Class{Kind: KindUnknown}
	}
	parts := strings.Split(rel, string(filepath.Separator))
	switch parts[0] {
	case ".staging":
		if len(parts) == 1 {
			return Class{Kind: KindUnknown, Root: root}
		}
		if parts[1] == "quarantine" {
			c := Class{Kind: KindQuarantine, Root: root}
			if len(parts) > 2 {
				c.ItemID = parts[2]
			}
			return c
		}
		c := Class{Kind: KindStaging, Root: root, ItemID: parts[1]}
		if len(parts) > 2 {
			c.Version = parts[2]
		}
		return c
	case ".trash":
		if len(parts) == 1 {
			return Class{Kind: KindUnknown, Root: root}
		}
		c := Class{Kind: KindTrash, Root: root, ItemID: parts[1]}
		if len(parts) > 2 {
			c.Version = parts[2]
		}
		return c
	}
	for _, id := range r.d.Copies.ItemIDs() {
		for _, cp := range r.d.Copies.Copies(id) {
			if cp.RootID != root.ID {
				continue
			}
			dir := filepath.Clean(cp.Dir)
			if path == dir || strings.HasPrefix(path, dir+string(filepath.Separator)) {
				return Class{Kind: KindCopy, Root: root, ItemID: id, Version: cp.Manifest.Version, Copy: cp}
			}
		}
	}
	return Class{Kind: KindUnknown, Root: root}
}

// CanDelete is rule R1 (and R3, R5, R9) for one path, evaluated now against the live index and
// journal. It returns whether the path may be deleted and the reason (ReasonOK when allowed).
func (r *Retention) CanDelete(path string) (bool, string) {
	return r.canDelete(r.Classify(path))
}

func (r *Retention) canDelete(c Class) (bool, string) { return r.canDeleteWithout(c, nil) }

// goneSet is a set of copies a dry run treats as already deleted, so that the second of two copies of
// one item is judged the way the real run will judge it (the first one is gone by then).
type goneSet map[string]bool

func goneKey(rootID, dir string) string { return rootID + "|" + filepath.Clean(dir) }

func (g goneSet) add(cp ark.Copy)      { g[goneKey(cp.RootID, cp.Dir)] = true }
func (g goneSet) has(cp ark.Copy) bool { return g != nil && g[goneKey(cp.RootID, cp.Dir)] }

// rootReadOnly is rule R8's test: the root is read-only (the flag, a read-only mount, or an unclean
// shutdown that the drive layer treats the same way).
func rootReadOnly(ro ark.Root) bool {
	return ro.ReadOnly || ro.State == ark.StateReadOnly || ro.State == ark.StateUnclean
}

// canDeleteWithout is CanDelete with the copies of gone counted as already deleted.
func (r *Retention) canDeleteWithout(c Class, gone goneSet) (bool, string) {
	switch c.Kind {
	case KindStaging, KindQuarantine, KindTrash, KindCopy:
	default:
		return false, ReasonNotAnItem
	}
	if rootReadOnly(c.Root) {
		return false, ReasonReadOnly // R8
	}
	switch c.Kind {
	case KindTrash:
		if c.Version == "" && c.ItemID == "" {
			return false, ReasonNotAnItem
		}
		if rec, ok := r.d.Journal.Get(c.Root.ID, c.ItemID); ok {
			switch rec.State {
			case journal.StateSwapping, journal.StateSwapped:
				// the replacement is not verified after its swap yet: the trash is the only copy of the
				// previous version
				return false, ReasonReplacementUnverified
			}
		}
		live := 0
		for _, cp := range r.d.Copies.Copies(c.ItemID) {
			if !gone.has(cp) {
				live++
			}
		}
		if live == 0 {
			return false, ReasonReplacementUnverified
		}
		return true, ReasonOK
	case KindCopy:
		if r.otherFreshCopy(c, gone) {
			return true, ReasonOK
		}
		return false, ReasonLastVerifiedCopy
	}
	return true, ReasonOK // staging and quarantine: not copies (R5)
}

// otherFreshCopy: after deleting c's copy, does another verified copy of the item exist that was
// verified within the freshness window? Other roots count only while they are mounted and not
// ejecting (the index holds exactly those); an intact verified trash entry counts too.
func (r *Retention) otherFreshCopy(c Class, gone goneSet) bool {
	now := r.d.Now()
	fresh := func(m manifest.Manifest) bool {
		at := m.LastReverifiedAt
		if m.VerifiedAt.After(at) {
			at = m.VerifiedAt
		}
		return !at.After(now) && now.Sub(at) <= r.d.Freshness
	}
	for _, cp := range r.d.Copies.Copies(c.ItemID) {
		if cp.RootID == c.Copy.RootID && filepath.Clean(cp.Dir) == filepath.Clean(c.Copy.Dir) {
			continue
		}
		if gone.has(cp) {
			continue
		}
		if fresh(cp.Manifest) {
			return true
		}
	}
	for _, ro := range r.d.Roots() {
		if ro.State == ark.StateEjecting || ro.Path == "" {
			continue
		}
		dir := filepath.Join(ro.Path, ".trash", c.ItemID)
		vers, err := r.d.FS.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, v := range vers {
			m, err := manifest.Load(r.d.FS, filepath.Join(dir, v.Name()))
			if err == nil && fresh(m) {
				return true
			}
		}
	}
	return false
}

// CanDeleteItem is CanDelete for the verified copy of an item on one root (rule R7: the model delete
// route asks it before it deletes, and DeleteCopy asks it again at the moment of the deletion). A root
// that holds no copy answers ReasonNotPresent.
func (r *Retention) CanDeleteItem(rootID, itemID string) (bool, string) {
	root, ok := r.root(rootID)
	if !ok {
		return false, ReasonNotPresent
	}
	for _, cp := range r.d.Copies.Copies(itemID) {
		if cp.RootID == rootID {
			return r.canDelete(Class{Kind: KindCopy, Root: root, ItemID: itemID, Version: cp.Manifest.Version, Copy: cp})
		}
	}
	return false, ReasonNotPresent
}

// Remove deletes path (a file or a tree) when CanDelete allows it, and returns *RefusedError when
// it does not. A path that does not exist is not an error. The rule is evaluated and the deletion
// done under one lock, so two removals cannot each see the other's copy as the survivor.
func (r *Retention) Remove(path string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.removeLocked(path)
}

func (r *Retention) removeLocked(path string) error {
	if ok, reason := r.CanDelete(path); !ok {
		return &RefusedError{Path: path, Reason: reason}
	}
	return r.d.FS.RemoveAll(path)
}

// RemoveStaging deletes a staging path (.staging/<item>[/<version>[/...]]) of a root. It is the call
// the item state machine makes after a swap or a permanent failure. Anything that is not staging is
// refused, so a bug elsewhere cannot turn it into a general delete.
func (r *Retention) RemoveStaging(path string) error {
	c := r.Classify(path)
	if c.Kind != KindStaging && c.Kind != KindQuarantine {
		return &RefusedError{Path: path, Reason: ReasonNotAnItem}
	}
	if err := r.Remove(path); err != nil {
		return err
	}
	// Tidy: .staging/<item> that holds nothing any more is removed too (an empty directory only).
	if c.Kind == KindStaging && c.Version != "" {
		r.tidyDir(filepath.Join(c.Root.Path, ".staging", c.ItemID))
	}
	return nil
}

// TrashEntry is one superseded version in .trash.
type TrashEntry struct {
	RootID  string
	ItemID  string
	Version string
	Path    string
	Bytes   int64
	// At is when the entry was last written (the swap that created it).
	At time.Time
}

// ListTrash returns the trash entries of a root, oldest first.
func (r *Retention) ListTrash(rootID string) ([]TrashEntry, error) {
	root, ok := r.root(rootID)
	if !ok {
		return nil, fmt.Errorf("retention: %s is not a mounted root", rootID)
	}
	base := filepath.Join(root.Path, ".trash")
	ids, err := r.d.FS.ReadDir(base)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var out []TrashEntry
	for _, id := range ids {
		if !id.IsDir() || !manifest.SafeName(id.Name()) {
			continue
		}
		vers, err := r.d.FS.ReadDir(filepath.Join(base, id.Name()))
		if err != nil {
			continue
		}
		for _, v := range vers {
			if !v.IsDir() || !manifest.SafeName(v.Name()) {
				continue
			}
			p := filepath.Join(base, id.Name(), v.Name())
			e := TrashEntry{RootID: root.ID, ItemID: id.Name(), Version: v.Name(), Path: p}
			if info, err := v.Info(); err == nil {
				e.At = info.ModTime()
			}
			e.Bytes = r.size(p, 0)
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].At.Equal(out[j].At) {
			return out[i].At.Before(out[j].At)
		}
		return out[i].Path < out[j].Path
	})
	return out, nil
}

func (r *Retention) root(id string) (ark.Root, bool) {
	for _, ro := range r.d.Roots() {
		if ro.ID == id {
			return ro, true
		}
	}
	return ark.Root{}, false
}

func (r *Retention) size(dir string, depth int) int64 {
	if depth > maxWalkDepth {
		return 0
	}
	ents, err := r.d.FS.ReadDir(dir)
	if err != nil {
		return 0
	}
	var n int64
	for _, e := range ents {
		switch {
		case e.IsDir():
			n += r.size(filepath.Join(dir, e.Name()), depth+1)
		case e.Type().IsRegular():
			if info, err := e.Info(); err == nil {
				n += info.Size()
			}
		}
	}
	return n
}

// ListQuarantine returns the quarantine entries of a root (.staging/quarantine/<item>/<stamp>), oldest
// first. The Version of an entry is its stamp directory name.
func (r *Retention) ListQuarantine(rootID string) ([]TrashEntry, error) {
	root, ok := r.root(rootID)
	if !ok {
		return nil, fmt.Errorf("retention: %s is not a mounted root", rootID)
	}
	base := filepath.Join(root.Path, ".staging", "quarantine")
	ids, err := r.d.FS.ReadDir(base)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var out []TrashEntry
	for _, id := range ids {
		if !id.IsDir() || !manifest.SafeName(id.Name()) {
			continue
		}
		stamps, err := r.d.FS.ReadDir(filepath.Join(base, id.Name()))
		if err != nil {
			continue
		}
		for _, st := range stamps {
			if !st.IsDir() {
				continue
			}
			p := filepath.Join(base, id.Name(), st.Name())
			e := TrashEntry{RootID: root.ID, ItemID: id.Name(), Version: st.Name(), Path: p, Bytes: r.size(p, 0)}
			if at, ok := quarantineTime(st.Name()); ok {
				e.At = at
			} else if info, err := st.Info(); err == nil {
				e.At = info.ModTime()
			}
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].At.Equal(out[j].At) {
			return out[i].At.Before(out[j].At)
		}
		return out[i].Path < out[j].Path
	})
	return out, nil
}

// quarantineStampLayout is the layout of a quarantine directory name; a second quarantine in the same
// second carries a "-<n>" suffix (the item machine picks a free name).
const quarantineStampLayout = "20060102T150405Z"

// quarantineTime reads the time out of a quarantine directory name, with or without its "-<n>" suffix.
func quarantineTime(name string) (time.Time, bool) {
	if i := strings.IndexByte(name, '-'); i > 0 {
		name = name[:i]
	}
	at, err := time.Parse(quarantineStampLayout, name)
	return at, err == nil
}

// Purge frees room on a root until need bytes are freed or nothing eligible is left ("space is
// needed", R3 and R5): quarantine first, oldest first (it is not a copy), then eligible trash, oldest
// first. Each entry goes through CanDelete at the moment it is deleted. It returns the bytes freed. It
// implements download.SpaceMaker, which is how the downloader asks for room, and never touches a
// .part or a verified copy.
func (r *Retention) Purge(ctx context.Context, rootID string, need int64) (int64, error) {
	var freed int64
	quarantined, err := r.ListQuarantine(rootID)
	if err != nil {
		return 0, err
	}
	for _, e := range quarantined {
		if freed >= need {
			return freed, nil
		}
		if err := ctx.Err(); err != nil {
			return freed, err
		}
		if err := r.Remove(e.Path); err != nil {
			var refused *RefusedError
			if errors.As(err, &refused) {
				continue
			}
			return freed, err
		}
		freed += e.Bytes
		r.d.Logger.Info("retention: quarantine purged", "component", "sync", "code", CodePurged, "root", rootID, "item", e.ItemID, "stamp", e.Version, "bytes", e.Bytes)
		r.tidyTrashItem(filepath.Dir(e.Path))
	}
	entries, err := r.ListTrash(rootID)
	if err != nil {
		return freed, err
	}
	for _, e := range entries {
		if freed >= need {
			break
		}
		if err := ctx.Err(); err != nil {
			return freed, err
		}
		if err := r.Remove(e.Path); err != nil {
			var refused *RefusedError
			if errors.As(err, &refused) {
				r.d.Logger.Debug("retention: trash entry kept", "component", "sync", "root", rootID, "item", e.ItemID, "version", e.Version, "reason", refused.Reason)
				continue
			}
			return freed, err
		}
		freed += e.Bytes
		r.d.Logger.Info("retention: trash purged", "component", "sync", "code", CodePurged, "root", rootID, "item", e.ItemID, "version", e.Version, "bytes", e.Bytes)
		r.tidyTrashItem(filepath.Dir(e.Path))
	}
	return freed, nil
}

// tidyTrashItem removes .trash/<item> when its last version is gone (an empty directory only).
func (r *Retention) tidyTrashItem(dir string) {
	if ents, err := r.d.FS.ReadDir(dir); err == nil && len(ents) == 0 {
		_ = r.d.FS.Remove(dir)
	}
}

// Expired is what PurgeExpired removed.
type Expired struct {
	Trash, Quarantine int
	Bytes             int64
}

// PurgeExpired deletes the trash older than trashDays and the quarantine older than quarantineDays
// on a root (R3 a, R5). The age of a trash entry is the age of its directory; of a quarantine entry
// the age of its <UTC-timestamp> directory. R1 and R3 still apply to every deletion.
func (r *Retention) PurgeExpired(ctx context.Context, rootID string, trashDays, quarantineDays int) (Expired, error) {
	var out Expired
	now := r.d.Now()
	entries, err := r.ListTrash(rootID)
	if err != nil {
		return out, err
	}
	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		if trashDays <= 0 || now.Sub(e.At) < time.Duration(trashDays)*24*time.Hour {
			continue
		}
		if err := r.Remove(e.Path); err != nil {
			var refused *RefusedError
			if errors.As(err, &refused) {
				continue
			}
			return out, err
		}
		out.Trash++
		out.Bytes += e.Bytes
		r.tidyTrashItem(filepath.Dir(e.Path))
	}
	if quarantineDays <= 0 {
		return out, nil
	}
	quarantined, err := r.ListQuarantine(rootID)
	if err != nil {
		return out, nil
	}
	for _, e := range quarantined {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		if e.At.IsZero() || now.Sub(e.At) < time.Duration(quarantineDays)*24*time.Hour {
			continue
		}
		if err := r.Remove(e.Path); err != nil {
			continue
		}
		out.Quarantine++
		out.Bytes += e.Bytes
		r.tidyTrashItem(filepath.Dir(e.Path))
	}
	return out, ctx.Err()
}

// PurgeUnknownStaging deletes the staging directories no journal record explains (ark-sync.md
// section 4.5.2 and implementation note 7): they were never verified. journal.Items.UnknownStaging
// names them; retention deletes them. It returns how many it removed.
func (r *Retention) PurgeUnknownStaging(unknown func(root ark.Root) ([]string, error), rootID string) (int, error) {
	root, ok := r.root(rootID)
	if !ok {
		return 0, fmt.Errorf("retention: %s is not a mounted root", rootID)
	}
	paths, err := unknown(root)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, p := range paths {
		if err := r.RemoveStaging(p); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// DeleteRequest asks DeleteCopy to remove the verified copy of an item on one root.
type DeleteRequest struct {
	RootID string
	ItemID string
	// KeepLast applies R1; false skips it (aictl delete --allow-last-copy, never the cloud).
	KeepLast bool
}

// DeleteResult is the outcome of DeleteCopy.
type DeleteResult struct {
	// Deleted is true when the copy was removed; Bytes is its size.
	Deleted bool
	Bytes   int64
	// Reason is ReasonLastVerifiedCopy or "not_present" when it was not.
	Reason string
}

// DeleteCopy removes the verified copy of an item on one root. With KeepLast the copy goes only when
// CanDelete says another fresh verified copy survives (R1); otherwise it is left and the result says
// why. The manifest is removed first, so that a crash in the middle leaves "not present", never a
// half copy that looks verified. The index and the journal are updated.
func (r *Retention) DeleteCopy(ctx context.Context, req DeleteRequest) (DeleteResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	root, ok := r.root(req.RootID)
	if !ok {
		return DeleteResult{}, fmt.Errorf("retention: %s is not a mounted root", req.RootID)
	}
	if rootReadOnly(root) {
		return DeleteResult{Reason: ReasonReadOnly}, nil // R8, whatever KeepLast says
	}
	var cp ark.Copy
	found := false
	for _, c := range r.d.Copies.Copies(req.ItemID) {
		if c.RootID == req.RootID {
			cp, found = c, true
			break
		}
	}
	if !found {
		return DeleteResult{Reason: ReasonNotPresent}, nil
	}
	cls := Class{Kind: KindCopy, Root: root, ItemID: req.ItemID, Version: cp.Manifest.Version, Copy: cp}
	if req.KeepLast {
		if ok, reason := r.canDelete(cls); !ok {
			return DeleteResult{Reason: reason}, nil
		}
	}
	if err := ctx.Err(); err != nil {
		return DeleteResult{}, err
	}
	bytes := cp.Manifest.TotalBytes
	if bytes == 0 {
		bytes = r.size(cp.Dir, 0)
	}
	// The manifest first: from this instant the copy is "not present" for every reader.
	if err := r.d.FS.RemoveAll(manifest.Path(cp.Dir)); err != nil {
		return DeleteResult{}, err
	}
	relDir := ""
	if rel, err := filepath.Rel(root.Path, cp.Dir); err == nil {
		relDir = filepath.ToSlash(rel)
	}
	if r.d.Index != nil {
		_, _ = r.d.Index.Refresh(ctx, root, relDir)
	}
	if cp.Manifest.IsDir() || len(cp.Files) == 0 {
		if err := r.d.FS.RemoveAll(cp.Dir); err != nil {
			return DeleteResult{Bytes: bytes}, err
		}
	} else {
		for _, f := range cp.Files {
			if err := r.d.FS.RemoveAll(f); err != nil {
				return DeleteResult{Bytes: bytes}, err
			}
		}
		_ = r.d.FS.Remove(filepath.Join(cp.Dir, manifest.SumsName))
		r.tidyDir(cp.Dir)
	}
	if r.d.Items != nil {
		if err := r.d.Items.Delete(req.RootID, req.ItemID); err != nil {
			r.d.Logger.Warn("retention: the journal record of a deleted copy was kept", "component", "sync", "root", req.RootID, "item", req.ItemID, "err", err)
		}
	}
	r.d.Logger.Info("retention: copy deleted", "component", "sync", "root", req.RootID, "item", req.ItemID, "version", cp.Manifest.Version, "bytes", bytes, "keep_last", req.KeepLast)
	return DeleteResult{Deleted: true, Bytes: bytes}, nil
}

// tidyDir removes an item directory that is empty now (never one with content).
func (r *Retention) tidyDir(dir string) {
	if ents, err := r.d.FS.ReadDir(dir); err == nil && len(ents) == 0 {
		_ = r.d.FS.Remove(dir)
	}
}
