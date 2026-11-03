package run

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/ark"
	"github.com/marinoscar/local-ai/agent/internal/sync/item"
	"github.com/marinoscar/local-ai/agent/internal/sync/journal"
	"github.com/marinoscar/local-ai/agent/internal/sync/plan"
)

func TestRootRunsAreRecordedPerRootAndSurviveARestart(t *testing.T) {
	r := newRig(t)
	r.addRoot(ark.Root{ID: driveID, Kind: ark.KindArkDrive, Path: r.dir + "/drive", State: ark.StateReady})
	r.setPlans(map[string][]plan.ItemPlan{"internal": {row("a", 1)}})
	eng := r.start()
	if _, ok := eng.RootRun("internal"); ok {
		t.Fatal("a root run before any run")
	}

	// A run on the internal root only (the drive is not named).
	id1 := r.startRun(StartRequest{Trigger: TriggerManual, Roots: []string{"internal"}})
	st1 := r.waitEnd(id1)
	rr, ok := eng.RootRun("internal")
	if !ok || rr.RunID != id1 || rr.Trigger != TriggerManual || rr.State != StateDone || !rr.FinishedAt.Equal(*st1.FinishedAt) || !rr.LastDone.Equal(*st1.FinishedAt) {
		t.Fatalf("internal = %+v", rr)
	}
	if _, ok := eng.RootRun(driveID); ok {
		t.Fatal("the drive was not part of the run")
	}

	// Later a scheduled run is skipped: it is the last thing that happened to every mounted root, but
	// it did not bring any of them up to date.
	r.clk.Advance(48 * time.Hour)
	r.net = NetState{Mode: plan.ModeOnline, Online: false}
	got, err := eng.Start(bg, StartRequest{Trigger: TriggerScheduled})
	if err != nil || got.State != StateSkipped {
		t.Fatalf("Start = %+v, %v", got, err)
	}
	for _, id := range []string{"internal", driveID} {
		rr, ok = eng.RootRun(id)
		if !ok || rr.RunID != got.RunID || rr.State != StateSkipped || rr.Trigger != TriggerScheduled {
			t.Fatalf("%s = %+v", id, rr)
		}
	}
	rr, _ = eng.RootRun("internal")
	if !rr.LastDone.Equal(*st1.FinishedAt) {
		t.Fatalf("the skipped run moved last_done: %v, want %v", rr.LastDone, st1.FinishedAt)
	}
	if rr, _ = eng.RootRun(driveID); !rr.LastDone.IsZero() {
		t.Fatalf("a root that no run finished has last_done %v", rr.LastDone)
	}

	// The record is on disk: a restarted agent knows it.
	want, _ := eng.RootRun("internal")
	e2, err := New(r.deps)
	if err != nil {
		t.Fatal(err)
	}
	if got2, ok := e2.RootRun("internal"); !ok || got2 != want {
		t.Fatalf("after a restart: %+v, want %+v", got2, want)
	}
}

func TestRunsMarkWhatTheyConfirmedCurrent(t *testing.T) {
	r := newRig(t)
	for _, id := range []string{"kept", "kept-unresolved", "downloaded"} {
		if _, err := r.jr.Items.Update("internal", id, func(i *journal.Item) { i.State, i.Version = journal.StateVerified, "v1" }); err != nil {
			t.Fatal(err)
		}
	}
	keepResolved := plan.ItemPlan{ID: "kept", Kind: "model", Action: plan.ActionKeep, FromVersion: "v1", ToVersion: "v1"}
	keepUnresolved := plan.ItemPlan{ID: "kept-unresolved", Kind: "model", Action: plan.ActionKeep, FromVersion: "v1", Reason: plan.ReasonUnresolved}
	noRecord := plan.ItemPlan{ID: "no-record", Kind: "model", Action: plan.ActionKeep, ToVersion: "v1"}
	r.setPlans(map[string][]plan.ItemPlan{"internal": {keepResolved, keepUnresolved, noRecord, row("downloaded", 5)}})
	r.start()
	at := r.clk.Now()
	r.waitEnd(r.startRun(StartRequest{Trigger: TriggerManual}))

	checked := func(id string) (string, bool) {
		rec, ok := r.jr.Items.Get("internal", id)
		if !ok {
			return "", false
		}
		raw, has := rec.Extras[ExtraCheckedCurrentAt]
		if !has {
			return "", false
		}
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			t.Fatal(err)
		}
		return s, true
	}
	want := at.UTC().Format(time.RFC3339)
	if got, ok := checked("kept"); !ok || got != want {
		t.Fatalf("a kept, resolved item: %q, %v (want %q)", got, ok, want)
	}
	if got, ok := checked("downloaded"); !ok || got != want {
		t.Fatalf("a verified item: %q, %v", got, ok)
	}
	if _, ok := checked("kept-unresolved"); ok {
		t.Fatal("an item the run could not resolve was marked current")
	}
	if _, ok := r.jr.Items.Get("internal", "no-record"); ok {
		t.Fatal("marking current created a record")
	}
}

func TestSyncState(t *testing.T) {
	r := newRig(t)
	r.mach.beh = func(ctx context.Context, job item.Job) item.Result { <-ctx.Done(); return cancelled(job) }
	r.setPlans(map[string][]plan.ItemPlan{"internal": {row("a", 1)}})
	eng := r.start()
	if eng.SyncState() != "" {
		t.Fatalf("idle state = %q", eng.SyncState())
	}
	id := r.startRun(StartRequest{Trigger: TriggerManual})
	recvN(t, r.mach.startedCh, 1)
	if eng.SyncState() != StateRunning {
		t.Fatalf("state = %q", eng.SyncState())
	}
	eng.Cancel()
	if got := eng.SyncState(); got != StateCancelling && got != "" {
		t.Fatalf("state after Cancel = %q", got)
	}
	r.waitEnd(id)
	if eng.SyncState() != "" {
		t.Fatalf("state after the run = %q", eng.SyncState())
	}
}
