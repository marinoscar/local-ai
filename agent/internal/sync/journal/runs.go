package journal

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"sync"
	"syscall"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/ark"
	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/jsonord"
	"github.com/marinoscar/local-ai/agent/internal/sync/manifest"
)

const (
	// RunsDirName is the directory of run logs inside the journal directory.
	RunsDirName = "runs"
	// KeepRuns is how many run logs are kept (the newest).
	KeepRuns = 60
	// CheckpointEvery is the download offset step between checkpoint events (64 MiB, ark-sync.md 4.6).
	CheckpointEvery = 64 << 20

	maxRunBytes = 256 << 20
)

// The events of a run log (ark-sync.md section 4.5 and 4.5.1).
const (
	// EvState announces a state transition of one item: write it before the side effect.
	EvState = "state"
	// EvCkpt records a download checkpoint of one file.
	EvCkpt = "ckpt"
	// EvSwapPlan records the planned renames and moves to trash of a swap, before the first one.
	EvSwapPlan = "swap_plan"
	// EvSwapDone records that the swap, including the MANIFEST write, finished.
	EvSwapDone = "swap_done"
)

// Event is one line of a run log. Which fields are used depends on Ev:
//
//	state      Root, Item, Version, From, To
//	ckpt       Root, Item, Version, File, Offset, ShaState, ETag, LastModified, URL
//	swap_plan  Root, Item, Version, Renames, Trash
//	swap_done  Root, Item, Version
//
// At is stamped by Append when zero. Any other Ev is accepted and carries Root, Item and Version if
// it has them, so other parts of ark sync can add events without touching this package; Replay
// ignores the ones it does not know. Request headers are never part of an event.
type Event struct {
	Ev      string
	At      time.Time
	Root    string
	Item    string
	Version string

	From ItemState
	To   ItemState

	File         string
	Offset       int64
	ShaState     string
	ETag         string
	LastModified string
	URL          string

	// Renames, Trash and Exchange are [source, destination] pairs of absolute paths. Trash is the
	// old version moving out (done first), Exchange the directory swaps (renameat2 RENAME_EXCHANGE,
	// done second), Renames the new version moving in, the manifest last.
	Renames  [][2]string
	Trash    [][2]string
	Exchange [][2]string
}

// MarshalJSON implements json.Marshaler: compact, fixed key order, no HTML escaping.
func (e Event) MarshalJSON() ([]byte, error) {
	kv := []jsonord.KV{{Key: "ev", Val: e.Ev}, {Key: "at", Val: jsonord.Stamp(e.At)}}
	add := func(key, val string) {
		if val != "" {
			kv = append(kv, jsonord.KV{Key: key, Val: val})
		}
	}
	add("root", e.Root)
	add("item", e.Item)
	add("version", e.Version)
	add("from", string(e.From))
	add("to", string(e.To))
	add("file", e.File)
	if e.Ev == EvCkpt || e.Offset != 0 {
		kv = append(kv, jsonord.KV{Key: "offset", Val: e.Offset})
	}
	add("sha_state", e.ShaState)
	add("etag", e.ETag)
	add("last_modified", e.LastModified)
	add("url", e.URL)
	if e.Ev == EvSwapPlan || e.Renames != nil {
		kv = append(kv, jsonord.KV{Key: "renames", Val: nonNil(e.Renames)})
	}
	if e.Ev == EvSwapPlan || e.Trash != nil {
		kv = append(kv, jsonord.KV{Key: "trash", Val: nonNil(e.Trash)})
	}
	if e.Exchange != nil {
		kv = append(kv, jsonord.KV{Key: "exchange", Val: e.Exchange})
	}
	return jsonord.Object(kv, nil)
}

func nonNil(p [][2]string) [][2]string {
	if p == nil {
		return [][2]string{}
	}
	return p
}

// UnmarshalJSON implements json.Unmarshaler. Unknown keys are ignored.
func (e *Event) UnmarshalJSON(data []byte) error {
	_, err := jsonord.Decode(data, map[string]any{
		"ev": &e.Ev, "at": (*jsonord.Stamp)(&e.At), "root": &e.Root, "item": &e.Item, "version": &e.Version,
		"from": &e.From, "to": &e.To, "file": &e.File, "offset": &e.Offset, "sha_state": &e.ShaState,
		"etag": &e.ETag, "last_modified": &e.LastModified, "url": &e.URL, "renames": &e.Renames, "trash": &e.Trash,
		"exchange": &e.Exchange,
	})
	return err
}

