package ark

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/execx"
)

// This file is the drive manager's action side (ark-sync.md section 4.1.5 and 3.5, SYNC-03): what the
// detector (detect.go) does not do. The detector watches the mount table and never changes anything; the
// DriveActions below are the four things a person asks of a drive: eject it (eject.go), set it up (init.go),
// mount it (mount.go) and change the profile it follows (profile.go). Every outside effect goes through a
// seam of this package (Detector, Ejector, FS, execx.Runner, MountOps, ProcFS, SymlinkResolver,
// EventPublisher, AlertSink), so the tests run it on fakes and never touch a device.
//
// The actions never format a drive and never delete content: init creates a directory tree and one JSON
// file on a filesystem that has none, and eject unmounts. Formatting, checking and re-identifying a disk
// are `sudo aictl ark format|repair|new-id` (internal/ark/prep), never the agent's (ark-sync.md section
// 4.17.1).

// Defaults of DriveActionsOptions.
const (
	// DefaultTransferWait is how long an eject waits for the agent's own transfers to let go of the
	// drive (ark-sync.md section 4.1.5 step 2).
	DefaultTransferWait = 30 * time.Second
	// DefaultUnmountRetries and DefaultUnmountDelay are the EBUSY policy of step 7: after the first
	// attempt, retry 3 times, 2 s apart.
	DefaultUnmountRetries = 3
	DefaultUnmountDelay   = 2 * time.Second
	// DefaultMountWait is how long Mount waits for the mount table to show a drive udisksctl mounted.
	DefaultMountWait = 5 * time.Second
	// DefaultFinishTimeout bounds the unmount half of an eject, which runs after the route answered.
	DefaultFinishTimeout = 2 * time.Minute

	// pollStep is the step of the two waits above (self-idle and mount table).
	pollStep = 250 * time.Millisecond
	// udisksTimeout bounds one udisksctl call; powering a disk off can take a few seconds.
	udisksTimeout = 30 * time.Second
	udisksMaxOut  = 64 << 10
)

// Errors of the actions. Each maps to one documented answer in drive_routes.go.
var (
	// ErrUdisksUnavailable: udisksctl is not installed, so a drive cannot be mounted.
	ErrUdisksUnavailable = errors.New("ark: udisksctl is not available")
	// ErrDriveReadOnly: the drive cannot be written (a read-only mount, an unclean filesystem).
	ErrDriveReadOnly = errors.New("ark: the drive is read-only")
	// ErrNoCatalog: no catalog is loaded, so a profile id cannot be checked.
	ErrNoCatalog = errors.New("ark: no catalog is loaded")
)

// WrongStateError: the action does not apply to a drive in this state (ark-sync.md section 3.5:
// "409 LAI-AGT-020 with details.state").
type WrongStateError struct {
	RootID string
	// State is the drive's current state.
	State DriveState
	// Reason refines the answer when the state alone does not say it ("localai_exists").
	Reason string
}

// Error implements error.
func (e *WrongStateError) Error() string {
	s := fmt.Sprintf("ark: drive %s is %s", e.RootID, e.State)
	if e.Reason != "" {
		s += " (" + e.Reason + ")"
	}
	return s
}

// BusyError: another action is running on this drive.
type BusyError struct {
	RootID string
	// Operation is the one that holds the drive: eject, init, mount or profile.
	Operation string
}

// Error implements error.
func (e *BusyError) Error() string {
	return fmt.Sprintf("ark: drive %s is busy with %s", e.RootID, e.Operation)
}

// BlockedError: the eject was refused because something still uses the drive (LAI-ARK-009).
type BlockedError struct {
	RootID   string
	Blockers []Blocker
}

// Error implements error.
func (e *BlockedError) Error() string {
	names := make([]string, 0, len(e.Blockers))
	for _, b := range e.Blockers {
		names = append(names, b.Name)
	}
	return fmt.Sprintf("ark: drive %s is in use by %s", e.RootID, strings.Join(names, ", "))
}

