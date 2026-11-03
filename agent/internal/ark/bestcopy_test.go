package ark_test

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/ark"
	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/sync/manifest"
)

var bg = context.Background()

func rebuild(t testing.TB, ix *ark.Index, roots ...ark.Root) []ark.Problem {
	t.Helper()
	problems, err := ix.Rebuild(bg, roots)
	if err != nil {
		t.Fatal(err)
	}
	return problems
}

func best(t testing.TB, ix *ark.Index, item string) ark.Copy {
	t.Helper()
	c, err := ix.BestCopy(item)
	if err != nil {
		t.Fatalf("BestCopy(%s): %v", item, err)
	}
	return c
}

func withState(r ark.Root, s ark.RootState) ark.Root { r.State = s; return r }

func TestBestCopyNotPresent(t *testing.T) {
	ix := ark.NewIndex(config.OSFS{})
	if _, err := ix.BestCopy("anything"); !errors.Is(err, ark.ErrNotPresent) {
		t.Fatalf("empty index: %v", err)
	}
	internal := testRoot(t, ark.InternalID, 0)
	putItem(t, internal, llama("qwen-x", "v1", "2026-11-01"))
	rebuild(t, ix, internal)
	if _, err := ix.BestCopy("other"); !errors.Is(err, ark.ErrNotPresent) {
		t.Fatalf("unknown item: %v", err)
	}
	if got := ix.ItemIDs(); len(got) != 1 || got[0] != "qwen-x" {
		t.Fatalf("ItemIDs = %v", got)
	}
}

func TestBestCopyContent(t *testing.T) {
	internal := testRoot(t, ark.InternalID, 0)
	spec := itemSpec{ref: model("piper-es", "piper"), version: "p1", sortKey: "2026-11-01", files: map[string]string{"es.onnx": "p", "es.onnx.json": "{}"}}
	dir := putItem(t, internal, spec)
	docs := itemSpec{ref: ark.ItemRef{Kind: "docs", ID: "docs-bundle"}, version: "d1", sortKey: "2026-11-01", files: map[string]string{"index.html": "x"}, dirItem: true}
	docsDir := putItem(t, internal, docs)
	ix := ark.NewIndex(config.OSFS{})
	rebuild(t, ix, internal)

	c := best(t, ix, "piper-es")
	if c.RootID != "internal" || c.Dir != dir || c.Manifest.Version != "p1" {
		t.Fatalf("%+v", c)
	}
	if len(c.Files) != 2 || c.Files[0] != filepath.Join(dir, "es.onnx") || c.Files[1] != filepath.Join(dir, "es.onnx.json") || !filepath.IsAbs(c.Files[0]) {
		t.Fatalf("files: %v", c.Files)
	}
	d := best(t, ix, "docs-bundle")
	if d.Dir != docsDir || len(d.Files) != 0 || !d.Manifest.IsDir() {
		t.Fatalf("directory item: %+v", d)
	}

	// The caller gets its own slices.
	c.Files[0] = "scribbled"
	c.Manifest.Files[0].Path = "scribbled"
	if again := best(t, ix, "piper-es"); again.Files[0] == "scribbled" || again.Manifest.Files[0].Path == "scribbled" {
		t.Fatal("a returned Copy must not alias the index")
	}
}

