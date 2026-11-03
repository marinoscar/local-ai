package state

import (
	"encoding/json"
	"time"
)

// The bounds of State.ModeHistory (agent.md section 3.5): the newest 50 changes, and nothing older
// than 90 days except the one entry that says which mode was in force when that period began.
const (
	ModeHistoryMax  = 50
	ModeHistoryDays = 90
)

// ModeHistoryEntry is one committed mode change: the mode the node changed to, and when. A zero At
// is a baseline whose start is unknown (a node that was already not online when the history began).
type ModeHistoryEntry struct {
	At    time.Time                  `json:"at"`
	Mode  Mode                       `json:"mode"`
	Extra map[string]json.RawMessage `json:"-"`
}

// UnmarshalJSON implements json.Unmarshaler.
func (v *ModeHistoryEntry) UnmarshalJSON(b []byte) error {
	type plain ModeHistoryEntry
	return decodeKeepExtra(b, (*plain)(v), &v.Extra)
}

// MarshalJSON implements json.Marshaler.
func (v ModeHistoryEntry) MarshalJSON() ([]byte, error) {
	type plain ModeHistoryEntry
	return encodeKeepExtra(plain(v), v.Extra)
}

// RecordModeChange appends the change to mode `to` at time `at` and prunes the list. Call it inside
// the same mutation that sets Mode, before Mode is overwritten, so the history and the mode are
// committed by one atomic write. A call that does not change the mode records nothing.
//
// A file written before the history existed has none: the first change then seeds the entry for the
// mode the node was in, from mode_changed_at when there is one, else from an unknown start (zero
// time), unless that mode is online, which is what readers assume before the first entry.
func (s *State) RecordModeChange(at time.Time, to Mode) {
	if len(s.ModeHistory) == 0 {
		switch {
		case s.ModeChangedAt != nil:
			s.ModeHistory = append(s.ModeHistory, ModeHistoryEntry{At: *s.ModeChangedAt, Mode: s.Mode})
		case s.Mode != ModeOnline:
			s.ModeHistory = append(s.ModeHistory, ModeHistoryEntry{Mode: s.Mode})
		}
	}
	if n := len(s.ModeHistory); n > 0 {
		last := s.ModeHistory[n-1]
		if last.Mode == to {
			return
		}
		if at.Before(last.At) {
			at = last.At // a clock that stepped back must not break the order
		}
	} else if to == ModeOnline {
		return
	}
	s.ModeHistory = append(s.ModeHistory, ModeHistoryEntry{At: at, Mode: to})
	s.pruneModeHistory(at)
}

// pruneModeHistory drops the oldest entries beyond ModeHistoryMax, and those that ended more than
// ModeHistoryDays before now, always keeping the entry in force at the start of that period (the
// baseline) so the mode at the start of the retained period is still known.
func (s *State) pruneModeHistory(now time.Time) {
	h := s.ModeHistory
	cutoff := now.AddDate(0, 0, -ModeHistoryDays)
	drop := 0
	for drop+1 < len(h) && h[drop+1].At.Before(cutoff) {
		drop++
	}
	if over := len(h) - drop - ModeHistoryMax; over > 0 {
		drop += over
	}
	if drop > 0 {
		s.ModeHistory = append([]ModeHistoryEntry{}, h[drop:]...)
	}
}

// ModeTimeline returns the mode changes oldest first, for a reader that wants the history of the
// node: a copy of ModeHistory, or, when none was recorded and the node is not online, the one entry
// for its current mode (since mode_changed_at, else from an unknown start). An empty result means
// the node has only ever been online as far as anything is recorded.
func (s State) ModeTimeline() []ModeHistoryEntry {
	if len(s.ModeHistory) > 0 {
		return append([]ModeHistoryEntry{}, s.ModeHistory...)
	}
	if s.Mode == ModeOnline {
		return []ModeHistoryEntry{}
	}
	e := ModeHistoryEntry{Mode: s.Mode}
	if s.ModeChangedAt != nil {
		e.At = *s.ModeChangedAt
	}
	return []ModeHistoryEntry{e}
}
