package router

import (
	"context"
	"errors"
	"regexp"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

var opIDRe = regexp.MustCompile(`^op_[a-z2-7]{12}$`)

func TestNewOpIDHasTheDocumentedShape(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		id := NewOpID()
		if !opIDRe.MatchString(id) {
			t.Fatalf("%q is not op_ plus 12 base32 characters", id)
		}
		seen[id] = true
	}
	if len(seen) < 199 {
		t.Errorf("op ids repeat: %d distinct of 200", len(seen))
	}
}

func TestOpLockTryAcquireNamesTheHolder(t *testing.T) {
	l := NewOpLock(nil, nil)
	h, err := l.TryAcquire(HolderLoad)
	if err != nil {
		t.Fatal(err)
	}
	if !opIDRe.MatchString(h.OpID) {
		t.Errorf("holder op id %q", h.OpID)
	}
	if cur, ok := l.Holder(); !ok || cur.OpID != h.OpID || cur.Kind != HolderLoad {
		t.Errorf("holder: %+v %v", cur, ok)
	}
	_, err = l.TryAcquire(HolderUnload)
	var be *BusyError
	if !errors.As(err, &be) || be.OpID != h.OpID || be.Kind != HolderLoad {
		t.Fatalf("got %v", err)
	}
	h.Release()
	h.Release() // a second release is harmless and must not free a lock somebody else took
	h2, err := l.TryAcquire(HolderUnload)
	if err != nil {
		t.Fatal(err)
	}
	h.Release()
	if _, err := l.TryAcquire(HolderLoad); err == nil {
		t.Error("a stale Release freed the lock of another holder")
	}
	h2.Release()
	if _, ok := l.Holder(); ok {
		t.Error("holder after release")
	}
}

func TestOpLockAcquireWait(t *testing.T) {
	l := NewOpLock(nil, nil)
	h, _ := l.TryAcquire(HolderLoad)

	// No wait: busy at once.
	if _, err := l.AcquireWait(bg, HolderLoad, 0); !isBusy(err) {
		t.Errorf("zero wait: %v", err)
	}
	// A wait that runs out is busy, naming the holder.
	start := time.Now()
	_, err := l.AcquireWait(bg, HolderLoad, 40*time.Millisecond)
	var be *BusyError
	if !errors.As(err, &be) || be.OpID != h.OpID {
		t.Errorf("timeout: %v", err)
	}
	if time.Since(start) < 30*time.Millisecond {
		t.Error("did not wait")
	}
	// A cancelled context is its own error, not busy.
	ctx, cancel := context.WithCancel(bg)
	cancel()
	if _, err := l.AcquireWait(ctx, HolderLoad, time.Second); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled: %v", err)
	}
	if _, err := l.AcquireWait(ctx, HolderLoad, 0); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled, no wait: %v", err)
	}
	// A wait that is served when the holder lets go.
	go func() {
		time.Sleep(20 * time.Millisecond)
		h.Release()
	}()
	h2, err := l.AcquireWait(bg, HolderLoad, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	h2.Release()
}

func isBusy(err error) bool {
	var be *BusyError
	return errors.As(err, &be)
}

func TestOpLockAsOpsLocker(t *testing.T) {
	l := NewOpLock(nil, nil)
	release, err := l.Acquire(bg)
	if err != nil {
		t.Fatal(err)
	}
	if cur, ok := l.Holder(); !ok || cur.Kind != HolderReload {
		t.Errorf("holder: %+v %v", cur, ok)
	}
	// Acquire waits until its context ends, and says who was in the way.
	ctx, cancel := context.WithTimeout(bg, 30*time.Millisecond)
	defer cancel()
	_, err = l.Acquire(ctx)
	if !errors.Is(err, context.DeadlineExceeded) || !isBusy(err) {
		t.Errorf("got %v", err)
	}
	release()
	release()
	r2, err := l.Acquire(bg)
	if err != nil {
		t.Fatal(err)
	}
	r2()
}

func TestOpLockIsExclusiveUnderContention(t *testing.T) {
	l := NewOpLock(nil, nil)
	var inside, maxInside, done atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			h, err := l.AcquireWait(bg, HolderOther, 10*time.Second)
			if err != nil {
				t.Error(err)
				return
			}
			n := inside.Add(1)
			for {
				m := maxInside.Load()
				if n <= m || maxInside.CompareAndSwap(m, n) {
					break
				}
			}
			time.Sleep(time.Millisecond)
			inside.Add(-1)
			done.Add(1)
			h.Release()
		}()
	}
	wg.Wait()
	if maxInside.Load() != 1 || done.Load() != 16 {
		t.Errorf("max concurrent holders %d, finished %d", maxInside.Load(), done.Load())
	}
}

func TestOpLockUsesTheInjectedIDsAndClock(t *testing.T) {
	now := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	l := NewOpLock(func() string { return "op_fixedfixed22" }, func() time.Time { return now })
	h, _ := l.TryAcquire(HolderLoad)
	defer h.Release()
	if cur, _ := l.Holder(); cur.OpID != "op_fixedfixed22" || !cur.Since.Equal(now) {
		t.Errorf("holder: %+v", cur)
	}
}
