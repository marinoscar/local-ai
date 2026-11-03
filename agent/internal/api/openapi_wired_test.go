package api_test

// The second side of the route bijection (agent.md section 4.14): agent/api/openapi.yaml against every
// route the agent implements. It is an external test package because the routes come from packages that
// import internal/api (auth), which an internal test of internal/api cannot import.

import (
	"sort"
	"strings"
	"testing"

	apispec "github.com/marinoscar/local-ai/agent/api"
	"github.com/marinoscar/local-ai/agent/internal/api"
	"github.com/marinoscar/local-ai/agent/internal/auth"
	authfake "github.com/marinoscar/local-ai/agent/internal/auth/fake"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/events"
	"github.com/marinoscar/local-ai/agent/internal/hardware"
	hwfake "github.com/marinoscar/local-ai/agent/internal/hardware/fake"
	"github.com/marinoscar/local-ai/agent/internal/modelsapi"
	"github.com/marinoscar/local-ai/agent/internal/portal"
	"github.com/marinoscar/local-ai/agent/internal/statusapi"
	"github.com/marinoscar/local-ai/agent/internal/svc/piper"
)

func spec(t *testing.T) *apispec.Spec {
	t.Helper()
	s, err := apispec.Default()
	if err != nil {
		t.Fatalf("agent/api/openapi.yaml does not parse: %v", err)
	}
	return s
}

// implementedRoutes is every route the agent implements: api.Routes() (the built-in route and what
// packages add to DefaultRegistry at init) plus the routes that constructors return for the daemon
// wiring (AGENT-20) to add to the server's registry: GET /events (api.EventsRoute), the five routes
// of the auth service (auth.Service.Routes), GET /doctor (api.DoctorRoute), the three /mode routes
// (api.ModeRoutes), the five routes of statusapi.Routes, GET /portal/status (portal.Route), the three model
// operation routes (api.ModelOpsRoutes), the four /cloud routes (api.CloudRoutes), POST, GET and
// DELETE /ark/sync (api.ArkSyncRoutes), GET /ark (api.ArkRoutes) and GET /ark/drives (api.ArkDrivesRoutes).
//
// A later issue whose routes come from a constructor adds it HERE, with fakes for its dependencies, in
// the PR that deletes the operations' x-localai-status: planned and x-localai-issue from openapi.yaml.
func implementedRoutes(t *testing.T) []api.Route {
	t.Helper()
	routes := api.Routes()

	bus := events.New(events.Options{})
	t.Cleanup(bus.Close)
	ev, err := api.EventsRoute(api.EventsOptions{Bus: bus})
	if err != nil {
		t.Fatal(err)
	}
	routes = append(routes, ev)

	svc := auth.New(auth.Config{Groups: authfake.NewGroups().Add(auth.OperatorsGroup, 990)})
	routes = append(routes, svc.Routes()...)

	routes = append(routes, hardware.Route(hwfake.New(hardware.Snapshot{}), hardware.HandlerOptions{}))

	// Only the metadata (method, pattern, auth, timeout) is compared, so the dependencies stay empty.
	routes = append(routes, api.ServicesRoutes(api.ServicesDeps{})...)
	routes = append(routes, api.LogsRoutes(api.LogsDeps{})...)

	routes = append(routes, api.DoctorRoute(api.DoctorOptions{Registry: doctor.NewRegistry()}))

	// GET /mode, POST /mode and POST /mode/survival (AGENT-12); the engine is not called.
	routes = append(routes, api.ModeRoutes(api.ModeDeps{})...)

	// GET /status, GET and PUT /profile, GET /catalog and POST /catalog/refresh (AGENT-14), and GET
	// /portal/status; the handlers are not called, so the dependencies stay empty.
	routes = append(routes, statusapi.Routes(statusapi.Deps{})...)
	routes = append(routes, portal.Route(portal.RouteOptions{}))
	// POST /models/{id}/load, POST /models/{id}/unload and PUT /models/default (ROUTER-05).
	routes = append(routes, api.ModelOpsRoutes(api.ModelOpsDeps{})...)
	// GET /cloud, POST /cloud/enroll, DELETE /cloud and POST /cloud/resend-key (AGENT-17).
	routes = append(routes, api.CloudRoutes(api.CloudDeps{})...)
	// GET /models, GET and DELETE /models/{id}, POST /models/{id}/download, GET /key and POST
	// /key/rotate (AGENT-15); the handlers are not called, so the dependencies stay empty.
	routes = append(routes, modelsapi.Routes(modelsapi.Deps{})...)
	// POST, GET and DELETE /drill (DRILL-01) and POST /drill/manual (DRILL-06); the handlers are not
	// called, so the service stays nil.
	routes = append(routes, api.DrillRoutes(api.DrillDeps{})...)
	// POST /ark/sync (SYNC-05: the dry-run branch; the planner is not called, so it stays nil) and GET and
	// DELETE /ark/sync (SYNC-07).
	routes = append(routes, api.ArkSyncRoutes(api.ArkSyncDeps{})...)
	// POST /speech/tts and GET /speech/voices (SVC-08); the handlers are not called, so the
	// dependencies stay empty.
	routes = append(routes, piper.Routes(piper.Deps{})...)
	// GET /ark (SYNC-12); the view is not called, so it stays nil.
	routes = append(routes, api.ArkRoutes(api.ArkDeps{})...)
	// POST /ark/prune (SYNC-08); the pruner is not called, so it stays nil.
	routes = append(routes, api.ArkPruneRoutes(api.ArkPruneDeps{})...)

	// GET /ark/drives (SYNC-02); the view is not called, so it stays nil.
	routes = append(routes, api.ArkDrivesRoutes(api.ArkDrivesDeps{})...)
	return routes
}

