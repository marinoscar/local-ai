package app

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/config"
)

// errDeadline is the cause of a context that withDeadline cancelled because its time ran out.
var errDeadline = errors.New("app: deadline exceeded")

// withDeadline returns a context that ends when d has passed on clock, or when parent ends. It is
// context.WithTimeout over an injectable clock: every step timeout and shutdown budget of this
// package runs on config.Clock, so a test with a manual clock fires one by calling Advance and never
// sleeps. After the deadline context.Cause(ctx) is errDeadline; ctx.Err() is context.Canceled, as
// for any cancelled context.
//
// The returned cancel must be called; it stops the goroutine and the ticker.
func withDeadline(parent context.Context, clock config.Clock, d time.Duration) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancelCause(parent)
	if d <= 0 {
		return ctx, func() { cancel(context.Canceled) }
	}
	tick := clock.NewTicker(d)
	stop := make(chan struct{})
	go func() {
		defer tick.Stop()
		select {
		case <-tick.C():
			cancel(errDeadline)
		case <-stop:
		case <-ctx.Done():
		}
	}()
	var once sync.Once
	return ctx, func() {
		once.Do(func() { close(stop) })
		cancel(context.Canceled)
	}
}

// timedOut reports whether ctx ended because its withDeadline time ran out.
func timedOut(ctx context.Context) bool { return errors.Is(context.Cause(ctx), errDeadline) }
