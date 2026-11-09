package cli

// Minimal decode structs for the agent bodies the read-only commands render (agent.md section 3.8,
// ark-sync.md section 3.5). They carry only the fields a renderer reads; unknown fields are ignored
// and a missing section stays at its zero value, so a renderer can degrade line by line. Times are
// kept as strings and parsed leniently (roWhen): one odd timestamp must not blank a whole command.
//
// The contract is agent/api/openapi.yaml: internal/api carries only the Health and error-envelope
// types, so these are aictl's own readers of the answers, and readonly_contract_test.go checks that
// the fixtures the golden tests render are valid answers of that document.

// wireStatus is GET /status.
type wireStatus struct {
	Node struct {
		ID           string `json:"id"`
		DisplayName  string `json:"display_name"`
		Profile      string `json:"profile"`
		AgentVersion string `json:"agent_version"`
		Backend      string `json:"backend"`
		LlamaBuild   string `json:"llama_build"`
		CatalogVer   string `json:"catalog_version"`
	} `json:"node"`
	Mode struct {
		Value      string          `json:"value"`
		Since      string          `json:"since"`
		Transition *wireTransition `json:"transition"`
	} `json:"mode"`
	Connectivity struct {
		Value string `json:"value"`
	} `json:"connectivity"`
	Hardware struct {
		GPU          string     `json:"gpu"`
		VRAMTotalMiB int64      `json:"vram_total_mib"`
		VRAMUsedMiB  int64      `json:"vram_used_mib"`
		RAMTotalMiB  int64      `json:"ram_total_mib"`
		RAMAvailMiB  int64      `json:"ram_available_mib"`
		GPUTempC     *float64   `json:"gpu_temp_c"`
		Power        *wirePower `json:"power"`
	} `json:"hardware"`
	Services  []wireService        `json:"services"`
	Models    *wireStatusModels    `json:"models"`
	Ark       *wireStatusArk       `json:"ark"`
	Cloud     *wireCloud           `json:"cloud"`
	Tailscale *wireStatusTailscale `json:"tailscale"`
	Alerts    []wireAlert          `json:"alerts"`
}

type wireStatusModels struct {
	Default string   `json:"default"`
	Loaded  []string `json:"loaded"`
	Loading []string `json:"loading"`
	Present int      `json:"present"`
}

type wireTransition struct {
	From   string `json:"from"`
	Target string `json:"target"`
	Step   string `json:"step"`
}

type wirePower struct {
	Source         string   `json:"source"`
	BatteryPercent *float64 `json:"battery_percent"`
	Charging       bool     `json:"charging"`
}

// wireService is one entry of /status services and of GET /services (which adds Enabled and
// LastExit).
type wireService struct {
	Name     string `json:"name"`
	Unit     string `json:"unit"`
	Active   string `json:"active"`
	Sub      string `json:"sub"`
	Since    string `json:"since"`
	Restarts int    `json:"restarts"`
	Healthy  bool   `json:"healthy"`
	State    string `json:"state"`
	Enabled  *bool  `json:"enabled"`
	// Device is where a companion runs its work, "cpu" or "gpu" (whisper, companion-services.md section 4.6).
	Device   string `json:"device"`
	LastExit *struct {
		Code *int   `json:"code"`
		At   string `json:"at"`
	} `json:"last_exit"`
}

type wireStatusArk struct {
	Roots []struct {
		Path      string   `json:"path"`
		Kind      string   `json:"kind"`
		Status    string   `json:"status"`
		ReadyPct  *float64 `json:"ready_pct"`
		UpdatedAt string   `json:"updated_at"`
	} `json:"roots"`
	Drive struct {
		Mounted bool `json:"mounted"`
	} `json:"drive"`
	Sync struct {
		State         string `json:"state"`
		LastSuccessAt string `json:"last_success_at"`
	} `json:"sync"`
}

// wireCloud is the cloud block of /status and the body of GET /cloud (a superset).
type wireCloud struct {
	Enrolled        bool   `json:"enrolled"`
	URL             string `json:"url"`
	State           string `json:"state"`
	Name            string `json:"name"`
	Since           string `json:"since"`
	LastHeartbeatAt string `json:"last_heartbeat_at"`
	LastClaimAt     string `json:"last_claim_at"`
	LastError       any    `json:"last_error"`
	Credential      string `json:"credential"`
	StateTransport  string `json:"state_transport"`
	KeySynced       *bool  `json:"key_synced"`
}

type wireStatusTailscale struct {
	Manage       bool   `json:"manage"`
	Pattern      string `json:"pattern"`
	Applied      bool   `json:"applied"`
	BackendState string `json:"backend_state"`
}

type wireAlert struct {
	Code     string         `json:"code"`
	Severity string         `json:"severity"`
	Title    string         `json:"title"`
	Fix      string         `json:"fix"`
	Since    string         `json:"since"`
	Details  map[string]any `json:"details"`
}

// wireModels is GET /models; GET /models/{id} is one wireModel plus Files and Preset.
type wireModels struct {
	ModelsMax int         `json:"models_max"`
	Default   string      `json:"default"`
	Models    []wireModel `json:"models"`
}

