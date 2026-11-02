package proxy

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/api"
)

// seen is what the fake upstream received.
type seen struct {
	Method, Path, EscapedPath, RawQuery, Host string
	Header                                    http.Header
	Body                                      []byte
}

// upstream is a loopback HTTP server that records every request and answers with handler.
type upstream struct {
	srv  *httptest.Server
	mu   sync.Mutex
	reqs []seen
}

func newUpstream(t *testing.T, handler http.HandlerFunc) *upstream {
	t.Helper()
	u := &upstream{}
	u.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body []byte
		if r.Body != nil && r.Header.Get("Upgrade") == "" {
			body, _ = io.ReadAll(r.Body)
		}
		u.mu.Lock()
		u.reqs = append(u.reqs, seen{r.Method, r.URL.Path, r.URL.EscapedPath(), r.URL.RawQuery, r.Host, r.Header.Clone(), body})
		u.mu.Unlock()
		if handler != nil {
			handler(w, r)
			return
		}
		_, _ = io.WriteString(w, "ok")
	}))
	t.Cleanup(u.srv.Close)
	return u
}

func (u *upstream) url() *url.URL {
	x, _ := url.Parse(u.srv.URL)
	return x
}

func (u *upstream) last(t *testing.T) seen {
	t.Helper()
	u.mu.Lock()
	defer u.mu.Unlock()
	if len(u.reqs) == 0 {
		t.Fatal("the upstream saw no request")
	}
	return u.reqs[len(u.reqs)-1]
}

func (u *upstream) count() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return len(u.reqs)
}

// deadURL is a loopback address nothing listens on.
func deadURL(t *testing.T) *url.URL {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	x, _ := url.Parse("http://" + addr)
	return x
}

// quiet is a logger for tests that do not look at the log.
var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func opts(target *url.URL, mod ...func(*Options)) Options {
	o := Options{Target: target, Logger: quiet}
	for _, m := range mod {
		m(&o)
	}
	return o
}

// serve runs one request through h with the recorder and returns the result.
func serve(h http.Handler, method, target string, hdr map[string]string, body io.Reader) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, target, body)
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

type errEnvelope struct {
	Error struct {
		Code    string         `json:"code"`
		Message string         `json:"message"`
		Fix     string         `json:"fix"`
		Details map[string]any `json:"details"`
	} `json:"error"`
}

func envelope(t *testing.T, rec *httptest.ResponseRecorder, status int, code string) errEnvelope {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("status = %d, want %d (body %s)", rec.Code, status, rec.Body.String())
	}
	var e errEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil {
		t.Fatalf("body is not an envelope: %q", rec.Body.String())
	}
	if e.Error.Code != code {
		t.Fatalf("code = %s, want %s", e.Error.Code, code)
	}
	return e
}

func TestNewPanicsOnBadOptions(t *testing.T) {
	mustURL := func(s string) *url.URL {
		u, err := url.Parse(s)
		if err != nil {
			t.Fatal(err)
		}
		return u
	}
	tests := []struct {
		name string
		o    Options
	}{
		{"nil target", Options{}},
		{"https", Options{Target: mustURL("https://127.0.0.1:8080")}},
		{"no scheme", Options{Target: &url.URL{Host: "127.0.0.1:8080"}}},
		{"localhost name", Options{Target: mustURL("http://localhost:8080")}},
		{"private address", Options{Target: mustURL("http://10.1.2.3:8080")}},
		{"public address", Options{Target: mustURL("http://203.0.113.9:80")}},
		{"other loopback", Options{Target: mustURL("http://127.0.0.2:8080")}},
		{"ipv6 loopback", Options{Target: mustURL("http://[::1]:8080")}},
		{"unspecified", Options{Target: mustURL("http://0.0.0.0:8080")}},
		{"dns name", Options{Target: mustURL("http://llama.internal:8080")}},
		{"no port", Options{Target: mustURL("http://127.0.0.1")}},
		{"port zero", Options{Target: mustURL("http://127.0.0.1:0")}},
		{"port too big", Options{Target: mustURL("http://127.0.0.1:70000")}},
		{"port not a number", Options{Target: &url.URL{Scheme: "http", Host: "127.0.0.1:http"}}},
		{"user info", Options{Target: mustURL("http://user:pw@127.0.0.1:8080")}},
		{"query", Options{Target: mustURL("http://127.0.0.1:8080/?x=1")}},
		{"fragment", Options{Target: mustURL("http://127.0.0.1:8080/#f")}},
		{"prefix without slash", Options{Target: mustURL("http://127.0.0.1:8080"), StripPrefix: "chat"}},
		{"prefix is root", Options{Target: mustURL("http://127.0.0.1:8080"), StripPrefix: "/"}},
		{"prefix with percent", Options{Target: mustURL("http://127.0.0.1:8080"), StripPrefix: "/a%2fb"}},
		{"forwarded prefix without prefix", Options{Target: mustURL("http://127.0.0.1:8080"), SetForwardedPrefix: true}},
		{"down status 500", Options{Target: mustURL("http://127.0.0.1:8080"), DownStatus: 500}},
		{"down status 504", Options{Target: mustURL("http://127.0.0.1:8080"), DownStatus: 504}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("New did not panic")
				}
			}()
			New(tc.o)
		})
	}
	t.Run("valid options do not panic", func(t *testing.T) {
		for _, s := range []string{"http://127.0.0.1:8080", "http://127.0.0.1:8888/", "http://127.0.0.1:1", "http://127.0.0.1:65535", "http://127.0.0.1:8080/base"} {
			New(Options{Target: mustURL(s), Logger: quiet})
		}
		New(Options{Target: mustURL("http://127.0.0.1:8080"), StripPrefix: "/chat", SetForwardedPrefix: true, DownStatus: 503})
	})
}