// validate is the check Append applies, so a caller bug fails loudly at the write-ahead point.
func (e Event) validate() error {
	if e.Ev == "" {
		return errors.New("journal: event has no ev")
	}
	needsItem := e.Ev == EvState || e.Ev == EvCkpt || e.Ev == EvSwapPlan || e.Ev == EvSwapDone
	if needsItem {
		if !ark.ValidRootID(e.Root) || !manifest.SafeName(e.Item) || !manifest.SafeName(e.Version) {
			return fmt.Errorf("journal: %s event needs a valid root, item and version, got %q %q %q", e.Ev, e.Root, e.Item, e.Version)
		}
	}
	switch e.Ev {
	case EvState:
		if !e.To.Valid() || (e.From != "" && !e.From.Valid()) {
			return fmt.Errorf("journal: state event %s/%s has invalid states %q -> %q", e.Root, e.Item, e.From, e.To)
		}
	case EvCkpt:
		if !manifest.ValidFilePath(e.File) || e.Offset < 0 {
			return fmt.Errorf("journal: ckpt event %s/%s has an invalid file %q or offset %d", e.Root, e.Item, e.File, e.Offset)
		}
	}
	return nil
}

var runIDRe = regexp.MustCompile(`^run-\d{8}T\d{6}Z-[0-9a-f]{4}$`)

// ValidRunID reports whether s has the form run-YYYYMMDDTHHMMSSZ-<4 hex>.
func ValidRunID(s string) bool { return runIDRe.MatchString(s) }

// NewRunID returns run-YYYYMMDDTHHMMSSZ-<4 hex> for the instant now (UTC), the random part read
// from rnd (crypto/rand when nil).
func NewRunID(now time.Time, rnd func(p []byte) (int, error)) (string, error) {
	if rnd == nil {
		rnd = rand.Read
	}
	var b [2]byte
	if _, err := io.ReadFull(readerFunc(rnd), b[:]); err != nil {
		return "", fmt.Errorf("journal: run id: %w", err)
	}
	return "run-" + now.UTC().Format("20060102T150405Z") + "-" + hex.EncodeToString(b[:]), nil
}

type readerFunc func(p []byte) (int, error)

func (f readerFunc) Read(p []byte) (int, error) { return f(p) }

// Runs manages the run log directory <journal dir>/runs.
type Runs struct {
	opts Options
	dir  string
}

// NewRuns returns the manager of the run logs under opts.Dir. Nothing is created until Start.
func NewRuns(opts Options) *Runs {
	opts = opts.withDefaults()
	return &Runs{opts: opts, dir: filepath.Join(opts.Dir, RunsDirName)}
}

// Dir is the run log directory.
func (r *Runs) Dir() string { return r.dir }

func (r *Runs) path(id string) string { return filepath.Join(r.dir, id+".jsonl") }

