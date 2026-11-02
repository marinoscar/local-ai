package statusapi_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/api"
	"github.com/marinoscar/local-ai/agent/internal/audit"
	"github.com/marinoscar/local-ai/agent/internal/catalog"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/modes"
	"github.com/marinoscar/local-ai/agent/internal/router"
	"github.com/marinoscar/local-ai/agent/internal/state"
	"github.com/marinoscar/local-ai/agent/internal/statusapi"
)

func rowOf(t testing.TB, rows []statusapi.ModelRow, id string) statusapi.ModelRow {
	t.Helper()
	for _, r := range rows {
		if r.ID == id {
			return r
		}
	}
	t.Fatalf("no row for %s", id)
	return statusapi.ModelRow{}
}

func contentOf(t testing.TB, rows []statusapi.ContentRow, id string) statusapi.ContentRow {
	t.Helper()
	for _, r := range rows {
		if r.ID == id {
			return r
		}
	}
	t.Fatalf("no content row for %s", id)
	return statusapi.ContentRow{}
}

func TestGetCatalog(t *testing.T) {
	r := newRig(t, func(_ *rig, d *statusapi.Deps) {
		d.Content = fakeContent{
			"wiki-alpha-en":    {Present: true, Verified: true, Status: "verified"},
			"medical-alpha-en": {Present: true, Status: "stale"},
		}
	})
	r.pres["/srv/localai|model-alpha-9b-q4"] = true
	r.pres["/srv/localai|model-beta-27b-q4"] = true // stored, but it never fits an 8 GB card
	r.pres["/srv/localai|whisper-small"] = true
	r.rt.set(router.View{At: t0, Models: []router.ModelStatus{
		{ID: "model-alpha-9b-q4", Status: router.StatusLoaded, Aliases: []string{"alpha"}},
		{ID: "model-gamma-vl-q4", Status: router.StatusUnloaded},
	}})
	rec := r.get("/catalog", "op")
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	body := decode[statusapi.CatalogBody](t, rec)

	if body.Version != "2026.11.02-1" || body.Source != "offline" || body.FetchedAt == nil || *body.FetchedAt != "2026-11-02T14:04:05Z" {
		t.Errorf("header = %q %q %v", body.Version, body.Source, body.FetchedAt)
	}
	if len(body.Models) != len(r.cat.Models) || len(body.Content) != len(r.cat.Content) || len(body.Profiles) != len(r.cat.Profiles) {
		t.Fatalf("%d models, %d content, %d profiles", len(body.Models), len(body.Content), len(body.Profiles))
	}

	alpha := rowOf(t, body.Models, "model-alpha-9b-q4")
	if !alpha.Present || !alpha.Verified || !alpha.Loadable || alpha.LoadableReason != nil || alpha.Status != "loaded" || !alpha.Loaded || !alpha.Default || !alpha.InProfile {
		t.Errorf("alpha = %+v", alpha)
	}
	if len(alpha.Locations) != 1 || alpha.Locations[0].Root != "/srv/localai" || !alpha.Locations[0].Complete || len(alpha.Aliases) != 1 || alpha.Aliases[0] != "alpha" {
		t.Errorf("alpha locations/aliases = %+v %v", alpha.Locations, alpha.Aliases)
	}
	if alpha.Requirements.MinVRAMMiB != 6400 || alpha.SurvivalTier != 1 || alpha.SizeBytes == 0 || alpha.Summary == "" {
		t.Errorf("alpha requirements = %+v", alpha)
	}

	beta := rowOf(t, body.Models, "model-beta-27b-q4")
	if !beta.Present || beta.Loadable || beta.LoadableReason == nil || beta.LoadableReason.Code != doctor.CAT005 || beta.Status != "stored" || beta.InProfile {
		t.Errorf("beta = %+v", beta)
	}
	gamma := rowOf(t, body.Models, "model-gamma-vl-q4")
	if gamma.Present || gamma.Loadable || gamma.LoadableReason == nil || gamma.LoadableReason.Code != doctor.LLM008 || gamma.Status != "absent" || !gamma.InProfile {
		t.Errorf("gamma = %+v", gamma)
	}
	if wh := rowOf(t, body.Models, "whisper-small"); wh.Status != "n/a" || !wh.Present || !wh.Loadable {
		t.Errorf("whisper = %+v", wh)
	}

	wiki := contentOf(t, body.Content, "wiki-alpha-en")
	if !wiki.Present || !wiki.Verified || wiki.Status != "verified" || !wiki.InProfile || wiki.SizeBytes == 0 || wiki.DisplayName == "" {
		t.Errorf("wiki = %+v", wiki)
	}
	if med := contentOf(t, body.Content, "medical-alpha-en"); !med.Present || med.Verified || med.Status != "stale" {
		t.Errorf("medical = %+v", med)
	}
	if es := contentOf(t, body.Content, "wiki-alpha-es"); es.Present || es.Status != "absent" || es.InProfile {
		t.Errorf("wiki es = %+v", es)
	}

	small := catalog.Profile{}
	for _, p := range r.cat.Profiles {
		if p.ID == "small-8gb" {
			small = p
		}
	}
	for _, p := range body.Profiles {
		if p.ID == "small-8gb" {
			if p.DisplayName != small.DisplayName || p.BudgetBytes != small.Storage.BudgetBytes || len(p.Include.Models) != len(small.Include.Models) || len(p.Include.Content) != len(small.Include.Content) {
				t.Errorf("profile row = %+v", p)
			}
			return
		}
	}
	t.Error("no small-8gb profile row")
}

