package run

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/ark"
	"github.com/marinoscar/local-ai/agent/internal/catalog"
	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/sync/item"
	"github.com/marinoscar/local-ai/agent/internal/sync/journal"
	"github.com/marinoscar/local-ai/agent/internal/sync/manifest"
	"github.com/marinoscar/local-ai/agent/internal/sync/plan"
)

func driveRoot(r *rig, id string, state ark.RootState, readOnly bool) {
	r.addRoot(ark.Root{ID: id, Kind: ark.KindArkDrive, Path: filepath.Join(r.dir, id, "localai"), Mount: filepath.Join(r.dir, id), State: state, ReadOnly: readOnly, MountID: 5})
}

// A verify run plans nothing: its Verify hook gets the roots that can be read (a read-only drive
// included), the items and the flag, and what it records is the run's result.
func TestAVerifyRunIsAllowedInEveryModeAndReportsWhatTheHookRecords(t *testing.T) {
	for _, mode := range []string{plan.ModeOnline, plan.ModeOffline, plan.ModeSurvival} {
		t.Run(mode, func(t *testing.T) {
			r := newRig(t, func(r *rig) { r.net = NetState{Mode: mode, Online: mode == plan.ModeOnline} })
			driveRoot(r, driveID, ark.StateReadOnly, true)
			driveRoot(r, driveB, ark.StateEjecting, false)
			var got VerifyRequest
			var rcRoots []string
			var ready [][]string
			r.deps.Ready = readyFunc(func(ids []string) map[string]string {
				ready = append(ready, append([]string(nil), ids...))
				return map[string]string{}
			})
			r.deps.Hooks.Verify = func(ctx context.Context, rc RunContext, req VerifyRequest) {
				got, rcRoots = req, rc.Roots
				rc.Begin(3, 3000)
				rc.Record(ItemResult{Root: "internal", Item: "ok-1", Result: "verified", Version: "v1"}, 1000)
				rc.Record(ItemResult{Root: driveID, Item: "rotten", Result: "quarantined", Version: "v1", Reason: "bit_rot", Code: doctor.SYN025}, 1000)
				rc.Record(ItemResult{Root: driveID, Item: "late", Result: "skipped", Version: "v1", Reason: "time_limit"}, 0)
				rc.Record(ItemResult{Item: "nowhere", Result: "skipped", Reason: "not_present"}, 0)
			}
			eng := r.start()
			id := r.startRun(StartRequest{Trigger: TriggerVerify, Items: []string{"rotten", "ok-1"}, Full: true})
			st := r.waitEnd(id)

			if !reflect.DeepEqual(got.Roots, []string{"internal", driveID}) || !got.Full || !reflect.DeepEqual(got.Items, []string{"ok-1", "rotten"}) {
				t.Fatalf("hook got %+v", got)
			}
			if !reflect.DeepEqual(rcRoots, got.Roots) {
				t.Fatalf("context roots = %v", rcRoots)
			}
			if st.Trigger != TriggerVerify || st.State != StateDone || st.Result != ResultOK && st.Result != ResultPartial {
				t.Fatalf("status = %+v", st)
			}
			if st.Totals.Items != 4 || st.Totals.Done != 1 || st.Totals.Failed != 1 || st.Totals.Skipped != 2 || st.Totals.BytesTotal != 3000 || st.Totals.BytesDone != 2000 {
				t.Fatalf("totals = %+v", st.Totals)
			}
			if len(st.Items) != 4 || st.Items[1].Code != doctor.SYN025 || st.Items[1].Result != "quarantined" {
				t.Fatalf("items = %+v", st.Items)
			}
			// The left item makes the result partial (exit 8), and the damaged one is in the events.
			if st.Result != ResultPartial {
				t.Fatalf("result = %s", st.Result)
			}
			done := r.evs.itemDone()
			if len(done) != 4 || done[1].Code != doctor.SYN025 {
				t.Fatalf("item_done = %+v", done)
			}
			// READY is regenerated for the roots that were checked, the ejecting drive was left out.
			if len(ready) == 0 || !reflect.DeepEqual(ready[len(ready)-1], []string{"internal", driveID}) {
				t.Fatalf("READY refreshed for %v", ready)
			}
			// A verify run is the last run that touched the root, but it is not a sync: it does not move
			// the clock of the age rules.
			rr, ok := eng.RootRun("internal")
			if !ok || rr.Trigger != TriggerVerify || !rr.LastDone.IsZero() {
				t.Fatalf("root run = %+v", rr)
			}
		})
	}
}

