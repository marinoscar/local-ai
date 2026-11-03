package health

import (
	"time"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/services"
)

// State is the health state of a companion service (companion-services.md section 4.6).
type State string

// The six states.
const (
	// StateOK: running and answering, nothing wrong.
	StateOK State = "ok"
	// StateDegraded: works, with a problem (a readiness finding of warning or worse, or a liveness
	// probe that failed fewer times than the restart threshold).
	StateDegraded State = "degraded"
	// StateDown: should run and does not respond (or is not running), or the restart budget is spent.
	StateDown State = "down"
	// StateMissingContent: nothing to serve; the unit is stopped on purpose and never restarted.
	StateMissingContent State = "missing_content"
	// StateDisabled: turned off in node.yaml; never probed, never restarted.
	StateDisabled State = "disabled"
	// StateStarting: the first 60 seconds after a start; failures do not count yet.
	StateStarting State = "starting"
)

// States lists the six values, in the order of the spec.
var States = []State{StateOK, StateDegraded, StateDown, StateMissingContent, StateDisabled, StateStarting}

// Valid reports whether s is one of the six states.
func (s State) Valid() bool {
	for _, v := range States {
		if s == v {
			return true
		}
	}
	return false
}

// Problem is one finding of a service: a doctor code and its severity. It is the element of the
// problems array of GET /services.
type Problem struct {
	Code     string          `json:"code"`
	Severity doctor.Severity `json:"severity"`
}

// Book is one library book of the kiwix record.
type Book struct {
	ContentID string `json:"content_id"`
	Name      string `json:"name"`
	Root      string `json:"root"`
}

// Content says whether a service has anything to serve. A service with Missing set is in the state
// missing_content: the supervisor neither probes nor restarts it. Content must be cheap and local: it
// runs on every tick, also while the unit is stopped.
type Content struct {
	// Missing is true when there is nothing to serve (no ZIM, no model, no voice, no map).
	Missing bool
	// Code is the doctor code that says why (LAI-SVC-001, 010, 022, 030, ...).
	Code string
}

// Readiness is what a service reports once its liveness probe passed (the "readiness detail" column
// of companion-services.md section 4.6). The per-service fields are filled by the owner of the probe.
type Readiness struct {
	// Detail is one short line for the record ("7 books").
	Detail string
	// Problems are the findings; one of warning or worse makes the state degraded.
	Problems []Problem
	// Books, Voices and Maps are nil when the field does not apply to the service and empty when it
	// applies and there is none.
	Books  []Book
	Voices []string
	Maps   []string
	// Model and Device are whisper's selected model and the device it runs on.
	Model, Device string
}

// Record is the supervisor's view of one service: the row of GET /services (agent.md section 3.8.22,
// companion-services.md section 4.6).
type Record struct {
	// Name is kiwix, whisper, piper or maps.
	Name string
	// Unit is the systemd unit; empty for piper and maps (null on the wire).
	Unit  string
	State State
	// Active and Sub are systemd's ActiveState and SubState; empty without a unit.
	Active, Sub string
	// Since is when the unit became active; zero when it is not running or has no unit.
	Since time.Time
	// Restarts is systemd's NRestarts.
	Restarts int
	// Healthy is State == StateOK.
	Healthy bool
	// Enabled reports whether the unit starts at boot (UnitFileState); false without a unit.
	Enabled bool
	// LastExit is nil when the main process never exited.
	LastExit *services.LastExit
	// LastCheck is when the liveness probe last ran; zero before the first run.
	LastCheck time.Time
	Detail    string
	Books     []Book
	Model     string
	Device    string
	Voices    []string
	Maps      []string
	// Problems are the findings, the primary one first (LAI-SVC-051 for a service the budget gave up
	// on, otherwise the worst).
	Problems []Problem
}

// HasUnit reports whether the service runs as a systemd unit (kiwix and whisper).
func (r Record) HasUnit() bool { return r.Unit != "" }

// Reduced is a service as the cloud heartbeat carries it: name and state only.
type Reduced struct {
	Name  string `json:"name"`
	State State  `json:"state"`
}

// order is the position of a service in every list: llama, kiwix, whisper, piper, maps. Names this
// package does not know sort last.
var order = map[string]int{"llama": 0, "kiwix": 1, "whisper": 2, "piper": 3, "maps": 4}

// Order returns the position of a service in the lists of GET /services and GET /status.
func Order(name string) int {
	if i, ok := order[name]; ok {
		return i
	}
	return len(order)
}
