package cloud

import (
	"math"
	"regexp"
	"strconv"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/hardware"
	"github.com/marinoscar/local-ai/agent/internal/statusapi"
)

// ExtraKeys is the fixed set of vitals.extra keys (agent.md section 4.8.3): exactly 16, the EAB
// maximum. The first five are the ones the cloud registers for display, the other 11 show on the
// fleet page by key. The set is part of the wire contract: never trim it, never add to it.
var ExtraKeys = [16]string{
	"localai.models_loaded", "localai.vram_used_mib", "localai.vram_total_mib", "localai.sync_running", "localai.alerts_active",
	"localai.gpu_temp_c", "localai.gpu_util_pct", "localai.cpu_temp_c", "localai.ram_available_mib",
	"localai.models_loadable", "localai.llama_up", "localai.ark_internal_free_gib",
	"localai.ark_drive_mounted", "localai.sync_age_h", "localai.alerts_critical", "localai.catalog_version",
}

// ExtraKeyPattern is the rule EAB applies to every vitals.extra key.
var ExtraKeyPattern = regexp.MustCompile(`^[a-z][a-z0-9_.]{1,47}$`)

// extraLimit is the largest magnitude of a vitals.extra value.
const extraLimit = 1e15

// Vitals is the heartbeat's "vitals" object (agent.md section 4.8.3). Host and Power are pointers:
// they are sent only after the server advertised node_host_vitals.
type Vitals struct {
	CPUPercent         float64            `json:"cpuPercent"`
	RSSBytes           int64              `json:"rssBytes"`
	StateDirFreeBytes  int64              `json:"stateDirFreeBytes"`
	StateDirTotalBytes int64              `json:"stateDirTotalBytes"`
	SlotsUsed          int                `json:"slotsUsed"`
	SlotsTotal         int                `json:"slotsTotal"`
	UptimeSeconds      int64              `json:"uptimeSeconds"`
	CLIVersion         string             `json:"cliVersion"`
	Counters           CountersWire       `json:"counters"`
	Extra              map[string]float64 `json:"extra,omitempty"`
	Host               *HostVitals        `json:"host,omitempty"`
	Power              *PowerVitals       `json:"power,omitempty"`
}

// PowerVitals is vitals.power.
type PowerVitals struct {
	Source               string `json:"source"`
	BatteryPercent       *int   `json:"batteryPercent,omitempty"`
	Charging             bool   `json:"charging"`
	TimeRemainingSeconds *int64 `json:"timeRemainingSeconds,omitempty"`
}

// VitalsInput is what the heartbeat gathers each time.
type VitalsInput struct {
	Now       time.Time
	StartedAt time.Time
	// CLIVersion is "aiagent/<version>".
	CLIVersion string
	HW         hardware.Snapshot
	Status     statusapi.Status
	Counters   CountersWire
	SlotsUsed  int
	SlotsTotal int
	// Host is non-nil when host and power vitals may be sent (node_host_vitals advertised).
	Host *HostVitals
	// NoExtra drops vitals.extra (the cloud answered 400 mentioning it earlier).
	NoExtra bool
}

// BuildVitals fills the vitals of one heartbeat.
func BuildVitals(in VitalsInput) Vitals {
	v := Vitals{
		CPUPercent:    clampF(in.HW.Self.CPUPercent, 0, 100),
		RSSBytes:      clampI(in.HW.Self.RSSMiB*1024*1024, 0, 1<<53-1),
		SlotsUsed:     clampInt(in.SlotsUsed, 0, 64),
		SlotsTotal:    clampInt(in.SlotsTotal, 1, 64),
		CLIVersion:    cut(in.CLIVersion, 64),
		Counters:      in.Counters,
		UptimeSeconds: 0,
	}
	if !in.StartedAt.IsZero() && in.Now.After(in.StartedAt) {
		v.UptimeSeconds = int64(in.Now.Sub(in.StartedAt) / time.Second)
	}
	if d, ok := stateDisk(in.HW); ok {
		v.StateDirFreeBytes = clampI(d.FreeBytes, 0, 1<<53-1)
		v.StateDirTotalBytes = clampI(d.TotalBytes, 0, 1<<53-1)
	}
	if !in.NoExtra {
		v.Extra = BuildExtra(in.Status, in.HW, in.Now)
	}
	if in.Host != nil {
		h := clampHost(*in.Host)
		v.Host = &h
		p := PowerVitals{Source: powerSource(in.HW.Power.Source), Charging: in.HW.Power.Charging}
		if in.HW.Power.BatteryPercent != nil {
			pct := clampInt(*in.HW.Power.BatteryPercent, 0, 100)
			p.BatteryPercent = &pct
		}
		if t := in.HW.Power.TimeRemainingS; t != nil {
			s := clampI(*t, 0, 7*24*3600)
			p.TimeRemainingSeconds = &s
		}
		v.Power = &p
	}
	return v
}

func powerSource(s string) string {
	switch s {
	case hardware.SourceAC, hardware.SourceBattery:
		return s
	}
	return hardware.SourceUnknown
}

