package ark_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/ark"
	"github.com/marinoscar/local-ai/agent/internal/execx"
)

// Mount runs udisksctl for a plugged-in drive that is not mounted, and returns the drive once the mount
// table shows it.
func TestMountMountsANotMountedDrive(t *testing.T) {
	a := newActRig(t)
	a.notMounted(true)
	if d := a.only(); d.State != ark.StateNotMounted || d.Mount != "" {
		t.Fatalf("setup: %+v", d)
	}

	d, err := a.acts.Mount(context.Background(), rid)
	if err != nil {
		t.Fatalf("Mount: %v", err)
	}
	if d.State != ark.StateReady || d.Mount != a.mnt || !d.Initialized {
		t.Errorf("drive = %+v", d)
	}
	want := [][]string{{udisksPath, "mount", "--no-user-interaction", "-b", "/dev/sdb1"}}
	if got := a.run.argv(); !reflect.DeepEqual(got, want) {
		t.Errorf("programs run = %q, want %q", got, want)
	}
	if len(a.m.ActiveRoots()) != 1 {
		t.Error("the mounted drive did not join the active roots")
	}
}

// A new drive that is mounted is reported as uninitialized, ready for Set up.
func TestMountOfANewDriveIsUninitialized(t *testing.T) {
	a := newActRig(t)
	a.notMounted(false)
	d, err := a.acts.Mount(context.Background(), rid)
	if err != nil || d.State != ark.StateUninitialized {
		t.Fatalf("Mount = %+v, %v", d, err)
	}
}

// The agent never mounts a drive by itself: detecting it, scanning again and refreshing run no program.
func TestTheDetectorNeverMounts(t *testing.T) {
	a := newActRig(t)
	a.notMounted(true)
	for i := 0; i < 3; i++ {
		a.refresh()
	}
	if got := a.run.argv(); len(got) != 0 {
		t.Errorf("programs run without a request: %q", got)
	}
	if a.only().State != ark.StateNotMounted {
		t.Error("the drive should still be not mounted")
	}
}

func TestMountFailures(t *testing.T) {
	t.Run("udisksctl refuses", func(t *testing.T) {
		a := newActRig(t)
		a.notMounted(true)
		a.run.handle = func(execx.Cmd) (execx.Result, error) {
			return execx.Result{ExitCode: 1, Stderr: []byte("\nError mounting /dev/sdb1: wrong fs type\nsecond line\n")}, nil
		}
		_, err := a.acts.Mount(context.Background(), rid)
		var me *ark.MountError
		if !errors.As(err, &me) || me.Device != "/dev/sdb1" || me.Reason != "Error mounting /dev/sdb1: wrong fs type" {
			t.Fatalf("err = %#v", err)
		}
		if a.only().State != ark.StateNotMounted {
			t.Error("the drive should still be not mounted")
		}
	})
	t.Run("udisksctl cannot be run", func(t *testing.T) {
		a := newActRig(t)
		a.notMounted(true)
		a.run.handle = func(execx.Cmd) (execx.Result, error) { return execx.Result{}, execx.ErrTimeout }
		_, err := a.acts.Mount(context.Background(), rid)
		var me *ark.MountError
		if !errors.As(err, &me) || !errors.Is(err, execx.ErrTimeout) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("udisksctl is not installed", func(t *testing.T) {
		a := newActRig(t, func(_ *actRig, o *ark.DriveActionsOptions) { o.Udisksctl = "" })
		a.notMounted(true)
		_, err := a.acts.Mount(context.Background(), rid)
		if !errors.Is(err, ark.ErrUdisksUnavailable) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("the mount never shows up in the table", func(t *testing.T) {
		a := newActRig(t)
		a.notMounted(true)
		a.run.handle = func(execx.Cmd) (execx.Result, error) { return execx.Result{}, nil } // exit 0, table unchanged
		_, err := a.acts.Mount(context.Background(), rid)
		var me *ark.MountError
		if !errors.As(err, &me) {
			t.Fatalf("err = %v", err)
		}
		if got := a.sleeps.total(); got != ark.DefaultMountWait {
			t.Errorf("waited %v, want %v", got, ark.DefaultMountWait)
		}
	})
	t.Run("already mounted: the table the detector read was stale", func(t *testing.T) {
		a := newActRig(t)
		a.notMounted(true)
		inner := a.run.handle
		a.run.handle = func(c execx.Cmd) (execx.Result, error) {
			_, _ = inner(c) // the table now has the drive
			return execx.Result{ExitCode: 1, Stderr: []byte("Error mounting /dev/sdb1: GDBus.Error:org.freedesktop.UDisks2.Error.AlreadyMounted: Device is already mounted\n")}, nil
		}
		d, err := a.acts.Mount(context.Background(), rid)
		if err != nil || d.State != ark.StateReady {
			t.Fatalf("Mount = %+v, %v", d, err)
		}
	})
}

// Mount applies to not_mounted only; any other state is refused with the state and nothing runs.
func TestMountRefusesEveryOtherState(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*actRig)
		state ark.DriveState
	}{
		{"ready", func(a *actRig) { a.ready() }, ark.StateReady},
		{"uninitialized", func(a *actRig) { a.uninitialized() }, ark.StateUninitialized},
		{"ejecting", func(a *actRig) {
			a.ready()
			if err := a.m.BeginEject(rid); err != nil {
				t.Fatal(err)
			}
		}, ark.StateEjecting},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := newActRig(t)
			tc.setup(a)
			_, err := a.acts.Mount(context.Background(), rid)
			if w := asWrongState(t, err); w.State != tc.state || w.RootID != rid {
				t.Errorf("wrong state = %+v", w)
			}
			if got := a.run.argv(); len(got) != 0 {
				t.Errorf("programs run: %q", got)
			}
		})
	}
	t.Run("an unknown drive", func(t *testing.T) {
		a := newActRig(t)
		a.notMounted(true)
		if _, err := a.acts.Mount(context.Background(), "ark-deadbeef"); !errors.Is(err, ark.ErrNoSuchDrive) {
			t.Errorf("err = %v", err)
		}
	})
}