func TestPathRewriting(t *testing.T) {
	up := newUpstream(t, nil)
	tests := []struct {
		name         string
		mod          func(*Options)
		target       string
		wantPath     string
		wantEscaped  string
		wantRawQuery string
	}{
		{"strip prefix", func(o *Options) { o.StripPrefix = "/chat" }, "/chat/props", "/props", "/props", ""},
		{"strip prefix keeps query", func(o *Options) { o.StripPrefix = "/chat" }, "/chat/v1/models?limit=3&x=a%20b", "/v1/models", "/v1/models", "limit=3&x=a%20b"},
		{"bare prefix is root", func(o *Options) { o.StripPrefix = "/chat" }, "/chat", "/", "/", ""},
		{"prefix with slash is root", func(o *Options) { o.StripPrefix = "/chat" }, "/chat/", "/", "/", ""},
		{"trailing slash in option", func(o *Options) { o.StripPrefix = "/chat/" }, "/chat/props", "/props", "/props", ""},
		{"nested prefix", func(o *Options) { o.StripPrefix = "/a/b" }, "/a/b/c/d", "/c/d", "/c/d", ""},
		{"encoded characters survive", func(o *Options) { o.StripPrefix = "/chat" }, "/chat/files/a%20b%2Fc.txt", "/files/a b/c.txt", "/files/a%20b%2Fc.txt", ""},
		{"keep path (library)", func(o *Options) {}, "/library/viewer?book=x", "/library/viewer", "/library/viewer", "book=x"},
		{"keep path root", func(o *Options) {}, "/", "/", "/", ""},
		{"base path is prepended", func(o *Options) { o.Target.Path = "/base" }, "/x/y", "/base/x/y", "/base/x/y", ""},
		{"base path with strip", func(o *Options) { o.Target.Path = "/base/"; o.StripPrefix = "/m" }, "/m/x", "/base/x", "/base/x", ""},
		{"no query stays none", func(o *Options) {}, "/p", "/p", "/p", ""},
		{"empty query value kept", func(o *Options) {}, "/p?a=&b", "/p", "/p", "a=&b"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			target := *up.url()
			h := New(opts(&target, tc.mod))
			rec := serve(h, "GET", tc.target, nil, nil)
			if rec.Code != 200 {
				t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
			}
			got := up.last(t)
			if got.Path != tc.wantPath || got.EscapedPath != tc.wantEscaped || got.RawQuery != tc.wantRawQuery {
				t.Fatalf("upstream saw path=%q escaped=%q query=%q; want %q %q %q", got.Path, got.EscapedPath, got.RawQuery, tc.wantPath, tc.wantEscaped, tc.wantRawQuery)
			}
		})
	}

	t.Run("a path outside the mount is a 404 and never forwarded", func(t *testing.T) {
		before := up.count()
		target := *up.url()
		h := New(opts(&target, func(o *Options) { o.StripPrefix = "/chat" }))
		for _, p := range []string{"/chatter", "/other/props", "/chat%2Fprops"} {
			rec := serve(h, "GET", p, nil, nil)
			if rec.Code != 404 {
				t.Fatalf("%s: status %d", p, rec.Code)
			}
		}
		if up.count() != before {
			t.Fatal("a request outside the mount reached the upstream")
		}
	})
	t.Run("dot-dot and NUL never reach the upstream", func(t *testing.T) {
		before := up.count()
		target := *up.url()
		h := New(opts(&target, func(o *Options) { o.StripPrefix = "/chat" }))
		for _, p := range []string{"/chat/../admin", "/chat/a/../../b", "/chat/%2e%2e/admin", "/chat/x%00y"} {
			r := httptest.NewRequest("GET", "/chat/x", nil)
			u, err := url.Parse(p)
			if err != nil {
				t.Fatal(err)
			}
			r.URL = u
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, r)
			if rec.Code != 404 {
				t.Fatalf("%s: status %d", p, rec.Code)
			}
		}
		if up.count() != before {
			t.Fatal("an unsafe path reached the upstream")
		}
	})
}