// clampHost keeps every host number inside the bounds EAB validates.
func clampHost(h HostVitals) HostVitals {
	h.CPUPercent = clampF(h.CPUPercent, 0, 100)
	h.CPUCount = clampInt(h.CPUCount, 1, 4096)
	h.LoadAvg1, h.LoadAvg5, h.LoadAvg15 = clampF(h.LoadAvg1, 0, 1e6), clampF(h.LoadAvg5, 0, 1e6), clampF(h.LoadAvg15, 0, 1e6)
	h.MemTotalBytes = clampI(h.MemTotalBytes, 0, 1<<53-1)
	h.MemFreeBytes = clampI(h.MemFreeBytes, 0, 1<<53-1)
	h.MemAvailableBytes = clampI(h.MemAvailableBytes, 0, 1<<53-1)
	h.OSRelease = cut(h.OSRelease, 128)
	h.Arch = cut(h.Arch, 32)
	h.UptimeSeconds = clampI(h.UptimeSeconds, 0, 1<<53-1)
	return h
}

// BuildExtra computes the 16 vitals.extra values. A value that cannot be computed is left out, never
// sent as null, and every value is finite and within +-1e15.
func BuildExtra(st statusapi.Status, hw hardware.Snapshot, now time.Time) map[string]float64 {
	x := map[string]float64{
		"localai.models_loaded":     float64(len(st.Models.Loaded)),
		"localai.sync_running":      boolF(st.Ark.Sync.State == "running" || st.Ark.Sync.State == "cancelling"),
		"localai.alerts_active":     float64(len(st.Alerts)),
		"localai.ram_available_mib": float64(st.Hardware.RAMAvailableMiB),
		"localai.models_loadable":   float64(st.Models.Loadable),
		"localai.llama_up":          boolF(llamaUp(st)),
		"localai.ark_drive_mounted": boolF(st.Ark.Drive.Mounted),
	}
	critical := 0
	for _, a := range st.Alerts {
		if a.Severity == "critical" {
			critical++
		}
	}
	x["localai.alerts_critical"] = float64(critical)
	if st.Hardware.VRAMUsedMiB != nil {
		x["localai.vram_used_mib"] = float64(*st.Hardware.VRAMUsedMiB)
	}
	if st.Hardware.VRAMTotalMiB != nil {
		x["localai.vram_total_mib"] = float64(*st.Hardware.VRAMTotalMiB)
	}
	if st.Hardware.GPUTempC != nil {
		x["localai.gpu_temp_c"] = *st.Hardware.GPUTempC
	}
	if st.Hardware.CPUTempC != nil {
		x["localai.cpu_temp_c"] = *st.Hardware.CPUTempC
	}
	var util *int
	for _, g := range st.Hardware.GPUs {
		if g.UtilPct != nil && (util == nil || *g.UtilPct > *util) {
			u := *g.UtilPct
			util = &u
		}
	}
	if util != nil {
		x["localai.gpu_util_pct"] = float64(*util)
	}
	if d, ok := internalRootDisk(hw); ok {
		x["localai.ark_internal_free_gib"] = math.Floor(float64(d.FreeBytes) / (1 << 30))
	}
	if st.Ark.Sync.LastSuccessAt != nil {
		if t, err := time.Parse(time.RFC3339, *st.Ark.Sync.LastSuccessAt); err == nil && !now.Before(t) {
			x["localai.sync_age_h"] = math.Floor(now.Sub(t).Hours())
		}
	}
	if st.Node.CatalogVersion != nil {
		if v, ok := EncodeCatalogVersion(*st.Node.CatalogVersion); ok {
			x["localai.catalog_version"] = v
		}
	}
	return sanitizeExtra(x)
}

func llamaUp(st statusapi.Status) bool {
	for _, s := range st.Services {
		if s.Name == "llama" {
			return s.Healthy
		}
	}
	return false
}

func internalRootDisk(hw hardware.Snapshot) (hardware.Disk, bool) { return hw.InternalRoot() }

func boolF(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

// sanitizeExtra drops any entry whose key breaks the EAB pattern or whose value is not finite or is
// beyond +-1e15, and keeps the map within the 16 declared keys.
func sanitizeExtra(x map[string]float64) map[string]float64 {
	allowed := map[string]bool{}
	for _, k := range ExtraKeys {
		allowed[k] = true
	}
	for k, v := range x {
		if !allowed[k] || !ExtraKeyPattern.MatchString(k) || math.IsNaN(v) || math.IsInf(v, 0) || math.Abs(v) > extraLimit {
			delete(x, k)
		}
	}
	return x
}

var catalogVersionRE = regexp.MustCompile(`^(\d{4})\.(\d{2})\.(\d{2})-(\d+)$`)

// EncodeCatalogVersion turns YYYY.MM.DD-N into the number YYYYMMDD.N (2026.11.02-1 becomes
// 20261102.1) that vitals.extra can carry.
func EncodeCatalogVersion(v string) (float64, bool) {
	m := catalogVersionRE.FindStringSubmatch(v)
	if m == nil {
		return 0, false
	}
	f, err := strconv.ParseFloat(m[1]+m[2]+m[3]+"."+m[4], 64)
	if err != nil {
		return 0, false
	}
	return f, true
}

// ValidCatalogVersion reports whether v is a snapshot-valid catalog version (YYYY.MM.DD-N).
func ValidCatalogVersion(v string) bool { return catalogVersionRE.MatchString(v) }

func clampF(v, lo, hi float64) float64 {
	if math.IsNaN(v) {
		return lo
	}
	return math.Min(math.Max(v, lo), hi)
}

func clampI(v, lo, hi int64) int64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func clampInt(v, lo, hi int) int { return int(clampI(int64(v), int64(lo), int64(hi))) }
