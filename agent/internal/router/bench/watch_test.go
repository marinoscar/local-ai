package bench

import (
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/events"
)

// lateWatch builds a watch whose goroutine is held back until the engine's stop is already
// pending: whatever the stream did before then is, for the goroutine, "already happened" and
// "already stopped" at once. stream runs while the goroutine is held (it plays the stream).
func lateWatch(ch chan events.Event, stream func()) *watch {
	release := make(chan struct{})
	w := startWatchGated(ch, func() { <-release })
	stream()
	stopped := make(chan struct{})
	go func() { w.stop(); close(stopped) }()
	<-w.stopCh // stop is signalled while the goroutine has not looked at the channel yet
	close(release)
	<-stopped
	return w
}

// TestWatchDecidesBeforeTheStop pins issue 253: what the stream did before the engine stopped the
// watch counts however late the goroutine runs. The goroutine's select picks between a closed
// channel and a closed stop signal at random, so each case runs many times.
func TestWatchDecidesBeforeTheStop(t *testing.T) {
	own := map[string]bool{"op_own": true}
	ev := func(typ, op string) events.Event { return events.Event{Type: typ, OpID: op, Node: "node-a"} }
	cases := []struct {
		name      string
		stream    func(ch chan events.Event)
		disturbed bool
	}{
		{"nothing happened", func(ch chan events.Event) {}, false},
		{"own operations only", func(ch chan events.Event) {
			ch <- ev(events.TypeModelLoading, "op_own")
			ch <- ev(events.TypeModelLoaded, "op_own")
		}, false},
		{"the stream broke", func(ch chan events.Event) { close(ch) }, true},
		{"the stream broke after own events", func(ch chan events.Event) {
			ch <- ev(events.TypeModelLoaded, "op_own")
			close(ch)
		}, true},
		{"a foreign operation", func(ch chan events.Event) { ch <- ev(events.TypeModelLoading, "op_other") }, true},
		{"an event without an operation id", func(ch chan events.Event) { ch <- ev(events.TypeModelUnloaded, "") }, true},
		{"resync", func(ch chan events.Event) { ch <- ev(events.TypeResync, "") }, true},
		{"shutdown", func(ch chan events.Event) { ch <- ev(events.TypeShutdown, "") }, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			for i := 0; i < 200; i++ {
				ch := make(chan events.Event, 8)
				w := lateWatch(ch, func() { c.stream(ch) })
				if got := w.disturbed(own); got != c.disturbed {
					t.Fatalf("round %d: disturbed = %v, want %v", i, got, c.disturbed)
				}
			}
		})
	}
}

// A close that follows the engine's own stop (its cancel of the events context) is not a loss.
func TestWatchOwnCancelIsNotALoss(t *testing.T) {
	for i := 0; i < 200; i++ {
		ch := make(chan events.Event, 8)
		w := startWatch(ch)
		ch <- events.Event{Type: events.TypeModelLoaded, OpID: "op_own"}
		w.stop()
		close(ch) // what the cancel does to the stream, after the watch has ended
		if w.disturbed(map[string]bool{"op_own": true}) {
			t.Fatalf("round %d: our own cancel was counted as a loss", i)
		}
	}
}