func TestAVerifyRunNeedsItsVerifierAndTheSlot(t *testing.T) {
	r := newRig(t)
	r.start()
	id := r.startRun(StartRequest{Trigger: TriggerVerify})
	if st := r.waitEnd(id); st.State != StateFailed || st.Code != doctor.AGT099 || st.Result != ResultFailed {
		t.Fatalf("a verify run with no verifier must fail loudly: %+v", st)
	}

	// The slot is shared with sync: while a verify run is checking, a sync is refused.
	block := make(chan struct{})
	r.deps.Hooks.Verify = func(ctx context.Context, rc RunContext, req VerifyRequest) {
		select {
		case <-block:
		case <-ctx.Done():
		}
	}
	r2 := newRig(t)
	r2.deps.Hooks = r.deps.Hooks
	r2.start()
	first := r2.startRun(StartRequest{Trigger: TriggerVerify})
	var busy *RunActiveError
	if _, err := r2.eng.Start(bg, StartRequest{Trigger: TriggerManual}); !errors.As(err, &busy) || busy.RunID != first {
		t.Fatalf("a sync during a verify: %v", err)
	}
	if _, err := r2.eng.Start(bg, StartRequest{Trigger: TriggerImport, Import: &ImportSpec{From: driveID}}); !errors.As(err, &busy) {
		t.Fatalf("an import during a verify: %v", err)
	}
	close(block)
	r2.waitEnd(first)
}

func TestAVerifyRunWithNoReadableRootIsRefused(t *testing.T) {
	r := newRig(t, func(r *rig) {
		root := r.roots[ark.InternalID]
		root.State = ark.StateUnusable
		r.roots[ark.InternalID] = root
		r.plnr.roots = []ark.Root{root}
	})
	r.start()
	if _, err := r.eng.Start(bg, StartRequest{Trigger: TriggerVerify}); !errors.As(err, new(NoRootsError)) {
		t.Fatalf("err = %v", err)
	}
	var unknown *UnknownRootError
	if _, err := r.eng.Start(bg, StartRequest{Trigger: TriggerVerify, Roots: []string{"ark-ffffffff"}}); !errors.As(err, &unknown) {
		t.Fatalf("err = %v", err)
	}
}

// At the end of a scheduled sync the Reverify hook runs; only the copies it found damaged become part
// of the run (a nightly run does not list every file it re-checked).
func TestTheScheduledReverifySliceReportsOnlyWhatIsWrong(t *testing.T) {
	r := newRig(t)
	r.setPlans(map[string][]plan.ItemPlan{ark.InternalID: {row("m1", 100)}})
	var gotBudget time.Duration
	r.deps.Hooks.Reverify = func(ctx context.Context, rc RunContext, budget time.Duration) {
		gotBudget = budget
		rc.Begin(2, 2000) // only a verify run counts what it is about to read
		rc.Record(ItemResult{Root: "internal", Item: "fine", Result: "verified", Version: "v1"}, 1000)
		rc.Record(ItemResult{Root: "internal", Item: "busy", Result: "skipped", Reason: "busy"}, 0)
		rc.Record(ItemResult{Root: "internal", Item: "rotten", Result: "quarantined", Version: "v1", Reason: "bit_rot", Code: doctor.SYN025}, 1000)
	}
	r.start()
	id := r.startRun(StartRequest{Trigger: TriggerScheduled})
	st := r.waitEnd(id)
	if gotBudget != 90*time.Minute {
		t.Fatalf("budget = %v", gotBudget)
	}
	if st.Totals.Items != 2 || st.Totals.Done != 1 || st.Totals.Failed != 1 || st.Totals.BytesTotal != 100 {
		t.Fatalf("totals = %+v", st.Totals)
	}
	var seen []string
	for _, it := range st.Items {
		seen = append(seen, it.Item+":"+it.Result)
	}
	sort.Strings(seen)
	if !reflect.DeepEqual(seen, []string{"m1:verified", "rotten:quarantined"}) {
		t.Fatalf("items = %v", seen)
	}
	if st.Result != ResultPartial {
		t.Fatalf("a damaged copy must show as a warning (exit 8), result = %s", st.Result)
	}
}