// MountError: udisksctl could not mount the drive.
type MountError struct {
	Device string
	// Reason is the first line udisksctl printed, cut down; "" when it printed nothing.
	Reason string
	Err    error
}

// Error implements error.
func (e *MountError) Error() string {
	s := "ark: could not mount " + e.Device
	if e.Reason != "" {
		s += ": " + e.Reason
	}
	return s
}

// Unwrap returns the underlying error.
func (e *MountError) Unwrap() error { return e.Err }

// SetupError: init could not write the drive.
type SetupError struct {
	RootID string
	Err    error
}

// Error implements error.
func (e *SetupError) Error() string {
	return fmt.Sprintf("ark: drive %s could not be set up: %v", e.RootID, e.Err)
}

// Unwrap returns the underlying error.
func (e *SetupError) Unwrap() error { return e.Err }

// UnknownProfileError: the profile id is not in the catalog.
type UnknownProfileError struct{ Profile string }

// Error implements error.
func (e *UnknownProfileError) Error() string { return "ark: unknown profile " + e.Profile }

// ExtrasWriter writes START-HERE.html and the inventory copy on a drive that was just set up
// (ark-sync.md section 4.15). internal/app's driveExtras implements it.
type ExtrasWriter interface {
	Write(ctx context.Context, r Root) error
}

// TransferCanceller stops the agent's own transfers that write to a root (step 2 of the eject). The sync
// engine's RootGone is behind it. CancelRoot does not wait: it reports whether anything was running for
// the root, and the eject waits by looking for the agent's own open files on the drive.
type TransferCanceller interface {
	CancelRoot(rootID string) bool
}

// ProfileLookup says whether a profile id is in the catalog in use. loaded is false when there is no
// catalog (the answer is then ErrNoCatalog, never "unknown profile").
type ProfileLookup func(id string) (exists, loaded bool)

// DriveActionsOptions configures DriveActions. Detector, Ejector and FS are required.
type DriveActionsOptions struct {
	// Detector and Ejector are the drive manager; *Manager is both.
	Detector Detector
	Ejector  Ejector
	// FS is the filesystem of the drive (init and set-profile write through it).
	FS FS
	// Runner and Udisksctl run udisksctl for Mount and for the power-off of an eject. An empty Udisksctl
	// means the program is not installed.
	Runner    execx.Runner
	Udisksctl string
	// Mounts does syncfs and umount; default OSMountOps.
	Mounts MountOps
	// Proc is /proc and Sys is sysfs; the defaults are OSProc and OSSys.
	Proc ProcFS
	Sys  SysFS
	// SelfPID is the agent's pid, which the blocker walk labels as the agent's own work; default
	// os.Getpid.
	SelfPID int
	// Events publishes drive.unmounted for the eject of a drive that was never in the active set (one
	// the detector never announced); the eject of an active drive is announced by Ejector.FinishEject.
	Events EventPublisher
	// Alerts holds LAI-ARK-009 when an eject that was accepted then failed; nil raises nothing.
	Alerts AlertSink
	// Extras writes the drive extras after init; nil writes none.
	Extras ExtrasWriter
	// Transfers stops the agent's transfers during an eject; nil means there are none.
	Transfers TransferCanceller
	// Profiles checks a profile id for set-profile; nil refuses every profile with ErrNoCatalog.
	Profiles ProfileLookup
	// Drive holds what a new ROOT.json records: the default profile, the node id, the agent version
	// and the clock (the same DriveOptions the detector uses).
	Drive DriveOptions
	// Owner, when set, is given the top localai/ directory of a drive that init creates.
	Owner *Owner
	// Sleep waits d or until ctx ends; default a timer. Tests count the waits instead of making them.
	Sleep func(ctx context.Context, d time.Duration) error

	// The waits. Zero means the default; a negative UnmountRetries means none.
	TransferWait   time.Duration
	UnmountRetries int
	UnmountDelay   time.Duration
	MountWait      time.Duration
	FinishTimeout  time.Duration

	Logger *slog.Logger
}

