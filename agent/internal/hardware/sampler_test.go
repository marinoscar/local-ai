package hardware

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/execx"
	execfake "github.com/marinoscar/local-ai/agent/internal/execx/fake"
)

// compose copies fixture trees under testdata into one MapFS, so a test can mix a procfs tree with
// a sysfs scenario and then change files between samples.
func compose(t testing.TB, dirs ...string) fstest.MapFS {
	t.Helper()
	m := fstest.MapFS{}
	for _, d := range dirs {
		root := filepath.Join("testdata", filepath.FromSlash(d))
		err := filepath.WalkDir(root, func(p string, de fs.DirEntry, err error) error {
			if err != nil || de.IsDir() {
				return err
			}
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(root, p)
			m[filepath.ToSlash(rel)] = &fstest.MapFile{Data: b}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return m
}

// runnerFunc adapts a function to execx.Runner.
type runnerFunc func(ctx context.Context, c execx.Cmd) (execx.Result, error)

func (f runnerFunc) Run(ctx context.Context, c execx.Cmd) (execx.Result, error) { return f(ctx, c) }

// smiRunner answers nvidia-smi from the normal fixture until it is told to fail, and counts calls.
type smiRunner struct {
	t     testing.TB
	fail  atomic.Bool
	calls atomic.Int64
}

func (r *smiRunner) Run(_ context.Context, c execx.Cmd) (execx.Result, error) {
	r.calls.Add(1)
	if r.fail.Load() {
		return exit(9, "Failed to initialize NVML: Driver/library version mismatch"), nil
	}
	if len(c.Args) > 0 && strings.HasPrefix(c.Args[0], "--query-gpu=") {
		return ok(string(testdata(r.t, "nvidia-smi/normal.csv"))), nil
	}
	if len(c.Args) > 0 && strings.HasPrefix(c.Args[0], "--query-compute-apps=") {
		return ok(string(testdata(r.t, "nvidia-smi/apps.csv"))), nil
	}
	return execx.Result{}, errors.New("unexpected command " + c.Path)
}

// fakeClock is a settable clock.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func fixedStatfs(p string) (int64, int64, error) {
	switch p {
	case InternalRootPath:
		return 342_000_000_000, 293_000_000_000, nil
	case StatePath:
		return 100_000_000_000, 60_000_000_000, nil
	case EtcPath:
		return 100_000_000_000, 60_000_000_000, nil
	}
	return 0, 0, errors.New("no such file or directory")
}

// nodeService is a Service over the NVIDIA laptop fixtures: procfs, the NVIDIA node, a discharging
// battery and Intel thermal zones.
func nodeService(t testing.TB, run execx.Runner, mod ...func(*Options)) (*Service, fstest.MapFS, *fakeClock) {
	t.Helper()
	fsys := compose(t, "proc/node-a", "sysfs/nvidia-node", "sysfs/battery-discharging-energy", "sysfs/thermal-intel")
	clk := &fakeClock{t: time.Date(2026, 11, 2, 15, 4, 0, 0, time.UTC)}
	opts := Options{
		FS: fsys, Exec: run, LookPath: only("nvidia-smi", "powerprofilesctl"), Statfs: fixedStatfs, Now: clk.Now,
		ExecTimeout: time.Second, Logger: slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})),
	}
	for _, m := range mod {
		m(&opts)
	}
	return New(opts), fsys, clk
}

func profileOK(r *smiRunner) execx.Runner {
	return runnerFunc(func(ctx context.Context, c execx.Cmd) (execx.Result, error) {
		if strings.HasSuffix(c.Path, "powerprofilesctl") {
			return ok("performance\n"), nil
		}
		return r.Run(ctx, c)
	})
}

