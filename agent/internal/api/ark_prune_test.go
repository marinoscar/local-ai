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

type fakePruner struct {
	reqs []ArkPruneRequest
	res  ArkPruneResult
	err  error
}

func (f *fakePruner) Prune(_ context.Context, req ArkPruneRequest) (ArkPruneResult, error) {
	f.reqs = append(f.reqs, req)
	return f.res, f.err
}

type arkPruneRig struct {
	t      *testing.T
	pruner *fakePruner
	audit  *recAudit
	h      http.Handler
}

func newArkPruneRig(t *testing.T) *arkPruneRig {
	t.Helper()
	r := &arkPruneRig{t: t, audit: &recAudit{}, pruner: &fakePruner{res: ArkPruneResult{
		Deleted:    []ArkPruned{{Root: "internal", Item: "osm-north-america", Version: "20260501", Bytes: 11200000000, Kind: "orphan"}},
		Refused:    []ArkPruneRefused{{Root: "internal", Item: "gutenberg-en", Version: "20260501", Reason: "last_verified_copy", Kind: "orphan"}},
		FreedBytes: 11200000000,
	}}}
	reg := NewRegistry()
	if err := RegisterArkPrune(reg, ArkPruneDeps{Pruner: r.pruner, Audit: r.audit, NewOpID: func() string { return "op_7k2m9q4r8t1c" }}); err != nil {
		t.Fatal(err)
	}
	s, _ := newTestServer(t, func(o *Options) { o.Registry = reg })
	r.h = s.Handler()
	return r
}

func (r *arkPruneRig) post(principal, body string) *httptest.ResponseRecorder {
	h := jsonHeaders(map[string]string{"X-Test-Principal": principal})
	return do(r.h, req{method: "POST", target: "/api/v1/ark/prune", body: body, header: h})
}

func TestArkPruneRouteMetadata(t *testing.T) {
	routes := ArkPruneRoutes(ArkPruneDeps{})
	if len(routes) != 1 {
		t.Fatalf("routes = %d", len(routes))
	}
	rt := routes[0]
	if rt.Method != http.MethodPost || rt.Pattern != "/ark/prune" || len(rt.Auth) != 1 || rt.Auth[0] != AuthAdmin || rt.Timeout != 60*time.Second {
		t.Errorf("route = %+v", rt)
	}
	if err := RegisterArkPrune(NewRegistry(), ArkPruneDeps{}); err == nil {
		t.Fatal("RegisterArkPrune must refuse a missing pruner")
	}
}

// Prune without the PIN: an operator (no admin grant) is refused with 403 admin_required before the
// handler runs (the real authenticator adds details.pin_required, proved for every admin route by
// auth's TestSocketPeer); nobody without credentials gets further than 401.
func TestArkPruneNeedsTheAdminPIN(t *testing.T) {
	r := newArkPruneRig(t)
	body := `{"orphans": true}`
	expectError(t, r.post("bearer", body), http.StatusForbidden, CodeAdminRequired)
	expectError(t, do(r.h, req{method: "POST", target: "/api/v1/ark/prune", body: body, header: map[string]string{"Content-Type": "application/json"}}), http.StatusUnauthorized, CodeUnauthenticated)
	if len(r.pruner.reqs) != 0 || len(r.audit.events()) != 0 {
		t.Fatalf("a refused request reached the pruner (%d) or the audit (%d)", len(r.pruner.reqs), len(r.audit.events()))
	}
}

