package ark

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"syscall"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/events"
)

// The safe eject (ark-sync.md section 4.1.5), step by step:
//
//  1. the drive is set to ejecting (Ejector.BeginEject): it leaves the active roots, so a sync that starts
//     now excludes it;
//  2. the agent's own transfers to it are cancelled (TransferCanceller) and the eject waits up to
//     TransferWait for them to let go;
//  3. every registered DriveUser releases the root: the router unloads the models that come from it
//     unless another verified copy exists, the Kiwix library is regenerated without it, whisper.env is
//     regenerated if the whisper model came from it (steps 3 to 5 of the spec are the users' work; this
//     package does not import them);
//  6. the /proc walk (FindBlockers) lists the programs that still hold the drive open;
//  7. syncfs(2), then umount(2), retried on EBUSY;
//  8. udisksctl power-off, best effort (skipped with EjectOptions.KeepPowered);
//  9. drive.unmounted {safe_to_unplug: true}, published by Ejector.FinishEject.
//
// Steps 1 to 6 run before Eject returns, so a refusal (LAI-ARK-009 with the blockers) is the answer of the
// request, and the drive goes back to the state it had. Steps 7 to 9 run after the route has answered 202:
// a sync of a big write cache can take longer than a request may. If they fail the drive goes back to its
// previous state too, no drive.unmounted is published, LAI-ARK-009 is raised as an alert with the reason
// and the programs that held it, and the next eject starts clean.

// DriveUser is something that uses files on a drive and can let go of them: the llama router (loaded
// models), the Kiwix library, the whisper service. They register themselves with RegisterUser; this package
// does not import them.
type DriveUser interface {
	// Name identifies the user in logs.
	Name() string
	// Release stops using files under rootID. force is the request's "eject anyway": a user that would
	// otherwise refuse (a model with no other copy is loaded) lets go of it instead. It returns the
	// blockers it could not release; a nil list means it has let go. An error is a failure of its own,
	// not a blocker: without force it ends the eject, with force it is logged and the eject goes on.
	Release(ctx context.Context, rootID string, force bool) ([]Blocker, error)
}

// DriveRestorer is optionally implemented by a DriveUser that wants to take the drive back when an eject
// that already called its Release is cancelled or fails (the Kiwix library regenerated with the root, the
// models loadable again). The detector publishes no event for a drive that never left, so without this the
// user would not know.
type DriveRestorer interface {
	Restore(ctx context.Context, rootID string)
}

// RegisterUser adds a DriveUser. Users are released in registration order.
func (a *DriveActions) RegisterUser(u DriveUser) {
	if u == nil {
		return
	}
	a.mu.Lock()
	a.users = append(a.users, u)
	a.mu.Unlock()
}

func (a *DriveActions) registeredUsers() []DriveUser {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]DriveUser(nil), a.users...)
}

// ejection is one eject in progress.
type ejection struct {
	rootID string
	drive  Drive
	mount  string
	force  bool
	// keepPowered skips step 8: `aictl ark repair` and `new-id` need the disk after the unmount.
	keepPowered bool
	// active: the drive was in the active set, so BeginEject ran and FinishEject will announce it.
	active bool
	// released are the users whose Release has run, for Restore on a cancel.
	released []DriveUser
	unlock   func()
}

// Eject runs steps 1 to 6 and, when nothing blocks, starts steps 7 to 9 in the background and returns nil
// (the route answers 202 ejecting). It returns *BlockedError when something still uses the drive, a
// *WrongStateError for a drive that is not mounted, *BusyError when another action holds the drive, and
// ErrNoSuchDrive for an unknown root id. The drive goes back to its previous state on every error.
//
// A drive that is mounted but not in the active set (uninitialized, or one whose ROOT.json is damaged) is
// ejected too, so a new drive can be set aside without setting it up: nothing uses it, so only the /proc
// walk applies, and the drive.unmounted event is published here.
func (a *DriveActions) Eject(ctx context.Context, rootID string, force bool) error {
	return a.EjectWith(ctx, rootID, EjectOptions{Force: force})
}

// EjectOptions are the choices of one eject.
type EjectOptions struct {
	// Force is "eject anyway": a model with no other copy is unloaded instead of refusing.
	Force bool
	// KeepPowered leaves the disk powered after the unmount (no step 8), for a repair or a new id
	// that runs on the unmounted filesystem right after (ark-sync.md section 4.17).
	KeepPowered bool
}

