package ark_test

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/ark"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/execx"
)

// This file holds the fakes of the drive action tests (SYNC-03): the system calls, the programs, the drive
// users and the alert sink. Nothing here touches a device or a real mount.

const udisksPath = "/usr/bin/udisksctl"

// fakeMountOps records syncfs and umount and fails them as scripted. A successful umount runs onUnmount
// (which makes the fake mount table lose the drive).
type fakeMountOps struct {
	tr *trace

	mu        sync.Mutex
	syncErr   error
	unmounts  []error // one entry per Unmount call, consumed in order; nil entries and an exhausted list succeed
	nSync     int
	nUnmount  int
	onUnmount func()
	// hold, when set, blocks Unmount until it is closed (to keep an eject in flight).
	hold chan struct{}
	// entered is closed when the first Unmount has started.
	entered chan struct{}
	once    sync.Once
}

func (f *fakeMountOps) Syncfs(mount string) error {
	f.mu.Lock()
	f.nSync++
	err := f.syncErr
	f.mu.Unlock()
	f.tr.add("syncfs %s", mount)
	return err
}

func (f *fakeMountOps) Unmount(mount string) error {
	f.mu.Lock()
	f.nUnmount++
	var err error
	if len(f.unmounts) > 0 {
		err, f.unmounts = f.unmounts[0], f.unmounts[1:]
	}
	hold, hook, entered := f.hold, f.onUnmount, f.entered
	f.mu.Unlock()
	if entered != nil {
		f.once.Do(func() { close(entered) })
	}
	if hold != nil {
		<-hold
	}
	f.tr.add("umount %s", mount)
	if err == nil && hook != nil {
		hook()
	}
	return err
}

func (f *fakeMountOps) counts() (syncs, unmounts int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.nSync, f.nUnmount
}

// fakeRunner is the execx.Runner of udisksctl: it records every call, answers through handle, and logs
// the call in the trace.
type fakeRunner struct {
	tr *trace

	mu     sync.Mutex
	calls  []execx.Cmd
	handle func(c execx.Cmd) (execx.Result, error)
}

func (f *fakeRunner) Run(_ context.Context, c execx.Cmd) (execx.Result, error) {
	f.mu.Lock()
	f.calls = append(f.calls, c)
	h := f.handle
	f.mu.Unlock()
	f.tr.add("run %s", strings.Join(append([]string{c.Path}, c.Args...), " "))
	if h == nil {
		return execx.Result{}, nil
	}
	return h(c)
}

func (f *fakeRunner) argv() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out [][]string
	for _, c := range f.calls {
		out = append(out, append([]string{c.Path}, c.Args...))
	}
	return out
}

// sleepLog is the Sleep of the actions: it records the waits and never waits.
type sleepLog struct {
	mu  sync.Mutex
	ds  []time.Duration
	err error
}

func (s *sleepLog) Sleep(_ context.Context, d time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ds = append(s.ds, d)
	return s.err
}

func (s *sleepLog) list() []time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]time.Duration(nil), s.ds...)
}

func (s *sleepLog) total() time.Duration {
	var t time.Duration
	for _, d := range s.list() {
		t += d
	}
	return t
}

// only returns the waits equal to d.
func (s *sleepLog) count(d time.Duration) int {
	n := 0
	for _, x := range s.list() {
		if x == d {
			n++
		}
	}
	return n
}

// fakeUser is a DriveUser. blockers and err are what Release answers.
type fakeUser struct {
	tr       *trace
	name     string
	blockers []ark.Blocker
	err      error

	mu       sync.Mutex
	forces   []bool
	restored int
}

func (u *fakeUser) Name() string { return u.name }

func (u *fakeUser) Release(_ context.Context, rootID string, force bool) ([]ark.Blocker, error) {
	u.mu.Lock()
	u.forces = append(u.forces, force)
	bl, err := u.blockers, u.err
	u.mu.Unlock()
	u.tr.add("release %s %s force=%v", u.name, rootID, force)
	return bl, err
}

func (u *fakeUser) set(bl []ark.Blocker, err error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.blockers, u.err = bl, err
}

func (u *fakeUser) releases() []bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]bool(nil), u.forces...)
}

// restoringUser is a fakeUser that also implements DriveRestorer.
type restoringUser struct{ *fakeUser }

func (u restoringUser) Restore(_ context.Context, rootID string) {
	u.mu.Lock()
	u.restored++
	u.mu.Unlock()
	u.tr.add("restore %s %s", u.name, rootID)
}

func (u restoringUser) restores() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.restored
}

// fakeTransfers is the TransferCanceller of the sync engine.
type fakeTransfers struct {
	tr      *trace
	running bool
}

func (f *fakeTransfers) CancelRoot(rootID string) bool {
	f.tr.add("cancel transfers %s", rootID)
	return f.running
}

// recEjector wraps the real Manager as the Ejector and logs the three calls in the trace.
type recEjector struct {
	ark.Ejector
	tr *trace
}

func (e recEjector) BeginEject(rootID string) error {
	e.tr.add("begin %s", rootID)
	return e.Ejector.BeginEject(rootID)
}

func (e recEjector) AbortEject(ctx context.Context, rootID string) {
	e.tr.add("abort %s", rootID)
	e.Ejector.AbortEject(ctx, rootID)
}

func (e recEjector) FinishEject(ctx context.Context, rootID string) {
	e.tr.add("finish %s", rootID)
	e.Ejector.FinishEject(ctx, rootID)
}

// actAlerts records the alerts of the actions (LAI-ARK-009).
type actAlerts struct {
	mu      sync.Mutex
	raised  []map[string]any
	cleared int
}