func TestRequestHeaders(t *testing.T) {
	up := newUpstream(t, nil)
	clientHeaders := map[string]string{
		"Authorization":       "Bearer CLIENT-TOKEN",
		"Proxy-Authorization": "Basic Y2xpZW50",
		"X-Api-Key":           "CLIENT-API-KEY",
		"Cookie":              "localai_session=SESSION; lai_portal=PORTAL",
		"X-Forwarded-For":     "10.9.9.9",
		"X-Forwarded-Host":    "evil.test",
		"X-Forwarded-Proto":   "https",
		"X-Forwarded-Prefix":  "/spoofed",
		"X-Forwarded-Port":    "443",
		"Forwarded":           "for=10.9.9.9;host=evil.test",
		"X-Real-Ip":           "10.9.9.9",
		"Via":                 "1.1 evil",
		"Keep-Alive":          "timeout=5",
		"Te":                  "trailers",
		"X-Custom-Secret":     "STRIP-ME",
		"X-Keep-Me":           "kept",
		"User-Agent":          "test-agent/1",
		"Accept":              "text/html",
		"Accept-Encoding":     "gzip",
		"Accept-Language":     "es",
	}
	target := *up.url()
	h := New(opts(&target, func(o *Options) {
		o.StripPrefix = "/chat"
		o.StripRequestHeaders = []string{"x-custom-secret"}
	}))
	r := httptest.NewRequest("GET", "http://evil.test/chat/props", nil)
	r.Host = "evil.test:9999"
	for k, v := range clientHeaders {
		r.Header.Set(k, v)
	}
	r.Header.Set("Connection", "close, X-Hop-Secret")
	r.Header.Set("X-Hop-Secret", "named-in-connection")
	h.ServeHTTP(httptest.NewRecorder(), r)

	got := up.last(t)
	for _, name := range []string{
		"Authorization", "Proxy-Authorization", "X-Api-Key", "Cookie", "X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto",
		"X-Forwarded-Prefix", "X-Forwarded-Port", "Forwarded", "X-Real-Ip", "Via", "Keep-Alive", "X-Custom-Secret", "X-Hop-Secret",
	} {
		if v := got.Header.Values(name); len(v) != 0 {
			t.Errorf("upstream saw %s: %q", name, v)
		}
	}
	for name, want := range map[string]string{"X-Keep-Me": "kept", "User-Agent": "test-agent/1", "Accept": "text/html", "Accept-Encoding": "gzip", "Accept-Language": "es"} {
		if got.Header.Get(name) != want {
			t.Errorf("%s = %q, want %q", name, got.Header.Get(name), want)
		}
	}
	if want := strings.TrimPrefix(up.srv.URL, "http://"); got.Host != want {
		t.Errorf("upstream Host = %q, want %q (pinned)", got.Host, want)
	}
	for k, v := range got.Header {
		for _, s := range v {
			if strings.Contains(s, "CLIENT-") || strings.Contains(s, "SESSION") || strings.Contains(s, "evil") {
				t.Errorf("client material reached the upstream in %s: %q", k, s)
			}
		}
	}
}

func TestInjectedHeaders(t *testing.T) {
	up := newUpstream(t, nil)
	target := *up.url()
	h := New(opts(&target, func(o *Options) {
		o.StripPrefix = "/chat"
		o.SetForwardedPrefix = true
		o.InjectHeaders = func(r *http.Request) { r.Header.Set("Authorization", "Bearer INJECTED-KEY") }
	}))

	t.Run("injected after stripping, replacing the client's", func(t *testing.T) {
		serve(h, "GET", "/chat/props", map[string]string{"Authorization": "Bearer CLIENT-KEY", "X-Forwarded-Prefix": "/spoof"}, nil)
		got := up.last(t)
		if v := got.Header.Values("Authorization"); len(v) != 1 || v[0] != "Bearer INJECTED-KEY" {
			t.Fatalf("Authorization = %q", v)
		}
		if got.Header.Get("X-Forwarded-Prefix") != "/chat" {
			t.Fatalf("X-Forwarded-Prefix = %q", got.Header.Get("X-Forwarded-Prefix"))
		}
	})
	t.Run("a client cannot get the injected header removed through Connection", func(t *testing.T) {
		serve(h, "GET", "/chat/props", map[string]string{"Connection": "Authorization, X-Forwarded-Prefix"}, nil)
		got := up.last(t)
		if got.Header.Get("Authorization") != "Bearer INJECTED-KEY" || got.Header.Get("X-Forwarded-Prefix") != "/chat" {
			t.Fatalf("headers lost: %v", got.Header)
		}
	})
	t.Run("not added when not asked", func(t *testing.T) {
		plain := New(opts(&target))
		serve(plain, "GET", "/library/", map[string]string{"X-Forwarded-Prefix": "/x"}, nil)
		got := up.last(t)
		if got.Header.Get("Authorization") != "" || got.Header.Get("X-Forwarded-Prefix") != "" || got.Header.Get("X-Forwarded-For") != "" {
			t.Fatalf("unexpected headers: %v", got.Header)
		}
	})
}

func TestBodyIsForwarded(t *testing.T) {
	up := newUpstream(t, nil)
	target := *up.url()
	h := New(opts(&target))
	rec := serve(h, "POST", "/x", map[string]string{"Content-Type": "application/json"}, strings.NewReader(`{"a":1}`))
	if rec.Code != 200 {
		t.Fatal(rec.Code)
	}
	if got := up.last(t); string(got.Body) != `{"a":1}` || got.Method != "POST" || got.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("%+v", got)
	}
}