// EjectWith is Eject with every option.
func (a *DriveActions) EjectWith(ctx context.Context, rootID string, o EjectOptions) error {
	force := o.Force
	unlock, err := a.lock(rootID, "eject")
	if err != nil {
		return err
	}
	d, err := a.find(rootID)
	if err != nil {
		unlock()
		return err
	}
	if d.State == StateEjecting || d.Mount == "" {
		unlock()
		return &WrongStateError{RootID: rootID, State: d.State}
	}
	mount := filepath.Clean(d.Mount)
	if mount == "/" || !filepath.IsAbs(mount) {
		unlock()
		return fmt.Errorf("ark: refusing to eject %q: not a drive mount point", d.Mount)
	}
	e := &ejection{rootID: rootID, drive: d, mount: mount, force: force, keepPowered: o.KeepPowered, unlock: unlock}

	// Step 1.
	if d.Active() {
		if err := a.o.Ejector.BeginEject(rootID); err != nil {
			unlock()
			switch {
			case errors.Is(err, ErrAlreadyEjecting):
				return &WrongStateError{RootID: rootID, State: StateEjecting}
			case errors.Is(err, ErrNoSuchDrive):
				// It left the active set between the lookup and now (unplugged, or unusable): look again.
				return fmt.Errorf("%w: %q", ErrNoSuchDrive, rootID)
			}
			return err
		}
		e.active = true
	}

	if err := a.prepareEject(ctx, e); err != nil {
		a.cancelEject(ctx, e)
		unlock()
		return err
	}
	a.log.Info("eject accepted", "root", rootID, "mount", mount, "force", force, "keep_powered", o.KeepPowered)
	a.wg.Add(1)
	go a.finishEject(ctx, e)
	return nil
}

// prepareEject is steps 2 to 6.
func (a *DriveActions) prepareEject(ctx context.Context, e *ejection) error {
	if e.active {
		// Step 2.
		if a.o.Transfers != nil && a.o.Transfers.CancelRoot(e.rootID) {
			a.log.Info("transfers to the drive are being stopped for the eject", "root", e.rootID)
		}
		a.waitSelfIdle(ctx, e.mount)

		// Steps 3 to 5.
		if blockers, err := a.releaseUsers(ctx, e); err != nil {
			return err
		} else if len(blockers) > 0 {
			return &BlockedError{RootID: e.rootID, Blockers: capBlockers(blockers)}
		}
	}

	// Step 6.
	blockers, err := FindBlockers(a.o.Proc, e.mount, a.o.SelfPID)
	if err != nil {
		// /proc cannot be read: the umount decides (EBUSY if something does hold the drive).
		a.log.Warn("the programs using the drive could not be listed; the unmount will tell", "root", e.rootID, "error", err.Error())
		return nil
	}
	if len(blockers) > 0 {
		return &BlockedError{RootID: e.rootID, Blockers: blockers}
	}
	return nil
}

// waitSelfIdle waits until the agent has no file open on the drive, at most TransferWait. Whatever is
// still open then is listed as a sync blocker by the walk of step 6.
func (a *DriveActions) waitSelfIdle(ctx context.Context, mount string) {
	for waited := time.Duration(0); ; waited += pollStep {
		blockers, err := FindBlockers(a.o.Proc, mount, a.o.SelfPID)
		if err != nil || !hasKind(blockers, BlockerSync) || waited >= a.o.TransferWait {
			return
		}
		if a.o.Sleep(ctx, pollStep) != nil {
			return
		}
	}
}

func hasKind(bs []Blocker, kind string) bool {
	for _, b := range bs {
		if b.Kind == kind {
			return true
		}
	}
	return false
}

// releaseUsers calls Release on every user, in order, and returns what they could not release. Every user
// is asked even when an earlier one refused, so the person gets the whole list at once.
func (a *DriveActions) releaseUsers(ctx context.Context, e *ejection) ([]Blocker, error) {
	var all []Blocker
	for _, u := range a.registeredUsers() {
		e.released = append(e.released, u)
		blockers, err := u.Release(ctx, e.rootID, e.force)
		if err != nil {
			if !e.force {
				return nil, fmt.Errorf("release %s: %w", u.Name(), err)
			}
			a.log.Warn("a drive user failed to let go; ejecting anyway", "user", u.Name(), "root", e.rootID, "error", err.Error())
		}
		for _, b := range blockers {
			if b.Kind == "" {
				b.Kind = BlockerModel
			}
			all = append(all, b)
		}
	}
	return all, nil
}

func capBlockers(bs []Blocker) []Blocker {
	if len(bs) > maxBlockers {
		return bs[:maxBlockers]
	}
	return bs
}

