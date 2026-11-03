package adapt

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/api"
	"github.com/marinoscar/local-ai/agent/internal/ark"
	"github.com/marinoscar/local-ai/agent/internal/audit"
	"github.com/marinoscar/local-ai/agent/internal/catalog"
	"github.com/marinoscar/local-ai/agent/internal/cloud/jobs"
	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/modelsapi"
	"github.com/marinoscar/local-ai/agent/internal/sync/item"
	"github.com/marinoscar/local-ai/agent/internal/sync/journal"
	"github.com/marinoscar/local-ai/agent/internal/sync/manifest"
	"github.com/marinoscar/local-ai/agent/internal/sync/plan"
	"github.com/marinoscar/local-ai/agent/internal/sync/retention"
	"github.com/marinoscar/local-ai/agent/internal/sync/run"
)

var bg = context.Background()

var epoch = time.Date(2026, 11, 2, 18, 4, 11, 0, time.UTC)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

const driveID = "ark-0a0b0c0d"

// ---- fakes of the engine's dependencies -----------------------------------------------------------------

type planner struct {
	mu    sync.Mutex
	roots []ark.Root
	plans []plan.RootPlan
	reqs  []run.PlanRequest
}

func (p *planner) Roots() []ark.Root { return p.roots }

func (p *planner) Plan(_ context.Context, req run.PlanRequest) (run.Plans, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.reqs = append(p.reqs, req)
	out := run.Plans{CatalogVersion: "v", MinFree: map[string]int64{}, Item: func(id string) (plan.CatalogItem, bool) {
		return plan.CatalogItem{ID: id, Kind: "model", Engine: "llama"}, true
	}}
	out.Plans = append(out.Plans, p.plans...)
	return out, nil
}

type machine struct {
	block chan struct{}
	// started gets a value when an item starts running (the plan is made by then).
	started chan struct{}
}

func (m *machine) Run(ctx context.Context, _ *journal.Run, job item.Job) item.Result {
	if m.started != nil {
		select {
		case m.started <- struct{}{}:
		default:
		}
	}
	if m.block != nil {
		select {
		case <-m.block:
		case <-ctx.Done():
			return item.Result{Root: job.Root.ID, Item: job.Ref.ID, State: journal.StateDownloading, Cancelled: true}
		}
	}
	return item.Result{Root: job.Root.ID, Item: job.Ref.ID, Version: "v1", State: journal.StateVerified}
}

func (m *machine) Recover(context.Context, *journal.Run, journal.Intent) (item.Result, bool) {
	return item.Result{}, false
}

type noCopies struct{}

func (noCopies) Copies(string) []ark.Copy { return nil }

type rig struct {
	t    *testing.T
	eng  *run.Engine
	pl   *planner
	mach *machine
	net  run.NetState
}

func newRig(t *testing.T) *rig {
	t.Helper()
	dir := t.TempDir()
	r := &rig{t: t, mach: &machine{}, net: run.NetState{Mode: plan.ModeOnline, Online: true}}
	internal := ark.Root{ID: ark.InternalID, Kind: ark.KindInternal, Path: filepath.Join(dir, "srv"), State: ark.StateReady}
	_ = os.MkdirAll(internal.Path, 0o755)
	row := plan.ItemPlan{ID: "m1", Kind: "model", Action: plan.ActionDownload, ToVersion: "v1", NeedBytes: 100, Source: plan.SourceNetwork}
	budget := plan.ItemPlan{ID: "big", Kind: "model", Action: plan.ActionSkip, Reason: plan.ReasonBudget}
	r.pl = &planner{roots: []ark.Root{internal}, plans: []plan.RootPlan{{Root: ark.InternalID, Items: []plan.ItemPlan{row, budget}, MinFreeBytes: 1}}}
	jr, _, err := journal.Open(bg, journal.Options{Dir: filepath.Join(dir, "var"), Logger: quiet})
	if err != nil {
		t.Fatal(err)
	}
	eng, err := run.New(run.Deps{
		Journal: jr, Machine: r.mach, Planner: r.pl, Copies: noCopies{}, FS: config.OSFS{}, JournalDir: filepath.Join(dir, "var"),
		Settings: run.DefaultSettings, Net: func() run.NetState { return r.net }, Logger: quiet,
		Priority: func(bool) error { return nil }, Lock: run.FileLock{Path: filepath.Join(dir, "lock")},
	})
	if err != nil {
		t.Fatal(err)
	}
	r.eng = eng
	return r
}

