package cloud

// The strict Go mirror of LocalaiStateSnapshotV1 (cloud-app.md section 3.6, the authoritative Zod
// schema; apps/cloud/apps/api/src/localai/contracts/state.schema.ts). Every object of the schema is
// .strict(), so a field added here and not there (or the other way round) makes the cloud answer
// 400: TestSnapshotMatchesSchema marshals a full snapshot and validates it against
// testdata/state.v1.schema.json, which is that Zod schema written out as JSON Schema.
//
// Nullable schema fields are pointers (JSON null), optional ones are omitempty, everything else is
// always present.

// StateSnapshot is LocalaiStateSnapshotV1.
type StateSnapshot struct {
	SchemaVersion  int             `json:"schema_version"`
	NodeID         string          `json:"node_id"`
	BootID         string          `json:"boot_id"`
	Seq            int64           `json:"seq"`
	ReportedAt     string          `json:"reported_at"`
	AgentVersion   string          `json:"agent_version"`
	Mode           string          `json:"mode"`
	ModeTransition *SnapTransition `json:"mode_transition"`
	Connectivity   string          `json:"connectivity"`
	Profile        string          `json:"profile"`
	CatalogVersion *string         `json:"catalog_version"`
	Llama          SnapLlama       `json:"llama"`
	Hardware       SnapHardware    `json:"hardware"`
	Models         []SnapModel     `json:"models"`
	Services       []SnapService   `json:"services"`
	Ark            SnapArk         `json:"ark"`
	Sync           SnapSync        `json:"sync"`
	Drill          *SnapDrill      `json:"drill"`
	Tailscale      SnapTailscale   `json:"tailscale"`
	Alerts         []SnapAlert     `json:"alerts"`
	// Ensure is "Install what's missing" (ADR-0010): optional on the wire, sent only to a cloud whose
	// handshake lists FeatureEnsure (an older cloud's strict schema would refuse the key).
	Ensure *SnapEnsure `json:"ensure,omitempty"`
}

// SnapEnsure is ensure: a compact picture of what the node still needs from its kit and whether the agent
// is installing it. Nothing in it is a secret or a path: the state, the setting, one sentence, the
// counts, what the person should do next and the status of each part.
type SnapEnsure struct {
	// State is complete, installing, needs_you or off.
	State string `json:"state"`
	// Auto is ensure.auto: on, ask or off.
	Auto string `json:"auto"`
	// Message is the agent's own sentence in English; the cloud writes its own text in the viewer's language.
	Message    string           `json:"message"`
	Progress   SnapProgress     `json:"progress"`
	NextAction SnapEnsureNext   `json:"next_action"`
	Steps      []SnapEnsureStep `json:"steps"`
}

// SnapProgress is ensure.progress.
type SnapProgress struct {
	Done  int `json:"done"`
	Total int `json:"total"`
}

// SnapEnsureNext is ensure.next_action: none, wait, press_button, confirm, sync or fix, and the step it is about.
type SnapEnsureNext struct {
	Kind string  `json:"kind"`
	Step *string `json:"step"`
}

// SnapEnsureStep is one entry of ensure.steps.
type SnapEnsureStep struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

// SnapTransition is mode_transition.
type SnapTransition struct {
	Target    string `json:"target"`
	Step      string `json:"step"`
	StartedAt string `json:"started_at"`
}

// SnapLlama is llama.
type SnapLlama struct {
	Build     string `json:"build"`
	Backend   string `json:"backend"`
	Router    string `json:"router"`
	ModelsMax int    `json:"models_max"`
}

// SnapHardware is hardware.
type SnapHardware struct {
	GPUs        []SnapGPU `json:"gpus"`
	RAMTotalMiB int64     `json:"ram_total_mib"`
	RAMUsedMiB  int64     `json:"ram_used_mib"`
	CPUTempC    *float64  `json:"cpu_temp_c"`
	Power       SnapPower `json:"power"`
}

// SnapGPU is one entry of hardware.gpus.
type SnapGPU struct {
	Name         string   `json:"name"`
	VRAMTotalMiB int64    `json:"vram_total_mib"`
	VRAMUsedMiB  int64    `json:"vram_used_mib"`
	TempC        *float64 `json:"temp_c"`
}

// SnapPower is hardware.power.
type SnapPower struct {
	Source         string   `json:"source"`
	BatteryPercent *float64 `json:"battery_percent"`
}