// TestOpenAPIMatchesImplementedRoutes: every implemented route is in the document with the same auth
// levels, timeout, gate exemption and media types and is not marked planned; every operation of the
// document that is not planned is implemented.
func TestOpenAPIMatchesImplementedRoutes(t *testing.T) {
	for _, p := range api.CompareRegistered(spec(t).Operations(), implementedRoutes(t)) {
		t.Error(p)
	}
}

// TestPlannedOperations lists the allowlist and checks it is what it claims to be: no planned
// operation is implemented (the comparison above says so too, with a message of its own), every other
// operation is, and the implemented ones are the ones expected so far.
func TestPlannedOperations(t *testing.T) {
	s := spec(t)
	implementedKeys := map[string]bool{}
	for _, rt := range implementedRoutes(t) {
		if !rt.Absolute {
			implementedKeys[rt.Method+" "+rt.Pattern] = true
		}
	}
	byIssue := map[string][]string{}
	implemented := 0
	for _, op := range s.Operations() {
		if !op.Planned() {
			implemented++
			if !implementedKeys[op.Key()] {
				t.Errorf("%s is not planned and not implemented", op.Key())
			}
			continue
		}
		if implementedKeys[op.Key()] {
			t.Errorf("%s is planned but implemented: the allowlist must shrink (remove x-localai-status and x-localai-issue)", op.Key())
		}
		issue, _ := op.Raw["x-localai-issue"].(string)
		byIssue[issue] = append(byIssue[issue], op.Key())
	}
	for _, key := range []string{"GET /health", "GET /events", "POST /session", "POST /session/code", "DELETE /session", "POST /ui-code", "POST /pin/verify", "GET /hardware", "GET /services", "POST /services/{name}/{action}", "GET /logs", "GET /doctor", "GET /mode", "POST /mode", "POST /mode/survival", "GET /status", "GET /portal/status", "GET /profile", "PUT /profile", "GET /catalog", "POST /catalog/refresh", "POST /models/{id}/load", "POST /models/{id}/unload", "PUT /models/default", "GET /models", "GET /models/{id}", "DELETE /models/{id}", "POST /models/{id}/download", "GET /key", "POST /key/rotate", "GET /cloud", "DELETE /cloud", "POST /cloud/enroll", "POST /cloud/resend-key", "POST /drill", "GET /drill", "DELETE /drill", "POST /drill/manual", "POST /ark/sync", "GET /ark/sync", "DELETE /ark/sync", "POST /speech/tts", "GET /speech/voices", "GET /ark", "POST /ark/prune"} {
		if !implementedKeys[key] {
			t.Errorf("%s must be implemented", key)
		}
	}
	var issues []string
	for issue := range byIssue {
		issues = append(issues, issue)
	}
	sort.Strings(issues)
	for _, issue := range issues {
		t.Logf("planned, lands with %s: %s", issue, strings.Join(byIssue[issue], ", "))
	}
	t.Logf("%d operation(s) implemented, %d planned", implemented, len(s.Operations())-implemented)
}
