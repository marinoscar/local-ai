package cloud

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/hardware"
)

func TestExtraKeysAreSixteenAndMatchTheEABPattern(t *testing.T) {
	if len(ExtraKeys) != 16 {
		t.Fatalf("%d extra keys, the EAB maximum is 16", len(ExtraKeys))
	}
	seen := map[string]bool{}
	for _, k := range ExtraKeys {
		if !ExtraKeyPattern.MatchString(k) || seen[k] {
			t.Errorf("key %q breaks the pattern or repeats", k)
		}
		seen[k] = true
	}
	x := BuildExtra(baseStatus(), baseHardware(), t0)
	if len(x) > 16 {
		t.Fatalf("BuildExtra returned %d keys", len(x))
	}
	for k := range x {
		if !seen[k] {
			t.Errorf("BuildExtra invented %q", k)
		}
	}
}

func TestBuildExtraOmitsWhatItCannotComputeAndNeverSendsNullOrNaN(t *testing.T) {
	st := baseStatus()
	st.Hardware.VRAMUsedMiB, st.Hardware.VRAMTotalMiB, st.Hardware.GPUTempC, st.Hardware.CPUTempC = nil, nil, nil, nil
	st.Hardware.GPUs = nil
	st.Node.CatalogVersion = nil
	st.Services = nil
	x := BuildExtra(st, hardware.Snapshot{}, t0)
	for _, k := range []string{"localai.vram_used_mib", "localai.vram_total_mib", "localai.gpu_temp_c", "localai.cpu_temp_c", "localai.gpu_util_pct",
		"localai.ark_internal_free_gib", "localai.sync_age_h", "localai.catalog_version"} {
		if _, ok := x[k]; ok {
			t.Errorf("%s should be omitted, got %v", k, x[k])
		}
	}
	if x["localai.llama_up"] != 0 {
		t.Error("llama_up without a service must be 0")
	}
	raw, err := json.Marshal(x)
	if err != nil {
		t.Fatal(err)
	}
	var back map[string]any
	_ = json.Unmarshal(raw, &back)
	for k, v := range back {
		if v == nil {
			t.Errorf("%s is null", k)
		}
	}
}

func TestSanitizeExtraDropsOutOfRangeAndForeignKeys(t *testing.T) {
	x := sanitizeExtra(map[string]float64{
		"localai.models_loaded": 1, "localai.vram_used_mib": math.NaN(), "localai.vram_total_mib": math.Inf(1),
		"localai.cpu_temp_c": 2e15, "localai.llama_up": -2e15, "other.key": 1, "localai.alerts_active": 1e15,
	})
	if len(x) != 2 || x["localai.models_loaded"] != 1 || x["localai.alerts_active"] != 1e15 {
		t.Errorf("sanitized %v", x)
	}
}

func TestEncodeCatalogVersion(t *testing.T) {
	for in, want := range map[string]float64{"2026.11.02-1": 20261102.1, "2026.11.02-12": 20261102.12, "2026.01.31-3": 20260131.3} {
		if got, ok := EncodeCatalogVersion(in); !ok || got != want {
			t.Errorf("%s = %v %v, want %v", in, got, ok, want)
		}
	}
	for _, bad := range []string{"", "2026.11.02", "v1", "2026-11-02-1", "2026.11.02-x"} {
		if _, ok := EncodeCatalogVersion(bad); ok {
			t.Errorf("%q accepted", bad)
		}
		if ValidCatalogVersion(bad) {
			t.Errorf("%q valid", bad)
		}
	}
}

func TestBuildVitalsClampsToTheEABBounds(t *testing.T) {
	hw := baseHardware()
	hw.Self = hardware.Self{CPUPercent: 250, RSSMiB: 40}
	big := int64(30 * 24 * 3600)
	hw.Power.TimeRemainingS = &big
	pct := 140
	hw.Power.BatteryPercent = &pct
	host := &HostVitals{CPUPercent: -3, CPUCount: 0, LoadAvg1: -1, OSRelease: string(make([]byte, 300)), Arch: "x86_64", MemTotalBytes: -5}
	v := BuildVitals(VitalsInput{Now: t0, StartedAt: t0.Add(-time.Hour), CLIVersion: "aiagent/0.1.0", HW: hw, Status: baseStatus(), Host: host, SlotsUsed: 99, SlotsTotal: 0})
	if v.CPUPercent != 100 || v.UptimeSeconds != 3600 || v.SlotsUsed != 64 || v.SlotsTotal != 1 || v.RSSBytes != 40<<20 {
		t.Errorf("vitals %+v", v)
	}
	if v.Host.CPUPercent != 0 || v.Host.CPUCount != 1 || v.Host.LoadAvg1 != 0 || len(v.Host.OSRelease) != 128 || v.Host.MemTotalBytes != 0 {
		t.Errorf("host %+v", v.Host)
	}
	if *v.Power.BatteryPercent != 100 || *v.Power.TimeRemainingSeconds != 7*24*3600 {
		t.Errorf("power %+v", v.Power)
	}
	if v.StateDirFreeBytes != 293_000_000_000 || v.StateDirTotalBytes != 342_000_000_000 {
		t.Errorf("state dir %d/%d", v.StateDirFreeBytes, v.StateDirTotalBytes)
	}
	// Without the host feature nothing of host or power exists; with NoExtra, no extra.
	v2 := BuildVitals(VitalsInput{Now: t0, HW: baseHardware(), Status: baseStatus(), NoExtra: true})
	if v2.Host != nil || v2.Power != nil || v2.Extra != nil {
		t.Errorf("gating: %+v", v2)
	}
}

