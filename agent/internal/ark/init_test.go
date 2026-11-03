package ark_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"syscall"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/ark"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
)

// Init on an uninitialized drive creates the layout and a valid ROOT.json, and the drive becomes ready.
func TestInitSetsUpAnEmptyDrive(t *testing.T) {
	fs := newFS()
	a := newActRig(t, func(_ *actRig, o *ark.DriveActionsOptions) {
		o.FS = fs
		o.Owner = &ark.Owner{UID: 1234, GID: 5678}
	})
	a.uninitialized()

	d, err := a.acts.Init(context.Background(), rid)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	if d.State != ark.StateReady || !d.Initialized || d.Profile != "ark-drive-test" || d.RootID != rid || d.Mount != a.mnt {
		t.Errorf("drive = %+v", d)
	}

	rj, err := a.rootJSON()
	if err != nil {
		t.Fatalf("ROOT.json: %v", err)
	}
	if rj.SchemaVersion != 1 || rj.RootID != rid || rj.Kind != ark.KindArkDrive || rj.Profile != "ark-drive-test" ||
		rj.CreatedBy.NodeID != "node-a" || rj.CreatedBy.AgentVersion != "0.0.0-test" || rj.Label != "AI-ARK" || !rj.CreatedAt.Equal(epoch) {
		t.Errorf("ROOT.json = %+v", rj)
	}
	for _, dir := range ark.LayoutDirs {
		fi, err := os.Stat(filepath.Join(a.mnt, "localai", filepath.FromSlash(dir)))
		if err != nil || !fi.IsDir() {
			t.Errorf("layout directory %s: %v", dir, err)
			continue
		}
		want := os.FileMode(0o755)
		if ark.PrivateDirs[dir] {
			want = 0o700
		}
		if fi.Mode().Perm() != want {
			t.Errorf("%s mode = %v, want %v", dir, fi.Mode().Perm(), want)
		}
	}
	// Init creates the localai tree and nothing else (the extras are another writer's job).
	if got := names(t, a.mnt); !reflect.DeepEqual(got, []string{"localai"}) {
		t.Errorf("the drive top level = %v", got)
	}
	if len(fs.chowns) != 1 || fs.chowns[0] != (chownCall{Path: filepath.Join(a.mnt, "localai"), UID: 1234, GID: 5678}) {
		t.Errorf("chowns = %+v; only the top directory goes to the localai user", fs.chowns)
	}

	// The detector knows it: active, announced once, ready.
	if roots := a.m.ActiveRoots(); len(roots) != 1 || roots[0].ID != rid || roots[0].State != ark.StateReady {
		t.Errorf("active roots = %+v", roots)
	}
	if n, _ := a.bus.counts(); n != 1 {
		t.Errorf("drive.mounted events = %d", n)
	}
	// ...and START-HERE and the inventory copy were asked for, for the new root.
	if got := a.extras.written(); len(got) != 1 || got[0].ID != rid || got[0].State != ark.StateReady || got[0].Path != filepath.Join(a.mnt, "localai") {
		t.Errorf("extras written for %+v", got)
	}
}

// An empty localai/ counts as none (the detector reads it as uninitialized), so it is set up in place; a
// failure of the extras does not fail the init.
func TestInitAcceptsAnEmptyLocalaiDirectory(t *testing.T) {
	a := newActRig(t)
	if err := os.Mkdir(filepath.Join(a.mnt, "localai"), 0o755); err != nil {
		t.Fatal(err)
	}
	a.extras.err = errors.New("inventory unreadable")
	a.uninitialized()
	if a.only().State != ark.StateUninitialized {
		t.Fatalf("setup: %+v", a.only())
	}
	d, err := a.acts.Init(context.Background(), rid)
	if err != nil || d.State != ark.StateReady {
		t.Fatalf("Init = %+v, %v", d, err)
	}
}

