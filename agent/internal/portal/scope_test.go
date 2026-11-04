package portal_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/api"
	"github.com/marinoscar/local-ai/agent/internal/auth"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/portal"
)

// scopeRig is the real middleware chain (api.Server) with the real auth.Service and the real
// Sessions, over routes that carry the Auth lists of the production routes.
type scopeRig struct {
	t      *testing.T
	m      *mode
	clk    *clock
	s      *portal.Sessions
	met    *portal.Metrics
	h      http.Handler
	cookie string
	hits   map[string]int
}

const origin = "http://127.0.0.1:7070"

func newScopeRig(t *testing.T) *scopeRig {
	t.Helper()
	r := &scopeRig{t: t, m: &mode{m: "survival"}, clk: &clock{t: t0}, hits: map[string]int{}}
	r.s, _, r.met = newSessions(t, r.m, r.clk)
	reg := api.NewRegistry()
	type rt struct {
		method, pattern string
		auth            []api.Level
	}
	op, portalToo := []api.Level{api.AuthOp}, []api.Level{api.AuthOp, api.AuthPortal}
	for _, x := range []rt{
		{"POST", "/models/{id}/load", portalToo}, {"POST", "/speech/stt", portalToo}, {"POST", "/speech/tts", portalToo},
		{"GET", "/speech/voices", portalToo}, {"GET", "/events", portalToo},
		{"GET", "/status", op}, {"GET", "/services", op}, {"POST", "/mode", []api.Level{api.AuthAdminStar}}, {"GET", "/key", []api.Level{api.AuthAdmin}},
		{"DELETE", "/models/{id}", []api.Level{api.AuthAdmin}}, {"POST", "/models/{id}/unload", op}, {"POST", "/cloud/enroll", []api.Level{api.AuthAdmin}},
		{"POST", "/mode/survival", []api.Level{api.AuthNone}}, {"GET", "/portal/status", []api.Level{api.AuthNone}},
	} {
		x := x
		err := reg.Add(api.Route{Method: x.method, Pattern: x.pattern, Auth: x.auth, GateExempt: true, MediaTypes: []string{"application/json", "audio/wav"},
			Handler: api.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) error {
				r.hits[x.method+" "+x.pattern]++
				w.WriteHeader(http.StatusOK)
				return nil
			})})
		if err != nil {
			t.Fatal(err)
		}
	}
	svc := auth.New(auth.Config{Portal: r.s, TokenPath: t.TempDir() + "/tok", PINPath: t.TempDir() + "/pin"})
	srv, err := api.New(api.Options{Registry: reg, Auth: svc, Version: "test", Mode: r.m.get, Guard: r.s.Guard})
	if err != nil {
		t.Fatal(err)
	}
	srv.SetMutationsOpen(true)
	r.h = srv.Handler()
	r.cookie, err = r.s.Issue()
	if err != nil {
		t.Fatal(err)
	}
	return r
}

type call struct {
	method, target string
	cookie         string
	header         map[string]string
}

