// Package fake holds the test doubles of internal/config: a manual clock for the reload poll and a
// filesystem that fails on demand to simulate a crash in the middle of an atomic write.
package fake

import (
	"sync"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/config"
)

// Clock is a manual config.Clock: time moves only when Advance is called.
type Clock struct {
	mu      sync.Mutex
	now     time.Time
	tickers []*ticker
}

// NewClock returns a Clock set to start.
func NewClock(start time.Time) *Clock { return &Clock{now: start} }

// Now implements config.Clock.
func (c *Clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// NewTicker implements config.Clock.
func (c *Clock) NewTicker(d time.Duration) config.Ticker {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &ticker{clock: c, period: d, next: c.now.Add(d), ch: make(chan time.Time, 1)}
	c.tickers = append(c.tickers, t)
	return t
}

// Advance moves time forward and fires every ticker that came due. A ticker whose channel is
// still full drops the tick, like a real one.
func (c *Clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	for _, t := range c.tickers {
		for !t.stopped && !t.next.After(c.now) {
			select {
			case t.ch <- t.next:
			default:
			}
			t.next = t.next.Add(t.period)
		}
	}
}

// Tickers returns how many tickers are running.
func (c *Clock) Tickers() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, t := range c.tickers {
		if !t.stopped {
			n++
		}
	}
	return n
}

type ticker struct {
	clock   *Clock
	period  time.Duration
	next    time.Time
	ch      chan time.Time
	stopped bool
}

func (t *ticker) C() <-chan time.Time { return t.ch }

func (t *ticker) Stop() {
	t.clock.mu.Lock()
	defer t.clock.mu.Unlock()
	t.stopped = true
}
