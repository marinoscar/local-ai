package auth

import (
	"testing"
	"time"
)

func TestFailureWindowSlides(t *testing.T) {
	w := failureWindow{limit: 5, span: 5 * time.Minute}
	for i := 0; i < 4; i++ {
		w.add(t0.Add(time.Duration(i) * time.Minute))
	}
	if _, blocked := w.blocked(t0.Add(4 * time.Minute)); blocked {
		t.Fatal("four failures must not block")
	}
	if got := w.remaining(t0.Add(4 * time.Minute)); got != 1 {
		t.Errorf("remaining = %d, want 1", got)
	}
	w.add(t0.Add(4 * time.Minute))
	wait, blocked := w.blocked(t0.Add(4 * time.Minute))
	if !blocked || wait != time.Minute {
		t.Fatalf("blocked = %v, wait %v; the oldest failure (t0) frees a slot at t0+5m", blocked, wait)
	}
	if _, blocked := w.blocked(t0.Add(5*time.Minute - time.Second)); !blocked {
		t.Error("still blocked just before the oldest ages out")
	}
	if _, blocked := w.blocked(t0.Add(5 * time.Minute)); blocked {
		t.Error("free once the oldest failure is 5 minutes old")
	}
}

func TestRetrySecondsRoundsUp(t *testing.T) {
	for in, want := range map[time.Duration]int{0: 1, time.Millisecond: 1, time.Second: 1, time.Second + time.Millisecond: 2, 5 * time.Minute: 300, time.Hour: 3600} {
		if got := retrySeconds(in); got != want {
			t.Errorf("retrySeconds(%v) = %d, want %d", in, got, want)
		}
	}
}

func TestCodeLimiterIsGlobalAndRefusesEverythingWhileFull(t *testing.T) {
	l := newCodeLimiter()
	now := t0
	for i := 0; i < 5; i++ {
		if _, blocked := l.blocked(now); blocked {
			t.Fatalf("blocked after %d failures", i)
		}
		l.fail(now)
		now = now.Add(10 * time.Second)
	}
	wait, blocked := l.blocked(now)
	if !blocked || wait != 5*time.Minute-50*time.Second {
		t.Fatalf("blocked = %v, wait = %v", blocked, wait)
	}
	if _, blocked := l.blocked(t0.Add(5 * time.Minute)); blocked {
		t.Error("the first failure aged out at +5m")
	}
}

func TestPINLimiterWindowThenLockout(t *testing.T) {
	l := newPINLimiter()
	now := t0
	for i := 0; i < 5; i++ {
		if _, _, blocked := l.check(now); blocked {
			t.Fatalf("blocked at failure %d", i)
		}
		rem, locked := l.fail(now)
		if locked || rem != 4-i {
			t.Fatalf("fail #%d: remaining %d locked %v", i+1, rem, locked)
		}
	}
	if wait, locked, blocked := l.check(now); !blocked || locked || wait != 5*time.Minute {
		t.Fatalf("check = %v %v %v; want a 5-minute window block", wait, locked, blocked)
	}
	// 15 more failures, 5 per window, reach 20 consecutive.
	var lockedNow bool
	for round := 0; round < 3; round++ {
		now = now.Add(5*time.Minute + time.Second)
		for i := 0; i < 5; i++ {
			if _, _, blocked := l.check(now); blocked {
				t.Fatalf("round %d blocked at %d", round, i)
			}
			_, lockedNow = l.fail(now)
		}
	}
	if !lockedNow {
		t.Fatal("the 20th consecutive failure starts the lockout")
	}
	if wait, locked, blocked := l.check(now); !blocked || !locked || wait != time.Hour {
		t.Fatalf("check = %v %v %v; want a 1 hour lockout", wait, locked, blocked)
	}
	// Lockout ends after an hour, with a fresh start.
	now = now.Add(time.Hour)
	if _, _, blocked := l.check(now); blocked {
		t.Fatal("unlocked after an hour")
	}
	if rem, _ := l.fail(now); rem != 4 {
		t.Errorf("after the lockout the window starts empty: remaining %d", rem)
	}
}

func TestPINLimiterSuccessResetsTheConsecutiveCounter(t *testing.T) {
	l := newPINLimiter()
	now := t0
	failN := func(n int) (locked bool) {
		for i := 0; i < n; i++ {
			now = now.Add(5*time.Minute + time.Second) // never fills the window
			_, locked = l.fail(now)
		}
		return locked
	}
	if failN(19) {
		t.Fatal("19 failures must not lock")
	}
	l.success()
	if failN(19) {
		t.Fatal("a success in between restarts the count")
	}
	if !failN(1) {
		t.Fatal("the 20th since the success locks")
	}
}
