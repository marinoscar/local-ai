package ready

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/api"
	"github.com/marinoscar/local-ai/agent/internal/ark"
	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/sync/plan"
)

// RefreshEvery is how often the reports are regenerated when nothing else triggers it: ages change
// even when nothing else does (ark-sync.md section 4.12).
const RefreshEvery = 6 * time.Hour

// Why a regeneration happened, for the log.
const (
	TriggerStart  = "start"
	TriggerMount  = "mount"
	TriggerRun    = "run"
	TriggerTick   = "tick"
	TriggerManual = "manual"
)

// Deps are what the service needs. FS, Clock, Plans and Roots are required; the rest is optional and
// each missing source makes the report more cautious, never more optimistic.
type Deps struct {
	// FS writes READY.json and reads the docs index.
	FS config.FS
	// Clock is the time seam: Now and the 6-hour ticker. config.RealClock in production, the manual
	// clock of internal/config/fake in tests.
	Clock config.Clock
	// Plans is the planner's input per root (*plan.Gatherer).
	Plans PlanSource
	// Roots returns the mounted content roots.
	Roots func() []ark.Root
	// Journal is items.json's records.
	Journal JournalItems
	// Runs is the engine's record of runs; nil means none is recorded.
	Runs RunSource
	// Kit is the kit verify report; nil means the kit rows use age alone.
	Kit KitSource
	// Docs says what the render of the manuals left; nil means IndexDocs.
	Docs DocsProbe
	// Modes returns the node's mode changes, oldest first; nil means no history, which counts as
	// online.
	Modes func() []ModeChange
	// Loadable says whether a catalog model can load on this node; nil counts every model as loadable.
	Loadable func(modelID string) bool
	// ReverifyDays returns node.yaml sync.reverify_days; nil or non-positive means 30.
	ReverifyDays func() int
	// SyncState returns the engine's state for status.ark.sync ("" means idle); optional.
	SyncState func() string
	// NodeID and AgentVersion go into generated_by.
	NodeID, AgentVersion string
	// Logger defaults to slog.Default.
	Logger *slog.Logger
}

// cached is the last report of one root.
type cached struct {
	report Report
	// raw is the exact JSON written to READY.json (and kept in memory for a read-only root): what the
	// cloud link sends.
	raw []byte
	// onDisk says whether raw is also on the root. It is false for a read-only root, which is never
	// written, and after a failed write.
	onDisk bool
	needs  Needs
}

// Service computes, stores and serves the readiness reports of the mounted roots. It writes
// `<root>/READY.json` atomically after every regeneration, except on a read-only root, whose report
// is kept in memory and served from there. It implements api.ArkReadyView (GET /ark), and the views
// of /status, /catalog and the cloud snapshot (views.go).
//
// The wiring (SYNC-14, AGENT-20) calls Mounted when a drive appears, Unmounted when it goes,
// RunFinished at the end of every sync, verify or import run, and Start for the 6-hour refresh.
type Service struct {
	d Deps

	// regen serialises regenerations: one at a time, so two triggers cannot write the same file.
	regen sync.Mutex

	mu    sync.RWMutex
	cache map[string]*cached
	order []string
	warm  bool
	// needs is the survival requirements of the node, from the internal root's profile.
	needs Needs

	cancel context.CancelFunc
	done   chan struct{}
}

var _ api.ArkReadyView = (*Service)(nil)

