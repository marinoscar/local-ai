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

// fakeModelOps is a ModelOps that records what the routes asked and answers what a test scripted.
type fakeModelOps struct {
	mu sync.Mutex

	loads    []ModelLoadRequest
	unloads  []ModelUnloadRequest
	defaults []string

	loadRes    ModelLoadResult
	loadErr    error
	unloadRes  ModelUnloadResult
	unloadErr  error
	defaultRes ModelDefaultResult
	defaultErr error
}

func (f *fakeModelOps) Load(_ context.Context, req ModelLoadRequest) (ModelLoadResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.loads = append(f.loads, req)
	return f.loadRes, f.loadErr
}

func (f *fakeModelOps) Unload(_ context.Context, req ModelUnloadRequest) (ModelUnloadResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.unloads = append(f.unloads, req)
	return f.unloadRes, f.unloadErr
}

func (f *fakeModelOps) SetDefault(_ context.Context, id string, _ audit.Actor) (ModelDefaultResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.defaults = append(f.defaults, id)
	return f.defaultRes, f.defaultErr
}

// opsRig serves the three routes over the real middleware chain with a fake orchestrator.
type opsRig struct {
	t   *testing.T
	ops *fakeModelOps
	srv *Server
	h   http.Handler
}

func newOpsRig(t *testing.T, mod ...func(*ModelOpsDeps)) *opsRig {
	t.Helper()
	r := &opsRig{t: t, ops: &fakeModelOps{
		loadRes:    ModelLoadResult{ID: "qwen3.5-9b-q4km", LoadMS: 7100, Unloaded: []string{"small-vlm-q4km"}, OpID: "op_7k2m9q4r8t1c"},
		unloadRes:  ModelUnloadResult{ID: "qwen3.5-9b-q4km", OpID: "op_7k2m9q4r8t1c"},
		defaultRes: ModelDefaultResult{Default: "qwen3.5-9b-q4km", Previous: "small-vlm-q4km", IniRegenerated: true},
	}}
	deps := ModelOpsDeps{Orchestrator: r.ops}
	for _, m := range mod {
		m(&deps)
	}
	reg := NewRegistry()
	if err := RegisterModelOps(reg, deps); err != nil {
		t.Fatal(err)
	}
	r.srv, _ = newTestServer(t, func(o *Options) { o.Registry = reg })
	r.h = r.srv.Handler()
	return r
}

func (r *opsRig) do(method, target, body string, hdr map[string]string) *httptest.ResponseRecorder {
	return do(r.h, req{method: method, target: "/api/v1" + target, body: body, header: jsonHeaders(hdr)})
}

func apiErr(status int, code string, details map[string]any) *Error {
	return NewError(code, status, details)
}

func TestModelOpsRoutesMetadata(t *testing.T) {
	routes := ModelOpsRoutes(ModelOpsDeps{})
	type want struct {
		method, pattern string
		auth            []Level
		timeout         time.Duration
	}
	wants := []want{
		{"POST", "/models/{id}/load", []Level{AuthOp, AuthPortal}, (300*3 + 30) * time.Second},
		{"POST", "/models/{id}/unload", []Level{AuthOp}, 90 * time.Second},
		{"PUT", "/models/default", []Level{AuthOp}, 5 * time.Second},
	}
	if len(routes) != len(wants) {
		t.Fatalf("%d routes", len(routes))
	}
	for i, w := range wants {
		rt := routes[i]
		if rt.Method != w.method || rt.Pattern != w.pattern || rt.Timeout != w.timeout || len(rt.Auth) != len(w.auth) || rt.GateExempt || !rt.Mutating() {
			t.Errorf("route %d = %+v", i, rt)
			continue
		}
		for j, l := range w.auth {
			if rt.Auth[j] != l {
				t.Errorf("route %d auth = %v", i, rt.Auth)
			}
		}
	}
	// The load deadline follows the node: load_timeout x (n+2) + 30 s.
	got := ModelOpsRoutes(ModelOpsDeps{LoadTimeout: 60 * time.Second, ModelsMax: 3})[0].Timeout
	if got != (60*5+30)*time.Second {
		t.Errorf("load timeout = %v", got)
	}
	if got := ModelOpsRoutes(ModelOpsDeps{ModelsMax: -4})[0].Timeout; got != (300*3+30)*time.Second {
		t.Errorf("a models_max below 1 counts as 1: %v", got)
	}
}

