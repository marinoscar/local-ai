package cloud

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/api"
	"github.com/marinoscar/local-ai/agent/internal/cloud/fake"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
)

func TestEnrollRegistersHandshakesAndLinks(t *testing.T) {
	r := newRig(t)
	res := r.enroll()
	if res.State != "linked" || res.ServerNodeID == "" || res.Reattached {
		t.Fatalf("enroll answer %+v", res)
	}
	// Register body: name is node.id, one eligible type per cloud.allow entry, the capabilities carry allow.
	regs := r.srv.RequestsFor(fake.RouteRegister)
	if len(regs) != 1 {
		t.Fatalf("register calls: %d", len(regs))
	}
	var body struct {
		Name          string   `json:"name"`
		Hostname      string   `json:"hostname"`
		CLIVersion    string   `json:"cliVersion"`
		EligibleTypes []string `json:"eligibleTypes"`
		Concurrency   int      `json:"concurrency"`
		Capabilities  struct {
			Localai struct {
				Schema  int      `json:"schema"`
				Allow   []string `json:"allow"`
				Backend string   `json:"backend"`
			} `json:"localai"`
		} `json:"capabilities"`
	}
	if err := regs[0].JSON(&body); err != nil {
		t.Fatal(err)
	}
	if body.Name != "ai-n3" || body.Hostname != "host-a" || body.CLIVersion != "aiagent/0.1.0" || body.Concurrency != 1 {
		t.Errorf("register body %+v", body)
	}
	if len(body.EligibleTypes) != 15 || body.EligibleTypes[0] != "localai.status.refresh" || len(body.Capabilities.Localai.Allow) != 15 || body.Capabilities.Localai.Schema != 1 {
		t.Errorf("eligible types / allow: %v / %v", body.EligibleTypes, body.Capabilities.Localai.Allow)
	}
	if regs[0].Credential != testCredential {
		t.Error("register did not carry the credential")
	}
	r.waitCount(fake.RouteConfig, 1)

	// Files: credential and link.json, both 0600.
	for _, p := range []string{r.credPath, r.linkPath} {
		fi, err := os.Stat(p)
		if err != nil || fi.Mode().Perm() != 0o600 {
			t.Errorf("%s: %v %v", p, fi, err)
		}
	}
	var rec LinkRecord
	data, _ := os.ReadFile(r.linkPath)
	if err := json.Unmarshal(data, &rec); err != nil || rec.ServerNodeID != res.ServerNodeID || rec.URL != r.srv.URL || len(rec.ServerFeatures) != 1 || rec.Handshake == nil {
		t.Errorf("link.json: %+v (%v)", rec, err)
	}
	if strings.Contains(string(data), testCredential) {
		t.Error("link.json holds the credential")
	}

	// The key is pushed at enroll (cloud_synced was false after the enroll) and the state is recorded.
	r.waitCount(fake.RouteKey, 1)
	key, _ := r.srv.StoredKey(res.ServerNodeID)
	if key != testKey {
		t.Error("the cloud did not get the llama key")
	}
	r.waitState(StateLinked)
	r.waitCount(fake.RouteHeartbeat, 1)
	r.waitCount(fake.RouteState, 1)
}

func TestCredentialAndKeyNeverReachTheLog(t *testing.T) {
	r := newRig(t)
	r.enroll()
	r.waitCount(fake.RouteKey, 1)
	waitSynced(t, r, true)
	r.waitCount(fake.RouteHeartbeat, 1)
	r.stop()
	logs := r.logs.String()
	for _, secret := range []string{testCredential, testKey} {
		if strings.Contains(logs, secret) {
			t.Fatalf("a secret reached the log:\n%s", logs)
		}
	}
	if !strings.Contains(logs, Fingerprint(testKey)) {
		t.Error("the key push did not log the fingerprint")
	}
	st := r.link.CloudStatus()
	if st.Credential == nil || *st.Credential == testCredential || !strings.HasPrefix(*st.Credential, "nod_") || !strings.HasSuffix(*st.Credential, testCredential[len(testCredential)-4:]) {
		t.Errorf("credential mask: %v", st.Credential)
	}
	raw, _ := json.Marshal(st)
	if strings.Contains(string(raw), testCredential) {
		t.Error("GET /cloud body holds the credential")
	}
}

