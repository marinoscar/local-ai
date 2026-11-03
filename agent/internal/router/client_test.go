package router

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/router/fake"
)

// logBuf is a goroutine-safe log sink.
type logBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *logBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *logBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func (l *logBuf) logger() *slog.Logger {
	return slog.New(slog.NewTextHandler(l, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

// rig is a fake router, a client on it and everything a leak scan needs.
type rig struct {
	fake   *fake.Router
	client *Client
	bearer string
	logs   *logBuf
}

func newRig(t *testing.T, mutate ...func(*Options)) *rig {
	t.Helper()
	bearer := secret(1)
	f := fake.New(bearer)
	t.Cleanup(f.Close)
	logs := &logBuf{}
	o := Options{BaseURL: f.URL(), Keys: StaticKey(bearer), Timeout: time.Second, Logger: logs.logger()}
	for _, m := range mutate {
		if m != nil {
			m(&o)
		}
	}
	c, err := NewClient(o)
	if err != nil {
		t.Fatal(err)
	}
	f.Add(fake.Model{ID: "model-a-q4km", Aliases: []string{"model-a"}, Tags: []string{"chat"}, ContextLength: 8192})
	f.Add(fake.Model{ID: "model-b-q4km", Tags: []string{"coder"}})
	return &rig{fake: f, client: c, bearer: bearer, logs: logs}
}

// noLeak fails when the key shows up in the logs or in any of the given errors.
func (r *rig) noLeak(t *testing.T, errs ...error) {
	t.Helper()
	if strings.Contains(r.logs.String(), r.bearer) {
		t.Errorf("the key is in the logs:\n%s", r.logs.String())
	}
	for _, e := range errs {
		if e != nil && strings.Contains(e.Error(), r.bearer) {
			t.Errorf("the key is in an error: %v", e)
		}
	}
}

// onlyAllowedCalls asserts the contract of llama-router.md section 3.6: nothing but the listed
// routes, never POST /models, DELETE /models or autoload.
func onlyAllowedCalls(t *testing.T, f *fake.Router) {
	t.Helper()
	ok := map[string]bool{
		"GET /health": true, "GET /models": true, "POST /models/load": true, "POST /models/unload": true,
		"GET /models/sse": true, "GET /props": true, "POST /v1/chat/completions": true, "GET /v1/models": true,
	}
	for _, q := range f.Requests() {
		if !ok[q.Method+" "+q.Path] {
			t.Errorf("forbidden call reached the router: %s", q)
		}
		if strings.Contains(q.RawQuery, "autoload") {
			t.Errorf("autoload sent: %s", q)
		}
		if q.Path != "/health" && !q.HasAuth {
			t.Errorf("call without a key: %s", q)
		}
	}
}

func TestHealthSendsNoKey(t *testing.T) {
	r := newRig(t)
	if err := r.client.Health(context.Background()); err != nil {
		t.Fatal(err)
	}
	reqs := r.fake.Requests()
	if len(reqs) != 1 || reqs[0].Path != "/health" || reqs[0].HasAuth {
		t.Fatalf("health must be one call without Authorization: %v", reqs)
	}
	// A client with no key source can still ask for health.
	c, err := NewClient(Options{BaseURL: r.fake.URL()})
	if err != nil || c.Health(context.Background()) != nil {
		t.Errorf("health without a key source: %v", err)
	}
}

func TestHealthStates(t *testing.T) {
	r := newRig(t)
	r.fake.Down(true)
	err := r.client.Health(context.Background())
	var he *HTTPError
	if !errors.Is(err, ErrDown) || !errors.As(err, &he) || he.Status != 503 || he.Message != "Loading model" {
		t.Errorf("503 health = %v", err)
	}
	if CodeOf(err) != CodeRouterDown {
		t.Errorf("CodeOf = %q", CodeOf(err))
	}
	r.fake.Down(false)
	r.fake.Force(500, `{"error":{"message":"x"}}`) // health is public: Force must not reach it
	if err := r.client.Health(context.Background()); err != nil {
		t.Errorf("health after recovery: %v", err)
	}
}

func TestModelsAndAuth(t *testing.T) {
	r := newRig(t)
	ms, err := r.client.Models(context.Background())
	if err != nil || len(ms) != 2 || ms[0].ID != "model-a-q4km" || ms[0].Aliases[0] != "model-a" || ms[0].ContextLength != 8192 {
		t.Fatalf("models: %+v, %v", ms, err)
	}
	if reqs := r.fake.Requests(); !reqs[0].KeyOK || reqs[0].Method != "GET" || reqs[0].Path != "/models" || reqs[0].RawQuery != "" {
		t.Errorf("request = %+v", reqs[0])
	}

	r.fake.SetKey(secret(2))
	_, err = r.client.Models(context.Background())
	if !errors.Is(err, ErrUnauthorized) || CodeOf(err) != CodeKeyRejected {
		t.Fatalf("wrong key: %v", err)
	}
	// A static key cannot be refreshed: exactly one extra request, no retry.
	if n := r.fake.Calls("GET", "/models"); n != 2 {
		t.Errorf("GET /models called %d times, want 2 (one good, one rejected)", n)
	}
	r.noLeak(t, err)
}

func TestKeyRotationIsAbsorbed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "api-key")
	m0 := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	writeKey(t, path, secret(1), m0)
	r := newRig(t, func(o *Options) { o.Keys = NewFileKey(nil, path) })
	if _, err := r.client.Models(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Rotate: new key in the router, new key file with the SAME mtime and size, so the cheap check
	// cannot see it. The 401 makes the client re-read once and retry.
	r.fake.SetKey(secret(2))
	writeKey(t, path, secret(2), m0)
	before := r.fake.Calls("GET", "/models")
	if _, err := r.client.Models(context.Background()); err != nil {
		t.Fatalf("a rotation must be absorbed by one retry: %v", err)
	}
	if n := r.fake.Calls("GET", "/models") - before; n != 2 {
		t.Errorf("rotation took %d requests, want 2 (rejected, then retried)", n)
	}
	if !strings.Contains(r.logs.String(), "retrying with the new key") {
		t.Errorf("the retry was not logged:\n%s", r.logs.String())
	}
	if !strings.Contains(r.logs.String(), Fingerprint(secret(2))) {
		t.Errorf("the log should carry the fingerprint of the new key")
	}
	r.noLeak(t)

	// The router restarted with a key the file does not have: unchanged file, no retry, 401.
	r.fake.SetKey(secret(3))
	before = r.fake.Calls("GET", "/models")
	_, err := r.client.Models(context.Background())
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("got %v, want ErrUnauthorized", err)
	}
	if n := r.fake.Calls("GET", "/models") - before; n != 1 {
		t.Errorf("an unchanged key file must not be retried: %d requests", n)
	}
	r.noLeak(t, err)
}

func TestKeyRotatedInvalidatesTheCache(t *testing.T) {
	path := filepath.Join(t.TempDir(), "api-key")
	m0 := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	writeKey(t, path, secret(1), m0)
	fk := NewFileKey(nil, path)
	r := newRig(t, func(o *Options) { o.Keys = fk })
	if _, err := r.client.Models(context.Background()); err != nil {
		t.Fatal(err)
	}
	writeKey(t, path, secret(2), m0)
	r.client.KeyRotated()
	if k, _ := fk.Key(); k != secret(2) {
		t.Errorf("KeyRotated must make the next Key read the file, got a stale key")
	}
	// A source without Invalidate is left alone.
	NewClientForTest(t, StaticKey(secret(1))).KeyRotated()
}

// NewClientForTest builds a client on a dummy loopback address.
func NewClientForTest(t *testing.T, keys KeySource) *Client {
	t.Helper()
	c, err := NewClient(Options{Keys: keys})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestNoKeyAvailable(t *testing.T) {
	r := newRig(t, func(o *Options) { o.Keys = NewFileKey(nil, filepath.Join(t.TempDir(), "missing")) })
	if _, err := r.client.Models(context.Background()); !errors.Is(err, ErrKeyUnavailable) {
		t.Errorf("got %v, want ErrKeyUnavailable", err)
	}
	r2 := newRig(t, func(o *Options) { o.Keys = nil })
	if _, err := r2.client.Models(context.Background()); !errors.Is(err, ErrKeyUnavailable) {
		t.Errorf("no key source: %v", err)
	}
	if n := len(r.fake.Requests()) + len(r2.fake.Requests()); n != 0 {
		t.Errorf("nothing may be sent without a key, %d requests were", n)
	}
}

func TestLoadUnload(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	if err := r.client.Load(ctx, "model-a-q4km"); err != nil {
		t.Fatal(err)
	}
	reqs := r.fake.Requests()
	last := reqs[len(reqs)-1]
	var body map[string]string
	if last.Method != "POST" || last.Path != "/models/load" || json.Unmarshal([]byte(last.Body), &body) != nil || body["model"] != "model-a-q4km" || len(body) != 1 {
		t.Errorf("load request = %+v", last)
	}
	if st := r.fake.Status("model-a-q4km"); st != fake.Loaded {
		t.Errorf("status after load = %q", st)
	}

	// The router refuses a load of a running model, and so does the client's error.
	err := r.client.Load(ctx, "model-a-q4km")
	var he *HTTPError
	if !errors.As(err, &he) || he.Status != 400 || he.Message != "model is already running" || errors.Is(err, ErrDown) {
		t.Errorf("load of a running model: %v", err)
	}
	if err := r.client.Unload(ctx, "model-a-q4km"); err != nil {
		t.Fatal(err)
	}
	if last := r.fake.Requests(); last[len(last)-1].Path != "/models/unload" {
		t.Errorf("unload went to %s", last[len(last)-1])
	}
	err = r.client.Unload(ctx, "model-a-q4km")
	if !errors.As(err, &he) || he.Message != "model is not running" {
		t.Errorf("unload of a stopped model: %v", err)
	}
	if err := r.client.Load(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("load of an unknown model: %v", err)
	}
	n := len(r.fake.Requests())
	for _, bad := range []string{"", "a\nb", strings.Repeat("a", 300)} {
		if err := r.client.Load(ctx, bad); !errors.Is(err, ErrBadModelID) {
			t.Errorf("Load(%q) = %v", bad, err)
		}
		if err := r.client.Unload(ctx, bad); !errors.Is(err, ErrBadModelID) {
			t.Errorf("Unload(%q) = %v", bad, err)
		}
	}
	if len(r.fake.Requests()) != n {
		t.Error("an invalid id must not reach the router")
	}
	r.noLeak(t)
}

func TestLoadAnswerWithoutSuccess(t *testing.T) {
	r := newRig(t)
	for name, body := range map[string]string{"empty object": `{}`, "not json": `ok`, "false": `{"success":false}`} {
		r.fake.Force(200, body)
		err := r.client.Load(context.Background(), "model-a-q4km")
		if err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

func TestChatAsksForTheTokensTheCallerChose(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	r.fake.Set("model-a-q4km", fake.Loaded)
	for _, tc := range []struct{ ask, want int }{{16, 16}, {1, 1}, {0, 1}, {-3, 1}} {
		if err := r.client.Chat(ctx, "model-a-q4km", tc.ask); err != nil {
			t.Fatalf("Chat(%d): %v", tc.ask, err)
		}
		var chat map[string]any
		for _, q := range r.fake.Requests() {
			if q.Path == "/v1/chat/completions" {
				chat = nil
				_ = json.Unmarshal([]byte(q.Body), &chat)
			}
		}
		if chat["max_tokens"] != float64(tc.want) || chat["stream"] != false {
			t.Errorf("Chat(%d) sent %v, want max_tokens %d", tc.ask, chat, tc.want)
		}
	}
	if err := r.client.Chat(ctx, "", 16); !errors.Is(err, ErrBadModelID) {
		t.Errorf("an empty model id = %v", err)
	}
}

func TestReload(t *testing.T) {
	r := newRig(t)
	if err := r.client.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	reqs := r.fake.Requests()
	if reqs[0].Path != "/models" || reqs[0].RawQuery != "reload=1" || r.fake.Reloads() != 1 {
		t.Errorf("reload request = %+v (reloads %d)", reqs[0], r.fake.Reloads())
	}
}

func TestPropsAndChatSmoke(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	if _, err := r.client.Props(ctx, "model-a-q4km"); err == nil {
		t.Error("props of an unloaded model must fail")
	}
	if err := r.client.ChatSmoke(ctx, "model-a-q4km"); err == nil {
		t.Error("a chat smoke on an unloaded model must fail")
	}
	r.fake.Set("model-a-q4km", fake.Loaded)
	p, err := r.client.Props(ctx, "model-a-q4km")
	if err != nil || p.NCtx != 4096 || p.BuildInfo == "" || p.Sleeping {
		t.Errorf("props = %+v, %v", p, err)
	}
	if err := r.client.ChatSmoke(ctx, "model-a-q4km"); err != nil {
		t.Fatal(err)
	}
	var chat map[string]any
	for _, q := range r.fake.Requests() {
		if q.Path == "/v1/chat/completions" {
			chat = nil
			_ = json.Unmarshal([]byte(q.Body), &chat)
		}
		if q.Path == "/props" && q.RawQuery != "model=model-a-q4km" {
			t.Errorf("props query = %q", q.RawQuery)
		}
	}
	if chat["max_tokens"] != float64(8) || chat["model"] != "model-a-q4km" || chat["stream"] != false {
		t.Errorf("chat body = %v", chat)
	}
	if _, err := r.client.Props(ctx, ""); !errors.Is(err, ErrBadModelID) {
		t.Errorf("props with an empty id: %v", err)
	}
	// An id that needs escaping reaches the router escaped.
	r.fake.Add(fake.Model{ID: "org/m:Q4 K&x=1", Status: fake.Loaded})
	if _, err := r.client.Props(ctx, "org/m:Q4 K&x=1"); err != nil {
		t.Errorf("props of an id that needs escaping: %v", err)
	}
	onlyAllowedCalls(t, r.fake)
	r.noLeak(t)
}

func TestFailureModes(t *testing.T) {
	cases := []struct {
		name   string
		script func(*rig)
		opts   func(*Options)
		want   error
	}{
		{"503 from the router", func(r *rig) { r.fake.Down(true) }, nil, ErrDown},
		{"connection dropped", func(r *rig) { r.fake.Reset(true) }, nil, ErrDown},
		{"slow answer", func(r *rig) { r.fake.Delay(300 * time.Millisecond) }, func(o *Options) { o.Timeout = 40 * time.Millisecond }, ErrDown},
		{"forced 502", func(r *rig) { r.fake.Force(502, `{"error":{"message":"bad gateway"}}`) }, nil, ErrDown},
		{"forced 504", func(r *rig) { r.fake.Force(504, ``) }, nil, ErrDown},
		{"redirect", func(r *rig) { r.fake.Redirect(true) }, nil, ErrRedirect},
		{"body is not json", func(r *rig) { r.fake.Malformed("<html>") }, nil, ErrMalformed},
		{"entry without id", func(r *rig) { r.fake.Malformed(`{"data":[{"status":{"value":"loaded"}}]}`) }, nil, ErrMalformed},
		{"truncated body", func(r *rig) { r.fake.Malformed(`{"data":[{"id":"a","stat`) }, nil, ErrMalformed},
		{"oversized body", func(r *rig) { r.fake.Malformed(`{"data":[],"pad":"` + strings.Repeat("x", maxModelsBody) + `"}`) }, nil, ErrMalformed},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t, c.opts)
			c.script(r)
			start := time.Now()
			_, err := r.client.Models(context.Background())
			if !errors.Is(err, c.want) {
				t.Fatalf("got %v, want %v", err, c.want)
			}
			if time.Since(start) > 2*time.Second {
				t.Errorf("the call took %s: the timeout did not bind", time.Since(start))
			}
			if errors.Is(c.want, ErrDown) && CodeOf(err) != CodeRouterDown {
				t.Errorf("CodeOf = %q", CodeOf(err))
			}
			r.noLeak(t, err)
		})
	}
}

func TestRedirectIsNotFollowed(t *testing.T) {
	r := newRig(t)
	r.fake.Redirect(true)
	if _, err := r.client.Models(context.Background()); !errors.Is(err, ErrRedirect) {
		t.Fatal(err)
	}
	if n := len(r.fake.Requests()); n != 1 {
		t.Errorf("%d requests: a redirect must be reported, never followed", n)
	}
}

func TestConnectionRefused(t *testing.T) {
	r := newRig(t)
	r.fake.Close()
	_, err := r.client.Models(context.Background())
	if !errors.Is(err, ErrDown) {
		t.Errorf("got %v, want ErrDown", err)
	}
	if err := r.client.Health(context.Background()); !errors.Is(err, ErrDown) {
		t.Errorf("health: %v", err)
	}
	r.noLeak(t, err)
}

func TestCallerCancelIsNotRouterDown(t *testing.T) {
	r := newRig(t)
	r.fake.Delay(time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(30 * time.Millisecond); cancel() }()
	_, err := r.client.Models(ctx)
	if !errors.Is(err, context.Canceled) || errors.Is(err, ErrDown) {
		t.Errorf("got %v, want context.Canceled and not ErrDown", err)
	}
}

func TestErrorTextNeverCarriesTheKey(t *testing.T) {
	r := newRig(t)
	// A misbehaving server that echoes the credential in its error message.
	r.fake.Force(500, `{"error":{"message":"bad credential `+r.bearer+` and Bearer abc.def-ghi","type":"server_error"}}`)
	_, err := r.client.Models(context.Background())
	var he *HTTPError
	if !errors.As(err, &he) || he.Status != 500 {
		t.Fatalf("got %v", err)
	}
	r.noLeak(t, err)
	if strings.Contains(err.Error(), "abc.def-ghi") {
		t.Errorf("a bearer token survived in %q", err)
	}
	// Long messages are cut.
	r.fake.Force(500, `{"error":{"message":"`+strings.Repeat("z", 1000)+`"}}`)
	_, err = r.client.Models(context.Background())
	if !errors.As(err, &he) || len(he.Message) > 200 {
		t.Errorf("message not cut: %d", len(he.Message))
	}
}

func TestProgressStream(t *testing.T) {
	r := newRig(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, err := r.client.Progress(ctx)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return r.fake.Streams() == 1 })
	r.fake.Emit("model-a-q4km", "status_change", map[string]any{
		"status":   "loading",
		"progress": map[string]any{"stages": []string{"text_model"}, "current": "text_model", "value": 0.5},
	})
	r.fake.EmitRaw(": ping\n\n")
	r.fake.EmitRaw("data: {broken\n\n")
	r.fake.Emit("model-a-q4km", "status_change", map[string]any{"status": "loaded"})

	p := recv(t, ch)
	if p.Model != "model-a-q4km" || p.Status != StatusLoading || !p.HasValue || p.Value != 0.5 {
		t.Errorf("first event = %+v", p)
	}
	if p = recv(t, ch); p.Status != StatusLoaded {
		t.Errorf("second event = %+v (the ping and the broken frame must be skipped)", p)
	}
	reqs := r.fake.Requests()
	if !reqs[0].KeyOK || reqs[0].Path != "/models/sse" {
		t.Errorf("sse request = %+v", reqs[0])
	}

	// The server ends the stream: the channel closes.
	r.fake.DropStreams()
	waitClosed(t, ch)

	// Cancelling the context also closes a stream.
	ch, err = r.client.Progress(ctx)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return r.fake.Streams() == 1 })
	cancel()
	waitClosed(t, ch)
	r.noLeak(t)
}