func TestRegisterModelOps(t *testing.T) {
	if err := RegisterModelOps(NewRegistry(), ModelOpsDeps{}); err == nil {
		t.Error("an orchestrator is required")
	}
	reg := NewRegistry()
	d := ModelOpsDeps{Orchestrator: &fakeModelOps{}}
	if err := RegisterModelOps(reg, d); err != nil {
		t.Fatal(err)
	}
	if err := RegisterModelOps(reg, d); err == nil {
		t.Error("registering twice must fail")
	}
}

func TestLoadRoute(t *testing.T) {
	t.Run("an empty body means wait and unload others", func(t *testing.T) {
		r := newOpsRig(t)
		rec := recordOK(t, r.do("POST", "/models/qwen3.5-9b-q4km/load", "", nil))
		if rec.Code != 200 {
			t.Fatalf("%d %s", rec.Code, rec.Body)
		}
		want := `{"id":"qwen3.5-9b-q4km","status":"loaded","already_loaded":false,"load_ms":7100,"unloaded":["small-vlm-q4km"],"op_id":"op_7k2m9q4r8t1c"}`
		if strings.TrimSpace(rec.Body.String()) != want {
			t.Errorf("body\n got  %s\n want %s", rec.Body, want)
		}
		if rec.Header().Get("X-LocalAI-Op-Id") != "op_7k2m9q4r8t1c" {
			t.Errorf("X-LocalAI-Op-Id = %q", rec.Header().Get("X-LocalAI-Op-Id"))
		}
		got := r.ops.loads
		if len(got) != 1 || got[0].ID != "qwen3.5-9b-q4km" || !got[0].Wait || !got[0].UnloadOthers || got[0].Actor.Kind != audit.KindOperator || got[0].Actor.Via != audit.ViaBearer {
			t.Errorf("request: %+v", got)
		}
	})
	t.Run("wait false answers 202", func(t *testing.T) {
		r := newOpsRig(t)
		r.ops.loadRes = ModelLoadResult{ID: "m", Accepted: true, OpID: "op_7k2m9q4r8t1c"}
		rec := recordOK(t, r.do("POST", "/models/m/load", `{"wait":false,"unload_others":false}`, nil))
		if rec.Code != 202 {
			t.Fatalf("%d %s", rec.Code, rec.Body)
		}
		if want := `{"id":"m","status":"loading","op_id":"op_7k2m9q4r8t1c"}`; strings.TrimSpace(rec.Body.String()) != want {
			t.Errorf("body %s", rec.Body)
		}
		if rec.Header().Get("X-LocalAI-Op-Id") != "op_7k2m9q4r8t1c" {
			t.Error("no op id header on the 202")
		}
		if got := r.ops.loads[0]; got.Wait || got.UnloadOthers {
			t.Errorf("flags were not passed on: %+v", got)
		}
	})
	t.Run("only unload_others given keeps wait true", func(t *testing.T) {
		r := newOpsRig(t)
		recordOK(t, r.do("POST", "/models/m/load", `{"unload_others":false}`, nil))
		if got := r.ops.loads[0]; !got.Wait || got.UnloadOthers {
			t.Errorf("%+v", got)
		}
	})
	t.Run("already loaded", func(t *testing.T) {
		r := newOpsRig(t)
		r.ops.loadRes = ModelLoadResult{ID: "m", AlreadyLoaded: true, OpID: "op_7k2m9q4r8t1c"}
		rec := recordOK(t, r.do("POST", "/models/m/load", `{}`, nil))
		want := `{"id":"m","status":"loaded","already_loaded":true,"load_ms":0,"unloaded":[],"op_id":"op_7k2m9q4r8t1c"}`
		if rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != want {
			t.Errorf("%d %s", rec.Code, rec.Body)
		}
	})
	t.Run("a warning is reported", func(t *testing.T) {
		r := newOpsRig(t)
		r.ops.loadRes.Warning = &ModelWarning{Code: doctor.HW005, Message: "Other programs are using a lot of graphics memory; this model may run slower."}
		rec := recordOK(t, r.do("POST", "/models/m/load", "", nil))
		m := decodeMap(t, rec)
		w, _ := m["warning"].(map[string]any)
		if w["code"] != doctor.HW005 || w["message"] == "" {
			t.Errorf("warning: %v", m["warning"])
		}
	})
	t.Run("a Portal session gets a forced body", func(t *testing.T) {
		r := newOpsRig(t)
		r.ops.loadRes = ModelLoadResult{ID: "m", Accepted: true, OpID: "op_7k2m9q4r8t1c"}
		// The client body is ignored altogether, even one that would be refused.
		rec := recordOK(t, r.do("POST", "/models/m/load", `{"wait":true,"unload_others":false,"bogus":1}`, map[string]string{"X-Test-Principal": "portal", "Origin": "http://127.0.0.1:7070"}))
		if rec.Code != 202 {
			t.Fatalf("%d %s", rec.Code, rec.Body)
		}
		got := r.ops.loads[0]
		if got.Wait || !got.UnloadOthers || got.Actor.Kind != audit.KindPortal || got.Actor.Via != audit.ViaPortal {
			t.Errorf("request: %+v", got)
		}
	})
	t.Run("an operator over the socket is recorded as such", func(t *testing.T) {
		r := newOpsRig(t)
		recordOK(t, r.do("POST", "/models/m/load", "", map[string]string{"X-Test-Principal": "socket"}))
		if got := r.ops.loads[0].Actor; got.Kind != audit.KindOperator || got.Via != audit.ViaSocket {
			t.Errorf("actor: %+v", got)
		}
	})
}

