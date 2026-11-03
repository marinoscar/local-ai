package ark_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/ark"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/events"
)

// ---- one drive, end to end ------------------------------------------------------------------------

func TestReadyDrive(t *testing.T) {
	r := newRig(t)
	mnt := r.dir("AI-ARK")
	r.initDrive(mnt, uuidA, "ark-drive-lite")
	r.mounts.set(r.rootDisk(), r.arkDisk(120, "8:17", mnt, "/dev/sdb1", uuidA, "ext4"))
	r.refresh()

	d := r.only()
	if d.State != ark.StateReady || d.RootID != "ark-3f9c0a12" || d.Device != "/dev/sdb1" || d.Mount != mnt || d.FSType != "ext4" || d.Label != "AI-ARK" {
		t.Fatalf("drive = %+v", d)
	}
	if !d.Initialized || d.ReadOnly || d.FSErrors != 0 || d.Profile != "ark-drive-lite" || len(d.Problems) != 0 {
		t.Errorf("drive = %+v", d)
	}
	if d.SizeBytes != 2000 || d.FreeBytes != 800 || d.MountID != 120 || d.DevNum != "8:17" {
		t.Errorf("sizes/ids = %+v", d)
	}

	roots := r.m.ActiveRoots()
	if len(roots) != 1 || roots[0].ID != "ark-3f9c0a12" || roots[0].Kind != ark.KindArkDrive || roots[0].Path != filepath.Join(mnt, "localai") ||
		roots[0].State != ark.StateReady || roots[0].ReadOnly || roots[0].NeedsServeVerify() || roots[0].MountID != 120 || roots[0].Profile != "ark-drive-lite" {
		t.Errorf("roots = %+v", roots)
	}

	if got := r.bus.mounted; len(got) != 1 || got[0] != (events.DriveMounted{RootID: "ark-3f9c0a12", Mount: mnt, State: "ready", ReadOnly: false}) {
		t.Errorf("drive.mounted = %+v", got)
	}
	// The write probe left nothing behind.
	if exists(filepath.Join(mnt, "localai", ".staging", ".probe")) {
		t.Error("the write probe file was left on the drive")
	}
	// The system disk was asked once and cached; so was the drive.
	r.refresh()
	if r.block.probeCount("/dev/nvme0n1p2") != 1 || r.block.probeCount("/dev/sdb1") != 1 {
		t.Errorf("blkid probes = %v; each mounted device is probed once until its mount disappears", r.block.probes)
	}
	if n, _ := r.bus.counts(); n != 1 {
		t.Errorf("a second pass with nothing changed published again: %d mounted events", n)
	}
	if r.fs.probeCount() != 1 {
		t.Errorf("the write probe ran %d times; once per mount is enough", r.fs.probeCount())
	}
}