func TestProgressErrors(t *testing.T) {
	r := newRig(t)
	r.fake.SSEStatus(404)
	if _, err := r.client.Progress(context.Background()); !errors.Is(err, ErrNotFound) {
		t.Errorf("404 stream: %v", err)
	}
	r.fake.SSEStatus(0)
	r.fake.SetKey(secret(5))
	if _, err := r.client.Progress(context.Background()); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("401 stream: %v", err)
	}
	r.fake.Down(true)
	if _, err := r.client.Progress(context.Background()); !errors.Is(err, ErrDown) {
		t.Errorf("503 stream: %v", err)
	}
}

func TestProgressHeaderTimeout(t *testing.T) {
	r := newRig(t, func(o *Options) { o.Timeout = 50 * time.Millisecond })
	r.fake.Delay(400 * time.Millisecond)
	start := time.Now()
	if _, err := r.client.Progress(context.Background()); !errors.Is(err, ErrDown) {
		t.Errorf("got %v, want ErrDown", err)
	}
	if time.Since(start) > time.Second {
		t.Errorf("the header wait is not bounded: %s", time.Since(start))
	}
}

func TestNeverCallsForbiddenRoutes(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	// Every public method, in every outcome.
	_ = r.client.Health(ctx)
	_, _ = r.client.Models(ctx)
	_ = r.client.Reload(ctx)
	_ = r.client.Load(ctx, "model-a-q4km")
	_ = r.client.Load(ctx, "model-a-q4km")
	_, _ = r.client.Props(ctx, "model-a-q4km")
	_ = r.client.ChatSmoke(ctx, "model-a-q4km")
	_ = r.client.Unload(ctx, "model-a-q4km")
	_ = r.client.Unload(ctx, "model-a-q4km")
	if ch, err := r.client.Progress(ctx); err == nil {
		r.fake.DropStreams()
		waitClosed(t, ch)
	}
	r.fake.Down(true)
	_ = r.client.Health(ctx)
	_, _ = r.client.Models(ctx)
	if len(r.fake.Requests()) < 10 {
		t.Fatalf("the scenario made only %d calls", len(r.fake.Requests()))
	}
	onlyAllowedCalls(t, r.fake)
	r.noLeak(t)

	// The allow list itself: a download, a delete and autoload are refused before anything is sent.
	n := len(r.fake.Requests())
	for _, cl := range []call{
		{method: http.MethodPost, path: "/models", body: map[string]string{"model": "x"}},
		{method: http.MethodDelete, path: "/models"},
		{method: http.MethodGet, path: "/models", query: map[string][]string{"autoload": {"true"}}},
		{method: http.MethodGet, path: "/models", query: map[string][]string{"reload": {"1"}, "autoload": {"false"}}},
		{method: http.MethodPost, path: "/models/load/x"},
		{method: http.MethodGet, path: "/key"},
	} {
		if _, err := r.client.do(ctx, cl); !errors.Is(err, ErrRefused) {
			t.Errorf("%s %s?%v: got %v, want ErrRefused", cl.method, cl.path, cl.query, err)
		}
	}
	if len(r.fake.Requests()) != n {
		t.Error("a refused call reached the router")
	}
}