func TestServicePrimeBuildsTheSnapshot(t *testing.T) {
	r := &smiRunner{t: t}
	s, _, clk := nodeService(t, profileOK(r))
	if !s.Snapshot().SampledAt.IsZero() {
		t.Fatal("a snapshot before the first sample must have a zero SampledAt")
	}
	s.Prime(context.Background())
	snap := s.Snapshot()

	if !snap.SampledAt.Equal(clk.Now()) {
		t.Errorf("sampled_at = %v", snap.SampledAt)
	}
	if len(snap.GPUs) != 1 || snap.GPUs[0].VRAMUsedByOthersMiB == nil || *snap.GPUs[0].VRAMUsedByOthersMiB != 210 || snap.GPUs[0].Stale {
		t.Errorf("gpus = %+v", snap.GPUs)
	}
	if snap.RAM.TotalMiB != 14336 || snap.CPU.Cores != 4 || snap.Self.RSSMiB != 50 {
		t.Errorf("ram/cpu/self = %+v %+v %+v", snap.RAM, snap.CPU, snap.Self)
	}
	if snap.Power.Source != SourceBattery || snap.Power.BatteryPercent == nil || *snap.Power.BatteryPercent != 64 || snap.Power.Profile == nil || *snap.Power.Profile != "performance" {
		t.Errorf("power = %+v", snap.Power)
	}
	if snap.Thermal.CPUTempC == nil || *snap.Thermal.CPUTempC != 72 || snap.Thermal.CPUThrottleEvents != 3 {
		t.Errorf("thermal = %+v", snap.Thermal)
	}
	if snap.Backend != BackendCUDA || snap.GPUMemoryForFit() != 8192 || snap.GOthersMiB() != 210 {
		t.Errorf("backend=%q fit=%d others=%d", snap.Backend, snap.GPUMemoryForFit(), snap.GOthersMiB())
	}
	// /opt/localai does not exist in the fake statfs: omitted, not reported with zeros.
	var roles []string
	for _, d := range snap.Disks {
		roles = append(roles, d.Role)
	}
	if strings.Join(roles, ",") != "internal_root,state,etc" {
		t.Errorf("disk roles = %v", roles)
	}
	if d, ok := snap.InternalRoot(); !ok || d.FS != "xfs" || d.Stale {
		t.Errorf("internal root = %+v", d)
	}
	if got := codes(s.Results()); got != "LAI-HW-012" {
		t.Errorf("results = %q, want only the battery notice", got)
	}
}

func TestServiceJSONMatchesTheSpecShape(t *testing.T) {
	s, _, _ := nodeService(t, profileOK(&smiRunner{t: t}))
	s.Prime(context.Background())
	b, err := json.Marshal(s.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"sampled_at", "gpus", "ram", "disks", "power", "thermal", "cpu"} {
		if _, ok := doc[k]; !ok {
			t.Errorf("key %q missing from %s", k, b)
		}
	}
	for _, k := range []string{"backend", "Backend", "self", "Self"} {
		if _, ok := doc[k]; ok {
			t.Errorf("internal key %q leaked into the wire body", k)
		}
	}
	g := doc["gpus"].([]any)[0].(map[string]any)
	for _, k := range []string{"index", "vendor", "name", "backend", "compute_cap", "driver", "unified_memory", "vram_total_mib", "vram_used_mib",
		"vram_used_by_others_mib", "temp_c", "util_pct", "power_w", "throttling", "processes"} {
		if _, ok := g[k]; !ok {
			t.Errorf("gpu key %q missing", k)
		}
	}
	if _, ok := g["stale"]; ok {
		t.Error("a fresh GPU must not carry stale")
	}
	if g["throttling"] == nil || g["processes"] == nil {
		t.Errorf("throttling and processes must be arrays, never null: %v %v", g["throttling"], g["processes"])
	}
	for _, banned := range []string{"uuid", "serial", "pci", "bus_id"} {
		if strings.Contains(strings.ToLower(string(b)), banned) {
			t.Errorf("the body mentions %q", banned)
		}
	}
	// A node with no GPU answers gpus: [] (not null), and no battery answers null fields.
	empty, _ := json.Marshal(Snapshot{}.clone())
	if !strings.Contains(string(empty), `"gpus":[]`) || !strings.Contains(string(empty), `"disks":[]`) {
		t.Errorf("empty snapshot = %s", empty)
	}
}

func TestProbeFailureKeepsTheLastValueAsStaleAndRecovers(t *testing.T) {
	r := &smiRunner{t: t}
	s, _, _ := nodeService(t, profileOK(r))
	ctx := context.Background()
	s.Prime(ctx)

	r.fail.Store(true)
	for i := 1; i <= 3; i++ {
		snap := s.Refresh(ctx)
		if len(snap.GPUs) != 1 || !snap.GPUs[0].Stale || snap.GPUs[0].VRAMTotalMiB != 8192 {
			t.Fatalf("failure %d: gpus = %+v, want the last value marked stale", i, snap.GPUs)
		}
		if snap.RAM.Stale || snap.Power.Stale {
			t.Errorf("failure %d: an unrelated section went stale", i)
		}
		raised := strings.Contains(codes(s.Results()), "LAI-HW-002")
		if raised != (i >= 3) {
			t.Errorf("failure %d: LAI-HW-002 raised = %v (alert after 3 failures)", i, raised)
		}
	}
	r.fail.Store(false)
	snap := s.Refresh(ctx)
	if snap.GPUs[0].Stale || strings.Contains(codes(s.Results()), "LAI-HW-002") {
		t.Errorf("a good sample must clear the stale flag and the alert: %+v %q", snap.GPUs, codes(s.Results()))
	}
}

