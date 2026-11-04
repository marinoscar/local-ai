package whisper

import (
	"context"
	"errors"
	"sync"
)

// Queue of speech requests (companion-services.md section 4.2.5, step 5): one request is in flight to
// whisper-server (it serialises anyway), up to two more wait, a further one is refused with 429
// LAI-SVC-015. A request enters the queue before its upload is read, so at most three recordings are
// ever held in memory.
const (
	DefaultInFlight = 1
	DefaultWaiting  = 2
)

// ErrBusy: the queue is full.
var ErrBusy = errors.New("whisper: too many speech requests at once")

// Queue admits requests and hands out the in-flight slot. It is safe for concurrent use.
type Queue struct {
	mu       sync.Mutex
	admitted int
	max      int // in flight + waiting
	slots    chan struct{}
	// onWait is called, outside the lock, once a request starts waiting for the slot. Tests use it to
	// know the queue holds the request; nothing else sets it.
	onWait func()
}

// NewQueue returns a Queue with inFlight running requests and waiting more behind them (values below 1
// and 0 use the defaults).
func NewQueue(inFlight, waiting int) *Queue {
	if inFlight < 1 {
		inFlight = DefaultInFlight
	}
	if waiting < 0 {
		waiting = DefaultWaiting
	}
	return &Queue{max: inFlight + waiting, slots: make(chan struct{}, inFlight)}
}

// Enter admits a request or refuses it with ErrBusy. leave must be called exactly once.
func (q *Queue) Enter() (leave func(), err error) {
	q.mu.Lock()
	if q.admitted >= q.max {
		q.mu.Unlock()
		return nil, ErrBusy
	}
	q.admitted++
	q.mu.Unlock()
	return func() {
		q.mu.Lock()
		q.admitted--
		q.mu.Unlock()
	}, nil
}

// Slot waits for the in-flight slot of an admitted request. It returns the function that gives the slot
// back, or the context's error when the request ended while it waited.
func (q *Queue) Slot(ctx context.Context) (release func(), err error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	free := func() { <-q.slots }
	select {
	case q.slots <- struct{}{}:
		return free, nil
	default:
	}
	if q.onWait != nil {
		q.onWait()
	}
	select {
	case q.slots <- struct{}{}:
		return free, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Stats reports how many requests are admitted and how many of them hold the in-flight slot.
func (q *Queue) Stats() (admitted, inFlight int) {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.admitted, len(q.slots)
}
