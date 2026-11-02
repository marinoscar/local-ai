package cli

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/api"
	"github.com/marinoscar/local-ai/agent/internal/cli/fake"
)

func asCLI(t *testing.T, err error) *CLIError {
	t.Helper()
	var ce *CLIError
	if !errors.As(err, &ce) {
		t.Fatalf("error %v (%T) is not a *CLIError", err, err)
	}
	return ce
}

func asAPI(t *testing.T, err error) *APIError {
	t.Helper()
	var ae *APIError
	if !errors.As(err, &ae) {
		t.Fatalf("error %v (%T) is not an *APIError", err, err)
	}
	return ae
}

// The transport matrix of aictl.md section 3.2 and 9.1: which transport carries the request, what
// is sent on it, and the error when neither works.
func TestTransportMatrix(t *testing.T) {
	cases := []struct {
		name     string
		opts     fake.Options
		dial     func(ctx context.Context, path string) error // nil: dial the real socket
		tokenBad bool
		wantVia  string // "" when the call must fail
		wantCode string
		wantExit int
		wantRead int // token file reads
	}{
		{name: "socket ok", wantVia: "socket", wantRead: 0},
		{name: "socket ENOENT falls back to TCP", opts: fake.Options{NoSocket: true}, wantVia: "tcp", wantRead: 1},
		{name: "socket ECONNREFUSED falls back to TCP", dial: func(context.Context, string) error { return dialErr(syscall.ECONNREFUSED) }, wantVia: "tcp", wantRead: 1},
		{name: "socket EACCES with a readable token falls back to TCP", dial: func(context.Context, string) error { return dialErr(syscall.EACCES) }, wantVia: "tcp", wantRead: 1},
		{name: "socket EACCES and unreadable token is exit 4", dial: func(context.Context, string) error { return dialErr(syscall.EACCES) }, tokenBad: true,
			wantCode: CodePermission, wantExit: ExitDenied, wantRead: 1},
		{name: "socket EACCES and TCP down reports the permission problem", opts: fake.Options{NoTCP: true}, dial: func(context.Context, string) error { return dialErr(syscall.EACCES) },
			wantCode: CodePermission, wantExit: ExitDenied, wantRead: 1},
		{name: "both down is exit 3", opts: fake.Options{NoSocket: true, NoTCP: true}, wantCode: CodeUnreachable, wantExit: ExitUnreachable, wantRead: 1},
		{name: "socket ENOENT and unreadable token is exit 3", opts: fake.Options{NoSocket: true}, tokenBad: true,
			wantCode: CodeUnreachable, wantExit: ExitUnreachable, wantRead: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, tc.opts)
			if tc.tokenBad {
				h.unreadable()
			}
			if tc.dial != nil {
				h.cfg.DialUnix = func(ctx context.Context, p string) (net.Conn, error) {
					if err := tc.dial(ctx, p); err != nil {
						return nil, err
					}
					return dialUnix(ctx, p)
				}
			}
			h.cfg.Verbose = true
			c := h.client(t)
			var out struct{ Mode string }
			_, err := c.Do(context.Background(), http.MethodGet, "/models", nil, &out)
			if tc.wantVia == "" {
				ce := asCLI(t, err)
				if ce.Code != tc.wantCode || ExitFor(err) != tc.wantExit {
					t.Fatalf("got %s exit %d, want %s exit %d", ce.Code, ExitFor(err), tc.wantCode, tc.wantExit)
				}
				if len(h.agent.Requests()) != 0 {
					t.Errorf("requests reached the agent: %+v", h.agent.Requests())
				}
			} else {
				// the agent answers /models with 404: the transport worked, the route is unknown
				ae := asAPI(t, err)
				if ae.Status != http.StatusNotFound {
					t.Fatalf("status = %d", ae.Status)
				}
				if got := c.Transport(); got != tc.wantVia {
					t.Fatalf("transport = %q, want %q", got, tc.wantVia)
				}
				if !strings.Contains(h.stderr.String(), "via "+tc.wantVia) {
					t.Errorf("-v did not print the transport: %q", h.stderr.String())
				}
				for _, r := range h.agent.Requests() {
					if r.Via != tc.wantVia {
						t.Errorf("request came via %s", r.Via)
					}
				}
			}
			if h.reads != tc.wantRead {
				t.Errorf("token file read %d times, want %d", h.reads, tc.wantRead)
			}
		})
	}
}