func TestRAMFailureIsStaleToo(t *testing.T) {
	s, fsys, _ := nodeService(t, profileOK(&smiRunner{t: t}))
	s.Prime(context.Background())
	delete(fsys, "proc/meminfo")
	snap := s.Refresh(context.Background())
	if !snap.RAM.Stale || snap.RAM.TotalMiB != 14336 {
		t.Errorf("ram = %+v", snap.RAM)
	}
	fsys["proc/meminfo"] = file("MemTotal: 2048 kB\nMemAvailable: 1024 kB\n")
	if snap = s.Refresh(context.Background()); snap.RAM.Stale || snap.RAM.TotalMiB != 2 {
		t.Errorf("ram after recovery = %+v", snap.RAM)
	}
}

func TestHungNvidiaSmiNeverStallsTheSampler(t *testing.T) {
	s, _, _ := nodeService(t, runnerFunc(func(ctx context.Context, c execx.Cmd) (execx.Result, error) {
		if strings.HasSuffix(c.Path, "nvidia-smi") {
			<-ctx.Done() // hangs until its deadline
			return execx.Result{}, ctx.Err()
		}
		return ok("performance\n"), nil
	}), func(o *Options) { o.ExecTimeout = 60 * time.Millisecond })
	start := time.Now()
	snap := s.Refresh(context.Background())
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("a hung nvidia-smi held the round for %v", d)
	}
	if snap.RAM.TotalMiB != 14336 || snap.Power.Source != SourceBattery {
		t.Errorf("the other probes must still answer: %+v", snap)
	}
	if len(snap.GPUs) != 0 {
		t.Errorf("gpus = %+v, want none: nothing ever answered", snap.GPUs)
	}
}

func TestProbeThatIgnoresItsDeadlineIsCutOffAtTheCap(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	s, _, _ := nodeService(t, runnerFunc(func(ctx context.Context, c execx.Cmd) (execx.Result, error) {
		if strings.HasSuffix(c.Path, "nvidia-smi") {
			<-release // a runner that honors nothing
		}
		return ok("performance\n"), nil
	}), func(o *Options) { o.SampleCap = 80 * time.Millisecond; o.ExecTimeout = 10 * time.Second })
	start := time.Now()
	snap := s.Refresh(context.Background())
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("round took %v with a cap of 80ms", d)
	}
	if snap.RAM.TotalMiB != 14336 {
		t.Errorf("fast probes lost: %+v", snap.RAM)
	}
}

func TestFirstSampleKeepsLastValueWhenThePowerProbeIsLate(t *testing.T) {
	var slow atomic.Bool
	release := make(chan struct{})
	defer close(release)
	r := &smiRunner{t: t}
	s, _, _ := nodeService(t, runnerFunc(func(ctx context.Context, c execx.Cmd) (execx.Result, error) {
		if strings.HasSuffix(c.Path, "powerprofilesctl") && slow.Load() {
			<-release
		}
		if strings.HasSuffix(c.Path, "powerprofilesctl") {
			return ok("performance\n"), nil
		}
		return r.Run(ctx, c)
	}), func(o *Options) { o.SampleCap = 100 * time.Millisecond; o.ExecTimeout = 10 * time.Second })
	s.Prime(context.Background())
	slow.Store(true)
	snap := s.Refresh(context.Background())
	if !snap.Power.Stale || snap.Power.Source != SourceBattery {
		t.Errorf("power = %+v, want the last value marked stale", snap.Power)
	}
}

