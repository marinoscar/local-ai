package ensure

import (
	"bytes"
	"fmt"

	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"gopkg.in/yaml.v3"
)

// BlockName is the node.yaml block this package owns (config.OwnedBlockNames).
const BlockName = "ensure"

// Settings is the ensure block of node.yaml (agent.md section 3.4):
//
//	ensure:
//	  auto: on        # on (default), ask or off
//
// YAML turns an unquoted on/off into a boolean in some readers, so true means on and false means off.
type Settings struct {
	Auto Auto
}

// DefaultSettings are the settings of a node.yaml without the block.
func DefaultSettings() Settings { return Settings{Auto: AutoOn} }

type rawSettings struct {
	Auto yaml.Node `yaml:"auto"`
}

// ParseSettings reads the block strictly. An empty or absent block is the defaults. A bad value is an
// error (LAI-AGT-002 with the path of the key), so the store keeps the last valid node.yaml.
func ParseSettings(raw config.RawBlock) (Settings, config.Problems) {
	s := DefaultSettings()
	if len(bytes.TrimSpace(raw)) == 0 {
		return s, nil
	}
	var r rawSettings
	if err := raw.DecodeStrict(&r); err != nil {
		return DefaultSettings(), config.Problems{bad("", err.Error())}
	}
	if r.Auto.Kind == 0 || (r.Auto.Kind == yaml.ScalarNode && r.Auto.Tag == "!!null") {
		return s, nil
	}
	if r.Auto.Kind != yaml.ScalarNode {
		return DefaultSettings(), config.Problems{bad("auto", "must be on, ask or off")}
	}
	switch v := r.Auto.Value; v {
	case "on", "true", "yes":
		s.Auto = AutoOn
	case "off", "false", "no":
		s.Auto = AutoOff
	case "ask":
		s.Auto = AutoAsk
	default:
		return DefaultSettings(), config.Problems{bad("auto", fmt.Sprintf("%q is not on, ask or off", v))}
	}
	return s, nil
}

func bad(path, msg string) config.Problem {
	return config.Problem{Path: path, Message: msg, Code: doctor.AGT002, Severity: config.SeverityError}
}

// BlockValidator checks the ensure block when node.yaml loads (config.Hooks.Blocks["ensure"]). A bad value
// is an error: the last valid node.yaml stays in use.
type BlockValidator struct{}

// ValidateBlock implements config.BlockValidator.
func (BlockValidator) ValidateBlock(raw config.RawBlock) config.Problems {
	_, probs := ParseSettings(raw)
	return probs
}

var _ config.BlockValidator = BlockValidator{}
