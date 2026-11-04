// Package portal serves GET /api/v1/portal/status, the reduced, localized, anonymous view the
// Survival Portal polls (agent.md section 3.8.5, survival-portal.md section 3.5).
//
// This package owns the route, the 2 s response cache, the language resolution, the capability model
// (ComputeCapabilities, survival-portal.md section 4.2) and the Builder that assembles the body from
// cached read-only views.
//
// The status is read by anyone at the keyboard, so nothing in it may identify the machine or its
// owner: no paths, keys, tokens, IP addresses, hostnames (only the node id), user names, cloud
// details, drive ids or logs. The types below have no field for any of those, and the redaction
// test marshals the body of a node full of them.
package portal

import (
	"context"
	"time"
)

// SchemaVersion is status.schema_version.
const SchemaVersion = 1

// Session says what the SPA may call (survival-portal.md section 3.5).
const (
	SessionNone   = "none"
	SessionPortal = "portal"
	SessionFull   = "full"
)

// Capability states (survival-portal.md section 4.2).
const (
	StateReady       = "ready"
	StateDegraded    = "degraded"
	StateUnavailable = "unavailable"
)

// DefaultEmergencyNumber is shown when node.yaml portal.emergency_number is not set.
const DefaultEmergencyNumber = "911"

// Request is what the route resolved before it asks for a body.
type Request struct {
	// Lang is "en" or "es": the language of titles and fixes.
	Lang string
	// Session is SessionNone, SessionPortal or SessionFull, from the caller's principal.
	Session string
	// Now is the time of the request.
	Now time.Time
}

// StatusBuilder builds the body of GET /portal/status. Implementations read cached state only: no
// check runs on a request.
type StatusBuilder interface {
	Build(ctx context.Context, req Request) (Status, error)
}

// Status is the body (schema PortalStatusV1, survival-portal.md section 3.5). first_aid is emitted empty
// here: PORTAL-11 fills it.
type Status struct {
	SchemaVersion   int          `json:"schema_version"`
	GeneratedAt     string       `json:"generated_at"`
	NodeID          string       `json:"node_id"`
	Mode            string       `json:"mode"`
	Language        string       `json:"language"`
	EmergencyNumber string       `json:"emergency_number"`
	Session         string       `json:"session"`
	Power           Power        `json:"power"`
	ClockOK         bool         `json:"clock_ok"`
	Brain           Brain        `json:"brain"`
	Capabilities    Capabilities `json:"capabilities"`
	FirstAid        []FirstAid   `json:"first_aid"`
	Ark             Ark          `json:"ark"`
	Alerts          []Alert      `json:"alerts"`
}

// Power is status.power.
type Power struct {
	OnAC        bool   `json:"on_ac"`
	BatteryPct  *int   `json:"battery_pct"`
	MinutesLeft *int   `json:"minutes_left"`
	State       string `json:"state"`
	Low         bool   `json:"low"`
	Critical    bool   `json:"critical"`
}

// Power states.
const (
	PowerDischarging = "discharging"
	PowerCharging    = "charging"
	PowerFull        = "full"
	PowerNoBattery   = "no_battery"
)

// Brain is status.brain. Loading is null when no model is starting.
type Brain struct {
	Loaded  []Model  `json:"loaded"`
	Loading *Model   `json:"loading"`
	Choices []Choice `json:"choices"`
}

// Model is a loaded or loading model: its id and display name, never a path.
type Model struct {
	ID          string   `json:"id"`
	DisplayName string   `json:"display_name"`
	Roles       []string `json:"roles"`
	Vision      bool     `json:"vision"`
}

// Choice is a model the Portal may offer in "Switch brain".
type Choice struct {
	ID          string   `json:"id"`
	DisplayName string   `json:"display_name"`
	Summary     string   `json:"summary"`
	Roles       []string `json:"roles"`
	Vision      bool     `json:"vision"`
	Loadable    bool     `json:"loadable"`
	Survival    bool     `json:"survival"`
	SpeedHint   string   `json:"speed_hint,omitempty"`
}

// FirstAid is a first-aid entry (filled by PORTAL-11).
type FirstAid struct {
	Key       string `json:"key"`
	Title     string `json:"title"`
	URL       string `json:"url"`
	Available bool   `json:"available"`
}

// Ark is status.ark: the readiness of the content roots in three facts.
type Ark struct {
	// Overall is green, yellow or red ("unknown" when no report exists).
	Overall         string `json:"overall"`
	SurvivalCapable bool   `json:"survival_capable"`
	DriveConnected  bool   `json:"drive_connected"`
}

// Alert is an active alert in the language of the request. It has no details: they can carry paths
// and device names.
type Alert struct {
	Code     string `json:"code"`
	Severity string `json:"severity"`
	Title    string `json:"title"`
	Fix      string `json:"fix"`
}

// Battery thresholds of the Portal's status line (hardware.md thresholds: 20 % and 10 %).
const (
	lowPct      = 20
	criticalPct = 10
)
