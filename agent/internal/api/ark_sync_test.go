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

type arkSyncRig struct {
	t       *testing.T
	planner *fakePlanner
	starter *fakeStarter
	h       http.Handler
}

func newArkSyncRig(t *testing.T, withStarter bool) *arkSyncRig {
	t.Helper()
	r := &arkSyncRig{t: t, planner: &fakePlanner{plans: []map[string]any{{
		"root": "internal", "profile": "small-8gb", "catalog_version": "2026.11.02-1",
		"budget_bytes": 220, "planned_bytes": 100, "items": []map[string]any{{"pos": 1, "id": "a", "kind": "model", "action": "keep"}},
		"findings": []any{},
	}}}, starter: &fakeStarter{res: ArkSyncStarted{RunID: "run-20261102T031500Z-a1f3"}}}
	deps := ArkSyncDeps{Planner: r.planner, NewOpID: func() string { return "op_7k2m9q4r8t1c" }}
	if withStarter {
		deps.Starter = r.starter
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

func TestArkSyncRoutesMetadata(t *testing.T) {
	routes := ArkSyncRoutes(ArkSyncDeps{})
	if len(routes) != 1 {
		t.Fatalf("routes = %d", len(routes))
	}
	rt := routes[0]
	if rt.Method != http.MethodPost || rt.Pattern != "/ark/sync" || len(rt.Auth) != 1 || rt.Auth[0] != AuthOp || rt.Timeout != 10*time.Second {
		t.Errorf("route = %+v", rt)
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