// TestClassificationEndToEnd is the table of ark-sync.md section 9.1 "drive classification", run
// through the whole detector: blkid, mountinfo, sysfs and the drive's files.
func TestClassificationEndToEnd(t *testing.T) {
	type want struct {
		state   ark.DriveState
		codes   []string
		rootID  string
		roleOn  bool // the drive is in the active set
		initial bool
	}
	cases := []struct {
		name  string
		setup func(r *rig, mnt string) ark.MountEntry
		want  want
		check func(t *testing.T, r *rig, mnt string, d ark.Drive)
	}{
		{name: "ext4 and AI-ARK is ready",
			setup: func(r *rig, mnt string) ark.MountEntry {
				r.initDrive(mnt, uuidA, "")
				return r.arkDisk(120, "8:17", mnt, "/dev/sdb1", uuidA, "ext4")
			},
			want: want{state: ark.StateReady, rootID: "ark-3f9c0a12", roleOn: true, initial: true}},
		{name: "exfat with the AI-ARK label is unusable (LAI-ARK-002) and never touched",
			setup: func(r *rig, mnt string) ark.MountEntry {
				return r.arkDisk(121, "8:33", mnt, "/dev/sdc1", "1A2B-3C4D", "exfat")
			},
			want: want{state: ark.StateUnusable, codes: []string{doctor.ARK002}},
			check: func(t *testing.T, r *rig, mnt string, d ark.Drive) {
				if got := r.fs.touched(mnt); len(got) != 0 {
					t.Errorf("an unusable drive was touched: %v", got)
				}
				if exists(filepath.Join(mnt, "localai")) {
					t.Error("localai was created on an exfat drive")
				}
			}},
		{name: "errors_count 3 is unclean (LAI-ARK-004): read-only, served, to be verified, never written",
			setup: func(r *rig, mnt string) ark.MountEntry {
				r.initDrive(mnt, uuidA, "")
				r.errs.set("/dev/sdb1", 3)
				return r.arkDisk(120, "8:17", mnt, "/dev/sdb1", uuidA, "ext4")
			},
			want: want{state: ark.StateUnclean, codes: []string{doctor.ARK004}, rootID: "ark-3f9c0a12", roleOn: true, initial: true},
			check: func(t *testing.T, r *rig, mnt string, d ark.Drive) {
				if d.FSErrors != 3 || !d.ReadOnly {
					t.Errorf("drive = %+v", d)
				}
				root := r.m.ActiveRoots()[0]
				if !root.ReadOnly || !root.NeedsServeVerify() || root.State != ark.StateUnclean {
					t.Errorf("root = %+v", root)
				}
				if r.fs.probeCount() != 0 {
					t.Error("the write probe ran on an unclean drive")
				}
			}},
		{name: "a read-only mount is read_only (LAI-ARK-003) and never written",
			setup: func(r *rig, mnt string) ark.MountEntry {
				r.initDrive(mnt, uuidA, "")
				return r.arkDisk(120, "8:17", mnt, "/dev/sdb1", uuidA, "ext4", "ro,relatime", "rw")
			},
			want: want{state: ark.StateReadOnly, codes: []string{doctor.ARK003}, rootID: "ark-3f9c0a12", roleOn: true, initial: true},
			check: func(t *testing.T, r *rig, mnt string, d ark.Drive) {
				if !d.ReadOnly || !r.m.ActiveRoots()[0].ReadOnly || r.m.ActiveRoots()[0].NeedsServeVerify() {
					t.Errorf("drive = %+v root = %+v", d, r.m.ActiveRoots())
				}
				if r.fs.probeCount() != 0 {
					t.Error("the write probe ran on a read-only drive")
				}
			}},
		{name: "a read-only superblock is read_only too",
			setup: func(r *rig, mnt string) ark.MountEntry {
				r.initDrive(mnt, uuidA, "")
				return r.arkDisk(120, "8:17", mnt, "/dev/sdb1", uuidA, "ext4", "rw,relatime", "ro,errors=remount-ro")
			},
			want: want{state: ark.StateReadOnly, codes: []string{doctor.ARK003}, rootID: "ark-3f9c0a12", roleOn: true, initial: true}},
		{name: "read-only with errors: read_only first, both codes, still verified at serve time",
			setup: func(r *rig, mnt string) ark.MountEntry {
				r.initDrive(mnt, uuidA, "")
				r.errs.set("/dev/sdb1", 1)
				return r.arkDisk(120, "8:17", mnt, "/dev/sdb1", uuidA, "ext4", "rw,relatime", "ro,errors=remount-ro")
			},
			want: want{state: ark.StateReadOnly, codes: []string{doctor.ARK003, doctor.ARK004}, rootID: "ark-3f9c0a12", roleOn: true, initial: true},
			check: func(t *testing.T, r *rig, mnt string, d ark.Drive) {
				if !r.m.ActiveRoots()[0].NeedsServeVerify() {
					t.Error("a read-only drive with errors must be verified before it is served")
				}
			}},
		{name: "no localai at all is uninitialized (LAI-ARK-010) and nothing is written",
			setup: func(r *rig, mnt string) ark.MountEntry {
				return r.arkDisk(120, "8:17", mnt, "/dev/sdb1", uuidA, "ext4")
			},
			want: want{state: ark.StateUninitialized, codes: []string{doctor.ARK010}, rootID: "ark-3f9c0a12"},
			check: func(t *testing.T, r *rig, mnt string, d ark.Drive) {
				if got := names(t, mnt); len(got) != 0 {
					t.Errorf("an uninitialized drive was written: %v", got)
				}
			}},
		{name: "an empty localai is uninitialized too",
			setup: func(r *rig, mnt string) ark.MountEntry {
				if err := os.Mkdir(filepath.Join(mnt, "localai"), 0o755); err != nil {
					t.Fatal(err)
				}
				return r.arkDisk(120, "8:17", mnt, "/dev/sdb1", uuidA, "ext4")
			},
			want: want{state: ark.StateUninitialized, codes: []string{doctor.ARK010}, rootID: "ark-3f9c0a12"},
			check: func(t *testing.T, r *rig, mnt string, d ark.Drive) {
				if got := names(t, filepath.Join(mnt, "localai")); len(got) != 0 {
					t.Errorf("localai was written: %v", got)
				}
			}},
		{name: "ROOT.json missing but the drive has content is recreated, ready, with LAI-ARK-010 as information",
			setup: func(r *rig, mnt string) ark.MountEntry {
				r.initDrive(mnt, uuidA, "")
				if err := os.Remove(filepath.Join(mnt, "localai", ark.RootJSONName)); err != nil {
					t.Fatal(err)
				}
				return r.arkDisk(120, "8:17", mnt, "/dev/sdb1", uuidA, "ext4")
			},
			want: want{state: ark.StateReady, codes: []string{doctor.ARK010}, rootID: "ark-3f9c0a12", roleOn: true, initial: true},
			check: func(t *testing.T, r *rig, mnt string, d ark.Drive) {
				rj, err := ark.ReadRootJSON(filepath.Join(mnt, "localai", ark.RootJSONName))
				if err != nil || rj.RootID != "ark-3f9c0a12" || rj.Kind != ark.KindArkDrive || rj.CreatedBy.NodeID != "node-a" {
					t.Errorf("recreated ROOT.json = %+v, %v", rj, err)
				}
			}},
		{name: "ROOT.json missing, content present, mounted read-only: carried in memory, not written",
			setup: func(r *rig, mnt string) ark.MountEntry {
				r.initDrive(mnt, uuidA, "")
				if err := os.Remove(filepath.Join(mnt, "localai", ark.RootJSONName)); err != nil {
					t.Fatal(err)
				}
				return r.arkDisk(120, "8:17", mnt, "/dev/sdb1", uuidA, "ext4", "ro,relatime", "rw")
			},
			want: want{state: ark.StateReadOnly, codes: []string{doctor.ARK003, doctor.ARK010}, rootID: "ark-3f9c0a12", roleOn: true, initial: true},
			check: func(t *testing.T, r *rig, mnt string, d ark.Drive) {
				if exists(filepath.Join(mnt, "localai", ark.RootJSONName)) {
					t.Error("ROOT.json was written to a read-only drive")
				}
			}},
		{name: "a damaged ROOT.json makes the drive unusable and the file is left alone",
			setup: func(r *rig, mnt string) ark.MountEntry {
				r.initDrive(mnt, uuidA, "")
				writeFile(t, filepath.Join(mnt, "localai", ark.RootJSONName), "{not json")
				return r.arkDisk(120, "8:17", mnt, "/dev/sdb1", uuidA, "ext4")
			},
			want: want{state: ark.StateUnusable, rootID: "ark-3f9c0a12"},
			check: func(t *testing.T, r *rig, mnt string, d ark.Drive) {
				if d.Detail == "" || !strings.Contains(d.Detail, "ROOT.json") {
					t.Errorf("detail = %q", d.Detail)
				}
				if got := readFile(t, filepath.Join(mnt, "localai", ark.RootJSONName)); got != "{not json" {
					t.Errorf("ROOT.json was changed: %q", got)
				}
			}},
		{name: "a ROOT.json of a newer schema is left alone",
			setup: func(r *rig, mnt string) ark.MountEntry {
				r.initDrive(mnt, uuidA, "")
				writeFile(t, filepath.Join(mnt, "localai", ark.RootJSONName), string(fixture(t, "schema-v2.json")))
				return r.arkDisk(120, "8:17", mnt, "/dev/sdb1", uuidA, "ext4")
			},
			want: want{state: ark.StateUnusable, rootID: "ark-3f9c0a12"}},
		{name: "a write probe refused by the filesystem is read_only (LAI-ARK-003) with the errno",
			setup: func(r *rig, mnt string) ark.MountEntry {
				r.initDrive(mnt, uuidA, "")
				r.fs.openErr = syscall.EROFS
				return r.arkDisk(120, "8:17", mnt, "/dev/sdb1", uuidA, "ext4")
			},
			want: want{state: ark.StateReadOnly, codes: []string{doctor.ARK003}, rootID: "ark-3f9c0a12", roleOn: true, initial: true},
			check: func(t *testing.T, r *rig, mnt string, d ark.Drive) {
				if got := d.Problems[0].Details["errno"]; got != "read-only file system" {
					t.Errorf("errno detail = %v", got)
				}
				if !r.m.ActiveRoots()[0].ReadOnly {
					t.Error("the root of a drive that cannot be written must be read-only")
				}
			}},
		{name: "a full drive (ENOSPC on the probe) is still ready: it can be read and cleaned",
			setup: func(r *rig, mnt string) ark.MountEntry {
				r.initDrive(mnt, uuidA, "")
				r.fs.openErr = syscall.ENOSPC
				return r.arkDisk(120, "8:17", mnt, "/dev/sdb1", uuidA, "ext4")
			},
			want: want{state: ark.StateReady, rootID: "ark-3f9c0a12", roleOn: true, initial: true}},
		{name: "a layout with no .staging is probed in the root directory",
			setup: func(r *rig, mnt string) ark.MountEntry {
				r.initDrive(mnt, uuidA, "")
				if err := os.Remove(filepath.Join(mnt, "localai", ".staging")); err != nil {
					t.Fatal(err)
				}
				return r.arkDisk(120, "8:17", mnt, "/dev/sdb1", uuidA, "ext4")
			},
			want: want{state: ark.StateReady, rootID: "ark-3f9c0a12", roleOn: true, initial: true},
			check: func(t *testing.T, r *rig, mnt string, d ark.Drive) {
				if exists(filepath.Join(mnt, "localai", ".staging")) {
					t.Error("the probe created .staging")
				}
			}},
		{name: "the null UUID gives no root id: unusable",
			setup: func(r *rig, mnt string) ark.MountEntry {
				return r.arkDisk(120, "8:17", mnt, "/dev/sdb1", "00000000-0000-0000-0000-000000000000", "ext4")
			},
			want: want{state: ark.StateUnusable}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t)
			mnt := r.dir("AI-ARK")
			e := c.setup(r, mnt)
			r.mounts.set(r.rootDisk(), e)
			r.refresh()

			d := r.only()
			if d.State != c.want.state {
				t.Fatalf("state = %s, want %s (problems %v, detail %q)", d.State, c.want.state, codes(d.Problems), d.Detail)
			}
			if d.RootID != c.want.rootID {
				t.Errorf("root id = %q, want %q", d.RootID, c.want.rootID)
			}
			if got := codes(d.Problems); !reflect.DeepEqual(got, c.want.codes) {
				t.Errorf("codes = %v, want %v", got, c.want.codes)
			}
			if d.Initialized != c.want.initial {
				t.Errorf("initialized = %v", d.Initialized)
			}
			if got := len(r.m.ActiveRoots()) == 1; got != c.want.roleOn {
				t.Errorf("in the active set = %v, want %v", got, c.want.roleOn)
			}
			if c.check != nil {
				c.check(t, r, mnt, d)
			}
		})
	}
}