func (r *rig) wait(id string) run.RunStatus {
	r.t.Helper()
	if err := r.eng.Wait(bg, id); err != nil {
		r.t.Fatal(err)
	}
	st, _ := r.eng.Status()
	return st
}

func apiErr(t *testing.T, err error, status int, code string) *api.Error {
	t.Helper()
	var e *api.Error
	if !errors.As(err, &e) || e.Status != status || e.Code != code {
		t.Fatalf("err = %v, want %d %s", err, status, code)
	}
	return e
}

// ---- the route adapters ---------------------------------------------------------------------------------

func TestStarterMapsTheEnginesErrorsToTheRoutesAnswers(t *testing.T) {
	r := newRig(t)
	r.mach.block = make(chan struct{})
	s := Starter{Engine: r.eng}

	got, err := s.Start(bg, api.ArkSyncRequest{Trigger: api.ArkTriggerManual, OpID: "op_7k2m9q4r8t1c"})
	if err != nil || !journal.ValidRunID(got.RunID) || got.State != run.StateRunning {
		t.Fatalf("Start = %+v, %v", got, err)
	}
	e := apiErr(t, second(s, api.ArkSyncRequest{Trigger: api.ArkTriggerManual}), http.StatusConflict, doctor.SYN005)
	if e.Details["run_id"] != got.RunID {
		t.Fatalf("details = %v", e.Details)
	}
	close(r.mach.block)
	r.wait(got.RunID)

	r.net = run.NetState{Mode: plan.ModeSurvival}
	e = apiErr(t, second(s, api.ArkSyncRequest{Trigger: api.ArkTriggerManual}), http.StatusConflict, doctor.SYN010)
	if e.Details["mode"] != plan.ModeSurvival {
		t.Fatalf("details = %v", e.Details)
	}
	r.net = run.NetState{Mode: plan.ModeOffline, Online: true}
	apiErr(t, second(s, api.ArkSyncRequest{Trigger: api.ArkTriggerManual}), http.StatusConflict, doctor.SYN011)

	r.net = run.NetState{Mode: plan.ModeOnline, Online: true}
	apiErr(t, second(s, api.ArkSyncRequest{Trigger: api.ArkTriggerManual, Roots: []string{"ark-ffffffff"}}), http.StatusBadRequest, api.CodeInvalidRequest)
	r.pl.roots = nil
	apiErr(t, second(s, api.ArkSyncRequest{Trigger: api.ArkTriggerManual}), http.StatusConflict, doctor.ARK001)
}

func second(s Starter, req api.ArkSyncRequest) error {
	_, err := s.Start(bg, req)
	return err
}

func TestStarterReportsASkippedScheduledRunAndTheCloudTrigger(t *testing.T) {
	r := newRig(t)
	r.net = run.NetState{Mode: plan.ModeSurvival}
	got, err := Starter{Engine: r.eng}.Start(bg, api.ArkSyncRequest{Trigger: api.ArkTriggerScheduled})
	if err != nil || got.State != run.StateSkipped || got.Reason != run.SkipMode || got.Code != doctor.SYN002 {
		t.Fatalf("Start = %+v, %v", got, err)
	}
	// A cloud actor's manual run is a run of trigger cloud.
	r.net = run.NetState{Mode: plan.ModeOnline, Online: true}
	got, err = Starter{Engine: r.eng}.Start(bg, api.ArkSyncRequest{Trigger: api.ArkTriggerManual, Actor: audit.Cloud("job-1")})
	if err != nil {
		t.Fatal(err)
	}
	if st := r.wait(got.RunID); st.Trigger != run.TriggerCloud {
		t.Fatalf("trigger = %s", st.Trigger)
	}
}

