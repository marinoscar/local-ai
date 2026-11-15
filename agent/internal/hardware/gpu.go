package hardware

// GPU is one entry of the gpus array of GET /hardware (agent.md section 3.8.6). GPU UUIDs, serial
// numbers and PCI bus ids are never collected (redaction rule), so none of them has a field here.
type GPU struct {
	Index   int    `json:"index"`
	Vendor  string `json:"vendor"`
	Name    string `json:"name"`
	Backend string `json:"backend"`
	// ComputeCap is the CUDA compute capability ("7.5"); null for AMD or when the driver says N/A.
	ComputeCap *string `json:"compute_cap"`
	// Driver is the driver version; null when unknown.
	Driver *string `json:"driver"`
	// UnifiedMemory is true for an AMD APU whose GPU memory is the system GTT (agent.md section 4.4.2).
	UnifiedMemory bool `json:"unified_memory"`
	// VRAMTotalMiB is the memory the fit rules use: the card's VRAM, or the GTT size when
	// UnifiedMemory is set.
	VRAMTotalMiB int64 `json:"vram_total_mib"`
	// VRAMUsedMiB is the memory in use (vram_used + gtt_used when UnifiedMemory is set).
	VRAMUsedMiB int64 `json:"vram_used_mib"`
	// VRAMUsedByOthersMiB is the part of VRAMUsedMiB that does not belong to the localai-llama
	// cgroup. It is null when it cannot be measured (an AMD GPU whose kernel does not expose DRM
	// fdinfo memory while llama-server is running).
	VRAMUsedByOthersMiB *int64   `json:"vram_used_by_others_mib"`
	TempC               *float64 `json:"temp_c"`
	UtilPct             *int     `json:"util_pct"`
	PowerW              *float64 `json:"power_w"`
	// Throttling lists why the GPU is slowing down: "thermal" (software or hardware thermal slowdown)
	// and "power_brake" (hardware power brake). Empty when it is not.
	Throttling []string `json:"throttling"`
	// Processes lists the compute processes. NVIDIA only, and only on a node with one GPU: without
	// the GPU uuid or bus id (never collected) a process cannot be attributed to one of several GPUs.
	Processes []GPUProcess `json:"processes"`
	// Stale is true when the last probe failed and this is the previous value.
	Stale bool `json:"stale,omitempty"`
}

// GPUProcess is one compute process using GPU memory.
type GPUProcess struct {
	PID     int    `json:"pid"`
	Name    string `json:"name"`
	VRAMMiB int64  `json:"vram_mib"`
}

// Throttle reason names.
const (
	ThrottleThermal    = "thermal"
	ThrottlePowerBrake = "power_brake"
)

// llamaCgroupUnit is the systemd unit of the router; its cgroup holds llama-server and its children.
const llamaCgroupUnit = "localai-llama.service"

// CgroupHasUnit reports whether a /proc/<pid>/cgroup file places the process in a systemd unit.
func CgroupHasUnit(b []byte, unit string) bool { return cgroupHasUnit(b, unit) }

// UnitVRAMMiB sums the graphics memory of the compute processes that run in a systemd unit
// (llama-router.md section 4.3.1: an engine's own memory is measured, not taken from what it says).
// readCgroup reads /proc/<pid>/cgroup; a process whose file cannot be read is not the unit's. known
// is false when the processes cannot be attributed: no GPU, several GPUs, or a GPU that does not list
// its processes (AMD).
func UnitVRAMMiB(gpus []GPU, unit string, readCgroup func(pid int) ([]byte, error)) (mib int64, known bool) {
	if len(gpus) != 1 || gpus[0].Vendor != VendorNVIDIA || gpus[0].Processes == nil {
		return 0, false
	}
	for _, p := range gpus[0].Processes {
		b, err := readCgroup(p.PID)
		if err == nil && cgroupHasUnit(b, unit) {
			mib += p.VRAMMiB
		}
	}
	return mib, true
}
