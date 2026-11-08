package prep

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/execx"
	"github.com/marinoscar/local-ai/agent/internal/execx/fake"
)

// A computer as lsblk shows it: its own NVMe disk with /boot/efi and /, a second internal SATA disk
// holding /srv/localai, a swap disk, a USB disk the desktop opened, a blank USB disk, a removable card
// reader with no card, a USB disk with LVM on it, a loop device and an optical drive.
const machine = `{"blockdevices":[
 {"name":"nvme0n1","path":"/dev/nvme0n1","type":"disk","size":512110190592,"model":"Internal NVMe","serial":"N1","tran":"nvme","rm":false,"hotplug":false,"label":null,"fstype":null,"uuid":null,"mountpoints":[null],
  "children":[
   {"name":"nvme0n1p1","path":"/dev/nvme0n1p1","type":"part","size":536870912,"rm":false,"hotplug":false,"fstype":"vfat","label":null,"mountpoints":["/boot/efi"]},
   {"name":"nvme0n1p2","path":"/dev/nvme0n1p2","type":"part","size":511000000000,"rm":false,"hotplug":false,"fstype":"ext4","label":null,"mountpoints":["/"]}]},
 {"name":"sda","path":"/dev/sda","type":"disk","size":2000398934016,"model":"Internal SATA","serial":"S1","tran":"sata","rm":false,"hotplug":false,"mountpoints":[null],
  "children":[{"name":"sda1","path":"/dev/sda1","type":"part","size":2000397885440,"fstype":"ext4","mountpoints":["/srv/localai"]}]},
 {"name":"sdb","path":"/dev/sdb","type":"disk","size":1000204886016,"model":"Portable SSD","serial":"P1","tran":"usb","rm":false,"hotplug":true,"mountpoints":[null],
  "children":[{"name":"sdb1","path":"/dev/sdb1","type":"part","size":1000203837440,"fstype":"exfat","label":"USB","mountpoints":["/media/person/USB"]}]},
 {"name":"sdc","path":"/dev/sdc","type":"disk","size":"2000398934016","model":"Blank SSD","serial":"B1","tran":"usb","rm":"0","hotplug":"1","mountpoints":[null]},
 {"name":"sdd","path":"/dev/sdd","type":"disk","size":0,"model":"Card Reader","serial":"C1","tran":"usb","rm":true,"hotplug":true,"mountpoints":[null]},
 {"name":"sde","path":"/dev/sde","type":"disk","size":500107862016,"model":"LVM USB","serial":"L1","tran":"usb","rm":false,"hotplug":true,"mountpoints":[null],
  "children":[{"name":"sde1","path":"/dev/sde1","type":"part","size":500106813440,"fstype":"LVM2_member","mountpoints":[null],
    "children":[{"name":"vg-data","path":"/dev/mapper/vg-data","type":"lvm","size":500000000000,"mountpoints":[null]}]}]},
 {"name":"sdf","path":"/dev/sdf","type":"disk","size":8000000000,"model":"Swap Stick","serial":"W1","tran":"usb","rm":true,"hotplug":true,"mountpoints":[null],
  "children":[{"name":"sdf1","path":"/dev/sdf1","type":"part","size":7999000000,"fstype":"swap","mountpoints":["[SWAP]"]}]},
 {"name":"sdg","path":"/dev/sdg","type":"disk","size":64000000000,"model":"Old Card","serial":"G1","tran":"","rm":true,"hotplug":false,"mountpoint":null,
  "children":[{"name":"sdg1","path":"/dev/sdg1","type":"part","size":63999000000,"fstype":"vfat","mountpoint":null}]},
 {"name":"loop0","path":"/dev/loop0","type":"loop","size":4096,"rm":false,"hotplug":false,"mountpoints":["/snap/core/1"]},
 {"name":"sr0","path":"/dev/sr0","type":"rom","size":1073741312,"rm":true,"hotplug":true,"tran":"sata","mountpoints":[null]}
]}`

