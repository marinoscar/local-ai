package services

import (
	"reflect"
	"testing"

	"github.com/godbus/dbus/v5"
)

// These tests cover the pure parts of the godbus adapter. The connection itself needs a system bus
// and is covered by the manual test on a real node.

func TestUnitFilesArgs(t *testing.T) {
	tests := []struct {
		method string
		want   []any
		bad    bool
	}{
		{method: "EnableUnitFiles", want: []any{[]string{"u.service"}, false, true}},
		{method: "DisableUnitFiles", want: []any{[]string{"u.service"}, false}},
		{method: "MaskUnitFiles", want: []any{[]string{"u.service"}, false, true}},
		{method: "UnmaskUnitFiles", want: []any{[]string{"u.service"}, false}},
		{method: "StartUnit", bad: true},
		{method: "", bad: true},
	}
	for _, tc := range tests {
		got, err := unitFilesArgs(tc.method, "u.service")
		if tc.bad {
			if err == nil {
				t.Errorf("%q accepted", tc.method)
			}
			continue
		}
		if err != nil || !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: %v, %v; want %v", tc.method, got, err, tc.want)
		}
	}
}

func TestTranslateSignals(t *testing.T) {
	tests := []struct {
		name string
		in   *dbus.Signal
		want Signal
		ok   bool
	}{
		{"job removed", &dbus.Signal{
			Name: "org.freedesktop.systemd1.Manager.JobRemoved", Path: "/org/freedesktop/systemd1",
			Body: []any{uint32(7), dbus.ObjectPath("/org/freedesktop/systemd1/job/7"), "localai-llama.service", "done"}},
			Signal{Name: "JobRemoved", Job: "/org/freedesktop/systemd1/job/7", Unit: "localai-llama.service", Result: "done"}, true},
		{"job removed with a bad body", &dbus.Signal{Name: "org.freedesktop.systemd1.Manager.JobRemoved", Body: []any{uint32(7)}}, Signal{}, false},
		{"job removed with wrong types", &dbus.Signal{Name: "org.freedesktop.systemd1.Manager.JobRemoved", Body: []any{uint32(7), "x", 3, 4}}, Signal{}, false},
		{"properties changed", &dbus.Signal{
			Name: "org.freedesktop.DBus.Properties.PropertiesChanged", Path: "/org/freedesktop/systemd1/unit/localai_2dllama_2eservice",
			Body: []any{"org.freedesktop.systemd1.Unit", map[string]dbus.Variant{}, []string{}}},
			Signal{Name: "PropertiesChanged", Path: "/org/freedesktop/systemd1/unit/localai_2dllama_2eservice"}, true},
		{"another signal", &dbus.Signal{Name: "org.freedesktop.systemd1.Manager.UnitNew"}, Signal{}, false},
	}
	for _, tc := range tests {
		got, ok := translate(tc.in)
		if ok != tc.ok || got != tc.want {
			t.Errorf("%s: got %+v, %v; want %+v, %v", tc.name, got, ok, tc.want, tc.ok)
		}
	}
}

func TestPropertyConverters(t *testing.T) {
	if asInt(uint32(5)) != 5 || asInt(int32(-1)) != -1 || asInt(uint64(9)) != 9 || asInt("x") != 0 || asInt(nil) != 0 {
		t.Error("asInt")
	}
	if !asTime(uint64(0)).IsZero() || !asTime("x").IsZero() || asTime(uint64(1_000_000)).Unix() != 1 {
		t.Error("asTime")
	}
	if asString(nil) != "" || asString("a") != "a" || asString(3) != "" {
		t.Error("asString")
	}
}
