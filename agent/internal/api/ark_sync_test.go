package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/audit"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
)

type fakePlanner struct {
	reqs  []ArkSyncRequest
	plans any
	err   error
}

func (f *fakePlanner) DryRun(_ context.Context, req ArkSyncRequest) (any, error) {
	f.reqs = append(f.reqs, req)
	return f.plans, f.err
}

type fakeStarter struct {
	reqs []ArkSyncRequest
	res  ArkSyncStarted
	err  error
}

func (f *fakeStarter) Start(_ context.Context, req ArkSyncRequest) (ArkSyncStarted, error) {
	f.reqs = append(f.reqs, req)
	return f.res, f.err
}

type fakeControl struct {
	body       any
	ok         bool
	cancelled  ArkSyncCancelled
	cancelErr  error
	cancelReqs []ArkSyncCancelRequest
}

func (f *fakeControl) Current(context.Context) (any, bool) { return f.body, f.ok }

func (f *fakeControl) Cancel(_ context.Context, req ArkSyncCancelRequest) (ArkSyncCancelled, error) {
	f.cancelReqs = append(f.cancelReqs, req)
	return f.cancelled, f.cancelErr
}

type arkSyncRig struct {
	t       *testing.T
	planner *fakePlanner
	starter *fakeStarter
	control *fakeControl
	h       http.Handler
}

func newArkSyncRig(t *testing.T, withStarter bool) *arkSyncRig {
	t.Helper()
	r := &arkSyncRig{t: t, control: &fakeControl{}, planner: &fakePlanner{plans: []map[string]any{{
		"root": "internal", "profile": "small-8gb", "catalog_version": "2026.11.02-1",
		"budget_bytes": 220, "planned_bytes": 100, "items": []map[string]any{{"pos": 1, "id": "a", "kind": "model", "action": "keep"}},
		"findings": []any{},
	}}}, starter: &fakeStarter{res: ArkSyncStarted{RunID: "run-20261102T031500Z-a1f3"}}}
	deps := ArkSyncDeps{Planner: r.planner, NewOpID: func() string { return "op_7k2m9q4r8t1c" }}
	if withStarter {
		deps.Starter = r.starter
		deps.Control = r.control
	}
	reg := NewRegistry()
	if err := RegisterArkSync(reg, deps); err != nil {
		t.Fatal(err)
	}
	s, _ := newTestServer(t, func(o *Options) { o.Registry = reg })
	r.h = s.Handler()
	return r
}

func (r *arkSyncRig) post(body string) *httptest.ResponseRecorder {
	return recordOK(r.t, do(r.h, req{method: "POST", target: "/api/v1/ark/sync", body: body, header: jsonHeaders(nil)}))
}

func (r *arkSyncRig) call(method string) *httptest.ResponseRecorder {
	return recordOK(r.t, do(r.h, req{method: method, target: "/api/v1/ark/sync", header: jsonHeaders(nil)}))
}

func TestArkSyncRoutesMetadata(t *testing.T) {
	routes := ArkSyncRoutes(ArkSyncDeps{})
	if len(routes) != 3 {
		t.Fatalf("routes = %d", len(routes))
	}
	for i, method := range []string{http.MethodPost, http.MethodGet, http.MethodDelete} {
		rt := routes[i]
		if rt.Method != method || rt.Pattern != "/ark/sync" || len(rt.Auth) != 1 || rt.Auth[0] != AuthOp || rt.Timeout != 10*time.Second {
			t.Errorf("route %d = %+v", i, rt)
		}
	}
	if err := RegisterArkSync(NewRegistry(), ArkSyncDeps{}); err == nil {
		t.Fatal("RegisterArkSync must refuse a missing planner")
	}
}

func TestArkSyncDryRunAnswers200WithThePlansAndWritesNothing(t *testing.T) {
	r := newArkSyncRig(t, true)
	rec := r.post(`{"roots": ["internal"], "items": ["a"], "dry_run": true, "trigger": "scheduled"}`)
	if rec.Code != http.StatusOK || rec.Header().Get("X-LocalAI-Op-Id") != "op_7k2m9q4r8t1c" {
		t.Fatalf("status %d, op id %q, body %s", rec.Code, rec.Header().Get("X-LocalAI-Op-Id"), rec.Body.String())
	}
	if !strings.HasPrefix(strings.TrimSpace(rec.Body.String()), `[{"budget_bytes":220`) {
		t.Errorf("body = %s, want the planner's list", rec.Body.String())
	}
	if len(r.planner.reqs) != 1 || len(r.starter.reqs) != 0 {
		t.Fatalf("planner calls %d, starter calls %d: a dry run must never start a run", len(r.planner.reqs), len(r.starter.reqs))
	}
	got := r.planner.reqs[0]
	if !got.DryRun || got.Trigger != ArkTriggerScheduled || len(got.Roots) != 1 || got.Roots[0] != "internal" || len(got.Items) != 1 || got.OpID != "op_7k2m9q4r8t1c" {
		t.Errorf("request = %+v", got)
	}
	if got.Actor.Kind != audit.KindOperator {
		t.Errorf("actor = %+v", got.Actor)
	}
}

