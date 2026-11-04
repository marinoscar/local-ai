package importer

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/ark"
	"github.com/marinoscar/local-ai/agent/internal/catalog"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/sync/manifest"
	"github.com/marinoscar/local-ai/agent/internal/sync/plan"
)

var bg = context.Background()

var now = time.Date(2026, 11, 2, 15, 0, 0, 0, time.UTC)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

const (
	driveA = "ark-0a0b0c0d"
	driveB = "ark-0e0f1011"
	days   = 24 * time.Hour
)

// fakeGatherer returns the PlanInput a test built; it records what it was asked.
type fakeGatherer struct {
	roots []ark.Root
	in    plan.PlanInput
	opts  []plan.GatherOptions
	err   error
}

func (g *fakeGatherer) Roots() []ark.Root { return g.roots }
func (g *fakeGatherer) Gather(_ context.Context, rootID string, opt plan.GatherOptions) (plan.PlanInput, error) {
	g.opts = append(g.opts, opt)
	if g.err != nil {
		return plan.PlanInput{}, g.err
	}
	in := g.in
	// Gather hands out its own maps, as the real one does.
	in.Present = cloneManifests(g.in.Present)
	in.OtherRoots = map[string][]plan.RootCopy{}
	for id, c := range g.in.OtherRoots {
		in.OtherRoots[id] = append([]plan.RootCopy(nil), c...)
	}
	return in, nil
}

func cloneManifests(in map[string]manifest.Manifest) map[string]manifest.Manifest {
	out := map[string]manifest.Manifest{}
	for k, v := range in {
		out[k] = v
	}
	return out
}

type fakeProfile struct {
	models, content []string
	calls           int
	err             error
}

func (p *fakeProfile) AddItems(models, content []string) error {
	p.calls++
	p.models, p.content = append(p.models, models...), append(p.content, content...)
	return p.err
}

type fakeCatalog struct {
	version   string
	installed []string
	err       error
}

func (c *fakeCatalog) Version() string { return c.version }
func (c *fakeCatalog) Install(_ context.Context, dir string) (string, error) {
	c.installed = append(c.installed, dir)
	if c.err != nil {
		return "", c.err
	}
	return "2026.12.01-1", nil
}

type codedErr struct{ code string }

func (e codedErr) Error() string { return e.code + ": rejected" }
func (e codedErr) Code() string  { return e.code }

func model(id string, size int64) catalog.Model {
	return catalog.Model{
		ID: id, Engine: "llama", SurvivalTier: 2,
		Source: catalog.ModelSource{Type: "huggingface", Revision: "rev-" + id + "-0000"},
		Files:  []catalog.ModelFile{{Kind: "weights", Name: id + ".gguf", SizeBytes: size}},
	}
}

func zim(id string, size int64) catalog.ContentItem {
	return catalog.ContentItem{ID: id, Kind: "kiwix", ExpectedSizeBytes: size, RefreshDays: 30, SurvivalTier: 2,
		Source: catalog.ContentSource{Type: "kiwix", VersionPolicy: "latest"}}
}

func mf(id, kind, version, sortKey string, size int64, age time.Duration) manifest.Manifest {
	m := manifest.Manifest{ItemID: id, ItemKind: kind, Version: version, SortKey: sortKey, TotalBytes: size, DownloadedAt: now.Add(-age)}
	if kind == manifest.KindModel {
		m.Engine = "llama"
	}
	return m
}

// rig is a node: the internal root wants m-new, m-gone, m-same, m-newer, m-older and w-zim; a drive
// holds some of them plus m-extra, which the profile does not list.
type rig struct {
	t    *testing.T
	g    *fakeGatherer
	prof *fakeProfile
	cat  *fakeCatalog
	p    *Planner
	dir  string
}

