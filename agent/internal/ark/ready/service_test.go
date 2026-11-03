package ready_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/ark"
	"github.com/marinoscar/local-ai/agent/internal/ark/ready"
	"github.com/marinoscar/local-ai/agent/internal/config"
	cfgfake "github.com/marinoscar/local-ai/agent/internal/config/fake"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/sync/journal"
	"github.com/marinoscar/local-ai/agent/internal/sync/plan"
)

// fakePlans is the planner's input per root: each root has a world that the test can change between
// two regenerations. It counts the calls and checks that the service never asks for a resolver.
type fakePlans struct {
	mu      sync.Mutex
	t       *testing.T
	worlds  map[string]*world
	errs    map[string]error
	calls   map[string]int
	resolve bool
}

func (f *fakePlans) Gather(_ context.Context, rootID string, opt plan.GatherOptions) (plan.PlanInput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls[rootID]++
	if opt.Resolve {
		f.resolve = true
	}
	if err := f.errs[rootID]; err != nil {
		return plan.PlanInput{}, err
	}
	w, ok := f.worlds[rootID]
	if !ok {
		return plan.PlanInput{}, &plan.ErrUnknownRoot{Root: rootID}
	}
	return w.planInput(), nil
}

func (f *fakePlans) count(id string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[id]
}

type fakeJournal struct{ items []journal.Item }

func (f fakeJournal) List() []journal.Item { return f.items }

type fakeRuns struct {
	last map[string]*ready.RunInfo
	done map[string]time.Time
}

func (f fakeRuns) LastRuns(id string) (*ready.RunInfo, time.Time) { return f.last[id], f.done[id] }

type fakeKit map[string]ready.KitReport

func (f fakeKit) KitReport(id string) ready.KitReport { return f[id] }

// rig is a service over two roots (the internal one and a drive) in real temporary directories.
type rig struct {
	t      *testing.T
	clock  *cfgfake.Clock
	fs     config.FS
	plans  *fakePlans
	roots  []ark.Root
	svc    *ready.Service
	regen  chan string
	deps   ready.Deps
	driveW *world
	intW   *world
}

const driveID = "ark-3f9c0a12"

func newRig(t *testing.T, mod ...func(*ready.Deps)) *rig {
	t.Helper()
	r := &rig{t: t, clock: cfgfake.NewClock(now), fs: config.OSFS{}, regen: make(chan string, 16)}
	r.intW = survivalWorld()
	r.driveW = survivalWorld().drive()
	r.plans = &fakePlans{t: t, worlds: map[string]*world{ark.InternalID: r.intW, driveID: r.driveW}, errs: map[string]error{}, calls: map[string]int{}}
	r.roots = []ark.Root{
		{ID: ark.InternalID, Kind: ark.KindInternal, Path: t.TempDir(), Profile: "test-profile", State: ark.StateReady},
		{ID: driveID, Kind: ark.KindArkDrive, Path: t.TempDir(), Profile: "ark-drive-full", State: ark.StateReady, MountID: 40},
	}
	r.deps = ready.Deps{
		FS: r.fs, Clock: r.clock, Plans: r.plans, NodeID: "ai-n3", AgentVersion: "0.4.0",
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Roots:       func() []ark.Root { return append([]ark.Root(nil), r.roots...) },
		Regenerated: func(trigger string, ids []string) { r.regen <- trigger },
	}
	for _, m := range mod {
		m(&r.deps)
	}
	svc, err := ready.New(r.deps)
	if err != nil {
		t.Fatal(err)
	}
	r.svc = svc
	return r
}

func (r *rig) rootPath(id string) string {
	for _, root := range r.roots {
		if root.ID == id {
			return root.Path
		}
	}
	r.t.Fatalf("no root %s", id)
	return ""
}

func (r *rig) setRoot(id string, f func(*ark.Root)) {
	for i := range r.roots {
		if r.roots[i].ID == id {
			f(&r.roots[i])
		}
	}
}

func (r *rig) readyFile(id string) ([]byte, error) {
	return os.ReadFile(filepath.Join(r.rootPath(id), "READY.json"))
}