func TestEnrollRejected401KeepsNothing(t *testing.T) {
	r := newRig(t)
	r.srv.Revoke(testCredential)
	_, err := r.link.Enroll(r.ctx, r.srv.URL, testCredential)
	var ae *api.Error
	if !errors.As(err, &ae) || ae.Code != doctor.CLD051 || ae.Status != 422 {
		t.Fatalf("err = %v", err)
	}
	if _, serr := os.Stat(r.credPath); !os.IsNotExist(serr) {
		t.Error("the rejected credential was kept")
	}
	if r.link.State() != StateUnenrolled {
		t.Errorf("state %s", r.link.State())
	}
}

func TestEnrollRejectedRestoresThePreviousCredential(t *testing.T) {
	r := newRig(t, withCredentialFile())
	other := "nod_" + strings.Repeat("Zy9w", 8)
	r.srv.Revoke(other)
	_, err := r.link.Enroll(r.ctx, r.srv.URL, other)
	var ae *api.Error
	if !errors.As(err, &ae) || ae.Code != doctor.CLD051 {
		t.Fatalf("err = %v", err)
	}
	data, _ := os.ReadFile(r.credPath)
	if strings.TrimSpace(string(data)) != testCredential {
		t.Error("the previous credential did not come back")
	}
	// The previous link resumes with it.
	r.waitState(StateLinked)
}

func TestEnrollNetworkFailureKeepsCredentialAndRetries(t *testing.T) {
	r := newRig(t)
	r.srv.Script(fake.RouteRegister, fake.Reply{Drop: true})
	res := r.enroll()
	if res.State != "backoff" || res.LastError == "" || res.ServerNodeID != "" {
		t.Fatalf("answer %+v", res)
	}
	if _, err := os.Stat(r.credPath); err != nil {
		t.Error("the credential was not kept")
	}
	if r.link.State() != StateRegistering {
		t.Fatalf("state %s", r.link.State())
	}
	// The retry waits the jittered backoff (the test jitter is the lower bound, 5 s), then registers.
	r.waitTimers(1)
	if d, _ := r.clk.NextDelay(); d != BackoffMin {
		t.Errorf("first retry in %v", d)
	}
	r.clk.Advance(BackoffMin)
	r.waitState(StateLinked)
	if r.srv.Count(fake.RouteRegister) != 2 {
		t.Errorf("register calls %d", r.srv.Count(fake.RouteRegister))
	}
}

func TestEnrollOfflineStoresTheCredentialAndStops(t *testing.T) {
	r := newRig(t, withOffline())
	res := r.enroll()
	if res.State != "stopped" {
		t.Fatalf("answer %+v", res)
	}
	if _, err := os.Stat(r.credPath); err != nil {
		t.Error("the credential was not stored")
	}
	if r.srv.Count(fake.RouteRegister) != 0 {
		t.Error("an offline node called the cloud")
	}
	// Back online: Start registers.
	r.online.Store(true)
	if err := r.link.Start(r.ctx); err != nil {
		t.Fatal(err)
	}
	r.waitState(StateLinked)
}

func TestEnrollValidation(t *testing.T) {
	r := newRig(t)
	for name, tc := range map[string]struct {
		url, cred string
		code      string
		status    int
	}{
		"http address":    {"http://example.test", testCredential, doctor.CLD062, 422},
		"loopback http":   {"http://127.0.0.1:9", testCredential, doctor.CLD062, 422},
		"path in address": {r.srv.URL + "/api", testCredential, doctor.CLD062, 422},
		"not a node cred": {r.srv.URL, "lat_" + strings.Repeat("a", 24), api.CodeInvalidRequest, 400},
		"short cred":      {r.srv.URL, "nod_x", api.CodeInvalidRequest, 400},
		"space in cred":   {r.srv.URL, "nod_" + strings.Repeat("a", 8) + " b", api.CodeInvalidRequest, 400},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := r.link.Enroll(r.ctx, tc.url, tc.cred)
			var ae *api.Error
			if !errors.As(err, &ae) || ae.Code != tc.code || ae.Status != tc.status {
				t.Fatalf("err = %v", err)
			}
			if _, serr := os.Stat(r.credPath); !os.IsNotExist(serr) {
				t.Error("a refused enroll wrote the credential")
			}
		})
	}
	if r.srv.Count(fake.RouteRegister) != 0 {
		t.Error("a refused enroll reached the cloud")
	}
}

