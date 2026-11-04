package detect

import (
	"context"
	"strconv"
	"strings"
)

// Backends.
const (
	BackendCUDA   = "cuda"
	BackendVulkan = "vulkan"
	BackendCPU    = "cpu"
)

// GPU is what the machine has for inference.
type GPU struct {
	Backend      string `json:"backend"` // cuda, vulkan or cpu
	Vendor       string `json:"vendor"`  // nvidia, amd, intel or ""
	Name         string `json:"name,omitempty"`
	VRAMMiB      int64  `json:"vram_mib"` // dedicated VRAM, or GTT on unified memory
	ComputeCap   string `json:"compute_cap,omitempty"`
	Driver       string `json:"driver,omitempty"`
	NvidiaFound  bool   `json:"nvidia_present"`       // an NVIDIA display device is on the PCI bus
	DriverBroken bool   `json:"nvidia_driver_broken"` // NVIDIA present but nvidia-smi fails
	NvidiaLoaded bool   `json:"nvidia_module_loaded"`
}

// HasNvidiaPCI scans `lspci -nn` output for a display controller (class 0300 or 0302) of vendor 10de.
func HasNvidiaPCI(lspci string) bool {
	for _, line := range strings.Split(lspci, "\n") {
		if (strings.Contains(line, "[0300]") || strings.Contains(line, "[0302]")) && strings.Contains(strings.ToLower(line), "[10de:") {
			return true
		}
	}
	return false
}

// ParseNvidiaSMI parses `nvidia-smi --query-gpu=name,memory.total,driver_version,compute_cap
// --format=csv,noheader`: the first GPU, memory in MiB. ok is false when no line parses.
func ParseNvidiaSMI(text string) (g GPU, ok bool) {
	for _, line := range strings.Split(text, "\n") {
		f := strings.Split(strings.TrimSpace(line), ",")
		if len(f) < 4 {
			continue
		}
		mem := strings.Fields(strings.TrimSpace(f[1]))
		if len(mem) == 0 {
			continue
		}
		n, err := strconv.ParseInt(mem[0], 10, 64)
		if err != nil {
			continue
		}
		return GPU{Backend: BackendCUDA, Vendor: "nvidia", Name: strings.TrimSpace(f[0]), VRAMMiB: n,
			Driver: strings.TrimSpace(f[2]), ComputeCap: strings.TrimSpace(f[3])}, true
	}
	return GPU{}, false
}

// ParseVulkanSummary returns the names of the physical devices in `vulkaninfo --summary` that are not
// the llvmpipe software renderer.
func ParseVulkanSummary(text string) []string {
	var out []string
	for _, line := range strings.Split(text, "\n") {
		t := strings.TrimSpace(line)
		if !strings.HasPrefix(t, "deviceName") {
			continue
		}
		_, v, _ := strings.Cut(t, "=")
		v = strings.TrimSpace(v)
		if v != "" && !strings.Contains(strings.ToLower(v), "llvmpipe") {
			out = append(out, v)
		}
	}
	return out
}

// DetectGPU follows installer.md section 4.3 P1. NVIDIA first (lspci, then nvidia-smi); then AMD and
// Intel from sysfs, confirmed by vulkaninfo when it is installed. A broken NVIDIA driver is reported
// (DriverBroken) and the backend falls to cpu.
func (e Env) DetectGPU(ctx context.Context) GPU {
	g := GPU{Backend: BackendCPU}
	if out, code, err := e.run(ctx, "lspci", "-nn"); err == nil && code == 0 {
		g.NvidiaFound = HasNvidiaPCI(out)
	}
	if mods, ok := e.read("/proc/modules"); ok {
		for _, l := range strings.Split(mods, "\n") {
			if strings.HasPrefix(l, "nvidia ") {
				g.NvidiaLoaded = true
			}
		}
	}
	if out, code, err := e.run(ctx, "nvidia-smi", "--query-gpu=name,memory.total,driver_version,compute_cap", "--format=csv,noheader"); err == nil && code == 0 {
		if smi, ok := ParseNvidiaSMI(out); ok {
			smi.NvidiaFound, smi.NvidiaLoaded = true, true
			return smi
		}
	}
	if g.NvidiaFound {
		g.Vendor, g.DriverBroken = "nvidia", true
		return g
	}
	for _, card := range e.glob("/sys/class/drm/card*/device") {
		v, _ := e.read(card + "/vendor")
		var vendor string
		switch strings.TrimSpace(v) {
		case "0x1002":
			vendor = "amd"
		case "0x8086":
			vendor = "intel"
		default:
			continue
		}
		vram := readMiB(e, card+"/mem_info_vram_total")
		if gtt := readMiB(e, card+"/mem_info_gtt_total"); gtt > vram {
			vram = gtt // unified memory: the usable pool is the GTT (llama-router.md section 4.3.1)
		}
		if out, code, err := e.run(ctx, "vulkaninfo", "--summary"); err == nil && code == 0 {
			names := ParseVulkanSummary(out)
			if len(names) == 0 {
				continue // only llvmpipe: no real Vulkan GPU
			}
			g.Name = names[0]
		}
		g.Backend, g.Vendor, g.VRAMMiB = BackendVulkan, vendor, vram
		return g
	}
	return g
}

func readMiB(e Env, path string) int64 {
	t, ok := e.read(path)
	if !ok {
		return 0
	}
	n, err := strconv.ParseInt(strings.TrimSpace(t), 10, 64)
	if err != nil || n < 0 {
		return 0
	}
	return n / (1024 * 1024)
}
