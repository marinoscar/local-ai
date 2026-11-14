package app

import (
	"context"
	"log/slog"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/catalog"
	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/statusapi"
)

// catalogWatchEvery is how often the installed catalog's CATALOG_VERSION mtime is checked (agent.md
// section 3.3: "re-read when CATALOG_VERSION mtime changes, checked every 10 s").
const catalogWatchEvery = 10 * time.Second

// catalogReloader puts a catalog that appeared in /etc/localai/catalog/ into the store every reader looks
// at. Catalog.Install writes CATALOG_VERSION last, so its mtime changes once per install, whoever did it:
// the refresh at the start of a sync run, `aictl catalog use --test`, `aictl catalog restore`, an import
// from a drive. Issue #489: nothing re-read the folder, so a catalog installed by a sync run's refresh
// stayed on disk while the agent kept planning with the old one (new items were "unknown").
type catalogReloader struct {
	dir   string
	store *statusapi.CatalogStore
	// regen regenerates the router presets after a version change; nil skips it.
	regen func(ctx context.Context)
	log   *slog.Logger
	// last is the mtime already in the store; a folder that failed to load is not retried until it changes.
	last time.Time
}

func newCatalogReloader(dir string, store *statusapi.CatalogStore, regen func(context.Context), log *slog.Logger) *catalogReloader {
	return &catalogReloader{dir: dir, store: store, regen: regen, log: log, last: statusapi.InstalledAt(dir)}
}

// check reloads the folder when CATALOG_VERSION changed since the last look and reports whether it did.
// A folder that does not load keeps the current catalog (LAI-CAT-001 is logged once per change).
func (r *catalogReloader) check(ctx context.Context) bool {
	m := statusapi.InstalledAt(r.dir)
	if m.IsZero() || m.Equal(r.last) {
		return false
	}
	r.last = m
	cat, err := catalog.Load(r.dir)
	if err != nil {
		r.log.Warn("the installed catalog changed but does not load; the current one stays in use",
			"code", catalog.CodeUnparseable, "dir", r.dir, "error", err.Error())
		return false
	}
	src := catalog.SourceOffline
	if catalog.IsTestCatalog(r.dir) {
		src = catalog.SourceTest
	}
	prev := r.store.Get()
	r.store.Set(catalog.Loaded{Catalog: cat, Source: src, Dir: r.dir}, m)
	if cat.Version == prev.Catalog.Version && src == prev.Source {
		return true
	}
	r.log.Info("the agent now uses the installed catalog", "version", cat.Version, "previous", prev.Catalog.Version, "source", string(src))
	if r.regen != nil && cat.Version != prev.Catalog.Version {
		r.regen(ctx)
	}
	return true
}

// catalogWatcher runs a catalogReloader every catalogWatchEvery.
type catalogWatcher struct {
	r      *catalogReloader
	clock  config.Clock
	cancel context.CancelFunc
	done   chan struct{}
}

// Name implements Worker.
func (w *catalogWatcher) Name() string { return "catalog-watch" }

// Start implements Worker.
func (w *catalogWatcher) Start(ctx context.Context) error {
	ctx, w.cancel = context.WithCancel(ctx)
	w.done = make(chan struct{})
	t := w.clock.NewTicker(catalogWatchEvery)
	go func() {
		defer close(w.done)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C():
				w.r.check(ctx)
			}
		}
	}()
	return nil
}

// Stop implements Worker.
func (w *catalogWatcher) Stop(ctx context.Context) error {
	if w.cancel == nil {
		return nil
	}
	w.cancel()
	select {
	case <-w.done:
	case <-ctx.Done():
		return ctx.Err()
	}
	return nil
}

// regenerateRouter is the regen of the reloader and of the sync run's refresh: the router presets follow
// the catalog; a problem is logged, never returned (the same rule as POST /catalog/refresh).
func (d *Daemon) regenerateRouter(ctx context.Context) {
	if d.sdeps.Regenerator == nil {
		return
	}
	for _, w := range statusapi.Regenerate(ctx, d.sdeps.Regenerator) {
		d.log.Warn("the router presets could not follow the new catalog", "code", w.Code, "message", w.Message)
	}
}
