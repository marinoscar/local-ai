package ensure

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/config"
)

// JournalFile is the name of the journal under the agent state folder (/var/lib/localai/ensure/).
const JournalFile = "journal.json"

// journalVersion is the schema of the file.
const journalVersion = 1

// journalRun is the latest pass: how it started, every step with its status, how it ended.
type journalRun struct {
	Run
	Steps []Step `json:"steps"`
}

type journalDoc struct {
	Version int         `json:"version"`
	Run     *journalRun `json:"run,omitempty"`
	// PendingRestart lists the services whose program was replaced while they ran (kiwix after
	// kiwix-tools): the plan offers a restart that needs a confirmation, and never does it alone.
	PendingRestart []string `json:"pending_restart,omitempty"`
}

// Journal is the state file of the engine: written before a step starts and after it ends, atomically, so
// a crash leaves a file that says what was running. The next start closes that pass as interrupted and the
// next pass plans again from the machine itself (every step is idempotent), never from the journal.
type Journal struct {
	Path string
	FS   config.FS

	mu  sync.Mutex
	doc journalDoc
}

// OpenJournal reads the file (a missing or damaged one is an empty journal) and closes a pass that a crash
// left open. recovered says that one was.
func OpenJournal(fsys config.FS, path string, now time.Time) (j *Journal, recovered *Run, err error) {
	if fsys == nil {
		fsys = config.OSFS{}
	}
	j = &Journal{Path: path, FS: fsys, doc: journalDoc{Version: journalVersion}}
	data, rerr := fsys.ReadFile(path)
	switch {
	case rerr == nil:
		var d journalDoc
		if json.Unmarshal(data, &d) == nil && d.Version == journalVersion {
			j.doc = d
		}
	case !errors.Is(rerr, fs.ErrNotExist):
		return j, nil, fmt.Errorf("ensure: cannot read the journal: %w", rerr)
	}
	if r := j.doc.Run; r != nil && r.FinishedAt == nil {
		t := now.UTC()
		r.FinishedAt, r.Result = &t, ResultFailed
		for i := range r.Steps {
			switch r.Steps[i].Status {
			case StatusRunning:
				r.Steps[i].Status, r.Steps[i].Error, r.Steps[i].FinishedAt = StatusFailed, "interrupted: the agent stopped while this was running", &t
			case StatusPending, StatusWaiting:
				r.Steps[i].Status = StatusCancelled
			}
		}
		run := r.Run
		if err := j.saveLocked(); err != nil {
			return j, &run, err
		}
		return j, &run, nil
	}
	return j, nil, nil
}

func (j *Journal) saveLocked() error {
	b, err := json.MarshalIndent(j.doc, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(j.Path), 0o755); err != nil {
		return fmt.Errorf("ensure: cannot create the journal folder: %w", err)
	}
	return config.WriteFileAtomicFS(j.FS, j.Path, append(b, '\n'), 0o640, -1, -1)
}

// Begin records a new pass with its steps.
func (j *Journal) Begin(run Run, steps []Step) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.doc.Run = &journalRun{Run: run, Steps: append([]Step(nil), steps...)}
	return j.saveLocked()
}

// Step records the new state of one step of the open pass.
func (j *Journal) Step(s Step) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.doc.Run == nil {
		return nil
	}
	for i := range j.doc.Run.Steps {
		if j.doc.Run.Steps[i].ID == s.ID {
			j.doc.Run.Steps[i] = s
			return j.saveLocked()
		}
	}
	j.doc.Run.Steps = append(j.doc.Run.Steps, s)
	return j.saveLocked()
}

// Finish closes the open pass.
func (j *Journal) Finish(result Result, at time.Time) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.doc.Run == nil {
		return nil
	}
	t := at.UTC()
	j.doc.Run.FinishedAt, j.doc.Run.Result = &t, result
	return j.saveLocked()
}

// Last returns a copy of the latest pass, if any.
func (j *Journal) Last() (Run, []Step, bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.doc.Run == nil {
		return Run{}, nil, false
	}
	return j.doc.Run.Run, append([]Step(nil), j.doc.Run.Steps...), true
}

// PendingRestart lists the services waiting for a restart.
func (j *Journal) PendingRestart() []string {
	j.mu.Lock()
	defer j.mu.Unlock()
	return append([]string(nil), j.doc.PendingRestart...)
}

// SetPendingRestart adds (on true) or removes a service from the restart list.
func (j *Journal) SetPendingRestart(service string, pending bool) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	set := map[string]bool{}
	for _, s := range j.doc.PendingRestart {
		set[s] = true
	}
	if set[service] == pending {
		return nil
	}
	if pending {
		set[service] = true
	} else {
		delete(set, service)
	}
	j.doc.PendingRestart = j.doc.PendingRestart[:0]
	for s := range set {
		j.doc.PendingRestart = append(j.doc.PendingRestart, s)
	}
	sort.Strings(j.doc.PendingRestart)
	return j.saveLocked()
}
