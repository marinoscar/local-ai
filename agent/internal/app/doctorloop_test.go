package app

import (
	"context"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
)

// The doctor's passes are one serialized loop: one on the clock's period, one at once on a kick, never
// two at the same time (two overlapping passes would raise a false LAI-AGT-099 for a check that is
// merely running).
func TestDoctorLoopRunsOnTheClockAndOnKicks(t *testing.T) {
	clock := cfgfakeClock()
	r := newRig(t, withEnv(func(e *Env) { e.Clock = clock }))
	d := r.start()
	r.waitStarted()

	var runs, overlap atomic.Int32
	var inside atomic.Int32
	if err := d.Components().Checks.Add(doctor.Check{ID: "app.probe", Area: "AGT", Fast: true, Run: func(context.Context) []doctor.Result {
		if inside.Add(1) > 1 {
			overlap.Add(1)
		}
		runs.Add(1)
		inside.Add(-1)
		return nil
	}}); err != nil {
		t.Fatal(err)
	}
	waitFor := func(what string, n int32) {
		t.Helper()
		deadline := time.Now().Add(30 * time.Second)
		for runs.Load() < n {
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %s (runs %d)", what, runs.Load())
			}
			runtime.Gosched()
		}
	}
	clock.Advance(doctor.DefaultInterval)
	waitFor("the periodic pass", 1)
	d.kickDoctor()
	waitFor("the kicked pass", 2)
	d.kickDoctor()
	d.kickDoctor() // coalesced with the pending one
	clock.Advance(doctor.DefaultInterval)
	waitFor("a pass after more kicks", 3)
	if overlap.Load() != 0 {
		t.Errorf("%d passes overlapped", overlap.Load())
	}
	if hasAlert(d, doctor.AGT099) {
		t.Errorf("a false LAI-AGT-099: %+v", d.Components().Doctor.Active())
	}
	// Stop ends the loop.
	l := &doctorLoop{d: d}
	if err := l.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := l.Start(context.Background()); err == nil {
		t.Error("a second Start must fail")
	}
	if err := l.Stop(context.Background()); err != nil {
		t.Error(err)
	}
	if err := l.Stop(context.Background()); err != nil {
		t.Error("a second Stop must be a no-op")
	}
}
