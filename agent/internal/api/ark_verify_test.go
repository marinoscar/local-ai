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

type fakeVerifyStarter struct {
	reqs    []ArkVerifyRequest
	started ArkSyncStarted
	err     error
}

func (f *fakeVerifyStarter) StartVerify(_ context.Context, req ArkVerifyRequest) (ArkSyncStarted, error) {
	f.reqs = append(f.reqs, req)
	return f.started, f.err
}

type arkVerifyRig struct {
	t       *testing.T
	starter *fakeVerifyStarter
	h       http.Handler
}

func newArkVerifyRig(t *testing.T) *arkVerifyRig {
	t.Helper()
	r := &arkVerifyRig{t: t, starter: &fakeVerifyStarter{started: ArkSyncStarted{RunID: "run-20261102T031500Z-a1f3"}}}
	reg := NewRegistry()
	if err := RegisterArkVerify(reg, ArkVerifyDeps{Starter: r.starter, NewOpID: func() string { return "op_7k2m9q4r8t1c" }}); err != nil {
		t.Fatal(err)
	}
	s, _ := newTestServer(t, func(o *Options) { o.Registry = reg })
	r.h = s.Handler()
	return r
}

func (r *arkVerifyRig) post(principal, body string) *httptest.ResponseRecorder {
	h := jsonHeaders(map[string]string{"X-Test-Principal": principal})
	return do(r.h, req{method: "POST", target: "/api/v1/ark/verify", body: body, header: h})
}

func TestArkVerifyRouteMetadata(t *testing.T) {
	routes := ArkVerifyRoutes(ArkVerifyDeps{})
	if len(routes) != 1 {
		t.Fatalf("routes = %d", len(routes))
	}
	rt := routes[0]
	if rt.Method != http.MethodPost || rt.Pattern != "/ark/verify" || len(rt.Auth) != 1 || rt.Auth[0] != AuthOp || rt.Timeout != 10*time.Second {
		t.Errorf("route = %+v", rt)
	}
	if err := RegisterArkVerify(NewRegistry(), ArkVerifyDeps{}); err == nil {
		t.Fatal("RegisterArkVerify must refuse a missing starter")
	}
}

func TestArkVerifyStartsARunAndAnswers202(t *testing.T) {
	r := newArkVerifyRig(t)
	rec := recordOK(t, r.post("bearer", `{"roots": ["internal", "ark-3f9c0a12"], "items": ["qwen-x"], "full": true}`))
	if rec.Code != http.StatusAccepted || rec.Header().Get("X-LocalAI-Op-Id") != "op_7k2m9q4r8t1c" {
		t.Fatalf("status %d, op id %q, body %s", rec.Code, rec.Header().Get("X-LocalAI-Op-Id"), rec.Body)
	}
	m := decodeMap(t, rec)
	if m["run_id"] != "run-20261102T031500Z-a1f3" || m["state"] != "running" || m["events"] != "/api/v1/events?types=sync.*" {
		t.Errorf("body = %v", m)
	}
	if len(r.starter.reqs) != 1 {
		t.Fatalf("starter calls = %d", len(r.starter.reqs))
	}
	got := r.starter.reqs[0]
	if strings.Join(got.Roots, ",") != "internal,ark-3f9c0a12" || strings.Join(got.Items, ",") != "qwen-x" || !got.Full || got.OpID != "op_7k2m9q4r8t1c" || got.Actor.Kind != audit.KindOperator {
		t.Errorf("request = %+v", got)
	}
}

func TestArkVerifyDefaultsAreAQuickCheckOfEverything(t *testing.T) {
	r := newArkVerifyRig(t)
	if rec := recordOK(t, r.post("bearer", `{}`)); rec.Code != http.StatusAccepted {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	got := r.starter.reqs[0]
	if len(got.Roots) != 0 || len(got.Items) != 0 || got.Full {
		t.Errorf("request = %+v", got)
	}
}

func TestArkVerifyValidation(t *testing.T) {
	cases := []struct {
		name, body, reason, field string
	}{
		{"an empty root", `{"roots": [" "]}`, "empty_value", "roots"},
		{"an empty item", `{"items": [""]}`, "empty_value", "items"},
		{"too many items", `{"items": [` + strings.TrimSuffix(strings.Repeat(`"x",`, 257), ",") + `]}`, "too_many", "items"},
		{"empty body", ``, "empty_body", ""},
		{"an unknown field", `{"fully": true}`, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newArkVerifyRig(t)
			e := expectError(t, r.post("bearer", tc.body), http.StatusBadRequest, doctor.AGT020)
			if tc.reason != "" && e.Error.Details["reason"] != tc.reason {
				t.Errorf("details = %v, want reason %s", e.Error.Details, tc.reason)
			}
			if tc.field != "" && e.Error.Details["field"] != tc.field {
				t.Errorf("details = %v, want field %s", e.Error.Details, tc.field)
			}
			if len(r.starter.reqs) != 0 {
				t.Error("nothing may start for a bad request")
			}
		})
	}
}

func TestArkVerifyAuthAndErrors(t *testing.T) {
	r := newArkVerifyRig(t)
	expectError(t, do(r.h, req{method: "POST", target: "/api/v1/ark/verify", body: `{}`, header: map[string]string{"Content-Type": "application/json"}}), http.StatusUnauthorized, CodeUnauthenticated)
	if rec := r.post("portal", `{}`); rec.Code < 400 {
		t.Fatalf("a Portal session may not start a verify run: %d %s", rec.Code, rec.Body)
	}
	if len(r.starter.reqs) != 0 {
		t.Fatal("a refused request reached the starter")
	}

	for _, tc := range []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"a run is active", NewError(doctor.SYN005, http.StatusConflict, map[string]any{"run_id": "run-20261102T031500Z-a1f3"}), http.StatusConflict, doctor.SYN005},
		{"a root that is not mounted", NewError(CodeInvalidRequest, http.StatusBadRequest, map[string]any{"field": "roots", "reason": "unknown_root"}), http.StatusBadRequest, CodeInvalidRequest},
		{"nothing to read", NewError(doctor.ARK001, http.StatusConflict, nil), http.StatusConflict, doctor.ARK001},
		{"a bug", errors.New("boom"), http.StatusInternalServerError, CodeInternal},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rr := newArkVerifyRig(t)
			rr.starter.err = tc.err
			expectError(t, rr.post("bearer", `{}`), tc.status, tc.code)
		})
	}
}
