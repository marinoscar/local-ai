package run

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/sync/journal"
)

// RootRunsName is the file in the journal directory that remembers, per root, the last run that
// touched it and when the last run that ended done finished: what the readiness report's last_sync
// and its age rules read (ark-sync.md section 4.12), kept across restarts.
const RootRunsName = "root-runs.json"

// ExtraCheckedCurrentAt is the key of the item's extras (journal.Item.Extras) under which a run
// records, as an RFC 3339 string, the last time it resolved the item and confirmed that the version on
// the root is the one wanted. The readiness report reads it (ark/ready.ExtraCheckedCurrentAt, the same
// key): an item that is not due for a refresh is as current as the last run that checked it.
const ExtraCheckedCurrentAt = "checked_current_at"

// RootRun is what the engine remembers about the runs of one root.
type RootRun struct {
	// RunID, Trigger, State and FinishedAt describe the last run that touched the root, whatever its
	// trigger and final state (a run skipped for lack of internet counts: it is the last thing that
	// happened).
	RunID      string    `json:"run_id"`
	Trigger    string    `json:"trigger"`
	State      string    `json:"state"`
	FinishedAt time.Time `json:"finished_at"`
	// LastDone is when the last run that ended done finished; zero when none did.
	LastDone time.Time `json:"last_done,omitempty"`
}

type rootRunsFile struct {
	Roots map[string]RootRun `json:"roots"`
}

func (e *Engine) rootRunsPath() string { return filepath.Join(e.d.JournalDir, RootRunsName) }

func (e *Engine) loadRootRuns() {
	e.rootRuns = map[string]RootRun{}
	data, err := e.d.FS.ReadFile(e.rootRunsPath())
	if err != nil {
		return
	}
	var f rootRunsFile
	if json.Unmarshal(data, &f) == nil && f.Roots != nil {
		e.rootRuns = f.Roots
	}
}

// noteRootRuns records the end of a run for the roots it touched. Caller holds e.mu.
func (e *Engine) noteRootRunsLocked(st RunStatus, rootIDs []string) {
	if st.FinishedAt == nil {
		return
	}
	for _, id := range rootIDs {
		rr := e.rootRuns[id]
		rr.RunID, rr.Trigger, rr.State, rr.FinishedAt = st.RunID, st.Trigger, st.State, *st.FinishedAt
		// A verify run checks what is stored; it does not bring the root up to date with the catalog, so
		// it does not move the clock the age rules of READY run on (ark-sync.md section 4.12.3).
		if st.State == StateDone && st.Trigger != TriggerVerify {
			rr.LastDone = *st.FinishedAt
		}
		e.rootRuns[id] = rr
	}
	data, err := json.MarshalIndent(rootRunsFile{Roots: e.rootRuns}, "", "  ")
	if err != nil {
		return
	}
	if err := config.WriteFileAtomicFS(e.d.FS, e.rootRunsPath(), append(data, '\n'), 0o644, -1, -1); err != nil && !errors.Is(err, config.ErrNotDurable) {
		e.d.Logger.Warn("root-runs.json not written", "component", "sync", "err", err.Error())
	}
}

// RootRun returns the last run that touched a root and when the last run that ended done finished
// (the readiness report's RunSource reads it through internal/sync/run/adapt). ok is false when no
// run is recorded for the root.
func (e *Engine) RootRun(rootID string) (RootRun, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	rr, ok := e.rootRuns[rootID]
	return rr, ok
}

// LastDriveSync is the newest time a run that ended done finished on any ark drive, mounted or not (the
// record in root-runs.json survives unplugging); zero when none did. The cloud snapshot's
// drive_last_synced_at reads it.
func (e *Engine) LastDriveSync() time.Time {
	e.mu.Lock()
	defer e.mu.Unlock()
	var newest time.Time
	for id, rr := range e.rootRuns {
		if strings.HasPrefix(id, "ark-") && rr.LastDone.After(newest) {
			newest = rr.LastDone
		}
	}
	return newest
}

// SyncState is the engine's state for status.ark.sync: "running" or "cancelling" while a run is active,
// "" when idle. It never reports a final state (done, cancelled, failed): finish publishes the final
// state while the run still holds the slot (the report hook and sync.done run before it is released), and
// a run that has settled is no longer syncing, so that window reads "" like the idle node it is about to
// be. The final state is Status's to report.
func (e *Engine) SyncState() string {
	e.mu.Lock()
	rs := e.active
	e.mu.Unlock()
	if rs == nil {
		return ""
	}
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if rs.finishedAt != nil {
		return ""
	}
	if rs.state == StateCancelling {
		return StateCancelling
	}
	// A run whose plan failed (state failed) still runs the end-of-run passes until finish settles it.
	return StateRunning
}

// markCurrent stamps the items a run confirmed to be current on a root with the time of the check
// (ExtraCheckedCurrentAt): the rows the plan kept with a resolved version, and the items a run
// verified. An item with no record is left alone (the readiness report falls back on the manifest).
func (e *Engine) markCurrent(rootID string, itemIDs []string, at time.Time) {
	stamp, _ := json.Marshal(at.UTC().Format(time.RFC3339))
	for _, id := range itemIDs {
		if _, ok := e.d.Journal.Items.Get(rootID, id); !ok {
			continue
		}
		if _, err := e.d.Journal.Items.Update(rootID, id, func(i *journal.Item) {
			extras := make(map[string]json.RawMessage, len(i.Extras)+1)
			for k, v := range i.Extras {
				extras[k] = v
			}
			extras[ExtraCheckedCurrentAt] = stamp
			i.Extras = extras
		}); err != nil {
			e.d.Logger.Warn("checked_current_at not recorded", "component", "sync", "root", rootID, "item", id, "err", err.Error())
		}
	}
}
