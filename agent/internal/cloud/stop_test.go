package cloud

import (
	"context"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/audit"
	"github.com/marinoscar/local-ai/agent/internal/cloud/fake"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
)

// The in-flight part of the offline stop (agent.md section 4.8.8, step 2): the claim loop stops, a
// running command gets 10 s, then it is failed with LAI-CLD-063 and cancelled when it can be.

const goingOfflineText = "LAI-CLD-063: the node is going offline"

func (cr *claimRig) stopAsync() <-chan error {
	errc := make(chan error, 1)
	go func() { errc <- cr.claimer.Stop(cr.ctx) }()
	return errc
}

func (cr *claimRig) waitErr(errc <-chan error) error {
	cr.t.Helper()
	select {
	case err := <-errc:
		return err
	case <-cr.ctx.Done():
		cr.t.Fatalf("Stop did not return: %v", cr.ctx.Err())
		return nil
	}
}

func TestStopFailsACommandThatOutlivesTenSecondsAndCancelsIt(t *testing.T) {
	release := make(chan struct{})
	h := blockingHandler(TypeStatusRefresh, release, true) // honours its context: cancellable
	cr := newClaimRig(t, []*testHandler{h}, withoutLoop())
	cr.start()
	cr.dispatchNow(fake.Job{ID: "job-1", Type: "localai.status.refresh", Params: map[string]any{}, Token: "tok-1"}, 20*time.Second)
	h.waitStarted(t, cr.ctx)
	done := cr.claimer.jobDone("job-1")

	errc := cr.stopAsync()
	cr.dueWithin(10 * time.Second)
	if n := cr.srv.Count(fake.RouteFailure); n != 0 {
		t.Fatalf("the command was failed before the 10 s were up: %d", n)
	}
	cr.step(10 * time.Second)
	if err := cr.waitErr(errc); err != nil {
		t.Fatal(err)
	}
	rec := cr.waitJob("job-1", fake.JobFailed)
	if rec.Error != goingOfflineText {
		t.Errorf("failure = %q, want %q", rec.Error, goingOfflineText)
	}
	// The cancelled operation ends; its outcome is dropped: exactly one settle, the failure.
	cr.waitDone(done)
	if cr.srv.Count(fake.RouteResult) != 0 || cr.srv.Count(fake.RouteFailure) != 1 {
		t.Errorf("results %d failures %d", cr.srv.Count(fake.RouteResult), cr.srv.Count(fake.RouteFailure))
	}
	if entries, _ := cr.claimer.d.Inflight.Load(); len(entries) != 0 {
		t.Errorf("inflight = %v", entries)
	}
	evs := cr.audit.wait(t, cr.ctx, 1)
	if len(evs) != 1 || evs[0].Result != audit.ResultFail || evs[0].Code != doctor.CLD063 {
		t.Errorf("audit = %+v", evs)
	}
	// Stop again: nothing left to do.
	if err := cr.claimer.Stop(cr.ctx); err != nil {
		t.Fatal(err)
	}
}

// A command that cannot be cancelled (a model load) is failed toward the cloud and carries on locally.
func TestStopLeavesANonCancellableCommandRunning(t *testing.T) {
	release := make(chan struct{})
	h := blockingHandler(TypeModelLoad, release, false)
	cr := newClaimRig(t, []*testHandler{h}, withoutLoop())
	cr.start()
	cr.dispatchNow(fake.Job{ID: "job-1", Type: "localai.model.load", Params: map[string]any{}, Token: "tok-1"}, 20*time.Second)
	h.waitStarted(t, cr.ctx)
	done := cr.claimer.jobDone("job-1")

	errc := cr.stopAsync()
	cr.dueWithin(10 * time.Second)
	cr.step(10 * time.Second)
	if err := cr.waitErr(errc); err != nil {
		t.Fatal(err)
	}
	if rec := cr.waitJob("job-1", fake.JobFailed); rec.Error != goingOfflineText {
		t.Errorf("failure = %q", rec.Error)
	}
	select {
	case <-done:
		t.Fatal("the operation was ended with the stop")
	default:
	}
	// The load finishes later: no second settle goes out, and the slot is freed.
	close(release)
	cr.waitDone(done)
	if cr.srv.Count(fake.RouteResult) != 0 || cr.srv.Count(fake.RouteFailure) != 1 {
		t.Errorf("results %d failures %d", cr.srv.Count(fake.RouteResult), cr.srv.Count(fake.RouteFailure))
	}
}

// A command that finishes inside the 10 s settles normally and the stop returns.
func TestStopWaitsForACommandThatFinishesInTime(t *testing.T) {
	release := make(chan struct{})
	h := blockingHandler(TypeStatusRefresh, release, true)
	cr := newClaimRig(t, []*testHandler{h}, withoutLoop())
	cr.start()
	cr.dispatchNow(fake.Job{ID: "job-1", Type: "localai.status.refresh", Params: map[string]any{}, Token: "tok-1"}, 20*time.Second)
	h.waitStarted(t, cr.ctx)

	errc := cr.stopAsync()
	cr.dueWithin(10 * time.Second)
	close(release)
	cr.waitJob("job-1", fake.JobSucceeded)
	if err := cr.waitErr(errc); err != nil {
		t.Fatal(err)
	}
	if cr.srv.Count(fake.RouteFailure) != 0 {
		t.Error("a command that finished in time was failed")
	}
}

