package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/audit"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
)

type fakeDrill struct {
	mu       sync.Mutex
	starts   []DrillStartRequest
	startRes DrillStartResult
	startErr error
	doc      []byte
	docErr   error
	gotRun   []string
	abortID  string
	abortErr error
	aborts   int

	manuals   []DrillManualRequest
	manualRes DrillManualResult
	manualErr error
}

func (f *fakeDrill) Manual(_ context.Context, req DrillManualRequest) (DrillManualResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.manuals = append(f.manuals, req)
	return f.manualRes, f.manualErr
}

func (f *fakeDrill) Start(_ context.Context, req DrillStartRequest) (DrillStartResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.starts = append(f.starts, req)
	return f.startRes, f.startErr
}

func (f *fakeDrill) Results(_ context.Context, runID string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gotRun = append(f.gotRun, runID)
	return f.doc, f.docErr
}

func (f *fakeDrill) Abort(context.Context) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.aborts++
	return f.abortID, f.abortErr
}

type drillRig struct {
	t     *testing.T
	svc   *fakeDrill
	audit *recAudit
	h     http.Handler
}

func newDrillRig(t *testing.T) *drillRig {
	t.Helper()
	r := &drillRig{t: t, svc: &fakeDrill{startRes: DrillStartResult{RunID: "drl-20261101T043512Z-9c1e"}, abortID: "drl-20261101T043512Z-9c1e", manualRes: DrillManualResult{RunID: "drl-20261102T150000Z-ab12-manual"}}, audit: &recAudit{}}
	reg := NewRegistry()
	if err := RegisterDrill(reg, DrillDeps{Service: r.svc, Audit: r.audit, NewOpID: func() string { return "op_7k2m9q4r8t1c" }}); err != nil {
		t.Fatal(err)
	}
	s, _ := newTestServer(t, func(o *Options) { o.Registry = reg })
	r.h = s.Handler()
	return r
}

func (r *drillRig) post(body string, extra map[string]string) *httptest.ResponseRecorder {
	return recordOK(r.t, do(r.h, req{method: "POST", target: "/api/v1/drill", body: body, header: jsonHeaders(extra)}))
}

func (r *drillRig) get(query string) *httptest.ResponseRecorder {
	return recordOK(r.t, do(r.h, req{target: "/api/v1/drill" + query, header: map[string]string{"X-Test-Principal": "bearer"}}))
}

func (r *drillRig) del() *httptest.ResponseRecorder {
	return recordOK(r.t, do(r.h, req{method: "DELETE", target: "/api/v1/drill", header: jsonHeaders(nil)}))
}

func TestDrillRoutesMetadata(t *testing.T) {
	routes := DrillRoutes(DrillDeps{})
	want := map[string]bool{"POST /drill": true, "GET /drill": true, "DELETE /drill": true, "POST /drill/manual": true}
	for _, rt := range routes {
		k := rt.Method + " " + rt.Pattern
		if !want[k] {
			t.Errorf("unexpected route %s", k)
		}
		delete(want, k)
		wantAuth, wantTimeout := AuthOp, 10*time.Second
		if k == "POST /drill/manual" {
			wantAuth, wantTimeout = AuthAdmin, 5*time.Second
		}
		if len(rt.Auth) != 1 || rt.Auth[0] != wantAuth || rt.Timeout != wantTimeout {
			t.Errorf("%s: auth %v timeout %v, want %s and %v", k, rt.Auth, rt.Timeout, wantAuth, wantTimeout)
		}
	}
	if len(want) != 0 {
		t.Errorf("missing routes %v", want)
	}
	if err := RegisterDrill(NewRegistry(), DrillDeps{}); err == nil {
		t.Fatal("RegisterDrill must refuse a missing service")
	}
}

