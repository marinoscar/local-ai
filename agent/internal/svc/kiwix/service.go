package kiwix

import (
	"context"
	"log/slog"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/ark"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/services"
)

// DefaultInterval is the debounce of section 4.1.2: at most one regeneration per 10 s.
const DefaultInterval = 10 * time.Second

// DefaultSettle is how long a trigger waits before the run, so the drive and sync handlers have
// updated the best-copy index first (they listen to the same events).
const DefaultSettle = 2 * time.Second

// DefaultOpenFileWait is how long after a drive is unmounted the service waits before it looks for
// files kiwix-serve still holds open under the removed mount (companion-services.md section 4.1.2
// step 7).
const DefaultOpenFileWait = 10 * time.Second

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
	// Units, when set, lets the service stop localai-kiwix when the library becomes empty, start it
	// when books come back, and restart it when it holds files of a removed mount (steps 6 and 7).
	Units Units
	// FD lists the open files of kiwix-serve for the step 7 check; default ProcFD.
	FD FDLister
	// OpenFileWait is the wait before that check; default DefaultOpenFileWait.
	OpenFileWait time.Duration

	kick chan struct{}
	rem  removals

	known, empty bool // the emptiness seen by the last good run
}

// NewService returns a Service; the zero value is not usable (the trigger channel).
func NewService(lib Regenerator) *Service {
	return &Service{Lib: lib, kick: make(chan struct{}, 1)}
}

// Removed tells the service a root was unmounted (the drive.unmounted event): the next run waits and
// checks that kiwix-serve let go of its files. The root's path is the one seen by an earlier run.
func (s *Service) Removed(rootID string) { s.rem.removed(rootID) }

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
	if s.Roots != nil {
		for _, r := range s.Roots() {
			s.rem.remember(r.ID, r.Path)
		}
	}
	res, err := s.Lib.Regenerate(ctx)
	if err == nil {
		log.Info("library regeneration done", "books", res.Books, "written", res.Written)
		s.transition(ctx, log, res)
	}
	if s.Roots != nil {
		for _, r := range s.Roots() {
			if ctx.Err() != nil {
				return
			}
			_, _ = s.Lib.RegenerateRoot(ctx, r.ID) // failures are logged by the library
		}
	}
	s.checkOpenFiles(ctx, log)
}

// transition applies step 6: an empty library stops the unit (the state is missing_content and
// LAI-SVC-001 is raised by the library and the health probe); books after an empty library, or on the
// first run after the agent started, start a unit that is stopped.
func (s *Service) transition(ctx context.Context, log *slog.Logger, res Result) {
	prevKnown, prevEmpty := s.known, s.empty
	s.known, s.empty = true, res.Empty
	if s.Units == nil {
		return
	}
	st, err := s.Units.State(ctx, services.UnitKiwix)
	if err != nil {
		log.Warn("the library unit state could not be read", "error", err.Error())
		return
	}
	switch {
	case res.Empty:
		if st.ActiveState == services.ActiveActive || st.ActiveState == services.ActiveActivating {
			if err := s.Units.Stop(ctx, services.UnitKiwix); err != nil {
				log.Warn("the library unit could not be stopped", "error", err.Error())
				return
			}
			log.Warn("the library has no books; the library unit was stopped", "code", doctor.SVC001)
		}
	case (!prevKnown || prevEmpty) && st.ActiveState == services.ActiveInactive:
		if err := s.Units.Start(ctx, services.UnitKiwix); err != nil {
			log.Warn("the library unit could not be started", "error", err.Error())
			return
		}
		log.Info("the library has books again; the library unit was started", "books", res.Books)
	}
}

// checkOpenFiles is step 7: for roots unmounted since the last check, wait, then restart the unit if
// kiwix-serve still has a file open under the removed mount.
func (s *Service) checkOpenFiles(ctx context.Context, log *slog.Logger) {
	if s.Units == nil {
		return
	}
	gone := s.rem.take()
	if len(gone) == 0 {
		return
	}
	wait := s.OpenFileWait
	if wait == 0 {
		wait = DefaultOpenFileWait
	}
	if s.sleep(ctx, wait) != nil {
		return
	}
	st, err := s.Units.State(ctx, services.UnitKiwix)
	if err != nil || st.MainPID <= 0 {
		return
	}
	fd := s.FD
	if fd == nil {
		fd = ProcFD{}
	}
	links, err := fd.Links(st.MainPID)
	if err != nil {
		log.Warn("the library process files could not be listed", "error", err.Error())
		return
	}
	for _, dir := range gone {
		if holdsUnder(links, dir) {
			log.Warn("the library still held files of a removed drive; restarting it")
			if err := s.Units.Restart(ctx, services.UnitKiwix); err != nil {
				log.Warn("the library unit could not be restarted", "error", err.Error())
			}
			return
		}
	}
}
