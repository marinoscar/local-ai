package hardware

import (
	"context"
	"testing"
	"testing/fstest"

	"github.com/marinoscar/local-ai/agent/internal/execx"
	execfake "github.com/marinoscar/local-ai/agent/internal/execx/fake"
)

const upower = "/usr/bin/upower"

func TestReadPowerFromSysfsFixtures(t *testing.T) {
	cases := []struct {
		fixture   string
		source    string
		percent   *int
		charging  bool
		remaining *int64
	}{
		{"battery-ac", SourceAC, ptr(100), false, nil},
		{"battery-charging", SourceAC, ptr(42), true, nil},
		// energy_now / power_now x 3600 = 32000000 / 12000000 x 3600 = 9600 s
		{"battery-discharging-energy", SourceBattery, ptr(64), false, ptr[int64](9600)},
		// charge_now / current_now x 3600 = 3000000 / 1500000 x 3600 = 7200 s
		{"battery-discharging-charge", SourceBattery, ptr(55), false, ptr[int64](7200)},
		{"battery-desktop", SourceAC, nil, false, nil},
	}
	for _, c := range cases {
		t.Run(c.fixture, func(t *testing.T) {
			r := execfake.New() // any upower call would fail the test: sysfs is complete
			p := testEnv(fixture(t, "sysfs/"+c.fixture), r).readPower(context.Background())
			if p.Source != c.source || p.Charging != c.charging {
				t.Errorf("power = %+v", p)
			}
			if (p.BatteryPercent == nil) != (c.percent == nil) || (c.percent != nil && *p.BatteryPercent != *c.percent) {
				t.Errorf("percent = %v, want %v", p.BatteryPercent, c.percent)
			}
			if (p.TimeRemainingS == nil) != (c.remaining == nil) || (c.remaining != nil && *p.TimeRemainingS != *c.remaining) {
				t.Errorf("time remaining = %v, want %v", p.TimeRemainingS, c.remaining)
			}
			if len(r.Calls()) != 0 {
				t.Errorf("unexpected commands: %v", r.Calls())
			}
		})
	}
}

func upowerRunner(t testing.TB, info string) *execfake.Runner {
	return execfake.New().
		Expect(execfake.Key(upower, "-e"), ok(string(testdata(t, "upower/e.txt"))), nil).
		Expect(execfake.Key(upower, "-i", "/org/freedesktop/UPower/devices/battery_BAT0"), ok(string(testdata(t, "upower/"+info))), nil)
}

func TestReadPowerFallsBackToUPowerWhenSysfsIsMissing(t *testing.T) {
	p := testEnv(fixture(t, "sysfs/battery-no-sysfs"), upowerRunner(t, "i-discharging.txt")).readPower(context.Background())
	if p.Source != SourceBattery || p.BatteryPercent == nil || *p.BatteryPercent != 64 {
		t.Fatalf("power = %+v", p)
	}
	// "3.2 hours" = 11520 s
	if p.TimeRemainingS == nil || *p.TimeRemainingS != 11520 {
		t.Errorf("time remaining = %v", p.TimeRemainingS)
	}
	full := testEnv(fixture(t, "sysfs/battery-no-sysfs"), upowerRunner(t, "i-full.txt")).readPower(context.Background())
	if full.Source != SourceAC || full.BatteryPercent == nil || *full.BatteryPercent != 100 || full.TimeRemainingS != nil {
		t.Errorf("full battery = %+v", full)
	}
}

func TestReadPowerFallsBackWhenTheBatteryHasNoCapacity(t *testing.T) {
	p := testEnv(fixture(t, "sysfs/battery-incomplete"), upowerRunner(t, "i-discharging.txt")).readPower(context.Background())
	if p.Source != SourceBattery || p.BatteryPercent == nil || *p.BatteryPercent != 64 {
		t.Fatalf("power = %+v", p)
	}
}