func TestControllerServesGetAndDelete(t *testing.T) {
	r := newRig(t)
	c := Controller{Engine: r.eng}
	if _, ok := c.Current(bg); ok {
		t.Fatal("a status before any run")
	}
	if res, err := c.Cancel(bg, api.ArkSyncCancelRequest{}); err != nil || res.Active || res.RunID != "" {
		t.Fatalf("Cancel with no run = %+v, %v", res, err)
	}
	r.mach.block = make(chan struct{})
	got, _ := Starter{Engine: r.eng}.Start(bg, api.ArkSyncRequest{Trigger: api.ArkTriggerManual})
	body, ok := c.Current(bg)
	st, isStatus := body.(run.RunStatus)
	if !ok || !isStatus || st.RunID != got.RunID || st.State != run.StateRunning || st.Items != nil {
		t.Fatalf("Current = %+v, %v", body, ok)
	}
	if res, err := c.Cancel(bg, api.ArkSyncCancelRequest{}); err != nil || !res.Active || res.RunID != got.RunID {
		t.Fatalf("Cancel = %+v, %v", res, err)
	}
	r.wait(got.RunID)
	body, _ = c.Current(bg)
	// An ended run carries what became of each item (ark-sync.md section 3.5); how many items had been
	// decided by the time the cancel landed depends on scheduling, so only the state is asserted here.
	if st = body.(run.RunStatus); st.State != run.StateCancelled {
		t.Fatalf("after cancel: %+v", st)
	}
	if res, _ := c.Cancel(bg, api.ArkSyncCancelRequest{}); res.Active || res.RunID != got.RunID {
		t.Fatalf("second Cancel = %+v", res)
	}
}

// ---- the model routes ------------------------------------------------------------------------------------

func TestModelsStartDownload(t *testing.T) {
	r := newRig(t)
	r.mach.block = make(chan struct{})
	m := Models{Engine: r.eng}
	model := &catalog.Model{ID: "m1"}

	got, err := m.StartModelDownload(bg, modelsapi.SyncDownload{Model: model, Actor: audit.Cloud("job-9")})
	if err != nil || !journal.ValidRunID(got.ID) {
		t.Fatalf("StartModelDownload = %+v, %v", got, err)
	}
	var active *modelsapi.RunActiveError
	if _, err := m.StartModelDownload(bg, modelsapi.SyncDownload{Model: model}); !errors.As(err, &active) || active.RunID != got.ID {
		t.Fatalf("second download: %v", err)
	}
	close(r.mach.block)
	st := r.wait(got.ID)
	if st.Trigger != run.TriggerCloud || st.Totals.Items != 2 && st.Totals.Done != 1 {
		t.Fatalf("status = %+v", st)
	}
	var unknown *modelsapi.UnknownRootError
	if _, err := m.StartModelDownload(bg, modelsapi.SyncDownload{Model: model, Roots: []string{"ark-ffffffff"}}); !errors.As(err, &unknown) || unknown.Root != "ark-ffffffff" {
		t.Fatalf("unknown root: %v", err)
	}
	r.pl.roots = nil
	if _, err := m.StartModelDownload(bg, modelsapi.SyncDownload{Model: model}); !errors.Is(err, modelsapi.ErrNoWritableRoot) {
		t.Fatalf("no root: %v", err)
	}
}

