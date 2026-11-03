package cloud

import (
	"encoding/json"
	"sync"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/events"
	"github.com/marinoscar/local-ai/agent/internal/hardware"
)

// The heartbeat event queue (agent.md section 4.8.3): at most 50 events travel in one heartbeat, the
// queue holds 200 (the oldest is dropped), and the events of a failed heartbeat go back to the front.
const (
	// MaxHeartbeatEvents is the most events one heartbeat carries.
	MaxHeartbeatEvents = 50
	// MaxQueuedEvents is the size of the queue.
	MaxQueuedEvents = 200
)

// Event severities of the heartbeat.
const (
	SevInfo     = "info"
	SevWarning  = "warning"
	SevCritical = "critical"
)

// Event types of the heartbeat: EAB's own power and node events, then ours.
const (
	EvPowerSourceChanged  = "power.source_changed"
	EvPowerBatteryLow     = "power.battery_low"
	EvPowerBatteryCrit    = "power.battery_critical"
	EvPowerBatteryRecover = "power.battery_recovered"
	EvNodeSleepResumed    = "node.sleep_resumed"
	EvNodeDiskLow         = "node.disk_low"
	EvAlertRaised         = "localai.alert.raised"
	EvAlertCleared        = "localai.alert.cleared"
	EvModelLoaded         = "localai.model.loaded"
	EvModelFailed         = "localai.model.failed"
	EvModeChanged         = "localai.mode.changed"
)

// HeartbeatEvent is one entry of the heartbeat's "events".
type HeartbeatEvent struct {
	Type     string         `json:"type"`
	Severity string         `json:"severity"`
	At       string         `json:"at"`
	Attrs    map[string]any `json:"attrs"`
}

// EventQueue is the bounded queue of events waiting for the next heartbeat. It is safe for
// concurrent use.
type EventQueue struct {
	mu sync.Mutex
	q  []HeartbeatEvent
}

// Push appends events, dropping the oldest ones beyond MaxQueuedEvents.
func (e *EventQueue) Push(evs ...HeartbeatEvent) {
	if len(evs) == 0 {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.q = append(e.q, evs...)
	if over := len(e.q) - MaxQueuedEvents; over > 0 {
		e.q = append([]HeartbeatEvent(nil), e.q[over:]...)
	}
}

// Take removes and returns up to MaxHeartbeatEvents events, oldest first.
func (e *EventQueue) Take() []HeartbeatEvent {
	e.mu.Lock()
	defer e.mu.Unlock()
	n := len(e.q)
	if n > MaxHeartbeatEvents {
		n = MaxHeartbeatEvents
	}
	out := append([]HeartbeatEvent(nil), e.q[:n]...)
	e.q = append([]HeartbeatEvent(nil), e.q[n:]...)
	return out
}

// Requeue puts events that were not delivered back at the front, keeping the queue within its size
// (the oldest are dropped first, so the newest events survive a long outage).
func (e *EventQueue) Requeue(evs []HeartbeatEvent) {
	if len(evs) == 0 {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.q = append(append([]HeartbeatEvent(nil), evs...), e.q...)
	if over := len(e.q) - MaxQueuedEvents; over > 0 {
		e.q = append([]HeartbeatEvent(nil), e.q[over:]...)
	}
}

// Len is the number of queued events.
func (e *EventQueue) Len() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.q)
}

// Clear drops everything (the link was unenrolled).
func (e *EventQueue) Clear() {
	e.mu.Lock()
	e.q = nil
	e.mu.Unlock()
}

func newEvent(at time.Time, typ, sev string, attrs map[string]any) HeartbeatEvent {
	if attrs == nil {
		attrs = map[string]any{}
	}
	return HeartbeatEvent{Type: typ, Severity: sev, At: at.UTC().Format(time.RFC3339), Attrs: attrs}
}

// Thresholds of the host observer. The battery bands have hysteresis: low below 20, critical below
// 10, recovered at 25 or more (or on mains power at 20 or more), so a percentage that hovers around
// 20 does not flap.
const (
	batteryLowBelow      = 20
	batteryCriticalBelow = 10
	batteryRecoveredAt   = 25
	diskLowBelowPct      = 5.0
	diskLowClearAtPct    = 10.0
	sleepGapIntervals    = 3
)

// HostObserver turns successive hardware snapshots into the EAB power events, node.sleep_resumed and
// node.disk_low. The first observation of each signal is a baseline and emits nothing (agent.md
// section 4.8.3). It is called once per heartbeat by one goroutine.
type HostObserver struct {
	last       time.Time
	haveSource bool
	source     string
	battery    int // 0 ok, 1 low, 2 critical
	haveBat    bool
	diskLow    bool
}

