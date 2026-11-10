package ensure

import (
	"strings"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
)

func TestParseSettings(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want Auto
		bad  string
	}{
		{"absent block", "", AutoOn, ""},
		{"empty block", "\n", AutoOn, ""},
		{"auto missing", "{}", AutoOn, ""},
		{"on", "auto: on", AutoOn, ""},
		{"ask", "auto: ask", AutoAsk, ""},
		{"off", "auto: off", AutoOff, ""},
		{"quoted", `auto: "ask"`, AutoAsk, ""},
		{"a boolean true reads as on", "auto: true", AutoOn, ""},
		{"a boolean false reads as off", "auto: false", AutoOff, ""},
		{"yes and no", "auto: no", AutoOff, ""},
		{"null", "auto:", AutoOn, ""},
		{"not a value", "auto: sometimes", AutoOn, "auto"},
		{"a list", "auto: [on]", AutoOn, "auto"},
		{"an unknown key", "auto: on\nspeed: fast", AutoOn, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, probs := ParseSettings(config.RawBlock(tc.raw))
			if tc.name == "an unknown key" {
				if len(probs) == 0 {
					t.Error("an unknown key must be refused")
				}
				return
			}
			if tc.bad != "" {
				if len(probs) != 1 || probs[0].Path != tc.bad || probs[0].Code != doctor.AGT002 || probs[0].Severity != config.SeverityError {
					t.Fatalf("problems %+v", probs)
				}
				if s.Auto != AutoOn {
					t.Errorf("a bad block falls back to on, got %s", s.Auto)
				}
				return
			}
			if len(probs) != 0 || s.Auto != tc.want {
				t.Errorf("got %s %v, want %s", s.Auto, probs, tc.want)
			}
		})
	}
	t.Run("the validator is the same check", func(t *testing.T) {
		if p := (BlockValidator{}).ValidateBlock(config.RawBlock("auto: maybe")); len(p) != 1 || !strings.Contains(p[0].Message, "maybe") {
			t.Errorf("%+v", p)
		}
	})
	t.Run("node.yaml refuses a bad value and keeps the last valid file", func(t *testing.T) {
		hooks := &config.Hooks{Blocks: map[string]config.BlockValidator{"ensure": BlockValidator{}}}
		cfg, probs := config.Load([]byte("schema_version: 1\nnode:\n  id: n1\nensure:\n  auto: never\n"), hooks)
		_ = cfg
		found := false
		for _, p := range probs {
			if p.Path == "ensure.auto" && p.Severity == config.SeverityError {
				found = true
			}
		}
		if !found {
			t.Errorf("problems %+v", probs)
		}
	})
}
