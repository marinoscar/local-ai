// Package journal remembers what ark sync is doing, so that a crash or a power cut never loses or
// corrupts content (docs/specs/ark-sync.md sections 3.1, 4.5 and 5). It has two parts that work
// together:
//
//   - items.json (Items): the current state of every (root, item) pair, replaced atomically on every
//     change, with a .bak of the previous version;
//   - runs/<run-id>.jsonl (Runs, Run): the append-only event log of one run. The write-ahead rule:
//     Run.Append returns only after the event is fsynced, and the caller performs the side effect it
//     announces after that, never before. Replay turns the log of the last run back into the intended
//     state of each item, which is what a restarted agent resumes from.
//
// What the journal is not: the source of truth for what content exists. That is the MANIFESTs: a
// lost or corrupt items.json is rebuilt from them and never loses a verified copy.
//
// The package never touches content directories except to read manifests (through internal/ark) and
// to list .staging. It deletes only its own housekeeping files (old run logs, temp files) through the
// FS seam, and never an item's files: that is retention's job (SYNC-08).
package journal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	"github.com/marinoscar/local-ai/agent/internal/jsonord"
	"github.com/marinoscar/local-ai/agent/internal/sync/manifest"
)

// Names and limits.
const (
	// DefaultDir is the journal directory of a node.
	DefaultDir = "/var/lib/localai/sync"
	// ItemsName and ItemsBackupName are the files in that directory.
	ItemsName       = "items.json"
	ItemsBackupName = "items.json.bak"
	// ItemsSchemaVersion is the only items.json schema this code reads and writes.
	ItemsSchemaVersion = 1

	maxItemsBytes = 64 << 20
)

// CodeRebuilt is the doctor code raised when items.json was damaged or missing and was restored
// from items.json.bak or rebuilt from the MANIFESTs (LAI-SYN-050, info).
const CodeRebuilt = doctor.SYN050

// ItemState is the state of one (root, item, target version) in the state machine of ark-sync.md
// section 4.5. Only the journal knows the states other than verified.
type ItemState string

// The states.
const (
	StatePlanned     ItemState = "planned"
	StateResolving   ItemState = "resolving"
	StateDownloading ItemState = "downloading"
	StateVerifying   ItemState = "verifying"
	StateStaged      ItemState = "staged"
	StateSwapping    ItemState = "swapping"
	StateSwapped     ItemState = "swapped"
	StateVerified    ItemState = "verified"
	StateQuarantined ItemState = "quarantined"
	StateFailed      ItemState = "failed"
	StateSkipped     ItemState = "skipped"
)

var allStates = map[ItemState]bool{
	StatePlanned: true, StateResolving: true, StateDownloading: true, StateVerifying: true, StateStaged: true,
	StateSwapping: true, StateSwapped: true, StateVerified: true, StateQuarantined: true, StateFailed: true, StateSkipped: true,
}

// Valid reports whether s is one of the eleven states.
func (s ItemState) Valid() bool { return allStates[s] }

// Checkpoint is the resume point of one file of a download: the offset of the last byte that is
// durable in the .part file, the marshalled state of the streaming sha256 at that offset (base64),
// and the validators the next request sends in If-Range. Request headers are never stored.
type Checkpoint struct {
	Offset       int64
	ShaState     string
	ETag         string
	LastModified string
	URL          string
	Extra        map[string]json.RawMessage
}

// MarshalJSON implements json.Marshaler.
func (c Checkpoint) MarshalJSON() ([]byte, error) {
	kv := []jsonord.KV{{Key: "offset", Val: c.Offset}, {Key: "sha_state", Val: c.ShaState}}
	if c.ETag != "" {
		kv = append(kv, jsonord.KV{Key: "etag", Val: c.ETag})
	}
	if c.LastModified != "" {
		kv = append(kv, jsonord.KV{Key: "last_modified", Val: c.LastModified})
	}
	if c.URL != "" {
		kv = append(kv, jsonord.KV{Key: "url", Val: c.URL})
	}
	return jsonord.Object(kv, c.Extra)
}

