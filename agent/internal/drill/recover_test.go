package drill

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/events"
)

// crashedRig returns a rig whose node "crashed" mid-drill: a first rig ran a drill whose second check
// hung, and the state and the files as they were at that instant are copied to a fresh rig (a new
// agent process: new engine, no memory of the run).
func crashedRig(t *testing.T, opts ...rigOpt) (fresh *rig, runID string) {
	t.Helper()
	old := newRig(t)
	g := newGate()
	old.add(Check{ID: "drl.first", Area: "x", Order: 1, Critical: true, Run: func(context.Context, *Env) Outcome {
		return Outcome{Result: ResultWarn, Code: doctor.DRL031}
	}})
	old.add(g.check("drl.hang", 2))
	res, err := old.eng.Start(context.Background(), StartRequest{Trigger: TriggerScheduled})
	if err != nil {
		t.Fatal(err)
	}
	<-g.started

	// The kill: take what is on disk right now.
	snap := old.state.Snapshot()
	files := map[string]*memNode{}
	old.fs.mu.Lock()
	for p, n := range old.fs.nodes {
		c := *n
		c.data = append([]byte(nil), n.data...)
		files[p] = &c
	}
	old.fs.mu.Unlock()

	fresh = newRig(t, opts...)
	fresh.state.mu.Lock()
	fresh.state.st = snap
	fresh.state.mu.Unlock()
	fresh.fs.nodes = files
	return fresh, res.RunID
}

func TestRecoverRestoresAndMarksTheInterruptedRunAborted(t *testing.T) {
	r, runID := crashedRig(t)
	if p := r.eng.block.inProgress(); p == nil || p.RunID != runID || !reflect.DeepEqual(p.Captured, r.rest.captured) {
		t.Fatalf("the crash must leave drill.in_progress behind: %+v", p)
	}
	if _, err := r.eng.Get(runID); !errors.Is(err, ErrNotFound) {
		t.Fatal("a run that never ended has no final document")
	}

	if err := r.eng.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	calls := r.rest.restoreCalls()
	if len(calls) != 1 || !reflect.DeepEqual(calls[0], r.rest.captured) {
		t.Fatalf("Recover must restore the captured state: %+v", calls)
	}
	d := r.doc(runID)
	if d.State != StateAborted || d.Overall != OverallAborted || d.SurvivalReady || d.FinishedAt == nil || d.Reason != "the agent restarted during the drill" {
		t.Fatalf("recovered document = %+v", d)
	}
	if d.Trigger != TriggerScheduled || d.NodeID != "ai-n3" || !d.StartedAt.Equal(t0) {
		t.Fatalf("the document keeps the run's header from its checkpoint: %+v", d)
	}
	if len(d.Checks) != 1 || d.Checks[0].ID != "drl.first" || d.Counts.Warn != 1 {
		t.Fatalf("the checks that finished before the crash are kept: %+v", d)
	}
	if d.Restored == nil || !d.Restored.OK || d.Code != "" {
		t.Fatalf("restored = %+v code %q", d.Restored, d.Code)
	}
	if string(r.state.block(t)["in_progress"]) != "null" {
		t.Fatal("Recover must clear drill.in_progress")
	}
	if _, ok := r.state.block(t)["last_result"]; ok {
		t.Fatal("an interrupted run is not a drill result")
	}
	if latest, _ := r.eng.Latest(); !json.Valid(latest) {
		t.Fatal("the recovered document is the latest finished run")
	}
	evs := r.pub.events()
	if len(evs) != 1 || evs[0].Typ != events.TypeDrillDone || evs[0].Data.(events.DrillDone) != (events.DrillDone{RunID: runID, Overall: "aborted"}) {
		t.Fatalf("events = %+v", evs)
	}

	// A second Recover has nothing to do, and the node can run drills again.
	before := r.state.mutations
	if err := r.eng.Recover(context.Background()); err != nil || r.state.mutations != before || len(r.rest.restoreCalls()) != 1 {
		t.Fatalf("a second Recover must be a no-op (err %v)", err)
	}
	r.add(passing("drl.a", 1))
	if _, d := r.startWait(StartRequest{}); d.Overall != OverallPass {
		t.Fatalf("a drill after recovery = %+v", d)
	}
}