func TestLoadRouteRefusals(t *testing.T) {
	r := newOpsRig(t)
	cases := []struct {
		name   string
		body   string
		hdr    map[string]string
		status int
		code   string
		reason string
	}{
		{"unknown field", `{"wait":true,"x":1}`, nil, 400, CodeInvalidRequest, "unknown_field"},
		{"wrong type", `{"wait":"yes"}`, nil, 400, CodeInvalidRequest, "wrong_type"},
		{"malformed", `{"wait":`, nil, 400, CodeInvalidRequest, "malformed_json"},
		{"trailing data", `{} {}`, nil, 400, CodeInvalidRequest, "trailing_data"},
		{"wrong content type", `{}`, map[string]string{"Content-Type": "text/plain"}, 415, CodeInvalidRequest, ""},
		{"too large", `{"wait":true,"pad":"` + strings.Repeat("a", 1<<20) + `"}`, nil, 413, CodeTooLarge, ""},
		{"no credentials", ``, map[string]string{"X-Test-Principal": "none"}, 401, CodeUnauthenticated, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := r.do("POST", "/models/m/load", tc.body, tc.hdr)
			e := expectError(t, rec, tc.status, tc.code)
			if tc.reason != "" && e.Error.Details["reason"] != tc.reason {
				t.Errorf("details: %v", e.Error.Details)
			}
		})
	}
	if len(r.ops.loads) != 0 {
		t.Errorf("the orchestrator was called for a refused request: %+v", r.ops.loads)
	}
}

