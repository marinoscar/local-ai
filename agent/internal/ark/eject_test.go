package ark_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/ark"
	"github.com/marinoscar/local-ai/agent/internal/execx"
)

const rid = "ark-3f9c0a12"

func asBlocked(t *testing.T, err error) *ark.BlockedError {
	t.Helper()
	var b *ark.BlockedError
	if !errors.As(err, &b) {
		t.Fatalf("err = %v, want *BlockedError", err)
	}
	return b
}

func asWrongState(t *testing.T, err error) *ark.WrongStateError {
	t.Helper()
	var w *ark.WrongStateError
	if !errors.As(err, &w) {
		t.Fatalf("err = %v, want *WrongStateError", err)
	}
	return w
}

// A clean eject runs the steps of ark-sync.md section 4.1.5 in order, ends with the drive unmounted and
// exactly one drive.unmounted {safe_to_unplug: true}, and the eject flow publishes none of its own.
func TestEjectRunsTheStepsInOrder(t *testing.T) {
	a := newActRig(t)
	a.ready()
	a.user("kiwix")
	a.user("router")

	if err := a.eject(false); err != nil {
		t.Fatalf("Eject: %v", err)
	}
	want := []string{
		"begin " + rid, "active []", // 1: the drive leaves the active roots
		"cancel transfers " + rid, "walk", // 2: transfers, and the wait for the agent to let go
		"release kiwix " + rid + " force=false", // 3-5: the drive users
		"release router " + rid + " force=false",
		"walk",                               // 6: who holds the drive
		"syncfs " + a.mnt, "umount " + a.mnt, // 7
		"run " + udisksPath + " power-off --no-user-interaction -b /dev/sdb", // 8: the whole disk
		"finish " + rid, "hook " + rid, "event unmounted " + rid + " safe=true", // 9
	}
	if got := a.tr.list(); !reflect.DeepEqual(got, want) {
		t.Errorf("trace:\n got %q\nwant %q", got, want)
	}
	if a.unmountedEvents() != 1 {
		t.Errorf("drive.unmounted events = %d, want exactly 1", a.unmountedEvents())
	}
	if len(a.m.Drives()) != 0 || len(a.m.ActiveRoots()) != 0 {
		t.Errorf("drives = %+v", a.m.Drives())
	}
	if raised, cleared := a.aalert.snapshot(); len(raised) != 0 || cleared != 1 {
		t.Errorf("alerts raised %v cleared %d", raised, cleared)
	}
	if a.alerts.isActive() {
		t.Error("a clean eject must not raise LAI-ARK-007")
	}
}

// KeepPowered (aictl ark repair and new-id) runs every step but the power-off: the filesystem is checked or
// given a new id right after, so the disk must stay.
func TestEjectKeepPoweredSkipsThePowerOff(t *testing.T) {
	a := newActRig(t)
	a.ready()
	err := a.acts.EjectWith(context.Background(), rid, ark.EjectOptions{KeepPowered: true})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if werr := a.acts.Wait(ctx); werr != nil || err != nil {
		t.Fatalf("eject %v, wait %v", err, werr)
	}
	for _, step := range a.tr.list() {
		if strings.Contains(step, "power-off") {
			t.Fatalf("powered off: %q", a.tr.list())
		}
	}
	if a.unmountedEvents() != 1 {
		t.Errorf("drive.unmounted events = %d", a.unmountedEvents())
	}
}