// UnmarshalJSON implements json.Unmarshaler.
func (c *Checkpoint) UnmarshalJSON(data []byte) error {
	extra, err := jsonord.Decode(data, map[string]any{
		"offset": &c.Offset, "sha_state": &c.ShaState, "etag": &c.ETag, "last_modified": &c.LastModified, "url": &c.URL,
	})
	c.Extra = extra
	return err
}

// Item is the journal's record of one (root, item) pair.
type Item struct {
	// RootID and ItemID identify the pair; they are the key of items.json, not fields of the value.
	RootID, ItemID string
	// State is the current state; Version is the target version it is about.
	State   ItemState
	Version string
	// Attempts counts the tries of the current version; LastError and Code describe the last failure
	// (Code is a doctor code).
	Attempts  int
	LastError string
	Code      string
	// PartialBytes is the total of the .part bytes on disk for the current version.
	PartialBytes int64
	// Checkpoints is the resume point per file, keyed by the file's path in the item.
	Checkpoints map[string]Checkpoint
	// ConsecutiveFailedRuns counts runs in a row in which the item failed (3 raises LAI-SYN-035).
	ConsecutiveFailedRuns int
	// UpdatedAt is when the journal last changed the record.
	UpdatedAt time.Time
	// Extras is the open bag other parts store their per-item facts in (extras.inventory is the last
	// copied sha256 of the drive inventory, SYNC-14). Values are raw JSON; the journal keeps them.
	Extras map[string]json.RawMessage
	// Extra holds unknown keys of the record, kept verbatim.
	Extra map[string]json.RawMessage
}

// Key is the items.json key of a pair: "<root_id>/<item_id>".
func Key(rootID, itemID string) string { return rootID + "/" + itemID }

// SplitKey is the inverse of Key.
func SplitKey(key string) (rootID, itemID string, ok bool) {
	rootID, itemID, ok = strings.Cut(key, "/")
	return rootID, itemID, ok && ark.ValidRootID(rootID) && manifest.SafeName(itemID)
}

// Key is Key(i.RootID, i.ItemID).
func (i Item) Key() string { return Key(i.RootID, i.ItemID) }

func (i Item) clone() Item {
	if i.Checkpoints != nil {
		cp := make(map[string]Checkpoint, len(i.Checkpoints))
		for k, v := range i.Checkpoints {
			cp[k] = v
		}
		i.Checkpoints = cp
	}
	i.Extras = cloneRaw(i.Extras)
	i.Extra = cloneRaw(i.Extra)
	return i
}

