package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/api/apitest"
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

// logLines parses the JSON log lines written so far.
func (s *syncBuf) lines(t *testing.T) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, l := range strings.Split(strings.TrimSpace(s.String()), "\n") {
		if l == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("log line is not JSON: %q", l)
		}
		out = append(out, m)
	}
	return out
}

// credAuth authenticates from the X-Test-Principal header so tests can pick any principal.
var credAuth = AuthFunc(func(r *http.Request, rt *Route) (Principal, error) {
	switch r.Header.Get("X-Test-Principal") {
	case "bearer":
		return Principal{Kind: KindOperator, Credential: CredBearer, Via: "bearer"}, nil
	case "session":
		return Principal{Kind: KindOperator, Credential: CredSession, Via: "session"}, nil
	case "portal":
		return Principal{Kind: KindPortal, Credential: CredPortal, Via: "portal"}, nil
	case "socket":
		return Principal{Kind: KindOperator, Credential: CredSocket, Via: "uid=1000"}, nil
	case "admin":
		return Principal{Kind: KindAdmin, Credential: CredSocket, Via: "uid=0"}, nil
	}
	return Anonymous(), nil
})

// testRoutes are the routes the middleware tests drive.
func testRoutes() []Route {
	jsonEcho := HandlerFunc(func(w http.ResponseWriter, r *http.Request) error {
		var body struct {
			Name string `json:"name"`
			N    int    `json:"n"`
		}
		if err := DecodeJSON(w, r, &body); err != nil {
			return err
		}
		WriteJSON(w, r, http.StatusOK, body)
		return nil
	})
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		WriteJSON(w, r, http.StatusOK, map[string]any{"ok": true, "id": r.PathValue("id")})
	})
	return []Route{
		{Method: "GET", Pattern: "/ping", Auth: []Level{AuthOp}, Timeout: 5 * time.Second, Handler: ok},
		{Method: "GET", Pattern: "/open", Auth: []Level{AuthNone}, Timeout: 5 * time.Second, Handler: ok},
		{Method: "GET", Pattern: "/models/{id}", Auth: []Level{AuthOp, AuthPortal}, Handler: ok},
		{Method: "PUT", Pattern: "/models/default", Auth: []Level{AuthOp}, Handler: ok},
		{Method: "POST", Pattern: "/echo", Auth: []Level{AuthOp, AuthPortal}, Timeout: 5 * time.Second, Handler: jsonEcho},
		{Method: "POST", Pattern: "/survival", Auth: []Level{AuthNone}, GateExempt: true, Handler: ok},
		{Method: "DELETE", Pattern: "/thing", Auth: []Level{AuthAdmin}, Handler: ok},
		{Method: "POST", Pattern: "/boom", Auth: []Level{AuthNone}, Handler: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			panic("handler bug with token lat_" + strings.Repeat("A", 30))
		})},
		{Method: "POST", Pattern: "/boom-late", Auth: []Level{AuthNone}, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, "partial")
			panic("after the response started")
		})},
		{Method: "POST", Pattern: "/upload", Auth: []Level{AuthOp}, MaxBody: 16, MediaTypes: []string{"multipart/form-data"}, Handler: ok},
		{Method: "POST", Pattern: "/slow", Auth: []Level{AuthNone}, Timeout: 40 * time.Millisecond, Handler: HandlerFunc(func(w http.ResponseWriter, r *http.Request) error {
			<-r.Context().Done()
			return r.Context().Err()
		})},
		{Method: "POST", Pattern: "/fail", Auth: []Level{AuthNone}, Handler: HandlerFunc(func(w http.ResponseWriter, r *http.Request) error {
			SetOpID(w, "op_7k2m9q4r8t1c")
			return NewError(CodeBusy, http.StatusConflict, map[string]any{"op_id": "op_7k2m9q4r8t1c"})
		})},
		{Method: "GET", Pattern: "/info", Auth: []Level{AuthNone}, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			info := InfoFrom(r.Context())
			p := ""
			if info.Peer != nil {
				p = "peer"
			}
			WriteJSON(w, r, 200, map[string]any{"transport": info.Transport, "peer": p, "route": info.Route.Pattern, "principal": info.Principal.String()})
		})},
	}
}