func TestBestCopyPreference(t *testing.T) {
	type place struct {
		root          string // "internal", "A" or "B"
		version, sort string
	}
	// Mount ids: A is mounted before B. Roots are passed to Rebuild in reverse mount order on purpose.
	tests := []struct {
		name  string
		items []place
		want  place
	}{
		{"internal's version beats a newer drive version",
			[]place{{"internal", "v1", "2026-01"}, {"A", "v2", "2026-06"}}, place{"internal", "v1", "2026-01"}},
		{"internal's version is served from a drive when internal has it too: internal first",
			[]place{{"A", "v1", "2026-01"}, {"internal", "v1", "2026-01"}, {"B", "v1", "2026-01"}}, place{"internal", "v1", "2026-01"}},
		{"no internal copy: newest sort key wins",
			[]place{{"A", "v1", "2026-01"}, {"B", "v2", "2026-06"}}, place{"B", "v2", "2026-06"}},
		{"no internal copy: newest even when it is on the later drive and older is on the earlier",
			[]place{{"B", "v9", "2026-09"}, {"A", "v1", "2026-01"}}, place{"B", "v9", "2026-09"}},
		{"same version on two drives: mount order",
			[]place{{"B", "v1", "2026-01"}, {"A", "v1", "2026-01"}}, place{"A", "v1", "2026-01"}},
		{"sort key is a string comparison",
			[]place{{"A", "x", "2026-9"}, {"B", "y", "2026-10"}}, place{"A", "x", "2026-9"}},
		{"same sort key, different versions: the greater version, whatever the mount order",
			[]place{{"A", "aaa", "2026-01"}, {"B", "bbb", "2026-01"}}, place{"B", "bbb", "2026-01"}},
		{"only a drive has it",
			[]place{{"B", "v1", "2026-01"}}, place{"B", "v1", "2026-01"}},
		{"only internal has it",
			[]place{{"internal", "v1", "2026-01"}}, place{"internal", "v1", "2026-01"}},
		{"internal older, both drives newer and equal: still internal's",
			[]place{{"internal", "v1", "2026-01"}, {"A", "v2", "2026-06"}, {"B", "v2", "2026-06"}}, place{"internal", "v1", "2026-01"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			roots := map[string]ark.Root{"internal": testRoot(t, ark.InternalID, 0), "A": testRoot(t, "ark-aaaa0001", 10), "B": testRoot(t, "ark-bbbb0002", 20)}
			for _, p := range tc.items {
				putItem(t, roots[p.root], llama("qwen-x", p.version, p.sort))
			}
			ix := ark.NewIndex(config.OSFS{})
			rebuild(t, ix, roots["B"], roots["A"], roots["internal"])
			c := best(t, ix, "qwen-x")
			wantRoot := roots[tc.want.root].ID
			if c.RootID != wantRoot || c.Manifest.Version != tc.want.version {
				t.Fatalf("got %s %s, want %s %s", c.RootID, c.Manifest.Version, wantRoot, tc.want.version)
			}
		})
	}
}

func TestCopiesOrder(t *testing.T) {
	internal, a, b := testRoot(t, ark.InternalID, 0), testRoot(t, "ark-aaaa0001", 10), testRoot(t, "ark-bbbb0002", 20)
	putItem(t, internal, llama("qwen-x", "v2", "2026-02"))
	putItem(t, a, llama("qwen-x", "v3", "2026-03"))
	putItem(t, b, llama("qwen-x", "v1", "2026-01"))
	putItem(t, b, llama("only-b", "v1", "2026-01"))
	ix := ark.NewIndex(config.OSFS{})
	rebuild(t, ix, a, b, internal)
	var got []string
	for _, c := range ix.Copies("qwen-x") {
		got = append(got, c.Manifest.Version)
	}
	// internal's v2 first, then newest first: v3 (A), v1 (B).
	if len(got) != 3 || got[0] != "v2" || got[1] != "v3" || got[2] != "v1" {
		t.Fatalf("order %v", got)
	}
	if ix.Copies("missing") != nil {
		t.Fatal("no copies for an unknown item")
	}
	if ids := ix.ItemIDs(); len(ids) != 2 || ids[0] != "only-b" || ids[1] != "qwen-x" {
		t.Fatalf("ItemIDs %v", ids)
	}
}

func TestBestCopyRootStates(t *testing.T) {
	// The drive holds the newest version; internal holds none, so the drive is the best copy until
	// its state excludes it.
	tests := []struct {
		state   ark.RootState
		served  bool
		comment string
	}{
		{ark.StateReady, true, ""},
		{ark.StateReadOnly, true, "a read-only drive is served"},
		{ark.StateEjecting, false, "rule 1"},
		{ark.StateNotMounted, false, ""},
		{ark.StateUnusable, false, ""},
		{ark.StateUninitialized, false, ""},
	}
	for _, tc := range tests {
		t.Run(string(tc.state), func(t *testing.T) {
			drive := testRoot(t, "ark-aaaa0001", 10)
			putItem(t, drive, llama("qwen-x", "v1", "2026-01"))
			ix := ark.NewIndex(config.OSFS{})
			rebuild(t, ix, withState(drive, tc.state))
			_, err := ix.BestCopy("qwen-x")
			if tc.served != (err == nil) {
				t.Fatalf("served = %v, err %v", tc.served, err)
			}
		})
	}
}

