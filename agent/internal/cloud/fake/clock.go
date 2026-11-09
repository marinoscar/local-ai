// Package fake holds the test doubles of internal/cloud: a manual clock and a fake EAB plus Local AI
// cloud server written from the specs (EnterpriseAppBase worker-nodes.md as agent.md section 4.8
// summarizes it, and cloud-app.md section 3.6). The package does not import internal/cloud, so the
// tests of that package can use it; a Clock needs a one-line adapter there because the interface
// method returns cloud.Timer.
package fake

import (
	"context"
	"sort"
	"sync"
	"time"
)

// Clock is a manual clock: time moves only when Advance is called, and a test can wait until the code
// under test has armed a timer, so nothing needs a sleep.
type Clock struct {
	mu      sync.Mutex
	now     time.Time
	timers  []*Timer
	changed chan struct{} // closed and replaced whenever the set of pending timers changes
}

// NewClock returns a Clock set to start.
func NewClock(start time.Time) *Clock {
	return &Clock{now: start, changed: make(chan struct{})}
}

// Now returns the manual time.
func (c *Clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// Timer is a pending one-shot timer of a Clock.
type Timer struct {
	clock   *Clock
	at      time.Time
	ch      chan time.Time
	stopped bool
	fired   bool
}

// NewTimer arms a timer for d from now. It returns *Timer; adapt it to the interface of the code
// under test.
func (c *Clock) NewTimer(d time.Duration) *Timer {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &Timer{clock: c, at: c.now.Add(d), ch: make(chan time.Time, 1)}
	c.timers = append(c.timers, t)
	c.notifyLocked()
	return t
}

// C delivers the time the timer fired at.
func (t *Timer) C() <-chan time.Time { return t.ch }

// Stop cancels the timer and reports whether it was still pending.
func (t *Timer) Stop() bool {
	t.clock.mu.Lock()
	defer t.clock.mu.Unlock()
	pending := !t.stopped && !t.fired
	t.stopped = true
	t.clock.notifyLocked()
	return pending
}

func (c *Clock) notifyLocked() {
	close(c.changed)
	c.changed = make(chan struct{})
}

// Advance moves time forward and fires every timer that came due, in order.
func (c *Clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	sort.SliceStable(c.timers, func(i, j int) bool { return c.timers[i].at.Before(c.timers[j].at) })
	for _, t := range c.timers {
		if !t.stopped && !t.fired && !t.at.After(c.now) {
			t.fired = true
			t.ch <- t.at
		}
	}
	c.notifyLocked()
}

// Pending is the number of timers armed and neither fired nor stopped.
func (c *Clock) Pending() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.pendingLocked()
}

func (c *Clock) pendingLocked() int {
	n := 0
	for _, t := range c.timers {
		if !t.stopped && !t.fired {
			n++
		}
	}
	return n
}

// NextDelay is the time until the earliest pending timer, false when none is pending.
func (c *Clock) NextDelay() (time.Duration, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var best time.Time
	found := false
	for _, t := range c.timers {
		if !t.stopped && !t.fired && (!found || t.at.Before(best)) {
			best, found = t.at, true
		}
	}
	if !found {
		return 0, false
	}
	return best.Sub(c.now), true
}

// WaitDue blocks until the earliest pending timer is due within d, or ctx ends. A test uses it to
// wait for the loop it wants to step to arm its timer ("the next wake-up is at most 15 s away")
// without caring about the other loops' longer timers.
func (c *Clock) WaitDue(ctx context.Context, d time.Duration) error {
	for {
		c.mu.Lock()
		ok := false
		for _, t := range c.timers {
			if !t.stopped && !t.fired && t.at.Sub(c.now) <= d {
				ok = true
				break
			}
		}
		ch := c.changed
		c.mu.Unlock()
		if ok {
			return nil
		}
		select {
		case <-ch:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// WaitPending blocks until at least n timers are pending, or ctx ends (the error is ctx's). Tests pass
// a context with a generous deadline that only ever fires when the code under test is broken.
func (c *Clock) WaitPending(ctx context.Context, n int) error {
	for {
		c.mu.Lock()
		ok := c.pendingLocked() >= n
		ch := c.changed
		c.mu.Unlock()
		if ok {
			return nil
		}
		select {
		case <-ch:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// WaitArmed blocks until a timer that fires exactly d from now is pending, or ctx ends. A test uses it to be
// sure a loop has armed its own timer (and not another loop's) before it moves the clock.
func (c *Clock) WaitArmed(ctx context.Context, d time.Duration) error {
	for {
		c.mu.Lock()
		ok := false
		for _, t := range c.timers {
			if !t.stopped && !t.fired && t.at.Sub(c.now) == d {
				ok = true
				break
			}
		}
		ch := c.changed
		c.mu.Unlock()
		if ok {
			return nil
		}
		select {
		case <-ch:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}
