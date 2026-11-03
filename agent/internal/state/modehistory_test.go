package state_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/state"
)

var hist0 = time.Date(2026, 11, 1, 8, 0, 0, 0, time.UTC)

func hAt(d time.Duration) time.Time { return hist0.Add(d) }

const hDay = 24 * time.Hour

type hStep struct {
	at time.Time
	to state.Mode
}

// commit does what the mode engine's persist step does: record, then set the mode.
func commit(s *state.State, st hStep) {
	s.RecordModeChange(st.at, st.to)
	at := st.at
	s.Mode, s.ModeChangedAt = st.to, &at
}

func entries(pairs ...any) []state.ModeHistoryEntry {
	out := []state.ModeHistoryEntry{}
	for i := 0; i < len(pairs); i += 2 {
		out = append(out, state.ModeHistoryEntry{At: pairs[i].(time.Time), Mode: pairs[i+1].(state.Mode)})
	}
	return out
}

func TestRecordModeChange(t *testing.T) {
	changedAt := hAt(-30 * hDay)
	tests := []struct {
		name  string
		start func() state.State
		steps []hStep
		want  []state.ModeHistoryEntry
	}{
		{"online to offline", func() state.State { return state.State{Mode: state.ModeOnline} },
			[]hStep{{hAt(time.Hour), state.ModeOffline}},
			entries(hAt(time.Hour), state.ModeOffline)},
		{"online, offline, online", func() state.State { return state.State{Mode: state.ModeOnline} },
			[]hStep{{hAt(time.Hour), state.ModeOffline}, {hAt(5 * time.Hour), state.ModeOnline}},
			entries(hAt(time.Hour), state.ModeOffline, hAt(5*time.Hour), state.ModeOnline)},
		{"survival in between", func() state.State { return state.State{Mode: state.ModeOnline} },
			[]hStep{{hAt(time.Hour), state.ModeSurvival}, {hAt(2 * time.Hour), state.ModeOffline}},
			entries(hAt(time.Hour), state.ModeSurvival, hAt(2*time.Hour), state.ModeOffline)},
		{"the same mode again records nothing", func() state.State { return state.State{Mode: state.ModeOnline} },
			[]hStep{{hAt(time.Hour), state.ModeOffline}, {hAt(2 * time.Hour), state.ModeOffline}},
			entries(hAt(time.Hour), state.ModeOffline)},
		{"a fresh online node staying online records nothing", func() state.State { return state.State{Mode: state.ModeOnline} },
			[]hStep{{hAt(time.Hour), state.ModeOnline}},
			entries()},
		{"a file from before the history: the mode it was in is seeded from mode_changed_at",
			func() state.State { return state.State{Mode: state.ModeOffline, ModeChangedAt: &changedAt} },
			[]hStep{{hAt(time.Hour), state.ModeOnline}},
			entries(changedAt, state.ModeOffline, hAt(time.Hour), state.ModeOnline)},
		{"a file from before the history, online since a change: nothing to seed",
			func() state.State { return state.State{Mode: state.ModeOnline} },
			[]hStep{{hAt(time.Hour), state.ModeSurvival}},
			entries(hAt(time.Hour), state.ModeSurvival)},
		{"a node that fell back to offline with no known start is seeded with an unknown start",
			func() state.State { return state.State{Mode: state.ModeOffline} },
			[]hStep{{hAt(time.Hour), state.ModeOnline}},
			entries(time.Time{}, state.ModeOffline, hAt(time.Hour), state.ModeOnline)},
		{"a clock that stepped back keeps the order",
			func() state.State { return state.State{Mode: state.ModeOnline} },
			[]hStep{{hAt(5 * time.Hour), state.ModeOffline}, {hAt(time.Hour), state.ModeOnline}},
			entries(hAt(5*time.Hour), state.ModeOffline, hAt(5*time.Hour), state.ModeOnline)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := tt.start()
			for _, st := range tt.steps {
				commit(&s, st)
			}
			got := s.ModeHistory
			if len(got) == 0 {
				got = entries()
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("history = %v\nwant      %v", got, tt.want)
			}
		})
	}
}

