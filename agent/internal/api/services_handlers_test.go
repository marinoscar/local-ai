package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/audit"
	"github.com/marinoscar/local-ai/agent/internal/services"
	svcfake "github.com/marinoscar/local-ai/agent/internal/services/fake"
)

// svcRig wires the /services routes to a fake Manager through the real Controller, so the handler,
// the controller and the wait loop run together.
type svcRig struct {
	t      *testing.T
	mgr    *svcfake.Manager
	audit  *recAudit
	llama  *fakeLlama
	mode   string
	status *staticStatus
	h      http.Handler
}

type staticStatus struct{ rows []services.Status }

func (s *staticStatus) Snapshot(context.Context) []services.Status { return s.rows }

type recAudit struct {
	mu   sync.Mutex
	evs  []audit.Event
	fail error
}

func (a *recAudit) Write(ev audit.Event) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.evs = append(a.evs, ev)
	return a.fail
}

func (a *recAudit) events() []audit.Event {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]audit.Event(nil), a.evs...)
}

type fakeLlama struct {
	mu       sync.Mutex
	calls    []string
	restored []string
	err      error
	skipOp   bool
}

func (f *fakeLlama) Run(ctx context.Context, opID string, action services.Action, op func(context.Context) error) ([]string, error) {
	f.mu.Lock()
	f.calls = append(f.calls, opID+" "+string(action))
	skip, ferr, restored := f.skipOp, f.err, f.restored
	f.mu.Unlock()
	if skip {
		return nil, ferr
	}
	if err := op(ctx); err != nil {
		return nil, err
	}
	if ferr != nil {
		return nil, ferr
	}
	return restored, nil
}

func (f *fakeLlama) callList() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func newSvcRig(t *testing.T, opts ...func(*ServicesDeps)) *svcRig {
	t.Helper()
	r := &svcRig{t: t, mgr: svcfake.New(), audit: &recAudit{}, llama: &fakeLlama{restored: []string{"model-a", "model-b"}}, mode: "online", status: &staticStatus{}}
	ctl := &services.Controller{Manager: r.mgr, WaitFor: 300 * time.Millisecond, Poll: 5 * time.Millisecond}
	deps := ServicesDeps{
		Status: r.status, Actions: ctl, Llama: r.llama, Mode: func() string { return r.mode }, Audit: r.audit,
		NewOpID: func() string { return "op_7k2m9q4r8t1c" },
	}
	for _, o := range opts {
		o(&deps)
	}
	reg := NewRegistry()
	if err := RegisterServices(reg, deps); err != nil {
		t.Fatal(err)
	}
	s, _ := newTestServer(t, func(o *Options) { o.Registry = reg })
	r.h = s.Handler()
	return r
}

func (r *svcRig) post(path, body string, extra map[string]string) *httptest.ResponseRecorder {
	return recordOK(r.t, do(r.h, req{method: "POST", target: "/api/v1" + path, body: body, header: jsonHeaders(extra)}))
}

func (r *svcRig) get(path string, extra map[string]string) *httptest.ResponseRecorder {
	h := map[string]string{"X-Test-Principal": "bearer"}
	for k, v := range extra {
		h[k] = v
	}
	return recordOK(r.t, do(r.h, req{target: "/api/v1" + path, header: h}))
}

func decodeMap(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("body is not JSON: %q", rec.Body.String())
	}
	return m
}

