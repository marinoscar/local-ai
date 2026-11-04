package app

import (
	"context"
	"encoding/json"

	"github.com/marinoscar/local-ai/agent/internal/events"
	"github.com/marinoscar/local-ai/agent/internal/maps/places"
)

// placesWorker keeps the in-memory place index current (companion-services.md section 4.4.4): it
// builds it in the background at agent start, and again after a verified sync of a places item or
// when a drive is mounted or unmounted (the best copy may have changed). Searches answer
// 503 {"state":"building"} until the first build is done; a rebuild keeps serving the old index until
// the new one is swapped in.
type placesWorker struct {
	d      *Daemon
	cancel context.CancelFunc
	done   chan struct{}
}

// Name implements Worker.
func (w *placesWorker) Name() string { return "place-index" }

// Start implements Worker: it subscribes before it returns, then builds in a goroutine.
func (w *placesWorker) Start(ctx context.Context) error {
	filter, err := events.ParseFilter(events.TypeSyncItemDone + "," + events.TypeDriveMounted + "," + events.TypeDriveUnmounted)
	if err != nil {
		return err
	}
	ctx, w.cancel = context.WithCancel(ctx)
	evs, err := w.d.c.Bus.Subscribe(ctx, filter, 0)
	if err != nil {
		w.cancel()
		return err
	}
	w.done = make(chan struct{})
	go func() {
		defer close(w.done)
		w.d.places.Rebuild(ctx) // agent start
		for ev := range evs {
			if ev.Type == events.TypeSyncItemDone {
				var d events.SyncItemDone
				if json.Unmarshal(ev.Data, &d) != nil || d.Result != "verified" || !places.IsPlacesItem(d.Item) {
					continue
				}
			}
			w.d.places.Rebuild(ctx)
		}
	}()
	return nil
}

// Stop implements Worker.
func (w *placesWorker) Stop(ctx context.Context) error {
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