func TestBodyLimit(t *testing.T) {
	up := newUpstream(t, nil)
	target := *up.url()
	h := New(opts(&target, func(o *Options) { o.BodyLimit = 1024 }))

	t.Run("declared too large", func(t *testing.T) {
		before := up.count()
		rec := serve(h, "POST", "/x", map[string]string{"Content-Type": "application/json"}, strings.NewReader(strings.Repeat("a", 2048)))
		e := envelope(t, rec, 413, api.CodeTooLarge)
		if e.Error.Details["limit"] != float64(1024) {
			t.Fatalf("details = %v", e.Error.Details)
		}
		if up.count() != before {
			t.Fatal("an oversized request reached the upstream")
		}
	})
	t.Run("exactly the limit passes", func(t *testing.T) {
		rec := serve(h, "POST", "/x", nil, strings.NewReader(strings.Repeat("a", 1024)))
		if rec.Code != 200 || len(up.last(t).Body) != 1024 {
			t.Fatalf("%d", rec.Code)
		}
	})
	t.Run("streamed body that grows past the limit", func(t *testing.T) {
		front := httptest.NewServer(h)
		defer front.Close()
		pr, pw := io.Pipe()
		go func() {
			chunk := bytes.Repeat([]byte("b"), 512)
			for i := 0; i < 8; i++ {
				if _, err := pw.Write(chunk); err != nil {
					return
				}
			}
			_ = pw.Close()
		}()
		req, _ := http.NewRequest("POST", front.URL+"/x", pr) // unknown length: chunked
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusRequestEntityTooLarge {
			b, _ := io.ReadAll(resp.Body)
			t.Fatalf("status = %d, want 413 (body %s)", resp.StatusCode, b)
		}
	})
	t.Run("default limit is 1 MiB", func(t *testing.T) {
		d := New(opts(&target))
		envelope(t, serve(d, "POST", "/x", nil, bytes.NewReader(make([]byte, 1<<20+1))), 413, api.CodeTooLarge)
		if rec := serve(d, "POST", "/x", nil, bytes.NewReader(make([]byte, 1<<20))); rec.Code != 200 {
			t.Fatalf("1 MiB exactly: %d", rec.Code)
		}
	})
	t.Run("a larger mount limit (chat)", func(t *testing.T) {
		d := New(opts(&target, func(o *Options) { o.BodyLimit = 32 << 20 }))
		if rec := serve(d, "POST", "/x", nil, bytes.NewReader(make([]byte, 3<<20))); rec.Code != 200 {
			t.Fatalf("3 MiB: %d", rec.Code)
		}
	})
}

func TestStreamingFlushesEachChunk(t *testing.T) {
	release := make(chan struct{})
	up := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl := w.(http.Flusher)
		_, _ = io.WriteString(w, "data: one\n\n")
		fl.Flush()
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		_, _ = io.WriteString(w, "data: two\n\n")
		fl.Flush()
	})
	target := *up.url()
	front := httptest.NewServer(New(opts(&target, func(o *Options) { o.ResponseTimeout = -1; o.IdleTimeout = 10 * time.Minute })))
	defer front.Close()

	resp, err := http.Get(front.URL + "/stream")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	br := bufio.NewReader(resp.Body)
	got := make(chan string, 1)
	go func() {
		l, _ := br.ReadString('\n')
		got <- l
	}()
	select {
	case l := <-got:
		if l != "data: one\n" {
			t.Fatalf("first chunk = %q", l)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the first chunk was not delivered while the upstream was still streaming: the proxy buffers")
	}
	close(release)
	rest, _ := io.ReadAll(br)
	if !strings.Contains(string(rest), "data: two") {
		t.Fatalf("rest = %q", rest)
	}
	if resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("Content-Type = %q", resp.Header.Get("Content-Type"))
	}
}