// New checks the dependencies and returns a service with no report yet.
func New(d Deps) (*Service, error) {
	var missing []string
	for name, ok := range map[string]bool{"FS": d.FS != nil, "Clock": d.Clock != nil, "Plans": d.Plans != nil, "Roots": d.Roots != nil} {
		if !ok {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return nil, fmt.Errorf("ready: the service needs %v", missing)
	}
	if d.Docs == nil {
		d.Docs = IndexDocs{FS: d.FS}
	}
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	return &Service{d: d, cache: map[string]*cached{}}, nil
}

func (s *Service) log() *slog.Logger { return s.d.Logger.With("component", "ark") }

// Name implements the app's worker interface.
func (s *Service) Name() string { return "ark-ready" }

// Mounted regenerates the report of a root that was just mounted (event drive.mounted).
func (s *Service) Mounted(ctx context.Context, rootID string) error {
	return s.Regenerate(ctx, TriggerMount, rootID)
}

// RunFinished regenerates the reports of the roots a sync, verify or import run touched (event
// sync.done). With none named it regenerates every root.
func (s *Service) RunFinished(ctx context.Context, rootIDs ...string) error {
	return s.Regenerate(ctx, TriggerRun, rootIDs...)
}

// Unmounted forgets the report of a root that is gone (event drive.unmounted): nobody can read a
// drive that was pulled, so it must not be reported as if it were there.
func (s *Service) Unmounted(rootID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.cache, rootID)
	s.order = without(s.order, rootID)
}

func without(list []string, id string) []string {
	out := list[:0:0]
	for _, v := range list {
		if v != id {
			out = append(out, v)
		}
	}
	return out
}

// Start regenerates once at once and then every RefreshEvery, in the background, until ctx ends or
// Stop is called. A second Start is an error.
func (s *Service) Start(ctx context.Context) error {
	s.mu.Lock()
	if s.cancel != nil {
		s.mu.Unlock()
		return errors.New("ready: service already started")
	}
	loopCtx, cancel := context.WithCancel(ctx)
	ticker := s.d.Clock.NewTicker(RefreshEvery)
	done := make(chan struct{})
	s.cancel, s.done = cancel, done
	s.mu.Unlock()
	go func() {
		defer close(done)
		defer ticker.Stop()
		_ = s.Regenerate(loopCtx, TriggerStart)
		for {
			select {
			case <-loopCtx.Done():
				return
			case <-ticker.C():
				_ = s.Regenerate(loopCtx, TriggerTick)
			}
		}
	}()
	return nil
}

