//go:build integration

package app

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/cloud"
	cloudfake "github.com/marinoscar/local-ai/agent/internal/cloud/fake"
	routerfake "github.com/marinoscar/local-ai/agent/internal/router/fake"
	"github.com/marinoscar/local-ai/agent/internal/state"
)

// Scenario 4 of agent.md section 9.2: a cloud model.load job is claimed from the fake cloud by the
// daemon's own claim loop, its executor loads the model through the same orchestrator as POST
// /models/{id}/load (so the fake llama-server really loads it), and the result is posted with the
// claim token and conforms to the cloud's schema (the fake refuses a result that does not). The state
// push that follows the command is sent, and the audit log names the cloud as the actor.
func TestIntegrationCloudModelLoadJob(t *testing.T) {
	cloudSrv := cloudfake.New(t)
	cred := "nod_" + newKey(t)[:40] // built at run time: no credential-looking literal in the source
	cloudSrv.Accept(cred)
	r := newRig(t, withEnv(func(e *Env) { e.CloudHTTP = cloudSrv.Client() }), func(r *rig) {
		r.writeNodeYAMLText(r.nodeYAML() + "cloud:\n  url: " + cloudSrv.URL + "\n")
		r.write("etc/localai/cloud-credential", cred+"\n", 0o600)
	})
	d := r.start()
	r.waitStarted()
	r.waitLoaded(modelAlpha) // the startup restore is done, so the job does not collide with it
	waitUntil(t, "the link to be linked", func() bool { return d.Components().Cloud.State() == cloud.StateLinked })

	cloudSrv.QueueJob(cloudfake.Job{
		ID: "job-load", Type: "localai.model.load", Token: "claim-token-1",
		Params: map[string]any{"model_id": modelGamma, "wait": true, "unload_others": true, "set_default": false},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := cloudSrv.WaitJob(ctx, "job-load", cloudfake.JobSucceeded); err != nil {
		rec, _ := cloudSrv.Job("job-load")
		t.Fatalf("%v: job = %+v\nlog:\n%s", err, rec, r.logText())
	}
	if r.llama.Status(modelGamma) != routerfake.Loaded || r.llama.Status(modelAlpha) == routerfake.Loaded {
		t.Errorf("the fake llama: gamma %q, alpha %q", r.llama.Status(modelGamma), r.llama.Status(modelAlpha))
	}
	rec, _ := cloudSrv.Job("job-load")
	var res map[string]any
	if err := json.Unmarshal(rec.Result, &res); err != nil || res["model_id"] != modelGamma || res["status"] != "loaded" {
		t.Errorf("result = %s (%v)", rec.Result, err)
	}
	posted := cloudSrv.RequestsFor(cloudfake.RouteResult)
	if len(posted) != 1 {
		t.Fatalf("%d results posted, want 1", len(posted))
	}
	var body struct {
		Type       string `json:"type"`
		ClaimToken string `json:"claimToken"`
	}
	if err := json.Unmarshal(posted[0].Body, &body); err != nil || body.Type != "localai.model.load" || body.ClaimToken != "claim-token-1" {
		t.Errorf("posted = %s (%v)", posted[0].Body, err)
	}

	// The audit log names the cloud, not an operator, as the actor of the load.
	r.d.Components().Audit.Close() // flush: Close is the last write
	if b := r.read("var/lib/localai/audit.jsonl"); !containsAll(b, `"model.load"`, `"cloud"`, "job-load") {
		t.Errorf("the audit log does not record the cloud job:\n%s", b)
	}
	// ark.sync is the sync engine's: the job starts a run (trigger cloud) and answers its id.
	cloudSrv.QueueJob(cloudfake.Job{ID: "job-ark", Type: "localai.ark.sync", Token: "claim-token-2", Params: map[string]any{}, Force: true})
	if err := cloudSrv.WaitJob(ctx, "job-ark", cloudfake.JobSucceeded); err != nil {
		rec, _ := cloudSrv.Job("job-ark")
		t.Fatalf("%v: job = %+v", err, rec)
	}
	rec, _ = cloudSrv.Job("job-ark")
	var syncRes struct {
		RunID   string `json:"sync_run_id"`
		Started bool   `json:"started"`
	}
	if err := json.Unmarshal(rec.Result, &syncRes); err != nil || !syncRes.Started || !strings.HasPrefix(syncRes.RunID, "run-") {
		t.Errorf("an ark.sync job = %s (%v), want a started run", rec.Result, err)
	}
	if st, ok := d.Components().SyncEngine.Status(); !ok || st.Trigger != "cloud" {
		t.Errorf("the engine's run = %+v, want one started by the cloud", st)
	}
	// ark.verify is the re-verification's (SYNC-13): once the sync run is over the slot is free, and the
	// job starts a run of trigger verify (the run slot is shared, so while the sync was active it would
	// have answered started: false with that run's id).
	if err := d.Components().SyncEngine.Wait(ctx, syncRes.RunID); err != nil {
		t.Fatal(err)
	}
	cloudSrv.QueueJob(cloudfake.Job{ID: "job-verify", Type: "localai.ark.verify", Token: "claim-token-3", Params: map[string]any{}, Force: true})
	if err := cloudSrv.WaitJob(ctx, "job-verify", cloudfake.JobSucceeded); err != nil {
		rec, _ := cloudSrv.Job("job-verify")
		t.Fatalf("%v: job = %+v", err, rec)
	}
	rec, _ = cloudSrv.Job("job-verify")
	var verifyRes struct {
		RunID   string `json:"verify_run_id"`
		Started bool   `json:"started"`
	}
	if err := json.Unmarshal(rec.Result, &verifyRes); err != nil || !verifyRes.Started || !strings.HasPrefix(verifyRes.RunID, "run-") {
		t.Errorf("an ark.verify job = %s (%v), want a started run", rec.Result, err)
	}
	if st, ok := d.Components().SyncEngine.Status(); !ok || st.Trigger != "verify" || st.RunID != verifyRes.RunID {
		t.Errorf("the engine's run = %+v, want the verify run %s", st, verifyRes.RunID)
	}
	if st := d.Components().State.Snapshot(); st.Mode != state.ModeOnline {
		t.Errorf("mode = %q", st.Mode)
	}
}

func containsAll(s string, parts ...string) bool {
	for _, p := range parts {
		if !strings.Contains(s, p) {
			return false
		}
	}
	return true
}
