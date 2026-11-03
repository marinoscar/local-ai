package verify

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/marinoscar/local-ai/agent/internal/ark"
	"github.com/marinoscar/local-ai/agent/internal/sync/journal"
)

// Serve-time verification (ark-sync.md section 4.1.3). A drive the kernel flagged as having filesystem
// errors (`unclean`) is read-only to the agent and its copies are invisible to the best-copy index
// until each has been re-hashed: before a model on that root is loaded, or a ZIM is added to the
// library, the consumer asks VerifyBeforeServe. A copy that passes is marked in the index
// (ark.Index.MarkServeVerified) and from then on is served like any other; a copy that fails is
// excluded in memory (ark.Index.Exclude) and recorded in the journal. Nothing is ever written to the
// drive: it is read-only and may be dying.

// ErrNotOnRoot: the root holds no copy of the item (or the item's manifest is not readable).
var ErrNotOnRoot = errors.New("verify: the root holds no copy of the item")

// DamagedError: the copy does not match its manifest (LAI-SYN-025). The consumer must not serve it.
type DamagedError struct {
	Root, Item, Version string
	Bad                 BadFile
}

func (e *DamagedError) Error() string {
	return fmt.Sprintf("%s: %s %s on %s no longer matches its checksum (%s %s)", CodeBitRot, e.Item, e.Version, e.Root, e.Bad.Path, e.Bad.Reason)
}

// Code is the doctor code of the error.
func (e *DamagedError) Code() string { return CodeBitRot }

// ServeDeps are the collaborators of a ServeVerifier. FS, Roots and Index are required.
type ServeDeps struct {
	FS    FS
	Roots func() []ark.Root
	// Scan lists the copies of a root; nil reads them with ark.ScanRoot.
	Scan    func(ctx context.Context, root ark.Root) ([]ark.ScannedItem, error)
	Index   Index
	Journal Journal
	Logger  *slog.Logger
}

type serveKey struct{ root, item, version string }

type serveEntry struct {
	done chan struct{}
	err  error
}

// ServeVerifier answers VerifyBeforeServe. The answer for a (root, item, version) is kept for the life
// of the mount, so a model that is loaded again and again is hashed once. Safe for concurrent use;
// callers that ask for the same copy at the same time share one read.
type ServeVerifier struct {
	d ServeDeps

	mu    sync.Mutex
	cache map[serveKey]*serveEntry
}

// NewServeVerifier checks the dependencies and returns a ServeVerifier.
func NewServeVerifier(d ServeDeps) (*ServeVerifier, error) {
	var missing []string
	if d.FS == nil {
		missing = append(missing, "FS")
	}
	if d.Roots == nil {
		missing = append(missing, "Roots")
	}
	if d.Index == nil {
		missing = append(missing, "Index")
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("verify: the serve-time verifier needs %v", missing)
	}
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	if d.Scan == nil {
		fsys := d.FS
		d.Scan = func(ctx context.Context, root ark.Root) ([]ark.ScannedItem, error) {
			items, _, err := ark.ScanRoot(ctx, fsys, root)
			return items, err
		}
	}
	return &ServeVerifier{d: d, cache: map[serveKey]*serveEntry{}}, nil
}

// VerifyBeforeServe makes sure the copy of an item on a root may be served. On a root that is not
// unclean it answers nil at once (the copy was verified when it was written and is re-checked by the
// re-verification). On an unclean root it hashes the copy against its manifest unless this mount has
// already done so: nil means "serve it" (and the index now offers it), *DamagedError means "do not" (and
// the index no longer offers it), ErrNotOnRoot means there is nothing to serve, and a context error
// means the answer is not known yet (nothing is remembered). The hashing reads through the page cache
// politely (see FS.OpenRead) and stops at the context.
func (v *ServeVerifier) VerifyBeforeServe(ctx context.Context, rootID, itemID string) error {
	root, ok := v.root(rootID)
	if !ok {
		return ErrNotOnRoot
	}
	if root.State != ark.StateUnclean {
		return nil
	}
	items, err := v.d.Scan(ctx, root)
	if err != nil {
		return err
	}
	var it *ark.ScannedItem
	for i := range items {
		if items[i].Manifest.ItemID == itemID {
			it = &items[i]
			break
		}
	}
	if it == nil {
		return ErrNotOnRoot
	}
	key := serveKey{rootID, itemID, it.Manifest.Version}

	v.mu.Lock()
	e, ok := v.cache[key]
	if !ok {
		e = &serveEntry{done: make(chan struct{})}
		v.cache[key] = e
	}
	v.mu.Unlock()
	if ok {
		select {
		case <-e.done:
			return e.err
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	err = v.verify(ctx, root, *it)
	if ctx.Err() != nil && err != nil && !isDamaged(err) {
		// An answer that was not reached is not an answer: forget it, wake the waiters to ask again.
		v.mu.Lock()
		delete(v.cache, key)
		v.mu.Unlock()
		e.err = err
		close(e.done)
		return err
	}
	e.err = err
	close(e.done)
	return err
}

func isDamaged(err error) bool {
	var d *DamagedError
	return errors.As(err, &d)
}

func (v *ServeVerifier) verify(ctx context.Context, root ark.Root, it ark.ScannedItem) error {
	m := it.Manifest
	chk, err := checkCopy(ctx, v.d.FS, it.Dir, m, nil, nil)
	if err != nil {
		return err
	}
	log := v.d.Logger.With("component", "sync", "root", root.ID, "item", m.ItemID, "version", m.Version)
	if chk.OK {
		v.d.Index.MarkServeVerified(root.ID, m.ItemID, m.Version)
		log.Info("a copy on an unclean drive passed its serve-time check")
		return nil
	}
	log.Error("a copy on an unclean drive no longer matches its checksum; it will not be served", "code", CodeBitRot, "file", chk.Bad.Path, "problem", chk.Bad.Reason)
	v.d.Index.Exclude(root.ID, m.ItemID, m.Version)
	if v.d.Journal != nil {
		_, jerr := v.d.Journal.Update(root.ID, m.ItemID, func(i *journal.Item) {
			i.State, i.Version, i.Code = journal.StateQuarantined, m.Version, CodeBitRot
			i.LastError = "the stored copy no longer matches its checksum"
			i.Attempts, i.PartialBytes, i.Checkpoints = 0, 0, nil
		})
		if jerr != nil {
			log.Warn("the journal was not told about the damaged copy", "err", jerr)
		}
	}
	return &DamagedError{Root: root.ID, Item: m.ItemID, Version: m.Version, Bad: chk.Bad}
}

// Unmounted forgets what was learned about a root: a drive that is plugged in again is judged anew
// (ark.Index drops its marks and exclusions on the same event).
func (v *ServeVerifier) Unmounted(rootID string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	for k := range v.cache {
		if k.root == rootID {
			delete(v.cache, k)
		}
	}
}

func (v *ServeVerifier) root(id string) (ark.Root, bool) {
	for _, r := range v.d.Roots() {
		if r.ID == id {
			return r, true
		}
	}
	return ark.Root{}, false
}