// Stop ends the refresh loop and waits for it, within ctx.
func (s *Service) Stop(ctx context.Context) error {
	s.mu.Lock()
	cancel, done := s.cancel, s.done
	s.cancel, s.done = nil, nil
	s.mu.Unlock()
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

// Regenerate recomputes the reports of the named roots (every mounted root when none is named),
// stores them in memory and writes READY.json on every root that is writable. Roots that are no
// longer mounted lose their report. A root that cannot be assessed keeps its previous report, or
// gets a red one saying so. The error is the context's, or the first write failure after every root
// was handled.
func (s *Service) Regenerate(ctx context.Context, trigger string, rootIDs ...string) error {
	s.regen.Lock()
	defer s.regen.Unlock()

	mounted := map[string]ark.Root{}
	var ids []string
	for _, r := range s.d.Roots() {
		if !scannable(r.State) {
			continue
		}
		mounted[r.ID] = r
		ids = append(ids, r.ID)
	}
	orderRoots(ids, mounted)

	// A root that is gone loses its report, whatever was asked.
	s.mu.Lock()
	for id := range s.cache {
		if _, ok := mounted[id]; !ok {
			delete(s.cache, id)
			s.order = without(s.order, id)
		}
	}
	s.mu.Unlock()

	want := ids
	if len(rootIDs) > 0 {
		want = nil
		for _, id := range rootIDs {
			if _, ok := mounted[id]; ok {
				want = append(want, id)
			}
		}
		orderRoots(want, mounted)
	}

	var firstErr error
	for _, id := range want {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := s.regenerateOne(ctx, trigger, mounted[id]); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	s.mu.Lock()
	s.warm = true
	s.mu.Unlock()
	return firstErr
}

// orderRoots sorts ids internal first, then drives in mount order, like the planner and the index.
func orderRoots(ids []string, roots map[string]ark.Root) {
	sort.SliceStable(ids, func(i, j int) bool {
		a, b := roots[ids[i]], roots[ids[j]]
		if (a.Kind == ark.KindInternal) != (b.Kind == ark.KindInternal) {
			return a.Kind == ark.KindInternal
		}
		if a.MountID != b.MountID {
			return a.MountID < b.MountID
		}
		return a.ID < b.ID
	})
}

// regenerateOne builds, stores and (when the root is writable) writes the report of one root.
func (s *Service) regenerateOne(ctx context.Context, trigger string, root ark.Root) error {
	in, err := s.input(ctx, root)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		s.log().Warn("ready: cannot assess a root", "root", root.ID, "trigger", trigger, "err", err)
		s.mu.RLock()
		_, have := s.cache[root.ID]
		s.mu.RUnlock()
		if have {
			// The previous report is the best picture there is; it ages in place until the root
			// answers again (its generated_at says when it was true).
			return nil
		}
		in = s.failedInput(root, assessFailureCode(root.Kind))
	}
	rep := Build(in)
	// A read-only root is never written: an unclean drive may be failing, and a read-only mount or a
	// network filesystem refuses. Its report is kept in memory and served from there.
	writable := !in.Root.ReadOnly && !root.ReadOnly && root.State != ark.StateUnclean && root.State != ark.StateReadOnly

	c := &cached{report: rep, needs: NeedsFor(in.Catalog, in.Profile, in.Loadable)}
	var werr error
	if writable {
		c.raw, werr = Write(s.d.FS, root.Path, rep)
		switch {
		case werr == nil:
			c.onDisk = true
		case IsDurabilityOnly(werr):
			s.log().Warn("ready: READY.json replaced but the directory sync failed", "root", root.ID, "err", werr)
			c.onDisk, werr = true, nil
		default:
			s.log().Warn("ready: cannot write READY.json", "root", root.ID, "err", werr)
		}
	} else if raw, merr := rep.MarshalIndented(); merr == nil {
		c.raw = raw
	}
	if c.raw == nil {
		// The marshalling of a report cannot fail; this keeps the cache coherent if it ever does.
		c.raw, _ = rep.MarshalIndented()
	}

	s.mu.Lock()
	if _, ok := s.cache[root.ID]; !ok {
		s.order = append(s.order, root.ID)
	}
	s.cache[root.ID] = c
	if root.Kind == ark.KindInternal {
		s.needs = c.needs
	}
	s.mu.Unlock()
	s.log().Debug("ready: report", "root", root.ID, "trigger", trigger, "overall", string(rep.Overall), "survival_capable", rep.SurvivalCapable, "written", c.onDisk)
	return werr
}

// failedInput is the Input of a root that could not be assessed.
func (s *Service) failedInput(root ark.Root, code string) Input {
	return Input{
		Now: s.d.Clock.Now(), NodeID: s.d.NodeID, AgentVersion: s.d.AgentVersion,
		Root:    RootFacts{ID: root.ID, Kind: root.Kind, Path: root.Path, Profile: root.Profile, State: root.State, ReadOnly: root.ReadOnly},
		Failure: code,
	}
}

// input gathers the Input of one root: the planner's offline input and plan, the journal, the run
// record, the kit report, the docs facts and the mode history. An unknown profile is not an error here
// but a report of its own; any other failure is returned.
func (s *Service) input(ctx context.Context, root ark.Root) (Input, error) {
	now := s.d.Clock.Now()
	pin, err := s.d.Plans.Gather(ctx, root.ID, plan.GatherOptions{})
	if err != nil {
		if e, ok := isUnknownProfile(err); ok {
			in := s.failedInput(root, "")
			in.ProfileUnknown = true
			in.Root.Profile = e.Profile
			return in, nil
		}
		return Input{}, err
	}
	// The planner sees the node's connectivity; the report must not: whether a pinned newer version
	// is installed does not depend on the cable. Plan as if online, from what the catalog pins (the
	// gatherer called no resolver), so the same disks always give the same report.
	pin.Mode, pin.Offline = plan.ModeOnline, false
	rp := plan.Plan(pin)

	in := Input{
		Now: now, NodeID: s.d.NodeID, AgentVersion: s.d.AgentVersion,
		Root: RootFacts{
			ID: root.ID, Kind: root.Kind, Path: root.Path, Profile: pin.Root.Profile, State: root.State,
			TotalBytes: pin.Root.TotalBytes, FreeBytes: pin.Root.FreeBytes, ReadOnly: pin.Root.ReadOnly || root.ReadOnly,
		},
		Plan: rp, Catalog: pin.Catalog, Profile: pin.Profile,
		Present:                 pin.Present,
		Journal:                 journalFacts(s.d.Journal, root.ID),
		NewestVerifiedElsewhere: newestElsewhere(pin.OtherRoots),
		Docs:                    s.d.Docs.Docs(root),
		Loadable:                s.d.Loadable,
	}
	if pin.Catalog != nil {
		in.CatalogVersion = pin.Catalog.Version
	}
	if s.d.Runs != nil {
		in.LastSync, in.LastDoneAt = s.d.Runs.LastRuns(root.ID)
	}
	if s.d.Kit != nil {
		in.Kit = s.d.Kit.KitReport(root.ID)
	}
	if s.d.ReverifyDays != nil {
		in.ReverifyDays = s.d.ReverifyDays()
	}
	if s.d.Modes != nil {
		from := in.LastDoneAt
		if from.IsZero() {
			from = now.Add(-InternalStaleDays * day)
		}
		in.OfflineMostly = !MostlyOnline(s.d.Modes(), from, now)
	}
	return in, nil
}

// Reports returns a copy of the reports of the mounted roots, internal first. Before the first
// regeneration it is empty.
func (s *Service) Reports() []Report {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Report, 0, len(s.order))
	for _, id := range s.order {
		if c, ok := s.cache[id]; ok {
			out = append(out, c.report)
		}
	}
	return out
}

