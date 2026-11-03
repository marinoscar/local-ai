package cloud

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/audit"
	"github.com/marinoscar/local-ai/agent/internal/cloud/fake"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
)

// inflight.json at the start of the process (agent.md section 4.8.6, step 6): a command an earlier
// process left behind is reported once as failed when its lease may still be valid, discarded
// otherwise, and never run again; agent.update is the update code's.

func (cr *claimRig) writeInflight(entries ...InflightEntry) {
	cr.t.Helper()
	if err := cr.claimer.d.Inflight.Save(entries); err != nil {
		cr.t.Fatal(err)
	}
}

// leftover stages a command the cloud thinks is running, with an entry for it in inflight.json.
func (cr *claimRig) leftover(id, typ string, leaseIn time.Duration) InflightEntry {
	cr.t.Helper()
	cr.srv.QueueJob(fake.Job{ID: id, Type: typ, Params: map[string]any{}, Token: "tok-" + id})
	cr.srv.MarkRunning(id)
	e := InflightEntry{JobID: id, Type: typ, Plane: PlaneLocalai, ClaimToken: "tok-" + id,
		LeaseExpiresAt: cr.clk.Now().Add(leaseIn), StartedAt: cr.clk.Now().Add(-time.Minute)}
	return e
}

func TestAnEntryWithAValidLeaseIsReportedAsFailedAfterARestart(t *testing.T) {
	cr := newClaimRig(t, []*testHandler{okHandler(), newTestHandler(TypeModelLoad, nil)})
	cr.writeInflight(cr.leftover("job-old", "localai.model.load", 30*time.Second))
	cr.start()
	rec := cr.waitJob("job-old", fake.JobFailed)
	if rec.Error != "LAI-AGT-017: the agent restarted during the command" {
		t.Errorf("failure = %q", rec.Error)
	}
	// The claim token the earlier process persisted is the one the failure carries (the fake refuses
	// any other with a 409), and the entry is gone.
	cr.waitCount(fake.RouteClaim, 1)
	if _, err := os.Stat(cr.inflight); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("inflight.json still exists: %v", err)
	}
	evs := cr.audit.wait(t, cr.ctx, 1)
	if evs[0].Result != audit.ResultFail || evs[0].Code != doctor.AGT017 || evs[0].Actor.JobID != "job-old" || evs[0].Target["type"] != "localai.model.load" {
		t.Errorf("audit = %+v", evs[0])
	}
	// The command was not run again.
	if len(cr.claimer.d.Handlers.handlers[TypeModelLoad].(*testHandler).started) != 0 {
		t.Error("the interrupted command was run again")
	}
}

func TestAnEntryWithAnExpiredLeaseIsDiscardedInSilence(t *testing.T) {
	cr := newClaimRig(t, []*testHandler{okHandler()})
	cr.writeInflight(cr.leftover("job-old", "localai.model.load", -time.Minute))
	cr.start()
	cr.waitCount(fake.RouteClaim, 1)
	if cr.srv.Count(fake.RouteFailure) != 0 {
		t.Error("an expired command was reported")
	}
	if _, err := os.Stat(cr.inflight); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("inflight.json still exists: %v", err)
	}
}

// agent.update is not the claim loop's: its entry stays untouched (the update code settles it after
// the restart it caused), with or without a hook.
func TestAnAgentUpdateEntryIsLeftToTheUpdateCode(t *testing.T) {
	t.Run("without a hook it stays in the file", func(t *testing.T) {
		cr := newClaimRig(t, []*testHandler{okHandler()})
		e := cr.leftover("job-up", "localai.agent.update", 120*time.Second)
		cr.writeInflight(e)
		cr.start()
		cr.waitCount(fake.RouteClaim, 1)
		if cr.srv.Count(fake.RouteFailure) != 0 {
			t.Error("the update command was failed")
		}
		got, err := cr.claimer.d.Inflight.Load()
		if err != nil || len(got) != 1 || got[0].JobID != "job-up" || got[0].ClaimToken != "tok-job-up" {
			t.Errorf("inflight = %+v %v", got, err)
		}
	})
	t.Run("a hook takes it over", func(t *testing.T) {
		var mu sync.Mutex
		var seen []InflightEntry
		cr := newClaimRig(t, []*testHandler{okHandler()}, withRecover(func(_ context.Context, e InflightEntry) bool {
			mu.Lock()
			defer mu.Unlock()
			seen = append(seen, e)
			return e.Type == "localai.agent.update"
		}))
		up := cr.leftover("job-up", "localai.agent.update", 120*time.Second)
		old := cr.leftover("job-old", "localai.model.load", 30*time.Second)
		cr.writeInflight(up, old)
		cr.start()
		cr.waitJob("job-old", fake.JobFailed) // the default handling still applies to the others
		cr.waitCount(fake.RouteClaim, 1)      // recovery is over before the first claim
		mu.Lock()
		defer mu.Unlock()
		if len(seen) != 2 {
			t.Fatalf("the hook saw %d entries", len(seen))
		}
		if cr.srv.Count(fake.RouteFailure) != 1 {
			t.Errorf("failures = %d, want only the model.load one", cr.srv.Count(fake.RouteFailure))
		}
		got, _ := cr.claimer.d.Inflight.Load()
		if len(got) != 1 || got[0].JobID != "job-up" {
			t.Errorf("inflight = %+v: the hook's entry is its own to remove", got)
		}
	})
}