// DriveActions runs eject, init, mount and set-profile on the drives of a Detector.
type DriveActions struct {
	o   DriveActionsOptions
	log *slog.Logger

	mu    sync.Mutex
	busy  map[string]string // root id -> the operation running on it
	users []DriveUser

	// wg tracks the unmount half of ejects that were accepted; Wait joins it.
	wg sync.WaitGroup
}

// NewDriveActions checks the options and returns the actions.
func NewDriveActions(o DriveActionsOptions) (*DriveActions, error) {
	var missing []string
	if o.Detector == nil {
		missing = append(missing, "Detector")
	}
	if o.Ejector == nil {
		missing = append(missing, "Ejector")
	}
	if o.FS == nil {
		missing = append(missing, "FS")
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("ark: the drive actions need %v", missing)
	}
	if o.Mounts == nil {
		o.Mounts = OSMountOps{}
	}
	if o.Proc == nil {
		o.Proc = OSProc{}
	}
	if o.Sys == nil {
		o.Sys = OSSys{}
	}
	if o.SelfPID == 0 {
		o.SelfPID = os.Getpid()
	}
	if o.Sleep == nil {
		o.Sleep = sleepCtx
	}
	if o.TransferWait <= 0 {
		o.TransferWait = DefaultTransferWait
	}
	if o.UnmountRetries == 0 {
		o.UnmountRetries = DefaultUnmountRetries
	}
	if o.UnmountDelay <= 0 {
		o.UnmountDelay = DefaultUnmountDelay
	}
	if o.MountWait <= 0 {
		o.MountWait = DefaultMountWait
	}
	if o.FinishTimeout <= 0 {
		o.FinishTimeout = DefaultFinishTimeout
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.Drive.Now == nil {
		o.Drive.Now = time.Now
	}
	return &DriveActions{o: o, log: o.Logger.With("component", "ark"), busy: map[string]string{}}, nil
}

// Wait blocks until every eject that was accepted has finished its unmount half, or ctx ends. The
// daemon calls it when it stops; the tests call it instead of sleeping.
func (a *DriveActions) Wait(ctx context.Context) error {
	done := make(chan struct{})
	go func() { a.wg.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// lock marks the drive as busy with op until the returned function is called. A second action on the
// same drive is a BusyError, so an eject and an init of one drive never interleave.
func (a *DriveActions) lock(rootID, op string) (func(), error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if cur, ok := a.busy[rootID]; ok {
		return nil, &BusyError{RootID: rootID, Operation: cur}
	}
	a.busy[rootID] = op
	var once sync.Once
	return func() {
		once.Do(func() {
			a.mu.Lock()
			delete(a.busy, rootID)
			a.mu.Unlock()
		})
	}, nil
}

// find returns the drive for an action, or ErrNoSuchDrive. Two clones share a root id; the detector
// answers with the one in use (the earlier mount), and so do the actions: the other one can be neither
// ejected, set up nor mounted by id.
func (a *DriveActions) find(rootID string) (Drive, error) {
	d, ok := a.o.Detector.Drive(rootID)
	if !ok {
		return Drive{}, fmt.Errorf("%w: %q", ErrNoSuchDrive, rootID)
	}
	return d, nil
}

// udisks runs udisksctl with a fixed argv.
func (a *DriveActions) udisks(ctx context.Context, args ...string) (execx.Result, error) {
	if a.o.Udisksctl == "" || a.o.Runner == nil {
		return execx.Result{}, ErrUdisksUnavailable
	}
	return a.o.Runner.Run(ctx, execx.Cmd{Path: a.o.Udisksctl, Args: args, Timeout: udisksTimeout, MaxOut: udisksMaxOut})
}

// sleepCtx waits d or until ctx ends.
func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