func cloneRaw(m map[string]json.RawMessage) map[string]json.RawMessage {
	if m == nil {
		return nil
	}
	out := make(map[string]json.RawMessage, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// MarshalJSON implements json.Marshaler (the value of an items.json entry).
func (i Item) MarshalJSON() ([]byte, error) {
	kv := []jsonord.KV{{Key: "state", Val: i.State}, {Key: "version", Val: i.Version}, {Key: "attempts", Val: i.Attempts}}
	if i.LastError != "" {
		kv = append(kv, jsonord.KV{Key: "last_error", Val: i.LastError})
	}
	if i.Code != "" {
		kv = append(kv, jsonord.KV{Key: "code", Val: i.Code})
	}
	kv = append(kv, jsonord.KV{Key: "partial_bytes", Val: i.PartialBytes})
	if len(i.Checkpoints) > 0 {
		kv = append(kv, jsonord.KV{Key: "checkpoints", Val: i.Checkpoints})
	}
	kv = append(kv, jsonord.KV{Key: "consecutive_failed_runs", Val: i.ConsecutiveFailedRuns})
	if !i.UpdatedAt.IsZero() {
		kv = append(kv, jsonord.KV{Key: "updated_at", Val: jsonord.Stamp(i.UpdatedAt)})
	}
	if len(i.Extras) > 0 {
		kv = append(kv, jsonord.KV{Key: "extras", Val: i.Extras})
	}
	return jsonord.Object(kv, i.Extra)
}

// UnmarshalJSON implements json.Unmarshaler.
func (i *Item) UnmarshalJSON(data []byte) error {
	extra, err := jsonord.Decode(data, map[string]any{
		"state": &i.State, "version": &i.Version, "attempts": &i.Attempts, "last_error": &i.LastError, "code": &i.Code,
		"partial_bytes": &i.PartialBytes, "checkpoints": &i.Checkpoints, "consecutive_failed_runs": &i.ConsecutiveFailedRuns,
		"updated_at": (*jsonord.Stamp)(&i.UpdatedAt), "extras": &i.Extras,
	})
	i.Extra = extra
	return err
}

// Errors of reading items.json. Open handles them itself; they are exported for tests and tools.
var (
	// ErrItemsCorrupt: not JSON, a value of the wrong type, an impossible state or key.
	ErrItemsCorrupt = errors.New("journal: items.json is damaged")
	// ErrItemsSchema: schema_version is not 1 (a newer agent wrote it).
	ErrItemsSchema = errors.New("journal: items.json has an unsupported schema_version")
)

// itemsFile is items.json: {"schema_version":1,"updated_at":...,"items":{"<root>/<item>":{...}}}.
type itemsFile struct {
	UpdatedAt time.Time
	Items     map[string]Item
	Extra     map[string]json.RawMessage
}

func encodeItems(f itemsFile) ([]byte, error) {
	items := f.Items
	if items == nil {
		items = map[string]Item{}
	}
	kv := []jsonord.KV{{Key: "schema_version", Val: ItemsSchemaVersion}}
	if !f.UpdatedAt.IsZero() {
		kv = append(kv, jsonord.KV{Key: "updated_at", Val: jsonord.Stamp(f.UpdatedAt)})
	}
	kv = append(kv, jsonord.KV{Key: "items", Val: items}) // json sorts map keys
	raw, err := jsonord.Object(kv, f.Extra)
	if err != nil {
		return nil, err
	}
	return jsonord.Indent(raw)
}

func parseItems(data []byte) (itemsFile, error) {
	if len(data) > maxItemsBytes {
		return itemsFile{}, fmt.Errorf("%w: %d bytes is too large", ErrItemsCorrupt, len(data))
	}
	var head struct {
		SchemaVersion *int `json:"schema_version"`
	}
	if err := json.Unmarshal(data, &head); err != nil {
		return itemsFile{}, fmt.Errorf("%w: %v", ErrItemsCorrupt, err)
	}
	switch {
	case head.SchemaVersion == nil:
		return itemsFile{}, fmt.Errorf("%w: schema_version is missing", ErrItemsCorrupt)
	case *head.SchemaVersion != ItemsSchemaVersion:
		return itemsFile{}, fmt.Errorf("%w: schema_version %d", ErrItemsSchema, *head.SchemaVersion)
	}
	var f itemsFile
	raw := map[string]json.RawMessage{}
	extra, err := jsonord.Decode(data, map[string]any{"schema_version": new(int), "updated_at": (*jsonord.Stamp)(&f.UpdatedAt), "items": &raw})
	if err != nil {
		return itemsFile{}, fmt.Errorf("%w: %v", ErrItemsCorrupt, err)
	}
	f.Extra = extra
	f.Items = make(map[string]Item, len(raw))
	for key, r := range raw {
		rootID, itemID, ok := SplitKey(key)
		if !ok {
			return itemsFile{}, fmt.Errorf("%w: key %q is not <root_id>/<item_id>", ErrItemsCorrupt, key)
		}
		var it Item
		if err := json.Unmarshal(r, &it); err != nil {
			return itemsFile{}, fmt.Errorf("%w: %s: %v", ErrItemsCorrupt, key, err)
		}
		if !it.State.Valid() {
			return itemsFile{}, fmt.Errorf("%w: %s has state %q", ErrItemsCorrupt, key, it.State)
		}
		it.RootID, it.ItemID = rootID, itemID
		f.Items[key] = it
	}
	return f, nil
}

// Scanned is what a scan of the mounted roots found: the verified items, which roots were actually
// read (so that "no manifest here" means something), and the LAI-SYN-052 problems of the ones left
// out.
type Scanned struct {
	Items    []ark.ScannedItem
	Roots    []string
	Problems []ark.Problem
}

// ScanFunc scans the mounted roots. It is called when items.json has to be rebuilt or reconciled.
type ScanFunc func(ctx context.Context) (Scanned, error)

// ScanRoots returns the ScanFunc that reads the given roots with ark.ScanRoot. Roots that are not
// mounted and serving (ejecting, unusable, ...) are not read.
func ScanRoots(fsys config.FS, roots func() []ark.Root) ScanFunc {
	return func(ctx context.Context) (Scanned, error) {
		var out Scanned
		for _, r := range roots() {
			if r.State != ark.StateReady && r.State != ark.StateReadOnly && r.State != ark.StateUnclean {
				continue
			}
			items, problems, err := ark.ScanRoot(ctx, fsys, r)
			if err != nil {
				return Scanned{}, err
			}
			out.Items = append(out.Items, items...)
			out.Problems = append(out.Problems, problems...)
			out.Roots = append(out.Roots, r.ID)
		}
		return out, nil
	}
}

// Source says where Open got the items from.
type Source string

// The sources.
const (
	// SourceFile: items.json was read as it is.
	SourceFile Source = "file"
	// SourceEmpty: nothing existed and no manifest was found: a fresh node. Not a problem.
	SourceEmpty Source = "empty"
	// SourceBackup: items.json was damaged or missing and items.json.bak was used.
	SourceBackup Source = "backup"
	// SourceRebuilt: items.json (and the backup) were unusable and the items were rebuilt from the
	// MANIFESTs.
	SourceRebuilt Source = "rebuilt"
)

// Options configures Open and Journal.
type Options struct {
	// FS is the filesystem seam; default OSFS.
	FS FS
	// Dir is the journal directory; default DefaultDir.
	Dir string
	// Now stamps records and events; default time.Now.
	Now func() time.Time
	// Scan reads the manifests of the mounted roots for a rebuild or a reconcile. nil means "no roots
	// are mounted yet": a damaged items.json then comes back empty.
	Scan ScanFunc
	// Logger receives notices (a file moved aside, the .bak that could not be written); default slog.Default.
	Logger *slog.Logger
	// KeepRuns is how many run logs are kept; default 60.
	KeepRuns int
	// Rand is the source of the run id's random part; default crypto/rand.
	Rand func(p []byte) (int, error)
}

func (o Options) withDefaults() Options {
	if o.FS == nil {
		o.FS = OSFS{}
	}
	if o.Dir == "" {
		o.Dir = DefaultDir
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.KeepRuns <= 0 {
		o.KeepRuns = KeepRuns
	}
	return o
}

// FS is the filesystem seam of the journal: config.FS (which the atomic writer takes) plus MkdirAll
// for the journal and runs directories. OSFS is the real one; tests wrap it to inject faults.
type FS interface {
	config.FS
	MkdirAll(path string, perm os.FileMode) error
}

// OSFS is the real filesystem.
type OSFS struct{ config.OSFS }

// MkdirAll implements FS.
func (OSFS) MkdirAll(path string, perm os.FileMode) error { return os.MkdirAll(path, perm) }

// Items is items.json in memory. Every mutation is written through atomically (temp file, fsync,
// rename, fsync of the directory) before it returns, after copying the previous good file to
// items.json.bak, so at any crash the file on disk is the state before or after the mutation. Safe
// for concurrent use.
type Items struct {
	opts Options

	mu    sync.Mutex
	items map[string]Item
	extra map[string]json.RawMessage // unknown top-level keys of the file
}

// Report is what Open tells the caller about the file it found.
type Report struct {
	// Source says where the items came from.
	Source Source
	// Problems has one LAI-SYN-050 (info) when the file had to be restored or rebuilt, plus the
	// LAI-SYN-052 problems of manifests the rebuild had to leave out.
	Problems []ark.Problem
	// MovedAside is where a damaged items.json was moved to ("" when none was).
	MovedAside string
	// Changed is how many records the reconcile with the manifests added, fixed or dropped.
	Changed int
}

// OpenItems loads items.json:
//
//  1. leftover temp files of an interrupted write are swept;
//  2. items.json is read; when it is damaged or from another schema it is renamed to
//     items.json.damaged-<UTC time> (never deleted);
//  3. when it is damaged or missing, items.json.bak is tried, and when that is unusable too the items
//     are rebuilt from the MANIFESTs (Options.Scan): a verified record for every manifest found;
//  4. a restored or rebuilt state is reconciled with the manifests (Reconcile), written out at once,
//     and reported as LAI-SYN-050.
//
// A fresh node (no file, no backup, no manifest) is SourceEmpty and raises nothing. The returned
// error is only a failure to write the repaired file or the context's.
func OpenItems(ctx context.Context, opts Options) (*Items, Report, error) {
	opts = opts.withDefaults()
	it := &Items{opts: opts, items: map[string]Item{}}
	path, bak := filepath.Join(opts.Dir, ItemsName), filepath.Join(opts.Dir, ItemsBackupName)
	var rep Report

	config.SweepTemps(opts.FS, path)
	config.SweepTemps(opts.FS, bak)

	f, reason, err := readItemsFile(opts.FS, path)
	if err == nil {
		it.items, it.extra = f.Items, f.Extra
		rep.Source = SourceFile
		return it, rep, nil
	}
	if !errors.Is(err, fs.ErrNotExist) && !errors.Is(err, ErrItemsCorrupt) && !errors.Is(err, ErrItemsSchema) {
		// The drive failed to read the file: its content is unknown, so it is neither trusted nor
		// replaced. Sync waits until the journal can be read.
		return nil, rep, fmt.Errorf("journal: read %s: %w", path, err)
	}
	if errors.Is(err, ErrItemsCorrupt) || errors.Is(err, ErrItemsSchema) {
		moved, merr := moveAside(opts.FS, path, "damaged", opts.Now())
		if merr != nil {
			opts.Logger.Warn("journal: could not move the damaged items.json aside", "error", merr)
		} else {
			rep.MovedAside = moved
		}
	}

	bf, _, berr := readItemsFile(opts.FS, bak)
	if berr == nil {
		it.items, it.extra = bf.Items, bf.Extra
		rep.Source = SourceBackup
	} else {
		rep.Source = SourceRebuilt
	}

	var sc Scanned
	if opts.Scan != nil {
		if sc, err = opts.Scan(ctx); err != nil {
			return nil, rep, err
		}
		rep.Problems = append(rep.Problems, sc.Problems...)
	}
	changed, err := it.reconcileLocked(sc)
	if err != nil {
		return nil, rep, err
	}
	rep.Changed = changed
	// A node that never synced has no file, no backup and no manifest: that is not a problem.
	if rep.Source == SourceRebuilt && reason == "missing" && errors.Is(berr, fs.ErrNotExist) && len(it.items) == 0 {
		rep.Source = SourceEmpty
		return it, rep, nil
	}
	rep.Problems = append(rep.Problems, ark.Problem{
		Code: CodeRebuilt, Severity: doctor.Info,
		Details: map[string]any{"source": string(rep.Source), "reason": reason, "items": len(it.items)},
	})
	if err := it.saveLocked(); err != nil && !errors.Is(err, config.ErrNotDurable) {
		return nil, rep, fmt.Errorf("journal: write the repaired items.json: %w", err)
	}
	return it, rep, nil
}

// readItemsFile reads and parses one items file. reason is "missing", or what is wrong with it.
func readItemsFile(fsys config.FS, path string) (itemsFile, string, error) {
	data, err := fsys.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return itemsFile{}, "missing", err
		}
		return itemsFile{}, "unreadable: " + err.Error(), err
	}
	f, err := parseItems(data)
	if err != nil {
		return itemsFile{}, err.Error(), err
	}
	return f, "", nil
}

// moveAside renames path to path.<marker>-<UTC time>, never overwriting.
func moveAside(fsys config.FS, path, marker string, now time.Time) (string, error) {
	base := fmt.Sprintf("%s.%s-%s", path, marker, now.UTC().Format("20060102T150405Z"))
	for n := 0; n < 100; n++ {
		dst := base
		if n > 0 {
			dst = fmt.Sprintf("%s-%d", base, n)
		}
		if _, err := fsys.Lstat(dst); err == nil {
			continue
		}
		return dst, fsys.Rename(path, dst)
	}
	return "", fmt.Errorf("journal: no free name for %s", base)
}

// Get returns a copy of the record of a pair.
func (it *Items) Get(rootID, itemID string) (Item, bool) {
	it.mu.Lock()
	defer it.mu.Unlock()
	i, ok := it.items[Key(rootID, itemID)]
	return i.clone(), ok
}

// List returns copies of all records, sorted by key.
func (it *Items) List() []Item {
	it.mu.Lock()
	defer it.mu.Unlock()
	keys := make([]string, 0, len(it.items))
	for k := range it.items {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]Item, len(keys))
	for n, k := range keys {
		out[n] = it.items[k].clone()
	}
	return out
}