// The newest ModeHistoryMax entries are kept, the oldest are dropped.
func TestModeHistoryIsBoundedByCount(t *testing.T) {
	s := state.State{Mode: state.ModeOnline}
	const flips = 120
	for i := 0; i < flips; i++ {
		to := state.ModeOffline
		if i%2 == 1 {
			to = state.ModeOnline
		}
		// One change per hour keeps the whole run inside the age bound, so only the count bites.
		commit(&s, hStep{hAt(time.Duration(i) * time.Hour), to})
	}
	if len(s.ModeHistory) != state.ModeHistoryMax {
		t.Fatalf("len = %d, want %d", len(s.ModeHistory), state.ModeHistoryMax)
	}
	first, last := s.ModeHistory[0], s.ModeHistory[len(s.ModeHistory)-1]
	if !first.At.Equal(hAt(time.Duration(flips-state.ModeHistoryMax) * time.Hour)) {
		t.Errorf("the oldest kept change is %v, the %d oldest should be gone", first.At, flips-state.ModeHistoryMax)
	}
	if !last.At.Equal(hAt((flips-1)*time.Hour)) || last.Mode != s.Mode {
		t.Errorf("the newest change is %+v, mode %s", last, s.Mode)
	}
	for i := 1; i < len(s.ModeHistory); i++ {
		if s.ModeHistory[i].At.Before(s.ModeHistory[i-1].At) || s.ModeHistory[i].Mode == s.ModeHistory[i-1].Mode {
			t.Fatalf("entry %d breaks the order or repeats a mode: %v", i, s.ModeHistory)
		}
	}
}

// Nothing older than ModeHistoryDays stays, except the entry that says what was in force when the
// period began.
func TestModeHistoryIsBoundedByAge(t *testing.T) {
	s := state.State{Mode: state.ModeOnline}
	// A change every 20 days for 200 days: offline at 20, online at 40, ...
	for i := 1; i <= 10; i++ {
		to := state.ModeOffline
		if i%2 == 0 {
			to = state.ModeOnline
		}
		commit(&s, hStep{hAt(time.Duration(i) * 20 * hDay), to})
	}
	now := hAt(200 * hDay)
	cutoff := now.AddDate(0, 0, -state.ModeHistoryDays) // day 110
	h := s.ModeHistory
	for i, e := range h {
		if i > 0 && e.At.Before(cutoff) {
			t.Errorf("entry %d (%v) is older than the cutoff %v", i, e.At, cutoff)
		}
	}
	// The baseline is the change at day 100 (the fifth change, to offline): the mode in force
	// at day 110.
	if !h[0].At.Equal(hAt(100*hDay)) || h[0].Mode != state.ModeOffline {
		t.Errorf("baseline = %+v, want offline since day 100", h[0])
	}
	if len(h) != 6 { // days 100, 120, 140, 160, 180, 200
		t.Errorf("len = %d: %v", len(h), h)
	}
}

func TestModeTimeline(t *testing.T) {
	since := hAt(-3 * hDay)
	tests := []struct {
		name string
		st   state.State
		want []state.ModeHistoryEntry
	}{
		{"online and never changed", state.State{Mode: state.ModeOnline}, entries()},
		{"offline since a recorded change, no history", state.State{Mode: state.ModeOffline, ModeChangedAt: &since},
			entries(since, state.ModeOffline)},
		{"offline for as long as anything is known", state.State{Mode: state.ModeOffline},
			entries(time.Time{}, state.ModeOffline)},
		{"survival with no history", state.State{Mode: state.ModeSurvival, ModeChangedAt: &since},
			entries(since, state.ModeSurvival)},
		{"online again, the history says so", state.State{Mode: state.ModeOnline, ModeHistory: entries(hAt(-9*hDay), state.ModeOffline, since, state.ModeOnline)},
			entries(hAt(-9*hDay), state.ModeOffline, since, state.ModeOnline)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.st.ModeTimeline(); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("timeline = %v, want %v", got, tt.want)
			}
		})
	}
	// The timeline is a copy: a caller cannot reach into the state.
	st := state.State{Mode: state.ModeOffline, ModeHistory: entries(since, state.ModeOffline)}
	st.ModeTimeline()[0].Mode = state.ModeOnline
	if st.ModeHistory[0].Mode != state.ModeOffline {
		t.Fatal("ModeTimeline shares memory with the state")
	}
}