func TestPowerEventsOnChangeOnly(t *testing.T) {
	var mu sync.Mutex
	var events []PowerEvent
	r := &smiRunner{t: t}
	s, fsys, _ := nodeService(t, profileOK(r), func(o *Options) {
		o.OnPowerChange = func(e PowerEvent) { mu.Lock(); events = append(events, e); mu.Unlock() }
	})
	ctx := context.Background()
	got := func() int { mu.Lock(); defer mu.Unlock(); return len(events) }

	s.Prime(ctx)
	if got() != 0 {
		t.Fatalf("the first observation is a baseline, got %v", events)
	}
	s.Refresh(ctx) // nothing changed
	if got() != 0 {
		t.Fatalf("unchanged power raised an event: %v", events)
	}
	fsys["sys/class/power_supply/BAT0/capacity"] = file("63\n")
	s.Refresh(ctx)
	if got() != 1 || *events[0].BatteryPercent != 63 || events[0].Source != SourceBattery {
		t.Fatalf("percent change: %v", events)
	}
	fsys["sys/class/power_supply/AC/online"] = file("1\n")
	fsys["sys/class/power_supply/BAT0/status"] = file("Charging\n")
	s.Refresh(ctx)
	if got() != 2 || events[1].Source != SourceAC || !events[1].Charging {
		t.Fatalf("plugged in: %v", events)
	}
	s.Refresh(ctx)
	if got() != 2 {
		t.Fatalf("steady state raised an event: %v", events)
	}
}

func TestBatteryAlertsFollowThePowerSource(t *testing.T) {
	s, fsys, _ := nodeService(t, profileOK(&smiRunner{t: t}))
	ctx := context.Background()
	s.Prime(ctx)
	if got := codes(s.Results()); got != "LAI-HW-012" {
		t.Fatalf("on battery at 64%%: %q", got)
	}
	fsys["sys/class/power_supply/BAT0/capacity"] = file("19\n")
	s.Refresh(ctx)
	if got := codes(s.Results()); got != "LAI-HW-012,LAI-HW-013" {
		t.Errorf("at 19%%: %q", got)
	}
	fsys["sys/class/power_supply/BAT0/capacity"] = file("9\n")
	s.Refresh(ctx)
	if got := codes(s.Results()); got != "LAI-HW-012,LAI-HW-014" {
		t.Errorf("at 9%%: %q", got)
	}
	fsys["sys/class/power_supply/AC/online"] = file("1\n")
	s.Refresh(ctx)
	if got := codes(s.Results()); got != "" {
		t.Errorf("on AC: %q", got)
	}
}

func TestFastSamplesAdvanceTheThreeSampleRuleOncePerRound(t *testing.T) {
	s, fsys, _ := nodeService(t, profileOK(&smiRunner{t: t}))
	ctx := context.Background()
	fsys["proc/meminfo"] = file("MemTotal: 10240000 kB\nMemAvailable: 256000 kB\nSwapTotal: 0 kB\nSwapFree: 0 kB\n") // 2.5 % available
	s.Prime(ctx)                                                                                                     // sample 1 (fast + disks)
	s.Refresh(ctx)                                                                                                   // sample 2
	if strings.Contains(codes(s.Results()), "LAI-HW-006") {
		t.Fatalf("two sampling rounds raised LAI-HW-006: %q", codes(s.Results()))
	}
	s.Refresh(ctx) // sample 3
	if !strings.Contains(codes(s.Results()), "LAI-HW-006") {
		t.Fatalf("three sampling rounds must raise LAI-HW-006: %q", codes(s.Results()))
	}
}

func TestDiskFailureKeepsLastValueAndDriveRootsAreListed(t *testing.T) {
	var failState atomic.Bool
	statfs := func(p string) (int64, int64, error) {
		if p == StatePath && failState.Load() {
			return 0, 0, errors.New("input/output error")
		}
		if p == "/media/ark" {
			return 1_000_000_000_000, 400_000_000_000, nil
		}
		return fixedStatfs(p)
	}
	s, _, _ := nodeService(t, profileOK(&smiRunner{t: t}), func(o *Options) {
		o.Statfs = statfs
		o.DriveRoots = func() []string { return []string{"/media/ark"} }
	})
	ctx := context.Background()
	s.Prime(ctx)
	var roles []string
	for _, d := range s.Snapshot().Disks {
		roles = append(roles, d.Role+":"+d.Path)
	}
	if strings.Join(roles, ",") != "internal_root:/srv/localai,drive_root:/media/ark,state:/var/lib/localai,etc:/etc/localai" {
		t.Fatalf("disks = %v", roles)
	}
	failState.Store(true)
	s.Refresh(ctx)
	for _, d := range s.Snapshot().Disks {
		if d.Path == StatePath && (!d.Stale || d.TotalBytes != 100_000_000_000) {
			t.Errorf("state disk = %+v, want the last value marked stale", d)
		}
		if d.Path == InternalRootPath && d.Stale {
			t.Errorf("an unrelated disk went stale: %+v", d)
		}
	}
}

