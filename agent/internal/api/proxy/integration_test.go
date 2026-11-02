package proxy

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/api"
	"github.com/marinoscar/local-ai/agent/internal/logx"
)

// syncBuf is a goroutine-safe log sink.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// harness is the real api.Server with mounts registered, served on a loopback port that the Host
// check was told about.
type harness struct {
	srv   *api.Server
	front *httptest.Server
	logs  *syncBuf
}

func (h *harness) url(path string) string { return h.front.URL + path }

// newHarness builds the server around routes. Anonymous requests are level none; the header
// X-Test-Session: 1 makes a request an operator session.
func newHarness(t *testing.T, routes ...api.Route) *harness {
	t.Helper()
	reg := api.NewRegistry()
	if err := reg.Add(routes...); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	logs := &syncBuf{}
	lv := new(slog.LevelVar)
	logger := logx.New(logs, lv)
	lv.Set(slog.LevelDebug) // after New, which sets its own default
	auth := api.AuthFunc(func(r *http.Request, rt *api.Route) (api.Principal, error) {
		if r.Header.Get("X-Test-Session") == "1" {
			return api.Principal{Kind: api.KindOperator, Credential: api.CredSession, Via: "session"}, nil
		}
		return api.Anonymous(), nil
	})
	s, err := api.New(api.Options{Registry: reg, Auth: auth, Port: ln.Addr().(*net.TCPAddr).Port, Logger: logger})
	if err != nil {
		t.Fatal(err)
	}
	s.SetMutationsOpen(true)
	front := httptest.NewUnstartedServer(s.Handler())
	front.Listener.Close()
	front.Listener = ln
	front.Start()
	t.Cleanup(front.Close)
	return &harness{srv: s, front: front, logs: logs}
}

// mountRoutes registers one proxy as the routes of a mount, as PORTAL-03 and SVC-04 will: one route
// per method, absolute pattern, no route deadline (the proxy has its own), the body limit raised to
// the mount's.
func mountRoutes(pattern string, h http.Handler, maxBody int64, methods ...string) []api.Route {
	var out []api.Route
	for _, m := range methods {
		out = append(out, api.Route{Method: m, Pattern: pattern, Absolute: true, Auth: []api.Level{api.AuthNone}, Handler: h, MaxBody: maxBody})
	}
	return out
}

