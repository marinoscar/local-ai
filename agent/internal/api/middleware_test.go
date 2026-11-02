package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/logx"
)

func TestHostMatrix(t *testing.T) {
	s, _ := newTestServer(t)
	h := s.Handler()
	tests := []struct {
		name   string
		host   string
		socket bool
		status int
	}{
		{"127.0.0.1 with port", "127.0.0.1:7070", false, 200},
		{"localhost with port", "localhost:7070", false, 200},
		{"127.0.0.1 without port", "127.0.0.1", false, 403},
		{"localhost without port", "localhost", false, 403},
		{"other host", "evil:7070", false, 403},
		{"rebinding name", "evil.example:7070", false, 403},
		{"wrong port", "127.0.0.1:7071", false, 403},
		{"upper case host is not exact", "LOCALHOST:7070", false, 403},
		{"trailing dot", "localhost.:7070", false, 403},
		{"port suffix trick", "127.0.0.1:7070.evil.example", false, 403},
		{"empty host", "", false, 200}, // httptest fills 127.0.0.1:7070 for an empty override
		{"socket ignores Host", "evil.example", true, 200},
		{"socket ignores a missing port", "127.0.0.1", true, 200},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			q := req{target: "/api/v1/open", host: tc.host}
			if tc.socket {
				q.socket = &PeerCred{UID: 1000, GID: 1000, PID: 1}
			}
			rec := do(h, q)
			if tc.status == 403 {
				expectError(t, rec, 403, CodeBadHost)
				if loc := rec.Header().Get("Location"); loc != "" {
					t.Fatalf("a Host refusal must not redirect, got Location %q", loc)
				}
			} else if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tc.status, rec.Body.String())
			}
		})
	}
}

func TestOriginMatrix(t *testing.T) {
	s, _ := newTestServer(t)
	h := s.Handler()
	tests := []struct {
		name   string
		header map[string]string
		socket bool
		status int
	}{
		{"loopback ip", map[string]string{"Origin": "http://127.0.0.1:7070"}, false, 200},
		{"localhost", map[string]string{"Origin": "http://localhost:7070"}, false, 200},
		{"desktop shell", map[string]string{"Origin": "localai://app"}, false, 200},
		{"null", map[string]string{"Origin": "null"}, false, 403},
		{"other port", map[string]string{"Origin": "http://127.0.0.1:7071"}, false, 403},
		{"another site", map[string]string{"Origin": "https://evil.example"}, false, 403},
		{"https on our port", map[string]string{"Origin": "https://127.0.0.1:7070"}, false, 403},
		{"empty origin", map[string]string{"Origin": ""}, false, 403},
		{"cross-site without Origin", map[string]string{"Sec-Fetch-Site": "cross-site"}, false, 403},
		{"same-site without Origin", map[string]string{"Sec-Fetch-Site": "same-site"}, false, 403},
		{"same-origin without Origin", map[string]string{"Sec-Fetch-Site": "same-origin"}, false, 200},
		{"none without Origin", map[string]string{"Sec-Fetch-Site": "none"}, false, 200},
		{"neither header (curl, aictl)", nil, false, 200},
		{"good Origin outranks a bad Sec-Fetch-Site", map[string]string{"Origin": "http://localhost:7070", "Sec-Fetch-Site": "cross-site"}, false, 200},
		{"bad Origin outranks a good Sec-Fetch-Site", map[string]string{"Origin": "https://evil.example", "Sec-Fetch-Site": "same-origin"}, false, 403},
		{"socket skips the Origin check", map[string]string{"Origin": "https://evil.example"}, true, 200},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			q := req{target: "/api/v1/open", header: tc.header}
			if tc.socket {
				q.socket = &PeerCred{UID: 1000}
			}
			rec := do(h, q)
			if tc.status == 403 {
				expectError(t, rec, 403, CodeBadOrigin)
			} else if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tc.status, rec.Body.String())
			}
		})
	}
}

func TestOriginHeaderRepeated(t *testing.T) {
	s, _ := newTestServer(t)
	r := req{target: "/api/v1/open"}.build()
	r.Header.Add("Origin", "http://127.0.0.1:7070")
	r.Header.Add("Origin", "http://127.0.0.1:7070")
	rec := newRecorder(s.Handler(), r)
	expectError(t, rec, 403, CodeBadOrigin)
}

