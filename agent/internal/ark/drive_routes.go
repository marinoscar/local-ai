package ark

import (
	"context"
	"errors"
	"net/http"
	"syscall"

	"github.com/marinoscar/local-ai/agent/internal/api"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
)

// This file is the answer side of the drive action routes (ark-sync.md section 3.5, agent.md section
// 3.8.20a): DriveAPI implements api.ArkDriveOps over DriveActions and turns the errors of the actions into
// the documented HTTP answers. internal/api owns the routes and imports nothing of this package, the same
// seam as GET /ark/drives (drives_api.go).

// DriveAPI implements api.ArkDriveOps.
type DriveAPI struct {
	Actions *DriveActions
	// View renders the drive object the init and mount routes return (the same as GET /ark/drives).
	View DrivesView
}

var _ api.ArkDriveOps = DriveAPI{}

// Eject implements api.ArkDriveOps.
func (d DriveAPI) Eject(ctx context.Context, req api.ArkEjectRequest) error {
	return driveError(d.Actions.EjectWith(ctx, req.RootID, EjectOptions{Force: req.Force, KeepPowered: req.KeepPowered}), req.RootID)
}

// Init implements api.ArkDriveOps.
func (d DriveAPI) Init(ctx context.Context, req api.ArkDriveRequest) (any, error) {
	drive, err := d.Actions.Init(ctx, req.RootID)
	if err != nil {
		return nil, driveError(err, req.RootID)
	}
	return d.View.View(drive, req.Lang), nil
}

// Mount implements api.ArkDriveOps.
func (d DriveAPI) Mount(ctx context.Context, req api.ArkDriveRequest) (any, error) {
	drive, err := d.Actions.Mount(ctx, req.RootID)
	if err != nil {
		return nil, driveError(err, req.RootID)
	}
	return d.View.View(drive, req.Lang), nil
}

// SetProfile implements api.ArkDriveOps.
func (d DriveAPI) SetProfile(ctx context.Context, req api.ArkProfileRequest) (api.ArkProfileResult, error) {
	profile, err := d.Actions.SetProfile(ctx, req.RootID, req.Profile)
	if err != nil {
		return api.ArkProfileResult{}, driveError(err, req.RootID)
	}
	return api.ArkProfileResult{RootID: req.RootID, Profile: profile}, nil
}

// driveError maps an error of the actions to the *api.Error the route writes. An error it does not know
// is returned as it is (a bug, 500).
func driveError(err error, rootID string) error {
	if err == nil {
		return nil
	}
	var (
		ws    *WrongStateError
		busy  *BusyError
		block *BlockedError
		mnt   *MountError
		setup *SetupError
		unk   *UnknownProfileError
	)
	switch {
	case errors.Is(err, ErrNoSuchDrive):
		return api.NewError(api.CodeNotFound, http.StatusNotFound, map[string]any{"root_id": rootID})
	case errors.As(err, &ws):
		d := map[string]any{"root_id": ws.RootID, "state": string(ws.State)}
		if ws.Reason != "" {
			d["reason"] = ws.Reason
		}
		return api.NewError(api.CodeInvalidRequest, http.StatusConflict, d)
	case errors.As(err, &busy):
		return api.NewError(api.CodeBusy, http.StatusConflict, map[string]any{"root_id": busy.RootID, "operation": busy.Operation})
	case errors.As(err, &block):
		blockers := block.Blockers
		if blockers == nil {
			blockers = []Blocker{}
		}
		return api.NewError(CodeEjectBlocked, http.StatusConflict, map[string]any{"root_id": block.RootID, "blockers": blockers})
	case errors.As(err, &mnt):
		reason := "mount_failed"
		if errors.Is(mnt.Err, ErrUdisksUnavailable) {
			reason = "udisksctl_unavailable"
		}
		d := map[string]any{"root_id": rootID, "device": mnt.Device, "reason": reason}
		if mnt.Reason != "" {
			d["error"] = mnt.Reason
		}
		return api.NewError(CodeNotMounted, http.StatusConflict, d)
	case errors.Is(err, ErrDriveReadOnly):
		return api.NewError(CodeDriveReadOnly, http.StatusConflict, map[string]any{"root_id": rootID})
	case errors.Is(err, ErrNoCatalog):
		return api.NewError(doctor.CAT001, http.StatusConflict, map[string]any{"reason": "no_catalog"})
	case errors.As(err, &unk):
		return api.NewError(doctor.CAT003, http.StatusUnprocessableEntity, map[string]any{"profile": unk.Profile})
	case errors.As(err, &setup):
		if errno, ok := setupCause(setup.Err); ok {
			d := map[string]any{"root_id": rootID, "reason": errnoReason(errno)}
			switch errno {
			case syscall.EROFS:
				return api.NewError(CodeDriveReadOnly, http.StatusConflict, d)
			case syscall.ENOSPC, syscall.EDQUOT:
				return api.NewError(doctor.SYN040, http.StatusConflict, d)
			default:
				return api.NewError(doctor.ARK014, http.StatusConflict, d)
			}
		}
	}
	return err
}

// errnoReason is the details.reason of a failed write.
func errnoReason(e syscall.Errno) string {
	switch e {
	case syscall.EROFS:
		return "read_only"
	case syscall.ENOSPC, syscall.EDQUOT:
		return "disk_full"
	}
	return "io_error"
}
