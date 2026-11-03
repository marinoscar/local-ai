package router

import (
	"context"
	"errors"
	"net/http"
	"testing"
)

func TestRestartWithPrepareRunsUnderTheLockBeforeTheRestart(t *testing.T) {
	r := newOrRig(t)
	r.restarting()
	r.load("m-a")
	var kind, opID string
	var restartsDuring int
	called := 0
	res, err := r.o.RestartWith(bg, RestartRequest{Actor: sysActor, Prepare: func(_ context.Context, id string) error {
		called++
		opID = id
		restartsDuring = r.rs.calls()
		if h, ok := r.o.Lock().Holder(); ok {
			kind = h.Kind
		}
		return nil
	}})
	if err != nil {
		t.Fatalf("RestartWith: %v", err)
	}
	if called != 1 || kind != HolderRestart || restartsDuring != 0 || r.rs.calls() != 1 {
		t.Errorf("prepare calls %d, holder %q, restarts during prepare %d, restarts after %d", called, kind, restartsDuring, r.rs.calls())
	}
	if opID == "" || opID != res.OpID {
		t.Errorf("prepare saw op %q, the result has %q", opID, res.OpID)
	}
	if _, held := r.o.Lock().Holder(); held {
		t.Error("the lock is still held")
	}
}

func TestRestartWithPrepareFailureStopsBeforeTheRestart(t *testing.T) {
	r := newOrRig(t)
	r.restarting()
	boom := errors.New("the key file could not be written")
	_, err := r.o.RestartWith(bg, RestartRequest{Actor: sysActor, Prepare: func(context.Context, string) error { return boom }})
	if !errors.Is(err, boom) {
		t.Fatalf("error = %v", err)
	}
	if r.rs.calls() != 0 {
		t.Error("llama was restarted after the prepare step failed")
	}
	if _, held := r.o.Lock().Holder(); held {
		t.Error("the lock is still held")
	}
}

func TestRestartWithPrepareDoesNotRunWhenTheLockIsBusy(t *testing.T) {
	r := newOrRig(t)
	r.restarting()
	hold, err := r.o.Lock().TryAcquire(HolderLoad)
	if err != nil {
		t.Fatal(err)
	}
	defer hold.Release()
	ran := false
	_, err = r.o.RestartWith(bg, RestartRequest{Actor: sysActor, Prepare: func(context.Context, string) error { ran = true; return nil }})
	oe := opErrOf(t, err)
	if oe.Code != CodeBusy || oe.Status != http.StatusConflict || oe.Details["op_id"] != hold.OpID {
		t.Errorf("error: %+v", oe)
	}
	if ran || r.rs.calls() != 0 {
		t.Error("prepare or the restart ran although the lock was busy")
	}
}