func TestACorruptInflightFileIsDiscarded(t *testing.T) {
	cr := newClaimRig(t, []*testHandler{okHandler()})
	if err := os.MkdirAll(cr.inflight[:strings.LastIndex(cr.inflight, "/")], 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cr.inflight, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	cr.start()
	cr.waitCount(fake.RouteClaim, 1)
	if _, err := os.Stat(cr.inflight); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a corrupt inflight.json was kept: %v", err)
	}
}

// Recovery happens once per process: a claim loop that starts again after a stop does not look at the
// file again (its own commands are tracked in memory).
func TestRecoveryRunsOnce(t *testing.T) {
	cr := newClaimRig(t, []*testHandler{okHandler()})
	cr.start()
	cr.waitCount(fake.RouteClaim, 1)
	if err := cr.claimer.Stop(cr.ctx); err != nil {
		t.Fatal(err)
	}
	cr.writeInflight(cr.leftover("job-late", "localai.model.load", 30*time.Second))
	cr.claimer.Start(cr.ctx)
	cr.waitCount(fake.RouteClaim, 2)
	if cr.srv.Count(fake.RouteFailure) != 0 {
		t.Error("the file was read again")
	}
}

// What AGENT-19 uses after a restart it caused: settle a command from its persisted entry.
func TestSettleResultAndFailureOfAnEntryFromAnEarlierProcess(t *testing.T) {
	cr := newClaimRig(t, []*testHandler{okHandler()}, withoutLoop())
	up := cr.leftover("job-up", "localai.agent.update", 120*time.Second)
	cr.writeInflight(up)
	// Not linked yet: nothing is sent and the entry stays.
	if err := cr.claimer.SettleResult(cr.ctx, up, map[string]any{}); !errors.Is(err, ErrNotLinked) {
		t.Fatalf("SettleResult before the link is up = %v", err)
	}
	if got, _ := cr.claimer.d.Inflight.Load(); len(got) != 1 {
		t.Fatalf("inflight = %v", got)
	}
	cr.start()

	res := map[string]any{"from_version": "0.1.0", "to_version": "0.2.0", "status": "updated"}
	if err := cr.claimer.SettleResult(cr.ctx, up, res); err != nil {
		t.Fatal(err)
	}
	rec, _ := cr.srv.Job("job-up")
	if rec.Status != fake.JobSucceeded || !strings.Contains(string(rec.Result), `"to_version":"0.2.0"`) {
		t.Errorf("job = %+v", rec)
	}
	if got, _ := cr.claimer.d.Inflight.Load(); len(got) != 0 {
		t.Errorf("inflight = %v", got)
	}

	down := cr.leftover("job-down", "localai.agent.update", 120*time.Second)
	cr.writeInflight(down)
	if err := cr.claimer.SettleFailure(cr.ctx, down, doctor.AGT009, "the update was rolled back"); err != nil {
		t.Fatal(err)
	}
	rec, _ = cr.srv.Job("job-down")
	if rec.Status != fake.JobFailed || rec.Error != "LAI-AGT-009: the update was rolled back" {
		t.Errorf("job = %+v", rec)
	}
	if got, _ := cr.claimer.d.Inflight.Load(); len(got) != 0 {
		t.Errorf("inflight = %v", got)
	}
}
