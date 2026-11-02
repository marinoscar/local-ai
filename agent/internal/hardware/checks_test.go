package hardware

import (
	"sort"
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 11, 2, 15, 0, 0, 0, time.UTC)

// codes returns the sorted codes of the results.
func codes(rs []Result) string {
	var c []string
	for _, r := range rs {
		c = append(c, r.Code)
	}
	sort.Strings(c)
	return strings.Join(c, ",")
}

func baseSnap() Snapshot {
	return Snapshot{
		SampledAt: t0,
		RAM:       RAM{TotalMiB: 16000, AvailableMiB: 8000, SwapTotalMiB: 4000, SwapFreeMiB: 4000},
		Power:     Power{Source: SourceAC},
		Thermal:   Thermal{Zones: []ThermalZone{}},
		Backend:   BackendCPU,
	}
}

func input(s Snapshot) evalInput { return evalInput{snap: s, now: t0, tickFast: true} }

// run feeds one fast sample per snapshot and returns the codes after each.
func run(ev *evaluator, mod func(in *evalInput), snaps ...Snapshot) []string {
	var out []string
	for _, s := range snaps {
		in := input(s)
		if mod != nil {
			mod(&in)
		}
		out = append(out, codes(ev.evaluate(in)))
	}
	return out
}

func batterySnap(source string, pct int) Snapshot {
	s := baseSnap()
	s.Power = Power{Source: source, BatteryPercent: ptr(pct)}
	return s
}

func TestBatteryThresholdsWithHysteresis(t *testing.T) {
	ev := &evaluator{}
	steps := []struct {
		source string
		pct    int
		want   string
	}{
		{SourceAC, 100, ""},
		{SourceBattery, 64, "LAI-HW-012"},
		{SourceBattery, 21, "LAI-HW-012"},
		{SourceBattery, 19, "LAI-HW-012,LAI-HW-013"},
		{SourceBattery, 24, "LAI-HW-012,LAI-HW-013"}, // still inside the 5-point re-arm band
		{SourceBattery, 25, "LAI-HW-012"},            // re-armed
		{SourceBattery, 9, "LAI-HW-012,LAI-HW-014"},  // critical replaces low
		{SourceBattery, 14, "LAI-HW-012,LAI-HW-014"}, // critical holds until 15
		{SourceBattery, 15, "LAI-HW-012,LAI-HW-013"}, // critical re-armed, low still latched (<25)
		{SourceBattery, 26, "LAI-HW-012"},
		{SourceBattery, 5, "LAI-HW-012,LAI-HW-014"},
		{SourceAC, 5, ""}, // plugging in clears everything at once
		{SourceBattery, 8, "LAI-HW-012,LAI-HW-014"},
	}
	for i, st := range steps {
		got := codes(ev.evaluate(input(batterySnap(st.source, st.pct))))
		if got != st.want {
			t.Fatalf("step %d (%s %d%%): codes = %q, want %q", i, st.source, st.pct, got, st.want)
		}
	}
}

func TestBatteryResultsCarryTheTextAndSeverity(t *testing.T) {
	ev := &evaluator{}
	s := batterySnap(SourceBattery, 8)
	s.Power.TimeRemainingS = ptr[int64](900)
	var crit Result
	for _, r := range ev.evaluate(input(s)) {
		if r.Code == CodeBatteryCrit {
			crit = r
		}
	}
	if crit.Check != CheckPower || crit.Severity != SeverityCritical || crit.Title != "Battery below 10%" || crit.Fix == "" {
		t.Errorf("result = %+v", crit)
	}
	if crit.Details["battery_percent"] != 8 {
		t.Errorf("details = %v", crit.Details)
	}
}

func TestOnACOrWithoutBatteryNothingBatteryRelatedIsRaised(t *testing.T) {
	ev := &evaluator{}
	desktop := baseSnap()
	desktop.Power = Power{Source: SourceAC}
	unknown := baseSnap()
	unknown.Power = Power{Source: SourceUnknown, BatteryPercent: ptr(3)}
	for _, got := range run(ev, nil, desktop, unknown, batterySnap(SourceAC, 3)) {
		if got != "" {
			t.Errorf("codes = %q, want none", got)
		}
	}
}

func TestPowerProfileIsInfoOnACOnly(t *testing.T) {
	ev := &evaluator{}
	s := baseSnap()
	s.Power.Profile = ptr("balanced")
	got := ev.evaluate(input(s))
	if codes(got) != "LAI-HW-015" || got[0].Severity != SeverityInfo {
		t.Errorf("on AC with balanced: %+v", got)
	}
	s.Power.Profile = ptr("performance")
	if c := codes(ev.evaluate(input(s))); c != "" {
		t.Errorf("performance: %q", c)
	}
	s.Power = Power{Source: SourceBattery, BatteryPercent: ptr(80), Profile: ptr("power-saver")}
	if c := codes(ev.evaluate(input(s))); c != "LAI-HW-012" {
		t.Errorf("on battery with power-saver: %q (015 is for AC only)", c)
	}
	s.Power = Power{Source: SourceAC}
	if c := codes(ev.evaluate(input(s))); c != "" {
		t.Errorf("unknown profile: %q", c)
	}
}

