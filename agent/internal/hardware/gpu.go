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