// The history is written with the state, survives a restart, and is not shared with a snapshot.
func TestModeHistorySurvivesARestart(t *testing.T) {
	m := seeded(nil)
	s := open(t, m)
	for _, st := range []hStep{{hAt(time.Hour), state.ModeOffline}, {hAt(2 * time.Hour), state.ModeOnline}, {hAt(3 * time.Hour), state.ModeSurvival}} {
		if err := s.MutateSync(func(x *state.State) error { commit(x, st); return nil }); err != nil {
			t.Fatal(err)
		}
	}
	want := entries(hAt(time.Hour), state.ModeOffline, hAt(2*time.Hour), state.ModeOnline, hAt(3*time.Hour), state.ModeSurvival)
	if got := s.Snapshot().ModeHistory; !reflect.DeepEqual(got, want) {
		t.Fatalf("live history = %v", got)
	}
	s.Snapshot().ModeHistory[0].Mode = state.ModeOnline
	if got := s.Snapshot().ModeHistory; !reflect.DeepEqual(got, want) {
		t.Fatal("a snapshot shares the history with the store")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(mustContents(t, m)), `"mode_history"`) {
		t.Fatalf("the file has no mode_history: %s", mustContents(t, m))
	}
	again := open(t, m)
	if got := again.Snapshot().ModeHistory; !reflect.DeepEqual(got, want) {
		t.Fatalf("history after a restart = %v", got)
	}
}

// A damaged state.json falls back to the backup, which carries the history that was current one write
// earlier; with no usable file the history is empty and the offline fallback is still a known mode.
func TestModeHistoryAndDamagedFiles(t *testing.T) {
	t.Run("the backup's history is used", func(t *testing.T) {
		m := seeded(nil)
		s := open(t, m)
		for _, st := range []hStep{{hAt(time.Hour), state.ModeOffline}, {hAt(2 * time.Hour), state.ModeOnline}} {
			if err := s.MutateSync(func(x *state.State) error { commit(x, st); return nil }); err != nil {
				t.Fatal(err)
			}
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		bak := onDisk(t, m, bakPath) // the version before the last write: only the first change
		m.Seed(testPath, []byte(`{"schema_version": 1, "mode": "on`))
		again := open(t, m)
		got := again.Snapshot()
		if !reflect.DeepEqual(got.ModeHistory, bak.ModeHistory) || len(got.ModeHistory) != 1 || got.ModeHistory[0].Mode != state.ModeOffline {
			t.Fatalf("history from the backup = %v, backup holds %v", got.ModeHistory, bak.ModeHistory)
		}
	})
	t.Run("nothing usable: defaults are offline and the timeline says so", func(t *testing.T) {
		m := seeded(map[string][]byte{testPath: []byte(`{`), bakPath: []byte(`also bad`)})
		s := open(t, m)
		got := s.Snapshot()
		if got.Mode != state.ModeOffline || len(got.ModeHistory) != 0 {
			t.Fatalf("mode %s history %v", got.Mode, got.ModeHistory)
		}
		if tl := got.ModeTimeline(); !reflect.DeepEqual(tl, entries(time.Time{}, state.ModeOffline)) {
			t.Fatalf("timeline = %v", tl)
		}
		// The first change from there records where the node was.
		if err := s.MutateSync(func(x *state.State) error { commit(x, hStep{hAt(time.Hour), state.ModeOnline}); return nil }); err != nil {
			t.Fatal(err)
		}
		want := entries(time.Time{}, state.ModeOffline, hAt(time.Hour), state.ModeOnline)
		if got := s.Snapshot().ModeHistory; !reflect.DeepEqual(got, want) {
			t.Fatalf("history = %v", got)
		}
	})
}

// A file written before the history existed loads with an empty one and is rewritten without the key
// until a mode changes; a key a newer agent added to an entry survives.
func TestModeHistoryFormat(t *testing.T) {
	m := seeded(map[string][]byte{testPath: fixture(t, "state-valid.json")})
	s := open(t, m)
	if h := s.Snapshot().ModeHistory; len(h) != 0 {
		t.Fatalf("history = %v", h)
	}
	if err := s.MutateSync(func(*state.State) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(mustContents(t, m)), "mode_history") {
		t.Fatal("an empty history must not be written")
	}

	var e state.ModeHistoryEntry
	if err := json.Unmarshal([]byte(`{"at":"2026-11-01T08:00:00Z","mode":"offline","by":"someone"}`), &e); err != nil {
		t.Fatal(err)
	}
	out, err := json.Marshal(e)
	if err != nil || !strings.Contains(string(out), `"by":"someone"`) || !strings.Contains(string(out), `"mode":"offline"`) {
		t.Fatalf("entry round trip = %s, %v", out, err)
	}
}

func mustContents(t testing.TB, m interface {
	Contents(string) ([]byte, bool)
}) []byte {
	t.Helper()
	b, ok := m.Contents(testPath)
	if !ok {
		t.Fatal("state.json does not exist")
	}
	return b
}
