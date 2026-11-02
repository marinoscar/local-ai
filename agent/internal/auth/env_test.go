package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/api"
	"github.com/marinoscar/local-ai/agent/internal/api/apitest"
	"github.com/marinoscar/local-ai/agent/internal/auth/fake"
	cfake "github.com/marinoscar/local-ai/agent/internal/config/fake"
)

const (
	testHost   = "127.0.0.1:7070"
	testOrigin = "http://127.0.0.1:7070"
	opsGID     = 990
	operatorA  = 1000 // a member of localai-operators
	operatorB  = 1001 // another member
	stranger   = 2000 // not a member
)

// env is a real api.Server with the auth service, the five routes and a few probe routes, driven
// through its handler. Time is the fake clock; the filesystem is a temp directory.
type env struct {
	t       *testing.T
	svc     *Service
	srv     *api.Server
	h       http.Handler
	clock   *cfake.Clock
	groups  *fake.Groups
	portal  *fake.Portal
	audit   *recAudit
	logs    *syncBuf
	token   string
	tokPath string
	pinPath string

	mu     sync.Mutex
	alerts []string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	e := &env{t: t, clock: newClock(), portal: fake.NewPortal(), audit: &recAudit{}}
	e.tokPath, e.pinPath = tempPaths(t)
	e.groups = fake.NewGroups().Add(OperatorsGroup, opsGID, operatorA, operatorB)
	logger, logs := newLogger()
	e.logs = logs
	tp, pp := ownPolicy(0o640), ownPolicy(0o600)
	e.svc = New(Config{
		TokenPath: e.tokPath, PINPath: e.pinPath, Clock: e.clock, Groups: e.groups, Portal: e.portal,
		Audit: e.audit, Logger: logger, TokenPolicy: &tp, PINPolicy: &pp,
		Alert: func(code, severity string, _ map[string]any) {
			e.mu.Lock()
			e.alerts = append(e.alerts, code+"/"+severity)
			e.mu.Unlock()
		},
	})
	if _, err := e.svc.EnsureToken(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(e.tokPath)
	if err != nil {
		t.Fatal(err)
	}
	e.token = strings.TrimSpace(string(data))

	reg := api.NewRegistry()
	if err := Register(reg, e.svc); err != nil {
		t.Fatal(err)
	}
	if err := reg.Add(probeRoutes()...); err != nil {
		t.Fatal(err)
	}
	srv, err := api.New(api.Options{Registry: reg, Auth: e.svc, Logger: logger, Port: 7070, Mode: func() string { return "online" }})
	if err != nil {
		t.Fatal(err)
	}
	srv.SetMutationsOpen(true)
	e.srv, e.h = srv, srv.Handler()
	return e
}

func probeRoutes() []api.Route {
	whoami := api.HandlerFunc(func(w http.ResponseWriter, r *http.Request) error {
		p := api.InfoFrom(r.Context()).Principal
		api.WriteJSON(w, r, http.StatusOK, map[string]any{"principal": p.String(), "kind": string(p.Kind), "credential": string(p.Credential)})
		return nil
	})
	return []api.Route{
		{Method: "GET", Pattern: "/whoami", Auth: []api.Level{api.AuthOp}, Handler: whoami},
		{Method: "GET", Pattern: "/open", Auth: []api.Level{api.AuthNone}, Handler: whoami},
		{Method: "GET", Pattern: "/admin-only", Auth: []api.Level{api.AuthAdmin}, Handler: whoami},
		{Method: "GET", Pattern: "/admin-star", Auth: []api.Level{api.AuthAdminStar}, Handler: whoami},
		{Method: "GET", Pattern: "/either", Auth: []api.Level{api.AuthOp, api.AuthPortal}, Handler: whoami},
		{Method: "POST", Pattern: "/mutate", Auth: []api.Level{api.AuthOp, api.AuthPortal}, Handler: whoami},
	}
}

// req describes one request.
type req struct {
	method  string
	path    string // under /api/v1
	header  map[string]string
	cookies map[string]string
	body    string // default "{}" for a mutating method
	peer    *api.PeerCred
	noPeer  bool // a socket request whose SO_PEERCRED failed
}

func bearer(token string) map[string]string {
	return map[string]string{"Authorization": "Bearer " + token}
}

func (q req) build() *http.Request {
	method := q.method
	if method == "" {
		method = http.MethodGet
	}
	body := q.body
	if body == "" && method != http.MethodGet {
		body = "{}"
	}
	r := httptest.NewRequest(method, api.BasePath+q.path, strings.NewReader(body))
	r.Host = testHost
	if method != http.MethodGet {
		r.Header.Set("Content-Type", "application/json")
	}
	for k, v := range q.header {
		r.Header.Set(k, v)
	}
	for k, v := range q.cookies {
		r.AddCookie(&http.Cookie{Name: k, Value: v})
	}
	ctx := r.Context()
	if q.peer != nil || q.noPeer {
		ctx = api.WithTransport(ctx, api.TransportSocket, q.peer)
	}
	return r.WithContext(ctx)
}

// do serves q and returns the recorded response.
func (e *env) do(q req) *httptest.ResponseRecorder {
	r := q.build()
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, r)
	// The documented routes and every error are recorded against openapi.yaml (agent.md section 4.14);
	// the probe routes of this package are not part of the document.
	if rec.Code >= 400 || apitest.Serves(r.Method, r.URL.RequestURI()) {
		apitest.RecordResult(e.t, r, rec)
	}
	return rec
}