func TestReadPowerWithoutAnyDataIsUnknownNotAnError(t *testing.T) {
	cases := []struct {
		name string
		run  *execfake.Runner
		look func(string) (string, error)
	}{
		{"upower not installed", execfake.New(), only()},
		{"upower -e fails", execfake.New().Expect(execfake.Key(upower, "-e"), exit(1, "x"), nil), nil},
		{"no battery device listed", execfake.New().Expect(execfake.Key(upower, "-e"), ok("/org/freedesktop/UPower/devices/line_power_AC\n"), nil), nil},
		{"upower -e times out", execfake.New().Expect(execfake.Key(upower, "-e"), execx.Result{}, execx.ErrTimeout), nil},
		{"hostile device path is never passed on", execfake.New().Expect(execfake.Key(upower, "-e"), ok("/org/freedesktop/UPower/devices/battery_x; rm -rf /\n"), nil), nil},
		{"upower -i fails", upowerRunner(t, "i-discharging.txt").Expect(execfake.Key(upower, "-i", "/org/freedesktop/UPower/devices/battery_BAT0"), exit(1, "x"), nil), nil},
		{"upower -i without percentage", upowerRunner(t, "i-discharging.txt").Expect(execfake.Key(upower, "-i", "/org/freedesktop/UPower/devices/battery_BAT0"), ok("state: discharging\n"), nil), nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := testEnv(fixture(t, "sysfs/battery-no-sysfs"), c.run)
			if c.look != nil {
				e.look = c.look
			}
			p := e.readPower(context.Background())
			if p.Source != SourceUnknown || p.BatteryPercent != nil || p.TimeRemainingS != nil || p.Charging {
				t.Errorf("power = %+v, want unknown without battery", p)
			}
		})
	}
}

func TestReadPowerSysfsEdgeCases(t *testing.T) {
	bat := func(extra map[string]string) fstest.MapFS {
		m := fstest.MapFS{
			"sys/class/power_supply/AC/type":         file("Mains\n"),
			"sys/class/power_supply/AC/online":       file("0\n"),
			"sys/class/power_supply/BAT0/type":       file("Battery\n"),
			"sys/class/power_supply/BAT0/capacity":   file("30\n"),
			"sys/class/power_supply/BAT0/status":     file("Discharging\n"),
			"sys/class/power_supply/BAT0/energy_now": file("30000000\n"),
			"sys/class/power_supply/BAT0/power_now":  file("15000000\n"),
		}
		for k, v := range extra {
			if v == "" {
				delete(m, k)
			} else {
				m[k] = file(v)
			}
		}
		return m
	}
	cases := []struct {
		name      string
		fsys      fstest.MapFS
		source    string
		remaining *int64
	}{
		{"discharging with rates", bat(nil), SourceBattery, ptr[int64](7200)},
		{"zero power_now leaves the estimate empty", bat(map[string]string{"sys/class/power_supply/BAT0/power_now": "0\n"}), SourceBattery, nil},
		{"negative current is taken as a magnitude", bat(map[string]string{
			"sys/class/power_supply/BAT0/power_now": "", "sys/class/power_supply/BAT0/charge_now": "2000000\n", "sys/class/power_supply/BAT0/current_now": "-1000000\n",
		}), SourceBattery, ptr[int64](7200)},
		{"no rates at all", bat(map[string]string{"sys/class/power_supply/BAT0/power_now": "", "sys/class/power_supply/BAT0/energy_now": ""}), SourceBattery, nil},
		{"Not charging on battery is unknown", bat(map[string]string{"sys/class/power_supply/BAT0/status": "Not charging\n"}), SourceUnknown, nil},
		{"USB-C charger online counts as AC", bat(map[string]string{
			"sys/class/power_supply/ucsi-source-psy-USBC000:001/type": "USB\n", "sys/class/power_supply/ucsi-source-psy-USBC000:001/online": "1\n",
		}), SourceAC, nil},
		{"a peripheral battery is ignored", bat(map[string]string{
			"sys/class/power_supply/hid-0001/type": "Battery\n", "sys/class/power_supply/hid-0001/scope": "Device\n", "sys/class/power_supply/hid-0001/capacity": "5\n",
		}), SourceBattery, ptr[int64](7200)},
		{"a battery that is not present is ignored", bat(map[string]string{"sys/class/power_supply/BAT0/present": "0\n"}), SourceUnknown, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := testEnv(c.fsys, execfake.New()).readPower(context.Background())
			if p.Source != c.source {
				t.Errorf("source = %q, want %q (%+v)", p.Source, c.source, p)
			}
			if (p.TimeRemainingS == nil) != (c.remaining == nil) || (c.remaining != nil && *p.TimeRemainingS != *c.remaining) {
				t.Errorf("remaining = %v, want %v", p.TimeRemainingS, c.remaining)
			}
		})
	}
	// A capacity outside 0..100 is not trusted: upower is asked instead.
	bad := bat(map[string]string{"sys/class/power_supply/BAT0/capacity": "250\n"})
	p := testEnv(bad, upowerRunner(t, "i-discharging.txt")).readPower(context.Background())
	if p.BatteryPercent == nil || *p.BatteryPercent != 64 {
		t.Errorf("bad capacity: %+v", p)
	}
}

