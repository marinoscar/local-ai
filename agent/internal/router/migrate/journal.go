package migrate

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/state"
)

// Step names.
const (
	M0  = "M0"
	M1  = "M1"
	M2  = "M2"
	M3  = "M3"
	M4  = "M4"
	M5  = "M5"
	M6  = "M6"
	M7  = "M7"
	M8  = "M8"
	M9  = "M9"
	M10 = "M10"
)

// forwardSteps are M0 to M10 in order.
var forwardSteps = []string{M0, M1, M2, M3, M4, M5, M6, M7, M8, M9, M10}

// rollbackSteps are R1 to R6 in order.
var rollbackSteps = []string{"R1", "R2", "R3", "R4", "R5", "R6"}

func stepIndex(list []string, s string) int {
	for i, v := range list {
		if v == s {
			return i
		}
	}
	return -1
}

// Progress is progress.json (llama-router.md section 4.9): the last step that completed, when, and the
// time the old service was stopped. The extra fields make a run that is killed resumable: which direction
// it was going, whether the agent was stopped by it, and the options of the rollback. A re-run continues
// from the recorded step.
type Progress struct {
	// Step is the last forward step that completed ("" before M0).
	Step string    `json:"step"`
	At   time.Time `json:"at"`
	// T0 is when the old service was stopped (M6); TLoaded when the new model was loaded (M7).
	T0      *time.Time `json:"t0,omitempty"`
	TLoaded *time.Time `json:"t_loaded,omitempty"`
	// Rollback is "R0" when a rollback started and the last rollback step that completed after that;
	// "R6" means the rollback is finished. Empty while going forward.
	Rollback string `json:"rollback,omitempty"`
	// Cause says why the rollback runs: "requested" or the failure that triggered it.
	Cause string `json:"cause,omitempty"`
	// RestoreFiles is the --restore-files choice of the rollback in progress.
	RestoreFiles bool `json:"restore_files,omitempty"`
	// AgentStopped is true when this migration stopped localai-agent.service and has not started it
	// again: a resumed run starts it at the end.
	AgentStopped bool `json:"agent_stopped,omitempty"`
}

// Direction of a recorded progress.
type direction int

const (
	dirFresh       direction = iota // nothing recorded, or a finished rollback: a new migration
	dirForward                      // a migration is in progress
	dirDone                         // M10 completed
	dirRollingBack                  // a rollback is in progress
)

func (p Progress) direction() direction {
	switch {
	case p.Rollback == "R6":
		return dirFresh
	case p.Rollback != "":
		return dirRollingBack
	case p.Step == M10:
		return dirDone
	case p.Step == "" && p.At.IsZero():
		return dirFresh
	}
	return dirForward
}

// readProgress reads progress.json; ok is false when there is none.
func (e *Engine) readProgress() (Progress, bool, error) {
	var p Progress
	data, err := e.fs.ReadFile(e.paths.Progress())
	if errors.Is(err, fs.ErrNotExist) {
		return p, false, nil
	}
	if err != nil {
		return p, false, wrap(CodeInterrupted, "", err, "cannot read %s", e.paths.Progress())
	}
	if err := json.Unmarshal(data, &p); err != nil {
		return p, false, wrap(CodeInterrupted, "", err, "%s is damaged; move it aside to start the migration again", e.paths.Progress())
	}
	return p, true, nil
}

// saveProgress writes progress.json atomically and mirrors it into state.json (best effort: the file is
// the authority, state.json is for the doctor).
func (e *Engine) saveProgress(p Progress) error {
	p.At = e.clock.Now().UTC().Truncate(time.Second)
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	if err := e.writeJournal(e.paths.Progress(), append(data, '\n')); err != nil {
		return wrap(CodeInterrupted, p.Step, err, "cannot record the progress")
	}
	e.prog = p
	e.mirrorState(p)
	return nil
}

// Migration states of state.migration.llama_qwen.
const (
	StateInProgress  = "in_progress"
	StateRollingBack = "rolling_back"
	StateDone        = "done"
	StateRolledBack  = "rolled_back"
)

func (e *Engine) mirrorState(p Progress) {
	entry := state.MigrationEntry{At: p.At}
	switch {
	case p.Rollback == "R6":
		entry.State, entry.Step = StateRolledBack, "R6"
	case p.Rollback != "":
		entry.State, entry.Step = StateRollingBack, p.Rollback
	case p.Step == M9 || p.Step == M10:
		// M9 commits the migration: from there on it is done, M10 only reports.
		entry.State, entry.Step = StateDone, p.Step
	default:
		entry.State, entry.Step = StateInProgress, p.Step
	}
	if err := e.state.SetMigration(entry); err != nil {
		e.warnf("cannot record the migration in state.json: %v", err)
	}
}

// writeJournal writes one of the migration's own files (root 0600, in a root 0700 directory).
func (e *Engine) writeJournal(p string, data []byte) error {
	if err := e.fs.MkdirAll(e.paths.MigDir, 0o700); err != nil {
		return err
	}
	if dir := filepath.Dir(p); dir != e.paths.MigDir {
		if err := e.fs.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	return config.WriteFileAtomicFS(e.fs, p, data, 0o600, e.rootUID, e.rootGID)
}

// savePlan writes plan.json and serve-before.json (the plan carries no secret: the key's fingerprint
// only).
func (e *Engine) savePlan(p *Plan) error {
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	if err := e.writeJournal(e.paths.Plan(), append(data, '\n')); err != nil {
		return wrap(CodeInterrupted, M0, err, "cannot write plan.json")
	}
	if err := e.writeJournal(e.paths.ServeBefore(), append([]byte(nil), p.ServeBefore...)); err != nil {
		return wrap(CodeInterrupted, M0, err, "cannot write serve-before.json")
	}
	return nil
}

// loadPlan reads plan.json and serve-before.json back for a resumed run.
func (e *Engine) loadPlan() (*Plan, error) {
	data, err := e.fs.ReadFile(e.paths.Plan())
	if err != nil {
		return nil, wrap(CodeInterrupted, "", err, "the plan of the interrupted migration is missing (%s); roll back with --rollback", e.paths.Plan())
	}
	p := &Plan{}
	if err := json.Unmarshal(data, p); err != nil {
		return nil, wrap(CodeInterrupted, "", err, "plan.json is damaged; roll back with --rollback")
	}
	if p.Version != planVersion {
		return nil, fail(CodeInterrupted, "", "plan.json has version %d, this program reads %d", p.Version, planVersion)
	}
	sb, err := e.fs.ReadFile(e.paths.ServeBefore())
	if err != nil {
		return nil, wrap(CodeInterrupted, "", err, "serve-before.json is missing")
	}
	p.ServeBefore = sb
	return p, nil
}

func (e *Engine) removeFile(path string) error {
	if err := e.fs.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("remove %s: %w", path, err)
	}
	return nil
}
