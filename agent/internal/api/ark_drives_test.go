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

// fakeDrives answers GET /ark/drives from a fixed list and records the language it was asked for.
type fakeDrives struct {
	langs  []string
	drives []any
	err    error
}

func (f *fakeDrives) Drives(_ context.Context, lang string) ([]any, error) {
	f.langs = append(f.langs, lang)
	return f.drives, f.err
}

func newArkDrivesRig(t *testing.T, view ArkDrivesView) http.Handler {
	t.Helper()
	reg := NewRegistry()
	if err := RegisterArkDrives(reg, ArkDrivesDeps{Drives: view}); err != nil {
		t.Fatal(err)
	}
	s, _ := newTestServer(t, func(o *Options) { o.Registry = reg })
	return s.Handler()
}

func getArkDrives(t *testing.T, h http.Handler, headers map[string]string) *recorderPair {
	t.Helper()
	rec := recordOK(t, do(h, req{method: "GET", target: "/api/v1/ark/drives", header: jsonHeaders(headers)}))
	return &recorderPair{rec.Code, rec.Body.String(), rec.Header()}
}

func TestArkDrivesRouteMetadata(t *testing.T) {
	routes := ArkDrivesRoutes(ArkDrivesDeps{})
	if len(routes) != 1 {
		t.Fatalf("routes = %d", len(routes))
	}
	rt := routes[0]
	if rt.Method != http.MethodGet || rt.Pattern != "/ark/drives" || len(rt.Auth) != 1 || rt.Auth[0] != AuthOp || rt.Timeout != 10*time.Second {
		t.Errorf("route = %+v", rt)
	}
	if err := RegisterArkDrives(NewRegistry(), ArkDrivesDeps{}); err == nil {
		t.Fatal("RegisterArkDrives must refuse a missing view")
	}
}

// TestArkDrivesRouteBody: the body is {"drives": [...]} with the view's drives, unchanged.
func TestArkDrivesRouteBody(t *testing.T) {
	view := &fakeDrives{drives: []any{
		map[string]any{"root_id": "ark-3f9c0a12", "device": "/dev/sdb1", "mount": "/media/operator/AI-ARK", "state": "ready"},
		map[string]any{"root_id": nil, "device": "/dev/sdc1", "mount": nil, "state": "unusable"},
	}}
	got := getArkDrives(t, newArkDrivesRig(t, view), nil)
	if got.code != http.StatusOK {
		t.Fatalf("status %d: %s", got.code, got.body)
	}
	if !strings.HasPrefix(got.body, `{"drives":[{"device":"/dev/sdb1",`) || !strings.Contains(got.body, `"root_id":null`) {
		t.Errorf("body = %.200s", got.body)
	}
}

func TestArkDrivesRouteEmptyIsAnEmptyList(t *testing.T) {
	for _, drives := range [][]any{nil, {}} {
		got := getArkDrives(t, newArkDrivesRig(t, &fakeDrives{drives: drives}), nil)
		if got.code != http.StatusOK || strings.TrimSpace(got.body) != `{"drives":[]}` {
			t.Errorf("status %d body %q", got.code, got.body)
		}
	}
}

// TestArkDrivesRouteLanguage: the view is asked for the language of the request.
func TestArkDrivesRouteLanguage(t *testing.T) {
	view := &fakeDrives{}
	h := newArkDrivesRig(t, view)
	getArkDrives(t, h, nil)
	getArkDrives(t, h, map[string]string{"Accept-Language": "es-CR,es;q=0.9"})
	getArkDrives(t, h, map[string]string{"Accept-Language": "fr"})
	if len(view.langs) != 3 || view.langs[0] != "en" || view.langs[1] != "es" || view.langs[2] != "en" {
		t.Errorf("languages = %v", view.langs)
	}
}

func TestArkDrivesRouteErrors(t *testing.T) {
	t.Run("a typed error keeps its code", func(t *testing.T) {
		view := &fakeDrives{err: NewError(doctor.AGT099, http.StatusInternalServerError, map[string]any{"reason": "view"})}
		rec := do(newArkDrivesRig(t, view), req{method: "GET", target: "/api/v1/ark/drives", header: jsonHeaders(nil)})
		expectError(t, rec, http.StatusInternalServerError, doctor.AGT099)
	})
	t.Run("a plain error is a 500", func(t *testing.T) {
		view := &fakeDrives{err: errors.New("boom")}
		rec := do(newArkDrivesRig(t, view), req{method: "GET", target: "/api/v1/ark/drives", header: jsonHeaders(nil)})
		expectError(t, rec, http.StatusInternalServerError, CodeInternal)
	})
	t.Run("a request with no credentials is refused", func(t *testing.T) {
		rec := do(newArkDrivesRig(t, &fakeDrives{}), req{method: "GET", target: "/api/v1/ark/drives"})
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("status = %d", rec.Code)
		}
	})
}
