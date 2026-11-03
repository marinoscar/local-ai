package plan_test

import (
	"context"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/api"
	"github.com/marinoscar/local-ai/agent/internal/ark"
	"github.com/marinoscar/local-ai/agent/internal/catalog"
	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/sync/journal"
	"github.com/marinoscar/local-ai/agent/internal/sync/manifest"
	"github.com/marinoscar/local-ai/agent/internal/sync/plan"
)

type fakeIndex struct {
	copies map[string][]ark.Copy
}

func (f *fakeIndex) ItemIDs() []string {
	var ids []string
	for id := range f.copies {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
func (f *fakeIndex) Copies(id string) []ark.Copy { return f.copies[id] }

type fakeJournal struct{ items []journal.Item }

func (f fakeJournal) List() []journal.Item { return f.items }

type gatherRig struct {
	t        *testing.T
	internal string
	drive    string
	roots    []ark.Root
	index    *fakeIndex
	journal  *fakeJournal
	catalog  *catalog.Catalog
	settings plan.NodeSettings
	network  plan.Network
	statfs   func(string) (int64, int64, error)
	res      *plan.Resolvers
	// timeout is Deps.ResolveTimeout (zero: the package default).
	timeout time.Duration
}

func newGatherRig(t *testing.T) *gatherRig {
	t.Helper()
	r := &gatherRig{t: t, internal: t.TempDir(), drive: t.TempDir(), index: &fakeIndex{copies: map[string][]ark.Copy{}}, journal: &fakeJournal{},
		network: plan.Network{Mode: plan.ModeOnline, Online: true}, res: plan.NewResolvers()}
	r.roots = []ark.Root{
		{ID: ark.InternalID, Kind: ark.KindInternal, Path: r.internal, State: ark.StateReady},
		{ID: "ark-3f9c0a12", Kind: ark.KindArkDrive, Path: r.drive, Mount: filepath.Dir(r.drive), MountID: 40, State: ark.StateReady, Profile: "drive-profile"},
	}
	models := []catalog.Model{model("m-pinned", 1, 10), model("m-other", 2, 20)}
	content := []catalog.ContentItem{zim("w-latest", 30, refresh(30)), docs(1)}
	internalProf := catalog.Profile{ID: "int-profile", Storage: catalog.Storage{Target: "internal", BudgetBytes: 1000, MinFreeBytes: 5},
		Include: catalog.Include{Models: []string{"m-pinned", "m-other"}, Content: []string{"w-latest", "docs-bundle"}}}
	driveProf := catalog.Profile{ID: "drive-profile", Storage: catalog.Storage{Target: "ark_drive", BudgetBytes: 1}, Include: catalog.Include{Models: []string{"m-pinned"}}}
	r.catalog = catalog.New(catVersion, models, content, []catalog.Profile{internalProf, driveProf})
	r.settings = plan.NodeSettings{Profile: "int-profile"}
	r.statfs = func(string) (int64, int64, error) { return 100_000_000_000_000, 50_000_000_000_000, nil }
	return r
}

func (r *gatherRig) gatherer() *plan.Gatherer {
	r.t.Helper()
	g, err := plan.NewGatherer(plan.Deps{
		FS:        config.OSFS{},
		Catalog:   func() *catalog.Catalog { return r.catalog },
		Settings:  func() plan.NodeSettings { return r.settings },
		Roots:     func() []ark.Root { return r.roots },
		Copies:    r.index,
		Journal:   r.journal,
		Statfs:    func(p string) (int64, int64, error) { return r.statfs(p) },
		Node:      func() plan.NodeInfo { return plan.NodeInfo{Backend: "cpu", Arch: "amd64", Codename: "resolute"} },
		Network:   func() plan.Network { return r.network },
		Resolvers: r.res,

		ResolveTimeout: r.timeout,
		Now:            func() time.Time { return now },
	})
	if err != nil {
		r.t.Fatal(err)
	}
	return g
}

func (r *gatherRig) copy(rootID, id, version string, size int64) {
	root := r.internal
	if rootID != ark.InternalID {
		root = r.drive
	}
	r.index.copies[id] = append(r.index.copies[id], ark.Copy{RootID: rootID, Dir: filepath.Join(root, "models", id),
		Manifest: manifest.Manifest{ItemID: id, ItemKind: manifest.KindModel, Version: version, TotalBytes: size, DownloadedAt: now.Add(-days)}})
}

func writeFile(t *testing.T, path string, size int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, make([]byte, size), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestNewGathererNamesWhatIsMissing(t *testing.T) {
	_, err := plan.NewGatherer(plan.Deps{})
	if err == nil {
		t.Fatal("an empty Deps must be refused")
	}
	for _, name := range []string{"Catalog", "Copies", "FS", "Journal", "Network", "Node", "Roots", "Settings", "Statfs"} {
		if !contains(err.Error(), name) {
			t.Errorf("error %q does not name %s", err, name)
		}
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestGatherBuildsTheInput(t *testing.T) {
	r := newGatherRig(t)
	r.settings.Overrides = catalog.Overrides{RemoveModels: []string{"m-other"}}
	r.copy(ark.InternalID, "m-pinned", "v1", 10)
	r.copy("ark-3f9c0a12", "m-pinned", "v1", 10)
	r.copy("ark-3f9c0a12", "m-other", "v1", 20)
	r.statfs = func(p string) (int64, int64, error) {
		if p != r.internal {
			t.Errorf("statfs asked about %s, want the root path %s", p, r.internal)
		}
		return 900, 400, nil
	}
	in, err := r.gatherer().Gather(context.Background(), ark.InternalID, plan.GatherOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if in.Root.ID != "internal" || in.Root.TotalBytes != 900 || in.Root.FreeBytes != 400 || in.Root.Profile != "int-profile" || in.Root.ReadOnly {
		t.Errorf("root = %+v", in.Root)
	}
	if in.Profile == nil || in.Profile.ID != "int-profile" || in.Overrides == nil || len(in.Overrides.RemoveModels) != 1 {
		t.Errorf("profile %+v overrides %+v", in.Profile, in.Overrides)
	}
	if in.Mode != plan.ModeOnline || in.Offline || !in.Now.Equal(now) || in.Node.Codename != "resolute" {
		t.Errorf("mode %q offline %v now %v node %+v", in.Mode, in.Offline, in.Now, in.Node)
	}
	if _, ok := in.Present["m-pinned"]; !ok || len(in.Present) != 1 {
		t.Errorf("present = %v", in.Present)
	}
	oc := in.OtherRoots["m-pinned"]
	if len(oc) != 1 || oc[0].RootID != "ark-3f9c0a12" || len(in.OtherRoots["m-other"]) != 1 {
		t.Errorf("other roots = %+v", in.OtherRoots)
	}
	// No resolvers: what the catalog pins (the model revision, the docs version) is resolved; the
	// kiwix item that follows "latest" is not.
	if got := in.Resolved["m-pinned"]; got.Version != "rev-m-pinned" || got.SizeBytes != 10 {
		t.Errorf("m-pinned resolved = %+v", got)
	}
	if got := in.Resolved["docs-bundle"]; got.Version != "catalog-"+catVersion {
		t.Errorf("docs resolved = %+v", got)
	}
	if _, ok := in.Resolved["w-latest"]; ok {
		t.Errorf("a latest item must stay unresolved without a resolver: %+v", in.Resolved["w-latest"])
	}
	if _, ok := in.Resolved["m-other"]; ok {
		t.Error("a removed model must not be resolved")
	}
}

func TestGatherDriveAndRootErrors(t *testing.T) {
	r := newGatherRig(t)
	g := r.gatherer()
	t.Run("a drive takes its own profile and no overrides", func(t *testing.T) {
		r.settings.Overrides = catalog.Overrides{AddModels: []string{"m-other"}}
		in, err := g.Gather(context.Background(), "ark-3f9c0a12", plan.GatherOptions{})
		if err != nil || in.Profile.ID != "drive-profile" || in.Overrides != nil || in.Root.Kind != ark.KindArkDrive {
			t.Fatalf("%+v, %v", in, err)
		}
	})
	t.Run("a drive with no profile in ROOT.json uses ark-drive-full", func(t *testing.T) {
		r.roots[1].Profile = ""
		_, err := g.Gather(context.Background(), "ark-3f9c0a12", plan.GatherOptions{})
		var up *plan.ErrUnknownProfile
		if !errors.As(err, &up) || up.Profile != ark.DefaultDriveProfile || up.Code() != doctor.CAT003 {
			t.Fatalf("err = %v, want the default drive profile to be looked up (and missing from this catalog)", err)
		}
		r.roots[1].Profile = "drive-profile"
	})
	t.Run("an unknown root", func(t *testing.T) {
		_, err := g.Gather(context.Background(), "ark-deadbeef", plan.GatherOptions{})
		var ur *plan.ErrUnknownRoot
		if !errors.As(err, &ur) || ur.Root != "ark-deadbeef" {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("an unknown profile on the internal root", func(t *testing.T) {
		r.settings.Profile = "nope"
		_, err := g.Gather(context.Background(), ark.InternalID, plan.GatherOptions{})
		var up *plan.ErrUnknownProfile
		if !errors.As(err, &up) || up.Profile != "nope" {
			t.Fatalf("err = %v", err)
		}
		r.settings.Profile = "int-profile"
	})
	t.Run("a statfs failure", func(t *testing.T) {
		r.statfs = func(string) (int64, int64, error) { return 0, 0, errors.New("io error") }
		if _, err := g.Gather(context.Background(), ark.InternalID, plan.GatherOptions{}); err == nil {
			t.Fatal("expected the statfs error")
		}
	})
	t.Run("read-only and unclean roots are planned read-only", func(t *testing.T) {
		r.statfs = func(string) (int64, int64, error) { return 10, 10, nil }
		for _, st := range []ark.RootState{ark.StateReadOnly, ark.StateUnclean} {
			r.roots[1].State = st
			in, err := g.Gather(context.Background(), "ark-3f9c0a12", plan.GatherOptions{})
			if err != nil || !in.Root.ReadOnly {
				t.Errorf("state %s: %+v, %v", st, in.Root, err)
			}
		}
		r.roots[1].State, r.roots[0].ReadOnly = ark.StateReady, true
		if in, _ := g.Gather(context.Background(), ark.InternalID, plan.GatherOptions{}); !in.Root.ReadOnly {
			t.Error("a root flagged ReadOnly must be read-only")
		}
	})
	t.Run("a cancelled context", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := g.Gather(ctx, ark.InternalID, plan.GatherOptions{}); !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v", err)
		}
	})
}

func TestGatherNetworkState(t *testing.T) {
	for _, tc := range []struct {
		name    string
		net     plan.Network
		offline bool
	}{
		{"online and reachable", plan.Network{Mode: plan.ModeOnline, Online: true}, false},
		{"online but no internet", plan.Network{Mode: plan.ModeOnline, Online: false}, true},
		{"offline mode", plan.Network{Mode: plan.ModeOffline, Online: true}, true},
		{"survival mode", plan.Network{Mode: plan.ModeSurvival, Online: true}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newGatherRig(t)
			r.network = tc.net
			in, err := r.gatherer().Gather(context.Background(), ark.InternalID, plan.GatherOptions{})
			if err != nil || in.Offline != tc.offline || in.Mode != tc.net.Mode {
				t.Errorf("offline %v mode %q err %v, want offline %v", in.Offline, in.Mode, err, tc.offline)
			}
		})
	}
}

func TestGatherPartialAndStaged(t *testing.T) {
	r := newGatherRig(t)
	writeFile(t, filepath.Join(r.internal, ".staging", "m-pinned", "v1", "weights.gguf"), 7)      // staged, complete
	writeFile(t, filepath.Join(r.internal, ".staging", "m-pinned", "v1", "second.gguf.part"), 99) // the journal counts .part
	writeFile(t, filepath.Join(r.internal, ".staging", "m-other", "v1", "x.gguf"), 5)
	r.journal.items = []journal.Item{
		{RootID: ark.InternalID, ItemID: "m-pinned", State: journal.StateDownloading, Version: "v1", PartialBytes: 13},
		{RootID: ark.InternalID, ItemID: "m-other", State: journal.StateVerified, Version: "v1", PartialBytes: 50}, // finished: not partial
		{RootID: ark.InternalID, ItemID: "../escape", State: journal.StateDownloading, Version: "v1", PartialBytes: 1},
		{RootID: "ark-3f9c0a12", ItemID: "m-pinned", State: journal.StateDownloading, Version: "v1", PartialBytes: 777}, // another root
	}
	in, err := r.gatherer().Gather(context.Background(), ark.InternalID, plan.GatherOptions{})
	if err != nil {
		t.Fatal(err)
	}
	got, ok := in.Partial["m-pinned"]
	if !ok || got.Version != "v1" || got.PartBytes != 13 || got.StagedBytes != 7 {
		t.Errorf("partial = %+v", in.Partial)
	}
	if len(in.Partial) != 1 {
		t.Errorf("partial = %+v, want only m-pinned", in.Partial)
	}
}

func TestGatherTrash(t *testing.T) {
	r := newGatherRig(t)
	r.copy(ark.InternalID, "m-pinned", "v2", 10) // live copy: its trash is eligible
	r.copy(ark.InternalID, "m-other", "v2", 20)  // live copy but mid-swap in the journal: not eligible
	r.journal.items = []journal.Item{{RootID: ark.InternalID, ItemID: "m-other", State: journal.StateSwapped, Version: "v2"}}
	writeFile(t, filepath.Join(r.internal, ".trash", "m-pinned", "v1", "a.gguf"), 11)
	writeFile(t, filepath.Join(r.internal, ".trash", "m-pinned", "v0", "sub", "b.gguf"), 4)
	writeFile(t, filepath.Join(r.internal, ".trash", "m-other", "v1", "c.gguf"), 6)
	writeFile(t, filepath.Join(r.internal, ".trash", "gone", "v1", "d.gguf"), 3) // no live copy
	writeFile(t, filepath.Join(r.internal, ".trash", "stray-file"), 1)           // not a directory
	old := now.Add(-48 * time.Hour)
	if err := os.Chtimes(filepath.Join(r.internal, ".trash", "m-pinned", "v1"), old, old); err != nil {
		t.Fatal(err)
	}
	in, err := r.gatherer().Gather(context.Background(), ark.InternalID, plan.GatherOptions{})
	if err != nil {
		t.Fatal(err)
	}
	type want struct {
		bytes    int64
		eligible bool
	}
	wants := map[string]want{"gone/v1": {3, false}, "m-other/v1": {6, false}, "m-pinned/v0": {4, true}, "m-pinned/v1": {11, true}}
	if len(in.Trash) != len(wants) {
		t.Fatalf("trash = %+v", in.Trash)
	}
	for _, e := range in.Trash {
		w, ok := wants[e.ItemID+"/"+e.Version]
		if !ok || e.Bytes != w.bytes || e.Eligible != w.eligible {
			t.Errorf("trash entry %+v, want %+v", e, w)
		}
		if e.ItemID == "m-pinned" && e.Version == "v1" && !e.TrashedAt.Equal(old) {
			t.Errorf("trashed at %v, want %v", e.TrashedAt, old)
		}
	}
	// And the planner can use it: a download that needs the trash makes room by purging it.
	if !sort.SliceIsSorted(in.Trash, func(i, j int) bool {
		return in.Trash[i].ItemID+in.Trash[i].Version < in.Trash[j].ItemID+in.Trash[j].Version
	}) {
		t.Error("the trash list must be sorted for a stable plan")
	}
}

func TestGatherTrashUsesTheManifestSize(t *testing.T) {
	r := newGatherRig(t)
	r.copy(ark.InternalID, "m-pinned", "v2", 10)
	dir := filepath.Join(r.internal, ".trash", "m-pinned", "v1")
	writeFile(t, filepath.Join(dir, "a.gguf"), 3)
	m := manifest.Manifest{SchemaVersion: manifest.SchemaVersion, ItemID: "m-pinned", ItemKind: manifest.KindModel, Engine: "llama", CatalogVersion: catVersion, Version: "v1",
		SortKey: "k", State: manifest.StateVerified, Source: manifest.Source{Type: "huggingface"}, TotalBytes: 3,
		Files:        []manifest.File{{Kind: "weights", Path: "a.gguf", SizeBytes: 3, SHA256: strings.Repeat("a", 64), SHA256Source: "catalog"}},
		Trust:        manifest.TrustPinned,
		DownloadedAt: now, VerifiedAt: now, LastReverifiedAt: now}
	data, err := m.Encode()
	if err != nil {
		t.Fatalf("fixture manifest does not encode: %v", err)
	}
	if err := os.WriteFile(manifest.Path(dir), data, 0o644); err != nil {
		t.Fatal(err)
	}
	in, err := r.gatherer().Gather(context.Background(), ark.InternalID, plan.GatherOptions{})
	if err != nil || len(in.Trash) != 1 || in.Trash[0].Bytes != 3 {
		t.Fatalf("trash = %+v, %v", in.Trash, err)
	}
}

func TestGatherResolvers(t *testing.T) {
	var calls atomic.Int32
	answer := plan.ResolverFunc(func(_ context.Context, it plan.CatalogItem) (plan.Resolved, error) {
		calls.Add(1)
		switch it.ID {
		case "w-latest":
			return plan.Resolved{Version: "2026-10", SortKey: "2026-10", SizeBytes: 77}, nil
		case "m-other":
			return plan.Resolved{}, errors.New("HEAD failed")
		}
		return plan.Resolved{Version: "from-resolver", SizeBytes: 1}, nil
	})
	register := func(r *gatherRig) {
		for _, typ := range []string{"kiwix", "huggingface"} {
			if err := r.res.Register(typ, answer); err != nil {
				t.Fatal(err)
			}
		}
	}
	t.Run("online with resolving: the resolver wins; its failure leaves the item unresolved", func(t *testing.T) {
		calls.Store(0)
		r := newGatherRig(t)
		register(r)
		in, err := r.gatherer().Gather(context.Background(), ark.InternalID, plan.GatherOptions{Resolve: true})
		if err != nil {
			t.Fatal(err)
		}
		if got := in.Resolved["w-latest"]; got.Version != "2026-10" || got.SizeBytes != 77 {
			t.Errorf("w-latest = %+v", got)
		}
		if got := in.Resolved["m-pinned"]; got.Version != "from-resolver" {
			t.Errorf("m-pinned = %+v: the resolver's answer must replace the catalog pin", got)
		}
		if _, ok := in.Resolved["m-other"]; ok {
			t.Error("a failed resolver must leave the item unresolved, not fall back to the catalog")
		}
		if got := in.Resolved["docs-bundle"]; got.Version == "" {
			t.Error("an item with no resolver still takes the catalog's answer")
		}
		if calls.Load() != 3 {
			t.Errorf("resolver calls = %d, want 3", calls.Load())
		}
	})
	t.Run("without Resolve the resolvers are not called", func(t *testing.T) {
		calls.Store(0)
		r := newGatherRig(t)
		register(r)
		in, _ := r.gatherer().Gather(context.Background(), ark.InternalID, plan.GatherOptions{})
		if calls.Load() != 0 || in.Resolved["m-pinned"].Version != "rev-m-pinned" {
			t.Errorf("calls %d, m-pinned %+v", calls.Load(), in.Resolved["m-pinned"])
		}
	})
	t.Run("offline: the resolvers are not called either", func(t *testing.T) {
		calls.Store(0)
		r := newGatherRig(t)
		register(r)
		r.network = plan.Network{Mode: plan.ModeOnline, Online: false}
		in, _ := r.gatherer().Gather(context.Background(), ark.InternalID, plan.GatherOptions{Resolve: true})
		if calls.Load() != 0 || !in.Offline || in.Resolved["m-pinned"].Version != "rev-m-pinned" {
			t.Errorf("calls %d offline %v", calls.Load(), in.Offline)
		}
	})
	t.Run("a resolver that hangs is cut off by its timeout", func(t *testing.T) {
		r := newGatherRig(t)
		if err := r.res.Register("kiwix", plan.ResolverFunc(func(ctx context.Context, _ plan.CatalogItem) (plan.Resolved, error) {
			<-ctx.Done()
			return plan.Resolved{}, ctx.Err()
		})); err != nil {
			t.Fatal(err)
		}
		in, err := r.gatherer().Gather(context.Background(), ark.InternalID, plan.GatherOptions{Resolve: true, ResolveTimeout: 20 * time.Millisecond})
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := in.Resolved["w-latest"]; ok {
			t.Error("a timed-out resolver must leave the item unresolved")
		}
	})
}

// The time a resolver call may take is the option of the gather, else the gatherer's own (the app gives
// apt_closure minutes), else the package default.
func TestGatherResolveTimeoutPrecedence(t *testing.T) {
	left := func(r *gatherRig, opt plan.GatherOptions) time.Duration {
		t.Helper()
		var got time.Duration
		if err := r.res.Register("kiwix", plan.ResolverFunc(func(ctx context.Context, _ plan.CatalogItem) (plan.Resolved, error) {
			if dl, ok := ctx.Deadline(); ok {
				got = time.Until(dl)
			}
			return plan.Resolved{}, errors.New("no answer")
		})); err != nil {
			t.Fatal(err)
		}
		opt.Resolve = true
		if _, err := r.gatherer().Gather(context.Background(), ark.InternalID, opt); err != nil {
			t.Fatal(err)
		}
		return got
	}
	r := newGatherRig(t)
	if d := left(r, plan.GatherOptions{}); d <= 0 || d > plan.DefaultResolveTimeout {
		t.Errorf("default: %v left, want at most %v", d, plan.DefaultResolveTimeout)
	}
	r = newGatherRig(t)
	r.timeout = 3 * time.Hour
	if d := left(r, plan.GatherOptions{}); d < 2*time.Hour {
		t.Errorf("the gatherer's timeout: %v left, want about 3h", d)
	}
	r = newGatherRig(t)
	r.timeout = 3 * time.Hour
	if d := left(r, plan.GatherOptions{ResolveTimeout: time.Minute}); d <= 0 || d > time.Minute {
		t.Errorf("the option's timeout: %v left, want at most a minute", d)
	}
}

func TestResolversRegistry(t *testing.T) {
	r := plan.NewResolvers()
	fn := plan.ResolverFunc(func(context.Context, plan.CatalogItem) (plan.Resolved, error) { return plan.Resolved{}, nil })
	if err := r.Register("kiwix", fn); err != nil {
		t.Fatal(err)
	}
	if err := r.Register("kiwix", fn); err == nil {
		t.Error("a second resolver for one source type must be refused")
	}
	if err := r.Register("", fn); err == nil {
		t.Error("an empty source type must be refused")
	}
	if err := r.Register("url", nil); err == nil {
		t.Error("a nil resolver must be refused")
	}
	if _, ok := r.For("kiwix"); !ok {
		t.Error("kiwix is registered")
	}
	if _, ok := r.For("url"); ok {
		t.Error("url is not")
	}
	var nilReg *plan.Resolvers
	if _, ok := nilReg.For("kiwix"); ok {
		t.Error("a nil registry has no resolvers")
	}
}

// snapshot lists every path under dirs with its size and mod time, to prove a dry run wrote nothing.
func snapshot(t *testing.T, dirs ...string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, d := range dirs {
		err := filepath.WalkDir(d, func(p string, e fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			info, err := e.Info()
			if err != nil {
				return err
			}
			out[p] = info.Mode().String() + "|" + info.ModTime().String() + "|" + time.Duration(info.Size()).String()
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return out
}

func TestDryRunWritesNothing(t *testing.T) {
	r := newGatherRig(t)
	r.copy(ark.InternalID, "m-pinned", "v1", 10)
	writeFile(t, filepath.Join(r.internal, ".staging", "m-other", "v1", "x.part"), 9)
	writeFile(t, filepath.Join(r.internal, ".trash", "m-pinned", "v0", "a"), 4)
	writeFile(t, filepath.Join(r.drive, "models", "m-pinned", "MANIFEST.json"), 2)
	state := t.TempDir() // stands for /var/lib/localai/sync
	writeFile(t, filepath.Join(state, "items.json"), 8)
	before := snapshot(t, r.internal, r.drive, state)

	svc := plan.NewDryRun(r.gatherer())
	got, err := svc.DryRun(context.Background(), api.ArkSyncRequest{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	plans, ok := got.([]plan.RootPlan)
	if !ok || len(plans) != 2 || plans[0].Root != "internal" || plans[1].Root != "ark-3f9c0a12" {
		t.Fatalf("plans = %+v", got)
	}
	if after := snapshot(t, r.internal, r.drive, state); !equalMaps(before, after) {
		t.Errorf("a dry run changed the roots or the state directory:\nbefore %v\nafter  %v", before, after)
	}
}

func equalMaps(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func apiErr(t *testing.T, err error, status int, code string) *api.Error {
	t.Helper()
	var e *api.Error
	if !errors.As(err, &e) || e.Status != status || e.Code != code {
		t.Fatalf("err = %v, want %d %s", err, status, code)
	}
	return e
}

func TestDryRunRootSelection(t *testing.T) {
	ctx := context.Background()
	t.Run("default is every writable root, internal first", func(t *testing.T) {
		r := newGatherRig(t)
		r.roots[0], r.roots[1] = r.roots[1], r.roots[0] // a drive listed first
		got, err := plan.NewDryRun(r.gatherer()).DryRun(ctx, api.ArkSyncRequest{DryRun: true})
		if err != nil {
			t.Fatal(err)
		}
		if p := got.([]plan.RootPlan); len(p) != 2 || p[0].Root != "internal" {
			t.Errorf("plans = %v", rootsOf(p))
		}
	})
	t.Run("a read-only drive is left out by default but can be named", func(t *testing.T) {
		r := newGatherRig(t)
		r.roots[1].State = ark.StateReadOnly
		d := plan.NewDryRun(r.gatherer())
		got, _ := d.DryRun(ctx, api.ArkSyncRequest{DryRun: true})
		if p := got.([]plan.RootPlan); len(p) != 1 || p[0].Root != "internal" {
			t.Errorf("default = %v", rootsOf(p))
		}
		got, err := d.DryRun(ctx, api.ArkSyncRequest{DryRun: true, Roots: []string{"ark-3f9c0a12"}})
		if err != nil {
			t.Fatal(err)
		}
		if p := got.([]plan.RootPlan); len(p) != 1 || !p[0].ReadOnly {
			t.Errorf("named = %+v", p)
		}
	})
	t.Run("no writable root is LAI-ARK-001", func(t *testing.T) {
		r := newGatherRig(t)
		r.roots[0].ReadOnly = true
		r.roots[1].State = ark.StateUnclean
		_, err := plan.NewDryRun(r.gatherer()).DryRun(ctx, api.ArkSyncRequest{DryRun: true})
		apiErr(t, err, http.StatusConflict, doctor.ARK001)
	})
	t.Run("roots by id, by path and by mount, planned once", func(t *testing.T) {
		r := newGatherRig(t)
		got, err := plan.NewDryRun(r.gatherer()).DryRun(ctx, api.ArkSyncRequest{DryRun: true, Roots: []string{r.drive + "/", "ark-3f9c0a12", filepath.Dir(r.drive), "internal"}})
		if err != nil {
			t.Fatal(err)
		}
		if p := got.([]plan.RootPlan); len(p) != 2 || p[0].Root != "ark-3f9c0a12" || p[1].Root != "internal" {
			t.Errorf("plans = %v, want the request's order", rootsOf(p))
		}
	})
	t.Run("an unknown root is a 400", func(t *testing.T) {
		r := newGatherRig(t)
		_, err := plan.NewDryRun(r.gatherer()).DryRun(ctx, api.ArkSyncRequest{DryRun: true, Roots: []string{"/mnt/nowhere"}})
		e := apiErr(t, err, http.StatusBadRequest, api.CodeInvalidRequest)
		if e.Details["field"] != "roots" || e.Details["value"] != "/mnt/nowhere" {
			t.Errorf("details = %v", e.Details)
		}
	})
	t.Run("an unknown profile is a 422 LAI-CAT-003", func(t *testing.T) {
		r := newGatherRig(t)
		r.settings.Profile = "nope"
		_, err := plan.NewDryRun(r.gatherer()).DryRun(ctx, api.ArkSyncRequest{DryRun: true, Roots: []string{"internal"}})
		e := apiErr(t, err, http.StatusUnprocessableEntity, doctor.CAT003)
		if e.Details["profile"] != "nope" {
			t.Errorf("details = %v", e.Details)
		}
	})
	t.Run("a statfs failure is a plain error (500)", func(t *testing.T) {
		r := newGatherRig(t)
		r.statfs = func(string) (int64, int64, error) { return 0, 0, errors.New("io error") }
		_, err := plan.NewDryRun(r.gatherer()).DryRun(ctx, api.ArkSyncRequest{DryRun: true})
		var e *api.Error
		if err == nil || errors.As(err, &e) {
			t.Errorf("err = %v, want a plain error", err)
		}
	})
}

func rootsOf(p []plan.RootPlan) []string {
	var out []string
	for _, x := range p {
		out = append(out, x.Root)
	}
	return out
}

func TestDryRunItemsFilter(t *testing.T) {
	r := newGatherRig(t)
	d := plan.NewDryRun(r.gatherer())
	got, err := d.DryRun(context.Background(), api.ArkSyncRequest{DryRun: true, Roots: []string{"internal"}, Items: []string{"m-pinned"}})
	if err != nil {
		t.Fatal(err)
	}
	p := got.([]plan.RootPlan)[0]
	if len(p.Items) != 1 || p.Items[0].ID != "m-pinned" || p.Items[0].Pos != 2 {
		t.Errorf("items = %+v", p.Items)
	}
	if p.BudgetBytes != 1000 {
		t.Errorf("budget = %d: the whole plan's figures stay", p.BudgetBytes)
	}
	_, err = d.DryRun(context.Background(), api.ArkSyncRequest{DryRun: true, Items: []string{"ghost"}})
	e := apiErr(t, err, http.StatusBadRequest, api.CodeInvalidRequest)
	if e.Details["field"] != "items" || e.Details["value"] != "ghost" {
		t.Errorf("details = %v", e.Details)
	}
}

func TestDryRunOfflinePlansWithoutTheNetwork(t *testing.T) {
	r := newGatherRig(t)
	r.network = plan.Network{Mode: plan.ModeSurvival, Online: true}
	got, err := plan.NewDryRun(r.gatherer()).DryRun(context.Background(), api.ArkSyncRequest{DryRun: true, Roots: []string{"internal"}})
	if err != nil {
		t.Fatal(err)
	}
	p := got.([]plan.RootPlan)[0]
	if !p.Offline || p.Mode != plan.ModeSurvival {
		t.Fatalf("plan = %+v", p)
	}
	for _, it := range p.Items {
		if it.Action == plan.ActionDownload && it.Source == plan.SourceNetwork {
			t.Errorf("%s would use the network in survival mode", it.ID)
		}
	}
}

func TestOSStatfs(t *testing.T) {
	total, free, err := plan.OSStatfs(t.TempDir())
	if err != nil || total <= 0 || free < 0 || free > total {
		t.Errorf("OSStatfs = %d, %d, %v", total, free, err)
	}
	if _, _, err := plan.OSStatfs(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Error("a missing path must fail")
	}
}