func peer(uid int) *api.PeerCred { return &api.PeerCred{UID: uid, GID: uid, PID: 4242} }

// asOperator is a socket request from the operator uid, the way aictl calls.
func asOperator(q req) req { q.peer = peer(operatorA); return q }

// errEnvelope is the decoded error answer.
type errEnvelope struct {
	Error struct {
		Code    string         `json:"code"`
		Message string         `json:"message"`
		Fix     string         `json:"fix"`
		Details map[string]any `json:"details"`
	} `json:"error"`
}

// wantError asserts the status and doctor code of an error answer and returns the envelope.
func wantError(t *testing.T, rec *httptest.ResponseRecorder, status int, code string) errEnvelope {
	t.Helper()
	var env errEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("not an error envelope (status %d): %q", rec.Code, rec.Body.String())
	}
	if rec.Code != status || env.Error.Code != code {
		t.Fatalf("got %d %s (%s), want %d %s", rec.Code, env.Error.Code, env.Error.Message, status, code)
	}
	if env.Error.Message == "" || env.Error.Fix == "" {
		t.Errorf("%s has no message or fix", code)
	}
	return env
}

// whoami asserts a 200 from a probe route and returns the principal string.
func wantPrincipal(t *testing.T, rec *httptest.ResponseRecorder, want string) {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Principal string `json:"principal"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Principal != want {
		t.Fatalf("principal = %q, want %q", got.Principal, want)
	}
}

// sessionCookie returns the localai_session cookie a response set.
func sessionCookie(t *testing.T, rec *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, c := range rec.Result().Cookies() {
		if c.Name == SessionCookie {
			return c
		}
	}
	t.Fatalf("no %s cookie in the response (status %d)", SessionCookie, rec.Code)
	return nil
}

// login creates a session from the bearer token and returns its cookie value.
func (e *env) login() string {
	e.t.Helper()
	rec := e.do(req{method: "POST", path: "/session", header: bearer(e.token)})
	if rec.Code != http.StatusNoContent {
		e.t.Fatalf("POST /session = %d %s", rec.Code, rec.Body.String())
	}
	return sessionCookie(e.t, rec).Value
}

// setPIN writes the admin PIN file.
func (e *env) setPIN(pin string) { writeFile(e.t, e.pinPath, cheapPHC(pin)+"\n", 0o600) }

// verifyPIN calls POST /pin/verify with the given credentials.
func (e *env) verifyPIN(q req, pin string) *httptest.ResponseRecorder {
	q.method, q.path = "POST", "/pin/verify"
	q.body = `{"pin": "` + pin + `"}`
	return e.do(q)
}

// grantFrom extracts the grant of a successful /pin/verify answer.
func grantFrom(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /pin/verify = %d %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Grant     string `json:"grant"`
		ExpiresAt string `json:"expires_at"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out.Grant
}

func (e *env) alertList() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.alerts...)
}