// Report returns the report of one root.
func (s *Service) Report(rootID string) (Report, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	c, ok := s.cache[rootID]
	if !ok {
		return Report{}, false
	}
	return c.report, true
}

// RawJSON returns the exact bytes of a root's report: the content of READY.json, or what it would
// hold for a read-only root. The cloud link sends these bytes as they are.
func (s *Service) RawJSON(rootID string) ([]byte, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	c, ok := s.cache[rootID]
	if !ok {
		return nil, false
	}
	return append([]byte(nil), c.raw...), true
}

// Colours maps each mounted root id to its overall colour: the `ready` of the sync.done event.
func (s *Service) Colours() map[string]string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]string, len(s.cache))
	for id, c := range s.cache {
		out[id] = string(c.report.Overall)
	}
	return out
}

// NodeSurvivalCapable is the node-level survival_capable: the requirements are met by the verified
// items of every mounted root together (survival.go). It is what /status and /portal/status show.
// Before the first report, or with no internal root, it is false.
func (s *Service) NodeSurvivalCapable() bool {
	s.mu.RLock()
	needs := s.needs
	reports := make([]Report, 0, len(s.cache))
	for _, id := range s.order {
		reports = append(reports, s.cache[id].report)
	}
	s.mu.RUnlock()
	return NodeSurvivalCapable(needs, reports)
}

// Ready implements api.ArkReadyView: the reports of the mounted roots with problem titles in lang. The
// first call before any regeneration computes them, so GET /ark never answers an empty list for a node
// that has roots.
func (s *Service) Ready(ctx context.Context, lang string) ([]any, error) {
	s.mu.RLock()
	warm := s.warm
	s.mu.RUnlock()
	if !warm {
		if err := s.Regenerate(ctx, TriggerManual); err != nil && ctx.Err() != nil {
			return nil, err
		}
	}
	reports := s.Reports()
	out := make([]any, len(reports))
	for i, r := range reports {
		out[i] = r.In(lang)
	}
	return out, nil
}
