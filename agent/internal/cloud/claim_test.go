package cloud

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/audit"
	"github.com/marinoscar/local-ai/agent/internal/cloud/fake"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
)

// The claim loop against the fake cloud: both planes, the pacing, the state machine rows of agent.md
// section 4.8.1 that the claim drives, and the backoff. The clock is manual and nothing sleeps.

func TestLocalaiPlaneLongPollRequestAndPacing(t *testing.T) {
	cr := newClaimRig(t, []*testHandler{okHandler(), newTestHandler(TypeModelLoad, nil)})
	cr.start()
	cr.waitCount(fake.RouteClaim, 1)

	req := cr.claimRequest(0)
	if strings.Join(req.Types, ",") != "status.refresh,model.load" && strings.Join(req.Types, ",") != "model.load,status.refresh" {
		t.Errorf("types = %v: want the allowed and registered names without the prefix", req.Types)
	}
	if req.Limit != 1 || req.WaitMs == nil || *req.WaitMs != 25000 {
		t.Errorf("limit %d waitMs %v, want 1 and 25000", req.Limit, req.WaitMs)
	}
	p := cr.srv.RequestsFor(fake.RouteClaim)[0].Path
	if !strings.HasSuffix(p, "/localai/commands/claim") {
		t.Errorf("path = %s", p)
	}

	// An empty answer is claimed again after 250 ms, not at once and not after 3 s.
	cr.waitTimers(1)
	if got := cr.srv.Count(fake.RouteClaim); got != 1 {
		t.Fatalf("claims before the pause ended = %d, want 1", got)
	}
	cr.step(250 * time.Millisecond)
	cr.waitCount(fake.RouteClaim, 2)
	cr.step(250 * time.Millisecond)
	cr.waitCount(fake.RouteClaim, 3)
	if n := cr.link.Counters().EmptyPolls.Load(); n < 2 {
		t.Errorf("empty polls = %d", n)
	}
}

func TestLongPollIsClampedToTheHandshake(t *testing.T) {
	cr := newClaimRig(t, []*testHandler{okHandler()})
	cr.srv.Handshake.ClaimWaitMaxMs = 12000
	cr.start()
	cr.waitCount(fake.RouteClaim, 1)
	if req := cr.claimRequest(0); req.WaitMs == nil || *req.WaitMs != 12000 {
		t.Errorf("waitMs = %v, want 12000", req.WaitMs)
	}
}

// A cloud that holds no request (claimWaitMaxMs 0) is polled like the plain plane, every
// cloud.poll.claim_seconds, so the loop never spins.
func TestNoLongPollPacesLikeAPlainPoll(t *testing.T) {
	cr := newClaimRig(t, []*testHandler{okHandler()})
	cr.srv.Handshake.ClaimWaitMaxMs = 0
	cr.start()
	cr.waitCount(fake.RouteClaim, 1)
	cr.dueWithin(3 * time.Second)
	cr.clk.Advance(250 * time.Millisecond)
	cr.step(2750 * time.Millisecond)
	cr.waitCount(fake.RouteClaim, 2)
	if got := cr.srv.Count(fake.RouteClaim); got != 2 {
		t.Fatalf("a claim went out 250 ms after an empty answer: %d claims", got)
	}
}

func TestEABPlaneRequestAndThreeSecondPacing(t *testing.T) {
	cr := newClaimRig(t, []*testHandler{okHandler()})
	cr.srv.Handshake.CommandPlane = "eab"
	cr.rewriteNodeYAML("  concurrency: 2\n")
	cr.cfg.ReloadNow()
	cr.start()
	cr.waitCount(fake.RouteClaim, 1)

	id, _ := cr.link.hasServer()
	if p := cr.srv.RequestsFor(fake.RouteClaim)[0].Path; p != "/api/nodes/"+id+"/claim" {
		t.Errorf("path = %s", p)
	}
	req := cr.claimRequest(0)
	if len(req.Types) != 1 || req.Types[0] != "localai.status.refresh" || req.Limit != 2 || req.WaitMs != nil {
		t.Errorf("request = %+v", req)
	}
	cr.dueWithin(3 * time.Second)
	cr.clk.Advance(250 * time.Millisecond)
	cr.step(2750 * time.Millisecond)
	cr.waitCount(fake.RouteClaim, 2)
	if got := cr.srv.Count(fake.RouteClaim); got != 2 {
		t.Fatalf("a plain poll came back after 250 ms: %d claims", got)
	}
}