func ids(reports []ready.Report) []string {
	out := make([]string, len(reports))
	for i, r := range reports {
		out[i] = r.Root.ID
	}
	return out
}

func TestNewNeedsItsDependencies(t *testing.T) {
	if _, err := ready.New(ready.Deps{}); err == nil {
		t.Fatal("New must refuse missing dependencies")
	}
	r := newRig(t)
	if r.svc.Name() == "" {
		t.Error("the service is a worker and needs a name")
	}
}

// TestRegenerateWritesEveryWritableRoot: the report of each mounted root is written atomically to
// <root>/READY.json, the bytes on disk are the bytes the service hands out, and the file parses.
func TestRegenerateWritesEveryWritableRoot(t *testing.T) {
	r := newRig(t)
	if err := r.svc.Regenerate(context.Background(), ready.TriggerManual); err != nil {
		t.Fatal(err)
	}
	if got := ids(r.svc.Reports()); !reflect.DeepEqual(got, []string{"internal", driveID}) {
		t.Fatalf("reports = %v: internal first", got)
	}
	for _, id := range []string{"internal", driveID} {
		onDisk, err := r.readyFile(id)
		if err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		raw, ok := r.svc.RawJSON(id)
		if !ok || string(raw) != string(onDisk) {
			t.Errorf("%s: RawJSON differs from the file", id)
		}
		parsed, _, err := ready.Read(r.fs, r.rootPath(id))
		if err != nil || parsed.Root.ID != id || parsed.GeneratedBy.NodeID != "ai-n3" || parsed.GeneratedBy.AgentVersion != "0.4.0" {
			t.Errorf("%s: parsed %+v err %v", id, parsed.Root, err)
		}
		info, _ := os.Stat(filepath.Join(r.rootPath(id), "READY.json"))
		if info.Mode().Perm() != 0o644 {
			t.Errorf("%s: mode %v", id, info.Mode().Perm())
		}
	}
	if got := r.plans.resolve; got {
		t.Error("a report must never ask the planner to resolve over the network")
	}
	// Both roots hold everything: the node is capable and the colours are green.
	if !r.svc.NodeSurvivalCapable() {
		t.Error("node survival capable")
	}
	if got := r.svc.Colours(); !reflect.DeepEqual(got, map[string]string{"internal": "green", driveID: "green"}) {
		t.Errorf("colours = %v", got)
	}
	// No temp file is left behind.
	ents, _ := os.ReadDir(r.rootPath("internal"))
	if len(ents) != 1 {
		t.Errorf("the root holds %d entries, want only READY.json", len(ents))
	}
}

// TestReadOnlyRootsAreNeverWritten: a read-only root, a root in state read_only and an unclean one keep
// their report in memory and serve it from there; nothing is created on them.
func TestReadOnlyRootsAreNeverWritten(t *testing.T) {
	tests := []struct {
		name string
		mod  func(root *ark.Root)
	}{
		{"read-only flag (network filesystem or failed layout)", func(root *ark.Root) { root.ReadOnly = true }},
		{"mounted read-only", func(root *ark.Root) { root.State = ark.StateReadOnly }},
		{"unclean", func(root *ark.Root) { root.State = ark.StateUnclean }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRig(t)
			r.setRoot(driveID, tt.mod)
			if err := r.svc.Regenerate(context.Background(), ready.TriggerMount); err != nil {
				t.Fatal(err)
			}
			if _, err := r.readyFile(driveID); !os.IsNotExist(err) {
				t.Fatalf("READY.json must not be written on the read-only root: %v", err)
			}
			if ents, _ := os.ReadDir(r.rootPath(driveID)); len(ents) != 0 {
				t.Errorf("nothing at all may be created on the root: %v", ents)
			}
			rep, ok := r.svc.Report(driveID)
			if !ok || rep.Root.ID != driveID {
				t.Fatal("the report is served from memory")
			}
			raw, ok := r.svc.RawJSON(driveID)
			if !ok || len(raw) == 0 {
				t.Fatal("RawJSON of a read-only root is what the file would hold")
			}
			if parsed, err := ready.Parse(raw); err != nil || parsed.Root.ID != driveID {
				t.Errorf("raw does not parse: %v", err)
			}
			// The writable root is still written.
			if _, err := r.readyFile("internal"); err != nil {
				t.Errorf("internal: %v", err)
			}
		})
	}
}

