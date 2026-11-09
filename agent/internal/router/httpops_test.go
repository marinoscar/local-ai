package router

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/api"
	"github.com/marinoscar/local-ai/agent/internal/api/apitest"
	"github.com/marinoscar/local-ai/agent/internal/audit"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/router/fake"
	"github.com/marinoscar/local-ai/agent/internal/state"
)

// httpRig serves the three model-operation routes over the real middleware chain, with the real
// orchestrator over the fake router behind them (internal/api has its own table tests with a fake
// orchestrator; these prove the two halves fit and every documented error is produced end to end).
type httpRig struct {
	*orRig
	h http.Handler
}

func newHTTPRig(t *testing.T, mut ...func(*orCfg)) *httpRig {
	t.Helper()
	r := newOrRig(t, mut...)
	reg := api.NewRegistry()
	if err := api.RegisterModelOps(reg, api.ModelOpsDeps{Orchestrator: NewHTTPOps(r.o)}); err != nil {
		t.Fatal(err)
	}
	auth := api.AuthFunc(func(rq *http.Request, _ *api.Route) (api.Principal, error) {
		switch rq.Header.Get("X-Test-Principal") {
		case "bearer":
			return api.Principal{Kind: api.KindOperator, Credential: api.CredBearer, Via: "bearer"}, nil
		case "portal":
			return api.Principal{Kind: api.KindPortal, Credential: api.CredPortal, Via: "portal"}, nil
		}
		return api.Anonymous(), nil
	})
	s, err := api.New(api.Options{Registry: reg, Auth: auth, Version: "test", Mode: func() string { return "online" }})
	if err != nil {
		t.Fatal(err)
	}
	s.SetMutationsOpen(true)
	return &httpRig{orRig: r, h: s.Handler()}
}

// call serves one request as an operator unless hdr says otherwise, and records the response against
// openapi.yaml: the body must match the schema and the status must be documented.
func (r *httpRig) call(method, path, body string, hdr map[string]string) *httptest.ResponseRecorder {
	r.t.Helper()
	q := httptest.NewRequest(method, "/api/v1"+path, strings.NewReader(body))
	q.Host = "127.0.0.1:7070"
	q.Header.Set("Content-Type", "application/json")
	q.Header.Set("X-Test-Principal", "bearer")
	for k, v := range hdr {
		q.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	r.h.ServeHTTP(rec, q)
	apitest.RecordResult(r.t, q, rec)
	return rec
}

type errReply struct {
	Error struct {
		Code    string         `json:"code"`
		Message string         `json:"message"`
		Fix     string         `json:"fix"`
		Details map[string]any `json:"details"`
	} `json:"error"`
}

func (r *httpRig) wantError(rec *httptest.ResponseRecorder, status int, code string) errReply {
	r.t.Helper()
	if rec.Code != status {
		r.t.Fatalf("status %d, want %d: %s", rec.Code, status, rec.Body)
	}
	var e errReply
	if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil {
		r.t.Fatalf("not an error envelope: %s", rec.Body)
	}
	if e.Error.Code != code {
		r.t.Fatalf("code %s, want %s: %s", e.Error.Code, code, rec.Body)
	}
	if e.Error.Message == "" || e.Error.Fix == "" {
		r.t.Errorf("message and fix must be filled: %s", rec.Body)
	}
	return e
}

func bodyMap(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("not JSON: %s", rec.Body)
	}
	return m
}

func TestHTTPLoadEndToEnd(t *testing.T) {
	r := newHTTPRig(t)
	r.load("m-a")
	rec := r.call("POST", "/models/m-b/load", "", nil)
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	m := bodyMap(t, rec)
	if m["id"] != "m-b" || m["status"] != "loaded" || m["already_loaded"] != false || m["load_ms"] != float64(5000) {
		t.Errorf("body: %v", m)
	}
	if got, _ := m["unloaded"].([]any); len(got) != 1 || got[0] != "m-a" {
		t.Errorf("unloaded: %v", m["unloaded"])
	}
	op, _ := m["op_id"].(string)
	if !opIDRe.MatchString(op) || rec.Header().Get("X-LocalAI-Op-Id") != op {
		t.Errorf("op id %q, header %q", op, rec.Header().Get("X-LocalAI-Op-Id"))
	}
	eqStrings(t, "router calls", r.calls(), []string{"unload m-a", "load m-b"})

	// The audit line names the request, the operation and who asked.
	a := r.aud.events()
	if len(a) != 1 {
		t.Fatalf("audit: %+v", a)
	}
	if a[0].Action != audit.ActionModelLoad || a[0].Result != audit.ResultOK || a[0].OpID != op ||
		a[0].ReqID == "" || a[0].ReqID != rec.Header().Get("X-Request-Id") ||
		a[0].Actor.Kind != audit.KindOperator || a[0].Actor.Via != audit.ViaBearer {
		t.Errorf("audit: %+v", a[0])
	}

	// Loading it again is not an error.
	rec = r.call("POST", "/models/m-b/load", `{}`, nil)
	m = bodyMap(t, rec)
	if rec.Code != 200 || m["already_loaded"] != true || m["load_ms"] != float64(0) {
		t.Errorf("repeat: %d %v", rec.Code, m)
	}
}