func TestWebSocketUpgrade(t *testing.T) {
	up := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") || !strings.Contains(strings.ToLower(r.Header.Get("Connection")), "upgrade") {
			http.Error(w, "not an upgrade", 400)
			return
		}
		conn, buf, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = buf.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSet-Cookie: up=1\r\n\r\n")
		_ = buf.Flush()
		b := make([]byte, 64)
		for {
			n, err := buf.Read(b)
			if n > 0 {
				_, _ = conn.Write(bytes.ToUpper(b[:n]))
			}
			if err != nil {
				return
			}
		}
	})
	target := *up.url()
	// A short total timeout must not cut an upgraded connection.
	front := httptest.NewServer(New(opts(&target, func(o *Options) {
		o.StripPrefix = "/chat"
		o.ResponseTimeout = 150 * time.Millisecond
		o.IdleTimeout = 150 * time.Millisecond
		o.DropSetCookie = true
	})))
	defer front.Close()

	conn, err := net.Dial("tcp", strings.TrimPrefix(front.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	_, _ = io.WriteString(conn, "GET /chat/ws HTTP/1.1\r\nHost: "+strings.TrimPrefix(front.URL, "http://")+"\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n"+
		"Sec-WebSocket-Key: "+base64.StdEncoding.EncodeToString(make([]byte, 16))+"\r\nSec-WebSocket-Version: 13\r\nAuthorization: Bearer CLIENT-KEY\r\nCookie: lai_portal=P\r\n\r\n")
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if resp.Header.Get("Set-Cookie") != "" {
		t.Fatalf("Set-Cookie not dropped on the upgrade: %v", resp.Header)
	}
	got := up.last(t)
	if got.Path != "/ws" || got.Header.Get("Authorization") != "" || got.Header.Get("Cookie") != "" || got.Header.Get("Sec-Websocket-Key") == "" {
		t.Fatalf("upstream saw %+v", got)
	}

	// Past both timeouts the tunnel still carries bytes, in both directions.
	for i := 0; i < 3; i++ {
		time.Sleep(120 * time.Millisecond)
		_, _ = io.WriteString(conn, "hello")
		buf := make([]byte, 5)
		if _, err := io.ReadFull(br, buf); err != nil {
			t.Fatalf("round %d: %v", i, err)
		}
		if string(buf) != "HELLO" {
			t.Fatalf("round %d: %q", i, buf)
		}
	}
}

func TestUpstreamDown(t *testing.T) {
	dead := deadURL(t)

	t.Run("browser gets the page with the code", func(t *testing.T) {
		h := New(opts(dead, func(o *Options) { o.DownStatus = 502; o.DownCode = api.CodeChatDown }))
		rec := serve(h, "GET", "/chat/", map[string]string{"Accept": "text/html,application/xhtml+xml"}, nil)
		if rec.Code != 502 {
			t.Fatalf("status = %d", rec.Code)
		}
		body := rec.Body.String()
		for _, want := range []string{"LAI-SVC-102", "The AI (llama) isn&#39;t answering", "What to do"} {
			if !strings.Contains(body, want) {
				t.Errorf("page lacks %q", want)
			}
		}
		if !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/html") || rec.Header().Get("Content-Security-Policy") != api.HTMLContentSecurityPolicy {
			t.Fatalf("headers: %v", rec.Header())
		}
		if strings.Contains(body, "127.0.0.1") || strings.Contains(body, dead.Host) {
			t.Fatalf("the page names the upstream: %s", body)
		}
	})
	t.Run("API client gets the envelope", func(t *testing.T) {
		h := New(opts(dead, func(o *Options) { o.DownStatus = 502; o.DownCode = api.CodeChatDown }))
		for _, accept := range []string{"application/json", "*/*", ""} {
			hdr := map[string]string{}
			if accept != "" {
				hdr["Accept"] = accept
			}
			e := envelope(t, serve(h, "GET", "/v1/models", hdr, nil), 502, api.CodeChatDown)
			if e.Error.Message == "" || e.Error.Fix == "" || e.Error.Details["reason"] != "upstream_down" {
				t.Fatalf("%+v", e)
			}
			if strings.Contains(e.Error.Message+e.Error.Fix, dead.Host) {
				t.Fatal("the envelope names the upstream")
			}
		}
	})
	t.Run("library: 503 and its own code, Spanish", func(t *testing.T) {
		h := New(opts(dead, func(o *Options) { o.DownStatus = 503; o.DownCode = api.CodeLibraryDown }))
		r := httptest.NewRequest("GET", "/library/", nil)
		r.Header.Set("Accept", "text/html")
		r = r.WithContext(api.WithRequestInfo(r.Context(), &api.RequestInfo{ID: "req_abcdefghijkl", Lang: "es"}))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		if rec.Code != 503 || !strings.Contains(rec.Body.String(), "El servicio de la biblioteca no se está ejecutando") || !strings.Contains(rec.Body.String(), "LAI-SVC-002") {
			t.Fatalf("%d %s", rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "req_abcdefghijkl") {
			t.Fatal("the page does not carry the request id")
		}
	})
	t.Run("defaults: 502 and the llama code", func(t *testing.T) {
		envelope(t, serve(New(opts(dead)), "GET", "/", nil, nil), 502, api.CodeRouterDown)
	})
	t.Run("a POST to a dead upstream", func(t *testing.T) {
		envelope(t, serve(New(opts(dead)), "POST", "/v1/chat/completions", map[string]string{"Content-Type": "application/json"}, strings.NewReader(`{}`)), 502, api.CodeRouterDown)
	})
	t.Run("an upstream that closes without answering", func(t *testing.T) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer ln.Close()
		go func() {
			for {
				c, err := ln.Accept()
				if err != nil {
					return
				}
				_ = c.Close()
			}
		}()
		u, _ := url.Parse("http://" + ln.Addr().String())
		envelope(t, serve(New(opts(u)), "GET", "/", nil, nil), 502, api.CodeRouterDown)
	})
	t.Run("client gone: nothing is written", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		r := httptest.NewRequest("GET", "/", nil).WithContext(ctx)
		rec := httptest.NewRecorder()
		New(opts(dead)).ServeHTTP(rec, r)
		if rec.Body.Len() != 0 {
			t.Fatalf("wrote %q for a client that left", rec.Body.String())
		}
	})
}

