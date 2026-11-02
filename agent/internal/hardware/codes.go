package hardware

import "github.com/marinoscar/local-ai/agent/internal/doctor"

// Doctor codes this package raises (agent.md section 8.3, doctor-codes.md).
//
// They are aliases of the constants of internal/doctor/codes.go, the one file allowed to spell a
// "LAI-..." literal (scripts/check-doctor-codes.sh). The English text below is this package's own
// table until its checks are registered with the doctor, which then localizes from the doctor table.
const (
	CodeNVIDIADriver    = doctor.HW002  // nvidia-smi fails while the driver is loaded
	CodeAMDDevices      = doctor.HW003  // /dev/kfd or /dev/dri/renderD* missing
	CodeAMDGroups       = doctor.HW004  // localai not in video/render
	CodeOthersVRAM      = doctor.HW005  // other programs use a lot of GPU memory
	CodeRAMLow          = doctor.HW006  // MemAvailable almost gone
	CodeDiskProfile     = doctor.HW007  // internal root free < profile storage.min_free_bytes
	CodeDiskFull        = doctor.HW008  // a listed filesystem is > 95 % full
	CodeGPUHot          = doctor.HW009  // GPU >= 87 C
	CodeCPUHot          = doctor.HW010  // CPU >= 95 C
	CodeThrottling      = doctor.HW011  // throttling in 3 consecutive samples
	CodeOnBattery       = doctor.HW012  // running on battery
	CodeBatteryLow      = doctor.HW013  // battery < 20 %
	CodeBatteryCrit     = doctor.HW014  // battery < 10 %
	CodePowerProfile    = doctor.HW015  // profile is not performance (on AC)
	CodeSwapHeavy       = doctor.HW016  // swap used > 50 %
	CodeBackendMismatch = doctor.HW017  // llama.backend does not match the hardware
	CodeOOMKill         = doctor.LLM027 // llama-server was OOM-killed
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
