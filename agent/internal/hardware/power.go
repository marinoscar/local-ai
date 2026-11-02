package hardware

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"regexp"
	"strconv"
	"strings"
)

// Power sources.
const (
	SourceAC      = "ac"
	SourceBattery = "battery"
	SourceUnknown = "unknown"
)

const powerSupplyDir = "sys/class/power_supply"

// Power is the power block of GET /hardware.
type Power struct {
	// Source is "ac" when a mains supply is online, "battery" when a battery is discharging, else "unknown".
	Source string `json:"source"`
	// BatteryPercent is null on a machine without a battery.
	BatteryPercent *int `json:"battery_percent"`
	Charging       bool `json:"charging"`
	// TimeRemainingS is the estimated time until empty while discharging; null otherwise.
	TimeRemainingS *int64 `json:"time_remaining_s"`
	// Profile is the power profile (powerprofilesctl get); null when the tool is absent.
	Profile *string `json:"profile"`
	// Stale is true when the last probe failed and these are the previous values.
	Stale bool `json:"stale,omitempty"`
}

// HasBattery reports whether a battery percentage is known.
func (p Power) HasBattery() bool { return p.BatteryPercent != nil }

var upowerPathRE = regexp.MustCompile(`^/org/freedesktop/UPower/devices/[A-Za-z0-9_]+$`)

// supply is the state of /sys/class/power_supply read once.
type supply struct {
	acOnline       bool
	haveBattery    bool // a Battery entry that is present
	percent        *int
	status         string
	timeRemainingS *int64
}

// readPower reads the AC and battery state. It never fails: a machine without any power data
// (a desktop, a VM) reports source "unknown" and no battery. Primary source is sysfs; when sysfs is
// missing or a battery entry has no usable capacity, upower is asked (agent.md section 4.4.6).
func (e *env) readPower(ctx context.Context) Power {
	s, incomplete := e.readSupplies()
	if incomplete {
		e.fillFromUPower(ctx, &s)
	}
	p := Power{Source: SourceUnknown, BatteryPercent: s.percent, TimeRemainingS: s.timeRemainingS}
	p.Charging = s.status == "Charging"
	switch {
	case s.acOnline:
		p.Source = SourceAC
	case s.status == "Discharging":
		p.Source = SourceBattery
	}
	if p.Source != SourceBattery {
		p.TimeRemainingS = nil
	}
	return p
}

// readSupplies reads sysfs. incomplete is true when sysfs is absent or has a battery it cannot read.
func (e *env) readSupplies() (s supply, incomplete bool) {
	entries, err := fs.ReadDir(e.fs, powerSupplyDir)
	if err != nil {
		return s, true
	}
	for _, en := range entries {
		dir := powerSupplyDir + "/" + en.Name() + "/"
		typ, err := e.readTrim(dir + "type")
		if err != nil {
			continue
		}
		if scope, _ := e.readTrim(dir + "scope"); scope == "Device" {
			continue // a peripheral (wireless mouse, headset), not this machine
		}
		switch {
		case typ == "Mains" || strings.HasPrefix(typ, "USB"):
			// USB-C chargers show up as type USB on current kernels; both mean line power.
			if v, err := e.readInt(dir + "online"); err == nil && v == 1 {
				s.acOnline = true
			}
		case typ == "Battery":
			if present, err := e.readInt(dir + "present"); err == nil && present == 0 {
				continue
			}
			if s.percent != nil {
				continue // the first battery speaks for the machine
			}
			s.haveBattery = true
			pct, err := e.readInt(dir + "capacity")
			if err != nil || pct < 0 || pct > 100 {
				incomplete = true
				continue
			}
			v := int(pct)
			s.percent = &v
			s.status, _ = e.readTrim(dir + "status")
			s.timeRemainingS = e.timeRemaining(dir, s.status)
			incomplete = false
		}
	}
	return s, incomplete
}

