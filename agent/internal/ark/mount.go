package ark

import (
	"context"
	"strings"
	"time"
)

// Mount mounts a plugged-in AI-ARK filesystem that is not mounted (state not_mounted, LAI-ARK-005):
// udisksctl mount --no-user-interaction -b <device> (ark-sync.md section 4.1.2 step 5). The agent never
// mounts a drive by itself; this runs only when a person pressed Mount. As root, udisksctl mounts at
// /media/root/AI-ARK, which is fine: any mount point is allowed (ADR-0005).
//
// It applies only to a drive in state not_mounted (*WrongStateError otherwise, with the current state).
// Two clones share a root id and the detector answers with the one in use, so a clone is never mounted by
// id while its twin is mounted. It returns the drive once the mount table shows it (a short wait, the
// detector is refreshed on each look); a mount that udisksctl refused is a *MountError.
func (a *DriveActions) Mount(ctx context.Context, rootID string) (Drive, error) {
	unlock, err := a.lock(rootID, "mount")
	if err != nil {
		return Drive{}, err
	}
	defer unlock()

	d, err := a.find(rootID)
	if err != nil {
		return Drive{}, err
	}
	if d.State != StateNotMounted || d.Mount != "" {
		return Drive{}, &WrongStateError{RootID: rootID, State: d.State}
	}
	if !validDevicePath(d.Device) {
		return Drive{}, &MountError{Device: d.Device, Reason: "not a /dev device", Err: ErrBadDevice}
	}

	res, err := a.udisks(ctx, "mount", "--no-user-interaction", "-b", d.Device)
	switch {
	case err != nil:
		return Drive{}, &MountError{Device: d.Device, Err: err}
	case res.ExitCode != 0 && !strings.Contains(string(res.Stderr), "AlreadyMounted"):
		// "AlreadyMounted" means the table the detector last read is stale: look again below.
		return Drive{}, &MountError{Device: d.Device, Reason: firstLine(res.Stderr)}
	}
	a.log.Info("drive mounted by request", "root", rootID, "device", d.Device)

	// Wait for the mount table to show it.
	for waited := time.Duration(0); ; waited += pollStep {
		if rerr := a.o.Detector.Refresh(ctx); rerr != nil {
			a.log.Warn("the drive scan after a mount failed", "root", rootID, "error", rerr.Error())
		}
		if cur, ok := a.o.Detector.Drive(rootID); ok && cur.Mount != "" {
			return cur, nil
		}
		if waited >= a.o.MountWait || a.o.Sleep(ctx, pollStep) != nil {
			break
		}
	}
	return Drive{}, &MountError{Device: d.Device, Reason: "the mount did not show up in the mount table"}
}
