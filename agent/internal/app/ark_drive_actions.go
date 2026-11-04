package app

import (
	"context"
	"log/slog"

	"github.com/marinoscar/local-ai/agent/internal/api"
	"github.com/marinoscar/local-ai/agent/internal/ark"
	"github.com/marinoscar/local-ai/agent/internal/sync/run"
)

// This file wires the drive actions (ark-sync.md section 4.1.5 and 3.5, SYNC-03): eject, init, mount and
// set-profile. internal/ark holds the logic; here it gets the real udisksctl, the sync engine, the
// catalog, the extras writer and the alert sink, and the four routes are registered over it. The router,
// Kiwix and whisper register themselves as drive users on d.ark.driveActions (RegisterUser) when they
// land; until then an eject finds a loaded model through the /proc walk, as a process holding the file.

// buildDriveActions builds the actions over the detector. A node without the detector has none.
func (s *arkStack) buildDriveActions(opts ark.Options) {
	d, env := s.d, s.d.env
	var owner *ark.Owner
	if d.haveLocalAI && !env.Unprivileged {
		owner = &ark.Owner{UID: d.localaiUID, GID: d.localaiGID}
	}
	udisks, err := env.Lookup("udisksctl")
	if err != nil {
		d.log.Warn("udisksctl is not installed: AI-ARK drives cannot be mounted by request, and a disk is not powered off after an eject", "error", err.Error())
		udisks = ""
	}
	drive := opts.Drive
	if drive.Now == nil {
		drive.Now = env.Clock.Now
	}
	a, err := ark.NewDriveActions(ark.DriveActionsOptions{
		Detector: s.drives, Ejector: s.drives, FS: ark.OSFS{},
		Runner: env.Runner, Udisksctl: udisks,
		Events: d.c.Bus, Alerts: d.c.Doctor,
		Extras: s.extras, Transfers: engineTransfers{s.engine},
		Profiles: s.hasProfile, Drive: drive, Owner: owner,
		Logger: d.log,
	})
	if err != nil {
		d.log.Error("the AI-ARK drive actions could not be built; eject, set up and mount are off", "error", err.Error())
		return
	}
	s.driveActions = a
}

// engineTransfers is ark.TransferCanceller over the sync engine: RootGone cancels the transfers that
// write to a root, at the next block and checkpoint, and the next run resumes them.
type engineTransfers struct{ e *run.Engine }

// CancelRoot implements ark.TransferCanceller.
func (t engineTransfers) CancelRoot(rootID string) bool {
	if t.e == nil {
		return false
	}
	return t.e.RootGone(rootID)
}

// hasProfile is ark.ProfileLookup over the catalog in use.
func (s *arkStack) hasProfile(id string) (exists, loaded bool) {
	cat := s.d.c.Catalog.Get().Catalog
	if cat == nil {
		return false, false
	}
	_, ok := cat.Profile(id)
	return ok, true
}

// driveOps is the api.ArkDriveOps of the stack.
func (s *arkStack) driveOps() api.ArkDriveOps {
	return ark.DriveAPI{Actions: s.driveActions, View: s.drivesView()}
}

// noDriveOps is the ops of a daemon without drive actions: 503, like the other ark routes.
type noDriveOps struct{ arkUnavailable }

// Eject implements api.ArkDriveOps.
func (u noDriveOps) Eject(context.Context, api.ArkEjectRequest) error { return u.err() }

// Init implements api.ArkDriveOps.
func (u noDriveOps) Init(context.Context, api.ArkDriveRequest) (any, error) { return nil, u.err() }

// Mount implements api.ArkDriveOps.
func (u noDriveOps) Mount(context.Context, api.ArkDriveRequest) (any, error) { return nil, u.err() }

// SetProfile implements api.ArkDriveOps.
func (u noDriveOps) SetProfile(context.Context, api.ArkProfileRequest) (api.ArkProfileResult, error) {
	return api.ArkProfileResult{}, u.err()
}

// driveActionsWorker lets the daemon wait for an eject that was accepted when it stops.
type driveActionsWorker struct {
	a   *ark.DriveActions
	log *slog.Logger
}

// Name implements Worker.
func (w driveActionsWorker) Name() string { return "ark-drive-actions" }

// Start implements Worker: the actions run on request, there is nothing to start.
func (w driveActionsWorker) Start(context.Context) error { return nil }

// Stop implements Worker: it waits for the unmount half of an accepted eject, within the stop budget.
func (w driveActionsWorker) Stop(ctx context.Context) error {
	if err := w.a.Wait(ctx); err != nil {
		w.log.Warn("an eject was still unmounting when the agent stopped", "error", err.Error())
		return err
	}
	return nil
}