func TestDrillStartDefaultsAndGoldenBody(t *testing.T) {
	r := newDrillRig(t)
	rec := r.post(`{"quick": true, "only": ["drl.fence"]}`, nil)
	if rec.Code != http.StatusAccepted || rec.Header().Get("X-LocalAI-Op-Id") != "op_7k2m9q4r8t1c" {
		t.Fatalf("status %d, op id %q, body %s", rec.Code, rec.Header().Get("X-LocalAI-Op-Id"), rec.Body.String())
	}
	if got := strings.TrimSpace(rec.Body.String()); got != `{"run_id":"drl-20261101T043512Z-9c1e"}` {
		t.Fatalf("body = %s", got)
	}
	got := r.svc.starts[0]
	if !got.Quick || len(got.Only) != 1 || got.Only[0] != "drl.fence" || got.Trigger != "manual" || got.OpID != "op_7k2m9q4r8t1c" {
		t.Fatalf("request = %+v", got)
	}
	if got.Actor.Kind != audit.KindOperator {
		t.Fatalf("actor = %+v", got.Actor)
	}
}

func TestDrillStartAcceptsAnEmptyBodyAndTheScheduledTrigger(t *testing.T) {
	r := newDrillRig(t)
	if rec := r.post("", nil); rec.Code != http.StatusAccepted {
		t.Fatalf("empty body: %d %s", rec.Code, rec.Body.String())
	}
	if rec := r.post(`{"trigger":"scheduled"}`, nil); rec.Code != http.StatusAccepted {
		t.Fatalf("scheduled: %d %s", rec.Code, rec.Body.String())
	}
	if r.svc.starts[0].Trigger != "manual" || r.svc.starts[1].Trigger != "scheduled" {
		t.Fatalf("triggers = %q %q", r.svc.starts[0].Trigger, r.svc.starts[1].Trigger)
	}
}

func TestDrillStartBodyValidation(t *testing.T) {
	r := newDrillRig(t)
	for _, tc := range []struct {
		name, body, reason string
	}{
		{"unknown trigger", `{"trigger":"cloud"}`, "unknown_trigger"},
		{"manual_plug is not a trigger", `{"trigger":"manual_plug"}`, "unknown_trigger"},
		{"unknown field", `{"quik":true}`, "unknown_field"},
		{"wrong type", `{"quick":"yes"}`, "wrong_type"},
		{"only is not a list", `{"only":"drl.fence"}`, "wrong_type"},
		{"malformed", `{"quick":`, "malformed_json"},
		{"trailing data", `{} {}`, "trailing_data"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := expectError(t, r.post(tc.body, nil), http.StatusBadRequest, doctor.AGT020)
			if e.Error.Details["reason"] != tc.reason {
				t.Fatalf("details = %v, want reason %s", e.Error.Details, tc.reason)
			}
		})
	}
	if len(r.svc.starts) != 0 {
		t.Fatal("a bad body must not reach the engine")
	}
}

func TestDrillStartErrorsPassThroughAndAreAudited(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		status int
		code   string
		result audit.Result
	}{
		{"a drill or an ark run is active", NewError(doctor.DRL002, http.StatusConflict, map[string]any{"reason": "drill_running"}), 409, doctor.DRL002, audit.ResultDenied},
		{"survival mode", NewError(doctor.DRL003, http.StatusConflict, nil), 409, doctor.DRL003, audit.ResultDenied},
		{"llama busy", NewError(doctor.DRL004, http.StatusConflict, nil), 409, doctor.DRL004, audit.ResultDenied},
		{"low battery", NewError(doctor.DRL005, http.StatusConflict, nil), 409, doctor.DRL005, audit.ResultDenied},
		{"unknown check", NewError(CodeInvalidRequest, http.StatusBadRequest, map[string]any{"field": "only"}), 400, doctor.AGT020, audit.ResultFail},
		{"the agent is stopping", NewError(CodeStarting, http.StatusServiceUnavailable, nil), 503, doctor.AGT024, audit.ResultFail},
		{"a bug", errors.New("boom"), 500, doctor.AGT099, audit.ResultFail},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newDrillRig(t)
			r.svc.startErr = tc.err
			rec := r.post(`{}`, nil)
			expectError(t, rec, tc.status, tc.code)
			if rec.Header().Get("X-LocalAI-Op-Id") != "op_7k2m9q4r8t1c" {
				t.Fatalf("op id header = %q", rec.Header().Get("X-LocalAI-Op-Id"))
			}
			evs := r.audit.events()
			if len(evs) != 1 || evs[0].Action != "drill.start" || evs[0].Result != tc.result || evs[0].OpID != "op_7k2m9q4r8t1c" {
				t.Fatalf("audit = %+v", evs)
			}
			if tc.status != 500 && evs[0].Code != tc.code {
				t.Fatalf("audit code = %q", evs[0].Code)
			}
		})
	}
}