func TestGetCatalogSources(t *testing.T) {
	for _, src := range []catalog.Source{catalog.SourceCloud, catalog.SourceGitHub, catalog.SourceOffline, catalog.SourceKit, catalog.SourceTest} {
		t.Run(string(src), func(t *testing.T) {
			r := newRig(t, nil)
			r.store.Set(catalog.Loaded{Catalog: r.cat, Source: src}, time.Time{})
			body := decode[statusapi.CatalogBody](t, r.get("/catalog", "op"))
			if body.Source != string(src) || body.FetchedAt != nil {
				t.Errorf("source %q fetched_at %v", body.Source, body.FetchedAt)
			}
		})
	}
}

// TestGetCatalogEmpty: a node whose catalog did not load (LAI-CAT-001) still answers.
func TestGetCatalogEmpty(t *testing.T) {
	r := newRig(t, nil)
	r.store.Set(emptyLoaded(), time.Time{})
	rec := r.get("/catalog", "op")
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	body := decode[statusapi.CatalogBody](t, rec)
	if body.Version != "" || body.Source != "offline" || len(body.Models) != 0 || body.Models == nil || body.Content == nil || body.Profiles == nil {
		t.Errorf("body = %+v", body)
	}
	// The profile is not in an empty catalog: GET /profile says so with LAI-CAT-003.
	wantError(t, r.get("/profile", "op"), 422, catalog.CodeUnknownProfile)
}

func TestGetCatalogWithoutViews(t *testing.T) {
	r := newRig(t, func(_ *rig, d *statusapi.Deps) { d.Models, d.Content = nil, nil })
	body := decode[statusapi.CatalogBody](t, r.get("/catalog", "op"))
	if body.Models == nil || len(body.Models) != 0 || len(body.Content) != len(r.cat.Content) || contentOf(t, body.Content, "wiki-alpha-en").Status != "absent" {
		t.Errorf("body = %+v", body)
	}
}

func TestGetCatalogAuth(t *testing.T) {
	r := newRig(t, nil)
	wantError(t, r.get("/catalog", ""), 401, api.CodeUnauthenticated)
	wantError(t, r.get("/catalog", "portal"), 401, doctor.SVC110)
}

func refreshed(cat *catalog.Catalog, src catalog.Source, previous string, changed bool) *catalog.RefreshResult {
	return &catalog.RefreshResult{Loaded: catalog.Loaded{Catalog: cat, Source: src}, Previous: previous, Changed: changed}
}