func TestHTTPLoadWaitFalse(t *testing.T) {
	r := newHTTPRig(t)
	r.load("m-a")
	rec := r.call("POST", "/models/m-b/load", `{"wait":false}`, nil)
	if rec.Code != 202 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	m := bodyMap(t, rec)
	if m["status"] != "loading" || m["id"] != "m-b" {
		t.Errorf("body: %v", m)
	}
	if err := r.o.Wait(bg); err != nil {
		t.Fatal(err)
	}
	eqStrings(t, "loaded", r.loadedNow(), []string{"m-b"})
	if ll := r.lastLoad(); ll.Result != state.LoadOK || ll.OpID != m["op_id"] {
		t.Errorf("last_load: %+v", ll)
	}
	if got := r.evs(); !contains1(got, "loaded m-b") {
		t.Errorf("events: %q", got)
	}
}

// A tight fit still loads, and the warning is on the 202 itself (#232): the planner knows it before the answer, so a
// client that does not wait (aictl, the Portal, the cloud) can show it. The rig checks the body against openapi.yaml.
func TestHTTPLoadWaitFalseCarriesTheMemoryWarning(t *testing.T) {
	t.Run("graphics memory held by other programs: LAI-HW-005 on the 202", func(t *testing.T) {
		r := newHTTPRig(t, func(c *orCfg) { c.env.GOthersMiB = 7900 })
		rec := r.call("POST", "/models/m-a/load", `{"wait":false}`, nil)
		if rec.Code != 202 {
			t.Fatalf("%d %s", rec.Code, rec.Body)
		}
		w, _ := bodyMap(t, rec)["warning"].(map[string]any)
		if w["code"] != doctor.HW005 || w["message"] == "" || len(w) != 2 {
			t.Errorf("warning: %v", w)
		}
		if err := r.o.Wait(bg); err != nil {
			t.Fatal(err)
		}
		eqStrings(t, "loaded", r.loadedNow(), []string{"m-a"})
	})
	t.Run("a load that fits has no warning on the 202", func(t *testing.T) {
		r := newHTTPRig(t)
		rec := r.call("POST", "/models/m-a/load", `{"wait":false}`, nil)
		if _, has := bodyMap(t, rec)["warning"]; rec.Code != 202 || has {
			t.Errorf("%d %s", rec.Code, rec.Body)
		}
		if err := r.o.Wait(bg); err != nil {
			t.Fatal(err)
		}
	})
}

func TestHTTPLoadAsAPortalSession(t *testing.T) {
	r := newHTTPRig(t, orSurvival())
	r.load("m-a")
	// The forced body: no waiting, and the planner evicts what must go.
	rec := r.call("POST", "/models/m-c/load", `{"wait":true,"unload_others":false}`, map[string]string{
		"X-Test-Principal": "portal", "Origin": "http://127.0.0.1:7070",
	})
	if rec.Code != 202 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if err := r.o.Wait(bg); err != nil {
		t.Fatal(err)
	}
	eqStrings(t, "loaded", r.loadedNow(), []string{"m-c"})
	if a := r.aud.events(); len(a) != 1 || a[0].Actor.Kind != audit.KindPortal {
		t.Errorf("audit: %+v", a)
	}
	// A model that does not fit is refused to a Portal session too.
	r.cfg.env.GTotalMiB = 3000
	r.wantError(r.call("POST", "/models/m-d/load", ``, map[string]string{"X-Test-Principal": "portal", "Origin": "http://127.0.0.1:7070"}), 422, doctor.CAT005)
}

