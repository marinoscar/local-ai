package router

import (
	"context"
	"crypto/rand"
	"errors"
	"sync"
	"time"
)

// DefaultLockWait is how long a caller that may wait (a cloud job, the mode engine's
// survival_models step) waits for modelOpsMu before it gives up with LAI-AGT-022 (agent.md
// section 4.9).
const DefaultLockWait = 60 * time.Second

// Kinds of holder, for the log and for BusyError.Kind.
const (
	HolderLoad   = "load"
	HolderUnload = "unload"
	HolderReload = "reload"
	HolderOther  = "other"
)

// BusyError says that another model operation holds modelOpsMu. OpID is that operation's id (the
// 409 LAI-AGT-022 answer carries it as details.op_id); Kind says what it is doing.
type BusyError struct {
	OpID string
	Kind string
}

// Error implements error.
func (e *BusyError) Error() string {
	if e.OpID == "" {
		return "router: another model operation is running"
	}
	return "router: another model operation is running (" + e.OpID + ")"
}

// Holder is the operation that holds the lock.
type Holder struct {
	OpID  string
	Kind  string
	Since time.Time
}

// OpLock is modelOpsMu (agent.md section 4.9): the one lock that serialises router load, unload,
// default, key rotation, llama restart and the reload of models.ini. It is not a sync.Mutex because
// a caller must be able to
//
//   - try it without blocking and learn who holds it (TryAcquire: busy answers 409 with the op_id);
//   - wait for it with a deadline and a context (AcquireWait: cloud jobs and survival_models wait up
//     to 60 s);
//   - hand it to the generator through the OpsLocker interface (Acquire).
//
// Every holder gets an op id ("op_" plus 12 base32 characters) when it takes the lock, so a
// refused caller can always name the operation that blocks it. No lock is ever held across an HTTP
// call to the cloud. The zero value is not usable; create it with NewOpLock.
type OpLock struct {
	sem   chan struct{}
	newID func() string
	now   func() time.Time

	mu     sync.Mutex
	holder *Holder
}

var _ OpsLocker = (*OpLock)(nil)

// NewOpLock returns an unlocked OpLock. newID makes the op ids (nil: random) and now stamps the
// holder (nil: the wall clock); tests pass fixed ones.
func NewOpLock(newID func() string, now func() time.Time) *OpLock {
	if newID == nil {
		newID = NewOpID
	}
	if now == nil {
		now = time.Now
	}
	return &OpLock{sem: make(chan struct{}, 1), newID: newID, now: now}
}

// opAlphabet is the base32 alphabet of ids (agent.md section 2): a-z and 2-7.
const opAlphabet = "abcdefghijklmnopqrstuvwxyz234567"

// NewOpID returns "op_" plus 12 random base32 characters (agent.md section 2).
func NewOpID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("router: no randomness: " + err.Error())
	}
	out := make([]byte, 0, 15)
	out = append(out, "op_"...)
	for _, x := range b {
		out = append(out, opAlphabet[int(x)&31])
	}
	return string(out)
}

// Hold is a held lock. Release it exactly once (more calls do nothing).
type Hold struct {
	// OpID is the id of the operation the lock was taken for.
	OpID    string
	release func()
}

// Release gives the lock back. It is safe to call more than once.
func (h Hold) Release() {
	if h.release != nil {
		h.release()
	}
}

func (l *OpLock) take(kind string) Hold {
	id := l.newID()
	l.mu.Lock()
	l.holder = &Holder{OpID: id, Kind: kind, Since: l.now()}
	l.mu.Unlock()
	var once sync.Once
	return Hold{OpID: id, release: func() {
		once.Do(func() {
			l.mu.Lock()
			l.holder = nil
			l.mu.Unlock()
			<-l.sem
		})
	}}
}

// busy describes the current holder. The holder may have just released: the answer then names an
// operation that is already over, which is harmless (the caller retries).
func (l *OpLock) busy() *BusyError {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.holder == nil {
		return &BusyError{}
	}
	return &BusyError{OpID: l.holder.OpID, Kind: l.holder.Kind}
}

// TryAcquire takes the lock without waiting. Busy returns a *BusyError naming the holder.
func (l *OpLock) TryAcquire(kind string) (Hold, error) {
	select {
	case l.sem <- struct{}{}:
		return l.take(kind), nil
	default:
		return Hold{}, l.busy()
	}
}

// AcquireWait takes the lock, waiting at most wait (wait <= 0 means do not wait: TryAcquire). A
// wait that runs out is a *BusyError; a cancelled context is the context's error.
func (l *OpLock) AcquireWait(ctx context.Context, kind string, wait time.Duration) (Hold, error) {
	if wait <= 0 {
		if err := ctx.Err(); err != nil {
			return Hold{}, err
		}
		return l.TryAcquire(kind)
	}
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case l.sem <- struct{}{}:
		return l.take(kind), nil
	case <-ctx.Done():
		return Hold{}, ctx.Err()
	case <-t.C:
		return Hold{}, l.busy()
	}
}

// Acquire implements OpsLocker: it waits for the lock until ctx ends, as the generator needs when
// a reload may unload running models. The holder is of kind HolderReload.
func (l *OpLock) Acquire(ctx context.Context) (release func(), err error) {
	select {
	case l.sem <- struct{}{}:
		return l.take(HolderReload).release, nil
	case <-ctx.Done():
		return nil, errors.Join(ctx.Err(), l.busy())
	}
}

// Holder returns the operation that holds the lock, if any.
func (l *OpLock) Holder() (Holder, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.holder == nil {
		return Holder{}, false
	}
	return *l.holder, true
}