func TestRefreshCatalog(t *testing.T) {
	t.Run("changed", func(t *testing.T) {
		r := newRig(t, nil)
		next := loadCatalog(t)
		next.Version = "2026.11.03-1"
		r.refresh.res = refreshed(next, catalog.SourceCloud, "2026.11.02-1", true)
		r.regen.res = statusapi.RegenResult{Written: true, Warnings: []statusapi.WarningBlock{{Code: doctor.LLM008, Message: "A model file is missing."}}}
		rec := r.send("POST", "/catalog/refresh", "op", `{}`)
		if rec.Code != 200 {
			t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
		}
		body := decode[statusapi.CatalogRefreshBody](t, rec)
		if body.Version != "2026.11.03-1" || body.Previous == nil || *body.Previous != "2026.11.02-1" || !body.Changed || body.Source != "cloud" || len(body.Warnings) != 1 {
			t.Errorf("body = %+v", body)
		}
		if got := r.store.Get(); got.Catalog.Version != "2026.11.03-1" || got.Source != catalog.SourceCloud || !got.FetchedAt.Equal(t0) {
			t.Errorf("the store holds %q %q %v", got.Catalog.Version, got.Source, got.FetchedAt)
		}
		if r.regen.count() != 1 {
			t.Errorf("regenerations = %d", r.regen.count())
		}
		evs := r.audit.events()
		if len(evs) != 1 || evs[0].Action != audit.ActionCatalogRefresh || evs[0].Result != audit.ResultOK || evs[0].Target["version"] != "2026.11.03-1" || evs[0].Target["source"] != "cloud" {
			t.Errorf("audit = %+v", evs)
		}
		if body := decode[statusapi.CatalogBody](t, r.get("/catalog", "op")); body.Version != "2026.11.03-1" || body.Source != "cloud" {
			t.Errorf("GET /catalog after the refresh = %q %q", body.Version, body.Source)
		}
	})
	t.Run("unchanged", func(t *testing.T) {
		r := newRig(t, nil)
		r.refresh.res = refreshed(r.cat, catalog.SourceGitHub, "2026.11.02-1", false)
		rec := r.send("POST", "/catalog/refresh", "op", `{}`)
		body := decode[statusapi.CatalogRefreshBody](t, rec)
		if rec.Code != 200 || body.Changed || body.Source != "github" || r.regen.count() != 0 {
			t.Errorf("status %d body %+v regenerations %d", rec.Code, body, r.regen.count())
		}
	})
	t.Run("first catalog has no previous", func(t *testing.T) {
		r := newRig(t, nil)
		r.refresh.res = refreshed(r.cat, catalog.SourceGitHub, "", true)
		body := decode[statusapi.CatalogRefreshBody](t, r.send("POST", "/catalog/refresh", "op", `{}`))
		if body.Previous != nil {
			t.Errorf("previous = %v", *body.Previous)
		}
	})
}

// TestRefreshCatalogOfflineAndSurvival: refresh needs the network, so it is refused (409 LAI-AGT-023)
// before the fetcher is asked, in every mode but online, and while a transition heads elsewhere.
func TestRefreshCatalogOfflineAndSurvival(t *testing.T) {
	cases := []struct {
		name      string
		effective modes.Mode
	}{{"offline", modes.Offline}, {"survival", modes.Survival}}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t, func(r *rig, _ *statusapi.Deps) {
				r.mode.info.Mode, r.mode.info.Effective = c.effective, c.effective
			})
			e := wantError(t, r.send("POST", "/catalog/refresh", "op", `{}`), 409, api.CodeNotInMode)
			if e.Error.Details["mode"] != string(c.effective) {
				t.Errorf("details = %v", e.Error.Details)
			}
			if r.refresh.count() != 0 {
				t.Error("the fetcher was asked")
			}
		})
	}
	t.Run("entering survival", func(t *testing.T) {
		r := newRig(t, func(r *rig, _ *statusapi.Deps) { r.mode.info.Effective = modes.Survival })
		wantError(t, r.send("POST", "/catalog/refresh", "op", `{}`), 409, api.CodeNotInMode)
	})
	t.Run("state only", func(t *testing.T) {
		r := newRig(t, func(_ *rig, d *statusapi.Deps) { d.Mode = nil })
		if err := r.st.MutateSync(func(s *state.State) error { s.Mode = state.ModeOffline; return nil }); err != nil {
			t.Fatal(err)
		}
		wantError(t, r.send("POST", "/catalog/refresh", "op", `{}`), 409, api.CodeNotInMode)
	})
}

