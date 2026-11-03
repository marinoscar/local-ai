package ark_test

import (
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"syscall"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/ark"
	"github.com/marinoscar/local-ai/agent/internal/config/fake"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
)

const driveUUID = "3F9C0A12-1234-5678-9ABC-DEF012345678"

func driveOpts() ark.DriveOptions {
	return ark.DriveOptions{
		NodeID: "ai-n3", AgentVersion: "0.4.0", Now: fixedNow,
		Logger: slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})),
	}
}

func driveAt(mount string) ark.DriveInfo {
	return ark.DriveInfo{Mount: mount, FSUUID: driveUUID, MountID: 42, Label: "AI-ARK"}
}

func TestOpenDriveRootUninitialized(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, mount string)
	}{
		{"no localai directory", func(*testing.T, string) {}},
		{"an empty localai directory", func(t *testing.T, mount string) {
			if err := os.Mkdir(filepath.Join(mount, "localai"), 0o755); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mount := t.TempDir()
			tc.setup(t, mount)
			before := names(t, mount)
			got, problems, err := ark.OpenDriveRoot(newFS(), driveAt(mount), driveOpts())
			if err != nil {
				t.Fatal(err)
			}
			if got.State != ark.StateUninitialized || got.ID != "ark-3f9c0a12" || got.Kind != ark.KindArkDrive {
				t.Fatalf("root = %+v", got)
			}
			if len(problems) != 1 || problems[0].Code != doctor.ARK010 || problems[0].Severity != doctor.Info {
				t.Fatalf("problems = %+v, want exactly LAI-ARK-010 as info", problems)
			}
			if !reflect.DeepEqual(names(t, mount), before) {
				t.Fatalf("an uninitialized drive was written: %v -> %v", before, names(t, mount))
			}
			if exists(filepath.Join(mount, "localai", ark.RootJSONName)) {
				t.Fatal("ROOT.json was created on an uninitialized drive")
			}
		})
	}
}

func TestOpenDriveRootRecreatesAMissingROOTJSON(t *testing.T) {
	mount := t.TempDir()
	dir := filepath.Join(mount, "localai")
	writeFile(t, filepath.Join(dir, "models", "m1", "m1.gguf"), "weights")

	got, problems, err := ark.OpenDriveRoot(newFS(), driveAt(mount), driveOpts())
	if err != nil {
		t.Fatal(err)
	}
	want := ark.Root{ID: "ark-3f9c0a12", Kind: ark.KindArkDrive, Path: dir, Mount: mount,
		Profile: "ark-drive-full", MountID: 42, State: ark.StateReady}
	if got != want {
		t.Fatalf("root = %+v, want %+v", got, want)
	}
	if len(problems) != 1 || problems[0].Code != doctor.ARK010 || problems[0].Severity != doctor.Info {
		t.Fatalf("problems = %+v, want LAI-ARK-010 as info", problems)
	}
	rj, err := ark.ReadRootJSON(filepath.Join(dir, ark.RootJSONName))
	if err != nil {
		t.Fatal(err)
	}
	if rj.RootID != "ark-3f9c0a12" || rj.Kind != ark.KindArkDrive || rj.Profile != "ark-drive-full" ||
		rj.Label != "AI-ARK" || !rj.CreatedAt.Equal(epoch) ||
		rj.CreatedBy.NodeID != "ai-n3" || rj.CreatedBy.AgentVersion != "0.4.0" {
		t.Fatalf("recreated ROOT.json = %+v", rj)
	}
	// Only ROOT.json was added: no layout is created on a drive here.
	if got := names(t, dir); !reflect.DeepEqual(got, []string{"ROOT.json", "models"}) {
		t.Fatalf("drive content = %v", got)
	}
	if readFile(t, filepath.Join(dir, "models", "m1", "m1.gguf")) != "weights" {
		t.Fatal("content changed")
	}
	// The second open is clean.
	again, problems, err := ark.OpenDriveRoot(newFS(), driveAt(mount), driveOpts())
	if err != nil || len(problems) != 0 || again != want {
		t.Fatalf("second open: %+v, %v, %v", again, codes(problems), err)
	}
}

func TestOpenDriveRootRecreationHonoursTheDefaultProfile(t *testing.T) {
	mount := t.TempDir()
	writeFile(t, filepath.Join(mount, "localai", "kit", "x"), "x")
	opts := driveOpts()
	opts.DefaultProfile = "ark-drive-lite"
	got, _, err := ark.OpenDriveRoot(newFS(), driveAt(mount), opts)
	if err != nil || got.Profile != "ark-drive-lite" {
		t.Fatalf("got %+v, %v", got, err)
	}
	rj, err := ark.ReadRootJSON(filepath.Join(mount, "localai", ark.RootJSONName))
	if err != nil || rj.Profile != "ark-drive-lite" {
		t.Fatalf("file %+v, %v", rj, err)
	}
}

