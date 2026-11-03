package adapt

import (
	"context"
	"errors"
	"net/http"

	"github.com/marinoscar/local-ai/agent/internal/api"
	"github.com/marinoscar/local-ai/agent/internal/ark"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/sync/importer"
	"github.com/marinoscar/local-ai/agent/internal/sync/plan"
	"github.com/marinoscar/local-ai/agent/internal/sync/run"
)

var _ api.ArkImporter = Importer{}

// Importer is api.ArkImporter over the importer and the engine: POST /ark/import. A dry run asks the
// importer for the plan and writes nothing; a real import resolves the source drive first, so that
// "several drives" and "no such drive" are answered by the request itself, then starts a run of trigger
// import, which plans again inside the run (the catalog and node.yaml are changed there, once the run
// holds the slot) and copies through the engine's import machine with the network refused.
type Importer struct {
	Engine  *run.Engine
	Planner *importer.Planner
}

// DryRunImport implements api.ArkImporter. The body is a list with the plan of the internal root, like a
// dry run of sync; the items filter shows only those rows.
func (i Importer) DryRunImport(ctx context.Context, req api.ArkImportRequest) (any, error) {
	res, err := i.Planner.Plan(ctx, importer.Request{
		From: req.FromRoot, Items: req.Items, AddToProfile: req.AddToProfile, Catalog: req.Catalog, DryRun: true,
	})
	if err != nil {
		return nil, importError(err)
	}
	rp := res.Plan
	if len(req.Items) > 0 {
		rp = rp.OnlyItems(req.Items)
	}
	return []plan.RootPlan{rp}, nil
}

// StartImport implements api.ArkImporter.
func (i Importer) StartImport(ctx context.Context, req api.ArkImportRequest) (api.ArkSyncStarted, error) {
	drive, err := i.Planner.Source(req.FromRoot)
	if err != nil {
		return api.ArkSyncStarted{}, importError(err)
	}
	started, err := i.Engine.Start(ctx, run.StartRequest{
		Items: req.Items, Trigger: run.TriggerImport, OpID: req.OpID, Actor: req.Actor,
		Import: &run.ImportSpec{From: drive.ID, AddToProfile: req.AddToProfile, Catalog: req.Catalog},
	})
	if err != nil {
		return api.ArkSyncStarted{}, mapStartError(err)
	}
	return api.ArkSyncStarted{RunID: started.RunID, State: started.State, Reason: started.Reason, Code: started.Code}, nil
}

// importError turns the importer's and the planner's typed errors into the *api.Error of the route.
func importError(err error) error {
	var several *importer.SeveralDrivesError
	var unknown *importer.UnknownDriveError
	var unusable *importer.UnusableDriveError
	var profile *plan.ErrUnknownProfile
	switch {
	case errors.As(err, &several):
		return api.NewError(doctor.ARK015, http.StatusConflict, map[string]any{"roots": several.Roots})
	case errors.Is(err, importer.ErrNoDrive):
		return api.NewError(api.CodeInvalidRequest, http.StatusBadRequest, map[string]any{"field": "from_root", "reason": "no_drive"})
	case errors.As(err, &unknown):
		return api.NewError(api.CodeInvalidRequest, http.StatusBadRequest, map[string]any{"field": "from_root", "reason": "unknown_root", "value": unknown.Root})
	case errors.As(err, &unusable):
		if unusable.State == string(ark.StateUnclean) {
			return api.NewError(doctor.ARK004, http.StatusConflict, map[string]any{"root": unusable.Root, "state": unusable.State})
		}
		return api.NewError(api.CodeInvalidRequest, http.StatusBadRequest, map[string]any{"field": "from_root", "reason": "drive_not_ready", "value": unusable.Root, "state": unusable.State})
	case errors.Is(err, importer.ErrNeedItems):
		return api.NewError(api.CodeInvalidRequest, http.StatusBadRequest, map[string]any{"field": "items", "reason": "required_with_add_to_profile"})
	case errors.As(err, &profile):
		return api.NewError(plan.CodeUnknownProfile, http.StatusUnprocessableEntity, map[string]any{"profile": profile.Profile, "root": profile.Root})
	}
	return err
}