func TestUnenrollDeregistersAndDeletesEverything(t *testing.T) {
	r := newRig(t)
	res := r.enroll()
	r.waitCount(fake.RouteKey, 1)
	if err := r.link.Unenroll(r.ctx); err != nil {
		t.Fatal(err)
	}
	if r.srv.Count(fake.RouteDeregister) != 1 {
		t.Errorf("deregister calls %d", r.srv.Count(fake.RouteDeregister))
	}
	for _, p := range []string{r.credPath, r.linkPath} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s still exists", p)
		}
	}
	if r.link.State() != StateUnenrolled || r.link.CloudStatus().Enrolled {
		t.Errorf("state %s", r.link.State())
	}
	if r.st.Snapshot().Key.CloudSynced {
		t.Error("cloud_synced stays true after unenroll")
	}
	if n, _ := r.srv.NodeByName("ai-n3"); !n.Deregistered || n.ID != res.ServerNodeID {
		t.Errorf("node %+v", n)
	}
}

func TestStartDecidesFromTheCredentialAndTheMode(t *testing.T) {
	t.Run("no credential", func(t *testing.T) {
		r := newRig(t)
		if err := r.link.Start(r.ctx); err != nil {
			t.Fatal(err)
		}
		if r.link.State() != StateUnenrolled || r.srv.Count(fake.RouteRegister) != 0 {
			t.Errorf("state %s", r.link.State())
		}
	})
	t.Run("credential, offline", func(t *testing.T) {
		r := newRig(t, withCredentialFile(), withOffline())
		_ = r.link.Start(r.ctx)
		if r.link.State() != StateStopped || r.srv.Count(fake.RouteRegister) != 0 {
			t.Errorf("state %s", r.link.State())
		}
	})
	t.Run("credential, online", func(t *testing.T) {
		r := newRig(t, withCredentialFile())
		_ = r.link.Start(r.ctx)
		r.waitState(StateLinked)
		if r.srv.RequestsFor(fake.RouteRegister)[0].Credential != testCredential {
			t.Error("register without the stored credential")
		}
	})
	t.Run("start twice is one link", func(t *testing.T) {
		r := newRig(t, withCredentialFile())
		_ = r.link.Start(r.ctx)
		_ = r.link.Start(r.ctx)
		r.waitState(StateLinked)
		r.waitCount(fake.RouteHeartbeat, 1)
		if r.srv.Count(fake.RouteRegister) != 1 {
			t.Errorf("register calls %d", r.srv.Count(fake.RouteRegister))
		}
	})
}

