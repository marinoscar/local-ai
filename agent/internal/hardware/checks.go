package hardware

import (
	"sort"
	"time"
)

// Thresholds (agent.md sections 4.4.1 and 4.4.4 to 4.4.7, issue AGENT-10).
const (
	samplesToRaise = 3 // consecutive bad samples before 005, 006, 011, 016 are raised
	rearmPoints    = 2 // percentage points back on the good side before 005, 006, 008, 016 clear

	othersVRAMPct = 25.0 // LAI-HW-005: others use more than this share of the fit memory
	ramLowPct     = 5.0  // LAI-HW-006: MemAvailable below this share of MemTotal
	swapHighPct   = 50.0 // LAI-HW-016: swap used above this share of SwapTotal
	diskLowPct    = 5.0  // LAI-HW-008: free below this share

	batteryLowPct  = 20 // LAI-HW-013
	batteryCritPct = 10 // LAI-HW-014
	batteryRearm   = 5  // points above the threshold before 013/014 clear

	gpuHotC   = 87.0 // LAI-HW-009
	cpuHotC   = 95.0 // LAI-HW-010
	tempRearm = 5.0  // degrees below the threshold before 009/010 clear

	nvidiaFailuresToRaise = 3 // LAI-HW-002 after this many consecutive nvidia-smi failures

	// OOMHold is how long LAI-LLM-027 stays raised after the last kill: a kill is an event, and the
	// alert needs a window to be seen in.
	OOMHold = 15 * time.Minute
)

// Result is one finding of a check: a doctor code with its plain-language text. It has the shape of
// a doctor check result (agent.md section 3.8.4), so AGENT-11 registers the functions of Checks
// without translation.
type Result struct {
	Check    string         `json:"check"`
	Code     string         `json:"code"`
	Severity string         `json:"severity"`
	Title    string         `json:"title"`
	Fix      string         `json:"fix"`
	Details  map[string]any `json:"details,omitempty"`
}

func newResult(code string, details map[string]any) Result {
	ci := codeTable[code]
	return Result{Check: ci.check, Code: code, Severity: ci.severity, Title: ci.title, Fix: ci.fix, Details: details}
}

// evalInput is everything the evaluator reads after one sample.
type evalInput struct {
	snap Snapshot
	now  time.Time

	// tickFast and tickDisks say which sample just completed. Latches advance only on their own
	// tick; other evaluations (an OOM check, a service verdict) re-read the verdicts unchanged.
	tickFast, tickDisks bool

	nvidiaPresent  bool
	amdPresent     bool
	nvidiaDriver   bool // /proc/driver/nvidia/version exists
	nvidiaFailures int  // consecutive failed nvidia-smi samples
	amdAccess      *AMDAccess

	configuredBackend string
	minFreeBytes      int64
	haveMinFree       bool

	lastOOM *OOMEvent
}

// latch raises after need consecutive bad samples and stays raised until clear holds. The
// distance between bad and clear is the hysteresis: a value that wobbles around the threshold does
// not flap the alert.
type latch struct {
	active bool
	streak int
}

func (l *latch) update(bad, clear bool, need int) bool {
	if l.active {
		if clear {
			l.active, l.streak = false, 0
		}
		return l.active
	}
	if bad {
		l.streak++
		if l.streak >= need {
			l.active = true
		}
	} else {
		l.streak = 0
	}
	return l.active
}

// evaluator holds the in-memory hysteresis state of every threshold (the first observation is a
// baseline: nothing is raised for a state that merely exists at start, except by its own rule).
type evaluator struct {
	others, ramLow, swap, diskFull     latch
	gpuHot, cpuHot, throttle           latch
	onBattery, batteryLow, batteryCrit latch

	prevThrottle     int64
	havePrevThrottle bool
}

// step advances a latch when its kind of sample just completed, and otherwise returns its verdict.
func step(tick bool, l *latch, bad, clear bool, need int) bool {
	if !tick {
		return l.active
	}
	return l.update(bad, clear, need)
}

