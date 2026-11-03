package piper

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/api"
	"github.com/marinoscar/local-ai/agent/internal/api/apitest"
	cfgfake "github.com/marinoscar/local-ai/agent/internal/config/fake"
	"github.com/marinoscar/local-ai/agent/internal/execx"
	"github.com/marinoscar/local-ai/agent/internal/execx/fake"
	"github.com/marinoscar/local-ai/agent/internal/svc/health"
)

// spokenText is spoken in several tests; its distinctive marker must never reach a log line.
const (
	spokenMarker = "Zebra-Quokka-7431"
	spokenText   = spokenMarker + " stays out of the logs"
)

// syncBuffer is a log sink safe for the concurrent handler.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// advancing is a Streamer that moves the fake clock when a process starts, so that the elapsed
// time of a request is deterministic and nothing sleeps.
type advancing struct {
	execx.Streamer
	clk *cfgfake.Clock
	by  time.Duration
}

func (a advancing) Start(ctx context.Context, c execx.StreamCmd) (execx.Process, error) {
	a.clk.Advance(a.by)
	return a.Streamer.Start(ctx, c)
}

// env is the real API server (full middleware chain) with the speech routes registered over a fake
// streamer, a temporary install root and fake voices.
type env struct {
	t        *testing.T
	ts       *httptest.Server
	srv      *api.Server
	svc      *Service
	streamer *fake.Streamer
	clk      *cfgfake.Clock
	metrics  *health.Metrics
	logs     *syncBuffer
	root     string
	cfg      Config
	cfgMu    sync.Mutex
	copies   fakeCopies
	done     chan struct{}
}

type envOpts struct {
	voices    []string
	noBinary  bool
	scripts   []fake.Script
	limiter   *Limiter
	catalog   VoiceCatalog
	configure func(*Config)
}

func newEnv(t *testing.T, o envOpts) *env {
	t.Helper()
	e := &env{t: t, streamer: fake.NewStreamer(o.scripts...), clk: cfgfake.NewClock(time.Unix(1_700_000_000, 0)),
		metrics: health.NewMetrics(), logs: &syncBuffer{}, cfg: DefaultConfig(), done: make(chan struct{}, 64)}
	if o.configure != nil {
		o.configure(&e.cfg)
	}
	e.root = t.TempDir()
	if !o.noBinary {
		e.root = installBinary(t)
	}
	e.copies = installedVoices(t, o.voices...)
	var cat VoiceCatalog = catalogVoices()
	if o.catalog != nil {
		cat = o.catalog
	}
	e.svc = New(Deps{
		Config:   func() Config { e.cfgMu.Lock(); defer e.cfgMu.Unlock(); return e.cfg },
		Catalog:  cat,
		Copies:   e.copies,
		Streamer: advancing{Streamer: e.streamer, clk: e.clk, by: 2 * time.Second},
		Clock:    e.clk,
		Metrics:  e.metrics,
		Logger:   slog.New(slog.NewTextHandler(e.logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		Root:     e.root,
		Limiter:  o.limiter,
	})
	e.svc.afterRequest = func() { e.done <- struct{}{} }

	reg := api.NewRegistry()
	if err := reg.Add(e.svc.Routes()...); err != nil {
		t.Fatal(err)
	}
	e.ts = httptest.NewUnstartedServer(nil)
	n := e.ts.Listener.Addr().(*net.TCPAddr).Port
	srv, err := api.New(api.Options{
		Registry: reg, Port: n, Version: "test", Mode: func() string { return "online" },
		Auth: api.AuthFunc(func(*http.Request, *api.Route) (api.Principal, error) {
			return api.Principal{Kind: api.KindOperator, Credential: api.CredBearer, Via: "bearer"}, nil
		}),
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	srv.SetMutationsOpen(true)
	srv.SetStarting(false)
	e.srv = srv
	e.ts.Config.Handler = srv.Handler()
	e.ts.Start()
	t.Cleanup(e.ts.Close)
	return e
}

func (e *env) setConfig(f func(*Config)) {
	e.cfgMu.Lock()
	defer e.cfgMu.Unlock()
	f(&e.cfg)
}

// waitDone blocks until n synthesis requests have finished (their metric counted, their process
// waited for), a signal of the server having applied everything, not merely received a request.
func (e *env) waitDone(n int) {
	e.t.Helper()
	for i := 0; i < n; i++ {
		<-e.done
	}
}

func (e *env) request(ctx context.Context, method, path, body string, hdr map[string]string) (*http.Request, error) {
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, e.ts.URL+"/api/v1"+path, rd)
	if err != nil {
		return nil, err
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	return req, nil
}

// tts posts a request and returns the response without reading its body.
func (e *env) tts(ctx context.Context, body map[string]any, hdr map[string]string) *http.Response {
	e.t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		e.t.Fatal(err)
	}
	return e.ttsRaw(ctx, string(b), hdr)
}

func (e *env) ttsRaw(ctx context.Context, body string, hdr map[string]string) *http.Response {
	e.t.Helper()
	req, err := e.request(ctx, http.MethodPost, "/speech/tts", body, hdr)
	if err != nil {
		e.t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	return resp
}

// ok posts a request whose answer is complete, records it against openapi.yaml and returns the status
// and the whole body.
func (e *env) ok(body map[string]any, hdr map[string]string) (*http.Response, []byte) {
	e.t.Helper()
	resp := e.tts(context.Background(), body, hdr)
	apitest.Record(e.t, resp)
	b, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		e.t.Fatalf("reading the body: %v", err)
	}
	return resp, b
}

// errorBody decodes the envelope of an error response.
func errorBody(t testing.TB, b []byte) (code string, details map[string]any) {
	t.Helper()
	var env struct {
		Error struct {
			Code    string         `json:"code"`
			Details map[string]any `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(b, &env); err != nil {
		t.Fatalf("not an error envelope: %q", b)
	}
	return env.Error.Code, env.Error.Details
}

// chunks splits pcm into pieces of n bytes.
func chunks(pcm []byte, n int) [][]byte {
	var out [][]byte
	for len(pcm) > 0 {
		k := n
		if k > len(pcm) {
			k = len(pcm)
		}
		out = append(out, pcm[:k])
		pcm = pcm[k:]
	}
	return out
}