func TestEABPlaneRunsUpToConcurrencyAndSettlesOnTheJobsPaths(t *testing.T) {
	release := make(chan struct{})
	h := blockingHandler(TypeStatusRefresh, release, false)
	cr := newClaimRig(t, []*testHandler{h})
	cr.srv.Handshake.CommandPlane = "eab"
	cr.rewriteNodeYAML("  concurrency: 2\n")
	cr.cfg.ReloadNow()
	cr.queue("job-a", "localai.status.refresh", map[string]any{})
	cr.queue("job-b", "localai.status.refresh", map[string]any{})
	cr.start()
	h.waitStarted(t, cr.ctx)
	h.waitStarted(t, cr.ctx)
	if n, info := cr.claimer.Inflight(); n != 2 || info == nil {
		t.Errorf("Inflight() = %d %v", n, info)
	}
	if info := func() map[string]any { _, i := cr.claimer.Inflight(); return i }(); info["claim_token"] != nil || info["job_id"] != "job-a" {
		t.Errorf("the inflight record = %v: it must not carry the claim token", info)
	}
	// Both slots are taken: no claim is made while they are.
	claims := cr.srv.Count(fake.RouteClaim)
	doneA, doneB := cr.claimer.jobDone("job-a"), cr.claimer.jobDone("job-b")
	close(release)
	cr.waitJob("job-a", fake.JobSucceeded)
	cr.waitJob("job-b", fake.JobSucceeded)
	cr.waitDone(doneA)
	cr.waitDone(doneB)
	if claims != 1 {
		t.Errorf("claims while the slots were full = %d, want 1", claims)
	}
	id, _ := cr.link.hasServer()
	for _, r := range cr.srv.RequestsFor(fake.RouteResult) {
		if !strings.HasPrefix(r.Path, "/api/nodes/"+id+"/jobs/job-") || !strings.HasSuffix(r.Path, "/result") {
			t.Errorf("result path = %s", r.Path)
		}
	}
}

// The localai plane runs one command at a time whatever cloud.concurrency says: the cloud hands a
// node one running command.
func TestLocalaiPlaneRunsOneAtATime(t *testing.T) {
	release := make(chan struct{})
	h := blockingHandler(TypeStatusRefresh, release, false)
	cr := newClaimRig(t, []*testHandler{h})
	cr.rewriteNodeYAML("  concurrency: 4\n")
	cr.cfg.ReloadNow()
	cr.queue("job-a", "localai.status.refresh", map[string]any{})
	cr.queue("job-b", "localai.status.refresh", map[string]any{})
	cr.start()
	h.waitStarted(t, cr.ctx)
	if n, _ := cr.claimer.Inflight(); n != 1 {
		t.Errorf("in flight = %d, want 1", n)
	}
	doneA := cr.claimer.jobDone("job-a")
	close(release)
	cr.waitJob("job-a", fake.JobSucceeded)
	cr.waitDone(doneA)
	h.waitStarted(t, cr.ctx)
	cr.waitJob("job-b", fake.JobSucceeded)
	cr.waitDone(cr.claimer.jobDone("job-b"))
}

