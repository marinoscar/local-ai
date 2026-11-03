package journal

import (
	"context"
	"fmt"
)

// Journal is the items file and the run logs of one node, opened together.
type Journal struct {
	// Items is items.json.
	Items *Items
	// Runs manages the run logs.
	Runs *Runs
}

// Recovery is what Open found out about the last run, for the engine to resume from.
type Recovery struct {
	Report
	// ReplayedRun is the id of the run log that was replayed ("" when there was none).
	ReplayedRun string
	// Skipped counts torn or unreadable lines of that log.
	Skipped int
	// Intents is the intended state of every item the replayed run touched, keyed by Key(root, item):
	// the in-flight swap plan, the checkpoints, the state the item was entering. The engine resumes
	// each non-terminal intent (ark-sync.md section 5) after checking the disk, because an intended
	// state may be one whose side effect never happened.
	Intents map[string]Intent
	// Applied is how many records the replay changed.
	Applied int
}

// Open opens the journal of a restarted (or first-starting) agent, in the order that loses nothing:
//
//  1. items.json is loaded, repaired from its backup or rebuilt from the manifests when it is
//     damaged or missing (LAI-SYN-050 in Recovery.Problems);
//  2. the newest run log is replayed, and its intended states are written into items.json (a record
//     that is verified for its version stays verified), so a crash between a journal append and the
//     side effect it announced is recovered;
//
// and only then may the caller Runs.Start the new run, so the log that carries the recovery
// information is never preceded by an empty one. Replaying twice is harmless: a crash during Open
// replays the same log on the next start.
func Open(ctx context.Context, opts Options) (*Journal, Recovery, error) {
	items, rep, err := OpenItems(ctx, opts)
	if err != nil {
		return nil, Recovery{Report: rep}, err
	}
	j := &Journal{Items: items, Runs: NewRuns(opts)}
	rec := Recovery{Report: rep}
	id, ok, err := j.Runs.Latest()
	if err != nil {
		return nil, rec, fmt.Errorf("journal: list run logs: %w", err)
	}
	if !ok {
		return j, rec, nil
	}
	log, err := j.Runs.Read(id)
	if err != nil {
		return nil, rec, fmt.Errorf("journal: read run log %s: %w", id, err)
	}
	rec.ReplayedRun, rec.Skipped = id, log.Skipped
	rec.Intents = Replay(log.Events)
	if rec.Applied, err = items.ApplyIntents(rec.Intents); err != nil {
		return nil, rec, fmt.Errorf("journal: apply the replay of %s: %w", id, err)
	}
	return j, rec, nil
}

// Transition journals a state change and then updates the record, in that order.
// fn edits the record besides its state (attempts, error, checkpoints); it may be nil. The event is
// durable before the record changes; when Append fails, nothing else happens. The side effect the
// transition announces is the caller's, performed after Transition returns.
func (j *Journal) Transition(run *Run, rootID, itemID, version string, to ItemState, fn func(*Item)) (Item, error) {
	from := ItemState("")
	if cur, ok := j.Items.Get(rootID, itemID); ok {
		from = cur.State
	}
	if err := run.Append(Event{Ev: EvState, Root: rootID, Item: itemID, Version: version, From: from, To: to}); err != nil {
		return Item{}, err
	}
	return j.Items.Update(rootID, itemID, func(i *Item) {
		i.State, i.Version = to, version
		if fn != nil {
			fn(i)
		}
	})
}