// The chat scenario of agent.md section 9.2 (6): survival anonymous works, online anonymous is
// refused, the key the upstream sees is the injected one, the WebUI's model load is intercepted and
// the management paths are refused. The enabling rule and the path table here are a stand-in for
// PORTAL-03's; what this proves is that the helper carries them.
func TestChatMountScenario(t *testing.T) {
	const key = "lai-chat-INJECTED-0123456789abcdef"
	up := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/chat/completions":
			w.Header().Set("Content-Type", "text/event-stream")
			fl := w.(http.Flusher)
			for _, tok := range []string{"Hel", "lo"} {
				_, _ = io.WriteString(w, "data: "+tok+"\n\n")
				fl.Flush()
			}
		default:
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"ok":true}`)
		}
	})
	target := *up.url()

	var mode atomic.Value
	mode.Store("survival")
	var loaded []string
	var mu sync.Mutex
	loadHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		loaded = append(loaded, body.Model)
		mu.Unlock()
		api.WriteJSON(w, r, 200, map[string]any{"success": true})
	})
	chat := New(Options{
		Target:             &target,
		StripPrefix:        "/chat",
		SetForwardedPrefix: true,
		InjectHeaders:      func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+key) },
		BodyLimit:          32 << 20,
		ResponseTimeout:    -1,
		IdleTimeout:        10 * time.Minute,
		DropSetCookie:      true,
		FrameHeaders:       true,
		DownStatus:         http.StatusBadGateway,
		DownCode:           api.CodeChatDown,
		MethodPolicy: func(r *http.Request) error {
			if r.Method == "GET" || r.Method == "HEAD" {
				return nil
			}
			if o := r.Header.Get("Origin"); o != "http://127.0.0.1:7070" && !strings.HasPrefix(o, "http://127.0.0.1:") {
				return api.NewError(api.CodeBadOrigin, http.StatusForbidden, nil)
			}
			return nil
		},
		PathPolicy: func(r *http.Request) (Decision, http.Handler, string) {
			p := r.URL.Path
			switch {
			case r.Method == "POST" && (p == "/models/load" || p == "/models/unload"):
				return Intercept, loadHandler, ""
			case strings.HasPrefix(p, "/tools"), strings.HasPrefix(p, "/cors-proxy"), strings.HasPrefix(p, "/models/download"):
				return Deny, nil, "LAI-SVC-115"
			case r.Method == "GET" || r.Method == "HEAD" || p == "/v1/chat/completions":
				return Allow, nil, ""
			}
			return Deny, nil, "LAI-SVC-115"
		},
		Logger: slog.New(slog.NewJSONHandler(io.Discard, nil)),
	})
	// The stand-in enabling rule: survival, or a session.
	gated := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if mode.Load() != "survival" && api.InfoFrom(r.Context()).Principal.Credential != api.CredSession {
			api.WriteErrorPage(w, r, api.NewError(api.CodeUnauthenticated, http.StatusForbidden, nil))
			return
		}
		chat.ServeHTTP(w, r)
	})
	h := newHarness(t, mountRoutes("/chat/", gated, 32<<20, "GET", "POST", "PUT", "PATCH", "DELETE")...)
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	do := func(method, path string, hdr map[string]string, body string) (*http.Response, string) {
		t.Helper()
		var rd io.Reader
		if body != "" {
			rd = strings.NewReader(body)
		}
		req, _ := http.NewRequest(method, h.url(path), rd)
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp, string(b)
	}
	origin := h.front.URL
	jsonOrigin := map[string]string{"Content-Type": "application/json", "Origin": origin}

	t.Run("survival: anonymous works and the upstream sees the injected key", func(t *testing.T) {
		resp, body := do("GET", "/chat/props", map[string]string{"Authorization": "Bearer CLIENT-KEY", "Cookie": "localai_session=S"}, "")
		if resp.StatusCode != 200 || body != `{"ok":true}` {
			t.Fatalf("%d %s", resp.StatusCode, body)
		}
		got := up.last(t)
		if got.Path != "/props" || got.Header.Get("Authorization") != "Bearer "+key || got.Header.Get("Cookie") != "" || got.Header.Get("X-Forwarded-Prefix") != "/chat" {
			t.Fatalf("upstream saw %+v", got)
		}
		if resp.Header.Get("X-Frame-Options") != "SAMEORIGIN" || resp.Header.Get("Referrer-Policy") != "no-referrer" {
			t.Fatalf("headers: %v", resp.Header)
		}
		if strings.Contains(body, key) || strings.Contains(strings.Join(resp.Header.Values("Set-Cookie"), ";"), key) {
			t.Fatal("the key came back to the browser")
		}
	})
	t.Run("a completion streams", func(t *testing.T) {
		resp, body := do("POST", "/chat/v1/chat/completions", jsonOrigin, `{"messages":[]}`)
		if resp.StatusCode != 200 || body != "data: Hel\n\ndata: lo\n\n" || resp.Header.Get("Content-Type") != "text/event-stream" {
			t.Fatalf("%d %q %v", resp.StatusCode, body, resp.Header)
		}
	})
	t.Run("a 3 MiB photo goes through, 1 MiB would have been refused by the default", func(t *testing.T) {
		big := `{"image":"` + strings.Repeat("A", 3<<20) + `"}`
		resp, _ := do("POST", "/chat/v1/chat/completions", jsonOrigin, big)
		if resp.StatusCode != 200 {
			t.Fatalf("status %d", resp.StatusCode)
		}
		if got := up.last(t); len(got.Body) != len(big) {
			t.Fatalf("upstream got %d bytes, want %d", len(got.Body), len(big))
		}
	})
	t.Run("model load is intercepted, not forwarded", func(t *testing.T) {
		before := up.count()
		resp, body := do("POST", "/chat/models/load", jsonOrigin, `{"model":"qwen-small"}`)
		if resp.StatusCode != 200 || !strings.Contains(body, `"success":true`) {
			t.Fatalf("%d %s", resp.StatusCode, body)
		}
		mu.Lock()
		defer mu.Unlock()
		if len(loaded) != 1 || loaded[0] != "qwen-small" || up.count() != before {
			t.Fatalf("loaded=%v upstream calls=%d", loaded, up.count()-before)
		}
	})
	t.Run("management paths are refused with the portal code", func(t *testing.T) {
		for _, p := range []string{"/chat/tools/run", "/chat/cors-proxy?url=http://x", "/chat/models/download"} {
			before := up.count()
			resp, body := do("GET", p, nil, "")
			if resp.StatusCode != 403 || !strings.Contains(body, "LAI-SVC-115") || up.count() != before {
				t.Fatalf("%s: %d %s", p, resp.StatusCode, body)
			}
		}
	})
	t.Run("a mutating request needs a same-origin Origin", func(t *testing.T) {
		resp, body := do("POST", "/chat/v1/chat/completions", map[string]string{"Content-Type": "application/json"}, `{}`)
		if resp.StatusCode != 403 || !strings.Contains(body, string(api.CodeBadOrigin)) {
			t.Fatalf("%d %s", resp.StatusCode, body)
		}
	})
	t.Run("/chat redirects to /chat/", func(t *testing.T) {
		resp, _ := do("GET", "/chat", nil, "")
		if resp.StatusCode != http.StatusMovedPermanently || resp.Header.Get("Location") != "/chat/" {
			t.Fatalf("%d %v", resp.StatusCode, resp.Header)
		}
	})
	t.Run("online: anonymous is refused, a session works", func(t *testing.T) {
		mode.Store("online")
		defer mode.Store("survival")
		resp, body := do("GET", "/chat/props", map[string]string{"Accept": "text/html"}, "")
		if resp.StatusCode != 403 || !strings.Contains(resp.Header.Get("Content-Type"), "text/html") || !strings.Contains(body, "LAI-SEC-001") {
			t.Fatalf("%d %s", resp.StatusCode, body)
		}
		if resp, _ := do("GET", "/chat/props", map[string]string{"X-Test-Session": "1"}, ""); resp.StatusCode != 200 {
			t.Fatalf("session: %d", resp.StatusCode)
		}
	})
	t.Run("no key in the logs", func(t *testing.T) {
		time.Sleep(100 * time.Millisecond) // access lines are written after the handlers return
		if strings.Contains(h.logs.String(), key) || strings.Contains(h.logs.String(), "CLIENT-KEY") {
			t.Fatalf("key in the access log:\n%s", h.logs.String())
		}
	})
}

// The library mount: path preserved, GET and HEAD only, 503 and its own code when kiwix is down.
func TestLibraryMountScenario(t *testing.T) {
	up := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, "<html>library "+r.URL.Path+"</html>")
	})
	target := *up.url()
	lib := New(Options{Target: &target, MethodPolicy: ReadOnly, DownStatus: http.StatusServiceUnavailable, DownCode: api.CodeLibraryDown, Logger: quiet})
	h := newHarness(t, mountRoutes("/library/", lib, 0, "GET")...)

	resp, err := http.Get(h.url("/library/viewer?content=wiki"))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != 200 || string(b) != "<html>library /library/viewer</html>" {
		t.Fatalf("%d %s", resp.StatusCode, b)
	}
	if got := up.last(t); got.Path != "/library/viewer" || got.RawQuery != "content=wiki" || got.Header.Get("X-Forwarded-Prefix") != "" {
		t.Fatalf("%+v", got)
	}
	// POST is not even routed (the route is GET only): 405 from the server, never the upstream.
	before := up.count()
	req, _ := http.NewRequest("POST", h.url("/library/x"), strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != 405 || up.count() != before {
		t.Fatalf("POST: %d", resp.StatusCode)
	}

	// kiwix down: a browser gets the page, 503, the code.
	up.srv.Close()
	req, _ = http.NewRequest("GET", h.url("/library/"), nil)
	req.Header.Set("Accept", "text/html")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != 503 || !strings.Contains(string(b), "LAI-SVC-002") || resp.Header.Get("X-Request-Id") == "" {
		t.Fatalf("%d %s", resp.StatusCode, b)
	}
	// The access line is written after the handler returns, so the client can be ahead of it.
	deadline := time.Now().Add(2 * time.Second)
	for !strings.Contains(h.logs.String(), `"code":"LAI-SVC-002"`) {
		if time.Now().After(deadline) {
			t.Fatalf("the access log lacks the code:\n%s", h.logs.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Nothing a client sends and nothing the proxy injects may reach a log, whatever fails. The proxy
// logs through a plain JSON handler here (no scrubber), so only its own discipline protects the key.
func TestNoSecretReachesALog(t *testing.T) {
	const (
		injected = "lai-INJECTED-SECRET-0a1b2c3d4e5f"
		clientAK = "CLIENT-AUTH-SECRET-9z8y7x"
		cookie   = "COOKIE-SECRET-4k3j2h"
		query    = "QUERY-TOKEN-5m6n7o"
		bodyTxt  = "BODY-SECRET-1q2w3e"
	)
	secrets := []string{injected, clientAK, cookie, query, bodyTxt}

	logs := &syncBuf{}
	logger := slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))

	slowUp := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(3 * time.Second):
		case <-r.Context().Done():
		}
	})
	okUp := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "fine")
	})
	// An upstream that echoes the request headers back in the body, as a debugging endpoint would:
	// the client is entitled to nothing it did not send, but the key must not be one of the headers
	// the proxy printed, so this checks the log, not the echo.
	dead := deadURL(t)

	build := func(target *url.URL, mod func(*Options)) http.Handler {
		o := Options{Target: target, Logger: logger, InjectHeaders: func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+injected) },
			DownCode: api.CodeChatDown}
		if mod != nil {
			mod(&o)
		}
		return New(o)
	}
	okT, slowT := *okUp.url(), *slowUp.url()
	cases := []struct {
		name string
		h    http.Handler
		want int
	}{
		{"success", build(&okT, nil), 200},
		{"upstream down", build(dead, nil), 502},
		{"total timeout", build(&slowT, func(o *Options) { o.ResponseTimeout = 100 * time.Millisecond }), 504},
		{"idle timeout", build(&slowT, func(o *Options) { o.ResponseTimeout = -1; o.IdleTimeout = 100 * time.Millisecond }), 504},
		{"body too large", build(&okT, func(o *Options) { o.BodyLimit = 8 }), 413},
		{"denied path", build(&okT, func(o *Options) {
			o.PathPolicy = func(*http.Request) (Decision, http.Handler, string) { return Deny, nil, "LAI-SVC-115" }
		}), 403},
		{"refused method", build(&okT, func(o *Options) { o.MethodPolicy = ReadOnly }), 405},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "/chat/v1/x?token="+query, strings.NewReader(`{"p":"`+bodyTxt+`"}`))
			r.Header.Set("Authorization", "Bearer "+clientAK)
			r.Header.Set("Cookie", "localai_session="+cookie)
			r.Header.Set("Content-Type", "application/json")
			r = r.WithContext(api.WithRequestInfo(r.Context(), &api.RequestInfo{ID: "req_abcdefghijkl", Lang: "en"}))
			rec := httptest.NewRecorder()
			tc.h.ServeHTTP(rec, r)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d (%s)", rec.Code, tc.want, rec.Body.String())
			}
			// The answer to the client must not carry any of them either.
			var all strings.Builder
			all.WriteString(rec.Body.String())
			for k, v := range rec.Header() {
				all.WriteString(k + ": " + strings.Join(v, ",") + "\n")
			}
			for _, s := range secrets {
				if strings.Contains(all.String(), s) {
					t.Fatalf("%q leaked into the response:\n%s", s, all.String())
				}
			}
		})
	}
	if out := logs.String(); out == "" {
		t.Fatal("the failure cases wrote no log line: the scan below would prove nothing")
	} else {
		for _, s := range secrets {
			if strings.Contains(out, s) {
				t.Fatalf("%q reached the log:\n%s", s, out)
			}
		}
		for _, want := range []string{"proxy.upstream_failed", "req_abcdefghijkl", "LAI-SVC-102"} {
			if !strings.Contains(out, want) {
				t.Errorf("the log lacks %q (the scan needs the failure lines to exist):\n%s", want, out)
			}
		}
	}
}
