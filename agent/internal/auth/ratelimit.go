package auth

import (
	"sync"
	"time"
)

// Rate limits (agent.md section 3.9.2).
const (
	// FailureWindow is the sliding window both limiters count failures in.
	FailureWindow = 5 * time.Minute
	// MaxFailuresInWindow is how many failures a window holds before the limiter answers 429.
	MaxFailuresInWindow = 5
	// LockoutAfter is the number of consecutive PIN failures that starts a lockout.
	LockoutAfter = 20
	// LockoutFor is how long the PIN stays locked.
	LockoutFor = time.Hour
)

// failureWindow is a sliding window of failure times. It is not safe for concurrent use.
type failureWindow struct {
	limit int
	span  time.Duration
	at    []time.Time
}

func (w *failureWindow) prune(now time.Time) {
	cut := now.Add(-w.span)
	i := 0
	for i < len(w.at) && !w.at[i].After(cut) {
		i++
	}
	if i > 0 {
		w.at = append(w.at[:0], w.at[i:]...)
	}
}

// blocked reports whether the window is full and, if so, how long until the oldest failure ages
// out and frees a slot.
func (w *failureWindow) blocked(now time.Time) (time.Duration, bool) {
	w.prune(now)
	if len(w.at) < w.limit {
		return 0, false
	}
	return w.at[0].Add(w.span).Sub(now), true
}

func (w *failureWindow) add(now time.Time) {
	w.prune(now)
	w.at = append(w.at, now)
}

func (w *failureWindow) remaining(now time.Time) int {
	w.prune(now)
	if n := w.limit - len(w.at); n > 0 {
		return n
	}
	return 0
}

func (w *failureWindow) reset() { w.at = nil }

// retrySeconds renders a wait as the whole seconds of a Retry-After header: rounded up, at least 1.
func retrySeconds(d time.Duration) int {
	s := int((d + time.Second - 1) / time.Second)
	if s < 1 {
		s = 1
	}
	return s
}

// codeLimiter is the UI-code failure limiter: 5 failures in 5 minutes, global (not per client).
// While it is full every attempt is refused, a correct code included, so guesses stay bounded.
type codeLimiter struct {
	mu sync.Mutex
	w  failureWindow
}

func newCodeLimiter() *codeLimiter {
	return &codeLimiter{w: failureWindow{limit: MaxFailuresInWindow, span: FailureWindow}}
}

// blocked reports whether attempts are refused now, and for how long.
func (l *codeLimiter) blocked(now time.Time) (time.Duration, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.blocked(now)
}

func (l *codeLimiter) fail(now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.w.add(now)
}

// pinLimiter is the PIN limiter: a sliding window of 5 failures in 5 minutes (global) and a
// consecutive counter that, at 20, locks the PIN for an hour. A success resets the consecutive
// counter (not the window). When a lockout ends the limiter starts afresh. The caller serializes
// check, verify and record (PIN.Verify holds its mutex), so the limit cannot be beaten by parallel
// requests.
type pinLimiter struct {
	w           failureWindow
	consecutive int
	lockedUntil time.Time
}

func newPINLimiter() *pinLimiter {
	return &pinLimiter{w: failureWindow{limit: MaxFailuresInWindow, span: FailureWindow}}
}

// check reports whether a verification may run now: not while locked, not while the window is full.
func (l *pinLimiter) check(now time.Time) (wait time.Duration, locked, blocked bool) {
	if !l.lockedUntil.IsZero() {
		if now.Before(l.lockedUntil) {
			return l.lockedUntil.Sub(now), true, true
		}
		l.lockedUntil = time.Time{}
		l.consecutive = 0
		l.w.reset()
	}
	if d, full := l.w.blocked(now); full {
		return d, false, true
	}
	return 0, false, false
}

// fail records a wrong PIN. It returns how many failures the window still allows, and whether this
// failure started a lockout.
func (l *pinLimiter) fail(now time.Time) (remaining int, lockedNow bool) {
	l.w.add(now)
	l.consecutive++
	if l.consecutive >= LockoutAfter {
		l.lockedUntil = now.Add(LockoutFor)
		return 0, true
	}
	return l.w.remaining(now), false
}

// success records a right PIN: the consecutive counter restarts.
func (l *pinLimiter) success() { l.consecutive = 0 }