// TestOtherLabelsAreNotArkDrives: AI-ARK2, ai-ark and friends are ignored entirely: not listed, no
// event, no read of them beyond blkid's label.
func TestOtherLabelsAreNotArkDrives(t *testing.T) {
	r := newRig(t)
	var es []ark.MountEntry
	es = append(es, r.rootDisk())
	for i, label := range []string{"AI-ARK2", "ai-ark", "AI-ARK ", "Ai-Ark", "AI_ARK", "", "ARK"} {
		mnt := r.dir("m" + string(rune('a'+i)))
		dev := "/dev/sd" + string(rune('b'+i)) + "1"
		r.block.disk(dev, label, uuidA, "ext4")
		es = append(es, entry(t, 100+i, "8:"+string(rune('0'+i)), mnt, "ext4", dev))
	}
	r.mounts.set(es...)
	r.refresh()
	if ds := r.m.Drives(); len(ds) != 0 {
		t.Fatalf("drives = %+v", ds)
	}
	if n, _ := r.bus.counts(); n != 0 {
		t.Errorf("events: %d", n)
	}
	if got := r.fs.touched(r.tmp); len(got) != 0 {
		t.Errorf("a non-ark mount was touched: %v", got)
	}
}

func TestPseudoAndOtherMountsAreNeverProbed(t *testing.T) {
	r := newRig(t)
	r.mounts.set(parseFixture(t, "desktop.txt")...)
	r.refresh()
	if len(r.m.Drives()) != 0 {
		t.Errorf("drives = %+v", r.m.Drives())
	}
	// Only real device candidates get a blkid call: the root disk, the EFI partition and the two
	// USB sticks. Not tmpfs, proc, sysfs, devtmpfs, the snap loop device, gvfs or the overlay.
	want := []string{"/dev/nvme0n1p2", "/dev/nvme0n1p1", "/dev/sdb1", "/dev/sdc1"}
	if !reflect.DeepEqual(r.block.probes, want) {
		t.Errorf("blkid was asked about %v, want %v", r.block.probes, want)
	}
}

// ---- several drives ---------------------------------------------------------------------------------

func TestDuplicateUUIDSecondDriveIsUnusable(t *testing.T) {
	r := newRig(t)
	m1, m2 := r.dir("first"), r.dir("second")
	r.initDrive(m1, uuidA, "")
	r.initDrive(m2, uuidA, "")
	// The later mount is listed first on purpose: the order of the table is not the order of mounting.
	later := r.arkDisk(140, "8:33", m2, "/dev/sdc1", uuidA, "ext4")
	earlier := r.arkDisk(120, "8:17", m1, "/dev/sdb1", uuidA, "ext4")
	r.mounts.set(r.rootDisk(), later, earlier)
	r.refresh()

	ds := r.m.Drives()
	if len(ds) != 2 {
		t.Fatalf("drives = %+v", ds)
	}
	if ds[0].Mount != m1 || ds[0].State != ark.StateReady {
		t.Errorf("first = %+v", ds[0])
	}
	if ds[1].Mount != m2 || ds[1].State != ark.StateUnusable || !hasProblem(ds[1], doctor.ARK006) || ds[1].Initialized {
		t.Errorf("second = %+v", ds[1])
	}
	if ds[1].RootID != ds[0].RootID {
		t.Errorf("a clone keeps the id its UUID gives: %q vs %q", ds[1].RootID, ds[0].RootID)
	}
	if got := r.m.ActiveRoots(); len(got) != 1 || got[0].Mount != m1 {
		t.Errorf("active roots = %+v", got)
	}
	if got := r.fs.touched(m2); len(got) != 0 {
		t.Errorf("the clone was touched: %v", got)
	}
	d, ok := r.m.Drive("ark-3f9c0a12")
	if !ok || d.Mount != m1 {
		t.Errorf("Drive(id) = %+v %v; it must be the one in use", d, ok)
	}
	// Both drives are still mounted, so pulling the clone changes nothing for the active set.
	if n, _ := r.bus.counts(); n != 1 {
		t.Errorf("mounted events = %d", n)
	}
}