func ramSnap(availMiB int64) Snapshot {
	s := baseSnap()
	s.RAM = RAM{TotalMiB: 10000, AvailableMiB: availMiB, SwapTotalMiB: 4000, SwapFreeMiB: 4000}
	return s
}

func TestRAMLowNeedsThreeSamplesAndClearsTwoPointsBack(t *testing.T) {
	ev := &evaluator{}
	got := run(ev, nil,
		ramSnap(400), ramSnap(400), // 4 %: two samples are not enough
		ramSnap(900),                             // 9 %: the streak breaks
		ramSnap(400), ramSnap(400), ramSnap(400), // three in a row raise it
		ramSnap(600), // 6 %: back above 5 but not 2 points back
		ramSnap(700), // 7 %: re-armed
		ramSnap(400), // one bad sample only
	)
	want := []string{"", "", "", "", "", "LAI-HW-006", "LAI-HW-006", "", ""}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("sample %d: codes = %q, want %q", i, got[i], want[i])
		}
	}
}

func swapSnap(totalMiB, freeMiB int64) Snapshot {
	s := baseSnap()
	s.RAM = RAM{TotalMiB: 16000, AvailableMiB: 8000, SwapTotalMiB: totalMiB, SwapFreeMiB: freeMiB}
	return s
}

func TestSwapHeavyOnlyWhenThereIsSwap(t *testing.T) {
	ev := &evaluator{}
	got := run(ev, nil,
		swapSnap(4000, 1500), swapSnap(4000, 1500), swapSnap(4000, 1500), // 62.5 % used
		swapSnap(4000, 2100),                           // 47.5 %: below 48, re-armed
		swapSnap(0, 0), swapSnap(0, 0), swapSnap(0, 0), // no swap at all: never raised
	)
	want := []string{"", "", "LAI-HW-016", "", "", "", ""}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("sample %d: codes = %q, want %q", i, got[i], want[i])
		}
	}
}

func gpuSnap(vendor string, total, used int64, others *int64) Snapshot {
	s := baseSnap()
	backend := BackendCUDA
	if vendor == VendorAMD {
		backend = BackendVulkan
	}
	s.Backend = backend
	s.GPUs = []GPU{{Vendor: vendor, Backend: backend, VRAMTotalMiB: total, VRAMUsedMiB: used, VRAMUsedByOthersMiB: others, Throttling: []string{}, Processes: []GPUProcess{}}}
	return s
}

