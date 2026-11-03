package drill

import (
	"context"
	"errors"

	"github.com/marinoscar/local-ai/agent/internal/events"
)

// Recover finishes a drill an earlier agent process did not: when state.json still has
// drill.in_progress, the agent crashed or was killed mid-drill, so it restores the captured state,
// marks the run aborted and clears the marker (drill.md section 4.3). Call it once at startup, after
// the router is back and before the daemon serves drills. With nothing in progress it does nothing.
//
// The restore is idempotent, so a crash between the restore and the marker being cleared does no harm:
// when the run's final document is already on disk, only the marker is cleared.
func (e *Engine) Recover(ctx context.Context) error {
	p := e.block.inProgress()
	if p == nil {
		return nil
	}
	if id, running := e.Running(); running && id == p.RunID {
		return nil
	}
	log := e.log.With("run_id", p.RunID)
	log.Warn("a drill was interrupted: restoring the node", "started_at", p.StartedAt)

	if _, err := e.d.Results.Get(p.RunID); err == nil {
		log.Info("the interrupted drill already has its final document: clearing the marker")
		return e.block.clearInProgress()
	} else if !errors.Is(err, ErrNotFound) {
		log.Warn("could not look for the interrupted drill's document", "error", err.Error())
	}

	var restored *Restored
	if e.d.Restorer != nil {
		r, err := e.d.Restorer.Restore(ctx, p.Captured)
		if r.LoadedModels == nil {
			r.LoadedModels = []string{}
		}
		if err != nil {
			log.Error("the drill could not restore the node state", "error", err.Error())
			r.OK = false
		}
		restored = &r
	}

	doc, err := e.d.Results.Partial(p.RunID)
	if err != nil {
		if !errors.Is(err, ErrNotFound) {
			log.Warn("the interrupted drill's checkpoint is unreadable", "error", err.Error())
		}
		doc = &Document{
			SchemaVersion: SchemaVersion, RunID: p.RunID, Trigger: TriggerManual,
			NodeID: e.d.NodeID, AgentVersion: e.d.AgentVersion, CatalogVersion: e.catalogVersion(),
			Mode: e.modeName(), StartedAt: p.StartedAt, Checks: []CheckResult{},
		}
	}
	finished := e.now()
	doc.State, doc.Overall, doc.SurvivalReady = StateAborted, OverallAborted, false
	doc.FinishedAt = &finished
	doc.Reason = "the agent restarted during the drill"
	doc.Restored = restored
	if restored != nil && !restored.OK {
		doc.Code = CodeRestoreFailed
	}
	if doc.Checks == nil {
		doc.Checks = []CheckResult{}
	}
	_, _, doc.Counts = aggregate(recordsOf(doc.Checks))

	if err := e.d.Results.Save(doc); err != nil {
		log.Error("could not save the interrupted drill's document", "error", err.Error())
	}
	if err := e.block.clearInProgress(); err != nil {
		return err
	}
	e.publish(events.TypeDrillDone, "", events.DrillDone{RunID: p.RunID, Overall: OverallAborted})
	log.Info("interrupted drill recovered", "restored", restored != nil && restored.OK)
	return nil
}

func recordsOf(cs []CheckResult) []record {
	out := make([]record, 0, len(cs))
	for _, c := range cs {
		out = append(out, record{CheckResult: c})
	}
	return out
}