func TestStopSendsLastSnapshotOfflineHeartbeatAndDeregister(t *testing.T) {
	r := newRig(t)
	r.enroll()
	r.waitCount(fake.RouteHeartbeat, 1)
	r.waitCount(fake.RouteState, 1)
	beforeState := r.srv.Count(fake.RouteState)
	beforeHB := r.srv.Count(fake.RouteHeartbeat)
	r.stop()
	if r.link.State() != StateStopped {
		t.Fatalf("state %s", r.link.State())
	}
	if got := r.srv.Count(fake.RouteState); got != beforeState+1 {
		t.Errorf("the last snapshot: %d state pushes, was %d", got, beforeState)
	}
	hbs := r.srv.RequestsFor(fake.RouteHeartbeat)
	if len(hbs) != beforeHB+1 {
		t.Fatalf("heartbeats %d, was %d", len(hbs), beforeHB)
	}
	var last map[string]any
	if err := hbs[len(hbs)-1].JSON(&last); err != nil || last["status"] != "offline" || len(last) != 1 {
		t.Errorf("offline heartbeat: %v %v", last, err)
	}
	if r.srv.Count(fake.RouteDeregister) != 1 {
		t.Errorf("deregister calls %d", r.srv.Count(fake.RouteDeregister))
	}
	// Order: snapshot, offline heartbeat, deregister.
	var order []string
	for _, q := range r.srv.Requests()[len(r.srv.Requests())-3:] {
		order = append(order, q.Route)
	}
	if strings.Join(order, ",") != "state,heartbeat,deregister" {
		t.Errorf("stop order %v", order)
	}
	// Nothing runs while stopped: the clock moving fires no timer-driven call.
	n := len(r.srv.Requests())
	r.clk.Advance(10 * time.Minute)
	if len(r.srv.Requests()) != n {
		t.Error("the link called the cloud while stopped")
	}
}

func TestShutdownDoesNotDeregisterAndKeepsTheState(t *testing.T) {
	r := newRig(t)
	r.enroll()
	r.waitCount(fake.RouteHeartbeat, 1)
	if err := r.link.Shutdown(r.ctx); err != nil {
		t.Fatal(err)
	}
	if r.srv.Count(fake.RouteDeregister) != 0 {
		t.Error("an agent shutdown deregistered the node")
	}
	hbs := r.srv.RequestsFor(fake.RouteHeartbeat)
	var last map[string]any
	_ = hbs[len(hbs)-1].JSON(&last)
	if last["status"] != "offline" {
		t.Errorf("last heartbeat %v", last)
	}
	if r.link.State() != StateLinked {
		t.Errorf("shutdown changed the state to %s", r.link.State())
	}
}

// Integration scenario 2 of agent.md section 9.2, at the link: online, survival (Stop), online (Start).
func TestOnlineSurvivalOnlineDeregistersAndRegistersAgain(t *testing.T) {
	r := newRig(t)
	first := r.enroll()
	r.waitCount(fake.RouteHeartbeat, 1)

	r.online.Store(false)
	r.stop()
	if r.srv.Count(fake.RouteDeregister) != 1 || r.link.State() != StateStopped {
		t.Fatalf("after stop: %d deregister, state %s", r.srv.Count(fake.RouteDeregister), r.link.State())
	}

	r.online.Store(true)
	if err := r.link.Start(r.ctx); err != nil {
		t.Fatal(err)
	}
	r.waitState(StateLinked)
	regs := r.srv.RequestsFor(fake.RouteRegister)
	if len(regs) != 2 {
		t.Fatalf("register calls %d", len(regs))
	}
	if st := r.link.CloudStatus(); st.ServerNodeID == nil || *st.ServerNodeID != first.ServerNodeID {
		t.Errorf("the node came back as %v, was %s", st.ServerNodeID, first.ServerNodeID)
	}
	if n, _ := r.srv.NodeByName("ai-n3"); n.Deregistered {
		t.Error("the node is still deregistered after it registered again")
	}
}

func TestEnrollAndUnenrollAnswer409WhileAnotherChangeRuns(t *testing.T) {
	r := newRig(t)
	r.link.opSem <- struct{}{} // another lifecycle operation holds the link
	_, err := r.link.Enroll(r.ctx, r.srv.URL, testCredential)
	var ae *api.Error
	if !errors.As(err, &ae) || ae.Code != api.CodeBusy || ae.Status != 409 {
		t.Fatalf("enroll err = %v", err)
	}
	err = r.link.Unenroll(r.ctx)
	if !errors.As(err, &ae) || ae.Code != api.CodeBusy || ae.Status != 409 {
		t.Fatalf("unenroll err = %v", err)
	}
	if _, serr := os.Stat(r.credPath); !os.IsNotExist(serr) || r.srv.Count(fake.RouteRegister) != 0 {
		t.Error("a refused enroll had an effect")
	}
	<-r.link.opSem
	r.enroll() // free again
}
