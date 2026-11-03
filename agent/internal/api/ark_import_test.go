package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/audit"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
)

type fakeImporter struct {
	dry     []ArkImportRequest
	started []ArkImportRequest
	plans   any
	run     ArkSyncStarted
	err     error
}

func (f *fakeImporter) DryRunImport(_ context.Context, req ArkImportRequest) (any, error) {
	f.dry = append(f.dry, req)
	return f.plans, f.err
}

func (f *fakeImporter) StartImport(_ context.Context, req ArkImportRequest) (ArkSyncStarted, error) {
	f.started = append(f.started, req)
	return f.run, f.err
}

type arkImportRig struct {
	t     *testing.T
	imp   *fakeImporter
	audit *recAudit
	h     http.Handler
}

func newArkImportRig(t *testing.T) *arkImportRig {
	t.Helper()
	r := &arkImportRig{t: t, audit: &recAudit{}, imp: &fakeImporter{
		run:   ArkSyncStarted{RunID: "run-20261102T031500Z-b7c2"},
		plans: []map[string]any{{"root": "internal", "profile": "laptop-offline", "items": []any{}}},
	}}
	reg := NewRegistry()
	if err := RegisterArkImport(reg, ArkImportDeps{Importer: r.imp, Audit: r.audit, NewOpID: func() string { return "op_7k2m9q4r8t1c" }}); err != nil {
		t.Fatal(err)
	}
	s, _ := newTestServer(t, func(o *Options) { o.Registry = reg })
	r.h = s.Handler()
	return r
}

func (r *arkImportRig) post(principal, body string) *httptest.ResponseRecorder {
	h := jsonHeaders(map[string]string{"X-Test-Principal": principal})
	return do(r.h, req{method: "POST", target: "/api/v1/ark/import", body: body, header: h})
}

func TestArkImportRouteMetadata(t *testing.T) {
	routes := ArkImportRoutes(ArkImportDeps{})
	if len(routes) != 1 {
		t.Fatalf("routes = %d", len(routes))
	}
	rt := routes[0]
	if rt.Method != http.MethodPost || rt.Pattern != "/ark/import" || len(rt.Auth) != 1 || rt.Auth[0] != AuthOp || rt.Timeout != 10*time.Second {
		t.Errorf("route = %+v", rt)
	}
	if err := RegisterArkImport(NewRegistry(), ArkImportDeps{}); err == nil {
		t.Fatal("RegisterArkImport must refuse a missing importer")
	}
}

func TestArkImportStartsARunAnswers202AndAudits(t *testing.T) {
	r := newArkImportRig(t)
	rec := recordOK(t, r.post("bearer", `{"from_root": "ark-3f9c0a12", "items": ["whisper-small"], "catalog": true}`))
	if rec.Code != http.StatusAccepted || rec.Header().Get("X-LocalAI-Op-Id") != "op_7k2m9q4r8t1c" {
		t.Fatalf("status %d, op id %q, body %s", rec.Code, rec.Header().Get("X-LocalAI-Op-Id"), rec.Body)
	}
	if m := decodeMap(t, rec); m["run_id"] != "run-20261102T031500Z-b7c2" || m["state"] != "running" {
		t.Errorf("body = %v", m)
	}
	if len(r.imp.started) != 1 || len(r.imp.dry) != 0 {
		t.Fatalf("started %d, dry %d", len(r.imp.started), len(r.imp.dry))
	}
	got := r.imp.started[0]
	if got.FromRoot != "ark-3f9c0a12" || strings.Join(got.Items, ",") != "whisper-small" || got.AddToProfile || !got.Catalog || got.DryRun || got.OpID != "op_7k2m9q4r8t1c" {
		t.Errorf("request = %+v", got)
	}
	evs := r.audit.events()
	if len(evs) != 1 || evs[0].Action != audit.ActionArkImport || evs[0].Result != audit.ResultOK || evs[0].Target["from_root"] != "ark-3f9c0a12" ||
		evs[0].Target["items"] != "1" || evs[0].Target["catalog"] != "true" || evs[0].Target["run_id"] != "run-20261102T031500Z-b7c2" {
		t.Fatalf("audit = %+v", evs)
	}
}

