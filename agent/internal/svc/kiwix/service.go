package kiwix

import (
	"context"
	"log/slog"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/ark"
)

// DefaultInterval is the debounce of section 4.1.2: at most one regeneration per 10 s.
const DefaultInterval = 10 * time.Second

// DefaultSettle is how long a trigger waits before the run, so the drive and sync handlers have
// updated the best-copy index first (they listen to the same events).
const DefaultSettle = 2 * time.Second

// Regenerator is what Service runs; *Library implements it.
type Regenerator interface {
	Regenerate(ctx context.Context) (Result, error)
	RegenerateRoot(ctx context.Context, rootID string) (RootResult, error)
}

// Service coalesces triggers (agent start, sync.item_done, drive.mounted, drive.unmounted) into
// regenerations. A trigger that arrives while a run (or the wait after it) is going is never lost: it
// makes exactly one more run afterwards, so a burst of triggers gives at most two runs.
type Service struct {
	Lib Regenerator
	// Roots lists the mounted roots whose per-root library is refreshed after the merged one.
	Roots func() []ark.Root
	// Enabled, when set, is asked before every run (node.yaml kiwix.enabled).
	Enabled func() bool
	// Interval is the minimum time between two run starts; default DefaultInterval.
	Interval time.Duration
	// Settle is the wait between a trigger and its run; default DefaultSettle.
	Settle time.Duration
	// Sleep waits d or until ctx ends; default a timer.
	Sleep func(ctx context.Context, d time.Duration) error
	Log   *slog.Logger

	kick chan struct{}
}

// NewService returns a Service; the zero value is not usable (the trigger channel).
func NewService(lib Regenerator) *Service {
	return &Service{Lib: lib, kick: make(chan struct{}, 1)}
}

// Trigger asks for a regeneration. It never blocks.
func (s *Service) Trigger() {
	select {
	case s.kick <- struct{}{}:
	default:
	}
}

func (s *Service) sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	if s.Sleep != nil {
		return s.Sleep(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Run is the loop; it returns when ctx ends.
func (s *Service) Run(ctx context.Context) {
	interval, settle := s.Interval, s.Settle
	if interval == 0 {
		interval = DefaultInterval
	}
	if settle == 0 {
		settle = DefaultSettle
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.kick:
		}
		if s.sleep(ctx, settle) != nil {
			return
		}
		select { // triggers that came during the settle belong to this run
		case <-s.kick:
		default:
		}
		s.Once(ctx)
		if s.sleep(ctx, interval) != nil {
			return
		}
	}
}

// Once is one regeneration: the merged library, then the per-root libraries.
func (s *Service) Once(ctx context.Context) {
	if s.Enabled != nil && !s.Enabled() {
		return
	}
	log := s.Log
	if log == nil {
		log = slog.Default()
	}
	log = log.With("component", "kiwix")
	res, err := s.Lib.Regenerate(ctx)
	if err == nil {
		log.Info("library regeneration done", "books", res.Books, "written", res.Written)
	}
	if s.Roots == nil {
		return
	}
	for _, r := range s.Roots() {
		if ctx.Err() != nil {
			return
		}
		_, _ = s.Lib.RegenerateRoot(ctx, r.ID) // failures are logged by the library
	}
}