func TestArkSyncDefaultsAndValidation(t *testing.T) {
	t.Run("trigger defaults to manual and an empty object is a whole-plan request", func(t *testing.T) {
		r := newArkSyncRig(t, false)
		if rec := r.post(`{"dry_run": true}`); rec.Code != http.StatusOK {
			t.Fatalf("%d %s", rec.Code, rec.Body.String())
		}
		if got := r.planner.reqs[0]; got.Trigger != ArkTriggerManual || len(got.Roots) != 0 || len(got.Items) != 0 || got.AllowOfflineMode {
			t.Errorf("request = %+v", got)
		}
	})
	cases := []struct {
		name, body, reason, field string
	}{
		{"unknown trigger", `{"dry_run": true, "trigger": "whenever"}`, "unknown_trigger", "trigger"},
		{"empty root", `{"dry_run": true, "roots": [" "]}`, "empty_value", "roots"},
		{"empty item", `{"dry_run": true, "items": [""]}`, "empty_value", "items"},
		{"too many roots", `{"dry_run": true, "roots": [` + strings.TrimSuffix(strings.Repeat(`"x",`, 257), ",") + `]}`, "too_many", "roots"},
		{"empty body", ``, "empty_body", ""},
		{"unknown field", `{"dry_run": true, "bogus": 1}`, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newArkSyncRig(t, true)
			e := expectError(t, r.post(tc.body), http.StatusBadRequest, doctor.AGT020)
			if tc.reason != "" && e.Error.Details["reason"] != tc.reason {
				t.Errorf("details = %v, want reason %s", e.Error.Details, tc.reason)
			}
			if tc.field != "" && e.Error.Details["field"] != tc.field {
				t.Errorf("details = %v, want field %s", e.Error.Details, tc.field)
			}
			if len(r.planner.reqs)+len(r.starter.reqs) != 0 {
				t.Error("nothing may be planned or started for a bad request")
			}
		})
	}
}

func TestArkSyncPlannerErrorsPassThrough(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"no writable root", NewError(doctor.ARK001, http.StatusConflict, nil), http.StatusConflict, doctor.ARK001},
		{"unknown profile", NewError(doctor.CAT003, http.StatusUnprocessableEntity, map[string]any{"profile": "x"}), http.StatusUnprocessableEntity, doctor.CAT003},
		{"a bug", errors.New("boom"), http.StatusInternalServerError, CodeInternal},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newArkSyncRig(t, true)
			r.planner.err = tc.err
			expectError(t, r.post(`{"dry_run": true}`), tc.status, tc.code)
		})
	}
}

func TestArkSyncRealStart(t *testing.T) {
	t.Run("without an engine the agent answers 503 starting", func(t *testing.T) {
		r := newArkSyncRig(t, false)
		rec := r.post(`{"trigger": "manual"}`)
		e := expectError(t, rec, http.StatusServiceUnavailable, CodeStarting)
		if e.Error.Details["reason"] != "sync_engine_not_ready" || rec.Header().Get("Retry-After") != "5" {
			t.Errorf("details %v, Retry-After %q", e.Error.Details, rec.Header().Get("Retry-After"))
		}
		if len(r.planner.reqs) != 0 {
			t.Error("a real start must not plan")
		}
	})
	t.Run("with an engine it answers 202", func(t *testing.T) {
		r := newArkSyncRig(t, true)
		rec := r.post(`{"allow_offline_mode": true, "trigger": "cloud"}`)
		if rec.Code != http.StatusAccepted {
			t.Fatalf("%d %s", rec.Code, rec.Body.String())
		}
		if got := strings.TrimSpace(rec.Body.String()); got != `{"run_id":"run-20261102T031500Z-a1f3","state":"running","events":"/api/v1/events?types=sync.*"}` {
			t.Errorf("body = %s", got)
		}
		if got := r.starter.reqs[0]; got.DryRun || !got.AllowOfflineMode || got.Trigger != ArkTriggerCloud {
			t.Errorf("request = %+v", got)
		}
	})
	t.Run("the engine's typed errors pass through", func(t *testing.T) {
		r := newArkSyncRig(t, true)
		r.starter.err = NewError(doctor.SYN005, http.StatusConflict, map[string]any{"run_id": "run-20261102T031500Z-a1f3"})
		e := expectError(t, r.post(`{}`), http.StatusConflict, doctor.SYN005)
		if e.Error.Details["run_id"] != "run-20261102T031500Z-a1f3" {
			t.Errorf("details = %v", e.Error.Details)
		}
	})
}