// cancelEject puts the drive back: the state returns to what it was and the users that already let go
// are told to take it again. It does not need the request's context, which may be the thing that ended.
func (a *DriveActions) cancelEject(ctx context.Context, e *ejection) {
	ctx = context.WithoutCancel(ctx)
	if e.active {
		a.o.Ejector.AbortEject(ctx, e.rootID)
	}
	for i := len(e.released) - 1; i >= 0; i-- {
		if r, ok := e.released[i].(DriveRestorer); ok {
			r.Restore(ctx, e.rootID)
		}
	}
}

// finishEject is steps 7 to 9, on its own context: the request that started it has answered.
func (a *DriveActions) finishEject(base context.Context, e *ejection) {
	defer a.wg.Done()
	defer e.unlock()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(base), a.o.FinishTimeout)
	defer cancel()

	if err := a.syncAndUnmount(ctx, e.mount); err != nil {
		a.failEject(ctx, e, err)
		return
	}
	if !e.keepPowered {
		a.powerOff(ctx, e.drive.Device)
	}

	// Step 9. For an active drive the detector announces it (and runs the OnRemoved hooks).
	if e.active {
		a.o.Ejector.FinishEject(ctx, e.rootID)
	} else {
		if err := a.o.Detector.Refresh(ctx); err != nil {
			a.log.Warn("the drive scan after an eject failed", "root", e.rootID, "error", err.Error())
		}
		a.publishUnmounted(e.rootID)
	}
	if a.o.Alerts != nil {
		a.o.Alerts.Clear(CodeEjectBlocked)
	}
	a.log.Info("drive ejected; it can be unplugged", "root", e.rootID, "mount", e.mount)
}

// syncAndUnmount is step 7. A failed syncfs is logged and the unmount goes on: umount flushes too, and its
// answer is the one that matters.
func (a *DriveActions) syncAndUnmount(ctx context.Context, mount string) error {
	if err := a.o.Mounts.Syncfs(mount); err != nil {
		a.log.Warn("syncfs failed before the unmount", "mount", mount, "error", err.Error())
	}
	for attempt := 0; ; attempt++ {
		err := a.o.Mounts.Unmount(mount)
		switch {
		case err == nil:
			return nil
		case errors.Is(err, syscall.EINVAL):
			// Not a mount point any more: somebody (the file manager) unmounted it first.
			a.log.Info("the drive was already unmounted", "mount", mount)
			return nil
		case !errors.Is(err, syscall.EBUSY) || attempt >= a.o.UnmountRetries:
			return err
		}
		a.log.Info("the drive is busy; trying to unmount again", "mount", mount, "attempt", attempt+1)
		if serr := a.o.Sleep(ctx, a.o.UnmountDelay); serr != nil {
			return serr
		}
	}
}

// powerOff is step 8: spin the disk down and tell the bus it can be removed. It is best effort (some USB
// bridges do not support it, and a device-mapper volume has no disk to power off) and never fails the
// eject.
func (a *DriveActions) powerOff(ctx context.Context, device string) {
	disk := WholeDisk(a.o.Sys, device)
	if disk == "" || !validDevicePath(disk) {
		a.log.Info("no whole disk to power off", "device", device)
		return
	}
	res, err := a.udisks(ctx, "power-off", "--no-user-interaction", "-b", disk)
	switch {
	case err != nil:
		a.log.Info("the disk was not powered off", "device", disk, "error", err.Error())
	case res.ExitCode != 0:
		a.log.Info("the disk was not powered off", "device", disk, "exit", res.ExitCode, "stderr", firstLine(res.Stderr))
	}
}

func (a *DriveActions) publishUnmounted(rootID string) {
	if a.o.Events == nil {
		return
	}
	if err := a.o.Events.Publish(events.TypeDriveUnmounted, "", events.DriveUnmounted{RootID: rootID, SafeToUnplug: true}); err != nil {
		a.log.Warn("drive.unmounted was refused by the bus", "root", rootID, "error", err.Error())
	}
}

// failEject handles an unmount that did not work after the route answered: the drive goes back, and
// LAI-ARK-009 is raised with the reason and whoever holds the drive now, because nobody is waiting on a
// response to read it.
func (a *DriveActions) failEject(ctx context.Context, e *ejection, cause error) {
	blockers, _ := FindBlockers(a.o.Proc, e.mount, a.o.SelfPID)
	a.log.Warn("the eject failed; the drive stays mounted", "root", e.rootID, "mount", e.mount, "error", cause.Error(), "blockers", len(blockers))
	a.cancelEject(ctx, e)
	if a.o.Alerts == nil {
		return
	}
	details := map[string]any{"root_id": e.rootID, "reason": errnoText(cause), "blockers": blockers}
	if err := a.o.Alerts.Raise(CodeEjectBlocked, details); err != nil {
		a.log.Warn("the eject alert could not be raised", "error", err.Error())
	}
}
