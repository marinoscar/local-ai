package ark

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"syscall"
)

// Init sets up an empty AI-ARK drive: it creates <mount>/localai/ with the full layout and a valid
// ROOT.json (kind ark_drive, the default profile), then has the detector read the drive again so it is
// ready, and writes the drive extras (START-HERE.html and the inventory copy, ark-sync.md section 4.15).
// It is the one place the layout is ever created on a drive, and it only creates: a directory tree and
// one JSON file (ark-sync.md section 6). It never formats and never removes anything.
//
// It applies only to a drive in state uninitialized (*WrongStateError otherwise, with the current state),
// and is refused when <mount>/localai already holds anything: the detector counts an empty localai/ as no
// localai/ (ark-sync.md section 3.2), so an empty directory is set up, but a directory with content or a
// file in its place is never touched (Reason "localai_exists").
//
// The returned drive is the detector's view after the new pass; its state is ready unless the drive then
// failed a check (a write probe on a drive that turned read-only).
func (a *DriveActions) Init(ctx context.Context, rootID string) (Drive, error) {
	unlock, err := a.lock(rootID, "init")
	if err != nil {
		return Drive{}, err
	}
	defer unlock()

	d, err := a.find(rootID)
	if err != nil {
		return Drive{}, err
	}
	if d.State != StateUninitialized || d.Mount == "" {
		return Drive{}, &WrongStateError{RootID: rootID, State: d.State}
	}
	rootDir := filepath.Join(d.Mount, DriveRootDir)
	switch occupied, err := a.rootDirOccupied(rootDir); {
	case err != nil:
		return Drive{}, &SetupError{RootID: rootID, Err: err}
	case occupied:
		return Drive{}, &WrongStateError{RootID: rootID, State: d.State, Reason: "localai_exists"}
	}

	root, err := NewDriveRoot(DriveInfo{Mount: d.Mount, FSUUID: d.UUID, MountID: d.MountID, Label: d.Label}, a.o.Drive.DefaultProfile)
	if err != nil {
		return Drive{}, &SetupError{RootID: rootID, Err: err}
	}
	if _, err := CreateLayout(a.o.FS, root.Path, LayoutOptions{Owner: a.o.Owner}); err != nil {
		return Drive{}, &SetupError{RootID: rootID, Err: err}
	}
	by := CreatedBy{NodeID: a.o.Drive.NodeID, AgentVersion: a.o.Drive.AgentVersion}
	rj := NewRootJSON(root, by, d.Label, a.o.Drive.Now())
	if err := WriteRootJSONFS(a.o.FS, filepath.Join(root.Path, RootJSONName), rj); err != nil {
		// The layout is there and ROOT.json is not: the next pass sees a localai/ with content and no
		// ROOT.json and recreates it with the same defaults (LAI-ARK-010 as information), so the drive is
		// not stuck. The person is told to try again.
		return Drive{}, &SetupError{RootID: rootID, Err: err}
	}
	a.log.Info("drive set up", "root", rootID, "mount", d.Mount, "profile", root.Profile)

	// The detector reads the drive again: it becomes ready, drive.mounted is published, and OnActive
	// updates the roots the sync engine plans over.
	if err := a.o.Detector.Refresh(ctx); err != nil {
		a.log.Warn("the drive scan after init failed", "root", rootID, "error", err.Error())
	}
	after, ok := a.o.Detector.Drive(rootID)
	if !ok {
		// Pulled out during the call. The drive is set up; say what is known.
		return d, nil
	}
	a.writeExtras(ctx, rootID)
	return after, nil
}

// rootDirOccupied reports whether localai/ exists and is not an empty directory.
func (a *DriveActions) rootDirOccupied(rootDir string) (bool, error) {
	info, err := a.o.FS.Stat(rootDir)
	switch {
	case err == nil && !info.IsDir():
		return true, nil
	case err == nil:
		entries, rerr := a.o.FS.ReadDir(rootDir)
		if rerr != nil {
			return false, fmt.Errorf("read %s: %w", rootDir, rerr)
		}
		return len(entries) > 0, nil
	case errors.Is(err, fs.ErrNotExist):
		return false, nil
	}
	return false, fmt.Errorf("stat %s: %w", rootDir, err)
}

// writeExtras asks the extras writer to put START-HERE.html and the inventory copy on the drive. A
// failure is logged; the drive is set up either way and the next sync run tries again.
func (a *DriveActions) writeExtras(ctx context.Context, rootID string) {
	if a.o.Extras == nil {
		return
	}
	for _, r := range a.o.Detector.ActiveRoots() {
		if r.ID != rootID {
			continue
		}
		if err := a.o.Extras.Write(ctx, r); err != nil {
			a.log.Warn("the drive extras could not be written after init", "root", rootID, "error", err.Error())
		}
		return
	}
}

// setupCause classifies the cause of a failed init for the answer: a read-only filesystem, a full one, or
// an I/O error get their own code. ok is false when the cause is none of those.
func setupCause(err error) (errno syscall.Errno, ok bool) {
	for _, e := range []syscall.Errno{syscall.EROFS, syscall.ENOSPC, syscall.EDQUOT, syscall.EIO} {
		if errors.Is(err, e) {
			return e, true
		}
	}
	return 0, false
}
