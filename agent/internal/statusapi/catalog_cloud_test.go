package statusapi_test

import (
	"context"
	"errors"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/api"
	"github.com/marinoscar/local-ai/agent/internal/audit"
	"github.com/marinoscar/local-ai/agent/internal/catalog"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/modes"
	"github.com/marinoscar/local-ai/agent/internal/statusapi"
)

// RefreshCatalog is the route without the HTTP: the cloud executor of catalog.refresh (AGENT-18) calls
// it with the cloud actor and gets the route's mode rule, errors and audit line.
func TestRefreshCatalogForTheCloud(t *testing.T) {
	actor := audit.Cloud("job-3")

	t.Run("a changed catalog is installed and audited with the cloud actor", func(t *testing.T) {
		r := newRig(t, nil)
		next := loadCatalog(t)
		next.Version = "2026.11.03-1"
		r.refresh.res = refreshed(next, catalog.SourceCloud, "2026.11.02-1", true)
		body, err := r.deps.RefreshCatalog(context.Background(), actor)
		if err != nil {
			t.Fatal(err)
		}
		if body.Version != "2026.11.03-1" || !body.Changed || body.Source != "cloud" {
			t.Errorf("body = %+v", body)
		}
		if got := r.store.Get(); got.Catalog.Version != "2026.11.03-1" {
			t.Errorf("the store holds %q", got.Catalog.Version)
		}
		evs := r.audit.events()
		if len(evs) != 1 || evs[0].Action != audit.ActionCatalogRefresh || evs[0].Result != audit.ResultOK || evs[0].Actor.Kind != audit.KindCloud || evs[0].Actor.JobID != "job-3" {
			t.Errorf("audit = %+v", evs)
		}
	})

	t.Run("outside online mode nothing is fetched", func(t *testing.T) {
		r := newRig(t, func(r *rig, _ *statusapi.Deps) {
			r.mode.info.Mode, r.mode.info.Effective = modes.Offline, modes.Offline
		})
		_, err := r.deps.RefreshCatalog(context.Background(), actor)
		var ae *api.Error
		if !errors.As(err, &ae) || ae.Code != api.CodeNotInMode || ae.Status != 409 {
			t.Fatalf("error = %#v", err)
		}
		if r.refresh.count() != 0 {
			t.Error("the fetcher was asked")
		}
	})

	t.Run("a rejected catalog is LAI-CAT-002 and the failure is audited with the cloud actor", func(t *testing.T) {
		r := newRig(t, nil)
		r.refresh.err = &catalog.RejectedError{Report: catalog.Report{Version: "2026.11.09-1"}}
		_, err := r.deps.RefreshCatalog(context.Background(), actor)
		var ae *api.Error
		if !errors.As(err, &ae) || ae.Code != catalog.CodeRejected || ae.Details["version"] != "2026.11.09-1" {
			t.Fatalf("error = %#v", err)
		}
		evs := r.audit.events()
		if len(evs) != 1 || evs[0].Result != audit.ResultFail || evs[0].Code != catalog.CodeRejected || evs[0].Actor.JobID != "job-3" {
			t.Errorf("audit = %+v", evs)
		}
	})

	t.Run("no source is LAI-CLD-013", func(t *testing.T) {
		r := newRig(t, nil)
		r.refresh.err = catalog.ErrNoSource
		_, err := r.deps.RefreshCatalog(context.Background(), actor)
		var ae *api.Error
		if !errors.As(err, &ae) || ae.Code != doctor.CLD013 || ae.Status != 502 {
			t.Errorf("error = %#v", err)
		}
	})
}