// A model whose only copy is on the drive refuses the eject (LAI-ARK-009) and names the model; the drive
// goes back to the state it had, the users that let go are told to take it back, and a retry works.
func TestEjectRefusedByAModelOnlyOnTheDrive(t *testing.T) {
	a := newActRig(t)
	a.ready()
	kiwix := a.user("kiwix")
	router := a.user("router")
	router.set([]ark.Blocker{{Kind: ark.BlockerModel, Name: "Brain X", PID: 0}}, nil)

	err := a.eject(false)
	b := asBlocked(t, err)
	if b.RootID != rid || !reflect.DeepEqual(b.Blockers, []ark.Blocker{{Kind: ark.BlockerModel, Name: "Brain X"}}) {
		t.Fatalf("blocked = %+v", b)
	}
	want := []string{
		"begin " + rid, "active []",
		"cancel transfers " + rid, "walk",
		"release kiwix " + rid + " force=false",
		"release router " + rid + " force=false",
		"abort " + rid, "active [" + rid + "]", // the drive is back
		"restore router " + rid, "restore kiwix " + rid, // in reverse order
	}
	if got := a.tr.list(); !reflect.DeepEqual(got, want) {
		t.Errorf("trace:\n got %q\nwant %q", got, want)
	}
	syncs, unmounts := a.mops.counts()
	if syncs != 0 || unmounts != 0 {
		t.Errorf("a refused eject touched the mount: syncfs %d umount %d", syncs, unmounts)
	}
	if d, ok := a.m.Drive(rid); !ok || d.State != ark.StateReady || d.ReadOnly || len(a.m.ActiveRoots()) != 1 {
		t.Errorf("drive after the refusal = %+v %v", d, ok)
	}
	if a.unmountedEvents() != 0 {
		t.Error("a refused eject published drive.unmounted")
	}
	if kiwix.restores() != 1 || router.restores() != 1 {
		t.Errorf("restores kiwix %d router %d", kiwix.restores(), router.restores())
	}

	// The person switches brain; the same eject now works.
	router.set(nil, nil)
	if err := a.eject(false); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if a.unmountedEvents() != 1 {
		t.Errorf("retry: unmounted events = %d", a.unmountedEvents())
	}
}

// force reaches every user, so the router can unload a model that has no other copy instead of refusing.
func TestEjectForceIsPassedToTheUsers(t *testing.T) {
	a := newActRig(t)
	a.ready()
	u := a.user("router")
	if err := a.eject(true); err != nil {
		t.Fatal(err)
	}
	if got := u.releases(); !reflect.DeepEqual(got, []bool{true}) {
		t.Errorf("force seen by the user = %v", got)
	}
}

// force does not override a program that holds the drive open, and a blocker a user could not release
// refuses even a forced eject.
func TestEjectForceDoesNotOverrideRealBlockers(t *testing.T) {
	a := newActRig(t)
	a.ready()
	u := a.user("router")
	u.set([]ark.Blocker{{Kind: ark.BlockerModel, Name: "Brain X"}}, nil)
	asBlocked(t, a.eject(true))

	u.set(nil, nil)
	a.proc.add(4242, "bash", a.mnt+"/localai")
	b := asBlocked(t, a.eject(true))
	if !reflect.DeepEqual(b.Blockers, []ark.Blocker{{Kind: ark.BlockerProcess, Name: "bash", PID: 4242}}) {
		t.Errorf("blockers = %+v", b.Blockers)
	}
	if syncs, unmounts := a.mops.counts(); syncs != 0 || unmounts != 0 {
		t.Errorf("syncfs %d umount %d", syncs, unmounts)
	}
}

// A shell whose working directory is on the drive is listed, by name and pid; the drive stays usable.
func TestEjectListsAShellWithItsCwdOnTheDrive(t *testing.T) {
	a := newActRig(t)
	a.ready()
	a.user("kiwix")
	a.proc.add(4242, "bash", a.mnt+"/localai/models", "/dev/pts/3")
	a.proc.add(7, "cron", "/", "/dev/null")

	b := asBlocked(t, a.eject(false))
	if !reflect.DeepEqual(b.Blockers, []ark.Blocker{{Kind: ark.BlockerProcess, Name: "bash", PID: 4242}}) {
		t.Fatalf("blockers = %+v", b.Blockers)
	}
	want := []string{
		"begin " + rid, "active []",
		"cancel transfers " + rid, "walk",
		"release kiwix " + rid + " force=false",
		"walk", // the users first, then the /proc walk (step 6)
		"abort " + rid, "active [" + rid + "]",
		"restore kiwix " + rid,
	}
	if got := a.tr.list(); !reflect.DeepEqual(got, want) {
		t.Errorf("trace:\n got %q\nwant %q", got, want)
	}
	if d := a.only(); d.State != ark.StateReady {
		t.Errorf("state after the refusal = %s", d.State)
	}

	// cd ~ and try again.
	a.proc.drop(4242)
	if err := a.eject(false); err != nil {
		t.Fatalf("retry: %v", err)
	}
}

