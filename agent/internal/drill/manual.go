package drill

import (
	"context"
	"fmt"
	"time"
	"unicode"
	"unicode/utf8"
)

// The manual quarterly "pull the plug" drill record (DRILL-06, drill.md section 4.6). Software cannot
// simulate an unplugged restart, so a person does it by hand and records the outcome:
// `aictl drill record-manual --result pass|fail --notes "..."` over POST /drill/manual. The record is
// a results document with trigger manual_plug and no checks.

// Limits of a manual record.
const (
	// MaxManualNotes is the longest notes text, in characters.
	MaxManualNotes = 2000
	// manualFutureSlack is how far ahead of the clock the at time of a record may be (clocks of the
	// node and of the person's computer differ a little).
	manualFutureSlack = 5 * time.Minute
	// manualMaxAge is how far back at may reach: the retention keeps 24 documents, so a record older
	// than a year could be pruned the moment it is stored.
	manualMaxAge = 366 * 24 * time.Hour
)

// The results a manual record carries.
const (
	ManualPass = OverallPass
	ManualFail = OverallFail
)

// ManualRequest is a manual_plug record to store.
type ManualRequest struct {
	// Result is "pass" or "fail".
	Result string
	// Notes is free text, at most MaxManualNotes characters. It stays in the document on the node.
	Notes string
	// At is when the drill was done; the zero value means now.
	At time.Time
}

// ManualRecord is what RecordManual stored.
type ManualRecord struct {
	RunID string
	At    time.Time
}

// RecordManual validates a manual record, stores it as <run-id>.json in the drill directory (it never
// moves latest.json, and it does not write state.drill.last_run_at or last_result: those describe
// completed runs of the drill itself) and tells the observer. The run id is
// drl-<at>-<4hex>-manual. A request the engine cannot honor is an *InvalidRequestError; a stopping
// engine is ErrClosing.
func (e *Engine) RecordManual(_ context.Context, req ManualRequest) (ManualRecord, error) {
	switch req.Result {
	case ManualPass, ManualFail:
	default:
		return ManualRecord{}, &InvalidRequestError{Field: "result", Reason: "invalid_result"}
	}
	if err := validateNotes(req.Notes); err != nil {
		return ManualRecord{}, err
	}
	now := e.now()
	at := now
	if !req.At.IsZero() {
		at = req.At.UTC().Truncate(time.Second)
		if at.After(now.Add(manualFutureSlack)) {
			return ManualRecord{}, &InvalidRequestError{Field: "at", Reason: "in_the_future"}
		}
		if at.Before(now.Add(-manualMaxAge)) {
			return ManualRecord{}, &InvalidRequestError{Field: "at", Reason: "too_old"}
		}
	}

	e.mu.Lock()
	closing := e.closing
	e.mu.Unlock()
	if closing {
		return ManualRecord{}, ErrClosing
	}

	id := e.newIDWith(at, "", "-manual")
	doc := &Document{
		SchemaVersion: SchemaVersion, RunID: id, Trigger: TriggerManualPlug,
		NodeID: e.d.NodeID, AgentVersion: e.d.AgentVersion, CatalogVersion: e.catalogVersion(),
		Mode: e.modeName(), StartedAt: at, FinishedAt: &at,
		Overall: req.Result, SurvivalReady: false,
		Checks: []CheckResult{}, Notes: req.Notes,
	}
	if err := e.d.Results.Save(doc); err != nil {
		return ManualRecord{}, fmt.Errorf("drill: store the manual record: %w", err)
	}
	e.log.Info("manual drill recorded", "run_id", id, "result", req.Result, "notes_chars", utf8.RuneCountInString(req.Notes))
	e.observe(doc)
	return ManualRecord{RunID: id, At: at}, nil
}

// validateNotes checks the notes: valid UTF-8, at most MaxManualNotes characters, and no control
// character other than a line break or a tab (the text is shown in terminals and the Portal).
func validateNotes(notes string) error {
	if !utf8.ValidString(notes) {
		return &InvalidRequestError{Field: "notes", Reason: "invalid_text"}
	}
	if utf8.RuneCountInString(notes) > MaxManualNotes {
		return &InvalidRequestError{Field: "notes", Reason: "notes_too_long"}
	}
	for _, r := range notes {
		if unicode.IsControl(r) && r != '\n' && r != '\r' && r != '\t' {
			return &InvalidRequestError{Field: "notes", Reason: "invalid_text"}
		}
	}
	return nil
}
