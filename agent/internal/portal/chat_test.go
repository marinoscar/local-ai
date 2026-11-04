package portal_test

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/api"
	"github.com/marinoscar/local-ai/agent/internal/audit"
	"github.com/marinoscar/local-ai/agent/internal/portal"
)

const chatKey = "unit-test-credential"

type seen struct {
	method, path, auth, cookie, fwd, host string
}

type llamaFake struct {
	srv  *httptest.Server
	mu   sync.Mutex
	reqs []seen
}

func newLlama(t *testing.T) *llamaFake {
	f := &llamaFake{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.reqs = append(f.reqs, seen{r.Method, r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("Cookie"), r.Header.Get("X-Forwarded-Prefix"), r.Host})
		f.mu.Unlock()
		http.SetCookie(w, &http.Cookie{Name: "up", Value: "1"})
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, "llama:"+r.URL.Path)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *llamaFake) count() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.reqs) }

func (f *llamaFake) port() int {
	_, p, _ := net.SplitHostPort(strings.TrimPrefix(f.srv.URL, "http://"))
	n, _ := strconv.Atoi(p)
	return n
}

type fakeOps struct {
	mu       sync.Mutex
	loads    []api.ModelLoadRequest
	unloads  []api.ModelUnloadRequest
	loadErr  error
	unloadFn func()
}

func (o *fakeOps) Load(_ context.Context, r api.ModelLoadRequest) (api.ModelLoadResult, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.loads = append(o.loads, r)
	return api.ModelLoadResult{ID: r.ID}, o.loadErr
}

func (o *fakeOps) Unload(_ context.Context, r api.ModelUnloadRequest) (api.ModelUnloadResult, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.unloads = append(o.unloads, r)
	return api.ModelUnloadResult{ID: r.ID}, nil
}

func (o *fakeOps) SetDefault(context.Context, string, audit.Actor) (api.ModelDefaultResult, error) {
	return api.ModelDefaultResult{}, nil
}

type chatCase struct {
	name      string
	mode      string
	principal api.PrincipalKind
	method    string
	path      string
	body      string
	headers   map[string]string
	wantCode  int
	wantBody  string // substring
	upstream  bool   // llama must have been reached
}

func TestChatMount(t *testing.T) {
	cases := []chatCase{
		{name: "anonymous outside survival is locked", mode: "normal", method: "GET", path: "/chat/", wantCode: 403, wantBody: "LAI-SVC-101"},
		{name: "locked page for a browser is html", mode: "normal", method: "GET", path: "/chat/", headers: map[string]string{"Accept": "text/html"}, wantCode: 403, wantBody: "LAI-SVC-101"},
		{name: "anonymous in survival needs nothing", mode: "survival", method: "GET", path: "/chat/", wantCode: 200, wantBody: "llama:/", upstream: true},
		{name: "operator outside survival", mode: "normal", principal: api.KindOperator, method: "GET", path: "/chat/props", wantCode: 200, wantBody: "llama:/props", upstream: true},
		{name: "portal session outside survival", mode: "normal", principal: api.KindPortal, method: "GET", path: "/chat/v1/models", wantCode: 200, wantBody: "llama:/v1/models", upstream: true},
		{name: "head is forwarded", mode: "survival", method: "HEAD", path: "/chat/props", wantCode: 200, upstream: true},
		{name: "completions with same origin", mode: "survival", method: "POST", path: "/chat/v1/chat/completions", body: `{}`, headers: map[string]string{"Origin": "http://node.test"}, wantCode: 200, wantBody: "llama:/v1/chat/completions", upstream: true},
		{name: "desktop origin is allowed", mode: "survival", method: "POST", path: "/chat/tokenize", body: `{}`, headers: map[string]string{"Origin": "localai://app"}, wantCode: 200, upstream: true},
		{name: "post without origin", mode: "survival", method: "POST", path: "/chat/v1/chat/completions", body: `{}`, wantCode: 403, wantBody: "LAI-SEC"},
		{name: "post from another origin", mode: "survival", method: "POST", path: "/chat/v1/chat/completions", body: `{}`, headers: map[string]string{"Origin": "http://evil.test"}, wantCode: 403, wantBody: "LAI-SEC"},
		{name: "model download refused", mode: "survival", method: "POST", path: "/chat/models/download", body: `{}`, headers: map[string]string{"Origin": "http://node.test"}, wantCode: 403, wantBody: "LAI-SVC-115"},
		{name: "model delete refused", mode: "survival", method: "DELETE", path: "/chat/models/alpha", headers: map[string]string{"Origin": "http://node.test"}, wantCode: 403, wantBody: "LAI-SVC-115"},
		{name: "slots write refused", mode: "survival", method: "POST", path: "/chat/slots/0", body: `{}`, headers: map[string]string{"Origin": "http://node.test"}, wantCode: 403, wantBody: "LAI-SVC-115"},
		{name: "slots read allowed", mode: "survival", method: "GET", path: "/chat/slots", wantCode: 200, upstream: true},
		{name: "cors proxy refused", mode: "survival", method: "GET", path: "/chat/cors-proxy?url=http://x", wantCode: 403, wantBody: "LAI-SVC-115"},
		{name: "tools refused", mode: "survival", method: "GET", path: "/chat/tools", wantCode: 403, wantBody: "LAI-SVC-115"},
		{name: "unknown post refused", mode: "survival", method: "POST", path: "/chat/props", body: `{}`, headers: map[string]string{"Origin": "http://node.test"}, wantCode: 403, wantBody: "LAI-SVC-115"},
		{name: "encoded slash is not under the mount", mode: "survival", method: "GET", path: "/chat%2Fprops", wantCode: 404},
		{name: "dotdot never reaches llama", mode: "survival", method: "GET", path: "/chat/../props", wantCode: 404},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			up := newLlama(t)
			h := chatHandler(t, up, tc.mode, &fakeOps{}, tc.method)
			rec := serveChat(h, tc)
			if rec.Code != tc.wantCode {
				t.Fatalf("status %d, want %d; body %q", rec.Code, tc.wantCode, rec.Body.String())
			}
			if tc.wantBody != "" && !strings.Contains(rec.Body.String(), tc.wantBody) {
				t.Errorf("body %q lacks %q", rec.Body.String(), tc.wantBody)
			}
			if got := up.count() > 0; got != tc.upstream {
				t.Errorf("llama reached = %v, want %v", got, tc.upstream)
			}
			assertNoKey(t, rec)
		})
	}
}