func TestEjectingExcludesAtOnce(t *testing.T) {
	internal, drive := testRoot(t, ark.InternalID, 0), testRoot(t, "ark-aaaa0001", 10)
	putItem(t, internal, llama("qwen-x", "v1", "2026-01"))
	putItem(t, drive, llama("qwen-x", "v1", "2026-01"))
	putItem(t, drive, llama("drive-only", "v1", "2026-01"))
	ix := ark.NewIndex(config.OSFS{})
	rebuild(t, ix, internal, drive)
	if c := ix.Copies("qwen-x"); len(c) != 2 {
		t.Fatalf("%d copies", len(c))
	}
	if !ix.SetRootState(drive.ID, ark.StateEjecting) {
		t.Fatal("known root")
	}
	if c := ix.Copies("qwen-x"); len(c) != 1 || c[0].RootID != "internal" {
		t.Fatalf("after eject: %+v", c)
	}
	if _, err := ix.BestCopy("drive-only"); !errors.Is(err, ark.ErrNotPresent) {
		t.Fatalf("drive-only: %v", err)
	}
	// A refused eject puts it back without a rescan.
	ix.SetRootState(drive.ID, ark.StateReady)
	if c := ix.Copies("qwen-x"); len(c) != 2 {
		t.Fatalf("after the eject was refused: %d copies", len(c))
	}
	if ix.SetRootState("ark-unknown0", ark.StateReady) {
		t.Fatal("unknown root")
	}
}

func TestUncleanRootNeedsServeVerification(t *testing.T) {
	internal, drive := testRoot(t, ark.InternalID, 0), testRoot(t, "ark-aaaa0001", 10)
	// The unclean internal root holds v1; the clean drive holds v2.
	putItem(t, internal, llama("qwen-x", "v1", "2026-01"))
	putItem(t, drive, llama("qwen-x", "v2", "2026-06"))
	putItem(t, internal, llama("unclean-only", "v1", "2026-01"))
	unclean := withState(internal, ark.StateUnclean)
	ix := ark.NewIndex(config.OSFS{})
	rebuild(t, ix, unclean, drive)

	// Rule 4: unverified, the unclean copy does not exist, and its version does not count as the
	// internal root's version (rule 2), so the drive's newer one wins.
	if c := best(t, ix, "qwen-x"); c.RootID != drive.ID || c.Manifest.Version != "v2" {
		t.Fatalf("before verification: %s %s", c.RootID, c.Manifest.Version)
	}
	if _, err := ix.BestCopy("unclean-only"); !errors.Is(err, ark.ErrNotPresent) {
		t.Fatalf("unclean-only: %v", err)
	}

	// A mark for another version or another root changes nothing.
	ix.MarkServeVerified("internal", "qwen-x", "v-other")
	ix.MarkServeVerified(drive.ID, "unclean-only", "v1")
	if c := best(t, ix, "qwen-x"); c.RootID != drive.ID {
		t.Fatal("a mark for another version must not count")
	}

	ix.MarkServeVerified("internal", "qwen-x", "v1")
	if c := best(t, ix, "qwen-x"); c.RootID != "internal" || c.Manifest.Version != "v1" {
		t.Fatalf("after verification: %s %s", c.RootID, c.Manifest.Version)
	}

	// The mark survives a rebuild that leaves the copy alone.
	rebuild(t, ix, unclean, drive)
	if c := best(t, ix, "qwen-x"); c.RootID != "internal" {
		t.Fatal("the mark must survive a rebuild")
	}
	// ... and a refresh of some other item.
	if _, err := ix.Refresh(bg, unclean, "models/unclean-only"); err != nil {
		t.Fatal(err)
	}
	if c := best(t, ix, "qwen-x"); c.RootID != "internal" {
		t.Fatal("the mark must survive a refresh of another item")
	}

	// A swap installs a new version on the unclean root: it needs its own verification.
	putItem(t, internal, llama("qwen-x", "v3", "2026-09"))
	if _, err := ix.Refresh(bg, unclean, "models/qwen-x"); err != nil {
		t.Fatal(err)
	}
	if c := best(t, ix, "qwen-x"); c.RootID != drive.ID {
		t.Fatalf("a new version on an unclean root is unverified: %s %s", c.RootID, c.Manifest.Version)
	}
	ix.MarkServeVerified("internal", "qwen-x", "v3")
	if c := best(t, ix, "qwen-x"); c.Manifest.Version != "v3" {
		t.Fatalf("after verifying v3: %s", c.Manifest.Version)
	}

	// Unmounted and back: verified again from scratch.
	rebuild(t, ix, drive)
	rebuild(t, ix, unclean, drive)
	if c := best(t, ix, "qwen-x"); c.RootID != drive.ID {
		t.Fatal("a root that went away loses its marks")
	}

	// A clean root ignores marks and needs none.
	rebuild(t, ix, internal, drive)
	if c := best(t, ix, "qwen-x"); c.RootID != "internal" || c.Manifest.Version != "v3" {
		t.Fatalf("clean root: %s %s", c.RootID, c.Manifest.Version)
	}
}

