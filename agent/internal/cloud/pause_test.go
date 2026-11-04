package cloud

import (
	"errors"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/cloud/fake"
)

// The shape internal/drill.LinkPauser asks for (drill imports this package, so it cannot be named here).
var _ interface {
	Pause()
	Resume()
} = (*Link)(nil)

// quietTurn lets the heartbeat loop take one due turn (it must skip it while paused).
func (r *rig) quietTurn() {
	r.t.Helper()
	r.step(15 * time.Second)
	r.waitTimers(2)
}

func TestPauseStopsEveryOutboundCallAndResumeSendsAtOnce(t *testing.T) {
	r := newRig(t)
	r.enroll()
	r.waitHeartbeatApplied(1)
	r.waitTimers(2)

	r.link.Pause()
	if !r.link.Paused() || !r.link.CloudStatus().Paused {
		t.Fatal("the pause is not visible")
	}
	before := len(r.srv.Requests())
	for i := 0; i < 3; i++ {
		r.quietTurn()
	}
	if got := len(r.srv.Requests()); got != before {
		t.Fatalf("%d requests while paused", got-before)
	}
	if r.link.State() != StateLinked {
		t.Errorf("state %s: a pause must keep the session", r.link.State())
	}
	if _, ok := r.link.Session(); ok {
		t.Error("the claim loop still gets a session while paused")
	}

	// Resume: the heartbeat goes out at once (no clock step), and the state loop is woken (it still honours the cloud's minimum interval).
	r.link.Resume()
	if r.link.Paused() || r.link.CloudStatus().Paused {
		t.Fatal("still paused after Resume")
	}
	r.waitCount(fake.RouteHeartbeat, 2)
}

func TestPauseDuringAHeartbeatLetsItFinishAndSendsNothingElse(t *testing.T) {
	r := newRig(t)
	release := r.srv.Hold(fake.RouteHeartbeat)
	r.enroll()
	r.waitCount(fake.RouteHeartbeat, 1) // on the wire, the answer is held
	r.link.Pause()
	release()
	// The answer is applied as usual and the loop re-arms its timer.
	r.waitTimers(2)
	r.waitState(StateLinked)
	n := len(r.srv.Requests())
	r.quietTurn()
	if got := len(r.srv.Requests()); got != n {
		t.Fatalf("%d requests after the pause", got-n)
	}
	r.link.Resume()
	r.waitCount(fake.RouteHeartbeat, 2)
}

func TestResumeAfterFailedHeartbeatsRecovers(t *testing.T) {
	r := newRig(t)
	r.srv.Forever(fake.RouteHeartbeat, fake.Err(500, "LAI-CLD-054", "boom"))
	r.enroll()
	r.waitCount(fake.RouteHeartbeat, 1)
	r.link.Pause()
	r.waitTimers(2)
	n := r.srv.Count(fake.RouteHeartbeat)
	r.quietTurn()
	if r.srv.Count(fake.RouteHeartbeat) != n {
		t.Fatal("a heartbeat was sent while paused")
	}
	r.srv.Clear(fake.RouteHeartbeat)
	r.link.Resume()
	r.waitHeartbeatApplied(n + 1)
	r.waitState(StateLinked)
}

func TestPausedClientRefusesWithoutConnecting(t *testing.T) {
	r := newRig(t)
	r.enroll()
	r.waitHeartbeatApplied(1)
	r.link.Pause()
	n := len(r.srv.Requests())
	_, err := r.link.client().Call(r.ctx, "GET", "/api/anything", nil, time.Second)
	var ne *NetError
	if !errors.As(err, &ne) || !errors.Is(err, ErrPaused) {
		t.Fatalf("err = %v, want a NetError wrapping ErrPaused", err)
	}
	if len(r.srv.Requests()) != n {
		t.Error("the fake saw a request")
	}
}

func TestPauseAndResumeAreIdempotentAndSafeWhenNotRunning(t *testing.T) {
	r := newRig(t)
	r.link.Resume() // not paused: nothing happens
	r.link.Pause()
	r.link.Pause()
	if !r.link.Paused() {
		t.Fatal("not paused")
	}
	r.link.Resume()
	r.link.Resume()
	if r.link.Paused() {
		t.Fatal("still paused")
	}
	if len(r.srv.Requests()) != 0 {
		t.Error("an unenrolled link sent something")
	}
}
