// Package throttle is the bandwidth limiter of ark sync (ark-sync.md section 4.7): one token
// bucket shared by every in-process download, the parser of the sync.bandwidth_limit setting, and
// a loopback CONNECT proxy that lets subprocess producers (pmtiles, git, apt) draw from the same
// bucket. It has no dependency on the rest of the agent.
package throttle

import (
	"context"
	"sync"
	"time"
)

// DefaultBurst is the bucket's burst: 1 MiB (ark-sync.md section 4.7).
const DefaultBurst = 1 << 20

// Bucket is what a downloader (and the proxy) draws bytes from. Wait blocks until n bytes may be
// used, or until ctx is done (then it returns ctx.Err() and takes nothing). It is safe for
// concurrent use.
type Bucket interface {
	Wait(ctx context.Context, n int) error
}

// Clock is the time seam of the bucket. After must return a channel that fires once d has passed.
type Clock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
}

type realClock struct{}

func (realClock) Now() time.Time                         { return time.Now() }
func (realClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// RealClock is the wall clock.
func RealClock() Clock { return realClock{} }

// Unlimited is the Bucket of a node with no bandwidth limit: Wait never blocks.
type Unlimited struct{}

// Wait implements Bucket.
func (Unlimited) Wait(ctx context.Context, _ int) error { return ctx.Err() }

// Unlimited reports true: StartProxy does not start a proxy for it.
func (Unlimited) Unlimited() bool { return true }

// TokenBucket is the in-house token bucket: tokens are bytes, they refill at rate bytes per second
// up to burst, and a caller that needs more than is available waits for the deficit to refill. The
// reservation is made under the lock, so concurrent callers queue behind each other and together
// never exceed the rate. A rate of 0 means unlimited. The rate can change while it is in use.
type TokenBucket struct {
	clock Clock
	burst float64

	mu     sync.Mutex
	rate   float64 // bytes per second; 0 = unlimited
	tokens float64
	last   time.Time
}

// NewBucket returns a bucket of bytesPerSec (0 = unlimited) with the default burst, full at the
// start. A nil clock means the wall clock.
func NewBucket(bytesPerSec int64, clock Clock) *TokenBucket {
	if clock == nil {
		clock = RealClock()
	}
	return &TokenBucket{clock: clock, burst: DefaultBurst, rate: float64(max(bytesPerSec, 0)), tokens: DefaultBurst, last: clock.Now()}
}

// Rate returns the current limit in bytes per second (0 = unlimited).
func (b *TokenBucket) Rate() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return int64(b.rate)
}

// Unlimited reports whether the bucket currently has no limit.
func (b *TokenBucket) Unlimited() bool { return b.Rate() == 0 }

// SetRate changes the limit (0 = unlimited). Tokens already owed stay owed.
func (b *TokenBucket) SetRate(bytesPerSec int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.refillLocked()
	b.rate = float64(max(bytesPerSec, 0))
}

// refillLocked adds the tokens earned since the last call. The clock is read here, under the lock,
// so a reservation and the sleep it asks for use the same instant.
func (b *TokenBucket) refillLocked() {
	now := b.clock.Now()
	if el := now.Sub(b.last); el > 0 {
		b.tokens = min(b.burst, b.tokens+el.Seconds()*b.rate)
		b.last = now
	}
}

// Wait implements Bucket. A request larger than the burst is taken in burst-sized pieces.
func (b *TokenBucket) Wait(ctx context.Context, n int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	for n > 0 {
		piece := min(n, int(b.burst))
		if err := b.waitPiece(ctx, piece); err != nil {
			return err
		}
		n -= piece
	}
	return nil
}

func (b *TokenBucket) waitPiece(ctx context.Context, n int) error {
	b.mu.Lock()
	if b.rate == 0 {
		b.mu.Unlock()
		return nil
	}
	b.refillLocked()
	b.tokens -= float64(n)
	var timer <-chan time.Time
	if b.tokens < 0 {
		wait := time.Duration(-b.tokens / b.rate * float64(time.Second))
		timer = b.clock.After(wait)
	}
	b.mu.Unlock()
	if timer == nil {
		return nil
	}
	select {
	case <-timer:
		return nil
	case <-ctx.Done():
		b.mu.Lock()
		b.tokens = min(b.burst, b.tokens+float64(n))
		b.mu.Unlock()
		return ctx.Err()
	}
}
