package ark

import (
	"context"
	"path/filepath"
)

// SetProfile changes the profile a drive is planned with: it checks the id against the catalog and writes
// ROOT.json's profile (ark-sync.md section 3.2: "changed with aictl ark drives set-profile"). Keys of
// ROOT.json that this agent does not know are kept (UpdateRootJSON). The detector then reads the drive
// again, so the roots the planner uses carry the new profile at once.
//
// It applies to a mounted, initialized drive that can be written: a drive that is not (not mounted,
// uninitialized, ejecting, unusable) is a *WrongStateError and one that is read-only is ErrDriveReadOnly.
// An id the catalog lacks is *UnknownProfileError, and no catalog at all is ErrNoCatalog. It returns the
// profile now in the file. The caller (the route) has already required the admin PIN.
func (a *DriveActions) SetProfile(ctx context.Context, rootID, profile string) (string, error) {
	unlock, err := a.lock(rootID, "profile")
	if err != nil {
		return "", err
	}
	defer unlock()

	d, err := a.find(rootID)
	if err != nil {
		return "", err
	}
	if !d.Initialized || d.Mount == "" || d.State == StateEjecting || d.State == StateUnusable {
		return "", &WrongStateError{RootID: rootID, State: d.State}
	}
	if d.ReadOnly {
		return "", ErrDriveReadOnly
	}

	var exists, loaded bool
	if a.o.Profiles != nil {
		exists, loaded = a.o.Profiles(profile)
	}
	switch {
	case !loaded:
		return "", ErrNoCatalog
	case !exists:
		return "", &UnknownProfileError{Profile: profile}
	}

	path := filepath.Join(d.Mount, DriveRootDir, RootJSONName)
	if d.Profile != profile {
		if _, err := UpdateRootJSON(a.o.FS, path, func(r *RootJSON) { r.Profile = profile }); err != nil {
			return "", &SetupError{RootID: rootID, Err: err}
		}
		a.log.Info("drive profile changed", "root", rootID, "from", d.Profile, "to", profile)
		if err := a.o.Detector.Refresh(ctx); err != nil {
			a.log.Warn("the drive scan after a profile change failed", "root", rootID, "error", err.Error())
		}
	}
	return profile, nil
}
