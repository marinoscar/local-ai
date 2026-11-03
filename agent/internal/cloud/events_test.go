package cloud

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/events"
	"github.com/marinoscar/local-ai/agent/internal/hardware"
)

func ev(i int) HeartbeatEvent {
	return HeartbeatEvent{Type: "localai.model.loaded", Severity: SevInfo, At: "2026-11-02T15:00:00Z", Attrs: map[string]any{"model": "m" + string(rune('a'+i%26)) + "-" + itoaPlain(i)}}
}

func TestEventQueueBoundsAndRequeue(t *testing.T) {
	var q EventQueue
	for i := 0; i < 250; i++ {
		q.Push(ev(i))
	}
	if q.Len() != MaxQueuedEvents {
		t.Fatalf("queue holds %d, want %d (the oldest are dropped)", q.Len(), MaxQueuedEvents)
	}
	first := q.Take()
	if len(first) != MaxHeartbeatEvents {
		t.Fatalf("a heartbeat takes %d events, want %d", len(first), MaxHeartbeatEvents)
	}
	if first[0].Attrs["model"] != ev(50).Attrs["model"] {
		t.Errorf("the oldest 50 were not the ones dropped: first is %v", first[0].Attrs)
	}
	// A failed heartbeat puts them back at the front, in order.
	q.Requeue(first)
	again := q.Take()
	if again[0].Attrs["model"] != first[0].Attrs["model"] || again[49].Attrs["model"] != first[49].Attrs["model"] {
		t.Error("requeue did not restore the order")
	}
	// Requeue never grows the queue beyond 200 and keeps the newest.
	q.Clear()
	for i := 0; i < 200; i++ {
		q.Push(ev(i))
	}
	q.Requeue([]HeartbeatEvent{ev(900), ev(901)})
	if q.Len() != 200 {
		t.Fatalf("len %d", q.Len())
	}
	all := q.Take()
	if all[0].Attrs["model"] == ev(900).Attrs["model"] {
		t.Error("requeued old events must be dropped before newer ones")
	}
	q.Clear()
	if q.Len() != 0 {
		t.Error("Clear")
	}
}

func snapWith(source string, pct *int, free, total int64) hardware.Snapshot {
	return hardware.Snapshot{
		Power: hardware.Power{Source: source, BatteryPercent: pct},
		Disks: []hardware.Disk{{Role: hardware.RoleState, TotalBytes: total, FreeBytes: free}},
	}
}

func p(n int) *int { return &n }

func TestHostObserverEmitsPowerEventsWithHysteresisAndTreatsTheFirstSampleAsBaseline(t *testing.T) {
	var o HostObserver
	now := t0
	iv := 15 * time.Second
	step := func(s hardware.Snapshot) []string {
		now = now.Add(iv)
		var types []string
		for _, e := range o.Observe(now, s, iv) {
			types = append(types, e.Type)
		}
		return types
	}
	// Baseline: nothing, even on battery at 15 %.
	if got := step(snapWith("battery", p(15), 50, 100)); len(got) != 0 {
		t.Fatalf("baseline emitted %v", got)
	}
	// The cases are a sequence: each one starts from the state the previous one left.
	for _, tc := range []struct {
		name string
		snap hardware.Snapshot
		want string
	}{
		{"back to ac", snapWith("ac", p(15), 50, 100), "power.source_changed"},
		{"critical on ac", snapWith("ac", p(9), 50, 100), "power.battery_critical"},
		{"critical stays", snapWith("ac", p(8), 50, 100), ""},
		{"into low band", snapWith("ac", p(12), 50, 100), ""},
		{"still low", snapWith("ac", p(19), 50, 100), ""},
		{"recovered on ac", snapWith("ac", p(20), 50, 100), "power.battery_recovered"},
		{"battery again", snapWith("battery", p(22), 50, 100), "power.source_changed"},
		{"low again", snapWith("battery", p(19), 50, 100), "power.battery_low"},
		{"hovering stays quiet", snapWith("battery", p(21), 50, 100), ""},
		{"flap below", snapWith("battery", p(19), 50, 100), ""},
		{"full recovery", snapWith("battery", p(26), 50, 100), "power.battery_recovered"},
	} {
		got := step(tc.snap)
		if tc.want == "" && len(got) != 0 || tc.want != "" && (len(got) != 1 || got[0] != tc.want) {
			t.Errorf("%s: got %v, want %q", tc.name, got, tc.want)
		}
	}
}

