package router

import (
	"time"

	"github.com/marinoscar/local-ai/agent/internal/catalog"
	"github.com/marinoscar/local-ai/agent/internal/hardware"
)

// Hardware of the three lab node classes and a CPU-only box, as snapshots (invented, round-ish
// values; MiB). Router blocks mirror the profiles of llama-router.md section 3.5.

func i64p(v int64) *int64 { return &v }

// snapAiN3 is a CUDA laptop: 8 GB card, 14 GiB of RAM.
func snapAiN3() hardware.Snapshot {
	return hardware.Snapshot{
		Backend: hardware.BackendCUDA,
		GPUs: []hardware.GPU{{
			Vendor: hardware.VendorNVIDIA, Backend: hardware.BackendCUDA,
			VRAMTotalMiB: 8192, VRAMUsedMiB: 300, VRAMUsedByOthersMiB: i64p(300),
		}},
		RAM: hardware.RAM{TotalMiB: 14336, AvailableMiB: 11000},
	}
}

// snapAiN2 is a CUDA desktop: 12 GB card, 32 GiB of RAM.
func snapAiN2() hardware.Snapshot {
	return hardware.Snapshot{
		Backend: hardware.BackendCUDA,
		GPUs: []hardware.GPU{{
			Vendor: hardware.VendorNVIDIA, Backend: hardware.BackendCUDA,
			VRAMTotalMiB: 12288, VRAMUsedByOthersMiB: i64p(0),
		}},
		RAM: hardware.RAM{TotalMiB: 32768, AvailableMiB: 28000},
	}
}

// snapAiN1 is a Vulkan unified-memory node: about 2 GiB of UMA VRAM and 61 GiB of GTT. The GPU's
// VRAMTotalMiB already carries the GTT size (the fit memory), as the sampler reports it.
func snapAiN1() hardware.Snapshot {
	return hardware.Snapshot{
		Backend: hardware.BackendVulkan,
		GPUs: []hardware.GPU{{
			Vendor: hardware.VendorAMD, Backend: hardware.BackendVulkan, UnifiedMemory: true,
			VRAMTotalMiB: 62464, VRAMUsedMiB: 1000, VRAMUsedByOthersMiB: i64p(1000),
		}},
		RAM: hardware.RAM{TotalMiB: 124928, AvailableMiB: 100000},
	}
}

// snapCPUOnly is a box with no GPU and 16 GiB of RAM.
func snapCPUOnly() hardware.Snapshot {
	return hardware.Snapshot{
		Backend: hardware.BackendCPU,
		RAM:     hardware.RAM{TotalMiB: 16384, AvailableMiB: 12000},
	}
}

func routerBlock(modelsMax, margin int) catalog.Router {
	return catalog.Router{ModelsMax: modelsMax, MemoryMarginMiB: margin}
}

// req builds catalog requirements.
func req(vram, ram int64, backends ...string) catalog.Requirements {
	if len(backends) == 0 {
		backends = []string{hardware.BackendCUDA, hardware.BackendVulkan, hardware.BackendCPU}
	}
	return catalog.Requirements{Backends: backends, MinVRAMMiB: vram, MinRAMMiB: ram}
}

func cand(id string, vram, ram int64, backends ...string) Candidate {
	return Candidate{ID: id, Requirements: req(vram, ram, backends...), Present: true}
}

// t0 is the base of the last_used timestamps; at(n) is n minutes later.
var t0 = time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)

func at(minutes int) time.Time { return t0.Add(time.Duration(minutes) * time.Minute) }

func loaded(id string, vram, ram int64, lastUsed time.Time) Loaded {
	return Loaded{ID: id, Requirements: req(vram, ram), LastUsed: lastUsed}
}
