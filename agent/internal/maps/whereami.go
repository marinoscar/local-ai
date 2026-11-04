package maps

import (
	"context"
	"encoding/json"
	"math"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/maps/gps"
	"github.com/marinoscar/local-ai/agent/internal/state"
)

// Sources of a position, the "source" field of GET /maps/whereami.
const (
	SourceGPS    = "gps"
	SourceManual = "manual"
	SourceNone   = "none"
)

// MaxLabel is the longest label of a manual location, in characters.
const MaxLabel = 80

// ManualLocation is state.json maps.manual_location (agent.md section 3.5).
type ManualLocation struct {
	Lat   float64   `json:"lat"`
	Lon   float64   `json:"lon"`
	Label string    `json:"label"`
	SetAt time.Time `json:"set_at"`
}

// ManualStore persists the manual location. The state store backs it in the daemon.
type ManualStore interface {
	Get() *ManualLocation
	Set(ManualLocation) error
	Clear() error
}

// Position is the body of GET /maps/whereami. The location never leaves the node: it is not in
// heartbeats, snapshots or cloud reports, and nothing here logs it.
type Position struct {
	Source    string   `json:"source"`
	Lat       *float64 `json:"lat"`
	Lon       *float64 `json:"lon"`
	AccuracyM *float64 `json:"accuracy_m"`
	Fix       *string  `json:"fix"`
	At        *string  `json:"at"`
	Label     *string  `json:"label"`
	// Code is doctor.SVC033 when a configured GPS has been silent for two minutes (the map screen
	// shows the hint), omitted otherwise.
	Code string `json:"code,omitempty"`
}

// Service answers "where am I" from the GPS tracker and the manual location.
type Service struct {
	Tracker *gps.Tracker
	Config  func() gps.Config
	Manual  ManualStore
	Clock   func() time.Time
}

func (s *Service) now() time.Time {
	if s.Clock != nil {
		return s.Clock()
	}
	return time.Now()
}

func (s *Service) cfg() gps.Config {
	if s.Config != nil {
		return s.Config()
	}
	return gps.Config{Mode: gps.ModeAuto}
}

// Position applies the precedence of companion-services.md section 4.4.5: a GPS fix (even a stale
// one, which the UI answers with the manual pin) before the manual location before none.
func (s *Service) Position() Position {
	var p Position
	if s.Tracker != nil {
		if f, ok := s.Tracker.Latest(); ok && s.cfg().Mode != gps.ModeOff {
			p = gpsPosition(f, s.Tracker.FixState(f))
			if s.Tracker.Silent(s.cfg()) {
				p.Code = doctor.SVC033
			}
			return p
		}
	}
	if s.Manual != nil {
		if m := s.Manual.Get(); m != nil {
			return manualPosition(*m)
		}
	}
	p.Source = SourceNone
	if s.Tracker != nil && s.Tracker.Silent(s.cfg()) {
		p.Code = doctor.SVC033
	}
	return p
}

// CatalogGPS is the "gps" object of /maps/catalog.json: available is true while a fresh GPS fix is
// held; source is the source GET /maps/whereami would report ("gps", "manual" or "none").
func (s *Service) CatalogGPS() (available bool, source string) {
	p := s.Position()
	return p.Source == SourceGPS && p.Fix != nil && *p.Fix != "stale", p.Source
}

// Set stores a manual location after validating it.
func (s *Service) Set(lat, lon float64, label string) (Position, error) {
	m := ManualLocation{Lat: lat, Lon: lon, Label: label, SetAt: s.now().UTC().Truncate(time.Second)}
	if err := s.Manual.Set(m); err != nil {
		return Position{}, err
	}
	return manualPosition(m), nil
}

// Clear removes the manual location.
func (s *Service) Clear() error { return s.Manual.Clear() }

// Check is the doctor check svc.gps: LAI-SVC-033 (info) while a configured GPS is silent. It is not
// a fast check: the code only matters on the map screen, so it never becomes an alert source.
func (s *Service) Check() doctor.Check {
	return doctor.Check{ID: "svc.gps", Area: "SVC", Timeout: 2 * time.Second, Run: func(context.Context) []doctor.Result {
		if s.Tracker != nil && s.Tracker.Silent(s.cfg()) {
			return []doctor.Result{{Code: doctor.SVC033, Severity: doctor.Info}}
		}
		return nil
	}}
}

func round(v float64, places int) float64 {
	p := math.Pow(10, float64(places))
	return math.Round(v*p) / p
}

func ptr[T any](v T) *T { return &v }

func gpsPosition(f gps.Fix, fix string) Position {
	p := Position{Source: SourceGPS, Lat: ptr(round(f.Lat, 6)), Lon: ptr(round(f.Lon, 6)), Fix: &fix,
		At: ptr(f.At.UTC().Format(time.RFC3339))}
	if f.AccuracyM > 0 {
		p.AccuracyM = ptr(f.AccuracyM)
	}
	return p
}

func manualPosition(m ManualLocation) Position {
	p := Position{Source: SourceManual, Lat: ptr(m.Lat), Lon: ptr(m.Lon), At: ptr(m.SetAt.UTC().Format(time.RFC3339))}
	if m.Label != "" {
		p.Label = ptr(m.Label)
	}
	return p
}

// StateMutator is the part of *state.Store the manual store uses.
type StateMutator interface {
	Snapshot() state.State
	MutateSync(fn func(*state.State) error) error
}

// StateStore keeps the manual location in state.json under maps.manual_location. The other keys of
// the maps sub-tree (the "_owner" marker and whatever a newer agent wrote) are preserved.
type StateStore struct{ S StateMutator }

// Get implements ManualStore.
func (m StateStore) Get() *ManualLocation {
	tree := decodeTree(m.S.Snapshot().Maps)
	raw, ok := tree["manual_location"]
	if !ok {
		return nil
	}
	var loc *ManualLocation
	if json.Unmarshal(raw, &loc) != nil || loc == nil {
		return nil
	}
	return loc
}

// Set implements ManualStore.
func (m StateStore) Set(loc ManualLocation) error {
	raw, err := json.Marshal(loc)
	if err != nil {
		return err
	}
	return m.write(raw)
}

// Clear implements ManualStore.
func (m StateStore) Clear() error { return m.write([]byte("null")) }

func (m StateStore) write(manual []byte) error {
	return m.S.MutateSync(func(st *state.State) error {
		tree := decodeTree(st.Maps)
		tree["manual_location"] = manual
		if _, ok := tree["_owner"]; !ok {
			tree["_owner"] = json.RawMessage(`"companion-services.md"`)
		}
		b, err := json.Marshal(tree)
		if err != nil {
			return err
		}
		st.Maps = b
		return nil
	})
}

func decodeTree(raw json.RawMessage) map[string]json.RawMessage {
	tree := map[string]json.RawMessage{}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &tree)
		if tree == nil {
			tree = map[string]json.RawMessage{}
		}
	}
	return tree
}