// Update applies fn to a copy of the record of the pair (a zero record when there is none), stamps
// UpdatedAt and writes items.json. The record must end up with a valid state, else nothing changes
// and an error is returned. The result is the new record. A write failure leaves both the file and
// the memory as they were, except an error wrapping config.ErrNotDurable (the file was replaced but
// the directory sync failed), where the new record is kept.
func (it *Items) Update(rootID, itemID string, fn func(*Item)) (Item, error) {
	if !ark.ValidRootID(rootID) || !manifest.SafeName(itemID) {
		return Item{}, fmt.Errorf("journal: %q is not a valid root/item pair", Key(rootID, itemID))
	}
	it.mu.Lock()
	defer it.mu.Unlock()
	key := Key(rootID, itemID)
	cur := it.items[key].clone()
	cur.RootID, cur.ItemID = rootID, itemID
	fn(&cur)
	cur.RootID, cur.ItemID = rootID, itemID
	if !cur.State.Valid() {
		return Item{}, fmt.Errorf("journal: %s: %q is not a state", key, cur.State)
	}
	cur.UpdatedAt = it.opts.Now()
	return cur.clone(), it.commitLocked(func(m map[string]Item) { m[key] = cur })
}

// Delete removes the record of a pair (nothing to do when there is none).
func (it *Items) Delete(rootID, itemID string) error {
	it.mu.Lock()
	defer it.mu.Unlock()
	key := Key(rootID, itemID)
	if _, ok := it.items[key]; !ok {
		return nil
	}
	return it.commitLocked(func(m map[string]Item) { delete(m, key) })
}

