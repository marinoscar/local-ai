package api

import (
	"context"
	"errors"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/audit"
	"github.com/marinoscar/local-ai/agent/internal/services"
)

// RunAction is the route without the HTTP: the cloud executor of service.restart (AGENT-18) calls it
// with the cloud actor and gets the route's behaviour, errors and audit line.
func TestRunActionIsTheRouteForTheCloud(t *testing.T) {
	cloudActor := audit.Cloud("job-5")

	t.Run("a llama restart goes through the hook and is audited with the cloud actor", func(t *testing.T) {
		r := newSvcRig(t)
		res, err := r.deps.RunAction(context.Background(), ServiceActionRequest{Name: "llama", Action: services.ActionRestart, Actor: cloudActor, ReqID: "req-1"})
		if err != nil {
			t.Fatal(err)
		}
		if res.Name != "llama" || res.Active != "active" || res.OpID != "op_7k2m9q4r8t1c" || len(res.RestoredModels) != 2 {
			t.Errorf("result = %+v", res)
		}
		evs := r.audit.events()
		if len(evs) != 1 {
			t.Fatalf("audit = %+v", evs)
		}
		ev := evs[0]
		if ev.Action != "service.restart" || ev.Result != audit.ResultOK || ev.Actor.Kind != audit.KindCloud || ev.Actor.JobID != "job-5" || ev.ReqID != "req-1" {
			t.Errorf("audit event = %+v", ev)
		}
	})

	t.Run("a busy model lock is LAI-AGT-022 with the holder's op id", func(t *testing.T) {
		r := newSvcRig(t)
		r.llama.skipOp = true
		r.llama.err = &BusyError{OpID: "op_holder0000ab"}
		_, err := r.deps.RunAction(context.Background(), ServiceActionRequest{Name: "llama", Action: services.ActionRestart, Actor: cloudActor})
		var ae *Error
		if !errors.As(err, &ae) || ae.Code != CodeBusy || ae.Status != 409 || ae.Details["op_id"] != "op_holder0000ab" {
			t.Errorf("error = %#v", err)
		}
	})

	t.Run("stopping llama in survival needs force, for the cloud as for anyone", func(t *testing.T) {
		r := newSvcRig(t)
		r.mode = "survival"
		_, err := r.deps.RunAction(context.Background(), ServiceActionRequest{Name: "llama", Action: services.ActionStop, Actor: cloudActor})
		var ae *Error
		if !errors.As(err, &ae) || ae.Code != CodeNotInMode {
			t.Fatalf("error = %#v", err)
		}
		if evs := r.audit.events(); len(evs) != 1 || evs[0].Result != audit.ResultDenied || evs[0].Actor.JobID != "job-5" {
			t.Errorf("audit = %+v", evs)
		}
	})

	t.Run("an unknown service or action is refused before anything runs", func(t *testing.T) {
		r := newSvcRig(t)
		for _, req := range []ServiceActionRequest{
			{Name: "sshd", Action: services.ActionRestart, Actor: cloudActor},
			{Name: "llama", Action: services.Action("reboot"), Actor: cloudActor},
		} {
			_, err := r.deps.RunAction(context.Background(), req)
			var ae *Error
			if !errors.As(err, &ae) || (ae.Code != CodeNotFound && ae.Code != CodeInvalidRequest) {
				t.Errorf("%+v: error = %#v", req, err)
			}
		}
		if len(r.mgr.Calls()) != 0 {
			t.Errorf("a unit was touched: %v", r.mgr.Calls())
		}
	})
}