// TestUncleanRootIsRed: an unclean drive is red with LAI-ARK-004 and is served, never written.
func TestUncleanRootIsRed(t *testing.T) {
	r := newRig(t)
	r.setRoot(driveID, func(root *ark.Root) { root.State = ark.StateUnclean })
	if err := r.svc.Regenerate(context.Background(), ready.TriggerMount, driveID); err != nil {
		t.Fatal(err)
	}
	rep, _ := r.svc.Report(driveID)
	if rep.Overall != ready.Red || !rep.HasProblem(doctor.ARK004) {
		t.Errorf("overall %s problems %v", rep.Overall, problemCodes(rep))
	}
	if _, ok := r.svc.Report("internal"); ok {
		t.Error("only the named root was regenerated")
	}
}

// TestDriveAbsent: a root that is not mounted (never there, pulled, ejecting, not mounted, unusable or
// not set up) has no report, and a root that goes away loses its report at the next regeneration.
func TestDriveAbsent(t *testing.T) {
	for _, state := range []ark.RootState{ark.StateNotMounted, ark.StateUnusable, ark.StateEjecting, ark.StateUninitialized} {
		t.Run(string(state), func(t *testing.T) {
			r := newRig(t)
			r.setRoot(driveID, func(root *ark.Root) { root.State = state })
			if err := r.svc.Regenerate(context.Background(), ready.TriggerManual); err != nil {
				t.Fatal(err)
			}
			if got := ids(r.svc.Reports()); !reflect.DeepEqual(got, []string{"internal"}) {
				t.Errorf("reports = %v", got)
			}
			if _, err := r.readyFile(driveID); !os.IsNotExist(err) {
				t.Error("nothing is written to a root that is not served")
			}
			if r.plans.count(driveID) != 0 {
				t.Error("a root that is not served is not even planned")
			}
		})
	}

	t.Run("pulled after a report", func(t *testing.T) {
		r := newRig(t)
		if err := r.svc.Regenerate(context.Background(), ready.TriggerManual); err != nil {
			t.Fatal(err)
		}
		r.roots = r.roots[:1] // the drive is gone
		if err := r.svc.Regenerate(context.Background(), ready.TriggerTick); err != nil {
			t.Fatal(err)
		}
		if got := ids(r.svc.Reports()); !reflect.DeepEqual(got, []string{"internal"}) {
			t.Errorf("reports = %v", got)
		}
		if _, ok := r.svc.RawJSON(driveID); ok {
			t.Error("the pulled drive still has bytes")
		}
	})

	t.Run("Unmounted forgets at once", func(t *testing.T) {
		r := newRig(t)
		_ = r.svc.Regenerate(context.Background(), ready.TriggerManual)
		r.svc.Unmounted(driveID)
		if got := ids(r.svc.Reports()); !reflect.DeepEqual(got, []string{"internal"}) {
			t.Errorf("reports = %v", got)
		}
		if r.svc.NodeSurvivalCapable() != true {
			t.Error("the internal root alone is capable in this fixture")
		}
	})
}

// TestNodeLevelSurvival: the node is capable through the union of its roots.
func TestNodeLevelSurvival(t *testing.T) {
	r := newRig(t)
	r.intW.without("wiki", "map-a", "map-assets")
	r.driveW.without("brain", "eyes", "extra", "whisper-small", "piper-en", "piper-es")
	if err := r.svc.Regenerate(context.Background(), ready.TriggerManual); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"internal", driveID} {
		if rep, _ := r.svc.Report(id); rep.SurvivalCapable {
			t.Errorf("%s alone must not be capable", id)
		}
	}
	if !r.svc.NodeSurvivalCapable() {
		t.Error("the two roots together are capable")
	}
	// The drive goes away: the node is no longer capable.
	r.roots = r.roots[:1]
	_ = r.svc.Regenerate(context.Background(), ready.TriggerTick)
	if r.svc.NodeSurvivalCapable() {
		t.Error("without the drive the node is not capable")
	}
}

