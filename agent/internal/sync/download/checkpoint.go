package download

import (
	"crypto/sha256"
	"encoding"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"hash"
	"sync"

	"github.com/marinoscar/local-ai/agent/internal/sync/journal"
)

// Checkpoint is the resume point of one file: the offset of the last byte that is durable in the
// .part, the base64 of the marshalled sha256 state at that offset, and the validators and URL of
// the response it came from. It is the journal's type; headers are never part of it.
type Checkpoint = journal.Checkpoint

// Checkpointer persists the resume points of one (root, item, version). Fetch loads the point of a
// file when it starts, saves one every journal.CheckpointEvery bytes, whenever it stops with a
// .part on disk (cancel, transient failure, disk skip), and at the end; it clears the point when it
// restarts a file from zero, when the bytes were rejected, and when the .part is committed.
//
// Save and Clear must be durable when they return nil. A Save that fails is logged and the download
// goes on (the previous point is still consistent); the file is simply resumed from there.
type Checkpointer interface {
	// Load returns the saved point of file (the file's path in the item), ok false when there is none.
	Load(file string) (Checkpoint, bool)
	// Save stores the point of file.
	Save(file string, c Checkpoint) error
	// Clear forgets the point of file.
	Clear(file string) error
}

// ErrNoRecord: the journal has no record of the item at this version, so a checkpoint has nowhere
// to live. The engine must journal the transition to downloading (journal.Transition) first.
var ErrNoRecord = errors.New("download: the journal has no record of this item at this version")

// JournalCheckpointer is the Checkpointer over the sync journal (ark-sync.md section 4.5.2): a
// checkpoint is a "ckpt" event appended to the run log first (write-ahead) and then written through
// items.json as Item.Checkpoints and Item.PartialBytes (the sum of the offsets of the item's files).
type JournalCheckpointer struct {
	// Items is items.json.
	Items *journal.Items
	// Run is the current run's log; nil skips the events (the record is still written).
	Run *journal.Run
	// RootID, ItemID and Version name the record; the record must exist at this version.
	RootID, ItemID, Version string
}

// Load implements Checkpointer. A record at another version has no points for this one.
func (c *JournalCheckpointer) Load(file string) (Checkpoint, bool) {
	rec, ok := c.Items.Get(c.RootID, c.ItemID)
	if !ok || rec.Version != c.Version {
		return Checkpoint{}, false
	}
	cp, ok := rec.Checkpoints[file]
	if !ok || cp.Offset <= 0 || cp.ShaState == "" {
		return Checkpoint{}, false
	}
	return cp, true
}

// Save implements Checkpointer.
func (c *JournalCheckpointer) Save(file string, cp Checkpoint) error {
	return c.put(file, cp, false)
}

// Clear implements Checkpointer: an event with offset 0 and no state (which Replay folds into "no
// point") and the entry removed from the record.
func (c *JournalCheckpointer) Clear(file string) error {
	return c.put(file, Checkpoint{}, true)
}

func (c *JournalCheckpointer) put(file string, cp Checkpoint, clear bool) error {
	rec, ok := c.Items.Get(c.RootID, c.ItemID)
	if !ok || rec.Version != c.Version {
		return ErrNoRecord
	}
	if clear {
		if _, has := rec.Checkpoints[file]; !has {
			return nil
		}
	}
	if c.Run != nil {
		err := c.Run.Append(journal.Event{
			Ev: journal.EvCkpt, Root: c.RootID, Item: c.ItemID, Version: c.Version, File: file,
			Offset: cp.Offset, ShaState: cp.ShaState, ETag: cp.ETag, LastModified: cp.LastModified, URL: cp.URL,
		})
		if err != nil {
			return err
		}
	}
	_, err := c.Items.Update(c.RootID, c.ItemID, func(i *journal.Item) {
		cps := make(map[string]journal.Checkpoint, len(i.Checkpoints)+1)
		for k, v := range i.Checkpoints {
			cps[k] = v
		}
		if clear {
			delete(cps, file)
		} else {
			cps[file] = cp
		}
		var sum int64
		for _, v := range cps {
			sum += v.Offset
		}
		i.Checkpoints, i.PartialBytes = cps, sum
	})
	return err
}

// MemoryCheckpointer keeps the points in memory: for callers with no journal (a resolver fetching a
// small metadata file) and for tests. It survives retries inside one process, not a restart.
type MemoryCheckpointer struct {
	mu sync.Mutex
	m  map[string]Checkpoint
	// Saves counts the Save calls (tests assert the cadence).
	Saves int
}

// Load implements Checkpointer.
func (c *MemoryCheckpointer) Load(file string) (Checkpoint, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	cp, ok := c.m[file]
	return cp, ok && cp.Offset > 0
}

// Save implements Checkpointer.
func (c *MemoryCheckpointer) Save(file string, cp Checkpoint) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = map[string]Checkpoint{}
	}
	c.m[file] = cp
	c.Saves++
	return nil
}

// Clear implements Checkpointer.
func (c *MemoryCheckpointer) Clear(file string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.m, file)
	return nil
}

// Get returns the stored point (tests).
func (c *MemoryCheckpointer) Get(file string) (Checkpoint, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	cp, ok := c.m[file]
	return cp, ok
}

// shaLen is the length of the marshalled crypto/sha256 state: the 4-byte magic, the eight 32-bit
// words, the 64-byte block buffer and the 8-byte count of bytes hashed.
const shaLen = 4 + 8*4 + 64 + 8

// encodeSHA marshals a sha256 state to base64 for the journal.
func encodeSHA(h hash.Hash) (string, error) {
	m, ok := h.(encoding.BinaryMarshaler)
	if !ok {
		return "", errors.New("download: the hash cannot be marshalled")
	}
	b, err := m.MarshalBinary()
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(b), nil
}

// decodeSHA restores a sha256 state and checks that it covers exactly offset bytes: a state from a
// different offset (a stale checkpoint) is refused instead of silently producing a wrong digest.
func decodeSHA(s string, offset int64) (hash.Hash, error) {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("download: checkpoint state is not base64: %w", err)
	}
	if len(b) != shaLen {
		return nil, fmt.Errorf("download: checkpoint state has %d bytes, want %d", len(b), shaLen)
	}
	if n := int64(binary.BigEndian.Uint64(b[len(b)-8:])); n != offset {
		return nil, fmt.Errorf("download: checkpoint state covers %d bytes but the offset is %d", n, offset)
	}
	h := sha256.New()
	u, ok := h.(encoding.BinaryUnmarshaler)
	if !ok {
		return nil, errors.New("download: the hash cannot be restored")
	}
	if err := u.UnmarshalBinary(b); err != nil {
		return nil, fmt.Errorf("download: checkpoint state: %w", err)
	}
	return h, nil
}