// The agent's own transfers get up to 30 s to let go; whatever is still open then is a sync blocker.
func TestEjectWaitsForTheAgentsOwnTransfers(t *testing.T) {
	t.Run("they stop after a few polls", func(t *testing.T) {
		a := newActRig(t)
		a.ready()
		a.xfer.running = true
		a.proc.add(selfPID, "aiagent", a.mnt+"/localai/.staging/m.part")
		a.proc.onWalk = func(n int) {
			if n == 4 {
				a.proc.drop(selfPID)
			}
		}
		if err := a.eject(false); err != nil {
			t.Fatal(err)
		}
		if got := a.sleeps.count(250 * time.Millisecond); got != 3 {
			t.Errorf("polls = %d, want 3", got)
		}
	})
	t.Run("they never stop", func(t *testing.T) {
		a := newActRig(t)
		a.ready()
		a.proc.add(selfPID, "aiagent", a.mnt+"/localai/.staging/m.part")
		b := asBlocked(t, a.eject(false))
		if !reflect.DeepEqual(b.Blockers, []ark.Blocker{{Kind: ark.BlockerSync, Name: "sync", PID: selfPID}}) {
			t.Errorf("blockers = %+v", b.Blockers)
		}
		if got := a.sleeps.total(); got != ark.DefaultTransferWait {
			t.Errorf("waited %v, want %v", got, ark.DefaultTransferWait)
		}
		if d := a.only(); d.State != ark.StateReady {
			t.Errorf("state = %s", d.State)
		}
	})
	t.Run("a shorter wait is honoured", func(t *testing.T) {
		a := newActRig(t, func(_ *actRig, o *ark.DriveActionsOptions) { o.TransferWait = time.Second })
		a.ready()
		a.proc.add(selfPID, "aiagent", a.mnt+"/x")
		asBlocked(t, a.eject(false))
		if got := a.sleeps.total(); got != time.Second {
			t.Errorf("waited %v", got)
		}
	})
}

// The unmount: EBUSY is retried 3 times, 2 s apart; other failures are not; a failure after the route
// answered puts the drive back, raises LAI-ARK-009 with the reason, and leaves the drive ejectable.
func TestEjectUnmountPolicy(t *testing.T) {
	busy := errnoErr("umount", "x", syscall.EBUSY)
	for _, tc := range []struct {
		name     string
		unmounts []error
		wantOK   bool
		calls    int
		retries  int // sleeps of 2 s
		reason   string
		gone     bool // the mount table no longer shows the drive when umount is called
	}{
		{name: "first try", wantOK: true, calls: 1},
		{name: "busy twice, then it works", unmounts: []error{busy, busy}, wantOK: true, calls: 3, retries: 2},
		{name: "busy three times, then it works", unmounts: []error{busy, busy, busy}, wantOK: true, calls: 4, retries: 3},
		{name: "busy every time", unmounts: []error{busy, busy, busy, busy, busy}, calls: 4, retries: 3, reason: "device or resource busy"},
		{name: "an I/O error is not retried", unmounts: []error{syscall.EIO}, calls: 1, reason: "input/output error"},
		{name: "a permission error is not retried", unmounts: []error{syscall.EPERM}, calls: 1, reason: "operation not permitted"},
		{name: "already unmounted by somebody else", unmounts: []error{syscall.EINVAL}, wantOK: true, calls: 1, gone: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := newActRig(t)
			a.ready()
			a.mops.unmounts = tc.unmounts
			if tc.gone {
				// The file manager unmounted it first: the table has lost the drive already.
				a.mounts.set(a.rootDisk())
			}
			if err := a.eject(false); err != nil {
				t.Fatalf("Eject: %v (the unmount half reports through the alert, never here)", err)
			}
			if _, unmounts := a.mops.counts(); unmounts != tc.calls {
				t.Errorf("umount calls = %d, want %d", unmounts, tc.calls)
			}
			if got := a.sleeps.count(ark.DefaultUnmountDelay); got != tc.retries {
				t.Errorf("retry waits = %d, want %d", got, tc.retries)
			}
			raised, cleared := a.aalert.snapshot()
			if tc.wantOK {
				if a.unmountedEvents() != 1 || len(raised) != 0 || cleared != 1 {
					t.Errorf("events %d raised %v cleared %d", a.unmountedEvents(), raised, cleared)
				}
				return
			}
			// Failed: nothing published, the alert says why, the drive is back and can be ejected again.
			if a.unmountedEvents() != 0 {
				t.Error("a failed eject published drive.unmounted")
			}
			if len(raised) != 1 || raised[0]["reason"] != tc.reason || raised[0]["root_id"] != rid {
				t.Errorf("alert = %v", raised)
			}
			if d := a.only(); d.State != ark.StateReady || len(a.m.ActiveRoots()) != 1 {
				t.Errorf("drive after the failure = %+v", d)
			}
			a.mops.unmounts = nil
			if err := a.eject(false); err != nil || a.unmountedEvents() != 1 {
				t.Errorf("retry: %v, events %d", err, a.unmountedEvents())
			}
			if _, cleared := a.aalert.snapshot(); cleared != 1 {
				t.Errorf("the alert was not cleared by the successful retry (%d)", cleared)
			}
		})
	}
}