// commitLocked applies mutate to a copy of the map, writes it and, if the write worked, adopts it.
func (it *Items) commitLocked(mutate func(map[string]Item)) error {
	next := make(map[string]Item, len(it.items)+1)
	for k, v := range it.items {
		next[k] = v
	}
	mutate(next)
	prev := it.items
	it.items = next
	if err := it.saveLocked(); err != nil {
		if !errors.Is(err, config.ErrNotDurable) {
			it.items = prev
		}
		return err
	}
	return nil
}

// saveLocked writes items.json: the previous good file is copied to .bak first (best effort: a failure
// is logged, never fatal), then the new content replaces the file atomically.
func (it *Items) saveLocked() error {
	fsys := it.opts.FS
	path, bak := filepath.Join(it.opts.Dir, ItemsName), filepath.Join(it.opts.Dir, ItemsBackupName)
	data, err := encodeItems(itemsFile{UpdatedAt: it.opts.Now(), Items: it.items, Extra: it.extra})
	if err != nil {
		return err
	}
	if err := fsys.MkdirAll(it.opts.Dir, 0o755); err != nil {
		return fmt.Errorf("journal: create %s: %w", it.opts.Dir, err)
	}
	if old, err := fsys.ReadFile(path); err == nil {
		if _, perr := parseItems(old); perr == nil { // never replace a good backup with a damaged file
			if berr := config.WriteFileAtomicFS(fsys, bak, old, 0o644, -1, -1); berr != nil && !errors.Is(berr, config.ErrNotDurable) {
				it.opts.Logger.Warn("journal: could not refresh items.json.bak", "error", berr)
			}
		}
	}
	return config.WriteFileAtomicFS(fsys, path, data, 0o644, -1, -1)
}

