package app

import (
	"context"
	"errors"
	"sync"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
)

// doctorLoop is the doctor's periodic pass as an app Worker: one pass every doctor.DefaultInterval on
// the daemon's clock, and one at once whenever the drill reporter says what it reports changed. It
// replaces doctor.Manager.Start because every pass must be serialized: the check registry marks a check
// as running while it runs and answers LAI-AGT-099 to a second run of the same check, so two passes
// that overlap (the periodic one and a kicked one) would raise a false alert. One goroutine owns every
// Tick here, and the first pass of startup (step 9) runs before it starts.
type doctorLoop struct {
	d *Daemon

	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
}

// Name implements Worker.
func (l *doctorLoop) Name() string { return "doctor" }

// Start implements Worker.
func (l *doctorLoop) Start(ctx context.Context) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.cancel != nil {
		return errors.New("doctor: loop already started")
	}
	loopCtx, cancel := context.WithCancel(ctx)
	l.cancel, l.done = cancel, make(chan struct{})
	tick := l.d.env.Clock.NewTicker(doctor.DefaultInterval)
	go func(done chan struct{}) {
		defer close(done)
		defer tick.Stop()
		for {
			select {
			case <-loopCtx.Done():
				return
			case <-tick.C():
			case <-l.d.doctorKick:
			}
			l.d.c.Doctor.Tick(loopCtx)
		}
	}(l.done)
	return nil
}

// Stop implements Worker.
func (l *doctorLoop) Stop(ctx context.Context) error {
	l.mu.Lock()
	cancel, done := l.cancel, l.done
	l.cancel, l.done = nil, nil
	l.mu.Unlock()
	if cancel == nil {
		return nil
	}
	cancel()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