func TestProfileStorageHookDrivesLAIHW007(t *testing.T) {
	var minFree atomic.Int64
	minFree.Store(10_000_000_000)
	var haveCatalog atomic.Bool
	s, _, _ := nodeService(t, profileOK(&smiRunner{t: t}), func(o *Options) {
		o.ProfileStorage = func() (int64, bool) { return minFree.Load(), haveCatalog.Load() }
	})
	ctx := context.Background()
	minFree.Store(400_000_000_000) // the internal root has 293 GB free
	s.Prime(ctx)
	if strings.Contains(codes(s.Results()), "LAI-HW-007") {
		t.Error("without a catalog the check must be skipped")
	}
	haveCatalog.Store(true)
	s.Refresh(ctx)
	if !strings.Contains(codes(s.Results()), "LAI-HW-007") {
		t.Errorf("results = %q, want LAI-HW-007", codes(s.Results()))
	}
	minFree.Store(100_000_000_000)
	s.Refresh(ctx)
	if strings.Contains(codes(s.Results()), "LAI-HW-007") {
		t.Errorf("results = %q, want LAI-HW-007 cleared", codes(s.Results()))
	}
}

func TestConfiguredBackendMismatchRaisesLAIHW017(t *testing.T) {
	fsys := compose(t, "proc/node-a", "sysfs/amd-dedicated")
	var configured atomic.Value
	configured.Store("cuda")
	s := New(Options{
		FS: fsys, Exec: execfake.New(), LookPath: only(), Statfs: fixedStatfs,
		ConfiguredBackend: func() string { return configured.Load().(string) },
		Logger:            slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
	})
	ctx := context.Background()
	s.Prime(ctx)
	if got := codes(s.Results()); got != "LAI-HW-017" {
		t.Fatalf("cuda configured on an AMD-only node: %q", got)
	}
	if b := s.Snapshot().Backend; b != BackendVulkan {
		t.Errorf("detected backend = %q", b)
	}
	configured.Store("vulkan")
	s.Refresh(ctx)
	if got := codes(s.Results()); got != "" {
		t.Errorf("vulkan configured: %q", got)
	}
}

func TestAMDNodeSnapshotAndAccessResults(t *testing.T) {
	fsys := compose(t, "proc/node-a", "sysfs/amd-strix-halo")
	delete(fsys, "dev/kfd")
	r := execfake.New().Expect(execfake.Key("/usr/bin/lspci", "-mm", "-s", "0000:c1:00.0"), ok(string(testdata(t, "lspci/strix-halo.txt"))), nil)
	s := New(Options{FS: fsys, Exec: r, LookPath: only("lspci"), Statfs: fixedStatfs, Logger: slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))})
	s.Prime(context.Background())
	snap := s.Snapshot()
	if len(snap.GPUs) != 1 || !snap.GPUs[0].UnifiedMemory || snap.GPUs[0].VRAMTotalMiB != 62464 {
		t.Fatalf("gpus = %+v", snap.GPUs)
	}
	if snap.GPUMemoryForFit() != 62464 || snap.GOthersMiB() != 1280 || !snap.UnifiedMemory(snap.Backend) || snap.Backend != BackendVulkan {
		t.Errorf("fit=%d others=%d backend=%s", snap.GPUMemoryForFit(), snap.GOthersMiB(), snap.Backend)
	}
	if got := codes(s.Results()); got != "LAI-HW-003" {
		t.Errorf("results = %q, want the missing /dev/kfd", got)
	}
}