func TestDrillStartAudit(t *testing.T) {
	r := newDrillRig(t)
	r.post(`{"quick":true,"trigger":"scheduled"}`, nil)
	evs := r.audit.events()
	if len(evs) != 1 {
		t.Fatalf("audit = %+v", evs)
	}
	e := evs[0]
	if e.Action != "drill.start" || e.Result != audit.ResultOK || e.Target["trigger"] != "scheduled" || e.Target["quick"] != "true" || e.Target["run_id"] != "drl-20261101T043512Z-9c1e" {
		t.Fatalf("audit = %+v", e)
	}
}

func TestDrillStartSkippedScheduledRunAnswers202WithTheReason(t *testing.T) {
	r := newDrillRig(t)
	retry := time.Date(2026, 11, 2, 4, 35, 12, 0, time.UTC)
	r.svc.startRes = DrillStartResult{RunID: "drl-20261101T043512Z-9c1e", State: DrillStateSkipped, Code: doctor.DRL004, Reason: "the AI is in use", RetryAt: &retry}
	rec := r.post(`{"trigger":"scheduled"}`, nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status %d %s", rec.Code, rec.Body.String())
	}
	want := `{"run_id":"drl-20261101T043512Z-9c1e","state":"skipped","code":"` + doctor.DRL004 + `","reason":"the AI is in use","retry_at":"2026-11-02T04:35:12Z"}`
	if got := strings.TrimSpace(rec.Body.String()); got != want {
		t.Fatalf("body = %s\nwant   %s", got, want)
	}
	if e := r.audit.events()[0]; e.Result != audit.ResultOK || e.Target["skipped"] != doctor.DRL004 {
		t.Fatalf("audit = %+v", e)
	}

	r.svc.startRes = DrillStartResult{RunID: "drl-20261101T043512Z-9c1e", State: DrillStateSkipped, Code: doctor.DRL003, Reason: "the node is in survival mode"}
	rec = r.post(`{"trigger":"scheduled"}`, nil)
	if got := strings.TrimSpace(rec.Body.String()); strings.Contains(got, "retry_at") {
		t.Fatalf("a run that is not retried has no retry_at: %s", got)
	}
}

func TestDrillAuditFailureDoesNotFailTheRequest(t *testing.T) {
	r := newDrillRig(t)
	r.audit.fail = errors.New("disk full")
	if rec := r.post(`{}`, nil); rec.Code != http.StatusAccepted {
		t.Fatalf("status %d", rec.Code)
	}
}