// evaluate updates the latches from one sample and returns the active results, ordered by code. A
// section that is stale (its probe failed) does not move its latches: the last verdict stands.
func (ev *evaluator) evaluate(in evalInput) []Result {
	var out []Result
	add := func(code string, details map[string]any) { out = append(out, newResult(code, details)) }
	s := in.snap
	sampled := !s.SampledAt.IsZero()
	// A stale section holds the previous values, so evaluating it again would count the same
	// reading twice: its latches stand still and the last verdict is reported.
	tickGPU := in.tickFast && !gpusStale(s)
	tickRAM := in.tickFast && !s.RAM.Stale
	tickThermal := in.tickFast && !s.Thermal.Stale
	tickPower := in.tickFast && !s.Power.Stale
	tickThrottle := tickGPU && tickThermal

	// GPU: driver, devices, groups, others' memory, backend.
	if in.nvidiaDriver && in.nvidiaFailures >= nvidiaFailuresToRaise {
		add(CodeNVIDIADriver, map[string]any{"failures": in.nvidiaFailures})
	}
	if in.amdPresent && in.amdAccess != nil {
		if len(in.amdAccess.MissingDevices) > 0 {
			add(CodeAMDDevices, map[string]any{"missing": in.amdAccess.MissingDevices})
		}
		if len(in.amdAccess.MissingGroups) > 0 {
			add(CodeAMDGroups, map[string]any{"missing_groups": in.amdAccess.MissingGroups})
		}
	}
	if fit := s.GPUMemoryForFit(); fit > 0 && othersMeasurable(s) {
		pct := float64(s.GOthersMiB()) * 100 / float64(fit)
		if step(tickGPU, &ev.others, pct > othersVRAMPct, pct <= othersVRAMPct-rearmPoints, samplesToRaise) {
			add(CodeOthersVRAM, map[string]any{"others_mib": s.GOthersMiB(), "gpu_memory_mib": fit, "others_pct": round1(pct)})
		}
	}
	if sampled && BackendMismatch(in.configuredBackend, in.nvidiaPresent, in.amdPresent) {
		add(CodeBackendMismatch, map[string]any{"configured": in.configuredBackend, "detected": BackendFor(in.nvidiaPresent, in.amdPresent)})
	}

	// Memory.
	if sampled && s.RAM.TotalMiB > 0 {
		avail := s.RAM.AvailablePct()
		if step(tickRAM, &ev.ramLow, avail < ramLowPct, avail >= ramLowPct+rearmPoints, samplesToRaise) {
			add(CodeRAMLow, map[string]any{"available_mib": s.RAM.AvailableMiB, "total_mib": s.RAM.TotalMiB})
		}
		if s.RAM.SwapTotalMiB > 0 {
			used := s.RAM.SwapUsedPct()
			if step(tickRAM, &ev.swap, used > swapHighPct, used <= swapHighPct-rearmPoints, samplesToRaise) {
				add(CodeSwapHeavy, map[string]any{"swap_used_pct": round1(used), "swap_total_mib": s.RAM.SwapTotalMiB})
			}
		} else {
			ev.swap = latch{}
		}
	}

	// Disks.
	if root, ok := s.InternalRoot(); ok && in.haveMinFree && !root.Stale && root.FreeBytes < in.minFreeBytes {
		add(CodeDiskProfile, map[string]any{"path": root.Path, "free_bytes": root.FreeBytes, "min_free_bytes": in.minFreeBytes})
	}
	if len(s.Disks) > 0 {
		var low []string
		minPct, stale := 100.0, false
		for _, d := range s.Disks {
			if d.Stale {
				stale = true
				continue
			}
			p := d.FreePct()
			if p < minPct {
				minPct = p
			}
			if p < diskLowPct {
				low = append(low, d.Path)
			}
		}
		// A stale measurement cannot clear the finding, only a bad one can raise it.
		tick := in.tickDisks && (!stale || len(low) > 0)
		if step(tick, &ev.diskFull, len(low) > 0, minPct >= diskLowPct+rearmPoints, 1) {
			sort.Strings(low)
			add(CodeDiskFull, map[string]any{"paths": low, "free_pct": round1(minPct)})
		}
	}

	// Thermal.
	if t, ok := s.HottestGPUTempC(); ok {
		if step(tickGPU, &ev.gpuHot, t >= gpuHotC, t < gpuHotC-tempRearm, 1) {
			add(CodeGPUHot, map[string]any{"temp_c": t})
		}
	}
	if s.Thermal.CPUTempC != nil {
		t := *s.Thermal.CPUTempC
		if step(tickThermal, &ev.cpuHot, t >= cpuHotC, t < cpuHotC-tempRearm, 1) {
			add(CodeCPUHot, map[string]any{"temp_c": t})
		}
	}
	if sampled {
		throttling := false
		var reasons []string
		for _, g := range s.GPUs {
			if len(g.Throttling) > 0 {
				throttling = true
				reasons = append(reasons, g.Throttling...)
			}
		}
		if tickThrottle && ev.havePrevThrottle && s.Thermal.CPUThrottleEvents > ev.prevThrottle {
			throttling = true
			reasons = append(reasons, "cpu")
		}
		if tickThrottle {
			ev.prevThrottle, ev.havePrevThrottle = s.Thermal.CPUThrottleEvents, true
		}
		if step(tickThrottle, &ev.throttle, throttling, !throttling, samplesToRaise) {
			add(CodeThrottling, map[string]any{"reasons": reasons})
		}
	}

	// Battery and AC.
	if sampled {
		p := s.Power
		onBattery := p.Source == SourceBattery
		if step(tickPower, &ev.onBattery, onBattery, !onBattery, 1) {
			det := map[string]any{}
			if p.BatteryPercent != nil {
				det["battery_percent"] = *p.BatteryPercent
			}
			if p.TimeRemainingS != nil {
				det["time_remaining_s"] = *p.TimeRemainingS
			}
			add(CodeOnBattery, det)
		}
		if p.BatteryPercent != nil {
			pct := *p.BatteryPercent
			low := step(tickPower, &ev.batteryLow, onBattery && pct < batteryLowPct, !onBattery || pct >= batteryLowPct+batteryRearm, 1)
			crit := step(tickPower, &ev.batteryCrit, onBattery && pct < batteryCritPct, !onBattery || pct >= batteryCritPct+batteryRearm, 1)
			switch {
			case crit:
				add(CodeBatteryCrit, map[string]any{"battery_percent": pct})
			case low:
				add(CodeBatteryLow, map[string]any{"battery_percent": pct})
			}
		} else {
			ev.batteryLow, ev.batteryCrit = latch{}, latch{}
		}
		if p.Source == SourceAC && p.Profile != nil && *p.Profile != "performance" {
			add(CodePowerProfile, map[string]any{"profile": *p.Profile})
		}
	}

	// OOM.
	if in.lastOOM != nil && in.now.Sub(in.lastOOM.At) < OOMHold {
		add(CodeOOMKill, map[string]any{"pid": in.lastOOM.PID, "source": in.lastOOM.Source, "at": in.lastOOM.At.UTC().Format(time.RFC3339)})
	}

	sort.SliceStable(out, func(i, j int) bool { return out[i].Code < out[j].Code })
	return out
}

func gpusStale(s Snapshot) bool {
	for _, g := range s.GPUs {
		if g.Stale {
			return true
		}
	}
	return false
}

// othersMeasurable is false when no GPU of the fit set could report its others' share.
func othersMeasurable(s Snapshot) bool {
	for _, g := range s.fitGPUs(s.Backend) {
		if g.VRAMUsedByOthersMiB != nil {
			return true
		}
	}
	return false
}
