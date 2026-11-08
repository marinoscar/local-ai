package statusapi

import (
	"encoding/json"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/catalog"
)

// The wire types of GET /status, GET and PUT /profile, GET /catalog and POST /catalog/refresh. Their
// json tags mirror the schemas of the same name in agent/api/openapi.yaml (Status, Profile, Catalog,
// CatalogRefresh); TestTypesMirrorSchemas fails when a field exists on one side only.

// Status is the body of GET /status (agent.md section 3.8.4; schema Status).
type Status struct {
	Node         NodeBlock         `json:"node"`
	Mode         ModeBlock         `json:"mode"`
	Connectivity ConnectivityBlock `json:"connectivity"`
	Hardware     HardwareBlock     `json:"hardware"`
	Services     []ServiceRow      `json:"services"`
	Models       ModelsBlock       `json:"models"`
	Ark          ArkBlock          `json:"ark"`
	Cloud        CloudBlock        `json:"cloud"`
	Tailscale    TailscaleBlock    `json:"tailscale"`
	Alerts       []AlertRow        `json:"alerts"`
}

// NodeBlock is status.node. Counters is present only with ?counters=1.
type NodeBlock struct {
	ID             string         `json:"id"`
	DisplayName    string         `json:"display_name"`
	Hostname       string         `json:"hostname"`
	Profile        *string        `json:"profile"`
	AgentVersion   string         `json:"agent_version"`
	Backend        string         `json:"backend"`
	LlamaMode      string         `json:"llama_mode"`
	LlamaBuild     *string        `json:"llama_build"`
	CatalogVersion *string        `json:"catalog_version"`
	UptimeS        int64          `json:"uptime_s"`
	Time           string         `json:"time"`
	Counters       map[string]any `json:"counters,omitempty"`
}

// ModeBlock is status.mode.
type ModeBlock struct {
	Value      string           `json:"value"`
	Since      *string          `json:"since"`
	Transition *TransitionBlock `json:"transition"`
}

// TransitionBlock is the transition in flight (schema ModeTransition).
type TransitionBlock struct {
	OpID      string         `json:"op_id"`
	From      string         `json:"from"`
	Target    string         `json:"target"`
	Step      string         `json:"step"`
	StartedAt string         `json:"started_at"`
	Warnings  []WarningBlock `json:"warnings"`
}

// WarningBlock is a doctor warning collected by a transition.
type WarningBlock struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// ConnectivityBlock is status.connectivity.
type ConnectivityBlock struct {
	Value     string  `json:"value"`
	CheckedAt *string `json:"checked_at"`
	NoneSince *string `json:"none_since"`
}

// HardwareBlock is status.hardware: the headline numbers of GET /hardware.
type HardwareBlock struct {
	GPU             *string    `json:"gpu"`
	VRAMTotalMiB    *int64     `json:"vram_total_mib"`
	VRAMUsedMiB     *int64     `json:"vram_used_mib"`
	RAMTotalMiB     int64      `json:"ram_total_mib"`
	RAMAvailableMiB int64      `json:"ram_available_mib"`
	GPUTempC        *float64   `json:"gpu_temp_c"`
	CPUTempC        *float64   `json:"cpu_temp_c"`
	Power           PowerBlock `json:"power"`
	// GPUs lists each GPU, with its compute processes. The state pushed to the cloud drops the processes.
	GPUs []GPUBlock `json:"gpus"`
}

// PowerBlock is status.hardware.power (schema Power).
type PowerBlock struct {
	Source         string  `json:"source"`
	BatteryPercent *int    `json:"battery_percent"`
	Charging       *bool   `json:"charging"`
	TimeRemainingS *int64  `json:"time_remaining_s"`
	Profile        *string `json:"profile"`
	Stale          bool    `json:"stale,omitempty"`
}

// GPUBlock is one entry of status.hardware.gpus.
type GPUBlock struct {
	Index        int          `json:"index"`
	Vendor       string       `json:"vendor"`
	Name         string       `json:"name"`
	VRAMTotalMiB int64        `json:"vram_total_mib"`
	VRAMUsedMiB  int64        `json:"vram_used_mib"`
	TempC        *float64     `json:"temp_c"`
	UtilPct      *int         `json:"util_pct"`
	Throttling   []string     `json:"throttling"`
	Processes    []GPUProcess `json:"processes,omitempty"`
}