func TestArkPruneAnswers200AndAudits(t *testing.T) {
	r := newArkPruneRig(t)
	rec := recordOK(t, r.post("admin", `{"roots": ["internal"], "orphans": true, "trash": true}`))
	if rec.Code != http.StatusOK || rec.Header().Get("X-LocalAI-Op-Id") != "op_7k2m9q4r8t1c" {
		t.Fatalf("status %d, op id %q, body %s", rec.Code, rec.Header().Get("X-LocalAI-Op-Id"), rec.Body)
	}
	m := decodeMap(t, rec)
	if m["freed_bytes"] != float64(11200000000) {
		t.Errorf("body = %v", m)
	}
	refused := m["refused"].([]any)[0].(map[string]any)
	if refused["reason"] != "last_verified_copy" || refused["item"] != "gutenberg-en" {
		t.Errorf("refused = %v", refused)
	}
	if len(r.pruner.reqs) != 1 {
		t.Fatalf("pruner calls = %d", len(r.pruner.reqs))
	}
	got := r.pruner.reqs[0]
	if len(got.Roots) != 1 || got.Roots[0] != "internal" || !got.Orphans || !got.Trash || got.DryRun || got.OpID != "op_7k2m9q4r8t1c" || got.Actor.Kind != audit.KindAdmin {
		t.Errorf("request = %+v", got)
	}
	evs := r.audit.events()
	if len(evs) != 1 || evs[0].Action != audit.ActionArkPrune || evs[0].Result != audit.ResultOK || evs[0].Target["deleted"] != "1" || evs[0].Target["refused"] != "1" || evs[0].Target["freed_bytes"] != "11200000000" {
		t.Fatalf("audit = %+v", evs)
	}
}

func TestArkPruneDryRunIsNotAudited(t *testing.T) {
	r := newArkPruneRig(t)
	rec := recordOK(t, r.post("admin", `{"trash": true, "dry_run": true}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if !r.pruner.reqs[0].DryRun || len(r.audit.events()) != 0 {
		t.Errorf("dry run %v, audit %d", r.pruner.reqs[0].DryRun, len(r.audit.events()))
	}
}

func TestArkPruneEmptyListsAreArraysNotNull(t *testing.T) {
	r := newArkPruneRig(t)
	r.pruner.res = ArkPruneResult{}
	rec := recordOK(t, r.post("admin", `{"orphans": true}`))
	if body := rec.Body.String(); !strings.Contains(body, `"deleted":[]`) || !strings.Contains(body, `"refused":[]`) {
		t.Errorf("body = %s", body)
	}
}

func TestArkPruneValidation(t *testing.T) {
	cases := []struct {
		name, body, reason, field string
	}{
		{"nothing selected is not 'everything'", `{}`, "nothing_selected", "orphans"},
		{"dry run alone selects nothing", `{"dry_run": true}`, "nothing_selected", "orphans"},
		{"empty root", `{"orphans": true, "roots": [" "]}`, "empty_value", "roots"},
		{"too many roots", `{"orphans": true, "roots": [` + strings.TrimSuffix(strings.Repeat(`"x",`, 33), ",") + `]}`, "too_many", "roots"},
		{"empty body", ``, "empty_body", ""},
		{"there is no field for the last copy", `{"orphans": true, "allow_last_copy": true}`, "", ""},
		{"nor a list of items to confirm", `{"orphans": true, "confirm_items": ["a"]}`, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newArkPruneRig(t)
			e := expectError(t, r.post("admin", tc.body), http.StatusBadRequest, doctor.AGT020)
			if tc.reason != "" && e.Error.Details["reason"] != tc.reason {
				t.Errorf("details = %v, want reason %s", e.Error.Details, tc.reason)
			}
			if tc.field != "" && e.Error.Details["field"] != tc.field {
				t.Errorf("details = %v, want field %s", e.Error.Details, tc.field)
			}
			if len(r.pruner.reqs) != 0 {
				t.Error("nothing may be pruned for a bad request")
			}
		})
	}
}

func TestArkPrunerErrorsPassThroughAndAreAudited(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"a sync run is active", NewError(doctor.SYN005, http.StatusConflict, map[string]any{"run_id": "run-20261102T031500Z-a1f3"}), http.StatusConflict, doctor.SYN005},
		{"no writable root", NewError(doctor.ARK001, http.StatusConflict, nil), http.StatusConflict, doctor.ARK001},
		{"a bug", errors.New("boom"), http.StatusInternalServerError, CodeInternal},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newArkPruneRig(t)
			r.pruner.err = tc.err
			expectError(t, r.post("admin", `{"orphans": true}`), tc.status, tc.code)
			evs := r.audit.events()
			if len(evs) != 1 || evs[0].Result != audit.ResultFail {
				t.Fatalf("audit = %+v", evs)
			}
		})
	}
}