func TestDrillGet(t *testing.T) {
	r := newDrillRig(t)
	r.svc.doc = []byte("{\n  \"schema_version\": 1,\n  \"run_id\": \"drl-20261101T043512Z-9c1e\",\n  \"trigger\": \"scheduled\",\n  \"started_at\": \"2026-11-01T04:35:12Z\",\n  \"overall\": \"pass\"\n}\n")
	rec := r.get("")
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "application/json; charset=utf-8" {
		t.Fatalf("status %d, content type %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	m := decodeMap(t, rec)
	if m["run_id"] != "drl-20261101T043512Z-9c1e" || m["overall"] != "pass" {
		t.Fatalf("body = %v", m)
	}
	r.get("?run_id=drl-20261101T043512Z-9c1e")
	if got := r.svc.gotRun; len(got) != 2 || got[0] != "" || got[1] != "drl-20261101T043512Z-9c1e" {
		t.Fatalf("run ids asked for = %q", got)
	}

	r.svc.docErr = NewError(CodeNotFound, http.StatusNotFound, map[string]any{"kind": "drill_results"})
	expectError(t, r.get("?run_id=drl-20200101T000000Z-aaaa"), http.StatusNotFound, doctor.AGT021)
}

func TestDrillAbort(t *testing.T) {
	r := newDrillRig(t)
	rec := r.del()
	if rec.Code != http.StatusAccepted || rec.Header().Get("X-LocalAI-Op-Id") != "op_7k2m9q4r8t1c" {
		t.Fatalf("status %d op id %q", rec.Code, rec.Header().Get("X-LocalAI-Op-Id"))
	}
	if got := strings.TrimSpace(rec.Body.String()); got != `{"run_id":"drl-20261101T043512Z-9c1e"}` {
		t.Fatalf("body = %s", got)
	}
	if e := r.audit.events()[0]; e.Action != "drill.abort" || e.Result != audit.ResultOK || e.Target["run_id"] != "drl-20261101T043512Z-9c1e" {
		t.Fatalf("audit = %+v", e)
	}

	r2 := newDrillRig(t)
	r2.svc.abortErr = NewError(CodeNotFound, http.StatusNotFound, map[string]any{"kind": "drill"})
	expectError(t, r2.del(), http.StatusNotFound, doctor.AGT021)
	if e := r2.audit.events()[0]; e.Action != "drill.abort" || e.Result != audit.ResultDenied || e.Code != doctor.AGT021 {
		t.Fatalf("audit = %+v", e)
	}
}

func TestDrillRoutesNeedAnOperator(t *testing.T) {
	r := newDrillRig(t)
	for _, q := range []req{
		{method: "POST", target: "/api/v1/drill", body: `{}`, header: map[string]string{"Content-Type": "application/json"}},
		{target: "/api/v1/drill"},
		{method: "DELETE", target: "/api/v1/drill", header: map[string]string{"Content-Type": "application/json"}},
	} {
		rec := do(r.h, q)
		expectError(t, rec, http.StatusUnauthorized, doctor.SEC001)
	}
	for _, q := range []req{
		{method: "POST", target: "/api/v1/drill", body: `{}`, header: map[string]string{"Content-Type": "application/json", "X-Test-Principal": "portal"}},
		{target: "/api/v1/drill", header: map[string]string{"X-Test-Principal": "portal"}},
		{method: "DELETE", target: "/api/v1/drill", header: map[string]string{"Content-Type": "application/json", "X-Test-Principal": "portal"}},
	} {
		rec := do(r.h, q)
		if rec.Code != http.StatusUnauthorized && rec.Code != http.StatusForbidden {
			t.Fatalf("a Portal session must not reach the drill routes: %d", rec.Code)
		}
	}
	if len(r.svc.starts) != 0 || r.svc.aborts != 0 {
		t.Fatal("an unauthenticated request must not reach the engine")
	}
}

func (r *drillRig) manual(body string, extra map[string]string) *httptest.ResponseRecorder {
	h := jsonHeaders(extra)
	if _, set := extra["X-Test-Principal"]; !set {
		h["X-Test-Principal"] = "admin"
	}
	return recordOK(r.t, do(r.h, req{method: "POST", target: "/api/v1/drill/manual", body: body, header: h}))
}

func TestDrillManualRecordsAndAnswers201(t *testing.T) {
	r := newDrillRig(t)
	rec := r.manual(`{"result":"pass","notes":"ok","at":"2026-11-02T09:00:00-06:00"}`, nil)
	if rec.Code != http.StatusCreated || rec.Header().Get("X-LocalAI-Op-Id") != "op_7k2m9q4r8t1c" {
		t.Fatalf("status %d, op id %q, body %s", rec.Code, rec.Header().Get("X-LocalAI-Op-Id"), rec.Body.String())
	}
	if got := strings.TrimSpace(rec.Body.String()); got != `{"run_id":"drl-20261102T150000Z-ab12-manual"}` {
		t.Fatalf("body = %s", got)
	}
	if len(r.svc.manuals) != 1 {
		t.Fatalf("manual calls = %d", len(r.svc.manuals))
	}
	got := r.svc.manuals[0]
	if got.Result != "pass" || got.Notes != "ok" || !got.At.Equal(time.Date(2026, 11, 2, 15, 0, 0, 0, time.UTC)) || got.OpID != "op_7k2m9q4r8t1c" || got.Actor.Kind != audit.KindAdmin {
		t.Fatalf("request = %+v", got)
	}
	evs := r.audit.events()
	if len(evs) != 1 {
		t.Fatalf("audit = %+v", evs)
	}
	e := evs[0]
	if e.Action != "drill.record_manual" || e.Result != audit.ResultOK || e.Target["result"] != "pass" || e.Target["run_id"] != "drl-20261102T150000Z-ab12-manual" || e.OpID != "op_7k2m9q4r8t1c" {
		t.Fatalf("audit = %+v", e)
	}
	if _, leaked := e.Target["notes"]; leaked {
		t.Fatal("the notes are not audited")
	}
}

func TestDrillManualOptionalFieldsAndFail(t *testing.T) {
	r := newDrillRig(t)
	if rec := r.manual(`{"result":"fail"}`, nil); rec.Code != http.StatusCreated {
		t.Fatalf("status %d %s", rec.Code, rec.Body.String())
	}
	if rec := r.manual(`{"result":"pass","at":null,"notes":""}`, nil); rec.Code != http.StatusCreated {
		t.Fatalf("status %d %s", rec.Code, rec.Body.String())
	}
	if got := r.svc.manuals; got[0].Result != "fail" || !got[0].At.IsZero() || got[0].Notes != "" || !got[1].At.IsZero() {
		t.Fatalf("requests = %+v", got)
	}
	// Notes of exactly 2000 characters, counted as characters and not bytes, pass.
	if rec := r.manual(`{"result":"pass","notes":"`+strings.Repeat("é", 2000)+`"}`, nil); rec.Code != http.StatusCreated {
		t.Fatalf("status %d", rec.Code)
	}
}

func TestDrillManualBodyValidation(t *testing.T) {
	r := newDrillRig(t)
	for _, tc := range []struct {
		name, body, reason, field string
	}{
		{"no result", `{"notes":"x"}`, "invalid_result", "result"},
		{"empty result", `{"result":""}`, "invalid_result", "result"},
		{"warn is not allowed", `{"result":"warn"}`, "invalid_result", "result"},
		{"upper case", `{"result":"PASS"}`, "invalid_result", "result"},
		{"notes too long", `{"result":"pass","notes":"` + strings.Repeat("a", 2001) + `"}`, "notes_too_long", "notes"},
		{"notes too long in characters", `{"result":"pass","notes":"` + strings.Repeat("é", 2001) + `"}`, "notes_too_long", "notes"},
		{"at is not a time", `{"result":"pass","at":"yesterday"}`, "invalid_at", "at"},
		{"at is a date only", `{"result":"pass","at":"2026-11-02"}`, "invalid_at", "at"},
		{"at without a zone", `{"result":"pass","at":"2026-11-02T15:00:00"}`, "invalid_at", "at"},
		{"at is empty", `{"result":"pass","at":""}`, "invalid_at", "at"},
		{"at is a number", `{"result":"pass","at":1762095600}`, "wrong_type", "at"},
		{"unknown field", `{"result":"pass","note":"x"}`, "unknown_field", "note"},
		{"result is not a string", `{"result":true}`, "wrong_type", "result"},
		{"empty body", ``, "empty_body", ""},
		{"malformed", `{"result":`, "malformed_json", ""},
		{"trailing data", `{"result":"pass"} {}`, "trailing_data", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := expectError(t, r.manual(tc.body, nil), http.StatusBadRequest, doctor.AGT020)
			if e.Error.Details["reason"] != tc.reason {
				t.Fatalf("details = %v, want reason %s", e.Error.Details, tc.reason)
			}
			if tc.field != "" && e.Error.Details["field"] != tc.field {
				t.Fatalf("details = %v, want field %s", e.Error.Details, tc.field)
			}
		})
	}
	if len(r.svc.manuals) != 0 || len(r.audit.events()) != 0 {
		t.Fatal("a bad body must reach neither the engine nor the audit log")
	}
}

func TestDrillManualNeedsAnAdminAndAJSONBody(t *testing.T) {
	r := newDrillRig(t)
	body := `{"result":"pass"}`
	// No credential: 401. An operator without the PIN grant, and a Portal session: refused. Nothing is recorded.
	expectError(t, do(r.h, req{method: "POST", target: "/api/v1/drill/manual", body: body, header: map[string]string{"Content-Type": "application/json"}}), http.StatusUnauthorized, doctor.SEC001)
	expectError(t, r.manual(body, map[string]string{"X-Test-Principal": "bearer"}), http.StatusForbidden, doctor.SEC002)
	if rec := r.manual(body, map[string]string{"X-Test-Principal": "portal"}); rec.Code != http.StatusUnauthorized && rec.Code != http.StatusForbidden {
		t.Fatalf("a Portal session must not record: %d", rec.Code)
	}
	if len(r.svc.manuals) != 0 || len(r.audit.events()) != 0 {
		t.Fatal("a refused caller must not reach the engine")
	}
	// A mutating request needs Content-Type: application/json.
	rec := recordOK(t, do(r.h, req{method: "POST", target: "/api/v1/drill/manual", body: body, header: map[string]string{"X-Test-Principal": "admin", "Content-Type": "text/plain"}}))
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("status %d", rec.Code)
	}
	if rec := r.manual(body, nil); rec.Code != http.StatusCreated {
		t.Fatalf("an admin records: %d %s", rec.Code, rec.Body.String())
	}
}

