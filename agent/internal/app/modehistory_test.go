package app

import (
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/ark/ready"
	"github.com/marinoscar/local-ai/agent/internal/state"
	statefake "github.com/marinoscar/local-ai/agent/internal/state/fake"
)

// READY's "was the node online for most of the window" answer (ark-sync.md section 4.12.2) read from
// the state store through the adapter the daemon passes as ready.Deps.Modes: a node that was offline
// or in survival on purpose must not get the no-recent-sync warning, a node with no record of that
// must.
func TestReadyModesFromTheStateStore(t *testing.T) {
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	from := now.Add(-20 * 24 * time.Hour)
	ago := func(days float64) time.Time { return now.Add(-time.Duration(days * 24 * float64(time.Hour))) }
	entry := func(at time.Time, m state.Mode) state.ModeHistoryEntry {
		return state.ModeHistoryEntry{At: at, Mode: m}
	}
	tests := []struct {
		name string
		seed func(*state.State)
		want bool // MostlyOnline
	}{
		{"online and never changed", func(s *state.State) {}, true},
		{"only ever offline, nothing recorded", func(s *state.State) { s.Mode = state.ModeOffline }, false},
		{"offline since a change before the history existed", func(s *state.State) {
			at := ago(30)
			s.Mode, s.ModeChangedAt = state.ModeOffline, &at
		}, false},
		{"offline for the last 12 of 20 days", func(s *state.State) {
			s.Mode = state.ModeOffline
			s.ModeHistory = []state.ModeHistoryEntry{entry(ago(12), state.ModeOffline)}
		}, false},
		{"offline for the last 9 of 20 days", func(s *state.State) {
			s.Mode = state.ModeOffline
			s.ModeHistory = []state.ModeHistoryEntry{entry(ago(9), state.ModeOffline)}
		}, true},
		{"offline, then back online 11 days ago", func(s *state.State) {
			s.ModeHistory = []state.ModeHistoryEntry{entry(ago(30), state.ModeOffline), entry(ago(11), state.ModeOnline)}
		}, true},
		{"survival most of the window, now online", func(s *state.State) {
			s.ModeHistory = []state.ModeHistoryEntry{entry(ago(19), state.ModeSurvival), entry(ago(2), state.ModeOnline)}
		}, false},
		{"unknown start, back online 5 days ago", func(s *state.State) {
			s.ModeHistory = []state.ModeHistoryEntry{entry(time.Time{}, state.ModeOffline), entry(ago(5), state.ModeOnline)}
		}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st, err := state.Open("/var/lib/localai/state.json", state.Options{FS: statefake.New(), Now: func() time.Time { return now }})
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			if err := st.MutateSync(func(s *state.State) error { tt.seed(s); return nil }); err != nil {
				t.Fatal(err)
			}
			modes := readyModes(st)
			if modes == nil {
				t.Fatal("no history source for a real store")
			}
			if got := ready.MostlyOnline(modes(), from, now); got != tt.want {
				t.Errorf("MostlyOnline = %v, want %v (changes %v)", got, tt.want, modes())
			}
		})
	}
	if readyModes(nil) != nil {
		t.Error("a missing store must leave ready.Deps.Modes nil")
	}
}
