package hardware

import (
	"io/fs"
	"regexp"
	"sort"
	"strings"
)

// Thermal is the thermal block of GET /hardware.
type Thermal struct {
	// CPUTempC is the hottest CPU sensor; null when the machine has none (a VM).
	CPUTempC *float64 `json:"cpu_temp_c"`
	// CPUThrottleEvents is the package throttle counter since boot (the largest over the CPUs).
	CPUThrottleEvents int64 `json:"cpu_throttle_events"`
	// Zones lists the sensors CPUTempC was taken from.
	Zones []ThermalZone `json:"zones"`
	// Stale is true when the last probe failed and these are the previous values.
	Stale bool `json:"stale,omitempty"`
}

// ThermalZone is one CPU temperature sensor.
type ThermalZone struct {
	Type  string  `json:"type"`
	TempC float64 `json:"temp_c"`
}

var (
	thermalZoneRE = regexp.MustCompile(`^thermal_zone[0-9]+$`)
	cpuDirRE      = regexp.MustCompile(`^cpu[0-9]+$`)
	tempInputRE   = regexp.MustCompile(`^temp[0-9]+_input$`)

	cpuZoneTypes = map[string]bool{"x86_pkg_temp": true, "TCPU": true, "cpu-thermal": true, "k10temp": true}
	cpuHwmons    = map[string]bool{"coretemp": true, "k10temp": true, "zenpower": true}
)

// Plausible CPU sensor range in degrees: a dead sensor reports 0 or a large negative number.
const (
	minSaneTempC = 1.0
	maxSaneTempC = 150.0
)

// readThermal reads thermal zones and hwmon sensors (agent.md section 4.4.7). Absent sensors are not
// an error: CPUTempC is simply null.
func (e *env) readThermal() Thermal {
	t := Thermal{Zones: []ThermalZone{}}
	var primary, fallback []ThermalZone

	if entries, err := fs.ReadDir(e.fs, "sys/class/thermal"); err == nil {
		for _, en := range entries {
			if !thermalZoneRE.MatchString(en.Name()) {
				continue
			}
			dir := "sys/class/thermal/" + en.Name() + "/"
			typ, err := e.readTrim(dir + "type")
			if err != nil {
				continue
			}
			temp, ok := e.milliCelsius(dir + "temp")
			if !ok {
				continue
			}
			switch {
			case cpuZoneTypes[typ]:
				primary = append(primary, ThermalZone{Type: typ, TempC: temp})
			case typ == "acpitz":
				fallback = append(fallback, ThermalZone{Type: typ, TempC: temp})
			}
		}
	}
	if entries, err := fs.ReadDir(e.fs, "sys/class/hwmon"); err == nil {
		for _, en := range entries {
			if !strings.HasPrefix(en.Name(), "hwmon") {
				continue
			}
			dir := "sys/class/hwmon/" + en.Name() + "/"
			name, err := e.readTrim(dir + "name")
			if err != nil || !cpuHwmons[name] {
				continue
			}
			if temp, ok := e.hottestInput(dir); ok {
				primary = append(primary, ThermalZone{Type: name, TempC: temp})
			}
		}
	}

	zones := primary
	if len(zones) == 0 {
		zones = fallback // acpitz only when nothing better exists
	}
	for _, z := range zones {
		if t.CPUTempC == nil || z.TempC > *t.CPUTempC {
			t.CPUTempC = ptr(z.TempC)
		}
	}
	sort.SliceStable(zones, func(i, j int) bool { return zones[i].Type < zones[j].Type })
	t.Zones = append(t.Zones, zones...)
	t.CPUThrottleEvents = e.throttleCount()
	return t
}

// milliCelsius reads a millidegree file and converts it; ok is false for an unreadable or
// implausible value.
func (e *env) milliCelsius(name string) (float64, bool) {
	v, err := e.readInt(name)
	if err != nil {
		return 0, false
	}
	c := float64(v) / 1000
	if c < minSaneTempC || c > maxSaneTempC {
		return 0, false
	}
	return round1(c), true
}

// hottestInput is the largest temp*_input of one hwmon device.
func (e *env) hottestInput(dir string) (float64, bool) {
	entries, err := fs.ReadDir(e.fs, strings.TrimSuffix(dir, "/"))
	if err != nil {
		return 0, false
	}
	best, found := 0.0, false
	for _, en := range entries {
		if !tempInputRE.MatchString(en.Name()) {
			continue
		}
		if c, ok := e.milliCelsius(dir + en.Name()); ok && (!found || c > best) {
			best, found = c, true
		}
	}
	return best, found
}

// throttleCount is the largest package_throttle_count over the CPUs; 0 when the counters do not
// exist (not an Intel package, or a VM).
func (e *env) throttleCount() int64 {
	entries, err := fs.ReadDir(e.fs, "sys/devices/system/cpu")
	if err != nil {
		return 0
	}
	var max int64
	for _, en := range entries {
		if !cpuDirRE.MatchString(en.Name()) {
			continue
		}
		if v, err := e.readInt("sys/devices/system/cpu/" + en.Name() + "/thermal_throttle/package_throttle_count"); err == nil && v > max {
			max = v
		}
	}
	return max
}