func TestClaimToExecuteToResult(t *testing.T) {
	release := make(chan struct{})
	h := blockingHandler(TypeStatusRefresh, release, false)
	cr := newClaimRig(t, []*testHandler{h})
	cr.queue("job-1", "localai.status.refresh", map[string]any{})
	cr.start()
	job := h.waitStarted(t, cr.ctx)
	if job.ID != "job-1" || job.Type != "localai.status.refresh" || job.Plane != PlaneLocalai || job.Actor != audit.Cloud("job-1") {
		t.Errorf("job = %+v", job)
	}

	// Step 3: the entry is on disk before the operation runs, root 0600, with the claim token.
	entries, err := cr.claimer.d.Inflight.Load()
	if err != nil || len(entries) != 1 {
		t.Fatalf("inflight = %v %v", entries, err)
	}
	e := entries[0]
	if e.JobID != "job-1" || e.Type != "localai.status.refresh" || e.Plane != "localai" || e.ClaimToken != "tok-job-1" || e.StartedAt.IsZero() || e.LeaseExpiresAt.IsZero() {
		t.Errorf("entry = %+v", e)
	}
	if fi := statFile(t, cr.inflight); fi.Mode().Perm() != 0o600 {
		t.Errorf("inflight.json mode = %v, want 0600", fi.Mode().Perm())
	}
	if n, info := cr.claimer.Inflight(); n != 1 || info["job_id"] != "job-1" || info["type"] != "localai.status.refresh" {
		t.Errorf("Inflight() = %d %v", n, info)
	}
	if got := cr.link.CloudStatus().Inflight; got["job_id"] != "job-1" {
		t.Errorf("GET /cloud inflight = %v", got)
	}

	done := cr.claimer.jobDone("job-1")
	close(release)
	rec := cr.waitJob("job-1", fake.JobSucceeded)
	cr.waitDone(done)

	// Step 6: result {type, claimToken, result}.
	var body struct {
		Type, ClaimToken string
		Result           map[string]any
	}
	if err := json.Unmarshal(cr.srv.RequestsFor(fake.RouteResult)[0].Body, &body); err != nil {
		t.Fatal(err)
	}
	if body.Type != "localai.status.refresh" || body.ClaimToken != "tok-job-1" || body.Result["seq"] != float64(1) {
		t.Errorf("result body = %+v", body)
	}
	if string(rec.Result) == "" {
		t.Error("the cloud has no result")
	}
	// Step 7: the entry is gone and a snapshot goes out.
	if entries, _ := cr.claimer.d.Inflight.Load(); len(entries) != 0 {
		t.Errorf("inflight after the job = %v", entries)
	}
	if n, info := cr.claimer.Inflight(); n != 0 || info != nil {
		t.Errorf("Inflight() after the job = %d %v", n, info)
	}
	// The audit line: type, result and the cloud actor.
	evs := cr.audit.wait(t, cr.ctx, 1)
	ev := evs[0]
	if ev.Action != audit.ActionCloudJob || ev.Result != audit.ResultOK || ev.Actor.Kind != audit.KindCloud || ev.Actor.JobID != "job-1" || ev.Target["type"] != "localai.status.refresh" {
		t.Errorf("audit = %+v", ev)
	}
	if c := cr.link.Counters(); c.Claims.Load() != 1 || c.Succeeded.Load() != 1 || c.Failed.Load() != 0 {
		t.Errorf("counters = %+v", c.Wire())
	}
}

// An assignment without a claim token: the renew and result bodies leave the key out (the fake answers
// 400 to a "claimToken": null).
func TestAssignmentWithoutAClaimTokenOmitsTheKey(t *testing.T) {
	release := make(chan struct{})
	h := blockingHandler(TypeStatusRefresh, release, false)
	cr := newClaimRig(t, []*testHandler{h})
	cr.srv.Handshake.HeartbeatIntervalS = 60
	cr.srv.QueueJob(fake.Job{ID: "job-n", Type: "localai.status.refresh", Params: map[string]any{}, RenewIntervalMs: 7000})
	cr.start()
	h.waitStarted(t, cr.ctx)
	cr.waitTimers(2)
	cr.step(7 * time.Second)
	cr.waitCount(fake.RouteRenew, 1)
	done := cr.claimer.jobDone("job-n")
	close(release)
	cr.waitJob("job-n", fake.JobSucceeded)
	cr.waitDone(done)
	for _, route := range []string{fake.RouteRenew, fake.RouteResult} {
		for _, r := range cr.srv.RequestsFor(route) {
			if strings.Contains(string(r.Body), "claimToken") {
				t.Errorf("%s body = %s: the key must be omitted", route, r.Body)
			}
		}
	}
	if entries, _ := cr.claimer.d.Inflight.Load(); len(entries) != 0 {
		t.Errorf("inflight = %v", entries)
	}
}

// ---------------------------------------------------------------------------------------------------
// The state machine rows the claim drives

func TestClaim401RevokesTheNode(t *testing.T) {
	cr := newClaimRig(t, []*testHandler{okHandler()})
	cr.srv.Script(fake.RouteClaim, fake.Reply{Status: 401})
	cr.start()
	cr.waitState(StateRevoked)
	cr.waitAlert(doctor.CLD051, true)
}

