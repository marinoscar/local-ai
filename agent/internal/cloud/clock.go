package cloud

import "time"

// Clock is the time seam of the loops: Now for timestamps and NewTimer for every wait, so tests drive
// the cadence with a manual clock (internal/cloud/fake.Clock) and never sleep.
type Clock interface {
	Now() time.Time
	NewTimer(d time.Duration) Timer
}

// Timer is a time.Timer behind an interface.
type Timer interface {
	// C delivers the time when the timer fires.
	C() <-chan time.Time
	// Stop prevents the timer from firing; it reports whether it was still pending.
	Stop() bool
}

// RealClock is the wall clock.
type RealClock struct{}

// Now implements Clock.
func (RealClock) Now() time.Time { return time.Now() }

// NewTimer implements Clock.
func (RealClock) NewTimer(d time.Duration) Timer { return realTimer{time.NewTimer(d)} }

type realTimer struct{ t *time.Timer }

func (r realTimer) C() <-chan time.Time { return r.t.C }
func (r realTimer) Stop() bool          { return r.t.Stop() }