func TestNodeSurvivalBeforeAnyReport(t *testing.T) {
	if newRig(t).svc.NodeSurvivalCapable() {
		t.Error("no report is not capable")
	}
}

// TestTriggers: Mounted regenerates one root, RunFinished the roots named (all when none), Unmounted
// none.
func TestTriggers(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	if err := r.svc.Mounted(ctx, driveID); err != nil {
		t.Fatal(err)
	}
	if r.plans.count(driveID) != 1 || r.plans.count("internal") != 0 {
		t.Errorf("Mounted(drive): calls %d / %d", r.plans.count(driveID), r.plans.count("internal"))
	}
	if got := <-r.regen; got != ready.TriggerMount {
		t.Errorf("trigger = %s", got)
	}
	if err := r.svc.RunFinished(ctx, "internal"); err != nil {
		t.Fatal(err)
	}
	if r.plans.count(driveID) != 1 || r.plans.count("internal") != 1 {
		t.Errorf("RunFinished(internal): calls %d / %d", r.plans.count(driveID), r.plans.count("internal"))
	}
	if got := <-r.regen; got != ready.TriggerRun {
		t.Errorf("trigger = %s", got)
	}
	if err := r.svc.RunFinished(ctx); err != nil {
		t.Fatal(err)
	}
	if r.plans.count(driveID) != 2 || r.plans.count("internal") != 2 {
		t.Errorf("RunFinished(): calls %d / %d", r.plans.count(driveID), r.plans.count("internal"))
	}
	<-r.regen
	// A root that is not mounted is not regenerated, and a run that touched nothing calls no hook.
	if err := r.svc.RunFinished(ctx, "ark-00000000"); err != nil {
		t.Fatal(err)
	}
	if got := <-r.regen; got != ready.TriggerRun {
		t.Errorf("trigger = %s", got)
	}
	if r.plans.count("ark-00000000") != 0 {
		t.Error("an unknown root is not planned")
	}
}

// TestRegenerateAfterARunShowsTheNewState: the report follows the disks.
func TestRegenerateAfterARunShowsTheNewState(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	r.intW.without("whisper-small")
	_ = r.svc.RunFinished(ctx)
	rep, _ := r.svc.Report("internal")
	if rep.Overall != ready.Red || rep.SurvivalCapable || item(t, rep, "whisper-small").Status != ready.StatusMissing {
		t.Fatalf("before: %s %v", rep.Overall, rep.SurvivalCapable)
	}
	r.intW.have("whisper-small", "v1", 0)
	_ = r.svc.RunFinished(ctx, "internal")
	rep, _ = r.svc.Report("internal")
	if rep.Overall != ready.Green || !rep.SurvivalCapable {
		t.Errorf("after: %s %v", rep.Overall, rep.SurvivalCapable)
	}
	disk, _ := r.readyFile("internal")
	if parsed, _ := ready.Parse(disk); parsed.Overall != ready.Green {
		t.Error("the file follows")
	}
}