// Reconcile makes the records agree with what the manifests of the scanned roots say. The manifest
// is the commit point of a swap, so it wins:
//
//   - a manifest with no record gets a verified record;
//   - a record for the manifest's version that is not verified yet (the journal is a step behind a
//     swap that committed) becomes verified, with its failure fields cleared;
//   - a verified record whose version differs from the manifest's takes the manifest's version;
//   - a verified record on a scanned root that has no manifest any more is dropped (the item is not
//     there; the planner derives what to fetch). Records in other states, and records of roots that
//     were not scanned (an unplugged drive), are left alone.
//
// It writes items.json once if anything changed, and returns how many records changed.
func (it *Items) Reconcile(sc Scanned) (int, error) {
	it.mu.Lock()
	defer it.mu.Unlock()
	return it.reconcileLocked(sc)
}

func (it *Items) reconcileLocked(sc Scanned) (int, error) {
	next := make(map[string]Item, len(it.items))
	for k, v := range it.items {
		next[k] = v
	}
	changed := 0
	now := it.opts.Now()
	scanned := map[string]bool{}
	for _, r := range sc.Roots {
		scanned[r] = true
	}
	seen := map[string]bool{}
	for _, s := range sc.Items {
		scanned[s.RootID] = true
		key := Key(s.RootID, s.Manifest.ItemID)
		seen[key] = true
		cur, ok := next[key]
		switch {
		case !ok:
			next[key] = Item{RootID: s.RootID, ItemID: s.Manifest.ItemID, State: StateVerified, Version: s.Manifest.Version, UpdatedAt: now}
			changed++
		case cur.State == StateVerified && cur.Version != s.Manifest.Version,
			cur.State != StateVerified && cur.Version == s.Manifest.Version:
			cur = cur.clone()
			cur.State, cur.Version, cur.UpdatedAt = StateVerified, s.Manifest.Version, now
			cur.Attempts, cur.LastError, cur.Code, cur.PartialBytes, cur.Checkpoints, cur.ConsecutiveFailedRuns = 0, "", "", 0, nil, 0
			next[key] = cur
			changed++
		}
	}
	for key, cur := range next {
		if cur.State == StateVerified && scanned[cur.RootID] && !seen[key] {
			delete(next, key)
			changed++
		}
	}
	if changed == 0 {
		return 0, nil
	}
	prev := it.items
	it.items = next
	if err := it.saveLocked(); err != nil {
		if !errors.Is(err, config.ErrNotDurable) {
			it.items = prev
		}
		return 0, err
	}
	return changed, nil
}

// UnknownStaging lists the directories under root's .staging that no record explains: a
// .staging/<item-id>/<version> for which the journal has no record of that item at that version in a
// state other than verified (a verified item has nothing left to stage). They were never verified, so
// they are junk; this function only names them, for retention to delete (SYNC-08). The quarantine
// tree .staging/quarantine is never listed. A missing .staging is no error.
func (it *Items) UnknownStaging(fsys config.FS, root ark.Root) ([]string, error) {
	staging := filepath.Join(root.Path, ".staging")
	itemDirs, err := fsys.ReadDir(staging)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	it.mu.Lock()
	defer it.mu.Unlock()
	var out []string
	for _, id := range itemDirs {
		if !id.IsDir() || id.Name() == "quarantine" {
			continue
		}
		versions, err := fsys.ReadDir(filepath.Join(staging, id.Name()))
		if err != nil {
			return nil, err
		}
		for _, v := range versions {
			rec, ok := it.items[Key(root.ID, id.Name())]
			if !ok || rec.State == StateVerified || rec.Version != v.Name() {
				out = append(out, filepath.Join(staging, id.Name(), v.Name()))
			}
		}
	}
	sort.Strings(out)
	return out, nil
}