func assertNoKey(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	if strings.Contains(rec.Body.String(), chatKey) {
		t.Error("the key reached the browser (body)")
	}
	for k, v := range rec.Header() {
		if strings.Contains(strings.Join(v, ","), chatKey) {
			t.Errorf("the key reached the browser (header %s)", k)
		}
	}
}

func chatHandler(t *testing.T, up *llamaFake, mode string, ops api.ModelOps, method string) http.Handler {
	t.Helper()
	routes := portal.ChatRoutes(portal.ChatOptions{
		Port: up.port(), Key: func() (string, error) { return chatKey, nil },
		Mode: func() string { return mode }, Ops: ops,
	})
	for _, rt := range routes {
		if rt.Pattern == "/chat/" && rt.Method == method {
			return rt.Handler
		}
	}
	// HEAD is served by GET.
	for _, rt := range routes {
		if rt.Pattern == "/chat/" && rt.Method == http.MethodGet {
			return rt.Handler
		}
	}
	t.Fatalf("no route for %s", method)
	return nil
}

func serveChat(h http.Handler, tc chatCase) *httptest.ResponseRecorder {
	u, _ := url.Parse("http://node.test" + tc.path)
	req := httptest.NewRequest(tc.method, u.String(), strings.NewReader(tc.body))
	req.RequestURI = ""
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer lat_client_token")
	req.Header.Set("X-Api-Key", "client-key")
	req.Header.Set("Cookie", "localai_session=abc")
	for k, v := range tc.headers {
		req.Header.Set(k, v)
	}
	info := &api.RequestInfo{Transport: api.TransportTCP, Lang: "en", MaxBody: portal.ChatBodyLimit}
	info.Principal = api.Principal{Kind: tc.principal}
	req = req.WithContext(api.WithRequestInfo(req.Context(), info))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestChatInjectsKeyAndStripsCredentials(t *testing.T) {
	up := newLlama(t)
	h := chatHandler(t, up, "survival", &fakeOps{}, "GET")
	rec := serveChat(h, chatCase{method: "GET", path: "/chat/props"})
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	got := up.reqs[0]
	if got.auth != "Bearer "+chatKey {
		t.Errorf("upstream Authorization = %q, want the injected key", got.auth)
	}
	if got.cookie != "" {
		t.Errorf("the client's cookie reached llama: %q", got.cookie)
	}
	if got.fwd != "/chat" {
		t.Errorf("X-Forwarded-Prefix = %q", got.fwd)
	}
	if got.path != "/props" {
		t.Errorf("upstream path = %q, want the /chat prefix stripped", got.path)
	}
	if rec.Header().Get("Set-Cookie") != "" {
		t.Error("upstream Set-Cookie was not dropped")
	}
	if rec.Header().Get("X-Frame-Options") != "SAMEORIGIN" || rec.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Errorf("frame headers missing: %v", rec.Header())
	}
}

