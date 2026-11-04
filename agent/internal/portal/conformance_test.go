package portal_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/api"
	"github.com/marinoscar/local-ai/agent/internal/api/apitest"
	"github.com/marinoscar/local-ai/agent/internal/portal"
	"github.com/marinoscar/local-ai/agent/internal/svc/health"
)

// TestFullBodyConformsToSchema: the body of a fully populated node, through the real route and
// middleware, is checked against PortalStatusV1 in api/openapi.yaml by apitest.RecordResult.
func TestFullBodyConformsToSchema(t *testing.T) {
	for _, mut := range []struct {
		name string
		mod  func(*fullRig)
	}{
		{"healthy", func(*fullRig) {}},
		{"degraded everywhere", func(r *fullRig) {
			r.rt.view.Models = nil
			r.llama.state = "down"
			r.record("kiwix").State = health.StateDown
			r.record("whisper").State = health.StateMissingContent
			r.record("maps").Maps = nil
			r.docs.d.Docs = nil
		}},
	} {
		t.Run(mut.name, func(t *testing.T) {
			r := newFullRig(t)
			mut.mod(r)
			reg := api.NewRegistry()
			if err := portal.Register(reg, portal.RouteOptions{Builder: r.b, Now: func() time.Time { return t0 }}); err != nil {
				t.Fatal(err)
			}
			s, err := api.New(api.Options{Registry: reg, Auth: principals, Version: "test", Mode: func() string { return "survival" }})
			if err != nil {
				t.Fatal(err)
			}
			for _, lang := range []string{"en", "es"} {
				req := httptest.NewRequest(http.MethodGet, "/api/v1/portal/status?lang="+lang, nil)
				req.Host = "127.0.0.1:7070"
				rec := httptest.NewRecorder()
				s.Handler().ServeHTTP(rec, req)
				apitest.RecordResult(t, req, rec)
				if rec.Code != 200 {
					t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
				}
			}
		})
	}
}