func TestOOMCheckAndServiceVerdictRaiseLAILLM027(t *testing.T) {
	clk := &fakeClock{t: time.Date(2026, 11, 2, 15, 4, 0, 0, time.UTC)}
	journal := execfake.New().Expect(journalKey(clk.Now().Add(-time.Minute)), ok(`{"__REALTIME_TIMESTAMP":"`+itoa(clk.Now().Add(-30*time.Second).UnixMicro())+`","MESSAGE":"Out of memory: Killed process 4242 (llama-server) total-vm:1kB"}`+"\n"), nil)
	s := New(Options{
		FS: compose(t, "proc/node-a"), Exec: journal, LookPath: only("journalctl"), Statfs: fixedStatfs, Now: clk.Now,
		Logger: slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
	})
	ctx := context.Background()
	s.Prime(ctx)
	if strings.Contains(codes(s.Results()), "LAI-LLM-027") {
		t.Fatal("no kill yet")
	}
	s.sampleOOM(ctx)
	if ev, ok := s.LastOOM(); !ok || ev.PID != 4242 {
		t.Fatalf("last OOM = %+v, %v", ev, ok)
	}
	if !strings.Contains(codes(s.Results()), "LAI-LLM-027") {
		t.Fatalf("results = %q", codes(s.Results()))
	}
	clk.Advance(OOMHold + time.Minute)
	s.Refresh(ctx)
	if strings.Contains(codes(s.Results()), "LAI-LLM-027") {
		t.Errorf("the alert must end after the hold window: %q", codes(s.Results()))
	}
	s.ObserveServiceResult("oom-kill")
	if !strings.Contains(codes(s.Results()), "LAI-LLM-027") {
		t.Errorf("the service watcher's verdict must raise it: %q", codes(s.Results()))
	}
}

func itoa(v int64) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func TestDetectBackend(t *testing.T) {
	smiList := func(out string, exitCode int) *execfake.Runner {
		return execfake.New().Expect(gpuKey(nvidiaReasons), execx.Result{Stdout: []byte(out), ExitCode: exitCode}, nil)
	}
	cases := []struct {
		name string
		fsys fstest.MapFS
		run  execx.Runner
		look func(string) (string, error)
		want string
	}{
		{"driver proc file means cuda without running anything", compose(t, "sysfs/nvidia-node"), execfake.New(), only(), BackendCUDA},
		{"nvidia-smi listing a GPU", fstest.MapFS{}, smiList(string(testdata(t, "nvidia-smi/normal.csv")), 0), only("nvidia-smi"), BackendCUDA},
		{"nvidia-smi installed but failing, no AMD", fstest.MapFS{}, smiList("", 9), only("nvidia-smi"), BackendCPU},
		{"nvidia-smi installed but listing nothing", fstest.MapFS{}, smiList("", 0), only("nvidia-smi"), BackendCPU},
		{"AMD card", compose(t, "sysfs/amd-dedicated"), execfake.New(), only(), BackendVulkan},
		{"NVIDIA wins over AMD", compose(t, "sysfs/nvidia-node", "sysfs/amd-dedicated"), execfake.New(), only(), BackendCUDA},
		{"nothing", fstest.MapFS{}, execfake.New(), only(), BackendCPU},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := New(Options{FS: c.fsys, Exec: c.run, LookPath: c.look, Statfs: fixedStatfs})
			if got := s.DetectBackend(); got != c.want {
				t.Errorf("DetectBackend = %q, want %q", got, c.want)
			}
		})
	}
	// The package-level function works on whatever machine runs the tests.
	switch got := DetectBackend(); got {
	case BackendCUDA, BackendVulkan, BackendCPU:
	default:
		t.Errorf("DetectBackend() = %q", got)
	}
}

func TestSampleGPUsInProcess(t *testing.T) {
	r := &smiRunner{t: t}
	s, _, _ := nodeService(t, r)
	gpus, err := s.SampleGPUs(context.Background())
	if err != nil || len(gpus) != 1 || gpus[0].VRAMUsedMiB != 6791 {
		t.Fatalf("gpus = %+v, err = %v", gpus, err)
	}
	if !s.Snapshot().SampledAt.IsZero() {
		t.Error("SampleGPUs must not touch the cached view")
	}
	r.fail.Store(true)
	if _, err := s.SampleGPUs(context.Background()); err == nil {
		t.Error("a failing driver must be an error")
	}
	// A node with no GPU at all is an empty list, not an error.
	none := New(Options{FS: fstest.MapFS{}, Exec: execfake.New(), LookPath: only()})
	if gpus, err := none.SampleGPUs(context.Background()); err != nil || len(gpus) != 0 {
		t.Errorf("no GPU: %+v, %v", gpus, err)
	}
}

