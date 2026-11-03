package run

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/ark"
	"github.com/marinoscar/local-ai/agent/internal/catalog"
	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/sync/download"
	"github.com/marinoscar/local-ai/agent/internal/sync/item"
	"github.com/marinoscar/local-ai/agent/internal/sync/journal"
	"github.com/marinoscar/local-ai/agent/internal/sync/manifest"
	"github.com/marinoscar/local-ai/agent/internal/sync/plan"
	"github.com/marinoscar/local-ai/agent/internal/sync/retention"
)

// A directory item (the rendered manuals) through the whole engine: the first root builds the tree with
// a registered resolver, the drive copies the tree file by file from the first root's SHA256SUMS after
// the first copy verified, and both end with the same tree hash. A second run that finds a newer
// version swaps the directory with renameat2(RENAME_EXCHANGE) and keeps the old tree in the trash.
func TestDirectoryItemThroughTheEngineWithALocalCopy(t *testing.T) {
	base := t.TempDir()
	internal := ark.Root{ID: ark.InternalID, Kind: ark.KindInternal, Path: filepath.Join(base, "srv"), State: ark.StateReady}
	drive := ark.Root{ID: driveID, Kind: ark.KindArkDrive, Path: filepath.Join(base, "drive", "localai"), Mount: filepath.Join(base, "drive"), State: ark.StateReady, MountID: 7}
	for _, r := range []ark.Root{internal, drive} {
		if err := os.MkdirAll(r.Path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	roots := func() []ark.Root { return []ark.Root{internal, drive} }
	clk := newFakeClock()
	fsys := item.OSFS{}
	jr, _, err := journal.Open(bg, journal.Options{Dir: filepath.Join(base, "var"), Now: clk.Now, Logger: quiet})
	if err != nil {
		t.Fatal(err)
	}
	idx := ark.NewIndex(fsys)
	if _, err := idx.Rebuild(bg, roots()); err != nil {
		t.Fatal(err)
	}
	ret, _ := retention.New(retention.Deps{FS: retention.OSFS{}, Roots: roots, Copies: idx, Journal: jr.Items, Index: idx, Items: jr.Items, Now: clk.Now, Logger: quiet})
	dl := download.New(download.Options{HTTP: download.NewHTTPClient(download.ClientConfig{}), Space: ret, Logger: quiet})
	machine, _ := item.New(item.Deps{FS: fsys, Journal: jr, Fetcher: dl, Index: idx, Remover: ret, Roots: roots, Now: clk.Now, NodeID: "ai-test", AgentVersion: "0.0.0", Logger: quiet})

	version := "catalog-1"
	content := map[string][]byte{"index.html": data(2000, 1), "guide/a.txt": data(70_000, 2), "guide/deep/b.txt": data(33, 3)}
	resolvers := NewItemResolvers()
	if err := resolvers.Register("repo_docs", ItemResolverFunc(func(ctx context.Context, it plan.CatalogItem, target plan.ItemPlan) (item.Resolution, error) {
		return item.Resolution{
			Version: version, SortKey: version, Source: manifest.Source{Type: "repo_docs"}, Trust: manifest.TrustComputed, Dir: true, Resolver: "repo_docs",
			Build: func(ctx context.Context, tree string) error {
				for name, b := range content {
					p := filepath.Join(tree, filepath.FromSlash(name))
					if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
						return err
					}
					if err := os.WriteFile(p, b, 0o600); err != nil {
						return err
					}
				}
				return nil
			},
		}, nil
	})); err != nil {
		t.Fatal(err)
	}
	docsItem := plan.ContentItem(&catalog.ContentItem{ID: "docs", Kind: "docs", Source: catalog.ContentSource{Type: "repo_docs"}})
	rowFirst := plan.ItemPlan{ID: "docs", Kind: "docs", Action: plan.ActionDownload, ToVersion: version, NeedBytes: 72_033, Source: plan.SourceNetwork}
	rowCopy := rowFirst
	rowCopy.Source, rowCopy.FromRoot, rowCopy.After = plan.SourceLocalCopy, ark.InternalID, ark.InternalID
	planner := &fakePlanner{roots: roots(), items: map[string]plan.CatalogItem{"docs": docsItem}, plans: []plan.RootPlan{
		{Root: ark.InternalID, Items: []plan.ItemPlan{rowFirst}, MinFreeBytes: 1},
		{Root: driveID, Items: []plan.ItemPlan{rowCopy}, MinFreeBytes: 1},
	}}
	eng, err := New(Deps{
		Journal: jr, Machine: machine, Planner: planner, Copies: idx, FS: config.OSFS{}, JournalDir: filepath.Join(base, "var"),
		Settings: DefaultSettings, Clock: clk, Logger: quiet, Priority: func(bool) error { return nil },
		Lock: FileLock{Path: filepath.Join(base, "lock")}, Retention: ret, Resolvers: resolvers,
		Unknown: func(r ark.Root) ([]string, error) { return jr.Items.UnknownStaging(config.OSFS{}, r) },
	})
	if err != nil {
		t.Fatal(err)
	}
	sync := func() RunStatus {
		got, err := eng.Start(bg, StartRequest{Trigger: TriggerManual})
		if err != nil {
			t.Fatal(err)
		}
		if err := eng.Wait(bg, got.RunID); err != nil {
			t.Fatal(err)
		}
		st, _ := eng.Status()
		return st
	}
	if st := sync(); st.State != StateDone || st.Totals.Done != 2 || st.Totals.Failed != 0 {
		t.Fatalf("first run = %+v", st)
	}
	var trees []string
	for _, r := range []ark.Root{internal, drive} {
		it, err := ark.LoadItem(fsys, r, "docs")
		if err != nil {
			t.Fatalf("%s: %v", r.ID, err)
		}
		trees = append(trees, it.Manifest.TreeSHA256)
		for name, b := range content {
			got, err := os.ReadFile(filepath.Join(r.Path, "docs", filepath.FromSlash(name)))
			if err != nil || !bytes.Equal(got, b) {
				t.Fatalf("%s/%s: %v", r.ID, name, err)
			}
		}
		if it.Manifest.FileCount != 3 || it.Manifest.Version != version {
			t.Fatalf("%s manifest = %+v", r.ID, it.Manifest)
		}
		if left, _ := os.ReadDir(filepath.Join(r.Path, ".staging")); len(left) != 0 {
			t.Fatalf("%s: staging is not clean", r.ID)
		}
	}
	if trees[0] != trees[1] {
		t.Fatalf("the two roots disagree on the tree hash: %v", trees)
	}

	// A newer version of the manuals: the first root swaps its directory (an exchange) and the old tree is
	// kept in the trash.
	version = "catalog-2"
	content["index.html"] = data(2100, 4)
	content["guide/new.txt"] = data(50, 5)
	delete(content, "guide/a.txt")
	planner.mu.Lock()
	planner.plans = []plan.RootPlan{{Root: ark.InternalID, Items: []plan.ItemPlan{{ID: "docs", Kind: "docs", Action: plan.ActionUpdate, ToVersion: version, NeedBytes: 2183, Source: plan.SourceNetwork}}, MinFreeBytes: 1}}
	planner.mu.Unlock()
	if st := sync(); st.State != StateDone || st.Totals.Done != 1 {
		t.Fatalf("second run = %+v", st)
	}
	it, err := ark.LoadItem(fsys, internal, "docs")
	if err != nil || it.Manifest.Version != "catalog-2" || len(it.Manifest.History) != 1 || it.Manifest.History[0].Version != "catalog-1" {
		t.Fatalf("updated manifest = %+v, %v", it.Manifest, err)
	}
	if _, err := os.Stat(filepath.Join(internal.Path, "docs", "guide", "a.txt")); err == nil {
		t.Fatal("a file of the old tree is still in the new one")
	}
	old, err := os.ReadFile(filepath.Join(internal.Path, ".trash", "docs", "catalog-1", "tree", "guide", "a.txt"))
	if err != nil || len(old) != 70_000 {
		t.Fatalf("the old tree is not in the trash: %v", err)
	}
	if m, err := manifest.Read(fsys, filepath.Join(internal.Path, ".trash", "docs", "catalog-1", "MANIFEST.json")); err != nil || m.Version != "catalog-1" {
		t.Fatalf("the old manifest is not in the trash: %v", err)
	}
	// The drive still has the old, complete tree: it was not part of the second run.
	if dm, err := ark.LoadItem(fsys, drive, "docs"); err != nil || dm.Manifest.Version != "catalog-1" {
		t.Fatalf("drive = %+v, %v", dm.Manifest, err)
	}
}