// orchestratorErrors are the errors an orchestrator answers with and how the routes show them
// (agent.md sections 3.8.8 to 3.8.10). The first block is for every route that can fail this way.
var orchestratorErrors = []struct {
	name    string
	err     error
	status  int
	code    string
	details map[string]any
	opHdr   string
}{
	{"unknown model", apiErr(404, CodeNotFound, map[string]any{"model": "m"}), 404, doctor.AGT021, map[string]any{"model": "m"}, ""},
	{"busy", apiErr(409, CodeBusy, map[string]any{"op_id": "op_aaaaaaaaaaa2", "running": "load"}), 409, doctor.AGT022, map[string]any{"op_id": "op_aaaaaaaaaaa2"}, ""},
	{"no room", apiErr(409, doctor.LLM011, map[string]any{"model": "m"}), 409, doctor.LLM011, nil, ""},
	{"last model in survival", apiErr(409, doctor.AGT023, map[string]any{"model": "m"}), 409, doctor.AGT023, nil, ""},
	{"does not fit", apiErr(422, doctor.CAT005, map[string]any{"model": "m"}), 422, doctor.CAT005, nil, ""},
	{"files missing", apiErr(422, doctor.LLM008, map[string]any{"model": "m"}), 422, doctor.LLM008, nil, ""},
	{"load failed", apiErr(500, doctor.LLM009, map[string]any{"op_id": "op_7k2m9q4r8t1c", "rolled_back": true, "restored": []string{"a"}}), 500, doctor.LLM009,
		map[string]any{"rolled_back": true}, "op_7k2m9q4r8t1c"},
	{"nothing loaded", apiErr(500, doctor.LLM012, map[string]any{"op_id": "op_7k2m9q4r8t1c", "rolled_back": false, "restored": []string{}}), 500, doctor.LLM012,
		map[string]any{"rolled_back": false}, "op_7k2m9q4r8t1c"},
	{"model list not written", apiErr(500, doctor.LLM007, nil), 500, doctor.LLM007, nil, ""},
	{"router down", apiErr(502, doctor.LLM004, nil), 502, doctor.LLM004, nil, ""},
	{"router rejects the key", apiErr(502, doctor.LLM005, nil), 502, doctor.LLM005, nil, ""},
	{"load timed out", apiErr(504, doctor.LLM010, map[string]any{"op_id": "op_7k2m9q4r8t1c", "rolled_back": true, "restored": []string{"a"}}), 504, doctor.LLM010,
		map[string]any{"rolled_back": true}, "op_7k2m9q4r8t1c"},
	{"unload hung", apiErr(504, doctor.LLM031, nil), 504, doctor.LLM031, nil, ""},
	{"agent stopping", apiErr(503, CodeStarting, nil), 503, doctor.AGT024, nil, ""},
	{"a bug", errors.New("boom"), 500, doctor.AGT099, nil, ""},
	{"the route deadline", context.DeadlineExceeded, 504, doctor.AGT099, map[string]any{"reason": "deadline_exceeded"}, ""},
}

func checkOrchestratorError(t *testing.T, rec *httptest.ResponseRecorder, tc struct {
	name    string
	err     error
	status  int
	code    string
	details map[string]any
	opHdr   string
}) {
	t.Helper()
	e := expectError(t, rec, tc.status, tc.code)
	for k, want := range tc.details {
		if got := e.Error.Details[k]; got != want {
			t.Errorf("details[%s] = %v, want %v", k, got, want)
		}
	}
	if got := rec.Header().Get("X-LocalAI-Op-Id"); got != tc.opHdr {
		t.Errorf("X-LocalAI-Op-Id = %q, want %q", got, tc.opHdr)
	}
	if tc.status == 503 && rec.Header().Get("Retry-After") != "5" {
		t.Errorf("Retry-After = %q", rec.Header().Get("Retry-After"))
	}
	if tc.code == doctor.AGT099 && e.Error.Details["request_id"] == nil {
		t.Errorf("a 500 or 504 of a bug carries request_id: %v", e.Error.Details)
	}
}

func TestLoadRouteMapsEveryOrchestratorError(t *testing.T) {
	for _, tc := range orchestratorErrors {
		t.Run(tc.name, func(t *testing.T) {
			r := newOpsRig(t)
			r.ops.loadErr = tc.err
			checkOrchestratorError(t, r.do("POST", "/models/m/load", "", nil), tc)
		})
	}
}

