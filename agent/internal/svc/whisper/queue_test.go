package whisper

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestQueueAdmitsOneRunningAndTwoWaiting(t *testing.T) {
	q := NewQueue(0, -1)
	var leaves []func()
	for i := 0; i < 3; i++ {
		l, err := q.Enter()
		if err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
		leaves = append(leaves, l)
	}
	if _, err := q.Enter(); !errors.Is(err, ErrBusy) {
		t.Fatalf("4th request: err = %v, want ErrBusy", err)
	}
	leaves[0]()
	l, err := q.Enter()
	if err != nil {
		t.Fatalf("after a request left: %v", err)
	}
	l()
	for _, l := range leaves[1:] {
		l()
	}
	if n, _ := q.Stats(); n != 0 {
		t.Errorf("admitted = %d after everyone left", n)
	}
}

func TestQueueSlotIsExclusiveAndInOrderOfRelease(t *testing.T) {
	q := NewQueue(1, 2)
	waiting := make(chan struct{}, 1)
	q.onWait = func() { waiting <- struct{}{} }
	rel, err := q.Slot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := make(chan error, 1)
	go func() {
		r, err := q.Slot(context.Background())
		if err == nil {
			r()
		}
		got <- err
	}()
	<-waiting
	select {
	case <-got:
		t.Fatal("the second request ran while the first held the slot")
	case <-time.After(20 * time.Millisecond):
	}
	rel()
	if err := <-got; err != nil {
		t.Fatal(err)
	}
}

func TestQueueSlotEndsWithTheContext(t *testing.T) {
	q := NewQueue(1, 2)
	rel, _ := q.Slot(context.Background())
	defer rel()
	ctx, cancel := context.WithCancel(context.Background())
	waiting := make(chan struct{}, 1)
	q.onWait = func() { waiting <- struct{}{} }
	done := make(chan error, 1)
	go func() { _, err := q.Slot(ctx); done <- err }()
	<-waiting
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if _, in := q.Stats(); in != 1 {
		t.Errorf("in flight = %d, the first holder must still have the slot", in)
	}
	if _, err := q.Slot(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("a dead context must not take a slot: %v", err)
	}
}
