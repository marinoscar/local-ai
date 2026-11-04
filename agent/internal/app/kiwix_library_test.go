package app

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/events"
	"github.com/marinoscar/local-ai/agent/internal/svc/kiwix"
)

type libCounter struct{ n atomic.Int32 }

func (c *libCounter) Regenerate(context.Context) (kiwix.Result, error) {
	c.n.Add(1)
	return kiwix.Result{}, nil
}
func (c *libCounter) RegenerateRoot(context.Context, string) (kiwix.RootResult, error) {
	return kiwix.RootResult{}, nil
}

func waitRuns(t *testing.T, c *libCounter, want int32) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if c.n.Load() >= want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("regenerations: got %d, want %d", c.n.Load(), want)
}

func TestKiwixLibraryWorkerTriggers(t *testing.T) {
	bus := events.New(events.Options{Node: "n"})
	c := &libCounter{}
	svc := kiwix.NewService(c)
	svc.Settle = time.Millisecond
	svc.Interval = time.Millisecond
	w := &kiwixLibraryWorker{bus: bus, svc: svc}
	if err := w.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Stop(context.Background()) }()
	waitRuns(t, c, 1) // agent start

	// A skipped item changes nothing.
	_ = bus.Publish(events.TypeSyncItemDone, "", events.SyncItemDone{Root: "internal", Item: "x", Result: "skipped"})
	time.Sleep(60 * time.Millisecond)
	if c.n.Load() != 1 {
		t.Fatalf("a skipped item must not regenerate, runs %d", c.n.Load())
	}
	_ = bus.Publish(events.TypeSyncItemDone, "", events.SyncItemDone{Root: "internal", Item: "x", Result: "verified"})
	waitRuns(t, c, 2)
	_ = bus.Publish(events.TypeDriveMounted, "", events.DriveMounted{RootID: "ark-0000aaaa", Mount: "/m", State: "ok"})
	waitRuns(t, c, 3)
	_ = bus.Publish(events.TypeDriveUnmounted, "", events.DriveUnmounted{RootID: "ark-0000aaaa", SafeToUnplug: true})
	waitRuns(t, c, 4)
}