func TestUnloadRoute(t *testing.T) {
	t.Run("an empty body is not forced", func(t *testing.T) {
		r := newOpsRig(t)
		rec := recordOK(t, r.do("POST", "/models/qwen3.5-9b-q4km/unload", "", nil))
		want := `{"id":"qwen3.5-9b-q4km","status":"unloaded","already_unloaded":false,"op_id":"op_7k2m9q4r8t1c"}`
		if rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != want {
			t.Fatalf("%d %s", rec.Code, rec.Body)
		}
		if rec.Header().Get("X-LocalAI-Op-Id") != "op_7k2m9q4r8t1c" {
			t.Error("no op id header")
		}
		if got := r.ops.unloads; len(got) != 1 || got[0].ID != "qwen3.5-9b-q4km" || got[0].Force || got[0].Actor.Kind != audit.KindOperator {
			t.Errorf("request: %+v", got)
		}
	})
	t.Run("force is passed on", func(t *testing.T) {
		r := newOpsRig(t)
		recordOK(t, r.do("POST", "/models/m/unload", `{"force":true}`, nil))
		if got := r.ops.unloads[0]; !got.Force {
			t.Errorf("%+v", got)
		}
	})
	t.Run("already unloaded", func(t *testing.T) {
		r := newOpsRig(t)
		r.ops.unloadRes = ModelUnloadResult{ID: "m", AlreadyUnloaded: true, OpID: "op_7k2m9q4r8t1c"}
		rec := recordOK(t, r.do("POST", "/models/m/unload", `{}`, nil))
		if m := decodeMap(t, rec); m["already_unloaded"] != true || m["status"] != "unloaded" {
			t.Errorf("%v", m)
		}
	})
	t.Run("a restart is reported", func(t *testing.T) {
		r := newOpsRig(t)
		r.ops.unloadRes = ModelUnloadResult{ID: "m", OpID: "op_7k2m9q4r8t1c", Restarted: true, Restored: []string{"a", "b"}}
		rec := recordOK(t, r.do("POST", "/models/m/unload", ``, nil))
		m := decodeMap(t, rec)
		if m["restarted"] != true {
			t.Errorf("%v", m)
		}
		if got, _ := m["restored"].([]any); len(got) != 2 {
			t.Errorf("restored: %v", m["restored"])
		}
	})
	t.Run("a Portal session is not allowed", func(t *testing.T) {
		r := newOpsRig(t)
		expectError(t, r.do("POST", "/models/m/unload", ``, map[string]string{"X-Test-Principal": "portal"}), 401, CodePortalSessionEnded)
		if len(r.ops.unloads) != 0 {
			t.Error("the orchestrator was called")
		}
	})
}

func TestUnloadRouteRefusals(t *testing.T) {
	r := newOpsRig(t)
	cases := []struct {
		name, body string
		hdr        map[string]string
		status     int
		code       string
	}{
		{"unknown field", `{"force":true,"x":1}`, nil, 400, CodeInvalidRequest},
		{"wrong type", `{"force":"yes"}`, nil, 400, CodeInvalidRequest},
		{"malformed", `{`, nil, 400, CodeInvalidRequest},
		{"wrong content type", `{}`, map[string]string{"Content-Type": "text/plain"}, 415, CodeInvalidRequest},
		{"too large", `{"force":true,"pad":"` + strings.Repeat("a", 1<<20) + `"}`, nil, 413, CodeTooLarge},
		{"no credentials", ``, map[string]string{"X-Test-Principal": "none"}, 401, CodeUnauthenticated},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			expectError(t, r.do("POST", "/models/m/unload", tc.body, tc.hdr), tc.status, tc.code)
		})
	}
	if len(r.ops.unloads) != 0 {
		t.Errorf("the orchestrator was called: %+v", r.ops.unloads)
	}
}

func TestUnloadRouteMapsEveryOrchestratorError(t *testing.T) {
	for _, tc := range orchestratorErrors {
		t.Run(tc.name, func(t *testing.T) {
			r := newOpsRig(t)
			r.ops.unloadErr = tc.err
			checkOrchestratorError(t, r.do("POST", "/models/m/unload", "", nil), tc)
		})
	}
}

func TestDefaultRoute(t *testing.T) {
	t.Run("sets the default", func(t *testing.T) {
		r := newOpsRig(t)
		rec := recordOK(t, r.do("PUT", "/models/default", `{"id":"qwen3.5-9b-q4km"}`, nil))
		want := `{"default":"qwen3.5-9b-q4km","previous":"small-vlm-q4km","ini_regenerated":true}`
		if rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != want {
			t.Fatalf("%d %s", rec.Code, rec.Body)
		}
		if got := r.ops.defaults; len(got) != 1 || got[0] != "qwen3.5-9b-q4km" {
			t.Errorf("request: %v", got)
		}
	})
	t.Run("no previous default is null", func(t *testing.T) {
		r := newOpsRig(t)
		r.ops.defaultRes = ModelDefaultResult{Default: "m"}
		rec := recordOK(t, r.do("PUT", "/models/default", `{"id":"m"}`, nil))
		if m := decodeMap(t, rec); m["previous"] != nil || m["ini_regenerated"] != false {
			t.Errorf("%v", m)
		}
	})
}

