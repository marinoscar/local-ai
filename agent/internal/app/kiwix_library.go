package app

import (
	"context"
	"encoding/json"
	"path/filepath"

	"github.com/marinoscar/local-ai/agent/internal/events"
	"github.com/marinoscar/local-ai/agent/internal/execx"
	"github.com/marinoscar/local-ai/agent/internal/svc/kiwix"
)

// kiwixLibraryWorker keeps the merged and per-root Kiwix libraries current (companion-services.md
// section 4.1.2): it regenerates at start, after a verified sync item, at the end of a sync run, and
// when a drive is mounted or unmounted. The service coalesces the triggers (at most once per 10 s). It
// never starts or stops localai-kiwix: kiwix-serve reloads the file by itself.
type kiwixLibraryWorker struct {
	bus    *events.Bus
	svc    *kiwix.Service
	cancel context.CancelFunc
	done   chan struct{}
}

// Name implements Worker.
func (w *kiwixLibraryWorker) Name() string { return "kiwix-library" }

// Start implements Worker: it subscribes before it returns, then asks for the first regeneration.
func (w *kiwixLibraryWorker) Start(ctx context.Context) error {
	filter, err := events.ParseFilter(events.TypeSyncItemDone + "," + events.TypeSyncDone + "," +
		events.TypeDriveMounted + "," + events.TypeDriveUnmounted)
	if err != nil {
		return err
	}
	ctx, w.cancel = context.WithCancel(ctx)
	ch, err := w.bus.Subscribe(ctx, filter, 0)
	if err != nil {
		w.cancel()
		return err
	}
	w.done = make(chan struct{})
	loopDone := make(chan struct{})
	go func() { defer close(loopDone); w.svc.Run(ctx) }()
	go func() {
		defer close(w.done)
		defer func() { <-loopDone }()
		for ev := range ch {
			if ev.Type == events.TypeSyncItemDone {
				var d events.SyncItemDone
				if json.Unmarshal(ev.Data, &d) != nil || d.Result != "verified" {
					continue // only a new verified copy can change a library
				}
			}
			w.svc.Trigger()
		}
	}()
	w.svc.Trigger() // agent start
	return nil
}

// Stop implements Worker.
func (w *kiwixLibraryWorker) Stop(ctx context.Context) error {
	if w.cancel == nil {
		return nil
	}
	w.cancel()
	select {
	case <-w.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// kiwixLibraryWorker builds the worker over the stack's best-copy index and root list.
func (s *arkStack) kiwixLibraryWorker() *kiwixLibraryWorker {
	d := s.d
	lib := &kiwix.Library{
		Finder:   s.index,
		Roots:    s.roots.List,
		Runner:   execx.Exec{},
		Tool:     d.c.Paths.Join(filepath.Join(kiwix.DefaultRoot, kiwix.CurrentName, kiwix.BinManage)),
		StateDir: d.c.Paths.Join(kiwix.DefaultStateDir),
		Alerts:   d.c.Doctor,
		Log:      d.log,
	}
	if d.haveLocalAI && !d.env.Unprivileged {
		lib.Cred = &execx.Credential{UID: uint32(d.localaiUID), GID: uint32(d.localaiGID)}
	}
	svc := kiwix.NewService(lib)
	svc.Roots = s.roots.List
	svc.Log = d.log
	svc.Enabled = func() bool { return blockEnabled(d.c.Config.Get(), "kiwix") }
	return &kiwixLibraryWorker{bus: d.c.Bus, svc: svc}
}