func TestOneDeviceMountedTwiceIsNotACloneOfItself(t *testing.T) {
	r := newRig(t)
	mnt, other := r.dir("AI-ARK"), r.dir("second-mount")
	r.initDrive(mnt, uuidA, "")
	e1 := r.arkDisk(120, "8:17", mnt, "/dev/sdb1", uuidA, "ext4")
	e2 := entry(t, 121, "8:17", other, "ext4", "/dev/sdb1") // the same device, mounted again
	r.mounts.set(r.rootDisk(), e1, e2)
	r.refresh()
	d := r.only()
	if d.State != ark.StateReady || d.Mount != mnt {
		t.Errorf("drive = %+v", d)
	}
}

func TestDrivesAreOrderedByMountID(t *testing.T) {
	r := newRig(t)
	uuids := []string{"b81d44e0-1c2a-4f6b-8d90-5a6b7c8d9e0f", "0a0a0a0a-1c2a-4f6b-8d90-5a6b7c8d9e0f", "f0f0f0f0-1c2a-4f6b-8d90-5a6b7c8d9e0f"}
	var es []ark.MountEntry
	mids := []int{50, 20, 35}
	for i, id := range mids {
		mnt := r.dir("d" + string(rune('a'+i)))
		r.initDrive(mnt, uuids[i], "")
		es = append(es, r.arkDisk(id, "8:"+string(rune('1'+i)), mnt, "/dev/sd"+string(rune('b'+i))+"1", uuids[i], "ext4"))
	}
	r.mounts.set(append([]ark.MountEntry{r.rootDisk()}, es...)...)
	r.refresh()
	var gotMIDs []int
	for _, d := range r.m.Drives() {
		gotMIDs = append(gotMIDs, d.MountID)
	}
	if want := []int{20, 35, 50}; !reflect.DeepEqual(gotMIDs, want) {
		t.Errorf("drive order = %v, want %v", gotMIDs, want)
	}
	var rootMIDs []int
	for _, rt := range r.m.ActiveRoots() {
		rootMIDs = append(rootMIDs, rt.MountID)
	}
	if !reflect.DeepEqual(rootMIDs, gotMIDs) {
		t.Errorf("active root order = %v", rootMIDs)
	}
}

// ---- drives that are not mounted ---------------------------------------------------------------------

func TestUnmountedDrivesAreReportedNeverMounted(t *testing.T) {
	r := newRig(t)
	mnt := r.dir("AI-ARK")
	r.initDrive(mnt, uuidA, "")
	mounted := r.arkDisk(120, "8:17", mnt, "/dev/sdb1", uuidA, "ext4")
	r.block.disk("/dev/sdd1", ark.ArkLabel, uuidB, "ext4")
	r.block.disk("/dev/sde1", ark.ArkLabel, "5E6F-7A8B", "exfat")
	r.block.labelled = []string{"/dev/sdb1", "/dev/sdd1", "/dev/sde1"} // blkid also lists the mounted one
	r.mounts.set(r.rootDisk(), mounted)
	r.refresh()

	ds := r.m.Drives()
	if len(ds) != 3 {
		t.Fatalf("drives = %+v", ds)
	}
	if ds[0].State != ark.StateReady {
		t.Errorf("mounted drive = %+v", ds[0])
	}
	nm := ds[1]
	if nm.Device != "/dev/sdd1" || nm.State != ark.StateNotMounted || nm.Mount != "" || nm.RootID != "ark-b81d44e0" ||
		!reflect.DeepEqual(codes(nm.Problems), []string{doctor.ARK005}) || nm.Active() {
		t.Errorf("not mounted drive = %+v", nm)
	}
	ex := ds[2]
	if ex.Device != "/dev/sde1" || ex.State != ark.StateUnusable || ex.RootID != "" || !reflect.DeepEqual(codes(ex.Problems), []string{doctor.ARK002}) {
		t.Errorf("unmounted exfat drive = %+v", ex)
	}
	if len(r.m.ActiveRoots()) != 1 {
		t.Errorf("a drive that is not mounted joined the active set: %+v", r.m.ActiveRoots())
	}
	if n, g := r.bus.counts(); n != 1 || g != 0 {
		t.Errorf("events mounted=%d unmounted=%d; only the mounted drive has one", n, g)
	}
}

func TestUnmountedScanKeepsTheLastAnswerWhenBlkidFails(t *testing.T) {
	r := newRig(t)
	r.block.disk("/dev/sdd1", ark.ArkLabel, uuidB, "ext4")
	r.block.labelled = []string{"/dev/sdd1"}
	r.mounts.set(r.rootDisk())
	r.refresh()
	if r.only().State != ark.StateNotMounted {
		t.Fatal("setup")
	}
	r.block.findErr = ark.ErrBlkidTimeout
	r.refresh()
	if d := r.only(); d.State != ark.StateNotMounted {
		t.Errorf("a failed scan dropped the drive: %+v", d)
	}
	r.block.findErr = nil
	r.block.labelled = nil
	r.refresh()
	if len(r.m.Drives()) != 0 {
		t.Errorf("an unplugged drive stayed: %+v", r.m.Drives())
	}
}

func TestAnUnmountedDriveThatGetsMountedBecomesActive(t *testing.T) {
	r := newRig(t)
	mnt := r.dir("AI-ARK")
	r.initDrive(mnt, uuidA, "")
	r.block.disk("/dev/sdb1", ark.ArkLabel, uuidA, "ext4")
	r.block.labelled = []string{"/dev/sdb1"}
	r.mounts.set(r.rootDisk())
	r.refresh()
	if d := r.only(); d.State != ark.StateNotMounted {
		t.Fatalf("drive = %+v", d)
	}
	// The user presses Mount (SYNC-03 does that); the table changes and the next pass sees it.
	r.mounts.set(r.rootDisk(), entry(t, 120, "8:17", mnt, "ext4", "/dev/sdb1"))
	r.refresh()
	d := r.only()
	if d.State != ark.StateReady || d.Device != "/dev/sdb1" {
		t.Errorf("drive = %+v", d)
	}
	if n, _ := r.bus.counts(); n != 1 {
		t.Errorf("mounted events = %d", n)
	}
}