func TestNewClientBaseURL(t *testing.T) {
	good := []string{"", "http://127.0.0.1:8080", "http://127.0.0.1:8080/", "http://localhost:9000", "http://[::1]:8080", "http://127.1.2.3:1"}
	bad := []string{
		"https://127.0.0.1:8080", "http://10.0.0.5:8080", "http://example.test:8080", "http://192.0.2.1:8080",
		"http://user:pw@127.0.0.1:8080", "http://127.0.0.1:8080/v1", "http://127.0.0.1:8080/?a=b", "http://127.0.0.1:8080/#f",
		"127.0.0.1:8080", "ftp://127.0.0.1", "http://0.0.0.0:8080", "://", "http://",
	}
	for _, u := range good {
		if _, err := NewClient(Options{BaseURL: u}); err != nil {
			t.Errorf("NewClient(%q) = %v", u, err)
		}
	}
	for _, u := range bad {
		if _, err := NewClient(Options{BaseURL: u}); !errors.Is(err, ErrBadBaseURL) {
			t.Errorf("NewClient(%q) = %v, want ErrBadBaseURL", u, err)
		}
	}
	c, _ := NewClient(Options{Port: 9123})
	if c.base.String() != "http://127.0.0.1:9123" {
		t.Errorf("port option: %s", c.base)
	}
	c, _ = NewClient(Options{})
	if c.base.String() != "http://127.0.0.1:8080" || c.timeout != DefaultTimeout || c.chatTimeout != DefaultChatTimeout {
		t.Errorf("defaults: %s %s %s", c.base, c.timeout, c.chatTimeout)
	}
}