func newRig(t *testing.T) *rig {
	t.Helper()
	dir := t.TempDir()
	models := []catalog.Model{
		model("m-new", 10), model("m-gone", 10), model("m-same", 10), model("m-newer", 10), model("m-older", 10), model("m-extra", 10), model("m-removed", 10),
	}
	content := []catalog.ContentItem{zim("w-zim", 30)}
	prof := catalog.Profile{ID: "int", Storage: catalog.Storage{Target: "internal", BudgetBytes: 1000, MinFreeBytes: 5},
		Include: catalog.Include{Models: []string{"m-new", "m-gone", "m-same", "m-newer", "m-older"}, Content: []string{"w-zim"}}}
	remove := catalog.Overrides{RemoveModels: []string{"m-removed"}}
	driveRoot := ark.Root{ID: driveA, Kind: ark.KindArkDrive, Path: filepath.Join(dir, "drive", "localai"), Mount: filepath.Join(dir, "drive"), State: ark.StateReady, MountID: 7}
	g := &fakeGatherer{
		roots: []ark.Root{{ID: ark.InternalID, Kind: ark.KindInternal, Path: filepath.Join(dir, "srv"), State: ark.StateReady}, driveRoot},
		in: plan.PlanInput{
			Root:      plan.RootInfo{ID: ark.InternalID, Kind: ark.KindInternal, Path: filepath.Join(dir, "srv"), Profile: "int", TotalBytes: 5000, FreeBytes: 4000},
			Catalog:   catalog.New("2026.11.02-1", models, content, []catalog.Profile{prof}),
			Profile:   &prof,
			Overrides: &remove,
			Mode:      plan.ModeSurvival, Now: now, Offline: true,
			Present: map[string]manifest.Manifest{
				"m-same":  mf("m-same", "model", "v1", "2026-01", 10, 3*days),
				"m-newer": mf("m-newer", "model", "v9", "2026-09", 10, 3*days),
				"m-older": mf("m-older", "model", "v1", "2026-01", 10, 3*days),
				"w-zim":   mf("w-zim", "kiwix", "2026-01", "2026-01", 30, 3*days),
			},
			OtherRoots: map[string][]plan.RootCopy{},
		},
	}
	onDrive := func(m manifest.Manifest) {
		g.in.OtherRoots[m.ItemID] = append(g.in.OtherRoots[m.ItemID], plan.RootCopy{RootID: driveA, Manifest: m})
	}
	onDrive(mf("m-new", "model", "v1", "2026-05", 10, 9*days))
	onDrive(mf("m-same", "model", "v1", "2026-01", 10, 9*days))
	onDrive(mf("m-newer", "model", "v5", "2026-05", 10, 9*days))
	onDrive(mf("m-older", "model", "v5", "2026-05", 10, 9*days))
	onDrive(mf("m-extra", "model", "v1", "2026-05", 10, 9*days))
	onDrive(mf("m-removed", "model", "v1", "2026-05", 10, 9*days))
	onDrive(mf("w-zim", "kiwix", "2026-10", "2026-10", 30, 9*days))
	// A copy of m-gone exists only on a second drive that is not the source.
	g.in.OtherRoots["m-gone"] = []plan.RootCopy{{RootID: driveB, Manifest: mf("m-gone", "model", "v1", "2026-05", 10, 9*days)}}
	r := &rig{t: t, g: g, prof: &fakeProfile{}, cat: &fakeCatalog{version: "2026.11.02-1"}, dir: dir}
	p, err := New(Deps{G: g, Profile: r.prof, Catalog: r.cat, Logger: quiet})
	if err != nil {
		t.Fatal(err)
	}
	r.p = p
	return r
}

func (r *rig) row(res Result, id string) plan.ItemPlan {
	r.t.Helper()
	it, ok := res.Plan.Item(id)
	if !ok {
		r.t.Fatalf("the plan has no row for %s", id)
	}
	return it
}

func TestNewNeedsAGatherer(t *testing.T) {
	if _, err := New(Deps{}); err == nil {
		t.Fatal("accepted nothing")
	}
}