// SnapModel is one entry of models.
type SnapModel struct {
	ID              string   `json:"id"`
	Present         bool     `json:"present"`
	Verified        bool     `json:"verified"`
	Loadable        bool     `json:"loadable"`
	LoadableReason  *string  `json:"loadable_reason"`
	Status          string   `json:"status"`
	Default         bool     `json:"default"`
	Slots           int      `json:"slots"`
	Ctx             *int     `json:"ctx"`
	Roles           []string `json:"roles"`
	InputModalities []string `json:"input_modalities"`
	Roots           []string `json:"roots"`

	// inProfile is not part of the wire form: the size trimming keeps the profile's models.
	inProfile bool
}

// SnapService is one entry of services.
type SnapService struct {
	Name        string  `json:"name"`
	ActiveState string  `json:"active_state"`
	Since       *string `json:"since"`
}

// SnapArk is ark.
type SnapArk struct {
	Roots             []SnapRoot `json:"roots"`
	DriveLastSyncedAt *string    `json:"drive_last_synced_at"`
}

// SnapRoot is one entry of ark.roots.
type SnapRoot struct {
	ID                 string       `json:"id"`
	Present            bool         `json:"present"`
	ReadyGeneratedAt   *string      `json:"ready_generated_at"`
	Tiers              SnapTiers    `json:"tiers"`
	ItemsMissing       int          `json:"items_missing"`
	ItemsFailed        int          `json:"items_failed"`
	ItemsSkippedBudget int          `json:"items_skipped_budget"`
	BytesUsed          int64        `json:"bytes_used"`
	BytesFree          int64        `json:"bytes_free"`
	LastSyncAt         *string      `json:"last_sync_at"`
	LastVerifyAt       *string      `json:"last_verify_at"`
	Overall            string       `json:"overall"`
	SurvivalCapable    bool         `json:"survival_capable"`
	Ready              *ReadyReport `json:"ready"`
}

// SnapTiers is ark.roots[].tiers.
type SnapTiers struct {
	T1 string `json:"t1"`
	T2 string `json:"t2"`
	T3 string `json:"t3"`
}

// SnapSync is sync.
type SnapSync struct {
	Running     bool         `json:"running"`
	RunID       *string      `json:"run_id"`
	ProgressPct *float64     `json:"progress_pct"`
	LastRun     *SnapSyncRun `json:"last_run"`
}

// SnapSyncRun is sync.last_run.
type SnapSyncRun struct {
	ID         string  `json:"id"`
	FinishedAt string  `json:"finished_at"`
	Result     string  `json:"result"`
	SkipReason *string `json:"skip_reason"`
	ErrorCode  *string `json:"error_code"`
}

// SnapDrill is drill.
type SnapDrill struct {
	RunID          string        `json:"run_id"`
	LastRunAt      string        `json:"last_run_at"`
	Result         string        `json:"result"`
	SurvivalReady  bool          `json:"survival_ready"`
	FailedChecks   []string      `json:"failed_checks"`
	FailedCheckIDs []string      `json:"failed_check_ids"`
	Results        *DrillResults `json:"results"`
}

// SnapTailscale is tailscale.
type SnapTailscale struct {
	ServePattern string  `json:"serve_pattern"`
	ServeActive  bool    `json:"serve_active"`
	BackendState *string `json:"backend_state"`
}

// SnapAlert is one entry of alerts.
type SnapAlert struct {
	Code     string `json:"code"`
	Severity string `json:"severity"`
	Title    string `json:"title"`
}

// ReadyReport is ReadyReportV1: the full READY.json of a root without root.path and
// generated_by.node_id (decoding READY.json into this type drops them, and every field the schema
// does not know).
type ReadyReport struct {
	SchemaVersion      int              `json:"schema_version"`
	GeneratedAt        string           `json:"generated_at"`
	GeneratedBy        ReadyGeneratedBy `json:"generated_by"`
	Root               ReadyRoot        `json:"root"`
	Overall            string           `json:"overall"`
	SurvivalCapable    bool             `json:"survival_capable"`
	LastSync           *ReadyLastSync   `json:"last_sync"`
	LastFullReverifyAt *string          `json:"last_full_reverify_at"`
	Counts             map[string]int   `json:"counts"`
	Items              []ReadyItem      `json:"items"`
	Problems           []ReadyProblem   `json:"problems"`
	ClockWrong         *bool            `json:"clock_wrong,omitempty"`
}

// ReadyGeneratedBy is generated_by.
type ReadyGeneratedBy struct {
	AgentVersion string `json:"agent_version"`
}

// ReadyRoot is root.
type ReadyRoot struct {
	ID             string  `json:"id"`
	Kind           string  `json:"kind"`
	Profile        string  `json:"profile"`
	CatalogVersion *string `json:"catalog_version"`
	FSTotalBytes   int64   `json:"fs_total_bytes"`
	FSFreeBytes    int64   `json:"fs_free_bytes"`
	BudgetBytes    int64   `json:"budget_bytes"`
	UsedBytes      int64   `json:"used_bytes"`
	TrashBytes     int64   `json:"trash_bytes"`
	ReadOnly       bool    `json:"read_only"`
}