// GPUProcess is one compute process on a GPU.
type GPUProcess struct {
	PID     int    `json:"pid"`
	Name    string `json:"name"`
	VRAMMiB int64  `json:"vram_mib"`
}

// ServiceRow is one entry of status.services. State is the health state of companion-services.md
// section 4.6 (ok, degraded, down, missing_content, disabled, starting); Unit is empty, and sent as
// null, for a service without a unit (piper, maps).
type ServiceRow struct {
	Name     string  `json:"name"`
	Unit     string  `json:"unit"`
	Active   string  `json:"active"`
	Sub      string  `json:"sub"`
	Since    *string `json:"since"`
	Restarts int     `json:"restarts"`
	Healthy  bool    `json:"healthy"`
	State    string  `json:"state,omitempty"`
}

// MarshalJSON writes an empty Unit as null; every other field is encoded as the struct tags say.
func (r ServiceRow) MarshalJSON() ([]byte, error) {
	type plain ServiceRow
	if r.Unit != "" {
		return json.Marshal(plain(r))
	}
	return json.Marshal(struct {
		plain
		Unit *string `json:"unit"`
	}{plain: plain(r)})
}

// ModelsBlock is status.models.
type ModelsBlock struct {
	Default   *string  `json:"default"`
	Loaded    []string `json:"loaded"`
	Loading   []string `json:"loading"`
	Present   int      `json:"present"`
	Loadable  int      `json:"loadable"`
	ModelsMax int      `json:"models_max"`
}

// ArkBlock is status.ark, the summary of ark-sync.md section 4.12.1.
type ArkBlock struct {
	Roots []ArkRootRow `json:"roots"`
	Drive ArkDriveRow  `json:"drive"`
	Sync  ArkSyncRow   `json:"sync"`
	// Metrics are the readiness gauges of ark-sync.md section 7; absent while no view supplies them.
	Metrics []ArkMetricRow `json:"metrics,omitempty"`
}

// ArkMetricRow is one entry of status.ark.metrics.
type ArkMetricRow struct {
	Name   string            `json:"name"`
	Labels map[string]string `json:"labels,omitempty"`
	Value  float64           `json:"value"`
}

// ArkRootRow is one content root of status.ark.
type ArkRootRow struct {
	Path      string  `json:"path"`
	Kind      string  `json:"kind"`
	Status    string  `json:"status"`
	ReadyPct  *int    `json:"ready_pct,omitempty"`
	UpdatedAt *string `json:"updated_at"`
}

// ArkDriveRow is status.ark.drive.
type ArkDriveRow struct {
	Mounted bool `json:"mounted"`
}

// ArkSyncRow is status.ark.sync.
type ArkSyncRow struct {
	State         string  `json:"state"`
	LastSuccessAt *string `json:"last_success_at"`
}

// CloudBlock is status.cloud.
type CloudBlock struct {
	Enrolled        bool    `json:"enrolled"`
	State           string  `json:"state"`
	URL             *string `json:"url"`
	LastHeartbeatAt *string `json:"last_heartbeat_at"`
}

// TailscaleBlock is status.tailscale.
type TailscaleBlock struct {
	Manage       bool    `json:"manage"`
	Pattern      string  `json:"pattern"`
	Applied      bool    `json:"applied"`
	BackendState *string `json:"backend_state"`
}

// AlertRow is one active alert (schema Alert). Details and Action are dropped from the state pushed
// to the cloud: Action is a button of the local UIs (doctor-codes.md section 1.7).
type AlertRow struct {
	Code     string         `json:"code"`
	Severity string         `json:"severity"`
	Title    string         `json:"title"`
	Fix      string         `json:"fix"`
	Action   string         `json:"action,omitempty"`
	Since    *string        `json:"since,omitempty"`
	Details  map[string]any `json:"details,omitempty"`
}