func TestHostCheckRunsBeforeOriginAndRouting(t *testing.T) {
	s, _ := newTestServer(t)
	// A bad Host on an unknown route and with a bad Origin is still the Host refusal.
	rec := do(s.Handler(), req{target: "/nope", host: "evil:7070", header: map[string]string{"Origin": "https://evil.example"}})
	expectError(t, rec, 403, CodeBadHost)
}

func TestCSRFRule(t *testing.T) {
	s, _ := newTestServer(t)
	h := s.Handler()
	post := func(principal string, header map[string]string) int {
		hd := jsonHeaders(header)
		hd["X-Test-Principal"] = principal
		rec := do(h, req{method: "POST", target: "/api/v1/echo", header: hd, body: `{}`})
		return rec.Code
	}
	tests := []struct {
		name      string
		principal string
		header    map[string]string
		status    int
	}{
		{"session cookie without Origin", "session", nil, 403},
		{"session with Origin", "session", map[string]string{"Origin": "http://127.0.0.1:7070"}, 200},
		{"session with desktop Origin", "session", map[string]string{"Origin": "localai://app"}, 200},
		{"session with Sec-Fetch-Site same-origin", "session", map[string]string{"Sec-Fetch-Site": "same-origin"}, 200},
		{"session with Sec-Fetch-Site none is not proof", "session", map[string]string{"Sec-Fetch-Site": "none"}, 403},
		{"portal cookie without Origin", "portal", nil, 403},
		{"portal with Origin", "portal", map[string]string{"Origin": "http://localhost:7070"}, 200},
		{"portal with Sec-Fetch-Site alone is refused", "portal", map[string]string{"Sec-Fetch-Site": "same-origin"}, 403},
		{"bearer needs no Origin", "bearer", nil, 200},
		{"socket peer needs no Origin", "socket", nil, 200},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := post(tc.principal, tc.header); got != tc.status {
				t.Fatalf("status = %d, want %d", got, tc.status)
			}
		})
	}

	t.Run("refusal carries the Origin code", func(t *testing.T) {
		hd := jsonHeaders(map[string]string{"X-Test-Principal": "session"})
		rec := do(h, req{method: "POST", target: "/api/v1/echo", header: hd, body: `{}`})
		e := expectError(t, rec, 403, CodeBadOrigin)
		if e.Error.Details["reason"] != "origin_required" {
			t.Fatalf("details = %v", e.Error.Details)
		}
	})
	t.Run("reads are not CSRF-checked", func(t *testing.T) {
		rec := do(h, req{target: "/api/v1/ping", header: map[string]string{"X-Test-Principal": "session"}})
		if rec.Code != 200 {
			t.Fatalf("status = %d", rec.Code)
		}
	})
	t.Run("on the socket a cookie session still needs a proof", func(t *testing.T) {
		hd := jsonHeaders(map[string]string{"X-Test-Principal": "session"})
		rec := do(h, req{method: "POST", target: "/api/v1/echo", header: hd, body: `{}`, socket: &PeerCred{UID: 1000}})
		expectError(t, rec, 403, CodeBadOrigin)
	})
}