func TestTimeouts(t *testing.T) {
	t.Run("slow backend: total timeout is 504 with the mount's code", func(t *testing.T) {
		up := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
			select {
			case <-time.After(5 * time.Second):
			case <-r.Context().Done():
			}
		})
		target := *up.url()
		h := New(opts(&target, func(o *Options) { o.ResponseTimeout = 150 * time.Millisecond; o.DownCode = api.CodeLibraryDown }))
		start := time.Now()
		e := envelope(t, serve(h, "GET", "/slow", nil, nil), 504, api.CodeLibraryDown)
		if d := time.Since(start); d > 2*time.Second {
			t.Fatalf("took %v", d)
		}
		if e.Error.Details["reason"] != "upstream_timeout" {
			t.Fatalf("%+v", e)
		}
	})
	t.Run("slow backend: idle timeout before the first byte", func(t *testing.T) {
		up := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
			select {
			case <-time.After(5 * time.Second):
			case <-r.Context().Done():
			}
		})
		target := *up.url()
		h := New(opts(&target, func(o *Options) { o.ResponseTimeout = -1; o.IdleTimeout = 150 * time.Millisecond }))
		e := envelope(t, serve(h, "GET", "/slow", nil, nil), 504, api.CodeRouterDown)
		if e.Error.Details["reason"] != "upstream_idle" {
			t.Fatalf("%+v", e)
		}
	})
	t.Run("a stream that keeps moving is never cut by the idle limit or a missing total cap", func(t *testing.T) {
		up := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
			fl := w.(http.Flusher)
			for i := 0; i < 10; i++ {
				_, _ = io.WriteString(w, "tick\n")
				fl.Flush()
				time.Sleep(60 * time.Millisecond)
			}
		})
		target := *up.url()
		front := httptest.NewServer(New(opts(&target, func(o *Options) { o.ResponseTimeout = -1; o.IdleTimeout = 250 * time.Millisecond })))
		defer front.Close()
		resp, err := http.Get(front.URL)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, err := io.ReadAll(resp.Body)
		if err != nil || strings.Count(string(b), "tick") != 10 {
			t.Fatalf("got %q, %v", b, err)
		}
	})
	t.Run("a stream that stalls is cut by the idle limit", func(t *testing.T) {
		up := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, "first\n")
			w.(http.Flusher).Flush()
			select {
			case <-time.After(5 * time.Second):
			case <-r.Context().Done():
			}
			_, _ = io.WriteString(w, "never\n")
		})
		target := *up.url()
		front := httptest.NewServer(New(opts(&target, func(o *Options) { o.ResponseTimeout = -1; o.IdleTimeout = 200 * time.Millisecond })))
		defer front.Close()
		start := time.Now()
		resp, err := http.Get(front.URL)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, err := io.ReadAll(resp.Body)
		if d := time.Since(start); d > 3*time.Second {
			t.Fatalf("the stalled stream was not cut: %v", d)
		}
		if strings.Contains(string(b), "never") {
			t.Fatalf("body = %q", b)
		}
		if err == nil {
			t.Fatalf("the client must see the stream break, not a clean end: %q", b)
		}
	})
	t.Run("the total cap applies to a stream that keeps moving", func(t *testing.T) {
		up := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
			fl := w.(http.Flusher)
			for i := 0; i < 100; i++ {
				if _, err := io.WriteString(w, "tick\n"); err != nil {
					return
				}
				fl.Flush()
				time.Sleep(30 * time.Millisecond)
			}
		})
		target := *up.url()
		front := httptest.NewServer(New(opts(&target, func(o *Options) { o.ResponseTimeout = 200 * time.Millisecond })))
		defer front.Close()
		resp, err := http.Get(front.URL)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, err := io.ReadAll(resp.Body)
		if err == nil || strings.Count(string(b), "tick") >= 100 {
			t.Fatalf("not cut: %d ticks, err %v", strings.Count(string(b), "tick"), err)
		}
	})
	t.Run("default total timeout is 600 s", func(t *testing.T) {
		up := newUpstream(t, nil)
		target := *up.url()
		p := New(opts(&target)).(*proxy)
		if p.total != 600*time.Second || p.idle != 0 {
			t.Fatalf("total=%v idle=%v", p.total, p.idle)
		}
		p = New(opts(&target, func(o *Options) { o.ResponseTimeout = -1 })).(*proxy)
		if p.total != 0 {
			t.Fatalf("a negative ResponseTimeout must mean no cap, got %v", p.total)
		}
	})
}