func TestOthersVRAMOverQuarterForThreeSamples(t *testing.T) {
	ev := &evaluator{}
	big, small := ptr[int64](2200), ptr[int64](200) // of 8192: 26.9 % and 2.4 %
	got := run(ev, nil,
		gpuSnap(VendorNVIDIA, 8192, 6000, big), gpuSnap(VendorNVIDIA, 8192, 6000, big),
		gpuSnap(VendorNVIDIA, 8192, 6000, big),
		gpuSnap(VendorNVIDIA, 8192, 6000, ptr[int64](1950)), // 23.8 %: below 23? no, above 23 -> stays
		gpuSnap(VendorNVIDIA, 8192, 6000, small),
	)
	want := []string{"", "", "LAI-HW-005", "LAI-HW-005", ""}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("sample %d: codes = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestOthersVRAMIsSkippedWhenNotMeasurable(t *testing.T) {
	ev := &evaluator{}
	for _, got := range run(ev, nil, gpuSnap(VendorAMD, 62464, 50000, nil), gpuSnap(VendorAMD, 62464, 50000, nil), gpuSnap(VendorAMD, 62464, 50000, nil)) {
		if got != "" {
			t.Errorf("codes = %q, want none (AMD share unknown)", got)
		}
	}
}

func TestGPUAndCPUHeat(t *testing.T) {
	ev := &evaluator{}
	hot := func(gpuT, cpuT float64) Snapshot {
		s := gpuSnap(VendorNVIDIA, 8192, 100, ptr[int64](0))
		s.GPUs[0].TempC = ptr(gpuT)
		s.Thermal.CPUTempC = ptr(cpuT)
		return s
	}
	got := run(ev, nil,
		hot(86, 94), hot(87, 95), hot(84, 92), hot(81.9, 89.9), hot(70, 60),
	)
	want := []string{"", "LAI-HW-009,LAI-HW-010", "LAI-HW-009,LAI-HW-010", "", ""}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("sample %d: codes = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestThrottlingNeedsThreeConsecutiveSamples(t *testing.T) {
	ev := &evaluator{}
	thr := func(reasons ...string) Snapshot {
		s := gpuSnap(VendorNVIDIA, 8192, 100, ptr[int64](0))
		s.GPUs[0].Throttling = reasons
		return s
	}
	got := run(ev, nil,
		thr(ThrottleThermal), thr(ThrottleThermal), thr(), // broken after two
		thr(ThrottleThermal), thr(ThrottlePowerBrake), thr(ThrottleThermal), // three in a row
		thr(ThrottleThermal), thr(), // clears on the first clean sample
	)
	want := []string{"", "", "", "", "", "LAI-HW-011", "LAI-HW-011", ""}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("sample %d: codes = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestCPUThrottleCounterDeltaCountsAsThrottling(t *testing.T) {
	ev := &evaluator{}
	cpu := func(events int64) Snapshot {
		s := baseSnap()
		s.Thermal.CPUThrottleEvents = events
		return s
	}
	got := run(ev, nil,
		cpu(100),                     // baseline: a counter that is already high is not throttling now
		cpu(101), cpu(105), cpu(106), // three consecutive deltas
		cpu(106), // no delta: clear
	)
	want := []string{"", "", "", "LAI-HW-011", ""}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("sample %d: codes = %q, want %q", i, got[i], want[i])
		}
	}
}

func diskSnap(root, state Disk) Snapshot {
	s := baseSnap()
	s.Disks = []Disk{root, state}
	return s
}

func TestDiskThresholds(t *testing.T) {
	d := func(path, role string, freePct int64) Disk {
		return Disk{Path: path, Role: role, FS: "ext4", TotalBytes: 1000, FreeBytes: freePct * 10}
	}
	ev := &evaluator{}
	in := func(s Snapshot) evalInput {
		i := input(s)
		i.tickFast, i.tickDisks = false, true
		i.minFreeBytes, i.haveMinFree = 300, true
		return i
	}
	steps := []struct {
		root, state int64
		want        string
	}{
		{50, 50, ""},
		{25, 50, "LAI-HW-007"},           // 250 < 300 bytes of the profile's minimum
		{25, 4, "LAI-HW-007,LAI-HW-008"}, // state under 5 % free
		{50, 6, "LAI-HW-008"},            // 6 % is still inside the 2-point re-arm band
		{50, 7, ""},                      // re-armed
		{50, 4, "LAI-HW-008"},
	}
	for i, st := range steps {
		got := codes(ev.evaluate(in(diskSnap(d(InternalRootPath, RoleInternalRoot, st.root), d(StatePath, RoleState, st.state)))))
		if got != st.want {
			t.Errorf("step %d: codes = %q, want %q", i, got, st.want)
		}
	}
	// Without a catalog LAI-HW-007 is skipped.
	i := in(diskSnap(d(InternalRootPath, RoleInternalRoot, 25), d(StatePath, RoleState, 50)))
	i.haveMinFree = false
	if got := codes((&evaluator{}).evaluate(i)); got != "" {
		t.Errorf("no profile: codes = %q", got)
	}
}

func TestDiskFullNamesTheFilesystems(t *testing.T) {
	ev := &evaluator{}
	s := baseSnap()
	s.Disks = []Disk{
		{Path: StatePath, Role: RoleState, TotalBytes: 1000, FreeBytes: 10},
		{Path: EtcPath, Role: RoleEtc, TotalBytes: 1000, FreeBytes: 10},
		{Path: OptPath, Role: RoleOpt, TotalBytes: 1000, FreeBytes: 900},
	}
	in := input(s)
	in.tickFast, in.tickDisks = false, true
	rs := ev.evaluate(in)
	if codes(rs) != "LAI-HW-008" || rs[0].Severity != SeverityCritical {
		t.Fatalf("results = %+v", rs)
	}
	paths, _ := rs[0].Details["paths"].([]string)
	if len(paths) != 2 || paths[0] != EtcPath || paths[1] != StatePath {
		t.Errorf("paths = %v", rs[0].Details["paths"])
	}
}

func TestStaleSamplesDoNotMoveTheLatches(t *testing.T) {
	ev := &evaluator{}
	good := ramSnap(400)
	stale := ramSnap(9000)
	stale.RAM.Stale = true
	// Two bad samples, then a stale one (ignored: it neither breaks nor extends the streak), then the third.
	got := run(ev, nil, good, good, stale, good)
	if got[3] != "LAI-HW-006" || got[2] != "" {
		t.Errorf("codes = %v", got)
	}
	// A raised finding is not cleared by a stale reading that looks healthy.
	if got := run(ev, nil, stale); got[0] != "LAI-HW-006" {
		t.Errorf("a stale healthy sample cleared the finding: %v", got)
	}
}

func TestRevaluationWithoutATickDoesNotAdvanceStreaks(t *testing.T) {
	ev := &evaluator{}
	s := ramSnap(400)
	for i := 0; i < 10; i++ { // e.g. ten OOM checks or service verdicts between samples
		in := input(s)
		in.tickFast = false
		if got := codes(ev.evaluate(in)); got != "" {
			t.Fatalf("re-evaluation %d raised %q without a sample", i, got)
		}
	}
	run(ev, nil, s, s)
	if got := codes(ev.evaluate(input(s))); got != "LAI-HW-006" {
		t.Errorf("third real sample: %q", got)
	}
}

func TestNVIDIADriverFailureAlertsAfterThree(t *testing.T) {
	ev := &evaluator{}
	for failures, want := range map[int]string{0: "", 1: "", 2: "", 3: "LAI-HW-002", 7: "LAI-HW-002"} {
		in := input(baseSnap())
		in.nvidiaDriver, in.nvidiaFailures = true, failures
		if got := codes(ev.evaluate(in)); got != want {
			t.Errorf("%d failures: %q, want %q", failures, got, want)
		}
	}
	in := input(baseSnap())
	in.nvidiaDriver, in.nvidiaFailures = false, 9 // a leftover binary with no driver is not an alarm
	if got := codes(ev.evaluate(in)); got != "" {
		t.Errorf("no driver: %q", got)
	}
}

func TestAMDAccessResults(t *testing.T) {
	ev := &evaluator{}
	in := input(baseSnap())
	in.amdPresent = true
	in.amdAccess = &AMDAccess{MissingDevices: []string{"/dev/kfd"}, MissingGroups: []string{"render"}}
	if got := codes(ev.evaluate(in)); got != "LAI-HW-003,LAI-HW-004" {
		t.Errorf("codes = %q", got)
	}
	in.amdAccess = &AMDAccess{}
	if got := codes(ev.evaluate(in)); got != "" {
		t.Errorf("healthy: %q", got)
	}
	in.amdPresent, in.amdAccess = false, &AMDAccess{MissingDevices: []string{"/dev/kfd"}}
	if got := codes(ev.evaluate(in)); got != "" {
		t.Errorf("no AMD GPU: %q", got)
	}
}

func TestBackendMismatchResult(t *testing.T) {
	for _, c := range []struct {
		configured    string
		nvidia, amd   bool
		sampled, want bool
	}{
		{"cuda", false, true, true, true},
		{"cuda", true, false, true, false},
		{"vulkan", false, false, true, true},
		{"cuda", false, false, false, false}, // before the first sample nothing is known
		{"", false, false, true, false},
	} {
		s := baseSnap()
		if !c.sampled {
			s.SampledAt = time.Time{}
		}
		in := input(s)
		in.configuredBackend, in.nvidiaPresent, in.amdPresent = c.configured, c.nvidia, c.amd
		got := codes((&evaluator{}).evaluate(in)) == "LAI-HW-017"
		if got != c.want {
			t.Errorf("%+v: raised = %v", c, got)
		}
	}
}

func TestOOMResultStaysForTheHoldWindow(t *testing.T) {
	ev := &evaluator{}
	ev0 := OOMEvent{At: t0, PID: 4242, Process: llamaProcess, Source: OOMSourceJournal}
	for _, c := range []struct {
		after time.Duration
		want  string
	}{{time.Minute, "LAI-LLM-027"}, {14 * time.Minute, "LAI-LLM-027"}, {OOMHold, ""}, {time.Hour, ""}} {
		in := input(baseSnap())
		in.lastOOM, in.now = &ev0, t0.Add(c.after)
		rs := ev.evaluate(in)
		if codes(rs) != c.want {
			t.Errorf("after %v: %q, want %q", c.after, codes(rs), c.want)
		}
		if c.want != "" && (rs[0].Check != CheckOOM || rs[0].Severity != SeverityError) {
			t.Errorf("result = %+v", rs[0])
		}
	}
}

func TestEveryCodeHasTextAndACheck(t *testing.T) {
	for code, ci := range codeTable {
		if ci.check == "" || ci.severity == "" || ci.title == "" || ci.fix == "" {
			t.Errorf("%s is incomplete: %+v", code, ci)
		}
		if !strings.HasPrefix(code, "LAI-HW-") && code != CodeOOMKill {
			t.Errorf("unexpected code %s", code)
		}
	}
}

func TestLatchBasics(t *testing.T) {
	var l latch
	if l.update(true, false, 2) || !l.update(true, false, 2) {
		t.Fatal("a latch with need 2 must raise on the second bad sample")
	}
	if !l.update(false, false, 2) {
		t.Fatal("inside the band the latch holds")
	}
	if l.update(false, true, 2) {
		t.Fatal("clear releases")
	}
	if l.update(true, false, 2) {
		t.Fatal("after a release the streak starts again")
	}
}
