package run

import (
	"fmt"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/sync/throttle"
)

// Settings is the sync: block of node.yaml (docs/specs/ark-sync.md section 3.3). The engine reads it
// through Deps.Settings at the start of every run, so a change made with `aictl config` takes effect
// for the next run (a run in progress keeps the values it started with).
type Settings struct {
	// Enabled false makes scheduled runs end "skipped: disabled"; a person's run proceeds.
	Enabled bool `yaml:"enabled"`
	// Concurrency is the number of simultaneous network downloads over all roots, 1 to 4.
	Concurrency int `yaml:"concurrency"`
	// PerRootWriters is the number of items in downloading, verifying or swapping per root.
	PerRootWriters int `yaml:"per_root_writers"`
	// BandwidthLimit is "0" (unlimited) or a rate such as "20MiB/s" (internal/sync/throttle).
	BandwidthLimit string `yaml:"bandwidth_limit"`
	// AllowOnBattery lets a scheduled run go on while the node is on battery.
	AllowOnBattery bool `yaml:"allow_on_battery"`
	// VerifyReread re-reads every file from disk after the download.
	VerifyReread bool `yaml:"verify_reread"`
	// ReverifyDays and ReverifyBudgetMinutes belong to the re-verification (SYNC-13).
	ReverifyDays          int `yaml:"reverify_days"`
	ReverifyBudgetMinutes int `yaml:"reverify_budget_minutes"`
	// TrashDays and QuarantineDays are the retention ages (rules R3 and R5).
	TrashDays      int `yaml:"trash_days"`
	QuarantineDays int `yaml:"quarantine_days"`
	// MaxRunHours stops a scheduled run gracefully after this long.
	MaxRunHours int `yaml:"max_run_hours"`
	// HFTokenFile is the optional Hugging Face token file (read by the resolvers, never logged).
	HFTokenFile string `yaml:"hf_token_file"`
	// ArkDrive configures the drive roots.
	ArkDrive ArkDriveSettings `yaml:"ark_drive"`
}

// ArkDriveSettings is sync.ark_drive.
type ArkDriveSettings struct {
	// Enabled false means drives are detected and served but never written.
	Enabled        bool   `yaml:"enabled"`
	DefaultProfile string `yaml:"default_profile"`
	CopyInventory  bool   `yaml:"copy_inventory"`
}

// DefaultSettings are the defaults of section 3.3.
func DefaultSettings() Settings {
	return Settings{
		Enabled: true, Concurrency: 2, PerRootWriters: 1, BandwidthLimit: "0", VerifyReread: true,
		ReverifyDays: 30, ReverifyBudgetMinutes: 90, TrashDays: 14, QuarantineDays: 7, MaxRunHours: 12,
		HFTokenFile: "/etc/localai/hf-token",
		ArkDrive:    ArkDriveSettings{Enabled: true, DefaultProfile: "ark-drive-full", CopyInventory: true},
	}
}

// ParseSettings reads the sync: block of node.yaml (the block as written, without the "sync:" key),
// strictly: an unknown key is an error. Absent keys keep their defaults. A block that is not there at
// all is DefaultSettings.
func ParseSettings(raw config.RawBlock) (Settings, error) {
	s := DefaultSettings()
	if len(raw) > 0 {
		if err := raw.DecodeStrict(&s); err != nil {
			return Settings{}, fmt.Errorf("sync: %w", err)
		}
	}
	if err := s.Validate(); err != nil {
		return Settings{}, err
	}
	return s, nil
}

// Validate checks the ranges of section 3.3.
func (s Settings) Validate() error {
	switch {
	case s.Concurrency < 1 || s.Concurrency > 4:
		return fmt.Errorf("sync.concurrency must be 1 to 4, got %d", s.Concurrency)
	case s.PerRootWriters < 1 || s.PerRootWriters > 4:
		return fmt.Errorf("sync.per_root_writers must be 1 to 4, got %d", s.PerRootWriters)
	case s.TrashDays < 0 || s.QuarantineDays < 0 || s.ReverifyDays < 0 || s.ReverifyBudgetMinutes < 0:
		return fmt.Errorf("sync: ages and budgets cannot be negative")
	case s.MaxRunHours < 0:
		return fmt.Errorf("sync.max_run_hours cannot be negative")
	}
	if _, err := throttle.ParseLimit(s.BandwidthLimit); err != nil {
		return fmt.Errorf("sync.bandwidth_limit: %w", err)
	}
	return nil
}

// MaxRun is max_run_hours as a duration (0: no limit).
func (s Settings) MaxRun() time.Duration { return time.Duration(s.MaxRunHours) * time.Hour }

// SettingsValidator validates the sync: block of node.yaml for the config store (config.Hooks.Blocks
// registers it under "sync"): the strict decoding and the ranges of ParseSettings, reported as one
// blocking problem.
type SettingsValidator struct{}

// ValidateBlock implements config.BlockValidator.
func (SettingsValidator) ValidateBlock(raw config.RawBlock) config.Problems {
	if _, err := ParseSettings(raw); err != nil {
		return config.Problems{{Message: err.Error(), Severity: config.SeverityError}}
	}
	return nil
}

var _ config.BlockValidator = SettingsValidator{}