// The alert of a failed unmount names whoever holds the drive then.
func TestEjectFailureAlertListsTheBlockers(t *testing.T) {
	a := newActRig(t)
	a.ready()
	busy := errnoErr("umount", "x", syscall.EBUSY)
	a.mops.unmounts = []error{busy, busy, busy, busy}
	// The process opens a file after the walk of step 6 passed.
	a.mops.onUnmount = nil
	a.sleeps.err = nil
	n := 0
	a.proc.onWalk = func(int) {
		n++
		if n == 3 { // walks 1 and 2 are the wait and step 6; this is the one after the failure
			a.proc.add(555, "rsync", a.mnt+"/localai/models/x")
		}
	}
	if err := a.eject(false); err != nil {
		t.Fatal(err)
	}
	raised, _ := a.aalert.snapshot()
	if len(raised) != 1 {
		t.Fatalf("alerts = %v", raised)
	}
	got, _ := raised[0]["blockers"].([]ark.Blocker)
	if !reflect.DeepEqual(got, []ark.Blocker{{Kind: ark.BlockerProcess, Name: "rsync", PID: 555}}) {
		t.Errorf("blockers in the alert = %+v", raised[0]["blockers"])
	}
}

// A failed syncfs is logged and the unmount decides.
func TestEjectGoesOnAfterAFailedSyncfs(t *testing.T) {
	a := newActRig(t)
	a.ready()
	a.mops.syncErr = syscall.EIO
	if err := a.eject(false); err != nil {
		t.Fatal(err)
	}
	if a.unmountedEvents() != 1 {
		t.Error("the eject should have completed")
	}
}

// The power-off is best effort: no whole disk, a failing udisksctl and no udisksctl at all never fail the eject.
func TestEjectPowerOffIsBestEffort(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*actRig, *ark.DriveActionsOptions)
		runs   int
	}{
		{name: "udisksctl fails", runs: 1, mutate: func(a *actRig, _ *ark.DriveActionsOptions) {
			a.run.handle = func(execx.Cmd) (execx.Result, error) {
				return execx.Result{ExitCode: 1, Stderr: []byte("Error powering off\n")}, nil
			}
		}},
		{name: "udisksctl cannot be started", runs: 1, mutate: func(a *actRig, _ *ark.DriveActionsOptions) {
			a.run.handle = func(execx.Cmd) (execx.Result, error) { return execx.Result{}, execx.ErrTimeout }
		}},
		{name: "udisksctl is not installed", runs: 0, mutate: func(_ *actRig, o *ark.DriveActionsOptions) { o.Udisksctl = "" }},
		{name: "a device-mapper volume has no disk to power off", runs: 0, mutate: func(_ *actRig, o *ark.DriveActionsOptions) {
			s := newFakeSys()
			s.links["/sys/class/block/sdb1"] = "/sys/devices/virtual/block/dm-0"
			o.Sys = s
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := newActRig(t, tc.mutate)
			a.ready()
			if err := a.eject(false); err != nil {
				t.Fatal(err)
			}
			if got := len(a.run.argv()); got != tc.runs {
				t.Errorf("udisksctl runs = %d, want %d", got, tc.runs)
			}
			if a.unmountedEvents() != 1 {
				t.Errorf("unmounted events = %d", a.unmountedEvents())
			}
		})
	}
}