func (r *scopeRig) do(c call) *httptest.ResponseRecorder {
	r.t.Helper()
	req := httptest.NewRequest(c.method, "/api/v1"+c.target, nil)
	req.Host = "127.0.0.1:7070"
	if c.method != http.MethodGet {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.cookie != "" {
		req.AddCookie(&http.Cookie{Name: portal.Cookie, Value: c.cookie})
	}
	for k, v := range c.header {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	r.h.ServeHTTP(rec, req)
	return rec
}

func (r *scopeRig) post(target string, header map[string]string) *httptest.ResponseRecorder {
	return r.do(call{method: "POST", target: target, cookie: r.cookie, header: header})
}

func errorCode(t testing.TB, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var e struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &e)
	return e.Error.Code
}

var withOrigin = map[string]string{"Origin": origin}

func TestScopeMatrix(t *testing.T) {
	cases := []struct {
		method, target string
		wantStatus     int
		wantCode       string
	}{
		// Allowed with a Portal session.
		{"POST", "/models/model-alpha-9b-q4/load", 200, ""},
		{"POST", "/speech/stt", 200, ""},
		{"POST", "/speech/tts", 200, ""},
		{"GET", "/speech/voices", 200, ""},
		{"GET", "/events", 200, ""},
		// Level none: open to everyone, a cookie changes nothing.
		{"GET", "/portal/status", 200, ""},
		{"POST", "/mode/survival", 200, ""},
		// Everything else is LAI-SVC-110.
		{"GET", "/status", 401, doctor.SVC110},
		{"GET", "/services", 401, doctor.SVC110},
		{"POST", "/mode", 401, doctor.SVC110},
		{"GET", "/key", 401, doctor.SVC110},
		{"DELETE", "/models/model-alpha-9b-q4", 401, doctor.SVC110},
		{"POST", "/models/model-alpha-9b-q4/unload", 401, doctor.SVC110},
		{"POST", "/cloud/enroll", 401, doctor.SVC110},
	}
	for _, c := range cases {
		t.Run(c.method+" "+c.target, func(t *testing.T) {
			r := newScopeRig(t)
			rec := r.do(call{method: c.method, target: c.target, cookie: r.cookie, header: withOrigin})
			if rec.Code != c.wantStatus || errorCode(t, rec) != c.wantCode {
				t.Errorf("status %d code %q, want %d %q: %s", rec.Code, errorCode(t, rec), c.wantStatus, c.wantCode, rec.Body.String())
			}
		})
	}
}

func TestOriginMatrixForPortalSessions(t *testing.T) {
	cases := []struct {
		name   string
		header map[string]string
		want   int
		code   string
	}{
		{"loopback ip", map[string]string{"Origin": "http://127.0.0.1:7070"}, 200, ""},
		{"localhost", map[string]string{"Origin": "http://localhost:7070"}, 200, ""},
		{"desktop shell", map[string]string{"Origin": "localai://app"}, 200, ""},
		{"missing", nil, 403, doctor.SEC007},
		{"Sec-Fetch-Site same-origin alone is not enough", map[string]string{"Sec-Fetch-Site": "same-origin"}, 403, doctor.SEC007},
		{"foreign", map[string]string{"Origin": "https://evil.example"}, 403, doctor.SEC007},
		{"other port", map[string]string{"Origin": "http://127.0.0.1:7071"}, 403, doctor.SEC007},
		{"null", map[string]string{"Origin": "null"}, 403, doctor.SEC007},
	}
	for _, target := range []string{"/models/model-alpha-9b-q4/load", "/speech/tts", "/speech/stt"} {
		for _, c := range cases {
			t.Run(target+" "+c.name, func(t *testing.T) {
				r := newScopeRig(t)
				rec := r.post(target, c.header)
				if rec.Code != c.want || errorCode(t, rec) != c.code {
					t.Errorf("status %d code %q, want %d %q", rec.Code, errorCode(t, rec), c.want, c.code)
				}
				if c.want == 403 && len(r.hits) != 0 {
					t.Errorf("the handler ran: %v", r.hits)
				}
			})
		}
	}
}

func TestRateLimits(t *testing.T) {
	cases := []struct {
		name, target string
		limit        int
		window       time.Duration
		code         string
	}{
		{"model loads 6 per hour", "/models/model-alpha-9b-q4/load", 6, time.Hour, doctor.SVC114},
		{"STT 30 per minute", "/speech/stt", 30, time.Minute, doctor.SVC015},
		{"TTS 120 per minute", "/speech/tts", 120, time.Minute, doctor.SVC015},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newScopeRig(t)
			for i := 1; i <= c.limit; i++ {
				if rec := r.post(c.target, withOrigin); rec.Code != 200 {
					t.Fatalf("request %d (inside the limit) = %d: %s", i, rec.Code, rec.Body.String())
				}
			}
			rec := r.post(c.target, withOrigin) // N+1
			if rec.Code != http.StatusTooManyRequests || errorCode(t, rec) != c.code {
				t.Fatalf("request %d = %d %q, want 429 %s", c.limit+1, rec.Code, errorCode(t, rec), c.code)
			}
			ra, err := strconv.Atoi(rec.Header().Get("Retry-After"))
			if err != nil || ra < 1 || time.Duration(ra)*time.Second > c.window {
				t.Errorf("Retry-After = %q", rec.Header().Get("Retry-After"))
			}
			if r.hits["POST "+routePattern(c.target)] != c.limit {
				t.Errorf("handler ran %d times, want %d", r.hits["POST "+routePattern(c.target)], c.limit)
			}
			// A second session has its own bucket.
			other, _ := r.s.Issue()
			if rec := r.do(call{method: "POST", target: c.target, cookie: other, header: withOrigin}); rec.Code != 200 {
				t.Errorf("a new session is limited: %d", rec.Code)
			}
			// The window slides: after it, the first session may go on.
			r.clk.Advance(c.window + time.Second)
			if rec := r.post(c.target, withOrigin); rec.Code != 200 {
				t.Errorf("after the window = %d", rec.Code)
			}
		})
	}
}

