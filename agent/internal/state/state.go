// Package state is the agent's runtime state store: /var/lib/localai/state.json (agent.md section
// 3.5). One goroutine writes the file, after every change, atomically, keeping the previous good
// version as state.json.bak; everyone else reads deep copies. A damaged file falls back to the
// backup and then to "mode: offline" (nothing exposed, nothing pulled). Survival resume after a power
// cut depends on this package, so it is doomsday-critical and uses only the standard library.
package state

import (
	"encoding/json"
	"time"
)

// SchemaVersion is the state.json schema this agent writes. A larger value in a file means a newer
// agent wrote it (loaded best-effort, rewritten with the same number so its fields stay valid).
const SchemaVersion = 1

// Mode is the node's operating mode (docs/architecture/modes.md).
type Mode string

// The three modes.
const (
	ModeOnline   Mode = "online"
	ModeOffline  Mode = "offline"
	ModeSurvival Mode = "survival"
)

// Valid reports whether m is one of the three modes.
func (m Mode) Valid() bool { return m == ModeOnline || m == ModeOffline || m == ModeSurvival }

// Enum values stored as plain strings (a newer agent may add values; the store never rejects them).
const (
	LoadOK          = "ok"
	LoadInProgress  = "in_progress"
	LoadFailed      = "failed"
	LoadTimeout     = "timeout"
	LoadInterrupted = "interrupted"

	UpdateOK         = "ok"
	UpdateRolledBack = "rolled_back"
	UpdateFailed     = "failed"

	ConnFull       = "full"
	ConnInternet   = "internet"
	ConnTailnet    = "tailnet"
	ConnNone       = "none"
	ConnNotChecked = "not_checked"
)

// State is the typed content of state.json. Keys the agent does not know are kept in Extra (at the
// top level and inside every typed object) and written back unchanged; the opaque sub-trees owned by
// other specs (Sync, Drill, Maps) are stored as raw JSON.
//
// A field of the wrong JSON type is a parse error (the file counts as corrupt). Newer agents may add
// fields but must not change the type of an existing one.
type State struct {
	SchemaVersion int        `json:"schema_version"`
	WrittenAt     time.Time  `json:"written_at"`
	AgentVersion  string     `json:"agent_version"`
	Mode          Mode       `json:"mode"`
	ModeChangedAt *time.Time `json:"mode_changed_at"`
	// ModeTransition is non-nil while a mode change runs; resume logic is AGENT-12's.
	ModeTransition *ModeTransition `json:"mode_transition"`
	// ModeHistory is the bounded list of committed mode changes, oldest first; absent until the first
	// change (modehistory.go).
	ModeHistory []ModeHistoryEntry `json:"mode_history,omitempty"`
	// PendingMode is "survival" while a POST /mode/survival received before startup step 12 waits.
	PendingMode  *Mode        `json:"pending_mode"`
	Agent        AgentInfo    `json:"agent"`
	Models       Models       `json:"models"`
	Llama        Llama        `json:"llama"`
	Key          Key          `json:"key"`
	Tailscale    Tailscale    `json:"tailscale"`
	Connectivity Connectivity `json:"connectivity"`
	// Migration maps a migration name (for example "llama_qwen") to its progress.
	Migration map[string]MigrationEntry `json:"migration"`
	Update    UpdateInfo                `json:"update"`

	// Owned by other specs, stored opaquely (each carries its own "_owner" marker).
	Sync  json.RawMessage `json:"sync,omitempty"`  // ark-sync.md
	Drill json.RawMessage `json:"drill,omitempty"` // drill.md
	Maps  json.RawMessage `json:"maps,omitempty"`  // companion-services.md

	Extra map[string]json.RawMessage `json:"-"`
}

// AgentInfo holds agent-level bookkeeping.
type AgentInfo struct {
	// RecentStarts is the start time of the last 10 agent starts, newest last (LAI-AGT-014).
	RecentStarts []time.Time                `json:"recent_starts"`
	Extra        map[string]json.RawMessage `json:"-"`
}

// Models is the model state.
type Models struct {
	Default       *string                    `json:"default"`
	DesiredLoaded []string                   `json:"desired_loaded"`
	LastUsed      map[string]time.Time       `json:"last_used"`
	LastLoad      *LastLoad                  `json:"last_load"`
	Extra         map[string]json.RawMessage `json:"-"`
}

// LastLoad is the most recent model load attempt.
type LastLoad struct {
	ID     string                     `json:"id"`
	OpID   string                     `json:"op_id"`
	Result string                     `json:"result"` // Load* values
	LoadMS int64                      `json:"load_ms"`
	At     time.Time                  `json:"at"`
	Extra  map[string]json.RawMessage `json:"-"`
}

// Llama records what the router reconcile last generated.
type Llama struct {
	Build         string                     `json:"build"`
	PreviousBuild *string                    `json:"previous_build"`
	IniSHA256     string                     `json:"ini_sha256"`
	UnitSHA256    string                     `json:"unit_sha256"`
	Extra         map[string]json.RawMessage `json:"-"`
}