func TestServicesListShape(t *testing.T) {
	r := newSvcRig(t)
	at := time.Date(2026, 11, 2, 8, 0, 5, 0, time.UTC)
	r.status.rows = []services.Status{
		{Name: "llama", Unit: "localai-llama.service", Active: "active", Sub: "running", Since: at, Restarts: 1, Healthy: true, Health: services.HealthOK, Enabled: true,
			LastExit: &services.LastExit{Code: 0, At: at.Add(-time.Minute)}},
		{Name: "kiwix", Unit: "localai-kiwix.service", Active: "active", Sub: "running", Since: at, Healthy: false, Health: services.HealthStarting, Enabled: true},
		{Name: "whisper", Unit: "localai-whisper.service", Active: "inactive", Sub: "dead", Health: services.HealthDown},
	}
	rec := r.get("/services", nil)
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "application/json; charset=utf-8" {
		t.Fatalf("status %d, Content-Type %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	want := `{"services":[` +
		`{"name":"llama","unit":"localai-llama.service","active":"active","sub":"running","since":"2026-11-02T08:00:05Z","restarts":1,"healthy":true,"state":"ok","enabled":true,"last_exit":{"code":0,"at":"2026-11-02T07:59:05Z"}},` +
		`{"name":"kiwix","unit":"localai-kiwix.service","active":"active","sub":"running","since":"2026-11-02T08:00:05Z","restarts":0,"healthy":false,"state":"starting","enabled":true,"last_exit":null},` +
		`{"name":"whisper","unit":"localai-whisper.service","active":"inactive","sub":"dead","since":null,"restarts":0,"healthy":false,"state":"down","enabled":false,"last_exit":null}` +
		`]}` + "\n"
	if got := rec.Body.String(); got != want {
		t.Errorf("body:\n got %s\nwant %s", got, want)
	}
}

// With the real watcher over a fake Manager, GET /services lists the three units with health.
func TestServicesListFromTheWatcher(t *testing.T) {
	mgr := svcfake.New()
	mgr.Set(services.UnitState{Unit: services.UnitLlama, ActiveState: "active", SubState: "running", UnitFileState: "enabled", ActiveEnterTimestamp: time.Now()})
	w := services.NewWatcher(services.WatcherOptions{Manager: mgr, Prober: okProber{}, Ports: services.Ports{Llama: 8080, Kiwix: 8888, Whisper: 8081}})
	reg := NewRegistry()
	if err := RegisterServices(reg, ServicesDeps{Status: w, Actions: &services.Controller{Manager: mgr}}); err != nil {
		t.Fatal(err)
	}
	s, _ := newTestServer(t, func(o *Options) { o.Registry = reg })
	rec := do(s.Handler(), req{target: "/api/v1/services", header: map[string]string{"X-Test-Principal": "bearer"}})
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var body struct {
		Services []map[string]any `json:"services"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || len(body.Services) != 3 {
		t.Fatalf("body = %s (%v)", rec.Body, err)
	}
	names := []string{}
	for _, row := range body.Services {
		names = append(names, row["name"].(string))
	}
	if strings.Join(names, ",") != "llama,kiwix,whisper" {
		t.Errorf("order = %v", names)
	}
	if body.Services[0]["active"] != "active" || body.Services[0]["enabled"] != true || body.Services[1]["active"] != "inactive" {
		t.Errorf("rows = %v", body.Services)
	}
}

type okProber struct{}

func (okProber) Check(context.Context, services.Probe) error { return nil }

func TestServicesAuth(t *testing.T) {
	r := newSvcRig(t)
	expectError(t, do(r.h, req{target: "/api/v1/services"}), 401, CodeUnauthenticated)
	expectError(t, do(r.h, req{method: "POST", target: "/api/v1/services/kiwix/start", body: "{}", header: map[string]string{"Content-Type": "application/json"}}), 401, CodeUnauthenticated)
	// A Portal session is not an operator.
	expectError(t, r.get("/services", map[string]string{"X-Test-Principal": "portal"}), 401, CodePortalSessionEnded)
	if rec := r.post("/services/kiwix/start", "{}", map[string]string{"X-Test-Principal": "socket"}); rec.Code != 200 {
		t.Errorf("a socket operator was refused: %d %s", rec.Code, rec.Body)
	}
}

func TestServicesActionsSucceed(t *testing.T) {
	tests := []struct {
		name, path string
		before     string
		wantActive string
		verb       services.Verb
		unit       string
	}{
		{"start kiwix", "/services/kiwix/start", "inactive", "active", services.VerbStart, services.UnitKiwix},
		{"stop kiwix", "/services/kiwix/stop", "active", "inactive", services.VerbStop, services.UnitKiwix},
		{"restart whisper", "/services/whisper/restart", "active", "active", services.VerbRestart, services.UnitWhisper},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := newSvcRig(t)
			r.mgr.Set(services.UnitState{Unit: tc.unit, ActiveState: tc.before, SubState: "x"})
			r.mgr.Reset()
			rec := r.post(tc.path, "", nil)
			if rec.Code != 200 {
				t.Fatalf("status %d: %s", rec.Code, rec.Body)
			}
			got := decodeMap(t, rec)
			svc := strings.Split(tc.path, "/")[2]
			if got["name"] != svc || got["active"] != tc.wantActive || got["op_id"] != "op_7k2m9q4r8t1c" {
				t.Errorf("body = %v", got)
			}
			if restored, ok := got["restored_models"].([]any); !ok || len(restored) != 0 {
				t.Errorf("restored_models = %#v, want []", got["restored_models"])
			}
			if rec.Header().Get("X-LocalAI-Op-Id") != "op_7k2m9q4r8t1c" {
				t.Errorf("op id header = %q", rec.Header().Get("X-LocalAI-Op-Id"))
			}
			calls := r.mgr.Calls()
			if len(calls) == 0 || calls[0].Verb != tc.verb || calls[0].Unit != tc.unit {
				t.Errorf("calls = %v", calls)
			}
			evs := r.audit.events()
			if len(evs) != 1 {
				t.Fatalf("audit = %+v", evs)
			}
			ev := evs[0]
			if ev.Action != "service."+string(tc.verb) || ev.Result != audit.ResultOK || ev.Code != "" || ev.OpID != "op_7k2m9q4r8t1c" ||
				ev.Target["service"] != svc || ev.Target["unit"] != tc.unit || ev.Actor.Kind != audit.KindOperator || ev.Actor.Via != audit.ViaBearer || ev.ReqID == "" {
				t.Errorf("audit event = %+v", ev)
			}
			if n := len(r.llama.callList()); n != 0 {
				t.Errorf("the llama hook ran for %s", svc)
			}
		})
	}
}

func TestServicesBodyHandling(t *testing.T) {
	r := newSvcRig(t)
	ok := func(body string) {
		t.Helper()
		if rec := r.post("/services/kiwix/start", body, nil); rec.Code != 200 {
			t.Errorf("body %q: status %d: %s", body, rec.Code, rec.Body)
		}
	}
	ok("")
	ok("{}")
	ok(`{"force": false}`)
	ok(`{"force": true}`) // force only matters for stopping llama in survival, and is harmless elsewhere

	e := expectError(t, r.post("/services/kiwix/start", `{"forse": true}`, nil), 400, CodeInvalidRequest)
	if e.Error.Details["field"] != "forse" || e.Error.Details["reason"] != "unknown_field" {
		t.Errorf("details = %v", e.Error.Details)
	}
	e = expectError(t, r.post("/services/kiwix/start", `{"force": "yes"}`, nil), 400, CodeInvalidRequest)
	if e.Error.Details["reason"] != "wrong_type" {
		t.Errorf("details = %v", e.Error.Details)
	}
	expectError(t, r.post("/services/kiwix/start", `{"force": true} {}`, nil), 400, CodeInvalidRequest)
	expectError(t, do(r.h, req{method: "POST", target: "/api/v1/services/kiwix/start", body: "{}", header: map[string]string{"X-Test-Principal": "bearer", "Content-Type": "text/plain"}}), 415, CodeInvalidRequest)
}

func TestServicesUnknownNameAndAction(t *testing.T) {
	r := newSvcRig(t)
	for _, name := range []string{"piper", "maps", "sync", "LLAMA", "localai-llama.service", "agent"} {
		e := expectError(t, r.post("/services/"+name+"/start", "{}", nil), 404, CodeNotFound)
		if e.Error.Details["kind"] != "service" {
			t.Errorf("%s: details = %v", name, e.Error.Details)
		}
	}
	for _, action := range []string{"explode", "Start", "enable", "mask", "kill"} {
		e := expectError(t, r.post("/services/llama/"+action, "{}", nil), 400, CodeInvalidRequest)
		if e.Error.Details["field"] != "action" || e.Error.Details["reason"] != "unknown_action" {
			t.Errorf("%s: details = %v", action, e.Error.Details)
		}
	}
	if n := len(r.mgr.Calls()) + len(r.llama.callList()); n != 0 {
		t.Errorf("systemd was touched %d times for refused requests", n)
	}
	if n := len(r.audit.events()); n != 0 {
		t.Errorf("refused requests were audited: %+v", r.audit.events())
	}
}

func TestServicesStopLlamaInSurvival(t *testing.T) {
	t.Run("without force: 409 LAI-AGT-023, nothing runs, denied is audited", func(t *testing.T) {
		r := newSvcRig(t)
		r.mode = "survival"
		r.mgr.Set(services.UnitState{Unit: services.UnitLlama, ActiveState: "active", SubState: "running"})
		r.mgr.Reset()
		for _, body := range []string{"", "{}", `{"force": false}`} {
			e := expectError(t, r.post("/services/llama/stop", body, nil), 409, CodeNotInMode)
			if e.Error.Details["mode"] != "survival" || e.Error.Details["requires"] != "force" {
				t.Errorf("details = %v", e.Error.Details)
			}
		}
		if len(r.mgr.Calls()) != 0 || len(r.llama.callList()) != 0 {
			t.Errorf("llama was stopped: %v %v", r.mgr.Calls(), r.llama.callList())
		}
		evs := r.audit.events()
		if len(evs) != 3 || evs[0].Result != audit.ResultDenied || evs[0].Code != CodeNotInMode || evs[0].Action != "service.stop" {
			t.Errorf("audit = %+v", evs)
		}
	})
	t.Run("with force it stops", func(t *testing.T) {
		r := newSvcRig(t)
		r.mode = "survival"
		r.mgr.Set(services.UnitState{Unit: services.UnitLlama, ActiveState: "active", SubState: "running"})
		rec := r.post("/services/llama/stop", `{"force": true}`, nil)
		if rec.Code != 200 || decodeMap(t, rec)["active"] != "inactive" {
			t.Fatalf("status %d: %s", rec.Code, rec.Body)
		}
		evs := r.audit.events()
		if len(evs) != 1 || evs[0].Result != audit.ResultOK || evs[0].Target["force"] != "true" {
			t.Errorf("audit = %+v", evs)
		}
	})
	t.Run("only llama is protected, and only a stop, and only in survival", func(t *testing.T) {
		for _, c := range []struct{ mode, path string }{
			{"survival", "/services/kiwix/stop"},
			{"survival", "/services/whisper/stop"},
			{"survival", "/services/llama/restart"},
			{"survival", "/services/llama/start"},
			{"online", "/services/llama/stop"},
			{"offline", "/services/llama/stop"},
		} {
			r := newSvcRig(t)
			r.mode = c.mode
			if rec := r.post(c.path, "{}", nil); rec.Code != 200 {
				t.Errorf("%s in %s: status %d: %s", c.path, c.mode, rec.Code, rec.Body)
			}
		}
	})
	t.Run("no mode source means never survival", func(t *testing.T) {
		r := newSvcRig(t, func(d *ServicesDeps) { d.Mode = nil })
		if rec := r.post("/services/llama/stop", "{}", nil); rec.Code != 200 {
			t.Errorf("status %d: %s", rec.Code, rec.Body)
		}
	})
}

func TestServicesLlamaGoesThroughTheHook(t *testing.T) {
	t.Run("restart restores the models", func(t *testing.T) {
		r := newSvcRig(t)
		rec := r.post("/services/llama/restart", "{}", nil)
		if rec.Code != 200 {
			t.Fatalf("status %d: %s", rec.Code, rec.Body)
		}
		got := decodeMap(t, rec)
		if restored, _ := got["restored_models"].([]any); len(restored) != 2 || restored[0] != "model-a" || got["active"] != "active" {
			t.Errorf("body = %v", got)
		}
		if calls := r.llama.callList(); len(calls) != 1 || calls[0] != "op_7k2m9q4r8t1c restart" {
			t.Errorf("hook calls = %v", calls)
		}
		if c := r.mgr.Calls(); len(c) == 0 || c[0].Verb != services.VerbRestart || c[0].Unit != services.UnitLlama {
			t.Errorf("manager calls = %v", c)
		}
	})
	t.Run("start and stop go through it too", func(t *testing.T) {
		r := newSvcRig(t)
		r.post("/services/llama/start", "{}", nil)
		r.post("/services/llama/stop", "{}", nil)
		if calls := r.llama.callList(); len(calls) != 2 || !strings.HasSuffix(calls[0], " start") || !strings.HasSuffix(calls[1], " stop") {
			t.Errorf("hook calls = %v", calls)
		}
	})
	t.Run("without a hook the unit is restarted and nothing is restored", func(t *testing.T) {
		r := newSvcRig(t, func(d *ServicesDeps) { d.Llama = nil })
		rec := r.post("/services/llama/restart", "{}", nil)
		got := decodeMap(t, rec)
		if rec.Code != 200 || len(got["restored_models"].([]any)) != 0 || got["active"] != "active" {
			t.Errorf("status %d body %v", rec.Code, got)
		}
	})
	t.Run("a busy model lock is 409 LAI-AGT-022 with the holder's op id", func(t *testing.T) {
		r := newSvcRig(t)
		r.llama.skipOp = true
		r.llama.err = &BusyError{OpID: "op_holder0000ab"}
		e := expectError(t, r.post("/services/llama/restart", "{}", nil), 409, CodeBusy)
		if e.Error.Details["op_id"] != "op_holder0000ab" {
			t.Errorf("details = %v", e.Error.Details)
		}
		if len(r.mgr.Calls()) != 0 {
			t.Errorf("the unit was touched while the lock was busy: %v", r.mgr.Calls())
		}
		if evs := r.audit.events(); len(evs) != 1 || evs[0].Result != audit.ResultFail || evs[0].Code != CodeBusy {
			t.Errorf("audit = %+v", evs)
		}
	})
	t.Run("an API error from the hook keeps its status and code", func(t *testing.T) {
		r := newSvcRig(t)
		r.llama.err = NewError(CodeUpstreamTimeout, http.StatusGatewayTimeout, map[string]any{"model": "model-a"})
		e := expectError(t, r.post("/services/llama/restart", "{}", nil), 504, CodeUpstreamTimeout)
		if e.Error.Details["model"] != "model-a" {
			t.Errorf("details = %v", e.Error.Details)
		}
	})
}

func TestServicesActionFailures(t *testing.T) {
	job := func(unit string, verb services.Verb) error {
		return &services.JobError{Unit: unit, Verb: verb, Result: "failed"}
	}
	tests := []struct {
		name   string
		path   string
		setup  func(m *svcfake.Manager)
		status int
		code   string
		reason string
	}{
		{"llama start fails", "/services/llama/start", func(m *svcfake.Manager) {
			m.Fail(services.VerbStart, services.UnitLlama, job(services.UnitLlama, services.VerbStart))
		}, 502, "LAI-LLM-001", "job_failed"},
		{"kiwix restart fails", "/services/kiwix/restart", func(m *svcfake.Manager) {
			m.Fail(services.VerbRestart, services.UnitKiwix, job(services.UnitKiwix, services.VerbRestart))
		}, 502, "LAI-SVC-002", "job_failed"},
		{"whisper start fails", "/services/whisper/start", func(m *svcfake.Manager) {
			m.Fail(services.VerbStart, services.UnitWhisper, job(services.UnitWhisper, services.VerbStart))
		}, 502, "LAI-SVC-011", "job_failed"},
		{"whisper start never settles", "/services/whisper/start", func(m *svcfake.Manager) {
			m.Freeze(services.UnitWhisper)
			m.Set(services.UnitState{Unit: services.UnitWhisper, ActiveState: "activating", SubState: "start"})
		}, 502, "LAI-SVC-011", "timeout"},
		{"kiwix lands in failed", "/services/kiwix/start", func(m *svcfake.Manager) {
			m.Freeze(services.UnitKiwix)
			m.Set(services.UnitState{Unit: services.UnitKiwix, ActiveState: "failed", SubState: "failed"})
		}, 502, "LAI-SVC-002", "wrong_state"},
		{"a stop that fails is internal", "/services/kiwix/stop", func(m *svcfake.Manager) {
			m.Fail(services.VerbStop, services.UnitKiwix, job(services.UnitKiwix, services.VerbStop))
		}, 500, "LAI-AGT-099", "job_failed"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := newSvcRig(t)
			tc.setup(r.mgr)
			e := expectError(t, r.post(tc.path, "{}", nil), tc.status, tc.code)
			if e.Error.Details["reason"] != tc.reason || e.Error.Details["op_id"] != "op_7k2m9q4r8t1c" || e.Error.Details["unit"] == nil || e.Error.Details["action"] == nil {
				t.Errorf("details = %v", e.Error.Details)
			}
			evs := r.audit.events()
			if len(evs) != 1 || evs[0].Result != audit.ResultFail || evs[0].Code != tc.code {
				t.Errorf("audit = %+v", evs)
			}
		})
	}
}

func TestServicesAllowlistViolationIsInternal(t *testing.T) {
	// The handler only ever names the three units; a Manager that refuses one is a bug: 500, not 502.
	r := newSvcRig(t)
	r.mgr.Fail(services.VerbStart, services.UnitKiwix, &services.ViolationError{Unit: services.UnitKiwix, Verb: services.VerbStart})
	rec := r.post("/services/kiwix/start", "{}", nil)
	expectError(t, rec, 500, CodeInternal)
}

func TestServicesErrorsAreLocalized(t *testing.T) {
	r := newSvcRig(t)
	r.mgr.Fail(services.VerbStart, services.UnitKiwix, &services.JobError{Unit: services.UnitKiwix, Verb: services.VerbStart, Result: "failed"})
	en := decodeErr(t, r.post("/services/kiwix/start", "{}", nil))
	es := decodeErr(t, r.post("/services/kiwix/start", "{}", map[string]string{"Accept-Language": "es"}))
	if en.Error.Code != "LAI-SVC-002" || es.Error.Code != "LAI-SVC-002" {
		t.Fatalf("codes %s %s", en.Error.Code, es.Error.Code)
	}
	if !strings.Contains(en.Error.Message, "library") || !strings.Contains(es.Error.Message, "biblioteca") {
		t.Errorf("messages: %q / %q", en.Error.Message, es.Error.Message)
	}
	for _, code := range []string{services.CodeLlamaNotRunning, services.CodeLibraryNotRunning, services.CodeSpeechNotRunning} {
		for _, lang := range []string{langEN, langES} {
			if tx := lookup(code, lang); tx.Message == unknownText[lang].Message || tx.Fix == "" {
				t.Errorf("%s/%s has no message of its own: %+v", code, lang, tx)
			}
		}
	}
}

func TestServicesAuditFailureDoesNotFailTheAction(t *testing.T) {
	r := newSvcRig(t)
	r.audit.fail = errors.New("disk full")
	if rec := r.post("/services/kiwix/start", "{}", nil); rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if len(r.audit.events()) != 1 {
		t.Error("the record was not attempted")
	}
}

func TestServicesAuditActors(t *testing.T) {
	tests := []struct {
		principal string
		kind      audit.ActorKind
		via       string
		uid       *int
	}{
		{"bearer", audit.KindOperator, audit.ViaBearer, nil},
		{"session", audit.KindOperator, audit.ViaSession, nil},
		{"socket", audit.KindOperator, audit.ViaSocket, nil},
		{"admin", audit.KindAdmin, audit.ViaSocket, nil},
	}
	for _, tc := range tests {
		r := newSvcRig(t)
		headers := map[string]string{"X-Test-Principal": tc.principal}
		if tc.principal == "session" {
			headers["Origin"] = "http://127.0.0.1:7070"
		}
		if rec := r.post("/services/kiwix/start", "{}", headers); rec.Code != 200 {
			t.Fatalf("%s: status %d: %s", tc.principal, rec.Code, rec.Body)
		}
		a := r.audit.events()[0].Actor
		if a.Kind != tc.kind || a.Via != tc.via {
			t.Errorf("%s: actor = %+v", tc.principal, a)
		}
	}
	// A socket request carries the peer's uid into the audit record.
	r := newSvcRig(t)
	rec := do(r.h, req{method: "POST", target: "/api/v1/services/kiwix/start", body: "{}", header: jsonHeaders(map[string]string{"X-Test-Principal": "socket"}), socket: &PeerCred{UID: 1000, GID: 1000, PID: 42}})
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if a := r.audit.events()[0].Actor; a.UID == nil || *a.UID != 1000 {
		t.Errorf("actor = %+v", a)
	}
}

func TestServicesRegistration(t *testing.T) {
	if err := RegisterServices(NewRegistry(), ServicesDeps{}); err == nil {
		t.Error("registered without a status source and an action runner")
	}
	reg := NewRegistry()
	deps := ServicesDeps{Status: &staticStatus{}, Actions: &services.Controller{Manager: svcfake.New()}}
	if err := RegisterServices(reg, deps); err != nil {
		t.Fatal(err)
	}
	if err := RegisterServices(reg, deps); err == nil {
		t.Error("the same routes were registered twice")
	}
	routes := reg.Routes()
	if len(routes) != 2 {
		t.Fatalf("routes = %+v", routes)
	}
	for _, rt := range routes {
		if len(rt.Auth) != 1 || rt.Auth[0] != AuthOp || rt.Handler == nil {
			t.Errorf("route %s %s: %+v", rt.Method, rt.Pattern, rt)
		}
		switch rt.Method + " " + rt.Pattern {
		case "GET /services":
			if rt.Timeout != 5*time.Second || rt.Mutating() {
				t.Errorf("GET /services: %+v", rt)
			}
		case "POST /services/{name}/{action}":
			if rt.Timeout != 120*time.Second || !rt.Mutating() || rt.GateExempt {
				t.Errorf("POST: %+v", rt)
			}
		default:
			t.Errorf("unexpected route %s %s", rt.Method, rt.Pattern)
		}
	}
}

func TestServicesOpIDDefault(t *testing.T) {
	id := servicesOpID()
	if len(id) != 15 || !strings.HasPrefix(id, "op_") || id == servicesOpID() {
		t.Errorf("op id = %q", id)
	}
	for _, c := range id[3:] {
		if !strings.ContainsRune(requestIDAlphabet, c) {
			t.Errorf("op id %q has %q outside a-z2-7", id, c)
		}
	}
	if d := (ServicesDeps{}); !strings.HasPrefix(d.opID(), "op_") {
		t.Errorf("default op id = %q", d.opID())
	}
}