func TestStartRefreshesOnItsOwnAndStopWaits(t *testing.T) {
	r := &smiRunner{t: t}
	s, _, _ := nodeService(t, profileOK(r), func(o *Options) { o.FastEvery = 10 * time.Millisecond; o.SlowEvery = 20 * time.Millisecond })
	s.Prime(context.Background())
	before := r.calls.Load()
	s.Start(context.Background())
	s.Start(context.Background()) // idempotent
	deadline := time.Now().Add(3 * time.Second)
	for r.calls.Load() < before+6 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if r.calls.Load() < before+6 {
		t.Fatalf("the loop did not sample: %d calls", r.calls.Load()-before)
	}
	s.Stop()
	s.Stop() // idempotent
	after := r.calls.Load()
	time.Sleep(60 * time.Millisecond)
	if r.calls.Load() != after {
		t.Error("the loop kept sampling after Stop")
	}
	New(Options{}).Stop() // Stop without Start is safe
}

func TestSnapshotsAreIndependentCopies(t *testing.T) {
	s, _, _ := nodeService(t, profileOK(&smiRunner{t: t}))
	s.Prime(context.Background())
	a := s.Snapshot()
	a.GPUs[0].Name = "changed"
	a.GPUs[0].Processes[0].Name = "changed"
	a.Disks[0].Path = "changed"
	a.Thermal.Zones[0].Type = "changed"
	b := s.Snapshot()
	if b.GPUs[0].Name == "changed" || b.GPUs[0].Processes[0].Name == "changed" || b.Disks[0].Path == "changed" || b.Thermal.Zones[0].Type == "changed" {
		t.Error("a caller's edit reached the sampler's state")
	}
}

func TestConcurrentSnapshotsAndRefreshes(t *testing.T) {
	s, _, _ := nodeService(t, profileOK(&smiRunner{t: t}))
	s.Prime(context.Background())
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for j := 0; j < 5; j++ {
				s.Refresh(context.Background())
			}
		}()
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				_ = s.Snapshot()
				_ = s.Results()
			}
		}()
	}
	wg.Wait()
}

func TestRefreshWithACancelledContextReturnsTheCachedView(t *testing.T) {
	s, _, _ := nodeService(t, profileOK(&smiRunner{t: t}))
	s.Prime(context.Background())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// Hold the round so acquire cannot succeed.
	s.sem <- struct{}{}
	snap := s.Refresh(ctx)
	<-s.sem
	if len(snap.GPUs) != 1 {
		t.Errorf("snapshot = %+v", snap)
	}
}

func TestChecksPartitionResultsByGroup(t *testing.T) {
	s := New(Options{FS: fixture(t, "sysfs/battery-discharging-energy")})
	s.mu.Lock()
	s.st.results = []Result{
		newResult(CodeOnBattery, nil), newResult(CodeRAMLow, nil), newResult(CodeGPUHot, nil), newResult(CodeOOMKill, nil),
	}
	s.mu.Unlock()
	byID := map[string]string{}
	for _, c := range s.Checks() {
		byID[c.ID] = codes(c.Run(context.Background()))
	}
	want := map[string]string{
		CheckGPU: "", CheckMemory: "LAI-HW-006", CheckDisk: "", CheckThermal: "LAI-HW-009", CheckPower: "LAI-HW-012", CheckOOM: "LAI-LLM-027",
	}
	for id, w := range want {
		if byID[id] != w {
			t.Errorf("%s: %q, want %q", id, byID[id], w)
		}
	}
	if len(s.Checks()) != 6 {
		t.Errorf("%d checks", len(s.Checks()))
	}
}