// ---- blkid and the mount table failing ------------------------------------------------------------------

func TestBlkidFailureOnAMountedDeviceIsRetried(t *testing.T) {
	for name, probeErr := range map[string]error{"timeout": ark.ErrBlkidTimeout, "other failure": errors.New("blkid exited 4")} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t)
			mnt := r.dir("AI-ARK")
			r.initDrive(mnt, uuidA, "")
			r.mounts.set(r.rootDisk(), r.arkDisk(120, "8:17", mnt, "/dev/sdb1", uuidA, "ext4"))
			r.block.probeErr["/dev/sdb1"] = probeErr
			r.refresh()
			if len(r.m.Drives()) != 0 {
				t.Fatalf("a mount whose label cannot be read was listed: %+v", r.m.Drives())
			}
			if got := r.fs.touched(mnt); len(got) != 0 {
				t.Errorf("the mount was read without a label: %v", got)
			}
			// Failures are not cached: the next pass asks again, and the drive appears.
			delete(r.block.probeErr, "/dev/sdb1")
			r.refresh()
			if d := r.only(); d.State != ark.StateReady {
				t.Errorf("drive = %+v", d)
			}
			if r.block.probeCount("/dev/sdb1") != 2 {
				t.Errorf("probes = %d", r.block.probeCount("/dev/sdb1"))
			}
		})
	}
}

func TestMountTableReadFailureKeepsTheState(t *testing.T) {
	r := newRig(t)
	mnt := r.dir("AI-ARK")
	r.initDrive(mnt, uuidA, "")
	r.mounts.set(r.rootDisk(), r.arkDisk(120, "8:17", mnt, "/dev/sdb1", uuidA, "ext4"))
	r.refresh()
	r.tr.reset()

	r.mounts.fail(errors.New("open /proc/self/mountinfo: too many open files"))
	if err := r.m.Refresh(context.Background()); err == nil {
		t.Fatal("a failed read of the mount table must be an error")
	}
	if d := r.only(); d.State != ark.StateReady {
		t.Errorf("a failed read changed the drives: %+v", d)
	}
	if got := r.tr.list(); len(got) != 0 {
		t.Errorf("a failed read caused events: %v", got)
	}
	if _, gone := r.bus.counts(); gone != 0 {
		t.Error("a failed read looked like a removal")
	}
}

func TestSkippedMountInfoLinesDoNotHideADrive(t *testing.T) {
	r := newRig(t)
	mnt := r.dir("AI-ARK")
	r.initDrive(mnt, uuidA, "")
	r.mounts.set(r.rootDisk(), r.arkDisk(120, "8:17", mnt, "/dev/sdb1", uuidA, "ext4"))
	r.mounts.mu.Lock()
	r.mounts.err = &ark.ParseError{Line: 7, Err: errors.New("too few fields")} // entries came back too
	r.mounts.mu.Unlock()
	r.refresh()
	if d := r.only(); d.State != ark.StateReady {
		t.Errorf("drive = %+v", d)
	}
}

// ---- removal --------------------------------------------------------------------------------------------

func TestSurpriseRemoval(t *testing.T) {
	r := newRig(t)
	mnt := r.dir("AI-ARK")
	r.initDrive(mnt, uuidA, "")
	e := r.arkDisk(120, "8:17", mnt, "/dev/sdb1", uuidA, "ext4")
	r.mounts.set(r.rootDisk(), e)
	r.refresh()
	r.tr.reset()

	// The drive is pulled with no eject.
	r.mounts.set(r.rootDisk())
	r.refresh()

	if got := r.hookedIDs(); !reflect.DeepEqual(got, []string{"ark-3f9c0a12"}) {
		t.Errorf("transfer-cancel hook calls = %v", got)
	}
	if len(r.bus.gone) != 1 || r.bus.gone[0] != (events.DriveUnmounted{RootID: "ark-3f9c0a12", SafeToUnplug: false}) {
		t.Errorf("drive.unmounted = %+v", r.bus.gone)
	}
	if !r.alerts.isActive() || !reflect.DeepEqual(r.alerts.raised[0]["root_ids"], []string{"ark-3f9c0a12"}) {
		t.Errorf("LAI-ARK-007 raised = %v", r.alerts.raised)
	}
	if len(r.m.Drives()) != 0 || len(r.m.ActiveRoots()) != 0 {
		t.Errorf("drives = %+v roots = %+v", r.m.Drives(), r.m.ActiveRoots())
	}
	// The order: the active set is updated first (so the roots list is right when the event is read),
	// then the transfers are cancelled, then the event, and the alert is part of the same step.
	want := []string{"active []", "hook ark-3f9c0a12", "event unmounted ark-3f9c0a12 safe=false", "alert raised [ark-3f9c0a12]"}
	if got := r.tr.list(); !reflect.DeepEqual(got, want) {
		t.Errorf("order = %q\nwant    %q", got, want)
	}
	// The blkid answer of the vanished mount is forgotten.
	r.mounts.set(r.rootDisk(), e)
	r.block.disk("/dev/sdb1", ark.ArkLabel, uuidA, "ext4")
	r.refresh()
	if r.block.probeCount("/dev/sdb1") != 2 {
		t.Errorf("blkid probes of the drive = %d; the cache must not outlive the mount", r.block.probeCount("/dev/sdb1"))
	}
}

func TestSurpriseRemovalAlertClearsWhenTheDriveComesBackClean(t *testing.T) {
	r := newRig(t)
	mnt := r.dir("AI-ARK")
	r.initDrive(mnt, uuidA, "")
	e := r.arkDisk(120, "8:17", mnt, "/dev/sdb1", uuidA, "ext4")
	r.mounts.set(r.rootDisk(), e)
	r.refresh()
	r.mounts.set(r.rootDisk())
	r.refresh()
	if !r.alerts.isActive() {
		t.Fatal("setup: no alert")
	}
	r.mounts.set(r.rootDisk(), e)
	r.refresh()
	if r.alerts.isActive() {
		t.Error("LAI-ARK-007 stayed after the drive came back clean")
	}
	if n, _ := r.bus.counts(); n != 2 {
		t.Errorf("mounted events = %d, want 2 (first mount and the return)", n)
	}
}