func mustParse(t *testing.T, raw string) []BlockDevice {
	t.Helper()
	devs, err := ParseLsblk([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	return devs
}

func TestParseLsblk(t *testing.T) {
	devs := mustParse(t, machine)
	if len(devs) != 10 {
		t.Fatalf("%d devices", len(devs))
	}
	sdc := devs[3]
	if sdc.Size != 2000398934016 || sdc.Removable || !sdc.Hotplug || sdc.Tran != "usb" || len(sdc.Mountpoints) != 0 {
		t.Errorf("string numbers and booleans: %+v", sdc)
	}
	if devs[0].Children[1].Mountpoints[0] != "/" || devs[0].Label != "" {
		t.Errorf("nested mount or null label: %+v", devs[0])
	}
	if got := devs[7]; !got.Removable || got.Mountpoints != nil {
		t.Errorf("old MOUNTPOINT column: %+v", got)
	}
	noPath := mustParse(t, `{"blockdevices":[{"name":"sdz","type":"disk","size":1}]}`)
	if noPath[0].Path != "/dev/sdz" {
		t.Errorf("path from name: %q", noPath[0].Path)
	}
	for _, bad := range []string{``, `{}`, `[]`, `{"blockdevices":[{"rm":"maybe"}]}`, `{"blockdevices":[{"size":"big"}]}`} {
		if _, err := ParseLsblk([]byte(bad)); err == nil {
			t.Errorf("%q parsed", bad)
		}
	}
}

func TestClassifyNeverOffersTheComputersOwnOrABusyDisk(t *testing.T) {
	devs := mustParse(t, machine)
	offered, refused := Classify(devs, DefaultSystemPaths)
	if len(offered) != 2 || offered[0].Path != "/dev/sdc" || offered[1].Path != "/dev/sdg" {
		t.Fatalf("offered %v", paths(offered))
	}
	want := map[string]Reason{
		"/dev/nvme0n1": ReasonSystem,  // the disk of / and /boot/efi
		"/dev/sda":     ReasonSystem,  // the disk of a Local AI data directory
		"/dev/sdb":     ReasonMounted, // a disk with a mounted partition
		"/dev/sdd":     ReasonNoMedia,
		"/dev/sde":     ReasonInUse, // LVM on it
		"/dev/sdf":     ReasonSystem,
	}
	got := map[string]Reason{}
	for _, r := range refused {
		got[r.Disk.Path] = r.Reason
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("refused %v, want %v", got, want)
	}
	for _, r := range refused {
		if r.Disk.Path == "/dev/nvme0n1" && r.Mount != "/boot/efi" {
			t.Errorf("mount named %q", r.Mount)
		}
	}
}

func TestClassifyTable(t *testing.T) {
	usb := func(mods ...func(*BlockDevice)) BlockDevice {
		d := BlockDevice{Name: "sdx", Path: "/dev/sdx", Type: "disk", Size: 1 << 40, Tran: "usb", Hotplug: true}
		for _, m := range mods {
			m(&d)
		}
		return d
	}
	part := func(mounts ...string) func(*BlockDevice) {
		return func(d *BlockDevice) {
			d.Children = append(d.Children, BlockDevice{Path: d.Path + "1", Type: "part", Mountpoints: mounts})
		}
	}
	cases := []struct {
		name   string
		dev    BlockDevice
		system []string
		want   Reason // "" = offered
	}{
		{"a blank USB disk is offered", usb(), DefaultSystemPaths, ""},
		{"a USB disk with an unmounted partition is offered", usb(part()), DefaultSystemPaths, ""},
		{"a removable disk without a bus is offered", usb(func(d *BlockDevice) { d.Tran, d.Hotplug, d.Removable = "", false, true }), DefaultSystemPaths, ""},
		{"the disk of / (even on USB)", usb(part("/")), DefaultSystemPaths, ReasonSystem},
		{"the disk of /boot", usb(part("/boot")), DefaultSystemPaths, ReasonSystem},
		{"a swap disk", usb(part(Swap)), DefaultSystemPaths, ReasonSystem},
		{"the disk of /var/lib/localai", usb(part("/var/lib/localai")), DefaultSystemPaths, ReasonSystem},
		{"a mount above a data directory (/srv)", usb(part("/srv")), DefaultSystemPaths, ReasonSystem},
		{"the target of a linked data directory", usb(part("/mnt/big")), append(append([]string(nil), DefaultSystemPaths...), "/mnt/big/localai"), ReasonSystem},
		{"the disk itself mounted", usb(func(d *BlockDevice) { d.Mountpoints = []string{"/media/p/X"} }), DefaultSystemPaths, ReasonMounted},
		{"a mounted partition", usb(part("/media/p/X")), DefaultSystemPaths, ReasonMounted},
		{"an AI-ARK drive in use", usb(part("/media/p/AI-ARK")), DefaultSystemPaths, ReasonMounted},
		{"a non-removable SATA disk", usb(func(d *BlockDevice) { d.Tran, d.Hotplug = "sata", false }), DefaultSystemPaths, ReasonInternal},
		{"a non-removable NVMe disk", usb(func(d *BlockDevice) { d.Tran, d.Hotplug, d.Path = "nvme", false, "/dev/nvme1n1" }), DefaultSystemPaths, ReasonInternal},
		{"LUKS on it", usb(func(d *BlockDevice) {
			d.Children = []BlockDevice{{Path: "/dev/sdx1", Type: "part", Children: []BlockDevice{{Path: "/dev/mapper/c", Type: "crypt"}}}}
		}), DefaultSystemPaths, ReasonInUse},
		{"a RAID member", usb(func(d *BlockDevice) { d.Children = []BlockDevice{{Path: "/dev/md0", Type: "raid1"}} }), DefaultSystemPaths, ReasonInUse},
		{"no media", usb(func(d *BlockDevice) { d.Size = 0 }), DefaultSystemPaths, ReasonNoMedia},
		{"a path that is not a plain node", usb(func(d *BlockDevice) { d.Path = "/dev/../sda" }), DefaultSystemPaths, ReasonBadPath},
		{"a path in a sub-directory", usb(func(d *BlockDevice) { d.Path = "/dev/mapper/x" }), DefaultSystemPaths, ReasonBadPath},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			offered, refused := Classify([]BlockDevice{tc.dev}, tc.system)
			if tc.want == "" {
				if len(offered) != 1 || len(refused) != 0 {
					t.Fatalf("offered %v refused %+v", paths(offered), refused)
				}
				return
			}
			if len(offered) != 0 || len(refused) != 1 || refused[0].Reason != tc.want {
				t.Fatalf("offered %v refused %+v, want %s", paths(offered), refused, tc.want)
			}
		})
	}
	// A partition or a loop device at the top level is not a disk: neither offered nor listed.
	o, r := Classify([]BlockDevice{{Path: "/dev/loop0", Type: "loop", Size: 1, Removable: true}, {Path: "/dev/sdz1", Type: "part", Size: 1, Tran: "usb"}}, DefaultSystemPaths)
	if len(o)+len(r) != 0 {
		t.Errorf("non-disks: %v %v", o, r)
	}
}