func TestSourceIsTheNamedDriveOrTheOnlyOne(t *testing.T) {
	r := newRig(t)
	t.Run("the only drive", func(t *testing.T) {
		d, err := r.p.Source("")
		if err != nil || d.ID != driveA {
			t.Fatalf("%v, %v", d.ID, err)
		}
	})
	t.Run("named by id, mount point or path", func(t *testing.T) {
		for _, name := range []string{driveA, filepath.Join(r.dir, "drive"), filepath.Join(r.dir, "drive", "localai")} {
			if d, err := r.p.Source(name); err != nil || d.ID != driveA {
				t.Fatalf("%s: %v, %v", name, d.ID, err)
			}
		}
	})
	t.Run("a read-only drive is a source", func(t *testing.T) {
		r.g.roots[1].State, r.g.roots[1].ReadOnly = ark.StateReadOnly, true
		defer func() { r.g.roots[1].State, r.g.roots[1].ReadOnly = ark.StateReady, false }()
		if _, err := r.p.Source(""); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("several drives and no name: LAI-ARK-015 with their ids", func(t *testing.T) {
		r.g.roots = append(r.g.roots, ark.Root{ID: driveB, Kind: ark.KindArkDrive, Path: filepath.Join(r.dir, "b", "localai"), State: ark.StateReady, MountID: 9})
		_, err := r.p.Source("")
		var several *SeveralDrivesError
		if !errors.As(err, &several) || !reflect.DeepEqual(several.Roots, []string{driveA, driveB}) || !strings.Contains(err.Error(), doctor.ARK015) {
			t.Fatalf("err = %v", err)
		}
		// Naming one settles it.
		if d, err := r.p.Source(driveB); err != nil || d.ID != driveB {
			t.Fatalf("%v, %v", d.ID, err)
		}
		r.g.roots = r.g.roots[:2]
	})
	t.Run("no drive, an unknown name, the internal root, a drive that cannot be read", func(t *testing.T) {
		if _, err := r.p.Source(ark.InternalID); !isType[*UnknownDriveError](err) {
			t.Fatalf("internal: %v", err)
		}
		if _, err := r.p.Source("ark-ffffffff"); !isType[*UnknownDriveError](err) {
			t.Fatalf("unknown: %v", err)
		}
		r.g.roots[1].State = ark.StateUnclean
		var bad *UnusableDriveError
		if _, err := r.p.Source(driveA); !errors.As(err, &bad) || bad.State != "unclean" {
			t.Fatalf("unclean named: %v", err)
		}
		if _, err := r.p.Source(""); !errors.Is(err, ErrNoDrive) {
			t.Fatalf("unclean only: %v", err)
		}
		r.g.roots[1].State = ark.StateReady
	})
}

func isType[T error](err error) bool {
	var t T
	return errors.As(err, &t)
}

// The plan: Resolved comes from the drive, every copy is local, and the node's own newer copies are
// never replaced by an older one on the drive.
func TestThePlanCopiesFromTheDriveAndNeverUsesTheNetwork(t *testing.T) {
	r := newRig(t)
	res, err := r.p.Plan(bg, Request{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Drive.ID != driveA || res.Input.Catalog == nil {
		t.Fatalf("result = %+v", res)
	}
	// Gathered for the internal root, with the network off and nothing resolved over it.
	if len(r.g.opts) != 1 || r.g.opts[0].Resolve || r.g.opts[0].Online == nil || *r.g.opts[0].Online {
		t.Fatalf("gather options = %+v", r.g.opts)
	}
	if !res.Plan.Offline {
		t.Fatal("the plan must say no network is used")
	}

	got := func(id string) plan.ItemPlan { return r.row(res, id) }
	if it := got("m-new"); it.Action != plan.ActionDownload || it.Source != plan.SourceLocalCopy || it.FromRoot != driveA || it.ToVersion != "v1" || it.After != "" {
		t.Errorf("m-new = %+v", it)
	}
	if it := got("m-gone"); it.Action != plan.ActionSkip || it.Reason != plan.ReasonUnresolved {
		t.Errorf("an item the drive does not hold = %+v", it) // a copy on another drive is not this drive's
	}
	if it := got("m-same"); it.Action != plan.ActionKeep {
		t.Errorf("m-same = %+v", it)
	}
	if it := got("m-newer"); it.Action != plan.ActionKeep || it.FromVersion != "v9" {
		t.Errorf("an older version on the drive must not replace a newer one: %+v", it)
	}
	if it := got("m-older"); it.Action != plan.ActionUpdate || it.Source != plan.SourceLocalCopy || it.ToVersion != "v5" || it.FromVersion != "v1" {
		t.Errorf("m-older = %+v", it)
	}
	// A ZIM that follows "latest" is taken from the drive when newer, although its refresh is not due:
	// somebody asked for it.
	if it := got("w-zim"); it.Action != plan.ActionUpdate || it.Source != plan.SourceLocalCopy || it.ToVersion != "2026-10" {
		t.Errorf("w-zim = %+v", it)
	}
	for _, it := range res.Plan.Items {
		if it.Source == plan.SourceNetwork {
			t.Errorf("%s would use the network", it.ID)
		}
	}
	// m-extra is on the drive but not wanted, and nobody asked for it: it is not in the plan.
	if _, ok := res.Plan.Item("m-extra"); ok {
		t.Error("an item the profile does not list was planned")
	}
	if len(r.prof.models)+len(r.prof.content) != 0 || r.prof.calls != 0 {
		t.Error("node.yaml was changed without being asked")
	}
	// The input of the caller is not modified (a second Plan gives the same answer).
	again, err := r.p.Plan(bg, Request{})
	if err != nil || !reflect.DeepEqual(again.Plan, res.Plan) {
		t.Fatalf("a second plan differs: %v", err)
	}
}

func TestAnItemTheProfileDoesNotListIsRefusedUnlessItIsAddedToTheProfile(t *testing.T) {
	r := newRig(t)

	t.Run("refused without add_to_profile", func(t *testing.T) {
		res, err := r.p.Plan(bg, Request{Items: []string{"m-extra", "m-new"}})
		if err != nil {
			t.Fatal(err)
		}
		if it := r.row(res, "m-extra"); it.Action != plan.ActionSkip || it.Reason != plan.ReasonNotInProfile || it.ToVersion != "v1" {
			t.Fatalf("m-extra = %+v", it)
		}
		if r.prof.calls != 0 || len(res.Added) != 0 {
			t.Fatal("node.yaml was changed")
		}
	})
	t.Run("added with add_to_profile, node.yaml written, the item planned as a copy", func(t *testing.T) {
		res, err := r.p.Plan(bg, Request{Items: []string{"m-extra", "m-new"}, AddToProfile: true})
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(r.prof.models, []string{"m-extra"}) || len(r.prof.content) != 0 || r.prof.calls != 1 || !reflect.DeepEqual(res.Added, []string{"m-extra"}) {
			t.Fatalf("profile editor got %v / %v (%d calls), added %v", r.prof.models, r.prof.content, r.prof.calls, res.Added)
		}
		if it := r.row(res, "m-extra"); it.Action != plan.ActionDownload || it.Source != plan.SourceLocalCopy || it.FromRoot != driveA {
			t.Fatalf("m-extra = %+v", it)
		}
	})
	t.Run("a dry run changes nothing and still shows the plan", func(t *testing.T) {
		r.prof.calls, r.prof.models = 0, nil
		res, err := r.p.Plan(bg, Request{Items: []string{"m-extra"}, AddToProfile: true, DryRun: true})
		if err != nil {
			t.Fatal(err)
		}
		if r.prof.calls != 0 {
			t.Fatal("a dry run wrote node.yaml")
		}
		if it := r.row(res, "m-extra"); it.Action != plan.ActionDownload {
			t.Fatalf("m-extra = %+v", it)
		}
	})
	t.Run("an item removed in the overrides is wanted again", func(t *testing.T) {
		res, err := r.p.Plan(bg, Request{Items: []string{"m-removed"}, AddToProfile: true, DryRun: true})
		if err != nil {
			t.Fatal(err)
		}
		if it := r.row(res, "m-removed"); it.Action != plan.ActionDownload {
			t.Fatalf("m-removed = %+v", it)
		}
	})
	t.Run("a content item goes to add_content", func(t *testing.T) {
		r.g.in.OtherRoots["w-more"] = []plan.RootCopy{{RootID: driveA, Manifest: mf("w-more", "kiwix", "2026-10", "2026-10", 20, days)}}
		r.g.in.Catalog = catalog.New("2026.11.02-1", append([]catalog.Model{model("m-extra", 10)}, model("m-new", 10)), []catalog.ContentItem{zim("w-zim", 30), zim("w-more", 20)}, []catalog.Profile{*r.g.in.Profile})
		r.prof.calls, r.prof.models, r.prof.content = 0, nil, nil
		if _, err := r.p.Plan(bg, Request{Items: []string{"w-more"}, AddToProfile: true}); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(r.prof.content, []string{"w-more"}) || len(r.prof.models) != 0 {
			t.Fatalf("models %v content %v", r.prof.models, r.prof.content)
		}
	})
	t.Run("add_to_profile without items is a mistake", func(t *testing.T) {
		if _, err := r.p.Plan(bg, Request{AddToProfile: true}); !errors.Is(err, ErrNeedItems) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("an item that is nowhere is unresolved, not an error", func(t *testing.T) {
		res, err := r.p.Plan(bg, Request{Items: []string{"never-heard-of-it", "m-gone"}})
		if err != nil {
			t.Fatal(err)
		}
		if it := r.row(res, "never-heard-of-it"); it.Action != plan.ActionSkip || it.Reason != plan.ReasonUnresolved {
			t.Fatalf("%+v", it)
		}
	})
	t.Run("the profile editor failing stops the import before anything is planned", func(t *testing.T) {
		bad := newRig(t)
		bad.prof.err = errors.New("node.yaml is read-only")
		if _, err := bad.p.Plan(bg, Request{Items: []string{"m-extra"}, AddToProfile: true}); err == nil || !strings.Contains(err.Error(), "read-only") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("no way to write node.yaml is refused", func(t *testing.T) {
		noProf := newRig(t)
		p, _ := New(Deps{G: noProf.g, Logger: quiet})
		if _, err := p.Plan(bg, Request{Items: []string{"m-extra"}, AddToProfile: true}); err == nil {
			t.Fatal("add_to_profile without a profile editor was accepted")
		}
	})
}

func TestTheCatalogOnTheDriveIsInstalledWhenNewerAndValid(t *testing.T) {
	writeVersion := func(r *rig, v string) string {
		dir := filepath.Join(r.dir, "drive", "localai", "kit", "catalog")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "CATALOG_VERSION"), []byte(v+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	t.Run("newer and valid: installed first", func(t *testing.T) {
		r := newRig(t)
		dir := writeVersion(r, "2026.12.01-1")
		res, err := r.p.Plan(bg, Request{Catalog: true})
		if err != nil || !reflect.DeepEqual(r.cat.installed, []string{dir}) || res.CatalogInstalled != "2026.12.01-1" || len(res.Warnings) != 0 {
			t.Fatalf("installed %v, result %+v, %v", r.cat.installed, res, err)
		}
	})
	t.Run("without catalog: true nothing is installed", func(t *testing.T) {
		r := newRig(t)
		writeVersion(r, "2026.12.01-1")
		if _, err := r.p.Plan(bg, Request{}); err != nil || len(r.cat.installed) != 0 {
			t.Fatalf("%v %v", r.cat.installed, err)
		}
	})
	t.Run("not newer: left alone, including a same-day higher number compared as a number", func(t *testing.T) {
		r := newRig(t)
		writeVersion(r, "2026.11.02-1")
		r.cat.version = "2026.11.02-1"
		if _, err := r.p.Plan(bg, Request{Catalog: true}); err != nil || len(r.cat.installed) != 0 {
			t.Fatalf("%v %v", r.cat.installed, err)
		}
		writeVersion(r, "2026.11.02-9")
		r.cat.version = "2026.11.02-10"
		if _, err := r.p.Plan(bg, Request{Catalog: true}); err != nil || len(r.cat.installed) != 0 {
			t.Fatalf("-9 is older than -10: %v %v", r.cat.installed, err)
		}
	})
	t.Run("invalid on the drive: LAI-CAT-002 as a warning, the import goes on with the catalog it has", func(t *testing.T) {
		r := newRig(t)
		writeVersion(r, "2026.12.01-1")
		r.cat.err = codedErr{doctor.CAT002}
		res, err := r.p.Plan(bg, Request{Catalog: true})
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Warnings) != 1 || !strings.HasPrefix(res.Warnings[0], doctor.CAT002) || res.CatalogInstalled != "" {
			t.Fatalf("warnings %v, installed %q", res.Warnings, res.CatalogInstalled)
		}
		if it := r.row(res, "m-new"); it.Action != plan.ActionDownload {
			t.Fatal("the import must go on")
		}
	})
	t.Run("a CATALOG_VERSION that is not a version is invalid too", func(t *testing.T) {
		r := newRig(t)
		writeVersion(r, "latest")
		res, err := r.p.Plan(bg, Request{Catalog: true})
		if err != nil || len(res.Warnings) != 1 || len(r.cat.installed) != 0 {
			t.Fatalf("%v %v %v", res.Warnings, r.cat.installed, err)
		}
	})
	t.Run("a drive with no catalog says nothing", func(t *testing.T) {
		r := newRig(t)
		res, err := r.p.Plan(bg, Request{Catalog: true})
		if err != nil || len(res.Warnings) != 0 || len(r.cat.installed) != 0 {
			t.Fatalf("%v %v %v", res.Warnings, r.cat.installed, err)
		}
	})
	t.Run("a dry run installs nothing", func(t *testing.T) {
		r := newRig(t)
		writeVersion(r, "2026.12.01-1")
		if _, err := r.p.Plan(bg, Request{Catalog: true, DryRun: true}); err != nil || len(r.cat.installed) != 0 {
			t.Fatalf("%v %v", r.cat.installed, err)
		}
	})
}

func TestAGatherFailureIsReturnedAsIs(t *testing.T) {
	r := newRig(t)
	r.g.err = &plan.ErrUnknownProfile{Profile: "gone", Root: ark.InternalID}
	_, err := r.p.Plan(bg, Request{})
	var up *plan.ErrUnknownProfile
	if !errors.As(err, &up) {
		t.Fatalf("err = %v", err)
	}
}