// TestHTTPEveryDocumentedLoadError drives each error of agent.md section 3.8.8 through the server.
func TestHTTPEveryDocumentedLoadError(t *testing.T) {
	cases := []struct {
		name   string
		mut    func(*orCfg)
		setup  func(*httpRig)
		path   string
		body   string
		status int
		code   string
		check  func(*testing.T, *httpRig, errReply, *httptest.ResponseRecorder)
	}{
		{name: "unknown model", path: "/models/nope/load", status: 404, code: doctor.AGT021},
		{name: "not a llama model", path: "/models/whisper-x/load", status: 422, code: doctor.CAT005},
		{name: "files missing", mut: func(c *orCfg) { c.presence.missing = map[string]bool{"m-b": true} }, path: "/models/m-b/load", status: 422, code: doctor.LLM008},
		{name: "does not fit", mut: func(c *orCfg) { c.env.GTotalMiB = 3000 }, path: "/models/m-b/load", status: 422, code: doctor.CAT005},
		{name: "no room without unload_others", setup: func(r *httpRig) { r.load("m-a") }, path: "/models/m-b/load", body: `{"unload_others":false}`, status: 409, code: doctor.LLM011},
		{name: "busy", setup: func(r *httpRig) {
			h, _ := r.o.Lock().TryAcquire(HolderOther)
			r.t.Cleanup(h.Release)
		}, path: "/models/m-b/load", status: 409, code: doctor.AGT022,
			check: func(t *testing.T, r *httpRig, e errReply, _ *httptest.ResponseRecorder) {
				h, _ := r.o.Lock().Holder()
				if e.Error.Details["op_id"] != h.OpID {
					t.Errorf("details: %v, holder %s", e.Error.Details, h.OpID)
				}
			}},
		{name: "router down", setup: func(r *httpRig) { r.f.Down(true) }, path: "/models/m-b/load", status: 502, code: doctor.LLM004},
		{name: "router rejects the key", setup: func(r *httpRig) { r.f.SetKey(secret(2)) }, path: "/models/m-b/load", status: 502, code: doctor.LLM005},
		{name: "not in the router's list", setup: func(r *httpRig) { r.f.Remove("m-b") }, path: "/models/m-b/load", status: 500, code: doctor.LLM007},
		{name: "load failed and rolled back", setup: func(r *httpRig) { r.load("m-a"); r.f.FailLoad("m-b", 137) }, path: "/models/m-b/load", status: 500, code: doctor.LLM009,
			check: func(t *testing.T, r *httpRig, e errReply, rec *httptest.ResponseRecorder) {
				d := e.Error.Details
				if d["rolled_back"] != true || d["exit_code"] != float64(137) || d["model"] != "m-b" {
					t.Errorf("details: %v", d)
				}
				if got, _ := d["restored"].([]any); len(got) != 1 || got[0] != "m-a" {
					t.Errorf("restored: %v", d["restored"])
				}
				if op, _ := d["op_id"].(string); !opIDRe.MatchString(op) || rec.Header().Get("X-LocalAI-Op-Id") != op {
					t.Errorf("op id %v header %q", d["op_id"], rec.Header().Get("X-LocalAI-Op-Id"))
				}
				eqStrings(t, "loaded", r.loadedNow(), []string{"m-a"})
			}},
		{name: "nothing could be loaded", setup: func(r *httpRig) {
			r.load("m-a")
			for _, id := range []string{"m-a", "m-b", "m-c", "m-d"} {
				r.f.RejectLoad(id, 500)
			}
		}, path: "/models/m-b/load", status: 500, code: doctor.LLM012,
			check: func(t *testing.T, r *httpRig, e errReply, _ *httptest.ResponseRecorder) {
				if e.Error.Details["rolled_back"] != false {
					t.Errorf("details: %v", e.Error.Details)
				}
			}},
		{name: "load timed out and rolled back", setup: func(r *httpRig) {
			r.load("m-a")
			r.setHook(func(int) {
				if r.f.Status("m-b") == fake.Loading {
					r.f.Set("m-b", fake.Loading)
				}
			})
		}, path: "/models/m-b/load", status: 504, code: doctor.LLM010,
			check: func(t *testing.T, r *httpRig, e errReply, _ *httptest.ResponseRecorder) {
				if e.Error.Details["rolled_back"] != true {
					t.Errorf("details: %v", e.Error.Details)
				}
			}},
		{name: "the agent is shutting down", setup: func(r *httpRig) {
			ctx, cancel := context.WithTimeout(bg, time.Second)
			defer cancel()
			_ = r.o.Shutdown(ctx)
		}, path: "/models/m-b/load", status: 503, code: doctor.AGT024},
		{name: "invalid body", path: "/models/m-b/load", body: `{"wait":"maybe"}`, status: 400, code: doctor.AGT020},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newHTTPRig(t, tc.mut)
			if tc.setup != nil {
				tc.setup(r)
			}
			rec := r.call("POST", tc.path, tc.body, nil)
			e := r.wantError(rec, tc.status, tc.code)
			if tc.check != nil {
				tc.check(t, r, e, rec)
			}
			if tc.status == 503 && rec.Header().Get("Retry-After") != "5" {
				t.Errorf("Retry-After = %q", rec.Header().Get("Retry-After"))
			}
		})
	}
}

