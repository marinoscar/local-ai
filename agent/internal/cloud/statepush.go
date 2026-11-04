package cloud

import (
	"context"
	"errors"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/events"
)

// ErrNoStateEndpoint: the cloud has no state endpoint (the reduced snapshot rides in the heartbeat).
var ErrNoStateEndpoint = errors.New("cloud: the cloud has no state endpoint")

// stateTriggers are the event types that make a new snapshot due (agent.md section 4.8.4).
var stateTriggers = []string{"model.*", "mode.*", "service.state", "sync.*", "alert.*", "drive.*", "key.rotated"}

type pushOutcome int

const (
	pushOK pushOutcome = iota
	// pushIgnored: 409 stale_seq, nothing to do.
	pushIgnored
	// pushBadRequest: 400; the Zod paths were logged, the next change sends a fresh snapshot.
	pushBadRequest
	// pushFallback: the cloud has no state endpoint; the capabilities fallback is on.
	pushFallback
	pushFailed
)

type pushResult struct {
	outcome    pushOutcome
	seq        int64
	reportedAt string
	retryAfter time.Duration
	err        error
}

// eventLoop feeds the heartbeat's event queue and wakes the state push loop from the agent's event
// bus. It resubscribes when the bus drops it for being slow.
func (l *Link) eventLoop(ctx context.Context) {
	if l.d.Events == nil {
		return
	}
	filter, err := events.NewFilter(stateTriggers...)
	if err != nil {
		l.log.Error("the event filter of the cloud link is invalid", "error", err.Error())
		return
	}
	for ctx.Err() == nil {
		ch, err := l.d.Events.Subscribe(ctx, filter, 0)
		if err != nil {
			if !l.wait(ctx, 5*time.Second) {
				return
			}
			continue
		}
		for ev := range ch {
			if he, ok := FromBusEvent(ev); ok {
				l.evq.Push(he)
			}
			l.kick(l.stateWake)
		}
		if !l.wait(ctx, time.Second) {
			return
		}
	}
}

// stateLoop pushes a snapshot at link start, then on every relevant change debounced to
// stateMinIntervalS, and at least every stateMaxIntervalS. While the cloud has no state endpoint it
// only re-runs the handshake once an hour to see whether one appeared.
func (l *Link) stateLoop(ctx context.Context) {
	bo := NewBackoff(l.rnd)
	var lastPush, holdUntil time.Time
	dirty := true
	for {
		if l.inStateFallback() {
			if !l.wait(ctx, capabilitiesProbe) {
				return
			}
			l.recheckHandshake(ctx)
			lastPush, dirty = time.Time{}, true
			continue
		}
		minI, maxI := l.stateIntervals()
		now := l.clk.Now()
		due := now
		switch {
		case lastPush.IsZero():
		case dirty:
			due = lastPush.Add(minI)
		default:
			due = lastPush.Add(maxI)
		}
		if holdUntil.After(due) {
			due = holdUntil
		}
		if d := due.Sub(now); d > 0 {
			t := l.clk.NewTimer(d)
			select {
			case <-ctx.Done():
				t.Stop()
				return
			case <-l.stateWake:
				t.Stop()
				dirty = true
				continue
			case <-t.C():
			}
		} else if ctx.Err() != nil {
			return
		}
		if !l.active() {
			lastPush = l.clk.Now()
			continue
		}
		res := l.pushStateOnce(ctx, callTimeout)
		lastPush = l.clk.Now()
		dirty = false
		holdUntil = time.Time{}
		switch res.outcome {
		case pushOK, pushIgnored, pushBadRequest:
			bo.Reset()
		case pushFallback:
			bo.Reset()
		case pushFailed:
			dirty = true
			holdUntil = l.clk.Now().Add(bo.Fail(res.retryAfter))
		}
	}
}

func (l *Link) inStateFallback() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.st.stateFallback
}

// recheckHandshake runs the handshake again (a 404 from a /localai/ route, or the hourly probe of the
// fallback). A 401 revokes the link; other failures are logged and left for the next attempt.
func (l *Link) recheckHandshake(ctx context.Context) {
	if err := l.handshake(ctx); err != nil {
		if l.noteAuth(err) {
			return
		}
		l.log.Debug("the handshake could not be repeated", "error", errText(err))
	}
}

// pushStateOnce builds a snapshot and sends it. Answers follow agent.md section 4.8.4: 200 is ok, 409
// stale_seq is ignored, 400 logs the Zod paths, 404 and 405 re-run the handshake and may switch on the
// capabilities fallback (LAI-CLD-056).
func (l *Link) pushStateOnce(ctx context.Context, timeout time.Duration) pushResult {
	path, ok := l.serverPath("/localai/state")
	if !ok {
		return pushResult{outcome: pushFailed, err: ErrNotLinked}
	}
	b, err := l.snaps.Build(ctx)
	if err != nil {
		l.log.Warn("a state snapshot could not be built", "error", errText(err))
		return pushResult{outcome: pushFailed, err: err}
	}
	l.logDropped("state snapshot", b.Dropped)
	if len(b.Trimmed) > 0 {
		l.log.Info("the state snapshot was cut to fit the size limit", "cut", b.Trimmed, "bytes", len(b.Body))
	}
	res := pushResult{seq: b.Snapshot.Seq, reportedAt: b.Snapshot.ReportedAt}
	_, err = l.client().Call(ctx, "POST", path, b.Body, timeout)
	if err == nil {
		b.Commit()
		res.outcome = pushOK
		return res
	}
	res.err = err
	ae := asAPIError(err)
	if ae == nil {
		res.outcome = pushFailed
		return res
	}
	res.retryAfter = ae.RetryAfter
	switch {
	case ae.Status == 409 && (ae.Code == doctor.CLD008 || ae.Reason == "stale_seq"):
		res.outcome = pushIgnored
	case ae.Status == 400:
		l.log.Warn("the cloud refused a state snapshot", "code", doctor.CLD008, "cloud_code", ae.Code, "reason", ae.Reason, "paths", ae.Paths)
		res.outcome = pushBadRequest
	case ae.Status == 404 || ae.Status == 405:
		l.recheckHandshake(ctx)
		if l.inStateFallback() {
			res.outcome = pushFallback
		} else {
			// The endpoint exists, so the node is what the cloud does not know: register again.
			l.signalRelink(relinkReq{reason: "the cloud does not know this node"})
			res.outcome = pushFailed
		}
	case ae.Status == 401:
		l.noteAuth(err)
		res.outcome = pushFailed
	default:
		res.outcome = pushFailed
	}
	return res
}

// StateRefresh is what PushNow reports.
type StateRefresh struct {
	Seq        int64
	ReportedAt string
}

// PushNow builds a snapshot and sends it now (the status.refresh command, agent.md section 4.8.6). It
// fails with ErrNotLinked unless the node is registered and with ErrNoStateEndpoint while the
// capabilities fallback is on (the heartbeat carries the state then).
func (l *Link) PushNow(ctx context.Context) (StateRefresh, error) {
	if !l.callable() {
		return StateRefresh{}, ErrNotLinked
	}
	if !l.hasStateEndpoint() {
		return StateRefresh{}, ErrNoStateEndpoint
	}
	res := l.pushStateOnce(ctx, callTimeout)
	switch res.outcome {
	case pushOK, pushIgnored:
		return StateRefresh{Seq: res.seq, ReportedAt: res.reportedAt}, nil
	case pushFallback:
		return StateRefresh{}, ErrNoStateEndpoint
	}
	if res.err == nil {
		res.err = errors.New("cloud: the state snapshot was refused")
	}
	return StateRefresh{}, res.err
}