func TestOpenDriveRootReadOnlyDriveIsNeverWritten(t *testing.T) {
	mount := t.TempDir()
	dir := filepath.Join(mount, "localai")
	writeFile(t, filepath.Join(dir, "docs", "index.html"), "<html>")
	d := driveAt(mount)
	d.ReadOnly = true
	got, problems, err := ark.OpenDriveRoot(newFS(), d, driveOpts())
	if err != nil {
		t.Fatal(err)
	}
	if !got.ReadOnly || got.State != ark.StateReadOnly || got.Profile != "ark-drive-full" {
		t.Fatalf("root = %+v", got)
	}
	if !reflect.DeepEqual(codes(problems), []string{doctor.ARK010}) {
		t.Fatalf("problems = %v", codes(problems))
	}
	if exists(filepath.Join(dir, ark.RootJSONName)) {
		t.Fatal("ROOT.json was written to a read-only drive")
	}
}

func TestOpenDriveRootWriteFailureServesTheDriveReadOnly(t *testing.T) {
	tests := []struct {
		name string
		op   string
	}{
		{"disk full on write", "write"},
		{"read-only remount on open", "open"},
		{"I/O error on sync", "sync"},
		{"I/O error on rename", "rename"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mount := t.TempDir()
			dir := filepath.Join(mount, "localai")
			writeFile(t, filepath.Join(dir, "kit", "x"), "x")
			fsys := newFS()
			fsys.FailOn(tc.op, 1)
			got, problems, err := ark.OpenDriveRoot(fsys, driveAt(mount), driveOpts())
			if !errors.Is(err, fake.ErrInjected) {
				t.Fatalf("err = %v", err)
			}
			if !got.ReadOnly || got.State != ark.StateReadOnly || got.ID != "ark-3f9c0a12" {
				t.Fatalf("root = %+v", got)
			}
			if !reflect.DeepEqual(codes(problems), []string{doctor.ARK010}) {
				t.Fatalf("problems = %v", codes(problems))
			}
			if exists(filepath.Join(dir, ark.RootJSONName)) {
				t.Fatal("a ROOT.json appeared although the write failed")
			}
			noTemps(t, dir)
		})
	}
}

func TestOpenDriveRootReadsAnExistingROOTJSON(t *testing.T) {
	tests := []struct {
		name        string
		fixture     string
		wantProfile string
	}{
		{"the spec example", "valid-drive.json", "ark-drive-full"},
		{"unknown keys do not matter", "extra-keys.json", "ark-drive-full"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			content := string(fixture(t, tc.fixture))
			mount := t.TempDir()
			p := filepath.Join(mount, "localai", ark.RootJSONName)
			writeFile(t, p, content)
			got, problems, err := ark.OpenDriveRoot(newFS(), driveAt(mount), driveOpts())
			if err != nil || len(problems) != 0 || got.State != ark.StateReady || got.Profile != tc.wantProfile {
				t.Fatalf("got %+v, %v, %v", got, codes(problems), err)
			}
			if readFile(t, p) != content {
				t.Fatal("an existing ROOT.json was rewritten")
			}
		})
	}
}

func TestOpenDriveRootProfileComesFromTheFile(t *testing.T) {
	mount := t.TempDir()
	p := filepath.Join(mount, "localai", ark.RootJSONName)
	rj := mustParse(t, "valid-drive.json")
	rj.Profile = "ark-drive-lite"
	writeFile(t, p, "")
	if err := ark.WriteRootJSON(p, rj); err != nil {
		t.Fatal(err)
	}
	got, _, err := ark.OpenDriveRoot(newFS(), driveAt(mount), driveOpts())
	if err != nil || got.Profile != "ark-drive-lite" {
		t.Fatalf("got %+v, %v", got, err)
	}
	// An empty profile in the file falls back to the default.
	rj.Profile = ""
	if err := ark.WriteRootJSON(p, rj); err != nil {
		t.Fatal(err)
	}
	opts := driveOpts()
	opts.DefaultProfile = "ark-drive-mid"
	if got, _, err = ark.OpenDriveRoot(newFS(), driveAt(mount), opts); err != nil || got.Profile != "ark-drive-mid" {
		t.Fatalf("empty profile: %+v, %v", got, err)
	}
}

func TestOpenDriveRootTheUUIDWinsOverAStaleRootID(t *testing.T) {
	mount := t.TempDir()
	d := driveAt(mount)
	d.FSUUID = "aaaaaaaa-1234-5678-9abc-def012345678" // tune2fs -U changed it
	p := filepath.Join(mount, "localai", ark.RootJSONName)
	writeFile(t, p, string(fixture(t, "valid-drive.json"))) // says ark-3f9c0a12
	got, problems, err := ark.OpenDriveRoot(newFS(), d, driveOpts())
	if err != nil || len(problems) != 0 || got.ID != "ark-aaaaaaaa" {
		t.Fatalf("got %+v, %v, %v", got, codes(problems), err)
	}
	if readFile(t, p) != string(fixture(t, "valid-drive.json")) {
		t.Fatal("the file was rewritten")
	}
}

