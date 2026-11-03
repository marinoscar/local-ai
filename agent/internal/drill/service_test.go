package drill

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/api"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/modes"
)

func apiErr(t *testing.T, err error, status int, code string) *api.Error {
	t.Helper()
	var ae *api.Error
	if !errors.As(err, &ae) || ae.Status != status || ae.Code != code {
		t.Fatalf("error = %v, want %d %s", err, status, code)
	}
	return ae
}

func TestServiceMapsEngineErrorsToAPIErrors(t *testing.T) {
	r := newRig(t)
	r.add(passing("drl.a", 1))
	svc := NewService(r.eng)
	ctx := context.Background()

	r.mode.set(modes.Survival)
	_, err := svc.Start(ctx, api.DrillStartRequest{Trigger: "manual"})
	ae := apiErr(t, err, http.StatusConflict, doctor.DRL003)
	if ae.Details["reason"] != "survival_mode" {
		t.Fatalf("details = %v", ae.Details)
	}
	r.mode.set(modes.Online)

	_, err = svc.Start(ctx, api.DrillStartRequest{Trigger: "manual", Only: []string{"drl.a", "drl.nope"}})
	ae = apiErr(t, err, http.StatusBadRequest, api.CodeInvalidRequest)
	if ae.Details["field"] != "only" || ae.Details["reason"] != "unknown_check" || !errorsDeepEqual(ae.Details["unknown"], []string{"drl.nope"}) {
		t.Fatalf("details = %v", ae.Details)
	}
	_, err = svc.Start(ctx, api.DrillStartRequest{Trigger: "cloud"})
	apiErr(t, err, http.StatusBadRequest, api.CodeInvalidRequest)

	_, err = svc.Abort(ctx)
	apiErr(t, err, http.StatusNotFound, api.CodeNotFound)
	_, err = svc.Results(ctx, "")
	if ae := apiErr(t, err, http.StatusNotFound, api.CodeNotFound); ae.Details["run_id"] != nil {
		t.Fatal("no run id was asked for")
	}
	_, err = svc.Results(ctx, "drl-20200101T000000Z-aaaa")
	if ae := apiErr(t, err, http.StatusNotFound, api.CodeNotFound); ae.Details["run_id"] != "drl-20200101T000000Z-aaaa" {
		t.Fatalf("details = %v", ae.Details)
	}
	_, err = svc.Results(ctx, "../../etc/passwd")
	apiErr(t, err, http.StatusNotFound, api.CodeNotFound)

	_ = r.eng.Shutdown(ctx)
	_, err = svc.Start(ctx, api.DrillStartRequest{Trigger: "manual"})
	apiErr(t, err, http.StatusServiceUnavailable, api.CodeStarting)
}

func errorsDeepEqual(got any, want []string) bool {
	g, ok := got.([]string)
	if !ok || len(g) != len(want) {
		return false
	}
	for i := range g {
		if g[i] != want[i] {
			return false
		}
	}
	return true
}

func TestServiceStartResultsAndAbort(t *testing.T) {
	r := newRig(t)
	g := newGate()
	r.add(g.check("drl.hang", 1))
	svc := NewService(r.eng)
	ctx := context.Background()

	res, err := svc.Start(ctx, api.DrillStartRequest{Trigger: "manual", OpID: "op_1"})
	if err != nil || res.RunID == "" || res.State != "" {
		t.Fatalf("Start = %+v, %v", res, err)
	}
	<-g.started
	live, err := svc.Results(ctx, res.RunID)
	if err != nil || len(live) == 0 {
		t.Fatalf("Results of the running run = %q, %v", live, err)
	}
	id, err := svc.Abort(ctx)
	if err != nil || id != res.RunID {
		t.Fatalf("Abort = %q, %v", id, err)
	}
	r.wait()
	final, err := svc.Results(ctx, "")
	if err != nil || len(final) == 0 {
		t.Fatalf("Results = %q, %v", final, err)
	}
	byID, _ := svc.Results(ctx, res.RunID)
	if string(byID) != string(final) {
		t.Fatal("the last run and the run by id must be the same document")
	}
}

func TestServiceSkippedScheduledRun(t *testing.T) {
	r := newRig(t)
	r.add(passing("drl.a", 1))
	svc := NewService(r.eng)
	r.ark.set(true)
	res, err := svc.Start(context.Background(), api.DrillStartRequest{Trigger: "scheduled"})
	if err != nil || res.State != api.DrillStateSkipped || res.Code != doctor.DRL002 || res.Reason == "" {
		t.Fatalf("Start = %+v, %v", res, err)
	}
	if res.RetryAt == nil || !res.RetryAt.Equal(t0.Add(24*time.Hour)) {
		t.Fatalf("RetryAt = %v", res.RetryAt)
	}
}
