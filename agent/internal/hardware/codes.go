package hardware

// Doctor codes this package raises (agent.md section 8.3, doctor-codes.md).
//
// They live in this one file on purpose: scripts/check-doctor-codes.sh forbids a "LAI-..." literal in
// any non-test Go file outside internal/doctor/codes.go once that file exists. AGENT-11 replaces
// these constants and the English text below with the doctor registry (en and es); nothing else in
// this package spells a code.
const (
	CodeNVIDIADriver    = "LAI-HW-002"  // nvidia-smi fails while the driver is loaded
	CodeAMDDevices      = "LAI-HW-003"  // /dev/kfd or /dev/dri/renderD* missing
	CodeAMDGroups       = "LAI-HW-004"  // localai not in video/render
	CodeOthersVRAM      = "LAI-HW-005"  // other programs use a lot of GPU memory
	CodeRAMLow          = "LAI-HW-006"  // MemAvailable almost gone
	CodeDiskProfile     = "LAI-HW-007"  // internal root free < profile storage.min_free_bytes
	CodeDiskFull        = "LAI-HW-008"  // a listed filesystem is > 95 % full
	CodeGPUHot          = "LAI-HW-009"  // GPU >= 87 C
	CodeCPUHot          = "LAI-HW-010"  // CPU >= 95 C
	CodeThrottling      = "LAI-HW-011"  // throttling in 3 consecutive samples
	CodeOnBattery       = "LAI-HW-012"  // running on battery
	CodeBatteryLow      = "LAI-HW-013"  // battery < 20 %
	CodeBatteryCrit     = "LAI-HW-014"  // battery < 10 %
	CodePowerProfile    = "LAI-HW-015"  // profile is not performance (on AC)
	CodeSwapHeavy       = "LAI-HW-016"  // swap used > 50 %
	CodeBackendMismatch = "LAI-HW-017"  // llama.backend does not match the hardware
	CodeOOMKill         = "LAI-LLM-027" // llama-server was OOM-killed
)

// Severities of a Result (doctor-codes.md).
const (
	SeverityInfo     = "info"
	SeverityWarning  = "warning"
	SeverityError    = "error"
	SeverityCritical = "critical"
)

// Check ids: one per group of codes, the unit the doctor registry runs.
const (
	CheckGPU     = "hw.gpu"
	CheckMemory  = "hw.memory"
	CheckDisk    = "hw.disk"
	CheckThermal = "hw.thermal"
	CheckPower   = "hw.power"
	CheckOOM     = "hw.oom"
)

// codeInfo is the plain-language text of a code (agent.md section 8.3, English).
type codeInfo struct {
	check    string
	severity string
	title    string
	fix      string
}

var codeTable = map[string]codeInfo{
	CodeNVIDIADriver: {CheckGPU, SeverityError, "The NVIDIA driver is not answering (nvidia-smi failed)",
		"Restart the computer. If it persists, reinstall the driver (runbooks/06-rebuild-a-node-offline.md)"},
	CodeAMDDevices: {CheckGPU, SeverityError, "The AMD GPU device files are missing (/dev/kfd, /dev/dri/renderD*)",
		"Restart the computer; check that the amdgpu driver loads"},
	CodeAMDGroups: {CheckGPU, SeverityError, "The localai user cannot use the GPU (not in video/render)",
		"Run sudo usermod -aG video,render localai and aictl restart llama"},
	CodeOthersVRAM: {CheckGPU, SeverityWarning, "Other programs are using a lot of graphics memory",
		"Close games or other GPU programs, or log out of the desktop session"},
	CodeBackendMismatch: {CheckGPU, SeverityError, "The configured backend does not match the hardware (e.g. CUDA set, no NVIDIA GPU)",
		"Run aictl profile suggest and fix llama.backend in node.yaml"},
	CodeRAMLow: {CheckMemory, SeverityWarning, "The computer is almost out of memory (RAM)",
		"Close other programs or load a smaller model"},
	CodeSwapHeavy: {CheckMemory, SeverityWarning, "The computer is using a lot of swap, which makes everything slow",
		"Load a smaller model or close other programs"},
	CodeDiskProfile: {CheckDisk, SeverityWarning, "Free disk space is below the profile's safety limit",
		"Remove files you don't need, or move content to the ark drive"},
	CodeDiskFull: {CheckDisk, SeverityCritical, "A disk used by Local AI is more than 95% full",
		"Free space now: delete large personal files or old models (aictl delete <model>)"},
	CodeGPUHot: {CheckThermal, SeverityWarning, "The GPU is very hot (87 °C or more)",
		"Make sure the vents are not blocked and the laptop is on a hard surface"},
	CodeCPUHot: {CheckThermal, SeverityWarning, "The CPU is very hot (95 °C or more)",
		"Same as above; clean the fans if dusty"},
	CodeThrottling: {CheckThermal, SeverityWarning, "The computer is slowing down to cool itself (throttling)",
		"Improve airflow; answers will be slower until it cools"},
	CodeOnBattery: {CheckPower, SeverityWarning, "Running on battery",
		"Plug in the charger. A laptop server stops when the battery runs out"},
	CodeBatteryLow: {CheckPower, SeverityWarning, "Battery below 20%", "Plug in the charger soon"},
	CodeBatteryCrit: {CheckPower, SeverityCritical, "Battery below 10%",
		"Plug in the charger now, or save your work"},
	CodePowerProfile: {CheckPower, SeverityInfo, `The power profile is not "performance"`,
		"Run powerprofilesctl set performance"},
	CodeOOMKill: {CheckOOM, SeverityError, "A model was stopped because the computer ran out of memory",
		"Choose a smaller model or a shorter context"},
}