// The socket request carries no credentials; the TCP request carries the trimmed token and the
// agent's Host.
func TestTransportHeaders(t *testing.T) {
	t.Run("socket sends no token", func(t *testing.T) {
		h := newHarness(t, fake.Options{})
		c := h.client(t)
		if _, err := c.Health(context.Background()); err != nil {
			t.Fatal(err)
		}
		reqs := h.agent.Requests()
		if len(reqs) != 1 || reqs[0].Via != "socket" {
			t.Fatalf("requests = %+v", reqs)
		}
		if got := reqs[0].Header.Get("Authorization"); got != "" {
			t.Errorf("Authorization over the socket = %q", got)
		}
		if h.reads != 0 {
			t.Errorf("token file was read although the socket works")
		}
	})
	t.Run("tcp sends the trimmed token and Host", func(t *testing.T) {
		h := newHarness(t, fake.Options{NoSocket: true})
		c := h.client(t)
		if _, err := c.Health(context.Background()); err != nil {
			t.Fatal(err)
		}
		r := h.agent.Requests()[0]
		if r.Via != "tcp" {
			t.Fatalf("via = %s", r.Via)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+testToken {
			t.Errorf("Authorization = %q", got)
		}
		if want := strings.TrimPrefix(h.agent.URL, "http://"); r.Host != want {
			t.Errorf("Host = %q, want %q", r.Host, want)
		}
		if !strings.HasPrefix(r.Host, "127.0.0.1:") {
			t.Errorf("Host %q is not the loopback address", r.Host)
		}
	})
}

func TestRequestHeadersAndBodies(t *testing.T) {
	cases := []struct {
		name         string
		method       string
		in           any
		lang         Lang
		wantBody     string
		wantMutating bool
	}{
		{"GET has no body and no content type", http.MethodGet, nil, LangEN, "", false},
		{"POST without a body sends {}", http.MethodPost, nil, LangES, "{}", true},
		{"PUT sends the JSON", http.MethodPut, map[string]string{"model": "m1"}, LangEN, `{"model":"m1"}`, true},
		{"DELETE without a body sends {}", http.MethodDelete, nil, LangEN, "{}", true},
		{"raw JSON goes out unchanged", http.MethodPost, json.RawMessage(`{"wait":true}`), LangEN, `{"wait":true}`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, fake.Options{})
			h.cfg.Lang = tc.lang
			h.agent.Respond(tc.method, "/x", 200, `{}`)
			c := h.client(t)
			if _, err := c.Do(context.Background(), tc.method, "/x", tc.in, nil); err != nil {
				t.Fatal(err)
			}
			reqs := h.agent.Requests()
			r := reqs[len(reqs)-1]
			if r.Path != "/x" {
				t.Fatalf("path = %q", r.Path)
			}
			if r.Body != tc.wantBody {
				t.Errorf("body = %q, want %q", r.Body, tc.wantBody)
			}
			ct := r.Header.Get("Content-Type")
			if tc.wantMutating && ct != "application/json" {
				t.Errorf("Content-Type = %q", ct)
			}
			if !tc.wantMutating && ct != "" {
				t.Errorf("Content-Type on a read = %q", ct)
			}
			if got := r.Header.Get("Accept-Language"); got != string(tc.lang) {
				t.Errorf("Accept-Language = %q, want %q", got, tc.lang)
			}
		})
	}
}

