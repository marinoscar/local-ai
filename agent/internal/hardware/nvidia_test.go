package hardware

import (
	"context"
	"errors"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/execx"
	execfake "github.com/marinoscar/local-ai/agent/internal/execx/fake"
)

const smi = "/usr/bin/nvidia-smi"

func gpuKey(field string) string {
	return execfake.Key(smi, "--query-gpu="+nvidiaFieldsHead+field, nvidiaFormat)
}

func appsKey() string { return execfake.Key(smi, nvidiaAppsQuery, nvidiaFormat) }

func nvidiaRunner(t testing.TB, csv, apps string) *execfake.Runner {
	t.Helper()
	r := execfake.New().Expect(gpuKey(nvidiaReasons), ok(string(testdata(t, "nvidia-smi/"+csv))), nil)
	if apps != "" {
		r.Expect(appsKey(), ok(string(testdata(t, "nvidia-smi/"+apps))), nil)
	} else {
		r.Expect(appsKey(), ok(""), nil)
	}
	return r
}

func TestNvidiaNormalSampleMatchesTheSpecExample(t *testing.T) {
	e := testEnv(fixture(t, "sysfs/nvidia-node"), nvidiaRunner(t, "normal.csv", "apps.csv"))
	gpus, err := newNvidiaProbe(e).sample(context.Background())
	if err != nil || len(gpus) != 1 {
		t.Fatalf("gpus = %+v, err = %v", gpus, err)
	}
	g := gpus[0]
	if g.Vendor != VendorNVIDIA || g.Backend != BackendCUDA || g.Name != "NVIDIA GeForce RTX 2070 with Max-Q Design" || g.UnifiedMemory {
		t.Errorf("identity = %+v", g)
	}
	if g.VRAMTotalMiB != 8192 || g.VRAMUsedMiB != 6791 || g.VRAMUsedByOthersMiB == nil || *g.VRAMUsedByOthersMiB != 210 {
		t.Errorf("memory total=%d used=%d others=%v", g.VRAMTotalMiB, g.VRAMUsedMiB, g.VRAMUsedByOthersMiB)
	}
	if g.TempC == nil || *g.TempC != 61 || g.UtilPct == nil || *g.UtilPct != 0 || g.PowerW == nil || *g.PowerW != 1.46 {
		t.Errorf("sensors temp=%v util=%v power=%v", g.TempC, g.UtilPct, g.PowerW)
	}
	if g.ComputeCap == nil || *g.ComputeCap != "7.5" || g.Driver == nil || *g.Driver != "595.99.02" {
		t.Errorf("compute cap / driver = %v / %v", g.ComputeCap, g.Driver)
	}
	if len(g.Throttling) != 0 || g.Throttling == nil {
		t.Errorf("throttling = %#v, want an empty non-nil slice", g.Throttling)
	}
	want := []GPUProcess{{4242, "llama-server", 6581}, {5151, "python3", 60}}
	if len(g.Processes) != 2 || g.Processes[0] != want[0] || g.Processes[1] != want[1] {
		t.Errorf("processes = %+v", g.Processes)
	}
}

func TestNvidiaNotAvailableValuesBecomeNull(t *testing.T) {
	e := testEnv(fixture(t, "sysfs/nvidia-node"), nvidiaRunner(t, "na.csv", ""))
	gpus, err := newNvidiaProbe(e).sample(context.Background())
	if err != nil || len(gpus) != 1 {
		t.Fatalf("gpus = %+v, err = %v", gpus, err)
	}
	g := gpus[0]
	if g.PowerW != nil || g.ComputeCap != nil || len(g.Throttling) != 0 {
		t.Errorf("N/A not null: power=%v cap=%v throttling=%v", g.PowerW, g.ComputeCap, g.Throttling)
	}
	if g.Driver == nil || *g.Driver != "595.99.02" || g.UtilPct == nil || *g.UtilPct != 3 {
		t.Errorf("present values lost: %+v", g)
	}
	// No compute apps at all: everything in use belongs to others (the desktop, a game).
	if g.VRAMUsedByOthersMiB == nil || *g.VRAMUsedByOthersMiB != 6791 || len(g.Processes) != 0 {
		t.Errorf("others = %v processes = %v", g.VRAMUsedByOthersMiB, g.Processes)
	}
}

func TestNvidiaTwoGPUs(t *testing.T) {
	e := testEnv(fixture(t, "sysfs/nvidia-node"), nvidiaRunner(t, "two-gpus.csv", "apps.csv"))
	gpus, err := newNvidiaProbe(e).sample(context.Background())
	if err != nil || len(gpus) != 2 {
		t.Fatalf("gpus = %+v, err = %v", gpus, err)
	}
	if gpus[0].Index != 0 || gpus[1].Index != 1 || gpus[1].VRAMTotalMiB != 12288 || gpus[1].Name != "NVIDIA GeForce RTX 3060 Example" {
		t.Errorf("rows = %+v", gpus)
	}
	// Used 20480 + 2048 = 22528; llama holds 6581, spread in proportion to use.
	for i, wantShare := range []int64{6581 * 20480 / 22528, 6581 * 2048 / 22528} {
		want := gpus[i].VRAMUsedMiB - wantShare
		if got := gpus[i].VRAMUsedByOthersMiB; got == nil || *got != want {
			t.Errorf("gpu %d others = %v, want %d", i, got, want)
		}
		if len(gpus[i].Processes) != 0 {
			t.Errorf("gpu %d: processes cannot be attributed without a uuid, got %+v", i, gpus[i].Processes)
		}
	}
}