func TestSurpriseRemovalAlertExpiresAfter24Hours(t *testing.T) {
	r := newRig(t)
	mnt := r.dir("AI-ARK")
	r.initDrive(mnt, uuidA, "")
	r.mounts.set(r.rootDisk(), r.arkDisk(120, "8:17", mnt, "/dev/sdb1", uuidA, "ext4"))
	r.refresh()
	r.mounts.set(r.rootDisk())
	r.refresh()

	r.clock.Advance(24*time.Hour - time.Minute)
	r.refresh()
	if !r.alerts.isActive() {
		t.Fatal("cleared before 24 h")
	}
	r.clock.Advance(2 * time.Minute)
	r.refresh()
	if r.alerts.isActive() {
		t.Error("LAI-ARK-007 must be cleared after 24 h")
	}
}

func TestReturningWithErrorsDoesNotClearTheAlert(t *testing.T) {
	r := newRig(t)
	mnt := r.dir("AI-ARK")
	r.initDrive(mnt, uuidA, "")
	e := r.arkDisk(120, "8:17", mnt, "/dev/sdb1", uuidA, "ext4")
	r.mounts.set(r.rootDisk(), e)
	r.refresh()
	r.mounts.set(r.rootDisk())
	r.refresh()
	r.errs.set("/dev/sdb1", 2)
	r.mounts.set(r.rootDisk(), e)
	r.refresh()
	if !r.alerts.isActive() {
		t.Error("only a clean mount clears LAI-ARK-007")
	}
}

func TestRemovingOneOfTwoDrivesLeavesTheOtherAlone(t *testing.T) {
	r := newRig(t)
	m1, m2 := r.dir("one"), r.dir("two")
	r.initDrive(m1, uuidA, "")
	r.initDrive(m2, uuidB, "")
	e1 := r.arkDisk(120, "8:17", m1, "/dev/sdb1", uuidA, "ext4")
	e2 := r.arkDisk(121, "8:33", m2, "/dev/sdc1", uuidB, "ext4")
	r.mounts.set(r.rootDisk(), e1, e2)
	r.refresh()
	r.tr.reset()
	r.mounts.set(r.rootDisk(), e2)
	r.refresh()
	if got := r.hookedIDs(); !reflect.DeepEqual(got, []string{"ark-3f9c0a12"}) {
		t.Errorf("hook calls = %v", got)
	}
	if roots := r.m.ActiveRoots(); len(roots) != 1 || roots[0].ID != "ark-b81d44e0" {
		t.Errorf("roots = %+v", roots)
	}
	if n, g := r.bus.counts(); n != 2 || g != 1 {
		t.Errorf("events mounted=%d unmounted=%d", n, g)
	}
}

func TestStateChangeOfAMountedDriveIsAnotherMountedEvent(t *testing.T) {
	r := newRig(t)
	mnt := r.dir("AI-ARK")
	r.initDrive(mnt, uuidA, "")
	r.mounts.set(r.rootDisk(), r.arkDisk(120, "8:17", mnt, "/dev/sdb1", uuidA, "ext4"))
	r.refresh()
	// The kernel turns the drive read-only after an error.
	r.errs.set("/dev/sdb1", 4)
	r.mounts.set(r.rootDisk(), entry(t, 120, "8:17", mnt, "ext4", "/dev/sdb1", "rw,relatime", "ro,errors=remount-ro"))
	r.refresh()
	if d := r.only(); d.State != ark.StateReadOnly || !hasProblem(d, doctor.ARK004) {
		t.Fatalf("drive = %+v", d)
	}
	if len(r.bus.mounted) != 2 || r.bus.mounted[1] != (events.DriveMounted{RootID: "ark-3f9c0a12", Mount: mnt, State: "read_only", ReadOnly: true}) {
		t.Errorf("mounted events = %+v", r.bus.mounted)
	}
	if _, g := r.bus.counts(); g != 0 {
		t.Error("a drive that stayed mounted must not be reported removed")
	}
	if roots := r.m.ActiveRoots(); len(roots) != 1 || !roots[0].NeedsServeVerify() || !roots[0].ReadOnly {
		t.Errorf("roots = %+v", roots)
	}
}

func TestADriveThatBecomesUnusableWhileMountedLeavesTheActiveSet(t *testing.T) {
	r := newRig(t)
	mnt := r.dir("AI-ARK")
	r.initDrive(mnt, uuidA, "")
	r.mounts.set(r.rootDisk(), r.arkDisk(120, "8:17", mnt, "/dev/sdb1", uuidA, "ext4"))
	r.refresh()
	writeFile(t, filepath.Join(mnt, "localai", ark.RootJSONName), "garbage")
	r.refresh()
	if d := r.only(); d.State != ark.StateUnusable {
		t.Fatalf("drive = %+v", d)
	}
	if len(r.bus.gone) != 1 || !r.bus.gone[0].SafeToUnplug {
		t.Errorf("unmounted = %+v; a drive that is still plugged in is not a surprise removal", r.bus.gone)
	}
	if r.alerts.isActive() {
		t.Error("no LAI-ARK-007 for a drive that is still mounted")
	}
	if got := r.hookedIDs(); len(got) != 1 {
		t.Errorf("the transfers writing to it must be cancelled: %v", got)
	}
}

// ---- eject bookkeeping (the eject itself is SYNC-03) -----------------------------------------------------

