package whisper

import (
	"context"
	"errors"
	"sync"
)

// Queue limits (companion-services.md section 4.2.5): one request is in flight to whisper and at most
// two wait; one more is refused with 429 LAI-SVC-015.
const (
	MaxInFlight = 1
	MaxWaiting  = 2
)

// ErrBusy: the slot is taken and the waiting line is full.
var ErrBusy = errors.New("whisper: too many speech requests at once")

// Queue admits requests in order. It is safe for concurrent use.
type Queue struct {
	slots   chan struct{}
	maxWait int
	mu      sync.Mutex
	waiting int
	// onWait is called after a request joined the line; tests use it to know the line holds it.
	onWait func()
}

// NewQueue returns a Queue with running slots and maxWait places in line; running below 1 and
// maxWait below 0 use the defaults.
func NewQueue(running, maxWait int) *Queue {
	if running < 1 {
		running = MaxInFlight
	}
	if maxWait < 0 {
		maxWait = MaxWaiting
	}
	return &Queue{slots: make(chan struct{}, running), maxWait: maxWait}
}

// Acquire takes the slot, waiting in line when it is busy. It returns the function that frees the
// slot (call it once), ErrBusy when the line is full, or the context's error if the caller gave up.
func (q *Queue) Acquire(ctx context.Context) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	free := func() { <-q.slots }
	select {
	case q.slots <- struct{}{}:
		return free, nil
	default:
	}
	q.mu.Lock()
	if q.waiting >= q.maxWait {
		q.mu.Unlock()
		return nil, ErrBusy
	}
	q.waiting++
	q.mu.Unlock()
	if q.onWait != nil {
		q.onWait()
	}
	leave := func() { q.mu.Lock(); q.waiting--; q.mu.Unlock() }
	select {
	case q.slots <- struct{}{}:
		leave()
		return free, nil
	case <-ctx.Done():
		leave()
		return nil, ctx.Err()
	}
}