func TestParseUPower(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		pct     int
		state   string
		remain  *int64
		wantOK  bool
		isEmpty bool
	}{
		{"hours", "state: discharging\npercentage: 64%\ntime to empty: 3.2 hours\n", 64, "discharging", ptr[int64](11520), true, false},
		{"minutes", "state: discharging\npercentage: 9%\ntime to empty: 45.0 minutes\n", 9, "discharging", ptr[int64](2700), true, false},
		{"seconds", "percentage: 1%\ntime to empty: 30 seconds\n", 1, "", ptr[int64](30), true, false},
		{"days", "percentage: 100%\ntime to empty: 1.5 days\n", 100, "", ptr[int64](129600), true, false},
		{"fractional percentage rounds", "percentage: 63.6%\n", 64, "", nil, true, false},
		{"unknown unit", "percentage: 5%\ntime to empty: 3 fortnights\n", 5, "", nil, true, false},
		{"no percentage", "state: discharging\n", 0, "discharging", nil, false, false},
		{"percentage out of range", "percentage: 400%\n", 0, "", nil, false, false},
		{"percentage garbage", "percentage: lots\n", 0, "", nil, false, false},
		{"empty", "", 0, "", nil, false, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			pct, state, remain, ok := parseUPower([]byte(c.in))
			if ok != c.wantOK {
				t.Fatalf("ok = %v", ok)
			}
			if !ok {
				return
			}
			if pct != c.pct || state != c.state || (remain == nil) != (c.remain == nil) || (c.remain != nil && *remain != *c.remain) {
				t.Errorf("got %d %q %v", pct, state, remain)
			}
		})
	}
}

func TestReadProfile(t *testing.T) {
	key := execfake.Key("/usr/bin/powerprofilesctl", "get")
	cases := []struct {
		name string
		run  *execfake.Runner
		look func(string) (string, error)
		want string
	}{
		{"performance", execfake.New().Expect(key, ok(string(testdata(t, "powerprofiles/get-performance.txt"))), nil), nil, "performance"},
		{"balanced", execfake.New().Expect(key, ok(string(testdata(t, "powerprofiles/get-balanced.txt"))), nil), nil, "balanced"},
		{"not installed", execfake.New(), only(), ""},
		{"fails", execfake.New().Expect(key, exit(1, "x"), nil), nil, ""},
		{"unexpected answer", execfake.New().Expect(key, ok("Error: daemon is not running\nsee the log"), nil), nil, ""},
		{"empty", execfake.New().Expect(key, ok("\n"), nil), nil, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := testEnv(nil, c.run)
			if c.look != nil {
				e.look = c.look
			}
			got := e.readProfile(context.Background())
			if (got == nil) != (c.want == "") || (got != nil && *got != c.want) {
				t.Errorf("profile = %v, want %q", got, c.want)
			}
		})
	}
}

func TestPowerEventComparison(t *testing.T) {
	a := PowerEvent{Source: SourceAC, BatteryPercent: ptr(80)}
	for _, c := range []struct {
		name string
		b    PowerEvent
		same bool
	}{
		{"identical", PowerEvent{Source: SourceAC, BatteryPercent: ptr(80)}, true},
		{"percent changed", PowerEvent{Source: SourceAC, BatteryPercent: ptr(79)}, false},
		{"source changed", PowerEvent{Source: SourceBattery, BatteryPercent: ptr(80)}, false},
		{"charging changed", PowerEvent{Source: SourceAC, BatteryPercent: ptr(80), Charging: true}, false},
		{"battery vanished", PowerEvent{Source: SourceAC}, false},
	} {
		if got := samePower(a, c.b); got != c.same {
			t.Errorf("%s: samePower = %v", c.name, got)
		}
	}
	if !samePower(PowerEvent{Source: SourceUnknown}, PowerEvent{Source: SourceUnknown}) {
		t.Error("two batteryless events must be equal")
	}
	if s := a.String(); s == "" {
		t.Error("String is empty")
	}
}