func TestEjectBookkeeping(t *testing.T) {
	setup := func(t *testing.T) (*rig, ark.MountEntry) {
		r := newRig(t)
		mnt := r.dir("AI-ARK")
		r.initDrive(mnt, uuidA, "")
		e := r.arkDisk(120, "8:17", mnt, "/dev/sdb1", uuidA, "ext4")
		r.mounts.set(r.rootDisk(), e)
		r.refresh()
		r.tr.reset()
		return r, e
	}

	t.Run("a clean eject: no surprise, one event with safe_to_unplug true", func(t *testing.T) {
		r, _ := setup(t)
		if err := r.m.BeginEject("ark-3f9c0a12"); err != nil {
			t.Fatal(err)
		}
		if d := r.only(); d.State != ark.StateEjecting || !d.ReadOnly {
			t.Errorf("drive = %+v", d)
		}
		if len(r.m.ActiveRoots()) != 0 {
			t.Error("an ejecting drive must leave the active roots (sync excludes it)")
		}
		// umount happens: the table loses the mount. The detector says nothing yet.
		r.mounts.set(r.rootDisk())
		r.refresh()
		if got := r.tr.list(); !reflect.DeepEqual(got, []string{"active []"}) {
			t.Errorf("trace = %q; no event, hook or alert before the eject is finished", got)
		}
		if d := r.only(); d.State != ark.StateEjecting {
			t.Errorf("an ejecting drive that already left the table = %+v", d)
		}
		r.m.FinishEject(context.Background(), "ark-3f9c0a12")
		want := []string{"active []", "hook ark-3f9c0a12", "event unmounted ark-3f9c0a12 safe=true"}
		if got := r.tr.list(); !reflect.DeepEqual(got, want) {
			t.Errorf("trace = %q\nwant    %q", got, want)
		}
		if len(r.m.Drives()) != 0 || r.alerts.isActive() || len(r.alerts.raised) != 0 {
			t.Errorf("drives=%v alert=%v", r.m.Drives(), r.alerts.raised)
		}
	})

	t.Run("refusals", func(t *testing.T) {
		r, _ := setup(t)
		if err := r.m.BeginEject("ark-deadbeef"); !errors.Is(err, ark.ErrNoSuchDrive) {
			t.Errorf("unknown drive: %v", err)
		}
		if err := r.m.BeginEject("ark-3f9c0a12"); err != nil {
			t.Fatal(err)
		}
		if err := r.m.BeginEject("ark-3f9c0a12"); !errors.Is(err, ark.ErrAlreadyEjecting) {
			t.Errorf("second eject: %v", err)
		}
	})

	t.Run("a cancelled eject puts the drive back with no event", func(t *testing.T) {
		r, _ := setup(t)
		if err := r.m.BeginEject("ark-3f9c0a12"); err != nil {
			t.Fatal(err)
		}
		r.m.AbortEject(context.Background(), "ark-3f9c0a12")
		if d := r.only(); d.State != ark.StateReady {
			t.Errorf("drive = %+v", d)
		}
		if len(r.m.ActiveRoots()) != 1 {
			t.Error("the root did not come back")
		}
		want := []string{"active []", "active [ark-3f9c0a12]"}
		if got := r.tr.list(); !reflect.DeepEqual(got, want) {
			t.Errorf("trace = %q, want %q", got, want)
		}
		r.m.AbortEject(context.Background(), "ark-3f9c0a12") // harmless when not ejecting
	})

	t.Run("an eject that never finishes is treated as a removal after the timeout", func(t *testing.T) {
		r, _ := setup(t)
		if err := r.m.BeginEject("ark-3f9c0a12"); err != nil {
			t.Fatal(err)
		}
		r.mounts.set(r.rootDisk())
		r.clock.Advance(ark.DefaultEjectTimeout + time.Minute)
		r.refresh()
		if len(r.bus.gone) != 1 || r.bus.gone[0].SafeToUnplug {
			t.Errorf("unmounted = %+v", r.bus.gone)
		}
		if !r.alerts.isActive() {
			t.Error("a drive that vanished during a stuck eject is a surprise removal")
		}
	})
}

// ---- subscriptions and hooks ------------------------------------------------------------------------------

func TestSubscribeSeesTheSameEvents(t *testing.T) {
	r := newRig(t)
	ch := r.m.Subscribe()
	mnt := r.dir("AI-ARK")
	r.initDrive(mnt, uuidA, "")
	e := r.arkDisk(120, "8:17", mnt, "/dev/sdb1", uuidA, "ext4")
	r.mounts.set(r.rootDisk(), e)
	r.refresh()
	got := recv(t, ch, time.Second)
	want := ark.DriveEvent{Type: events.TypeDriveMounted, RootID: "ark-3f9c0a12", Mount: mnt, State: ark.StateReady}
	if got != want {
		t.Errorf("event = %+v, want %+v", got, want)
	}
	r.mounts.set(r.rootDisk())
	r.refresh()
	got = recv(t, ch, time.Second)
	if got != (ark.DriveEvent{Type: events.TypeDriveUnmounted, RootID: "ark-3f9c0a12", SafeToUnplug: false}) {
		t.Errorf("event = %+v", got)
	}
	quiet(t, ch, 20*time.Millisecond)
}

func TestASlowSubscriberNeverBlocksTheDetector(t *testing.T) {
	r := newRig(t)
	_ = r.m.Subscribe() // never read
	mnt := r.dir("AI-ARK")
	r.initDrive(mnt, uuidA, "")
	e := r.arkDisk(120, "8:17", mnt, "/dev/sdb1", uuidA, "ext4")
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 40; i++ { // 80 events, the buffer holds 32
			r.mounts.set(r.rootDisk(), e)
			r.refresh()
			r.mounts.set(r.rootDisk())
			r.refresh()
		}
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the detector blocked on a subscriber that does not read")
	}
}

func TestNewManagerChecksItsDependencies(t *testing.T) {
	if _, err := ark.NewManager(ark.Options{}); err == nil {
		t.Fatal("an empty Options must be refused")
	} else {
		for _, name := range []string{"Mounts", "Block", "FS", "Clock"} {
			if !strings.Contains(err.Error(), name) {
				t.Errorf("error %q does not name %s", err, name)
			}
		}
	}
}

// ---- the running loop: the poll path and the fallback path ----------------------------------------------

func startRun(t *testing.T, r *rig) context.CancelFunc {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); r.m.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	// The first pass has run once the table was read.
	waitFor(t, 2*time.Second, "the first pass", func() bool { return r.mounts.readCount() >= 1 })
	waitFor(t, 2*time.Second, "the scan ticker", func() bool { return r.clock.Tickers() >= 1 })
	return cancel
}