func TestRefreshCatalogFailures(t *testing.T) {
	t.Run("rejected by lint keeps the previous catalog", func(t *testing.T) {
		r := newRig(t, nil)
		r.refresh.err = &catalog.RejectedError{Report: catalog.Report{Version: "2026.11.09-1", Problems: []catalog.Problem{
			{Code: doctor.CAT011, File: "models.yaml", Path: "models[0].files[0].sha256", Message: "sha256 is not 64 hex digits"},
		}}}
		e := wantError(t, r.send("POST", "/catalog/refresh", "op", `{}`), 422, catalog.CodeRejected)
		problems, _ := e.Error.Details["problems"].([]any)
		if len(problems) != 1 || e.Error.Details["problem_count"] != float64(1) || e.Error.Details["version"] != "2026.11.09-1" {
			t.Errorf("details = %v", e.Error.Details)
		}
		if got := r.store.Get(); got.Catalog != r.cat || got.Source != catalog.SourceOffline {
			t.Error("the store changed")
		}
		evs := r.audit.events()
		if len(evs) != 1 || evs[0].Result != audit.ResultFail || evs[0].Code != catalog.CodeRejected {
			t.Errorf("audit = %+v", evs)
		}
		if r.regen.count() != 0 {
			t.Error("the router was regenerated")
		}
	})
	t.Run("rejected with many problems lists ten", func(t *testing.T) {
		r := newRig(t, nil)
		var ps []catalog.Problem
		for i := 0; i < 25; i++ {
			ps = append(ps, catalog.Problem{Code: doctor.CAT010, File: "models.yaml", Path: "x", Message: "m"})
		}
		r.refresh.err = &catalog.RejectedError{Report: catalog.Report{Problems: ps}}
		e := wantError(t, r.send("POST", "/catalog/refresh", "op", `{}`), 422, catalog.CodeRejected)
		if got, _ := e.Error.Details["problems"].([]any); len(got) != 10 || e.Error.Details["problem_count"] != float64(25) {
			t.Errorf("details = %v", e.Error.Details)
		}
	})
	t.Run("no source", func(t *testing.T) {
		r := newRig(t, nil)
		r.refresh.err = catalog.ErrNoSource
		e := wantError(t, r.send("POST", "/catalog/refresh", "op", `{}`), 502, doctor.CLD013)
		if e.Error.Details["reason"] != "no_source" {
			t.Errorf("details = %v", e.Error.Details)
		}
	})
	t.Run("fetch failed", func(t *testing.T) {
		r := newRig(t, nil)
		r.refresh.err = errors.New("catalog refresh: github: connection refused 192.0.2.1")
		rec := r.send("POST", "/catalog/refresh", "op", `{}`)
		e := wantError(t, rec, 502, doctor.CLD013)
		if e.Error.Details["reason"] != "fetch_failed" {
			t.Errorf("details = %v", e.Error.Details)
		}
		if strings.Contains(rec.Body.String(), "192.0.2.1") {
			t.Errorf("the transport error reached the client: %s", rec.Body.String())
		}
		if got := r.store.Get(); got.Catalog != r.cat {
			t.Error("the store changed")
		}
	})
}

func TestRefreshCatalogRequestRules(t *testing.T) {
	r := newRig(t, nil)
	wantError(t, r.send("POST", "/catalog/refresh", "", `{}`), 401, api.CodeUnauthenticated)
	wantError(t, r.send("POST", "/catalog/refresh", "op", `{"force": true}`), 400, api.CodeInvalidRequest)
	wantError(t, r.send("POST", "/catalog/refresh", "op", ``), 400, api.CodeInvalidRequest)
	wantError(t, do(t, r.h, call{method: "POST", target: "/catalog/refresh", principal: "op", body: `{}`, header: map[string]string{"Content-Type": "text/plain"}}), 415, api.CodeInvalidRequest)
	if r.refresh.count() != 0 {
		t.Error("the fetcher was asked for a bad request")
	}
}

// TestRefreshCatalogDeadline: a fetch cut off by the route deadline is the generic deadline error,
// not a 502 that blames a source.
func TestRefreshCatalogDeadline(t *testing.T) {
	r := newRig(t, func(_ *rig, d *statusapi.Deps) { d.Refresher = deadlineRefresher{} })
	var h http.Handler
	for _, rt := range statusapi.Routes(r.deps) {
		if rt.Method == "POST" && rt.Pattern == "/catalog/refresh" {
			h = rt.Handler
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	<-ctx.Done()
	req := httptest.NewRequest("POST", "/api/v1/catalog/refresh", strings.NewReader(`{}`)).WithContext(ctx)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code == 502 || strings.Contains(rec.Body.String(), doctor.CLD013) {
		t.Errorf("status %d: %s", rec.Code, rec.Body.String())
	}
	if len(r.audit.events()) != 0 || r.store.Get().Catalog != r.cat {
		t.Error("a cut-off refresh audited or changed something")
	}
}

type deadlineRefresher struct{}

func (deadlineRefresher) Refresh(ctx context.Context) (*catalog.RefreshResult, error) {
	return nil, ctx.Err()
}