type wireModel struct {
	ID             string   `json:"id"`
	DisplayName    string   `json:"display_name"`
	Summary        string   `json:"summary"`
	Engine         string   `json:"engine"`
	Roles          []string `json:"roles"`
	InProfile      bool     `json:"in_profile"`
	SizeBytes      int64    `json:"size_bytes"`
	Present        bool     `json:"present"`
	Verified       bool     `json:"verified"`
	Loadable       bool     `json:"loadable"`
	LoadableReason *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"loadable_reason"`
	Status       string   `json:"status"`
	Loaded       bool     `json:"loaded"`
	Default      bool     `json:"default"`
	Aliases      []string `json:"aliases"`
	Requirements *struct {
		Backends []string `json:"backends"`
		MinVRAM  int64    `json:"min_vram_mib"`
		MinRAM   int64    `json:"min_ram_mib"`
	} `json:"requirements"`
	LastLoadMS *int64 `json:"last_load_ms"`
	Locations  []struct {
		Root       string `json:"root"`
		Complete   bool   `json:"complete"`
		VerifiedAt string `json:"verified_at"`
	} `json:"locations"`
	Files []struct {
		Kind      string `json:"kind"`
		Name      string `json:"name"`
		SizeBytes int64  `json:"size_bytes"`
	} `json:"files"`
}

// wireLogs is GET /logs.
type wireLogs struct {
	Unit      string        `json:"unit"`
	Lines     []wireLogLine `json:"lines"`
	Truncated bool          `json:"truncated"`
}

type wireLogLine struct {
	At       string `json:"at"`
	Priority int    `json:"priority"`
	Message  string `json:"message"`
}

// wireDoctor is GET /doctor.
type wireDoctor struct {
	RanAt      string `json:"ran_at"`
	DurationMS int64  `json:"duration_ms"`
	Summary    struct {
		OK       int `json:"ok"`
		Info     int `json:"info"`
		Warning  int `json:"warning"`
		Error    int `json:"error"`
		Critical int `json:"critical"`
	} `json:"summary"`
	Results []struct {
		Check    string         `json:"check"`
		Code     string         `json:"code"`
		Severity string         `json:"severity"`
		Title    string         `json:"title"`
		Fix      string         `json:"fix"`
		Details  map[string]any `json:"details"`
	} `json:"results"`
	Passed []string `json:"passed"`
}

// wireProfile is GET /profile.
type wireProfile struct {
	Profile     string   `json:"profile"`
	DisplayName string   `json:"display_name"`
	Overrides   any      `json:"overrides"`
	Suggested   string   `json:"suggested"`
	Reasons     []string `json:"match_reasons"`
	Effective   struct {
		Models       []string `json:"models"`
		Content      []string `json:"content"`
		PlannedBytes int64    `json:"planned_bytes"`
		BudgetBytes  int64    `json:"budget_bytes"`
	} `json:"effective"`
}

// wireArk is GET /ark: the READY.json of every mounted root (ark-sync.md section 4.12.1).
type wireArk struct {
	Roots []wireReady `json:"roots"`
}

type wireReady struct {
	GeneratedAt string `json:"generated_at"`
	Root        struct {
		ID          string `json:"id"`
		Kind        string `json:"kind"`
		Path        string `json:"path"`
		Profile     string `json:"profile"`
		FSFreeBytes int64  `json:"fs_free_bytes"`
		BudgetBytes int64  `json:"budget_bytes"`
		UsedBytes   int64  `json:"used_bytes"`
		ReadOnly    bool   `json:"read_only"`
	} `json:"root"`
	Overall  string   `json:"overall"`
	ReadyPct *float64 `json:"ready_pct"`
	LastSync *struct {
		State      string `json:"state"`
		FinishedAt string `json:"finished_at"`
	} `json:"last_sync"`
	Counts map[string]int `json:"counts"`
	Items  []struct {
		ID     string `json:"id"`
		Status string `json:"status"`
		Bytes  int64  `json:"bytes"`
	} `json:"items"`
	Problems []struct {
		Code     string `json:"code"`
		Severity string `json:"severity"`
		Item     string `json:"item"`
		Title    string `json:"title"`
	} `json:"problems"`
}

// wireSyncRun is GET /ark/sync (the active or last run). Only the fields the status line needs.
type wireSyncRun struct {
	State      string `json:"state"`
	Trigger    string `json:"trigger"`
	FinishedAt string `json:"finished_at"`
	Reason     string `json:"reason"`
	Totals     struct {
		Items  int `json:"items"`
		Done   int `json:"done"`
		Failed int `json:"failed"`
	} `json:"totals"`
}

// wireDrives is GET /ark/drives.
type wireDrives struct {
	Drives []struct {
		RootID    *string `json:"root_id"`
		Device    string  `json:"device"`
		Mount     string  `json:"mount"`
		FSType    string  `json:"fstype"`
		Label     string  `json:"label"`
		SizeBytes int64   `json:"size_bytes"`
		FreeBytes int64   `json:"free_bytes"`
		State     string  `json:"state"`
		ReadOnly  bool    `json:"read_only"`
		Ready     *struct {
			Overall string `json:"overall"`
		} `json:"ready"`
		Problems []struct {
			Code  string `json:"code"`
			Title string `json:"title"`
		} `json:"problems"`
	} `json:"drives"`
}