// install puts a verified model on a root.
func install(t *testing.T, root ark.Root, id string, verified time.Time) {
	t.Helper()
	dir := filepath.Join(root.Path, "models", id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := []byte("weights " + id)
	if err := os.WriteFile(filepath.Join(dir, "w.gguf"), body, 0o644); err != nil {
		t.Fatal(err)
	}
	m := manifest.Manifest{
		SchemaVersion: 1, ItemID: id, ItemKind: "model", Engine: "llama", Version: "rev1", SortKey: "rev1", State: "verified",
		Source: manifest.Source{Type: "huggingface"}, Trust: manifest.TrustPinned,
		Files:      []manifest.File{{Kind: "weights", Path: "w.gguf", SizeBytes: int64(len(body)), SHA256: strings.Repeat("0", 64)}},
		TotalBytes: int64(len(body)), DownloadedAt: verified, VerifiedAt: verified, LastReverifiedAt: verified,
	}
	if err := manifest.Write(config.OSFS{}, manifest.Path(dir), m); err != nil {
		t.Fatal(err)
	}
}

func TestModelsDeleteFollowsRetentionPerRoot(t *testing.T) {
	r := newRig(t)
	dir := t.TempDir()
	internal := r.pl.roots[0]
	drive := ark.Root{ID: driveID, Kind: ark.KindArkDrive, Path: filepath.Join(dir, "localai"), Mount: dir, State: ark.StateReady, MountID: 4}
	_ = os.MkdirAll(drive.Path, 0o755)
	r.pl.roots = []ark.Root{internal, drive}
	now := time.Now()
	install(t, internal, "m1", now)
	install(t, drive, "m1", now)
	install(t, internal, "only-here", now)

	idx := ark.NewIndex(config.OSFS{})
	if _, err := idx.Rebuild(bg, r.pl.roots); err != nil {
		t.Fatal(err)
	}
	jr, _, _ := journal.Open(bg, journal.Options{Dir: filepath.Join(dir, "j"), Logger: quiet})
	ret, err := retention.New(retention.Deps{FS: retention.OSFS{}, Roots: func() []ark.Root { return r.pl.roots }, Copies: idx, Journal: jr.Items, Index: idx, Items: jr.Items, Logger: quiet})
	if err != nil {
		t.Fatal(err)
	}
	m := Models{Engine: r.eng, Retention: ret}

	t.Run("a model on two roots, keep_last: the first root loses its copy, the last stays", func(t *testing.T) {
		res, err := m.DeleteModel(bg, modelsapi.SyncDelete{Model: &catalog.Model{ID: "m1"}, KeepLast: true})
		if err != nil {
			t.Fatal(err)
		}
		// Roots go one at a time in mounted order: internal is deleted (the drive's copy survives it),
		// then the drive's copy is the last one and is kept.
		if len(res.Deleted) != 1 || res.Deleted[0].Root != ark.InternalID || res.Deleted[0].Bytes == 0 {
			t.Fatalf("deleted = %+v", res.Deleted)
		}
		if len(res.Kept) != 1 || res.Kept[0] != (modelsapi.KeptCopy{Root: driveID, Reason: modelsapi.KeptLastVerifiedCopy}) {
			t.Fatalf("kept = %+v", res.Kept)
		}
	})
	t.Run("a named root with no copy is reported not_present", func(t *testing.T) {
		res, err := m.DeleteModel(bg, modelsapi.SyncDelete{Model: &catalog.Model{ID: "only-here"}, Roots: []string{drive.Path}, KeepLast: true})
		if err != nil || len(res.Deleted) != 0 || len(res.Kept) != 1 || res.Kept[0] != (modelsapi.KeptCopy{Root: driveID, Reason: modelsapi.KeptNotPresent}) {
			t.Fatalf("%+v, %v", res, err)
		}
	})
	t.Run("without keep_last the last copy goes", func(t *testing.T) {
		res, err := m.DeleteModel(bg, modelsapi.SyncDelete{Model: &catalog.Model{ID: "only-here"}, Roots: []string{"internal"}})
		if err != nil || len(res.Deleted) != 1 || len(res.Kept) != 0 {
			t.Fatalf("%+v, %v", res, err)
		}
	})
	t.Run("an unknown root", func(t *testing.T) {
		var unknown *modelsapi.UnknownRootError
		if _, err := m.DeleteModel(bg, modelsapi.SyncDelete{Model: &catalog.Model{ID: "m1"}, Roots: []string{"/nowhere"}}); !errors.As(err, &unknown) {
			t.Fatalf("err = %v", err)
		}
	})
}

// ---- the cloud ----------------------------------------------------------------------------------------

type verifier struct{ got jobs.ArkRequest }

func (v *verifier) StartVerify(_ context.Context, req jobs.ArkRequest) (jobs.ArkVerifyStarted, error) {
	v.got = req
	return jobs.ArkVerifyStarted{RunID: "run-20261102T031500Z-a1f3", ItemsPlanned: 4}, nil
}

func TestJobsArk(t *testing.T) {
	r := newRig(t)
	j := Jobs{Engine: r.eng}

	t.Run("a dry run answers the plan and starts nothing", func(t *testing.T) {
		got, err := j.StartSync(bg, jobs.ArkRequest{DryRun: true})
		if err != nil || got.RunID != "" || got.ItemsToDownload != 1 || got.BytesToDownload != 100 || got.ItemsSkippedBudget != 1 {
			t.Fatalf("%+v, %v", got, err)
		}
		if r.eng.RunActive() {
			t.Fatal("a dry run took the slot")
		}
	})
	t.Run("a real start is a run of trigger cloud", func(t *testing.T) {
		got, err := j.StartSync(bg, jobs.ArkRequest{Actor: audit.Cloud("job-3")})
		if err != nil || !journal.ValidRunID(got.RunID) || got.ItemsToDownload != 1 {
			t.Fatalf("%+v, %v", got, err)
		}
		if st := r.wait(got.RunID); st.Trigger != run.TriggerCloud {
			t.Fatalf("trigger = %s", st.Trigger)
		}
	})
	t.Run("a run already active is the executor's RunActiveError", func(t *testing.T) {
		r.mach.block = make(chan struct{})
		first, _ := j.StartSync(bg, jobs.ArkRequest{})
		_, err := j.StartSync(bg, jobs.ArkRequest{})
		var active *modelsapi.RunActiveError
		if !errors.As(err, &active) || active.RunID != first.RunID {
			t.Fatalf("err = %v", err)
		}
		close(r.mach.block)
		r.wait(first.RunID)
	})
	t.Run("ark.verify without the re-verification is unavailable, with it the verifier answers", func(t *testing.T) {
		if _, err := j.StartVerify(bg, jobs.ArkRequest{}); !errors.Is(err, ErrVerifyUnavailable) {
			t.Fatalf("err = %v", err)
		}
		v := &verifier{}
		got, err := Jobs{Engine: r.eng, Verifier: v}.StartVerify(bg, jobs.ArkRequest{Items: []string{"m1"}})
		if err != nil || got.ItemsPlanned != 4 || len(v.got.Items) != 1 {
			t.Fatalf("%+v, %v", got, err)
		}
	})
	t.Run("mode refusals are the route's errors", func(t *testing.T) {
		r.net = run.NetState{Mode: plan.ModeSurvival}
		_, err := j.StartSync(bg, jobs.ArkRequest{})
		apiErr(t, err, http.StatusConflict, doctor.SYN010)
		// A dry run plans in survival mode: it needs no network.
		if _, err := j.StartSync(bg, jobs.ArkRequest{DryRun: true}); err != nil {
			t.Fatalf("dry run in survival: %v", err)
		}
	})
}

func TestCloudSyncReport(t *testing.T) {
	r := newRig(t)
	c := CloudSync{Engine: r.eng}
	if rep := c.Sync(); rep.Running || rep.Last != nil {
		t.Fatalf("before any run: %+v", rep)
	}
	r.mach.block = make(chan struct{})
	r.mach.started = make(chan struct{}, 1)
	got, _ := Starter{Engine: r.eng}.Start(bg, api.ArkSyncRequest{Trigger: api.ArkTriggerManual})
	<-r.mach.started
	rep := c.Sync()
	if !rep.Running || rep.RunID != got.RunID || rep.ProgressPct == nil || *rep.ProgressPct != 0 || rep.Last != nil {
		t.Fatalf("running: %+v", rep)
	}
	close(r.mach.block)
	r.wait(got.RunID)
	rep = c.Sync()
	if rep.Running || rep.Last == nil || rep.Last.ID != got.RunID || rep.Last.State != run.StateDone || rep.Last.LeftItems != 1 || rep.Last.FailedItems != 0 {
		t.Fatalf("after: %+v", rep.Last)
	}
	// The planner left one item out for the budget: the run is "partial" for the cloud.
	if rep.Last.FinishedAt.IsZero() {
		t.Fatal("finished_at is zero")
	}
	// A skipped run carries its reason.
	r.net = run.NetState{Mode: plan.ModeSurvival}
	sk, _ := Starter{Engine: r.eng}.Start(bg, api.ArkSyncRequest{Trigger: api.ArkTriggerScheduled})
	rep = c.Sync()
	if rep.Last == nil || rep.Last.ID != sk.RunID || rep.Last.State != run.StateSkipped || rep.Last.SkipReason != run.SkipMode {
		t.Fatalf("skipped: %+v", rep.Last)
	}
}

// ---- the planner adapter -----------------------------------------------------------------------------

type fakeIndex struct{}

func (fakeIndex) ItemIDs() []string        { return nil }
func (fakeIndex) Copies(string) []ark.Copy { return nil }

type fakeItems struct{}

func (fakeItems) List() []journal.Item { return nil }

func TestGathererPlanner(t *testing.T) {
	dir := t.TempDir()
	internal := ark.Root{ID: ark.InternalID, Kind: ark.KindInternal, Path: dir, State: ark.StateReady}
	models := []catalog.Model{{
		ID: "m1", Engine: "llama", SurvivalTier: 1, Source: catalog.ModelSource{Type: "huggingface", Revision: "0123456789abcdef0123456789abcdef01234567"},
		Files: []catalog.ModelFile{{Kind: "weights", Name: "m1.gguf", SizeBytes: 10, SHA256: strings.Repeat("1", 64)}},
	}}
	content := []catalog.ContentItem{{ID: "docs", Kind: "docs", ExpectedSizeBytes: 1, Source: catalog.ContentSource{Type: "repo_docs"}}}
	prof := catalog.Profile{ID: "p", Storage: catalog.Storage{Target: "internal", BudgetBytes: 1000, MinFreeBytes: 7}, Include: catalog.Include{Models: []string{"m1"}, Content: []string{"docs"}}}
	cat := catalog.New("2026.11.02-1", models, content, []catalog.Profile{prof})
	g, err := plan.NewGatherer(plan.Deps{
		FS: config.OSFS{}, Catalog: func() *catalog.Catalog { return cat }, Settings: func() plan.NodeSettings { return plan.NodeSettings{Profile: "p"} },
		Roots: func() []ark.Root { return []ark.Root{internal} }, Copies: fakeIndex{}, Journal: fakeItems{},
		Statfs: func(string) (int64, int64, error) { return 1 << 40, 1 << 39, nil }, Node: func() plan.NodeInfo { return plan.NodeInfo{} },
		// The gatherer thinks the node is offline (mode offline); a run a person allowed asks for online.
		Network: func() plan.Network { return plan.Network{Mode: plan.ModeOffline, Online: true} },
		Now:     func() time.Time { return epoch },
	})
	if err != nil {
		t.Fatal(err)
	}
	p := GathererPlanner{G: g}
	if got := p.Roots(); len(got) != 1 || got[0].ID != ark.InternalID {
		t.Fatalf("roots = %+v", got)
	}
	plans, err := p.Plan(bg, run.PlanRequest{RootIDs: []string{ark.InternalID}, Offline: false})
	if err != nil {
		t.Fatal(err)
	}
	if len(plans.Plans) != 1 || plans.CatalogVersion != "2026.11.02-1" || plans.MinFree[ark.InternalID] != 7 {
		t.Fatalf("plans = %+v", plans)
	}
	var m1 plan.ItemPlan
	for _, it := range plans.Plans[0].Items {
		if it.ID == "m1" {
			m1 = it
		}
	}
	if m1.Action != plan.ActionDownload || m1.ToVersion != "0123456789ab" || m1.Source != plan.SourceNetwork {
		t.Fatalf("m1 = %+v (the online override must let it download in offline mode)", m1)
	}
	if it, ok := plans.Item("m1"); !ok || it.Model == nil || it.Model.ID != "m1" {
		t.Fatalf("Item(m1) = %+v, %v", it, ok)
	}
	if it, ok := plans.Item("docs"); !ok || it.Content == nil {
		t.Fatalf("Item(docs) = %+v, %v", it, ok)
	}
	if _, ok := plans.Item("ghost"); ok {
		t.Fatal("an unknown item resolved")
	}
	// Offline, the same row is unresolved only when the catalog does not pin it; this one is pinned.
	if _, err := p.Plan(bg, run.PlanRequest{RootIDs: []string{ark.InternalID}, Offline: true}); err != nil {
		t.Fatal(err)
	}
	// An unknown root is the gatherer's typed error.
	var unknown *plan.ErrUnknownRoot
	if _, err := p.Plan(bg, run.PlanRequest{RootIDs: []string{"ark-ffffffff"}}); !errors.As(err, &unknown) {
		t.Fatalf("err = %v", err)
	}
}
