package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/audit"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
)

// fakeDriveOps records the requests of the four drive routes and answers what the test set.
type fakeDriveOps struct {
	eject   []ArkEjectRequest
	init    []ArkDriveRequest
	mount   []ArkDriveRequest
	profile []ArkProfileRequest

	err   error
	drive any
}

func (f *fakeDriveOps) Eject(_ context.Context, req ArkEjectRequest) error {
	f.eject = append(f.eject, req)
	return f.err
}

func (f *fakeDriveOps) Init(_ context.Context, req ArkDriveRequest) (any, error) {
	f.init = append(f.init, req)
	return f.drive, f.err
}

func (f *fakeDriveOps) Mount(_ context.Context, req ArkDriveRequest) (any, error) {
	f.mount = append(f.mount, req)
	return f.drive, f.err
}

func (f *fakeDriveOps) SetProfile(_ context.Context, req ArkProfileRequest) (ArkProfileResult, error) {
	f.profile = append(f.profile, req)
	return ArkProfileResult{RootID: req.RootID, Profile: req.Profile}, f.err
}

type arkDriveRig struct {
	t     *testing.T
	ops   *fakeDriveOps
	audit *recAudit
	h     http.Handler
}

func newArkDriveRig(t *testing.T) *arkDriveRig {
	t.Helper()
	r := &arkDriveRig{t: t, audit: &recAudit{}, ops: &fakeDriveOps{drive: map[string]any{"root_id": "ark-3f9c0a12", "device": "/dev/sdb1", "mount": "/media/operator/AI-ARK", "state": "ready"}}}
	reg := NewRegistry()
	if err := RegisterArkDrive(reg, ArkDriveDeps{Ops: r.ops, Audit: r.audit, NewOpID: func() string { return "op_7k2m9q4r8t1c" }}); err != nil {
		t.Fatal(err)
	}
	s, _ := newTestServer(t, func(o *Options) { o.Registry = reg })
	r.h = s.Handler()
	return r
}

func (r *arkDriveRig) call(method, action, principal, body string) *httptest.ResponseRecorder {
	h := jsonHeaders(map[string]string{"X-Test-Principal": principal})
	return do(r.h, req{method: method, target: "/api/v1/ark/drives/ark-3f9c0a12/" + action, body: body, header: h})
}

func TestArkDriveRouteMetadata(t *testing.T) {
	want := map[string]struct {
		method string
		level  Level
	}{
		"/ark/drives/{root_id}/eject":   {http.MethodPost, AuthOp},
		"/ark/drives/{root_id}/init":    {http.MethodPost, AuthOp},
		"/ark/drives/{root_id}/mount":   {http.MethodPost, AuthOp},
		"/ark/drives/{root_id}/profile": {http.MethodPut, AuthAdmin},
	}
	routes := ArkDriveRoutes(ArkDriveDeps{})
	if len(routes) != len(want) {
		t.Fatalf("routes = %d", len(routes))
	}
	for _, rt := range routes {
		w, ok := want[rt.Pattern]
		if !ok || rt.Method != w.method || len(rt.Auth) != 1 || rt.Auth[0] != w.level || rt.Timeout != 60*time.Second {
			t.Errorf("route = %+v, want %+v", rt, w)
		}
	}
	if err := RegisterArkDrive(NewRegistry(), ArkDriveDeps{}); err == nil {
		t.Fatal("RegisterArkDrive must refuse a missing operations implementation")
	}
}

