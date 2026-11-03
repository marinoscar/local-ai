package ark_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/ark"
)

// SetProfile validates the id against the catalog and writes ROOT.json, keeping the keys it does not know;
// the roots the planner uses carry the new profile at once.
func TestSetProfileWritesRootJSON(t *testing.T) {
	a := newActRig(t)
	a.initDrive(a.mnt, uuidA, "")
	p := filepath.Join(a.mnt, "localai", ark.RootJSONName)
	// A newer agent's key must survive.
	body := readFile(t, p)
	writeFile(t, p, strings.Replace(body, `"notes": ""`, `"notes": "", "x_future": {"a": 1}`, 1))
	a.mounts.set(a.rootDisk(), a.arkDisk(120, "8:17", a.mnt, "/dev/sdb1", uuidA, "ext4"))
	a.refresh()
	if got := a.m.ActiveRoots()[0].Profile; got != ark.DefaultDriveProfile {
		t.Fatalf("profile before = %q", got)
	}

	got, err := a.acts.SetProfile(context.Background(), rid, "ark-drive-lite")
	if err != nil || got != "ark-drive-lite" {
		t.Fatalf("SetProfile = %q, %v", got, err)
	}
	rj, err := a.rootJSON()
	if err != nil || rj.Profile != "ark-drive-lite" || rj.RootID != rid {
		t.Fatalf("ROOT.json = %+v, %v", rj, err)
	}
	if !strings.Contains(readFile(t, p), `"x_future"`) {
		t.Error("a key the agent does not know was dropped")
	}
	if got := a.m.ActiveRoots()[0].Profile; got != "ark-drive-lite" {
		t.Errorf("the active root still plans with %q", got)
	}
	if d := a.only(); d.Profile != "ark-drive-lite" {
		t.Errorf("drive profile = %q", d.Profile)
	}
}

func TestSetProfileRefusals(t *testing.T) {
	t.Run("an unknown profile writes nothing", func(t *testing.T) {
		a := newActRig(t)
		a.ready()
		before := readFile(t, filepath.Join(a.mnt, "localai", ark.RootJSONName))
		_, err := a.acts.SetProfile(context.Background(), rid, "no-such-profile")
		var up *ark.UnknownProfileError
		if !errors.As(err, &up) || up.Profile != "no-such-profile" {
			t.Fatalf("err = %v", err)
		}
		if after := readFile(t, filepath.Join(a.mnt, "localai", ark.RootJSONName)); after != before {
			t.Error("ROOT.json changed")
		}
	})
	t.Run("no catalog", func(t *testing.T) {
		for name, lookup := range map[string]ark.ProfileLookup{
			"not loaded": func(string) (bool, bool) { return false, false },
			"not given":  nil,
		} {
			a := newActRig(t, func(_ *actRig, o *ark.DriveActionsOptions) { o.Profiles = lookup })
			a.ready()
			if _, err := a.acts.SetProfile(context.Background(), rid, "ark-drive-full"); !errors.Is(err, ark.ErrNoCatalog) {
				t.Errorf("%s: err = %v", name, err)
			}
		}
	})
	t.Run("a drive that is read-only", func(t *testing.T) {
		a := newActRig(t)
		a.initDrive(a.mnt, uuidA, "")
		a.mounts.set(a.rootDisk(), a.arkDisk(120, "8:17", a.mnt, "/dev/sdb1", uuidA, "ext4", "ro,relatime"))
		a.refresh()
		if _, err := a.acts.SetProfile(context.Background(), rid, "ark-drive-lite"); !errors.Is(err, ark.ErrDriveReadOnly) {
			t.Errorf("err = %v", err)
		}
	})
	for _, tc := range []struct {
		name  string
		setup func(*actRig)
		state ark.DriveState
	}{
		{"uninitialized", func(a *actRig) { a.uninitialized() }, ark.StateUninitialized},
		{"not mounted", func(a *actRig) { a.notMounted(true) }, ark.StateNotMounted},
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
			_, err := a.acts.SetProfile(context.Background(), rid, "ark-drive-lite")
			if w := asWrongState(t, err); w.State != tc.state {
				t.Errorf("wrong state = %+v", w)
			}
		})
	}
	t.Run("an unknown drive", func(t *testing.T) {
		a := newActRig(t)
		a.ready()
		if _, err := a.acts.SetProfile(context.Background(), "ark-deadbeef", "ark-drive-lite"); !errors.Is(err, ark.ErrNoSuchDrive) {
			t.Errorf("err = %v", err)
		}
	})
}

// The same profile again is a success that writes nothing; a different one needs the disk to work.
func TestSetProfileSameProfileWritesNothing(t *testing.T) {
	fs := newFS()
	a := newActRig(t, func(_ *actRig, o *ark.DriveActionsOptions) { o.FS = fs })
	a.initDrive(a.mnt, uuidA, "ark-drive-lite")
	a.mounts.set(a.rootDisk(), a.arkDisk(120, "8:17", a.mnt, "/dev/sdb1", uuidA, "ext4"))
	a.refresh()
	fs.FailOn("rename", -1) // any write now fails

	if got, err := a.acts.SetProfile(context.Background(), rid, "ark-drive-lite"); err != nil || got != "ark-drive-lite" {
		t.Fatalf("the same profile: %q, %v", got, err)
	}
	_, err := a.acts.SetProfile(context.Background(), rid, "ark-drive-full")
	var se *ark.SetupError
	if !errors.As(err, &se) {
		t.Fatalf("a different profile on a failing disk: %v", err)
	}
	if rj, err := a.rootJSON(); err != nil || rj.Profile != "ark-drive-lite" {
		t.Errorf("ROOT.json = %+v, %v", rj, err)
	}
}
