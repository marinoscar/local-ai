//go:build integration

package app

import (
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/cloud"
	cloudfake "github.com/marinoscar/local-ai/agent/internal/cloud/fake"
	"github.com/marinoscar/local-ai/agent/internal/services"
)

// Scenario 2, without the survival leg: a node linked to the (fake) cloud goes online to offline and
// back. Leaving online stops the link with the final deregister heartbeat; coming back registers
// again; a graceful shutdown of the agent uses the shutdown variant and never deregisters, so the
// cloud keeps showing the node until its heartbeat lapses (agent.md section 4.8.8).
func TestIntegrationCloudLinkAcrossModeChanges(t *testing.T) {
	cloudSrv := cloudfake.New(t)
	cred := "nod_" + newKey(t)[:40] // built at run time: no credential-looking literal in the source
	cloudSrv.Accept(cred)
	r := newRig(t, withEnv(func(e *Env) { e.CloudHTTP = cloudSrv.Client() }), func(r *rig) {
		r.writeNodeYAMLText(r.nodeYAML() + "cloud:\n  url: " + cloudSrv.URL + "\n")
		r.write("etc/localai/cloud-credential", cred+"\n", 0o600)
	})
	d := r.start()
	r.waitStarted()
	waitUntil(t, "the link to be linked", func() bool { return d.Components().Cloud.State() == cloud.StateLinked })
	if n := cloudSrv.Count(cloudfake.RouteRegister); n != 1 {
		t.Fatalf("registrations after the start = %d, want 1", n)
	}
	if got := r.do("GET", "/cloud", nil).json()["state"]; got != "linked" {
		t.Errorf("GET /cloud state = %v", got)
	}

	if rp := r.do("POST", "/mode", map[string]any{"target": "offline", "wait": true}); rp.status != 200 {
		t.Fatalf("POST /mode offline = %d %s", rp.status, rp.body)
	}
	if n := cloudSrv.Count(cloudfake.RouteDeregister); n != 1 {
		t.Errorf("deregistrations after leaving online = %d, want 1", n)
	}
	if rp := r.do("POST", "/mode", map[string]any{"target": "online", "wait": true}); rp.status != 200 {
		t.Fatalf("POST /mode online = %d %s", rp.status, rp.body)
	}
	if n := cloudSrv.Count(cloudfake.RouteRegister); n != 2 {
		t.Errorf("registrations after coming back online = %d, want 2", n)
	}

	d.Shutdown()
	if n := cloudSrv.Count(cloudfake.RouteDeregister); n != 1 {
		t.Errorf("an agent shutdown deregistered: %d deregistrations, want the one of the mode change", n)
	}
	for _, c := range r.sysd.Calls() {
		if c.Unit == services.UnitLlama && (c.Verb == services.VerbStop || c.Verb == services.VerbRestart) {
			t.Errorf("shutdown touched llama: %+v", c)
		}
	}
}
