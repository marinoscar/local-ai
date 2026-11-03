package cloud

import (
	"math/rand/v2"
	"time"
)

// The backoff of agent.md section 4.8.7: per loop, full-jitter exponential,
// delay = rand(5 s, min(300 s, 5 s x 2^failures)), reset on the first success.
const (
	// BackoffMin is the shortest wait after a failure.
	BackoffMin = 5 * time.Second
	// BackoffMax is the longest upper bound of the jitter window.
	BackoffMax = 300 * time.Second
)

// RandDuration returns a duration in [lo, hi]; the production one is random, tests pass fixed ones.
type RandDuration func(lo, hi time.Duration) time.Duration

func randomDuration(lo, hi time.Duration) time.Duration {
	if hi <= lo {
		return lo
	}
	return lo + time.Duration(rand.Int64N(int64(hi-lo)+1))
}

// BackoffBounds returns the jitter window after the given number of consecutive failures (at least 1).
func BackoffBounds(failures int) (lo, hi time.Duration) {
	if failures < 1 {
		failures = 1
	}
	hi = BackoffMax
	// 5 s x 2^6 = 320 s is already over the cap, so stop shifting there (no overflow).
	if failures <= 6 {
		if w := BackoffMin * time.Duration(1<<uint(failures)); w < hi {
			hi = w
		}
	}
	return BackoffMin, hi
}

// Backoff is the failure counter of one loop. It is not safe for concurrent use: each loop owns one.
type Backoff struct {
	rnd      RandDuration
	failures int
}

// NewBackoff returns a Backoff; a nil rnd is the random jitter.
func NewBackoff(rnd RandDuration) *Backoff {
	if rnd == nil {
		rnd = randomDuration
	}
	return &Backoff{rnd: rnd}
}

// Fail counts a failure and returns how long to wait. A server Retry-After (429) wins when it is
// longer than the jittered delay.
func (b *Backoff) Fail(retryAfter time.Duration) time.Duration {
	b.failures++
	lo, hi := BackoffBounds(b.failures)
	d := b.rnd(lo, hi)
	if retryAfter > d {
		d = retryAfter
	}
	return d
}

// Reset forgets the failures.
func (b *Backoff) Reset() { b.failures = 0 }

// Failures is the number of consecutive failures so far.
func (b *Backoff) Failures() int { return b.failures }