// Key is the llama API key bookkeeping (never the key itself).
type Key struct {
	Fingerprint string                     `json:"fingerprint"`
	RotatedAt   *time.Time                 `json:"rotated_at"`
	CloudSynced bool                       `json:"cloud_synced"`
	Extra       map[string]json.RawMessage `json:"-"`
}

// Tailscale records the applied serve pattern.
type Tailscale struct {
	AppliedPattern string                     `json:"applied_pattern"`
	AppliedAt      *time.Time                 `json:"applied_at"`
	Extra          map[string]json.RawMessage `json:"-"`
}

// Connectivity is the prober's last verdict.
type Connectivity struct {
	Value     string                     `json:"value"` // Conn* values
	Since     *time.Time                 `json:"since"`
	NoneSince *time.Time                 `json:"none_since"`
	Extra     map[string]json.RawMessage `json:"-"`
}

// MigrationEntry is the progress of one migration.
type MigrationEntry struct {
	State string                     `json:"state"`
	Step  string                     `json:"step"`
	At    time.Time                  `json:"at"`
	Extra map[string]json.RawMessage `json:"-"`
}

// UpdateInfo holds the self-update outcome (an update in progress is update/pending.json, not here).
type UpdateInfo struct {
	Last  *UpdateLast                `json:"last"`
	Extra map[string]json.RawMessage `json:"-"`
}

// UpdateLast is the outcome of the most recent self-update.
type UpdateLast struct {
	Result string                     `json:"result"` // Update* values
	From   string                     `json:"from"`
	To     string                     `json:"to"`
	At     time.Time                  `json:"at"`
	Extra  map[string]json.RawMessage `json:"-"`
}

// ModeTransition is the in-progress mode change, recorded before every step so it can resume after a
// crash (agent.md section 4.2.2).
type ModeTransition struct {
	OpID      string                     `json:"op_id"`
	From      Mode                       `json:"from"`
	Target    Mode                       `json:"target"`
	Step      string                     `json:"step"`
	StartedAt time.Time                  `json:"started_at"`
	Attempts  map[string]int             `json:"attempts"`
	Warnings  []TransitionWarning        `json:"warnings"`
	Actor     Actor                      `json:"actor"`
	Extra     map[string]json.RawMessage `json:"-"`
}

// TransitionWarning is a doctor warning collected while a transition ran.
type TransitionWarning struct {
	Code    string                     `json:"code"`
	Message string                     `json:"message"`
	Extra   map[string]json.RawMessage `json:"-"`
}

// Actor says who asked for a transition.
type Actor struct {
	Kind  string                     `json:"kind"`
	Via   string                     `json:"via"`
	Extra map[string]json.RawMessage `json:"-"`
}

// newState returns the state of a fresh node (agent.md section 3.5 load rule 3): online, no default
// model, everything else empty.
func newState() State {
	s := State{SchemaVersion: SchemaVersion, Mode: ModeOnline}
	s.normalize()
	return s
}

// normalize replaces nil maps and slices of known fields with empty ones, so callers can write into
// them without a nil check and the file shows [] and {} instead of null.
func (s *State) normalize() {
	if s.Agent.RecentStarts == nil {
		s.Agent.RecentStarts = []time.Time{}
	}
	if s.Models.DesiredLoaded == nil {
		s.Models.DesiredLoaded = []string{}
	}
	if s.Models.LastUsed == nil {
		s.Models.LastUsed = map[string]time.Time{}
	}
	if s.Migration == nil {
		s.Migration = map[string]MigrationEntry{}
	}
	if t := s.ModeTransition; t != nil {
		if t.Attempts == nil {
			t.Attempts = map[string]int{}
		}
		if t.Warnings == nil {
			t.Warnings = []TransitionWarning{}
		}
	}
}

// Clone returns a deep copy that shares no map, slice or pointer with s.
func (s State) Clone() State {
	return deepCopy(s)
}

// The JSON methods below keep unknown keys: decoding stores every key the struct does not know in
// Extra, encoding appends Extra after the known fields. See extra.go.

// UnmarshalJSON implements json.Unmarshaler.
func (s *State) UnmarshalJSON(b []byte) error {
	type plain State
	return decodeKeepExtra(b, (*plain)(s), &s.Extra)
}

// MarshalJSON implements json.Marshaler.
func (s State) MarshalJSON() ([]byte, error) {
	type plain State
	return encodeKeepExtra(plain(s), s.Extra)
}

// UnmarshalJSON implements json.Unmarshaler.
func (v *AgentInfo) UnmarshalJSON(b []byte) error {
	type plain AgentInfo
	return decodeKeepExtra(b, (*plain)(v), &v.Extra)
}

// MarshalJSON implements json.Marshaler.
func (v AgentInfo) MarshalJSON() ([]byte, error) {
	type plain AgentInfo
	return encodeKeepExtra(plain(v), v.Extra)
}

// UnmarshalJSON implements json.Unmarshaler.
func (v *Models) UnmarshalJSON(b []byte) error {
	type plain Models
	return decodeKeepExtra(b, (*plain)(v), &v.Extra)
}

