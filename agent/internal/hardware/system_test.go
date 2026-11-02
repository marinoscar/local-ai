package hardware

import (
	"errors"
	"testing"
	"testing/fstest"
	"time"
)

func TestReadRAMAndCPUFromFixture(t *testing.T) {
	e := testEnv(fixture(t, "proc/node-a"), nil)
	ram, err := e.readRAM()
	if err != nil {
		t.Fatal(err)
	}
	want := RAM{TotalMiB: 14336, AvailableMiB: 5120, SwapTotalMiB: 4096, SwapFreeMiB: 4000}
	if ram != want {
		t.Errorf("ram = %+v, want %+v", ram, want)
	}
	if got := ram.AvailablePct(); got < 35.6 || got > 35.8 {
		t.Errorf("AvailablePct = %v", got)
	}
	if got := ram.SwapUsedPct(); got < 2.3 || got > 2.4 {
		t.Errorf("SwapUsedPct = %v", got)
	}
	cpu, err := e.readCPU()
	if err != nil {
		t.Fatal(err)
	}
	if cpu.Model != "Example(R) Core(TM) X1 CPU @ 2.70GHz" || cpu.Cores != 4 || cpu.Load1 != 0.40 {
		t.Errorf("cpu = %+v", cpu)
	}
}

func TestParseMeminfoCases(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    RAM
		wantErr bool
	}{
		{"no swap", "MemTotal: 2048 kB\nMemAvailable: 1024 kB\n", RAM{TotalMiB: 2, AvailableMiB: 1}, false},
		{"old kernel without MemAvailable", "MemTotal: 2048 kB\nMemFree: 1024 kB\n", RAM{}, true},
		{"no MemTotal", "MemAvailable: 1024 kB\n", RAM{}, true},
		{"garbage value", "MemTotal: lots kB\nMemAvailable: 1 kB\n", RAM{}, true},
		{"negative", "MemTotal: -5 kB\nMemAvailable: 1 kB\n", RAM{}, true},
		{"empty", "", RAM{}, true},
		{"binary", "\x00\x01\x02", RAM{}, true},
		{"key without value is skipped", "MemTotal:\nMemTotal: 4096 kB\nMemAvailable: 2048 kB\n", RAM{TotalMiB: 4, AvailableMiB: 2}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := parseMeminfo([]byte(c.in))
			if (err != nil) != c.wantErr || got != c.want {
				t.Fatalf("got %+v, %v; want %+v (err %v)", got, err, c.want, c.wantErr)
			}
		})
	}
}

func TestRAMPercentagesAreSafeWithoutData(t *testing.T) {
	if got := (RAM{}).AvailablePct(); got != 100 {
		t.Errorf("AvailablePct on empty = %v", got)
	}
	if got := (RAM{}).SwapUsedPct(); got != 0 {
		t.Errorf("SwapUsedPct on empty = %v", got)
	}
}

func TestReadRAMMissingFile(t *testing.T) {
	if _, err := testEnv(fstest.MapFS{}, nil).readRAM(); err == nil {
		t.Fatal("expected an error when /proc/meminfo is absent")
	}
}

func TestParseCPUInfoCases(t *testing.T) {
	cases := []struct {
		name      string
		in        string
		model     string
		cores     int
		wantError bool
	}{
		{"x86", "processor : 0\nmodel name : Example CPU\nprocessor : 1\nmodel name : Example CPU\n", "Example CPU", 2, false},
		{"arm uses Model", "processor : 0\nprocessor : 1\nprocessor : 2\nModel : Example Board 5\n", "Example Board 5", 3, false},
		{"arm uses Hardware", "processor : 0\nHardware : Example SoC\n", "Example SoC", 1, false},
		{"model name wins over Hardware", "processor : 0\nHardware : SoC\nmodel name : Named\n", "Named", 1, false},
		{"no processors", "model name : Example CPU\n", "", 0, true},
		{"empty", "", "", 0, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			model, cores, err := parseCPUInfo([]byte(c.in))
			if (err != nil) != c.wantError || model != c.model || cores != c.cores {
				t.Fatalf("got %q %d %v", model, cores, err)
			}
		})
	}
}

func TestParseLoadavgCases(t *testing.T) {
	for _, c := range []struct {
		in      string
		want    float64
		wantErr bool
	}{
		{"0.40 0.52 0.61 1/812 123456\n", 0.40, false},
		{"12.5", 12.5, false},
		{"", 0, true},
		{"abc 1 2", 0, true},
		{"-1 0 0", 0, true},
	} {
		got, err := parseLoadavg([]byte(c.in))
		if (err != nil) != c.wantErr || got != c.want {
			t.Errorf("parseLoadavg(%q) = %v, %v", c.in, got, err)
		}
	}
}

func TestReadCPUFailsOnMissingLoadavg(t *testing.T) {
	fsys := fstest.MapFS{"proc/cpuinfo": file("processor : 0\nmodel name : X\n")}
	if _, err := testEnv(fsys, nil).readCPU(); err == nil {
		t.Fatal("expected an error without /proc/loadavg")
	}
}

func TestSelfTrackerCPUPercentAndRSS(t *testing.T) {
	e := testEnv(fixture(t, "proc/node-a"), nil)
	var tr selfTracker
	t0 := time.Date(2026, 11, 2, 15, 0, 0, 0, time.UTC)
	s, err := tr.read(e, t0)
	if err != nil {
		t.Fatal(err)
	}
	if s.CPUPercent != 0 || s.RSSMiB != 50 {
		t.Errorf("first sample = %+v, want 0%% and 50 MiB", s)
	}
	// 200 ticks (2 CPU seconds) were used at t0; the same file read 10 s later shows no delta.
	s, err = tr.read(e, t0.Add(10*time.Second))
	if err != nil || s.CPUPercent != 0 {
		t.Fatalf("second sample = %+v, %v", s, err)
	}
	// Simulate 100 more ticks in 10 s: 10 % of one core.
	tr.ticks -= 100
	s, err = tr.read(e, t0.Add(20*time.Second))
	if err != nil || s.CPUPercent != 10 {
		t.Fatalf("delta sample = %+v, %v", s, err)
	}
}

