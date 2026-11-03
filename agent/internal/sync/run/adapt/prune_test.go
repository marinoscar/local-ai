package adapt

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/api"
	"github.com/marinoscar/local-ai/agent/internal/ark"
	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/sync/journal"
	"github.com/marinoscar/local-ai/agent/internal/sync/plan"
	"github.com/marinoscar/local-ai/agent/internal/sync/retention"
)

type pruneRig struct {
	*rig
	internal, drive ark.Root
	idx             *ark.Index
	pruner          Pruner
}

// newPruneRig is a node with two roots whose planner calls "osm-north" and "only-here" orphans on the
// internal root and "osm-north" on the drive too.
func newPruneRig(t *testing.T) *pruneRig {
	t.Helper()
	r := newRig(t)
	dir := t.TempDir()
	internal := r.pl.roots[0]
	drive := ark.Root{ID: driveID, Kind: ark.KindArkDrive, Path: filepath.Join(dir, "localai"), Mount: dir, State: ark.StateReady, MountID: 4}
	if err := os.MkdirAll(drive.Path, 0o755); err != nil {
		t.Fatal(err)
	}
	r.pl.roots = []ark.Root{internal, drive}
	now := time.Now()
	install(t, internal, "osm-north", now)
	install(t, drive, "osm-north", now)
	install(t, internal, "only-here", now)
	install(t, internal, "wanted", now)
	orphan := func(id string) plan.ItemPlan { return plan.ItemPlan{ID: id, Kind: "model", Action: plan.ActionOrphan} }
	r.pl.plans = []plan.RootPlan{
		{Root: ark.InternalID, CatalogVersion: "v1", Items: []plan.ItemPlan{{ID: "wanted", Kind: "model", Action: plan.ActionKeep}, orphan("osm-north"), orphan("only-here")}},
		{Root: driveID, CatalogVersion: "v1", Items: []plan.ItemPlan{orphan("osm-north")}},
	}
	idx := ark.NewIndex(config.OSFS{})
	if _, err := idx.Rebuild(bg, r.pl.roots); err != nil {
		t.Fatal(err)
	}
	jr, _, err := journal.Open(bg, journal.Options{Dir: filepath.Join(dir, "j"), Logger: quiet})
	if err != nil {
		t.Fatal(err)
	}
	ret, err := retention.New(retention.Deps{FS: retention.OSFS{}, Roots: func() []ark.Root { return r.pl.roots }, Copies: idx, Journal: jr.Items, Index: idx, Items: jr.Items, Logger: quiet})
	if err != nil {
		t.Fatal(err)
	}
	return &pruneRig{rig: r, internal: internal, drive: drive, idx: idx, pruner: Pruner{Engine: r.eng, Retention: ret}}
}

func TestPrunerDeletesOrphansAndRefusesTheLastCopy(t *testing.T) {
	r := newPruneRig(t)
	res, err := r.pruner.Prune(bg, api.ArkPruneRequest{Orphans: true})
	if err != nil {
		t.Fatal(err)
	}
	// osm-north is on both roots: the internal copy goes (the drive's survives), then the drive's is
	// the last one. only-here is the only copy of its item. wanted is not an orphan and stays.
	if len(res.Deleted) != 1 || res.Deleted[0].Root != ark.InternalID || res.Deleted[0].Item != "osm-north" || res.Deleted[0].Kind != "orphan" || res.Deleted[0].Version != "rev1" {
		t.Fatalf("deleted = %+v", res.Deleted)
	}
	want := map[string]string{driveID + "/osm-north": "last_verified_copy", "internal/only-here": "last_verified_copy"}
	if len(res.Refused) != len(want) {
		t.Fatalf("refused = %+v", res.Refused)
	}
	for _, f := range res.Refused {
		if want[f.Root+"/"+f.Item] != f.Reason {
			t.Errorf("refused %+v", f)
		}
	}
	if res.FreedBytes != res.Deleted[0].Bytes || res.FreedBytes == 0 {
		t.Errorf("freed %d", res.FreedBytes)
	}
	for _, p := range []string{
		filepath.Join(r.drive.Path, "models", "osm-north", "w.gguf"),
		filepath.Join(r.internal.Path, "models", "only-here", "w.gguf"),
		filepath.Join(r.internal.Path, "models", "wanted", "w.gguf"),
	} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s is gone: %v", p, err)
		}
	}
	if _, err := os.Stat(filepath.Join(r.internal.Path, "models", "osm-north", "w.gguf")); !os.IsNotExist(err) {
		t.Errorf("the orphan with another copy is still there: %v", err)
	}
	// Planned offline, whatever the network says.
	if len(r.pl.reqs) == 0 || !r.pl.reqs[len(r.pl.reqs)-1].Offline {
		t.Errorf("plan requests = %+v", r.pl.reqs)
	}
}