func (a *actAlerts) Raise(code string, details map[string]any, _ ...doctor.RaiseOption) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if code != doctor.ARK009 {
		return fmt.Errorf("unexpected alert %s", code)
	}
	a.raised = append(a.raised, details)
	return nil
}

func (a *actAlerts) Clear(code string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if code == doctor.ARK009 {
		a.cleared++
	}
}

func (a *actAlerts) snapshot() (raised []map[string]any, cleared int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]map[string]any(nil), a.raised...), a.cleared
}

// fakeExtras records the roots the drive extras were asked for.
type fakeExtras struct {
	mu    sync.Mutex
	roots []ark.Root
	err   error
}

func (f *fakeExtras) Write(_ context.Context, r ark.Root) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.roots = append(f.roots, r)
	return f.err
}

func (f *fakeExtras) written() []ark.Root {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]ark.Root(nil), f.roots...)
}

// ---- the rig ---------------------------------------------------------------------------------------

// actRig is the detector rig plus the actions on fakes.
type actRig struct {
	*rig
	mnt    string
	proc   *fakeProc
	mops   *fakeMountOps
	run    *fakeRunner
	sleeps *sleepLog
	xfer   *fakeTransfers
	extras *fakeExtras
	aalert *actAlerts
	acts   *ark.DriveActions
}

const selfPID = 100

// testProfiles are the profile ids the fake catalog knows.
var testProfiles = map[string]bool{"ark-drive-full": true, "ark-drive-lite": true, "ark-drive-test": true}

func profileLookup(id string) (exists, loaded bool) { return testProfiles[id], true }

func newActRig(t *testing.T, mutate ...func(*actRig, *ark.DriveActionsOptions)) *actRig {
	t.Helper()
	r := newRig(t)
	a := &actRig{rig: r, mnt: r.dir("AI-ARK"), proc: newFakeProc(), sleeps: &sleepLog{}}
	a.mops = &fakeMountOps{tr: r.tr, onUnmount: func() { r.mounts.set(r.rootDisk()) }}
	a.run = &fakeRunner{tr: r.tr}
	a.xfer = &fakeTransfers{tr: r.tr}
	a.extras = &fakeExtras{}
	a.aalert = &actAlerts{}
	a.proc.onWalk = func(int) { r.tr.add("walk") }
	o := ark.DriveActionsOptions{
		Detector: r.m, Ejector: recEjector{Ejector: r.m, tr: r.tr}, FS: r.fs,
		Runner: a.run, Udisksctl: udisksPath, Mounts: a.mops, Proc: a.proc, Sys: newFakeSys(), SelfPID: selfPID,
		Events: r.bus, Alerts: a.aalert, Extras: a.extras, Transfers: a.xfer, Profiles: profileLookup,
		Drive: ark.DriveOptions{DefaultProfile: "ark-drive-test", NodeID: "node-a", AgentVersion: "0.0.0-test", Now: fixedNow},
		Sleep: a.sleeps.Sleep,
	}
	for _, m := range mutate {
		m(a, &o)
	}
	acts, err := ark.NewDriveActions(o)
	if err != nil {
		t.Fatal(err)
	}
	a.acts = acts
	return a
}

// ready sets up an initialised drive at a.mnt and has the detector see it.
func (a *actRig) ready() {
	a.t.Helper()
	a.initDrive(a.mnt, uuidA, "")
	a.mounts.set(a.rootDisk(), a.arkDisk(120, "8:17", a.mnt, "/dev/sdb1", uuidA, "ext4"))
	a.refresh()
	a.tr.reset()
}

// uninitialized plugs in a drive with no localai/.
func (a *actRig) uninitialized() {
	a.t.Helper()
	a.mounts.set(a.rootDisk(), a.arkDisk(120, "8:17", a.mnt, "/dev/sdb1", uuidA, "ext4"))
	a.refresh()
	a.tr.reset()
}

// notMounted plugs in a drive that the table does not show; udisksctl mount (the fake runner) mounts it.
func (a *actRig) notMounted(initialised bool) {
	a.t.Helper()
	if initialised {
		a.initDrive(a.mnt, uuidA, "")
	}
	e := a.arkDisk(120, "8:17", a.mnt, "/dev/sdb1", uuidA, "ext4")
	a.block.labelled = []string{"/dev/sdb1"}
	a.mounts.set(a.rootDisk())
	a.run.handle = func(c execx.Cmd) (execx.Result, error) {
		a.mounts.set(a.rootDisk(), e)
		return execx.Result{Stdout: []byte("Mounted /dev/sdb1 at " + a.mnt + "\n")}, nil
	}
	a.refresh()
	a.tr.reset()
}

// eject runs Eject and waits for the unmount half.
func (a *actRig) eject(force bool) error {
	a.t.Helper()
	err := a.acts.Eject(context.Background(), "ark-3f9c0a12", force)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if werr := a.acts.Wait(ctx); werr != nil {
		a.t.Fatalf("the eject did not finish: %v", werr)
	}
	return err
}

func (a *actRig) user(name string) restoringUser {
	u := restoringUser{&fakeUser{tr: a.tr, name: name}}
	a.acts.RegisterUser(u)
	return u
}

// unmountedEvents is the number of drive.unmounted events published with safe_to_unplug true.
func (a *actRig) unmountedEvents() int {
	a.bus.mu.Lock()
	defer a.bus.mu.Unlock()
	n := 0
	for _, g := range a.bus.gone {
		if g.SafeToUnplug {
			n++
		}
	}
	return n
}

// rootJSON reads the ROOT.json of the drive.
func (a *actRig) rootJSON() (ark.RootJSON, error) {
	return ark.ReadRootJSON(a.mnt + "/localai/" + ark.RootJSONName)
}

// errno wraps an errno the way the os package does.
func errnoErr(op, path string, e error) error { return &os.PathError{Op: op, Path: path, Err: e} }
