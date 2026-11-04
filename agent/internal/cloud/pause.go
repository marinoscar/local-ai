package cloud

import (
	"errors"
	"net/http"
	"sync/atomic"
)

// Pause and resume of the link (drill.md section 4.4). A drill refuses every non-loopback dial, and the
// cloud link is the one component that would try to reach the internet, so while a drill runs the link
// stands still: no heartbeat, no state push, no key push, no claim, no register. The session is kept
// (nothing is deregistered and the state does not change), so the cloud only sees a gap in heartbeats.

// ErrPaused is the error of a call refused because the link is paused. The loops do not call while
// paused, so this is the backstop for a call that starts just after Pause ran.
var ErrPaused = errors.New("cloud: the link is paused")

// Pause stops the link's outbound traffic without ending its session. A call already on the wire is
// left to finish (its answer is handled normally); no new call is started until Resume. It is safe to
// call when the link is not running and to call twice.
func (l *Link) Pause() {
	if l.paused.Swap(true) {
		return
	}
	l.log.Info("the cloud link is paused")
}

// Resume lets the link go on after Pause: the heartbeat is sent at once, then the state and the key are
// pushed again. It is safe to call when the link is not paused or not running.
func (l *Link) Resume() {
	if !l.paused.Swap(false) {
		return
	}
	l.log.Info("the cloud link is resumed")
	l.kick(l.hbWake)
	l.kick(l.stateWake)
	l.kick(l.keyWake)
}

// Paused says whether the link is paused (GET /cloud shows it).
func (l *Link) Paused() bool { return l.paused.Load() }

// pauseGate is the Doer every call of the link goes through; while the link is paused it refuses the
// call before any connection is made.
type pauseGate struct {
	next   Doer
	paused *atomic.Bool
}

// Do implements Doer.
func (g pauseGate) Do(req *http.Request) (*http.Response, error) {
	if g.paused.Load() {
		return nil, &NetError{Msg: "the link is paused", err: ErrPaused}
	}
	return g.next.Do(req)
}
