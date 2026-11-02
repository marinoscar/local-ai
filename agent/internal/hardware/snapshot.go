package hardware

import "time"

// Snapshot is one consistent view of the machine: the body of GET /hardware plus a few fields for
// in-process consumers (the router fit rules, status, the cloud heartbeat). Snapshots are values;
// the slices inside are never shared with the sampler.
type Snapshot struct {
	// SampledAt is when the fast probes (GPU, RAM, CPU, thermal, power) last completed. Zero before
	// the first sample.
	SampledAt time.Time `json:"sampled_at"`
	GPUs      []GPU     `json:"gpus"`
	RAM       RAM       `json:"ram"`
	Disks     []Disk    `json:"disks"`
	Power     Power     `json:"power"`
	Thermal   Thermal   `json:"thermal"`
	CPU       CPU       `json:"cpu"`

	// Backend is the backend detected from the hardware (BackendFor). Not on the wire.
	Backend string `json:"-"`
	// Self is the agent's own CPU and memory use, for heartbeat vitals. Not on the wire.
	Self Self `json:"-"`
}

// FitMemory is G_total of llama-router.md section 4.3.1 for a backend: cuda sums the VRAM of the
// NVIDIA GPUs; vulkan sums the AMD GPUs (the GTT size for a unified-memory APU) and, on a node with
// no AMD GPU, the NVIDIA GPUs (Vulkan runs on them too); cpu is 0. Values are MiB.
func (s Snapshot) FitMemory(backend string) int64 {
	var total int64
	for _, g := range s.fitGPUs(backend) {
		total += g.VRAMTotalMiB
	}
	return total
}

// OthersMiB is G_others of llama-router.md section 4.3.1 for a backend: the GPU memory in use by
// processes outside the localai-llama cgroup. A GPU whose share cannot be measured counts as 0.
func (s Snapshot) OthersMiB(backend string) int64 {
	var total int64
	for _, g := range s.fitGPUs(backend) {
		if g.VRAMUsedByOthersMiB != nil {
			total += *g.VRAMUsedByOthersMiB
		}
	}
	return total
}

// GPUMemoryForFit is FitMemory for the detected backend.
func (s Snapshot) GPUMemoryForFit() int64 { return s.FitMemory(s.Backend) }

// GOthersMiB is OthersMiB for the detected backend.
func (s Snapshot) GOthersMiB() int64 { return s.OthersMiB(s.Backend) }

// UnifiedMemory reports whether the GPU memory used for fit is the system memory (an AMD APU), so
// the router reserves 4096 MiB of RAM instead of 2048 (llama-router.md section 4.3.1).
func (s Snapshot) UnifiedMemory(backend string) bool {
	for _, g := range s.fitGPUs(backend) {
		if g.UnifiedMemory {
			return true
		}
	}
	return false
}

func (s Snapshot) fitGPUs(backend string) []GPU {
	var nvidia, amd []GPU
	for _, g := range s.GPUs {
		switch g.Vendor {
		case VendorNVIDIA:
			nvidia = append(nvidia, g)
		case VendorAMD:
			amd = append(amd, g)
		}
	}
	switch backend {
	case BackendCUDA:
		return nvidia
	case BackendVulkan:
		if len(amd) > 0 {
			return amd
		}
		return nvidia
	}
	return nil
}

// HottestGPUTempC is the highest GPU temperature, if any GPU reports one.
func (s Snapshot) HottestGPUTempC() (float64, bool) {
	var max float64
	found := false
	for _, g := range s.GPUs {
		if g.TempC != nil && (!found || *g.TempC > max) {
			max, found = *g.TempC, true
		}
	}
	return max, found
}

// InternalRoot returns the disk with the internal_root role.
func (s Snapshot) InternalRoot() (Disk, bool) {
	for _, d := range s.Disks {
		if d.Role == RoleInternalRoot {
			return d, true
		}
	}
	return Disk{}, false
}

// clone deep-copies the slices so a caller can keep or modify a Snapshot freely.
func (s Snapshot) clone() Snapshot {
	out := s
	out.GPUs = make([]GPU, len(s.GPUs))
	for i, g := range s.GPUs {
		g.Throttling = append([]string{}, g.Throttling...)
		g.Processes = append([]GPUProcess{}, g.Processes...)
		out.GPUs[i] = g
	}
	out.Disks = append([]Disk{}, s.Disks...)
	out.Thermal.Zones = append([]ThermalZone{}, s.Thermal.Zones...)
	return out
}
