package app

import (
	"context"
	"errors"
	"runtime"
	"testing"
	"time"

	cfgfake "github.com/marinoscar/local-ai/agent/internal/config/fake"
)

var t0 = time.Date(2026, 11, 3, 8, 0, 0, 0, time.UTC)

func TestWatchdogBeat(t *testing.T) {
	tests := []struct {
		name      string
		probes    []WatchProbe
		advance   bool // advance the manual clock past ProbeTimeout once the probe is running
		wantPing  bool
		wantError string
	}{
		{name: "no probes", wantPing: true},
		{name: "all pass", probes: []WatchProbe{
			{Name: "http", Run: func(context.Context) error { return nil }},
			{Name: "state", Run: func(context.Context) error { return nil }},
		}, wantPing: true},
		{name: "the http probe fails", probes: []WatchProbe{
			{Name: "http", Run: func(context.Context) error { return errors.New("connection refused") }},
		}},
		{name: "the second probe fails, the first passed", probes: []WatchProbe{
			{Name: "http", Run: func(context.Context) error { return nil }},
			{Name: "state", Run: func(context.Context) error { return errors.New("writer stuck") }},
		}},
		{name: "a probe that never answers is a hung loop", advance: true, probes: []WatchProbe{
			{Name: "state", Run: func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }},
		}},
		{name: "a probe that ignores its context cannot hold the watchdog", advance: true, probes: []WatchProbe{
			{Name: "http", Run: func(context.Context) error { select {} }},
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			l := newNotifyListener(t, false)
			clock := cfgfake.NewClock(t0)
			w := NewWatchdog(NewNotifier(l.name), clock, 0, nil, tc.probes...)
			result := make(chan bool, 1)
			go func() { result <- w.Beat(context.Background()) }()
			if tc.advance {
				// The probe is waiting on its deadline ticker: wait for the client side (the probe's
				// ticker exists) and then move the clock, never sleep.
				for clock.Tickers() == 0 {
					runtime.Gosched()
				}
				clock.Advance(ProbeTimeout)
			}
			if got := <-result; got != tc.wantPing {
				t.Fatalf("Beat() = %v, want %v", got, tc.wantPing)
			}
			pings, withheld := w.Counts()
			if tc.wantPing {
				if got := l.next(t); got != "WATCHDOG=1" {
					t.Errorf("message = %q", got)
				}
				if pings != 1 || withheld != 0 {
					t.Errorf("counts = %d, %d", pings, withheld)
				}
				return
			}
			if pings != 0 || withheld != 1 {
				t.Errorf("counts = %d pings, %d withheld; want 0, 1", pings, withheld)
			}
			select {
			case m := <-l.msgs:
				t.Errorf("a ping was sent although the loop is unhealthy: %q", m)
			default:
			}
		})
	}
}

// The loop pings once per interval of the injected clock and stops when stopped.
func TestWatchdogLoopCadence(t *testing.T) {
	l := newNotifyListener(t, false)
	clock := cfgfakeClock()
	w := NewWatchdog(NewNotifier(l.name), clock, DefaultWatchdogEvery, nil)
	if err := w.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := w.Start(context.Background()); err == nil {
		t.Error("a second Start must fail")
	}
	for i := 1; i <= 3; i++ {
		clock.Advance(DefaultWatchdogEvery)
		if got := l.next(t); got != "WATCHDOG=1" {
			t.Fatalf("ping %d = %q", i, got)
		}
	}
	if err := w.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if clock.Tickers() != 0 {
		t.Errorf("%d tickers still running after Stop", clock.Tickers())
	}
	if pings, _ := w.Counts(); pings != 3 {
		t.Errorf("pings = %d, want 3", pings)
	}
	if err := w.Stop(context.Background()); err != nil {
		t.Errorf("a second Stop must be a no-op: %v", err)
	}
}

func cfgfakeClock() *cfgfake.Clock { return cfgfake.NewClock(t0) }

func TestWithDeadline(t *testing.T) {
	clock := cfgfakeClock()
	ctx, cancel := withDeadline(context.Background(), clock, 5*time.Second)
	defer cancel()
	select {
	case <-ctx.Done():
		t.Fatal("ended before the clock moved")
	default:
	}
	clock.Advance(4 * time.Second)
	select {
	case <-ctx.Done():
		t.Fatal("ended early")
	default:
	}
	clock.Advance(time.Second)
	<-ctx.Done()
	if !timedOut(ctx) {
		t.Errorf("cause = %v, want the deadline", context.Cause(ctx))
	}

	// A parent that ends first is not a timeout, and cancel stops the ticker.
	parent, stop := context.WithCancel(context.Background())
	c2, cancel2 := withDeadline(parent, clock, time.Hour)
	stop()
	<-c2.Done()
	if timedOut(c2) {
		t.Error("a cancelled parent is not a timeout")
	}
	cancel2()
	cancel2() // idempotent
}