func TestPolicies(t *testing.T) {
	up := newUpstream(t, nil)
	target := *up.url()

	t.Run("ReadOnly", func(t *testing.T) {
		h := New(opts(&target, func(o *Options) { o.MethodPolicy = ReadOnly }))
		for _, m := range []string{"GET", "HEAD"} {
			if rec := serve(h, m, "/library/", nil, nil); rec.Code != 200 {
				t.Fatalf("%s: %d", m, rec.Code)
			}
		}
		before := up.count()
		for _, m := range []string{"POST", "PUT", "DELETE", "PATCH"} {
			rec := serve(h, m, "/library/", map[string]string{"Content-Type": "application/json"}, strings.NewReader("{}"))
			envelope(t, rec, 405, api.CodeInvalidRequest)
			if rec.Header().Get("Allow") != "GET, HEAD" {
				t.Fatalf("Allow = %q", rec.Header().Get("Allow"))
			}
		}
		if up.count() != before {
			t.Fatal("a refused method reached the upstream")
		}
	})
	t.Run("PathPolicy sees the upstream path", func(t *testing.T) {
		var seenPaths []string
		h := New(opts(&target, func(o *Options) {
			o.StripPrefix = "/chat"
			o.PathPolicy = func(r *http.Request) (Decision, http.Handler, string) {
				seenPaths = append(seenPaths, r.Method+" "+r.URL.Path)
				return Allow, nil, ""
			}
		}))
		serve(h, "GET", "/chat/props", nil, nil)
		serve(h, "POST", "/chat", nil, strings.NewReader("{}"))
		if len(seenPaths) != 2 || seenPaths[0] != "GET /props" || seenPaths[1] != "POST /" {
			t.Fatalf("policy saw %v", seenPaths)
		}
	})
	t.Run("deny, intercept, allow", func(t *testing.T) {
		const codeDenied = "LAI-SVC-115"
		var interceptedPath string
		var interceptedBody string
		h := New(opts(&target, func(o *Options) {
			o.StripPrefix = "/chat"
			o.PathPolicy = func(r *http.Request) (Decision, http.Handler, string) {
				switch {
				case strings.HasPrefix(r.URL.Path, "/tools"):
					return Deny, nil, codeDenied
				case r.Method == "POST" && r.URL.Path == "/models/load":
					return Intercept, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						b, _ := io.ReadAll(r.Body)
						interceptedPath, interceptedBody = r.URL.Path, string(b)
						_, _ = io.WriteString(w, `{"success":true}`)
					}), ""
				case r.URL.Path == "/broken":
					return Intercept, nil, ""
				}
				return Allow, nil, ""
			}
		}))
		before := up.count()
		e := envelope(t, serve(h, "GET", "/chat/tools/run", nil, nil), 403, codeDenied)
		if e.Error.Details["reason"] != "path_not_allowed" {
			t.Fatalf("%+v", e)
		}
		rec := serve(h, "POST", "/chat/models/load", map[string]string{"Content-Type": "application/json"}, strings.NewReader(`{"model":"m1"}`))
		if rec.Code != 200 || rec.Body.String() != `{"success":true}` {
			t.Fatalf("%d %s", rec.Code, rec.Body.String())
		}
		if interceptedPath != "/chat/models/load" || interceptedBody != `{"model":"m1"}` {
			t.Fatalf("the intercept handler got path=%q body=%q (want the original request)", interceptedPath, interceptedBody)
		}
		envelope(t, serve(h, "GET", "/chat/broken", nil, nil), 500, api.CodeInternal)
		if up.count() != before {
			t.Fatal("a denied or intercepted request reached the upstream")
		}
		if rec := serve(h, "GET", "/chat/props", nil, nil); rec.Code != 200 || up.count() != before+1 {
			t.Fatalf("allowed path: %d", rec.Code)
		}
	})
	t.Run("deny without a code is a generic 403, and the page is for browsers", func(t *testing.T) {
		h := New(opts(&target, func(o *Options) {
			o.PathPolicy = func(*http.Request) (Decision, http.Handler, string) { return Deny, nil, "" }
		}))
		envelope(t, serve(h, "GET", "/x", nil, nil), 403, api.CodeInvalidRequest)
		rec := serve(h, "GET", "/x", map[string]string{"Accept": "text/html"}, nil)
		if rec.Code != 403 || !strings.Contains(rec.Header().Get("Content-Type"), "text/html") {
			t.Fatalf("%d %v", rec.Code, rec.Header())
		}
	})
	t.Run("method policy runs before path policy", func(t *testing.T) {
		called := false
		h := New(opts(&target, func(o *Options) {
			o.MethodPolicy = ReadOnly
			o.PathPolicy = func(*http.Request) (Decision, http.Handler, string) { called = true; return Allow, nil, "" }
		}))
		serve(h, "POST", "/x", nil, strings.NewReader("{}"))
		if called {
			t.Fatal("PathPolicy ran for a refused method")
		}
	})
}