// ProfileBody is the body of GET /profile and of the PUT /profile answer (agent.md section 3.8.30;
// schema Profile). Warnings appears only on a PUT whose router reload had a problem.
type ProfileBody struct {
	Profile      string            `json:"profile"`
	DisplayName  string            `json:"display_name"`
	Overrides    catalog.Overrides `json:"overrides"`
	Suggested    *string           `json:"suggested"`
	MatchReasons []string          `json:"match_reasons"`
	Effective    catalog.Plan      `json:"effective"`
	Warnings     []WarningBlock    `json:"warnings,omitempty"`
}

// CatalogBody is the body of GET /catalog (agent.md section 3.8.21; schema Catalog).
type CatalogBody struct {
	Version   string       `json:"version"`
	Source    string       `json:"source"`
	FetchedAt *string      `json:"fetched_at"`
	Models    []ModelRow   `json:"models"`
	Content   []ContentRow `json:"content"`
	// Profiles lists every profile of the installed catalog and the ids it includes (aictl profile set).
	Profiles []ProfileRow `json:"profiles"`
}

// ProfileRow is one entry of GET /catalog "profiles".
type ProfileRow struct {
	ID          string         `json:"id"`
	DisplayName string         `json:"display_name"`
	Include     ProfileInclude `json:"include"`
	BudgetBytes int64          `json:"budget_bytes"`
}

// ProfileInclude is what a profile keeps.
type ProfileInclude struct {
	Models  []string `json:"models"`
	Content []string `json:"content"`
}

// ContentRow is one entry of GET /catalog "content".
type ContentRow struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	DisplayName string `json:"display_name"`
	Present     bool   `json:"present"`
	Verified    bool   `json:"verified"`
	InProfile   bool   `json:"in_profile"`
	SizeBytes   int64  `json:"size_bytes"`
	Status      string `json:"status"`
}

// CatalogRefreshBody is the answer of POST /catalog/refresh (schema CatalogRefresh).
type CatalogRefreshBody struct {
	Version  string         `json:"version"`
	Previous *string        `json:"previous"`
	Changed  bool           `json:"changed"`
	Source   string         `json:"source"`
	Warnings []WarningBlock `json:"warnings,omitempty"`
}

// ModelRow is a catalog model merged with local state: the shape of GET /models (agent.md section
// 3.8.7, schema Model) without the fields only GET /models/{id} carries (files, preset).
type ModelRow struct {
	ID             string           `json:"id"`
	DisplayName    string           `json:"display_name"`
	Summary        string           `json:"summary,omitempty"`
	Engine         string           `json:"engine"`
	Roles          []string         `json:"roles"`
	SurvivalTier   int              `json:"survival_tier,omitempty"`
	InProfile      bool             `json:"in_profile"`
	SizeBytes      int64            `json:"size_bytes"`
	Present        bool             `json:"present"`
	Verified       bool             `json:"verified"`
	Locations      []ModelLocation  `json:"locations"`
	Loadable       bool             `json:"loadable"`
	LoadableReason *ReasonBody      `json:"loadable_reason"`
	Status         string           `json:"status"`
	Loaded         bool             `json:"loaded"`
	Default        bool             `json:"default"`
	Aliases        []string         `json:"aliases"`
	Requirements   ModelRequirement `json:"requirements"`
	LastLoadMS     *int64           `json:"last_load_ms,omitempty"`
}

// ModelLocation is a content root that holds a complete verified copy of a model.
type ModelLocation struct {
	Root       string  `json:"root"`
	Complete   bool    `json:"complete"`
	VerifiedAt *string `json:"verified_at,omitempty"`
}

// ReasonBody is a doctor code with its plain-language message (loadable_reason).
type ReasonBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// ModelRequirement is the requirements object of a model row.
type ModelRequirement struct {
	Backends   []string `json:"backends"`
	MinVRAMMiB int64    `json:"min_vram_mib"`
	MinRAMMiB  int64    `json:"min_ram_mib"`
}

// rfc3339 renders t as RFC 3339 UTC with a Z (the wire form of every time).
func rfc3339(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// timePtr is rfc3339 for an optional time: the zero time is JSON null.
func timePtr(t time.Time) *string {
	if t.IsZero() {
		return nil
	}
	s := rfc3339(t)
	return &s
}

// strPtr returns nil for "" (JSON null).
func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