func TestHTTPDoesNotFitIsLocalized(t *testing.T) {
	r := newHTTPRig(t, func(c *orCfg) { c.env.GTotalMiB = 3000 })
	en := r.wantError(r.call("POST", "/models/m-b/load", "", nil), 422, doctor.CAT005)
	es := r.wantError(r.call("POST", "/models/m-b/load", "", map[string]string{"Accept-Language": "es"}), 422, doctor.CAT005)
	if !strings.Contains(en.Error.Message, "graphics memory") || !strings.Contains(es.Error.Message, "memoria gráfica") {
		t.Errorf("en %q es %q", en.Error.Message, es.Error.Message)
	}
	if en.Error.Fix == es.Error.Fix {
		t.Errorf("the fix was not localized: %q", es.Error.Fix)
	}
}

func TestHTTPUnloadEndToEnd(t *testing.T) {
	r := newHTTPRig(t, orMax(2))
	r.load("m-a", "m-b")
	rec := r.call("POST", "/models/m-a/unload", "", nil)
	m := bodyMap(t, rec)
	op, _ := m["op_id"].(string)
	if rec.Code != 200 || m["status"] != "unloaded" || m["already_unloaded"] != false || !opIDRe.MatchString(op) || rec.Header().Get("X-LocalAI-Op-Id") != op {
		t.Fatalf("%d %v", rec.Code, m)
	}
	eqStrings(t, "loaded", r.loadedNow(), []string{"m-b"})
	a := r.aud.events()
	if len(a) != 1 || a[0].Action != audit.ActionModelUnload || a[0].ReqID != rec.Header().Get("X-Request-Id") || a[0].OpID != op {
		t.Errorf("audit: %+v", a)
	}
	// Again: already unloaded.
	rec = r.call("POST", "/models/m-a/unload", `{}`, nil)
	if m := bodyMap(t, rec); rec.Code != 200 || m["already_unloaded"] != true {
		t.Errorf("repeat: %d %v", rec.Code, m)
	}
}

func TestHTTPEveryDocumentedUnloadError(t *testing.T) {
	cases := []struct {
		name   string
		mut    func(*orCfg)
		setup  func(*httpRig)
		path   string
		body   string
		status int
		code   string
	}{
		{name: "unknown model", path: "/models/nope/unload", status: 404, code: doctor.AGT021},
		{name: "not a llama model", path: "/models/whisper-x/unload", status: 422, code: doctor.CAT005},
		{name: "busy", setup: func(r *httpRig) { h, _ := r.o.Lock().TryAcquire(HolderOther); r.t.Cleanup(h.Release) }, path: "/models/m-a/unload", status: 409, code: doctor.AGT022},
		{name: "last model in survival mode", mut: orSurvival(), setup: func(r *httpRig) { r.load("m-c") }, path: "/models/m-c/unload", status: 409, code: doctor.AGT023},
		{name: "router down", setup: func(r *httpRig) { r.f.Down(true) }, path: "/models/m-a/unload", status: 502, code: doctor.LLM004},
		{name: "model would not stop and llama cannot be restarted", mut: func(c *orCfg) { c.noRestarter = true },
			setup: func(r *httpRig) { r.load("m-a"); r.f.Stick("m-a", true) }, path: "/models/m-a/unload", status: 504, code: doctor.LLM031},
		{name: "restart failed", setup: func(r *httpRig) { r.load("m-a"); r.f.Stick("m-a", true); r.rs.err = errTest }, path: "/models/m-a/unload", status: 502, code: doctor.LLM004},
		{name: "nothing could be loaded afterwards", mut: orMax(2), setup: func(r *httpRig) {
			r.load("m-a", "m-b")
			r.f.Stick("m-a", true)
			r.f.RejectLoad("m-b", 500)
			r.rs.hook = r.f.Restart
		}, path: "/models/m-a/unload", status: 500, code: doctor.LLM012},
		{name: "invalid body", path: "/models/m-a/unload", body: `{"force":"yes"}`, status: 400, code: doctor.AGT020},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newHTTPRig(t, tc.mut)
			if tc.setup != nil {
				tc.setup(r)
			}
			r.wantError(r.call("POST", tc.path, tc.body, nil), tc.status, tc.code)
		})
	}
}