// Every row of the section 3.4 table, from a fake agent returning that status.
func TestExitCodeMap(t *testing.T) {
	cases := []struct {
		status int
		code   string
		exit   int
	}{
		{http.StatusBadRequest, api.CodeInvalidRequest, ExitUsage},
		{http.StatusRequestEntityTooLarge, api.CodeTooLarge, ExitUsage},
		{http.StatusUnsupportedMediaType, api.CodeInvalidRequest, ExitUsage},
		{http.StatusMethodNotAllowed, api.CodeInvalidRequest, ExitUsage},
		{http.StatusUnauthorized, api.CodeUnauthenticated, ExitDenied},
		{http.StatusForbidden, api.CodeAdminRequired, ExitDenied},
		{http.StatusNotFound, api.CodeNotFound, ExitNotFound},
		{http.StatusConflict, api.CodeBusy, ExitRefused},
		{http.StatusUnprocessableEntity, api.CodeNotInMode, ExitRefused},
		{http.StatusTooManyRequests, api.CodeBusy, ExitRefused},
		// A PIN lockout is "PIN locked" (exit 4), not the generic rate limit (aictl.md section 3.4).
		{http.StatusTooManyRequests, api.CodeRateLimited, ExitDenied},
		{http.StatusInternalServerError, api.CodeInternal, ExitFailed},
		{http.StatusBadGateway, api.CodeRouterDown, ExitFailed},
		{http.StatusServiceUnavailable, api.CodeInternal, ExitUnreachable}, // a 503 that is not "starting": no retry
		{http.StatusGatewayTimeout, api.CodeUpstreamTimeout, ExitTimeout},
	}
	for _, tc := range cases {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			h := newHarness(t, fake.Options{})
			h.agent.RespondError(http.MethodGet, "/things", tc.status, tc.code, "Message.", "Fix it.", map[string]any{"k": "v"})
			c := h.client(t)
			_, err := c.Do(context.Background(), http.MethodGet, "/things", nil, nil)
			ae := asAPI(t, err)
			if ae.Status != tc.status || ae.Code != tc.code || ae.Message != "Message." || ae.Fix != "Fix it." ||
				ae.Details["k"] != "v" || ae.RequestID != "req_ABCDEFGHJKLM" {
				t.Fatalf("decoded error = %+v", ae)
			}
			if got := ExitFor(err); got != tc.exit {
				t.Errorf("exit = %d, want %d", got, tc.exit)
			}
			if h.sleeps.count() != 0 {
				t.Errorf("a %d was retried", tc.status)
			}
		})
	}
}

func TestClientDeadline(t *testing.T) {
	h := newHarness(t, fake.Options{})
	h.agent.Handle(http.MethodGet, "/slow", func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(2 * time.Second):
		case <-r.Context().Done():
		}
	})
	c := h.client(t)
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	_, err := c.Do(ctx, http.MethodGet, "/slow", nil, nil)
	ce := asCLI(t, err)
	if ce.Code != CodeTimeout || ExitFor(err) != ExitTimeout {
		t.Fatalf("got %s exit %d", ce.Code, ExitFor(err))
	}
}

func TestClientCancelled(t *testing.T) {
	h := newHarness(t, fake.Options{})
	h.agent.Handle(http.MethodGet, "/slow", func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() })
	c := h.client(t)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(100 * time.Millisecond); cancel() }()
	_, err := c.Do(ctx, http.MethodGet, "/slow", nil, nil)
	if ce := asCLI(t, err); ce.Code != CodeInterrupted || ExitFor(err) != ExitInterrupted {
		t.Fatalf("got %s exit %d", ce.Code, ExitFor(err))
	}
}

func startingHandler(failures int, calls *atomic.Int32) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if int(calls.Add(1)) <= failures {
			w.Header().Set("Retry-After", "5")
			fake.WriteEnvelope(w, http.StatusServiceUnavailable, api.CodeStarting, "Starting.", "Wait.", nil)
			return
		}
		json.NewEncoder(w).Encode(api.Health{OK: true, Version: "0.0.0-dev", Mode: "online"})
	}
}

