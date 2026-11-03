package modes_test

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/modes"
	"github.com/marinoscar/local-ai/agent/internal/state"
)

func hist(pairs ...any) []state.ModeHistoryEntry {
	out := []state.ModeHistoryEntry{}
	for i := 0; i < len(pairs); i += 2 {
		out = append(out, state.ModeHistoryEntry{At: pairs[i].(time.Time), Mode: pairs[i+1].(modes.Mode)})
	}
	return out
}

func historyOf(r *rig) []state.ModeHistoryEntry {
	h := r.st.Snapshot().ModeHistory
	if len(h) == 0 {
		return hist()
	}
	return h
}

// Every committed change is recorded with the engine's clock, in the write that sets the mode, and a
// request that changes nothing is not.
func TestPersistRecordsTheModeHistory(t *testing.T) {
	r := newRig(t, withMode(modes.Online))
	if got := historyOf(r); len(got) != 0 {
		t.Fatalf("a fresh online node has history %v", got)
	}
	t1 := t0
	r.mustRequest(modes.Offline, true)
	r.clk.Advance(6 * time.Hour)
	t2 := r.clk.Now().UTC().Truncate(time.Second)
	r.mustRequest(modes.Online, true)
	r.clk.Advance(time.Hour)
	if res := r.mustRequest(modes.Online, true); !res.Already {
		t.Fatalf("online again was not already: %+v", res)
	}
	// Re-applying the mode re-runs the steps but changes no mode.
	if _, err := r.eng.Request(context.Background(), modes.Online, modes.RequestOptions{Wait: true, Reapply: true, Actor: operator}); err != nil {
		t.Fatal(err)
	}
	r.clk.Advance(time.Hour)
	t3 := r.clk.Now().UTC().Truncate(time.Second)
	r.mustRequest(modes.Survival, true)

	want := hist(t1, modes.Offline, t2, modes.Online, t3, modes.Survival)
	if got := historyOf(r); !reflect.DeepEqual(got, want) {
		t.Fatalf("history = %v\nwant      %v", got, want)
	}
	if snap := r.st.Snapshot(); snap.ModeChangedAt == nil || !snap.ModeChangedAt.Equal(t3) || snap.Mode != modes.Survival {
		t.Fatalf("mode %s changed at %v: the history and the mode must agree", snap.Mode, snap.ModeChangedAt)
	}
}

// A node that was already offline when the history began records where it was on its first change.
func TestPersistSeedsTheBaselineOfAnOfflineNode(t *testing.T) {
	r := newRig(t, withMode(modes.Offline))
	r.mustRequest(modes.Online, true)
	want := hist(time.Time{}, modes.Offline, t0, modes.Online)
	if got := historyOf(r); !reflect.DeepEqual(got, want) {
		t.Fatalf("history = %v\nwant      %v", got, want)
	}
}

// The history is on disk once the change is committed: a restart sees it.
func TestModeHistoryIsKeptAcrossARestart(t *testing.T) {
	r1 := newRig(t, withMode(modes.Online))
	r1.mustRequest(modes.Offline, true)
	r1.clk.Advance(2 * time.Hour)
	r1.mustRequest(modes.Online, true)
	want := historyOf(r1)
	if len(want) != 2 {
		t.Fatalf("history = %v", want)
	}
	if err := r1.st.Close(); err != nil {
		t.Fatal(err)
	}
	r2 := newRig(t, withFS(r1.fs), noSeed())
	if got := historyOf(r2); !reflect.DeepEqual(got, want) {
		t.Fatalf("history after a restart = %v\nwant %v", got, want)
	}
	r2.clk.Advance(time.Hour)
	r2.mustRequest(modes.Survival, true)
	if got := historyOf(r2); len(got) != 3 || got[2].Mode != modes.Survival {
		t.Fatalf("history after one more change = %v", got)
	}
}
