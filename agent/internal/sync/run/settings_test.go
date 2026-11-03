package run

import (
	"strings"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/config"
)

func TestParseSettings(t *testing.T) {
	t.Run("an absent block is the defaults", func(t *testing.T) {
		s, err := ParseSettings(nil)
		if err != nil || s != DefaultSettings() {
			t.Fatalf("%+v, %v", s, err)
		}
		if !s.Enabled || s.Concurrency != 2 || s.PerRootWriters != 1 || s.BandwidthLimit != "0" || s.AllowOnBattery || !s.VerifyReread ||
			s.TrashDays != 14 || s.QuarantineDays != 7 || s.MaxRunHours != 12 || !s.ArkDrive.Enabled || s.ArkDrive.DefaultProfile != "ark-drive-full" {
			t.Fatalf("defaults = %+v", s)
		}
		if s.MaxRun() != 12*time.Hour {
			t.Fatalf("MaxRun = %v", s.MaxRun())
		}
	})
	t.Run("the block of the spec, with keys overridden", func(t *testing.T) {
		raw := config.RawBlock(`
enabled: false
concurrency: 4
per_root_writers: 2
bandwidth_limit: "20MiB/s"
allow_on_battery: true
verify_reread: false
max_run_hours: 6
ark_drive:
  enabled: false
  copy_inventory: false
`)
		s, err := ParseSettings(raw)
		if err != nil {
			t.Fatal(err)
		}
		if s.Enabled || s.Concurrency != 4 || s.PerRootWriters != 2 || s.BandwidthLimit != "20MiB/s" || !s.AllowOnBattery || s.VerifyReread || s.MaxRunHours != 6 ||
			s.ArkDrive.Enabled || s.ArkDrive.CopyInventory || s.ArkDrive.DefaultProfile != "ark-drive-full" || s.TrashDays != 14 {
			t.Fatalf("settings = %+v", s)
		}
	})
	for name, tc := range map[string]struct{ yaml, msg string }{
		"an unknown key is refused":   {"surprise: 1", "surprise"},
		"concurrency above 4":         {"concurrency: 5", "concurrency"},
		"concurrency below 1":         {"concurrency: 0", "concurrency"},
		"zero writers":                {"per_root_writers: 0", "per_root_writers"},
		"a bad bandwidth limit":       {`bandwidth_limit: "20mb/s"`, "bandwidth_limit"},
		"a negative age":              {"trash_days: -1", "negative"},
		"a negative time limit":       {"max_run_hours: -2", "max_run_hours"},
		"an unknown key in ark_drive": {"ark_drive:\n  nope: true", "nope"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ParseSettings(config.RawBlock(tc.yaml))
			if err == nil || !strings.Contains(err.Error(), tc.msg) {
				t.Fatalf("err = %v, want one mentioning %q", err, tc.msg)
			}
		})
	}
}