// ReadyLastSync is last_sync.
type ReadyLastSync struct {
	RunID      string `json:"run_id"`
	Trigger    string `json:"trigger"`
	FinishedAt string `json:"finished_at"`
	State      string `json:"state"`
	AgeDays    int    `json:"age_days"`
}

// ReadyItem is one entry of items.
type ReadyItem struct {
	ID               string `json:"id"`
	Kind             string `json:"kind"`
	Tier             int    `json:"tier"`
	Survival         *bool  `json:"survival,omitempty"`
	Status           string `json:"status"`
	Version          string `json:"version,omitempty"`
	VerifiedAt       string `json:"verified_at,omitempty"`
	LastReverifiedAt string `json:"last_reverified_at,omitempty"`
	CheckedCurrentAt string `json:"checked_current_at,omitempty"`
	AgeDays          *int   `json:"age_days,omitempty"`
	Freshness        string `json:"freshness,omitempty"`
	Bytes            *int64 `json:"bytes,omitempty"`
	Trust            string `json:"trust,omitempty"`
	Reason           string `json:"reason,omitempty"`
	Code             string `json:"code,omitempty"`
}

// ReadyProblem is one entry of problems.
type ReadyProblem struct {
	Code     string `json:"code"`
	Severity string `json:"severity"`
	Item     string `json:"item,omitempty"`
	Title    string `json:"title"`
}

// DrillResults is DrillResultsV1: the drill results document without node_id.
type DrillResults struct {
	SchemaVersion  int          `json:"schema_version"`
	RunID          string       `json:"run_id"`
	Trigger        string       `json:"trigger"`
	AgentVersion   string       `json:"agent_version"`
	CatalogVersion *string      `json:"catalog_version"`
	Mode           string       `json:"mode"`
	Quick          bool         `json:"quick"`
	StartedAt      string       `json:"started_at"`
	FinishedAt     string       `json:"finished_at"`
	Overall        string       `json:"overall"`
	SurvivalReady  bool         `json:"survival_ready"`
	Counts         DrillCounts  `json:"counts"`
	Checks         []DrillCheck `json:"checks"`
	Restored       DrillRestore `json:"restored"`
	NetworkFence   DrillFence   `json:"network_fence"`
}

// DrillCounts is counts.
type DrillCounts struct {
	Pass int `json:"pass"`
	Warn int `json:"warn"`
	Fail int `json:"fail"`
	Skip int `json:"skip"`
}

// DrillCheck is one entry of checks. Details values are strings, numbers, booleans or null.
type DrillCheck struct {
	ID      string         `json:"id"`
	Area    string         `json:"area"`
	Result  string         `json:"result"`
	Code    string         `json:"code,omitempty"`
	MS      int64          `json:"ms"`
	Details map[string]any `json:"details,omitempty"`
}

// DrillRestore is restored.
type DrillRestore struct {
	LoadedModels []string `json:"loaded_models"`
	DefaultModel *string  `json:"default_model"`
	OK           bool     `json:"ok"`
}

// DrillFence is network_fence.
type DrillFence struct {
	Enabled      bool `json:"enabled"`
	BlockedDials int  `json:"blocked_dials"`
}

// ReducedState is the reduced snapshot of the capabilities fallback (agent.md section 4.8.4, at most
// 32 KiB): mode, models loaded and default, service states, ark tiers and alert codes. It rides in
// the heartbeat's capabilities.localai.state while the cloud has no state endpoint.
type ReducedState struct {
	SchemaVersion int              `json:"schema_version"`
	BootID        string           `json:"boot_id"`
	Seq           int64            `json:"seq"`
	ReportedAt    string           `json:"reported_at"`
	Mode          string           `json:"mode"`
	DefaultModel  *string          `json:"default_model"`
	LoadedModels  []string         `json:"loaded_models"`
	Services      []ReducedService `json:"services"`
	Ark           []ReducedRoot    `json:"ark"`
	Alerts        []string         `json:"alerts"`
}

// ReducedService is a service state of the reduced snapshot.
type ReducedService struct {
	Name        string `json:"name"`
	ActiveState string `json:"active_state"`
}

// ReducedRoot is the tiers of one root of the reduced snapshot.
type ReducedRoot struct {
	ID      string `json:"id"`
	T1      string `json:"t1"`
	T2      string `json:"t2"`
	T3      string `json:"t3"`
	Overall string `json:"overall"`
}