func TestRebuildReportsInvalidManifests(t *testing.T) {
	internal := testRoot(t, ark.InternalID, 0)
	dir := putItem(t, internal, llama("qwen-x", "v1", "2026-01"))
	putItem(t, internal, llama("fine", "v1", "2026-01"))
	if err := os.Truncate(filepath.Join(dir, "qwen-x.gguf"), 1); err != nil {
		t.Fatal(err)
	}
	ix := ark.NewIndex(config.OSFS{})
	problems := rebuild(t, ix, internal)
	if len(problems) != 1 || problems[0].Code != doctor.SYN052 || problems[0].Details["dir"] != "models/qwen-x" {
		t.Fatalf("problems: %+v", problems)
	}
	if _, err := ix.BestCopy("qwen-x"); !errors.Is(err, ark.ErrNotPresent) {
		t.Fatal("a size mismatch makes the item missing")
	}
	if _, err := ix.BestCopy("fine"); err != nil {
		t.Fatal(err)
	}
}

func TestRefresh(t *testing.T) {
	internal := testRoot(t, ark.InternalID, 0)
	drive := testRoot(t, "ark-aaaa0001", 10)
	dir := putItem(t, internal, llama("qwen-x", "v1", "2026-01"))
	ix := ark.NewIndex(config.OSFS{})
	rebuild(t, ix, internal, drive)
	if best(t, ix, "qwen-x").Manifest.Version != "v1" {
		t.Fatal("setup")
	}

	t.Run("a swap installs a new version", func(t *testing.T) {
		putItem(t, internal, llama("qwen-x", "v2", "2026-02"))
		problems, err := ix.Refresh(bg, internal, "models/qwen-x")
		if err != nil || len(problems) != 0 {
			t.Fatalf("%v %v", err, problems)
		}
		if c := best(t, ix, "qwen-x"); c.Manifest.Version != "v2" || len(ix.Copies("qwen-x")) != 1 {
			t.Fatalf("%s, %d copies", c.Manifest.Version, len(ix.Copies("qwen-x")))
		}
	})
	t.Run("a new item appears", func(t *testing.T) {
		putItem(t, drive, llama("fresh", "v1", "2026-01"))
		if _, err := ix.Refresh(bg, drive, "models/fresh"); err != nil {
			t.Fatal(err)
		}
		if c := best(t, ix, "fresh"); c.RootID != drive.ID {
			t.Fatalf("%+v", c)
		}
	})
	t.Run("the manifest is renamed to trash: not present on that root", func(t *testing.T) {
		if err := os.Rename(filepath.Join(dir, manifest.FileName), filepath.Join(dir, "MANIFEST.json.trashed")); err != nil {
			t.Fatal(err)
		}
		problems, err := ix.Refresh(bg, internal, "models/qwen-x")
		if err != nil || len(problems) != 0 {
			t.Fatalf("a missing manifest is normal: %v %v", err, problems)
		}
		if _, err := ix.BestCopy("qwen-x"); !errors.Is(err, ark.ErrNotPresent) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("an invalid manifest removes the copy and reports LAI-SYN-052", func(t *testing.T) {
		putItem(t, internal, llama("qwen-x", "v3", "2026-03"))
		if _, err := ix.Refresh(bg, internal, "models/qwen-x"); err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(dir, manifest.FileName), "{")
		problems, err := ix.Refresh(bg, internal, "models/qwen-x")
		if err != nil || len(problems) != 1 || problems[0].Code != doctor.SYN052 {
			t.Fatalf("%v %v", err, problems)
		}
		if _, err := ix.BestCopy("qwen-x"); !errors.Is(err, ark.ErrNotPresent) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("a root that is ejecting is recorded, not read", func(t *testing.T) {
		before := ix.Copies("fresh")
		if _, err := ix.Refresh(bg, withState(drive, ark.StateEjecting), "models/fresh"); err != nil {
			t.Fatal(err)
		}
		if len(before) != 1 || len(ix.Copies("fresh")) != 0 {
			t.Fatal("an ejecting root must stop serving")
		}
	})
	t.Run("a cancelled context changes nothing", func(t *testing.T) {
		ctx, cancel := context.WithCancel(bg)
		cancel()
		if _, err := ix.Refresh(ctx, internal, "models/qwen-x"); !errors.Is(err, context.Canceled) {
			t.Fatalf("got %v", err)
		}
	})
}

// blockFS blocks every ReadDir of one directory until released, and says when one has started.
type blockFS struct {
	config.FS
	dir     string
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (b *blockFS) ReadDir(name string) ([]os.DirEntry, error) {
	if name == b.dir {
		b.once.Do(func() { close(b.entered) })
		<-b.release
		return nil, &fs.PathError{Op: "readdir", Path: name, Err: fs.ErrNotExist}
	}
	return b.FS.ReadDir(name)
}

func TestRebuildCancelKeepsTheOldIndex(t *testing.T) {
	internal, hung := testRoot(t, ark.InternalID, 0), testRoot(t, "ark-aaaa0001", 10)
	putItem(t, internal, llama("qwen-x", "v1", "2026-01"))

	bfs := &blockFS{FS: config.OSFS{}, dir: filepath.Join(hung.Path, "models"), entered: make(chan struct{}), release: make(chan struct{})}
	slow := ark.NewIndex(bfs)
	if _, err := slow.Rebuild(bg, []ark.Root{internal}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(bg)
	done := make(chan error, 1)
	go func() {
		_, err := slow.Rebuild(ctx, []ark.Root{internal, hung})
		done <- err
	}()
	<-bfs.entered // the hung drive's scan is in flight
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
	close(bfs.release)
	if _, err := slow.BestCopy("qwen-x"); err != nil {
		t.Fatalf("the old index must stay: %v", err)
	}
}

func TestIndexRaceFree(t *testing.T) {
	internal, drive := testRoot(t, ark.InternalID, 0), testRoot(t, "ark-aaaa0001", 10)
	putItem(t, internal, llama("qwen-x", "v1", "2026-01"))
	putItem(t, drive, llama("qwen-x", "v2", "2026-02"))
	ix := ark.NewIndex(config.OSFS{})
	rebuild(t, ix, internal, drive)

	var wg sync.WaitGroup
	for r := 0; r < 4; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				c, err := ix.BestCopy("qwen-x")
				if err != nil {
					t.Errorf("BestCopy: %v", err)
					return
				}
				c.Files[0] = "scribble" // our own copy
				ix.Copies("qwen-x")
				ix.ItemIDs()
			}
		}()
	}
	for w := 0; w < 2; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				switch (i + w) % 4 {
				case 0:
					if _, err := ix.Rebuild(bg, []ark.Root{internal, drive}); err != nil {
						t.Errorf("Rebuild: %v", err)
					}
				case 1:
					if _, err := ix.Refresh(bg, drive, "models/qwen-x"); err != nil {
						t.Errorf("Refresh: %v", err)
					}
				case 2:
					ix.SetRootState(drive.ID, ark.StateReady)
				default:
					ix.MarkServeVerified(drive.ID, "qwen-x", "v2")
				}
			}
		}(w)
	}
	wg.Wait()
}