func TestContentType(t *testing.T) {
	s, _ := newTestServer(t)
	h := s.Handler()
	tests := []struct {
		name string
		ct   string // "-" means no header
		want int
	}{
		{"json", "application/json", 200},
		{"json utf-8", "application/json; charset=utf-8", 200},
		{"json UTF-8 upper case", "application/json; charset=UTF-8", 200},
		{"json no space", "application/json;charset=utf-8", 200},
		{"text/plain", "text/plain", 415},
		{"form", "application/x-www-form-urlencoded", 415},
		{"missing", "-", 415},
		{"other charset", "application/json; charset=latin1", 415},
		{"extra parameter", "application/json; foo=bar", 415},
		{"two parameters", "application/json; charset=utf-8; foo=bar", 415},
		{"look-alike", "application/jsonx", 415},
		{"vendor json", "application/vnd.api+json", 415},
		{"garbage", "application/json; =", 415},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			hd := map[string]string{"X-Test-Principal": "bearer"}
			if tc.ct != "-" {
				hd["Content-Type"] = tc.ct
			}
			rec := do(h, req{method: "POST", target: "/api/v1/echo", header: hd, body: `{"name":"a"}`})
			if tc.want == 415 {
				e := expectError(t, rec, 415, CodeInvalidRequest)
				if e.Error.Details["reason"] != "unsupported_media_type" {
					t.Fatalf("details = %v", e.Error.Details)
				}
			} else if rec.Code != tc.want {
				t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
			}
		})
	}

	t.Run("an empty body still needs the content type", func(t *testing.T) {
		rec := do(h, req{method: "DELETE", target: "/api/v1/thing", header: map[string]string{"X-Test-Principal": "admin"}})
		expectError(t, rec, 415, CodeInvalidRequest)
		rec = do(h, req{method: "DELETE", target: "/api/v1/thing", header: map[string]string{"X-Test-Principal": "admin", "Content-Type": "application/json"}})
		if rec.Code != 200 {
			t.Fatalf("status = %d", rec.Code)
		}
	})
	t.Run("a route can name its own media types", func(t *testing.T) {
		hd := map[string]string{"X-Test-Principal": "bearer", "Content-Type": "multipart/form-data; boundary=xyz"}
		if rec := do(h, req{method: "POST", target: "/api/v1/upload", header: hd, body: "x"}); rec.Code != 200 {
			t.Fatalf("multipart status = %d: %s", rec.Code, rec.Body.String())
		}
		hd["Content-Type"] = "application/json"
		expectError(t, do(h, req{method: "POST", target: "/api/v1/upload", header: hd, body: "x"}), 415, CodeInvalidRequest)
	})
	t.Run("reads need no content type", func(t *testing.T) {
		if rec := do(h, req{target: "/api/v1/ping", header: map[string]string{"X-Test-Principal": "bearer"}}); rec.Code != 200 {
			t.Fatalf("status = %d", rec.Code)
		}
	})
}

func TestBodyLimit(t *testing.T) {
	s, _ := newTestServer(t)
	h := s.Handler()
	big := `{"name":"` + strings.Repeat("a", int(DefaultMaxBody)) + `"}`

	t.Run("over the limit by Content-Length", func(t *testing.T) {
		rec := do(h, req{method: "POST", target: "/api/v1/echo", header: jsonHeaders(nil), body: big})
		e := expectError(t, rec, 413, CodeTooLarge)
		if e.Error.Details["limit"] != float64(DefaultMaxBody) {
			t.Fatalf("details = %v", e.Error.Details)
		}
	})
	t.Run("over the limit with unknown length", func(t *testing.T) {
		r := req{method: "POST", target: "/api/v1/echo", header: jsonHeaders(nil), body: big}.build()
		r.ContentLength = -1 // chunked: only the reader can enforce the limit
		rec := newRecorder(h, r)
		expectError(t, rec, 413, CodeTooLarge)
	})
	t.Run("exactly at the limit is accepted by the middleware", func(t *testing.T) {
		pad := int(DefaultMaxBody) - len(`{"name":""}`)
		rec := do(h, req{method: "POST", target: "/api/v1/echo", header: jsonHeaders(nil), body: `{"name":"` + strings.Repeat("a", pad) + `"}`})
		if rec.Code != 200 {
			t.Fatalf("status = %d: %.200s", rec.Code, rec.Body.String())
		}
	})
	t.Run("a route's own limit applies", func(t *testing.T) {
		hd := map[string]string{"X-Test-Principal": "bearer", "Content-Type": "multipart/form-data; boundary=x"}
		rec := do(h, req{method: "POST", target: "/api/v1/upload", header: hd, body: strings.Repeat("x", 17)})
		e := expectError(t, rec, 413, CodeTooLarge)
		if e.Error.Details["limit"] != float64(16) {
			t.Fatalf("details = %v", e.Error.Details)
		}
		if rec := do(h, req{method: "POST", target: "/api/v1/upload", header: hd, body: strings.Repeat("x", 16)}); rec.Code != 200 {
			t.Fatalf("status = %d", rec.Code)
		}
	})
	t.Run("the speech limit is 25 MiB", func(t *testing.T) {
		if SpeechMaxBody != 25*1024*1024 || DefaultMaxBody != 1024*1024 {
			t.Fatal("limits changed")
		}
	})
}