func TestReadThermalFixtures(t *testing.T) {
	cases := []struct {
		fixture   string
		cpu       *float64
		zones     []ThermalZone
		throttles int64
	}{
		{"thermal-intel", ptr(72.0), []ThermalZone{{"x86_pkg_temp", 72}}, 3},
		// k10temp hwmon: the hottest of its inputs (71), not nvme and not amdgpu.
		{"thermal-amd", ptr(71.0), []ThermalZone{{"k10temp", 71}}, 0},
		// acpitz is only the fallback: the hottest of the two zones.
		{"thermal-acpitz-only", ptr(58.0), []ThermalZone{{"acpitz", 55}, {"acpitz", 58}}, 0},
		{"thermal-none", nil, nil, 0},
	}
	for _, c := range cases {
		t.Run(c.fixture, func(t *testing.T) {
			th := testEnv(fixture(t, "sysfs/"+c.fixture), nil).readThermal()
			if (th.CPUTempC == nil) != (c.cpu == nil) || (c.cpu != nil && *th.CPUTempC != *c.cpu) {
				t.Errorf("cpu temp = %v, want %v", th.CPUTempC, c.cpu)
			}
			if th.Zones == nil || len(th.Zones) != len(c.zones) {
				t.Fatalf("zones = %#v, want %v", th.Zones, c.zones)
			}
			for i, z := range c.zones {
				if th.Zones[i] != z {
					t.Errorf("zone %d = %+v, want %+v", i, th.Zones[i], z)
				}
			}
			if th.CPUThrottleEvents != c.throttles {
				t.Errorf("throttle events = %d, want %d", th.CPUThrottleEvents, c.throttles)
			}
		})
	}
}

func TestReadThermalIgnoresBrokenSensors(t *testing.T) {
	fsys := fstest.MapFS{
		"sys/class/thermal/thermal_zone0/type": file("x86_pkg_temp\n"), "sys/class/thermal/thermal_zone0/temp": file("0\n"),
		"sys/class/thermal/thermal_zone1/type": file("TCPU\n"), "sys/class/thermal/thermal_zone1/temp": file("-274000\n"),
		"sys/class/thermal/thermal_zone2/type": file("cpu-thermal\n"), "sys/class/thermal/thermal_zone2/temp": file("garbage\n"),
		"sys/class/thermal/thermal_zone3/type": file("cpu-thermal\n"), // no temp file
		"sys/class/thermal/thermal_zone4/temp": file("50000\n"),       // no type file
		"sys/class/thermal/thermal_zone5/type": file("TCPU\n"), "sys/class/thermal/thermal_zone5/temp": file("63000\n"),
		"sys/class/thermal/cooling_device0/type": file("Processor\n"),
	}
	th := testEnv(fsys, nil).readThermal()
	if th.CPUTempC == nil || *th.CPUTempC != 63 || len(th.Zones) != 1 {
		t.Errorf("thermal = %+v", th)
	}
}

func TestReadThermalPrefersCPUSensorsOverAcpitz(t *testing.T) {
	fsys := fstest.MapFS{
		"sys/class/thermal/thermal_zone0/type": file("acpitz\n"), "sys/class/thermal/thermal_zone0/temp": file("90000\n"),
		"sys/class/thermal/thermal_zone1/type": file("x86_pkg_temp\n"), "sys/class/thermal/thermal_zone1/temp": file("60000\n"),
		"sys/class/hwmon/hwmon0/name": file("coretemp\n"), "sys/class/hwmon/hwmon0/temp1_input": file("64000\n"), "sys/class/hwmon/hwmon0/temp2_input": file("66500\n"),
	}
	th := testEnv(fsys, nil).readThermal()
	if th.CPUTempC == nil || *th.CPUTempC != 66.5 || len(th.Zones) != 2 {
		t.Errorf("thermal = %+v (acpitz at 90 must not win while real CPU sensors exist)", th)
	}
}