// List returns the ids of the run logs on disk, oldest first (the id sorts by time).
func (r *Runs) List() ([]string, error) {
	entries, err := r.opts.FS.ReadDir(r.dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var ids []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || filepath.Ext(name) != ".jsonl" {
			continue
		}
		if id := name[:len(name)-len(".jsonl")]; ValidRunID(id) {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids, nil
}

// Latest returns the id of the newest run log; ok is false when there is none.
func (r *Runs) Latest() (id string, ok bool, err error) {
	ids, err := r.List()
	if err != nil || len(ids) == 0 {
		return "", false, err
	}
	return ids[len(ids)-1], true, nil
}

// Start creates the log of a new run with a fresh id and returns it open for appending. The file
// and its directory entry are durable before Start returns. Afterwards the log directory is pruned
// to the newest KeepRuns files (the new one included): the only deletion this package does.
func (r *Runs) Start() (*Run, error) {
	if err := r.opts.FS.MkdirAll(r.dir, 0o755); err != nil {
		return nil, fmt.Errorf("journal: create %s: %w", r.dir, err)
	}
	var lastErr error
	for try := 0; try < 8; try++ {
		id, err := NewRunID(r.opts.Now(), r.opts.Rand)
		if err != nil {
			return nil, err
		}
		f, err := r.opts.FS.OpenFile(r.path(id), os.O_WRONLY|os.O_CREATE|os.O_EXCL|os.O_APPEND, 0o644)
		if errors.Is(err, fs.ErrExist) {
			lastErr = err
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("journal: create run log: %w", err)
		}
		if err := r.opts.FS.SyncDir(r.dir); err != nil && !isSyncDirUnsupported(err) {
			_ = f.Close()
			return nil, fmt.Errorf("journal: sync %s: %w", r.dir, err)
		}
		r.prune()
		return newRun(r, id, f), nil
	}
	return nil, fmt.Errorf("journal: no free run id: %w", lastErr)
}

// Open reopens an existing run log for appending (a restarted agent that carries on with the run it
// was in, or a replayer that records the outcome in the log it read).
func (r *Runs) Open(id string) (*Run, error) {
	if !ValidRunID(id) {
		return nil, fmt.Errorf("journal: %q is not a run id", id)
	}
	f, err := r.opts.FS.OpenFile(r.path(id), os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	run := newRun(r, id, f)
	run.dirty = true // the last line may be torn: the next event starts on a fresh line
	return run, nil
}

// prune removes the oldest run logs beyond KeepRuns. Failures are logged, never fatal.
func (r *Runs) prune() {
	ids, err := r.List()
	if err != nil || len(ids) <= r.opts.KeepRuns {
		return
	}
	for _, id := range ids[:len(ids)-r.opts.KeepRuns] {
		if err := r.opts.FS.Remove(r.path(id)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			r.opts.Logger.Warn("journal: could not remove an old run log", "run", id, "error", err)
		}
	}
}

func isSyncDirUnsupported(err error) bool {
	return errors.Is(err, syscall.EINVAL) || errors.Is(err, syscall.ENOTSUP)
}

// RunLog is a run log read back.
type RunLog struct {
	// Events are the lines that parsed, in order.
	Events []Event
	// Skipped counts lines that did not parse: the torn last line of a crash, or a line a failed
	// append left behind.
	Skipped int
}

// Read reads the run log id. A torn or garbage line does not stop the read: it is counted in Skipped.
func (r *Runs) Read(id string) (RunLog, error) {
	if !ValidRunID(id) {
		return RunLog{}, fmt.Errorf("journal: %q is not a run id", id)
	}
	data, err := r.opts.FS.ReadFile(r.path(id))
	if err != nil {
		return RunLog{}, err
	}
	return ParseRunLog(data)
}

// ParseRunLog parses the content of a run log.
func ParseRunLog(data []byte) (RunLog, error) {
	if len(data) > maxRunBytes {
		return RunLog{}, fmt.Errorf("journal: run log of %d bytes is too large", len(data))
	}
	var log RunLog
	for _, line := range bytes.Split(data, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var ev Event
		if err := json.Unmarshal(line, &ev); err != nil || ev.Ev == "" {
			log.Skipped++
			continue
		}
		log.Events = append(log.Events, ev)
	}
	return log, nil
}

// Run is one open run log. Append is safe for concurrent use.
type Run struct {
	// ID is the run id.
	ID string

	runs *Runs

	mu     sync.Mutex
	f      config.File
	dirty  bool // a previous write failed: the file may end in a torn line
	closed bool
}

func newRun(r *Runs, id string, f config.File) *Run { return &Run{ID: id, runs: r, f: f} }

// Append writes one event as one line and fsyncs it before it returns. This is the write-ahead
// point: the caller performs the side effect the event announces only after Append returned nil.
// When Append fails the caller must not perform the side effect. The line may nevertheless be on
// disk (the write worked and the sync failed): Replay tolerates an event whose side effect never
// happened, because the engine re-checks the disk before it trusts a state. After a failed
// append the next one starts on a fresh line, so a torn line never swallows a good event.
func (r *Run) Append(ev Event) error {
	if ev.At.IsZero() {
		ev.At = r.runs.opts.Now()
	}
	if err := ev.validate(); err != nil {
		return err
	}
	line, err := jsonord.Marshal(ev)
	if err != nil {
		return err
	}
	line = append(line, '\n')

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return errors.New("journal: run log is closed")
	}
	if r.dirty {
		line = append([]byte{'\n'}, line...)
	}
	n, err := r.f.Write(line)
	if err == nil && n != len(line) {
		err = io.ErrShortWrite
	}
	if err != nil {
		r.dirty = true
		return fmt.Errorf("journal: append to %s: %w", r.ID, err)
	}
	if err := r.f.Sync(); err != nil {
		r.dirty = true
		return fmt.Errorf("journal: sync %s: %w", r.ID, err)
	}
	r.dirty = false
	return nil
}

// Close closes the log. Appending afterwards is an error.
func (r *Run) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	r.closed = true
	return r.f.Close()
}

// SwapPlan is the planned rename list of a swap (ark-sync.md section 4.5.1).
type SwapPlan struct {
	Renames  [][2]string
	Trash    [][2]string
	Exchange [][2]string
}

// Intent is what a run log says one item was about to be, reconstructed by Replay.
type Intent struct {
	RootID, ItemID string
	// Version is the target version of the last event about the item.
	Version string
	// State is the state of the last state event's To: the state the item was entering when the run
	// ended, possibly without the side effect having happened. Empty when the log has no state event.
	State ItemState
	// Checkpoints are the last checkpoint of each file of the current version.
	Checkpoints map[string]Checkpoint
	// Swap is the plan of a swap that started and did not log swap_done; nil otherwise. A crash in
	// the middle of a swap is resumed from it (replay the renames, skip the ones already done).
	Swap *SwapPlan
	// At is the time of the last event about the item.
	At time.Time
}

// Replay folds the events of a run log into the intended state of each item (key: Key(root, item)).
// The write-ahead rule makes this exact: every transition is in the log before its side effect, so
// the last state event is where the item was going; items.json, written after the side effect, can
// only be a step behind. Events of other kinds, and events about no item, are ignored. A change of
// version starts the item's record afresh (checkpoints and swap plan belong to one version).
func Replay(events []Event) map[string]Intent {
	out := map[string]Intent{}
	for _, ev := range events {
		if ev.Root == "" || ev.Item == "" {
			continue
		}
		switch ev.Ev {
		case EvState, EvCkpt, EvSwapPlan, EvSwapDone:
		default:
			continue
		}
		key := Key(ev.Root, ev.Item)
		in, ok := out[key]
		if !ok || in.Version != ev.Version {
			in = Intent{RootID: ev.Root, ItemID: ev.Item, Version: ev.Version}
		}
		in.At = ev.At
		switch ev.Ev {
		case EvState:
			in.State = ev.To
			if ev.To == StatePlanned {
				in.Checkpoints, in.Swap = nil, nil
			}
		case EvCkpt:
			cp := make(map[string]Checkpoint, len(in.Checkpoints)+1)
			for k, v := range in.Checkpoints {
				cp[k] = v
			}
			cp[ev.File] = Checkpoint{Offset: ev.Offset, ShaState: ev.ShaState, ETag: ev.ETag, LastModified: ev.LastModified, URL: ev.URL}
			in.Checkpoints = cp
		case EvSwapPlan:
			in.Swap = &SwapPlan{Renames: ev.Renames, Trash: ev.Trash, Exchange: ev.Exchange}
		case EvSwapDone:
			in.Swap = nil
		}
		out[key] = in
	}
	return out
}

// ApplyIntents brings the records up to the intended states of a replay: for every intent with a
// state, the record takes the intent's state and version, and the checkpoints the log has (the
// journal's are newer than the record's). A record that is verified for the intent's version is left
// alone: verified is terminal, and the manifest decided it. It writes items.json once if anything
// changed and returns how many records changed.
func (it *Items) ApplyIntents(intents map[string]Intent) (int, error) {
	keys := make([]string, 0, len(intents))
	for k := range intents {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	it.mu.Lock()
	defer it.mu.Unlock()
	next := make(map[string]Item, len(it.items))
	for k, v := range it.items {
		next[k] = v
	}
	changed := 0
	for _, key := range keys {
		in := intents[key]
		if in.State == "" || !in.State.Valid() {
			continue
		}
		cur, ok := next[key]
		if ok && cur.State == StateVerified && cur.Version == in.Version {
			continue
		}
		rec := cur.clone()
		rec.RootID, rec.ItemID = in.RootID, in.ItemID
		if rec.Version != in.Version {
			rec.Attempts, rec.LastError, rec.Code, rec.PartialBytes, rec.Checkpoints = 0, "", "", 0, nil
		}
		same := ok && rec.State == in.State && rec.Version == in.Version
		rec.State, rec.Version = in.State, in.Version
		for file, cp := range in.Checkpoints {
			if old, had := rec.Checkpoints[file]; !had || !sameCheckpoint(old, cp) {
				if rec.Checkpoints == nil {
					rec.Checkpoints = map[string]Checkpoint{}
				}
				rec.Checkpoints[file] = cp
				same = false
			}
		}
		if same {
			continue
		}
		rec.UpdatedAt = it.opts.Now()
		next[key] = rec
		changed++
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

func sameCheckpoint(a, b Checkpoint) bool {
	return a.Offset == b.Offset && a.ShaState == b.ShaState && a.ETag == b.ETag && a.LastModified == b.LastModified && a.URL == b.URL
}
