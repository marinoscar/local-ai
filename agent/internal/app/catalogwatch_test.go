package app

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/catalog"
	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/statusapi"
)

// copyRepoCatalog copies the repository's catalog/ (a real, valid catalog) into a fresh folder.
func copyRepoCatalog(t *testing.T) string {
	t.Helper()
	src := filepath.Join("..", "..", "..", "catalog")
	dst := filepath.Join(t.TempDir(), "catalog")
	err := filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, b, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	return dst
}

// setVersion rewrites CATALOG_VERSION and moves its mtime forward, as Install does when it writes it last.
func setVersion(t *testing.T, dir, version string, at time.Time) {
	t.Helper()
	p := filepath.Join(dir, catalog.FileVersion)
	if err := os.WriteFile(p, []byte(version+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, at, at); err != nil {
		t.Fatal(err)
	}
}

func newTestReloader(t *testing.T, dir string) (*catalogReloader, *statusapi.CatalogStore, *int) {
	t.Helper()
	cat, err := catalog.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	store := statusapi.NewCatalogStore(catalog.Loaded{Catalog: cat, Source: catalog.SourceOffline, Dir: dir}, statusapi.InstalledAt(dir))
	regens := 0
	r := newCatalogReloader(dir, store, func(context.Context) { regens++ }, slog.New(slog.NewTextHandler(io.Discard, nil)))
	return r, store, &regens
}

// Issue #489: a catalog installed on disk (by a sync run's refresh, `catalog use --test`, `catalog restore`
// or an import) is in use after the next check, and only a real change is reloaded.
func TestCatalogReloader(t *testing.T) {
	base := time.Now().Add(time.Hour).Truncate(time.Second)
	cases := []struct {
		name       string
		change     func(t *testing.T, dir string)
		reloaded   bool
		version    string // "" = the starting version
		source     catalog.Source
		regenCalls int
	}{
		{name: "nothing changed", change: func(*testing.T, string) {}, reloaded: false, source: catalog.SourceOffline},
		{
			name:     "a new version installed on disk is used, presets regenerated",
			change:   func(t *testing.T, dir string) { setVersion(t, dir, "2099.01.01-1", base) },
			reloaded: true, version: "2099.01.01-1", source: catalog.SourceOffline, regenCalls: 1,
		},
		{
			name: "a test catalog (TEST marker) is reported as test",
			change: func(t *testing.T, dir string) {
				if err := os.WriteFile(filepath.Join(dir, catalog.FileTestMarker), nil, 0o644); err != nil {
					t.Fatal(err)
				}
				setVersion(t, dir, "2099.01.01-2", base)
			},
			reloaded: true, version: "2099.01.01-2", source: catalog.SourceTest, regenCalls: 1,
		},
		{
			name: "the same version rewritten is reloaded without regenerating",
			change: func(t *testing.T, dir string) {
				b, _ := os.ReadFile(filepath.Join(dir, catalog.FileVersion))
				setVersion(t, dir, strings.TrimSpace(string(b)), base)
			},
			reloaded: true, source: catalog.SourceOffline, regenCalls: 0,
		},
		{
			name: "a catalog that does not load keeps the current one",
			change: func(t *testing.T, dir string) {
				if err := os.WriteFile(filepath.Join(dir, "ark.yaml"), []byte("content: [\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				setVersion(t, dir, "2099.01.01-3", base)
			},
			reloaded: false, source: catalog.SourceOffline, regenCalls: 0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := copyRepoCatalog(t)
			r, store, regens := newTestReloader(t, dir)
			start := store.Get().Catalog.Version
			tc.change(t, dir)
			if got := r.check(context.Background()); got != tc.reloaded {
				t.Fatalf("check() = %v, want %v", got, tc.reloaded)
			}
			want := tc.version
			if want == "" {
				want = start
			}
			cur := store.Get()
			if cur.Catalog.Version != want || cur.Source != tc.source {
				t.Fatalf("store has %s (%s), want %s (%s)", cur.Catalog.Version, cur.Source, want, tc.source)
			}
			if *regens != tc.regenCalls {
				t.Fatalf("presets regenerated %d times, want %d", *regens, tc.regenCalls)
			}
			// A second look without a new change does nothing (also after a folder that failed to load).
			if r.check(context.Background()) {
				t.Fatal("a second check without a change reloaded again")
			}
		})
	}
}

// A broken catalog is retried once it changes again, and the fixed one is then used.
func TestCatalogReloaderRecoversAfterABrokenInstall(t *testing.T) {
	dir := copyRepoCatalog(t)
	r, store, _ := newTestReloader(t, dir)
	good, _ := os.ReadFile(filepath.Join(dir, "ark.yaml"))
	if err := os.WriteFile(filepath.Join(dir, "ark.yaml"), []byte("content: [\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	base := time.Now().Add(time.Hour).Truncate(time.Second)
	setVersion(t, dir, "2099.02.01-1", base)
	if r.check(context.Background()) {
		t.Fatal("a broken catalog was loaded")
	}
	if err := os.WriteFile(filepath.Join(dir, "ark.yaml"), good, 0o644); err != nil {
		t.Fatal(err)
	}
	setVersion(t, dir, "2099.02.01-2", base.Add(time.Second))
	if !r.check(context.Background()) || store.Get().Catalog.Version != "2099.02.01-2" {
		t.Fatalf("the repaired catalog was not used: %s", store.Get().Catalog.Version)
	}
}

// The worker checks on every tick and stops cleanly.
func TestCatalogWatcherTicksAndStops(t *testing.T) {
	dir := copyRepoCatalog(t)
	r, store, _ := newTestReloader(t, dir)
	clock := newTickClock()
	w := &catalogWatcher{r: r, clock: clock}
	if err := w.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	setVersion(t, dir, "2099.03.01-1", time.Now().Add(time.Hour).Truncate(time.Second))
	clock.tick()
	deadline := time.Now().Add(5 * time.Second)
	for store.Get().Catalog.Version != "2099.03.01-1" {
		if time.Now().After(deadline) {
			t.Fatal("the watcher did not reload on its tick")
		}
		time.Sleep(10 * time.Millisecond)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := w.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if clock.interval() != catalogWatchEvery {
		t.Fatalf("ticker interval %v, want %v", clock.interval(), catalogWatchEvery)
	}
}

// tickClock is a config.Clock whose single ticker fires only when the test calls tick.
type tickClock struct {
	ch    chan time.Time
	every chan time.Duration
	last  time.Duration
}

func newTickClock() *tickClock {
	return &tickClock{ch: make(chan time.Time), every: make(chan time.Duration, 1)}
}

func (c *tickClock) Now() time.Time { return time.Now() }

func (c *tickClock) NewTicker(d time.Duration) config.Ticker {
	c.every <- d
	return tickerOf{c.ch}
}

func (c *tickClock) tick() { c.ch <- time.Now() }

func (c *tickClock) interval() time.Duration {
	select {
	case d := <-c.every:
		c.last = d
	default:
	}
	return c.last
}

type tickerOf struct{ ch chan time.Time }

func (t tickerOf) C() <-chan time.Time { return t.ch }
func (tickerOf) Stop()                 {}

// bundleGitHub serves the rig's own catalog under another version, as catalog-stable would after a release.
type bundleGitHub struct {
	dir     string
	version string
}

func (g bundleGitHub) FetchCatalog(context.Context, string) (*catalog.Bundle, error) {
	b := &catalog.Bundle{Version: g.version}
	err := filepath.Walk(g.dir, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(g.dir, p)
		content, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if rel == catalog.FileVersion {
			content = []byte(g.version + "\n")
		}
		b.Files = append(b.Files, catalog.BundleFile{Path: filepath.ToSlash(rel), Content: string(content)})
		return nil
	})
	return b, err
}

// Issue #489: the refresh at the start of a sync run must put the new catalog into the store, so the plan
// of the same run (and the API) use it, not only the files on disk.
func TestSyncRunCatalogRefreshIsUsedAtOnce(t *testing.T) {
	// The rig's installed catalog (its fixture with the placeholders filled in), served as a newer version.
	r := newRig(t, func(r *rig) {
		r.env.CatalogGitHub = bundleGitHub{dir: r.path("etc/localai/catalog"), version: "2099.04.01-1"}
	})
	d := r.start()
	before := d.c.Catalog.Get().Catalog.Version
	if before == "2099.04.01-1" {
		t.Fatal("the rig already starts with the new version")
	}
	if err := (catalogRefresh{d}).Refresh(context.Background()); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if got := d.c.Catalog.Get().Catalog.Version; got != "2099.04.01-1" {
		t.Fatalf("after the sync run's refresh the agent uses %s, want 2099.04.01-1 (was %s)", got, before)
	}
}