// Observe compares snap with what it saw before. interval is the current heartbeat interval; a gap
// between two calls of more than three intervals means the machine slept.
func (h *HostObserver) Observe(now time.Time, snap hardware.Snapshot, interval time.Duration) []HeartbeatEvent {
	var out []HeartbeatEvent
	if !h.last.IsZero() && interval > 0 && now.Sub(h.last) > sleepGapIntervals*interval {
		out = append(out, newEvent(now, EvNodeSleepResumed, SevInfo, map[string]any{"gapSeconds": int64(now.Sub(h.last) / time.Second)}))
	}
	h.last = now

	p := snap.Power
	if p.Source != "" && !p.Stale {
		if h.haveSource && p.Source != h.source {
			out = append(out, newEvent(now, EvPowerSourceChanged, SevInfo, map[string]any{"source": p.Source}))
		}
		h.haveSource, h.source = true, p.Source
	}
	if p.BatteryPercent != nil && !p.Stale {
		pct := *p.BatteryPercent
		level := h.battery
		switch {
		case pct < batteryCriticalBelow:
			level = 2
		case pct < batteryLowBelow:
			if level < 1 {
				level = 1
			}
			if level == 2 && pct >= batteryCriticalBelow {
				level = 1
			}
		case pct >= batteryRecoveredAt || (p.Source == hardware.SourceAC && pct >= batteryLowBelow):
			level = 0
		}
		if h.haveBat && level != h.battery {
			switch {
			case level == 2:
				out = append(out, newEvent(now, EvPowerBatteryCrit, SevCritical, map[string]any{"batteryPercent": pct}))
			case level == 1 && h.battery == 0:
				out = append(out, newEvent(now, EvPowerBatteryLow, SevWarning, map[string]any{"batteryPercent": pct}))
			case level == 0:
				out = append(out, newEvent(now, EvPowerBatteryRecover, SevInfo, map[string]any{"batteryPercent": pct}))
			}
		}
		h.haveBat, h.battery = true, level
	}
	if d, ok := stateDisk(snap); ok && d.TotalBytes > 0 && !d.Stale {
		pct := d.FreePct()
		switch {
		case !h.diskLow && pct < diskLowBelowPct:
			h.diskLow = true
			out = append(out, newEvent(now, EvNodeDiskLow, SevWarning, map[string]any{"freePercent": float64(int(pct*10)) / 10}))
		case h.diskLow && pct >= diskLowClearAtPct:
			h.diskLow = false
		}
	}
	return out
}

// FromBusEvent maps an agent bus event to its heartbeat event; ok is false for the events the cloud
// is not told about this way.
func FromBusEvent(ev events.Event) (HeartbeatEvent, bool) {
	at := ev.At
	switch ev.Type {
	case events.TypeAlertRaised, events.TypeAlertCleared:
		var a events.Alert
		if json.Unmarshal(ev.Data, &a) != nil || a.Code == "" {
			return HeartbeatEvent{}, false
		}
		typ := EvAlertRaised
		if ev.Type == events.TypeAlertCleared {
			typ = EvAlertCleared
		}
		return newEvent(at, typ, alertSeverity(a.Severity), map[string]any{"code": a.Code}), true
	case events.TypeModelLoaded:
		var m events.ModelLoaded
		if json.Unmarshal(ev.Data, &m) != nil || m.Model == "" {
			return HeartbeatEvent{}, false
		}
		return newEvent(at, EvModelLoaded, SevInfo, map[string]any{"model": m.Model}), true
	case events.TypeModelFailed:
		var m events.ModelFailed
		if json.Unmarshal(ev.Data, &m) != nil || m.Model == "" {
			return HeartbeatEvent{}, false
		}
		return newEvent(at, EvModelFailed, SevWarning, map[string]any{"model": m.Model}), true
	case events.TypeModeChanged:
		var m events.ModeChanged
		if json.Unmarshal(ev.Data, &m) != nil || m.To == "" {
			return HeartbeatEvent{}, false
		}
		return newEvent(at, EvModeChanged, SevInfo, map[string]any{"to": m.To}), true
	}
	return HeartbeatEvent{}, false
}

// alertSeverity maps an alert severity to the heartbeat's: info stays info, warning and error become
// warning, critical stays critical (agent.md section 4.8.3).
func alertSeverity(s string) string {
	switch s {
	case "critical":
		return SevCritical
	case "warning", "error":
		return SevWarning
	}
	return SevInfo
}

// stateDisk is the filesystem of /var/lib/localai in a hardware snapshot.
func stateDisk(snap hardware.Snapshot) (hardware.Disk, bool) {
	for _, d := range snap.Disks {
		if d.Role == hardware.RoleState {
			return d, true
		}
	}
	return hardware.Disk{}, false
}