// MarshalJSON implements json.Marshaler.
func (v Models) MarshalJSON() ([]byte, error) {
	type plain Models
	return encodeKeepExtra(plain(v), v.Extra)
}

// UnmarshalJSON implements json.Unmarshaler.
func (v *LastLoad) UnmarshalJSON(b []byte) error {
	type plain LastLoad
	return decodeKeepExtra(b, (*plain)(v), &v.Extra)
}

// MarshalJSON implements json.Marshaler.
func (v LastLoad) MarshalJSON() ([]byte, error) {
	type plain LastLoad
	return encodeKeepExtra(plain(v), v.Extra)
}

// UnmarshalJSON implements json.Unmarshaler.
func (v *Llama) UnmarshalJSON(b []byte) error {
	type plain Llama
	return decodeKeepExtra(b, (*plain)(v), &v.Extra)
}

// MarshalJSON implements json.Marshaler.
func (v Llama) MarshalJSON() ([]byte, error) {
	type plain Llama
	return encodeKeepExtra(plain(v), v.Extra)
}

// UnmarshalJSON implements json.Unmarshaler.
func (v *Key) UnmarshalJSON(b []byte) error {
	type plain Key
	return decodeKeepExtra(b, (*plain)(v), &v.Extra)
}

// MarshalJSON implements json.Marshaler.
func (v Key) MarshalJSON() ([]byte, error) {
	type plain Key
	return encodeKeepExtra(plain(v), v.Extra)
}

// UnmarshalJSON implements json.Unmarshaler.
func (v *Tailscale) UnmarshalJSON(b []byte) error {
	type plain Tailscale
	return decodeKeepExtra(b, (*plain)(v), &v.Extra)
}

// MarshalJSON implements json.Marshaler.
func (v Tailscale) MarshalJSON() ([]byte, error) {
	type plain Tailscale
	return encodeKeepExtra(plain(v), v.Extra)
}

// UnmarshalJSON implements json.Unmarshaler.
func (v *Connectivity) UnmarshalJSON(b []byte) error {
	type plain Connectivity
	return decodeKeepExtra(b, (*plain)(v), &v.Extra)
}

// MarshalJSON implements json.Marshaler.
func (v Connectivity) MarshalJSON() ([]byte, error) {
	type plain Connectivity
	return encodeKeepExtra(plain(v), v.Extra)
}

// UnmarshalJSON implements json.Unmarshaler.
func (v *MigrationEntry) UnmarshalJSON(b []byte) error {
	type plain MigrationEntry
	return decodeKeepExtra(b, (*plain)(v), &v.Extra)
}

// MarshalJSON implements json.Marshaler.
func (v MigrationEntry) MarshalJSON() ([]byte, error) {
	type plain MigrationEntry
	return encodeKeepExtra(plain(v), v.Extra)
}

// UnmarshalJSON implements json.Unmarshaler.
func (v *UpdateInfo) UnmarshalJSON(b []byte) error {
	type plain UpdateInfo
	return decodeKeepExtra(b, (*plain)(v), &v.Extra)
}

// MarshalJSON implements json.Marshaler.
func (v UpdateInfo) MarshalJSON() ([]byte, error) {
	type plain UpdateInfo
	return encodeKeepExtra(plain(v), v.Extra)
}

// UnmarshalJSON implements json.Unmarshaler.
func (v *UpdateLast) UnmarshalJSON(b []byte) error {
	type plain UpdateLast
	return decodeKeepExtra(b, (*plain)(v), &v.Extra)
}

// MarshalJSON implements json.Marshaler.
func (v UpdateLast) MarshalJSON() ([]byte, error) {
	type plain UpdateLast
	return encodeKeepExtra(plain(v), v.Extra)
}

// UnmarshalJSON implements json.Unmarshaler.
func (v *ModeTransition) UnmarshalJSON(b []byte) error {
	type plain ModeTransition
	return decodeKeepExtra(b, (*plain)(v), &v.Extra)
}

// MarshalJSON implements json.Marshaler.
func (v ModeTransition) MarshalJSON() ([]byte, error) {
	type plain ModeTransition
	return encodeKeepExtra(plain(v), v.Extra)
}

// UnmarshalJSON implements json.Unmarshaler.
func (v *TransitionWarning) UnmarshalJSON(b []byte) error {
	type plain TransitionWarning
	return decodeKeepExtra(b, (*plain)(v), &v.Extra)
}

// MarshalJSON implements json.Marshaler.
func (v TransitionWarning) MarshalJSON() ([]byte, error) {
	type plain TransitionWarning
	return encodeKeepExtra(plain(v), v.Extra)
}

// UnmarshalJSON implements json.Unmarshaler.
func (v *Actor) UnmarshalJSON(b []byte) error {
	type plain Actor
	return decodeKeepExtra(b, (*plain)(v), &v.Extra)
}

// MarshalJSON implements json.Marshaler.
func (v Actor) MarshalJSON() ([]byte, error) {
	type plain Actor
	return encodeKeepExtra(plain(v), v.Extra)
}
