package piper

import (
	"context"
	"errors"
	"sync"
)

// Concurrency of syntheses (companion-services.md section 4.3.2): up to 2 piper processes run at once
// and 4 more requests may wait; one more is refused with 429 LAI-SVC-015.
const (
	DefaultMaxRunning = 2
	DefaultMaxQueued  = 4
)

// ErrBusy: every slot is taken and the queue is full.
var ErrBusy = errors.New("piper: too many syntheses at once")

// Limiter admits at most running syntheses at a time and lets at most queued others wait, in order.
// It is safe for concurrent use.
type Limiter struct {
	slots  chan struct{}
	maxQ   int
	mu     sync.Mutex
	queued int
	// onWait is called, outside the lock, right after a request has joined the queue. Tests use it
	// to know the queue really holds the request; nothing else sets it.
	onWait func()
}

// NewLimiter returns a Limiter; values below 1 and 0 use the defaults (2 and 4).
func NewLimiter(running, queued int) *Limiter {
	if running < 1 {
		running = DefaultMaxRunning
	}
	if queued < 0 {
		queued = DefaultMaxQueued
	}
	return &Limiter{slots: make(chan struct{}, running), maxQ: queued}
}

// Acquire takes a slot, waiting in the queue when none is free. It returns the function that gives
// the slot back (call it exactly once), ErrBusy when the queue is full, or the context's error when
// the request ended while it waited (the client left, or the route deadline passed).
func (l *Limiter) Acquire(ctx context.Context) (release func(), err error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	free := func() { <-l.slots }
	select {
	case l.slots <- struct{}{}:
		return free, nil
	default:
	}
	l.mu.Lock()
	if l.queued >= l.maxQ {
		l.mu.Unlock()
		return nil, ErrBusy
	}
	l.queued++
	l.mu.Unlock()
	if l.onWait != nil {
		l.onWait()
	}
	select {
	case l.slots <- struct{}{}:
		l.leaveQueue()
		return free, nil
	case <-ctx.Done():
		l.leaveQueue()
		return nil, ctx.Err()
	}
}

func (l *Limiter) leaveQueue() {
	l.mu.Lock()
	l.queued--
	l.mu.Unlock()
}

// Stats reports how many syntheses run and how many wait.
func (l *Limiter) Stats() (running, queued int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.slots), l.queued
}