func TestOpenDriveRootBadROOTJSONIsAnErrorAndTheFileIsLeftAlone(t *testing.T) {
	tests := []struct {
		fixture string
		want    error
	}{
		{"truncated.json", ark.ErrRootJSONCorrupt},
		{"garbage.json", ark.ErrRootJSONCorrupt},
		{"empty-object.json", ark.ErrRootJSONCorrupt},
		{"bad-kind.json", ark.ErrRootJSONCorrupt},
		{"schema-v2.json", ark.ErrRootJSONSchema},
	}
	for _, tc := range tests {
		t.Run(tc.fixture, func(t *testing.T) {
			mount := t.TempDir()
			p := filepath.Join(mount, "localai", ark.RootJSONName)
			writeFile(t, p, string(fixture(t, tc.fixture)))
			got, problems, err := ark.OpenDriveRoot(newFS(), driveAt(mount), driveOpts())
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if got.State != ark.StateUnusable || !got.ReadOnly || got.ID != "ark-3f9c0a12" || len(problems) != 0 {
				t.Fatalf("got %+v, %v", got, codes(problems))
			}
			if readFile(t, p) != string(fixture(t, tc.fixture)) {
				t.Fatal("the file was touched")
			}
			if got := names(t, filepath.Dir(p)); !reflect.DeepEqual(got, []string{ark.RootJSONName}) {
				t.Fatalf("directory = %v", got)
			}
		})
	}
}

func TestOpenDriveRootUnreadableROOTJSON(t *testing.T) {
	mount := t.TempDir()
	p := filepath.Join(mount, "localai", ark.RootJSONName)
	writeFile(t, p, string(fixture(t, "valid-drive.json")))
	fsys := newFS()
	fsys.FailOn("readfile", 1)
	got, _, err := ark.OpenDriveRoot(fsys, driveAt(mount), driveOpts())
	if !errors.Is(err, fake.ErrInjected) || errors.Is(err, ark.ErrRootJSONCorrupt) {
		t.Fatalf("err = %v", err)
	}
	if got.State != ark.StateUnusable || !got.ReadOnly {
		t.Fatalf("root = %+v", got)
	}
	if readFile(t, p) != string(fixture(t, "valid-drive.json")) {
		t.Fatal("the file was touched")
	}
}

func TestOpenDriveRootOtherFailures(t *testing.T) {
	t.Run("localai is a file", func(t *testing.T) {
		mount := t.TempDir()
		writeFile(t, filepath.Join(mount, "localai"), "i am a file")
		got, _, err := ark.OpenDriveRoot(newFS(), driveAt(mount), driveOpts())
		if !errors.Is(err, ark.ErrLayoutConflict) || got.State != ark.StateUnusable {
			t.Fatalf("got %+v, %v", got, err)
		}
		if readFile(t, filepath.Join(mount, "localai")) != "i am a file" {
			t.Fatal("the file was touched")
		}
	})
	t.Run("stat fails", func(t *testing.T) {
		fsys := newFS()
		fsys.FailOn("stat", 1)
		got, _, err := ark.OpenDriveRoot(fsys, driveAt(t.TempDir()), driveOpts())
		if !errors.Is(err, fake.ErrInjected) || got.State != ark.StateUnusable {
			t.Fatalf("got %+v, %v", got, err)
		}
	})
	t.Run("directory cannot be listed", func(t *testing.T) {
		mount := t.TempDir()
		writeFile(t, filepath.Join(mount, "localai", "x"), "x")
		fsys := &readDirFailFS{tfs: newFS()}
		got, _, err := ark.OpenDriveRoot(fsys, driveAt(mount), driveOpts())
		if !errors.Is(err, syscall.EIO) || got.State != ark.StateUnusable {
			t.Fatalf("got %+v, %v", got, err)
		}
	})
	t.Run("unusable UUID", func(t *testing.T) {
		d := driveAt(t.TempDir())
		d.FSUUID = ""
		got, problems, err := ark.OpenDriveRoot(newFS(), d, driveOpts())
		if !errors.Is(err, ark.ErrBadUUID) || got != (ark.Root{}) || len(problems) != 0 {
			t.Fatalf("got %+v, %v, %v", got, codes(problems), err)
		}
	})
	t.Run("no mount point", func(t *testing.T) {
		d := driveAt("")
		if _, _, err := ark.OpenDriveRoot(newFS(), d, driveOpts()); err == nil {
			t.Fatal("want an error")
		}
	})
}

type readDirFailFS struct{ *tfs }

func (readDirFailFS) ReadDir(string) ([]os.DirEntry, error) { return nil, syscall.EIO }