func TestProxyEnvironmentIsIgnored(t *testing.T) {
	// A proxy on a closed port would break every call if the client honoured the environment.
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	t.Setenv("http_proxy", "http://127.0.0.1:1")
	t.Setenv("NO_PROXY", "")
	r := newRig(t, func(o *Options) { o.Transport = nil })
	if _, err := r.client.Models(context.Background()); err != nil {
		t.Fatalf("the client went through the proxy: %v", err)
	}
}

func TestDialerRefusesNonLoopback(t *testing.T) {
	tr := newTransport()
	// A documentation address that must never be dialled: the Control hook rejects it before any packet.
	_, err := tr.DialContext(context.Background(), "tcp", "203.0.113.7:8080")
	if err == nil || !strings.Contains(err.Error(), "non-loopback") {
		t.Errorf("dial of a public address: %v", err)
	}
}

func TestHTTPErrorShapes(t *testing.T) {
	cases := []struct {
		e    *HTTPError
		text string
		is   []error
	}{
		{&HTTPError{Status: 401}, "router: HTTP 401", []error{ErrUnauthorized}},
		{&HTTPError{Status: 404, Message: "model is not found"}, "router: HTTP 404: model is not found", []error{ErrNotFound}},
		{&HTTPError{Status: 503}, "router: HTTP 503", []error{ErrDown}},
		{&HTTPError{Status: 400, Message: "m"}, "router: HTTP 400: m", nil},
	}
	for _, c := range cases {
		if c.e.Error() != c.text {
			t.Errorf("Error() = %q, want %q", c.e.Error(), c.text)
		}
		for _, target := range []error{ErrUnauthorized, ErrNotFound, ErrDown} {
			want := false
			for _, x := range c.is {
				want = want || x == target
			}
			if errors.Is(c.e, target) != want {
				t.Errorf("%s is %v: %v, want %v", c.text, target, !want, want)
			}
		}
	}
	if CodeOf(errors.New("x")) != "" || CodeOf(nil) != "" {
		t.Error("CodeOf of an unrelated error must be empty")
	}
}

// waitFor polls cond for up to two seconds.
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not reached in time")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func recv(t *testing.T, ch <-chan Progress) Progress {
	t.Helper()
	select {
	case p, ok := <-ch:
		if !ok {
			t.Fatal("the stream closed")
		}
		return p
	case <-time.After(2 * time.Second):
		t.Fatal("no event in time")
	}
	return Progress{}
}

func waitClosed(t *testing.T, ch <-chan Progress) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				return
			}
		case <-deadline:
			t.Fatal("the stream did not close")
		}
	}
}
