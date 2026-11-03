//go:build integration

package app

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/cloud"
	cloudfake "github.com/marinoscar/local-ai/agent/internal/cloud/fake"
	"github.com/marinoscar/local-ai/agent/internal/state"
	"github.com/marinoscar/local-ai/agent/internal/update"
)

// Scenario 5, second half (agent.md section 9.2): the process that systemd restarted after an update
// runs its health check at startup step 15 and settles the cloud command that asked for it. The first
// process left what it must leave (the marker pending.json with the command's claim token, and the
// command in inflight.json); the new one finds both, the probe of the daemon passes (listeners, state
// store, /health, router, cloud link), state.update.last records the success, the marker is gone and the
// cloud's command ends succeeded with status "updated". The first half (a signed release downloaded,
// installed and the process replaced) is internal/update's and the claim loop's own scenario test.
func TestIntegrationUpdatedProcessSettlesTheCloudCommand(t *testing.T) {
	const from, to = "0.1.0", "0.2.0"
	cloudSrv := cloudfake.New(t)
	cred := "nod_" + newKey(t)[:40]
	cloudSrv.Accept(cred)
	now := time.Now().UTC().Truncate(time.Second)
	cloudSrv.QueueJob(cloudfake.Job{ID: "job-update", Type: "localai.agent.update", Token: "claim-token-9", Params: map[string]any{"version": to}})
	cloudSrv.MarkRunning("job-update") // an earlier process claimed it

	r := newRig(t, withEnv(func(e *Env) { e.CloudHTTP = cloudSrv.Client(); e.Version = to; e.UpdateClock = quickClock{} }), func(r *rig) {
		r.writeNodeYAMLText(r.nodeYAML() + "cloud:\n  url: " + cloudSrv.URL + "\n")
		r.write("etc/localai/cloud-credential", cred+"\n", 0o600)
		marker, _ := json.Marshal(update.Pending{
			From: from, To: to, StartedAt: now.Add(-20 * time.Second), Deadline: now.Add(2 * time.Minute),
			Job: &update.JobRef{JobID: "job-update", ClaimToken: "claim-token-9"},
		})
		r.write("var/lib/localai/update/pending.json", string(marker), 0o600)
		inflight, _ := json.Marshal(map[string]any{"version": 1, "jobs": []cloud.InflightEntry{{
			JobID: "job-update", Type: "localai.agent.update", Plane: "localai", ClaimToken: "claim-token-9",
			LeaseExpiresAt: now.Add(time.Minute), StartedAt: now.Add(-20 * time.Second),
		}}})
		r.write("var/lib/localai/cloud/inflight.json", string(inflight), 0o600)
	})
	d := r.start()
	r.waitStarted()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := cloudSrv.WaitJob(ctx, "job-update", cloudfake.JobSucceeded); err != nil {
		rec, _ := cloudSrv.Job("job-update")
		t.Fatalf("%v: job = %+v\nlog:\n%s", err, rec, r.logText())
	}
	rec, _ := cloudSrv.Job("job-update")
	var res map[string]any
	if err := json.Unmarshal(rec.Result, &res); err != nil || res["status"] != "updated" || res["to_version"] != to || res["from_version"] != from {
		t.Errorf("result = %s (%v)", rec.Result, err)
	}
	last := d.Components().State.Snapshot().Update.Last
	if last == nil || last.Result != state.UpdateOK || last.From != from || last.To != to {
		t.Errorf("state.update.last = %+v, want ok %s to %s", last, from, to)
	}
	if _, err := os.Stat(r.path("var/lib/localai/update/pending.json")); err == nil {
		t.Error("the marker survived a confirmed update")
	}
	if hasAlert(d, "LAI-AGT-009") {
		t.Error("a confirmed update raised the rollback alert")
	}
}

// quickClock is the self-update's clock with its waits scaled down to a millisecond: the production
// waits (a 2 s probe interval, a 15 s settle retry while the link is not up yet) decide nothing the
// test checks.
type quickClock struct{}

func (quickClock) Now() time.Time { return time.Now() }

func (quickClock) Sleep(ctx context.Context, _ time.Duration) error {
	select {
	case <-time.After(time.Millisecond):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
