package hardware

// BackendFor is the backend detection rule of agent.md section 4.4.3: NVIDIA present -> cuda; else
// AMD present -> vulkan; else cpu.
func BackendFor(nvidia, amd bool) string {
	switch {
	case nvidia:
		return BackendCUDA
	case amd:
		return BackendVulkan
	default:
		return BackendCPU
	}
}

// BackendMismatch reports whether the backend configured in node.yaml (llama.backend) cannot work
// on this hardware (LAI-HW-017): cuda needs an NVIDIA GPU, vulkan needs a GPU of either vendor, cpu
// always works. An empty or unknown value is not a mismatch (config validation owns that).
func BackendMismatch(configured string, nvidia, amd bool) bool {
	switch configured {
	case BackendCUDA:
		return !nvidia
	case BackendVulkan:
		return !nvidia && !amd
	default:
		return false
	}
}