func TestPrunerDryRunChangesNothing(t *testing.T) {
	r := newPruneRig(t)
	dry, err := r.pruner.Prune(bg, api.ArkPruneRequest{Orphans: true, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(r.internal.Path, "models", "osm-north", "w.gguf")); err != nil {
		t.Fatalf("a dry run deleted: %v", err)
	}
	real, err := r.pruner.Prune(bg, api.ArkPruneRequest{Orphans: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(dry.Deleted) != len(real.Deleted) || len(dry.Refused) != len(real.Refused) || dry.FreedBytes != real.FreedBytes {
		t.Fatalf("dry %+v\nreal %+v", dry, real)
	}
}

func TestPrunerNamesRootsByIDOrPath(t *testing.T) {
	r := newPruneRig(t)
	res, err := r.pruner.Prune(bg, api.ArkPruneRequest{Roots: []string{r.drive.Path}, Orphans: true, DryRun: true})
	// Only the named root is looked at; its copy has another one (internal), so a real run could delete it.
	if err != nil || len(res.Deleted) != 1 || res.Deleted[0].Root != driveID || len(res.Refused) != 0 {
		t.Fatalf("%+v, %v", res, err)
	}
	apiErr(t, second2(r.pruner, api.ArkPruneRequest{Roots: []string{"ark-ffffffff"}, Orphans: true}), http.StatusBadRequest, api.CodeInvalidRequest)
}

func second2(p Pruner, req api.ArkPruneRequest) error {
	_, err := p.Prune(bg, req)
	return err
}

func TestPrunerRefusesWhileASyncRunIsActive(t *testing.T) {
	r := newPruneRig(t)
	r.mach.block = make(chan struct{})
	got, err := Starter{Engine: r.eng}.Start(bg, api.ArkSyncRequest{Trigger: api.ArkTriggerManual})
	if err != nil {
		t.Fatal(err)
	}
	e := apiErr(t, second2(r.pruner, api.ArkPruneRequest{Orphans: true}), http.StatusConflict, doctor.SYN005)
	if e.Details["run_id"] != got.RunID {
		t.Errorf("details = %v", e.Details)
	}
	if _, err := os.Stat(filepath.Join(r.internal.Path, "models", "osm-north", "w.gguf")); err != nil {
		t.Fatalf("a refused prune deleted: %v", err)
	}
	close(r.mach.block)
	r.wait(got.RunID)
}

// With no catalog every item would look like an orphan: prune refuses rather than guess.
func TestPrunerNeverCallsEverythingAnOrphanWithoutACatalog(t *testing.T) {
	r := newPruneRig(t)
	r.pl.plans = []plan.RootPlan{{Root: ark.InternalID, CatalogVersion: "", Items: []plan.ItemPlan{{ID: "osm-north", Action: plan.ActionOrphan}}}}
	apiErr(t, second2(r.pruner, api.ArkPruneRequest{Orphans: true}), http.StatusConflict, doctor.CAT001)
	r.pl.plans = nil
	apiErr(t, second2(r.pruner, api.ArkPruneRequest{Orphans: true}), http.StatusConflict, doctor.CAT001)
	if _, err := os.Stat(filepath.Join(r.internal.Path, "models", "osm-north", "w.gguf")); err != nil {
		t.Fatalf("deleted: %v", err)
	}
	// Trash needs no catalog.
	if _, err := r.pruner.Prune(context.Background(), api.ArkPruneRequest{Trash: true, DryRun: true}); err != nil {
		t.Fatal(err)
	}
}