func TestRetryWhileStarting(t *testing.T) {
	cases := []struct {
		name       string
		failures   int
		wantErr    bool
		wantSleeps int
		wantCalls  int
	}{
		{"succeeds on the first try", 0, false, 0, 1},
		{"succeeds within the window", 3, false, 3, 4},
		{"succeeds on the last retry", 10, false, 10, 11},
		{"still starting after 10 s is exit 3", 11, true, 10, 11},
		{"never up is exit 3", 1000, true, 10, 11},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, fake.Options{})
			var calls atomic.Int32
			h.agent.Handle(http.MethodGet, "/health", startingHandler(tc.failures, &calls))
			c := h.client(t)
			_, err := c.Health(context.Background())
			if tc.wantErr {
				ce := asCLI(t, err)
				if ce.Code != CodeUnreachable || ExitFor(err) != ExitUnreachable || ce.RequestID == "" {
					t.Fatalf("got %+v exit %d", ce, ExitFor(err))
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if h.sleeps.count() != tc.wantSleeps || int(calls.Load()) != tc.wantCalls {
				t.Errorf("sleeps %d calls %d, want %d and %d", h.sleeps.count(), calls.Load(), tc.wantSleeps, tc.wantCalls)
			}
			for _, d := range h.sleeps.d {
				if d != time.Second {
					t.Errorf("retry interval = %v, want 1s", d)
				}
			}
		})
	}
}

func TestRetryStopsWhenContextEnds(t *testing.T) {
	h := newHarness(t, fake.Options{})
	var calls atomic.Int32
	h.agent.Handle(http.MethodGet, "/health", startingHandler(1000, &calls))
	ctx, cancel := context.WithCancel(context.Background())
	h.cfg.Sleep = func(ctx context.Context, d time.Duration) error { cancel(); return ctx.Err() }
	c := h.client(t)
	_, err := c.Health(ctx)
	if ce := asCLI(t, err); ce.Code != CodeInterrupted {
		t.Fatalf("got %s", ce.Code)
	}
	if calls.Load() != 1 {
		t.Errorf("calls = %d", calls.Load())
	}
}

// After the agent has answered once, a lost connection (it is restarting) is retried like a 503.
func TestRetryAfterAgentRestart(t *testing.T) {
	cases := []struct {
		name       string
		failDials  int
		wantErr    bool
		wantSleeps int
	}{
		{"back after two failures", 2, false, 2},
		{"down for good is exit 3", 1000, true, 10},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, fake.Options{})
			h.agent.Respond(http.MethodGet, "/ping", 200, `{}`)
			var down atomic.Bool
			var failed atomic.Int32
			h.cfg.DialUnix = func(ctx context.Context, p string) (net.Conn, error) {
				if down.Load() && int(failed.Add(1)) <= tc.failDials {
					return nil, dialErr(syscall.ENOENT)
				}
				return dialUnix(ctx, p)
			}
			h.cfg.URL = "http://127.0.0.1:1" // TCP is not available either
			c := h.client(t)
			if _, err := c.Do(context.Background(), http.MethodGet, "/ping", nil, nil); err != nil {
				t.Fatal(err)
			}
			down.Store(true)
			_, err := c.Do(context.Background(), http.MethodGet, "/ping", nil, nil)
			if tc.wantErr {
				if ce := asCLI(t, err); ce.Code != CodeUnreachable || ExitFor(err) != ExitUnreachable {
					t.Fatalf("got %s", ce.Code)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if h.sleeps.count() != tc.wantSleeps {
				t.Errorf("sleeps = %d, want %d", h.sleeps.count(), tc.wantSleeps)
			}
		})
	}
}

func TestFirstContactIsNotRetried(t *testing.T) {
	h := newHarness(t, fake.Options{NoSocket: true, NoTCP: true})
	c := h.client(t)
	_, err := c.Health(context.Background())
	if ce := asCLI(t, err); ce.Code != CodeUnreachable {
		t.Fatalf("got %s", ce.Code)
	}
	if h.sleeps.count() != 0 {
		t.Errorf("an agent that was never reached was retried %d times", h.sleeps.count())
	}
}