func TestNvidiaThrottleBitmask(t *testing.T) {
	cases := []struct {
		file string
		want []string
	}{
		{"throttle-thermal.csv", []string{ThrottleThermal}},
		{"throttle-power-brake.csv", []string{ThrottleThermal, ThrottlePowerBrake}},
	}
	for _, c := range cases {
		e := testEnv(fixture(t, "sysfs/nvidia-node"), nvidiaRunner(t, c.file, ""))
		gpus, err := newNvidiaProbe(e).sample(context.Background())
		if err != nil || len(gpus) != 1 || strings.Join(gpus[0].Throttling, ",") != strings.Join(c.want, ",") {
			t.Errorf("%s: %+v, %v; want %v", c.file, gpus, err, c.want)
		}
	}
	for in, want := range map[string]string{
		"0x0000000000000000": "", "0x20": "thermal", "0x40": "thermal", "0x60": "thermal", "0x80": "power_brake",
		"0x01": "", "0x04": "", "0xE0": "thermal,power_brake", "garbage": "", "": "",
	} {
		if got := strings.Join(throttleReasons(in), ","); got != want {
			t.Errorf("throttleReasons(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNvidiaFallsBackToTheOldReasonsFieldAndRemembersIt(t *testing.T) {
	r := execfake.New().
		Expect(gpuKey(nvidiaReasons), exit(2, string(testdata(t, "nvidia-smi/invalid-field.txt"))), nil).
		Expect(gpuKey(nvidiaReasonsOld), ok(string(testdata(t, "nvidia-smi/normal.csv"))), nil).
		Expect(appsKey(), ok(""), nil)
	p := newNvidiaProbe(testEnv(fixture(t, "sysfs/nvidia-node"), r))
	for i := 0; i < 2; i++ {
		if gpus, err := p.sample(context.Background()); err != nil || len(gpus) != 1 {
			t.Fatalf("sample %d: %+v, %v", i, gpus, err)
		}
	}
	var withNew, withOld int
	for _, c := range r.Calls() {
		switch {
		case strings.HasSuffix(c.Args[0], nvidiaReasonsOld):
			withOld++
		case strings.HasSuffix(c.Args[0], nvidiaReasons) && c.Args[0] != "":
			withNew++
		}
	}
	// The new name is tried once; afterwards only the name that worked is used.
	if withNew != 1 || withOld != 2 {
		t.Errorf("new-field queries = %d, old-field queries = %d; want 1 and 2", withNew, withOld)
	}
}

func TestNvidiaFailures(t *testing.T) {
	cases := []struct {
		name   string
		runner *execfake.Runner
		look   func(string) (string, error)
	}{
		{"driver mismatch", execfake.New().Expect(gpuKey(nvidiaReasons), exit(9, string(testdata(t, "nvidia-smi/driver-mismatch.txt"))), nil), nil},
		{"malformed csv", execfake.New().Expect(gpuKey(nvidiaReasons), ok(string(testdata(t, "nvidia-smi/malformed.csv"))), nil), nil},
		{"wrong column count", execfake.New().Expect(gpuKey(nvidiaReasons), ok("0, name, 1, 2\n"), nil), nil},
		{"unparsable index", execfake.New().Expect(gpuKey(nvidiaReasons), ok("x, n, 8192, 1, 1, 1, 1, 1, 7.5, 1, 0x0\n"), nil), nil},
		{"zero total", execfake.New().Expect(gpuKey(nvidiaReasons), ok("0, n, 0, 1, 1, 1, 1, 1, 7.5, 1, 0x0\n"), nil), nil},
		{"empty name", execfake.New().Expect(gpuKey(nvidiaReasons), ok("0, , 8192, 1, 1, 1, 1, 1, 7.5, 1, 0x0\n"), nil), nil},
		{"start error", execfake.New().Expect(gpuKey(nvidiaReasons), execx.Result{}, errors.New("fork/exec: permission denied")), nil},
		{"timeout", execfake.New().Expect(gpuKey(nvidiaReasons), execx.Result{}, execx.ErrTimeout), nil},
		{"tool missing", execfake.New(), only()},
		{"old field also rejected", execfake.New().
			Expect(gpuKey(nvidiaReasons), exit(2, "is not a valid field"), nil).
			Expect(gpuKey(nvidiaReasonsOld), exit(2, "is not a valid field"), nil), nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := testEnv(fixture(t, "sysfs/nvidia-node"), c.runner)
			if c.look != nil {
				e.look = c.look
			}
			if gpus, err := newNvidiaProbe(e).sample(context.Background()); err == nil {
				t.Fatalf("expected an error, got %+v", gpus)
			}
		})
	}
}

func TestNvidiaToolMissingWrapsErrToolMissing(t *testing.T) {
	e := testEnv(nil, nil)
	e.look = only()
	_, err := newNvidiaProbe(e).sample(context.Background())
	if !errors.Is(err, ErrToolMissing) {
		t.Fatalf("err = %v, want ErrToolMissing", err)
	}
}

func TestNvidiaHungProgramDoesNotStallTheProbe(t *testing.T) {
	e := testEnv(fixture(t, "sysfs/nvidia-node"), blockingRunner{})
	e.execTimeout = 50 * time.Millisecond
	start := time.Now()
	_, err := newNvidiaProbe(e).sample(context.Background())
	if err == nil || !errors.Is(err, execx.ErrTimeout) && !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want a timeout", err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("a hung nvidia-smi held the probe for %v", d)
	}
}

func TestNvidiaAppsQueryFailureOnlyLosesTheDetail(t *testing.T) {
	r := execfake.New().
		Expect(gpuKey(nvidiaReasons), ok(string(testdata(t, "nvidia-smi/normal.csv"))), nil).
		Expect(appsKey(), exit(1, "boom"), nil)
	gpus, err := newNvidiaProbe(testEnv(fixture(t, "sysfs/nvidia-node"), r)).sample(context.Background())
	if err != nil || len(gpus) != 1 || len(gpus[0].Processes) != 0 {
		t.Fatalf("gpus = %+v, err = %v", gpus, err)
	}
}

func TestNvidiaNeverQueriesIdentifiers(t *testing.T) {
	r := nvidiaRunner(t, "normal.csv", "apps.csv")
	e := testEnv(fixture(t, "sysfs/nvidia-node"), r)
	if _, err := newNvidiaProbe(e).sample(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(r.Calls()) == 0 {
		t.Fatal("nothing was run")
	}
	for _, c := range r.Calls() {
		line := strings.ToLower(strings.Join(c.Args, " "))
		for _, banned := range []string{"uuid", "serial", "pci.bus_id", "gpu_bus_id", "gpu_uuid"} {
			if strings.Contains(line, banned) {
				t.Errorf("argv %q queries %q (redaction rule, agent.md 4.4.1)", line, banned)
			}
		}
		if c.Timeout != time.Second || c.MaxOut != ExecMaxOut {
			t.Errorf("limits not applied: timeout=%v maxout=%d", c.Timeout, c.MaxOut)
		}
	}
}

func TestNvidiaPresence(t *testing.T) {
	cases := []struct {
		name       string
		fsys       fstest.MapFS
		look       func(string) (string, error)
		wantDriver bool
		wantPres   bool
	}{
		{"driver proc file", fstest.MapFS{nvidiaDriverProc: file("x")}, only(), true, true},
		{"binary only", fstest.MapFS{}, only("nvidia-smi"), false, true},
		{"both", fstest.MapFS{nvidiaDriverProc: file("x")}, only("nvidia-smi"), true, true},
		{"neither", fstest.MapFS{}, only(), false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := testEnv(c.fsys, nil)
			e.look = c.look
			p := newNvidiaProbe(e)
			if p.driverLoaded() != c.wantDriver || p.present() != c.wantPres {
				t.Errorf("driver=%v present=%v", p.driverLoaded(), p.present())
			}
		})
	}
}

func TestCgroupMatching(t *testing.T) {
	for _, c := range []struct {
		name string
		in   string
		want bool
	}{
		{"v2 unit", "0::/system.slice/localai-llama.service\n", true},
		{"v1 hybrid", "12:cpu:/user.slice\n1:name=systemd:/system.slice/localai-llama.service\n0::/system.slice/localai-llama.service\n", true},
		{"child cgroup of the unit", "0::/system.slice/localai-llama.service/worker\n", true},
		{"another unit", "0::/system.slice/localai-whisper.service\n", false},
		{"similar name is not the unit", "0::/system.slice/not-localai-llama.service\n", false},
		{"user session", "0::/user.slice/user-1000.slice/session-3.scope\n", false},
		{"empty", "", false},
		{"garbage", "no colons here\n", false},
	} {
		if got := cgroupHasUnit([]byte(c.in), llamaCgroupUnit); got != c.want {
			t.Errorf("%s: got %v", c.name, got)
		}
	}
}

func TestParseNvidiaAppsCases(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    []GPUProcess
		wantErr bool
	}{
		{"empty output means no processes", "", nil, false},
		{"whitespace", "\n  \n", nil, false},
		{"path reduced to base name", "7, /usr/bin/llama-server, 100\n", []GPUProcess{{7, "llama-server", 100}}, false},
		{"memory N/A", "8, python3, [N/A]\n", []GPUProcess{{8, "python3", 0}}, false},
		{"bad pid", "x, python3, 1\n", nil, true},
		{"bad memory", "9, python3, lots\n", nil, true},
		{"wrong columns", "9, python3\n", nil, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := parseNvidiaApps([]byte(c.in))
			if (err != nil) != c.wantErr || len(got) != len(c.want) {
				t.Fatalf("got %+v, %v", got, err)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Errorf("row %d = %+v, want %+v", i, got[i], c.want[i])
				}
			}
		})
	}
}