func TestCountersWire(t *testing.T) {
	var c Counters
	c.Claims.Add(12)
	c.EmptyPolls.Add(5)
	c.HeartbeatFailures.Add(2)
	raw, _ := json.Marshal(c.Wire())
	var m map[string]float64
	_ = json.Unmarshal(raw, &m)
	for _, k := range []string{"claims", "emptyPolls", "claimFailures", "succeeded", "failed", "rateLimited", "leaseRenewals", "leaseRenewFailures", "heartbeatFailures", "watchdogTrips"} {
		if _, ok := m[k]; !ok {
			t.Errorf("counter %s missing", k)
		}
	}
	if m["claims"] != 12 || m["heartbeatFailures"] != 2 || c.Map()["cloud_claims"] != int64(12) {
		t.Errorf("counters %v / %v", m, c.Map())
	}
}

func TestHostParsers(t *testing.T) {
	if a, b, c := parseLoadAvg("0.40 0.50 0.60 1/234 5678\n"); a != 0.4 || b != 0.5 || c != 0.6 {
		t.Errorf("loadavg %v %v %v", a, b, c)
	}
	if a, _, _ := parseLoadAvg("garbage"); a != 0 {
		t.Error("garbage loadavg")
	}
	total, free, avail := parseMemInfo("MemTotal:       14680064 kB\nMemFree:         1048576 kB\nMemAvailable:    5242880 kB\nBuffers: 1 kB\nBad line\nSwapTotal: x kB\n")
	if total != 14680064*1024 || free != 1048576*1024 || avail != 5242880*1024 {
		t.Errorf("meminfo %d %d %d", total, free, avail)
	}
	if parseUptime("172800.55 1000.2\n") != 172800 || parseUptime("") != 0 || parseUptime("-1 2") != 0 {
		t.Error("uptime")
	}
	if got := parseOSRelease("NAME=\"Test OS\"\nPRETTY_NAME=\"Test OS 1.0 LTS\"\nID=test\n"); got != "Test OS 1.0 LTS" {
		t.Errorf("os-release %q", got)
	}
	if archName("amd64") != "x86_64" || archName("arm64") != "aarch64" || archName("riscv64") != "riscv64" {
		t.Error("arch names")
	}
	if total, idle, ok := parseCPUStat("cpu  100 0 100 700 100 0 0 0 0 0\ncpu0 1 1 1 1 1 0 0 0 0 0\n"); !ok || total != 1000 || idle != 800 {
		t.Errorf("cpu stat %d %d %v", total, idle, ok)
	}
	if _, _, ok := parseCPUStat("intr 1 2 3"); ok {
		t.Error("a file without a cpu line")
	}
}

func TestProcHostReadsAFakeRootAndComputesCPUPercentFromDeltas(t *testing.T) {
	root := t.TempDir()
	write := func(rel, content string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("proc/loadavg", "0.40 0.50 0.60 1/2 3\n")
	write("proc/meminfo", "MemTotal: 1024 kB\nMemFree: 512 kB\nMemAvailable: 768 kB\n")
	write("proc/uptime", "100.5 1.0\n")
	write("etc/os-release", "PRETTY_NAME=\"Fixture OS\"\n")
	write("proc/stat", "cpu  100 0 100 700 100 0 0 0 0 0\n")
	p := &ProcHost{Root: root}
	h := p.Host()
	if h.CPUPercent != 0 || h.LoadAvg5 != 0.5 || h.MemTotalBytes != 1024*1024 || h.UptimeSeconds != 100 || h.OSRelease != "Fixture OS" || h.CPUCount < 1 || h.Arch == "" {
		t.Errorf("first sample %+v", h)
	}
	// Between the samples 100 of 200 new jiffies were busy.
	write("proc/stat", "cpu  150 0 150 750 150 0 0 0 0 0\n")
	if h := p.Host(); math.Abs(h.CPUPercent-50) > 0.01 {
		t.Errorf("cpu percent %v", h.CPUPercent)
	}
	// A machine without /proc files yields zeros, never an error.
	empty := (&ProcHost{Root: t.TempDir()}).Host()
	if empty.MemTotalBytes != 0 || empty.OSRelease != "" {
		t.Errorf("empty root %+v", empty)
	}
}