func TestArkSyncStartedSkippedRunAnswers202WithTheReason(t *testing.T) {
	r := newArkSyncRig(t, true)
	r.starter.res = ArkSyncStarted{RunID: "run-20261102T031500Z-a1f3", State: "skipped", Reason: "battery", Code: doctor.SYN003}
	rec := r.post(`{"trigger": "scheduled"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	want := `{"run_id":"run-20261102T031500Z-a1f3","state":"skipped","reason":"battery","code":"LAI-SYN-003","events":"/api/v1/events?types=sync.*"}`
	if got := strings.TrimSpace(rec.Body.String()); got != want {
		t.Errorf("body = %s", got)
	}
}

func TestArkSyncGet(t *testing.T) {
	t.Run("the active or last run", func(t *testing.T) {
		r := newArkSyncRig(t, true)
		r.control.ok = true
		r.control.body = map[string]any{"run_id": "run-20261102T031500Z-a1f3", "trigger": "manual", "state": "running", "started_at": "2026-11-02T03:15:02Z", "finished_at": nil, "totals": map[string]any{"items": 31}}
		rec := r.call(http.MethodGet)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"run_id":"run-20261102T031500Z-a1f3"`) || !strings.Contains(rec.Body.String(), `"state":"running"`) {
			t.Fatalf("%d %s", rec.Code, rec.Body.String())
		}
	})
	t.Run("no run yet is 404 LAI-AGT-021", func(t *testing.T) {
		r := newArkSyncRig(t, true)
		expectError(t, r.call(http.MethodGet), http.StatusNotFound, doctor.AGT021)
	})
	t.Run("without an engine the agent answers 503 starting", func(t *testing.T) {
		r := newArkSyncRig(t, false)
		e := expectError(t, r.call(http.MethodGet), http.StatusServiceUnavailable, CodeStarting)
		if e.Error.Details["reason"] != "sync_engine_not_ready" {
			t.Errorf("details = %v", e.Error.Details)
		}
	})
}

func TestArkSyncDelete(t *testing.T) {
	t.Run("a run is cancelling", func(t *testing.T) {
		r := newArkSyncRig(t, true)
		r.control.cancelled = ArkSyncCancelled{RunID: "run-20261102T031500Z-a1f3", Active: true}
		rec := r.call(http.MethodDelete)
		if rec.Code != http.StatusAccepted || rec.Header().Get("X-LocalAI-Op-Id") != "op_7k2m9q4r8t1c" {
			t.Fatalf("%d %s, op id %q", rec.Code, rec.Body.String(), rec.Header().Get("X-LocalAI-Op-Id"))
		}
		if got := strings.TrimSpace(rec.Body.String()); got != `{"run_id":"run-20261102T031500Z-a1f3","state":"cancelling"}` {
			t.Errorf("body = %s", got)
		}
		if len(r.control.cancelReqs) != 1 || r.control.cancelReqs[0].OpID != "op_7k2m9q4r8t1c" || r.control.cancelReqs[0].Actor.Kind != audit.KindOperator {
			t.Errorf("cancel requests = %+v", r.control.cancelReqs)
		}
	})
	t.Run("nothing running is idle, and still 202 (idempotent)", func(t *testing.T) {
		r := newArkSyncRig(t, true)
		r.control.cancelled = ArkSyncCancelled{RunID: "run-20261102T031500Z-a1f3"}
		rec := r.call(http.MethodDelete)
		if got := strings.TrimSpace(rec.Body.String()); rec.Code != http.StatusAccepted || got != `{"run_id":"run-20261102T031500Z-a1f3","state":"idle"}` {
			t.Fatalf("%d %s", rec.Code, got)
		}
	})
	t.Run("an error of the engine passes through", func(t *testing.T) {
		r := newArkSyncRig(t, true)
		r.control.cancelErr = errors.New("boom")
		expectError(t, r.call(http.MethodDelete), http.StatusInternalServerError, CodeInternal)
	})
	t.Run("without an engine the agent answers 503 starting", func(t *testing.T) {
		r := newArkSyncRig(t, false)
		expectError(t, r.call(http.MethodDelete), http.StatusServiceUnavailable, CodeStarting)
	})
}
