package cloud

import (
	"context"
	"slices"
	"sync"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
)

// supervise is the goroutine behind the link. It alternates between the register phase (register and
// handshake, with backoff; hourly probes while revoked or refused) and the linked phase (the heartbeat,
// state push, key and event loops plus the claim loop), and returns when ctx ends. startLinked skips
// the first register phase (Enroll registered already).
func (l *Link) supervise(ctx context.Context, startLinked bool, bo *Backoff, firstWait time.Duration) {
	if bo == nil {
		bo = NewBackoff(l.rnd)
	}
	linked := startLinked
	if !linked && firstWait > 0 && !l.wait(ctx, firstWait) {
		return
	}
	for ctx.Err() == nil {
		if !linked {
			if !l.connectUntilLinked(ctx, bo) {
				return
			}
		}
		linked = false
		req, ok := l.runLinked(ctx)
		if !ok {
			return
		}
		if req.revoked {
			l.markRevoked(req.reason)
			if !l.probeRevoked(ctx) {
				return
			}
			linked = true
			continue
		}
		l.log.Info("registering again", "reason", req.reason)
		l.setState(StateRegistering, req.reason)
	}
}

// connectUntilLinked runs register and handshake until both succeed. It returns false when ctx ends.
func (l *Link) connectUntilLinked(ctx context.Context, bo *Backoff) bool {
	for {
		if !l.active() {
			if !l.wait(ctx, l.heartbeatInterval()) {
				return false
			}
			continue
		}
		delay, done, revoked := l.connectOnce(ctx, bo)
		switch {
		case done:
			bo.Reset()
			return true
		case revoked:
			l.markRevoked("the cloud rejected the credential")
			if !l.probeRevoked(ctx) {
				return false
			}
			return true
		}
		if !l.wait(ctx, delay) {
			return false
		}
	}
}

// connectOnce is one register plus handshake. It returns the wait before the next attempt, whether the
// link is now linked, and whether the credential was rejected.
func (l *Link) connectOnce(ctx context.Context, bo *Backoff) (delay time.Duration, linked, revoked bool) {
	res := l.registerOnce(ctx)
	switch res.outcome {
	case regRevoked:
		return 0, false, true
	case regRejected:
		l.raise(doctor.CLD054, map[string]any{"status": res.status})
		l.setState(StateRegistering, errText(res.err))
		return hourly, false, false
	case regTransient:
		l.setState(StateRegistering, errText(res.err))
		return bo.Fail(res.retryAfter), false, false
	}
	if err := l.handshake(ctx); err != nil {
		if l.noteAuthRevoked(err) {
			return 0, false, true
		}
		l.setState(StateRegistering, errText(err))
		var ra time.Duration
		if ae := asAPIError(err); ae != nil {
			ra = ae.RetryAfter
		}
		return bo.Fail(ra), false, false
	}
	l.becomeLinked()
	return 0, true, false
}

func (l *Link) markRevoked(reason string) {
	l.raise(doctor.CLD051, nil)
	l.setState(StateRevoked, reason)
}

// probeRevoked registers once an hour while the credential is rejected; it returns true as soon as a
// register is accepted (the state is then linked) and false when ctx ends. A new enroll restarts the
// supervisor with the new credential, so the probe never sees it.
func (l *Link) probeRevoked(ctx context.Context) bool {
	for {
		if !l.wait(ctx, hourly) {
			return false
		}
		if !l.active() {
			continue
		}
		res := l.registerOnce(ctx)
		if res.outcome != regOK {
			continue
		}
		if err := l.handshake(ctx); err != nil {
			continue
		}
		l.clear(doctor.CLD051)
		l.becomeLinked()
		return true
	}
}

// runLinked runs the loops of the linked phase until something asks to register again or ctx ends.
func (l *Link) runLinked(ctx context.Context) (relinkReq, bool) {
	lctx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	for _, loop := range []func(context.Context){l.heartbeatLoop, l.stateLoop, l.keyLoop, l.tailnetLoop, l.eventLoop, l.configLoop} {
		wg.Add(1)
		go func(f func(context.Context)) {
			defer wg.Done()
			f(lctx)
		}(loop)
	}
	if l.d.Claim != nil {
		l.d.Claim.Start(lctx)
	}
	// A request that arrived before the loops started (a 401 of the handshake) counts too.
	var req relinkReq
	got := false
	if r, ok := l.takeRelink(); ok {
		req, got = r, true
	}
	for !got {
		select {
		case <-ctx.Done():
		case <-l.sigCh:
			req, got = l.takeRelink()
			if !got {
				continue
			}
		}
		break
	}
	cancel()
	wg.Wait()
	if l.d.Claim != nil {
		stopCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
		_ = l.d.Claim.Stop(stopCtx)
		stop()
	}
	if !got {
		return relinkReq{}, false
	}
	return req, true
}

// configLoop registers again when cloud.allow changes (agent.md section 4.8.2: eligibleTypes is
// recomputed and re-registered on a hot reload).
func (l *Link) configLoop(ctx context.Context) {
	sub, ok := l.d.Config.(configSubscriber)
	if !ok {
		return
	}
	ch, cancel := sub.Subscribe()
	defer cancel()
	for {
		select {
		case <-ctx.Done():
			return
		case c, open := <-ch:
			if !open {
				return
			}
			if slices.Contains(c.Changed, "cloud.allow") || slices.Contains(c.Changed, "cloud.concurrency") {
				l.signalRelink(relinkReq{reason: "cloud.allow changed"})
			}
		}
	}
}

// retryAfterOf is the Retry-After of an API error, 0 for anything else.
func retryAfterOf(err error) time.Duration {
	if ae := asAPIError(err); ae != nil {
		return ae.RetryAfter
	}
	return 0
}