func TestChatLoadUnloadAreIntercepted(t *testing.T) {
	cases := []struct {
		name, path, body string
		portal           bool
		opsErr           error
		wantCode         int
		wantWait         bool
	}{
		{name: "load", path: "/chat/models/load", body: `{"model":"alpha"}`, wantCode: 200, wantWait: true},
		{name: "portal load does not wait", path: "/chat/models/load", body: `{"model":"alpha"}`, portal: true, wantCode: 200, wantWait: false},
		{name: "unload", path: "/chat/models/unload", body: `{"model":"alpha"}`, wantCode: 200},
		{name: "no model", path: "/chat/models/load", body: `{}`, wantCode: 400},
		{name: "not json", path: "/chat/models/unload", body: `nope`, wantCode: 400},
		{name: "fit rules refuse", path: "/chat/models/load", body: `{"model":"big"}`, opsErr: api.NewError("LAI-HW-005", 422, nil), wantCode: 422, wantWait: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			up := newLlama(t)
			ops := &fakeOps{loadErr: tc.opsErr}
			h := chatHandler(t, up, "survival", ops, "POST")
			c := chatCase{mode: "survival", method: "POST", path: tc.path, body: tc.body, headers: map[string]string{"Origin": "http://node.test"}}
			if tc.portal {
				c.principal = api.KindPortal
			}
			rec := serveChat(h, c)
			if rec.Code != tc.wantCode {
				t.Fatalf("status %d, want %d: %s", rec.Code, tc.wantCode, rec.Body.String())
			}
			if up.count() != 0 {
				t.Error("an intercepted call reached llama")
			}
			if tc.wantCode == 200 {
				if !strings.Contains(rec.Body.String(), `"success":true`) {
					t.Errorf("body %q is not router-compatible", rec.Body.String())
				}
			}
			if tc.path == "/chat/models/load" && tc.wantCode != 400 {
				if len(ops.loads) != 1 || ops.loads[0].ID == "" || ops.loads[0].Wait != tc.wantWait || !ops.loads[0].UnloadOthers {
					t.Errorf("load request = %+v", ops.loads)
				}
			}
			if strings.HasSuffix(tc.path, "unload") && tc.wantCode == 200 && len(ops.unloads) != 1 {
				t.Errorf("unloads = %+v", ops.unloads)
			}
		})
	}
}

func TestChatUpstreamDownIs502WithCode(t *testing.T) {
	up := newLlama(t)
	port := up.port()
	up.srv.Close()
	routes := portal.ChatRoutes(portal.ChatOptions{Port: port, Key: func() (string, error) { return chatKey, nil }, Mode: func() string { return "survival" }})
	rec := serveChat(routes[0].Handler, chatCase{method: "GET", path: "/chat/", headers: map[string]string{"Accept": "text/html"}})
	if rec.Code != 502 || !strings.Contains(rec.Body.String(), "LAI-SVC-102") {
		t.Fatalf("got %d %q", rec.Code, rec.Body.String())
	}
	assertNoKey(t, rec)
}

func TestChatKeyUnreadableStillNeverLeaks(t *testing.T) {
	up := newLlama(t)
	routes := portal.ChatRoutes(portal.ChatOptions{Port: up.port(), Key: func() (string, error) { return "", errors.New("gone") }, Mode: func() string { return "survival" }})
	rec := serveChat(routes[0].Handler, chatCase{method: "GET", path: "/chat/props"})
	if rec.Code != 200 || up.reqs[0].auth != "" {
		t.Fatalf("status %d, upstream auth %q: a client credential must not be forwarded", rec.Code, up.reqs[0].auth)
	}
}

func TestChatRedirectAndRouteShape(t *testing.T) {
	routes := portal.ChatRoutes(portal.ChatOptions{Port: 8080})
	methods := map[string]bool{}
	for _, rt := range routes {
		if !rt.Absolute || len(rt.Auth) != 1 || rt.Auth[0] != api.AuthNone || rt.Timeout != 0 {
			t.Errorf("%s %s: route shape %+v", rt.Method, rt.Pattern, rt)
		}
		if rt.Pattern == "/chat/" {
			methods[rt.Method] = true
			if rt.MaxBody != portal.ChatBodyLimit {
				t.Errorf("%s MaxBody = %d", rt.Method, rt.MaxBody)
			}
		}
		if rt.Pattern == "/chat" {
			req := httptest.NewRequest("GET", "/chat?x=1", nil)
			rec := httptest.NewRecorder()
			rt.Handler.ServeHTTP(rec, req)
			if rec.Code != 301 || rec.Header().Get("Location") != "/chat/?x=1" {
				t.Errorf("redirect: %d %q", rec.Code, rec.Header().Get("Location"))
			}
		}
	}
	for _, m := range []string{"GET", "POST", "PUT", "PATCH", "DELETE"} {
		if !methods[m] {
			t.Errorf("no %s route", m)
		}
	}
}