func routePattern(target string) string {
	if target == "/models/model-alpha-9b-q4/load" {
		return "/models/{id}/load"
	}
	return target
}

// TestRateLimitIsPerBucketAndNotForOthers: one bucket full does not block another route.
func TestRateLimitIsPerBucket(t *testing.T) {
	r := newScopeRig(t)
	for i := 0; i < 7; i++ {
		r.post("/models/model-alpha-9b-q4/load", withOrigin)
	}
	if rec := r.post("/speech/tts", withOrigin); rec.Code != 200 {
		t.Errorf("TTS after a full load bucket = %d", rec.Code)
	}
	if rec := r.do(call{method: "GET", target: "/speech/voices", cookie: r.cookie}); rec.Code != 200 {
		t.Errorf("voices = %d", rec.Code)
	}
}

func TestLimitedRequestsAreCounted(t *testing.T) {
	r := newScopeRig(t)
	for i := 0; i < 7; i++ {
		r.post("/models/model-alpha-9b-q4/load", withOrigin)
	}
	if got := r.met.Counters()[`localai_portal_rate_limited_total{bucket="load"}`]; got != int64(1) {
		t.Errorf("rate_limited = %v", got)
	}
}

// TestLeavingSurvivalEndsPortalSessions: the mode engine calls DropAll; the next request is 401 LAI-SVC-110.
func TestLeavingSurvivalEndsPortalSessions(t *testing.T) {
	for _, how := range []string{"DropAll", "mode alone"} {
		t.Run(how, func(t *testing.T) {
			r := newScopeRig(t)
			if rec := r.do(call{method: "GET", target: "/speech/voices", cookie: r.cookie}); rec.Code != 200 {
				t.Fatalf("before = %d", rec.Code)
			}
			r.m.set("online")
			if how == "DropAll" {
				r.s.DropAll()
			}
			rec := r.do(call{method: "GET", target: "/speech/voices", cookie: r.cookie})
			if rec.Code != 401 || errorCode(t, rec) != doctor.SVC110 {
				t.Errorf("after = %d %q", rec.Code, errorCode(t, rec))
			}
			// The same cookie does not come back to life when survival returns after DropAll.
			r.m.set("survival")
			if how == "DropAll" {
				if rec := r.do(call{method: "GET", target: "/speech/voices", cookie: r.cookie}); rec.Code != 401 {
					t.Errorf("a dropped session resolved again: %d", rec.Code)
				}
			}
			// Level none routes still work, with the stale cookie.
			if rec := r.do(call{method: "GET", target: "/portal/status", cookie: r.cookie}); rec.Code != 200 {
				t.Errorf("a none route with a stale cookie = %d", rec.Code)
			}
		})
	}
}

func TestExpiredSessionIsServiceEnded(t *testing.T) {
	r := newScopeRig(t)
	r.clk.Advance(portal.SessionTTL + time.Minute)
	rec := r.do(call{method: "GET", target: "/speech/voices", cookie: r.cookie})
	if rec.Code != 401 || errorCode(t, rec) != doctor.SVC110 {
		t.Errorf("expired = %d %q", rec.Code, errorCode(t, rec))
	}
}

func TestBucketOf(t *testing.T) {
	cases := []struct{ method, pattern, want string }{
		{"POST", "/models/{id}/load", portal.BucketLoad},
		{"POST", "/speech/stt", portal.BucketSTT},
		{"POST", "/speech/tts", portal.BucketTTS},
		{"GET", "/speech/voices", ""},
		{"GET", "/models/{id}/load", ""},
		{"POST", "/models/{id}/unload", ""},
	}
	for _, c := range cases {
		if got := portal.BucketOf(c.method, c.pattern); got != c.want {
			t.Errorf("BucketOf(%s %s) = %q, want %q", c.method, c.pattern, got, c.want)
		}
	}
}
