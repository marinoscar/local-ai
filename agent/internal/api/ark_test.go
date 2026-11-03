package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
)

// fakeReady answers GET /ark from a fixed list and records the language it was asked for.
type fakeReady struct {
	langs []string
	roots []any
	err   error
}

func (f *fakeReady) Ready(_ context.Context, lang string) ([]any, error) {
	f.langs = append(f.langs, lang)
	return f.roots, f.err
}

// readyFixture is a report that matches the ReadyReport schema of openapi.yaml.
func readyFixture(id, kind, overall string) map[string]any {
	return map[string]any{
		"schema_version": 1, "generated_at": "2026-11-02T04:02:10Z",
		"generated_by": map[string]any{"node_id": "ai-n3", "agent_version": "0.4.0"},
		"root": map[string]any{
			"id": id, "kind": kind, "path": "/srv/localai", "profile": "small-8gb", "catalog_version": "2026.11.02-1",
			"fs_total_bytes": 502, "fs_free_bytes": 105, "budget_bytes": 220, "used_bytes": 186, "trash_bytes": 41, "read_only": false,
		},
		"overall": overall, "survival_capable": true, "clock_wrong": false,
		"last_sync":             map[string]any{"run_id": "run-20261102T031500Z-a1f3", "trigger": "scheduled", "finished_at": "2026-11-02T04:02:09Z", "state": "done", "age_days": 0},
		"last_full_reverify_at": nil,
		"counts":                map[string]any{"verified": 1, "stale": 1, "missing": 0, "skipped": 0, "failed": 0, "quarantined": 0, "orphan": 0},
		"items": []any{
			map[string]any{"id": "a", "kind": "model", "tier": 1, "survival": true, "status": "verified", "version": "0123456789ab", "age_days": 0, "freshness": "green", "bytes": 5, "trust": "pinned"},
			map[string]any{"id": "b", "kind": "kiwix", "tier": 1, "status": "stale", "reason": "update failed", "code": "LAI-SYN-031"},
		},
		"problems": []any{map[string]any{"code": "LAI-SYN-031", "severity": "warning", "item": "b", "title": "Download failed after all retries"}},
	}
}

func newArkRig(t *testing.T, view ArkReadyView) http.Handler {
	t.Helper()
	reg := NewRegistry()
	if err := RegisterArk(reg, ArkDeps{Ready: view}); err != nil {
		t.Fatal(err)
	}
	s, _ := newTestServer(t, func(o *Options) { o.Registry = reg })
	return s.Handler()
}

func getArk(t *testing.T, h http.Handler, headers map[string]string) *recorderPair {
	t.Helper()
	rec := recordOK(t, do(h, req{method: "GET", target: "/api/v1/ark", header: jsonHeaders(headers)}))
	return &recorderPair{rec.Code, rec.Body.String(), rec.Header()}
}

type recorderPair struct {
	code   int
	body   string
	header http.Header
}

func TestArkRouteMetadata(t *testing.T) {
	routes := ArkRoutes(ArkDeps{})
	if len(routes) != 1 {
		t.Fatalf("routes = %d", len(routes))
	}
	rt := routes[0]
	if rt.Method != http.MethodGet || rt.Pattern != "/ark" || len(rt.Auth) != 1 || rt.Auth[0] != AuthOp || rt.Timeout != 10*time.Second {
		t.Errorf("route = %+v", rt)
	}
	if err := RegisterArk(NewRegistry(), ArkDeps{}); err == nil {
		t.Fatal("RegisterArk must refuse a missing view")
	}
}

// TestArkRouteBody: the body is {"roots": [...]} with the view's reports, and is recorded against the
// ReadyReport schema of openapi.yaml.
func TestArkRouteBody(t *testing.T) {
	view := &fakeReady{roots: []any{readyFixture("internal", "internal", "yellow"), readyFixture("ark-3f9c0a12", "ark_drive", "green")}}
	got := getArk(t, newArkRig(t, view), nil)
	if got.code != http.StatusOK {
		t.Fatalf("status %d: %s", got.code, got.body)
	}
	if !strings.HasPrefix(got.body, `{"roots":[{"clock_wrong":false,`) {
		t.Errorf("body = %.120s", got.body)
	}
	if strings.Count(got.body, `"schema_version":1`) != 2 {
		t.Errorf("two reports expected: %s", got.body)
	}
}

func TestArkRouteEmptyIsAnEmptyList(t *testing.T) {
	for _, roots := range [][]any{nil, {}} {
		got := getArk(t, newArkRig(t, &fakeReady{roots: roots}), nil)
		if got.code != http.StatusOK || strings.TrimSpace(got.body) != `{"roots":[]}` {
			t.Errorf("status %d body %q", got.code, got.body)
		}
	}
}

// TestArkRouteLanguage: the view is asked for the language of the request.
func TestArkRouteLanguage(t *testing.T) {
	view := &fakeReady{}
	h := newArkRig(t, view)
	getArk(t, h, nil)
	getArk(t, h, map[string]string{"Accept-Language": "es-CR,es;q=0.9"})
	getArk(t, h, map[string]string{"Accept-Language": "fr"})
	if len(view.langs) != 3 || view.langs[0] != "en" || view.langs[1] != "es" || view.langs[2] != "en" {
		t.Errorf("languages = %v", view.langs)
	}
}

func TestArkRouteErrors(t *testing.T) {
	t.Run("a typed error keeps its code", func(t *testing.T) {
		view := &fakeReady{err: NewError(doctor.AGT099, http.StatusInternalServerError, map[string]any{"reason": "view"})}
		rec := do(newArkRig(t, view), req{method: "GET", target: "/api/v1/ark", header: jsonHeaders(nil)})
		expectError(t, rec, http.StatusInternalServerError, doctor.AGT099)
	})
	t.Run("a plain error is a 500", func(t *testing.T) {
		view := &fakeReady{err: errors.New("boom")}
		rec := do(newArkRig(t, view), req{method: "GET", target: "/api/v1/ark", header: jsonHeaders(nil)})
		expectError(t, rec, http.StatusInternalServerError, CodeInternal)
	})
	t.Run("a request with no credentials is refused", func(t *testing.T) {
		rec := do(newArkRig(t, &fakeReady{}), req{method: "GET", target: "/api/v1/ark"})
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("status = %d", rec.Code)
		}
	})
}