// Only an uninitialized drive can be set up; any other state is refused and nothing is written.
func TestInitRefusesEveryOtherState(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*actRig)
		state ark.DriveState
	}{
		{"ready", func(a *actRig) { a.ready() }, ark.StateReady},
		{"read only", func(a *actRig) {
			a.initDrive(a.mnt, uuidA, "")
			a.mounts.set(a.rootDisk(), a.arkDisk(120, "8:17", a.mnt, "/dev/sdb1", uuidA, "ext4", "ro,relatime"))
			a.refresh()
		}, ark.StateReadOnly},
		{"unusable (a damaged ROOT.json)", func(a *actRig) {
			a.initDrive(a.mnt, uuidA, "")
			writeFile(t, filepath.Join(a.mnt, "localai", ark.RootJSONName), "{not json")
			a.mounts.set(a.rootDisk(), a.arkDisk(120, "8:17", a.mnt, "/dev/sdb1", uuidA, "ext4"))
			a.refresh()
		}, ark.StateUnusable},
		{"not mounted", func(a *actRig) { a.notMounted(false) }, ark.StateNotMounted},
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
			before := names(t, a.mnt)
			_, err := a.acts.Init(context.Background(), rid)
			w := asWrongState(t, err)
			if w.State != tc.state || w.RootID != rid || w.Reason != "" {
				t.Errorf("wrong state = %+v, want state %s", w, tc.state)
			}
			if got := names(t, a.mnt); !reflect.DeepEqual(got, before) {
				t.Errorf("a refused init changed the drive: %v -> %v", before, got)
			}
			if len(a.extras.written()) != 0 {
				t.Error("a refused init wrote the extras")
			}
		})
	}
	t.Run("an unknown drive", func(t *testing.T) {
		a := newActRig(t)
		a.uninitialized()
		if _, err := a.acts.Init(context.Background(), "ark-deadbeef"); !errors.Is(err, ark.ErrNoSuchDrive) {
			t.Errorf("err = %v", err)
		}
	})
}

// localai/ that got content after the detector looked is never touched.
func TestInitRefusesWhenLocalaiAlreadyHoldsSomething(t *testing.T) {
	for name, prep := range map[string]func(string){
		"a file inside": func(dir string) { writeFile(t, filepath.Join(dir, "models", "keep.gguf"), "x") },
		"a file in its place": func(dir string) {
			if err := os.Remove(dir); err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			writeFile(t, dir, "x")
		},
	} {
		t.Run(name, func(t *testing.T) {
			a := newActRig(t)
			a.uninitialized()
			prep(filepath.Join(a.mnt, "localai"))
			_, err := a.acts.Init(context.Background(), rid)
			w := asWrongState(t, err)
			if w.Reason != "localai_exists" || w.State != ark.StateUninitialized {
				t.Errorf("wrong state = %+v", w)
			}
			if exists(filepath.Join(a.mnt, "localai", ark.RootJSONName)) {
				t.Error("ROOT.json was written into a localai/ that had content")
			}
		})
	}
}

// A failed write is a *SetupError with the cause; ROOT.json is never half-written, and a drive whose
// layout was made but whose ROOT.json was not is recreated by the next detector pass.
func TestInitFailures(t *testing.T) {
	t.Run("the layout cannot be created", func(t *testing.T) {
		fs := newFS()
		a := newActRig(t, func(a *actRig, o *ark.DriveActionsOptions) {
			fs.mkdirErr[filepath.Join(a.mnt, "localai")] = errnoErr("mkdir", "localai", syscall.EROFS)
			o.FS = fs
		})
		a.uninitialized()
		_, err := a.acts.Init(context.Background(), rid)
		var se *ark.SetupError
		if !errors.As(err, &se) || !errors.Is(err, syscall.EROFS) || se.RootID != rid {
			t.Fatalf("err = %v", err)
		}
		if got := names(t, a.mnt); len(got) != 0 {
			t.Errorf("the drive was changed: %v", got)
		}
		if a.only().State != ark.StateUninitialized {
			t.Errorf("state = %s", a.only().State)
		}
	})
	t.Run("ROOT.json cannot be written", func(t *testing.T) {
		fs := newFS()
		a := newActRig(t, func(_ *actRig, o *ark.DriveActionsOptions) { o.FS = fs })
		a.uninitialized()
		fs.FailOn("rename", -1)
		_, err := a.acts.Init(context.Background(), rid)
		var se *ark.SetupError
		if !errors.As(err, &se) {
			t.Fatalf("err = %v", err)
		}
		if exists(filepath.Join(a.mnt, "localai", ark.RootJSONName)) {
			t.Error("a half-written ROOT.json was left behind")
		}
		// The next pass sees a localai/ with content and no ROOT.json and recreates it with the defaults.
		a.refresh()
		d := a.only()
		if d.State != ark.StateReady || !d.HasProblem(doctor.ARK010) {
			t.Errorf("drive after the next pass = %+v", d)
		}
		if _, err := a.rootJSON(); err != nil {
			t.Errorf("ROOT.json after the next pass: %v", err)
		}
	})
}