// Eject answers 202 {"state":"ejecting"}, passes force, and is audited; the body is optional.
func TestArkDriveEject(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		force      bool
	}{
		{"no body", ``, false},
		{"empty object", `{}`, false},
		{"force false", `{"force": false}`, false},
		{"force true", `{"force": true}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newArkDriveRig(t)
			rec := recordOK(t, r.call("POST", "eject", "bearer", tc.body))
			if rec.Code != http.StatusAccepted || rec.Header().Get("X-LocalAI-Op-Id") != "op_7k2m9q4r8t1c" {
				t.Fatalf("status %d, op id %q, body %s", rec.Code, rec.Header().Get("X-LocalAI-Op-Id"), rec.Body)
			}
			if m := decodeMap(t, rec); len(m) != 1 || m["state"] != "ejecting" {
				t.Errorf("body = %v", m)
			}
			if len(r.ops.eject) != 1 {
				t.Fatalf("eject calls = %d", len(r.ops.eject))
			}
			got := r.ops.eject[0]
			if got.RootID != "ark-3f9c0a12" || got.Force != tc.force || got.OpID != "op_7k2m9q4r8t1c" {
				t.Errorf("request = %+v", got)
			}
			evs := r.audit.events()
			if len(evs) != 1 || evs[0].Action != audit.ActionArkEject || evs[0].Result != audit.ResultOK ||
				evs[0].Target["root_id"] != "ark-3f9c0a12" || evs[0].Target["force"] != strconv.FormatBool(tc.force) {
				t.Fatalf("audit = %+v", evs)
			}
		})
	}
}

// Init and mount answer 200 with the drive object the operations returned.
func TestArkDriveInitAndMount(t *testing.T) {
	for _, tc := range []struct {
		action string
		audit  string
		calls  func(*fakeDriveOps) int
	}{
		{"init", audit.ActionArkInit, func(f *fakeDriveOps) int { return len(f.init) }},
		{"mount", audit.ActionArkMount, func(f *fakeDriveOps) int { return len(f.mount) }},
	} {
		t.Run(tc.action, func(t *testing.T) {
			r := newArkDriveRig(t)
			rec := recordOK(t, r.call("POST", tc.action, "bearer", `{}`))
			if rec.Code != http.StatusOK {
				t.Fatalf("status %d: %s", rec.Code, rec.Body)
			}
			if m := decodeMap(t, rec); m["root_id"] != "ark-3f9c0a12" || m["state"] != "ready" {
				t.Errorf("body = %v", m)
			}
			if tc.calls(r.ops) != 1 {
				t.Errorf("calls = %d", tc.calls(r.ops))
			}
			if evs := r.audit.events(); len(evs) != 1 || evs[0].Action != tc.audit || evs[0].Result != audit.ResultOK {
				t.Fatalf("audit = %+v", evs)
			}
			// The body is exactly {}: a field, an empty body and trailing data are all 400.
			for _, bad := range []string{``, `{"x": 1}`, `[]`} {
				expectError(t, r.call("POST", tc.action, "bearer", bad), http.StatusBadRequest, doctor.AGT020)
			}
			if tc.calls(r.ops) != 1 {
				t.Error("a bad body reached the operation")
			}
		})
	}
}

// The profile route needs the admin PIN, validates the id, and answers {root_id, profile}.
func TestArkDriveProfile(t *testing.T) {
	t.Run("an operator without the PIN is refused before the handler", func(t *testing.T) {
		r := newArkDriveRig(t)
		expectError(t, r.call("PUT", "profile", "bearer", `{"profile":"ark-drive-full"}`), http.StatusForbidden, CodeAdminRequired)
		expectError(t, do(r.h, req{method: "PUT", target: "/api/v1/ark/drives/ark-3f9c0a12/profile", body: `{"profile":"x"}`, header: map[string]string{"Content-Type": "application/json"}}), http.StatusUnauthorized, CodeUnauthenticated)
		if len(r.ops.profile) != 0 || len(r.audit.events()) != 0 {
			t.Fatalf("a refused request reached the operation (%d) or the audit (%d)", len(r.ops.profile), len(r.audit.events()))
		}
	})
	t.Run("an administrator sets it", func(t *testing.T) {
		r := newArkDriveRig(t)
		rec := recordOK(t, r.call("PUT", "profile", "admin", `{"profile":"ark-drive-full"}`))
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d: %s", rec.Code, rec.Body)
		}
		if m := decodeMap(t, rec); len(m) != 2 || m["root_id"] != "ark-3f9c0a12" || m["profile"] != "ark-drive-full" {
			t.Errorf("body = %v", m)
		}
		evs := r.audit.events()
		if len(evs) != 1 || evs[0].Action != audit.ActionArkProfileSet || evs[0].Actor.Kind != audit.KindAdmin || evs[0].Target["profile"] != "ark-drive-full" {
			t.Fatalf("audit = %+v", evs)
		}
	})
	t.Run("validation", func(t *testing.T) {
		for name, body := range map[string]string{
			"missing":      `{}`,
			"empty":        `{"profile": ""}`,
			"space":        `{"profile": "a b"}`,
			"too long":     `{"profile": "` + strings.Repeat("x", maxProfileIDLen+1) + `"}`,
			"control char": `{"profile": "a\u0001b"}`,
			"unknown key":  `{"profile": "x", "force": true}`,
			"empty body":   ``,
		} {
			r := newArkDriveRig(t)
			expectError(t, r.call("PUT", "profile", "admin", body), http.StatusBadRequest, doctor.AGT020)
			if len(r.ops.profile) != 0 {
				t.Errorf("%s: reached the operation", name)
			}
		}
	})
}

// Errors of the operations pass through unchanged and every refusal is audited with its code.
func TestArkDriveErrorsPassThroughAndAreAudited(t *testing.T) {
	blocked := NewError(doctor.ARK009, http.StatusConflict, map[string]any{
		"blockers": []map[string]any{{"kind": "process", "name": "bash", "pid": 4242}},
	})
	for _, tc := range []struct {
		name, method, action, principal, body string
		err                                   error
		status                                int
		code                                  string
	}{
		{"eject blocked", "POST", "eject", "bearer", `{}`, blocked, http.StatusConflict, doctor.ARK009},
		{"eject unknown drive", "POST", "eject", "bearer", `{}`, NewError(CodeNotFound, http.StatusNotFound, nil), http.StatusNotFound, CodeNotFound},
		{"init wrong state", "POST", "init", "bearer", `{}`, NewError(CodeInvalidRequest, http.StatusConflict, map[string]any{"state": "ready"}), http.StatusConflict, CodeInvalidRequest},
		{"mount refused", "POST", "mount", "bearer", `{}`, NewError(doctor.ARK005, http.StatusConflict, nil), http.StatusConflict, doctor.ARK005},
		{"profile unknown", "PUT", "profile", "admin", `{"profile":"nope"}`, NewError(doctor.CAT003, http.StatusUnprocessableEntity, nil), http.StatusUnprocessableEntity, doctor.CAT003},
		{"a bug", "POST", "eject", "bearer", `{}`, errors.New("boom"), http.StatusInternalServerError, CodeInternal},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newArkDriveRig(t)
			r.ops.err = tc.err
			rec := r.call(tc.method, tc.action, tc.principal, tc.body)
			e := expectError(t, rec, tc.status, tc.code)
			if tc.name == "eject blocked" {
				bl, _ := e.Error.Details["blockers"].([]any)
				if len(bl) != 1 {
					t.Errorf("details = %v", e.Error.Details)
				}
			}
			evs := r.audit.events()
			if len(evs) != 1 || evs[0].Result != audit.ResultFail || evs[0].Code != tc.code {
				t.Fatalf("audit = %+v", evs)
			}
		})
	}
}
