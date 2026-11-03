//go:build integration

package app

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/cloud"
	cloudfake "github.com/marinoscar/local-ai/agent/internal/cloud/fake"
)

// recordingUpdater is the self-update the extension point of AGENT-19 receives: it answers that the
// node is already on the requested version.
type recordingUpdater struct {
	mu       sync.Mutex
	versions []string
}

func (u *recordingUpdater) Update(_ context.Context, _ cloud.Job, version string) (cloud.Outcome, error) {
	u.mu.Lock()
	u.versions = append(u.versions, version)
	u.mu.Unlock()
	return cloud.Outcome{Result: map[string]any{"from_version": version, "to_version": version, "status": "already_current"}}, nil
}

// UseAgentUpdate is how AGENT-19 plugs in: the updater runs the agent.update job of the daemon's claim
// loop, and without one the job type is refused (LAI-AGT-023).
func TestIntegrationAgentUpdateHookReachesTheClaimLoop(t *testing.T) {
	cloudSrv := cloudfake.New(t)
	cred := "nod_" + newKey(t)[:40]
	cloudSrv.Accept(cred)
	upd := &recordingUpdater{}
	r := newRig(t, withEnv(func(e *Env) { e.CloudHTTP = cloudSrv.Client() }), func(r *rig) {
		r.writeNodeYAMLText(r.nodeYAML() + "cloud:\n  url: " + cloudSrv.URL + "\n")
		r.write("etc/localai/cloud-credential", cred+"\n", 0o600)
		if err := r.hooks.UseAgentUpdate(func(c *Components) AgentUpdate {
			if c.Cloud == nil || c.State == nil {
				t.Error("the updater factory must see the link and the state store")
			}
			return AgentUpdate{Updater: upd}
		}); err != nil {
			t.Fatal(err)
		}
	})
	d := r.start()
	r.waitStarted()
	waitUntil(t, "the link to be linked", func() bool { return d.Components().Cloud.State() == cloud.StateLinked })

	cloudSrv.QueueJob(cloudfake.Job{ID: "job-update", Type: "localai.agent.update", Token: "claim-token-3", Params: map[string]any{"version": "0.2.0"}})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := cloudSrv.WaitJob(ctx, "job-update", cloudfake.JobSucceeded); err != nil {
		rec, _ := cloudSrv.Job("job-update")
		t.Fatalf("%v: job = %+v", err, rec)
	}
	upd.mu.Lock()
	defer upd.mu.Unlock()
	if len(upd.versions) != 1 || upd.versions[0] != "0.2.0" {
		t.Errorf("the updater saw %v", upd.versions)
	}
	rec, _ := cloudSrv.Job("job-update")
	var res map[string]any
	if err := json.Unmarshal(rec.Result, &res); err != nil || res["status"] != "already_current" {
		t.Errorf("result = %s (%v)", rec.Result, err)
	}
}