func TestOptionsIsAlways405WithoutCORS(t *testing.T) {
	s, _ := newTestServer(t)
	h := s.Handler()
	for _, target := range []string{"/api/v1/ping", "/api/v1/echo", "/api/v1/health", "/api/v1/unknown", "/", "/portal"} {
		t.Run(target, func(t *testing.T) {
			rec := do(h, req{method: "OPTIONS", target: target, header: map[string]string{
				"Origin": "http://127.0.0.1:7070", "Access-Control-Request-Method": "POST", "Access-Control-Request-Headers": "content-type",
			}})
			expectError(t, rec, 405, CodeInvalidRequest)
			for k := range rec.Header() {
				if strings.HasPrefix(strings.ToLower(k), "access-control-") {
					t.Fatalf("CORS header %s on an OPTIONS answer", k)
				}
			}
		})
	}
	t.Run("a bad origin is refused before the method", func(t *testing.T) {
		rec := do(h, req{method: "OPTIONS", target: "/api/v1/echo", header: map[string]string{"Origin": "https://evil.example"}})
		expectError(t, rec, 403, CodeBadOrigin)
	})
	t.Run("no response ever carries CORS headers", func(t *testing.T) {
		rec := do(h, req{target: "/api/v1/open", header: map[string]string{"Origin": "http://127.0.0.1:7070"}})
		for k := range rec.Header() {
			if strings.HasPrefix(strings.ToLower(k), "access-control-") {
				t.Fatalf("CORS header %s", k)
			}
		}
	})
}

func TestRouting(t *testing.T) {
	s, _ := newTestServer(t)
	h := s.Handler()

	t.Run("unknown route is a 404 envelope", func(t *testing.T) {
		e := expectError(t, do(h, req{target: "/api/v1/nope"}), 404, CodeNotFound)
		if e.Error.Details["reason"] != "unknown_route" {
			t.Fatalf("details = %v", e.Error.Details)
		}
		expectError(t, do(h, req{target: "/"}), 404, CodeNotFound)
		expectError(t, do(h, req{target: "/api/v2/health"}), 404, CodeNotFound)
	})
	t.Run("wrong method is a 405 envelope with Allow", func(t *testing.T) {
		rec := do(h, req{method: "POST", target: "/api/v1/ping", header: jsonHeaders(nil)})
		e := expectError(t, rec, 405, CodeInvalidRequest)
		if got := rec.Header().Get("Allow"); got != "GET, HEAD" {
			t.Fatalf("Allow = %q", got)
		}
		if e.Error.Details["reason"] != "method_not_allowed" {
			t.Fatalf("details = %v", e.Error.Details)
		}
		rec = do(h, req{method: "DELETE", target: "/api/v1/models/default", header: jsonHeaders(nil)})
		expectError(t, rec, 405, CodeInvalidRequest)
		if got := rec.Header().Get("Allow"); got != "GET, HEAD, PUT" {
			t.Fatalf("Allow for /models/default = %q", got)
		}
	})
	t.Run("path values reach the handler", func(t *testing.T) {
		rec := do(h, req{target: "/api/v1/models/qwen-x", header: map[string]string{"X-Test-Principal": "bearer"}})
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"id":"qwen-x"`) {
			t.Fatalf("%d %s", rec.Code, rec.Body.String())
		}
	})
	t.Run("the more specific pattern wins", func(t *testing.T) {
		rec := do(h, req{method: "PUT", target: "/api/v1/models/default", header: jsonHeaders(nil), body: `{}`})
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"id":""`) {
			t.Fatalf("%d %s", rec.Code, rec.Body.String())
		}
	})
	t.Run("unclean paths never redirect", func(t *testing.T) {
		for _, p := range []string{"/api//v1/open", "/api/v1/../v1/open", "/api/v1/./open"} {
			rec := do(h, req{target: p})
			expectError(t, rec, 404, CodeNotFound)
			if rec.Header().Get("Location") != "" {
				t.Fatalf("%s redirected", p)
			}
		}
	})
	t.Run("HEAD is served by GET routes", func(t *testing.T) {
		rec := do(h, req{method: "HEAD", target: "/api/v1/open"})
		if rec.Code != 200 {
			t.Fatalf("status = %d", rec.Code)
		}
	})
	t.Run("route is known to the handler", func(t *testing.T) {
		rec := do(h, req{target: "/api/v1/info"})
		if !strings.Contains(rec.Body.String(), `"route":"/info"`) || !strings.Contains(rec.Body.String(), `"transport":"tcp"`) {
			t.Fatalf("%s", rec.Body.String())
		}
	})
}