func TestArkImportWithoutASourceLeavesTheChoiceToTheAgent(t *testing.T) {
	r := newArkImportRig(t)
	if rec := recordOK(t, r.post("bearer", `{}`)); rec.Code != http.StatusAccepted {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if got := r.imp.started[0]; got.FromRoot != "" || len(got.Items) != 0 || got.AddToProfile || got.Catalog {
		t.Errorf("request = %+v", got)
	}
}

func TestArkImportDryRunAnswers200WithThePlanAndIsNotAudited(t *testing.T) {
	r := newArkImportRig(t)
	rec := recordOK(t, r.post("bearer", `{"from_root": "ark-3f9c0a12", "items": ["whisper-small"], "dry_run": true}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if body := rec.Body.String(); !strings.Contains(body, `"root":"internal"`) || !strings.HasPrefix(body, "[") {
		t.Errorf("body = %s", body)
	}
	if len(r.imp.dry) != 1 || len(r.imp.started) != 0 || !r.imp.dry[0].DryRun || len(r.audit.events()) != 0 {
		t.Errorf("dry %d, started %d, audit %d", len(r.imp.dry), len(r.imp.started), len(r.audit.events()))
	}
}

// add_to_profile changes node.yaml: an operator needs admin* (the socket's operators have it, the
// others the PIN); a dry run changes nothing and needs nothing.
func TestArkImportAddToProfileNeedsAdminStarAndItems(t *testing.T) {
	body := `{"from_root": "ark-3f9c0a12", "items": ["whisper-small"], "add_to_profile": true}`

	t.Run("an operator over TCP without the PIN is refused before anything happens", func(t *testing.T) {
		r := newArkImportRig(t)
		e := expectError(t, r.post("bearer", body), http.StatusForbidden, CodeAdminRequired)
		if e.Error.Details["pin_required"] != true {
			t.Errorf("details = %v", e.Error.Details)
		}
		if len(r.imp.started) != 0 {
			t.Fatal("the importer was reached")
		}
	})
	t.Run("a socket operator and root may", func(t *testing.T) {
		for _, who := range []string{"socket", "admin"} {
			r := newArkImportRig(t)
			rec := recordOK(t, r.post(who, body))
			if rec.Code != http.StatusAccepted || len(r.imp.started) != 1 || !r.imp.started[0].AddToProfile {
				t.Fatalf("%s: %d %s", who, rec.Code, rec.Body)
			}
		}
	})
	t.Run("a dry run needs no PIN", func(t *testing.T) {
		r := newArkImportRig(t)
		rec := recordOK(t, r.post("bearer", strings.Replace(body, "}", `, "dry_run": true}`, 1)))
		if rec.Code != http.StatusOK || len(r.imp.dry) != 1 || !r.imp.dry[0].AddToProfile {
			t.Fatalf("%d %s", rec.Code, rec.Body)
		}
	})
	t.Run("without items it is a mistake", func(t *testing.T) {
		r := newArkImportRig(t)
		e := expectError(t, r.post("admin", `{"add_to_profile": true}`), http.StatusBadRequest, doctor.AGT020)
		if e.Error.Details["field"] != "items" || e.Error.Details["reason"] != "required_with_add_to_profile" {
			t.Errorf("details = %v", e.Error.Details)
		}
	})
}

func TestArkImportValidation(t *testing.T) {
	cases := []struct {
		name, body, reason, field string
	}{
		{"an empty item", `{"items": [" "]}`, "empty_value", "items"},
		{"too many items", `{"items": [` + strings.TrimSuffix(strings.Repeat(`"x",`, 257), ",") + `]}`, "too_many", "items"},
		{"empty body", ``, "empty_body", ""},
		{"an unknown field", `{"force": true}`, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newArkImportRig(t)
			e := expectError(t, r.post("bearer", tc.body), http.StatusBadRequest, doctor.AGT020)
			if tc.reason != "" && e.Error.Details["reason"] != tc.reason {
				t.Errorf("details = %v, want reason %s", e.Error.Details, tc.reason)
			}
			if tc.field != "" && e.Error.Details["field"] != tc.field {
				t.Errorf("details = %v, want field %s", e.Error.Details, tc.field)
			}
			if len(r.imp.started)+len(r.imp.dry) != 0 {
				t.Error("nothing may start for a bad request")
			}
		})
	}
}

func TestArkImportErrorsPassThroughAndStartsAreAudited(t *testing.T) {
	several := NewError(doctor.ARK015, http.StatusConflict, map[string]any{"roots": []string{"ark-0a0b0c0d", "ark-0e0f1011"}})
	for _, tc := range []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"several drives and no from_root", several, http.StatusConflict, doctor.ARK015},
		{"a run is active", NewError(doctor.SYN005, http.StatusConflict, map[string]any{"run_id": "run-20261102T031500Z-a1f3"}), http.StatusConflict, doctor.SYN005},
		{"an unknown drive", NewError(CodeInvalidRequest, http.StatusBadRequest, map[string]any{"field": "from_root", "reason": "unknown_root"}), http.StatusBadRequest, CodeInvalidRequest},
		{"a profile the catalog lacks", NewError(doctor.CAT003, http.StatusUnprocessableEntity, nil), http.StatusUnprocessableEntity, doctor.CAT003},
		{"a bug", errors.New("boom"), http.StatusInternalServerError, CodeInternal},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newArkImportRig(t)
			r.imp.err = tc.err
			e := expectError(t, r.post("bearer", `{}`), tc.status, tc.code)
			if tc.code == doctor.ARK015 {
				if roots, _ := e.Error.Details["roots"].([]any); len(roots) != 2 || roots[0] != "ark-0a0b0c0d" {
					t.Errorf("details = %v", e.Error.Details)
				}
			}
			if evs := r.audit.events(); len(evs) != 1 || evs[0].Result != audit.ResultFail {
				t.Fatalf("audit = %+v", evs)
			}
		})
	}
	t.Run("a dry run that fails is not audited", func(t *testing.T) {
		r := newArkImportRig(t)
		r.imp.err = several
		expectError(t, r.post("bearer", `{"dry_run": true}`), http.StatusConflict, doctor.ARK015)
		if len(r.audit.events()) != 0 {
			t.Fatal("audited")
		}
	})
}