func TestClaim403DisablesAndEachHeartbeatReChecks(t *testing.T) {
	cr := newClaimRig(t, []*testHandler{okHandler()})
	cr.srv.Locked(func() { cr.srv.Disabled = true })
	cr.start()
	cr.waitState(StateDisabled)
	cr.waitAlert(doctor.CLD052, true)

	// While disabled the loop makes no claim of its own: only the heartbeat's probe does.
	before := cr.srv.Count(fake.RouteClaim)
	cr.waitTimers(1)
	cr.srv.Locked(func() { cr.srv.Disabled = false })
	cr.queue("job-d", "localai.status.refresh", map[string]any{})
	// The disabled heartbeat period is 300 s; the heartbeat calls Probe, which claims once without a
	// long poll and runs what it finds.
	cr.step(300 * time.Second)
	cr.waitState(StateLinked)
	cr.waitJob("job-d", fake.JobSucceeded)
	reqs := cr.srv.RequestsFor(fake.RouteClaim)
	if len(reqs) <= before {
		t.Fatalf("no probe claim: %d requests", len(reqs))
	}
	var probe claimRequest
	if err := reqs[before].JSON(&probe); err != nil {
		t.Fatal(err)
	}
	if probe.WaitMs == nil || *probe.WaitMs != 0 {
		t.Errorf("the probe claim waited: %+v", probe)
	}
	cr.waitAlert(doctor.CLD052, false)
}

func TestClaim404RegistersAgain(t *testing.T) {
	cr := newClaimRig(t, []*testHandler{okHandler()})
	cr.srv.Script(fake.RouteClaim, fake.Err(404, "", ""))
	cr.start()
	cr.waitCount(fake.RouteRegister, 2)
	cr.waitState(StateLinked)
	// The loop starts again with the new registration and claims.
	cr.waitCount(fake.RouteClaim, 2)
}

func TestClaimFailureBacksOffThenReturnsToLinked(t *testing.T) {
	cr := newClaimRig(t, []*testHandler{okHandler()})
	cr.srv.Script(fake.RouteClaim, fake.Reply{Status: 503}, fake.Reply{Status: 503})
	cr.start()
	cr.waitState(StateBackoff)
	if n := cr.link.Counters().ClaimFailures.Load(); n != 1 {
		t.Errorf("claim failures = %d", n)
	}
	cr.dueWithin(5 * time.Second)
	// The first retry comes after the lower bound of the jitter window, 5 s (never at once).
	cr.clk.Advance(4 * time.Second)
	cr.step(time.Second)
	cr.waitCount(fake.RouteClaim, 2)
	if got := cr.srv.Count(fake.RouteClaim); got != 2 {
		t.Fatalf("a retry came before the backoff ended: %d claims", got)
	}
	// The second failure doubles the window: lower bound still 5 s here, upper 20 s; this test's
	// jitter takes the lower bound.
	cr.waitTimers(1)
	cr.step(5 * time.Second)
	cr.waitCount(fake.RouteClaim, 3)
	cr.waitState(StateLinked)
}

func TestClaim429CountsAsRateLimitedAndHonoursRetryAfter(t *testing.T) {
	cr := newClaimRig(t, []*testHandler{okHandler()})
	cr.srv.Script(fake.RouteClaim, fake.Reply{Status: 429, Header: map[string]string{"Retry-After": "30"}})
	cr.start()
	cr.waitCount(fake.RouteClaim, 1)
	cr.dueWithin(30 * time.Second)
	cr.waitState(StateBackoff)
	if n := cr.link.Counters().RateLimited.Load(); n != 1 {
		t.Errorf("rate limited = %d", n)
	}
	cr.clk.Advance(29 * time.Second)
	cr.step(time.Second)
	cr.waitCount(fake.RouteClaim, 2)
	if got := cr.srv.Count(fake.RouteClaim); got != 2 {
		t.Fatalf("the loop claimed before Retry-After: %d claims", got)
	}
}