// When the caller gives up first (a link that only registers again waits 5 s), the commands are left
// running and settle later.
func TestStopThatRunsOutOfTimeLeavesTheCommandsAlone(t *testing.T) {
	release := make(chan struct{})
	h := blockingHandler(TypeStatusRefresh, release, true)
	cr := newClaimRig(t, []*testHandler{h}, withoutLoop())
	cr.start()
	cr.dispatchNow(fake.Job{ID: "job-1", Type: "localai.status.refresh", Params: map[string]any{}, Token: "tok-1"}, 20*time.Second)
	h.waitStarted(t, cr.ctx)

	ctx, cancel := context.WithCancel(cr.ctx)
	errc := make(chan error, 1)
	go func() { errc <- cr.claimer.Stop(ctx) }()
	cr.dueWithin(10 * time.Second)
	cancel()
	if err := cr.waitErr(errc); err == nil {
		t.Fatal("Stop reported success although its context ended")
	}
	if cr.srv.Count(fake.RouteFailure) != 0 {
		t.Fatal("the command was failed")
	}
	close(release)
	cr.waitJob("job-1", fake.JobSucceeded)
}

// mode.offline has settled before its transition starts, and the stop that transition causes does not
// wait for it.
func TestAnInFlightModeOfflineIsNotWaitedFor(t *testing.T) {
	var settledBefore bool
	var stopErr error
	var cr *claimRig
	h := newTestHandler(TypeModeOffline, nil)
	h.run = func(_ context.Context, job Job, _ any) (Outcome, error) {
		return Outcome{
			Result: resultFor(TypeModeOffline),
			After: func(ctx context.Context) {
				rec, _ := cr.srv.Job(job.ID)
				settledBefore = rec.Status == fake.JobSucceeded
				// What the transition's link_stop step does: stop the link, claim loop first.
				stopErr = cr.link.Stop(ctx)
			},
		}, nil
	}
	cr = newClaimRig(t, []*testHandler{h})
	cr.queue("job-1", "localai.mode.offline", map[string]any{})
	cr.start()
	h.waitStarted(t, cr.ctx)
	cr.waitJob("job-1", fake.JobSucceeded)
	cr.waitDone(cr.claimer.jobDone("job-1"))

	if !settledBefore {
		t.Error("the transition started before the result was settled")
	}
	if stopErr != nil {
		t.Errorf("link.Stop = %v", stopErr)
	}
	if st := cr.link.State(); st != StateStopped {
		t.Errorf("state = %s, want stopped", st)
	}
	// The stop did not wait 10 s for the command: nothing advanced the clock, and no failure went out.
	if cr.srv.Count(fake.RouteFailure) != 0 {
		t.Error("the in-flight mode.offline was failed")
	}
	// The link stop ran: last snapshot, offline heartbeat, deregister.
	hbs := cr.srv.RequestsFor(fake.RouteHeartbeat)
	if len(hbs) == 0 || string(hbs[len(hbs)-1].Body) != `{"status":"offline"}` {
		t.Errorf("last heartbeat = %v", hbs)
	}
	if cr.srv.Count(fake.RouteDeregister) != 1 {
		t.Errorf("deregister = %d", cr.srv.Count(fake.RouteDeregister))
	}
	// A snapshot is not pushed after mode.offline by the pipeline: the stop sends the last one.
	evs := cr.audit.wait(t, cr.ctx, 1)
	if evs[0].Result != audit.ResultOK {
		t.Errorf("audit = %+v", evs[0])
	}
}

// The whole offline stop through the link: a running command, 10 s, LAI-CLD-063, then the last
// snapshot, the offline heartbeat and the deregister.
func TestLinkStopWithARunningCommand(t *testing.T) {
	release := make(chan struct{})
	h := blockingHandler(TypeStatusRefresh, release, true)
	cr := newClaimRig(t, []*testHandler{h})
	cr.queue("job-1", "localai.status.refresh", map[string]any{})
	cr.start()
	h.waitStarted(t, cr.ctx)

	errc := make(chan error, 1)
	go func() { errc <- cr.link.Stop(cr.ctx) }()
	cr.dueWithin(10 * time.Second)
	cr.step(10 * time.Second)
	if err := cr.waitErr(errc); err != nil {
		t.Fatal(err)
	}
	if rec := cr.waitJob("job-1", fake.JobFailed); rec.Error != goingOfflineText {
		t.Errorf("failure = %q", rec.Error)
	}
	if st := cr.link.State(); st != StateStopped {
		t.Errorf("state = %s", st)
	}
	if cr.srv.Count(fake.RouteDeregister) != 1 {
		t.Errorf("deregister = %d", cr.srv.Count(fake.RouteDeregister))
	}
	// No claim is made after the stop.
	claims := cr.srv.Count(fake.RouteClaim)
	cr.clk.Advance(time.Minute)
	if got := cr.srv.Count(fake.RouteClaim); got != claims {
		t.Errorf("claims after the stop: %d -> %d", claims, got)
	}
}