// An import is planned for the internal root with the spec, in survival mode too, uses its own machine,
// refuses a row that does not copy from the drive, and tells the wiring when the drive's copy is bad.
func TestAnImportRunPlansTheInternalRootFromTheDriveAndNeverTouchesTheNetwork(t *testing.T) {
	for _, mode := range []string{plan.ModeOnline, plan.ModeOffline, plan.ModeSurvival} {
		t.Run(mode, func(t *testing.T) {
			r := newRig(t, func(r *rig) { r.net = NetState{Mode: mode, Online: false} })
			driveRoot(r, driveID, ark.StateReady, false)
			row1 := row("m1", 100)
			row1.Source, row1.FromRoot = plan.SourceLocalCopy, driveID
			row2 := row("m2", 100)
			row2.Source, row2.FromRoot = plan.SourceLocalCopy, driveID
			row3 := plan.ItemPlan{ID: "m3", Kind: "model", Action: plan.ActionSkip, Reason: plan.ReasonNotInProfile}
			r.setPlans(map[string][]plan.ItemPlan{ark.InternalID: {row1, row2, row3}})

			network := newFakeMachine() // must never be used
			imp := newFakeMachine()
			imp.beh = func(ctx context.Context, job item.Job) item.Result {
				if job.Ref.ID == "m2" {
					return item.Result{Root: job.Root.ID, Item: "m2", Version: "v1", State: journal.StateFailed, Code: doctor.SYN070, Reason: "permanent"}
				}
				return verified(job, "v1")
			}
			r.deps.Machine, r.deps.ImportMachine = network, imp
			var faults []string
			r.deps.SourceFault = func(root, item, version string) { faults = append(faults, root+"/"+item+"/"+version) }
			r.start()

			id := r.startRun(StartRequest{Trigger: TriggerImport, Roots: []string{driveID}, Items: []string{"m1", "m2", "m3"},
				Import: &ImportSpec{From: driveID, AddToProfile: true, Catalog: true}})
			st := r.waitEnd(id)

			req := r.plnr.lastRequest()
			if !reflect.DeepEqual(req.RootIDs, []string{ark.InternalID}) || req.Import == nil || req.Import.From != driveID || !req.Import.AddToProfile || !req.Import.Catalog || !req.Offline {
				t.Fatalf("plan request = %+v", req)
			}
			if !reflect.DeepEqual(req.Import.Items, []string{"m1", "m2", "m3"}) {
				t.Fatalf("import items = %v", req.Import.Items)
			}
			if len(network.order()) != 0 {
				t.Fatalf("the sync machine ran an import: %v", network.order())
			}
			if got := imp.order(); !reflect.DeepEqual(sorted(got), []string{"internal/m1", "internal/m2"}) {
				t.Fatalf("import machine ran %v", got)
			}
			if st.Trigger != TriggerImport || st.State != StateDone || !reflect.DeepEqual(st.Roots, []string{"internal"}) {
				t.Fatalf("status = %+v", st)
			}
			// m1 copied, m2's drive copy was damaged (LAI-SYN-070), m3 was refused as not in the profile.
			byItem := map[string]ItemResult{}
			for _, it := range st.Items {
				byItem[it.Item] = it
			}
			if byItem["m1"].Result != "verified" || byItem["m2"].Code != doctor.SYN070 || byItem["m3"].Result != "skipped" || byItem["m3"].Reason != string(plan.ReasonNotInProfile) {
				t.Fatalf("items = %+v", st.Items)
			}
			if !reflect.DeepEqual(faults, []string{driveID + "/m2/v1"}) {
				t.Fatalf("source faults = %v", faults)
			}
			// An import confirms nothing is current: no checked_current_at on what it copied.
			if rec, ok := r.jr.Items.Get(ark.InternalID, "m1"); ok && len(rec.Extras[ExtraCheckedCurrentAt]) != 0 {
				t.Fatalf("an import stamped checked_current_at: %+v", rec.Extras)
			}
			// It does count as the last run that ended done on the internal root.
			if rr, _ := r.eng.RootRun("internal"); rr.Trigger != TriggerImport || rr.LastDone.IsZero() && st.Totals.Failed == 0 {
				t.Fatalf("root run = %+v", rr)
			}
		})
	}
}

