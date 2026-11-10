package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
)

type fakeEnsure struct {
	views    []string
	starts   []EnsureStartRequest
	cancels  int
	running  bool // what EnsureStart and EnsureCancel report
	startErr error
}

// okView is a picture that matches the schema of the document.
func okView(state, message string) map[string]any {
	return map[string]any{
		"state": state, "auto": "on", "message": message, "next_action": map[string]any{"kind": "none", "text": "Nothing to do."},
		"progress": map[string]any{"done": 0, "total": 0}, "steps": []any{}, "run": nil, "dry_run": false, "checked_at": "2026-11-10T12:00:00Z",
	}
}

func (f *fakeEnsure) EnsureView(_ context.Context, lang string) (any, error) {
	f.views = append(f.views, lang)
	return okView("complete", lang), nil
}
func (f *fakeEnsure) EnsureStart(_ context.Context, req EnsureStartRequest) (any, bool, error) {
	f.starts = append(f.starts, req)
	return okView("installing", "Installing what's missing"), f.running, f.startErr
}
func (f *fakeEnsure) EnsureCancel(context.Context, string) (any, bool, error) {
	f.cancels++
	return okView("complete", "Complete"), f.running, nil
}

func newEnsureRig(t *testing.T) (*fakeEnsure, http.Handler) {
	t.Helper()
	f := &fakeEnsure{}
	reg := NewRegistry()
	if err := RegisterEnsure(reg, EnsureDeps{Engine: f, NewOpID: func() string { return "op_7k2m9q4r8t1c" }}); err != nil {
		t.Fatal(err)
	}
	s, _ := newTestServer(t, func(o *Options) { o.Registry = reg })
	return f, s.Handler()
}

func ensureReq(h http.Handler, method, principal, body string, extra map[string]string) *httptest.ResponseRecorder {
	hd := jsonHeaders(map[string]string{"X-Test-Principal": principal})
	if principal == "portal" {
		hd["Origin"] = "http://127.0.0.1:7070"
	}
	for k, v := range extra {
		hd[k] = v
	}
	return do(h, req{method: method, target: "/api/v1/ensure", body: body, header: hd})
}

func TestEnsureRouteMetadata(t *testing.T) {
	routes := EnsureRoutes(EnsureDeps{})
	want := map[string]time.Duration{http.MethodGet: 5 * time.Second, http.MethodPost: 10 * time.Second, http.MethodDelete: 10 * time.Second}
	if len(routes) != 3 {
		t.Fatalf("routes = %d", len(routes))
	}
	for _, rt := range routes {
		if rt.Pattern != "/ensure" || !reflect.DeepEqual(rt.Auth, []Level{AuthOp, AuthPortal}) || rt.Timeout != want[rt.Method] {
			t.Errorf("route = %+v", rt)
		}
	}
	if err := RegisterEnsure(NewRegistry(), EnsureDeps{}); err == nil {
		t.Fatal("RegisterEnsure must refuse a missing engine")
	}
}