func TestSystemPathsFollowLinks(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "big")
	if err := os.Mkdir(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "localai")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	got := SystemPaths([]string{"/", link, filepath.Join(dir, "missing")}, filepath.EvalSymlinks)
	resolvedReal, _ := filepath.EvalSymlinks(real)
	if len(got) != 4 || got[3] != resolvedReal {
		t.Errorf("got %v", got)
	}
	if got := SystemPaths([]string{"/"}, nil); len(got) != 1 {
		t.Errorf("nil resolver: %v", got)
	}
}

func TestValidDevice(t *testing.T) {
	for _, ok := range []string{"/dev/sdb", "/dev/sdb1", "/dev/nvme1n1p1", "/dev/mmcblk0p1"} {
		if !ValidDevice(ok) {
			t.Errorf("%q refused", ok)
		}
	}
	for _, bad := range []string{"", "/dev/", "sdb", "/dev/sdb ", "/dev/../sda", "/dev/mapper/x", "/dev/sd*", "/dev/sdb;rm", "-rf", "/tmp/x", "/dev/-x"} {
		if ValidDevice(bad) {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestSameDisk(t *testing.T) {
	a := BlockDevice{Path: "/dev/sdc", Type: "disk", Size: 10, Model: "M", Serial: "S"}
	b := a
	if !SameDisk(a, b) {
		t.Fatal("same")
	}
	for _, mod := range []func(*BlockDevice){
		func(d *BlockDevice) { d.Size = 11 }, func(d *BlockDevice) { d.Serial = "T" },
		func(d *BlockDevice) { d.Model = "N" }, func(d *BlockDevice) { d.Path = "/dev/sdd" }, func(d *BlockDevice) { d.Type = "part" },
	} {
		c := a
		mod(&c)
		if SameDisk(a, c) {
			t.Errorf("%+v is the same", c)
		}
	}
}

func TestCheckDrive(t *testing.T) {
	ark := `{"blockdevices":[
 {"name":"nvme0n1","path":"/dev/nvme0n1","type":"disk","size":1,"tran":"nvme","children":[
   {"name":"nvme0n1p2","path":"/dev/nvme0n1p2","type":"part","fstype":"ext4","label":"AI-ARK","mountpoints":["/"]}]},
 {"name":"sdb","path":"/dev/sdb","type":"disk","size":1,"tran":"usb","children":[
   {"name":"sdb1","path":"/dev/sdb1","type":"part","fstype":"ext4","label":"AI-ARK","mountpoints":["/media/root/AI-ARK"]}]},
 {"name":"sdc","path":"/dev/sdc","type":"disk","size":1,"tran":"usb","children":[
   {"name":"sdc1","path":"/dev/sdc1","type":"part","fstype":"exfat","label":"AI-ARK","mountpoints":[null]}]},
 {"name":"sdd","path":"/dev/sdd","type":"disk","size":1,"tran":"usb","children":[
   {"name":"sdd1","path":"/dev/sdd1","type":"part","fstype":"ext4","label":"AI-ARK","mountpoints":[null]},
   {"name":"sdd2","path":"/dev/sdd2","type":"part","fstype":"vfat","mountpoints":["/media/root/OTHER"]}]}]}`
	devs := mustParse(t, ark)
	cases := []struct {
		dev, own string
		want     DriveProblem
	}{
		{"/dev/sdb1", "/media/root/AI-ARK", DriveOK},
		{"/dev/sdb1", "", DriveOtherMounts}, // still mounted after the eject
		{"/dev/sdb1", "/media/root/ELSE", DriveOtherMounts},
		{"/dev/nvme0n1p2", "/", DriveSystem}, // an ext4 AI-ARK root filesystem is still the system
		{"/dev/sdc1", "", DriveNotArk},
		{"/dev/sdd1", "", DriveOtherMounts},
		{"/dev/sdz1", "", DriveUnknown},
		{"/dev/../sdb1", "", DriveSystem},
	}
	for _, tc := range cases {
		if got := CheckDrive(devs, tc.dev, tc.own, DefaultSystemPaths); got != tc.want {
			t.Errorf("%s (%q): %q, want %q", tc.dev, tc.own, got, tc.want)
		}
	}
}

func TestCheckProgressAndResult(t *testing.T) {
	cases := []struct {
		line string
		ok   bool
		want CheckProgress
	}{
		{"1 250 1000 /dev/sdb1", true, CheckProgress{1, 250, 1000}},
		{"5 7 7 /dev/sdb1", true, CheckProgress{5, 7, 7}},
		{"Pass 1: Checking inodes, blocks, and sizes", false, CheckProgress{}},
		{"AI-ARK: 11/122101760 files (0.0% non-contiguous)", false, CheckProgress{}},
		{"6 1 2 /dev/sdb1", false, CheckProgress{}},
		{"1 1 2 sdb1", false, CheckProgress{}},
		{"1 -1 2 /dev/sdb1", false, CheckProgress{}},
	}
	for _, tc := range cases {
		got, ok := ParseCheckProgress(tc.line)
		if ok != tc.ok || got != tc.want {
			t.Errorf("%q: %+v %v", tc.line, got, ok)
		}
	}
	if p := (CheckProgress{Pass: 1, Done: 1, Total: 3}); p.Percent() != 33 {
		t.Errorf("percent %d", p.Percent())
	}
	if (CheckProgress{}).Percent() != 0 || (CheckProgress{Done: 5, Total: 2}).Percent() != 100 {
		t.Error("percent bounds")
	}
	results := map[int]CheckOutcome{0: CheckClean, 1: CheckFixed, 2: CheckFixed, 3: CheckFixed, 4: CheckErrorsLeft, 5: CheckErrorsLeft,
		8: CheckFailed, 12: CheckFailed, 16: CheckFailed, 32: CheckFailed, 128: CheckFailed, -1: CheckFailed}
	for code, want := range results {
		if got := CheckResult(code); got != want {
			t.Errorf("exit %d: %s, want %s", code, got, want)
		}
	}
}

// The exact argv of every program: what the person reads in the spec and the runbook is what runs.
func TestExecToolsArgv(t *testing.T) {
	r := fake.New()
	ok := execx.Result{}
	look := func(name string) (string, error) { return "/usr/sbin/" + name, nil }
	r.Expect(fake.Key("/usr/sbin/lsblk", "--json", "--bytes", "--output", LsblkColumns), execx.Result{Stdout: []byte(machine)}, nil)
	r.Expect(fake.Key("/usr/sbin/wipefs", "--all", "/dev/sdc"), ok, nil)
	r.Expect(fake.Key("/usr/sbin/parted", "--script", "/dev/sdc", "mklabel", "gpt", "mkpart", "AI-ARK", "ext4", "1MiB", "100%"), ok, nil)
	r.Expect(fake.Key("/usr/sbin/udevadm", "settle", "--timeout=30"), ok, nil)
	r.Expect(fake.Key("/usr/sbin/mkfs.ext4", "-L", "AI-ARK", "-m", "0", "-E", "lazy_itable_init=0,lazy_journal_init=0", "/dev/sdc1"), ok, nil)
	r.Expect(fake.Key("/usr/sbin/tune2fs", "-U", "random", "/dev/sdc1"), ok, nil)
	r.Expect(fake.Key("/usr/sbin/udisksctl", "mount", "--no-user-interaction", "-b", "/dev/sdc1"), ok, nil)
	r.Expect(fake.Key("/usr/sbin/udisksctl", "unmount", "--no-user-interaction", "-b", "/dev/sdc1"), ok, nil)
	tools := ExecTools{Run: r, Lookup: look}
	ctx := context.Background()
	if devs, err := tools.ListBlock(ctx); err != nil || len(devs) != 10 {
		t.Fatalf("list: %v", err)
	}
	for name, err := range map[string]error{
		"wipe": tools.Wipe(ctx, "/dev/sdc"), "partition": tools.Partition(ctx, "/dev/sdc"), "settle": tools.Settle(ctx),
		"mkfs": tools.MakeFS(ctx, "/dev/sdc1"), "uuid": tools.NewUUID(ctx, "/dev/sdc1"),
		"mount": tools.Mount(ctx, "/dev/sdc1"), "unmount": tools.Unmount(ctx, "/dev/sdc1"),
	} {
		if err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	for _, c := range r.Calls() {
		if c.Stdin == nil || c.Timeout <= 0 || c.MaxOut <= 0 {
			t.Errorf("%s: no empty stdin or no bounds", c.Path)
		}
	}
}

func TestExecToolsRefuseABadDeviceBeforeRunningAnything(t *testing.T) {
	r := fake.New()
	s := fake.NewStreamer()
	tools := ExecTools{Run: r, Stream: s, Lookup: func(n string) (string, error) { return "/usr/sbin/" + n, nil }}
	ctx := context.Background()
	for _, dev := range []string{"", "/dev/sd*", "/dev/../sda", "sdb", "/dev/sdb /dev/sda"} {
		errs := []error{tools.Wipe(ctx, dev), tools.Partition(ctx, dev), tools.MakeFS(ctx, dev), tools.NewUUID(ctx, dev), tools.Mount(ctx, dev), tools.Unmount(ctx, dev)}
		_, cerr := tools.Check(ctx, dev, nil)
		errs = append(errs, cerr)
		for i, err := range errs {
			if !errors.Is(err, ErrBadDevice) {
				t.Errorf("%q tool %d: %v", dev, i, err)
			}
		}
	}
	if len(r.Calls()) != 0 || len(s.Calls()) != 0 {
		t.Errorf("ran %v %v", r.Calls(), s.Calls())
	}
}

func TestExecToolsErrors(t *testing.T) {
	ctx := context.Background()
	missing := ExecTools{Run: fake.New(), Lookup: func(string) (string, error) { return "", execx.ErrNotFound }}
	if err := missing.Wipe(ctx, "/dev/sdc"); !errors.Is(err, ErrMissing) {
		t.Errorf("missing: %v", err)
	}
	r := fake.New()
	r.Expect(fake.Key("/sbin/wipefs", "--all", "/dev/sdc"), execx.Result{ExitCode: 1, Stderr: []byte("\nwipefs: error: /dev/sdc: probing initialization failed: Device or resource busy\n")}, nil)
	r.Expect(fake.Key("/sbin/udisksctl", "mount", "--no-user-interaction", "-b", "/dev/sdc1"), execx.Result{ExitCode: 1, Stderr: []byte("Error mounting /dev/sdc1: GDBus.Error:org.freedesktop.UDisks2.Error.AlreadyMounted: Device is already mounted")}, nil)
	r.Expect(fake.Key("/sbin/udisksctl", "unmount", "--no-user-interaction", "-b", "/dev/sdc1"), execx.Result{ExitCode: 1, Stderr: []byte("Error unmounting: target is busy")}, nil)
	tools := ExecTools{Run: r, Lookup: func(n string) (string, error) { return "/sbin/" + n, nil }}
	err := tools.Wipe(ctx, "/dev/sdc")
	var te *ToolError
	if !errors.As(err, &te) || te.Tool != "wipefs" || te.ExitCode != 1 || !strings.Contains(te.Reason, "Device or resource busy") {
		t.Errorf("wipe: %#v", err)
	}
	if err := tools.Mount(ctx, "/dev/sdc1"); err != nil {
		t.Errorf("already mounted is fine: %v", err)
	}
	if err := tools.Unmount(ctx, "/dev/sdc1"); !errors.As(err, &te) || te.Reason != "Error unmounting: target is busy" {
		t.Errorf("unmount: %v", err)
	}
}

func TestExecToolsCheckStreamsProgress(t *testing.T) {
	s := fake.NewStreamer(fake.Script{
		Chunks: [][]byte{
			[]byte("Pass 1: Checking inodes, blocks, and sizes\n1 10 100 /dev/sdb1\n"),
			[]byte("1 100 100 /dev/sdb1\n2 5 10 /dev/sdb1\nAI-ARK: ***** FILE SYSTEM WAS MODIFIED *****\n"),
		},
		ExitCode: 1,
	})
	tools := ExecTools{Stream: s, Lookup: func(n string) (string, error) { return "/sbin/" + n, nil }}
	var got []CheckProgress
	code, err := tools.Check(context.Background(), "/dev/sdb1", func(p CheckProgress) { got = append(got, p) })
	if err != nil || code != 1 || CheckResult(code) != CheckFixed {
		t.Fatalf("code %d err %v", code, err)
	}
	if len(got) != 3 || got[2] != (CheckProgress{2, 5, 10}) {
		t.Errorf("progress %+v", got)
	}
	calls := s.Calls()
	if len(calls) != 1 || calls[0].Path != "/sbin/fsck.ext4" || strings.Join(calls[0].Args, " ") != "-f -y -C 1 /dev/sdb1" {
		t.Errorf("argv %+v", calls)
	}

	s = fake.NewStreamer(fake.Script{StartErr: errors.New("no such file")})
	tools.Stream = s
	if _, err := tools.Check(context.Background(), "/dev/sdb1", nil); err == nil {
		t.Error("a check that cannot start is an error")
	}
}

func paths(ds []BlockDevice) []string {
	var out []string
	for _, d := range ds {
		out = append(out, d.Path)
	}
	return out
}