func TestHostObserverDiskLowSleepAndStalenessHysteresis(t *testing.T) {
	var o HostObserver
	iv := 15 * time.Second
	now := t0
	obs := func(s hardware.Snapshot, gap time.Duration) []HeartbeatEvent {
		now = now.Add(gap)
		return o.Observe(now, s, iv)
	}
	if got := obs(snapWith("ac", nil, 50, 100), iv); len(got) != 0 {
		t.Fatalf("baseline %v", got)
	}
	got := obs(snapWith("ac", nil, 4, 100), iv)
	if len(got) != 1 || got[0].Type != "node.disk_low" || got[0].Severity != SevWarning {
		t.Fatalf("disk low: %+v", got)
	}
	if got := obs(snapWith("ac", nil, 3, 100), iv); len(got) != 0 {
		t.Errorf("disk low repeated: %v", got)
	}
	if got := obs(snapWith("ac", nil, 8, 100), iv); len(got) != 0 {
		t.Errorf("between the bands: %v", got)
	}
	if got := obs(snapWith("ac", nil, 12, 100), iv); len(got) != 0 {
		t.Errorf("clearing is silent: %v", got)
	}
	if got := obs(snapWith("ac", nil, 4, 100), iv); len(got) != 1 {
		t.Errorf("disk low must fire again after clearing: %v", got)
	}
	// Sleep: a gap of more than three intervals.
	if got := obs(snapWith("ac", nil, 12, 100), 3*iv); len(got) != 0 {
		t.Errorf("exactly three intervals is not a sleep: %v", got)
	}
	got = obs(snapWith("ac", nil, 12, 100), 3*iv+time.Second)
	if len(got) != 1 || got[0].Type != "node.sleep_resumed" || got[0].Attrs["gapSeconds"] != int64(46) {
		t.Fatalf("sleep: %+v", got)
	}
	// A stale hardware sample changes nothing.
	stale := snapWith("battery", p(1), 1, 100)
	stale.Power.Stale = true
	stale.Disks[0].Stale = true
	if got := obs(stale, iv); len(got) != 0 {
		t.Errorf("stale sample: %v", got)
	}
}

func TestFromBusEventMapping(t *testing.T) {
	mk := func(typ string, data any) events.Event {
		raw, _ := json.Marshal(data)
		return events.Event{Type: typ, At: t0, Data: raw}
	}
	for name, tc := range map[string]struct {
		ev       events.Event
		wantType string
		wantSev  string
		attr     string
		attrVal  any
		ok       bool
	}{
		"alert info":        {mk(events.TypeAlertRaised, events.Alert{Code: "LAI-HW-001", Severity: "info"}), "localai.alert.raised", "info", "code", "LAI-HW-001", true},
		"alert warning":     {mk(events.TypeAlertRaised, events.Alert{Code: "LAI-HW-002", Severity: "warning"}), "localai.alert.raised", "warning", "code", "LAI-HW-002", true},
		"alert error":       {mk(events.TypeAlertRaised, events.Alert{Code: "LAI-HW-003", Severity: "error"}), "localai.alert.raised", "warning", "code", "LAI-HW-003", true},
		"alert critical":    {mk(events.TypeAlertCleared, events.Alert{Code: "LAI-HW-004", Severity: "critical"}), "localai.alert.cleared", "critical", "code", "LAI-HW-004", true},
		"model loaded":      {mk(events.TypeModelLoaded, events.ModelLoaded{Model: "qwen3.5-9b-q4km"}), "localai.model.loaded", "info", "model", "qwen3.5-9b-q4km", true},
		"model failed":      {mk(events.TypeModelFailed, events.ModelFailed{Model: "qwen3.5-9b-q4km"}), "localai.model.failed", "warning", "model", "qwen3.5-9b-q4km", true},
		"mode changed":      {mk(events.TypeModeChanged, events.ModeChanged{From: "online", To: "offline"}), "localai.mode.changed", "info", "to", "offline", true},
		"unrelated":         {mk(events.TypeServiceState, events.ServiceState{Name: "llama"}), "", "", "", nil, false},
		"alert no code":     {mk(events.TypeAlertRaised, events.Alert{}), "", "", "", nil, false},
		"broken payload":    {events.Event{Type: events.TypeModelLoaded, Data: json.RawMessage(`{`)}, "", "", "", nil, false},
		"model no name":     {mk(events.TypeModelLoaded, events.ModelLoaded{}), "", "", "", nil, false},
		"mode without 'to'": {mk(events.TypeModeChanged, events.ModeChanged{From: "x"}), "", "", "", nil, false},
	} {
		t.Run(name, func(t *testing.T) {
			he, ok := FromBusEvent(tc.ev)
			if ok != tc.ok {
				t.Fatalf("ok = %v", ok)
			}
			if !ok {
				return
			}
			if he.Type != tc.wantType || he.Severity != tc.wantSev || he.Attrs[tc.attr] != tc.attrVal || !strings.HasSuffix(he.At, "Z") {
				t.Errorf("event %+v", he)
			}
		})
	}
}