func TestParseSelfStatHandlesOddCommandNames(t *testing.T) {
	tail := " S 1 1 1 0 -1 0 0 0 0 0 7 3 0 0 20 0 1 0 1 1 1 1"
	for _, c := range []struct {
		name string
		in   string
		want uint64
		bad  bool
	}{
		{"plain", "1 (aiagent)" + tail, 10, false},
		{"spaces and parens in the name", "1 (a) (b c)" + tail, 10, false},
		{"no parenthesis", "1 aiagent S", 0, true},
		{"too few fields", "1 (x) S 1 2", 0, true},
		{"non-numeric times", "1 (x) S 1 1 1 0 -1 0 0 0 0 0 a b 0", 0, true},
	} {
		got, err := parseSelfStat([]byte(c.in))
		if (err != nil) != c.bad || got != c.want {
			t.Errorf("%s: got %d, %v", c.name, got, err)
		}
	}
}

func TestParseVmRSSMissing(t *testing.T) {
	if _, err := parseVmRSS([]byte("Name:\tx\n")); err == nil {
		t.Error("expected an error without VmRSS")
	}
	if _, err := parseVmRSS([]byte("VmRSS:\n")); err == nil {
		t.Error("expected an error for an empty VmRSS")
	}
	if _, err := parseVmRSS([]byte("VmRSS: x kB\n")); err == nil {
		t.Error("expected an error for a bad VmRSS")
	}
}

func TestMountinfoAndFSType(t *testing.T) {
	mounts := parseMountinfo(testdata(t, "proc/node-a/proc/self/mountinfo"))
	if len(mounts) != 5 {
		t.Fatalf("parsed %d mounts: %+v", len(mounts), mounts)
	}
	for path, want := range map[string]string{
		"/srv/localai":           "xfs",
		"/srv/localai/models/x":  "xfs",
		"/var/lib/localai":       "btrfs",
		"/opt/localai":           "ext4", // falls back to the root mount
		"/media/ark drive":       "exfat",
		"/media/ark drive/lai/x": "exfat",
		"/srv/localai-other":     "ext4", // a longer name is not inside /srv/localai
	} {
		if got := fsTypeOf(mounts, path); got != want {
			t.Errorf("fsTypeOf(%q) = %q, want %q", path, got, want)
		}
	}
	if got := fsTypeOf(nil, "/srv/localai"); got != "" {
		t.Errorf("no mounts: got %q", got)
	}
	if got := parseMountinfo([]byte("garbage line\n1 2 3 4 5 - \n")); len(got) != 0 {
		t.Errorf("malformed lines parsed as %+v", got)
	}
}

func TestUnescapeOctal(t *testing.T) {
	for in, want := range map[string]string{
		`/plain`:         "/plain",
		`/a\040b`:        "/a b",
		`/tab\011x`:      "/tab\tx",
		`/trailing\04`:   `/trailing\04`,
		`/not\9zzoctal`:  `/not\9zzoctal`,
		`/back\134slash`: `/back\slash`,
	} {
		if got := unescapeOctal(in); got != want {
			t.Errorf("unescapeOctal(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestReadDisks(t *testing.T) {
	e := testEnv(fixture(t, "proc/node-a"), nil)
	statfs := func(p string) (int64, int64, error) {
		switch p {
		case "/srv/localai":
			return 342_000_000_000, 293_000_000_000, nil
		case "/var/lib/localai":
			return 100_000_000_000, 4_000_000_000, nil
		}
		return 0, 0, errors.New("no such file or directory")
	}
	disks, errs := e.readDisks(statfs, []DiskTarget{
		{InternalRootPath, RoleInternalRoot}, {StatePath, RoleState}, {OptPath, RoleOpt},
	})
	if len(disks) != 2 || len(errs) != 1 || errs[OptPath] == nil {
		t.Fatalf("disks = %+v errs = %v", disks, errs)
	}
	if d := disks[0]; d.FS != "xfs" || d.Role != RoleInternalRoot || d.FreeBytes != 293_000_000_000 {
		t.Errorf("internal root = %+v", d)
	}
	if d := disks[1]; d.FS != "btrfs" || d.FreePct() != 4 {
		t.Errorf("state = %+v (free %v%%)", d, d.FreePct())
	}
	if (Disk{}).FreePct() != 100 {
		t.Error("FreePct of an unmeasured disk must not look full")
	}
}

func TestReadDisksWithoutMountinfoStillMeasures(t *testing.T) {
	e := testEnv(fstest.MapFS{}, nil)
	disks, errs := e.readDisks(func(string) (int64, int64, error) { return 10, 5, nil }, []DiskTarget{{"/x", RoleEtc}})
	if len(disks) != 1 || len(errs) != 0 || disks[0].FS != "" {
		t.Fatalf("disks = %+v errs = %v", disks, errs)
	}
}

func TestOSStatfsOnTempDir(t *testing.T) {
	total, free, err := osStatfs(t.TempDir())
	if err != nil {
		t.Skipf("statfs unsupported here: %v", err)
	}
	if total <= 0 || free < 0 || free > total {
		t.Errorf("total=%d free=%d", total, free)
	}
	if _, _, err := osStatfs("/definitely/not/here"); err == nil {
		t.Error("expected an error for a missing path")
	}
}