func TestProbeFailuresAreLoggedOncePerEpisode(t *testing.T) {
	var buf syncBuffer
	r := &smiRunner{t: t}
	s, _, _ := nodeService(t, profileOK(r), func(o *Options) {
		o.Logger = slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	})
	ctx := context.Background()
	s.Prime(ctx)
	r.fail.Store(true)
	for i := 0; i < 5; i++ {
		s.Refresh(ctx)
	}
	if n := strings.Count(buf.String(), "hardware probe failed"); n != 1 {
		t.Errorf("failure logged %d times, want once per episode:\n%s", n, buf.String())
	}
	r.fail.Store(false)
	s.Refresh(ctx)
	if !strings.Contains(buf.String(), "hardware probe recovered") {
		t.Errorf("recovery not logged:\n%s", buf.String())
	}
	if strings.Contains(strings.ToLower(buf.String()), "uuid") {
		t.Error("log mentions a uuid")
	}
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func TestFitMemoryByBackend(t *testing.T) {
	nv := GPU{Vendor: VendorNVIDIA, VRAMTotalMiB: 8192, VRAMUsedByOthersMiB: ptr[int64](100)}
	nv2 := GPU{Vendor: VendorNVIDIA, VRAMTotalMiB: 12288, VRAMUsedByOthersMiB: ptr[int64](50)}
	amd := GPU{Vendor: VendorAMD, VRAMTotalMiB: 62464, UnifiedMemory: true, VRAMUsedByOthersMiB: ptr[int64](1280)}
	amdUnknown := GPU{Vendor: VendorAMD, VRAMTotalMiB: 16384}
	cases := []struct {
		name    string
		gpus    []GPU
		backend string
		fit     int64
		others  int64
		unified bool
		hottest bool
	}{
		{"cuda sums NVIDIA GPUs", []GPU{nv, nv2}, BackendCUDA, 20480, 150, false, false},
		{"cuda ignores the AMD iGPU of a hybrid laptop", []GPU{amd, nv}, BackendCUDA, 8192, 100, false, false},
		{"vulkan on hybrid uses the AMD memory", []GPU{amd, nv}, BackendVulkan, 62464, 1280, true, false},
		{"vulkan with only NVIDIA uses its VRAM", []GPU{nv}, BackendVulkan, 8192, 100, false, false},
		{"cpu has no GPU memory", []GPU{nv, amd}, BackendCPU, 0, 0, false, false},
		{"unknown others counts as zero", []GPU{amdUnknown}, BackendVulkan, 16384, 0, false, false},
		{"no GPUs", nil, BackendCUDA, 0, 0, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := Snapshot{GPUs: c.gpus, Backend: c.backend}
			if s.FitMemory(c.backend) != c.fit || s.OthersMiB(c.backend) != c.others || s.UnifiedMemory(c.backend) != c.unified {
				t.Errorf("fit=%d others=%d unified=%v", s.FitMemory(c.backend), s.OthersMiB(c.backend), s.UnifiedMemory(c.backend))
			}
			if s.GPUMemoryForFit() != c.fit || s.GOthersMiB() != c.others {
				t.Errorf("derived values differ: %d %d", s.GPUMemoryForFit(), s.GOthersMiB())
			}
		})
	}
	if _, ok := (Snapshot{}).HottestGPUTempC(); ok {
		t.Error("no GPU has no temperature")
	}
	hot := Snapshot{GPUs: []GPU{{TempC: ptr(60.0)}, {}, {TempC: ptr(71.5)}}}
	if v, ok := hot.HottestGPUTempC(); !ok || v != 71.5 {
		t.Errorf("hottest = %v", v)
	}
	if _, ok := (Snapshot{}).InternalRoot(); ok {
		t.Error("no internal root")
	}
}

func TestFitUsedMiBByBackend(t *testing.T) {
	nv := GPU{Vendor: VendorNVIDIA, VRAMUsedMiB: 6000}
	nv2 := GPU{Vendor: VendorNVIDIA, VRAMUsedMiB: 500}
	amd := GPU{Vendor: VendorAMD, UnifiedMemory: true, VRAMUsedMiB: 41000}
	cases := []struct {
		name    string
		gpus    []GPU
		backend string
		want    int64
		count   int
	}{
		{"cuda sums NVIDIA GPUs", []GPU{nv, nv2}, BackendCUDA, 6500, 2},
		{"cuda ignores the AMD iGPU", []GPU{amd, nv}, BackendCUDA, 6000, 1},
		{"vulkan on hybrid counts the AMD memory", []GPU{amd, nv}, BackendVulkan, 41000, 1},
		{"vulkan with only NVIDIA", []GPU{nv}, BackendVulkan, 6000, 1},
		{"cpu has no GPU memory", []GPU{nv, amd}, BackendCPU, 0, 0},
		{"no GPUs", nil, BackendCUDA, 0, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := Snapshot{GPUs: c.gpus}
			if got := s.FitUsedMiB(c.backend); got != c.want {
				t.Errorf("FitUsedMiB = %d, want %d", got, c.want)
			}
			if got := len(s.FitGPUs(c.backend)); got != c.count {
				t.Errorf("FitGPUs = %d entries, want %d", got, c.count)
			}
		})
	}
}