func sorted(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

func TestAnImportNeedsItsSourceAndAWritableInternalRoot(t *testing.T) {
	r := newRig(t)
	r.start()
	if _, err := r.eng.Start(bg, StartRequest{Trigger: TriggerImport}); err == nil || !strings.Contains(err.Error(), "source") {
		t.Fatalf("err = %v", err)
	}

	ro := newRig(t, func(r *rig) {
		root := r.roots[ark.InternalID]
		root.ReadOnly = true
		r.roots[ark.InternalID] = root
		r.plnr.roots = []ark.Root{root}
	})
	ro.start()
	if _, err := ro.eng.Start(bg, StartRequest{Trigger: TriggerImport, Import: &ImportSpec{From: driveID}}); !errors.As(err, new(NoRootsError)) {
		t.Fatalf("a read-only internal root cannot be imported into: %v", err)
	}
}

func TestAPlanWarningReachesTheRunAndAFailedPlanFailsIt(t *testing.T) {
	r := newRig(t)
	driveRoot(r, driveID, ark.StateReady, false)
	r.setPlans(map[string][]plan.ItemPlan{ark.InternalID: nil})
	r.plnr.warnings = []string{doctor.CAT002 + ": the catalog on the drive is not valid, so the catalog this computer has is used"}
	r.start()
	id := r.startRun(StartRequest{Trigger: TriggerImport, Import: &ImportSpec{From: driveID, Catalog: true}})
	st := r.waitEnd(id)
	if len(st.Warnings) != 1 || !strings.HasPrefix(st.Warnings[0], doctor.CAT002) {
		t.Fatalf("warnings = %v", st.Warnings)
	}

	r.plnr.warnings = nil
	r.plnr.err = errCoded{doctor.ARK015}
	id = r.startRun(StartRequest{Trigger: TriggerImport, Import: &ImportSpec{From: driveID}})
	if st := r.waitEnd(id); st.State != StateFailed || st.Code != doctor.ARK015 {
		t.Fatalf("status = %+v", st)
	}
}

type errCoded struct{ code string }

func (e errCoded) Error() string { return "coded " + e.code }
func (e errCoded) Code() string  { return e.code }

func TestResolveImportCopiesFromTheDriveAndChecksTheCatalogPin(t *testing.T) {
	dir := t.TempDir()
	good, other := sha([]byte("weights")), sha([]byte("something else"))
	m := manifest.Manifest{
		ItemID: "qwen-x", ItemKind: "model", Engine: "llama", Version: "0123456789ab", SortKey: "2026-11", State: "verified",
		Source: manifest.Source{Type: "huggingface"}, Trust: manifest.TrustPinned,
		Files: []manifest.File{{Kind: "weights", Path: "w.gguf", SizeBytes: 7, SHA256: good, SHA256Source: "catalog"}},
	}
	c := copies{"qwen-x": {{RootID: driveID, Dir: filepath.Join(dir, "drive", "models", "qwen-x"), Manifest: m}}}
	e := &Engine{d: Deps{FS: config.OSFS{}, Copies: c}}
	model := func(pin string) plan.CatalogItem {
		return plan.ModelItem(&catalog.Model{
			ID: "qwen-x", Engine: "llama", Source: catalog.ModelSource{Type: "huggingface", Repo: "org-alpha/qwen-x", Revision: "0123456789abcdef0123456789abcdef01234567"},
			Files: []catalog.ModelFile{{Kind: "weights", Name: "w.gguf", SizeBytes: 7, SHA256: pin}},
		})
	}
	row := plan.ItemPlan{ID: "qwen-x", Source: plan.SourceLocalCopy, FromRoot: driveID, ToVersion: "0123456789ab"}

	t.Run("the drive's checksum agrees with the catalog's pin", func(t *testing.T) {
		res, err := e.resolveImport(&task{row: row, cat: model(good), catalogVersion: "2026.11.02-1"})
		if err != nil || len(res.Files) != 1 || !res.Local || res.Files[0].SHA256 != good || res.Files[0].Source == nil {
			t.Fatalf("%+v, %v", res, err)
		}
	})
	t.Run("the drive holds a file the catalog does not pin: refused, LAI-SYN-021", func(t *testing.T) {
		_, err := e.resolveImport(&task{row: row, cat: model(other), catalogVersion: "2026.11.02-1"})
		var re *item.ResolveError
		if !errors.As(err, &re) || re.Code != doctor.SYN021 || !re.Permanent {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("an item the catalog pins nothing for is trusted on the drive's manifest", func(t *testing.T) {
		unpinned := plan.ModelItem(&catalog.Model{ID: "qwen-x", Engine: "llama", Files: []catalog.ModelFile{{Kind: "weights", Name: "w.gguf"}}})
		res, err := e.resolveImport(&task{row: row, cat: unpinned})
		if err != nil || res.Files[0].SHA256 != good {
			t.Fatalf("%+v, %v", res, err)
		}
	})
	t.Run("a row that would use the network is refused", func(t *testing.T) {
		for _, bad := range []plan.ItemPlan{
			{ID: "qwen-x", Source: plan.SourceNetwork},
			{ID: "qwen-x", Source: plan.SourceLocalCopy},
			{ID: "qwen-x", Source: plan.SourceLocalBuild, FromRoot: driveID},
		} {
			_, err := e.resolveImport(&task{row: bad, cat: model(good)})
			var re *item.ResolveError
			if !errors.As(err, &re) || !errors.Is(err, ErrNotFromDrive) {
				t.Fatalf("%+v: %v", bad, err)
			}
		}
	})
	t.Run("a drive copy that is gone is LAI-SYN-070", func(t *testing.T) {
		e2 := &Engine{d: Deps{FS: config.OSFS{}, Copies: copies{}}}
		_, err := e2.resolveImport(&task{row: row, cat: model(good)})
		var re *item.ResolveError
		if !errors.As(err, &re) || re.Code != doctor.SYN070 {
			t.Fatalf("err = %v", err)
		}
	})
}