func TestEnsureGetUsesTheLanguageOfTheRequest(t *testing.T) {
	f, h := newEnsureRig(t)
	rec := recordOK(t, ensureReq(h, http.MethodGet, "bearer", "", map[string]string{"Accept-Language": "es-CR,es;q=0.9"}))
	if rec.Code != http.StatusOK || decodeMap(t, rec)["message"] != "es" || len(f.views) != 1 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	rec = recordOK(t, ensureReq(h, http.MethodGet, "portal", "", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("a Portal session may read: %d", rec.Code)
	}
}

func TestEnsurePostStartsAndSaysWhetherAPassRuns(t *testing.T) {
	f, h := newEnsureRig(t)
	f.running = true
	rec := recordOK(t, ensureReq(h, http.MethodPost, "bearer", `{}`, nil))
	if rec.Code != http.StatusAccepted || rec.Header().Get("X-LocalAI-Op-Id") != "op_7k2m9q4r8t1c" {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	f.running = false
	if rec = recordOK(t, ensureReq(h, http.MethodPost, "bearer", `{"dry_run": true}`, nil)); rec.Code != http.StatusOK {
		t.Fatalf("a dry run, or nothing to do, is 200: %d", rec.Code)
	}
	if len(f.starts) != 2 || !f.starts[1].DryRun || f.starts[0].OpID != "op_7k2m9q4r8t1c" || f.starts[0].Actor.Kind != "operator" {
		t.Errorf("starts %+v", f.starts)
	}
	// No body at all is a start.
	if rec = recordOK(t, do(h, req{method: "POST", target: "/api/v1/ensure", header: jsonHeaders(nil)})); rec.Code != http.StatusOK || len(f.starts) != 3 {
		t.Errorf("empty body: %d %s", rec.Code, rec.Body)
	}
}

func TestEnsureConfirmationNeedsAdminStar(t *testing.T) {
	f, h := newEnsureRig(t)
	body := `{"confirm": ["whisper.build"]}`
	// An operator with a token (over TCP): refused with a hint to give the PIN.
	rec := recordOK(t, ensureReq(h, http.MethodPost, "bearer", body, nil))
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), doctor.SEC002) || !strings.Contains(rec.Body.String(), `"pin_required":true`) {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if len(f.starts) != 0 {
		t.Fatal("the engine was asked")
	}
	for _, p := range []string{"socket", "admin"} {
		f.starts = nil
		rec = recordOK(t, ensureReq(h, http.MethodPost, p, body, nil))
		if rec.Code != http.StatusOK || len(f.starts) != 1 || !reflect.DeepEqual(f.starts[0].Confirm, []string{"whisper.build"}) {
			t.Errorf("%s: %d %s %+v", p, rec.Code, rec.Body, f.starts)
		}
	}
	// A dry run changes nothing, so it needs no more than op.
	f.starts = nil
	if rec = recordOK(t, ensureReq(h, http.MethodPost, "bearer", `{"dry_run": true, "confirm": ["whisper.build"]}`, nil)); rec.Code != http.StatusOK || len(f.starts) != 1 {
		t.Errorf("dry run: %d %s", rec.Code, rec.Body)
	}
}

func TestEnsureFromThePortalIgnoresTheBody(t *testing.T) {
	f, h := newEnsureRig(t)
	f.running = true
	rec := recordOK(t, ensureReq(h, http.MethodPost, "portal", `{"dry_run": true, "confirm": ["whisper.build"], "unknown": 1}`, nil))
	if rec.Code != http.StatusAccepted || len(f.starts) != 1 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if got := f.starts[0]; got.DryRun || len(got.Confirm) != 0 || got.Actor.Kind != "portal" {
		t.Errorf("the Portal's request was %+v", got)
	}
}

func TestEnsureRefusesABadBodyBeforeTheEngine(t *testing.T) {
	f, h := newEnsureRig(t)
	many := `{"confirm": ["a","b","c","d","e","f","g","h","i"]}`
	long := `{"confirm": ["` + strings.Repeat("x", 65) + `"]}`
	for name, body := range map[string]string{"unknown field": `{"nope": 1}`, "not json": `{`, "too many": many, "empty id": `{"confirm": [" "]}`, "long id": long, "a string": `{"confirm": "whisper.build"}`} {
		rec := recordOK(t, ensureReq(h, http.MethodPost, "socket", body, nil))
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), doctor.AGT020) {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
	if len(f.starts) != 0 {
		t.Errorf("the engine was asked %d times", len(f.starts))
	}
	if rec := recordOK(t, ensureReq(h, http.MethodPost, "socket", `{"confirm": ["x"]}`, map[string]string{"Content-Type": "text/plain"})); rec.Code != http.StatusUnsupportedMediaType {
		t.Errorf("content type: %d", rec.Code)
	}
}

func TestEnsureErrorsOfTheEnginePassThrough(t *testing.T) {
	f, h := newEnsureRig(t)
	f.startErr = NewError(doctor.SYN005, http.StatusConflict, map[string]any{"run_id": "run-1"})
	rec := recordOK(t, ensureReq(h, http.MethodPost, "bearer", `{}`, nil))
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), doctor.SYN005) {
		t.Errorf("%d %s", rec.Code, rec.Body)
	}
}

func TestEnsureDeleteCancels(t *testing.T) {
	f, h := newEnsureRig(t)
	f.running = true
	rec := recordOK(t, ensureReq(h, http.MethodDelete, "bearer", "", nil))
	if rec.Code != http.StatusAccepted || f.cancels != 1 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	f.running = false
	if rec = recordOK(t, ensureReq(h, http.MethodDelete, "portal", "", nil)); rec.Code != http.StatusOK {
		t.Errorf("idle: %d", rec.Code)
	}
}

func TestEnsureNeedsACredential(t *testing.T) {
	_, h := newEnsureRig(t)
	for _, m := range []string{http.MethodGet, http.MethodPost, http.MethodDelete} {
		if rec := recordOK(t, ensureReq(h, m, "", "", nil)); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s: %d", m, rec.Code)
		}
	}
}