func waitFor(t *testing.T, d time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// TestPluggingADriveIsSeenWithinTwoSecondsOnThePollPath: a mount-table signal is enough, no timer.
func TestPluggingADriveIsSeenWithinTwoSecondsOnThePollPath(t *testing.T) {
	r := newRig(t, func(o *ark.Options) { o.Debounce = 20 * time.Millisecond })
	mnt := r.dir("AI-ARK")
	r.initDrive(mnt, uuidA, "")
	r.mounts.set(r.rootDisk())
	ch := r.m.Subscribe()
	startRun(t, r)

	start := time.Now()
	r.mounts.set(r.rootDisk(), r.arkDisk(120, "8:17", mnt, "/dev/sdb1", uuidA, "ext4"))
	r.mounts.signal()
	ev := recv(t, ch, 2*time.Second)
	if ev.Type != events.TypeDriveMounted || ev.RootID != "ark-3f9c0a12" || ev.State != ark.StateReady {
		t.Errorf("event = %+v", ev)
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Errorf("took %v", took)
	}
}

// TestPluggingADriveIsSeenOnTheFallbackTimerWithoutAnySignal: with the poll disabled (it never
// fires), the 30 s re-read finds the drive.
func TestPluggingADriveIsSeenOnTheFallbackTimerWithoutAnySignal(t *testing.T) {
	r := newRig(t)
	mnt := r.dir("AI-ARK")
	r.initDrive(mnt, uuidA, "")
	r.mounts.set(r.rootDisk())
	ch := r.m.Subscribe()
	startRun(t, r)

	r.mounts.set(r.rootDisk(), r.arkDisk(120, "8:17", mnt, "/dev/sdb1", uuidA, "ext4"))
	quiet(t, ch, 30*time.Millisecond) // nothing yet: nobody told the detector
	r.clock.Advance(29 * time.Second)
	quiet(t, ch, 30*time.Millisecond)
	r.clock.Advance(1 * time.Second)
	ev := recv(t, ch, 2*time.Second)
	if ev.Type != events.TypeDriveMounted || ev.RootID != "ark-3f9c0a12" {
		t.Errorf("event = %+v", ev)
	}
}

// TestTheTimerAlsoScansForUnmountedDrives: the 30 s tick runs the blkid scan, a mount-table signal
// does not.
func TestTheTimerAlsoScansForUnmountedDrives(t *testing.T) {
	r := newRig(t)
	r.block.disk("/dev/sdd1", ark.ArkLabel, uuidB, "ext4")
	r.mounts.set(r.rootDisk())
	startRun(t, r)
	scans := r.block.findCount()
	if scans != 1 {
		t.Fatalf("the first pass scans once, got %d", scans)
	}

	r.block.mu.Lock()
	r.block.labelled = []string{"/dev/sdd1"}
	r.block.mu.Unlock()
	r.mounts.signal()
	waitFor(t, 2*time.Second, "a pass after the signal", func() bool { return r.mounts.readCount() >= 2 })
	time.Sleep(30 * time.Millisecond)
	if got := r.block.findCount(); got != scans {
		t.Errorf("a mount-table change must not run the blkid scan (%d scans)", got)
	}
	if len(r.m.Drives()) != 0 {
		t.Errorf("drives = %+v", r.m.Drives())
	}

	r.clock.Advance(ark.DefaultScanInterval)
	waitFor(t, 2*time.Second, "the unmounted drive", func() bool { return len(r.m.Drives()) == 1 })
	if d := r.m.Drives()[0]; d.State != ark.StateNotMounted {
		t.Errorf("drive = %+v", d)
	}
}

// TestABurstOfChangesIsOnePass: the debounce collapses what an automounter does.
func TestABurstOfChangesIsOnePass(t *testing.T) {
	r := newRig(t, func(o *ark.Options) { o.Debounce = 150 * time.Millisecond })
	r.mounts.set(r.rootDisk())
	startRun(t, r)
	before := r.mounts.readCount()
	for i := 0; i < 20; i++ {
		r.mounts.signal()
		time.Sleep(5 * time.Millisecond)
	}
	waitFor(t, 2*time.Second, "the pass after the burst", func() bool { return r.mounts.readCount() > before })
	time.Sleep(300 * time.Millisecond)
	if got := r.mounts.readCount() - before; got != 1 {
		t.Errorf("a burst of 20 signals took %d passes, want 1", got)
	}
}

func TestRunStopsAndClosesSubscriptions(t *testing.T) {
	r := newRig(t)
	r.mounts.set(r.rootDisk())
	ch := r.m.Subscribe()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); r.m.Run(ctx) }()
	waitFor(t, 2*time.Second, "the first pass", func() bool { return r.mounts.readCount() >= 1 })
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop")
	}
	if _, ok := <-ch; ok {
		t.Error("the subscription must be closed when the detector stops")
	}
	if late := r.m.Subscribe(); func() bool { _, ok := <-late; return ok }() {
		t.Error("a subscription taken after the stop must be closed")
	}
}

// TestAMountSourceThatCannotBeWatchedStillRunsOnTheTimer: Changes is closed at once (the file could
// not be opened); the detector does not spin and the timer still works.
func TestAMountSourceThatCannotBeWatchedStillRunsOnTheTimer(t *testing.T) {
	r := newRig(t)
	close(r.mounts.changes)
	mnt := r.dir("AI-ARK")
	r.initDrive(mnt, uuidA, "")
	r.mounts.set(r.rootDisk())
	ch := r.m.Subscribe()
	startRun(t, r)
	time.Sleep(50 * time.Millisecond)
	if got := r.mounts.readCount(); got > 3 {
		t.Fatalf("the detector spins on a closed change channel: %d reads", got)
	}
	r.mounts.set(r.rootDisk(), r.arkDisk(120, "8:17", mnt, "/dev/sdb1", uuidA, "ext4"))
	r.clock.Advance(ark.DefaultScanInterval)
	if ev := recv(t, ch, 2*time.Second); ev.Type != events.TypeDriveMounted {
		t.Errorf("event = %+v", ev)
	}
}

func TestStartAndStopAsAWorker(t *testing.T) {
	r := newRig(t)
	mnt := r.dir("AI-ARK")
	r.initDrive(mnt, uuidA, "")
	r.mounts.set(r.rootDisk(), r.arkDisk(120, "8:17", mnt, "/dev/sdb1", uuidA, "ext4"))
	if r.m.Name() == "" {
		t.Error("a worker needs a name")
	}
	if err := r.m.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Start returns after the first pass: the drive that was already plugged in is known.
	if d := r.only(); d.State != ark.StateReady {
		t.Errorf("drive after Start = %+v", d)
	}
	if err := r.m.Start(context.Background()); err == nil {
		t.Error("a second Start must be refused")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := r.m.Stop(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestDrivesReturnsCopies(t *testing.T) {
	r := newRig(t)
	mnt := r.dir("AI-ARK")
	r.mounts.set(r.rootDisk(), r.arkDisk(120, "8:17", mnt, "/dev/sdb1", uuidA, "ext4"))
	r.refresh()
	d := r.m.Drives()
	d[0].State = ark.StateReady
	d[0].Problems[0].Code = "changed"
	again := r.m.Drives()
	if again[0].State != ark.StateUninitialized || again[0].Problems[0].Code != doctor.ARK010 {
		t.Errorf("the detector's state was changed through a returned slice: %+v", again[0])
	}
	if _, ok := r.m.Drive(""); ok {
		t.Error("Drive(\"\") must find nothing")
	}
}