func TestTransportAndPeerReachTheHandler(t *testing.T) {
	s, _ := newTestServer(t)
	rec := do(s.Handler(), req{target: "/api/v1/info", socket: &PeerCred{UID: 1000, GID: 1000, PID: 42}})
	if !strings.Contains(rec.Body.String(), `"transport":"socket"`) || !strings.Contains(rec.Body.String(), `"peer":"peer"`) {
		t.Fatalf("%s", rec.Body.String())
	}
	// Without the mark the request is TCP, so the TCP-only checks apply.
	rec = do(s.Handler(), req{target: "/api/v1/info", host: "evil:7070"})
	expectError(t, rec, 403, CodeBadHost)
}

func TestRequestID(t *testing.T) {
	s, _ := newTestServer(t)
	h := s.Handler()
	valid := "req_abcdefgh2345"
	tests := []struct {
		name     string
		in       string
		echoed   bool
		target   string
		wantCode int
	}{
		{"valid is echoed", valid, true, "/api/v1/open", 200},
		{"none is generated", "", false, "/api/v1/open", 200},
		{"too short", "req_abc", false, "/api/v1/open", 200},
		{"upper case", "req_ABCDEFGH2345", false, "/api/v1/open", 200},
		{"digits 0 1 8 9 are not base32", "req_abcdefgh0189", false, "/api/v1/open", 200},
		{"wrong prefix", "abc_abcdefgh2345", false, "/api/v1/open", 200},
		{"too long", valid + "a", false, "/api/v1/open", 200},
		{"log injection", "req_abcd\nfake log line", false, "/api/v1/open", 200},
		{"echoed on an error", valid, true, "/api/v1/nope", 404},
		{"echoed on a refusal", valid, true, "/api/v1/ping", 401},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			hd := map[string]string{}
			if tc.in != "" {
				hd["X-Request-Id"] = tc.in
			}
			r := req{target: tc.target, header: hd}.build()
			if strings.Contains(tc.in, "\n") {
				r.Header.Set("X-Request-Id", tc.in) // the recorder path allows what a real server would not
			}
			rec := newRecorder(h, r)
			if rec.Code != tc.wantCode {
				t.Fatalf("status = %d", rec.Code)
			}
			got := rec.Header().Get("X-Request-Id")
			if !ValidRequestID(got) {
				t.Fatalf("X-Request-Id %q is not valid", got)
			}
			if tc.echoed && got != tc.in {
				t.Fatalf("not echoed: %q", got)
			}
			if !tc.echoed && got == tc.in {
				t.Fatalf("an invalid id was echoed: %q", got)
			}
		})
	}
	t.Run("generated ids are unique and well formed", func(t *testing.T) {
		seen := map[string]bool{}
		for i := 0; i < 500; i++ {
			id := NewRequestID()
			if !ValidRequestID(id) || seen[id] {
				t.Fatalf("bad or repeated id %q", id)
			}
			seen[id] = true
		}
	})
}

func TestPanicBecomes500(t *testing.T) {
	s, logs := newTestServer(t)
	h := s.Handler()
	rec := do(h, req{method: "POST", target: "/api/v1/boom", header: jsonHeaders(nil), body: `{}`})
	e := expectError(t, rec, 500, CodeInternal)
	id := rec.Header().Get("X-Request-Id")
	if e.Error.Details["request_id"] != id || !ValidRequestID(id) {
		t.Fatalf("details.request_id = %v, header %q", e.Error.Details["request_id"], id)
	}
	text := logs.String()
	if !strings.Contains(text, "panic in request handler") || !strings.Contains(text, id) {
		t.Fatalf("panic not logged with the request id:\n%s", text)
	}
	if strings.Contains(text, strings.Repeat("A", 30)) {
		t.Fatalf("a token in the panic value reached the log unscrubbed:\n%s", text)
	}
	if strings.Contains(rec.Body.String(), "handler bug") {
		t.Fatalf("the panic value leaked to the client: %s", rec.Body.String())
	}
	// The server is still serving afterwards, and the access log recorded the 500.
	if rec := do(h, req{target: "/api/v1/open"}); rec.Code != 200 {
		t.Fatalf("server did not survive the panic: %d", rec.Code)
	}
	found := false
	for _, l := range logs.lines(t) {
		if l["msg"] == "api.request" && l["req_id"] == id && l["http_status"] == float64(500) && l["code"] == CodeInternal {
			found = true
		}
	}
	if !found {
		t.Fatalf("no access line for the panicking request:\n%s", text)
	}
}