// TestStartRegeneratesAtStartAndEverySixHours: with the manual clock, one regeneration at start and
// one more each time six hours pass; the report says when it was made.
func TestStartRegeneratesAtStartAndEverySixHours(t *testing.T) {
	r := newRig(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := r.svc.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if err := r.svc.Start(ctx); err == nil {
		t.Error("a second Start is an error")
	}
	if got := <-r.regen; got != ready.TriggerStart {
		t.Fatalf("first trigger = %s", got)
	}
	rep, _ := r.svc.Report("internal")
	if rep.GeneratedAt != "2026-11-02T15:00:00Z" {
		t.Errorf("generated_at = %s", rep.GeneratedAt)
	}

	r.clock.Advance(5*time.Hour + 59*time.Minute)
	select {
	case got := <-r.regen:
		t.Fatalf("regenerated before six hours: %s", got)
	default:
	}
	r.clock.Advance(time.Minute)
	if got := <-r.regen; got != ready.TriggerTick {
		t.Fatalf("second trigger = %s", got)
	}
	rep, _ = r.svc.Report("internal")
	if rep.GeneratedAt != "2026-11-02T21:00:00Z" {
		t.Errorf("generated_at = %s, want the clock at the tick", rep.GeneratedAt)
	}
	r.clock.Advance(RefreshEvery)
	if got := <-r.regen; got != ready.TriggerTick {
		t.Fatalf("third trigger = %s", got)
	}

	stop, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer stopCancel()
	if err := r.svc.Stop(stop); err != nil {
		t.Fatal(err)
	}
	if r.clock.Tickers() != 0 {
		t.Error("the ticker is stopped")
	}
	if err := r.svc.Stop(stop); err != nil {
		t.Errorf("a second Stop is a no-op: %v", err)
	}
}

// RefreshEvery is repeated here so the test reads as "six hours".
const RefreshEvery = ready.RefreshEvery

// TestAgesChangeWithTheClockAlone: nothing on the disks changes, six hours pass, and the report is
// regenerated with new ages (the reason for the ticker).
func TestAgesChangeWithTheClockAlone(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	_ = r.svc.Regenerate(ctx, ready.TriggerManual)
	a1 := item2(t, r.svc, "internal", "brain")
	r.clock.Advance(3 * day)
	_ = r.svc.Regenerate(ctx, ready.TriggerTick)
	a2 := item2(t, r.svc, "internal", "brain")
	if a2 != a1+3 {
		t.Errorf("age %d then %d, want +3", a1, a2)
	}
}

func item2(t *testing.T, s *ready.Service, root, id string) int {
	t.Helper()
	rep, ok := s.Report(root)
	if !ok {
		t.Fatalf("no report for %s", root)
	}
	it := item(t, rep, id)
	return *it.AgeDays
}

// TestCorruptReadyFileIsOverwritten: a damaged READY.json on the root is never an input; the next
// regeneration replaces it.
func TestCorruptReadyFileIsOverwritten(t *testing.T) {
	r := newRig(t)
	path := filepath.Join(r.rootPath("internal"), "READY.json")
	if err := os.WriteFile(path, []byte(`{"schema_version": 1, "overall": `), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ready.Read(r.fs, r.rootPath("internal")); !errors.Is(err, ready.ErrCorrupt) {
		t.Fatalf("Read = %v, want ErrCorrupt", err)
	}
	if err := r.svc.Regenerate(context.Background(), ready.TriggerMount, "internal"); err != nil {
		t.Fatal(err)
	}
	if rep, _, err := ready.Read(r.fs, r.rootPath("internal")); err != nil || rep.Overall != ready.Green {
		t.Errorf("after: %v %v", rep.Overall, err)
	}
}

// TestUnknownProfile: a root whose profile is not in the catalog is red with LAI-CAT-003, and the
// report is written like any other.
func TestUnknownProfile(t *testing.T) {
	r := newRig(t)
	r.plans.errs["internal"] = &plan.ErrUnknownProfile{Profile: "no-such-profile", Root: "internal"}
	if err := r.svc.Regenerate(context.Background(), ready.TriggerManual, "internal"); err != nil {
		t.Fatal(err)
	}
	rep, _ := r.svc.Report("internal")
	if rep.Overall != ready.Red || rep.SurvivalCapable || !rep.HasProblem(doctor.CAT003) || rep.Root.Profile != "no-such-profile" || len(rep.Items) != 0 {
		t.Errorf("report = %s %v %v profile %s items %d", rep.Overall, rep.SurvivalCapable, problemCodes(rep), rep.Root.Profile, len(rep.Items))
	}
	if _, err := r.readyFile("internal"); err != nil {
		t.Errorf("written: %v", err)
	}
}

// TestAssessFailure: a root the planner cannot read keeps its previous report; with none it gets a red
// one that says so, with the code of its kind.
func TestAssessFailure(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	boom := errors.New("statfs: input/output error")
	r.plans.errs["internal"], r.plans.errs[driveID] = boom, boom
	if err := r.svc.Regenerate(ctx, ready.TriggerMount); err != nil {
		t.Fatal(err)
	}
	in, _ := r.svc.Report("internal")
	dr, _ := r.svc.Report(driveID)
	if in.Overall != ready.Red || !in.HasProblem(doctor.AGT099) || dr.Overall != ready.Red || !dr.HasProblem(doctor.ARK014) {
		t.Errorf("internal %v, drive %v", problemCodes(in), problemCodes(dr))
	}

	// With a previous report, a failure keeps it.
	delete(r.plans.errs, "internal")
	_ = r.svc.Regenerate(ctx, ready.TriggerRun, "internal")
	good, _ := r.svc.Report("internal")
	if good.Overall != ready.Green {
		t.Fatalf("the fixture should be green: %s", good.Overall)
	}
	r.clock.Advance(time.Hour)
	r.plans.errs["internal"] = boom
	if err := r.svc.Regenerate(ctx, ready.TriggerTick, "internal"); err != nil {
		t.Fatal(err)
	}
	kept, _ := r.svc.Report("internal")
	if kept.Overall != ready.Green || kept.GeneratedAt != good.GeneratedAt {
		t.Errorf("the previous report must be kept: %s %s", kept.Overall, kept.GeneratedAt)
	}
}

// TestWriteFailures: a failed write keeps the old file and the new report in memory and is returned; a
// failed directory sync is not an error.
func TestWriteFailures(t *testing.T) {
	t.Run("rename fails", func(t *testing.T) {
		faulty := cfgfake.New(config.OSFS{})
		r := newRig(t, func(d *ready.Deps) { d.FS = faulty })
		ctx := context.Background()
		if err := r.svc.Regenerate(ctx, ready.TriggerManual, "internal"); err != nil {
			t.Fatal(err)
		}
		before, _ := r.readyFile("internal")
		r.intW.without("whisper-small")
		faulty.FailOn("rename", -1)
		err := r.svc.Regenerate(ctx, ready.TriggerRun, "internal")
		if err == nil {
			t.Fatal("the write failure must be returned")
		}
		after, _ := r.readyFile("internal")
		if string(after) != string(before) {
			t.Error("a failed write must leave the previous file untouched")
		}
		rep, _ := r.svc.Report("internal")
		if rep.Overall != ready.Red {
			t.Errorf("the new report is served from memory: %s", rep.Overall)
		}
		if ents, _ := os.ReadDir(r.rootPath("internal")); len(ents) != 1 {
			t.Errorf("a temp file was left behind: %v", ents)
		}
	})
	t.Run("directory sync fails", func(t *testing.T) {
		faulty := cfgfake.New(config.OSFS{})
		r := newRig(t, func(d *ready.Deps) { d.FS = faulty })
		faulty.FailOn("syncdir", -1)
		if err := r.svc.Regenerate(context.Background(), ready.TriggerManual, "internal"); err != nil {
			t.Fatalf("durability alone is not an error: %v", err)
		}
		if _, _, err := ready.Read(config.OSFS{}, r.rootPath("internal")); err != nil {
			t.Errorf("the file is in place: %v", err)
		}
	})
}

// TestReadyView: GET /ark's view. A cold service computes on first use; titles follow the language;
// an empty node answers an empty list.
func TestReadyView(t *testing.T) {
	r := newRig(t)
	r.intW.without("wiki")
	r.intW.free, r.intW.minFree = 2, 10
	r.intW.have("brain", "v1", 0)
	roots, err := r.svc.Ready(context.Background(), "es")
	if err != nil || len(roots) != 2 {
		t.Fatalf("roots = %d err %v", len(roots), err)
	}
	rep := roots[0].(ready.Report)
	if rep.Root.ID != "internal" || !rep.HasProblem(doctor.SYN040) {
		t.Fatalf("internal = %v", problemCodes(rep))
	}
	es, _, _ := doctor.Text(doctor.SYN040, "es")
	if got := rep.Problems[len(rep.Problems)-1].Title; got != es {
		t.Errorf("title = %q, want %q", got, es)
	}
	b, err := json.Marshal(roots)
	if err != nil || len(b) == 0 {
		t.Fatalf("marshal: %v", err)
	}
	en, _ := r.svc.Ready(context.Background(), "en")
	enTitle, _, _ := doctor.Text(doctor.SYN040, "en")
	if got := en[0].(ready.Report).Problems[len(rep.Problems)-1].Title; got != enTitle {
		t.Errorf("title = %q", got)
	}
	if r.plans.count("internal") != 1 {
		t.Errorf("the reports are computed once and served from memory: %d calls", r.plans.count("internal"))
	}

	empty := newRig(t, func(d *ready.Deps) { d.Roots = func() []ark.Root { return nil } })
	if roots, err := empty.svc.Ready(context.Background(), "en"); err != nil || len(roots) != 0 {
		t.Errorf("an empty node: %v %v", roots, err)
	}
}

// TestInputFacts: the journal, the run record, the kit report, the mode history and reverify_days
// reach the report.
func TestInputFacts(t *testing.T) {
	checked, _ := json.Marshal(now.Add(-2 * day).Format(time.RFC3339))
	bad, _ := json.Marshal(12)
	runs := fakeRuns{
		last: map[string]*ready.RunInfo{"internal": {RunID: "run-x", Trigger: "manual", FinishedAt: now.Add(-20 * day), State: "done"}},
		done: map[string]time.Time{"internal": now.Add(-20 * day)},
	}
	modes := []ready.ModeChange{{At: now.Add(-21 * day), Mode: "offline"}}
	driveWiki := func(w *world) {
		for i := range w.content {
			if w.content[i].ID == "wiki" {
				w.content[i].Source.VersionPolicy = "pinned"
			}
		}
		w.have("wiki", "v0", day).target("wiki", "v1", 20)
	}
	r := newRig(t, func(d *ready.Deps) {
		d.Journal = fakeJournal{items: []journal.Item{
			{RootID: "internal", ItemID: "brain", State: journal.StateVerified, Extras: map[string]json.RawMessage{ready.ExtraCheckedCurrentAt: checked}},
			{RootID: "internal", ItemID: "eyes", State: journal.StateVerified, Extras: map[string]json.RawMessage{ready.ExtraCheckedCurrentAt: bad}},
			{RootID: driveID, ItemID: "wiki", State: journal.StateFailed, Version: "v9", Code: doctor.SYN031},
		}}
		d.Runs = runs
		d.Kit = fakeKit{"internal": {Findings: []ready.KitFinding{{Code: doctor.KIT040}}}}
		d.Modes = func() []ready.ModeChange { return modes }
		d.ReverifyDays = func() int { return 5 }
	})
	r.intW.reverified("brain", 11*day)
	driveWiki(r.driveW)
	if err := r.svc.Regenerate(context.Background(), ready.TriggerManual); err != nil {
		t.Fatal(err)
	}
	in, _ := r.svc.Report("internal")
	brain := item(t, in, "brain")
	if brain.CheckedCurrentAt != "2026-10-31T15:00:00Z" || *brain.AgeDays != 2 {
		t.Errorf("checked_current_at from the journal extras: %+v", brain)
	}
	if item(t, in, "eyes").CheckedCurrentAt != "" {
		t.Error("an extras value that is not a time is ignored")
	}
	if in.LastSync == nil || in.LastSync.RunID != "run-x" || in.LastSync.AgeDays != 20 {
		t.Errorf("last_sync = %+v", in.LastSync)
	}
	if !in.HasProblem(doctor.KIT040) || in.Overall != ready.Red {
		t.Errorf("kit report: %v %s", problemCodes(in), in.Overall)
	}
	if in.HasProblem(doctor.SYN004) {
		t.Error("the node was offline for most of the 20 days: no LAI-SYN-004")
	}
	if !in.HasProblem(doctor.SYN025) {
		t.Errorf("reverify_days of 5 makes 11 days overdue: %v", problemCodes(in))
	}
	if got := item(t, in, "wiki"); got.Status != ready.StatusVerified {
		t.Errorf("a failure in the drive's journal does not touch the internal root's items: %s", got.Status)
	}
	dr, _ := r.svc.Report(driveID)
	if got := item(t, dr, "wiki"); got.Status != ready.StatusStale || got.Code != doctor.SYN031 {
		t.Errorf("the drive's own journal: %s %s", got.Status, got.Code)
	}
}

// TestOnlineMostlyFromModes: with a mode history of mostly online, the no-recent-sync warning shows.
func TestOnlineMostlyFromModes(t *testing.T) {
	runs := fakeRuns{done: map[string]time.Time{"internal": now.Add(-20 * day)}}
	r := newRig(t, func(d *ready.Deps) {
		d.Runs = runs
		d.Modes = func() []ready.ModeChange { return []ready.ModeChange{{At: now.Add(-3 * day), Mode: "offline"}} }
	})
	_ = r.svc.Regenerate(context.Background(), ready.TriggerManual, "internal")
	rep, _ := r.svc.Report("internal")
	if !rep.HasProblem(doctor.SYN004) {
		t.Errorf("problems = %v", problemCodes(rep))
	}
}

// TestCloudSeesTheBytesOfTheFile: the cloud link gets the exact bytes of READY.json, and the tiers,
// counts and bytes of the root.
func TestCloudSeesTheBytesOfTheFile(t *testing.T) {
	r := newRig(t)
	r.driveW.without("wiki-es")
	r.driveW.without("map-b")
	if err := r.svc.Regenerate(context.Background(), ready.TriggerManual); err != nil {
		t.Fatal(err)
	}
	got := r.svc.CloudSource().Ark()
	if len(got.Roots) != 2 || got.Roots[0].ID != "internal" || got.Roots[1].ID != driveID {
		t.Fatalf("roots = %+v", got.Roots)
	}
	for _, row := range got.Roots {
		disk, err := r.readyFile(row.ID)
		if err != nil || string(row.ReadyJSON) != string(disk) {
			t.Errorf("%s: the cloud must get the bytes of the file", row.ID)
		}
		if row.Overall == "" || !row.Present {
			t.Errorf("row %s: overall %q present %v", row.ID, row.Overall, row.Present)
		}
	}
	d := got.Roots[1]
	if d.Kind != "ark_drive" || d.T1 != "ok" || d.T2 != "missing" || d.T3 != "partial" || d.ItemsMissing != 2 {
		t.Errorf("drive row: kind %s tiers %s %s %s missing %d", d.Kind, d.T1, d.T2, d.T3, d.ItemsMissing)
	}
}

func TestTiers(t *testing.T) {
	item := func(tier int, st ready.Status) ready.Item { return ready.Item{ID: "x", Tier: tier, Status: st} }
	tests := []struct {
		name       string
		items      []ready.Item
		t1, t2, t3 string
	}{
		{"nothing planned", nil, "ok", "ok", "not_planned"},
		{"all verified", []ready.Item{item(1, ready.StatusVerified), item(2, ready.StatusVerified), item(3, ready.StatusVerified)}, "ok", "ok", "ok"},
		{"none verified", []ready.Item{item(1, ready.StatusMissing), item(2, ready.StatusStale), item(3, ready.StatusFailed)}, "missing", "missing", "missing"},
		{"some verified", []ready.Item{item(1, ready.StatusVerified), item(1, ready.StatusMissing)}, "partial", "ok", "not_planned"},
		{"orphans are not planned", []ready.Item{item(3, ready.StatusOrphan)}, "ok", "ok", "not_planned"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a, b, c := ready.Tiers(ready.Report{Items: tt.items})
			if a != tt.t1 || b != tt.t2 || c != tt.t3 {
				t.Errorf("tiers = %s %s %s, want %s %s %s", a, b, c, tt.t1, tt.t2, tt.t3)
			}
		})
	}
}
