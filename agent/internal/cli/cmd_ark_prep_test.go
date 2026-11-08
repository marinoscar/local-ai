package cli

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/ark/prep"
	"github.com/marinoscar/local-ai/agent/internal/cli/fake"
)

// Tests of `aictl ark format|repair|new-id` (aictl.md section 4.10, ark-sync.md section 4.17, issue #434).
// No disk is touched: prep.Tools is a fake that records every call and answers from scripted lsblk
// snapshots, and the agent is the usual fake.

// fakeTools is a scripted prep.Tools.
type fakeTools struct {
	mu    sync.Mutex
	lists []string // lsblk JSON, one per ListBlock call; the last one repeats
	nList int
	calls []string
	fail  map[string]error // by method name
	code  int              // fsck exit code
	prog  []prep.CheckProgress
	// mounted is what Mount was called with (the agent fake lists those drives).
	mounted map[string]bool
}

func (f *fakeTools) record(s string) {
	f.mu.Lock()
	f.calls = append(f.calls, s)
	f.mu.Unlock()
}

func (f *fakeTools) err(name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.fail[name]
}

func (f *fakeTools) ListBlock(context.Context) ([]prep.BlockDevice, error) {
	f.record("list")
	if err := f.err("list"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	i := f.nList
	if i >= len(f.lists) {
		i = len(f.lists) - 1
	}
	f.nList++
	raw := f.lists[i]
	f.mu.Unlock()
	return prep.ParseLsblk([]byte(raw))
}

func (f *fakeTools) do(name, dev string) error {
	f.record(name + " " + dev)
	if !prep.ValidDevice(dev) {
		return &prep.ToolError{Tool: name, ExitCode: -1, Err: prep.ErrBadDevice}
	}
	return f.err(name)
}

func (f *fakeTools) Wipe(_ context.Context, d string) error      { return f.do("wipe", d) }
func (f *fakeTools) Partition(_ context.Context, d string) error { return f.do("partition", d) }
func (f *fakeTools) MakeFS(_ context.Context, d string) error    { return f.do("mkfs", d) }
func (f *fakeTools) NewUUID(_ context.Context, d string) error   { return f.do("newuuid", d) }
func (f *fakeTools) Unmount(_ context.Context, d string) error   { return f.do("unmount", d) }
func (f *fakeTools) Settle(context.Context) error {
	f.record("settle")
	return f.err("settle")
}

func (f *fakeTools) Mount(_ context.Context, d string) error {
	if err := f.do("mount", d); err != nil {
		return err
	}
	f.mu.Lock()
	if f.mounted == nil {
		f.mounted = map[string]bool{}
	}
	f.mounted[d] = true
	f.mu.Unlock()
	return nil
}

func (f *fakeTools) Check(_ context.Context, d string, progress func(prep.CheckProgress)) (int, error) {
	if err := f.do("check", d); err != nil {
		return -1, err
	}
	for _, p := range f.prog {
		progress(p)
	}
	return f.code, nil
}

func (f *fakeTools) isMounted(d string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.mounted[d]
}

func (f *fakeTools) list() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

// changed lists the calls that change a disk; a refusal must leave it empty.
func (f *fakeTools) changed() []string {
	var out []string
	for _, c := range f.list() {
		if c != "list" && c != "settle" {
			out = append(out, c)
		}
	}
	return out
}

func useTools(t *testing.T, f *fakeTools) {
	t.Helper()
	oldT, oldS, oldP, oldW := arkPrepTools, arkPrepSystemPaths, arkPrepPoll, arkPrepAgentWait
	t.Cleanup(func() { arkPrepTools, arkPrepSystemPaths, arkPrepPoll, arkPrepAgentWait = oldT, oldS, oldP, oldW })
	arkPrepTools = func() prep.Tools { return f }
	arkPrepSystemPaths = func() []string { return prep.DefaultSystemPaths }
	arkPrepPoll = time.Millisecond
	arkPrepAgentWait = 300 * time.Millisecond
}

// lsblk builds an lsblk document from top-level disks.
func lsblk(disks ...string) string { return `{"blockdevices":[` + strings.Join(disks, ",") + `]}` }

const (
	// The computer's own disk, a second internal disk with Local AI data, a swap stick, and a USB disk
	// the desktop opened: none of them may ever be offered.
	sysNVMe = `{"name":"nvme0n1","path":"/dev/nvme0n1","type":"disk","size":512110190592,"model":"Internal NVMe","serial":"N1","tran":"nvme","rm":false,"hotplug":false,"mountpoints":[null],"children":[
 {"name":"nvme0n1p1","path":"/dev/nvme0n1p1","type":"part","fstype":"vfat","mountpoints":["/boot/efi"]},
 {"name":"nvme0n1p2","path":"/dev/nvme0n1p2","type":"part","fstype":"ext4","mountpoints":["/"]}]}`
	dataSATA = `{"name":"sda","path":"/dev/sda","type":"disk","size":2000398934016,"model":"Internal SATA","serial":"S1","tran":"sata","rm":false,"hotplug":false,"children":[
 {"name":"sda1","path":"/dev/sda1","type":"part","fstype":"ext4","mountpoints":["/srv/localai"]}]}`
	swapUSB = `{"name":"sdf","path":"/dev/sdf","type":"disk","size":8000000000,"model":"Swap Stick","serial":"W1","tran":"usb","rm":true,"hotplug":true,"children":[
 {"name":"sdf1","path":"/dev/sdf1","type":"part","fstype":"swap","mountpoints":["[SWAP]"]}]}`
	openUSB = `{"name":"sdb","path":"/dev/sdb","type":"disk","size":1000204886016,"model":"Portable SSD","serial":"P1","tran":"usb","rm":false,"hotplug":true,"children":[
 {"name":"sdb1","path":"/dev/sdb1","type":"part","fstype":"exfat","label":"PHOTOS","mountpoints":["/media/person/PHOTOS"]}]}`
	// The new disk: one old exFAT partition, not mounted.
	newUSB = `{"name":"sdc","path":"/dev/sdc","type":"disk","size":2000398934016,"model":"Blank SSD","serial":"B1","tran":"usb","rm":false,"hotplug":true,"children":[
 {"name":"sdc1","path":"/dev/sdc1","type":"part","fstype":"exfat","label":"OLD","mountpoints":[null]}]}`
	newUSBOpened = `{"name":"sdc","path":"/dev/sdc","type":"disk","size":2000398934016,"model":"Blank SSD","serial":"B1","tran":"usb","rm":false,"hotplug":true,"children":[
 {"name":"sdc1","path":"/dev/sdc1","type":"part","fstype":"exfat","label":"OLD","mountpoints":["/media/person/OLD"]}]}`
	newUSBPartitioned = `{"name":"sdc","path":"/dev/sdc","type":"disk","size":2000398934016,"model":"Blank SSD","serial":"B1","tran":"usb","rm":false,"hotplug":true,"children":[
 {"name":"sdc1","path":"/dev/sdc1","type":"part","fstype":null,"mountpoints":[null]}]}`
	swappedUSB = `{"name":"sdc","path":"/dev/sdc","type":"disk","size":64000000000,"model":"Other Stick","serial":"X9","tran":"usb","rm":true,"hotplug":true}`
)

// formatRig is an agent that verifies the PIN, lists the new drive once it is mounted and sets it up.
func formatRig(t *testing.T, f *fakeTools) *mgRig {
	t.Helper()
	useTools(t, f)
	r := newMgRig(t, fake.Options{})
	r.agent().Handle(http.MethodPost, "/pin/verify", func(w http.ResponseWriter, req *http.Request) {
		var body struct {
			Pin string `json:"pin"`
		}
		json.NewDecoder(req.Body).Decode(&body)
		if body.Pin == testPIN {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"grant":"`+testGrant+`","expires_at":"2026-11-02T15:09:05Z"}`)
			return
		}
		fake.WriteEnvelope(w, http.StatusUnauthorized, "LAI-SEC-003", "That PIN is not right.", "Try again.", map[string]any{"remaining_attempts": 3})
	})
	r.agent().Handle(http.MethodGet, "/ark/drives", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if f.isMounted("/dev/sdc1") {
			io.WriteString(w, drivesBody(`{"root_id":"ark-1234abcd","device":"/dev/sdc1","mount":"/media/root/AI-ARK","fstype":"ext4","label":"AI-ARK","size_bytes":2000398934016,"state":"uninitialized"}`))
			return
		}
		io.WriteString(w, drivesBody())
	})
	r.agent().Respond(http.MethodPost, "/ark/drives/ark-1234abcd/init", 200, `{"root_id":"ark-1234abcd","state":"ready","mount":"/media/root/AI-ARK"}`)
	return r
}

func newFormatTools() *fakeTools {
	machine := lsblk(sysNVMe, dataSATA, swapUSB, openUSB, newUSB)
	return &fakeTools{lists: []string{machine, machine, lsblk(sysNVMe, dataSATA, swapUSB, openUSB, newUSBPartitioned)}}
}

func TestArkFormatErasesOnlyTheChosenDiskAfterThePINAndERASE(t *testing.T) {
	f := newFormatTools()
	r := formatRig(t, f)
	got := r.run(mgArgs("ark", "format").withTerm("1", testPIN, "ERASE"))
	if got.code != 0 {
		t.Fatalf("%+v\n%v", got, f.list())
	}
	want := []string{"list", "list", "wipe /dev/sdc1", "wipe /dev/sdc", "partition /dev/sdc", "settle", "list",
		"wipe /dev/sdc1", "mkfs /dev/sdc1", "settle", "mount /dev/sdc1"}
	if strings.Join(f.list(), "|") != strings.Join(want, "|") {
		t.Errorf("calls\n got %q\nwant %q", f.list(), want)
	}
	if posts(r, "/init") != 1 {
		t.Errorf("init not called: %v", r.requestLines())
	}
	// The numbered list holds the new disk only; every other disk is under "Not offered" with its reason.
	numbered := 0
	for _, l := range strings.Split(got.out, "\n") {
		if strings.HasPrefix(l, "  1  ") {
			numbered++
			if !strings.Contains(l, "/dev/sdc  Blank SSD") || !strings.Contains(l, "named OLD") {
				t.Errorf("offered line %q", l)
			}
		}
		if strings.HasPrefix(l, "  2  ") {
			t.Errorf("a second disk was offered: %q", l)
		}
	}
	for _, want := range []string{
		"/dev/nvme0n1  Internal NVMe: this computer's own disk (it holds /boot/efi)",
		"/dev/sda  Internal SATA: this computer's own disk (it holds /srv/localai)",
		"/dev/sdf  Swap Stick: this computer uses it as swap",
		"/dev/sdb  Portable SSD: open at /media/person/PHOTOS; eject it in the file manager first",
		"The new AI-ARK drive is ready (ark-1234abcd, mounted at /media/root/AI-ARK)",
	} {
		if !strings.Contains(got.out, want) {
			t.Errorf("missing %q in\n%s", want, got.out)
		}
	}
	if numbered != 1 || !strings.Contains(got.errOut, "Type ERASE to erase it") || !strings.Contains(got.errOut, "Admin PIN") {
		t.Errorf("stderr %s", got.errOut)
	}
	secretFree(t, testPIN, got.out, got.errOut)
}

func TestArkFormatSpanish(t *testing.T) {
	f := newFormatTools()
	r := formatRig(t, f)
	got := r.run(mgArgs("--lang", "es", "ark", "format").withTerm("1", testPIN, "ERASE"))
	if got.code != 0 || !strings.Contains(got.out, "La nueva unidad AI-ARK está lista") || !strings.Contains(got.errOut, "Escriba ERASE para borrarlo") ||
		!strings.Contains(got.out, "el disco de esta computadora") {
		t.Fatalf("%+v", got)
	}
}

// Every answer that is not exactly right, and Ctrl-C at any question, change nothing.
func TestArkFormatAnswersThatDoNothing(t *testing.T) {
	ctrlC := fake.Input{Err: context.Canceled}
	typed := func(in ...fake.Input) *fake.Terminal { return &fake.Terminal{TTY: true, Script: in} }
	line := func(s string) fake.Input { return fake.Input{Text: s} }
	cases := []struct {
		name string
		term *fake.Terminal
		code int
		pins int
	}{
		{"empty number", typed(line("")), 0, 0},
		{"number zero", typed(line("0")), 0, 0},
		{"number past the list", typed(line("2")), 0, 0},
		{"a device typed instead of a number", typed(line("/dev/sda")), 0, 0},
		{"a word", typed(line("sdc")), 0, 0},
		{"end of input at the number", typed(), 0, 0},
		{"Ctrl-C at the number", typed(ctrlC), ExitInterrupted, 0},
		{"empty PIN", typed(line("1"), line("")), ExitDenied, 0},
		{"three wrong PINs", typed(line("1"), line("1111"), line("2222"), line("3333")), ExitDenied, 3},
		{"Ctrl-C at the PIN", typed(line("1"), ctrlC), ExitInterrupted, 0},
		{"empty word", typed(line("1"), line(testPIN), line("")), 0, 1},
		{"lower-case word", typed(line("1"), line(testPIN), line("erase")), 0, 1},
		{"yes instead of the word", typed(line("1"), line(testPIN), line("yes")), 0, 1},
		{"Spanish word", typed(line("1"), line(testPIN), line("BORRAR")), 0, 1},
		{"the word and more", typed(line("1"), line(testPIN), line("ERASE it")), 0, 1},
		{"end of input at the word", typed(line("1"), line(testPIN)), 0, 1},
		{"Ctrl-C at the word", typed(line("1"), line(testPIN), ctrlC), ExitInterrupted, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFormatTools()
			r := formatRig(t, f)
			o := mgArgs("ark", "format")
			o.term = tc.term
			got := r.run(o)
			if got.code != tc.code {
				t.Fatalf("exit %d, want %d: %+v", got.code, tc.code, got)
			}
			if ch := f.changed(); len(ch) != 0 {
				t.Fatalf("the disk was touched: %v", ch)
			}
			if n := r.agent().Count("/pin/verify"); n != tc.pins {
				t.Errorf("pins sent %d, want %d", n, tc.pins)
			}
			if posts(r, "/init") != 0 {
				t.Error("init was called")
			}
		})
	}
}

// --yes answers nothing, --json is refused, and only root may run it: all before any disk is listed.
func TestArkFormatRefusedBeforeAnything(t *testing.T) {
	cases := []struct {
		name string
		run  mgRun
		code int
	}{
		{"--yes without a terminal", mgArgs("--yes", "ark", "format"), ExitUsage},
		{"no terminal", mgArgs("ark", "format"), ExitUsage},
		{"--json with a terminal", mgArgs("--json", "ark", "format").withTerm("1", testPIN, "ERASE"), ExitUsage},
		{"not root", func() mgRun { o := mgArgs("ark", "format").withTerm("1", testPIN, "ERASE"); o.euid = 1000; return o }(), ExitDenied},
		{"an argument", mgArgs("ark", "format", "/dev/sdc").withTerm("1", testPIN, "ERASE"), ExitUsage},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFormatTools()
			r := formatRig(t, f)
			got := r.run(tc.run)
			if got.code != tc.code || len(f.list()) != 0 || r.agent().Count("/pin/verify") != 0 {
				t.Fatalf("%+v calls %v", got, f.list())
			}
		})
	}
	// --yes with a terminal still asks every question: the script ends at the number, nothing happens.
	f := newFormatTools()
	r := formatRig(t, f)
	got := r.run(mgArgs("--yes", "ark", "format").withTerm())
	if got.code != 0 || len(f.changed()) != 0 || !strings.Contains(got.out, "Nothing was changed.") {
		t.Fatalf("%+v %v", got, f.list())
	}
}

func TestArkFormatNoDiskAndAChangedDisk(t *testing.T) {
	f := &fakeTools{lists: []string{lsblk(sysNVMe, dataSATA, openUSB)}}
	r := formatRig(t, f)
	got := r.run(mgArgs("ark", "format").withTerm("1", testPIN, "ERASE"))
	if got.code != ExitNotFound || !strings.Contains(got.errOut, "LAI-ARK-016") || len(f.changed()) != 0 {
		t.Fatalf("no disk: %+v", got)
	}

	for name, second := range map[string]string{"opened": newUSBOpened, "swapped": swappedUSB, "gone": ""} {
		disks := []string{sysNVMe}
		if second != "" {
			disks = append(disks, second)
		}
		f := &fakeTools{lists: []string{lsblk(sysNVMe, newUSB), lsblk(disks...)}}
		r := formatRig(t, f)
		got := r.run(mgArgs("ark", "format").withTerm("1", testPIN, "ERASE"))
		if got.code != ExitRefused || !strings.Contains(got.errOut, "nothing was erased") || len(f.changed()) != 0 {
			t.Errorf("%s: %+v %v", name, got, f.list())
		}
	}
}

func TestArkFormatToolFailures(t *testing.T) {
	for _, tc := range []struct {
		step, want string
		missing    bool
	}{
		{"mkfs", "partly erased", false},
		{"wipe", "partly erased", false},
		{"mount", "is not mounted", false},
		{"partition", "kit install-debs --packages parted", true},
	} {
		f := newFormatTools()
		tool := map[string]string{"mkfs": "mkfs.ext4", "wipe": "wipefs", "mount": "udisksctl", "partition": "parted"}[tc.step]
		var err error = &prep.ToolError{Tool: tool, ExitCode: 1, Reason: "something broke"}
		if tc.missing {
			err = &prep.ToolError{Tool: tool, ExitCode: -1, Err: prep.ErrMissing}
		}
		f.fail = map[string]error{tc.step: err}
		r := formatRig(t, f)
		got := r.run(mgArgs("ark", "format").withTerm("1", testPIN, "ERASE"))
		if got.code != ExitFailed || !strings.Contains(got.errOut, "LAI-ARK-017") || !strings.Contains(got.errOut, tc.want) || posts(r, "/init") != 0 {
			t.Errorf("%s: %+v", tc.step, got)
		}
	}
	// lsblk itself failing offers nothing and touches nothing.
	f := newFormatTools()
	f.fail = map[string]error{"list": &prep.ToolError{Tool: "lsblk", ExitCode: 1, Reason: "no"}}
	r := formatRig(t, f)
	if got := r.run(mgArgs("ark", "format").withTerm("1", testPIN, "ERASE")); got.code != ExitFailed || len(f.changed()) != 0 {
		t.Errorf("lsblk: %+v", got)
	}
}

// ---------------------------------------------------------------------------------------------------
// repair

const (
	arkUSBMounted = `{"name":"sdb","path":"/dev/sdb","type":"disk","size":2000398934016,"model":"Ark SSD","serial":"A1","tran":"usb","hotplug":true,"children":[
 {"name":"sdb1","path":"/dev/sdb1","type":"part","fstype":"ext4","label":"AI-ARK","mountpoints":["/media/root/AI-ARK"]}]}`
	arkUSBUnmounted = `{"name":"sdb","path":"/dev/sdb","type":"disk","size":2000398934016,"model":"Ark SSD","serial":"A1","tran":"usb","hotplug":true,"children":[
 {"name":"sdb1","path":"/dev/sdb1","type":"part","fstype":"ext4","label":"AI-ARK","mountpoints":[null]}]}`
	uncleanDrive = `{"root_id":"ark-0a0a0a0a","device":"/dev/sdb1","mount":"/media/root/AI-ARK","fstype":"ext4","label":"AI-ARK","size_bytes":2000398934016,"state":"unclean","problems":[{"code":"LAI-ARK-004"}]}`
)

// repairRig: the drive leaves the list once ejected and comes back once Mount was called.
func repairRig(t *testing.T, f *fakeTools, listed string, onEject ...func()) *mgRig {
	t.Helper()
	useTools(t, f)
	r := newMgRig(t, fake.Options{})
	var mu sync.Mutex
	ejected := false
	r.agent().Handle(http.MethodPost, "/ark/drives/ark-0a0a0a0a/eject", func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		ejected = true
		mu.Unlock()
		for _, h := range onEject {
			h()
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		io.WriteString(w, `{"state":"ejecting"}`)
	})
	r.agent().Handle(http.MethodGet, "/ark/drives", func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		gone := ejected
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case f.isMounted("/dev/sdb1"):
			io.WriteString(w, drivesBody(`{"root_id":"ark-0a0a0a0a","device":"/dev/sdb1","mount":"/media/root/AI-ARK","fstype":"ext4","label":"AI-ARK","state":"ready"}`))
		case gone:
			io.WriteString(w, drivesBody())
		default:
			io.WriteString(w, drivesBody(listed))
		}
	})
	return r
}

func newRepairTools(code int) *fakeTools {
	return &fakeTools{lists: []string{lsblk(sysNVMe, arkUSBMounted), lsblk(sysNVMe, arkUSBUnmounted)}, code: code,
		prog: []prep.CheckProgress{cp(1, 0, 100), cp(1, 5, 100), cp(1, 50, 100), cp(1, 100, 100), cp(2, 10, 10), cp(5, 3, 3)}}
}

func cp(pass int, done, total int64) prep.CheckProgress {
	return prep.CheckProgress{Pass: pass, Done: done, Total: total}
}

func TestArkRepair(t *testing.T) {
	for _, tc := range []struct {
		name string
		code int
		exit int
		want string
	}{
		{"clean", 0, 0, "No errors were found. AI-ARK is mounted again at /media/root/AI-ARK."},
		{"fixed", 1, 0, "Errors were found and fixed."},
		{"errors left", 4, ExitFailed, "LAI-ARK-018"},
		{"check failed", 8, ExitFailed, "LAI-ARK-017"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newRepairTools(tc.code)
			r := repairRig(t, f, uncleanDrive)
			got := r.run(mgArgs("ark", "repair", "ark-0a0a0a0a", "--yes"))
			if got.code != tc.exit || !strings.Contains(got.out+got.errOut, tc.want) {
				t.Fatalf("%+v", got)
			}
			want := []string{"list", "list", "check /dev/sdb1", "mount /dev/sdb1"}
			if strings.Join(f.list(), "|") != strings.Join(want, "|") {
				t.Errorf("calls %q", f.list())
			}
			if b := r.lastBody(http.MethodPost, "/ark/drives/ark-0a0a0a0a/eject"); b != `{"keep_powered":true}` {
				t.Errorf("eject body %q", b)
			}
			// One line per pass and tenth, not per fsck line.
			if n := strings.Count(got.errOut, "Checking step"); n != 5 || !strings.Contains(got.errOut, "Checking step 2 of 5 (folders): 100%") {
				t.Errorf("progress (%d):\n%s", n, got.errOut)
			}
		})
	}
	t.Run("json", func(t *testing.T) {
		f := newRepairTools(1)
		r := repairRig(t, f, uncleanDrive)
		got := r.run(mgArgs("--json", "ark", "repair", "ark-0a0a0a0a", "--yes"))
		var doc map[string]any
		if got.code != 0 || json.Unmarshal([]byte(got.out), &doc) != nil || doc["result"] != "fixed" || doc["mount"] != "/media/root/AI-ARK" {
			t.Fatalf("%+v", got)
		}
	})
}

func TestArkRepairRefusals(t *testing.T) {
	copyDrive := `{"root_id":"ark-0a0a0a0a","device":"/dev/sdb1","mount":"/media/root/AI-ARK","fstype":"ext4","label":"AI-ARK","state":"unusable","problems":[{"code":"LAI-ARK-006"}]}`
	badDrive := `{"root_id":null,"device":"/dev/sdb1","mount":"/media/root/AI-ARK","fstype":"exfat","label":"AI-ARK","state":"unusable"}`
	rootDrive := `{"root_id":"ark-0a0a0a0a","device":"/dev/nvme0n1p2","mount":"/","fstype":"ext4","label":"AI-ARK","state":"ready"}`
	nonRoot := mgArgs("ark", "repair", "ark-0a0a0a0a", "--yes")
	nonRoot.euid = 1000
	cases := []struct {
		name   string
		listed string
		run    mgRun
		code   int
		want   string
	}{
		{"a copy with LAI-ARK-006", copyDrive, mgArgs("ark", "repair", "ark-0a0a0a0a", "--yes"), ExitRefused, "ark new-id ark-0a0a0a0a"},
		{"not an ext4 AI-ARK drive", badDrive, mgArgs("ark", "repair", "/dev/sdb1", "--yes"), ExitRefused, "cannot be checked"},
		{"the system disk", rootDrive, mgArgs("ark", "repair", "ark-0a0a0a0a", "--yes"), ExitRefused, "this computer's own disk"},
		{"unknown", uncleanDrive, mgArgs("ark", "repair", "ark-ffffffff", "--yes"), ExitNotFound, "No ark drive matches"},
		{"not root", uncleanDrive, nonRoot, ExitDenied, "LAI-CLI-006"},
		{"no terminal, no --yes", uncleanDrive, mgArgs("ark", "repair", "ark-0a0a0a0a"), ExitUsage, "LAI-CLI-007"},
		{"answer no", uncleanDrive, mgArgs("ark", "repair", "ark-0a0a0a0a").withTerm("n"), 0, "Nothing was changed."},
		{"Ctrl-C at the question", uncleanDrive, func() mgRun {
			o := mgArgs("ark", "repair", "ark-0a0a0a0a")
			o.term = &fake.Terminal{TTY: true, Script: []fake.Input{{Err: context.Canceled}}}
			return o
		}(), ExitInterrupted, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newRepairTools(0)
			f.lists = []string{lsblk(sysNVMe, arkUSBMounted)}
			r := repairRig(t, f, tc.listed)
			got := r.run(tc.run)
			if got.code != tc.code || !strings.Contains(got.out+got.errOut, tc.want) {
				t.Fatalf("%+v", got)
			}
			if len(f.changed()) != 0 || posts(r, "/eject") != 0 {
				t.Errorf("touched: %v %v", f.list(), r.requestLines())
			}
		})
	}
}

func TestArkRepairEjectBlocked(t *testing.T) {
	f := newRepairTools(0)
	useTools(t, f)
	r := newMgRig(t, fake.Options{})
	r.agent().Respond(http.MethodGet, "/ark/drives", 200, drivesBody(uncleanDrive))
	r.agent().RespondError(http.MethodPost, "/ark/drives/ark-0a0a0a0a/eject", http.StatusConflict, "LAI-ARK-009",
		"The drive can't be ejected: files are in use", "Close the listed programs.", map[string]any{"blockers": []any{map[string]any{"kind": "process", "name": "viewer", "pid": 42}}})
	got := r.run(mgArgs("ark", "repair", "ark-0a0a0a0a", "--yes"))
	if got.code != ExitRefused || !strings.Contains(got.errOut, "viewer (process 42)") || len(f.changed()) != 0 {
		t.Fatalf("%+v %v", got, f.list())
	}
}

// ---------------------------------------------------------------------------------------------------
// new-id

const (
	twinInUse  = `{"root_id":"ark-0a0a0a0a","device":"/dev/sdb1","mount":"/media/root/AI-ARK","fstype":"ext4","label":"AI-ARK","state":"ready"}`
	twinCopy   = `{"root_id":"ark-0a0a0a0a","device":"/dev/sdc1","mount":"/media/root/AI-ARK1","fstype":"ext4","label":"AI-ARK","state":"unusable","problems":[{"code":"LAI-ARK-006"}]}`
	copyUSBOn  = `{"name":"sdc","path":"/dev/sdc","type":"disk","size":2000398934016,"model":"Copy SSD","serial":"C2","tran":"usb","hotplug":true,"children":[{"name":"sdc1","path":"/dev/sdc1","type":"part","fstype":"ext4","label":"AI-ARK","mountpoints":["/media/root/AI-ARK1"]}]}`
	copyUSBOff = `{"name":"sdc","path":"/dev/sdc","type":"disk","size":2000398934016,"model":"Copy SSD","serial":"C2","tran":"usb","hotplug":true,"children":[{"name":"sdc1","path":"/dev/sdc1","type":"part","fstype":"ext4","label":"AI-ARK","mountpoints":[null]}]}`
)

func newIDRig(t *testing.T, f *fakeTools, listed ...string) *mgRig {
	t.Helper()
	useTools(t, f)
	r := newMgRig(t, fake.Options{})
	r.agent().Handle(http.MethodGet, "/ark/drives", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if f.isMounted("/dev/sdc1") {
			io.WriteString(w, drivesBody(twinInUse, `{"root_id":"ark-77777777","device":"/dev/sdc1","mount":"/media/root/AI-ARK1","fstype":"ext4","label":"AI-ARK","state":"ready"}`))
			return
		}
		io.WriteString(w, drivesBody(listed...))
	})
	return r
}

func newIDTools(code int) *fakeTools {
	return &fakeTools{lists: []string{lsblk(sysNVMe, arkUSBMounted, copyUSBOn), lsblk(sysNVMe, arkUSBMounted, copyUSBOff)}, code: code}
}

func TestArkNewID(t *testing.T) {
	for _, arg := range []string{"ark-0a0a0a0a", "/media/root/AI-ARK1/", "/dev/sdc1"} {
		f := newIDTools(0)
		r := newIDRig(t, f, twinInUse, twinCopy)
		got := r.run(mgArgs("ark", "new-id", arg, "--yes"))
		if got.code != 0 || !strings.Contains(got.out, "The copy now has its own id, ark-77777777") {
			t.Fatalf("%s: %+v", arg, got)
		}
		want := []string{"list", "unmount /dev/sdc1", "list", "check /dev/sdc1", "newuuid /dev/sdc1", "mount /dev/sdc1"}
		if strings.Join(f.list(), "|") != strings.Join(want, "|") {
			t.Errorf("%s: calls %q", arg, f.list())
		}
		// The agent's eject would pick the twin in use: it is never called.
		if posts(r, "/eject") != 0 {
			t.Errorf("eject called: %v", r.requestLines())
		}
	}
	f := newIDTools(0)
	r := newIDRig(t, f, twinInUse, twinCopy)
	got := r.run(mgArgs("--json", "ark", "new-id", "ark-0a0a0a0a", "--yes"))
	var doc map[string]any
	if got.code != 0 || json.Unmarshal([]byte(got.out), &doc) != nil || doc["root_id"] != "ark-77777777" || doc["old_root_id"] != "ark-0a0a0a0a" {
		t.Fatalf("json: %+v", got)
	}
}

func TestArkNewIDRefusalsAndFailures(t *testing.T) {
	copy2 := strings.Replace(strings.Replace(twinCopy, "/dev/sdc1", "/dev/sdd1", 1), "AI-ARK1", "AI-ARK2", 1)
	cases := []struct {
		name   string
		listed []string
		arg    string
		code   int
		want   string
	}{
		{"no copy", []string{twinInUse}, "ark-0a0a0a0a", ExitRefused, "does not share its id"},
		{"the drive in use by path", []string{twinInUse, twinCopy}, "/media/root/AI-ARK", ExitRefused, "does not share its id"},
		{"two copies", []string{twinInUse, twinCopy, copy2}, "ark-0a0a0a0a", ExitUsage, "More than one copy"},
		{"unknown", []string{twinInUse, twinCopy}, "ark-12345678", ExitNotFound, "No ark drive matches"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newIDTools(0)
			r := newIDRig(t, f, tc.listed...)
			got := r.run(mgArgs("ark", "new-id", tc.arg, "--yes"))
			if got.code != tc.code || !strings.Contains(got.errOut, tc.want) || len(f.changed()) != 0 {
				t.Fatalf("%+v %v", got, f.list())
			}
		})
	}
	t.Run("the check leaves errors: the id is not changed and the copy is mounted again", func(t *testing.T) {
		f := newIDTools(4)
		r := newIDRig(t, f, twinInUse, twinCopy)
		got := r.run(mgArgs("ark", "new-id", "ark-0a0a0a0a", "--yes"))
		if got.code != ExitFailed || !strings.Contains(got.errOut, "The id was not changed") {
			t.Fatalf("%+v", got)
		}
		for _, c := range f.list() {
			if strings.HasPrefix(c, "newuuid") {
				t.Fatalf("tune2fs ran: %v", f.list())
			}
		}
		if !f.isMounted("/dev/sdc1") {
			t.Error("not mounted again")
		}
	})
	t.Run("tune2fs fails", func(t *testing.T) {
		f := newIDTools(0)
		f.fail = map[string]error{"newuuid": &prep.ToolError{Tool: "tune2fs", ExitCode: 1, Reason: "bad"}}
		r := newIDRig(t, f, twinInUse, twinCopy)
		got := r.run(mgArgs("ark", "new-id", "ark-0a0a0a0a", "--yes"))
		if got.code != ExitFailed || !strings.Contains(got.errOut, "LAI-ARK-017") {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("answer no", func(t *testing.T) {
		f := newIDTools(0)
		r := newIDRig(t, f, twinInUse, twinCopy)
		got := r.run(mgArgs("ark", "new-id", "ark-0a0a0a0a").withTerm(""))
		if got.code != 0 || len(f.changed()) != 0 {
			t.Fatalf("%+v %v", got, f.list())
		}
	})
}

// Ctrl-C after the yes does not stop the disk work: the check and the remount run to the end, and the
// command says so.
func TestArkRepairCtrlCDuringTheWorkFinishesIt(t *testing.T) {
	f := newRepairTools(0)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// The person presses Ctrl-C while the drive is being ejected.
	r := repairRig(t, f, uncleanDrive, cancel)
	o := mgArgs("ark", "repair", "ark-0a0a0a0a", "--yes")
	o.ctx = ctx
	got := r.run(o)
	if !strings.Contains(strings.Join(f.list(), "|"), "check /dev/sdb1|mount /dev/sdb1") || !strings.Contains(got.errOut, "goes on to the end") {
		t.Fatalf("%+v %v", got, f.list())
	}
	if !errors.Is(ctx.Err(), context.Canceled) || got.code != 0 || !strings.Contains(got.out, "No errors were found") {
		t.Errorf("%+v", got)
	}
}

// ---------------------------------------------------------------------------------------------------
// menu

// arkMenuPath finds the entry that runs args at any depth of the menu, by its command, so the test
// does not depend on its position, and returns the digit keys that reach it (other entries may be added).
func arkMenuPath(t *testing.T, args ...string) []menuKey {
	t.Helper()
	var walk func(items []menuItem, path []menuKey) []menuKey
	walk = func(items []menuItem, path []menuKey) []menuKey {
		for j, it := range items {
			here := append(append([]menuKey(nil), path...), digit((j+1)%10))
			if len(it.args) > 0 && strings.Join(it.args, " ") == strings.Join(args, " ") {
				return here
			}
			if found := walk(it.sub, here); found != nil {
				return found
			}
		}
		return nil
	}
	if p := walk(menuTree(), nil); p != nil {
		return p
	}
	t.Fatalf("no menu entry runs %v", args)
	return nil
}

func TestMenuArkDriveEntries(t *testing.T) {
	drives := drivesBody(
		`{"root_id":"ark-aaaaaaaa","device":"/dev/sdb1","mount":"/media/root/AI-ARK","label":"AI-ARK","state":"ready"}`,
		`{"root_id":"ark-0a0a0a0a","device":"/dev/sdc1","mount":"/media/root/AI-ARK1","label":"AI-ARK","state":"unclean","problems":[{"code":"LAI-ARK-004"}]}`,
		`{"root_id":"ark-aaaaaaaa","device":"/dev/sdd1","mount":"/media/root/AI-ARK2","label":"AI-ARK","state":"unusable","problems":[{"code":"LAI-ARK-006"}]}`,
		`{"root_id":null,"device":"/dev/sde1","mount":"/media/root/X","label":"AI-ARK","state":"unusable"}`)
	run := func(keys ...menuKey) *fakeHost {
		h := &fakeHost{keys: keys, captures: map[string]capture{"status": {statusOK, 0}, "ark drives": {drives, 0}}}
		newTestMenu(h, LangEN).run()
		return h
	}
	path := arkMenuPath(t, "ark", "repair")
	// The drive with errors comes first; the copy and the unusable drive are not offered.
	h := run(append(append([]menuKey(nil), path...), digit(1))...)
	if len(h.runs) != 1 || strings.Join(h.runs[0], " ") != "ark repair ark-0a0a0a0a" {
		t.Fatalf("repair ran %v", h.runs)
	}
	if h = run(append(append([]menuKey(nil), path...), digit(3))...); len(h.runs) != 0 {
		t.Fatalf("a third drive was offered for repair: %v", h.runs)
	}
	if h = run(append(append([]menuKey(nil), path...), digit(2))...); len(h.runs) != 1 || strings.Join(h.runs[0], " ") != "ark repair ark-aaaaaaaa" {
		t.Fatalf("repair ran %v", h.runs)
	}

	// new-id offers only the copy, by its mount path (the root id names both twins).
	path = arkMenuPath(t, "ark", "new-id")
	if h = run(append(append([]menuKey(nil), path...), key(keyEnter))...); len(h.runs) != 1 || strings.Join(h.runs[0], " ") != "ark new-id /media/root/AI-ARK2" {
		t.Fatalf("new-id ran %v", h.runs)
	}
	if cs := copyPicker().choices([]byte(drivesBody(`{"root_id":"ark-aaaaaaaa","device":"/dev/sdb1","state":"ready"}`)), LangEN); len(cs) != 0 {
		t.Errorf("no copy, but offered %v", cs)
	}

	// format has no picker: the command asks for the number itself.
	path = arkMenuPath(t, "ark", "format")
	if h = run(path...); len(h.runs) != 1 || strings.Join(h.runs[0], " ") != "ark format" {
		t.Fatalf("format ran %v", h.runs)
	}
}