// timeRemaining is energy_now / power_now x 3600 (or charge_now / current_now x 3600) in seconds
// while discharging; nil when the rates are missing or zero.
func (e *env) timeRemaining(dir, status string) *int64 {
	if status != "Discharging" {
		return nil
	}
	for _, pair := range [][2]string{{"energy_now", "power_now"}, {"charge_now", "current_now"}} {
		now, err1 := e.readInt(dir + pair[0])
		rate, err2 := e.readInt(dir + pair[1])
		if err1 != nil || err2 != nil || now < 0 {
			continue
		}
		if rate < 0 {
			rate = -rate // some firmware reports a negative current while discharging
		}
		if rate == 0 {
			continue
		}
		return ptr(now * 3600 / rate)
	}
	return nil
}

// fillFromUPower asks upower for the first battery device: `upower -e`, then `upower -i <path>`.
// Absence of upower, or any failure, leaves s as it was.
func (e *env) fillFromUPower(ctx context.Context, s *supply) {
	out, err := e.execOK(ctx, "upower", "-e")
	if err != nil {
		return
	}
	var path string
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.Contains(line, "battery_") && upowerPathRE.MatchString(line) {
			path = line
			break
		}
	}
	if path == "" {
		return
	}
	info, err := e.execOK(ctx, "upower", "-i", path)
	if err != nil {
		return
	}
	pct, state, remaining, ok := parseUPower(info)
	if !ok {
		return
	}
	s.haveBattery = true
	s.percent = &pct
	switch state {
	case "discharging":
		s.status = "Discharging"
		s.timeRemainingS = remaining
	case "charging":
		s.status = "Charging"
		s.acOnline = true
	case "fully-charged", "pending-charge":
		s.status = "Full"
		s.acOnline = true // a full or waiting battery means line power
	}
}

// parseUPower reads percentage, state and "time to empty" from `upower -i` output.
func parseUPower(b []byte) (percent int, state string, remainingS *int64, ok bool) {
	gotPct := false
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		k, v, found := strings.Cut(sc.Text(), ":")
		if !found {
			continue
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		switch k {
		case "percentage":
			f, err := strconv.ParseFloat(strings.TrimSuffix(v, "%"), 64)
			if err != nil || f < 0 || f > 100 {
				return 0, "", nil, false
			}
			percent, gotPct = int(f+0.5), true
		case "state":
			state = v
		case "time to empty":
			remainingS = parseDuration(v)
		}
	}
	return percent, state, remainingS, gotPct
}

// parseDuration reads upower's "3.2 hours", "45.0 minutes", "30 seconds", "1.1 days".
func parseDuration(v string) *int64 {
	f := strings.Fields(v)
	if len(f) != 2 {
		return nil
	}
	n, err := strconv.ParseFloat(f[0], 64)
	if err != nil || n < 0 {
		return nil
	}
	mult := map[string]float64{"seconds": 1, "minutes": 60, "hours": 3600, "days": 86400}[f[1]]
	if mult == 0 {
		return nil
	}
	return ptr(int64(n*mult + 0.5))
}

var profileRE = regexp.MustCompile(`^[a-z][a-z-]{0,31}$`)

// readProfile runs `powerprofilesctl get`. Absence of the tool, a failure or an unexpected answer
// is "unknown" (nil), never an error.
func (e *env) readProfile(ctx context.Context) *string {
	out, err := e.execOK(ctx, "powerprofilesctl", "get")
	if err != nil {
		return nil
	}
	s := strings.TrimSpace(string(out))
	if !profileRE.MatchString(s) {
		return nil
	}
	return &s
}

// PowerEvent is the payload of the hardware.power event (agent.md section 3.9).
type PowerEvent struct {
	Source         string `json:"source"`
	BatteryPercent *int   `json:"battery_percent"`
	Charging       bool   `json:"charging"`
}

func eventOf(p Power) PowerEvent {
	return PowerEvent{Source: p.Source, BatteryPercent: p.BatteryPercent, Charging: p.Charging}
}

func samePower(a, b PowerEvent) bool {
	if a.Source != b.Source || a.Charging != b.Charging {
		return false
	}
	if (a.BatteryPercent == nil) != (b.BatteryPercent == nil) {
		return false
	}
	return a.BatteryPercent == nil || *a.BatteryPercent == *b.BatteryPercent
}

func (p PowerEvent) String() string {
	pct := "none"
	if p.BatteryPercent != nil {
		pct = strconv.Itoa(*p.BatteryPercent) + "%"
	}
	return fmt.Sprintf("%s %s charging=%v", p.Source, pct, p.Charging)
}