func TestResponseRules(t *testing.T) {
	up := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Set-Cookie", "a=1; Path=/")
		w.Header().Add("Set-Cookie", "b=2")
		w.Header().Set("Content-Security-Policy", "default-src 'none'")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, "<html>upstream</html>")
	})
	target := *up.url()

	t.Run("defaults keep cookies and the upstream CSP, add nosniff", func(t *testing.T) {
		rec := serve(New(opts(&target)), "GET", "/", nil, nil)
		h := rec.Header()
		if len(h.Values("Set-Cookie")) != 2 {
			t.Fatalf("Set-Cookie = %v", h.Values("Set-Cookie"))
		}
		if h.Get("Content-Security-Policy") != "default-src 'none'" {
			t.Fatalf("upstream CSP replaced: %q", h.Get("Content-Security-Policy"))
		}
		if h.Get("X-Content-Type-Options") != "nosniff" {
			t.Fatalf("nosniff missing: %v", h)
		}
		if h.Get("X-Frame-Options") != "DENY" || h.Get("Referrer-Policy") != "" {
			t.Fatalf("frame headers touched without being asked: %v", h)
		}
	})
	t.Run("DropSetCookie and FrameHeaders", func(t *testing.T) {
		rec := serve(New(opts(&target, func(o *Options) { o.DropSetCookie = true; o.FrameHeaders = true })), "GET", "/", nil, nil)
		h := rec.Header()
		if len(h.Values("Set-Cookie")) != 0 {
			t.Fatalf("Set-Cookie = %v", h.Values("Set-Cookie"))
		}
		if h.Get("X-Frame-Options") != "SAMEORIGIN" || h.Get("Referrer-Policy") != "no-referrer" || h.Get("X-Content-Type-Options") != "nosniff" {
			t.Fatalf("headers: %v", h)
		}
		if h.Get("Content-Security-Policy") != "default-src 'none'" {
			t.Fatal("the upstream CSP must stay")
		}
		if rec.Body.String() != "<html>upstream</html>" {
			t.Fatalf("body = %q", rec.Body.String())
		}
	})
	t.Run("status and body pass through", func(t *testing.T) {
		u := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusTeapot)
			_, _ = io.WriteString(w, "short and stout")
		})
		t2 := *u.url()
		rec := serve(New(opts(&t2)), "GET", "/", nil, nil)
		if rec.Code != 418 || rec.Body.String() != "short and stout" {
			t.Fatalf("%d %q", rec.Code, rec.Body.String())
		}
	})
	t.Run("compressed answers pass through untouched", func(t *testing.T) {
		u := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Encoding", "gzip")
			_, _ = w.Write([]byte{0x1f, 0x8b, 0x08, 0x00})
		})
		t2 := *u.url()
		rec := serve(New(opts(&t2)), "GET", "/", map[string]string{"Accept-Encoding": "gzip"}, nil)
		if rec.Header().Get("Content-Encoding") != "gzip" || !bytes.Equal(rec.Body.Bytes(), []byte{0x1f, 0x8b, 0x08, 0x00}) {
			t.Fatalf("%v %v", rec.Header(), rec.Body.Bytes())
		}
		if got := u.last(t).Header.Get("Accept-Encoding"); got != "gzip" {
			t.Fatalf("Accept-Encoding = %q", got)
		}
	})
	t.Run("HEAD has no body", func(t *testing.T) {
		rec := serve(New(opts(&target)), "HEAD", "/", nil, nil)
		if rec.Code != 200 || rec.Body.Len() != 0 {
			t.Fatalf("%d %q", rec.Code, rec.Body.String())
		}
	})
}

func TestLocationRewrite(t *testing.T) {
	tests := []struct {
		name, prefix, loc, want string
	}{
		{"absolute path gets the prefix", "/chat", "/login", "/chat/login"},
		{"absolute path keeps query", "/chat", "/a?b=1", "/chat/a?b=1"},
		{"upstream absolute URL becomes a path", "/chat", "@UP@/login", "/chat/login"},
		{"relative stays", "/chat", "login", "login"},
		{"relative dot", "/chat", "./login", "./login"},
		{"other host untouched", "/chat", "https://example.test/x", "https://example.test/x"},
		{"protocol relative untouched", "/chat", "//example.test/x", "//example.test/x"},
		{"no prefix: path unchanged", "", "/library/x", "/library/x"},
		{"no prefix: upstream URL becomes a path", "", "@UP@/library/x", "/library/x"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var up *upstream
			up = newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Location", strings.ReplaceAll(tc.loc, "@UP@", up.srv.URL))
				w.WriteHeader(http.StatusFound)
			})
			target := *up.url()
			rec := serve(New(opts(&target, func(o *Options) { o.StripPrefix = tc.prefix })), "GET", tc.prefix+"/start", nil, nil)
			if rec.Code != 302 || rec.Header().Get("Location") != tc.want {
				t.Fatalf("Location = %q, want %q (status %d)", rec.Header().Get("Location"), tc.want, rec.Code)
			}
		})
	}
}

func TestUpstreamIsPinnedToLoopback(t *testing.T) {
	// The request names another host in every way a client can; the bytes still go to the target.
	up := newUpstream(t, nil)
	target := *up.url()
	h := New(opts(&target))
	r := httptest.NewRequest("GET", "http://203.0.113.9:80/where?x=1", nil)
	r.Host = "203.0.113.9"
	r.Header.Set("X-Forwarded-Host", "203.0.113.9")
	r.Header.Set("Forwarded", "host=203.0.113.9")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	got := up.last(t)
	if got.Path != "/where" || got.Host != strings.TrimPrefix(up.srv.URL, "http://") {
		t.Fatalf("%+v", got)
	}
}