func TestVersionSkew(t *testing.T) {
	cases := []struct {
		name      string
		agent     string
		wantWarn  bool
		healthRaw string
	}{
		{name: "same version is silent", agent: "0.0.0-dev"},
		{name: "different version warns once and goes on", agent: "9.9.9", wantWarn: true},
		{name: "unknown fields are ignored", agent: "0.0.0-dev",
			healthRaw: `{"ok":true,"version":"0.0.0-dev","mode":"online","starting":false,"future_field":{"a":1}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, fake.Options{Version: tc.agent})
			if tc.healthRaw != "" {
				h.agent.Respond(http.MethodGet, "/health", 200, tc.healthRaw)
			}
			h.agent.Respond(http.MethodGet, "/a", 200, `{}`)
			h.agent.Respond(http.MethodGet, "/b", 200, `{}`)
			c := h.client(t)
			for _, p := range []string{"/a", "/b"} {
				if _, err := c.Do(context.Background(), http.MethodGet, p, nil, nil); err != nil {
					t.Fatalf("%s: %v", p, err)
				}
			}
			if n := h.agent.Count("/health"); n != 1 {
				t.Errorf("/health requested %d times, want once (cached)", n)
			}
			if tc.wantWarn {
				if len(h.warns) != 1 || h.warns[0].Code != CodeVersionSkew {
					t.Fatalf("warnings = %+v", h.warns)
				}
				if h.warns[0].Details["agent_version"] != "9.9.9" || h.warns[0].Details["cli_version"] != "0.0.0-dev" {
					t.Errorf("details = %v", h.warns[0].Details)
				}
			} else if len(h.warns) != 0 {
				t.Errorf("unexpected warnings %+v", h.warns)
			}
		})
	}
}

func TestUndecodableAnswers(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		call   func(c *Client) error
	}{
		{"health that is not JSON", 200, "<html>proxy</html>", func(c *Client) error {
			_, err := c.Health(context.Background())
			return err
		}},
		{"2xx that is not JSON", 200, "not json", func(c *Client) error {
			var v struct{ A int }
			_, err := c.Do(context.Background(), http.MethodGet, "/x", nil, &v)
			return err
		}},
		{"2xx with the wrong type", 200, `{"A":"text"}`, func(c *Client) error {
			var v struct{ A int }
			_, err := c.Do(context.Background(), http.MethodGet, "/x", nil, &v)
			return err
		}},
		{"error that is not an envelope", 500, "oops", func(c *Client) error {
			_, err := c.Do(context.Background(), http.MethodGet, "/x", nil, nil)
			return err
		}},
		{"error envelope without a code", 404, `{"error":{"message":"m"}}`, func(c *Client) error {
			_, err := c.Do(context.Background(), http.MethodGet, "/x", nil, nil)
			return err
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, fake.Options{})
			h.agent.Respond(http.MethodGet, "/x", tc.status, tc.body)
			if strings.HasPrefix(tc.name, "health") {
				h.agent.Respond(http.MethodGet, "/health", tc.status, tc.body)
			}
			c := h.client(t)
			err := tc.call(c)
			ce := asCLI(t, err)
			if ce.Code != CodeBadResponse || ExitFor(err) != ExitFailed {
				t.Fatalf("got %s exit %d", ce.Code, ExitFor(err))
			}
			if ce.RequestID != "req_ABCDEFGHJKLM" {
				t.Errorf("request id = %q", ce.RequestID)
			}
		})
	}
}

func TestRawJSONPassthrough(t *testing.T) {
	h := newHarness(t, fake.Options{})
	h.agent.Respond(http.MethodGet, "/models", 200, `{"models":[{"id":"m1"}],"extra":true}`)
	c := h.client(t)
	var raw json.RawMessage
	r, err := c.Do(context.Background(), http.MethodGet, "/models", nil, &raw)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"models":[{"id":"m1"}],"extra":true}` || string(r.Body) != string(raw) {
		t.Errorf("raw = %s", raw)
	}
}

func TestLoopbackURLOnly(t *testing.T) {
	cases := []struct {
		url string
		ok  bool
	}{
		{"http://127.0.0.1:7070", true},
		{"http://localhost:7070", true},
		{"http://[::1]:7070", true},
		{"http://127.0.0.1:7070/", true},
		{"http://example.test:7070", false},
		{"http://10.0.0.5:7070", false},
		{"http://127.0.0.1.example.test:7070", false},
		{"http://localhost.example.test:7070", false},
		{"https://127.0.0.1:7070", false},
		{"http://user:secret@127.0.0.1:7070", false},
		{"http://127.0.0.1:7070/api", false},
		{"127.0.0.1:7070", false},
		{"http://0.0.0.0:7070", false},
		{"", true}, // empty means the default
	}
	for _, tc := range cases {
		t.Run(tc.url, func(t *testing.T) {
			dials := 0
			_, err := NewClient(Config{URL: tc.url, DialUnix: func(context.Context, string) (net.Conn, error) {
				dials++
				return nil, dialErr(syscall.ENOENT)
			}})
			if tc.ok {
				if err != nil {
					t.Fatalf("refused: %v", err)
				}
				return
			}
			ce := asCLI(t, err)
			if ce.Code != CodeUsage || ExitFor(err) != ExitUsage {
				t.Fatalf("got %s exit %d", ce.Code, ExitFor(err))
			}
			if dials != 0 {
				t.Errorf("a connection was attempted")
			}
		})
	}
}

// A bad --url is refused even when the socket would work, and nothing reaches the agent.
func TestBadURLMakesNoRequest(t *testing.T) {
	h := newHarness(t, fake.Options{})
	h.cfg.URL = "http://example.test:7070"
	if _, err := NewClient(h.cfg); ExitFor(err) != ExitUsage {
		t.Fatalf("err = %v", err)
	}
	if n := len(h.agent.Requests()); n != 0 {
		t.Errorf("%d requests reached the agent", n)
	}
}

func TestTCPClientIgnoresProxyEnvironment(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	t.Setenv("http_proxy", "http://127.0.0.1:1")
	h := newHarness(t, fake.Options{NoSocket: true})
	c := h.client(t)
	if _, err := c.Health(context.Background()); err != nil {
		t.Fatalf("the proxy environment was used: %v", err)
	}
	tr := c.tcp.Transport.(*http.Transport)
	if tr.Proxy != nil {
		t.Error("TCP transport has a proxy function")
	}
}

func TestStream(t *testing.T) {
	h := newHarness(t, fake.Options{})
	h.agent.Handle(http.MethodGet, "/events", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") != "text/event-stream" {
			t.Errorf("Accept = %q", r.Header.Get("Accept"))
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("data: {}\n\n"))
	})
	h.agent.RespondError(http.MethodGet, "/logs", 404, api.CodeNotFound, "Nope.", "", nil)
	c := h.client(t)
	resp, err := c.Stream(context.Background(), http.MethodGet, "/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	_, err = c.Stream(context.Background(), http.MethodGet, "/logs", nil)
	if ae := asAPI(t, err); ae.Status != 404 {
		t.Fatalf("status = %d", ae.Status)
	}
}

// LOCALAI_CLI_DEBUG logs requests and responses; secrets never appear in the log.
func TestDebugLogScrubsSecrets(t *testing.T) {
	h := newHarness(t, fake.Options{NoSocket: true})
	h.cfg.Debug = true
	hexKey := strings.Repeat("0123456789abcdef", 4) // built at run time: the redaction check forbids 64-hex literals
	h.agent.Respond(http.MethodPost, "/key/x", 200, `{"key":"`+hexKey+`","ok":true}`)
	c := h.client(t)
	if _, err := c.Do(context.Background(), http.MethodPost, "/key/x", map[string]string{"token": "lat_" + "supersecretvalue123456"}, nil); err != nil {
		t.Fatal(err)
	}
	log := h.stderr.String()
	for _, want := range []string{`"path":"/api/v1/key/x"`, `"status":200`, `"method":"POST"`} {
		if !strings.Contains(log, want) {
			t.Errorf("log lacks %s:\n%s", want, log)
		}
	}
	for _, secret := range []string{testToken, "supersecretvalue", hexKey[:32]} {
		if strings.Contains(log, secret) {
			t.Errorf("log leaks %q:\n%s", secret, log)
		}
	}
}
