package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/audit"
	cfake "github.com/marinoscar/local-ai/agent/internal/config/fake"
	"github.com/marinoscar/local-ai/agent/internal/modes"
	"github.com/marinoscar/local-ai/agent/internal/modes/fake"
	"github.com/marinoscar/local-ai/agent/internal/state"
	statefake "github.com/marinoscar/local-ai/agent/internal/state/fake"
)

var modeT0 = time.Date(2026, 11, 2, 15, 0, 0, 0, time.UTC)

// modeRig wires the /mode routes to the real mode engine over a real state store on an in-memory
// filesystem, with the engine's fakes, behind the real middleware chain.
type modeRig struct {
	t     *testing.T
	st    *state.Store
	env   *fake.Env
	eng   *modes.Engine
	srv   *Server
	h     http.Handler
	audit *recAudit
}

func newModeRig(t *testing.T, mode state.Mode, open bool) *modeRig {
	t.Helper()
	clk := cfake.NewClock(modeT0)
	st, err := state.Open("/var/lib/localai/state.json", state.Options{FS: statefake.New(), Now: clk.Now})
	if err != nil {
		t.Fatal(err)
	}
	if mode != modes.Online {
		if err := st.MutateSync(func(s *state.State) error {
			s.Mode = mode
			ts := modeT0.Add(-time.Hour)
			s.ModeChangedAt = &ts
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	env := fake.NewEnv()
	eng, err := modes.New(env.Deps(st), modes.Options{Clock: clk})
	if err != nil {
		t.Fatal(err)
	}
	if open {
		if _, err := eng.Open(); err != nil {
			t.Fatal(err)
		}
	}
	ra := &recAudit{}
	reg := NewRegistry()
	if err := RegisterMode(reg, ModeDeps{Engine: eng, Audit: ra}); err != nil {
		t.Fatal(err)
	}
	s, _ := newTestServer(t, func(o *Options) { o.Registry = reg })
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer cancel()
		_ = eng.Shutdown(ctx)
		_ = st.Close()
	})
	return &modeRig{t: t, st: st, env: env, eng: eng, srv: s, h: s.Handler(), audit: ra}
}

func (r *modeRig) get(extra map[string]string) *httptest.ResponseRecorder {
	h := map[string]string{"X-Test-Principal": "bearer"}
	for k, v := range extra {
		h[k] = v
	}
	return recordOK(r.t, do(r.h, req{target: "/api/v1/mode", header: h}))
}

func (r *modeRig) post(path, body string, extra map[string]string) *httptest.ResponseRecorder {
	return recordOK(r.t, do(r.h, req{method: "POST", target: "/api/v1" + path, body: body, header: jsonHeaders(extra)}))
}

func (r *modeRig) idle() {
	r.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := r.eng.WaitIdle(ctx); err != nil {
		r.t.Fatal(err)
	}
}

func TestModeRoutesAreRegistered(t *testing.T) {
	reg := NewRegistry()
	if err := RegisterMode(reg, ModeDeps{Engine: &modes.Engine{}}); err != nil {
		t.Fatal(err)
	}
	got := map[string]Route{}
	for _, rt := range reg.Routes() {
		got[rt.Method+" "+rt.Pattern] = rt
	}
	want := map[string][]Level{"GET /mode": {AuthOp}, "POST /mode": {AuthOp}, "POST /mode/survival": {AuthNone}}
	if len(got) != len(want) {
		t.Fatalf("routes = %v", got)
	}
	for k, levels := range want {
		rt, ok := got[k]
		if !ok || len(rt.Auth) != 1 || rt.Auth[0] != levels[0] {
			t.Errorf("%s: %+v", k, rt)
		}
	}
	if !got["POST /mode/survival"].GateExempt || got["POST /mode"].GateExempt || got["GET /mode"].GateExempt {
		t.Error("only POST /mode/survival is exempt from the startup gate")
	}
	if got["POST /mode"].Timeout != 20*time.Minute || got["GET /mode"].Timeout != 5*time.Second || got["POST /mode/survival"].Timeout != 5*time.Second {
		t.Error("route timeouts differ from agent.md 3.8")
	}
	if err := RegisterMode(NewRegistry(), ModeDeps{}); err == nil {
		t.Error("RegisterMode without an engine must fail")
	}
}

func TestModeGetShape(t *testing.T) {
	r := newModeRig(t, modes.Online, true)
	rec := r.get(nil)
	want := `{"mode":"online","since":null,"transition":null,"allowed_targets":["offline","survival"],"leave_requires_admin":false,"connectivity":"not_checked","suggest_survival":false}` + "\n"
	if rec.Code != 200 || rec.Body.String() != want {
		t.Fatalf("%d %s\nwant %s", rec.Code, rec.Body, want)
	}

	r = newModeRig(t, modes.Survival, true)
	m := decodeMap(t, r.get(nil))
	if m["mode"] != "survival" || m["leave_requires_admin"] != true || m["since"] != "2026-11-02T14:00:00Z" {
		t.Fatalf("survival: %v", m)
	}
	if got := m["allowed_targets"].([]any); len(got) != 2 || got[0] != "online" || got[1] != "offline" {
		t.Fatalf("allowed_targets %v", got)
	}
}

func TestModeGetShowsTheTransitionInFlight(t *testing.T) {
	r := newModeRig(t, modes.Online, true)
	entered, release := make(chan struct{}, 1), make(chan struct{})
	r.env.Sync.CancelB.SetHook(func(ctx context.Context) error {
		entered <- struct{}{}
		select {
		case <-release:
		case <-ctx.Done():
		}
		return nil
	})
	rec := r.post("/mode", `{"target":"survival"}`, nil)
	<-entered
	m := decodeMap(t, r.get(nil))
	tr, _ := m["transition"].(map[string]any)
	if tr == nil || tr["op_id"] != decodeMap(t, rec)["op_id"] || tr["target"] != "survival" || tr["from"] != "online" || tr["step"] != "sync_cancel" || tr["started_at"] != "2026-11-02T15:00:00Z" {
		t.Fatalf("transition = %v", m["transition"])
	}
	if ws, ok := tr["warnings"].([]any); !ok || len(ws) != 0 {
		t.Fatalf("warnings = %v", tr["warnings"])
	}
	if m["mode"] != "online" {
		t.Fatalf("mode %v flipped before persist", m["mode"])
	}
	close(release)
	r.idle()
	if m := decodeMap(t, r.get(nil)); m["transition"] != nil || m["mode"] != "survival" {
		t.Fatalf("after: %v", m)
	}
}

func TestModeAuth(t *testing.T) {
	r := newModeRig(t, modes.Online, true)
	expectError(t, do(r.h, req{target: "/api/v1/mode"}), 401, CodeUnauthenticated)
	expectError(t, do(r.h, req{method: "POST", target: "/api/v1/mode", body: `{"target":"offline"}`, header: map[string]string{"Content-Type": "application/json"}}), 401, CodeUnauthenticated)
	expectError(t, r.get(map[string]string{"X-Test-Principal": "portal"}), 401, CodePortalSessionEnded)
	if rec := r.get(map[string]string{"X-Test-Principal": "socket"}); rec.Code != 200 {
		t.Fatalf("socket operator: %d", rec.Code)
	}
}

func TestModePostAcceptedWithoutWait(t *testing.T) {
	r := newModeRig(t, modes.Online, true)
	rec := r.post("/mode", `{"target":"offline"}`, nil)
	if rec.Code != 202 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	m := decodeMap(t, rec)
	tr, _ := m["transition"].(map[string]any)
	op, _ := m["op_id"].(string)
	if !strings.HasPrefix(op, "op_") || m["from"] != "online" || m["target"] != "offline" || tr["step"] != "link_stop" || tr["started_at"] != "2026-11-02T15:00:00Z" {
		t.Fatalf("body = %v", m)
	}
	if rec.Header().Get("X-LocalAI-Op-Id") != op {
		t.Fatalf("op id header %q", rec.Header().Get("X-LocalAI-Op-Id"))
	}
	r.idle()
	if r.eng.Current() != modes.Offline {
		t.Fatal("not applied")
	}
}

func TestModePostWaitReturnsTheResult(t *testing.T) {
	r := newModeRig(t, modes.Online, true)
	r.env.Tailscale.OffB.Set(nil, modes.Warning{Code: modes.CodeTailscaleServe, Message: "x"})
	rec := r.post("/mode", `{"target":"survival","wait":true}`, nil)
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	m := decodeMap(t, rec)
	if m["from"] != "online" || m["mode"] != "survival" || m["op_id"] == "" {
		t.Fatalf("body = %v", m)
	}
	ws, ok := m["warnings"].([]any)
	if !ok || len(ws) != 1 || ws[0].(map[string]any)["code"] != modes.CodeTailscaleServe {
		t.Fatalf("warnings = %v", m["warnings"])
	}
	r.env.Tailscale.OffB.Set(nil)
	rec = r.post("/mode", `{"target":"offline","wait":true}`, map[string]string{"X-Test-Principal": "admin"})
	if m := decodeMap(t, rec); rec.Code != 200 || m["mode"] != "offline" || len(m["warnings"].([]any)) != 0 {
		t.Fatalf("%d %v", rec.Code, m)
	}
}

func TestModePostSameTargetIsAlreadyUnlessReapply(t *testing.T) {
	r := newModeRig(t, modes.Offline, true)
	rec := r.post("/mode", `{"target":"offline"}`, nil)
	if rec.Code != 200 || rec.Body.String() != `{"already":true,"mode":"offline"}`+"\n" {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if len(r.env.Effects()) != 0 {
		t.Fatalf("ran %v", r.env.Effects())
	}
	rec = r.post("/mode", `{"target":"offline","wait":true,"reapply":true}`, nil)
	if m := decodeMap(t, rec); rec.Code != 200 || m["from"] != "offline" || m["mode"] != "offline" || m["op_id"] == "" {
		t.Fatalf("reapply: %d %s", rec.Code, rec.Body)
	}
	if len(r.env.Effects()) == 0 {
		t.Fatal("reapply did nothing")
	}
}

func TestModePostValidation(t *testing.T) {
	r := newModeRig(t, modes.Online, true)
	for _, body := range []string{`{"target":"sideways"}`, `{"target":""}`, `{}`} {
		e := expectError(t, r.post("/mode", body, nil), 400, CodeInvalidRequest)
		if e.Error.Details["field"] != "target" || e.Error.Details["reason"] != "invalid_value" {
			t.Errorf("%s: details %v", body, e.Error.Details)
		}
	}
	e := expectError(t, r.post("/mode", `{"target":"offline","nope":1}`, nil), 400, CodeInvalidRequest)
	if e.Error.Details["reason"] != "unknown_field" || e.Error.Details["field"] != "nope" {
		t.Errorf("details %v", e.Error.Details)
	}
	if e := expectError(t, r.post("/mode", ``, nil), 400, CodeInvalidRequest); e.Error.Details["reason"] != "empty_body" {
		t.Errorf("details %v", e.Error.Details)
	}
	e = expectError(t, r.post("/mode", `{"target":"offline","wait":"yes"}`, nil), 400, CodeInvalidRequest)
	if e.Error.Details["reason"] != "wrong_type" {
		t.Errorf("details %v", e.Error.Details)
	}
	rec := do(r.h, req{method: "POST", target: "/api/v1/mode", body: `{"target":"offline"}`, header: map[string]string{"X-Test-Principal": "bearer", "Content-Type": "text/plain"}})
	expectError(t, rec, 415, CodeInvalidRequest)
	if len(r.env.Effects()) != 0 {
		t.Fatal("an invalid request changed something")
	}
}

func TestModePostBusyWhileAnotherTransitionRuns(t *testing.T) {
	r := newModeRig(t, modes.Online, true)
	entered, release := make(chan struct{}, 1), make(chan struct{})
	r.env.Link.StopB.SetHook(func(ctx context.Context) error {
		entered <- struct{}{}
		select {
		case <-release:
		case <-ctx.Done():
		}
		return nil
	})
	first := decodeMap(t, r.post("/mode", `{"target":"offline"}`, nil))
	<-entered
	e := expectError(t, r.post("/mode", `{"target":"online"}`, nil), 409, CodeBusy)
	if e.Error.Details["op_id"] != first["op_id"] || e.Error.Details["target"] != "offline" {
		t.Fatalf("details %v", e.Error.Details)
	}
	// survival is the exception: it preempts, answered 202 with its own op
	rec := r.post("/mode", `{"target":"survival"}`, nil)
	sv := decodeMap(t, rec)
	if rec.Code != 202 || sv["op_id"] == first["op_id"] || sv["from"] != "online" || sv["target"] != "survival" {
		t.Fatalf("survival while busy: %d %s", rec.Code, rec.Body)
	}
	close(release)
	r.idle()
	if r.eng.Current() != modes.Survival {
		t.Fatalf("mode %s", r.eng.Current())
	}
}

func TestModePostWaitOfAPreemptedTransitionIsConflict(t *testing.T) {
	r := newModeRig(t, modes.Online, true)
	entered, release := make(chan struct{}, 1), make(chan struct{})
	r.env.Sync.CancelB.SetHook(func(ctx context.Context) error {
		entered <- struct{}{}
		select {
		case <-release:
		case <-ctx.Done():
		}
		return nil
	})
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- r.post("/mode", `{"target":"offline","wait":true}`, nil) }()
	<-entered
	sv := decodeMap(t, recordOK(t, do(r.h, req{method: "POST", target: "/api/v1/mode/survival", body: `{}`, header: map[string]string{"Content-Type": "application/json"}})))
	close(release)
	e := expectError(t, <-done, 409, CodeBusy)
	if e.Error.Details["preempted"] != true || e.Error.Details["op_id"] != sv["op_id"] {
		t.Fatalf("details %v (survival %v)", e.Error.Details, sv)
	}
	r.idle()
}

// ---- leaving survival ------------------------------------------------------------------------------

func TestLeavingSurvivalNeedsAdminStar(t *testing.T) {
	cases := []struct {
		name      string
		principal string
		want      int
	}{
		{"operator over TCP with a bearer token", "bearer", 403},
		{"operator over TCP with a session", "session", 403},
		{"operator on the socket", "socket", 200},
		{"admin", "admin", 200},
	}
	for _, c := range cases {
		for _, target := range []string{"offline", "online"} {
			t.Run(c.name+"_to_"+target, func(t *testing.T) {
				r := newModeRig(t, modes.Survival, true)
				rec := r.post("/mode", `{"target":"`+target+`","wait":true}`, map[string]string{"X-Test-Principal": c.principal, "Origin": "http://127.0.0.1:7070"})
				if rec.Code != c.want {
					t.Fatalf("status %d, want %d: %s", rec.Code, c.want, rec.Body)
				}
				if c.want == 403 {
					e := expectError(t, rec, 403, CodeAdminRequired)
					if e.Error.Details["pin_required"] != true {
						t.Errorf("details %v", e.Error.Details)
					}
					if r.eng.Current() != modes.Survival || len(r.env.Effects()) != 0 {
						t.Fatal("a refused request changed something")
					}
					evs := r.audit.events()
					if len(evs) != 1 || evs[0].Action != audit.ActionModeChange || evs[0].Result != audit.ResultDenied || evs[0].Code != CodeAdminRequired ||
						evs[0].Target["from"] != "survival" || evs[0].Target["to"] != target {
						t.Fatalf("audit %+v", evs)
					}
				} else if r.eng.Current() != state.Mode(target) {
					t.Fatalf("mode %s", r.eng.Current())
				}
			})
		}
	}
}

func TestEnteringSurvivalNeverNeedsMoreThanOperator(t *testing.T) {
	for _, from := range []state.Mode{modes.Online, modes.Offline} {
		r := newModeRig(t, from, true)
		rec := r.post("/mode", `{"target":"survival","wait":true}`, map[string]string{"X-Test-Principal": "bearer"})
		if rec.Code != 200 {
			t.Fatalf("from %s: %d %s", from, rec.Code, rec.Body)
		}
	}
}

func TestSurvivalRequestWhileLeavingSurvivalIsNotALeave(t *testing.T) {
	r := newModeRig(t, modes.Survival, true)
	rec := r.post("/mode", `{"target":"survival","reapply":true,"wait":true}`, map[string]string{"X-Test-Principal": "bearer"})
	if rec.Code != 200 || decodeMap(t, rec)["mode"] != "survival" {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

// ---- POST /mode/survival -------------------------------------------------------------------------

func (r *modeRig) survival(body string, h map[string]string) *httptest.ResponseRecorder {
	hdr := map[string]string{"Content-Type": "application/json"}
	for k, v := range h {
		hdr[k] = v
	}
	return recordOK(r.t, do(r.h, req{method: "POST", target: "/api/v1/mode/survival", body: body, header: hdr}))
}

func TestSurvivalRouteWorksAnonymously(t *testing.T) {
	r := newModeRig(t, modes.Online, true)
	rec := r.survival(`{}`, nil)
	m := decodeMap(t, rec)
	op, _ := m["op_id"].(string)
	if rec.Code != 202 || m["target"] != "survival" || !strings.HasPrefix(op, "op_") || len(m) != 2 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	r.idle()
	if r.eng.Current() != modes.Survival {
		t.Fatalf("mode %s", r.eng.Current())
	}
	evs := r.env.Audit.Events()
	if len(evs) != 1 || evs[0].Actor.Kind != audit.KindAnonymous || evs[0].Actor.Via != "portal" || evs[0].OpID != op {
		t.Fatalf("audit %+v", evs)
	}
	mt := r.st.Snapshot().ModeTransition
	if mt != nil {
		t.Fatalf("transition left: %+v", mt)
	}
}

func TestSurvivalRouteBodyVariants(t *testing.T) {
	r := newModeRig(t, modes.Survival, true)
	rec := r.survival(`{}`, nil)
	if rec.Code != 200 || rec.Body.String() != `{"already":true,"mode":"survival"}`+"\n" {
		t.Fatalf("already: %d %s", rec.Code, rec.Body)
	}
	rec = r.survival(`{"reapply":true}`, nil)
	if rec.Code != 202 || decodeMap(t, rec)["target"] != "survival" {
		t.Fatalf("reapply: %d %s", rec.Code, rec.Body)
	}
	r.idle()
	if r.env.Rec.Count("models.load") != 1 {
		t.Fatal("reapply did not re-run the steps")
	}
	// an empty body is fine (Content-Type is still required); unknown fields are not
	if rec := r.survival(``, nil); rec.Code != 200 {
		t.Fatalf("empty body: %d %s", rec.Code, rec.Body)
	}
	e := expectError(t, r.survival(`{"wait":true}`, nil), 400, CodeInvalidRequest)
	if e.Error.Details["field"] != "wait" {
		t.Fatalf("details %v", e.Error.Details)
	}
}

func TestSurvivalRouteRefusesBadHostOriginAndContentType(t *testing.T) {
	r := newModeRig(t, modes.Online, true)
	expectError(t, do(r.h, req{method: "POST", target: "/api/v1/mode/survival", body: `{}`, host: "evil.example:7070", header: map[string]string{"Content-Type": "application/json"}}), 403, CodeBadHost)
	expectError(t, r.survival(`{}`, map[string]string{"Origin": "https://evil.example"}), 403, CodeBadOrigin)
	expectError(t, do(r.h, req{method: "POST", target: "/api/v1/mode/survival", body: `{}`, header: map[string]string{"Content-Type": "text/plain"}}), 415, CodeInvalidRequest)
	expectError(t, do(r.h, req{method: "OPTIONS", target: "/api/v1/mode/survival"}), 405, CodeInvalidRequest)
	if r.eng.Current() != modes.Online || len(r.env.Effects()) != 0 {
		t.Fatal("a refused request changed something")
	}
}

func TestSurvivalRecordsTheOperatorWhenOneIsSignedIn(t *testing.T) {
	r := newModeRig(t, modes.Online, true)
	r.survival(`{}`, map[string]string{"X-Test-Principal": "bearer"})
	r.idle()
	evs := r.env.Audit.Events()
	if len(evs) != 1 || evs[0].Actor.Kind != audit.KindOperator || evs[0].Actor.Via != "bearer" {
		t.Fatalf("audit %+v", evs)
	}
}

// Before startup step 12 the gate is closed: POST /mode/survival is queued as pending_mode, every
// other mutating route answers 503, and the engine runs the survival request once it opens.
func TestSurvivalIsQueuedBeforeTheGateOpens(t *testing.T) {
	r := newModeRig(t, modes.Online, false)
	r.srv.SetMutationsOpen(false)
	expectError(t, r.post("/mode", `{"target":"offline"}`, nil), 503, CodeStarting)

	rec := r.survival(`{}`, nil)
	m := decodeMap(t, rec)
	if rec.Code != 202 || m["queued"] != true || m["target"] != "survival" || m["op_id"] == "" {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if p := r.st.Snapshot().PendingMode; p == nil || *p != modes.Survival {
		t.Fatalf("pending_mode = %v", p)
	}
	if len(r.env.Effects()) != 0 {
		t.Fatalf("ran early: %v", r.env.Effects())
	}
	if _, err := r.eng.Open(); err != nil {
		t.Fatal(err)
	}
	r.srv.SetMutationsOpen(true)
	r.idle()
	if r.eng.Current() != modes.Survival {
		t.Fatalf("mode %s", r.eng.Current())
	}
	if ev := r.env.Audit.Events(); len(ev) != 1 || ev[0].OpID != m["op_id"] {
		t.Fatalf("ran under another op: %+v (answered %v)", ev, m["op_id"])
	}
}

func TestSurvivalIsQueuedWhileStopping(t *testing.T) {
	r := newModeRig(t, modes.Online, true)
	if err := r.eng.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	rec := r.survival(`{}`, nil)
	if m := decodeMap(t, rec); rec.Code != 202 || m["queued"] != true {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	// the gate is open in this rig, so the engine itself refuses the others
	expectError(t, r.post("/mode", `{"target":"offline"}`, nil), 503, CodeStarting)
}

func TestSurvivalPreemptsARunningTransitionThroughTheRoute(t *testing.T) {
	r := newModeRig(t, modes.Offline, true)
	entered, release := make(chan struct{}, 1), make(chan struct{})
	r.env.Link.StartB.SetHook(func(ctx context.Context) error {
		entered <- struct{}{}
		select {
		case <-release:
		case <-ctx.Done():
		}
		return nil
	})
	r.post("/mode", `{"target":"online"}`, nil)
	<-entered
	rec := r.survival(`{}`, nil)
	if rec.Code != 202 || decodeMap(t, rec)["queued"] != nil {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	close(release)
	r.idle()
	if r.eng.Current() != modes.Survival || r.env.Rec.Count("prober.start") != 0 {
		t.Fatalf("mode %s effects %v", r.eng.Current(), r.env.Effects())
	}
}