func TestDefaultRouteRefusals(t *testing.T) {
	r := newOpsRig(t)
	cases := []struct {
		name, body string
		hdr        map[string]string
		status     int
		code       string
		field      string
	}{
		{"empty body", ``, nil, 400, CodeInvalidRequest, ""},
		{"no id", `{}`, nil, 400, CodeInvalidRequest, "id"},
		{"empty id", `{"id":""}`, nil, 400, CodeInvalidRequest, "id"},
		{"unknown field", `{"id":"m","x":1}`, nil, 400, CodeInvalidRequest, "x"},
		{"wrong type", `{"id":7}`, nil, 400, CodeInvalidRequest, "id"},
		{"wrong content type", `{"id":"m"}`, map[string]string{"Content-Type": "text/plain"}, 415, CodeInvalidRequest, ""},
		{"too large", `{"id":"` + strings.Repeat("a", 1<<20) + `"}`, nil, 413, CodeTooLarge, ""},
		{"no credentials", `{"id":"m"}`, map[string]string{"X-Test-Principal": "none"}, 401, CodeUnauthenticated, ""},
		{"a Portal session", `{"id":"m"}`, map[string]string{"X-Test-Principal": "portal"}, 401, CodePortalSessionEnded, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := expectError(t, r.do("PUT", "/models/default", tc.body, tc.hdr), tc.status, tc.code)
			if tc.field != "" && e.Error.Details["field"] != tc.field {
				t.Errorf("details: %v", e.Error.Details)
			}
		})
	}
	if len(r.ops.defaults) != 0 {
		t.Errorf("the orchestrator was called: %v", r.ops.defaults)
	}
}

func TestDefaultRouteMapsEveryOrchestratorError(t *testing.T) {
	// SetDefault takes no lock and loads nothing: only these errors are documented for the route.
	possible := map[string]bool{
		"unknown model": true, "does not fit": true, "files missing": true, "model list not written": true,
		"agent stopping": true, "a bug": true, "the route deadline": true,
	}
	for _, tc := range orchestratorErrors {
		if !possible[tc.name] {
			continue
		}
		t.Run(tc.name, func(t *testing.T) {
			r := newOpsRig(t)
			r.ops.defaultErr = tc.err
			tc.opHdr = "" // PUT /models/default creates no operation
			rec := r.do("PUT", "/models/default", `{"id":"m"}`, nil)
			if rec.Header().Get("X-LocalAI-Op-Id") != "" && (tc.code != doctor.LLM009 && tc.code != doctor.LLM010 && tc.code != doctor.LLM012) {
				t.Errorf("unexpected op id header %q", rec.Header().Get("X-LocalAI-Op-Id"))
			}
			// The op id header of a failed load is the same helper: only the status and code are
			// the contract of this route.
			e := expectError(t, rec, tc.status, tc.code)
			for k, want := range tc.details {
				if got := e.Error.Details[k]; got != want {
					t.Errorf("details[%s] = %v, want %v", k, got, want)
				}
			}
		})
	}
}

func TestModelOpsAreHeldBackWhileTheGateIsClosed(t *testing.T) {
	r := newOpsRig(t)
	r.srv.SetMutationsOpen(false)
	for _, c := range []struct{ method, target, body string }{
		{"POST", "/models/m/load", ""},
		{"POST", "/models/m/unload", ""},
		{"PUT", "/models/default", `{"id":"m"}`},
	} {
		rec := r.do(c.method, c.target, c.body, nil)
		expectError(t, rec, 503, CodeStarting)
		if rec.Header().Get("Retry-After") != "5" {
			t.Errorf("%s %s: Retry-After %q", c.method, c.target, rec.Header().Get("Retry-After"))
		}
	}
	if len(r.ops.loads)+len(r.ops.unloads)+len(r.ops.defaults) != 0 {
		t.Error("the orchestrator was called through a closed gate")
	}
}

func TestModelOpsRoutesAnswer405ForOtherMethods(t *testing.T) {
	r := newOpsRig(t)
	for _, c := range []struct{ method, target, allow string }{
		{"GET", "/models/m/load", "POST"},
		{"PUT", "/models/m/unload", "POST"},
		{"POST", "/models/default", "PUT"},
	} {
		rec := r.do(c.method, c.target, "", nil)
		expectError(t, rec, 405, CodeInvalidRequest)
		if rec.Header().Get("Allow") != c.allow {
			t.Errorf("%s %s: Allow %q", c.method, c.target, rec.Header().Get("Allow"))
		}
	}
}