func TestHTTPUnloadForceAndRestart(t *testing.T) {
	t.Run("force unloads the last model in survival mode", func(t *testing.T) {
		r := newHTTPRig(t, orSurvival())
		r.load("m-c")
		rec := r.call("POST", "/models/m-c/unload", `{"force":true}`, nil)
		if rec.Code != 200 || len(r.loadedNow()) != 0 {
			t.Errorf("%d %s loaded %v", rec.Code, rec.Body, r.loadedNow())
		}
	})
	t.Run("a hung model restarts llama and the rest comes back", func(t *testing.T) {
		r := newHTTPRig(t, orMax(2))
		r.load("m-a", "m-b")
		r.f.Stick("m-a", true)
		r.rs.hook = r.f.Restart
		rec := r.call("POST", "/models/m-a/unload", "", nil)
		m := bodyMap(t, rec)
		if rec.Code != 200 || m["restarted"] != true {
			t.Fatalf("%d %v", rec.Code, m)
		}
		if got, _ := m["restored"].([]any); len(got) != 1 || got[0] != "m-b" {
			t.Errorf("restored: %v", m["restored"])
		}
	})
}

func TestHTTPDefaultEndToEnd(t *testing.T) {
	r := newHTTPRig(t)
	rec := r.call("PUT", "/models/default", `{"id":"m-b"}`, nil)
	m := bodyMap(t, rec)
	// Without a generator the file is not rewritten; the profile's default is the previous one.
	if rec.Code != 200 || m["default"] != "m-b" || m["previous"] != "m-a" || m["ini_regenerated"] != false {
		t.Fatalf("%d %v", rec.Code, m)
	}
	if got := r.snap().Models.Default; got == nil || *got != "m-b" {
		t.Errorf("state: %v", got)
	}
	a := r.aud.events()
	if len(a) != 1 || a[0].Action != audit.ActionModelSetDefault || a[0].ReqID != rec.Header().Get("X-Request-Id") || a[0].Target["model"] != "m-b" {
		t.Errorf("audit: %+v", a)
	}
	if got := r.evs(); len(got) != 1 || !strings.HasPrefix(got[0], "default_changed m-b previous=m-a") {
		t.Errorf("events: %q", got)
	}
	if n := len(r.calls()); n != 0 {
		t.Errorf("a default change called the router: %v", r.calls())
	}
}

func TestHTTPEveryDocumentedDefaultError(t *testing.T) {
	cases := []struct {
		name   string
		mut    func(*orCfg)
		body   string
		status int
		code   string
	}{
		{"unknown model", nil, `{"id":"nope"}`, 404, doctor.AGT021},
		{"not a llama model", nil, `{"id":"whisper-x"}`, 422, doctor.CAT005},
		{"files missing", func(c *orCfg) { c.presence.missing = map[string]bool{"m-b": true} }, `{"id":"m-b"}`, 422, doctor.LLM008},
		{"does not fit", func(c *orCfg) { c.env.GTotalMiB = 3000 }, `{"id":"m-b"}`, 422, doctor.CAT005},
		{"no id", nil, `{}`, 400, doctor.AGT020},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newHTTPRig(t, tc.mut)
			r.wantError(r.call("PUT", "/models/default", tc.body, nil), tc.status, tc.code)
			if r.snap().Models.Default != nil {
				t.Error("the default was set")
			}
		})
	}
}
