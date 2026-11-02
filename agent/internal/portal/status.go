// Package portal serves GET /api/v1/portal/status, the reduced, localized, anonymous view the
// Survival Portal polls (agent.md section 3.8.5, survival-portal.md section 3.5).
//
// This package owns the route, the 2 s response cache, the language resolution and a minimal
// builder (mode, language, emergency number, power, the loaded brain, the chat capability and the
// alerts). PORTAL-01 replaces the builder with the full capability model by implementing
// StatusBuilder; the route and the cache stay.
//
// The status is read by anyone at the keyboard, so nothing in it may identify the machine or its
// owner: no paths, keys, tokens, IP addresses, hostnames (only the node id), user names, cloud
// details, drive ids or logs. The types below have no field for any of those, and the redaction
// test marshals the body of a node full of them.
package portal

import (
	"context"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/catalog"
	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/hardware"
	"github.com/marinoscar/local-ai/agent/internal/modes"
	"github.com/marinoscar/local-ai/agent/internal/router"
	"github.com/marinoscar/local-ai/agent/internal/statusapi"
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

// Status is the body (schema PortalStatus). Optional parts of survival-portal.md section 3.5 that the
// minimal builder does not compute (ark, first_aid entries, brain.choices) are present and empty.
type Status struct {
	SchemaVersion   int                   `json:"schema_version"`
	GeneratedAt     string                `json:"generated_at"`
	NodeID          string                `json:"node_id"`
	Mode            string                `json:"mode"`
	Language        string                `json:"language"`
	EmergencyNumber string                `json:"emergency_number"`
	Session         string                `json:"session"`
	Power           Power                 `json:"power"`
	ClockOK         bool                  `json:"clock_ok"`
	Brain           Brain                 `json:"brain"`
	Capabilities    map[string]Capability `json:"capabilities"`
	FirstAid        []FirstAid            `json:"first_aid"`
	Alerts          []Alert               `json:"alerts"`
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

// Choice is a model the Portal may offer in "Switch brain" (filled by PORTAL-01).
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

// Capability is one button of the home screen. Code is set only when a problem causes the state.
type Capability struct {
	State  string `json:"state"`
	Code   string `json:"code,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// FirstAid is a first-aid entry (filled by PORTAL-11).
type FirstAid struct {
	Key       string `json:"key"`
	Title     string `json:"title"`
	URL       string `json:"url"`
	Available bool   `json:"available"`
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

// MinimalSources are the views the minimal builder reads. Config and Catalog are required in
// practice; everything else may be nil.
type MinimalSources struct {
	Config   statusapi.ConfigView
	State    statusapi.StateView
	Mode     statusapi.ModeView
	Hardware statusapi.HardwareView
	Router   statusapi.RouterView
	Catalog  *statusapi.CatalogStore
	Alerts   statusapi.AlertView
	// Settings resolves emergency_number (and the language, for the route); nil uses the defaults.
	Settings func() Settings
	// BuildTime is when the agent binary was built: a clock earlier than it is wrong (clock_ok false).
	// The zero time disables the check.
	BuildTime time.Time
}

// Minimal is the builder AGENT-14 ships: enough for the Portal to show the mode, the battery, the
// running brain and the problems. PORTAL-01 completes it.
type Minimal struct{ src MinimalSources }

var _ StatusBuilder = (*Minimal)(nil)

// NewMinimal returns the minimal builder over src.
func NewMinimal(src MinimalSources) *Minimal { return &Minimal{src: src} }

// Build implements StatusBuilder.
func (b *Minimal) Build(_ context.Context, req Request) (Status, error) {
	s := b.src
	lang := req.Lang
	if lang != doctor.LangES {
		lang = doctor.LangEN
	}
	cfg := config.Defaults()
	if s.Config != nil {
		cfg = s.Config.Get()
	}
	settings := Settings{EmergencyNumber: DefaultEmergencyNumber}
	if s.Settings != nil {
		settings = s.Settings()
	}
	if settings.EmergencyNumber == "" {
		settings.EmergencyNumber = DefaultEmergencyNumber
	}
	var snap hardware.Snapshot
	if s.Hardware != nil {
		snap = s.Hardware.Snapshot()
	}
	session := req.Session
	if session == "" {
		session = SessionNone
	}
	brain := b.brain()
	return Status{
		SchemaVersion: SchemaVersion, GeneratedAt: req.Now.UTC().Format(time.RFC3339),
		NodeID: cfg.Node.ID, Mode: b.mode(), Language: lang, EmergencyNumber: settings.EmergencyNumber,
		Session: session, Power: powerOf(snap.Power),
		ClockOK:      s.BuildTime.IsZero() || !req.Now.Before(s.BuildTime),
		Brain:        brain,
		Capabilities: map[string]Capability{"chat": chatCapability(brain)},
		FirstAid:     []FirstAid{},
		Alerts:       b.alerts(lang),
	}, nil
}

// mode is the committed mode: the engine's, else state.json's, else offline (the safe value).
func (b *Minimal) mode() string {
	switch {
	case b.src.Mode != nil:
		return string(b.src.Mode.Info().Mode)
	case b.src.State != nil:
		return string(b.src.State.Snapshot().Mode)
	}
	return string(modes.Offline)
}

func powerOf(p hardware.Power) Power {
	out := Power{OnAC: p.Source == hardware.SourceAC, BatteryPct: p.BatteryPercent}
	if p.TimeRemainingS != nil {
		m := int(*p.TimeRemainingS / 60)
		out.MinutesLeft = &m
	}
	switch {
	case !p.HasBattery():
		out.State = PowerNoBattery
	case p.Source == hardware.SourceBattery:
		out.State = PowerDischarging
		out.Low = *p.BatteryPercent < lowPct
		out.Critical = *p.BatteryPercent < criticalPct
	case p.Charging:
		out.State = PowerCharging
	default:
		out.State = PowerFull
	}
	return out
}

// brain lists the loaded and the loading models the router reports and the catalog knows. A model
// that is not in the catalog (a LLAMA_CACHE model) is left out: the Portal offers only what the
// catalog describes.
func (b *Minimal) brain() Brain {
	out := Brain{Loaded: []Model{}, Choices: []Choice{}}
	if b.src.Router == nil || b.src.Catalog == nil {
		return out
	}
	cat := b.src.Catalog.Get().Catalog
	for _, ms := range b.src.Router.View().Models {
		cm, ok := cat.Model(ms.ID)
		if !ok {
			continue
		}
		m := modelOf(cm)
		switch {
		case ms.Failed:
		case ms.Status == router.StatusLoaded:
			out.Loaded = append(out.Loaded, m)
		case ms.Status == router.StatusLoading && out.Loading == nil:
			out.Loading = &m
		}
	}
	return out
}

func modelOf(cm *catalog.Model) Model {
	roles := append([]string{}, cm.Roles...)
	vision := cm.Llama != nil && cm.Llama.MmprojFileKind != ""
	for _, r := range roles {
		if r == "vision" {
			vision = true
		}
	}
	return Model{ID: cm.ID, DisplayName: cm.DisplayName, Roles: roles, Vision: vision}
}

// chatCapability follows survival-portal.md section 4.2: ready with a brain, degraded while one
// loads, unavailable (LAI-SVC-106, "No brain is running") otherwise.
func chatCapability(b Brain) Capability {
	switch {
	case len(b.Loaded) > 0:
		return Capability{State: StateReady}
	case b.Loading != nil:
		return Capability{State: StateDegraded, Reason: "loading"}
	}
	return Capability{State: StateUnavailable, Code: doctor.SVC106}
}

func (b *Minimal) alerts(lang string) []Alert {
	out := []Alert{}
	if b.src.Alerts == nil {
		return out
	}
	for _, a := range b.src.Alerts.Active() {
		a = a.In(lang)
		out = append(out, Alert{Code: a.Code, Severity: string(a.Severity), Title: a.Title, Fix: a.Fix})
	}
	return out
}
