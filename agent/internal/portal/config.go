package portal

import (
	"bytes"
	"fmt"
	"regexp"

	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
)

// Config is the portal block of node.yaml (survival-portal.md section 3.4). Hot-reloadable.
type Config struct {
	// Language is auto, en or es. auto = profile.survival.portal_language, then the UI language.
	Language string `yaml:"language"`
	// EmergencyNumber is shown on the home screen and the card.
	EmergencyNumber string `yaml:"emergency_number"`
	// Kiosk makes ai-desktop open the Portal full screen even outside survival.
	Kiosk bool `yaml:"kiosk"`
}

// DefaultConfig is the block when node.yaml has none.
func DefaultConfig() Config {
	return Config{Language: "auto", EmergencyNumber: DefaultEmergencyNumber}
}

// emergencyRe accepts what can be dialled: digits, and an optional leading + or separators. It is
// shown on a screen and a card, so markup and long text are refused.
var emergencyRe = regexp.MustCompile(`^[0-9+*#][0-9 +*#-]{0,19}$`)

// ParseConfig reads the block strictly. An empty block is the defaults. Errors carry LAI-AGT-002 and
// the block path.
func ParseConfig(raw config.RawBlock) (Config, config.Problems) {
	c := DefaultConfig()
	var probs config.Problems
	bad := func(path, msg string) {
		probs = append(probs, config.Problem{Path: path, Message: msg, Code: doctor.AGT002, Severity: config.SeverityError})
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return c, nil
	}
	if err := raw.DecodeStrict(&c); err != nil {
		bad("", err.Error())
		return DefaultConfig(), probs
	}
	switch c.Language {
	case "auto", doctor.LangEN, doctor.LangES:
	default:
		bad("language", fmt.Sprintf("%q is not auto, en or es", c.Language))
	}
	if !emergencyRe.MatchString(c.EmergencyNumber) {
		bad("emergency_number", fmt.Sprintf("%q is not a phone number of at most 20 characters", c.EmergencyNumber))
	}
	return c, probs
}

// BlockValidator checks the portal block when node.yaml loads (config.Hooks.Blocks["portal"]). A bad
// value is an error: the last valid node.yaml stays in use.
type BlockValidator struct{}

// ValidateBlock implements config.BlockValidator.
func (BlockValidator) ValidateBlock(raw config.RawBlock) config.Problems {
	_, probs := ParseConfig(raw)
	return probs
}

var _ config.BlockValidator = BlockValidator{}