func TestRecoverReportsARestoreThatFailed(t *testing.T) {
	r, runID := crashedRig(t)
	r.rest.restoreErr = errors.New("router refused")
	if err := r.eng.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	d := r.doc(runID)
	if d.Restored == nil || d.Restored.OK || d.Code != doctor.DRL090 || d.State != StateAborted {
		t.Fatalf("document = %+v", d)
	}
	if string(r.state.block(t)["in_progress"]) != "null" {
		t.Fatal("one attempt: the marker must be cleared even when the restore failed")
	}
}

func TestRecoverWithoutARestorer(t *testing.T) {
	r, runID := crashedRig(t, withoutRestorer())
	if err := r.eng.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if d := r.doc(runID); d.State != StateAborted || d.Restored != nil {
		t.Fatalf("document = %+v", d)
	}
}

func TestRecoverDoesNotRestoreTwiceWhenTheDocumentIsAlreadyFinal(t *testing.T) {
	r := newRig(t)
	r.add(passing("drl.a", 1))
	res, _ := r.startWait(StartRequest{})
	// The crash came after the document was written and before the marker was cleared.
	p := InProgress{RunID: res.RunID, StartedAt: t0, Captured: r.rest.captured}
	if err := r.eng.block.setInProgress(p); err != nil {
		t.Fatal(err)
	}
	before := len(r.rest.restoreCalls())
	if err := r.eng.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(r.rest.restoreCalls()) != before {
		t.Fatal("a run with its final document was already restored")
	}
	if d := r.doc(res.RunID); d.State == StateAborted || d.Overall != OverallPass {
		t.Fatalf("the final document must be left alone: %+v", d)
	}
	if string(r.state.block(t)["in_progress"]) != "null" {
		t.Fatal("the marker must be cleared")
	}
}

func TestRecoverWithoutACheckpoint(t *testing.T) {
	r := newRig(t)
	id := "drl-20261101T043512Z-beef"
	if err := r.eng.block.setInProgress(InProgress{RunID: id, StartedAt: t0.Add(-time.Hour), Captured: Captured{LoadedModels: []string{"m1", "m2"}, DefaultModel: "m2"}}); err != nil {
		t.Fatal(err)
	}
	if err := r.eng.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	d := r.doc(id)
	if d.State != StateAborted || d.Trigger != TriggerManual || !d.StartedAt.Equal(t0.Add(-time.Hour)) || d.Checks == nil || len(d.Checks) != 0 {
		t.Fatalf("minimal document = %+v", d)
	}
	if got := r.rest.restoreCalls(); len(got) != 1 || !reflect.DeepEqual(got[0].LoadedModels, []string{"m1", "m2"}) || got[0].DefaultModel != "m2" {
		t.Fatalf("restore calls = %+v", got)
	}
}

func TestRecoverWithNothingInProgress(t *testing.T) {
	for name, block := range map[string]string{
		"no block":        "",
		"null":            `{"in_progress":null}`,
		"unreadable":      `{"in_progress":"garbage"}`,
		"no run id":       `{"in_progress":{"started_at":"2026-11-01T04:35:12Z"}}`,
		"broken block":    `not json`,
		"another feature": `{"last_result":"pass"}`,
	} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t)
			if block != "" {
				r.state.setBlock(block)
			}
			if err := r.eng.Recover(context.Background()); err != nil {
				t.Fatal(err)
			}
			if r.state.mutations != 0 || len(r.rest.restoreCalls()) != 0 || len(r.pub.events()) != 0 {
				t.Fatal("nothing in progress: Recover must do nothing")
			}
		})
	}
}

func TestRecoverLeavesARunOfThisProcessAlone(t *testing.T) {
	r := newRig(t)
	g := newGate()
	r.add(g.check("drl.hang", 1))
	res, _ := r.eng.Start(context.Background(), StartRequest{})
	<-g.started
	if err := r.eng.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(r.rest.restoreCalls()) != 0 {
		t.Fatal("Recover must not restore under a drill that is running")
	}
	close(g.release)
	r.wait()
	if d := r.doc(res.RunID); d.Overall != OverallPass {
		t.Fatalf("document = %+v", d)
	}
}