func TestPanicAfterResponseStartedAbortsTheConnection(t *testing.T) {
	s, _ := newTestServer(t)
	defer func() {
		if rec := recover(); rec != http.ErrAbortHandler {
			t.Fatalf("recovered %v, want http.ErrAbortHandler", rec)
		}
	}()
	do(s.Handler(), req{method: "POST", target: "/api/v1/boom-late", header: jsonHeaders(nil), body: `{}`})
	t.Fatal("expected the handler to abort")
}

func TestMutatingGate(t *testing.T) {
	s, _ := newTestServer(t)
	h := s.Handler()
	s.SetMutationsOpen(false)
	s.SetStarting(true)

	t.Run("mutating route answers 503 with Retry-After while the gate is closed", func(t *testing.T) {
		rec := do(h, req{method: "POST", target: "/api/v1/echo", header: jsonHeaders(nil), body: `{}`})
		expectError(t, rec, 503, CodeStarting)
		if got := rec.Header().Get("Retry-After"); got != "5" {
			t.Fatalf("Retry-After = %q", got)
		}
		for _, m := range []string{"PUT", "DELETE"} {
			target := map[string]string{"PUT": "/api/v1/models/default", "DELETE": "/api/v1/thing"}[m]
			hd := jsonHeaders(map[string]string{"X-Test-Principal": "admin"})
			expectError(t, do(h, req{method: m, target: target, header: hd}), 503, CodeStarting)
		}
	})
	t.Run("reads and health answer while starting", func(t *testing.T) {
		if rec := do(h, req{target: "/api/v1/ping", header: map[string]string{"X-Test-Principal": "bearer"}}); rec.Code != 200 {
			t.Fatalf("GET status = %d", rec.Code)
		}
		rec := do(h, req{target: "/api/v1/health"})
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"starting":true`) {
			t.Fatalf("health: %d %s", rec.Code, rec.Body.String())
		}
	})
	t.Run("POST /mode/survival style routes are exempt", func(t *testing.T) {
		rec := do(h, req{method: "POST", target: "/api/v1/survival", header: map[string]string{"Content-Type": "application/json"}, body: `{}`})
		if rec.Code != 200 {
			t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
		}
	})
	t.Run("auth is decided before the gate", func(t *testing.T) {
		rec := do(h, req{method: "POST", target: "/api/v1/echo", header: map[string]string{"Content-Type": "application/json"}, body: `{}`})
		expectError(t, rec, 401, CodeUnauthenticated)
	})
	t.Run("opening the gate does not clear starting", func(t *testing.T) {
		s.SetMutationsOpen(true)
		if rec := do(h, req{method: "POST", target: "/api/v1/echo", header: jsonHeaders(nil), body: `{}`}); rec.Code != 200 {
			t.Fatalf("status = %d", rec.Code)
		}
		if !s.Starting() {
			t.Fatal("the gate must not touch the starting flag")
		}
		rec := do(h, req{target: "/api/v1/health"})
		if !strings.Contains(rec.Body.String(), `"starting":true`) {
			t.Fatalf("health: %s", rec.Body.String())
		}
		s.SetStarting(false)
		rec = do(h, req{target: "/api/v1/health"})
		if !strings.Contains(rec.Body.String(), `"starting":false`) {
			t.Fatalf("health: %s", rec.Body.String())
		}
	})
	t.Run("stopping closes the gate again", func(t *testing.T) {
		_ = s.Stop(t.Context())
		expectError(t, do(h, req{method: "POST", target: "/api/v1/echo", header: jsonHeaders(nil), body: `{}`}), 503, CodeStarting)
		if rec := do(h, req{method: "POST", target: "/api/v1/survival", header: map[string]string{"Content-Type": "application/json"}, body: `{}`}); rec.Code != 200 {
			t.Fatalf("the exempt route must still answer while stopping: %d", rec.Code)
		}
		select {
		case <-s.Stopping():
		default:
			t.Fatal("Stopping() is not closed")
		}
	})
}

func TestAuthSeam(t *testing.T) {
	t.Run("default is DenyAll: only level none works", func(t *testing.T) {
		s, _ := newTestServer(t, func(o *Options) { o.Auth = nil })
		h := s.Handler()
		if rec := do(h, req{target: "/api/v1/open"}); rec.Code != 200 {
			t.Fatalf("none route: %d", rec.Code)
		}
		expectError(t, do(h, req{target: "/api/v1/ping"}), 401, CodeUnauthenticated)
		if rec := do(h, req{target: "/api/v1/health"}); rec.Code != 200 {
			t.Fatalf("health: %d", rec.Code)
		}
	})
	t.Run("AllowAll lets everything through", func(t *testing.T) {
		s, _ := newTestServer(t, func(o *Options) { o.Auth = AllowAll{} })
		rec := do(s.Handler(), req{method: "DELETE", target: "/api/v1/thing", header: map[string]string{"Content-Type": "application/json"}})
		if rec.Code != 200 {
			t.Fatalf("status = %d", rec.Code)
		}
	})
	t.Run("the authenticator sees the matched route and transport", func(t *testing.T) {
		var gotRoute string
		var gotTransport Transport
		var gotPeer *PeerCred
		s, _ := newTestServer(t, func(o *Options) {
			o.Auth = AuthFunc(func(r *http.Request, rt *Route) (Principal, error) {
				gotRoute = rt.Method + " " + rt.Pattern
				info := InfoFrom(r.Context())
				gotTransport, gotPeer = info.Transport, info.Peer
				return Principal{Kind: KindOperator, Credential: CredSocket, Via: "uid=1000"}, nil
			})
		})
		do(s.Handler(), req{target: "/api/v1/ping", socket: &PeerCred{UID: 1000, GID: 7, PID: 9}})
		if gotRoute != "GET /ping" || gotTransport != TransportSocket || gotPeer == nil || gotPeer.UID != 1000 {
			t.Fatalf("route %q transport %q peer %v", gotRoute, gotTransport, gotPeer)
		}
	})
	t.Run("an authenticator error is written as is", func(t *testing.T) {
		s, _ := newTestServer(t, func(o *Options) {
			o.Auth = AuthFunc(func(*http.Request, *Route) (Principal, error) {
				return Principal{}, NewError(CodePeerNotOperator, http.StatusForbidden, nil)
			})
		})
		expectError(t, do(s.Handler(), req{target: "/api/v1/open"}), 403, CodePeerNotOperator)
	})
	t.Run("an unexpected authenticator error is a 500", func(t *testing.T) {
		s, _ := newTestServer(t, func(o *Options) {
			o.Auth = AuthFunc(func(*http.Request, *Route) (Principal, error) { return Principal{}, http.ErrNoCookie })
		})
		expectError(t, do(s.Handler(), req{target: "/api/v1/open"}), 500, CodeInternal)
	})
	t.Run("a principal that does not satisfy the route is refused", func(t *testing.T) {
		s, _ := newTestServer(t) // credAuth gives an operator, DELETE /thing needs admin
		hd := jsonHeaders(nil)
		expectError(t, do(s.Handler(), req{method: "DELETE", target: "/api/v1/thing", header: hd}), 403, CodeAdminRequired)
		s2, _ := newTestServer(t, func(o *Options) {
			o.Auth = AuthFunc(func(*http.Request, *Route) (Principal, error) { return Principal{}, nil })
		})
		expectError(t, do(s2.Handler(), req{target: "/api/v1/ping"}), 401, CodeUnauthenticated)
	})
	t.Run("the portal level is accepted where listed", func(t *testing.T) {
		s, _ := newTestServer(t)
		h := s.Handler()
		if rec := do(h, req{target: "/api/v1/models/x", header: map[string]string{"X-Test-Principal": "portal"}}); rec.Code != 200 {
			t.Fatalf("portal on op+portal route: %d", rec.Code)
		}
		expectError(t, do(h, req{target: "/api/v1/ping", header: map[string]string{"X-Test-Principal": "portal"}}), 401, CodePortalSessionEnded)
	})
}

func TestPrincipalSatisfies(t *testing.T) {
	anon := Anonymous()
	op := Principal{Kind: KindOperator, Credential: CredBearer, Via: "bearer"}
	sockOp := Principal{Kind: KindOperator, Credential: CredSocket, Via: "uid=1000"}
	admin := Principal{Kind: KindAdmin, Credential: CredSocket, Via: "uid=0"}
	portal := Principal{Kind: KindPortal, Credential: CredPortal, Via: "portal"}
	tests := []struct {
		name   string
		p      Principal
		levels []Level
		want   bool
	}{
		{"anonymous on none", anon, []Level{AuthNone}, true},
		{"anonymous on op", anon, []Level{AuthOp}, false},
		{"zero principal on op", Principal{}, []Level{AuthOp}, false},
		{"operator on op", op, []Level{AuthOp}, true},
		{"operator on admin", op, []Level{AuthAdmin}, false},
		{"operator on admin*", op, []Level{AuthAdminStar}, false},
		{"socket operator on admin*", sockOp, []Level{AuthAdminStar}, true},
		{"socket operator on admin", sockOp, []Level{AuthAdmin}, false},
		{"admin on op", admin, []Level{AuthOp}, true},
		{"admin on admin", admin, []Level{AuthAdmin}, true},
		{"admin on admin*", admin, []Level{AuthAdminStar}, true},
		{"portal on portal", portal, []Level{AuthPortal}, true},
		{"portal on op", portal, []Level{AuthOp}, false},
		{"portal on op or portal", portal, []Level{AuthOp, AuthPortal}, true},
		{"operator on op or portal", op, []Level{AuthOp, AuthPortal}, true},
		{"nothing listed", admin, nil, false},
		{"unknown level", admin, []Level{"root"}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.p.Satisfies(tc.levels); got != tc.want {
				t.Fatalf("Satisfies = %v, want %v", got, tc.want)
			}
		})
	}
	for p, want := range map[string]string{
		Anonymous().String(): "anonymous", op.String(): "operator:bearer", sockOp.String(): "operator:uid=1000",
		admin.String(): "admin:uid=0", (Principal{}).String(): "anonymous", (Principal{Kind: KindOperator}).String(): "operator",
	} {
		if p != want {
			t.Fatalf("String() = %q, want %q", p, want)
		}
	}
}

func TestRouteDeadline(t *testing.T) {
	s, _ := newTestServer(t)
	rec := do(s.Handler(), req{method: "POST", target: "/api/v1/slow", header: map[string]string{"Content-Type": "application/json"}, body: `{}`})
	e := expectError(t, rec, 504, CodeInternal)
	if e.Error.Details["reason"] != "deadline_exceeded" || e.Error.Details["request_id"] == nil {
		t.Fatalf("details = %v", e.Error.Details)
	}
}

func TestAccessLog(t *testing.T) {
	s, logs := newTestServer(t)
	h := s.Handler()
	do(h, req{target: "/api/v1/ping?token=secretvalue", header: map[string]string{"X-Test-Principal": "bearer", "X-Request-Id": "req_abcdefgh2345"}})
	do(h, req{method: "POST", target: "/api/v1/fail", header: map[string]string{"Content-Type": "application/json"}, body: `{}`})

	var get, post map[string]any
	for _, l := range logs.lines(t) {
		if l["msg"] != "api.request" {
			continue
		}
		switch l["method"] {
		case "GET":
			get = l
		case "POST":
			post = l
		}
	}
	if get == nil || post == nil {
		t.Fatalf("missing access lines:\n%s", logs.String())
	}
	if get["level"] != "DEBUG" || post["level"] != "INFO" {
		t.Fatalf("levels: GET %v, POST %v (want DEBUG and INFO)", get["level"], post["level"])
	}
	if get["req_id"] != "req_abcdefgh2345" || get["path"] != "/api/v1/ping" || get["http_status"] != float64(200) ||
		get["principal"] != "operator:bearer" || get["component"] != "api" || get["transport"] != "tcp" {
		t.Fatalf("GET line: %v", get)
	}
	if _, ok := get["duration_ms"].(float64); !ok {
		t.Fatalf("no duration_ms: %v", get)
	}
	if strings.Contains(logs.String(), "secretvalue") {
		t.Fatal("the query string must never reach the access log")
	}
	if post["http_status"] != float64(409) || post["code"] != CodeBusy || post["principal"] != "anonymous" {
		t.Fatalf("POST line: %v", post)
	}

	t.Run("at info level reads are not logged", func(t *testing.T) {
		buf := &syncBuf{}
		s2, _ := newTestServer(t, func(o *Options) { o.Logger = logx.New(buf, nil) })
		do(s2.Handler(), req{target: "/api/v1/open"})
		if strings.Contains(buf.String(), "api.request") {
			t.Fatalf("a GET was logged at info level:\n%s", buf.String())
		}
		do(s2.Handler(), req{method: "POST", target: "/api/v1/fail", header: map[string]string{"Content-Type": "application/json"}, body: `{}`})
		if !strings.Contains(buf.String(), "api.request") {
			t.Fatal("a mutating request must be logged at info level")
		}
	})
}