func TestEjectRefusals(t *testing.T) {
	t.Run("an unknown drive", func(t *testing.T) {
		a := newActRig(t)
		a.ready()
		if err := a.acts.Eject(context.Background(), "ark-deadbeef", false); !errors.Is(err, ark.ErrNoSuchDrive) {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("a drive that is not mounted", func(t *testing.T) {
		a := newActRig(t)
		a.notMounted(true)
		w := asWrongState(t, a.eject(false))
		if w.State != ark.StateNotMounted || w.RootID != rid {
			t.Errorf("wrong state = %+v", w)
		}
	})
	t.Run("a second eject while one is in flight", func(t *testing.T) {
		a := newActRig(t)
		a.ready()
		a.mops.hold = make(chan struct{})
		a.mops.entered = make(chan struct{})
		if err := a.acts.Eject(context.Background(), rid, false); err != nil {
			t.Fatal(err)
		}
		<-a.mops.entered
		var busy *ark.BusyError
		err := a.acts.Eject(context.Background(), rid, false)
		if !errors.As(err, &busy) || busy.Operation != "eject" || busy.RootID != rid {
			t.Errorf("err = %v", err)
		}
		// Neither init, mount nor set-profile may run on a drive that is being ejected.
		if _, err := a.acts.Init(context.Background(), rid); !errors.As(err, &busy) {
			t.Errorf("init during an eject: %v", err)
		}
		close(a.mops.hold)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := a.acts.Wait(ctx); err != nil {
			t.Fatal(err)
		}
		if a.unmountedEvents() != 1 {
			t.Errorf("unmounted events = %d", a.unmountedEvents())
		}
	})
	t.Run("a drive the detector is already ejecting", func(t *testing.T) {
		a := newActRig(t)
		a.ready()
		if err := a.m.BeginEject(rid); err != nil {
			t.Fatal(err)
		}
		w := asWrongState(t, a.eject(false))
		if w.State != ark.StateEjecting {
			t.Errorf("state = %s", w.State)
		}
	})
}

// A user that fails: without force the eject stops and the drive goes back; with force it goes on.
func TestEjectUserFailure(t *testing.T) {
	boom := errors.New("the router did not answer")
	t.Run("without force", func(t *testing.T) {
		a := newActRig(t)
		a.ready()
		u := a.user("router")
		u.set(nil, boom)
		err := a.eject(false)
		if !errors.Is(err, boom) {
			t.Fatalf("err = %v", err)
		}
		if d := a.only(); d.State != ark.StateReady {
			t.Errorf("state = %s", d.State)
		}
		if u.restores() != 1 {
			t.Errorf("restores = %d", u.restores())
		}
		if syncs, _ := a.mops.counts(); syncs != 0 {
			t.Error("the mount was touched")
		}
	})
	t.Run("with force", func(t *testing.T) {
		a := newActRig(t)
		a.ready()
		u := a.user("router")
		u.set(nil, boom)
		if err := a.eject(true); err != nil {
			t.Fatalf("err = %v", err)
		}
		if a.unmountedEvents() != 1 {
			t.Error("the forced eject should have completed")
		}
	})
}

// A drive that is mounted but not set up can be ejected too: nothing uses it, so there is no begin, no
// transfer to stop and no user to ask; the unmounted event is published once, by the eject itself.
func TestEjectOfADriveThatIsNotSetUp(t *testing.T) {
	a := newActRig(t)
	a.uninitialized()
	u := a.user("router")
	if err := a.eject(false); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"walk",
		"syncfs " + a.mnt, "umount " + a.mnt,
		"run " + udisksPath + " power-off --no-user-interaction -b /dev/sdb",
		"event unmounted " + rid + " safe=true",
	}
	if got := a.tr.list(); !reflect.DeepEqual(got, want) {
		t.Errorf("trace:\n got %q\nwant %q", got, want)
	}
	if len(u.releases()) != 0 {
		t.Error("a drive that is not in the active set has no user to release")
	}
	if a.unmountedEvents() != 1 {
		t.Errorf("unmounted events = %d", a.unmountedEvents())
	}
}

// With no way to unmount (the platform cannot) the failure is reported like any other.
func TestEjectWithoutAMountOpsFailsCleanly(t *testing.T) {
	a := newActRig(t)
	a.ready()
	a.mops.unmounts = []error{ark.ErrMountOpsUnsupported}
	if err := a.eject(false); err != nil {
		t.Fatal(err)
	}
	if raised, _ := a.aalert.snapshot(); len(raised) != 1 {
		t.Errorf("alerts = %v", raised)
	}
	if d := a.only(); d.State != ark.StateReady {
		t.Errorf("state = %s", d.State)
	}
}