func TestMalformedClaimAnswerIsAFailureNotACrash(t *testing.T) {
	cr := newClaimRig(t, []*testHandler{okHandler()})
	cr.srv.Script(fake.RouteClaim, fake.Reply{Status: 200, Body: map[string]any{"data": map[string]any{"jobs": "not a list"}}})
	cr.start()
	cr.waitState(StateBackoff)
	cr.waitTimers(1)
	cr.step(5 * time.Second)
	cr.waitCount(fake.RouteClaim, 2)
	cr.waitState(StateLinked)
}

func TestNothingIsClaimedOutsideOnlineMode(t *testing.T) {
	cr := newClaimRig(t, []*testHandler{okHandler()})
	cr.start()
	cr.waitCount(fake.RouteClaim, 1)
	cr.waitTimers(1)
	// The node goes offline: the next pass of the loop waits instead of claiming.
	cr.online.Store(false)
	cr.step(250 * time.Millisecond)
	cr.waitTimers(1)
	if err := cr.clk.WaitDue(cr.ctx, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if got := cr.srv.Count(fake.RouteClaim); got != 1 {
		t.Fatalf("claims while offline = %d", got)
	}
	cr.online.Store(true)
	cr.step(5 * time.Second)
	cr.waitCount(fake.RouteClaim, 2)
}

func TestNoAllowedTypeMeansNoClaim(t *testing.T) {
	cr := newClaimRig(t, []*testHandler{okHandler()}, withRigOpts(func(o *rigOptions) { o.allow = []string{"model.delete"} }))
	cr.start()
	cr.waitTimers(1)
	if got := cr.srv.Count(fake.RouteClaim); got != 0 {
		t.Errorf("claims with nothing allowed that the build can run = %d", got)
	}
}

// ---------------------------------------------------------------------------------------------------
// Stop and start

// A claim loop that is stopped cancels its long poll, and starts again afterwards.
func TestStopCancelsTheLongPollAndTheLoopRestarts(t *testing.T) {
	cr := newClaimRig(t, []*testHandler{okHandler()})
	cr.srv.HoldClaims(true)
	cr.start()
	if err := cr.srv.WaitHeld(cr.ctx, 1); err != nil {
		t.Fatal(err)
	}
	if err := cr.claimer.Stop(cr.ctx); err != nil {
		t.Fatal(err)
	}
	// Stop is idempotent.
	if err := cr.claimer.Stop(cr.ctx); err != nil {
		t.Fatal(err)
	}
	if st := cr.link.State(); st != StateLinked {
		t.Errorf("a cancelled long poll moved the link to %s", st)
	}
	if n := cr.link.Counters().ClaimFailures.Load(); n != 0 {
		t.Errorf("a cancelled long poll counted as a failure: %d", n)
	}
	// The fake has noticed that the cancelled request is gone, or it could still answer it with the job.
	if err := cr.srv.WaitHeldAtMost(cr.ctx, 0); err != nil {
		t.Fatal(err)
	}
	cr.srv.HoldClaims(false)
	cr.srv.QueueJob(fake.Job{ID: "job-r", Type: "localai.status.refresh", Params: map[string]any{}, Token: "tok-r"})
	cr.claimer.Start(cr.ctx)
	cr.waitJob("job-r", fake.JobSucceeded)
	cr.waitDone(cr.claimer.jobDone("job-r"))
}

func TestStartWithoutALinkDoesNothing(t *testing.T) {
	reg, _ := NewRegistry(okHandler())
	c, err := NewClaimer(ClaimerDeps{Handlers: reg})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c.Start(ctx)
	if err := c.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := NewClaimer(ClaimerDeps{}); err == nil {
		t.Error("a claimer without handlers was accepted")
	}
}

func TestRegistryRejectsUnknownAndDuplicateTypes(t *testing.T) {
	if _, err := NewRegistry(newTestHandler("rm.rf", nil)); err == nil {
		t.Error("an unknown type was accepted")
	}
	if _, err := NewRegistry(okHandler(), okHandler()); err == nil {
		t.Error("a duplicate type was accepted")
	}
	r, err := NewRegistry(okHandler())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := r.Lookup("status.refresh"); !ok {
		t.Error("lookup failed")
	}
	if _, ok := r.Lookup("model.delete"); ok {
		t.Error("lookup of a missing type succeeded")
	}
	var nilReg *Registry
	if _, ok := nilReg.Lookup("x"); ok || nilReg.Types() != nil {
		t.Error("a nil registry has types")
	}
}
