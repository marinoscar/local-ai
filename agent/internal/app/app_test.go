package app

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"reflect"
	"sync"
	"syscall"
	"testing"
	"time"
)

type rec struct {
	mu  sync.Mutex
	log []string
}

func (r *rec) add(s string) { r.mu.Lock(); r.log = append(r.log, s); r.mu.Unlock() }
func (r *rec) get() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.log...)
}

type fakeWorker struct {
	name     string
	r        *rec
	startErr error
	stopErr  error
	stopHang bool
	ctxCh    chan context.Context // receives the context Start was given
}

func (w *fakeWorker) Name() string { return w.name }
func (w *fakeWorker) Start(ctx context.Context) error {
	w.r.add("start " + w.name)
	if w.ctxCh != nil {
		w.ctxCh <- ctx
	}
	return w.startErr
}
func (w *fakeWorker) Stop(ctx context.Context) error {
	w.r.add("stop " + w.name)
	if w.stopHang {
		<-make(chan struct{}) // never returns: must be abandoned after the budget
	}
	return w.stopErr
}

func waitStarts(t *testing.T, r *rec, n int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		c := 0
		for _, e := range r.get() {
			if len(e) > 5 && e[:5] == "start" {
				c++
			}
		}
		if c >= n {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("workers did not start: %v", r.get())
}

func quiet() Option { return WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))) }

func TestStartOrderAndReverseStopOrder(t *testing.T) {
	r := &rec{}
	a := New(quiet())
	for _, n := range []string{"a", "b", "c"} {
		a.Register(&fakeWorker{name: n, r: r})
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	waitStarts(t, r, 3)
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}
	want := []string{"start a", "start b", "start c", "stop c", "stop b", "stop a"}
	if got := r.get(); !reflect.DeepEqual(got, want) {
		t.Errorf("order = %v, want %v", got, want)
	}
}

func TestStartFailureStopsAlreadyStartedWorkers(t *testing.T) {
	r := &rec{}
	boom := errors.New("boom")
	a := New(quiet())
	a.Register(&fakeWorker{name: "a", r: r})
	a.Register(&fakeWorker{name: "b", r: r})
	a.Register(&fakeWorker{name: "c", r: r, startErr: boom})
	a.Register(&fakeWorker{name: "d", r: r})
	err := a.Run(context.Background())
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want %v", err, boom)
	}
	want := []string{"start a", "start b", "start c", "stop b", "stop a"}
	if got := r.get(); !reflect.DeepEqual(got, want) {
		t.Errorf("order = %v, want %v (c failed so it is not stopped; d never starts)", got, want)
	}
}

func TestWorkersShareTheRootContextAndItIsCancelledOnShutdown(t *testing.T) {
	r := &rec{}
	w := &fakeWorker{name: "a", r: r, ctxCh: make(chan context.Context, 1)}
	a := New(quiet())
	a.Register(w)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	wctx := <-w.ctxCh
	if wctx.Err() != nil {
		t.Fatal("worker context cancelled before shutdown")
	}
	cancel()
	<-done
	if wctx.Err() == nil {
		t.Error("worker context should be cancelled after shutdown")
	}
}

func TestStopFailureAndHangDoNotBlockShutdown(t *testing.T) {
	r := &rec{}
	a := New(quiet(), WithStopBudget(100*time.Millisecond))
	a.Register(&fakeWorker{name: "a", r: r})
	a.Register(&fakeWorker{name: "hang", r: r, stopHang: true})
	a.Register(&fakeWorker{name: "err", r: r, stopErr: errors.New("nope")})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	waitStarts(t, r, 3)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run = %v, want nil after a best-effort shutdown", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("shutdown blocked on a hanging worker")
	}
	want := []string{"start a", "start hang", "start err", "stop err", "stop hang", "stop a"}
	if got := r.get(); !reflect.DeepEqual(got, want) {
		t.Errorf("order = %v, want %v (every worker is still asked to stop)", got, want)
	}
}

func TestSIGTERMCancelsTheSignalContext(t *testing.T) {
	ctx, stop := SignalContext(context.Background())
	defer stop()
	if err := syscall.Kill(syscall.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("SIGTERM did not cancel the context")
	}
}

func TestVersionDefaults(t *testing.T) {
	if Version != "0.0.0-dev" {
		t.Errorf("default Version = %q", Version)
	}
}