func TestDrillManualServiceErrorsPassThroughAndAreAudited(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		status int
		code   string
		result audit.Result
	}{
		{"at in the future", NewError(CodeInvalidRequest, http.StatusBadRequest, map[string]any{"field": "at", "reason": "in_the_future"}), 400, doctor.AGT020, audit.ResultFail},
		{"the agent is stopping", NewError(CodeStarting, http.StatusServiceUnavailable, nil), 503, doctor.AGT024, audit.ResultFail},
		{"a bug", errors.New("boom"), 500, doctor.AGT099, audit.ResultFail},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newDrillRig(t)
			r.svc.manualErr = tc.err
			rec := r.manual(`{"result":"pass"}`, nil)
			expectError(t, rec, tc.status, tc.code)
			evs := r.audit.events()
			if len(evs) != 1 || evs[0].Action != "drill.record_manual" || evs[0].Result != tc.result || evs[0].Target["result"] != "pass" {
				t.Fatalf("audit = %+v", evs)
			}
			if _, has := evs[0].Target["run_id"]; has {
				t.Fatal("nothing was stored: no run id")
			}
		})
	}
}

func TestDrillManualAuditFailureDoesNotFailTheRequest(t *testing.T) {
	r := newDrillRig(t)
	r.audit.fail = errors.New("disk full")
	if rec := r.manual(`{"result":"pass"}`, nil); rec.Code != http.StatusCreated {
		t.Fatalf("status %d", rec.Code)
	}
}