// newTestServer builds a server with the test routes, the credAuth authenticator and a JSON log at
// debug level. The mutating-route gate is open unless a test closes it.
func newTestServer(t *testing.T, mod ...func(*Options)) (*Server, *syncBuf) {
	t.Helper()
	reg := NewRegistry()
	if err := reg.Add(testRoutes()...); err != nil {
		t.Fatal(err)
	}
	logs := &syncBuf{}
	lv := new(slog.LevelVar)
	logger := logx.New(logs, lv)
	lv.Set(slog.LevelDebug)
	opts := Options{
		Registry: reg,
		Auth:     credAuth,
		Logger:   logger,
		Version:  "1.2.3-test",
		Mode:     func() string { return "online" },
	}
	for _, m := range mod {
		m(&opts)
	}
	s, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	s.SetMutationsOpen(true)
	return s, logs
}

// req describes one test request.
type req struct {
	method string
	target string
	host   string // default 127.0.0.1:7070
	header map[string]string
	body   string
	socket *PeerCred // non-nil: serve as a socket request with this peer
	ctx    func(context.Context) context.Context
}

func (q req) build() *http.Request {
	method := q.method
	if method == "" {
		method = "GET"
	}
	r := httptest.NewRequest(method, q.target, strings.NewReader(q.body))
	r.Host = "127.0.0.1:7070"
	if q.host != "" {
		r.Host = q.host
	}
	for k, v := range q.header {
		r.Header.Set(k, v)
	}
	ctx := r.Context()
	if q.socket != nil {
		ctx = WithTransport(ctx, TransportSocket, q.socket)
	}
	if q.ctx != nil {
		ctx = q.ctx(ctx)
	}
	return r.WithContext(ctx)
}

// served maps each recorder `do` and `newRecorder` filled to the request it answered, so that
// expectError can hand both to apitest.RecordResult.
var served sync.Map // *httptest.ResponseRecorder -> *http.Request

// recordOK records a success response served by do against openapi.yaml; the errors of a test are
// recorded by expectError, so they are left alone here.
func recordOK(t testing.TB, rec *httptest.ResponseRecorder) *httptest.ResponseRecorder {
	t.Helper()
	if r, ok := served.Load(rec); ok && rec.Code < 400 {
		apitest.RecordResult(t, r.(*http.Request), rec)
	}
	return rec
}

func do(h http.Handler, q req) *httptest.ResponseRecorder {
	r := q.build()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	served.Store(rec, r)
	return rec
}

// errBody is the decoded error envelope.
type errBody struct {
	Error struct {
		Code    string         `json:"code"`
		Message string         `json:"message"`
		Fix     string         `json:"fix"`
		Details map[string]any `json:"details"`
	} `json:"error"`
}

func decodeErr(t *testing.T, rec *httptest.ResponseRecorder) errBody {
	t.Helper()
	var e errBody
	if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil {
		t.Fatalf("body is not an error envelope: %q (%v)", rec.Body.String(), err)
	}
	if e.Error.Details == nil {
		t.Fatalf("details must be an object, got: %s", rec.Body.String())
	}
	return e
}

func expectError(t *testing.T, rec *httptest.ResponseRecorder, status int, code string) errBody {
	t.Helper()
	// Every error a test asserts is also recorded against openapi.yaml (agent.md section 4.14): it
	// must be an ErrorEnvelope and, on a documented route, a documented status.
	if r, ok := served.Load(rec); ok {
		apitest.RecordResult(t, r.(*http.Request), rec)
	}
	if rec.Code != status {
		t.Fatalf("status = %d, want %d (body %s)", rec.Code, status, rec.Body.String())
	}
	e := decodeErr(t, rec)
	if e.Error.Code != code {
		t.Fatalf("code = %s, want %s (body %s)", e.Error.Code, code, rec.Body.String())
	}
	if e.Error.Message == "" || e.Error.Fix == "" {
		t.Fatalf("message and fix must be filled: %s", rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Fatalf("Content-Type = %q", ct)
	}
	return e
}

func jsonHeaders(extra map[string]string) map[string]string {
	h := map[string]string{"Content-Type": "application/json", "X-Test-Principal": "bearer"}
	for k, v := range extra {
		h[k] = v
	}
	return h
}

func newRecorder(h http.Handler, r *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	served.Store(rec, r)
	return rec
}
